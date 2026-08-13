package scaleway_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The golden-image path, which is why snapshots and images are served at all
// (SW-2): snapshot a volume, cut an image from it, read both back.
//
// Each test below names a refusal the handlers cite by name. They are here
// because a comment saying "this case is refused" is the defect this repository
// has met three times: the sentence survives, the guard does not.

// snapshotOfVolume takes a snapshot and returns its id.
func snapshotOfVolume(t *testing.T, ts *httptest.Server, name, volumeID string) string {
	t.Helper()
	status, created := do(t, ts, "POST", zoneURL+"/snapshots",
		`{"name":"`+name+`","volume_id":"`+volumeID+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("create snapshot: expected 201, got %d (%v)", status, created)
	}
	snapshot, _ := created["snapshot"].(map[string]any)
	id, _ := snapshot["id"].(string)
	if id == "" {
		t.Fatalf("create snapshot: no id in %v", created)
	}
	return id
}

// rootVolumeOf reads the id of the root volume a server carries.
func rootVolumeOf(t *testing.T, server map[string]any) string {
	t.Helper()
	volumes, _ := server["volumes"].(map[string]any)
	root, _ := volumes["0"].(map[string]any)
	id, _ := root["id"].(string)
	if id == "" {
		t.Fatalf("the server carries no root volume: %v", server["volumes"])
	}
	return id
}

// The sequence a client walks, end to end: what a create answers, the following
// read answers identically. A create whose GET disagrees is the most common
// cause of "Provider produced inconsistent result after apply".
func TestASnapshotReadsBackAsItWasCreated(t *testing.T) {
	ts := newTestServer(t)
	_, server := serverWith(t, ts, `{"name":"golden","commercial_type":"DEV1-S"}`)
	volumeID := rootVolumeOf(t, server)

	status, created := do(t, ts, "POST", zoneURL+"/snapshots",
		`{"name":"golden-snap","volume_id":"`+volumeID+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("create snapshot: expected 201, got %d (%v)", status, created)
	}
	snapshot, _ := created["snapshot"].(map[string]any)

	// Immediately usable: a snapshot lingering in "snapshotting" would only make
	// a client wait for a transition this emulator has no information about.
	if snapshot["state"] != "available" {
		t.Errorf("state is %v, want available", snapshot["state"])
	}
	base, _ := snapshot["base_volume"].(map[string]any)
	if base == nil || base["id"] != volumeID {
		t.Errorf("base_volume does not name the volume snapshotted: %v", snapshot["base_volume"])
	}
	// The SDK declares error_reason as a pointer a caller may read: present and
	// null, never absent.
	if got, present := snapshot["error_reason"]; !present || got != nil {
		t.Errorf("error_reason is %v (present=%v), want null and present", got, present)
	}

	id, _ := snapshot["id"].(string)
	status, got := do(t, ts, "GET", zoneURL+"/snapshots/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("get snapshot: expected 200, got %d (%v)", status, got)
	}
	read, _ := got["snapshot"].(map[string]any)
	for _, field := range []string{"id", "name", "state", "size", "volume_type", "creation_date", "zone"} {
		if read[field] != snapshot[field] {
			t.Errorf("%s reads back as %v, created as %v", field, read[field], snapshot[field])
		}
	}

	status, listed := do(t, ts, "GET", zoneURL+"/snapshots?name=golden", "")
	if status != http.StatusOK {
		t.Fatalf("list snapshots: expected 200, got %d (%v)", status, listed)
	}
	if items, _ := listed["snapshots"].([]any); len(items) != 1 {
		t.Errorf("list by name found %d snapshots, want 1: %v", len(items), listed)
	}
}

// A snapshot of a volume nobody created is refused rather than recorded.
//
// Answering success about a snapshot of nothing is the half-success this
// project exists to avoid, and unlike an image identifier nobody hardcodes a
// production volume id into a fixture, so refusing costs no script.
func TestASnapshotOfNothingIsRefused(t *testing.T) {
	ts := newTestServer(t)

	status, got := do(t, ts, "POST", zoneURL+"/snapshots",
		`{"name":"orphan","volume_id":"11111111-1111-4111-8111-111111111111"}`)
	if status != http.StatusNotFound {
		t.Fatalf("snapshot of an unknown volume: expected 404, got %d (%v)", status, got)
	}

	// And the store did not keep it: a refusal that records anyway is a refusal
	// in the response only.
	_, listed := do(t, ts, "GET", zoneURL+"/snapshots", "")
	if items, _ := listed["snapshots"].([]any); len(items) != 0 {
		t.Errorf("the refused snapshot was recorded anyway: %v", listed)
	}
}

// Importing from Object Storage stays refused, and says which argument.
//
// docs/limits.md carries why Object Storage is not emulated. A snapshot
// restored from a bucket nobody serves would answer success about bytes that do
// not exist, which is worse than the refusal.
func TestASnapshotImportedFromABucketIsRefused(t *testing.T) {
	ts := newTestServer(t)

	status, got := do(t, ts, "POST", zoneURL+"/snapshots",
		`{"name":"imported","bucket":"backups","key":"disk.qcow2"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("import from a bucket: expected 400, got %d (%v)", status, got)
	}
}

// Deleting a snapshot an image is cut from is refused.
//
// The invariant volumes already hold, and the order Terraform walks when one
// plan removes an image and its snapshot: without the refusal the image is left
// naming a snapshot that is gone, and the client has no signal to retry.
func TestASnapshotAnImageIsCutFromDoesNotDelete(t *testing.T) {
	ts := newTestServer(t)
	_, server := serverWith(t, ts, `{"name":"golden","commercial_type":"DEV1-S"}`)
	snapshotID := snapshotOfVolume(t, ts, "golden-snap", rootVolumeOf(t, server))

	status, created := do(t, ts, "POST", zoneURL+"/images",
		`{"name":"golden-img","root_volume":"`+snapshotID+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("create image: expected 201, got %d (%v)", status, created)
	}

	// 400 with type=precondition_failed in the body, which is where Scaleway's
	// HTTP API puts it and what the SDK branches on; writePrecondition carries
	// the reasoning.
	status, got := do(t, ts, "DELETE", zoneURL+"/snapshots/"+snapshotID, "")
	if status != http.StatusBadRequest || got["type"] != "precondition_failed" {
		t.Fatalf("delete a snapshot an image is cut from: expected 400 precondition_failed, got %d (%v)", status, got)
	}
	// Still there, which is the point of refusing.
	if status, _ := do(t, ts, "GET", zoneURL+"/snapshots/"+snapshotID, ""); status != http.StatusOK {
		t.Errorf("the snapshot went away despite the refusal: get answered %d", status)
	}

	// And the order that does work: the image first, then the snapshot.
	image, _ := created["image"].(map[string]any)
	imageID, _ := image["id"].(string)
	if status, got := do(t, ts, "DELETE", zoneURL+"/images/"+imageID, ""); status != http.StatusNoContent {
		t.Fatalf("delete image: expected 204, got %d (%v)", status, got)
	}
	if status, got := do(t, ts, "DELETE", zoneURL+"/snapshots/"+snapshotID, ""); status != http.StatusNoContent {
		t.Errorf("delete snapshot after its image: expected 204, got %d (%v)", status, got)
	}
}
