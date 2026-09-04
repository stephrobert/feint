package machine

import (
	"context"
	"net/netip"
	"strings"
	"testing"
)

// The guard of #667: a plan two of whose steps claim the same property is
// refused before the runtime is asked for anything, and what a consistent plan
// claims is answered against a Shape with three outcomes.
//
// The Recorder is the instrument for the first half: with the guard removed it
// records Start, RouteAddress and the rest, and the assertion on an empty
// sequence bites. Hand-built Shapes are the instrument for the second: a
// comparator is a pure function of a claim and a Shape, so a Shape written by
// the test is the exact fixture, with nothing between it and the verdict.

// egressRecorder is the contract recorder with the egress half, which the
// recorder itself deliberately lacks: adding RouteEgress to its vocabulary
// would change the sequences the cross-pack equivalence compares. It also
// declares a Dialect that lays a default route, so the derivation claims one.
type egressRecorder struct {
	*Recorder
	laid []string
}

func (e *egressRecorder) RouteEgress(_ context.Context, machine, network string) error {
	e.laid = append(e.laid, "RouteEgress "+machine+" via "+network)
	return nil
}

func (e *egressRecorder) DropEgress(_ context.Context, machine string) error {
	e.laid = append(e.laid, "DropEgress "+machine)
	return nil
}

func (e *egressRecorder) Dialect() Dialect { return Dialect{LaysDefaultRoute: true} }

// egressReconciler is rebootReconciler over an egress-capable runtime.
func egressReconciler(b *groupSyncBench, e *egressRecorder, plan Plan) Reconciler {
	sync := b.sync()
	sync.Binding.driver = e
	sync.Binding.RunningState = "running"
	sync.Binding.FailedState = "stopped"
	return Reconciler{
		Groups:      sync,
		PlanOf:      func(*testResource) Plan { return plan },
		PublicBlock: netip.MustParsePrefix("203.0.113.0/24"),
	}
}

// deriver is a Reconciler with nothing but the block, which is all Expect
// reads of it.
func deriver() Reconciler {
	return Reconciler{PublicBlock: netip.MustParsePrefix("203.0.113.0/24")}
}

// ovnDialect is the OVN driver's dialect, restated so these tests do not
// depend on the Incus file: a routed next hop, the three aggregates, and a
// default route the driver lays.
func ovnDialect() Dialect {
	return Dialect{
		RoutedNextHop:    netip.MustParseAddr("169.254.0.1"),
		Aggregates:       privateAggregatePrefixes(),
		LaysDefaultRoute: true,
	}
}

// claimNames renders the claims the way a verdict cites them, for assertions.
func claimNames(claims []Claim) []string {
	out := make([]string, 0, len(claims))
	for _, c := range claims {
		out = append(out, c.String())
	}
	return out
}

// TestAPlanWithTwoClaimantsToTheDefaultRouteIsRefusedBeforeTheRuntimeIsAsked
// is the whole of #667 at the level where it can be held deterministically: a
// public address owns the default route, an egress network claims it too, and
// the runtime must never hear of the plan.
func TestAPlanWithTwoClaimantsToTheDefaultRouteIsRefusedBeforeTheRuntimeIsAsked(t *testing.T) {
	b := newGroupSyncBench()
	vm := b.machine("m", "")
	r := rebootReconciler(b, Plan{Publics: []string{"203.0.113.2"}, Egress: "fnt-x"})
	if r.PowerOn(context.Background(), vm, Boot{Image: "ubuntu:24.04"}) {
		t.Fatal("a plan that claims the default route twice was started")
	}
	if got := b.rec.Sequence(); len(got) != 0 {
		t.Fatalf("the runtime was asked before the plan was judged: %v", got)
	}
	// And the state is the pack's own failed one, the way a missing plan
	// publishes it (#543): a refused boot is not a running machine.
	if vm.State != "stopped" {
		t.Errorf("a refused plan left the state %q, want the pack's failed state", vm.State)
	}
}

// The accepting half, which matters as much: one claimant is not a
// contradiction, whichever of the three it is. A guard that refused
// everything would pass the test above and prove nothing.
func TestAPlanWithOneClaimantToTheDefaultRouteIsStarted(t *testing.T) {
	for name, plan := range map[string]Plan{
		"a public address": {Publics: []string{"203.0.113.2"}},
		"an egress":        {Egress: "fnt-x"},
		"a refusal":        {NoEgress: true},
		"silence":          {},
	} {
		t.Run(name, func(t *testing.T) {
			b := newGroupSyncBench()
			vm := b.machine("m", "")
			r := rebootReconciler(b, plan)
			if !r.PowerOn(context.Background(), vm, Boot{Image: "ubuntu:24.04"}) {
				t.Fatalf("a plan with %s alone was refused; sequence: %v", name, b.rec.Sequence())
			}
			if firstOf(b.rec.Sequence(), "Start") < 0 {
				t.Fatalf("the runtime was never asked to start the machine: %v", b.rec.Sequence())
			}
		})
	}
}

// TestAContradictoryPlanNeverReachesTheEgressRouter is the hot half. A machine
// already running cannot be refused a start; what is refused is the step that
// executes the contradiction. The accepting half runs first, on the same
// double, so an empty list cannot be a double that records nothing.
func TestAContradictoryPlanNeverReachesTheEgressRouter(t *testing.T) {
	b := newGroupSyncBench()
	e := &egressRecorder{Recorder: b.rec}
	vm := b.machine("m", "10.0.0.5")
	vm.State = "running"

	consistent := egressReconciler(b, e, Plan{Egress: "fnt-x"})
	consistent.ReplayAddresses(context.Background(), vm)
	if len(e.laid) != 1 || !strings.HasSuffix(e.laid[0], "via fnt-x") {
		t.Fatalf("the double did not record the egress a consistent plan lays: %v", e.laid)
	}
	e.laid = nil

	contradictory := egressReconciler(b, e, Plan{Publics: []string{"203.0.113.2"}, Egress: "fnt-x"})
	contradictory.ReplayAddresses(context.Background(), vm)
	if len(e.laid) != 0 {
		t.Fatalf("a contradictory plan reached the egress router, which is the overwrite #660 measured: %v", e.laid)
	}
	// The public address itself is still routed: the refusal is of the step
	// that contradicts, never of the machine's other claims.
	if firstOf(b.rec.Sequence(), "RouteAddress") < 0 {
		t.Fatalf("the public address was not routed on the hot path: %v", b.rec.Sequence())
	}
}

// The refusal names every claimant, so an operator reads which two steps
// disagree rather than which port stopped answering.
func TestARefusalNamesEveryClaimant(t *testing.T) {
	_, err := deriver().Expect(Plan{Publics: []string{"203.0.113.2"}, Egress: "fnt-x", NoEgress: true}, Dialect{})
	if err == nil {
		t.Fatal("three claimants to the default route were accepted")
	}
	for _, claimant := range []string{"203.0.113.2", "egress through fnt-x", "NoEgress"} {
		if !strings.Contains(err.Error(), claimant) {
			t.Errorf("the refusal does not name %q: %v", claimant, err)
		}
	}
}

// TestAPublicAddressAloneClaimsNoValueForTheDefaultRoute holds the silence
// #672 asks for: which door a reply leaves by is a measurement, one of the two
// shapes has none, and the derivation states no value it cannot know. The
// address still claims its /32 — that half is measured on every shape.
func TestAPublicAddressAloneClaimsNoValueForTheDefaultRoute(t *testing.T) {
	claims, err := deriver().Expect(Plan{Publics: []string{"203.0.113.2"}}, ovnDialect())
	if err != nil {
		t.Fatalf("a public address alone was refused: %v", err)
	}
	names := claimNames(claims)
	for _, name := range names {
		if strings.Contains(name, "default route") || strings.Contains(name, "door") {
			t.Errorf("the derivation states a value for the way out of a public address, which nobody measured: %v", names)
		}
	}
	if len(names) != 1 || names[0] != "carries(any interface, 203.0.113.2/32)" {
		t.Errorf("a public address claims exactly its /32 on some interface, got %v", names)
	}
}

// An egress or a refusal claims a value only where the driver lays one: under
// a bridge RouteEgress and DropEgress answer nil without touching the guest.
func TestTheWayOutIsClaimedOnlyWhereTheDriverLaysIt(t *testing.T) {
	bridge := Dialect{RoutedNextHop: netip.MustParseAddr("169.254.0.1")}
	for name, plan := range map[string]Plan{"egress": {Egress: "fnt-x"}, "refusal": {NoEgress: true}} {
		claims, err := deriver().Expect(plan, bridge)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(claims) != 0 {
			t.Errorf("%s under a bridge claims %v, where the driver lays nothing", name, claimNames(claims))
		}
		claims, err = deriver().Expect(plan, ovnDialect())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(claims) != 1 {
			t.Errorf("%s under OVN claims %v, want exactly the way out", name, claimNames(claims))
		}
	}
}

// One interface per network. The same network reached through Boot and
// Memberships is one interface asked for twice, and a plan listing the same
// attachment two times is a duplicate rather than a contradiction.
func TestANetworkAskedForTwiceIsRefusedAndADuplicateIsNot(t *testing.T) {
	both := Plan{
		Boot:        []Attachment{{Network: "fnt-x", Address: "10.0.0.2"}},
		Memberships: []Attachment{{Network: "fnt-x", Address: "10.0.0.3"}},
	}
	if _, err := deriver().Expect(both, Dialect{}); err == nil ||
		!strings.Contains(err.Error(), "the interface on fnt-x") {
		t.Fatalf("one network with two addresses was accepted: %v", err)
	}
	twice := Plan{Boot: []Attachment{{Network: "fnt-x", Address: "10.0.0.2"}, {Network: "fnt-x", Address: "10.0.0.2"}}}
	claims, err := deriver().Expect(twice, Dialect{})
	if err != nil {
		t.Fatalf("the same attachment listed twice was refused: %v", err)
	}
	if got := claimNames(claims); len(got) != 1 || got[0] != "carries(fnt-x, 10.0.0.2)" {
		t.Errorf("a duplicate attachment claims %v, want one interface", got)
	}
}

// A public address outside the pack's block is one the layer never routes, so
// it claims nothing — and in particular it does not own the default route.
func TestAPublicAddressOutsideTheBlockClaimsNothing(t *testing.T) {
	claims, err := deriver().Expect(Plan{Publics: []string{"198.51.100.7"}, Egress: "fnt-x"}, ovnDialect())
	if err != nil {
		t.Fatalf("an address the layer never routes was counted as a claimant: %v", err)
	}
	if got := claimNames(claims); len(got) != 1 || got[0] != "default route" {
		t.Errorf("claims %v, want the egress alone", got)
	}
}

// A stored address is untrusted input: one that is not an address refuses the
// plan here rather than reaching a device key.
func TestAnAttachmentThatIsNotAnAddressRefusesThePlan(t *testing.T) {
	_, err := deriver().Expect(Plan{Boot: []Attachment{{Network: "fnt-x", Address: "10.0.0.2; rm -rf /"}}}, Dialect{})
	if err == nil || !strings.Contains(err.Error(), "not an address") {
		t.Fatalf("a poisoned attachment address was derived into a claim: %v", err)
	}
}

// What a whole plan claims under OVN, so a claim added or dropped by accident
// moves this list.
func TestTheDerivationClaimsWhatThePlanSays(t *testing.T) {
	plan := Plan{
		Boot:        []Attachment{{Network: "fnt-a", Address: "10.0.0.2", PrefixLen: 24, Secondary: []string{"10.0.0.9"}}},
		Memberships: []Attachment{{Network: "fnt-b"}},
		Publics:     []string{"203.0.113.2", "203.0.113.2"},
	}
	claims, err := deriver().Expect(plan, ovnDialect())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"carries(fnt-a, 10.0.0.2/24)",
		"carries(fnt-a, 10.0.0.9/24)",
		"leases(fnt-b)",
		"carries(any interface, 203.0.113.2/32)",
		"private aggregates",
	}
	if got := claimNames(claims); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("claims:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// ---- the comparators, each against a Shape written by the test -------------

// shapeA is the routed shape beside a private NIC, as docs/limits.md records
// it: eth0 routed carrying the public /32, eth1 on fnt-x pinned at /24, the
// default route via the routed next hop, the aggregates via the gateway.
func shapeA() Shape {
	return Shape{
		Interfaces: map[string]Interface{
			"eth0": {Routed: true, Addresses: []netip.Prefix{netip.MustParsePrefix("203.0.113.2/32")}},
			"eth1": {Network: "fnt-x", Addresses: []netip.Prefix{netip.MustParsePrefix("10.30.1.10/24")}},
		},
		Routes: []Route{
			{Dst: defaultDst, Via: netip.MustParseAddr("169.254.0.1"), Dev: "eth0"},
			{Dst: netip.MustParsePrefix("10.0.0.0/8"), Via: netip.MustParseAddr("10.30.1.1"), Dev: "eth1"},
			{Dst: netip.MustParsePrefix("172.16.0.0/12"), Via: netip.MustParseAddr("10.30.1.1"), Dev: "eth1"},
			{Dst: netip.MustParsePrefix("192.168.0.0/16"), Via: netip.MustParseAddr("10.30.1.1"), Dev: "eth1"},
		},
		Gateways: map[string]netip.Prefix{"fnt-x": netip.MustParsePrefix("10.30.1.1/24")},
	}
}

func requireOutcome(t *testing.T, v Verdict, want Outcome) {
	t.Helper()
	if v.Outcome != want {
		t.Fatalf("%s: got %s, want %s", v, v.Outcome, want)
	}
}

// The mask is judged when the plan said one, and only then: /24 and /32 are
// not the same address to the kernel.
func TestCarriesJudgesTheMaskOnlyWhenThePlanSaidOne(t *testing.T) {
	s := shapeA()
	addr := netip.MustParseAddr("10.30.1.10")
	requireOutcome(t, carries{Network: "fnt-x", Address: addr, Bits: 24}.Check(s), Held)
	requireOutcome(t, carries{Network: "fnt-x", Address: addr}.Check(s), Held)
	v := carries{Network: "fnt-x", Address: addr, Bits: 32}.Check(s)
	requireOutcome(t, v, Broken)
	if v.Want != "10.30.1.10/32" || !strings.Contains(v.Got, "10.30.1.10/24") {
		t.Errorf("a broken mask does not say both masks: %s", v)
	}
	// Any interface for a public /32, and a network the machine is not on.
	requireOutcome(t, carries{Address: netip.MustParseAddr("203.0.113.2"), Bits: 32}.Check(s), Held)
	v = carries{Network: "fnt-y", Address: addr}.Check(s)
	requireOutcome(t, v, Broken)
	if !strings.Contains(v.Got, "no interface on fnt-y") {
		t.Errorf("a missing interface is not named: %s", v)
	}
}

// A leased interface carries some address of its block, whichever DHCP chose.
func TestLeasesAcceptsAnyAddressOfTheBlock(t *testing.T) {
	s := shapeA()
	requireOutcome(t, leases{Network: "fnt-x"}.Check(s), Held)
	s.Interfaces["eth1"] = Interface{Network: "fnt-x", Addresses: []netip.Prefix{netip.MustParsePrefix("10.99.0.5/24")}}
	v := leases{Network: "fnt-x"}.Check(s)
	requireOutcome(t, v, Broken)
	if v.Want != "an address of 10.30.1.0/24 on eth1" {
		t.Errorf("want %q", v.Want)
	}
}

// The default route is via AND dev, exactly, and nothing else about it.
func TestADefaultRouteClaimComparesViaAndDev(t *testing.T) {
	s := shapeA()
	s.Routes[0] = Route{Dst: defaultDst, Via: netip.MustParseAddr("10.30.1.1"), Dev: "eth1"}
	requireOutcome(t, defaultRoute{Network: "fnt-x"}.Check(s), Held)

	// The regression of 2026-09-04, seen by this comparator: the route left
	// through the private gateway where the plan wanted another door.
	s.Routes[0] = Route{Dst: defaultDst, Via: netip.MustParseAddr("10.77.0.1"), Dev: "eth1"}
	v := defaultRoute{Network: "fnt-x"}.Check(s)
	requireOutcome(t, v, Broken)
	if v.Want != "via 10.30.1.1 dev eth1" || v.Got != "via 10.77.0.1 dev eth1" {
		t.Errorf("the verdict does not cite both routes: %s", v)
	}
	s.Routes = s.Routes[1:]
	v = defaultRoute{Network: "fnt-x"}.Check(s)
	requireOutcome(t, v, Broken)
	if v.Got != "no default route" {
		t.Errorf("an absent route is not named as absent: %s", v)
	}
}

func TestNoDefaultRouteIsBrokenByAnyDefaultRoute(t *testing.T) {
	s := shapeA()
	v := noDefaultRoute{}.Check(s)
	requireOutcome(t, v, Broken)
	if v.Got != "via 169.254.0.1 dev eth0" {
		t.Errorf("the route that should not exist is not cited: %s", v)
	}
	s.Routes = s.Routes[1:]
	requireOutcome(t, noDefaultRoute{}.Check(s), Held)
}

// Each aggregate rides a gateway of the plan, on that gateway's interface;
// one via somebody else's gateway is the route DHCP replaced (#549).
func TestTheAggregatesMustRideAGatewayOfThePlan(t *testing.T) {
	s := shapeA()
	claim := reachesAggregates{Blocks: privateAggregatePrefixes(), Networks: []string{"fnt-x"}}
	requireOutcome(t, claim.Check(s), Held)
	s.Routes[2] = Route{Dst: netip.MustParsePrefix("172.16.0.0/12"), Via: netip.MustParseAddr("10.77.0.1"), Dev: "eth1"}
	v := claim.Check(s)
	requireOutcome(t, v, Broken)
	if !strings.Contains(v.Got, "172.16.0.0/12: via 10.77.0.1 dev eth1") {
		t.Errorf("the misrouted aggregate is not named with its route: %s", v)
	}
	s.Routes = s.Routes[:1]
	v = claim.Check(s)
	requireOutcome(t, v, Broken)
	if !strings.Contains(v.Got, "10.0.0.0/8: none") {
		t.Errorf("a missing aggregate is not named as missing: %s", v)
	}
}

// A claim whose gateway nobody read is unreadable, never held and never
// broken: the third outcome, on the claims that need a second read.
func TestAClaimNeedingAGatewayNobodyReadIsUnreadable(t *testing.T) {
	s := shapeA()
	s.Gateways = nil
	for _, c := range []Claim{
		leases{Network: "fnt-x"},
		defaultRoute{Network: "fnt-x"},
		reachesAggregates{Blocks: privateAggregatePrefixes(), Networks: []string{"fnt-x"}},
	} {
		v := c.Check(s)
		requireOutcome(t, v, Unreadable)
		if !strings.Contains(v.Got, "fnt-x") || !strings.Contains(v.Got, "not read") {
			t.Errorf("an unreadable verdict does not say what was not read: %s", v)
		}
	}
}

// TestTheIncusDialectRestatesWhatTheModeLays holds the driver's dialect to the
// code it describes: the routed next hop is the constant the routed NIC gets,
// the aggregates are the blocks installGuestPrivateRoutes lays, and only OVN
// lays a default route.
func TestTheIncusDialectRestatesWhatTheModeLays(t *testing.T) {
	ovn := ovnDriver(&fakeRuntime{}).Dialect()
	if ovn.RoutedNextHop.String() != routedNextHop {
		t.Errorf("OVN routed next hop %s, want %s", ovn.RoutedNextHop, routedNextHop)
	}
	if !ovn.LaysDefaultRoute {
		t.Error("the OVN dialect does not lay a default route, and RouteEgress does")
	}
	if got := prefixes(ovn.Aggregates); got != strings.Join(privateAggregates, ", ") {
		t.Errorf("OVN aggregates %s, want %s", got, strings.Join(privateAggregates, ", "))
	}
	bridge := newFakeDriver(&fakeRuntime{}).Dialect()
	if bridge.LaysDefaultRoute || len(bridge.Aggregates) != 0 {
		t.Errorf("the bridge dialect claims %+v, and the bridge lays neither", bridge)
	}
	if bridge.RoutedNextHop != ovn.RoutedNextHop {
		t.Error("the routed next hop differs by mode, and the routed NIC does not")
	}
}

// A driver with no dialect is read as the zero one: it refuses no less, and it
// claims nothing that depends on a mode nobody declared.
func TestADriverWithNoDialectClaimsNothingModeDependent(t *testing.T) {
	b := newGroupSyncBench()
	r := rebootReconciler(b, Plan{Egress: "fnt-x", Boot: []Attachment{{Network: "fnt-x"}}})
	claims, err := r.Expect(r.PlanOf(nil), r.dialect())
	if err != nil {
		t.Fatal(err)
	}
	if got := claimNames(claims); len(got) != 1 || got[0] != "leases(fnt-x)" {
		t.Errorf("claims %v under a runtime that declares nothing, want the lease alone", got)
	}
	if _, err := r.Expect(Plan{Publics: []string{"203.0.113.2"}, Egress: "fnt-x"}, r.dialect()); err == nil {
		t.Error("the zero dialect stopped the refusal, and the refusal depends on no dialect")
	}
}

// TestSilenceLeavesTheRouteAloneAndOnlyARefusalTakesItAway holds the three
// states #660 gave the plan, at the layer that executes them: an egress lays
// the route, a refusal takes it away, and silence asks the runtime for
// nothing — the state a machine holding a public address is in, whose route
// RouteAddress owns. Before #660 silence meant "take it away", and a machine
// with a public address lost the route its address had laid.
func TestSilenceLeavesTheRouteAloneAndOnlyARefusalTakesItAway(t *testing.T) {
	for name, tc := range map[string]struct {
		plan Plan
		want []string
	}{
		"an egress lays it":       {Plan{Egress: "fnt-x"}, []string{"RouteEgress feint-bench-m via fnt-x"}},
		"a refusal takes it away": {Plan{NoEgress: true}, []string{"DropEgress feint-bench-m"}},
		"silence asks nothing":    {Plan{Publics: []string{"203.0.113.2"}}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			b := newGroupSyncBench()
			e := &egressRecorder{Recorder: b.rec}
			vm := b.machine("m", "10.0.0.5")
			vm.State = "running"
			egressReconciler(b, e, tc.plan).ReplayEgress(context.Background(), vm)
			if strings.Join(e.laid, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("the runtime was asked %v, want %v", e.laid, tc.want)
			}
		})
	}
}
