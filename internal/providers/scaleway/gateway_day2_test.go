package scaleway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
)

// egressRuntime records the way-out gestures, which is the whole story of
// #678: who was given a default route, and whose was taken back.
type egressRuntime struct {
	*fakeRuntime

	mu      sync.Mutex
	routed  []string // machine names given a way out
	dropped []string // machine names whose way out was taken back
}

func newEgressRuntime() *egressRuntime {
	rt := &egressRuntime{fakeRuntime: newFakeRuntime()}
	close(rt.release) // nothing here needs to block
	return rt
}

func (r *egressRuntime) RouteEgress(_ context.Context, name, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routed = append(r.routed, name)
	return nil
}

func (r *egressRuntime) DropEgress(_ context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dropped = append(r.dropped, name)
	return nil
}

func (r *egressRuntime) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.routed), len(r.dropped)
}

// day2Platform is the shape the issue names: a Private Network, a Public
// Gateway attached to it WITHOUT the default route, and a machine already
// running on that network.
func day2Platform(t *testing.T, ts *httptest.Server) (gatewayNetworkID string) {
	t.Helper()

	status, pn := do(t, ts, "POST", "/vpc/v2/regions/fr-par/private-networks",
		`{"name":"platform","subnets":["10.180.0.0/24"]}`)
	if status != http.StatusOK {
		t.Fatalf("create private network: status %d (%v)", status, pn)
	}
	pnID, _ := pn["id"].(string)

	status, ip := do(t, ts, "POST", "/vpc-gw/v2/zones/fr-par-1/ips", `{}`)
	if status != http.StatusOK {
		t.Fatalf("create gateway ip: status %d (%v)", status, ip)
	}
	ipID, _ := ip["id"].(string)

	status, gw := do(t, ts, "POST", "/vpc-gw/v2/zones/fr-par-1/gateways",
		`{"name":"edge","type":"VPC-GW-S","ip_id":"`+ipID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("create gateway: status %d (%v)", status, gw)
	}
	gwID, _ := gw["id"].(string)

	// Attached WITHOUT the route, which is the whole point: the platform is
	// standing before anybody decides it should reach out.
	status, gn := do(t, ts, "POST", "/vpc-gw/v2/zones/fr-par-1/gateway-networks",
		`{"gateway_id":"`+gwID+`","private_network_id":"`+pnID+`","enable_masquerade":true,"push_default_route":false}`)
	if status != http.StatusOK {
		t.Fatalf("attach gateway: status %d (%v)", status, gn)
	}
	gatewayNetworkID, _ = gn["id"].(string)

	status, srv := do(t, ts, "POST", "/instance/v1/zones/fr-par-1/servers",
		`{"name":"worker","commercial_type":"DEV1-S","image":"ubuntu_jammy"}`)
	// 201, which is what this API answers for a create; the rest of this
	// fixture reads 200 because the gateway product answers 200.
	if status != http.StatusCreated {
		t.Fatalf("create server: status %d (%v)", status, srv)
	}
	server, _ := srv["server"].(map[string]any)
	serverID, _ := server["id"].(string)

	status, _ = do(t, ts, "POST", "/instance/v1/zones/fr-par-1/servers/"+serverID+"/action",
		`{"action":"poweron"}`)
	// 202: an action is accepted, which is what the cloud answers and what the
	// CLI waits on.
	if status != http.StatusAccepted {
		t.Fatalf("poweron: status %d", status)
	}
	status, nic := do(t, ts, "POST", "/instance/v1/zones/fr-par-1/servers/"+serverID+"/private_nics",
		`{"private_network_id":"`+pnID+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("attach nic: status %d (%v)", status, nic)
	}
	return gatewayNetworkID
}

// Enabling the default route on an EXISTING attachment reaches the machines
// already on that network (#678).
//
// Measured on `95a2abf` under `--vm incus-ovn`, on a machine holding zero
// public address:
//
//	PATCH push_default_route=true
//	GET   push_default_route -> true          the store holds it
//	guest ip route show default -> (nothing)  nobody told the machine
//	dns ok, tcp443 no                         it reaches its own switch, not the world
//
// Which settles the two candidates the issue named: the PATCH stores what it is
// given, and nothing replayed the plan of the machines already there.
func TestEnablingTheDefaultRouteReachesTheMachinesAlreadyThere(t *testing.T) {
	rt := newEgressRuntime()
	ts := newRuntimeTestServer(t, machine.Use(rt))
	gnID := day2Platform(t, ts)

	before, _ := rt.counts()

	status, body := do(t, ts, "PATCH", "/vpc-gw/v2/zones/fr-par-1/gateway-networks/"+gnID,
		`{"push_default_route":true}`)
	if status != http.StatusOK {
		t.Fatalf("enable the default route: status %d (%v)", status, body)
	}
	if pushed, _ := body["push_default_route"].(bool); !pushed {
		t.Fatalf("the attachment does not carry the flag it was just given: %v", body)
	}

	after, _ := rt.counts()
	if after <= before {
		t.Fatal("enabling the default route on a standing attachment reached no machine: " +
			"the flag is stored and the platform still cannot leave, which is the Day-2 gesture #678 measured")
	}
}

// And the reverse, which the creation path already held: detaching the gateway
// takes the way out back.
//
// A machine that keeps a default route after its gateway is gone reaches the
// Internet the cloud would have cut off — the emulator being permissive in the
// direction that teaches a client something false.
func TestDetachingTheGatewayTakesTheWayOutBack(t *testing.T) {
	rt := newEgressRuntime()
	ts := newRuntimeTestServer(t, machine.Use(rt))
	gnID := day2Platform(t, ts)

	status, _ := do(t, ts, "PATCH", "/vpc-gw/v2/zones/fr-par-1/gateway-networks/"+gnID,
		`{"push_default_route":true}`)
	if status != http.StatusOK {
		t.Fatalf("enable the default route: status %d", status)
	}
	_, droppedBefore := rt.counts()

	status, _ = do(t, ts, "DELETE", "/vpc-gw/v2/zones/fr-par-1/gateway-networks/"+gnID, "")
	if status != http.StatusOK {
		t.Fatalf("detach the gateway: status %d", status)
	}

	_, droppedAfter := rt.counts()
	if droppedAfter <= droppedBefore {
		t.Fatal("detaching the gateway left the machines their default route: " +
			"a way out that outlives its gateway is a way out the cloud would have cut off")
	}
}
