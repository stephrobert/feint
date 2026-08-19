package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// provedPage renders the page from the repository as it stands.
func provedPage(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	rendered, err := renderProved(
		filepath.Join(root, conformanceWorkflow),
		filepath.Join(root, conformanceRoot),
		filepath.Join(root, stacksRoot),
		filepath.Join(root, stacksScript),
	)
	if err != nil {
		t.Fatalf("render the proved-against page: %v", err)
	}
	return rendered
}

// rowContaining returns the single table row naming key, and fails when there
// is none.
//
// A helper that fails rather than one that returns "" and lets the caller skip:
// the assertions below are about rows, and a row that stopped existing must
// break the test that reads it rather than quietly pass it.
func rowContaining(t *testing.T, rendered, key string) string {
	t.Helper()
	for _, line := range strings.Split(rendered, "\n") {
		if strings.HasPrefix(line, "| ") && strings.Contains(line, key) {
			return line
		}
	}
	t.Fatalf("no row naming %s in:\n%s", key, rendered)
	return ""
}

// The three states a Terraform constraint can be in, kept apart.
//
// This is the whole point of the page. An exact pin names the version that
// answered; `~> 1.7` is resolved on the runner by `terraform init -upgrade`, so
// what answered is not knowable here; and two blocks in this repository carry no
// constraint at all. Printing the second as if it were the first is the lie a
// consumer would pin against, and inventing a number for the third is the
// omission #325 names explicitly.
func TestTheProvedPageSeparatesAnExactPinFromAConstraintAndFromNothing(t *testing.T) {
	rendered := provedPage(t)

	exact := rowContaining(t, rendered, "tools/conformance/scaleway/terraform`")
	if !strings.Contains(exact, "2.81.0") || !strings.Contains(exact, "exact") {
		t.Errorf("the Scaleway fixture pins one version and the page does not say so:\n  %s", exact)
	}

	loose := rowContaining(t, rendered, "tools/conformance/outscale/terraform`")
	if !strings.Contains(loose, "constraint") {
		t.Errorf("`~> 1.7` is presented as a proven version:\n  %s", loose)
	}
	if strings.Contains(loose, "exact") {
		t.Errorf("a constraint reads as an exact pin:\n  %s", loose)
	}

	// The stack that pins nothing. It must say so, and it must not borrow the
	// number from another row.
	none := rowContaining(t, rendered, "examples/stacks/exoscale")
	if !strings.Contains(none, "not pinned") {
		t.Errorf("a required_providers entry with no version does not read as unpinned:\n  %s", none)
	}
	for _, invented := range []string{"2.81.0", "1.7", "0.6"} {
		if strings.Contains(none, invented) {
			t.Errorf("the unpinned row carries the version %s, which nothing in that stack pins:\n  %s",
				invented, none)
		}
	}
}

// A version the workflow declares and no step reads is a version this page
// would publish while CI installed something else.
func TestEveryPinnedVersionIsInstalledByAStepThatUsesIt(t *testing.T) {
	root := repoRoot(t)
	workflow := filepath.Join(root, conformanceWorkflow)

	// The accepting half, on the workflow as it stands.
	unused, err := unusedPins(workflow)
	if err != nil {
		t.Fatalf("read the workflow: %v", err)
	}
	if len(unused) != 0 {
		t.Fatalf("%v is declared and never interpolated: either CI installs something else, "+
			"or the pin is decorative", unused)
	}

	// The refusing half: a step that hard-codes the version it used to
	// interpolate. Both files stay plausible on their own, which is the profile
	// of the defect.
	body, err := os.ReadFile(workflow)
	if err != nil {
		t.Fatal(err)
	}
	hardCoded := strings.ReplaceAll(string(body), "${SCW_VERSION}", "2.56.3")
	if hardCoded == string(body) {
		t.Fatal("the workflow no longer interpolates SCW_VERSION anywhere: this test is " +
			"measuring a file it does not understand")
	}
	copied := filepath.Join(t.TempDir(), "conformance.yml")
	if err := os.WriteFile(copied, []byte(hardCoded), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = renderProved(copied, filepath.Join(root, conformanceRoot),
		filepath.Join(root, stacksRoot), filepath.Join(root, stacksScript))
	if err == nil {
		t.Fatal("the page renders a pinned version no step reads, so it would publish a number " +
			"nothing installs")
	}
	if !strings.Contains(err.Error(), "SCW_VERSION") {
		t.Errorf("the refusal does not name the variable, which is the one thing needed to fix it: %v", err)
	}
}

// A pin CI installs and this page does not publish is one a consumer cannot
// align on. The mirror of the client matrix's own refusal.
func TestAPinnedVersionNoClientClaimsIsRefused(t *testing.T) {
	root := repoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, conformanceWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	// Declared and interpolated, so it passes the other guard and fails only
	// this one.
	extra := strings.Replace(string(body), "  SCW_VERSION:",
		"  KUBECTL_VERSION: '1.34.0'\n  # uses ${KUBECTL_VERSION}\n  SCW_VERSION:", 1)
	if extra == string(body) {
		t.Fatal("the workflow no longer declares SCW_VERSION at the top level: this test is " +
			"measuring a file it does not understand")
	}
	copied := filepath.Join(t.TempDir(), "conformance.yml")
	if err := os.WriteFile(copied, []byte(extra), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = renderProved(copied, filepath.Join(root, conformanceRoot),
		filepath.Join(root, stacksRoot), filepath.Join(root, stacksScript))
	if err == nil {
		t.Fatal("a version CI pins is silently left off the page")
	}
	if !strings.Contains(err.Error(), "KUBECTL_VERSION") {
		t.Errorf("the refusal does not name the unpublished pin: %v", err)
	}
}

// "Not pinned" must mean nothing pins it, never "the reader did not see it".
//
// The workflow declares its versions single-quoted and pinnedVersions reads that
// form. Change the quoting and the pin vanishes from the map, the row reads as
// unpinned, and the page understates a client CI installs at an exact version —
// a lie by omission in the one column this page exists to make trustworthy.
func TestAPinDeclaredInAFormTheReaderCannotSeeIsRefused(t *testing.T) {
	root := repoRoot(t)
	workflow := filepath.Join(root, conformanceWorkflow)

	// The accepting half: every declaration in the workflow as it stands is one
	// the reader parses.
	unreadable, err := unreadablePins(workflow)
	if err != nil {
		t.Fatalf("read the workflow: %v", err)
	}
	if len(unreadable) != 0 {
		t.Fatalf("%v is declared in a form the reader cannot parse, so the page reports it "+
			"as unpinned", unreadable)
	}

	body, err := os.ReadFile(workflow)
	if err != nil {
		t.Fatal(err)
	}
	requoted := strings.Replace(string(body), "SCW_VERSION: '", "SCW_VERSION: \"", 1)
	if requoted == string(body) {
		t.Fatal("the workflow no longer declares SCW_VERSION single-quoted: this test is " +
			"measuring a file it does not understand")
	}
	requoted = strings.Replace(requoted, "2.56.3'", "2.56.3\"", 1)
	copied := filepath.Join(t.TempDir(), "conformance.yml")
	if err := os.WriteFile(copied, []byte(requoted), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = renderProved(copied, filepath.Join(root, conformanceRoot),
		filepath.Join(root, stacksRoot), filepath.Join(root, stacksScript))
	if err == nil {
		t.Fatal("a pin the reader cannot parse renders as \"not pinned\" instead of being refused")
	}
	if !strings.Contains(err.Error(), "SCW_VERSION") {
		t.Errorf("the refusal does not name the declaration it could not read: %v", err)
	}
}

// A fixture nothing applies is not a proof, and the page says which is which.
func TestAFixtureNoSuiteAppliesIsNotAProof(t *testing.T) {
	root := repoRoot(t)
	rendered := provedPage(t)

	applied := rowContaining(t, rendered, "examples/stacks/scaleway")
	if !strings.HasSuffix(strings.TrimSpace(applied), "| yes |") {
		t.Errorf("the Scaleway stack is applied by tools/conformance/stacks.sh and the page "+
			"does not credit it:\n  %s", applied)
	}
	// The other half of the same derivation, on a stack that exists and that no
	// `run_stack` line names.
	unapplied := rowContaining(t, rendered, "examples/stacks/exoscale")
	if !strings.HasSuffix(strings.TrimSpace(unapplied), "| no |") {
		t.Errorf("a stack no suite applies is presented as applied in CI:\n  %s", unapplied)
	}

	// And the column follows the script rather than the directory listing: with
	// the invocation removed, the same stack stops being credited.
	body, err := os.ReadFile(filepath.Join(root, stacksScript))
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.Replace(string(body), "run_stack scaleway", "true # run_stack scaleway", 1)
	if trimmed == string(body) {
		t.Fatal("tools/conformance/stacks.sh no longer applies the scaleway stack: either it " +
			"was removed, in which case the page must have stopped crediting it, or this test " +
			"is measuring a file it does not understand")
	}
	copied := filepath.Join(t.TempDir(), "stacks.sh")
	if err := os.WriteFile(copied, []byte(trimmed), 0o600); err != nil {
		t.Fatal(err)
	}
	without, err := renderProved(filepath.Join(root, conformanceWorkflow),
		filepath.Join(root, conformanceRoot), filepath.Join(root, stacksRoot), copied)
	if err != nil {
		t.Fatalf("render from the trimmed script: %v", err)
	}
	if row := rowContaining(t, without, "examples/stacks/scaleway"); strings.HasSuffix(strings.TrimSpace(row), "| yes |") {
		t.Errorf("the stack is still credited after CI stopped applying it, so the column is "+
			"not read from the script:\n  %s", row)
	}

	// And the second half of the same question, which is a different file: the
	// script naming a stack proves nothing if no workflow runs the script. A
	// suite that exists and runs only under `mise run conformance` is a proof
	// somebody has to take, not one this page can claim.
	flow, err := os.ReadFile(filepath.Join(root, conformanceWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	unrun := strings.ReplaceAll(string(flow), stacksScript, "tools/conformance/nothing.sh")
	if unrun == string(flow) {
		t.Fatalf("%s no longer runs %s: this test is measuring a file it does not understand",
			conformanceWorkflow, stacksScript)
	}
	copiedFlow := filepath.Join(t.TempDir(), "conformance.yml")
	if err := os.WriteFile(copiedFlow, []byte(unrun), 0o600); err != nil {
		t.Fatal(err)
	}
	page, err := renderProved(copiedFlow, filepath.Join(root, conformanceRoot),
		filepath.Join(root, stacksRoot), filepath.Join(root, stacksScript))
	if err != nil {
		t.Fatalf("render from the trimmed workflow: %v", err)
	}
	if row := rowContaining(t, page, "examples/stacks/scaleway"); strings.HasSuffix(strings.TrimSpace(row), "| yes |") {
		t.Errorf("a stack is credited to CI while no workflow runs the suite that applies it:\n  %s", row)
	}
}

// Every entry of a required_providers block, not the first one.
func TestEveryEntryOfARequiredProvidersBlockIsRead(t *testing.T) {
	body := `terraform {
  required_providers {
    # A comment quoting a sentence in double quotes, "like this one", and a
    # stray brace { to go with it.
    scaleway = {
      source  = "scaleway/scaleway"
      version = "2.81.0"
    }
    incus = {
      source = "lxc/incus"
    }
  }
}
`
	pins := requiredProviders(body)
	if len(pins) != 2 {
		t.Fatalf("read %d entries from a two-provider block: %+v", len(pins), pins)
	}
	if pins[0].Source != "scaleway/scaleway" || pins[0].Constraint != "2.81.0" {
		t.Errorf("the first entry came back as %+v", pins[0])
	}
	if pins[1].Source != "lxc/incus" || pins[1].Constraint != "" {
		t.Errorf("the second entry came back as %+v; a block with no version must not borrow "+
			"the previous one's", pins[1])
	}
}

// A walk that finds nothing is refused, and so is a committed lock file.
//
// The first is the "conditional that stops measuring" failure in its purest
// form: a table generated from an empty walk is indistinguishable, on the page,
// from a repository that pins nothing. The second is a fact that would change
// what every cell means — a constraint resolved by a lock is knowable — while
// the page kept printing the old sentence.
func TestAnEmptyWalkAndACommittedLockAreBothRefused(t *testing.T) {
	always := func(string) bool { return true }

	empty := t.TempDir()
	if _, err := providerPins(empty, always); err == nil {
		t.Error("a root with no required_providers block renders an empty table instead of refusing")
	}

	locked := t.TempDir()
	if err := os.WriteFile(filepath.Join(locked, "main.tf"),
		[]byte("terraform {\n  required_providers {\n    a = {\n      source = \"a/a\"\n    }\n  }\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, ".terraform.lock.hcl"), []byte("provider \"registry.terraform.io/a/a\" {\n  version = \"1.0.0\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := providerPins(locked, always)
	if err == nil {
		t.Fatal("a committed lock file is ignored, so the page keeps saying the constraint is " +
			"resolved on the runner when it is resolved in the repository")
	}
	if !strings.Contains(err.Error(), ".terraform.lock.hcl") {
		t.Errorf("the refusal does not name the file: %v", err)
	}
}
