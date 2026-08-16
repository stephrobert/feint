package machine

import (
	"context"
	"errors"
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

func TestToACLRule(t *testing.T) {
	cases := []struct {
		name string
		in   FirewallRule
		want aclRule
		ok   bool
	}{
		{
			name: "an ingress rule keeps its source and its port range",
			in:   FirewallRule{Direction: "ingress", Action: "allow", Protocol: "TCP", Source: "10.0.0.0/8", PortFrom: 80, PortTo: 90},
			want: aclRule{Action: "allow", State: "enabled", Protocol: "tcp", Source: "10.0.0.0/8", DestinationPort: "80-90"},
			ok:   true,
		},
		{
			name: "a single port is not written as a range",
			in:   FirewallRule{Direction: "ingress", Action: "allow", Protocol: "tcp", PortFrom: 22, PortTo: 22},
			want: aclRule{Action: "allow", State: "enabled", Protocol: "tcp", DestinationPort: "22"},
			ok:   true,
		},
		{
			name: "no port means every port, not port zero",
			in:   FirewallRule{Direction: "ingress", Action: "allow", Protocol: "tcp"},
			want: aclRule{Action: "allow", State: "enabled", Protocol: "tcp"},
			ok:   true,
		},
		{
			name: "icmp carries no port, which the runtime rejects on it",
			in:   FirewallRule{Direction: "ingress", Action: "allow", Protocol: "ICMP", PortFrom: 22},
			want: aclRule{Action: "allow", State: "enabled", Protocol: "icmp4"},
			ok:   true,
		},
		{
			name: "an empty protocol means any, and takes no port either",
			in:   FirewallRule{Direction: "egress", Action: "drop", Destination: "0.0.0.0/0", PortFrom: 53},
			want: aclRule{Action: "drop", State: "enabled", Destination: "0.0.0.0/0"},
			ok:   true,
		},
		{
			name: "a stateless allow survives translation rather than becoming stateful",
			in:   FirewallRule{Direction: "ingress", Action: "allow-stateless", Protocol: "udp", PortFrom: 53},
			want: aclRule{Action: "allow-stateless", State: "enabled", Protocol: "udp", DestinationPort: "53"},
			ok:   true,
		},
		{
			name: "an action the runtime does not know is dropped, not approximated",
			in:   FirewallRule{Direction: "ingress", Action: "log", Protocol: "tcp"},
			ok:   false,
		},
		{
			name: "a protocol the runtime does not know is dropped too",
			in:   FirewallRule{Direction: "ingress", Action: "allow", Protocol: "sctp"},
			ok:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := toACLRule(tc.in)
			if ok != tc.ok {
				t.Fatalf("acceptance: got %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if got != tc.want {
				t.Fatalf("got  %+v\nwant %+v", got, tc.want)
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
