package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The record answers for every operation the document declares and the scan does
// not know, and it answers with a decision rather than a name (#622).
//
// This is the gate that puts those operations in front of the same triage as
// everything else. Without it they were outside `total`, so `unknown: 0` was
// exact over a set that did not contain them — a count that was right about the
// wrong population.
func TestContractOnlyOperationsAreTriaged(t *testing.T) {
	record := filepath.Join("..", "..", "coverage", "contract-only.json")
	only := []string{
		"iam/v1alpha1.CheckPermissions",
		"instance/v1.SetImage",
		"instance/v1.SetSecurityGroup",
		"instance/v1.SetSecurityGroupRule",
		"instance/v1.SetSnapshot",
		"instance/v1.SetVolume",
	}

	problems, err := contractOnlyGaps(record, "scaleway", only)
	if err != nil {
		t.Fatalf("read the record: %v", err)
	}
	if len(problems) > 0 {
		t.Errorf("the committed record does not account for the document's surface:\n%s",
			strings.Join(problems, "\n"))
	}

	// An operation nobody decided about is the failure this exists for.
	problems, err = contractOnlyGaps(record, "scaleway", append(only, "instance/v1.SetSomethingNew"))
	if err != nil {
		t.Fatalf("read the record: %v", err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "SetSomethingNew") {
		t.Errorf("an undecided operation was not reported: %v", problems)
	}

	// And a stale one, because a record that only ever grows is a ratchet nobody
	// can lower: an entry naming an operation the SDK has since wrapped covers
	// nothing and must go.
	problems, err = contractOnlyGaps(record, "scaleway", only[:len(only)-1])
	if err != nil {
		t.Fatalf("read the record: %v", err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "covers nothing") {
		t.Errorf("a stale entry was not reported: %v", problems)
	}
}

// A status and a reason, held to the standard Declined() already lives under.
// Out of scope and not done yet are different answers, and an entry that says
// neither is a name parked in a file.
func TestAContractOnlyEntryStatesAStatusAndAReason(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		path := filepath.Join(dir, "record.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		return path
	}

	for _, c := range []struct{ name, body, want string }{
		{
			"an unknown status",
			`{"providers":{"scaleway":[{"operation":"a/v1.X","status":"maybe","reason":"a reason long enough to weigh properly"}]}}`,
			"status",
		},
		{
			"a reason nobody can weigh",
			`{"providers":{"scaleway":[{"operation":"a/v1.X","status":"backlog","reason":"later"}]}}`,
			"weigh",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			problems, err := contractOnlyGaps(write(c.body), "scaleway", []string{"a/v1.X"})
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if len(problems) != 1 || !strings.Contains(problems[0], c.want) {
				t.Errorf("want a complaint about %q, got %v", c.want, problems)
			}
		})
	}

	// The accepting half: both statuses are honoured, or the guard refuses
	// everything and the distinction it exists for is decoration.
	for _, status := range []string{"declined", "backlog"} {
		body := `{"providers":{"scaleway":[{"operation":"a/v1.X","status":"` + status +
			`","reason":"a reason long enough for a reader to weigh"}]}}`
		problems, err := contractOnlyGaps(write(body), "scaleway", []string{"a/v1.X"})
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if len(problems) != 0 {
			t.Errorf("status %q was refused: %v", status, problems)
		}
	}
}
