package store

import (
	"bytes"
	"strings"
	"testing"
)

// The ledger is what keeps a mutated run from being read as a measurement
// (#193). Each property below is one way it could be lost.

// A snapshot of a mutated store carries its history into whatever loads it.
//
// This is the boundary built to cross instances — snapshot.go says the format
// is made to outlive its instance — so it is the one where a mark that lived
// beside the state instead of inside it would evaporate.
func TestAMutatedStateCarriesItsLedgerAcrossASnapshot(t *testing.T) {
	src := &Store{}
	src.RecordMutation("2026-11-13T09:00:00Z set instance/server abc tags=[]")

	var buf bytes.Buffer
	if err := src.Snapshot(&buf); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !strings.Contains(buf.String(), "tags=[]") {
		t.Fatalf("the snapshot carries no ledger:\n%s", buf.String())
	}

	dst := &Store{}
	if err := dst.Restore(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := dst.Mutations(); len(got) != 1 {
		t.Fatalf("the receiving store reports %d mutations, want 1: %v", len(got), got)
	}
}

// A clean snapshot loaded over a mutated store restores clean resources and
// does not restore a clean history.
//
// Without it, the way to launder a run would be one command long, and nothing
// would say it had happened.
func TestACleanSnapshotDoesNotLaunderAMutatedRun(t *testing.T) {
	s := &Store{}
	s.RecordMutation("2026-11-13T09:00:00Z unset instance/server abc tags")

	clean := `{"format":"feint-snapshot","version":1,"resources":[]}`
	if err := s.Restore(strings.NewReader(clean)); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := s.Mutations(); len(got) != 1 {
		t.Fatalf("the mark did not survive a clean load: %v", got)
	}
	if s.Len() != 0 {
		t.Fatalf("the clean load left %d resources", s.Len())
	}
}

// A read-modify-write is the shape every mutation has: GET the state, change
// one attribute, PUT it back. The ledger that comes back therefore already
// contains the store's own history, and a blind append would duplicate every
// earlier line on every mutation — a ledger that grows quadratically and
// reports three mutations where there were two.
func TestAReadModifyWriteDoesNotDuplicateTheLedger(t *testing.T) {
	s := &Store{}
	s.RecordMutation("one")

	var buf bytes.Buffer
	if err := s.Snapshot(&buf); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	body := strings.Replace(buf.String(), `"one"`, `"one",
    "two"`, 1)
	if err := s.Restore(strings.NewReader(body)); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got := s.Mutations()
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("the ledger reads %v, want [one two]", got)
	}
}

// A restored ledger is untrusted input, like everything else that crossed a
// persistence boundary. These lines are printed in a terminal and served on
// /_feint/conformance, so what is refused is refused at the door.
func TestARestoredMutationLedgerIsValidated(t *testing.T) {
	cases := []struct {
		name string
		body string
		says string
	}{
		{
			name: "a control character is refused",
			body: `{"format":"feint-snapshot","version":1,"resources":[],` +
				`"mutations":["set a b\u001b[2Jc=1"]}`,
			says: "control character",
		},
		{
			name: "a record longer than the bound is refused",
			body: `{"format":"feint-snapshot","version":1,"resources":[],` +
				`"mutations":["` + strings.Repeat("x", maxMutationRecord+1) + `"]}`,
			says: "bound is",
		},
		{
			name: "more records than the store keeps is refused",
			body: `{"format":"feint-snapshot","version":1,"resources":[],` +
				`"mutations":[` + strings.TrimSuffix(strings.Repeat(`"x",`, maxMutationRecords+1), ",") + `]}`,
			says: "at most",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Store{}
			err := s.Restore(strings.NewReader(tc.body))
			if err == nil {
				t.Fatalf("the ledger was accepted")
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal never says %q: %v", tc.says, err)
			}
			if got := s.Mutations(); len(got) != 0 {
				t.Errorf("a refused ledger was recorded anyway: %v", got)
			}
		})
	}
}
