package ci

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/cli"
	"github.com/stephrobert/feint/internal/core/machine"
)

// The runtime proof installs the Incus release the code names, and only that
// one (#979).
//
// Before, runtime-proof.yml ran `apt-get install incus incus-client` with no
// version, so every night installed whatever Zabbly stable had published last;
// internal/cli/doctor.go named 7.2; the station ran 7.6. Three statements of
// one fact, and nothing compared them. Now machine.IncusTested is the fact,
// incusRecommended and the install page read it, and this test holds the
// workflow to it.

// incusPinWant is the only spelling of an Incus package an apt line may carry:
// the pinned upstream release, any build of it. Zabbly keeps the last two
// builds and nothing older, so a build timestamp would fail at the next
// rebuild; `-*` after the release still refuses every other release (7.6.1
// does not match `1:7.6-*`).
const incusPinWant = "1:${INCUS_VERSION}-*"

var (
	incusVersionDecl = regexp.MustCompile(`(?m)^\s*INCUS_VERSION:\s*['"]?([^'"\s]*)['"]?\s*$`)
	aptInstall       = regexp.MustCompile(`\bapt(?:-get)?\s+install\b`)
	incusPackage     = regexp.MustCompile(`^incus(?:-[a-z]+)?$`)
)

// incusPinProblems lists every way a workflow departs from the pin. installs
// is how many Incus packages its apt lines name, so a caller can tell "pinned
// correctly" from "installs no Incus at all".
func incusPinProblems(workflow, tested string) (problems []string, installs int) {
	decls := incusVersionDecl.FindAllStringSubmatch(workflow, -1)

	joined := strings.ReplaceAll(workflow, "\\\n", " ")
	for _, line := range strings.Split(joined, "\n") {
		if !aptInstall.MatchString(line) {
			continue
		}
		for _, word := range strings.Fields(line) {
			word = strings.Trim(word, `"'`)
			name, version, pinned := strings.Cut(word, "=")
			if !incusPackage.MatchString(name) {
				continue
			}
			installs++
			switch {
			case !pinned:
				problems = append(problems, "installs "+name+" with no version: tonight's proof runs whatever Zabbly published last")
			case version != incusPinWant:
				problems = append(problems, "installs "+name+"="+version+", not "+name+"="+incusPinWant)
			}
		}
	}

	if installs == 0 {
		return problems, 0
	}
	switch len(decls) {
	case 0:
		problems = append(problems, "installs Incus and declares no INCUS_VERSION: the glob expands to nothing apt can find")
	case 1:
		if got := decls[0][1]; got != tested {
			problems = append(problems, "pins INCUS_VERSION "+got+" and machine.IncusTested is "+tested+
				": the page recommends one release and the proof runs another")
		}
	default:
		problems = append(problems, "declares INCUS_VERSION more than once: one workflow, one release")
	}
	return problems, installs
}

// TestTheRuntimeProofInstallsTheTestedIncus is the control on the real files:
// runtime-proof.yml installs Incus pinned to machine.IncusTested in both of its
// jobs, and no workflow installs Incus any other way.
func TestTheRuntimeProofInstallsTheTestedIncus(t *testing.T) {
	dir := filepath.Join("..", "..", ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	proofInstalls := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // a path this repository owns
		if err != nil {
			t.Fatal(err)
		}
		problems, installs := incusPinProblems(string(body), machine.IncusTested)
		for _, p := range problems {
			t.Errorf("%s %s", e.Name(), p)
		}
		if e.Name() == "runtime-proof.yml" {
			proofInstalls = installs
		}
	}
	// The subject must be there: a workflow that stopped installing Incus has
	// no problem to report, and this test would pass on nothing. Two jobs
	// (runtime, stacks), two packages each (incus, incus-client).
	if proofInstalls != 4 {
		t.Errorf("runtime-proof.yml names %d Incus packages on its apt lines, want 4 (incus and incus-client, in runtime and stacks)", proofInstalls)
	}
}

// One case per question incusPinProblems asks. Each refused workflow is sound
// on every other question, so the reason is asserted, not just a refusal.
func TestIncusPinProblemsAsksEachQuestionApart(t *testing.T) {
	const env = "env:\n  INCUS_VERSION: '7.6'\n"
	const pinned = `sudo apt-get install -y "incus=1:${INCUS_VERSION}-*" "incus-client=1:${INCUS_VERSION}-*" netcat-openbsd` + "\n"
	cases := []struct {
		name     string
		workflow string
		want     string // "" means accepted
		installs int
	}{
		{"accepted", env + pinned, "", 2},
		{"accepted across a continuation", env + "sudo apt-get install -y \\\n  \"incus=1:${INCUS_VERSION}-*\" \\\n  \"incus-client=1:${INCUS_VERSION}-*\"\n", "", 2},
		{"no Incus at all is not a problem of this test", "run: sudo apt-get install -y jq\n", "", 0},
		{"bare package", env + `sudo apt-get install -y incus "incus-client=1:${INCUS_VERSION}-*"` + "\n", "installs incus with no version", 2},
		{"bare companion package", env + pinned + "sudo apt install -y incus-extra\n", "installs incus-extra with no version", 3},
		{"one build pinned", env + `sudo apt-get install -y "incus=1:7.6-ubuntu24.04-202611072126" "incus-client=1:${INCUS_VERSION}-*"` + "\n", "not incus=" + incusPinWant, 2},
		{"another release", "env:\n  INCUS_VERSION: '7.2'\n" + pinned, "machine.IncusTested is 7.6", 2},
		{"no declaration", pinned, "declares no INCUS_VERSION", 2},
		{"two declarations", env + "    env:\n      INCUS_VERSION: '7.6'\n" + pinned, "more than once", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			problems, installs := incusPinProblems(c.workflow, "7.6")
			if installs != c.installs {
				t.Errorf("counted %d Incus packages, want %d", installs, c.installs)
			}
			if c.want == "" {
				if len(problems) != 0 {
					t.Errorf("a sound workflow was refused: %v", problems)
				}
				return
			}
			if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, c.want) }) {
				t.Errorf("want a problem saying %q, got %v", c.want, problems)
			}
			if len(problems) != 1 {
				t.Errorf("want exactly one problem, got %v: the case fails more than its own question", problems)
			}
		})
	}
}

// The install role pins the same release (#979). The machines that prove
// docs/install.md (tools/install/ansible/site.yml) installed Zabbly's newest
// Incus with no version, so the page's verdicts could rest on a release the
// proof never ran. The role reads no Go constant, so this test holds its
// variable to machine.IncusTested the way the workflow's INCUS_VERSION is held.

// rolePinWant is the only spelling of a package the role may install from
// Zabbly: the role's own variable, any build of it, as in runtime-proof.yml.
const rolePinWant = "1:{{ feint_host_incus_version }}-*"

var (
	roleVersionDecl = regexp.MustCompile(`(?m)^feint_host_incus_version:\s*['"]?([^'"\s]*)['"]?\s*$`)
	rolePackagesKey = regexp.MustCompile(`(?m)^feint_host_incus_packages:(.*)$`)
)

// rolePinProblems lists every way the role's Debian variables depart from the
// pin. installs counts the Incus packages the role names, so a caller can tell
// "pinned" from "installs no Incus at all".
func rolePinProblems(vars, tested string) (problems []string, installs int) {
	var items []string
	if loc := rolePackagesKey.FindStringSubmatchIndex(vars); loc != nil {
		inline := strings.TrimSpace(vars[loc[2]:loc[3]])
		if inline != "" {
			items = strings.Split(strings.Trim(inline, "[]"), ",")
		}
		// A block list: the indented "- item" lines right after the key.
		for _, line := range strings.Split(vars[loc[1]:], "\n")[1:] {
			item, ok := strings.CutPrefix(strings.TrimSpace(line), "- ")
			if !ok || !strings.HasPrefix(line, " ") {
				break
			}
			items = append(items, item)
		}
	}
	for _, item := range items {
		name, version, pinned := strings.Cut(strings.Trim(strings.TrimSpace(item), `"'`), "=")
		if !incusPackage.MatchString(name) {
			continue
		}
		installs++
		switch {
		case !pinned:
			problems = append(problems, "installs "+name+" with no version: the machines that prove the install page run whatever Zabbly published last")
		case version != rolePinWant:
			problems = append(problems, "installs "+name+"="+version+", not "+name+"="+rolePinWant)
		}
	}
	if installs == 0 {
		return problems, 0
	}
	decls := roleVersionDecl.FindAllStringSubmatch(vars, -1)
	switch len(decls) {
	case 0:
		problems = append(problems, "installs Incus and declares no feint_host_incus_version")
	case 1:
		if release := decls[0][1]; release != tested {
			problems = append(problems, "pins feint_host_incus_version "+release+" and machine.IncusTested is "+tested+
				": the install machines run one release and the proof another")
		}
	default:
		problems = append(problems, "declares feint_host_incus_version more than once")
	}
	return problems, installs
}

// TestTheInstallRoleInstallsTheTestedIncus is the control on the real file.
func TestTheInstallRoleInstallsTheTestedIncus(t *testing.T) {
	path := filepath.Join("..", "install", "ansible", "roles", "feint_host", "vars", "debian.yml")
	body, err := os.ReadFile(path) //nolint:gosec // a path this repository owns
	if err != nil {
		t.Fatal(err)
	}
	problems, installs := rolePinProblems(string(body), machine.IncusTested)
	for _, p := range problems {
		t.Errorf("%s %s", path, p)
	}
	// The subject must be there: a role that stopped naming Incus has no
	// problem to report. Two packages, incus and incus-client.
	if installs != 2 {
		t.Errorf("%s names %d Incus packages, want 2 (incus and incus-client)", path, installs)
	}
}

// One case per question rolePinProblems asks; each refused file is sound on
// every other question.
func TestRolePinProblemsAsksEachQuestionApart(t *testing.T) {
	const decl = "feint_host_incus_version: \"7.6\"\n"
	const block = "feint_host_incus_packages:\n  - \"incus=1:{{ feint_host_incus_version }}-*\"\n  - \"incus-client=1:{{ feint_host_incus_version }}-*\"\nfeint_host_ovn_packages: [ovn-central]\n"
	cases := []struct {
		name, vars, want string
		installs         int
	}{
		{"accepted, block list", decl + block, "", 2},
		{"accepted, flow list", decl + "feint_host_incus_packages: [\"incus=1:{{ feint_host_incus_version }}-*\", \"incus-client=1:{{ feint_host_incus_version }}-*\"]\n", "", 2},
		{"no Incus at all", "feint_host_ovn_packages: [ovn-central]\n", "", 0},
		{"the bare list before #979", decl + "feint_host_incus_packages: [incus, \"incus-client=1:{{ feint_host_incus_version }}-*\"]\n", "installs incus with no version", 2},
		{"a release written in place of the variable", decl + "feint_host_incus_packages:\n  - \"incus=1:7.6-*\"\n  - \"incus-client=1:{{ feint_host_incus_version }}-*\"\n", "not incus=" + rolePinWant, 2},
		{"another release", "feint_host_incus_version: \"7.2\"\n" + block, "machine.IncusTested is 7.6", 2},
		{"no declaration", block, "declares no feint_host_incus_version", 2},
		{"two declarations", decl + decl + block, "more than once", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			problems, installs := rolePinProblems(c.vars, "7.6")
			if installs != c.installs {
				t.Errorf("counted %d Incus packages, want %d", installs, c.installs)
			}
			if c.want == "" {
				if len(problems) != 0 {
					t.Errorf("a sound file was refused: %v", problems)
				}
				return
			}
			if len(problems) != 1 || !strings.Contains(problems[0], c.want) {
				t.Errorf("want exactly one problem saying %q, got %v", c.want, problems)
			}
		})
	}
}

// The install role refuses OVN on the releases doctor gives no OVN command
// to, and on those only (#979). The table lives in internal/cli
// (oldOVNReleases); the role's guard restates its codenames, so this test
// holds the two together.
var roleOldOVN = regexp.MustCompile(`ansible_distribution_release in \[([^\]]*)\]`)

func roleOldOVNCodenames(tasks string) ([]string, bool) {
	m := roleOldOVN.FindStringSubmatch(tasks)
	if m == nil {
		return nil, false
	}
	var names []string
	for _, item := range strings.Split(m[1], ",") {
		if name := strings.Trim(strings.TrimSpace(item), `"'`); name != "" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names, true
}

func TestTheRoleRefusesOVNOnTheReleasesDoctorNames(t *testing.T) {
	path := filepath.Join("..", "install", "ansible", "roles", "feint_host", "tasks", "debian.yml")
	body, err := os.ReadFile(path) //nolint:gosec // a path this repository owns
	if err != nil {
		t.Fatal(err)
	}
	got, found := roleOldOVNCodenames(string(body))
	if !found {
		t.Fatalf("%s carries no `ansible_distribution_release in [...]` guard: the role installs OVN everywhere", path)
	}
	want := slices.Clone(cli.OldOVNCodenames())
	slices.Sort(want)
	if len(want) == 0 {
		t.Fatal("doctor's table of releases is empty: nothing to compare")
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s refuses OVN on %v, doctor on %v: one table, two lists that disagree", path, got, want)
	}
}
