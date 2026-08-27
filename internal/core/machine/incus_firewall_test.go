package machine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"testing"
)

// The firewall path is the one a second provider pack reuses unchanged, and
// most of what it must get right is invisible until it hurts. A rule set
// missing from an inherited interface leaves a machine answering on an address
// nothing published; a default-action key sent to an OVN NIC re-plugs the
// device and the guest loses every address it held, intermittently and only
// under OVN. Both are argument-level facts, so a test can hold them.

// fakeRuntime records the commands a driver issues and answers the queries it
// makes. Answers are keyed by the first two arguments, which is enough to tell
// "query /1.0/instances/x" from "query /1.0/networks/y".
type fakeRuntime struct {
	calls   [][]string
	answers map[string]string
	fail    map[string]error
	// hook answers before the maps do, for the cases where the answer depends
	// on what came before: a network that refuses to go on the first pass and
	// accepts on the second.
	hook func(call int, args []string) ([]byte, error, bool)
}

func (f *fakeRuntime) run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	if f.hook != nil {
		if out, err, handled := f.hook(len(f.calls)-1, args); handled {
			return out, err
		}
	}
	key := strings.Join(args, " ")
	for pattern, err := range f.fail {
		if strings.Contains(key, pattern) {
			return nil, err
		}
	}
	for pattern, answer := range f.answers {
		if strings.Contains(key, pattern) {
			return []byte(answer), nil
		}
	}
	// The ownership probe answers "ours" unless a test says otherwise.
	//
	// Every machine in these tests is one the emulator created, so that is the
	// case a fixture should not have to restate; a test about the *refusal*
	// declares the empty label explicitly, which is the half worth spelling out.
	// Without this default, adding the guard to ApplyFirewall turned four
	// unrelated tests red for a reason none of them is about.
	if strings.HasPrefix(key, "config get ") && strings.Contains(key, "user."+LabelKey) {
		return []byte("feint\n"), nil
	}
	return nil, nil
}

// commands returns the recorded calls as joined strings, for readable assertions.
func (f *fakeRuntime) commands() []string {
	out := make([]string, 0, len(f.calls))
	for _, call := range f.calls {
		out = append(out, strings.Join(call, " "))
	}
	return out
}

func (f *fakeRuntime) matching(substr string) []string {
	var out []string
	for _, cmd := range f.commands() {
		if strings.Contains(cmd, substr) {
			out = append(out, cmd)
		}
	}
	return out
}

// twoNICs is a machine with an interface of its own and one inherited from the
// runtime's default profile, which is the shape every emulated server has: the
// pack attaches a private NIC, the profile already handed out eth0.
const twoNICs = `{
  "expanded_devices": {
    "eth0": {"type": "nic", "network": "incusbr0"},
    "eth1": {"type": "nic", "network": "scw-abc"}
  },
  "devices": {
    "eth1": {"type": "nic", "network": "scw-abc"}
  }
}`

func newFakeDriver(f *fakeRuntime) *Incus {
	d := NewIncus()
	d.runner = f.run
	return d
}

func TestApplyFirewallCoversEveryInterface(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/srv": twoNICs,
		"/1.0/networks/":     `{"type": "bridge"}`,
	}}
	d := newFakeDriver(f)

	err := d.ApplyFirewall(context.Background(), "srv", FirewallBinding{
		Names:          []string{"sg-one"},
		DefaultIngress: "drop",
		DefaultEgress:  "allow",
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	applied := f.matching("security.acls=")
	if len(applied) != 2 {
		t.Fatalf("expected both interfaces to be covered, got %d:\n%s",
			len(applied), strings.Join(applied, "\n"))
	}

	// The inherited interface is overridden, not set: Incus refuses "set" on a
	// device that comes from a profile, and editing the profile would change
	// every other instance sharing it.
	var own, inherited string
	for _, cmd := range applied {
		switch {
		case strings.Contains(cmd, " eth1 "):
			own = cmd
		case strings.Contains(cmd, " eth0 "):
			inherited = cmd
		}
	}
	if !strings.Contains(own, "config device set srv eth1") {
		t.Errorf("the machine's own interface should be set, got %q", own)
	}
	if !strings.Contains(inherited, "config device override srv eth0") {
		t.Errorf("the inherited interface should be overridden, got %q", inherited)
	}
}

func TestApplyFirewallCarriesEveryGroup(t *testing.T) {
	// Scaleway allows one group per server, Outscale and AWS allow several. The
	// core has always taken a list; nothing exercised it with more than one
	// name, which is the first wall a second pack would hit.
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/srv": twoNICs,
		"/1.0/networks/":     `{"type": "bridge"}`,
	}}
	d := newFakeDriver(f)

	if err := d.ApplyFirewall(context.Background(), "srv", FirewallBinding{
		Names:          []string{"sg-web", "sg-admin", "sg-metrics"},
		DefaultIngress: "drop",
		DefaultEgress:  "allow",
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	for _, cmd := range f.matching("security.acls=") {
		if !strings.Contains(cmd, "security.acls=sg-web,sg-admin,sg-metrics") {
			t.Fatalf("every group must reach the interface, got %q", cmd)
		}
	}
}

func TestApplyFirewallDetachesWhenNoGroupIsNamed(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/srv": twoNICs,
		"/1.0/networks/":     `{"type": "bridge"}`,
	}}
	d := newFakeDriver(f)

	if err := d.ApplyFirewall(context.Background(), "srv", FirewallBinding{}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	applied := f.matching("security.acls=")
	if len(applied) != 2 {
		t.Fatalf("detaching must still touch every interface, got %d", len(applied))
	}
	for _, cmd := range applied {
		if !strings.Contains(cmd, "security.acls= ") && !strings.HasSuffix(cmd, "security.acls=") {
			t.Errorf("expected an empty rule set list, got %q", cmd)
		}
		// The default actions mean nothing without a rule set attached, and the
		// runtime rejects them on a bare NIC.
		if strings.Contains(cmd, "security.acls.default") {
			t.Errorf("default actions must not be sent with no rule set: %q", cmd)
		}
	}
}

func TestApplyFirewallSendsOnlyTheACLKeyToAnOVNInterface(t *testing.T) {
	// Measured on Incus 7.2 and confirmed in nic_ovn.go: security.acls is the
	// only ACL key an OVN NIC updates in place. Any other key makes Incus
	// remove and re-add the device, and the guest loses every address the
	// interface carried. The conformance suite would see that as a machine
	// that stopped answering, once in a while.
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/srv":     twoNICs,
		"/1.0/networks/scw-abc":  `{"type": "ovn"}`,
		"/1.0/networks/incusbr0": `{"type": "bridge"}`,
	}}
	d := newFakeDriver(f)
	d.OVN = true

	if err := d.ApplyFirewall(context.Background(), "srv", FirewallBinding{
		Names:          []string{"sg-one"},
		DefaultIngress: "drop",
		DefaultEgress:  "allow",
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	for _, cmd := range f.matching("security.acls=") {
		isOVN := strings.Contains(cmd, " eth1 ")
		hasDefaults := strings.Contains(cmd, "security.acls.default")
		if isOVN && hasDefaults {
			t.Fatalf("the OVN interface must receive security.acls alone, got %q", cmd)
		}
		if !isOVN && !hasDefaults {
			t.Fatalf("a bridged interface still takes its default actions, got %q", cmd)
		}
	}
}

func TestApplyFirewallRefusesAnUnsafeMachineName(t *testing.T) {
	f := &fakeRuntime{}
	d := newFakeDriver(f)

	if err := d.ApplyFirewall(context.Background(), "srv; rm -rf /", FirewallBinding{}); err == nil {
		t.Fatal("expected a name carrying shell metacharacters to be refused")
	}
	if len(f.calls) != 0 {
		t.Fatalf("nothing should reach the runtime, got %v", f.commands())
	}
}

func TestRemoveFirewallIsIdempotent(t *testing.T) {
	// The ownership probe runs first now, so the fake has to answer it the way
	// the runtime does: a rule set that is gone fails the query, and one that is
	// ours carries the description the emulator writes.
	f := &fakeRuntime{
		fail: map[string]error{
			"/1.0/network-acls/sg-gone": errors.New(`Error: Network ACL not found`),
			"acl delete":                errors.New("Error: ACL is in use by 1 NIC"),
		},
		answers: map[string]string{
			"/1.0/network-acls/sg-used": `{"name": "sg-used", "description": "feint security group"}`,
		},
	}
	d := newFakeDriver(f)

	// Deleting a rule set that is already gone is the normal path when a
	// security group is removed twice, or removed after a sweep.
	if err := d.RemoveFirewall(context.Background(), "sg-gone"); err != nil {
		t.Fatalf("removing an absent rule set must succeed, got %v", err)
	}

	if err := d.RemoveFirewall(context.Background(), "sg-used"); err == nil {
		t.Fatal("a rule set still attached must not be reported as removed")
	}
}

// TestToACLRules holds the translation table, and its ICMP rows are #454.
//
// The protocol used to be chosen from the rule's own name alone, so an ICMP rule
// sourced from an IPv6 block was written as icmp4; the daemon answered `Cannot
// use IPv6 source addresses with "icmp4" protocol`, and because the set is
// written in one PUT the refusal cost every rule of that group. The family of
// the addresses decides now, and a rule no protocol expresses is dropped alone.
func TestToACLRules(t *testing.T) {
	cases := []struct {
		name string
		in   FirewallRule
		want []aclRule
	}{
		{
			name: "an ingress rule keeps its source and its port range",
			in:   FirewallRule{Direction: "ingress", Action: "allow", Protocol: "TCP", Source: "10.0.0.0/8", PortFrom: 80, PortTo: 90},
			want: []aclRule{{Action: "allow", State: "enabled", Protocol: "tcp", Source: "10.0.0.0/8", DestinationPort: "80-90"}},
		},
		{
			name: "a single port is not written as a range",
			in:   FirewallRule{Direction: "ingress", Action: "allow", Protocol: "tcp", PortFrom: 22, PortTo: 22},
			want: []aclRule{{Action: "allow", State: "enabled", Protocol: "tcp", DestinationPort: "22"}},
		},
		{
			name: "no port means every port, not port zero",
			in:   FirewallRule{Direction: "ingress", Action: "allow", Protocol: "tcp"},
			want: []aclRule{{Action: "allow", State: "enabled", Protocol: "tcp"}},
		},
		{
			name: "icmp from an IPv4 block carries no port, which the runtime rejects on it",
			in:   FirewallRule{Direction: "ingress", Action: "allow", Protocol: "ICMP", Source: "0.0.0.0/0", PortFrom: 22},
			want: []aclRule{{Action: "allow", State: "enabled", Protocol: "icmp4", Source: "0.0.0.0/0"}},
		},
		{
			// The defect. The name says nothing about a family; ::/0 does.
			name: "icmp from an IPv6 block becomes the IPv6 protocol",
			in:   FirewallRule{Direction: "ingress", Action: "allow", Protocol: "ICMP", Source: "::/0"},
			want: []aclRule{{Action: "allow", State: "enabled", Protocol: "icmp6", Source: "::/0"}},
		},
		{
			// "icmp4" is this package's own spelling for ICMP, not a claim about
			// IPv4. Reading it as one would drop exactly the rules #454 is about.
			name: "the icmp4 spelling is family-agnostic too",
			in:   FirewallRule{Direction: "ingress", Action: "allow", Protocol: "icmp4", Source: "2001:db8::/32"},
			want: []aclRule{{Action: "allow", State: "enabled", Protocol: "icmp6", Source: "2001:db8::/32"}},
		},
		{
			name: "an egress icmp rule reads its destination",
			in:   FirewallRule{Direction: "egress", Action: "drop", Protocol: "icmp", Destination: "fd00::/8"},
			want: []aclRule{{Action: "drop", State: "enabled", Protocol: "icmp6", Destination: "fd00::/8"}},
		},
		{
			name: "a bare address fixes a family as well as a block does",
			in:   FirewallRule{Direction: "ingress", Action: "allow", Protocol: "icmp", Source: "2001:db8::1"},
			want: []aclRule{{Action: "allow", State: "enabled", Protocol: "icmp6", Source: "2001:db8::1"}},
		},
		{
			name: "an address range fixes a family through both of its bounds",
			in:   FirewallRule{Direction: "ingress", Action: "allow", Protocol: "icmp", Source: "10.0.0.1-10.0.0.9"},
			want: []aclRule{{Action: "allow", State: "enabled", Protocol: "icmp4", Source: "10.0.0.1-10.0.0.9"}},
		},
		{
			// "ICMP from anywhere" means both families. Half of it enforced
			// silently is the same defect one size smaller.
			name: "icmp with no address at all becomes both protocols",
			in:   FirewallRule{Direction: "ingress", Action: "allow", Protocol: "icmp"},
			want: []aclRule{
				{Action: "allow", State: "enabled", Protocol: "icmp4"},
				{Action: "allow", State: "enabled", Protocol: "icmp6"},
			},
		},
		{
			name: "an explicit v6 spelling with no address stays v6, and gains no v4 twin",
			in:   FirewallRule{Direction: "ingress", Action: "allow", Protocol: "ipv6-icmp"},
			want: []aclRule{{Action: "allow", State: "enabled", Protocol: "icmp6"}},
		},
		{
			name: "the icmpv6 spelling is understood rather than dropped",
			in:   FirewallRule{Direction: "ingress", Action: "allow", Protocol: "ICMPv6", Source: "::/0"},
			want: []aclRule{{Action: "allow", State: "enabled", Protocol: "icmp6", Source: "::/0"}},
		},
		{
			name: "an empty protocol means any, and takes no port either",
			in:   FirewallRule{Direction: "egress", Action: "drop", Destination: "0.0.0.0/0", PortFrom: 53},
			want: []aclRule{{Action: "drop", State: "enabled", Destination: "0.0.0.0/0"}},
		},
		{
			// An "any" rule carries whatever addresses it was given: the empty
			// protocol has no family, so an IPv6 block is not its business.
			name: "an any rule with an IPv6 block is untouched by the family question",
			in:   FirewallRule{Direction: "ingress", Action: "allow", Source: "::/0"},
			want: []aclRule{{Action: "allow", State: "enabled", Source: "::/0"}},
		},
		{
			name: "a stateless allow survives translation rather than becoming stateful",
			in:   FirewallRule{Direction: "ingress", Action: "allow-stateless", Protocol: "udp", PortFrom: 53},
			want: []aclRule{{Action: "allow-stateless", State: "enabled", Protocol: "udp", DestinationPort: "53"}},
		},
		{
			name: "an action the runtime does not know is dropped, not approximated",
			in:   FirewallRule{Direction: "ingress", Action: "log", Protocol: "tcp"},
		},
		{
			name: "a protocol the runtime does not know is dropped too",
			in:   FirewallRule{Direction: "ingress", Action: "allow", Protocol: "sctp"},
		},
		{
			// No ICMP protocol covers both families, and approximating with one
			// of them would enforce something the API does not describe.
			name: "an icmp rule naming both families is dropped rather than halved",
			in:   FirewallRule{Direction: "ingress", Action: "allow", Protocol: "icmp", Source: "10.0.0.0/8,::/0"},
		},
		{
			name: "an explicit v6 spelling beside an IPv4 block is a contradiction, not a v4 rule",
			in:   FirewallRule{Direction: "ingress", Action: "allow", Protocol: "icmpv6", Source: "10.0.0.0/8"},
		},
		{
			// Three outcomes, never two: an address this cannot read is not an
			// address that is absent, and guessing a family for it sends the
			// daemon a value it refuses — which costs the whole group.
			name: "an unreadable source is dropped rather than read as no address",
			in:   FirewallRule{Direction: "ingress", Action: "allow", Protocol: "icmp", Source: "not-an-address"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := toACLRules(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d rules %+v, want %d %+v", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("rule %d:\ngot  %+v\nwant %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestOrDropClosesRatherThanOpens(t *testing.T) {
	for _, action := range []string{"allow", "drop", "reject"} {
		if got := orDrop(action); got != action {
			t.Errorf("orDrop(%q) = %q, want it carried through", action, got)
		}
	}
	// An unstated or unknown policy must close. Opening on a value nobody
	// recognised is the failure that cannot be seen from the API.
	for _, action := range []string{"", "accept", "ALLOW", "permit"} {
		if got := orDrop(action); got != "drop" {
			t.Errorf("orDrop(%q) = %q, want drop", action, got)
		}
	}
}

func TestFirewallNameIsStableAndFitsAnInterface(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	first := FirewallName("scw", id)
	if first != FirewallName("scw", id) {
		t.Fatal("the same id must always give the same name")
	}
	if len(first) > MaxNetworkNameLen {
		t.Fatalf("%q is longer than an interface name allows", first)
	}
	// A second pack uses another prefix, and the two must not collide on one id.
	if other := FirewallName("osc", id); other == first {
		t.Fatalf("two providers produced the same name %q for one id", first)
	}
}

// ApplyFirewall refuses an instance the emulator did not create (#209).
//
// safeName answers "could this become a command argument safely" and accepts
// `production-database` like every other name on the host. The machine name
// arrives from Resource.Runtime, which PUT /_feint/state and `feint snapshot
// load` restore verbatim, so a crafted snapshot named any instance and this
// call edited its network devices.
//
// RemoveFirewall already asked the question. The guarded list forgot that
// installing a rule set on somebody else's NIC is as much a change as removing
// one — a reconfiguring path, not only a destructive one.
//
// The assertion is on the arguments, not on the error: what matters is that no
// command carrying the foreign name is ever emitted, which is what the runner
// seam exists to measure.
func TestApplyFirewallRefusesAnInstanceTheEmulatorDidNotCreate(t *testing.T) {
	const foreign = "production-database"
	f := &fakeRuntime{answers: map[string]string{
		// The label is absent: this instance is the operator's, not ours.
		"config get " + foreign: "",
		"/1.0/instances/":       twoNICs,
		"/1.0/networks/":        `{"type": "bridge"}`,
	}}
	d := newFakeDriver(f)

	err := d.ApplyFirewall(context.Background(), foreign, FirewallBinding{
		Names:          []string{"sg-one"},
		DefaultIngress: "drop",
		DefaultEgress:  "allow",
	})
	if err == nil {
		t.Fatal("a firewall was applied to an instance the emulator never created")
	}

	for _, command := range f.commands() {
		if !strings.Contains(command, foreign) {
			continue
		}
		// Reading the label is the question itself, so it is allowed to name it.
		if strings.HasPrefix(command, "config get ") {
			continue
		}
		t.Errorf("a command reached the operator's own instance: %s", command)
	}
}

// mixedNICs is the terraform conformance server's machine: a routed eth0
// carrying the public addresses from the launch, and a private NIC attached
// afterwards on a managed network.
const mixedNICs = `{
  "expanded_devices": {
    "eth0": {"type": "nic", "nictype": "routed", "ipv4.address": "203.0.113.7"},
    "eth1": {"type": "nic", "network": "scw-abc"}
  },
  "devices": {
    "eth0": {"type": "nic", "nictype": "routed", "ipv4.address": "203.0.113.7"},
    "eth1": {"type": "nic", "network": "scw-abc"}
  }
}`

// TestApplyFirewallRefusesAGroupOnARoutedNIC holds the honest half of #337. A
// routed NIC accepts no security option — measured on Incus 7.2 and 7.3, every
// key an invalid device option — so a binding that names rule sets must come
// back as the typed refusal, with no doomed key ever sent to that interface,
// while the interfaces that can enforce still get their rule sets. Until #337
// the keys were sent, the failure was logged as an operational ERROR, and the
// control plane answered as if the group were enforced.
func TestApplyFirewallRefusesAGroupOnARoutedNIC(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/srv": mixedNICs,
		"/1.0/networks/":     `{"type": "bridge"}`,
	}}
	d := newFakeDriver(f)

	err := d.ApplyFirewall(context.Background(), "srv", FirewallBinding{
		Names:          []string{"sg-one"},
		DefaultIngress: "drop",
		DefaultEgress:  "allow",
	})
	if !errors.Is(err, ErrFirewallUnenforceable) {
		t.Fatalf("a rule set on a routed NIC must be refused with the typed error, got %v", err)
	}
	for _, cmd := range f.matching("security.acls") {
		if strings.Contains(cmd, " eth0 ") {
			t.Errorf("a security option was sent to the routed NIC: %q", cmd)
		}
	}
	if got := f.matching("config device set srv eth1 security.acls=sg-one"); len(got) != 1 {
		t.Errorf("the enforceable interface must still get its rule set, got:\n%s",
			strings.Join(f.commands(), "\n"))
	}
}

// TestApplyFirewallDetachIgnoresARoutedNIC holds the other direction: no rule
// set has ever been attached to a routed NIC, so an empty binding — the
// detach-all every permissive group now becomes — has nothing to take back
// there and must not fail over it.
func TestApplyFirewallDetachIgnoresARoutedNIC(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/srv": mixedNICs,
		"/1.0/networks/":     `{"type": "bridge"}`,
	}}
	d := newFakeDriver(f)

	if err := d.ApplyFirewall(context.Background(), "srv", FirewallBinding{}); err != nil {
		t.Fatalf("detaching must succeed on a machine with a routed NIC, got %v", err)
	}
	for _, cmd := range f.matching("security.acls") {
		if strings.Contains(cmd, " eth0 ") {
			t.Errorf("the routed NIC has nothing to detach and must not be touched: %q", cmd)
		}
	}
	if got := f.matching("config device set srv eth1 security.acls="); len(got) != 1 {
		t.Errorf("the managed interface must still be detached, got:\n%s",
			strings.Join(f.commands(), "\n"))
	}
}

// And the accepting half. A guard that refused everything would pass the test
// above and leave the product unable to attach a security group at all.
func TestApplyFirewallStillWorksOnOurOwnInstance(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/srv": twoNICs,
		"/1.0/networks/":     `{"type": "bridge"}`,
	}}
	d := newFakeDriver(f)

	if err := d.ApplyFirewall(context.Background(), "srv", FirewallBinding{
		Names:          []string{"sg-one"},
		DefaultIngress: "drop",
		DefaultEgress:  "allow",
	}); err != nil {
		t.Fatalf("the guard refused an instance the emulator created: %v", err)
	}
	var applied bool
	for _, command := range f.commands() {
		if strings.Contains(command, "security.acls") {
			applied = true
		}
	}
	if !applied {
		t.Errorf("no rule set was applied:\n%s", strings.Join(f.commands(), "\n"))
	}
}

// The rest of this file is #454: a security group that carried one ICMP rule
// with an IPv6 source lost its *whole* firewall, because the protocol was
// chosen from the rule's name alone and the daemon refuses icmp4 beside an IPv6
// address — and the rule set is written in one PUT.

// aclDaemon is a runtime that validates a rule set the way incusd does, so a
// test measures whether the write is *accepted* rather than how it is spelled.
//
// This is the planted witness rule 1 of measurement-integrity asks for: without
// it, a test asserting "the body holds two rules" would pass on a body the real
// daemon rejects, which is exactly the state this defect shipped in. The one
// refusal reproduced here is the one the issue recorded verbatim:
//
//	Invalid ingress rule 1: Cannot use IPv6 source addresses with "icmp4" protocol
type aclDaemon struct {
	fakeRuntime
	// written holds the last body each rule set was written with.
	written map[string]aclBody
}

func newACLDaemon() *aclDaemon {
	d := &aclDaemon{written: map[string]aclBody{}}
	d.hook = d.answer
	return d
}

func (a *aclDaemon) answer(_ int, args []string) ([]byte, error, bool) {
	if len(args) < 6 || args[0] != "query" || args[2] != "PUT" {
		return nil, nil, false
	}
	name := strings.TrimPrefix(args[5], "/1.0/network-acls/")
	var body aclBody
	if err := json.Unmarshal([]byte(args[4]), &body); err != nil {
		return nil, errors.New("incus query: Error: not JSON"), true
	}
	for direction, rules := range map[string][]aclRule{"ingress": body.Ingress, "egress": body.Egress} {
		for i, rule := range rules {
			if err := validateLikeIncus(direction, i, rule); err != nil {
				return nil, err, true
			}
		}
	}
	a.written[name] = body
	return []byte("{}"), nil, true
}

// validateLikeIncus refuses what the daemon refuses: an ICMP protocol beside an
// address of the other family. Quoted shape, not invented — see the issue.
func validateLikeIncus(direction string, index int, rule aclRule) error {
	want := map[string]bool{"icmp4": true, "icmp6": false}
	wantV4, isICMP := want[rule.Protocol]
	if !isICMP {
		return nil
	}
	for field, value := range map[string]string{"source": rule.Source, "destination": rule.Destination} {
		for _, member := range strings.Split(value, ",") {
			member = strings.TrimSpace(member)
			if member == "" {
				continue
			}
			prefix, err := netip.ParsePrefix(member)
			if err != nil {
				addr, addrErr := netip.ParseAddr(member)
				if addrErr != nil {
					return fmt.Errorf("incus query: Error: Invalid %s rule %d: Invalid %s %q",
						direction, index, field, member)
				}
				prefix = netip.PrefixFrom(addr, addr.BitLen())
			}
			if prefix.Addr().Is4() != wantV4 {
				return fmt.Errorf("incus query: Error: Invalid %s rule %d: Cannot use %s %s addresses with %q protocol",
					direction, index, familyOf(prefix), field, rule.Protocol)
			}
		}
	}
	return nil
}

func familyOf(prefix netip.Prefix) string {
	if prefix.Addr().Is4() {
		return "IPv4"
	}
	return "IPv6"
}

// witnessGroup is the reduced witness of the replay campaign: a group holding
// TCP/22 from anywhere, optionally plus one ICMP rule from the block given.
func witnessGroup(name, icmpFrom string) FirewallSpec {
	spec := FirewallSpec{
		Name:           name,
		DefaultIngress: "drop",
		DefaultEgress:  "allow",
		Rules: []FirewallRule{
			{Direction: "ingress", Action: "allow", Protocol: "tcp", Source: "0.0.0.0/0", PortFrom: 22, PortTo: 22},
		},
	}
	if icmpFrom != "" {
		spec.Rules = append(spec.Rules,
			FirewallRule{Direction: "ingress", Action: "allow", Protocol: "icmp", Source: icmpFrom})
	}
	return spec
}

// TestAnICMPRuleWithAnIPv6SourceKeepsItsGroup is the test the issue asked for.
//
// Red before the fix: the body carried icmp4 beside ::/0, the daemon refused the
// PUT, and the TCP rule standing next to the ICMP one was lost with it — the API
// went on describing two rules while the host enforced one.
func TestAnICMPRuleWithAnIPv6SourceKeepsItsGroup(t *testing.T) {
	daemon := newACLDaemon()
	d := newFakeDriver(&daemon.fakeRuntime)

	if err := d.EnsureFirewall(context.Background(), witnessGroup("scw-dual", "::/0")); err != nil {
		t.Fatalf("the daemon refused the rule set: %v", err)
	}

	body, written := daemon.written["scw-dual"]
	if !written {
		t.Fatal("no rule set reached the daemon")
	}
	if len(body.Ingress) != 2 {
		t.Fatalf("the group must keep both of its rules, got %d: %+v", len(body.Ingress), body.Ingress)
	}
	if got := body.Ingress[0]; got.Protocol != "tcp" || got.DestinationPort != "22" {
		t.Errorf("the TCP rule beside the ICMP one was altered: %+v", got)
	}
	if got := body.Ingress[1]; got.Protocol != "icmp6" || got.Source != "::/0" {
		t.Errorf("an ICMP rule from an IPv6 block must be written as icmp6, got %+v", got)
	}
}

// TestTwoGroupsDifferingByOneRuleEachEnforceWhatTheyDescribe reproduces the
// campaign's own reduced witness, at the layer that has a seam for it.
//
// Two groups, identical but for one rule, so the difference names the cause and
// nothing else. Measured under --vm incus-ovn before the fix: the API described
// 1 rule and 2 rules, the host enforced 1 and 1.
func TestTwoGroupsDifferingByOneRuleEachEnforceWhatTheyDescribe(t *testing.T) {
	daemon := newACLDaemon()
	d := newFakeDriver(&daemon.fakeRuntime)

	control := witnessGroup("scw-control", "")
	dual := witnessGroup("scw-dual", "::/0")
	for _, spec := range []FirewallSpec{control, dual} {
		if err := d.EnsureFirewall(context.Background(), spec); err != nil {
			t.Fatalf("%s: the daemon refused the rule set: %v", spec.Name, err)
		}
	}

	for _, spec := range []FirewallSpec{control, dual} {
		body, written := daemon.written[spec.Name]
		if !written {
			t.Fatalf("%s: nothing reached the daemon", spec.Name)
		}
		// What the API describes is len(spec.Rules); what the host enforces is
		// what the accepted body holds. Every rule must be represented — the
		// count can be higher, since a family-agnostic ICMP rule becomes two.
		if len(body.Ingress) < len(spec.Rules) {
			t.Errorf("%s: the API describes %d rules and the host holds %d: %+v",
				spec.Name, len(spec.Rules), len(body.Ingress), body.Ingress)
		}
	}

	// And the difference between the two groups is exactly one rule, which is
	// what makes this witness name a cause rather than a symptom.
	if got := len(daemon.written["scw-dual"].Ingress) - len(daemon.written["scw-control"].Ingress); got != 1 {
		t.Fatalf("the two groups must differ by exactly one enforced rule, got %d", got)
	}
}

// TestADroppedRuleIsReported holds the backstop's second half. Dropping a rule
// the runtime cannot express is right; dropping it in silence is what let the
// contract's own promise — "visibly absent" — go unkept for as long as it did.
func TestADroppedRuleIsReported(t *testing.T) {
	var log bytes.Buffer
	daemon := newACLDaemon()
	d := newFakeDriver(&daemon.fakeRuntime)
	d.Log = slog.New(slog.NewTextHandler(&log, nil))

	spec := witnessGroup("scw-mixed", "")
	spec.Rules = append(spec.Rules,
		// Both families in one rule: no ICMP protocol covers it.
		FirewallRule{Direction: "ingress", Action: "allow", Protocol: "icmp", Source: "10.0.0.0/8,::/0"})
	if err := d.EnsureFirewall(context.Background(), spec); err != nil {
		t.Fatalf("one inexpressible rule must not cost the write: %v", err)
	}

	if got := len(daemon.written["scw-mixed"].Ingress); got != 1 {
		t.Fatalf("the expressible rule must survive alone, got %d rules", got)
	}
	line := log.String()
	if !strings.Contains(line, "level=WARN") {
		t.Fatalf("a dropped rule must be reported, got %q", line)
	}
	if !strings.Contains(line, "scw-mixed") || !strings.Contains(line, "10.0.0.0/8,::/0") {
		t.Fatalf("the report must name the rule set and the rule, got %q", line)
	}

	// The accepting half: a group whose rules are all expressible says nothing.
	log.Reset()
	if err := d.EnsureFirewall(context.Background(), witnessGroup("scw-plain", "::/0")); err != nil {
		t.Fatalf("plain group: %v", err)
	}
	if strings.Contains(log.String(), "level=WARN") {
		t.Fatalf("a group with nothing dropped must stay quiet, got %q", log.String())
	}
}

// TestAFirewallWriteTheHostRefusesWithdrawsTheCapability is the half that holds
// whatever the translation did not foresee.
//
// A refused write used to leave `/_feint/health` publishing
// `capabilities.firewall: true` while the host enforced nothing of that group —
// a lying 200 in the security plane, and the thing this project exists to
// refuse. What the host answered wins over what the flag promised (#181), and a
// refusal is the host answering.
func TestAFirewallWriteTheHostRefusesWithdrawsTheCapability(t *testing.T) {
	f := &fakeRuntime{fail: map[string]error{
		"-X PUT": errors.New(`incus query: Error: Invalid ingress rule 1: something new`),
	}}
	var log bytes.Buffer
	d := newFakeDriver(f)
	d.Log = slog.New(slog.NewTextHandler(&log, nil))

	// The accepting half first, and it matters: a driver that published
	// firewall=false from the start would pass the assertion below and claim
	// nothing at all.
	if !d.Capabilities().Firewall {
		t.Fatal("the Incus driver must claim the firewall before anything refuses it")
	}

	if err := d.EnsureFirewall(context.Background(), witnessGroup("scw-refused", "")); err == nil {
		t.Fatal("the refusal must reach the caller")
	}
	if d.Capabilities().Firewall {
		t.Error("the host refused a rule set and the process still claims to enforce firewalls")
	}
	if !strings.Contains(log.String(), "capabilities.firewall") {
		t.Errorf("the withdrawal must be said, not only published, got %q", log.String())
	}

	// A name this driver refused itself never reached the daemon, so it is not
	// the host denying anything — and the name comes from a restorable snapshot,
	// which would otherwise hand a crafted state file a switch on a published
	// claim.
	clean := newFakeDriver(&fakeRuntime{})
	if err := clean.EnsureFirewall(context.Background(), FirewallSpec{Name: "-oops"}); err == nil {
		t.Fatal("an unsafe rule-set name must still be refused")
	}
	if !clean.Capabilities().Firewall {
		t.Error("a refusal by this driver's own guard must not withdraw the host's capability")
	}
}

// A machine whose groups enforce nothing attaches nothing — but on an OVN
// network that carries the emulator's isolation set, "nothing" is not neutral:
// the network ACL forces the reject default onto every NIC, so a bare detach
// closes the machine entirely. The NIC wears the permissive posture set
// instead, whose catch-all allows at 300 stay under the isolation's rejects at
// 400: open to the station, closed to the foreign subnets, which is what a
// group that filters nothing means there (#491).
func TestAMachineWithoutAGroupStaysOpenOnAnIsolatedNetwork(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/srv":     twoNICs,
		"/1.0/networks/scw-abc":  `{"type": "ovn", "config": {"security.acls": "iso-fnt-abc"}}`,
		"/1.0/networks/incusbr0": `{"type": "bridge"}`,
	}}
	d := newFakeDriver(f)
	d.OVN = true

	if err := d.ApplyFirewall(context.Background(), "srv", FirewallBinding{}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	ovn := f.matching(" eth1 security.acls=")
	if len(ovn) != 1 || !strings.Contains(ovn[0], "security.acls=opn-fnt") {
		t.Fatalf("the OVN interface of an isolated network must wear the permissive set, got %v", ovn)
	}
	if got := f.matching("/1.0/network-acls/opn-fnt"); len(got) == 0 {
		t.Error("the permissive set was attached without being written first")
	}
	// The bridged interface keeps the plain detach: no network ACL forces a
	// default onto it.
	for _, cmd := range f.matching(" eth0 security.acls=") {
		if strings.Contains(cmd, "opn-fnt") {
			t.Errorf("a bridged interface has no business wearing the permissive set: %q", cmd)
		}
	}
}

// The other half of the invariant: on an OVN network that carries no ACL, an
// empty binding still clears the interface, exactly as before — a NIC with no
// rule set on a bare network filters nothing, and attaching the permissive set
// there would hand its egress catch-all to the sender's side of the pipeline,
// opening a restrictive same-subnet neighbour that today filters faithfully.
func TestAnEmptyBindingStillClearsANICOnAnUnisolatedNetwork(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/srv":     twoNICs,
		"/1.0/networks/scw-abc":  `{"type": "ovn", "config": {}}`,
		"/1.0/networks/incusbr0": `{"type": "bridge"}`,
	}}
	d := newFakeDriver(f)
	d.OVN = true

	if err := d.ApplyFirewall(context.Background(), "srv", FirewallBinding{}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	ovn := f.matching(" eth1 security.acls=")
	if len(ovn) != 1 || strings.Contains(ovn[0], "opn-fnt") {
		t.Fatalf("an empty binding on an unisolated network must clear the interface, got %v", ovn)
	}
	if got := f.matching("/1.0/network-acls/opn-fnt"); len(got) != 0 {
		t.Errorf("the permissive set has no business existing here: %v", got)
	}
}

// A restrictive binding is never replaced by the permissive set, isolation or
// not: the groups' own rule sets are what the machine wears, and the reject
// default the network ACL forces is exactly the group's default-deny.
func TestARestrictiveBindingKeepsItsGroupsOnAnIsolatedNetwork(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/srv":     twoNICs,
		"/1.0/networks/scw-abc":  `{"type": "ovn", "config": {"security.acls": "iso-fnt-abc"}}`,
		"/1.0/networks/incusbr0": `{"type": "bridge"}`,
	}}
	d := newFakeDriver(f)
	d.OVN = true

	if err := d.ApplyFirewall(context.Background(), "srv", FirewallBinding{
		Names:          []string{"sg-one"},
		DefaultIngress: "drop",
		DefaultEgress:  "allow",
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	ovn := f.matching(" eth1 security.acls=")
	if len(ovn) != 1 || !strings.Contains(ovn[0], "security.acls=sg-one") {
		t.Fatalf("the group must reach the interface unchanged, got %v", ovn)
	}
	if got := f.matching("opn-fnt"); len(got) != 0 {
		t.Errorf("the permissive set has no business near a restrictive binding: %v", got)
	}
}

// TestTheUnenforceableRefusalNamesTheAddressThatEscapes is #548. The machine
// under test is the one the Scaleway stack produces when the flexible IP is
// attached at creation: a routed eth0 carrying the published address, and a
// private eth1 on a managed network that takes the rule set. Measured on
// 2026-08-27 under `--vm incus-ovn`, port 22 answered on 203.0.113.2 from the
// station while the group's inbound default was drop and only 443 was allowed
// — and the same port on the private address was refused, which is the
// negative control that tells the two interfaces apart.
//
// The refusal already existed; what it did not do was say *what* escapes. An
// operator reading "eth0" learns nothing they can check, because they cannot
// see the machine's devices from the API. The address is the fact they can
// act on: it is the one a client is about to connect to.
//
// Three halves, so a guard that refuses everything cannot pass here: the
// enforceable interface still receives its rule set, no security key is ever
// sent to the routed one, and the refusal carries both the interface and the
// address.
func TestTheUnenforceableRefusalNamesTheAddressThatEscapes(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/srv": mixedNICs,
		"/1.0/networks/":     `{"type": "bridge"}`,
	}}
	d := newFakeDriver(f)

	err := d.ApplyFirewall(context.Background(), "srv", FirewallBinding{
		Names:          []string{"sg-one"},
		DefaultIngress: "drop",
		DefaultEgress:  "allow",
	})
	if !errors.Is(err, ErrFirewallUnenforceable) {
		t.Fatalf("a rule set on a routed NIC must be refused with the typed error, got %v", err)
	}
	for _, want := range []string{"eth0", "203.0.113.7"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q, so nobody can check it: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "eth1") {
		t.Errorf("the refusal names the interface that *is* covered: %v", err)
	}
	if got := f.matching("config device set srv eth1 security.acls=sg-one"); len(got) != 1 {
		t.Errorf("the enforceable interface must still get its rule set, got:\n%s",
			strings.Join(f.commands(), "\n"))
	}
}

// TestTheUnenforceableRefusalNamesAnAddressRoutedAfterTheLaunch holds the half
// carriedAddresses exists for: a public address attached to a running machine
// lands in the routed NIC's ipv4.routes, not in the ipv4.address list the
// launch wrote. A refusal reading only the launch key would be silent about
// exactly the addresses a client adds afterwards.
func TestTheUnenforceableRefusalNamesAnAddressRoutedAfterTheLaunch(t *testing.T) {
	const lateAddress = `{
  "expanded_devices": {
    "eth0": {"type": "nic", "nictype": "routed", "ipv4.address": "203.0.113.7", "ipv4.routes": "203.0.113.9/32"}
  },
  "devices": {
    "eth0": {"type": "nic", "nictype": "routed", "ipv4.address": "203.0.113.7", "ipv4.routes": "203.0.113.9/32"}
  }
}`
	f := &fakeRuntime{answers: map[string]string{"/1.0/instances/srv": lateAddress}}
	d := newFakeDriver(f)

	err := d.ApplyFirewall(context.Background(), "srv", FirewallBinding{
		Names:          []string{"sg-one"},
		DefaultIngress: "drop",
		DefaultEgress:  "allow",
	})
	if !errors.Is(err, ErrFirewallUnenforceable) {
		t.Fatalf("a rule set on a routed NIC must be refused with the typed error, got %v", err)
	}
	if !strings.Contains(err.Error(), "203.0.113.9") {
		t.Fatalf("the address routed after the launch escapes unnamed: %v", err)
	}
}
