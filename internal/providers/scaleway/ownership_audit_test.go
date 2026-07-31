package scaleway_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// What a second whole-pack audit found in the fixes of the first one. Every
// defect here was introduced by a correction, which is why they are together:
// the lesson is not "volumes are hard", it is that a fix which invents a value
// or writes a fact in a second place is a fix that costs more than the defect.

const zone = "/instance/v1/zones/fr-par-1"

func aVolume(t *testing.T, ts *httptest.Server, name string) string {
	t.Helper()
	_, out := do(t, ts, "POST", zone+"/volumes",
		`{"name":"`+name+`","volume_type":"b_ssd","size":10000000000}`)
	vol, _ := out["volume"].(map[string]any)
	id, _ := vol["id"].(string)
	if id == "" {
		t.Fatalf("no volume created: %v", out)
	}
	return id
}

func aServer(t *testing.T, ts *httptest.Server, name string) string {
	t.Helper()
	_, out := do(t, ts, "POST", zone+"/servers",
		`{"name":"`+name+`","commercial_type":"DEV1-S","image":"ubuntu_jammy"}`)
	srv, _ := out["server"].(map[string]any)
	id, _ := srv["id"].(string)
	if id == "" {
		t.Fatalf("no server created: %v", out)
	}
	return id
}

// A volume's state is one the SDK declares, whatever it is attached to.
//
// The previous wave answered "in_use" for an attached volume. `VolumeState` in
// instance_sdk.go has eight values and that is not one of them, and
// WaitForVolume treats only available and error as terminal: `tofu apply` with
// additional_volume_ids timed out after five minutes, `tofu destroy` after ten,
// and the volume became undeletable by Terraform — measurably worse than the
// defect the fix replaced. Rule 4, inside a fix that cited rule 4.
func TestAVolumeStateIsOneTheSDKDeclares(t *testing.T) {
	// The SDK's own enum, copied from instance_sdk.go. A state outside it is a
	// state no official client knows how to wait on.
	declared := map[string]bool{
		"available": true, "snapshotting": true, "fetching": true, "saving": true,
		"attaching": true, "resizing": true, "hotsyncing": true, "error": true,
	}

	ts := newTestServer(t)
	volID := aVolume(t, ts, "data")
	srvID := aServer(t, ts, "host")

	if status, out := do(t, ts, "POST", zone+"/servers/"+srvID+"/attach-volume",
		`{"volume_id":"`+volID+`"}`); status != http.StatusOK {
		t.Fatalf("attach-volume: %d %v", status, out)
	}

	_, out := do(t, ts, "GET", zone+"/volumes/"+volID, "")
	vol, _ := out["volume"].(map[string]any)
	state, _ := vol["state"].(string)
	if !declared[state] {
		t.Errorf("an attached volume answers state %q, which the SDK does not declare", state)
	}
	// And it does say it is attached — a check that only refused states would
	// pass with the server field dropped entirely.
	if server, _ := vol["server"].(map[string]any); server == nil || server["id"] != srvID {
		t.Errorf("the attached volume names %v, want the server %s", vol["server"], srvID)
	}
}

// Attaching a volume must not take it from the server that owns it.
//
// The guard read Attrs["server"] while every other reader in the pack — the
// view, volumesOf, the delete and terminate paths — reads
// Runtime[runtimeServerKey]. So it saw nothing on a root volume, and an audit
// moved one server's root volume onto another: both then listed it.
func TestAttachingDoesNotStealAnotherServersVolume(t *testing.T) {
	ts := newTestServer(t)
	first := aServer(t, ts, "first")
	second := aServer(t, ts, "second")

	_, out := do(t, ts, "GET", zone+"/servers/"+first, "")
	srv, _ := out["server"].(map[string]any)
	volumes, _ := srv["volumes"].(map[string]any)
	root, _ := volumes["0"].(map[string]any)
	rootID, _ := root["id"].(string)
	if rootID == "" {
		t.Fatalf("the first server has no root volume: %v", srv)
	}

	status, body := do(t, ts, "POST", zone+"/servers/"+second+"/attach-volume",
		`{"volume_id":"`+rootID+`"}`)
	if status == http.StatusOK {
		t.Fatalf("the root volume of %s was attached to %s: %v", first, second, body)
	}

	// And the first server still has it, rather than having half lost it.
	_, out = do(t, ts, "GET", zone+"/volumes/"+rootID, "")
	vol, _ := out["volume"].(map[string]any)
	if server, _ := vol["server"].(map[string]any); server == nil || server["id"] != first {
		t.Errorf("the refused attach moved the volume: it now names %v", vol["server"])
	}
}

// An address is a valid reference for every verb that takes one, delete
// included.
//
// The lookup by address was added for GET and PATCH; delete resolved through it
// and then removed the *reference the client typed* rather than the id it had
// just resolved. `scw instance ip delete 203.0.113.7` answered 204 and the
// address survived — a 200-that-does-nothing, which is the class the lookup was
// added to fix, reintroduced one door over.
//
// This is one of the two tests a previous commit cited by name without ever
// writing it. It exists now.
func TestAnAddressIsAValidIPReference(t *testing.T) {
	ts := newTestServer(t)
	_, out := do(t, ts, "POST", zone+"/ips", `{}`)
	ip, _ := out["ip"].(map[string]any)
	address, _ := ip["address"].(string)
	id, _ := ip["id"].(string)
	if address == "" || id == "" {
		t.Fatalf("no address allocated: %v", out)
	}

	if status, _ := do(t, ts, "GET", zone+"/ips/"+address, ""); status != http.StatusOK {
		t.Errorf("GET by address: %d", status)
	}
	if status, _ := do(t, ts, "DELETE", zone+"/ips/"+address, ""); status != http.StatusNoContent {
		t.Errorf("DELETE by address: %d", status)
	}
	// The one the previous version got wrong: it answered, and kept it.
	if status, _ := do(t, ts, "GET", zone+"/ips/"+id, ""); status != http.StatusNotFound {
		t.Errorf("the address survived its own delete: GET by id answered %d", status)
	}
}

// Deleting a server leaves its volumes available, not owned by a ghost.
//
// volumesOf reads Runtime, and the attach paths wrote Attrs, so a deleted
// server left every verb-attached volume claiming it forever — through the API
// and through Terraform, where a tainted replace left the volume naming a
// server that no longer existed.
func TestDeletingAServerReleasesTheVolumesItHeld(t *testing.T) {
	ts := newTestServer(t)
	srvID := aServer(t, ts, "doomed")
	volID := aVolume(t, ts, "attached")

	if status, out := do(t, ts, "POST", zone+"/servers/"+srvID+"/attach-volume",
		`{"volume_id":"`+volID+`"}`); status != http.StatusOK {
		t.Fatalf("attach-volume: %d %v", status, out)
	}
	if status, _ := do(t, ts, "DELETE", zone+"/servers/"+srvID, ""); status != http.StatusNoContent {
		t.Fatalf("delete server: %d", status)
	}

	_, out := do(t, ts, "GET", zone+"/volumes/"+volID, "")
	vol, _ := out["volume"].(map[string]any)
	if server := vol["server"]; server != nil {
		t.Errorf("the volume still names %v after its server was deleted", server)
	}
	if state, _ := vol["state"].(string); state != "available" {
		t.Errorf("the released volume is %q, want available", state)
	}
}
