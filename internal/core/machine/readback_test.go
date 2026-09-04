package machine

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

// The reading half of the guard (#668): what the driver reads back, how the
// three outcomes are told apart, and the two controls without which the
// instrument proves nothing — a divergence planted on purpose must be found,
// and a read that failed must be neither held nor broken.

// regressionMachine answers the fake runtime for the machine of 2026-09-04
// (#660): one OVN interface on fnt-web carrying its private address and the
// public /32, the default route through the private gateway, and the noise a
// real table carries — connected routes, a link-local one, an unreachable
// line, a metadata route, a profile device the operator owns.
func regressionMachine(f *fakeRuntime) {
	f.hook = func(_ int, args []string) ([]byte, error, bool) {
		switch key := strings.Join(args, " "); {
		case key == "query /1.0/instances/srv":
			return []byte(`{
  "devices": {
    "eth0": {"type": "nic", "network": "fnt-web", "ipv4.address": "10.77.0.2", "ipv4.routes.external": "203.0.113.2/32", "security.acls": "fnt-sg1"},
    "root": {"type": "disk", "path": "/"}
  },
  "expanded_devices": {
    "eth0": {"type": "nic", "network": "fnt-web", "ipv4.address": "10.77.0.2", "ipv4.routes.external": "203.0.113.2/32", "security.acls": "fnt-sg1"},
    "eth9": {"type": "nic", "network": "incusbr0"},
    "root": {"type": "disk", "path": "/"}
  }
}`), nil, true
		case key == "network get fnt-web ipv4.address":
			return []byte("10.77.0.1/24\n"), nil, true
		case key == "exec srv -- ip -4 -o addr show":
			return []byte(`1: lo    inet 127.0.0.1/8 scope host lo\       valid_lft forever preferred_lft forever
2: eth0    inet 10.77.0.2/24 brd 10.77.0.255 scope global eth0\       valid_lft forever preferred_lft forever
2: eth0    inet 203.0.113.2/32 scope global eth0\       valid_lft forever preferred_lft forever
2: eth0    inet 10.99.0.1/24 scope link eth0\       valid_lft forever preferred_lft forever
9: eth9    inet 10.76.154.20/24 brd 10.76.154.255 scope global dynamic eth9\       valid_lft 3000sec preferred_lft 3000sec
`), nil, true
		case key == "exec srv -- ip -4 route show":
			return []byte(`default via 10.77.0.1 dev eth0 proto dhcp src 10.77.0.2 metric 100
10.0.0.0/8 via 10.77.0.1 dev eth0 proto dhcp src 10.77.0.2 metric 100
10.77.0.0/24 dev eth0 proto kernel scope link src 10.77.0.2 metric 100
10.77.0.1 dev eth0 proto dhcp scope link src 10.77.0.2 metric 100
169.254.0.0/16 dev eth0 scope link metric 1000
169.254.169.254 via 10.77.0.1 dev eth0 proto static
172.16.0.0/12 via 10.77.0.1 dev eth0 proto dhcp src 10.77.0.2 metric 100
192.168.0.0/16 via 10.77.0.1 dev eth0 proto dhcp src 10.77.0.2 metric 100
unreachable 198.51.100.0/24 dev lo
`), nil, true
		case strings.HasPrefix(key, "exec srv -- ip -4 route get 10.209.83.1 from 203.0.113.2"):
			return []byte("10.209.83.1 from 203.0.113.2 via 10.77.0.1 dev eth0 uid 0 \n    cache \n"), nil, true
		}
		return nil, nil, false
	}
}

func mustObserve(t *testing.T, d *Incus) Shape {
	t.Helper()
	shape, err := d.Observe(context.Background(), "srv")
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	return shape
}

// TestObserveReadsBackWhatTheMachineCarries is the reading on the machine of
// the regression, with everything a real table carries that must NOT enter
// the Shape: the connected routes, the link-local block and the metadata
// route inside it, the unreachable line, the scope-link address, the
// loopback, and the profile device's address.
func TestObserveReadsBackWhatTheMachineCarries(t *testing.T) {
	f := &fakeRuntime{}
	regressionMachine(f)
	shape := mustObserve(t, ovnDriver(f))

	if len(shape.Interfaces) != 1 {
		t.Fatalf("interfaces %v, want eth0 alone", shape.interfaceNames())
	}
	eth0 := shape.Interfaces["eth0"]
	if eth0.Network != "fnt-web" || eth0.Routed {
		t.Errorf("eth0 read as %+v", eth0)
	}
	if got := prefixes(eth0.Addresses); got != "10.77.0.2/24, 203.0.113.2/32" {
		t.Errorf("eth0 carries %s, want the private /24 and the public /32 and nothing of scope link", got)
	}
	if strings.Join(eth0.RuleSets, ",") != "fnt-sg1" {
		t.Errorf("eth0 wears %v", eth0.RuleSets)
	}
	want := []string{
		"0.0.0.0/0 via 10.77.0.1 dev eth0",
		"10.0.0.0/8 via 10.77.0.1 dev eth0",
		"172.16.0.0/12 via 10.77.0.1 dev eth0",
		"192.168.0.0/16 via 10.77.0.1 dev eth0",
	}
	got := make([]string, 0, len(shape.Routes))
	for _, r := range shape.Routes {
		got = append(got, r.Dst.String()+" "+r.String())
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("routes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if gw := shape.Gateways["fnt-web"]; gw.String() != "10.77.0.1/24" {
		t.Errorf("gateway of fnt-web read as %s", gw)
	}
	if len(shape.Gateways) != 1 {
		t.Errorf("gateways %v, want fnt-web alone", shape.Gateways)
	}
}

// A profile device is the operator's: their bridge, their address, and no
// plan ever claimed it.
func TestObserveReadsOnlyTheMachinesOwnNICs(t *testing.T) {
	f := &fakeRuntime{}
	regressionMachine(f)
	shape := mustObserve(t, ovnDriver(f))
	if _, read := shape.Interfaces["eth9"]; read {
		t.Fatal("the profile's eth9 entered the Shape, and it carries an address no plan claims")
	}
}

// A NIC the machine owns on a network the emulator did not derive is read as
// an interface, and its network's gateway is not asked for: that gateway is
// the operator's, and a claim needing it answers Unreadable instead.
func TestObserveReadsNoForeignNetwork(t *testing.T) {
	f := &fakeRuntime{}
	f.hook = func(_ int, args []string) ([]byte, error, bool) {
		switch key := strings.Join(args, " "); key {
		case "query /1.0/instances/srv":
			return []byte(`{"devices": {"eth1": {"type": "nic", "network": "incusbr0"}},
 "expanded_devices": {"eth1": {"type": "nic", "network": "incusbr0"}}}`), nil, true
		case "exec srv -- ip -4 -o addr show":
			return []byte("3: eth1    inet 10.76.154.20/24 scope global eth1\n"), nil, true
		case "exec srv -- ip -4 route show":
			return []byte("default via 10.76.154.1 dev eth1 proto dhcp\n"), nil, true
		}
		return nil, nil, false
	}
	shape := mustObserve(t, newFakeDriver(f))
	if shape.Interfaces["eth1"].Network != "incusbr0" {
		t.Errorf("the foreign NIC was not read as an interface: %+v", shape.Interfaces)
	}
	if len(f.matching("network get incusbr0")) != 0 || len(shape.Gateways) != 0 {
		t.Errorf("the operator's network was asked for its gateway: %v", f.matching("network get"))
	}
	v := leases{Network: "incusbr0"}.Check(shape)
	if v.Outcome != Unreadable {
		t.Errorf("a claim on the foreign network answered %s, want unreadable", v)
	}
}

// A virtual machine names its interfaces by PCI slot, not by device: the
// Shape is keyed by what the guest calls them, the way the routes are.
func TestObserveNamesAVirtualMachinesInterfacesByMAC(t *testing.T) {
	f := &fakeRuntime{}
	f.hook = func(_ int, args []string) ([]byte, error, bool) {
		switch key := strings.Join(args, " "); key {
		case "query /1.0/instances/srv":
			return []byte(`{"devices": {"eth0": {"type": "nic", "network": "fnt-web", "ipv4.address": "10.77.0.2"}},
 "expanded_devices": {"eth0": {"type": "nic", "network": "fnt-web", "ipv4.address": "10.77.0.2"}}}`), nil, true
		case "query /1.0/instances/srv/state":
			return []byte(`{"network": {"enp5s0": {"hwaddr": "00:16:3e:11:22:33"}, "lo": {"hwaddr": ""}}}`), nil, true
		case "config device get srv eth0 hwaddr":
			return []byte("00:16:3e:11:22:33\n"), nil, true
		case "network get fnt-web ipv4.address":
			return []byte("10.77.0.1/24\n"), nil, true
		case "exec srv -- ip -4 -o addr show":
			return []byte("2: enp5s0    inet 10.77.0.2/24 brd 10.77.0.255 scope global enp5s0\n"), nil, true
		case "exec srv -- ip -4 route show":
			return []byte("default via 10.77.0.1 dev enp5s0 proto dhcp\n"), nil, true
		}
		return nil, nil, false
	}
	d := ovnDriver(f)
	d.VM = true
	shape := mustObserve(t, d)
	iface, read := shape.Interfaces["enp5s0"]
	if !read || prefixes(iface.Addresses) != "10.77.0.2/24" {
		t.Fatalf("the VM's interface was not read under the guest's name: %+v", shape.Interfaces)
	}
	if v := (carries{Network: "fnt-web", Address: netip.MustParseAddr("10.77.0.2"), Bits: 24}).Check(shape); v.Outcome != Held {
		t.Errorf("the pinned address on a VM answered %s", v)
	}
}

// TestTheReplyDoorOfTheRegressionIsBroken reproduces #660 through the fake
// runtime: the reply from the public address leaves through the private
// gateway, and a door claim wanting the routed next hop is broken with both
// doors named.
//
// The wanted door is the TEST's, written down as what docs/limits.md records
// for the routed shape, and deliberately not derived: which door is right on
// the shape this fixture models is the measurement #672 asks for, and this
// test holds the comparator, not the table.
func TestTheReplyDoorOfTheRegressionIsBroken(t *testing.T) {
	f := &fakeRuntime{}
	regressionMachine(f)
	d := ovnDriver(f)
	shape := mustObserve(t, d)

	from, to := netip.MustParseAddr("203.0.113.2"), netip.MustParseAddr("10.209.83.1")
	got, err := d.Door(context.Background(), "srv", from, to)
	if err != nil {
		t.Fatalf("door: %v", err)
	}
	shape.Doors = map[netip.Addr]Route{from: got}

	claim := door{From: from, To: to, Via: netip.MustParseAddr("169.254.0.1"), Dev: "eth0"}
	v := claim.Check(shape)
	if v.Outcome != Broken {
		t.Fatalf("the regression's door answered %s, want broken", v)
	}
	if v.Want != "via 169.254.0.1 dev eth0" || v.Got != "via 10.77.0.1 dev eth0" {
		t.Errorf("the verdict does not name both doors: %s", v)
	}
	if v.String() != "door(203.0.113.2) broken: want via 169.254.0.1 dev eth0, got via 10.77.0.1 dev eth0" {
		t.Errorf("rendered as %q", v.String())
	}
}

// A machine with no route towards the destination has answered a fact about
// itself, not refused a read: the door is broken, never unreadable. A read
// the kernel refuses for another reason stays a failed read.
func TestADoorWithNoRouteIsBrokenNotUnreadable(t *testing.T) {
	f := &fakeRuntime{fail: map[string]error{
		"route get": errors.New("RTNETLINK answers: Network is unreachable"),
	}}
	d := ovnDriver(f)
	from, to := netip.MustParseAddr("203.0.113.2"), netip.MustParseAddr("10.209.83.1")
	got, err := d.Door(context.Background(), "srv", from, to)
	if err != nil || got.Dev != "" {
		t.Fatalf("no route answered (%v, %v), want the zero route and no error", got, err)
	}
	v := (door{From: from, To: to, Via: netip.MustParseAddr("169.254.0.1"), Dev: "eth0"}).Check(
		Shape{Doors: map[netip.Addr]Route{from: got}})
	if v.Outcome != Broken || v.Got != "no route" {
		t.Errorf("a machine with no route answered %s", v)
	}

	f.fail["route get"] = errors.New("RTNETLINK answers: Invalid argument")
	if _, err := d.Door(context.Background(), "srv", from, to); err == nil {
		t.Error("a read the kernel refused for another reason was not reported as a failed read")
	}
}

// TestTheParsersReadBothImplementationsOfIp holds each parser to the two
// formats in play: iproute2 on the Debian, Ubuntu and RHEL images, busybox on
// Alpine. A parser that read one would report the other's machines bare.
func TestTheParsersReadBothImplementationsOfIp(t *testing.T) {
	for name, out := range map[string]string{
		"iproute2": `2: eth0    inet 10.30.1.10/24 brd 10.30.1.255 scope global eth0\       valid_lft forever preferred_lft forever`,
		"busybox":  `2: eth0    inet 10.30.1.10/24 scope global eth0`,
	} {
		got := parseAddresses([]byte(out))
		if prefixes(got["eth0"]) != "10.30.1.10/24" {
			t.Errorf("%s addresses: %v", name, got)
		}
	}
	for name, out := range map[string]string{
		"iproute2": "default via 10.30.1.1 dev eth0 proto dhcp src 10.30.1.10 metric 100\n10.30.1.0/24 dev eth0 proto kernel scope link src 10.30.1.10\n",
		"busybox":  "default via 10.30.1.1 dev eth0  metric 100\n10.30.1.0/24 dev eth0 scope link  src 10.30.1.10\n",
	} {
		got := parseRoutes([]byte(out))
		if len(got) != 1 || got[0].Dst != defaultDst || got[0].String() != "via 10.30.1.1 dev eth0" {
			t.Errorf("%s routes: %v", name, got)
		}
	}
	for name, out := range map[string]string{
		"iproute2": "10.209.83.1 from 203.0.113.2 via 169.254.0.1 dev eth0 uid 0 \n    cache \n",
		"busybox":  "10.209.83.1 from 203.0.113.2 via 169.254.0.1 dev eth0  src 203.0.113.2 \n",
	} {
		got, err := parseDoor([]byte(out))
		if err != nil || got.String() != "via 169.254.0.1 dev eth0" {
			t.Errorf("%s door: %v, %v", name, got, err)
		}
	}
	// iproute2 ends every route line with a space, which the repository's
	// whitespace hook strips from a fixture: held here as an escape, so the
	// tolerance is measured rather than assumed.
	if got := parseRoutes([]byte("default via 10.30.1.1 dev eth0 proto dhcp metric 100\x20\n")); len(got) != 1 {
		t.Errorf("a trailing space on a route line: %v", got)
	}
	// A host route prints its address bare, and a connected door names the
	// device alone.
	if got := parseRoutes([]byte("203.0.113.9 via 10.0.0.1 dev eth1\n")); len(got) != 1 || got[0].Dst.String() != "203.0.113.9/32" {
		t.Errorf("a bare host route: %v", got)
	}
	if got, err := parseDoor([]byte("10.30.1.11 from 10.30.1.10 dev eth0 uid 0\n")); err != nil || got.Via.IsValid() || got.Dev != "eth0" {
		t.Errorf("a connected door: %v, %v", got, err)
	}
	if _, err := parseDoor([]byte("\n")); err == nil {
		t.Error("an empty answer parsed as a door")
	}
}

// ---- the layer: verify over an observing runtime ----------------------------

// observingRecorder is the contract recorder with the reading half: the
// Shape a test writes is what the runtime reports, which is the only way to
// plant a divergence and know exactly what the verifier had to find.
type observingRecorder struct {
	*Recorder
	shape      Shape
	err        error
	noFirewall bool
	// queue, when set, is read one Shape per Observe ahead of shape: how a
	// test writes a machine that changes between two readings.
	queue []Shape
	// reads counts the Observe calls, which is how a test holds a bound.
	reads int
	// failFirst makes that many leading reads fail before the shape answers:
	// a machine unreadable before its reboot and readable after it.
	failFirst int
}

func (o *observingRecorder) Observe(context.Context, string) (Shape, error) {
	o.reads++
	if o.failFirst > 0 {
		o.failFirst--
		return Shape{}, errUnreadable
	}
	if len(o.queue) > 0 {
		next := o.queue[0]
		o.queue = o.queue[1:]
		return next, o.err
	}
	return o.shape, o.err
}

func (o *observingRecorder) Door(_ context.Context, _ string, from, _ netip.Addr) (Route, error) {
	if route, read := o.shape.Doors[from]; read {
		return route, nil
	}
	return Route{}, errors.New("no door was planted")
}

func (o *observingRecorder) Capabilities() Capabilities {
	caps := o.Recorder.Capabilities()
	if o.noFirewall {
		caps.Firewall = false
	}
	return caps
}

func observingReconciler(b *groupSyncBench, o *observingRecorder, plan Plan) Reconciler {
	sync := b.sync()
	sync.Binding.driver = o
	sync.Binding.RunningState = "running"
	sync.Binding.FailedState = "stopped"
	sync.PlanOf = func(*testResource) Plan { return plan }
	return Reconciler{
		Groups:      sync,
		PlanOf:      func(*testResource) Plan { return plan },
		PublicBlock: netip.MustParsePrefix("203.0.113.0/24"),
	}
}

// pinnedOn is the Shape of a machine carrying one pinned address on one
// emulated network, its gateway read.
func pinnedOn(network, cidr string, sets ...string) Shape {
	return Shape{
		Interfaces: map[string]Interface{
			"eth0": {Network: network, Addresses: []netip.Prefix{netip.MustParsePrefix(cidr)}, RuleSets: sets},
		},
		Gateways: map[string]netip.Prefix{network: netip.MustParsePrefix("10.30.1.1/24")},
	}
}

func outcomes(verdicts []Verdict) string {
	out := make([]string, 0, len(verdicts))
	for _, v := range verdicts {
		out = append(out, v.String())
	}
	return strings.Join(out, "\n")
}

// TestAPlantedDivergenceIsReported is the positive control: a verifier that
// has never found anything is indistinguishable from one that does not look.
// The accepting half runs on the same plan and the same runtime, so the
// finding cannot be a verifier that refuses everything.
func TestAPlantedDivergenceIsReported(t *testing.T) {
	plan := Plan{Boot: []Attachment{{Network: "fnt-x", Address: "10.30.1.10", PrefixLen: 24}}}
	b := newGroupSyncBench()
	o := &observingRecorder{Recorder: b.rec, shape: pinnedOn("fnt-x", "10.30.1.10/24")}
	vm := b.machine("m", "10.30.1.10")
	r := observingReconciler(b, o, plan)

	verdicts, asked := r.verify(context.Background(), vm, nil)
	if !asked || len(verdicts) != 1 || verdicts[0].Outcome != Held {
		t.Fatalf("a machine carrying what its plan claims answered asked=%v:\n%s", asked, outcomes(verdicts))
	}

	// One digit off, planted: the address the runtime reports is not the
	// one the plan pinned.
	o.shape = pinnedOn("fnt-x", "10.30.1.11/24")
	verdicts, asked = r.verify(context.Background(), vm, nil)
	if !asked || len(verdicts) != 1 {
		t.Fatalf("asked=%v, verdicts:\n%s", asked, outcomes(verdicts))
	}
	v := verdicts[0]
	if v.Outcome != Broken || v.Claim != "carries(fnt-x, 10.30.1.10/24)" || v.Got != "eth0: 10.30.1.11/24" {
		t.Fatalf("the planted divergence was not reported as itself: %s", v)
	}
}

// TestAnUnreadableShapeIsNeitherHeldNorBroken is the second control: a read
// that failed answers Unreadable on every claim, counted, and a runtime that
// cannot be asked at all is not asked — a fourth state, told from the third.
func TestAnUnreadableShapeIsNeitherHeldNorBroken(t *testing.T) {
	plan := Plan{Boot: []Attachment{{Network: "fnt-x", Address: "10.30.1.10", PrefixLen: 24}}, Publics: []string{"203.0.113.2"}}
	b := newGroupSyncBench()
	o := &observingRecorder{Recorder: b.rec, err: errors.New("the agent isn't currently running")}
	vm := b.machine("m", "10.30.1.10")

	verdicts, asked := observingReconciler(b, o, plan).verify(context.Background(), vm, nil)
	if !asked || len(verdicts) != 2 {
		t.Fatalf("asked=%v, verdicts:\n%s", asked, outcomes(verdicts))
	}
	for _, v := range verdicts {
		if v.Outcome != Unreadable || !strings.Contains(v.Got, "agent") {
			t.Errorf("a failed read answered %s, want unreadable naming the failure", v)
		}
	}

	// A runtime with no reading half is not asked, and says so.
	plain := rebootReconciler(b, plan)
	if verdicts, asked := plain.verify(context.Background(), vm, nil); asked || len(verdicts) != 0 {
		t.Errorf("a runtime that cannot be asked answered asked=%v with %d verdicts", asked, len(verdicts))
	}
	// And so is a resource with no machine behind it.
	bare := b.machine("bare", "")
	if verdicts, asked := observingReconciler(b, o, plan).verify(context.Background(), bare, nil); asked || len(verdicts) != 0 {
		t.Errorf("a resource with no machine answered asked=%v with %d verdicts", asked, len(verdicts))
	}
}

// The rule sets a machine wears are claimed on its filtered interfaces, from
// the same fields the firewall step reads; an interface on a network the
// pack declared outside its groups' reach is claimed bare.
func TestTheRuleSetsAMachineWearsAreClaimedOnItsFilteredInterfaces(t *testing.T) {
	plan := Plan{
		Boot:        []Attachment{{Network: "fnt-x", Address: "10.30.1.10", PrefixLen: 24}},
		Memberships: []Attachment{{Network: "fnt-y", Unfiltered: true}},
	}
	b := newGroupSyncBench()
	b.group("g", "")
	set := FirewallName("bench", "g")
	shape := pinnedOn("fnt-x", "10.30.1.10/24", set)
	shape.Interfaces["eth1"] = Interface{Network: "fnt-y", Addresses: []netip.Prefix{netip.MustParsePrefix("10.40.0.5/24")}}
	shape.Gateways["fnt-y"] = netip.MustParsePrefix("10.40.0.1/24")
	o := &observingRecorder{Recorder: b.rec, shape: shape}
	vm := b.machine("m", "10.30.1.10", "g")
	r := observingReconciler(b, o, plan)

	verdicts, _ := r.verify(context.Background(), vm, nil)
	names := map[string]Verdict{}
	for _, v := range verdicts {
		names[v.Claim] = v
	}
	for _, claim := range []string{"wears(fnt-x)", "wears(fnt-y)"} {
		v, claimed := names[claim]
		if !claimed {
			t.Fatalf("%s was not claimed:\n%s", claim, outcomes(verdicts))
		}
		if v.Outcome != Held {
			t.Errorf("%s answered %s on a machine wearing exactly its group", claim, v)
		}
	}

	// The unfiltered interface planted wearing the set: the pack said its
	// groups do not reach it, and the runtime bound one anyway.
	eth1 := shape.Interfaces["eth1"]
	eth1.RuleSets = []string{set}
	shape.Interfaces["eth1"] = eth1
	verdicts, _ = r.verify(context.Background(), vm, nil)
	for _, v := range verdicts {
		if v.Claim == "wears(fnt-y)" && (v.Outcome != Broken || v.Want != "no rule set on eth1") {
			t.Errorf("an unfiltered interface wearing a set answered %s", v)
		}
	}
}

// A host that withdrew the firewall capability (#454) binds nothing, and a
// claim about the bindings would be broken on every machine for a fact
// /_feint/health already publishes.
func TestAWithdrawnFirewallClaimsNoRuleSets(t *testing.T) {
	plan := Plan{Boot: []Attachment{{Network: "fnt-x", Address: "10.30.1.10", PrefixLen: 24}}}
	b := newGroupSyncBench()
	b.group("g", "")
	o := &observingRecorder{Recorder: b.rec, shape: pinnedOn("fnt-x", "10.30.1.10/24"), noFirewall: true}
	vm := b.machine("m", "10.30.1.10", "g")

	verdicts, _ := observingReconciler(b, o, plan).verify(context.Background(), vm, nil)
	for _, v := range verdicts {
		if strings.HasPrefix(v.Claim, "wears(") {
			t.Fatalf("a withdrawn firewall still claims rule sets: %s", v)
		}
	}
}

// The wears comparator on hand-built shapes: exact set equality, and "none"
// when the claim is bare.
func TestWearsComparesTheRuleSetsPerInterface(t *testing.T) {
	shape := pinnedOn("fnt-x", "10.30.1.10/24", "fnt-a", "fnt-b")
	requireOutcome(t, wears{Network: "fnt-x", Sets: []string{"fnt-a", "fnt-b"}}.Check(shape), Held)
	v := wears{Network: "fnt-x", Sets: []string{"fnt-a"}}.Check(shape)
	requireOutcome(t, v, Broken)
	if v.Want != "fnt-a on eth0" || v.Got != "fnt-a, fnt-b on eth0" {
		t.Errorf("the verdict does not name both bindings: %s", v)
	}
	v = wears{Network: "fnt-x"}.Check(shape)
	requireOutcome(t, v, Broken)
	if v.Want != "no rule set on eth0" {
		t.Errorf("a bare claim wants %q", v.Want)
	}
	requireOutcome(t, wears{Network: "fnt-x"}.Check(pinnedOn("fnt-x", "10.30.1.10/24")), Held)
	v = wears{Network: "fnt-z", Sets: []string{"fnt-a"}}.Check(shape)
	requireOutcome(t, v, Broken)
	if !strings.Contains(v.Got, "no interface on fnt-z") {
		t.Errorf("a missing interface is not named: %s", v)
	}
}
