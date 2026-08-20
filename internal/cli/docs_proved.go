package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// What a release was proved against, in the versions that proved it.
//
// A downstream consumer runs this emulator as a required CI gate with no
// credentials, resolving its providers fresh on every pull request. On
// 2026-08-18 their Scaleway lane went red: provider 2.81.0, published the day
// before, moved private NICs onto `instance/v2alpha1`, and their `~> 2.68`
// constraint picked it up the next morning. They pinned the emulated lane to
// `~> 2.80.0` and wrote down what it cost them — "the lane stopped emulating
// the provider our clusters actually run" — and asked for one thing (#325):
//
//	A per-release statement of known-good provider versions — even just
//	"0.9.0 tested against scaleway 2.81.0" — would let consumers pin
//	deliberately instead of discovering the mismatch in a red pipeline.
//
// The numbers already existed. The conformance workflow pins every client it
// installs, and every fixture and stack pins the Terraform provider that
// answers through it. They were simply never published together, so the only
// way to learn them was to read four files or, as this consumer did, probe two
// binaries side by side.
//
// So this page is generated from those files, never typed. Three things it must
// never do, each of them a way of lying that this repository refuses elsewhere:
//
//  1. print a version nothing installs — every pin is checked against a step
//     that interpolates it (TestEveryPinnedVersionIsInstalledByAStepThatUsesIt);
//  2. present a constraint as a proven version — `~> 1.7` is resolved fresh by
//     `terraform init -upgrade` on every run, so the version that answered is
//     not knowable from this repository, and the page says exactly that;
//  3. invent a pin where there is none — two stacks carry no constraint at all,
//     and the honest cell is "not pinned".
//
// TestTheProvedPageSeparatesAnExactPinFromAConstraintAndFromNothing fails
// without the third, which is the one an artefact is most tempted to smooth
// over.

const (
	provedStartMarker = "<!-- proved:start -->"
	provedEndMarker   = "<!-- proved:end -->"

	// clientsDoc is the stable path a consumer pinning a version meets. The
	// release body carries the same generated block, spliced by the release
	// workflow from this file at the tag it is cutting.
	clientsDoc = "docs/clients.md"

	// stacksRoot holds the example stacks — written the way a platform team
	// writes Terraform, and applied by the conformance suite — and stacksScript
	// is what decides which of them CI actually applies.
	stacksRoot   = "examples/stacks"
	stacksScript = "tools/conformance/stacks.sh"
)

// pinnedIn names the file every client version comes from. One string, so the
// page cannot credit a pin to a file that does not hold it.
const pinnedIn = conformanceWorkflow

var (
	// runStack matches the stacks the conformance suite applies. A stack in the
	// directory that no line names is a stack CI never runs, and the page must
	// not present its provider constraint as a proof.
	runStack = regexp.MustCompile(`(?m)^\s*run_stack\s+([a-z0-9-]+)`)
	// exactVersion is a Terraform constraint that names one version and only
	// one: `2.81.0`, or `= 2.81.0`. Anything else — `~>`, `>=`, a range — is a
	// constraint whose resolution happens on the runner.
	exactVersion = regexp.MustCompile(`^=?\s*\d+\.\d+\.\d+$`)
)

// providerPin is one Terraform provider constraint, and where it was found.
type providerPin struct {
	// Dir is the fixture or stack directory, relative to the repository root.
	Dir string
	// Source is the registry address, `scaleway/scaleway`.
	Source string
	// Constraint is the version constraint as written, empty when the block
	// carries none.
	Constraint string
	// Driven says whether a suite the conformance workflow runs applies this
	// directory. Derived, never assumed: examples/stacks/exoscale exists, pins
	// nothing, and no `run_stack` line names it.
	Driven bool
}

// pinKind classifies a constraint into the three things it can be, and says
// what each one is worth.
//
// The distinction is the whole point of the page. An exact pin is a version
// that answered; a constraint is a version somebody's runner chose that morning;
// nothing is nothing. Collapsing the three into one column is the shape of the
// lie this repository exists to avoid.
func pinKind(constraint string) (string, string) {
	switch {
	case strings.TrimSpace(constraint) == "":
		return "not pinned", "whatever the registry served that day"
	case exactVersion.MatchString(strings.TrimSpace(constraint)):
		return "exact", "the version that answered"
	default:
		return "constraint", "resolved fresh on each run, so the version that answered is not knowable here"
	}
}

// block returns the text between the brace opening at or after start and its
// match, plus the index just past the closing brace.
func block(body string, start int) (string, int) {
	open := strings.IndexByte(body[start:], '{')
	if open < 0 {
		return "", len(body)
	}
	open += start
	depth := 0
	for i := open; i < len(body); i++ {
		switch body[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return body[open+1 : i], i + 1
			}
		}
	}
	return "", len(body)
}

var (
	attrSource  = regexp.MustCompile(`(?m)^\s*source\s*=\s*"([^"]+)"`)
	attrVersion = regexp.MustCompile(`(?m)^\s*version\s*=\s*"([^"]+)"`)
	entryHead   = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9_-]+)\s*=\s*\{`)
)

// requiredProviders reads every entry of every required_providers block in one
// file.
//
// Every entry, not the first: the regexp this replaces stopped at the first
// source/version pair it found, so a fixture declaring two providers published
// one of them and hid the other. A table that is short by a row reads as
// complete, which is the defect the client matrix was already filed for once.
//
// Comments are stripped first, by the stripper stack.go already owns rather
// than by a second one written here. It matters more than it looks: one fixture
// quotes a sentence in double quotes inside a comment, and a stripper that does
// not know it is inside a comment opens a string that never closes and swallows
// the rest of the file.
func requiredProviders(body string) []providerPin {
	clean := stripHCLComments(body)
	var out []providerPin
	for i := 0; ; {
		j := strings.Index(clean[i:], "required_providers")
		if j < 0 {
			return out
		}
		inner, next := block(clean, i+j)
		i = next
		for k := 0; ; {
			head := entryHead.FindStringSubmatchIndex(inner[k:])
			if head == nil {
				break
			}
			entry, after := block(inner, k+head[0])
			k = after
			pin := providerPin{}
			if m := attrSource.FindStringSubmatch(entry); m != nil {
				pin.Source = m[1]
			}
			if m := attrVersion.FindStringSubmatch(entry); m != nil {
				pin.Constraint = m[1]
			}
			if pin.Source != "" {
				out = append(out, pin)
			}
		}
	}
}

// providerPins walks the fixture roots and reads every constraint they declare.
//
// An empty root is an error rather than an empty table. A walk that finds
// nothing is indistinguishable, on the page, from a repository that pins
// nothing — and this repository has just finished removing five checks that
// skipped themselves when their subject moved.
func providerPins(root string, driven func(dir string) bool) ([]providerPin, error) {
	var out []providerPin
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// A committed lock file would change what every cell of this table
		// means: a constraint resolved by a lock is knowable, and the page says
		// it is not. Refuse rather than keep printing the old sentence.
		if d.Name() == ".terraform.lock.hcl" {
			return fmt.Errorf("%s is committed: a lock file resolves the constraints this page "+
				"reports as unresolved, so the generator has to read it before the page can be trusted", path)
		}
		if filepath.Ext(path) != ".tf" {
			return nil
		}
		body, err := os.ReadFile(path) //nolint:gosec // a path from our own walk
		if err != nil {
			return err
		}
		dir := filepath.ToSlash(filepath.Dir(path))
		for _, pin := range requiredProviders(string(body)) {
			pin.Dir = dir
			pin.Driven = driven(dir)
			out = append(out, pin)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no required_providers block under %s: the walk found nothing, "+
			"which is not the same fact as a repository that pins nothing", root)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir < out[j].Dir
		}
		return out[i].Source < out[j].Source
	})
	return out, nil
}

// stacksAppliedInCI answers which example stacks the conformance suite applies.
//
// Both halves are read: the script that names them, and the workflow that runs
// the script. A stack CI never applies proves nothing, whatever it pins, and
// examples/stacks/exoscale is exactly that today.
func stacksAppliedInCI(script, workflow string) (map[string]bool, error) {
	body, err := os.ReadFile(script) //nolint:gosec // a path this repository owns
	if err != nil {
		return nil, err
	}
	flow, err := os.ReadFile(workflow) //nolint:gosec // a path this repository owns
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	// The workflow is searched for the repository-relative path a workflow can
	// actually carry, not for the path this function was handed: a test renders
	// from a copy elsewhere on disk, and comparing against that copy's own path
	// would answer "CI does not run it" about every one of them.
	if !strings.Contains(string(flow), stacksScript) {
		return out, nil
	}
	for _, m := range runStack.FindAllStringSubmatch(string(body), -1) {
		out[m[1]] = true
	}
	return out, nil
}

// fixturesAppliedInCI answers which directories under tools/conformance a suite
// the workflow runs actually applies.
//
// The suites locate their fixture the same way, one idiom:
//
//	DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/terraform" && pwd)"
//
// so the reference is looked for as that idiom rather than as a bare directory
// name. Matching the bare name would credit `tools/conformance/scaleway/terraform`
// to a script called terraform.sh whatever it did, which is a check that always
// answers yes.
func fixturesAppliedInCI(root, workflow string) (map[string]bool, error) {
	runs, err := suitesRunInCI(workflow)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, run := range runs {
		script := filepath.Join(root, run.provider, run.suite+".sh")
		body, err := os.ReadFile(script) //nolint:gosec // a path derived from the workflow
		if err != nil {
			return nil, fmt.Errorf("%s runs %s and it cannot be read: %w", workflow, script, err)
		}
		entries, err := os.ReadDir(filepath.Join(root, run.provider))
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if strings.Contains(string(body), `}")/`+entry.Name()+`"`) {
				out[filepath.ToSlash(filepath.Join(root, run.provider, entry.Name()))] = true
			}
		}
	}
	return out, nil
}

// unusedPins names every version this workflow declares and never interpolates.
//
// A pin nothing reads is a number the page would publish as the version CI
// installs, while CI installs whatever the download step hard-codes. The
// mismatch is invisible in both files on their own, which is the profile of
// every defect this generator exists to close.
func unusedPins(workflow string) ([]string, error) {
	body, err := os.ReadFile(workflow) //nolint:gosec // a path this repository owns
	if err != nil {
		return nil, err
	}
	versions, err := pinnedVersions(workflow)
	if err != nil {
		return nil, err
	}
	var out []string
	for name := range versions {
		if !strings.Contains(string(body), "${"+name+"}") && !strings.Contains(string(body), "$"+name+" ") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// versionDeclaration matches a version variable the way YAML declares one,
// whatever its quoting. pinnedVersions reads single-quoted values only, which is
// the form this workflow uses; this is how the page learns that a declaration
// has stopped being in that form instead of reporting the client as unpinned.
var versionDeclaration = regexp.MustCompile(`(?m)^\s*([A-Z0-9_]+_VERSION):\s*\S`)

// unreadablePins names variables the workflow declares and the reader cannot
// parse.
//
// "Not pinned" has to mean nothing pins it, never "the parser did not see it".
// A double-quoted value would slip past pinnedVersions, and the row would read
// as unpinned while CI installed an exact version — an artefact lying by
// omission, in the one column #325 exists to make trustworthy.
func unreadablePins(workflow string) ([]string, error) {
	body, err := os.ReadFile(workflow) //nolint:gosec // a path this repository owns
	if err != nil {
		return nil, err
	}
	versions, err := pinnedVersions(workflow)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range versionDeclaration.FindAllStringSubmatch(string(body), -1) {
		if _, ok := versions[m[1]]; !ok {
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out, nil
}

// renderProved builds the page.
func renderProved(workflow, root, stacks, script string) (string, error) {
	versions, err := pinnedVersions(workflow)
	if err != nil {
		return "", err
	}
	proofs, err := proofsPerClient(workflow)
	if err != nil {
		return "", err
	}

	// Every variable the workflow pins belongs to a client this page prints.
	// The mirror of the refusal renderClients already makes for a client CI
	// drives and clientSources does not list: a pinned version nobody publishes
	// is a version a consumer cannot align on.
	claimed := map[string]bool{}
	for _, c := range clientSources {
		claimed[c.variable] = true
	}
	var unclaimed []string
	for name := range versions {
		if !claimed[name] {
			unclaimed = append(unclaimed, name)
		}
	}
	if len(unclaimed) > 0 {
		sort.Strings(unclaimed)
		return "", fmt.Errorf(
			"%s pins %s and clientSources in internal/cli/docs_clients.go claims no client for it: "+
				"a version CI installs and this page does not publish is one a consumer cannot align on",
			workflow, strings.Join(unclaimed, ", "))
	}

	// And every variable it declares is one the reader can see, so an unpinned
	// cell means unpinned rather than unparsed.
	unreadable, err := unreadablePins(workflow)
	if err != nil {
		return "", err
	}
	if len(unreadable) > 0 {
		return "", fmt.Errorf(
			"%s declares %s in a form pinnedVersions cannot read: the page would print "+
				"\"not pinned\" for a client CI installs at an exact version",
			workflow, strings.Join(unreadable, ", "))
	}

	// And every variable it pins is read by a step. A number declared and never
	// interpolated is a number this page would publish while CI installed
	// something else.
	unused, err := unusedPins(workflow)
	if err != nil {
		return "", err
	}
	if len(unused) > 0 {
		return "", fmt.Errorf(
			"%s declares %s and no step interpolates it: the page would publish a version "+
				"nothing installs", workflow, strings.Join(unused, ", "))
	}

	pins, err := providerPinsOfRepository(workflow, root, stacks, script)
	if err != nil {
		return "", err
	}
	return renderProvedTables(versions, proofs, pins), nil
}

// providerPinsOfRepository reads every provider constraint the fixtures and the
// stacks declare, each marked with whether a suite the conformance workflow
// runs applies it.
//
// Its own function because two readers want it: the table below, and the
// refusal in docs_stacks.go that will not let a directory be applied in CI
// without saying which provider versions it accepts. Computing it twice would
// be two chances for the page and the refusal to disagree about the same row.
func providerPinsOfRepository(workflow, root, stacks, script string) ([]providerPin, error) {
	appliedStacks, err := stacksAppliedInCI(script, workflow)
	if err != nil {
		return nil, err
	}
	appliedFixtures, err := fixturesAppliedInCI(root, workflow)
	if err != nil {
		return nil, err
	}
	driven := func(dir string) bool {
		if appliedFixtures[dir] {
			return true
		}
		// The first segment under the stacks root, not the whole tail: a stack
		// is applied with its `modules/` subtree copied beside it, so
		// examples/stacks/outscale/modules/net is applied exactly when
		// examples/stacks/outscale is. Comparing the whole tail said no, which
		// would have understated a proof — the failure the client matrix was
		// filed for, in its other direction.
		rest := strings.TrimPrefix(dir, stacks+"/")
		if rest == dir {
			return false
		}
		name, _, _ := strings.Cut(rest, "/")
		return appliedStacks[name]
	}

	fixturePins, err := providerPins(root, driven)
	if err != nil {
		return nil, err
	}
	stackPins, err := providerPins(stacks, driven)
	if err != nil {
		return nil, err
	}
	return append(fixturePins, stackPins...), nil
}

// renderProved builds the page, continued.
func renderProvedTables(versions map[string]string, proofs map[string][]clientProof, pins []providerPin) string {
	var b strings.Builder
	b.WriteString(docsGenerated)
	b.WriteString("\n\n### The clients\n\n")
	b.WriteString("| Client | Version | Pinned in | Drives, in CI |\n|---|---|---|---|\n")
	for _, c := range clientSources {
		version, ok := versions[c.variable]
		where := fmt.Sprintf("`%s` (`%s`)", pinnedIn, c.variable)
		if !ok {
			// Never invented. A client CI drives on a version nobody pinned is
			// a client whose version this release cannot claim.
			version, where = "not pinned", "nowhere"
		}
		names := make([]string, 0, len(proofs[c.name]))
		for _, proof := range proofs[c.name] {
			name := providerName(proof.provider)
			if len(names) == 0 || names[len(names)-1] != name {
				names = append(names, name)
			}
		}
		drives := "not run in CI"
		if len(names) > 0 {
			drives = strings.Join(names, ", ")
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", c.name, version, where, drives)
	}

	b.WriteString("\n### The Terraform providers\n\n")
	b.WriteString("The client above is the engine; what answers this emulator is the provider.\n")
	b.WriteString("Each row is one `required_providers` entry, read where it is written.\n\n")
	b.WriteString("| Fixture or stack | Provider | Constraint | What that is worth | Applied in CI |\n|---|---|---|---|---|\n")
	for _, pin := range pins {
		kind, meaning := pinKind(pin.Constraint)
		constraint := "—"
		if strings.TrimSpace(pin.Constraint) != "" {
			constraint = "`" + pin.Constraint + "`"
		}
		applied := "no"
		if pin.Driven {
			applied = "yes"
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | %s | %s: %s | %s |\n",
			pin.Dir, pin.Source, constraint, kind, meaning, applied)
	}

	b.WriteString("\nRead the third column narrowly, because it is the one a consumer pins against.\n")
	b.WriteString("An **exact** constraint names the version that answered. A **constraint** is\n")
	b.WriteString("resolved by `terraform init -upgrade` on the runner, every run, so the version\n")
	b.WriteString("that answered is not knowable from this repository — it is a floor, not a\n")
	b.WriteString("proof. **Not pinned** is not pinned: nothing here says which version ran, and\n")
	b.WriteString("no number is invented to fill the cell.\n")
	b.WriteString(renderStackExceptions())
	return b.String()
}

// spliceProved reports whether the page is out of date, and leaves it alone.
func spliceProved(path, workflow, root, stacks, script string) (bool, error) {
	current, err := os.ReadFile(path) //nolint:gosec // a path this repository owns
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !strings.Contains(string(current), provedStartMarker) {
		return false, nil
	}
	rendered, err := renderProved(workflow, root, stacks, script)
	if err != nil {
		return false, err
	}
	updated, err := spliceSection(string(current), provedStartMarker, provedEndMarker, rendered)
	if err != nil {
		return false, err
	}
	return updated != string(current), nil
}

func writeSplicedProved(path, workflow, root, stacks, script string) error {
	current, err := os.ReadFile(path) //nolint:gosec // same path as above
	if err != nil {
		return err
	}
	rendered, err := renderProved(workflow, root, stacks, script)
	if err != nil {
		return err
	}
	updated, err := spliceSection(string(current), provedStartMarker, provedEndMarker, rendered)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(updated), 0o644) //nolint:gosec // documentation is world-readable by design
}
