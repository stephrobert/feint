package outscale_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/store/storetest"
	"github.com/stephrobert/feint/internal/providers/outscale"
)

// A terminated Vm's private address returns to its Subnet.
//
// deleteVms says it itself: upstream keeps the private address of a stopped
// Vm and releases it at termination. The allocator did only half — a
// terminated Vm stayed in the store for ReadVms polling, and its address
// stayed reserved with it, forever. A Subnet slowly filled with ghosts
// nothing could release, which no real cloud does; on a /29 it takes three
// machines to reach the lie.
//
// The sweep at the end is the other half of the story: this exact reuse is
// what the state-blind invariant used to report as a double allocation, the
// false verdict that got the first version of this fix reverted. The sweep
// runs here with the pack's own word on liveness, so this test holds the
// measure and the measured together and red flags whichever regresses.
func TestATerminatedVmsAddressReturnsToItsSubnet(t *testing.T) {
	ts, st := newOutscaleBarrageServer(t)

	// A /29 holds three usable addresses: eight, minus the four Outscale
	// reserves at the head, minus the broadcast.
	_, subnetID := netAndSubnet(t, ts, "10.60.0.0/16", "10.60.1.0/29")

	create := func() (int, string, string) {
		status, out := postRaw(ts, "CreateVms",
			fmt.Sprintf(`{"ImageId":"ami-00000001","SubnetId":%q,"BootOnCreation":false}`, subnetID))
		vms, _ := out["Vms"].([]any)
		if len(vms) == 0 {
			return status, "", ""
		}
		vm, _ := vms[0].(map[string]any)
		id, _ := vm["VmId"].(string)
		address, _ := vm["PrivateIp"].(string)
		return status, id, address
	}

	// Fill the Subnet.
	ids := make([]string, 0, 3)
	addresses := make([]string, 0, 3)
	for i := range 3 {
		status, id, address := create()
		if status != http.StatusOK || address == "" {
			t.Fatalf("create %d answered %d with address %q; the /29 should hold three", i, status, address)
		}
		ids = append(ids, id)
		addresses = append(addresses, address)
	}

	// The fourth is refused: the pool is genuinely empty.
	if status, _, _ := create(); status == http.StatusOK {
		t.Fatal("a fourth Vm was placed in a full /29")
	}

	// Terminate one. The record stays readable — that is deliberate — but the
	// address it held is free again.
	if status, _ := postRaw(ts, "DeleteVms", fmt.Sprintf(`{"VmIds":[%q]}`, ids[0])); status != http.StatusOK {
		t.Fatalf("DeleteVms answered %d", status)
	}
	status, _, reused := create()
	if status != http.StatusOK {
		t.Fatalf("after a termination the Subnet still refuses a create (%d): the dead reservation is back", status)
	}
	if reused != addresses[0] {
		t.Errorf("the freed address was %s and the new Vm got %s", addresses[0], reused)
	}

	// And the invariant agrees, with the pack's own word on liveness: the
	// terminated record still carries the address string, and holds nothing.
	if found := storetest.Sweep(st.All(), outscale.Gone); len(found) != 0 {
		t.Errorf("the sweep convicts the legitimate reuse:\n%s", strings.Join(found, "\n"))
	}
}
