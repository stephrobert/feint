package machine

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/stephrobert/feint/internal/core/resource"
)

// A verdict nobody reads is a confession nobody hears (#670). The reading half
// (#668) answers every claim a plan makes; this file is where the answer goes:
// a log line where a failure belongs, a key on the resource out of the
// client's reach, four counters on /_feint/health, and one bounded retry for
// the class of divergence a replay can mend.
//
// What the API state must NOT become. The rule this repository holds is that
// the published state is the one the effect produced, not the one the
// intention aimed at. A machine running with the wrong door is running and
// unreachable; publishing it stopped would be the lie in the other direction,
// the one incus.go already refuses twice. So the state stays true, and the
// verdict goes everywhere else.

// VerifiedKey is the Runtime key the last verification's summary is published
// under. Runtime is never serialised into a client's view, and /_feint/state
// shows it to whoever asks, which is how the runtime leg names a broken claim
// rather than a counter.
//
// The value is one of "held", "broken: <claim> want <x>, got <y>; …" and
// "unreadable: <claim>: <why>; …" — a prefix a reader can branch on, then the
// verdicts themselves.
const VerifiedKey = "verified"

// Verification is the count of what the reading half answered, published on
// /_feint/health. Held, Broken and Unreadable count claims; Repaired counts
// verifications whose first reading was broken and whose second, after one
// replay, held.
//
// Repaired is a counter for a reason: a repair that fires on every boot is a
// defect hidden behind a retry, and the number is what makes it visible.
type Verification struct {
	Held       int64 `json:"held"`
	Broken     int64 `json:"broken"`
	Unreadable int64 `json:"unreadable"`
	Repaired   int64 `json:"repaired"`
}

// tally is the process-wide count behind Verification. It lives on the
// Runtime handle — one per Use, shared by every Binding that handle is bound
// into — rather than in a package variable, so two emulators in one test
// binary count apart and a test can read exactly what its own runtime saw.
type tally struct {
	held, broken, unreadable, repaired atomic.Int64
}

// count adds one verification's verdicts. Nil-safe: a Binding built without a
// Runtime, which every bench in this package is, counts nowhere.
func (t *tally) count(verdicts []Verdict, repaired bool) {
	if t == nil {
		return
	}
	for _, v := range verdicts {
		switch v.Outcome {
		case Held:
			t.held.Add(1)
		case Broken:
			t.broken.Add(1)
		case Unreadable:
			t.unreadable.Add(1)
		}
	}
	if repaired {
		t.repaired.Add(1)
	}
}

func (t *tally) snapshot() Verification {
	if t == nil {
		return Verification{}
	}
	return Verification{
		Held:       t.held.Load(),
		Broken:     t.broken.Load(),
		Unreadable: t.unreadable.Load(),
		Repaired:   t.repaired.Load(),
	}
}

// Verification answers what this runtime's verifications counted so far.
func (r Runtime) Verification() Verification { return r.tally.snapshot() }

// summary renders one verification for VerifiedKey: the broken claims first,
// because a reader branches on the prefix; the unreadable ones when nothing
// is broken; "held" when every claim held.
func summary(verdicts []Verdict) string {
	var broken, unreadable []string
	for _, v := range verdicts {
		switch v.Outcome {
		case Broken:
			broken = append(broken, v.Claim+" want "+v.Want+", got "+v.Got)
		case Unreadable:
			unreadable = append(unreadable, v.Claim+": "+v.Got)
		}
	}
	switch {
	case len(broken) > 0:
		return "broken: " + strings.Join(broken, "; ")
	case len(unreadable) > 0:
		return "unreadable: " + strings.Join(unreadable, "; ")
	}
	return "held"
}

func anyBroken(verdicts []Verdict) bool {
	for _, v := range verdicts {
		if v.Outcome == Broken {
			return true
		}
	}
	return false
}

// publish is where a verification's answer goes: the log, the resource, the
// counters. It changes no state — see the file comment — and it is written
// inside the change() closure of Transition, so the key lands through Commit
// with everything else and merges per key with a concurrent writer to another
// field (mergeRuntime). A resource deleted meanwhile makes Commit answer
// false and the verdict falls with the rest, which is correct.
//
// ERROR for a broken claim and WARN for an unreadable one, in the words the
// repository holds for the two levels: a failure, and a degradation. The
// repair, when one happened, is a WARN naming what the first reading found:
// it ended well, and the number of times it was needed is the point.
//
// TestABrokenVerdictReachesTheResourceAndNotItsState and
// TestABrokenVerdictIsLoggedAsAFailure fail without this.
func (r Reconciler) publish(res *resource.Resource, verdicts []Verdict, repairedFrom []Verdict) {
	if len(verdicts) == 0 {
		return
	}
	b := r.binding()
	machine := res.Runtime[b.RuntimeKey]
	var unreadable []Verdict
	for _, v := range verdicts {
		switch v.Outcome {
		case Broken:
			b.logger().Error("the machine does not carry what its plan claims",
				"provider", b.Provider, "resource", res.ID, "machine", machine,
				"claim", v.Claim, "want", v.Want, "got", v.Got)
		case Unreadable:
			unreadable = append(unreadable, v)
		}
	}
	if len(unreadable) > 0 {
		b.logger().Warn("part of the machine's shape could not be read, so those claims are neither held nor broken",
			"provider", b.Provider, "resource", res.ID, "machine", machine,
			"claims", len(unreadable), "first", unreadable[0].Claim, "why", unreadable[0].Got)
	}
	if len(repairedFrom) > 0 {
		b.logger().Warn("the machine did not carry what its plan claims until the replay ran a second time",
			"provider", b.Provider, "resource", res.ID, "machine", machine,
			"first_reading", summary(repairedFrom))
	}
	if res.Runtime == nil {
		res.Runtime = map[string]string{}
	}
	res.Runtime[VerifiedKey] = summary(verdicts)
	b.tally.count(verdicts, len(repairedFrom) > 0)
}

// check verifies the machine behind a resource and publishes the answer,
// with one bounded retry for the class a replay can mend.
//
// A broken first reading replays the post-boot order once — every step of it
// is idempotent by construction — and reads again; what is published is the
// second reading, with the first named beside it when the second held. Once,
// and never for an unreadable reading: a virtual machine whose agent has not
// answered is read again from the late-address door, and replaying onto it
// would not make it readable. The contradiction class never reaches here, it
// is refused at derivation (#667), and replaying it would replay the same
// contradiction.
//
// TestARepairIsAttemptedOnceAndCounted fails without the bound.
func (r Reconciler) check(ctx context.Context, res *resource.Resource, extra []Claim) {
	verdicts, asked := r.verify(ctx, res, extra)
	if !asked {
		return
	}
	if !anyBroken(verdicts) {
		r.publish(res, verdicts, nil)
		return
	}
	plan, declared := r.plan(res)
	if !declared {
		r.publish(res, verdicts, nil)
		return
	}
	r.replay(ctx, res, plan)
	again, _ := r.verify(ctx, res, extra)
	if anyBroken(again) {
		r.publish(res, again, nil)
		return
	}
	r.publish(res, again, verdicts)
}
