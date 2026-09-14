package scaleway_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const v2alphaServers = "/instance/v2alpha1/zones/fr-par-1/servers"

func detach(t *testing.T, ts *httptest.Server, serverID, body string) (int, map[string]any) {
	t.Helper()
	return do(t, ts, "POST", v2alphaServers+"/"+serverID+"/detach-private-network-interface", body)
}

// A detach takes the interface off its server, and the server says so.
//
// Provider 2.83.0 sends this before every delete of an interface
// (`fix(instance): detach private network interface before deleting it`), and a
// 501 here failed every `terraform destroy` against this emulator. The answer is
// the server, so the assertion is on what the server publishes afterwards rather
// than on the status code alone.
func TestADetachLeavesTheInterfaceOffItsServer(t *testing.T) {
	ts := newTestServer(t)
	serverID, _, nic := attachedNIC(t, ts, "detach", "10.171.0.0/24")
	nicID, _ := nic["id"].(string)

	status, before := detach(t, ts, serverID, `{"private_network_interface_id":"`+nicID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("detach: expected 200, got %d (%v)", status, before)
	}

	// The answer is a v2alpha1 server, and the interface is gone from it.
	ifaces, _ := before["private_network_interfaces"].([]any)
	for _, raw := range ifaces {
		iface, _ := raw.(map[string]any)
		if iface["id"] == nicID {
			t.Errorf("the detached interface is still on the server it answered: %v", before)
		}
	}

	// And the v1 door agrees, because there is one store behind the two.
	status, list := do(t, ts, "GET", zoneURL+"/servers/"+serverID+"/private_nics", "")
	if status != http.StatusOK {
		t.Fatalf("list v1 nics: expected 200, got %d (%v)", status, list)
	}
	nics, _ := list["private_nics"].([]any)
	for _, raw := range nics {
		got, _ := raw.(map[string]any)
		if got["id"] == nicID {
			t.Errorf("v1 still attaches the interface v2alpha1 detached: %v", list)
		}
	}
}

// A detach dissociates; it does not delete.
//
// Measured through `feint proxy` on provider 2.83.0, and the recording is the
// whole argument — the four calls in order:
//
//	200  POST   …/servers/{id}/detach-private-network-interface
//	200  GET    …/private-network-interfaces/{id}     <- still there
//	204  DELETE …/private-network-interfaces/{id}     <- the client deletes it
//	404  GET    …/private-network-interfaces/{id}
//
// Which is `fix(instance): detach private network interface before deleting it`
// spelled out: two calls, in that order. The first version of this handler
// reused releaseNIC and deleted at the detach — the client then got a 404 on its
// read and never issued the DELETE at all, so the suite passed for a reason that
// was not the one it claimed.
//
// The upstream SDK says the same thing without any measurement:
// DetachAndDeletePrivateNetworkInterface exists as a separate operation, which
// is only meaningful if the plain detach leaves the interface in place.
func TestADetachedInterfaceSurvivesItsDetach(t *testing.T) {
	ts := newTestServer(t)
	serverID, pnID, nic := attachedNIC(t, ts, "survives", "10.178.0.0/24")
	nicID, _ := nic["id"].(string)

	status, body := detach(t, ts, serverID, `{"private_network_interface_id":"`+nicID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("detach: expected 200, got %d (%v)", status, body)
	}

	// The read the client makes next. It must answer, because the client deletes
	// what it can still see.
	status, after := do(t, ts, "GET", v2alphaNICs+"/"+nicID, "")
	if status != http.StatusOK {
		t.Fatalf("the interface is gone after a detach: GET answered %d (%v). "+
			"A detach that deletes makes the client skip its own DELETE", status, after)
	}
	if after["private_network_id"] != pnID {
		t.Errorf("the surviving interface changed network: %v, want %v",
			after["private_network_id"], pnID)
	}

	// And the delete the client sends afterwards still works, which is the other
	// half of the sequence.
	if status, body := do(t, ts, "DELETE", v2alphaNICs+"/"+nicID, ""); status != http.StatusNoContent {
		t.Errorf("deleting a detached interface answered %d, want 204 (%v)", status, body)
	}
	if status, _ := do(t, ts, "GET", v2alphaNICs+"/"+nicID, ""); status != http.StatusNotFound {
		t.Errorf("the interface survived its own delete: GET answered %d", status)
	}
}

// An interface that belongs to another server is not this server's to detach.
//
// The identifier arrives in a request body, so it is checked against the store
// rather than trusted because it resolved to something. Without the check, a
// client could take an interface off a machine it never named.
func TestADetachRefusesAnInterfaceOfAnotherServer(t *testing.T) {
	ts := newTestServer(t)
	_, _, mine := attachedNIC(t, ts, "mine", "10.172.0.0/24")
	otherServer, _, _ := attachedNIC(t, ts, "other", "10.173.0.0/24")
	mineID, _ := mine["id"].(string)

	status, body := detach(t, ts, otherServer, `{"private_network_interface_id":"`+mineID+`"}`)
	if status == http.StatusOK {
		t.Fatalf("a server detached an interface of another server: %v", body)
	}

	// The accepting half: the interface is still where it was, so the refusal
	// refused rather than half-acted.
	status, list := do(t, ts, "GET", v2alphaNICs+"/"+mineID, "")
	if status != http.StatusOK {
		t.Fatalf("read the interface back: expected 200, got %d (%v)", status, list)
	}
	if list["server_id"] == "" || list["server_id"] == nil {
		t.Errorf("the refused detach took the interface off its server anyway: %v", list)
	}
}

// The server answers the status vocabulary of the API it is being read through.
//
// instance/v1 says `running`; instance/v2alpha1 declares an enum that holds
// `started` and no `running` at all. Echoing the stored state answers a value
// the API description forbids, and a client branching on the enum sees a state
// that is not in it.
func TestAServerRendersTheStatusVocabularyOfItsOwnApi(t *testing.T) {
	ts := newTestServer(t)
	serverID, _, nic := attachedNIC(t, ts, "status", "10.174.0.0/24")
	nicID, _ := nic["id"].(string)

	status, _ := do(t, ts, "POST", zoneURL+"/servers/"+serverID+"/action", `{"action":"poweron"}`)
	if status != http.StatusAccepted && status != http.StatusOK {
		t.Fatalf("poweron: got %d", status)
	}
	// v1 is asked first, so the test compares two doors rather than one door
	// against a constant it wrote itself.
	_, v1 := do(t, ts, "GET", zoneURL+"/servers/"+serverID, "")
	server, _ := v1["server"].(map[string]any)
	if server["state"] != "running" {
		t.Fatalf("v1 says %v, this test needs a running server to compare", server["state"])
	}

	_, answer := detach(t, ts, serverID, `{"private_network_interface_id":"`+nicID+`"}`)
	if answer["status"] != "started" {
		t.Errorf("v2alpha1 answers status %v for a server v1 calls running; the enum holds "+
			"`started` and no `running`", answer["status"])
	}
}

// Every field the API description declares for this response is answered.
//
// The omission gate (#88) reports a declared field a served response leaves out,
// and it judges on the `fields` leg alone. This holds the same property one
// package in, so the shape is wrong here before it is wrong in a leg that takes
// minutes to run.
func TestADetachedServerCarriesEveryDeclaredField(t *testing.T) {
	ts := newTestServer(t)
	serverID, _, nic := attachedNIC(t, ts, "fields", "10.175.0.0/24")
	nicID, _ := nic["id"].(string)

	status, answer := detach(t, ts, serverID, `{"private_network_interface_id":"`+nicID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("detach: expected 200, got %d (%v)", status, answer)
	}

	// The nineteen of contracts/scaleway.json, written out rather than read from
	// the contract: reading it would compare the response against the same file
	// the response is generated from, and pass whatever both became.
	for _, field := range []string{
		"id", "name", "project_id", "zone", "tags", "server_type", "status",
		"status_detail", "architecture", "created_at", "updated_at", "rescue_mode",
		"placement_group_id", "boot_volume_id", "volumes", "private_network_interfaces",
		"filesystems", "windows_rdp_password", "public_network_interface",
	} {
		if _, ok := answer[field]; !ok {
			t.Errorf("the response omits the declared field %q", field)
		}
	}
	if answer["id"] != serverID {
		t.Errorf("the answer names server %v, the detach was sent to %v", answer["id"], serverID)
	}
}

// Every value that belongs to an enum is a member of ITS OWN enum.
//
// The first version of this file answered the server's status on the public
// interface and v1's `sbs_volume` in a v2alpha1 volume, and the tests above
// stayed green through both: they assert that a field is PRESENT, which says
// nothing about whether its value is one the API description allows. The
// contract check caught them, one leg and a minute later.
//
// Three enums, three vocabularies, and the point is that they do not coincide:
// a server is `started` where an interface is `available`, and a volume v1 calls
// `sbs_volume` is `sbs` here.
func TestAServerRendersTheVolumeVocabularyOfItsOwnApi(t *testing.T) {
	ts := newTestServer(t)

	// The server carries a PUBLIC address, and that is not decoration: without
	// one, `public_network_interface` is null and the assertion below runs on
	// nothing. The first version of this test had no address, and falsify showed
	// it — the mutation that answers a server's vocabulary on an interface came
	// back green, because the interface it would have spoiled did not exist.
	//
	// The population a verdict needs is the one that triggers it, which is the
	// trap CLAUDE.md names and this test walked into.
	status, ip := do(t, ts, "POST", zoneURL+"/ips", `{"type":"routed_ipv4"}`)
	if status != http.StatusCreated {
		t.Fatalf("reserve an address: expected 201, got %d (%v)", status, ip)
	}
	reserved, _ := ip["ip"].(map[string]any)
	ipID, _ := reserved["id"].(string)

	pnID, _ := privateNetwork(t, ts, `{"name":"vocab","subnets":["10.177.0.0/24"]}`)
	serverID, _ := serverWith(t, ts,
		`{"name":"vocab","commercial_type":"DEV1-S","public_ips":["`+ipID+`"]}`)
	status, created := do(t, ts, "POST",
		zoneURL+"/servers/"+serverID+"/private_nics", `{"private_network_id":"`+pnID+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("attach an interface: expected 201, got %d (%v)", status, created)
	}
	nic, _ := created["private_nic"].(map[string]any)
	nicID, _ := nic["id"].(string)

	status, answer := detach(t, ts, serverID, `{"private_network_interface_id":"`+nicID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("detach: expected 200, got %d (%v)", status, answer)
	}

	// contracts/scaleway.json, instance/v2alpha1…Server.Volume.VolumeType.
	allowedVolume := map[string]bool{
		"unknown_volume_type": true, "l_ssd": true, "sbs": true, "scratch": true,
	}
	volumes, _ := answer["volumes"].([]any)
	if len(volumes) == 0 {
		t.Fatal("the server answered no volume, so this test would assert nothing")
	}
	for _, raw := range volumes {
		vol, _ := raw.(map[string]any)
		kind, _ := vol["volume_type"].(string)
		if !allowedVolume[kind] {
			t.Errorf("volume_type %q is not one this API declares; v1's own spelling is not v2alpha1's", kind)
		}
	}

	// …Server.PublicNetworkInterface.Status, which holds no `started`.
	allowedInterface := map[string]bool{"unknown_status": true, "available": true, "syncing": true}
	pub, ok := answer["public_network_interface"].(map[string]any)
	if !ok {
		t.Fatalf("the server answers no public interface, so this half asserts nothing: %v",
			answer["public_network_interface"])
	}
	st, _ := pub["status"].(string)
	if !allowedInterface[st] {
		t.Errorf("the public interface answers status %q, which is a SERVER's vocabulary, "+
			"not an interface's", st)
	}

	// …Server.Status, which holds no `running`.
	allowedServer := map[string]bool{
		"unknown_status": true, "started": true, "stopped": true, "paused": true,
		"starting": true, "stopping": true, "pausing": true, "locked": true, "rebooting": true,
	}
	if st, _ := answer["status"].(string); !allowedServer[st] {
		t.Errorf("the server answers status %q, which its own enum does not hold", st)
	}
}

// A detach names what it cannot find, and refuses an empty identifier.
func TestADetachRefusesWhatItCannotResolve(t *testing.T) {
	ts := newTestServer(t)
	serverID, _, _ := attachedNIC(t, ts, "refuse", "10.176.0.0/24")

	// The STATUS is the assertion, not merely "not 200". A body that names no
	// interface is a malformed request — 400, `invalid_arguments`, naming the
	// field — while an identifier nothing holds is a 404. Asserting only "not
	// 200" let both collapse into the same check: falsify showed it, by removing
	// the empty-value guard and watching this test stay green, because an empty
	// identifier resolves to nothing and falls into the 404 below.
	//
	// The difference is what a client branches on, so it is what is held here.
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"no identifier at all", `{}`, http.StatusBadRequest},
		{"an empty identifier", `{"private_network_interface_id":""}`, http.StatusBadRequest},
		{"an identifier nothing holds",
			`{"private_network_interface_id":"a0000000-0000-4000-8000-000000000001"}`,
			http.StatusNotFound},
	} {
		status, body := detach(t, ts, serverID, tc.body)
		if status != tc.want {
			t.Errorf("%s: answered %d, want %d (%v)", tc.name, status, tc.want, body)
		}
	}

	// And a server nothing holds is a 404 rather than a detach on nothing.
	if status, body := detach(t, ts, "a0000000-0000-4000-8000-000000000002",
		`{"private_network_interface_id":"whatever"}`); status != http.StatusNotFound {
		t.Errorf("a detach on an absent server answered %d, want 404 (%v)", status, body)
	}
}
