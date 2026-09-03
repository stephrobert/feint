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

// instanceVolumeOf makes a volume instance/v1 owns, which is the only kind it
// snapshots.
//
// The tests here used to hand rootVolumeOf(server) to snapshotOfVolume, and that
// disk lives in BLOCK since #365. fr-par answers 404 `instance_volume` to an
// instance/v1 snapshot of a block volume (#648), so those tests were exercising
// a route the cloud does not have.
func instanceVolumeOf(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	status, created := do(t, ts, "POST", zoneURL+"/volumes",
		`{"name":"snapshot-subject","volume_type":"l_ssd","size":10000000000}`)
	if status != http.StatusCreated {
		t.Fatalf("create an instance volume: expected 201, got %d (%v)", status, created)
	}
	volume, _ := created["volume"].(map[string]any)
	id, _ := volume["id"].(string)
	if id == "" {
		t.Fatalf("create an instance volume: no id in %v", created)
	}
	// Attached, because a disk nothing was ever attached to has nothing to
	// snapshot and fr-par refuses it (#650). The server is the cheapest way to
	// give the disk a history; it is never started, which the measurement says
	// is enough.
	serverID, _ := serverWith(t, ts, `{"name":"snapshot-host","commercial_type":"DEV1-S"}`)
	if status, out := do(t, ts, "POST", zoneURL+"/servers/"+serverID+"/attach-volume",
		`{"volume_id":"`+id+`"}`); status != http.StatusOK {
		t.Fatalf("attach the volume: expected 200, got %d (%v)", status, out)
	}
	return id
}

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

// The sequence a client walks, end to end: what a create answers, the following
// read answers identically. A create whose GET disagrees is the most common
// cause of "Provider produced inconsistent result after apply".
func TestASnapshotReadsBackAsItWasCreated(t *testing.T) {
	ts := newTestServer(t)
	// An instance volume, not a server's root disk: that one lives in block
	// since #365, and instance/v1 does not snapshot what it does not own (#648).
	volumeID := instanceVolumeOf(t, ts)

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
	snapshotID := snapshotOfVolume(t, ts, "golden-snap", instanceVolumeOf(t, ts))

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

// A disk nothing was ever attached to has nothing to snapshot, and the cloud
// says so in those words (#650).
//
// Measured on fr-par, 2026-09-03, three ways, because the line between accepted
// and refused is not where it first looks:
//
//	a volume created and never attached          400 "cannot create a RO disk from an empty disk"
//	the root disk of a server that never started 201
//	a disk detached from a deleted server        201
//
// So the question is not whether the machine ran. It is whether the disk was
// ever anybody's — which is why the mark is set on attach and never cleared on
// detach.
//
// Why it is worth a refusal rather than a shrug: the published example stack
// built its golden image exactly this way, so the pattern it teaches did not
// survive contact with the real cloud. The reporter found out by running their
// copy of it there.
func TestASnapshotOfAVolumeNothingEverWroteToIsRefused(t *testing.T) {
	ts := newTestServer(t)

	status, created := do(t, ts, "POST", zoneURL+"/volumes",
		`{"name":"empty","volume_type":"l_ssd","size":10000000000}`)
	if status != http.StatusCreated {
		t.Fatalf("create a volume: %d (%v)", status, created)
	}
	volume, _ := created["volume"].(map[string]any)
	volumeID, _ := volume["id"].(string)

	status, body := do(t, ts, "POST", zoneURL+"/snapshots",
		`{"name":"of-nothing","volume_id":"`+volumeID+`"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("a snapshot of a never-attached volume answered %d, want 400 (%v)", status, body)
	}
	if body["type"] != "invalid_arguments" {
		t.Errorf("type is %v, want invalid_arguments", body["type"])
	}
	details, _ := body["details"].([]any)
	if len(details) != 1 {
		t.Fatalf("details holds %d entries, want 1 (%v)", len(details), body)
	}
	d, _ := details[0].(map[string]any)
	if d["help_message"] != "cannot create a RO disk from an empty disk" {
		t.Errorf("help_message is %v, want the cloud's own sentence", d["help_message"])
	}
	if d["reason"] != "constraint" {
		t.Errorf("reason is %v, want constraint", d["reason"])
	}
	// No argument_name at all in that entry, which is why ArgumentError omits
	// the field when it is empty. A `"argument_name": ""` is a field the cloud
	// does not send.
	if _, carries := d["argument_name"]; carries {
		t.Errorf("the details entry carries an argument_name and fr-par sends none: %v", d)
	}

	// Attached once, and it can be snapshotted — the accepting half, without
	// which a guard that refused every snapshot would satisfy everything above.
	serverID, _ := serverWith(t, ts, `{"name":"holder","commercial_type":"DEV1-S"}`)
	if status, out := do(t, ts, "POST", zoneURL+"/servers/"+serverID+"/attach-volume",
		`{"volume_id":"`+volumeID+`"}`); status != http.StatusOK {
		t.Fatalf("attach the volume: %d (%v)", status, out)
	}
	if status, out := do(t, ts, "POST", zoneURL+"/snapshots",
		`{"name":"of-something","volume_id":"`+volumeID+`"}`); status != http.StatusCreated {
		t.Fatalf("a snapshot of an attached volume answered %d, want 201 (%v)", status, out)
	}

	// And still after it is detached: the disk keeps what the machine wrote, so
	// deleting the server does not empty it. Measured — this is the half that
	// makes the mark a history rather than a current state.
	if status, out := do(t, ts, "DELETE", zoneURL+"/servers/"+serverID, ""); status != http.StatusNoContent {
		t.Fatalf("delete the server: %d (%v)", status, out)
	}
	if status, out := do(t, ts, "POST", zoneURL+"/snapshots",
		`{"name":"after-detach","volume_id":"`+volumeID+`"}`); status != http.StatusCreated {
		t.Fatalf("a snapshot of a detached volume answered %d, want 201: the mark is a current "+
			"state where it must be a history (%v)", status, out)
	}
}
