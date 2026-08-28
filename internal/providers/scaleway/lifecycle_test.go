package scaleway_test

import (
	"net/http"
	"testing"
)

// standby is a state of its own, and nothing covered it.
//
// The SDK declares `stopped in place` beside `stopped` (ServerStateStoppedInPlace),
// and the Terraform provider polls for the exact one its `state = "standby"`
// asked for: collapsing both into "stopped" made a plan fail with "expected
// state stopped in place but found stopped". A real provider found that, after
// the emulator had shipped the two actions as synonyms — because `scw` has no
// standby verb and the conformance suite never asked for one.
func TestStopInPlaceReachesItsOwnState(t *testing.T) {
	ts := newTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"

	status, out := do(t, ts, "POST", zone+"/servers", `{"name":"demo","commercial_type":"DEV1-S"}`)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d", status)
	}
	server, _ := out["server"].(map[string]any)
	id, _ := server["id"].(string)

	state := func() string {
		t.Helper()
		status, out := do(t, ts, "GET", zone+"/servers/"+id, "")
		if status != http.StatusOK {
			t.Fatalf("get: status %d", status)
		}
		s, _ := out["server"].(map[string]any)
		got, _ := s["state"].(string)
		return got
	}
	action := func(name string) {
		t.Helper()
		if status, _ := do(t, ts, "POST", zone+"/servers/"+id+"/action", `{"action":"`+name+`"}`); status != http.StatusAccepted {
			t.Fatalf("%s: status %d", name, status)
		}
	}

	action("poweron")
	if got := state(); got != "running" {
		t.Fatalf("after poweron the state is %q, want running", got)
	}

	action("stop_in_place")
	if got := state(); got != "stopped in place" {
		t.Fatalf("after stop_in_place the state is %q, want \"stopped in place\"", got)
	}

	// And poweroff is not the same action: it reaches the plain stopped state.
	action("poweron")
	action("poweroff")
	if got := state(); got != "stopped" {
		t.Fatalf("after poweroff the state is %q, want stopped", got)
	}
}

// allowed_actions is derived from the state rather than from the action that was
// asked for, which is what keeps a failed start from advertising poweroff on a
// server that never came up. Nothing tested that derivation: an audit replaced
// the whole function with a fixed list and every test still passed.
func TestAllowedActionsFollowTheState(t *testing.T) {
	ts := newTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"

	status, out := do(t, ts, "POST", zone+"/servers", `{"name":"demo","commercial_type":"DEV1-S"}`)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d", status)
	}
	server, _ := out["server"].(map[string]any)
	id, _ := server["id"].(string)

	allowed := func() map[string]bool {
		t.Helper()
		status, out := do(t, ts, "GET", zone+"/servers/"+id, "")
		if status != http.StatusOK {
			t.Fatalf("get: status %d", status)
		}
		s, _ := out["server"].(map[string]any)
		list, _ := s["allowed_actions"].([]any)
		set := make(map[string]bool, len(list))
		for _, a := range list {
			if name, ok := a.(string); ok {
				set[name] = true
			}
		}
		return set
	}
	action := func(name string) {
		t.Helper()
		if status, _ := do(t, ts, "POST", zone+"/servers/"+id+"/action", `{"action":"`+name+`"}`); status != http.StatusAccepted {
			t.Fatalf("%s: status %d", name, status)
		}
	}

	stopped := allowed()
	if !stopped["poweron"] || stopped["poweroff"] {
		t.Fatalf("a stopped server allows %v; want poweron and not poweroff", stopped)
	}

	action("poweron")
	running := allowed()
	for _, want := range []string{"poweroff", "reboot", "stop_in_place"} {
		if !running[want] {
			t.Fatalf("a running server does not allow %s: %v", want, running)
		}
	}
	if running["poweron"] {
		t.Fatalf("a running server still advertises poweron: %v", running)
	}

	// Standby keeps the machine, so powering it fully off is available from
	// there as much as starting it again.
	action("stop_in_place")
	standby := allowed()
	if !standby["poweron"] || !standby["poweroff"] {
		t.Fatalf("a standby server allows %v; want both poweron and poweroff", standby)
	}
	if standby["reboot"] {
		t.Fatalf("a standby server advertises reboot: %v", standby)
	}
}

// Deleting a server detaches its disks, it does not delete them. That is what
// the real API does, and it is why `scw instance server delete` carries a
// --with-volumes flag: without it, the CLI deletes the server and then removes
// the volumes itself, one call at a time, polling each one. A volume that
// vanished with its server makes those calls 404.
//
// Nothing covered it: an audit removed the detachment loop from the delete path
// and the whole suite still passed.
func TestDeletingAServerLeavesItsVolumeAvailable(t *testing.T) {
	ts := newTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"

	status, out := do(t, ts, "POST", zone+"/servers", `{"name":"demo","commercial_type":"DEV1-S"}`)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d", status)
	}
	server, _ := out["server"].(map[string]any)
	id, _ := server["id"].(string)
	volumes, _ := server["volumes"].(map[string]any)
	root, _ := volumes["0"].(map[string]any)
	rootID, _ := root["id"].(string)

	if status, _ := do(t, ts, "DELETE", zone+"/servers/"+id, ""); status != http.StatusNoContent {
		t.Fatalf("delete: status %d", status)
	}

	// In block, where the root disk lives since #365, and where `scw instance
	// server delete` goes to read it: the recorded account answered 200 there
	// after the server was gone, then 204 to the delete.
	status, out = do(t, ts, "GET", blockURL+"/volumes/"+rootID, "")
	if status != http.StatusOK {
		t.Fatalf("the root volume went with the server: status %d", status)
	}
	if refs, _ := out["references"].([]any); len(refs) != 0 {
		t.Fatalf("the volume still belongs to the deleted server: %v", out["references"])
	}
	if out["status"] != "available" {
		t.Fatalf("the released volume reads status %v, want available: this is the field the CLI polls", out["status"])
	}
}

// public_ips is served as a list the client can read, and it was once stored as
// an empty literal written at creation and never touched. The fix has no
// regression test, so an audit replaced the field with an empty list and nothing
// failed.
func TestPublicIPsCarryTheAttachedAddress(t *testing.T) {
	ts := newTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"

	status, out := do(t, ts, "POST", zone+"/ips", `{}`)
	if status != http.StatusCreated {
		t.Fatalf("create ip: status %d (%v)", status, out)
	}
	ip, _ := out["ip"].(map[string]any)
	ipID, _ := ip["id"].(string)
	address, _ := ip["address"].(string)

	status, out = do(t, ts, "POST", zone+"/servers",
		`{"name":"demo","commercial_type":"DEV1-S","public_ip":"`+ipID+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("create server: status %d (%v)", status, out)
	}
	server, _ := out["server"].(map[string]any)
	list, _ := server["public_ips"].([]any)
	if len(list) != 1 {
		t.Fatalf("the server carries %d public ips, want 1: %v", len(list), server["public_ips"])
	}
	first, _ := list[0].(map[string]any)
	if got, _ := first["address"].(string); got != address {
		t.Fatalf("public_ips carries %q, want the address that was attached (%q)", got, address)
	}
}

// Detaching a flexible IP must actually detach it.
//
// `scw instance ip detach` sends `PATCH /ips/{id}` with `{"server": null}` —
// the SDK's NullableStringValue. The request struct read that field as a
// *string, and encoding/json decodes both JSON null and an absent field to nil,
// so the detach branch was unreachable from any real client: the emulator
// answered 200 and left the address attached, while the struct's own comment
// said a null server detaches. An audit reproduced it with the CLI.
func TestDetachingAnAddressActuallyDetachesIt(t *testing.T) {
	ts := newTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"

	_, out := do(t, ts, "POST", zone+"/servers", `{"name":"demo","commercial_type":"DEV1-S"}`)
	server, _ := out["server"].(map[string]any)
	serverID, _ := server["id"].(string)

	_, out = do(t, ts, "POST", zone+"/ips", `{}`)
	ip, _ := out["ip"].(map[string]any)
	ipID, _ := ip["id"].(string)

	if status, _ := do(t, ts, "PATCH", zone+"/ips/"+ipID, `{"server":"`+serverID+`"}`); status != 200 {
		t.Fatalf("attach: status %d", status)
	}
	// The shape a real client sends, and the one that used to be a no-op.
	status, out := do(t, ts, "PATCH", zone+"/ips/"+ipID, `{"server":null}`)
	if status != 200 {
		t.Fatalf("detach: status %d", status)
	}
	ip, _ = out["ip"].(map[string]any)
	if ip["server"] != nil {
		t.Errorf("the address still carries a server after a null detach: %v", ip["server"])
	}
	if state, _ := ip["state"].(string); state != "detached" {
		t.Errorf("state is %q after a detach, want detached", state)
	}
}

// A deleted server must not leave its addresses claiming it.
//
// `scw instance ip list` showed an address attached to a server that no longer
// existed, on both the delete and the terminate path — and with a machine
// runtime the route outlived the machine. The real API detaches them.
func TestDeletingAServerReleasesItsAddresses(t *testing.T) {
	for _, how := range []string{"delete", "terminate"} {
		t.Run(how, func(t *testing.T) {
			ts := newTestServer(t)
			const zone = "/instance/v1/zones/fr-par-1"

			_, out := do(t, ts, "POST", zone+"/ips", `{}`)
			ip, _ := out["ip"].(map[string]any)
			ipID, _ := ip["id"].(string)

			_, out = do(t, ts, "POST", zone+"/servers",
				`{"name":"demo","commercial_type":"DEV1-S","public_ip":"`+ipID+`"}`)
			server, _ := out["server"].(map[string]any)
			id, _ := server["id"].(string)

			// An address attached at create must also say it is attached.
			_, out = do(t, ts, "GET", zone+"/ips/"+ipID, "")
			ip, _ = out["ip"].(map[string]any)
			if state, _ := ip["state"].(string); state != "attached" {
				t.Errorf("state is %q while the address carries a server", state)
			}

			if how == "delete" {
				do(t, ts, "POST", zone+"/servers/"+id+"/action", `{"action":"poweroff"}`)
				do(t, ts, "DELETE", zone+"/servers/"+id, "")
			} else {
				do(t, ts, "POST", zone+"/servers/"+id+"/action", `{"action":"terminate"}`)
			}

			_, out = do(t, ts, "GET", zone+"/ips/"+ipID, "")
			ip, _ = out["ip"].(map[string]any)
			if ip["server"] != nil {
				t.Errorf("the address still names the %sd server: %v", how, ip["server"])
			}
			if state, _ := ip["state"].(string); state != "detached" {
				t.Errorf("state is %q after the server went away, want detached", state)
			}
		})
	}
}

// The volumes a client names at create must be attached, not only the root one.
//
// Only Volumes["0"] was read, so a create naming an existing volume under "1" —
// the shape the Terraform provider sends for additional_volume_ids — answered
// 201 with the volume left detached and nothing saying so.
func TestAdditionalVolumesAreAttachedAtCreate(t *testing.T) {
	ts := newTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"

	status, out := do(t, ts, "POST", zone+"/volumes", `{"name":"extra","size":10000000000,"volume_type":"b_ssd"}`)
	if status != 201 {
		t.Fatalf("volume create: status %d", status)
	}
	vol, _ := out["volume"].(map[string]any)
	volID, _ := vol["id"].(string)

	_, out = do(t, ts, "POST", zone+"/servers",
		`{"name":"demo","commercial_type":"DEV1-S","volumes":{"0":{"size":20000000000},"1":{"id":"`+volID+`"}}}`)
	server, _ := out["server"].(map[string]any)
	volumes, _ := server["volumes"].(map[string]any)
	if _, ok := volumes["1"]; !ok {
		t.Fatalf("the named volume was dropped: server carries %v", keysOf(volumes))
	}

	// And the volume itself must know: a server holding a volume the volume does
	// not know about is the contradiction this project exists to avoid.
	_, out = do(t, ts, "GET", zone+"/volumes/"+volID, "")
	vol, _ = out["volume"].(map[string]any)
	if vol["server"] == nil {
		t.Error("the volume does not name the server it was attached to")
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// `scw instance server terminate` detaches the root volume first, on every
// server, because every emulated server owns one. That call answered 501 and the
// command failed outright — an audit reproduced it with the CLI.
func TestTerminateWalksDetachVolume(t *testing.T) {
	ts := newTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"

	_, out := do(t, ts, "POST", zone+"/servers", `{"name":"demo","commercial_type":"DEV1-S"}`)
	server, _ := out["server"].(map[string]any)
	id, _ := server["id"].(string)
	volumes, _ := server["volumes"].(map[string]any)
	root, _ := volumes["0"].(map[string]any)
	rootID, _ := root["id"].(string)

	// The exact sequence the CLI issues.
	status, out := do(t, ts, "POST", zone+"/servers/"+id+"/detach-volume", `{"volume_id":"`+rootID+`"}`)
	if status != 200 {
		t.Fatalf("detach-volume: status %d, want 200", status)
	}
	server, _ = out["server"].(map[string]any)
	if volumes, _ = server["volumes"].(map[string]any); len(volumes) != 0 {
		t.Errorf("the volume is still attached: %v", volumes)
	}
	// And the volume is free, not orphaned.
	_, out = do(t, ts, "GET", zone+"/volumes/"+rootID, "")
	vol, _ := out["volume"].(map[string]any)
	if vol["server"] != nil {
		t.Errorf("the detached volume still names a server: %v", vol["server"])
	}

	// Attaching it back is the other half of the pair.
	if status, _ := do(t, ts, "POST", zone+"/servers/"+id+"/attach-volume", `{"volume_id":"`+rootID+`"}`); status != 200 {
		t.Fatalf("attach-volume: status %d", status)
	}
	if status, _ := do(t, ts, "POST", zone+"/servers/"+id+"/action", `{"action":"terminate"}`); status != 202 {
		t.Fatal("terminate refused after the volume round-trip")
	}
}
