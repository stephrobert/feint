package machine

import (
	"context"
	"strings"
	"testing"
)

// Survey is the read-only half of the sweep: what a restart names must be
// exactly what Prune would remove, and looking must never become touching.
// The world is pruneWorld, deliberately: one labelled machine next to two
// foreign ones, one labelled network next to the host's bridge, two owned
// rule sets next to an operator's.

func TestSurveyFindsOnlyTheEmulatorsWorkAndTouchesNothing(t *testing.T) {
	f := &fakeRuntime{answers: pruneWorld()}
	d := newFakeDriver(f)

	left, err := d.Survey(context.Background())
	if err != nil {
		t.Fatalf("survey: %v", err)
	}

	if len(left.Machines) != 1 || left.Machines[0] != "feint-scw-1" {
		t.Errorf("machines = %v, want exactly [feint-scw-1]: a foreign name in this list ends up in a log line that sends someone to `feint clean`", left.Machines)
	}
	if len(left.Networks) != 1 || left.Networks[0] != "scw-aaaa" {
		t.Errorf("networks = %v, want exactly [scw-aaaa]", left.Networks)
	}
	if len(left.Firewalls) != 2 {
		t.Errorf("rule sets = %v, want the described one and the isolation one", left.Firewalls)
	}
	if left.Total() != 4 {
		t.Errorf("total = %d, want 4", left.Total())
	}

	// Naming is the whole contract. A survey that stops, deletes or unsets
	// anything is a sweep wearing the wrong name, and it would run at every
	// startup.
	for _, cmd := range f.commands() {
		if !strings.HasPrefix(cmd, "query ") {
			t.Errorf("survey issued a non-read command: %q", cmd)
		}
	}
}

func TestSurveyReportsAnUnreachableRuntime(t *testing.T) {
	f := &fakeRuntime{fail: map[string]error{
		"/1.0/instances?recursion=1": context.DeadlineExceeded,
	}}
	d := newFakeDriver(f)

	if _, err := d.Survey(context.Background()); err == nil {
		t.Fatal("a listing that failed must surface, not read as an empty runtime")
	}
}
