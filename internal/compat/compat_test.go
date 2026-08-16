package compat

import (
	"strings"
	"testing"
)

// The three buckets, and the boundary between the last two is the whole point.
//
// An expression that errors is not interesting: the consumer sees it. What #170
// is about is an expression that runs, answers, and answers something false —
// and whether the payload gave the consumer any way to know.

func expr(before, after string) Expression {
	return Expression{
		Name:    "probed count",
		Surface: "conformance",
		Source:  `[.evidence[] | select(.probed == true)] | length`,
		Means:   "the operations the protocol probe drove",
		Before:  before,
		After:   after,
	}
}

func TestTheSameAnswerIsCompatible(t *testing.T) {
	got := Classify(expr("172", "172"), Versions{Before: "3", After: "3", BeforeKnown: true, AfterKnown: true})
	if got.Verdict != Compatible {
		t.Fatalf("verdict is %q, want compatible: %s", got.Verdict, got.Why)
	}
}

// A changed answer with a moved version is the policy working: the consumer can
// see the boundary before it misreads anything.
func TestAChangedAnswerWithAMovedVersionIsExplicitlyBroken(t *testing.T) {
	got := Classify(expr("172", "0"), Versions{Before: "2", After: "3", BeforeKnown: true, AfterKnown: true})
	if got.Verdict != ExplicitlyBroken {
		t.Fatalf("verdict is %q, want explicitly broken: %s", got.Verdict, got.Why)
	}
	if !strings.Contains(got.Why, "schema_version") {
		t.Errorf("the finding does not tell the reader what to key on: %q", got.Why)
	}
}

// The one that fails a release: the answer changed and the version did not.
func TestAChangedAnswerWithAStillVersionIsSilentlyWrong(t *testing.T) {
	got := Classify(expr("172", "0"), Versions{Before: "3", After: "3", BeforeKnown: true, AfterKnown: true})
	if got.Verdict != SilentlyWrong {
		t.Fatalf("verdict is %q, want silently wrong: %s", got.Verdict, got.Why)
	}
	if !strings.Contains(got.Why, "still runs") {
		t.Errorf("the finding does not say what makes it silent: %q", got.Why)
	}
}

// The 0.8 boundary, and the reading that decides it.
//
// A release that carried no schema_version gave its consumers nothing to key on,
// so a changed answer across that boundary is silently wrong however carefully
// the current release versions itself. The opposite reading — treating "absent"
// as "different, therefore detectable" — would let every release before the
// policy claim the policy's protection retroactively, which is exactly the kind
// of comfortable answer this repository exists to refuse.
func TestAnAbsentVersionOnTheOlderSideIsNoSignalAtAll(t *testing.T) {
	got := Classify(expr("172", "0"), Versions{After: "3", AfterKnown: true})
	if got.Verdict != SilentlyWrong {
		t.Fatalf("verdict is %q, want silently wrong: %s", got.Verdict, got.Why)
	}
	if !strings.Contains(got.Why, "carried no schema_version") {
		t.Errorf("the finding does not name why the consumer was defenceless: %q", got.Why)
	}
}

// And a version that disappears is a signal, because a consumer that checked it
// stops finding it.
func TestAVanishedVersionIsDetectable(t *testing.T) {
	got := Classify(expr("172", "0"), Versions{Before: "3", BeforeKnown: true})
	if got.Verdict != ExplicitlyBroken {
		t.Fatalf("verdict is %q, want explicitly broken: %s", got.Verdict, got.Why)
	}
}

// The gate refuses a silently-wrong finding and lets an accepted one through,
// both halves asserted: a gate that refuses everything is as useless as one that
// refuses nothing.
func TestTheGateFailsOnSilentlyWrongUnlessItIsAccepted(t *testing.T) {
	findings := []Finding{
		{Expression: Expression{Name: "probed count"}, Verdict: SilentlyWrong},
		{Expression: Expression{Name: "route count"}, Verdict: SilentlyWrong},
		{Expression: Expression{Name: "health state"}, Verdict: Compatible},
	}

	fail, stale := Gate(findings, nil)
	if len(fail) != 2 {
		t.Fatalf("with nothing accepted the gate reports %d finding(s), want 2", len(fail))
	}
	if len(stale) != 0 {
		t.Errorf("an empty acceptance list produced stale entries: %v", stale)
	}

	fail, stale = Gate(findings, []Accepted{
		{Name: "probed count", Reason: "0.8 shipped before schema_version existed"},
		{Name: "route count", Reason: "same boundary"},
	})
	if len(fail) != 0 {
		t.Errorf("the gate still fails on accepted findings: %v", fail)
	}
	if len(stale) != 0 {
		t.Errorf("entries that matched were reported stale: %v", stale)
	}
}

// A stale exemption is reported, because an acceptance that no longer matches
// anything is a gate quietly ceasing to cover what it names — the same defect as
// a frozen fixture nobody regenerates.
func TestAnAcceptanceThatMatchesNothingIsReported(t *testing.T) {
	findings := []Finding{{Expression: Expression{Name: "probed count"}, Verdict: Compatible}}
	_, stale := Gate(findings, []Accepted{{Name: "probed count", Reason: "fixed since"}})
	if len(stale) != 1 || stale[0] != "probed count" {
		t.Fatalf("a stale acceptance was not reported: %v", stale)
	}
}
