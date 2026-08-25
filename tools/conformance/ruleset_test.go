package conformance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The required status checks name the matrix legs, and a renamed leg silently
// stops being required.
//
// Measured on this repository, 2026-08-25 (#460): renaming the Outscale leg from
// `oapi-cli` to `octl` in .github/workflows/conformance.yml left
// .github/rulesets/main.json requiring a context named `oapi-cli`. GitHub
// matches a required check by NAME against the checks a run reports, so the
// effect is one of two, and neither is visible from a green pull request:
//
//   - the ruleset is live and the pull request waits forever on a check no job
//     will ever report;
//   - the ruleset is re-applied from the file and the new leg is required by
//     nobody, so the Outscale conformance job can go red without blocking a
//     merge.
//
// The second is the dangerous one, and it is exactly the shape CLAUDE.md warns
// about: a gate that stops measuring reads identically to a gate that passes.
// `.claude/skills/branch-and-pr` states the rule in prose; this is the control.
//
// It compares only the contexts that ARE matrix legs. The ruleset also requires
// checks from other workflows (TruffleHog, Zizmor, Conventional Commits…) and
// deliberately does not require every leg — `fields` is absent — so the
// assertion is one-directional: every required context that looks like a leg of
// this matrix must be one. Widening it to "every leg is required" would fail on
// a decision somebody made on purpose.
func TestEveryRequiredConformanceCheckIsAMatrixLeg(t *testing.T) {
	root := repoRoot(t)

	legs := matrixLegs(t, filepath.Join(root, ".github", "workflows", "conformance.yml"))
	if len(legs) == 0 {
		t.Fatal("no matrix leg was read from conformance.yml, so this test compared nothing")
	}
	// The witness the rule about absence demands: the leg this test was written
	// for has to be found, or the reader above is matching nothing.
	if !legs["octl"] {
		t.Fatalf("the matrix does not name `octl`; the reader found %v", keys(legs))
	}

	contexts := requiredContexts(t, filepath.Join(root, ".github", "rulesets", "main.json"))
	if len(contexts) == 0 {
		t.Fatal("no required context was read from the ruleset, so this test compared nothing")
	}

	// A context is judged only when it is lowercase and dash-free of spaces —
	// the shape a matrix leg has. Everything else belongs to another workflow.
	legShaped := regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	matched := 0
	for _, ctx := range contexts {
		if !legShaped.MatchString(ctx) {
			continue
		}
		matched++
		if !legs[ctx] {
			t.Errorf("the ruleset requires the check %q and .github/workflows/conformance.yml "+
				"has no such leg: a required check no job reports either blocks every pull "+
				"request forever, or stops requiring the job it was named after. The legs are %v.",
				ctx, keys(legs))
		}
	}
	if matched == 0 {
		t.Fatal("no required context was even considered a leg, so the comparison above ran on nothing")
	}
}

// matrixLegs reads the `client:` matrix of the conformance workflow.
//
// A line rather than a YAML parse, deliberately: this module carries no
// dependencies, and the one line it needs has a fixed shape that a change would
// break loudly here rather than quietly at merge time.
func matrixLegs(t *testing.T, path string) map[string]bool {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the conformance workflow: %v", err)
	}
	line := regexp.MustCompile(`(?m)^\s*client:\s*\[([^\]]*)\]`)
	m := line.FindSubmatch(body)
	if m == nil {
		t.Fatal("no `client: [...]` matrix line in the conformance workflow: the reader this " +
			"test depends on found nothing, which would make it pass by looking nowhere")
	}
	out := map[string]bool{}
	for _, name := range strings.Split(string(m[1]), ",") {
		if name = strings.TrimSpace(name); name != "" {
			out[name] = true
		}
	}
	return out
}

func requiredContexts(t *testing.T, path string) []string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the ruleset: %v", err)
	}
	var ruleset struct {
		Rules []struct {
			Type       string `json:"type"`
			Parameters struct {
				RequiredStatusChecks []struct {
					Context string `json:"context"`
				} `json:"required_status_checks"`
			} `json:"parameters"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(body, &ruleset); err != nil {
		t.Fatalf("parsing the ruleset: %v", err)
	}
	var out []string
	for _, rule := range ruleset.Rules {
		for _, check := range rule.Parameters.RequiredStatusChecks {
			if check.Context != "" {
				out = append(out, check.Context)
			}
		}
	}
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// repoRoot walks up from the test's directory until it finds go.mod, so the
// test works from a worktree as well as from the checkout.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolving the working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory, so the repository root is unknown")
		}
		dir = parent
	}
}
