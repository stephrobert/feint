package emulator

import "testing"

// The rank a `nth` rule fires on is the rank of the call, which is not the
// rule's hit count.
//
// Before this, the only counter a rule carried was `hits`, incremented after
// the decision and only on the path that fires. A rule that declined a call
// counted nothing, so "the third call" was unreachable: by the time the third
// call arrived, the counter still read zero. `o.calls[operation]` could not
// serve either — it is incremented in `o.record`, which the faulted path
// returns before reaching.
//
// So the rank counts every call the rule saw, before the decision. That
// ordering is the whole fix, and it is what this test holds.
func TestTheRankCountsCallsTheRuleDeclines(t *testing.T) {
	fs := &faultSet{rules: []*Fault{{Operation: "a/API.One", Status: 503, Nth: 3}}}

	var ranks []int
	var firedOn []int
	for range 5 {
		_, rank, _, claimed := fs.fire("a/API.One")
		ranks = append(ranks, rank)
		if claimed {
			firedOn = append(firedOn, rank)
		}
	}

	want := []int{1, 2, 3, 4, 5}
	for i, got := range ranks {
		if got != want[i] {
			t.Fatalf("call %d reported rank %d, want %d — a declined call was not counted, so "+
				"`nth` can never name a call after the first", i+1, got, want[i])
		}
	}
	if len(firedOn) != 1 || firedOn[0] != 3 {
		t.Errorf("the rule fired on calls %v, want exactly [3]: `nth 3` names the third call, not "+
			"the third firing", firedOn)
	}
}

// The accepting half, which a guard that refuses everything would also pass:
// a rule with no `nth` still answers every call, and `times` still bounds it.
func TestARuleWithoutARankStillAnswersEveryCall(t *testing.T) {
	fs := &faultSet{rules: []*Fault{{Operation: "a/API.One", Status: 503}}}
	for i := range 4 {
		if _, _, _, claimed := fs.fire("a/API.One"); !claimed {
			t.Fatalf("call %d was not claimed; a rule with no nth and no times answers every call", i+1)
		}
	}

	bounded := &faultSet{rules: []*Fault{{Operation: "a/API.Two", Status: 503, Times: 2}}}
	var claims int
	for range 4 {
		if _, _, _, claimed := bounded.fire("a/API.Two"); claimed {
			claims++
		}
	}
	if claims != 2 {
		t.Errorf("a times-2 rule fired %d times, want 2: adding the rank must not change what times "+
			"means", claims)
	}
}

// An operation no rule targets reports rank zero, which is how a caller tells
// "nothing is armed here" from "this is the first call".
func TestAnUnarmedOperationHasNoRank(t *testing.T) {
	fs := &faultSet{rules: []*Fault{{Operation: "a/API.One", Status: 503}}}
	if _, rank, _, claimed := fs.fire("a/API.Other"); claimed || rank != 0 {
		t.Errorf("an unarmed operation reported rank %d claimed=%v, want 0 and false", rank, claimed)
	}
}

// The receipt says which call was hit, which the counters cannot.
//
// "hits: 2" is true of a rule that answered calls 1 and 2 and of one that
// answered calls 3 and 7. A test asserting a schedule needs to tell them apart,
// and that is what #851 asks the receipt for.
func TestTheReceiptNamesTheCallThatWasHit(t *testing.T) {
	fs := &faultSet{rules: []*Fault{{Operation: "a/API.One", Status: 503, Nth: 3}}}
	for range 4 {
		if _, _, slot, claimed := fs.fire("a/API.One"); claimed {
			fs.recordFired(slot, 503, false)
		}
	}

	got, _ := fs.receipt()
	if len(got) != 1 {
		t.Fatalf("the receipt holds %d line(s), want 1: %+v", len(got), got)
	}
	if got[0].Operation != "a/API.One" || got[0].Rank != 3 || got[0].Status != 503 {
		t.Errorf("the receipt says %+v, want operation a/API.One at rank 3 with status 503", got[0])
	}
}

// Arming a new set clears the receipt with it: a line from a rule that is no
// longer armed, read as part of this campaign, is the mixed population this
// repository keeps paying for.
func TestArmingANewSetClearsTheReceipt(t *testing.T) {
	fs := &faultSet{rules: []*Fault{{Operation: "a/API.One", Status: 503}}}
	_, _, slot, _ := fs.fire("a/API.One")
	fs.recordFired(slot, 503, false)
	if lines, _ := fs.receipt(); len(lines) != 1 {
		t.Fatal("nothing was recorded, so the rest of this test would prove nothing")
	}

	fs.replace([]*Fault{{Operation: "a/API.Two", Status: 429}})
	if got, _ := fs.receipt(); len(got) != 0 {
		t.Errorf("the receipt survived a new campaign with %d line(s): %+v", len(got), got)
	}
}

// The receipt is bounded, and a reader can see that it is partial rather than
// believe it complete.
func TestTheReceiptStopsGrowing(t *testing.T) {
	fs := &faultSet{}
	fs.rules = []*Fault{{Operation: "a/API.One", Status: 503}}
	for i := 0; i < maxFiredReceipt+10; i++ {
		_, _, slot, _ := fs.fire("a/API.One")
		fs.recordFired(slot, 503, false)
	}
	lines, dropped := fs.receipt()
	if dropped != 10 {
		t.Errorf("the receipt dropped %d firing(s), want 10: a reader cannot tell a full receipt "+
			"from a truncated one without it", dropped)
	}
	if got := len(lines); got != maxFiredReceipt {
		t.Errorf("the receipt holds %d lines, want it capped at %d", got, maxFiredReceipt)
	}
}
