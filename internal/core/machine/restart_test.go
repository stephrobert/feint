package machine

import (
	"bytes"
	"context"
	"log/slog"
	"net/netip"
	"strings"
	"testing"
)

// A reboot needs no table of expected values (#669): the shape after is the
// shape before, and the observing recorder is the instrument — the test
// writes both shapes, so what the comparison had to find is exact.

// routedBeside is the routed shape beside a private NIC (shapeA) with the
// default route as given: the shape of 2026-09-04 before and after.
func routedBeside(defaultVia, defaultDev string) Shape {
	s := shapeA()
	s.Routes[0] = Route{Dst: defaultDst, Via: netip.MustParseAddr(defaultVia), Dev: defaultDev}
	return s
}

// restartPlan is the plan both shapes satisfy: the pinned private address
// and the public /32 the routed NIC carries.
var restartPlan = Plan{
	Boot:    []Attachment{{Network: "fnt-x", Address: "10.30.1.10", PrefixLen: 24}},
	Publics: []string{"203.0.113.2"},
}

// TestARebootComparesTheShapeAfterWithTheShapeBefore is the regression, seen
// at the moment of the reboot: the machine came back with its default route
// through the private gateway where it had left through the routed next hop.
// The accepting half runs first on the same runtime, so the finding cannot be
// a comparison that refuses everything.
func TestARebootComparesTheShapeAfterWithTheShapeBefore(t *testing.T) {
	before := routedBeside("169.254.0.1", "eth0")
	after := routedBeside("10.77.0.1", "eth1")

	b := newGroupSyncBench()
	o := &observingRecorder{Recorder: b.rec, queue: []Shape{before, before}}
	vm := b.machine("m", "10.30.1.10")
	vm.State = "running"
	r := observingReconciler(b, o, restartPlan)
	if !r.Reboot(context.Background(), vm, Boot{Image: "ubuntu:24.04"}) {
		t.Fatalf("the reboot did not bring the machine back; sequence: %v", b.rec.Sequence())
	}
	if o.reads != 2 || vm.Runtime[VerifiedKey] != "held" {
		t.Fatalf("a reboot that changed nothing: reads=%d verified=%q", o.reads, vm.Runtime[VerifiedKey])
	}

	// The same reboot, and the machine comes back by another door. Queued
	// twice after the before, because a broken reading is read once more
	// after one replay, and the machine stays wrong.
	o.queue = []Shape{before, after, after}
	o.reads = 0
	r.Reboot(context.Background(), vm, Boot{Image: "ubuntu:24.04"})
	if o.reads != 3 {
		t.Fatalf("reads=%d, want the before, the after, and one more after the repair", o.reads)
	}
	want := "restart(default route) want via 169.254.0.1 dev eth0, got via 10.77.0.1 dev eth1"
	got := vm.Runtime[VerifiedKey]
	if !strings.HasPrefix(got, "broken: ") || !strings.Contains(got, want) {
		t.Fatalf("Runtime[%s] = %q, want it broken on %q", VerifiedKey, got, want)
	}
	// And the plan's own claims still held: the address is carried, the
	// public /32 is carried; only the restart claims moved.
	if strings.Contains(got, "carries(") {
		t.Errorf("a claim the machine still satisfies was reported broken: %s", got)
	}
	if vm.State != "running" {
		t.Errorf("state %q after a reboot the runtime honoured, want running", vm.State)
	}
}

// A before that could not be read is said, and the reboot is judged on its
// plan alone: no restart claim is invented from a shape nobody saw.
func TestARebootWhoseBeforeCouldNotBeReadIsJudgedOnItsPlanAlone(t *testing.T) {
	var log bytes.Buffer
	b := newGroupSyncBench()
	o := &observingRecorder{Recorder: b.rec, shape: routedBeside("169.254.0.1", "eth0"), failFirst: 1}
	vm := b.machine("m", "10.30.1.10")
	vm.State = "running"
	r := observingReconciler(b, o, restartPlan)
	r.Groups.Binding.Log = slog.New(slog.NewTextHandler(&log, nil))
	r.Reboot(context.Background(), vm, Boot{Image: "ubuntu:24.04"})
	if got := vm.Runtime[VerifiedKey]; got != "held" {
		t.Fatalf("Runtime[%s] = %q, want held on the plan alone", VerifiedKey, got)
	}
	if !strings.Contains(log.String(), "judged on its plan alone") || !strings.Contains(log.String(), "level=WARN") {
		t.Errorf("an unreadable before was not said:\n%s", log.String())
	}
}

// A machine that was not up has no before: nothing is read until the boot's
// own reading, and nothing is claimed about a shape that did not exist.
func TestARebootOfAStoppedMachineHasNoBefore(t *testing.T) {
	b := newGroupSyncBench()
	o := &observingRecorder{Recorder: b.rec, shape: routedBeside("169.254.0.1", "eth0")}
	vm := b.machine("m", "10.30.1.10")
	vm.State = "stopped"
	r := observingReconciler(b, o, restartPlan)
	r.Reboot(context.Background(), vm, Boot{Image: "ubuntu:24.04"})
	if o.reads != 1 || vm.Runtime[VerifiedKey] != "held" {
		t.Fatalf("a reboot of a stopped machine: reads=%d verified=%q", o.reads, vm.Runtime[VerifiedKey])
	}
}

// TestTheRestartClaimsCompareWhatCompares: the three claims derived from a
// before, each on hand-built shapes — sets rather than lists for addresses,
// rule sets and routes; a lost interface, a lost route and a gained route
// each named as themselves.
func TestTheRestartClaimsCompareWhatCompares(t *testing.T) {
	before := routedBeside("169.254.0.1", "eth0")
	claims := restartClaims(before)
	if got := claimNames(claims); strings.Join(got, ",") != "restart(eth0),restart(eth1),restart(default route),restart(routes)" {
		t.Fatalf("claims %v", got)
	}
	for _, c := range claims {
		requireOutcome(t, c.Check(before), Held)
	}

	// The same addresses in another order, and the same rule sets in another
	// order, are the same interface.
	shuffled := routedBeside("169.254.0.1", "eth0")
	eth1 := shuffled.Interfaces["eth1"]
	eth1.Addresses = []netip.Prefix{netip.MustParsePrefix("10.30.1.10/24")}
	eth1.RuleSets = []string{"fnt-b", "fnt-a"}
	shuffled.Interfaces["eth1"] = eth1
	sets := before
	sets.Interfaces["eth1"] = Interface{Network: "fnt-x", Addresses: eth1.Addresses, RuleSets: []string{"fnt-a", "fnt-b"}}
	requireOutcome(t, restartClaims(sets)[1].Check(shuffled), Held)

	// An address gone from eth1.
	bare := routedBeside("169.254.0.1", "eth0")
	bare.Interfaces["eth1"] = Interface{Network: "fnt-x"}
	v := claims[1].Check(bare)
	requireOutcome(t, v, Broken)
	if v.Want != "fnt-x: 10.30.1.10/24; no rule set" || v.Got != "fnt-x: nothing; no rule set" {
		t.Errorf("a lost address: %s", v)
	}
	// An interface gone altogether.
	delete(bare.Interfaces, "eth0")
	v = claims[0].Check(bare)
	requireOutcome(t, v, Broken)
	if v.Got != "no interface eth0" {
		t.Errorf("a lost interface: %s", v)
	}
	// The default route by another door on the same device: the half the
	// regression lived in, and the half a comparison on the device alone
	// would miss.
	viaOnly := routedBeside("10.77.0.1", "eth0")
	v = claims[2].Check(viaOnly)
	requireOutcome(t, v, Broken)
	if v.Want != "via 169.254.0.1 dev eth0" || v.Got != "via 10.77.0.1 dev eth0" {
		t.Errorf("a default route by another door: %s", v)
	}
	// A default route gone, and one gained.
	none := routedBeside("169.254.0.1", "eth0")
	none.Routes = none.Routes[1:]
	v = claims[2].Check(none)
	requireOutcome(t, v, Broken)
	if v.Got != "no default route" {
		t.Errorf("a lost default route: %s", v)
	}
	requireOutcome(t, restartClaims(none)[2].Check(before), Broken)
	// A private route lost, and one gained.
	fewer := routedBeside("169.254.0.1", "eth0")
	fewer.Routes = fewer.Routes[:3]
	v = claims[3].Check(fewer)
	requireOutcome(t, v, Broken)
	if !strings.Contains(v.Want, "192.168.0.0/16 via 10.30.1.1 dev eth1") || strings.Contains(v.Got, "192.168.0.0/16") {
		t.Errorf("a lost route is not named: %s", v)
	}
	more := routedBeside("169.254.0.1", "eth0")
	more.Routes = append(more.Routes, Route{Dst: netip.MustParsePrefix("100.64.0.0/10"), Via: netip.MustParseAddr("10.30.1.1"), Dev: "eth1"})
	v = claims[3].Check(more)
	requireOutcome(t, v, Broken)
	if !strings.Contains(v.Got, "100.64.0.0/10 via 10.30.1.1 dev eth1") {
		t.Errorf("a gained route is not named: %s", v)
	}
	// An empty table before and after compares equal, and says so.
	empty := Shape{}
	requireOutcome(t, restartClaims(empty)[0].Check(empty), Held)
	requireOutcome(t, restartClaims(empty)[1].Check(empty), Held)
}
