package machine

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The exit half of #521. Two green conformance runs each left `feint-uplink`
// standing: every resource had been deleted by the client that made it, the
// closing `feint stop` pruned nothing, and the next run's own doorstep refused
// the host on exactly that network. The release below is the driver's part of
// "a run ends on the host state its own doorstep accepts" — and its refusing
// halves matter as much, because an exit that deleted an uplink it does not
// hold would be the #455 family of reach one object up.

// TestAShutdownReleaseTakesTheUnusedUplinkOfThisProcess is the accepting half:
// labelled, held by this pid, nothing drawing from it — it goes.
func TestAShutdownReleaseTakesTheUnusedUplinkOfThisProcess(t *testing.T) {
	self := strconv.Itoa(os.Getpid())
	f := &fakeRuntime{answers: map[string]string{
		"query /1.0/networks/feint-uplink": ourUplinkJSON(self, ""),
	}}
	d := newFakeDriver(f)
	d.OVN = true

	released, err := d.releaseUplink(context.Background())
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if !released {
		t.Fatal("the unused uplink of this process was left standing; the next run's doorstep refuses exactly that (#521)")
	}
	if deletes := f.matching("network delete feint-uplink"); len(deletes) != 1 {
		t.Fatalf("expected exactly one delete of the uplink, got:\n%s", strings.Join(f.commands(), "\n"))
	}
}

// TestAReleaseLeavesAnUplinkStillInUse: networks still drawing from the uplink
// are this run's leftovers, and the doorstep must keep naming them — a forced
// teardown here would hide a leaking client behind the exit.
func TestAReleaseLeavesAnUplinkStillInUse(t *testing.T) {
	self := strconv.Itoa(os.Getpid())
	f := &fakeRuntime{answers: map[string]string{
		"query /1.0/networks/feint-uplink": ourUplinkJSON(self, "10.70.1.0/24"),
	}, fail: map[string]error{
		"network delete feint-uplink": errors.New(`Network "feint-uplink" is currently in use`),
	}}
	d := newFakeDriver(f)
	d.OVN = true

	released, err := d.releaseUplink(context.Background())
	if err != nil {
		t.Fatalf("an uplink still in use is the outcome asked for, not an error: %v", err)
	}
	if released {
		t.Fatal("the release claims an uplink the runtime refused to delete")
	}
}

// TestAReleaseNeverTouchesAnUplinkThisProcessDoesNotHold is the ownership
// half: a labelled uplink held by another pid is another run's plumbing, and
// an unlabelled bridge under the uplink's name is the operator's. Neither may
// see a delete, or any mutating command at all.
func TestAReleaseNeverTouchesAnUplinkThisProcessDoesNotHold(t *testing.T) {
	self := strconv.Itoa(os.Getpid())
	for name, uplink := range map[string]string{
		"held by another pid": ourUplinkJSON("31337", ""),
		"operator's bridge":   `{"type":"bridge","config":{}}`,
		"not a bridge":        `{"type":"ovn","config":{"user.` + LabelKey + `":"feint"}}`,
		// A holder claim without the label: the pid is a value anybody can
		// write, so it must never outrank the ownership mark — the same order
		// mustOwn keeps one layer down.
		"unlabelled but claiming this pid": `{"type":"bridge","config":{"user.` + UplinkHolderKey + `":"` + self + `"}}`,
	} {
		f := &fakeRuntime{answers: map[string]string{
			"query /1.0/networks/feint-uplink": uplink,
		}}
		d := newFakeDriver(f)
		d.OVN = true

		released, err := d.releaseUplink(context.Background())
		if err != nil {
			t.Fatalf("%s: release: %v", name, err)
		}
		if released {
			t.Fatalf("%s: the release claims an uplink that is not this process's", name)
		}
		if deletes := f.matching("network delete"); len(deletes) != 0 {
			t.Fatalf("%s: a delete was issued against an uplink this process does not hold:\n%s",
				name, strings.Join(f.commands(), "\n"))
		}
	}
}

// TestAReleaseDoesNothingOffOVN: the other modes have no uplink, and their
// exit must not grow one question about it.
func TestAReleaseDoesNothingOffOVN(t *testing.T) {
	f := &fakeRuntime{}
	d := newFakeDriver(f)

	released, err := d.releaseUplink(context.Background())
	if err != nil || released {
		t.Fatalf("released=%v err=%v off OVN, want false and nil", released, err)
	}
	if len(f.commands()) != 0 {
		t.Fatalf("bridge mode asked the runtime about an uplink it does not have:\n%s",
			strings.Join(f.commands(), "\n"))
	}
}

// TestAReleaseTreatsAMissingUplinkAsAlreadyGone: the sweep may have run first
// (`serve --cleanup` prunes the uplink itself), and absence is then the state
// this release exists to reach.
func TestAReleaseTreatsAMissingUplinkAsAlreadyGone(t *testing.T) {
	f := &fakeRuntime{fail: map[string]error{
		"query /1.0/networks/feint-uplink": errors.New("Network not found"),
	}}
	d := newFakeDriver(f)
	d.OVN = true

	released, err := d.releaseUplink(context.Background())
	if err != nil {
		t.Fatalf("a missing uplink is the outcome asked for, not an error: %v", err)
	}
	if released {
		t.Fatal("the release claims an uplink that was never there")
	}
}
