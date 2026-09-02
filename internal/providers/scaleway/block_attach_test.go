package scaleway_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/store/storetest"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// The server-volume relationship, over a disk that lives in the block product.
//
// Every operation here was measured against the binary built from 3b00d23 with
// scw 2.56.3 on 2026-08-28, and every one of them failed: attach-volume,
// detach-volume, the update's volume map and a create naming a block volume
// resolved instance/v1 alone, so a disk `scw block volume create` had just made
// — or a root disk the same emulator had published under volumes["0"] — was
// unreachable through all of them. The one that did not answer 404 answered
// worse: detach-volume answered 200 and released nothing.
//
// These are not tests about a root volume. A block volume reaches a server under
// any key, and AttachServerVolumeRequest declares volume_type with "sbs_volume"
// among its values (instance_sdk.go), so the operation is defined over one.

// blockStatusOf reads a block volume's status and how many servers reference it.
func blockStatusOf(t *testing.T, ts *httptest.Server, id string) (status string, references int) {
	t.Helper()
	code, out := do(t, ts, "GET", blockURL+"/volumes/"+id, "")
	if code != http.StatusOK {
		t.Fatalf("block volume %s answers %d, want 200", id, code)
	}
	status, _ = out["status"].(string)
	refs, _ := out["references"].([]any)
	return status, len(refs)
}

// serverVolumeMap reads a server's volumes map.
func serverVolumeMap(t *testing.T, ts *httptest.Server, serverID string) map[string]any {
	t.Helper()
	_, out := do(t, ts, "GET", zone+"/servers/"+serverID, "")
	srv, _ := out["server"].(map[string]any)
	volumes, _ := srv["volumes"].(map[string]any)
	return volumes
}

// A block volume attaches and detaches through the instance server routes, and
// says so in both products afterwards.
//
// `scw instance server attach-volume server-id=… volume-id=<a block volume>
// volume-type=sbs_volume` answered "cannot find resource 'volume'" before #571,
// on a volume created two commands earlier by `scw block volume create`.
func TestABlockVolumeAttachesAndDetachesThroughTheServerRoutes(t *testing.T) {
	ts := newTestServer(t)
	srv := aServer(t, ts, "host")
	vol := blockVolumeWith(t, ts, "data", 10000000000)

	status, body := do(t, ts, "POST", zone+"/servers/"+srv+"/attach-volume", `{"volume_id":"`+vol+`","volume_type":"sbs_volume"}`)
	if status != http.StatusOK {
		t.Fatalf("attach-volume answered %d, want 200: %v", status, body)
	}

	// The entry the server publishes is an instance VolumeServer carrying
	// volume_type "sbs_volume": that value is what sends the Terraform provider
	// to the block fallback, and the instance rendering of a block volume has no
	// volume_type at all.
	volumes := serverVolumeMap(t, ts, srv)
	var entry map[string]any
	for _, v := range volumes {
		listed, _ := v.(map[string]any)
		if id, _ := listed["id"].(string); id == vol {
			entry = listed
		}
	}
	if entry == nil {
		t.Fatalf("the server does not list the attached block volume: %v", volumes)
	}
	if entry["volume_type"] != "sbs_volume" {
		t.Errorf("the attached block volume is published as %v, want sbs_volume", entry["volume_type"])
	}

	if got, refs := blockStatusOf(t, ts, vol); got != "in_use" || refs != 1 {
		t.Errorf("after attach the block volume reads %s/%d references, want in_use/1", got, refs)
	}

	status, body = do(t, ts, "POST", zone+"/servers/"+srv+"/detach-volume", `{"volume_id":"`+vol+`"}`)
	if status != http.StatusOK {
		t.Fatalf("detach-volume answered %d, want 200: %v", status, body)
	}
	if got, refs := blockStatusOf(t, ts, vol); got != "available" || refs != 0 {
		t.Errorf("after detach the block volume reads %s/%d references, want available/0", got, refs)
	}
	for key, v := range serverVolumeMap(t, ts, srv) {
		listed, _ := v.(map[string]any)
		if id, _ := listed["id"].(string); id == vol {
			t.Errorf("the server still lists the detached block volume under %q", key)
		}
	}
}

// A block volume a server holds says in_use, not available.
//
// block/v1's `status` IS the resource state here, and detachStoredVolume already
// set it back to available on the way out while nothing set it to in_use on the
// way in. So a volume created free and then attached answered references:
// [attached] and status: available at the same time — and `scw` polls the
// status, never the references (its own -D trace of `server terminate` shows
// five identical GETs waiting on that field).
func TestAttachingABlockVolumeMarksItInUse(t *testing.T) {
	ts := newTestServer(t)
	srv := aServer(t, ts, "host")
	vol := blockVolumeWith(t, ts, "fresh", 10000000000)

	if got, _ := blockStatusOf(t, ts, vol); got != "available" {
		t.Fatalf("a fresh block volume reads %s, want available: the test measures a transition", got)
	}
	if status, body := do(t, ts, "POST", zone+"/servers/"+srv+"/attach-volume", `{"volume_id":"`+vol+`"}`); status != http.StatusOK {
		t.Fatalf("attach-volume answered %d: %v", status, body)
	}
	if got, refs := blockStatusOf(t, ts, vol); got != "in_use" || refs != 1 {
		t.Errorf("an attached block volume reads %s with %d references, want in_use with 1", got, refs)
	}
}

// Detaching a block ROOT volume really releases it, which is what stops the CLI
// hanging.
//
// `scw instance server terminate` walks GetVolume (instance, 404) → GetVolume
// (block, 200) → detach-volume → then polls the block volume until its status
// leaves in_use. detach-volume resolved kindVolume alone, so it answered 200 and
// changed nothing: measured on 2026-08-28 against a binary built from 3b00d23,
// `scw instance server terminate <server> with-block=true` returned rc=124 at
// twenty-five seconds with five identical block GETs in its own trace, on a
// server anybody can create with `root-volume=sbs:20GB`.
func TestTerminateReleasesABlockRootVolume(t *testing.T) {
	ts := newTestServer(t)
	srv, body := serverWith(t, ts,
		`{"name":"sbs","commercial_type":"DEV1-S","volumes":{"0":{"volume_type":"sbs_volume","size":20000000000}}}`)
	volumes, _ := body["volumes"].(map[string]any)
	root, _ := volumes["0"].(map[string]any)
	rootID, _ := root["id"].(string)
	if rootID == "" {
		t.Fatalf("the server carries no root volume: %v", body)
	}
	if got, _ := blockStatusOf(t, ts, rootID); got != "in_use" {
		t.Fatalf("a root volume in use reads %s, want in_use: the test measures a transition", got)
	}

	if status, out := do(t, ts, "POST", zone+"/servers/"+srv+"/detach-volume", `{"volume_id":"`+rootID+`"}`); status != http.StatusOK {
		t.Fatalf("detach-volume answered %d: %v", status, out)
	}
	// The field the client polls. A 200 that leaves this at in_use is the hang.
	if got, refs := blockStatusOf(t, ts, rootID); got != "available" || refs != 0 {
		t.Fatalf("after detach-volume the block root reads %s with %d references, want available with 0 — this is the poll that never ends", got, refs)
	}
}

// An update's volume map reaches a block volume.
//
// The map is the field a Terraform plan writes, and it refused with "volume …
// does not exist in fr-par-1" about a disk the same emulator had created.
func TestAnUpdatesVolumeMapReachesABlockVolume(t *testing.T) {
	ts := newTestServer(t)
	srv := aServer(t, ts, "host")
	root, _ := serverVolumeMap(t, ts, srv)["0"].(map[string]any)
	rootID, _ := root["id"].(string)
	vol := blockVolumeWith(t, ts, "extra", 10000000000)

	status, body := do(t, ts, "PATCH", zone+"/servers/"+srv,
		`{"volumes":{"0":{"id":"`+rootID+`"},"1":{"id":"`+vol+`"}}}`)
	if status != http.StatusOK {
		t.Fatalf("the update answered %d, want 200: %v", status, body)
	}
	entry, _ := serverVolumeMap(t, ts, srv)["1"].(map[string]any)
	if entry == nil || entry["id"] != vol {
		t.Fatalf("the update did not attach the block volume: %v", serverVolumeMap(t, ts, srv))
	}
	if entry["volume_type"] != "sbs_volume" {
		t.Errorf("the block volume is published as %v, want sbs_volume", entry["volume_type"])
	}
	if got, refs := blockStatusOf(t, ts, vol); got != "in_use" || refs != 1 {
		t.Errorf("the block volume reads %s/%d after the update, want in_use/1", got, refs)
	}
}

// A create naming a block volume attaches it, the way
// additional_volume_ids does.
//
// It was skipped silently: the create answered 201 with the volume left
// detached and nothing saying so, which is the defect
// TestAdditionalVolumesAreAttachedAtCreate was written for, one product
// further on.
func TestACreateNamingABlockVolumeAttachesIt(t *testing.T) {
	ts := newTestServer(t)
	vol := blockVolumeWith(t, ts, "carried", 10000000000)

	srv, body := serverWith(t, ts,
		`{"name":"carrier","commercial_type":"DEV1-S","image":"ubuntu_jammy","volumes":{"1":{"id":"`+vol+`"}}}`)
	volumes, _ := body["volumes"].(map[string]any)
	entry, _ := volumes["1"].(map[string]any)
	if entry == nil || entry["id"] != vol {
		t.Fatalf("the create did not attach the block volume: %v", volumes)
	}
	if entry["volume_type"] != "sbs_volume" {
		t.Errorf("the block volume is published as %v, want sbs_volume", entry["volume_type"])
	}
	if holder := holderOf(t, ts, vol); holder != srv {
		t.Errorf("the block volume names %q, want the server %q", holder, srv)
	}
}

// An instance snapshot of a block volume works, and does not promise the block
// product.
//
// The route resolved kindVolume alone, so it answered 404 on the disk the same
// emulator published under the server's volumes["0"]. It answers now — and the
// TYPE it answers was got wrong once, in this same change, which is why the
// assertion below is about what the value must NOT be.
//
// sbs_snapshot was the first answer, read straight off the SDK's
// VolumeVolumeType enum, and it broke a command: `scw instance image list` calls
// block.GetSnapshot for every image whose root_volume.volume_type is
// sbs_snapshot and fails the WHOLE listing on error (scaleway-cli 2.56.3,
// internal/namespaces/instance/v1/custom_image.go:222). Cutting an image from
// such a snapshot made `scw instance image list` answer "cannot find resource
// 'snapshot'" for the entire zone — measured 2026-08-28, against the emulator
// this test runs in.
//
// So the invariant is not "the type is unified". It is: **a type that promises
// the block product must be answerable by the block product**, and this test
// asserts the promise rather than the spelling, so it keeps holding the day a
// snapshot really does cross the two.
//
// What a client asks for still wins, because the request field "overrides the
// volume_type of the snapshot" in the SDK's own words.
func TestAnInstanceSnapshotOfABlockVolumeDoesNotPromiseTheBlockProduct(t *testing.T) {
	ts := newTestServer(t)
	_, body := serverWith(t, ts,
		`{"name":"sbs","commercial_type":"DEV1-S","volumes":{"0":{"volume_type":"sbs_volume","size":20000000000}}}`)
	volumes, _ := body["volumes"].(map[string]any)
	root, _ := volumes["0"].(map[string]any)
	rootID, _ := root["id"].(string)

	status, out := do(t, ts, "POST", zone+"/snapshots", `{"name":"snap","volume_id":"`+rootID+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("snapshot of a block volume answered %d, want 201: %v", status, out)
	}
	snap, _ := out["snapshot"].(map[string]any)
	snapID, _ := snap["id"].(string)
	base, _ := snap["base_volume"].(map[string]any)
	if base == nil || base["id"] != rootID {
		t.Errorf("the snapshot does not name the volume it was taken of: %v", snap["base_volume"])
	}
	// The size comes from the volume, not from the catalogue default: a
	// snapshot that reports 20 GB of a 40 GB disk is a lie a client stores.
	if size, _ := snap["size"].(float64); size != 20000000000 {
		t.Errorf("the snapshot reports size %v, want the volume's 20000000000", snap["size"])
	}
	// b_ssd would name the product it was NOT taken from.
	if snap["volume_type"] != "unified" {
		t.Errorf("the snapshot of a block volume is typed %v, want unified: the fallback names the "+
			"instance product, and stating the value keeps this test discriminating whatever that "+
			"fallback becomes (it was b_ssd until #393)", snap["volume_type"])
	}
	// And the promise: sbs_snapshot means "this id resolves in block/v1alpha1".
	if snap["volume_type"] == "sbs_snapshot" {
		if status, _ := do(t, ts, "GET", blockURL+"/snapshots/"+snapID, ""); status != http.StatusOK {
			t.Errorf("the snapshot is typed sbs_snapshot and block answers %d for it: "+
				"`scw instance image list` reads block.GetSnapshot on exactly that type and fails the whole listing", status)
		}
	}

	// A named type wins, because the request field overrides.
	status, out = do(t, ts, "POST", zone+"/snapshots",
		`{"name":"unified","volume_id":"`+rootID+`","volume_type":"unified"}`)
	if status != http.StatusCreated {
		t.Fatalf("snapshot with an explicit type answered %d: %v", status, out)
	}
	snap, _ = out["snapshot"].(map[string]any)
	if snap["volume_type"] != "unified" {
		t.Errorf("the client named unified and the snapshot reports %v", snap["volume_type"])
	}
}

// The orphan sweep knows a block volume belongs to a server.
//
// `Owns` declared kindPrivateNIC and kindVolume and not kindBlockVolume, so
// storetest.Orphans — the invariant that no disk names a machine that is gone —
// skipped every disk of one product for as long as that product has existed
// here. This is measurement-integrity's rule 1 made executable: a sweep that
// reports nothing is indistinguishable from a sweep that looked nowhere, so the
// witness is planted. The instance volume beside it is the control: if the
// planting itself were broken, neither would be reported and the test would
// still be green on the mutation.
func TestTheSweepSeesABlockVolumeThatNamesADeadServer(t *testing.T) {
	orphanBlock := &resource.Resource{
		ID:      "block-orphan",
		Kind:    "block/volume",
		Tenant:  resource.Tenant{Provider: scaleway.Name},
		Runtime: map[string]string{"server": "a-server-that-is-gone"},
	}
	orphanInstance := &resource.Resource{
		ID:      "instance-orphan",
		Kind:    "instance/volume",
		Tenant:  resource.Tenant{Provider: scaleway.Name},
		Runtime: map[string]string{"server": "a-server-that-is-gone"},
	}

	found := storetest.Orphans([]*resource.Resource{orphanBlock, orphanInstance}, scaleway.Owns, nil)
	if len(found) != 2 {
		t.Fatalf("the sweep reported %d orphan(s), want both the block and the instance disk: %v", len(found), found)
	}
	var sawBlock bool
	for _, line := range found {
		if strings.Contains(line, "block-orphan") {
			sawBlock = true
		}
	}
	if !sawBlock {
		t.Errorf("the sweep did not report the block volume that names a dead server: %v", found)
	}
}

// A default DEV1-S gets its root disk in the block product, like the cloud.
//
// #365, and the whole of it. A DEV1-S created on a real fr-par-1 account was
// given an SBS root volume — corpus/scaleway/scw-instance.jsonl, recorded
// 2026-08-21 and confirmed 2026-08-24 — and `scw` then read it back through
// GET /block/v1alpha1/zones/fr-par-1/volumes/{id} (200) and deleted it there
// (204). This emulator answered 404 on both, on a path every `scw instance
// server delete` takes.
//
// The client asks for nothing here: no `volumes` map at all, which is what `scw
// instance server create type=DEV1-S image=ubuntu_jammy` actually sends (`scw
// -D`, 2.56.3). So this is the default and not an opt-in.
func TestADefaultRootVolumeLivesInBlockLikeTheCloud(t *testing.T) {
	ts := newTestServer(t)
	_, server := serverWith(t, ts, `{"name":"plain","commercial_type":"DEV1-S","image":"ubuntu_jammy"}`)

	volumes, _ := server["volumes"].(map[string]any)
	root, _ := volumes["0"].(map[string]any)
	if root == nil {
		t.Fatalf("the server carries no root volume: %v", server["volumes"])
	}
	if root["volume_type"] != "sbs_volume" {
		t.Fatalf("a default root volume is %v, want sbs_volume: the cloud gives a DEV1-S an SBS root", root["volume_type"])
	}
	id, _ := root["id"].(string)

	// The read #365 is titled after. Instance keeps its typed 404 so the SDK's
	// fallback fires, and block answers.
	if status, _ := do(t, ts, "GET", zone+"/volumes/"+id, ""); status != http.StatusNotFound {
		t.Errorf("instance answered %d for the root volume, want 404 so getUnknownVolume falls back", status)
	}
	status, got := do(t, ts, "GET", blockURL+"/volumes/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("block answered %d for the root volume, want 200: this is #365", status)
	}
	if got["id"] != id {
		t.Errorf("block answered another volume: %v", got)
	}
}

// A root disk restored from an image names the snapshot it came from.
//
// The cloud does: corpus/scaleway/scw-instance.jsonl seq 9 answers a
// parent_snapshot_id on the root volume of a DEV1-S created from ubuntu_jammy,
// and this emulator answered null there. It became comparable only when #365
// made the volume answerable at all — before that the whole object was missing
// and `corpus --check` excused it wholesale.
func TestARootVolumeNamesTheImageSnapshotItCameFrom(t *testing.T) {
	ts := newTestServer(t)
	_, server := serverWith(t, ts, `{"name":"plain","commercial_type":"DEV1-S","image":"ubuntu_jammy"}`)
	volumes, _ := server["volumes"].(map[string]any)
	root, _ := volumes["0"].(map[string]any)
	id, _ := root["id"].(string)

	_, got := do(t, ts, "GET", blockURL+"/volumes/"+id, "")
	parent, _ := got["parent_snapshot_id"].(string)
	if parent == "" {
		t.Fatalf("the root volume names no parent snapshot: %v", got["parent_snapshot_id"])
	}
	// The identifier the emulator itself publishes for that image's root volume,
	// read back through the image door: two views of one fact, and inventing a
	// third id here would give a client two answers to the same question.
	_, image := do(t, ts, "GET", zone+"/images/"+imageIDOf(t, server), "")
	img, _ := image["image"].(map[string]any)
	imageRoot, _ := img["root_volume"].(map[string]any)
	if imageRoot == nil || imageRoot["id"] != parent {
		t.Errorf("the root volume's parent is %q and the image's root volume is %v: the two must be the same snapshot", parent, image["image"])
	}
}

// imageIDOf reads the image id off a server body.
func imageIDOf(t *testing.T, server map[string]any) string {
	t.Helper()
	image, _ := server["image"].(map[string]any)
	id, _ := image["id"].(string)
	if id == "" {
		t.Fatalf("the server carries no image: %v", server["image"])
	}
	return id
}

// A released block volume says WHEN it was released.
//
// Both recordings carry null on every read while the volume is held and a
// timestamp on the read that follows the detach: scw-instance.jsonl seq 9/14
// null then seq 18 a string, scw-billed-shapes.jsonl seq 2/13/27 null then seq
// 33 a string. `feint corpus --check` reported "last_detached_at is string
// upstream, null here" the moment #365 made this volume comparable.
func TestAReleasedBlockVolumeSaysWhenItWasDetached(t *testing.T) {
	ts := newTestServer(t)
	srv, body := serverWith(t, ts, `{"name":"plain","commercial_type":"DEV1-S"}`)
	volumes, _ := body["volumes"].(map[string]any)
	root, _ := volumes["0"].(map[string]any)
	id, _ := root["id"].(string)

	_, got := do(t, ts, "GET", blockURL+"/volumes/"+id, "")
	if got["last_detached_at"] != nil {
		t.Fatalf("a volume that was never detached reports %v, want null", got["last_detached_at"])
	}

	if status, out := do(t, ts, "DELETE", zone+"/servers/"+srv, ""); status != http.StatusNoContent {
		t.Fatalf("delete server answered %d: %v", status, out)
	}
	_, got = do(t, ts, "GET", blockURL+"/volumes/"+id, "")
	if _, ok := got["last_detached_at"].(string); !ok {
		t.Errorf("a released volume reports last_detached_at %v, want the moment it was released", got["last_detached_at"])
	}
}
