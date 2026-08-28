package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// theProviders is the vocabulary the claim reader searches for, taken from the
// packs this binary mounts rather than typed.
func theProviders(t *testing.T) []string {
	t.Helper()
	facts, err := readPackFacts()
	if err != nil {
		t.Fatalf("read the pack facts: %v", err)
	}
	if len(facts.Providers) == 0 {
		t.Fatal("no pack is mounted: the claim reader would find no provider name in any sentence")
	}
	return facts.Providers
}

// planted writes one generated block to a temporary page and returns what the
// reader says about it.
func planted(t *testing.T, block string) []string {
	t.Helper()
	page := filepath.Join(t.TempDir(), "README.md")
	body := "# feint\n\n<!-- promise:start -->\n" + block + "\n<!-- promise:end -->\n"
	if err := os.WriteFile(page, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return unownedCapabilityClaims(page, theProviders(t))
}

// The exact sentence #592 measured, planted, and the reader has to find it.
//
// This is the witness rule: a control whose success is "nothing was found" is
// indistinguishable from a control that looked nowhere. The value planted here
// is not invented — it is `README.md:41` as it stood on `main@3b00d23`, which is
// the sentence that survived every green `docs:check` between #525 landing on
// 2026-08-26 and the audit that read it two days later.
func TestTheOldFalsePromiseIsCaughtByTheClaimReader(t *testing.T) {
	const wasOnLine41 = "**Run your Terraform against Scaleway, Outscale or Exoscale without a cloud\n" +
		"account, without credentials, and without creating a single resource.**"

	problems := planted(t, wasOnLine41)
	if len(problems) == 0 {
		t.Fatal("the sentence that was false for two days passed: this reader would have watched " +
			"#592 happen and said nothing")
	}
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, "Exoscale") || !strings.Contains(joined, "Terraform") {
		t.Errorf("the refusal names neither the pack nor the client:\n  %s",
			strings.Join(problems, "\n  "))
	}
	// And only that pair. Scaleway and Outscale really are driven by Terraform,
	// and a reader that reported them too would be one nobody could act on.
	if len(problems) != 1 {
		t.Errorf("%d problems for one sentence: the two pairs that are true were reported as "+
			"well, which is a reader that cries at everything\n  %s",
			len(problems), strings.Join(problems, "\n  "))
	}
}

// And the accepting half, which is the one a refusing-everything reader fails.
func TestTheClaimReaderAcceptsWhatTheMatrixCarries(t *testing.T) {
	for _, accepted := range []struct {
		name  string
		block string
	}{
		{
			"the promise this repository ships",
			mustRenderPromise(t, false),
		},
		{
			"the French promise",
			mustRenderPromise(t, true),
		},
		{
			"a recipe pointing Terraform at Scaleway",
			"```bash\nfeint start\neval \"$(feint env scaleway)\"\nterraform apply\n```",
		},
		{
			"a sentence naming the refused pair and the reason",
			"Terraform joins Exoscale the day a release carries " +
				"exoscale/terraform-provider-exoscale#573.",
		},
		{
			"a table row per client",
			"| Client | Drives |\n|---|---|\n| Terraform | Scaleway, Outscale |\n| `exo` | Exoscale |",
		},
	} {
		t.Run(accepted.name, func(t *testing.T) {
			if problems := planted(t, accepted.block); len(problems) != 0 {
				t.Errorf("a legitimate block was refused, so the reader breaks the product it "+
					"protects:\n  %s", strings.Join(problems, "\n  "))
			}
		})
	}
}

// And the refusing half, on the shapes the unit rule exists for.
func TestTheClaimReaderRefusesEveryShapeOfTheClaim(t *testing.T) {
	for _, refused := range []struct {
		name  string
		block string
		want  string
	}{
		{
			"a quick start pointing Terraform at the pack the doorstep refuses",
			"```bash\nfeint start\neval \"$(feint env exoscale)\"\nterraform apply\n```",
			"Exoscale",
		},
		{
			"a sentence naming the refused pair without saying why",
			"Terraform drives Exoscale as well.",
			"exoscale/terraform-provider-exoscale#573",
		},
		{
			"a table row claiming it",
			"| Client | Drives |\n|---|---|\n| Terraform | Exoscale |",
			"Exoscale",
		},
		{
			"a list item claiming it",
			"- **Exoscale** with Terraform.\n- **Scaleway** with `scw`.",
			"Exoscale",
		},
		{
			"a pair no row carries at all",
			"The `scw` CLI drives Outscale.",
			"carries no row",
		},
	} {
		t.Run(refused.name, func(t *testing.T) {
			problems := planted(t, refused.block)
			if len(problems) == 0 {
				t.Fatalf("this block claimed a capability nothing owns and passed:\n%s", refused.block)
			}
			if !strings.Contains(strings.Join(problems, "\n"), refused.want) {
				t.Errorf("the refusal does not say what to fix:\n  %s", strings.Join(problems, "\n  "))
			}
		})
	}
}

// The unit rule is what makes co-occurrence mean something, and each of its
// three cases was needed by a real block in this repository.
func TestAUnitIsASentenceATableRowAListItemOrAWholeFence(t *testing.T) {
	block := "" +
		"| Terraform | Outscale, Scaleway |\n" +
		"| `exo` | Exoscale |\n" +
		"\n" +
		"First sentence. Second sentence.\n" +
		"\n" +
		"- one item\n" +
		"- another item\n" +
		"\n" +
		"```bash\nline one\nline two\n```\n"

	units := capabilityUnits(block)
	want := []string{
		"| Terraform | Outscale, Scaleway |",
		"| `exo` | Exoscale |",
		"First sentence.",
		"Second sentence.",
		"- one item",
		"- another item",
		"line one\nline two",
	}
	if len(units) != len(want) {
		t.Fatalf("got %d units, want %d:\n  %s", len(units), len(want), strings.Join(units, "\n  "))
	}
	for i := range want {
		if strings.TrimSpace(units[i]) != want[i] {
			t.Errorf("unit %d is %q, want %q", i, strings.TrimSpace(units[i]), want[i])
		}
	}
}

// `exo` is not hiding inside `exoscale`, and `terraform` really is inside
// `terraform-provider-exoscale`.
//
// Both matter and they pull in opposite directions. A substring search would
// find `exo` in every mention of the pack and claim a CLI nobody named; a
// whole-token rule that stopped at punctuation would miss the client named in
// the upstream issue reference, which is exactly the string a legitimate
// sentence about the refusal carries.
func TestTheReaderTokenisesRatherThanSearchesForSubstrings(t *testing.T) {
	providers := theProviders(t)

	if pairs := capabilityPairsIn("Exoscale is a cloud.", providers); len(pairs) != 0 {
		t.Errorf("`exo` was found inside `Exoscale`: %v", pairs)
	}
	pairs := capabilityPairsIn("exoscale/terraform-provider-exoscale#573", providers)
	found := false
	for _, pair := range pairs {
		if pair[0] == "terraform" && pair[1] == "exoscale" {
			found = true
		}
	}
	if !found {
		t.Errorf("the upstream reference names both and the reader saw neither: %v", pairs)
	}
}

// A configuration whose resource blocks are not its objects is refused rather
// than counted wrong.
//
// The equality "one `resource` block, one object" is what lets the front page
// print a number, and `count` or `for_each` breaks it silently — the figure
// would be off by however many the loop makes, on the one line a first-time
// reader checks against their own terminal. Refusing says the example has
// outgrown the job; counting anyway would publish an invented measurement.
func TestAQuickStartThatMultipliesItsResourcesIsRefusedRatherThanMiscounted(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		t.Helper()
		path := filepath.Join(dir, "main.tf")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	// The accepting half first: a plain configuration counts.
	plain := write("resource \"a\" \"one\" {}\nresource \"b\" \"two\" {\n  name = \"x\"\n}\n" +
		"# resource \"c\" \"commented\" {}\n")
	got, err := resourceCount(plain)
	if err != nil {
		t.Fatalf("a plain configuration was refused: %v", err)
	}
	if got != 2 {
		t.Errorf("counted %d resources, want 2: a commented block or a nested one was miscounted", got)
	}

	for _, refused := range []struct{ name, body string }{
		{"count", "resource \"a\" \"one\" {\n  count = 3\n}\n"},
		{"for_each", "resource \"a\" \"one\" {\n  for_each = toset([\"x\", \"y\"])\n}\n"},
		{"nothing at all", "variable \"endpoint\" {\n  type = string\n}\n"},
	} {
		t.Run(refused.name, func(t *testing.T) {
			if _, err := resourceCount(write(refused.body)); err == nil {
				t.Errorf("a configuration whose blocks are not its objects was counted anyway, so "+
					"the README would print a number nobody produces (%s)", refused.name)
			}
		})
	}
}

// mustRenderPromise is the block this repository really ships, in one locale.
func mustRenderPromise(t *testing.T, french bool) string {
	t.Helper()
	rendered, err := renderPromise(french)
	if err != nil {
		t.Fatalf("render the promise: %v", err)
	}
	return rendered
}

// The French page is in French.
//
// #591's fourth finding: the generated blocks were injected in English into both
// READMEs on the rule that *a command needs no translation*, and the rule was
// right about commands and wrong about the prose beside them — README.fr.md
// carried "On your machine" and "In CI, or anywhere Docker runs" in the middle
// of a French page.
func TestTheFrenchQuickStartIsInFrench(t *testing.T) {
	root := repoRoot(t)
	rendered, err := renderQuickstart(filepath.Join(root, goModPath), filepath.Join(root, changelogPath), true)
	if err != nil {
		t.Fatalf("render the French quick start: %v", err)
	}
	// The sentences that were landing in English, named one by one so a
	// regression says which.
	for _, english := range []string{
		"On your machine",
		"In CI, or anywhere Docker runs",
		"In GitHub Actions without a container",
		"Already have a project?",
		"detaches, waits until it answers",
	} {
		if strings.Contains(rendered, english) {
			t.Errorf("the French quick start carries %q", english)
		}
	}
	// And the accepting half: it really is the block, not an empty string that
	// would pass every assertion above.
	for _, french := range []string{
		"Sur votre machine",
		"En CI, ou partout où Docker tourne",
		"Vous avez déjà un projet ?",
	} {
		if !strings.Contains(rendered, french) {
			t.Errorf("the French quick start does not carry %q, so the check above measured an "+
				"empty render", french)
		}
	}
	// The commands themselves are not translated, which is the half of the old
	// rule that was right.
	for _, command := range []string{"feint up", "feint down", "terraform apply", "feint start"} {
		if !strings.Contains(rendered, command) {
			t.Errorf("the French quick start lost the command %q", command)
		}
	}
}

// The quick start sends a reader at a directory that exists and that a suite
// applies.
//
// #593's complaint in one line: the block printed `terraform apply` with no
// directory, no `main.tf` and no provider block, under an `Apply complete!` that
// was not reachable from it. A path in a generated block that resolves to
// nothing is the same defect with a longer walk.
func TestTheQuickStartPointsAtADirectoryThatExists(t *testing.T) {
	root := repoRoot(t)
	rendered, err := renderQuickstart(filepath.Join(root, goModPath), filepath.Join(root, changelogPath), false)
	if err != nil {
		t.Fatalf("render the quick start: %v", err)
	}
	stack := quickstartRoot + "/" + quickstartLead
	if !strings.Contains(rendered, stack) {
		t.Fatalf("the quick start does not name %s:\n%s", stack, rendered)
	}
	for _, needed := range []string{"main.tf", "feint.yaml"} {
		path := filepath.Join(root, quickstartRoot, quickstartLead, needed)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("the quick start sends a reader to %s and it has no %s: the four commands "+
				"cannot work", stack, needed)
		}
	}
	// The output it displays is the output those commands produce, counted from
	// the configuration rather than typed. `Resources: 5 added` sat under three
	// commands that could not produce it, and that is #593's second finding.
	added, err := resourceCount(filepath.Join(root, quickstartRoot, quickstartLead, "main.tf"))
	if err != nil {
		t.Fatalf("count what the quick start creates: %v", err)
	}
	line := fmt.Sprintf("Apply complete! Resources: %d added, 0 changed, 0 destroyed.", added)
	if !strings.Contains(rendered, line) {
		t.Errorf("the quick start does not print %q, so the output it shows is not derived from "+
			"the configuration it runs", line)
	}

	// And it teaches the verbs this project ships rather than the ones before
	// them: `feint up` first, the hand-driven form underneath.
	up := strings.Index(rendered, "feint up")
	start := strings.Index(rendered, "feint start")
	if up < 0 || start < 0 {
		t.Fatalf("the quick start lost one of the two doors:\n%s", rendered)
	}
	if up > start {
		t.Error("the quick start teaches `feint start` before `feint up`, which is the 0.10 " +
			"sequence in front of the one 0.11 ships (#593)")
	}
}
