package scaleway_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// serverWith creates a server and returns its id and its decoded body.
func serverWith(t *testing.T, ts *httptest.Server, body string) (string, map[string]any) {
	t.Helper()
	status, created := do(t, ts, "POST", zoneURL+"/servers", body)
	if status != http.StatusCreated {
		t.Fatalf("create server: expected 201, got %d (%v)", status, created)
	}
	server, _ := created["server"].(map[string]any)
	id, _ := server["id"].(string)
	if id == "" {
		t.Fatalf("create server: no id in %v", created)
	}
	return id, server
}

// A server always carries a root volume under key "0". The Terraform provider
// reads it there and sizes the rest with len(volumes)-1, so an empty map is not
// a missing field: it panics the plugin.
//
// Since #365 that disk lives in the BLOCK product, which is where the cloud puts
// it, so this test states the same fact one product further on: the key, the id,
// the type the client branches on, and the read that answers.
func TestServerCarriesARootVolume(t *testing.T) {
	ts := newTestServer(t)

	_, server := serverWith(t, ts, `{"name":"vol","commercial_type":"DEV1-S"}`)
	volumes, _ := server["volumes"].(map[string]any)
	if len(volumes) != 1 {
		t.Fatalf("expected exactly the root volume, got %v", server["volumes"])
	}
	root, ok := volumes["0"].(map[string]any)
	if !ok {
		t.Fatalf(`the root volume is not keyed "0": %v`, volumes)
	}

	volumeID, _ := root["id"].(string)
	if volumeID == "" {
		t.Fatalf("the root volume has no id: %v", root)
	}
	// A LOCAL volume would make the CLI refuse the creation it just asked for,
	// because it sums local volumes against the catalogue constraint. sbs_volume
	// is not local, sums to nothing there, and is what a real DEV1-S is given.
	if root["volume_type"] != "sbs_volume" {
		t.Errorf("root volume type is %v, want sbs_volume", root["volume_type"])
	}

	// Readable where the client goes to read it: block, after a typed 404 on the
	// instance side. Both halves matter — the provider fetches the volume by id
	// right after the create, and it only tries block once instance has refused.
	if status, got := do(t, ts, "GET", zoneURL+"/volumes/"+volumeID, ""); status != http.StatusNotFound {
		t.Fatalf("instance answered %d for a block root, want 404 so the fallback happens (%v)", status, got)
	}
	status, got := do(t, ts, "GET", blockURL+"/volumes/"+volumeID, "")
	if status != http.StatusOK {
		t.Fatalf("get block volume: expected 200, got %d (%v)", status, got)
	}
	if got["id"] != volumeID {
		t.Errorf("block answered another volume: %v", got)
	}
	if holder := holderOf(t, ts, volumeID); holder != server["id"] {
		t.Errorf("the volume names %q, want its server %v", holder, server["id"])
	}
}

// Deleting a server detaches its volumes and keeps them: on Scaleway the disk
// outlives the machine, and the CLI polls each volume after the server is gone.
//
// The sequence asserted here is the one the real cloud answered, recorded in
// corpus/scaleway/scw-instance.jsonl: DELETE the server (204), GET the volume in
// block/v1alpha1 (200), DELETE it there (204).
func TestDeletingAServerKeepsItsVolume(t *testing.T) {
	ts := newTestServer(t)

	id, server := serverWith(t, ts, `{"name":"keep","commercial_type":"DEV1-S"}`)
	volumes, _ := server["volumes"].(map[string]any)
	root, _ := volumes["0"].(map[string]any)
	volumeID, _ := root["id"].(string)

	if status, _ := do(t, ts, "DELETE", zoneURL+"/servers/"+id, ""); status != http.StatusNoContent {
		t.Fatalf("delete server: expected 204, got %d", status)
	}

	status, got := do(t, ts, "GET", blockURL+"/volumes/"+volumeID, "")
	if status != http.StatusOK {
		t.Fatalf("the volume vanished with its server: get returned %d (%v)", status, got)
	}
	// The field `scw` polls before it deletes: a root left in_use is the hang
	// #571 measured, and its references must be empty as well.
	if got["status"] != "available" {
		t.Errorf("the released volume reads status %v, want available", got["status"])
	}
	if refs, _ := got["references"].([]any); len(refs) != 0 {
		t.Errorf("the released volume still references %d server(s): %v", len(refs), got["references"])
	}

	// Detached, it can now be deleted, which is what the CLI does next.
	if status, _ := do(t, ts, "DELETE", blockURL+"/volumes/"+volumeID, ""); status != http.StatusNoContent {
		t.Errorf("delete a detached volume: expected 204, got %d", status)
	}
}

// An attached volume cannot be deleted, and a client that destroys in the wrong
// order depends on that error to retry.
//
// On an INSTANCE volume, which a client still creates explicitly with `scw
// instance volume create` and attaches as an additional disk. The block half of
// the same refusal is TestABlockVolumeAttachedToAServerDoesNotDelete: two
// products, two error shapes, and neither may be inferred from the other.
func TestAttachedVolumeRefusesDeletion(t *testing.T) {
	ts := newTestServer(t)

	server := aServer(t, ts, "busy")
	volumeID := aVolume(t, ts, "busy-extra")
	if status, out := do(t, ts, "POST", zoneURL+"/servers/"+server+"/attach-volume", `{"volume_id":"`+volumeID+`"}`); status != http.StatusOK {
		t.Fatalf("attach-volume answered %d: %v", status, out)
	}

	status, denied := do(t, ts, "DELETE", zoneURL+"/volumes/"+volumeID, "")
	if status != http.StatusBadRequest {
		t.Fatalf("delete an attached volume: expected 400, got %d", status)
	}
	if denied["type"] != "precondition_failed" {
		t.Errorf("got error type %v, want precondition_failed", denied["type"])
	}
}

// The image a client asks for must come back as an object, never null: the
// provider reads server.Image without checking and crashes on a null.
func TestServerCarriesItsImage(t *testing.T) {
	ts := newTestServer(t)

	_, server := serverWith(t, ts, `{"name":"img","commercial_type":"DEV1-S","image":"debian_bookworm"}`)
	image, ok := server["image"].(map[string]any)
	if !ok {
		t.Fatalf("the server carries no image object: %v", server["image"])
	}
	// A label is echoed back as the image name, so a client that asked for
	// Debian does not read Ubuntu.
	if image["name"] != "debian_bookworm" {
		t.Errorf("image name is %v, want debian_bookworm", image["name"])
	}
	if image["id"] == nil || image["id"] == "" {
		t.Errorf("the image has no id: %v", image)
	}
	if root, _ := image["root_volume"].(map[string]any); root == nil {
		t.Errorf("the image has no root volume: %v", image)
	}
}
