package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
)

// A station with only a distribution on it: no Incus, no OVN, nothing.
func distroOnly(osRelease string) station {
	return station{distro: parseOSRelease(osRelease)}
}

// TestABareStationHearsEveryStepInOnePass is cited by setupChecks. Before it,
// a station without Incus heard about Incus, OVN and the wiring, and nothing
// about the group, the initialisation or the images: "3 steps missing" out of
// six, and two more runs of doctor to learn the rest.
func TestABareStationHearsEveryStepInOnePass(t *testing.T) {
	for _, c := range []struct{ name, osRelease string }{
		{"Debian", "PRETTY_NAME=\"Debian GNU/Linux 13\"\nID=debian\n"},
		{"Fedora", "PRETTY_NAME=\"Fedora Linux 43\"\nID=fedora\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := distroOnly(c.osRelease)
			checks := setupChecks(s, "incus-ovn")
			want := []string{stepIncus, stepGroup, stepOVN, stepWiring, stepInit, stepImages}
			if got := missingSteps(checks); !reflect.DeepEqual(got, want) {
				t.Fatalf("missing steps %v, want %v in this order", got, want)
			}
			var out bytes.Buffer
			renderDoctor(&out, checks, doctorView{mode: "incus-ovn", distro: s.distro})
			text := out.String()
			at := 0
			for _, piece := range []string{
				"1. Incus is not installed.",
				"2. You are not in the incus-admin group yet. (do step 1 first)",
				"3. OVN and Open vSwitch are not installed.",
				"4. Open vSwitch is not connected to OVN. (do step 3 first)",
				"5. Doctor cannot check the Incus setup yet. (do step 2 first)",
				"incus admin init --minimal",
				"6. Doctor cannot check the machine images yet. (do step 5 first)",
				"feint images",
				"6 steps missing for --vm incus-ovn.",
			} {
				i := strings.Index(text[at:], piece)
				if i < 0 {
					t.Fatalf("%q missing or out of order in:\n%s", piece, text)
				}
				at += i + len(piece)
			}
		})
	}
}

// TestTheIncusRemedySaysWhyForEachFamily is cited by incusSource: one reason
// for every family told a Fedora reader the commands used the Zabbly packages.
func TestTheIncusRemedySaysWhyForEachFamily(t *testing.T) {
	for osRelease, want := range map[string]string{
		"ID=debian\n":                       "so this uses the Zabbly packages",
		"ID=fedora\n":                       "Fedora's own packages are recent enough",
		"ID=rocky\nID_LIKE=\"rhel fedora\"": "unofficial COPR",
	} {
		c := byID(setupChecks(distroOnly(osRelease), "incus-ovn"), stepIncus)
		if !strings.Contains(c.detail, want) {
			t.Errorf("%q: the reason is %q, want it to say %q", osRelease, c.detail, want)
		}
		if want != "so this uses the Zabbly packages" && strings.Contains(c.detail, "uses the Zabbly packages") {
			t.Errorf("%q is told about Zabbly: %q", osRelease, c.detail)
		}
	}
}

// TestNoPrintedCommandDependsOnItsIndentation is cited by the Debian Incus
// block. Doctor prints every command indented; a heredoc whose EOF is indented
// never closes, and deb822 reads an indented line as a continuation. So no
// block holds a heredoc, and the sources block is pasted exactly as doctor
// prints it, indentation included, and the file it writes is read back.
func TestNoPrintedCommandDependsOnItsIndentation(t *testing.T) {
	for name, block := range setupBlocks {
		for _, line := range block {
			if strings.Contains(line, "<<") {
				t.Errorf("block %s holds a heredoc, which breaks once indented: %q", name, line)
			}
		}
	}
	if _, err := exec.LookPath("dpkg"); err != nil {
		t.Skip("no dpkg here: the Debian sources block cannot be pasted")
	}
	if _, err := os.Stat("/etc/os-release"); err != nil {
		t.Skip("no /etc/os-release here")
	}
	var lines []string
	for _, line := range commands("debian-incus") {
		if strings.Contains(line, "printf") || len(lines) > 0 && len(lines) < 4 {
			lines = append(lines, line)
		}
	}
	if len(lines) != 4 {
		t.Fatalf("the sources block is not the four lines expected: %q", lines)
	}
	target := filepath.Join(t.TempDir(), "zabbly-incus.sources")
	script := strings.ReplaceAll(strings.Join(lines, "\n"), "sudo tee /etc/apt/sources.list.d/zabbly-incus.sources", "tee "+target)
	indented := "          " + strings.ReplaceAll(script, "\n", "\n          ")
	if out, err := exec.Command("sh", "-c", indented).CombinedOutput(); err != nil { //nolint:gosec // the block under test
		t.Fatalf("the block, pasted as printed, fails: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(target) //nolint:gosec // the test's own file
	if err != nil {
		t.Fatalf("read what it wrote: %v", err)
	}
	got := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	keys := []string{"Enabled: yes", "Types: deb", "URIs: https://pkgs.zabbly.com/incus/stable", "Suites: ", "Components: main", "Architectures: ", "Signed-By: /etc/apt/keyrings/zabbly.asc"}
	if len(got) != len(keys) {
		t.Fatalf("the file has %d lines, want %d:\n%s", len(got), len(keys), raw)
	}
	for i, key := range keys {
		if !strings.HasPrefix(got[i], key) || len(got[i]) == len(key) && strings.HasSuffix(key, " ") {
			t.Errorf("line %d is %q, want it to start with %q and carry a value", i+1, got[i], key)
		}
	}
}

// TestTheProbeDoesNotTakeFeintsUplinkForInit is cited by probeStation: the
// bridge feint makes for itself is not the one `incus admin init` makes.
func TestTheProbeDoesNotTakeFeintsUplinkForInit(t *testing.T) {
	networks := `[{"name":"feint-uplink","type":"bridge","managed":true,"config":{"user.` + machine.LabelKey + `":"uplink"}}]`
	probe := hostProbe{
		osRelease: func() (string, error) { return "ID=debian\n", nil },
		lookPath: func(name string) (string, error) {
			if name == "incus" {
				return "/fake/incus", nil
			}
			return "", errors.New("absent")
		},
		exists: func(string) bool { return false },
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			switch strings.Join(args, " ") {
			case "version":
				return []byte("Client version: 7.6\nServer version: 7.6\n"), nil
			case "query /1.0/storage-pools?recursion=1":
				return []byte(`[{"name":"default"}]`), nil
			case "query /1.0/networks?recursion=1":
				return []byte(networks), nil
			}
			return nil, errors.New("unexpected")
		},
		images: func(context.Context) ([]string, error) { return nil, nil },
	}
	if got := byID(setupChecks(probeStation(context.Background(), probe), "incus"), stepInit); got.state == verdictOK {
		t.Fatalf("feint's own uplink was read as the bridge init makes: %+v", got)
	}
	// The accepting half: incusbr0, without the label, is the bridge.
	networks = `[{"name":"incusbr0","type":"bridge","managed":true,"config":{}}]`
	if got := byID(setupChecks(probeStation(context.Background(), probe), "incus"), stepInit); got.state != verdictOK {
		t.Fatalf("incusbr0 was not read as the bridge: %+v", got)
	}
}

// TestThePlaybookIsOfferedOnlyFromAFeintCheckout is cited by playbookFound.
func TestThePlaybookIsOfferedOnlyFromAFeintCheckout(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(localPlaybook)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, localPlaybook), []byte("---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, _ := playbookIn(dir); ok {
		t.Fatal("a playbook outside any Go module was offered")
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, _ := playbookIn(dir); ok {
		t.Fatal("a playbook in another module was offered")
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+feintModule+"\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, where := playbookIn(dir); !ok || where != dir {
		t.Fatalf("the feint checkout was not recognised (%v, %q)", ok, where)
	}
}

// The client install hint follows the last client it installs, and comes
// before jq's own command: printed after it, it read as jq's remedy.
func TestTheClientHintIsNotUnderJq(t *testing.T) {
	p := fakeTools(map[string]string{})
	p.family = "debian"
	var out bytes.Buffer
	renderDoctor(&out, clientChecks(p), doctorView{mode: "off"})
	text := out.String()
	hint := strings.Index(text, "feint doctor --fix --clients --vm off")
	jq := strings.Index(text, "sudo apt-get install -y jq")
	tofu := strings.Index(text, "tofu is missing")
	if hint < 0 || jq < 0 || tofu < 0 || hint < tofu || hint > jq {
		t.Fatalf("the hint is not between the last client and jq (tofu %d, hint %d, jq %d):\n%s", tofu, hint, jq, text)
	}
}

// The playbook adds the reader to incus-admin, and the group applies to a new
// login: pasted with the playbook, a plain `feint images` ran without it and
// Incus refused. The images run under sg, and the note comes before the two
// lines, so nothing sits between commands meant to be pasted together.
func TestTheSummarySaysTheGroupNeedsANewLogin(t *testing.T) {
	var out bytes.Buffer
	// No tool present either, so the line about Ansible is printed and read.
	checks := append(setupChecks(distroOnly("ID=debian\n"), "incus-ovn"), toolChecks(fakeTools(map[string]string{}))...)
	renderDoctor(&out, checks, doctorView{mode: "incus-ovn", playbook: true, checkout: "/src/feint"})
	text := out.String()
	flat := strings.Join(strings.Fields(text), " ")
	if !strings.Contains(flat, "from the feint clone in /src/feint.") || !strings.Contains(flat, "only a new login picks the group up") {
		t.Errorf("the summary does not name the clone and the new login:\n%s", text)
	}
	pair := "    " + playbookCmd + "\n    sg incus-admin -c 'feint images'\n"
	if !strings.Contains(text, pair) {
		t.Fatalf("the playbook and the images build are not one pasteable pair under sg:\n%s", text)
	}
	if note := strings.Index(text, "only a new login"); note < 0 || note > strings.Index(text, pair) {
		t.Errorf("the note about the group comes after the commands it governs:\n%s", text)
	}
	if strings.Contains(text, "\n    feint images\n") {
		t.Errorf("a bare feint images is still offered:\n%s", text)
	}
	if !strings.Contains(flat, "It needs Ansible: install uv, then Ansible (see Repository tools).") {
		t.Errorf("the Ansible line is absent, or still sends the reader to log out:\n%s", text)
	}
}

// TestAutoCountsTheOVNStepsAsOptional is cited by setupChecks and summary:
// under auto, OVN is optional, said once, and not in "N steps missing". Under
// incus-ovn the same station counts them: that is the accepting half.
func TestAutoCountsTheOVNStepsAsOptional(t *testing.T) {
	s := readyStation()
	s.ovsInstalled, s.ovnInstalled = false, false
	render := func(mode string) (string, []check) {
		checks := setupChecks(s, mode)
		var out bytes.Buffer
		renderDoctor(&out, checks, doctorView{mode: mode})
		return out.String(), checks
	}
	auto, checks := render("auto")
	flat := strings.Join(strings.Fields(auto), " ")
	for _, id := range []string{stepOVN, stepWiring} {
		c := byID(checks, id)
		if !c.optional || !strings.HasPrefix(c.detail, "Optional: without it, auto picks a mode where two VPCs can reach each other.") ||
			strings.Contains(c.detail, "only mode that keeps two VPCs apart") || strings.Contains(c.detail, "creating a subnet fails") {
			t.Errorf("auto: %s is not optional, or repeats itself: %+v", id, c)
		}
	}
	if strings.Contains(flat, "steps missing for --vm auto") || !strings.Contains(flat, "Everything --vm auto needs is present.") ||
		!strings.Contains(flat, "Also missing, and optional: 2 steps.") || !strings.Contains(flat, "2 optional") {
		t.Errorf("auto counts the OVN steps as missing:\n%s", auto)
	}
	ovn, checks := render("incus-ovn")
	if c := byID(checks, stepOVN); c.optional || !strings.Contains(ovn, "2 steps missing for --vm incus-ovn.") || strings.Contains(ovn, "optional") {
		t.Errorf("incus-ovn: OVN is not a required step:\n%s", ovn)
	}
}

// TestTheUVPinIsJudgedOnlyInAClone: the pin is the repository's, and outside
// a clone `mise install uv` cannot meet it. Off the pin in a clone, the title
// names both versions without brackets inside brackets.
func TestTheUVPinIsJudgedOnlyInAClone(t *testing.T) {
	present := map[string]string{"uv": "uv 0.13.8 (0a1b2c3 2026-11-01)", "mise": ""}
	outside := fakeTools(present)
	if c := byID(toolChecks(outside), "tool-uv"); c.state != verdictOK || c.title != "uv 0.13.8" || len(c.commands) != 0 {
		t.Errorf("outside a clone, uv off the pin is judged: %+v", c)
	}
	inside := fakeTools(present)
	inside.inCheckout = true
	if c := byID(toolChecks(inside), "tool-uv"); c.state != verdictWarn || c.title != "uv 0.13.8 is installed; the repository pins "+uvPin+"." || len(c.commands) == 0 {
		t.Errorf("in a clone, uv off the pin is not named with both versions: %+v", c)
	}
	if !inFeintCheckout("../..") || inFeintCheckout(t.TempDir()) {
		t.Errorf("the clone test does not tell this repository from an empty directory")
	}
}

// TestTheFixPlanNamesThePlaybookByItsAbsolutePath is cited by runFix: the
// line runs from wherever it is pasted, the path quoted as one word.
func TestTheFixPlanNamesThePlaybookByItsAbsolutePath(t *testing.T) {
	plan := fixPlan{root: []string{"sudo true"}}
	var out bytes.Buffer
	runFix(context.Background(), &out, strings.NewReader(""), false, false, doctorView{mode: "incus-ovn", playbook: true, checkout: "/src/it's feint"}, plan, fixDeps{})
	want := "    cd '/src/it'\\''s feint' && " + playbookCmd + "\n"
	if !strings.Contains(out.String(), want) {
		t.Errorf("the plan does not name the playbook from the quoted absolute checkout (want %q):\n%s", want, out.String())
	}
}

// TestTheFixPlanRunsNothingBeforeTheQuestion is cited by buildFixPlan: the
// version of the binary --fix replaces is the report's reading, and the
// target is not run to get it.
func TestTheFixPlanRunsNothingBeforeTheQuestion(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	target := filepath.Join(dir, "scw")
	if err := os.WriteFile(target, []byte("#!/bin/sh\ntouch "+shellQuote(marker)+"\necho 'Version 9.9.9'\n"), 0o700); err != nil { //nolint:gosec // an executable fixture
		t.Fatal(err)
	}
	deps := (&fixWorld{s: readyStation()}).deps()
	deps.clientDir = dir
	for _, c := range []struct {
		name  string
		check check
		want  string
	}{
		{"PATH led the report to the target", check{id: "client-scw", state: verdictWarn, fixKey: "clients", foundAt: target, answered: "Version 2.50.0"},
			"replaces " + target + " (it answered: Version 2.50.0)"},
		{"PATH led the report elsewhere", check{id: "client-scw", state: verdictWarn, fixKey: "clients", foundAt: "/usr/bin/scw", answered: "Version 2.50.0"},
			"replaces " + target},
	} {
		plan := buildFixPlan([]check{c.check}, deps, true)
		if len(plan.steps) != 1 || len(plan.steps[0].shows) != 2 || plan.steps[0].shows[1] != c.want {
			t.Errorf("%s: want %q: %+v", c.name, c.want, plan.steps)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("building the plan ran the binary it would replace")
	}
}

// Without a home directory there is no ~/.local/bin: a relative one would put
// the binaries wherever doctor runs. The plan installs nothing and says why.
func TestFixInstallsNoClientWithoutAHome(t *testing.T) {
	deps := (&fixWorld{s: readyStation()}).deps()
	missing := check{id: "client-scw", state: verdictWarn, fixKey: "clients"}
	deps.clientDir = ""
	plan := buildFixPlan([]check{missing}, deps, true)
	if len(plan.steps) != 0 || len(plan.notes) != 1 || !strings.Contains(plan.notes[0], "home directory is unknown") {
		t.Fatalf("a client is planned without a home, or silently dropped: %+v", plan)
	}
	// The accepting half: with a home, the client is a step.
	deps.clientDir = "/home/you/.local/bin"
	if plan := buildFixPlan([]check{missing}, deps, true); len(plan.steps) != 1 || len(plan.notes) != 0 {
		t.Fatalf("a client is not planned with a home: %+v", plan)
	}
}

// The download client fetches over https only: a pinned digest protects the
// binary, not the sums file's integrity against a plain-text hop.
func TestTheDownloadRefusesPlainHTTP(t *testing.T) {
	_, err := httpDownload(context.Background(), "http://example.invalid/scw")
	if err == nil || !strings.Contains(err.Error(), "is not https") {
		t.Fatalf("a plain http URL was not refused for that reason: %v", err)
	}
}
