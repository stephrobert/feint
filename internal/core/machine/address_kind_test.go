package machine

import (
	"context"
	"net/netip"
	"testing"

	"github.com/stephrobert/feint/internal/core/resource"
)

// Which address a machine "answers on" was a guess until #548, and the guess
// lived in two places at once: the driver picked one address off the
// lowest-named interface, and the layer above then decided what kind it was
// from the pack's declared public block. The two agreed only while a routed
// NIC sorted before a managed one — which is exactly the arrangement the
// address migration ends, since it puts both addresses on one interface.
//
// The measurement that settles it, 2026-08-28 under `--vm incus-ovn`, on a
// Scaleway server created with its flexible IP whose private NIC arrived
// afterwards and whose address was then migrated onto it:
//
//	{"eth0":[],"eth1":["10.199.0.2","203.0.113.2"]}   stable over three reads
//
// The old reading answered 10.199.0.2 — the private address — where it had
// answered 203.0.113.2, for three packs at once, and Exoscale publishes that
// value as `public-ip`.

// twoAddressesOnOneInterface is that machine, as `incus list --format json`
// describes it: nothing on the routed NIC any more, both addresses on the
// managed one, the private one listed first.
const twoAddressesOnOneInterface = `[{
  "name": "feint-scw-mig",
  "status": "Running",
  "state": {"network": {
    "lo":   {"addresses": [{"family": "inet", "address": "127.0.0.1", "scope": "local"}]},
    "eth0": {"addresses": []},
    "eth1": {"addresses": [
      {"family": "inet", "address": "10.199.0.2",  "scope": "global"},
      {"family": "inet", "address": "203.0.113.2", "scope": "global"},
      {"family": "inet6", "address": "fd42::2",    "scope": "global"}
    ]}
  }}
}]`

// TestInspectReportsEveryAddressTheMachineCarries: the driver answers the set
// and chooses nothing, so no reader downstream can inherit a choice nobody
// made.
func TestInspectReportsEveryAddressTheMachineCarries(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"list feint-scw-mig": twoAddressesOnOneInterface,
	}}
	d := newFakeDriver(f)

	m, ok, err := d.Inspect(context.Background(), "feint-scw-mig")
	if err != nil || !ok {
		t.Fatalf("inspect: ok=%v err=%v", ok, err)
	}
	if len(m.Addresses) != 2 {
		t.Fatalf("addresses = %v, want both globals of the interface", m.Addresses)
	}
	// Both, in the order the runtime listed them within an interface, and the
	// interfaces themselves in name order: a map has none, and an answer that
	// changed between two reads of one machine would be worse than a guess.
	if m.Addresses[0] != "10.199.0.2" || m.Addresses[1] != "203.0.113.2" {
		t.Errorf("addresses = %v, want [10.199.0.2 203.0.113.2]", m.Addresses)
	}
	if !m.Running {
		t.Errorf("the machine reads as not running")
	}
}

// And the layer picks out of that set by the pack's own declaration, which is
// the half that must not depend on which address came first.
func TestTheKindOfAnAddressIsTheBlocksAnswerNotTheOrders(t *testing.T) {
	block := netip.MustParsePrefix("203.0.113.0/24")
	for name, recorded := range map[string]string{
		"private first": "10.199.0.2,203.0.113.2",
		"public first":  "203.0.113.2,10.199.0.2",
	} {
		t.Run(name, func(t *testing.T) {
			r := Reconciler{
				Groups:      GroupSync{Binding: Binding{AddressKey: "address"}},
				PlanOf:      func(*resource.Resource) Plan { return Plan{} },
				PublicBlock: block,
			}
			res := &resource.Resource{ID: "srv", Runtime: map[string]string{"address": recorded}}

			if got := r.PublicAddressOf(res); got != "203.0.113.2" {
				t.Errorf("public = %q, want 203.0.113.2", got)
			}
			if got := r.PrivateAddressOf(res); got != "10.199.0.2" {
				t.Errorf("private = %q, want 10.199.0.2", got)
			}
		})
	}
}

// TestARestoredAddressThatIsNotAnAddressIsNotPublished: the recorded value
// comes back from Resource.Runtime, which a restored snapshot controls
// verbatim, and both readers above hand what they find to a client. Only the
// public half ever went through a parser, so a crafted entry reached
// Outscale's PrivateIp untouched.
func TestARestoredAddressThatIsNotAnAddressIsNotPublished(t *testing.T) {
	r := Reconciler{
		Groups:      GroupSync{Binding: Binding{AddressKey: "address"}},
		PlanOf:      func(*resource.Resource) Plan { return Plan{} },
		PublicBlock: netip.MustParsePrefix("203.0.113.0/24"),
	}
	res := &resource.Resource{ID: "srv", Runtime: map[string]string{
		"address": "<script>alert(1)</script>,not-an-address,10.199.0.2",
	}}

	if got := r.PublicAddressOf(res); got != "" {
		t.Errorf("public = %q, want nothing: no entry is inside the block", got)
	}
	// The one entry that parses is the one that is published, and the two that
	// do not never reach a field a client reads.
	if got := r.PrivateAddressOf(res); got != "10.199.0.2" {
		t.Errorf("private = %q, want 10.199.0.2", got)
	}
}

// TestABootRecordsEveryAddressTheReplayInstalled: the record a boot leaves is
// written before the replay installs the addresses the plan promised, so it is
// older than the machine by exactly those. Measured 2026-08-28 under
// `--vm incus-ovn`, on a server rebooted through the API once its public
// address had moved onto its private NIC: the store came back `10.199.0.2`
// alone while the station reached 203.0.113.2 on that machine in the same
// pass, and a pack asking the layer for a public address would have been told
// there was none.
//
// What the recorder can hold is the read itself and its two directions, since
// the double answers one address per machine and cannot stage a set that grows
// mid-boot. The runtime half is the measurement above, re-read after the fix:
// `10.199.0.2,203.0.113.2`.
func TestABootRecordsEveryAddressTheReplayInstalled(t *testing.T) {
	b := newGroupSyncBench()
	vm := b.machine("m", "")
	r := reconcilerBench(b, Plan{Publics: []string{"203.0.113.9"}})

	if !r.PowerOn(context.Background(), vm, Boot{Image: "ubuntu:24.04"}) {
		t.Fatalf("the boot did not start; sequence: %v", b.rec.Sequence())
	}
	if got := vm.Runtime["address"]; got == "" {
		t.Fatalf("the boot recorded no address at all: %v", vm.Runtime)
	}

	// A record older than the machine is replaced by what the runtime holds.
	vm.Runtime["address"] = "10.0.0.254"
	if !r.binding().Rescan(context.Background(), vm) {
		t.Fatalf("a stale record was not re-read: %v", vm.Runtime)
	}
	if got := vm.Runtime["address"]; got == "10.0.0.254" {
		t.Errorf("the record still holds what nobody answered: %q", got)
	}

	// And a runtime with nothing to say leaves the record alone, which is what
	// a virtual machine still booting looks like: overwriting there would
	// erase what an earlier read had learned.
	before := vm.Runtime["address"]
	vm.Runtime["machine"] = ""
	if r.binding().Rescan(context.Background(), vm) {
		t.Errorf("a machine the runtime cannot answer for had its record overwritten")
	}
	if vm.Runtime["address"] != before {
		t.Errorf("the record moved to %q, want %q kept", vm.Runtime["address"], before)
	}
}
