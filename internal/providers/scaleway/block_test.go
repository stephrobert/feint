package scaleway_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Block Storage (SW-3), and above all the path #8 exists to unblock: a server
// whose root volume is an sbs_volume, read back the way the Terraform provider
// reads it — instance first, block on the fallback.

const blockURL = "/block/v1/zones/fr-par-1"

// blockVolumeWith creates a blank block volume and returns its id.
func blockVolumeWith(t *testing.T, ts *httptest.Server, name string, size int) string {
	t.Helper()
	status, created := do(t, ts, "POST", blockURL+"/volumes",
		`{"name":"`+name+`","from_empty":{"size":`+itoa(size)+`}}`)
	if status != http.StatusOK {
		t.Fatalf("create block volume: expected 200, got %d (%v)", status, created)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("create block volume: no id in %v", created)
	}
	return id
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// The measurement @vde-dis reported, turned into a test.
//
// A server created with root_volume.volume_type = "sbs_volume" must be absent
// from the instance volumes endpoint and present on the block one. That is not
// a detail of storage: the Terraform provider reads every volume through
// GetUnknownVolume, instance first and block only on a typed 404, so a volume
// answering on the instance side would never exercise the fallback, and one
// answering on neither is the "waiting for Volume failed: http error 404 Not
// Found" that killed the apply before SW-3.
func TestAnSbsRootVolumeIsReadableThroughTheBlockFallback(t *testing.T) {
	ts := newTestServer(t)

	_, server := serverWith(t, ts,
		`{"name":"sbs","commercial_type":"DEV1-S","volumes":{"0":{"volume_type":"sbs_volume","size":20000000000}}}`)

	volumes, _ := server["volumes"].(map[string]any)
	root, _ := volumes["0"].(map[string]any)
	if root == nil {
		t.Fatalf("the server carries no root volume: %v", server["volumes"])
	}
	// The value that tells the provider to fall back at all.
	if root["volume_type"] != "sbs_volume" {
		t.Fatalf("root volume_type is %v, want sbs_volume: honouring the request is the whole point", root["volume_type"])
	}
	id, _ := root["id"].(string)

	// Step one of GetUnknownVolume: instance answers a typed 404, and the type
	// is what the SDK branches on. A plain-text 404 leaves the provider with
	// nothing to fall back from.
	status, body := do(t, ts, "GET", zoneURL+"/volumes/"+id, "")
	if status != http.StatusNotFound {
		t.Errorf("instance answered %d for an sbs volume, want 404 so the fallback happens", status)
	}
	if body["type"] == nil {
		t.Errorf("the instance 404 carries no type, and the SDK branches on it: %v", body)
	}

	// Step two: block answers it.
	status, got := do(t, ts, "GET", blockURL+"/volumes/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("block answered %d for the root volume, want 200 (this is #8's apply failure)", status)
	}
	if got["id"] != id {
		t.Errorf("block answered another volume: %v", got)
	}
	// And it knows what it is attached to, which is how a client sees the link.
	refs, _ := got["references"].([]any)
	if len(refs) != 1 {
		t.Fatalf("the attached volume reports %d references, want 1: %v", len(refs), got["references"])
	}
	ref, _ := refs[0].(map[string]any)
	if ref["product_resource_id"] != server["id"] {
		t.Errorf("the reference names %v, want the server %v", ref["product_resource_id"], server["id"])
	}
	if ref["status"] != "attached" || ref["type"] != "exclusive" {
		t.Errorf("the reference reads %v/%v, want attached/exclusive", ref["status"], ref["type"])
	}
}

// A reference identifier that moves between two reads is a permanent Terraform
// diff. It is derived from the pair it joins for exactly that reason.
func TestAReferenceKeepsItsIdentifierBetweenTwoReads(t *testing.T) {
	ts := newTestServer(t)
	_, server := serverWith(t, ts,
		`{"name":"sbs","commercial_type":"DEV1-S","volumes":{"0":{"volume_type":"sbs_volume"}}}`)
	volumes, _ := server["volumes"].(map[string]any)
	root, _ := volumes["0"].(map[string]any)
	id, _ := root["id"].(string)

	first := referenceIDOf(t, ts, id)
	second := referenceIDOf(t, ts, id)
	if first != second {
		t.Errorf("the reference identifier moved between two reads: %q then %q", first, second)
	}
	if first == "" {
		t.Error("the reference carries no identifier")
	}
}

func referenceIDOf(t *testing.T, ts *httptest.Server, volumeID string) string {
	t.Helper()
	_, got := do(t, ts, "GET", blockURL+"/volumes/"+volumeID, "")
	refs, _ := got["references"].([]any)
	if len(refs) == 0 {
		return ""
	}
	ref, _ := refs[0].(map[string]any)
	id, _ := ref["id"].(string)
	return id
}

// The lifecycle a client walks, and the shape the recording of a real account
// showed.
func TestABlockVolumeReadsBackAsItWasCreated(t *testing.T) {
	ts := newTestServer(t)

	status, created := do(t, ts, "POST", blockURL+"/volumes",
		`{"name":"data","from_empty":{"size":10000000000},"tags":["keep"]}`)
	if status != http.StatusOK {
		t.Fatalf("create: expected 200, got %d (%v)", status, created)
	}
	// The envelope is the object itself: block/v1 answers bare where instance/v1
	// wraps in {"volume": ...}. Two products of one cloud that do not agree.
	if created["volume"] != nil {
		t.Errorf("the response is wrapped, and block/v1 answers the resource bare: %v", created)
	}
	// Present and null on every volume the recorded account returned.
	for _, field := range []string{"kms_key_id", "last_detached_at"} {
		if got, present := created[field]; !present || got != nil {
			t.Errorf("%s is %v (present=%v), want null and present", field, got, present)
		}
	}
	specs, _ := created["specs"].(map[string]any)
	if specs == nil || specs["class"] != "sbs" {
		t.Errorf("specs.class is %v, want sbs: %v", specs["class"], created)
	}
	// Never null: a client ranging over it crashes, and the recording shows an
	// array on a volume attached to nothing.
	if _, ok := created["references"].([]any); !ok {
		t.Errorf("references is %v, want an array even when nothing is attached", created["references"])
	}

	id, _ := created["id"].(string)
	status, read := do(t, ts, "GET", blockURL+"/volumes/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("get: expected 200, got %d (%v)", status, read)
	}
	for _, field := range []string{"id", "name", "size", "type", "status", "created_at", "zone"} {
		if read[field] != created[field] {
			t.Errorf("%s reads back as %v, created as %v", field, read[field], created[field])
		}
	}

	status, listed := do(t, ts, "GET", blockURL+"/volumes", "")
	if status != http.StatusOK {
		t.Fatalf("list: expected 200, got %d (%v)", status, listed)
	}
	if listed["total_count"] == nil {
		t.Error("the listing carries no total_count, and the SDK decodes one")
	}
}

// "Precisely one of FromEmpty, FromSnapshot must be set" is the SDK's own
// wording. Neither leaves the size undecidable; both leaves it ambiguous.
func TestABlockVolumeFromNeitherOrBothIsRefused(t *testing.T) {
	ts := newTestServer(t)

	for _, body := range []string{
		`{"name":"neither"}`,
		`{"name":"both","from_empty":{"size":10000000000},"from_snapshot":{"snapshot_id":"11111111-1111-4111-8111-111111111111"}}`,
	} {
		status, got := do(t, ts, "POST", blockURL+"/volumes", body)
		if status != http.StatusBadRequest {
			t.Errorf("create with %s: expected 400, got %d (%v)", body, status, got)
		}
	}

	// And a snapshot nobody took is a 404, not a record of nothing.
	status, got := do(t, ts, "POST", blockURL+"/volumes",
		`{"name":"orphan","from_snapshot":{"snapshot_id":"11111111-1111-4111-8111-111111111111"}}`)
	if status != http.StatusNotFound {
		t.Errorf("create from an unknown snapshot: expected 404, got %d (%v)", status, got)
	}
}

// A volume grows and does not shrink. Answering success on a smaller size would
// tell a client its data survived a truncation nothing performed.
func TestABlockVolumeDoesNotShrink(t *testing.T) {
	ts := newTestServer(t)
	id := blockVolumeWith(t, ts, "data", 10000000000)

	status, got := do(t, ts, "PATCH", blockURL+"/volumes/"+id, `{"size":5000000000}`)
	if status != http.StatusBadRequest {
		t.Fatalf("shrink: expected 400, got %d (%v)", status, got)
	}
	// Growing works, which is the half a guard that refuses everything would
	// also pass.
	status, grown := do(t, ts, "PATCH", blockURL+"/volumes/"+id, `{"size":20000000000}`)
	if status != http.StatusOK {
		t.Fatalf("grow: expected 200, got %d (%v)", status, grown)
	}
	if grown["size"] != float64(20000000000) {
		t.Errorf("size after growing is %v, want 20000000000", grown["size"])
	}
}

// An encryption key names Scaleway's Key Manager, which is not emulated.
// Accepting it would answer success about an encryption nothing performs.
func TestABlockVolumeWithAKeyManagerKeyIsRefused(t *testing.T) {
	ts := newTestServer(t)

	status, got := do(t, ts, "POST", blockURL+"/volumes",
		`{"name":"secret","from_empty":{"size":10000000000},"kms_key_id":"11111111-1111-4111-8111-111111111111"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("create with a KMS key: expected 400, got %d (%v)", status, got)
	}
}

// Deleting a volume out from under its server is refused, which is the
// invariant instance volumes already hold and the one Terraform depends on.
func TestABlockVolumeAttachedToAServerDoesNotDelete(t *testing.T) {
	ts := newTestServer(t)
	_, server := serverWith(t, ts,
		`{"name":"sbs","commercial_type":"DEV1-S","volumes":{"0":{"volume_type":"sbs_volume"}}}`)
	volumes, _ := server["volumes"].(map[string]any)
	root, _ := volumes["0"].(map[string]any)
	id, _ := root["id"].(string)

	status, got := do(t, ts, "DELETE", blockURL+"/volumes/"+id, "")
	if status != http.StatusBadRequest || got["type"] != "precondition_failed" {
		t.Fatalf("delete an attached volume: expected 400 precondition_failed, got %d (%v)", status, got)
	}
	if status, _ := do(t, ts, "GET", blockURL+"/volumes/"+id, ""); status != http.StatusOK {
		t.Errorf("the volume went away despite the refusal: get answered %d", status)
	}

	// The order that works: the server first, then the disk it outlived.
	serverID, _ := server["id"].(string)
	if status, _ := do(t, ts, "POST", zoneURL+"/servers/"+serverID+"/action", `{"action":"poweroff"}`); status >= 400 {
		t.Fatalf("poweroff answered %d", status)
	}
	if status, got := do(t, ts, "DELETE", zoneURL+"/servers/"+serverID, ""); status >= 400 {
		t.Fatalf("delete server: %d (%v)", status, got)
	}
	if status, got := do(t, ts, "DELETE", blockURL+"/volumes/"+id, ""); status != http.StatusNoContent {
		t.Errorf("delete the volume once its server is gone: expected 204, got %d (%v)", status, got)
	}
}

// A snapshot of a volume nobody created is refused rather than recorded, for
// the reason the instance side refuses one.
func TestABlockSnapshotOfNothingIsRefused(t *testing.T) {
	ts := newTestServer(t)

	status, got := do(t, ts, "POST", blockURL+"/snapshots",
		`{"name":"orphan","volume_id":"11111111-1111-4111-8111-111111111111"}`)
	if status != http.StatusNotFound {
		t.Fatalf("snapshot of an unknown volume: expected 404, got %d (%v)", status, got)
	}
	_, listed := do(t, ts, "GET", blockURL+"/snapshots", "")
	if items, _ := listed["snapshots"].([]any); len(items) != 0 {
		t.Errorf("the refused snapshot was recorded anyway: %v", listed)
	}
}

// The order the API imposes when a plan removes a volume and the snapshot it
// was restored from.
func TestABlockSnapshotAVolumeCameFromDoesNotDelete(t *testing.T) {
	ts := newTestServer(t)
	volumeID := blockVolumeWith(t, ts, "source", 10000000000)

	status, snap := do(t, ts, "POST", blockURL+"/snapshots",
		`{"name":"snap","volume_id":"`+volumeID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("create snapshot: expected 200, got %d (%v)", status, snap)
	}
	snapID, _ := snap["id"].(string)
	parent, _ := snap["parent_volume"].(map[string]any)
	if parent == nil || parent["id"] != volumeID {
		t.Errorf("the snapshot does not name the volume it came from: %v", snap["parent_volume"])
	}

	// A volume restored from it, which is what makes the snapshot undeletable.
	status, restored := do(t, ts, "POST", blockURL+"/volumes",
		`{"name":"restored","from_snapshot":{"snapshot_id":"`+snapID+`"}}`)
	if status != http.StatusOK {
		t.Fatalf("restore: expected 200, got %d (%v)", status, restored)
	}
	// The size comes from the snapshot when no resize is asked for.
	if restored["size"] != snap["size"] {
		t.Errorf("the restored volume is %v, and its snapshot was %v", restored["size"], snap["size"])
	}
	if restored["parent_snapshot_id"] != snapID {
		t.Errorf("the restored volume does not name its snapshot: %v", restored["parent_snapshot_id"])
	}

	status, got := do(t, ts, "DELETE", blockURL+"/snapshots/"+snapID, "")
	if status != http.StatusBadRequest || got["type"] != "precondition_failed" {
		t.Fatalf("delete a snapshot a volume came from: expected 400 precondition_failed, got %d (%v)", status, got)
	}

	restoredID, _ := restored["id"].(string)
	if status, _ := do(t, ts, "DELETE", blockURL+"/volumes/"+restoredID, ""); status != http.StatusNoContent {
		t.Fatalf("delete the restored volume: %d", status)
	}
	if status, got := do(t, ts, "DELETE", blockURL+"/snapshots/"+snapID, ""); status != http.StatusNoContent {
		t.Errorf("delete the snapshot once nothing came from it: expected 204, got %d (%v)", status, got)
	}
}

// The catalogue is served rather than declined, for the reason CLAUDE.md
// records about the instance one: a client reads the stock before it creates,
// and gives up on a 404.
func TestTheBlockCatalogueAnswers(t *testing.T) {
	ts := newTestServer(t)

	status, got := do(t, ts, "GET", blockURL+"/volume-types", "")
	if status != http.StatusOK {
		t.Fatalf("volume types: expected 200, got %d (%v)", status, got)
	}
	types, _ := got["volume_types"].([]any)
	if len(types) == 0 {
		t.Fatal("the catalogue answered no volume type, and a client reads it before creating")
	}
	entry, _ := types[0].(map[string]any)
	if entry["type"] != "sbs" {
		t.Errorf("the catalogue offers %v, and the volumes this pack creates are sbs", entry["type"])
	}
	if entry["pricing"] == nil {
		t.Error("the entry carries no pricing, and the SDK decodes a Money there")
	}
}

// A volume restored from a snapshot may grow, and may not shrink below it.
//
// The guard existed on updateBlockVolume and only there, while its comment
// stated a property of the volume — "a volume grows and does not shrink". An
// audit created a 1 GB volume from a 10 GB snapshot and got 201 with a healthy
// "available": the data cannot fit, and the emulator said it did.
//
// Nothing else could have seen it. The barrage of #134 drives no block route,
// and the invariant sweep asks about identity and uniqueness, not about whether
// a size makes sense. Only a client suite would have caught it, and no fixture
// creates from a snapshot with a smaller size.
func TestABlockVolumeRestoredSmallerThanItsSnapshotIsRefused(t *testing.T) {
	ts := newTestServer(t)
	volumeID := blockVolumeWith(t, ts, "source", 10000000000)

	status, snap := do(t, ts, "POST", blockURL+"/snapshots",
		`{"name":"snap","volume_id":"`+volumeID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("create snapshot: expected 200, got %d (%v)", status, snap)
	}
	snapID, _ := snap["id"].(string)

	status, got := do(t, ts, "POST", blockURL+"/volumes",
		`{"name":"shrunk","from_snapshot":{"snapshot_id":"`+snapID+`","size":1000000000}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("restore into a volume smaller than the snapshot: expected 400, got %d (%v)", status, got)
	}

	// The accepting halves, because a guard that refuses every resize would pass
	// the assertion above and break the product: growing works, and asking for
	// nothing takes the snapshot's own size.
	status, grown := do(t, ts, "POST", blockURL+"/volumes",
		`{"name":"grown","from_snapshot":{"snapshot_id":"`+snapID+`","size":20000000000}}`)
	if status != http.StatusOK {
		t.Fatalf("restore into a larger volume: expected 200, got %d (%v)", status, grown)
	}
	if grown["size"] != float64(20000000000) {
		t.Errorf("the grown volume is %v, want 20000000000", grown["size"])
	}

	status, same := do(t, ts, "POST", blockURL+"/volumes",
		`{"name":"same","from_snapshot":{"snapshot_id":"`+snapID+`"}}`)
	if status != http.StatusOK {
		t.Fatalf("restore with no size: expected 200, got %d (%v)", status, same)
	}
	if same["size"] != snap["size"] {
		t.Errorf("with no size the volume is %v, and its snapshot is %v", same["size"], snap["size"])
	}
}

// TestBlockListsHonourThePageSize: both block lists predate the pack's paging
// helper and served everything whatever the client asked. Invisible while the
// emulated account never held two block volumes; the seeded probe (#163) made
// two and measured `per_page=1` answering both. The refusal to regress is
// cheap: two volumes, two snapshots, one page of one.
func TestBlockListsHonourThePageSize(t *testing.T) {
	ts := newTestServer(t)

	for _, name := range []string{"first", "second"} {
		status, created := do(t, ts, "POST", blockURL+"/volumes",
			`{"name":"`+name+`","from_empty":{"size":10000000000}}`)
		if status != http.StatusOK {
			t.Fatalf("create volume %s: expected 200, got %d (%v)", name, status, created)
		}
		id, _ := created["id"].(string)
		status, snapped := do(t, ts, "POST", blockURL+"/snapshots",
			`{"name":"snap-`+name+`","volume_id":"`+id+`"}`)
		if status != http.StatusOK {
			t.Fatalf("create snapshot of %s: expected 200, got %d (%v)", name, status, snapped)
		}
	}

	for _, list := range []struct{ path, field string }{
		{"/volumes", "volumes"},
		{"/snapshots", "snapshots"},
	} {
		status, got := do(t, ts, "GET", blockURL+list.path+"?per_page=1", "")
		if status != http.StatusOK {
			t.Fatalf("list %s: expected 200, got %d (%v)", list.path, status, got)
		}
		items, _ := got[list.field].([]any)
		if len(items) != 1 {
			t.Errorf("%s?per_page=1 answered %d items, and the parameter is served on every other list", list.path, len(items))
		}
		if count, _ := got["total_count"].(float64); count != 2 {
			t.Errorf("%s total_count is %v, and the page must not shrink it", list.path, got["total_count"])
		}
	}
}
