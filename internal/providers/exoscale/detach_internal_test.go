package exoscale

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/resource"
)

// TestDetachingAnInstanceTakesTheDeviceOffTheRuntime closes, on this pack, the
// window #426 measured on Scaleway.
//
// This handler used to say in a comment that it could not do it: "the machine
// keeps its interface until it stops, because the driver deliberately has no
// hot-unplug ... the same window the Scaleway NIC has". That window is what
// leaves a device on a container, which keeps its network alive, which makes
// RemoveNetwork answer "The network is currently in use" — and the bridge then
// outlives the run holding its address block, failing the next one on "Address
// already in use".
//
// The driver has Detach now. This asserts the pack uses it, by reading the argv
// the runtime was handed rather than the operation the client got back: the
// operation was already correct while nothing at all was sent.
func TestDetachingAnInstanceTakesTheDeviceOffTheRuntime(t *testing.T) {
	driver := &recordingDriver{}
	p := runtimePack(driver)

	const instanceID = "11111111-1111-4111-8111-111111111111"
	const networkID = "22222222-2222-4222-8222-222222222222"
	now := time.Unix(1700000000, 0).UTC()

	pn := resource.New(networkID, kindPrivateNetwork, resource.Tenant{Provider: Name}, "created", now)
	pn.Runtime = map[string]string{runtimeNetworkKey: "fnt-abc123def45"}
	p.env.Store.Put(pn)

	inst := resource.New(instanceID, kindInstance, resource.Tenant{Provider: Name}, "running", now)
	inst.Runtime = map[string]string{p.binding().RuntimeKey: "feint-exo-" + instanceID}
	inst.Attrs = map[string]any{
		attrInstancePrivateNetworks: attachmentsToAttr([]pnAttachment{{NetworkID: networkID}}),
	}
	p.env.Store.Put(inst)

	// The witness: nothing has been asked of the runtime yet, so a fake that
	// recorded a detach for any call would be caught here.
	if len(driver.detached) != 0 {
		t.Fatalf("the runtime was asked to detach %v before the request", driver.detached)
	}

	req := httptest.NewRequest(http.MethodPut,
		"/v2/private-network/"+networkID+":detach",
		strings.NewReader(`{"instance":{"id":"`+instanceID+`"}}`))
	req.SetPathValue("id", networkID)
	rec := httptest.NewRecorder()
	p.detachInstanceFromPrivateNetwork(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("detach answered %d: %s", rec.Code, rec.Body.String())
	}
	if len(driver.detached) == 0 {
		t.Fatal("the membership was removed and the runtime was never asked to take the device off: " +
			"the interface stays on the container, and the network under it can then never be removed (#426)")
	}
	// Named on both ends: a detach aimed at the wrong machine or the wrong
	// network leaves exactly the same leftover as no detach at all.
	if got := driver.detached[0]; got != "feint-exo-"+instanceID+" fnt-abc123def45" {
		t.Fatalf("the detach named %q, which is not this instance on this network", got)
	}
}
