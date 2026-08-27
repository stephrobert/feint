package machine

import (
	"context"
	"strings"
	"testing"
)

// Where a security group applies is a provider fact, and until 2026-08-27 this
// driver had no way to be told it (#574).
//
// Exoscale states the divergence in one sentence — "Security group rules do
// not apply to traffic inside private networks" — and the emulator applied
// them anyway, onto the very NIC that carries the private lease. The `default`
// group has no ingress rule, so its rule set translates to a drop default: two
// instances of one private network measured 0/10 probes through, and 10/10
// with the rule set stripped off the NIC by hand, under `--vm incus`.
//
// Under `--vm incus-ovn` the same wrong write did not bite, because the
// sender's catch-all egress allow sits at priority 300 and outranks the
// receiver's NIC default deny at 100/111 (#491). That is an accident of rule
// ordering, so the assertions below are about the *commands emitted*, never
// about a connection succeeding: an argument-level fact is the only one that
// distinguishes "the rule is absent" from "the rule is present and outranked".

// unfilteredNICs is a machine with an interface on a network the pack covers
// and one on a network it does not — the shape an Exoscale instance has the
// moment it joins a private network.
const unfilteredNICs = `{
  "expanded_devices": {
    "eth0": {"type": "nic", "network": "fnt-default"},
    "eth1": {"type": "nic", "network": "fnt-privnet"}
  },
  "devices": {
    "eth0": {"type": "nic", "network": "fnt-default"},
    "eth1": {"type": "nic", "network": "fnt-privnet"}
  }
}`

func TestNoRuleSetIsBoundToAnUnfilteredInterface(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/srv": unfilteredNICs,
		"/1.0/networks/":     `{"type": "bridge"}`,
	}}
	d := newFakeDriver(f)

	if err := d.ApplyFirewall(context.Background(), "srv", FirewallBinding{
		Names:          []string{"exo-one"},
		DefaultIngress: "drop",
		DefaultEgress:  "allow",
		Unfiltered:     []string{"fnt-privnet"},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// The refusing half: nothing the driver emitted names a rule set on the
	// membership interface. Asserted over every command rather than over the
	// one the test expects to find, which is the difference between "the set
	// is not there" and "the set is not where I looked".
	for _, cmd := range f.matching(" eth1 ") {
		if strings.Contains(cmd, "exo-one") {
			t.Errorf("a rule set reached the unfiltered interface: %q", cmd)
		}
		if strings.Contains(cmd, "security.acls.default") {
			t.Errorf("a default action reached the unfiltered interface: %q", cmd)
		}
	}
	// It is emptied, not skipped: a rule set an earlier version of this code
	// already wrote onto the NIC has to come off, or restarting the machine
	// that the fix repairs would keep the defect alive.
	cleared := f.matching(" eth1 security.acls=")
	if len(cleared) != 1 || !strings.HasSuffix(cleared[0], "security.acls=") {
		t.Fatalf("the unfiltered interface must be cleared exactly once, got %v", cleared)
	}

	// The accepting half, and it is what stops this from being a guard that
	// refuses everything: the covered interface still wears the set and its
	// defaults. Scaleway and Outscale filter their private NICs, their suites
	// assert it, and nothing here may take that away.
	covered := f.matching(" eth0 security.acls=")
	if len(covered) != 1 || !strings.Contains(covered[0], "security.acls=exo-one") {
		t.Fatalf("the covered interface must wear the rule set, got %v", covered)
	}
	if !strings.Contains(covered[0], "security.acls.default.ingress.action=drop") {
		t.Errorf("the covered interface must keep its default actions, got %q", covered[0])
	}
}

// Under OVN an unfiltered interface must end up *open inside its own segment*,
// which is not the same as "no key written".
//
// A network carrying the emulator's isolation set forces the reject default
// onto every NIC attached to it (a network's security.acls "apply to NICs
// connected to this network"). Clearing the NIC and stopping there would close
// the very segment this change exists to reopen, so the set-less NIC wears the
// permissive posture instead — whose catch-all allows at 300 still lose to the
// isolation's rejects at 400, so two private networks stay apart.
func TestAnUnfilteredInterfaceStaysOpenOnAnIsolatedNetwork(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/srv":        unfilteredNICs,
		"/1.0/networks/fnt-privnet": `{"type": "ovn", "config": {"security.acls": "iso-fnt-privnet"}}`,
		"/1.0/networks/fnt-default": `{"type": "ovn", "config": {}}`,
	}}
	d := newFakeDriver(f)
	d.OVN = true

	if err := d.ApplyFirewall(context.Background(), "srv", FirewallBinding{
		Names:          []string{"exo-one"},
		DefaultIngress: "drop",
		DefaultEgress:  "allow",
		Unfiltered:     []string{"fnt-privnet"},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	membership := f.matching(" eth1 security.acls=")
	if len(membership) != 1 || !strings.Contains(membership[0], "security.acls=opn-fnt") {
		t.Fatalf("the unfiltered interface of an isolated network must wear the permissive set, got %v", membership)
	}
	if got := f.matching("/1.0/network-acls/opn-fnt"); len(got) == 0 {
		t.Error("the permissive set was attached without being written first")
	}
	for _, cmd := range f.matching(" eth1 ") {
		if strings.Contains(cmd, "exo-one") {
			t.Errorf("the group still reached the unfiltered interface under OVN: %q", cmd)
		}
	}
	if covered := f.matching(" eth0 security.acls=exo-one"); len(covered) != 1 {
		t.Fatalf("the covered interface must still wear the group under OVN, got %v", covered)
	}
}

// A binding that declares no scope is the binding two of the three packs send,
// and it must reach every interface exactly as it did before #574. Without
// this the change would be indistinguishable from one that stopped filtering
// altogether.
func TestABindingThatDeclaresNoScopeCoversEveryInterface(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/srv": unfilteredNICs,
		"/1.0/networks/":     `{"type": "bridge"}`,
	}}
	d := newFakeDriver(f)

	if err := d.ApplyFirewall(context.Background(), "srv", FirewallBinding{
		Names:          []string{"scw-one"},
		DefaultIngress: "drop",
		DefaultEgress:  "allow",
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	applied := f.matching("security.acls=scw-one")
	if len(applied) != 2 {
		t.Fatalf("both interfaces must wear the rule set, got %d: %v", len(applied), applied)
	}
}
