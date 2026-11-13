package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The limits index is a generated section like every other one in `feint
// docs`: a stale table must fail `docs --check`, and regenerating must repair
// it. A table nobody regenerates reads as a measurement, which is worse than
// no table.
func TestTheLimitsIndexIsRegeneratedAndChecked(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "README.md")
	limits := filepath.Join(dir, "limits.md")
	staleTarget := "intro\n\n<!-- coverage:start -->\nwritten by hand, and wrong\n<!-- coverage:end -->\n\noutro\n"
	if err := os.WriteFile(target, []byte(staleTarget), 0o600); err != nil {
		t.Fatal(err)
	}
	staleLimits := "# Limits\n\nintro\n\n## Product coverage at a glance\n\n<!-- limits-index:start -->\nwritten by hand, and wrong\n<!-- limits-index:end -->\n\n## Object Storage: simple explicit doors\n"
	if err := os.WriteFile(limits, []byte(staleLimits), 0o600); err != nil {
		t.Fatal(err)
	}
	coverage := repoPath(t, "coverage")

	flags := []string{"docs", "--file", target, "--coverage", coverage, "--limits", limits}
	if code, _, errOut := run(flags...); code != 0 {
		t.Fatalf("docs exited %d: %s", code, errOut)
	}
	written, err := os.ReadFile(limits) //nolint:gosec // a path this test just made
	if err != nil {
		t.Fatal(err)
	}
	body := string(written)
	if strings.Contains(body, "written by hand, and wrong") {
		t.Error("the limits index was not regenerated at all")
	}
	if !strings.Contains(body, "not scanned") && !strings.Contains(body, "partially served") {
		t.Errorf("the regenerated index carries no product rows:\n%s", body)
	}
	// The hand-written heading and the section after it survive the splice:
	// rewriting the whole file from a template would put the prose at the
	// mercy of the generator.
	if !strings.Contains(body, "## Product coverage at a glance") || !strings.Contains(body, "## Object Storage: simple explicit doors") {
		t.Errorf("the splice rewrote prose outside its markers:\n%s", body)
	}
	// And the run is idempotent, which is what a gate reads.
	if code, _, errOut := run(append(append([]string{}, flags...), "--check")...); code != 0 {
		t.Fatalf("docs --check is red right after a write: %s", errOut)
	}

	// A stale row fails the gate: the check compares, it does not trust.
	corrupt := strings.Replace(body, "| `instance` | partially served |", "| `instance` | served |", 1)
	if corrupt == body {
		// No instance row in this artefact: corrupt any table row's status,
		// which must fail the same way.
		corrupt = strings.Replace(body, " | partially served |", " | served |", 1)
	}
	if err := os.WriteFile(limits, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := run(append(append([]string{}, flags...), "--check")...); code != 2 {
		t.Fatalf("docs --check on a stale index exited %d, want 2: %s", code, errOut)
	}
}
