package scaleway_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
)

// The teardown half of a private NIC, and of the network under it (#426).
//
// Both tests here are about the same measured lie, at its two ends. Read on a
// host, not deduced: DELETE on a private NIC answered 204 while
// `incus config device show` still listed the device; DELETE on the private
// network then answered 204 while `incus network list` still listed the bridge.
// Three runs of tools/conformance/stacks.sh left three bridges, three ACLs and
// three dnsmasq behind on a passing run, and the next run failed on
// "Address already in use" for blocks the API had reported gone.

// nicOnRunningServer creates a private network, a running server and a NIC joining
// them, and answers the ids plus the two runtime names the pack derived.
func nicOnRunningServer(t *testing.T, ts *httptest.Server, subnet string) (pnID, serverID, nicID string) {
	t.Helper()

	status, pn := do(t, ts, "POST", "/vpc/v2/regions/fr-par/private-networks",
		fmt.Sprintf(`{"name":"detach-test","subnets":[%q]}`, subnet))
	if status != http.StatusOK {
		t.Fatalf("create private network: status %d (%v)", status, pn)
	}
	pnID, _ = pn["id"].(string)

	const zone = "/instance/v1/zones/fr-par-1"
	status, srv := do(t, ts, "POST", zone+"/servers", `{"name":"detach-host","commercial_type":"DEV1-S"}`)
	if status != http.StatusCreated {
		t.Fatalf("create server: status %d (%v)", status, srv)
	}
	serverID, _ = srv["server"].(map[string]any)["id"].(string)

	// Powered on, because the backing machine name is written when the machine
	// starts, and a NIC on a server that never booted has nothing to detach
	// from — which would make this test pass by vacuity.
	if status, out := do(t, ts, "POST", zone+"/servers/"+serverID+"/action", `{"action":"poweron"}`); status != http.StatusOK && status != http.StatusAccepted {
		t.Fatalf("poweron: status %d (%v)", status, out)
	}

	status, nic := do(t, ts, "POST", zone+"/servers/"+serverID+"/private_nics",
		fmt.Sprintf(`{"private_network_id":%q}`, pnID))
	if status != http.StatusCreated {
		t.Fatalf("create private NIC: status %d (%v)", status, nic)
	}
	nicID, _ = nic["private_nic"].(map[string]any)["id"].(string)
	return pnID, serverID, nicID
}

// TestDeletingAPrivateNICDetachesItFromTheRuntime is the missing destruction
// itself. Before this, releaseNIC deleted the NIC from the store and told the
// runtime nothing at all, so the device stayed on the container for as long as
// the container lived.
//
// The assertion reads what the driver was asked to do, never the status code.
// That distinction is the whole finding: the 204 was correct-looking and the
// device was still there, and no return value anywhere could have said so.
func TestDeletingAPrivateNICDetachesItFromTheRuntime(t *testing.T) {
	rt := newFakeRuntime()
	close(rt.release)
	ts := newRuntimeTestServer(t, rt)

	_, serverID, nicID := nicOnRunningServer(t, ts, "10.71.0.0/24")

	// The witness. Nothing has been deleted yet, so the recording must be
	// empty; without this the test would also pass on a fake that records a
	// detach for every call, which is a check that cannot fail.
	if before := rt.detaches(); len(before) != 0 {
		t.Fatalf("the runtime was asked to detach %v before anything was deleted", before)
	}

	const zone = "/instance/v1/zones/fr-par-1"
	if status, _ := do(t, ts, "DELETE", zone+"/servers/"+serverID+"/private_nics/"+nicID, ""); status != http.StatusNoContent {
		t.Fatalf("delete private NIC: status %d", status)
	}

	got := rt.detaches()
	if len(got) == 0 {
		t.Fatal("the NIC was deleted and the runtime was never asked to detach anything: " +
			"the device stays on the container, and the network under it can then never be removed (#426)")
	}
	// Named, not merely counted: a detach aimed at the wrong machine or the
	// wrong network leaves exactly the same leftover as no detach at all.
	if !strings.Contains(got[0], "feint-scw-"+serverID) {
		t.Fatalf("the detach named %q, which is not this server's machine", got[0])
	}
	if !strings.Contains(got[0], " "+machine.NetworkPrefix+"-") {
		t.Fatalf("the detach named %q, which is not a network this emulator created", got[0])
	}
}

// TestAPrivateNetworkTheRuntimeKeptIsNotReportedDeleted is the other end: the
// pack used to log a failed RemoveNetwork and answer 204 anyway.
//
// What that produced, measured: the store forgot the network, the bridge and
// its dnsmasq stayed up holding the block, and the next run died on "Address
// already in use" minutes in, blaming a block the API said was free. A create
// that succeeds while nothing exists and a delete that succeeds while
// everything does are the same lie in two directions, and the create path in
// vpc.go already refuses its half.
func TestAPrivateNetworkTheRuntimeKeptIsNotReportedDeleted(t *testing.T) {
	rt := &keepingRuntime{fakeRuntime: newFakeRuntime()}
	close(rt.release)
	ts := newRuntimeTestServer(t, rt)

	status, pn := do(t, ts, "POST", "/vpc/v2/regions/fr-par/private-networks",
		`{"name":"kept","subnets":["10.72.0.0/24"]}`)
	if status != http.StatusOK {
		t.Fatalf("create private network: status %d (%v)", status, pn)
	}
	pnID, _ := pn["id"].(string)

	status, body := do(t, ts, "DELETE", "/vpc/v2/regions/fr-par/private-networks/"+pnID, "")
	if status == http.StatusNoContent {
		t.Fatal("the runtime refused to remove the backing network and the API still answered 204: " +
			"a network reported gone while its bridge holds the block is what fails the next run (#426)")
	}
	// The pack's established refusal shape, which is upstream's: a 400 whose
	// body types itself precondition_failed, exactly as a delete refused for a
	// still-attached NIC already answers a few lines above it in vpc.go.
	if status != http.StatusBadRequest {
		t.Fatalf("delete answered %d, want 400 (%v)", status, body)
	}
	if kind, _ := body["type"].(string); kind != "precondition_failed" {
		t.Fatalf("delete answered type %q, want precondition_failed: the client must get "+
			"something it can act on and retry, not an opaque failure (%v)", kind, body)
	}

	// And the record must survive its own failed delete. A store that forgot it
	// anyway would leave the host object with nothing naming it, which is
	// precisely the orphan #316, #342 and #375 each swept a symptom of.
	if status, _ := do(t, ts, "GET", "/vpc/v2/regions/fr-par/private-networks/"+pnID, ""); status != http.StatusOK {
		t.Fatalf("after the refused delete the network reads back %d, want 200: "+
			"dropping it here orphans the bridge that is still standing", status)
	}
}

// keepingRuntime is a runtime that cannot remove a network, which is what Incus
// answers while any instance still holds a device on it.
type keepingRuntime struct {
	*fakeRuntime
}

func (k *keepingRuntime) RemoveNetwork(context.Context, string) error {
	return fmt.Errorf(`incus network: Error: The network is currently in use`)
}
