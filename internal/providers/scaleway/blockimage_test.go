package scaleway_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/store"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// An Instance image is cut from an SBS snapshot, and that is now the only path
// to a golden image (#651).
//
// b_ssd is retired (#393), so every root volume is SBS, and an instance
// snapshot of an SBS root volume is refused upstream (#648): a stack that
// builds an image goes through scaleway_block_snapshot or it does not build
// one. The emulator resolved instance snapshots only, and answered 404 to the
// id Terraform hands it.
//
// The shape is copied from a real account, 2026-09-03, reading an image whose
// own root_volume named a block snapshot:
//
//	"root_volume": {"id": "<the block snapshot>", "name": "",
//	                "size": 10000000000, "volume_type": "sbs_snapshot"}
func TestAnImageIsCutFromAnSBSSnapshot(t *testing.T) {
	ts := newTestServer(t)

	status, volume := do(t, ts, "POST", blockURL+"/volumes",
		`{"name":"root","from_empty":{"size":10000000000}}`)
	if status != http.StatusOK {
		t.Fatalf("create a volume: %d (%v)", status, volume)
	}
	volumeID, _ := volume["id"].(string)

	status, snapshot := do(t, ts, "POST", blockURL+"/snapshots",
		`{"name":"reference","volume_id":"`+volumeID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("create a block snapshot: %d (%v)", status, snapshot)
	}
	snapshotID, _ := snapshot["id"].(string)

	status, image := do(t, ts, "POST", zoneURL+"/images",
		`{"name":"golden","root_volume":"`+snapshotID+`","arch":"x86_64"}`)
	if status != http.StatusCreated {
		t.Fatalf("cut an image from a block snapshot: expected 201, got %d (%v)", status, image)
	}
	made, _ := image["image"].(map[string]any)
	root, _ := made["root_volume"].(map[string]any)
	if got, _ := root["id"].(string); got != snapshotID {
		t.Errorf("the image's root volume is %q, want the snapshot %q", got, snapshotID)
	}
	if got, _ := root["volume_type"].(string); got != "sbs_snapshot" {
		t.Errorf("the image's root volume is of type %q, want sbs_snapshot", got)
	}
	// Empty on the real account, on an image whose snapshot carried a name.
	if got, _ := root["name"].(string); got != "" {
		t.Errorf("the image's root volume is named %q, and the real cloud names it \"\"", got)
	}
	if got, _ := root["size"].(float64); got != 10000000000 {
		t.Errorf("the image's root volume is %v bytes, want the snapshot's 10000000000", root["size"])
	}
}

// The half of #651 the measurement refused, and it is here so nobody "fixes"
// it later: the issue asks for the same id to resolve through the Instance
// API, and the real cloud does not resolve it.
//
// Measured 2026-09-03 on a real account, with a block snapshot in `available`:
//
//	$ scw instance snapshot get <that id>
//	cannot find resource 'instance_snapshot' with ID '<that id>'
//	$ scw instance snapshot list
//	[]
//
// So the 404 is the cloud's answer, and serving the snapshot there would be a
// divergence. What the cloud does accept is the id as an image's root volume,
// which is TestAnImageIsCutFromAnSBSSnapshot above.
func TestABlockSnapshotStaysInvisibleToTheInstanceAPI(t *testing.T) {
	ts := newTestServer(t)

	status, volume := do(t, ts, "POST", blockURL+"/volumes",
		`{"name":"root","from_empty":{"size":10000000000}}`)
	if status != http.StatusOK {
		t.Fatalf("create a volume: %d (%v)", status, volume)
	}
	volumeID, _ := volume["id"].(string)
	status, snapshot := do(t, ts, "POST", blockURL+"/snapshots",
		`{"name":"reference","volume_id":"`+volumeID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("create a block snapshot: %d (%v)", status, snapshot)
	}
	snapshotID, _ := snapshot["id"].(string)

	if status, got := do(t, ts, "GET", zoneURL+"/snapshots/"+snapshotID, ""); status != http.StatusNotFound {
		t.Errorf("the Instance API answered %d for a block snapshot, and the real cloud answers 404 (%v)", status, got)
	}
	status, listing := do(t, ts, "GET", zoneURL+"/snapshots", "")
	if status != http.StatusOK {
		t.Fatalf("list instance snapshots: %d", status)
	}
	if got, _ := listing["snapshots"].([]any); len(got) != 0 {
		t.Errorf("the Instance listing carries %d snapshot(s), and the real cloud answers none", len(got))
	}
}

// A block volume carries its performance type, and the snapshot repeats the
// parent's (#651).
//
// Measured 2026-09-03: `scw block volume get` answers "sbs_5k" with
// specs.perf_iops 5000, and the snapshot's parent_volume.type answers "sbs_5k"
// too. Both answered "sbs" here, which is the storage class, the value that
// belongs in specs.class and nowhere else.
func TestABlockVolumeCarriesItsPerformanceType(t *testing.T) {
	ts := newTestServer(t)

	for _, want := range []struct {
		iops string
		typ  string
	}{
		{iops: "", typ: "sbs_5k"},
		{iops: `,"perf_iops":5000`, typ: "sbs_5k"},
		{iops: `,"perf_iops":15000`, typ: "sbs_15k"},
	} {
		status, volume := do(t, ts, "POST", blockURL+"/volumes",
			`{"name":"typed","from_empty":{"size":5000000000}`+want.iops+`}`)
		if status != http.StatusOK {
			t.Fatalf("create a volume%s: %d (%v)", want.iops, status, volume)
		}
		if got, _ := volume["type"].(string); got != want.typ {
			t.Errorf("a volume%s is of type %q, want %q", want.iops, got, want.typ)
		}
		specs, _ := volume["specs"].(map[string]any)
		if class, _ := specs["class"].(string); class != "sbs" {
			t.Errorf("specs.class is %q, and the storage class is where sbs belongs", class)
		}

		volumeID, _ := volume["id"].(string)
		status, snapshot := do(t, ts, "POST", blockURL+"/snapshots",
			`{"name":"of-typed","volume_id":"`+volumeID+`"}`)
		if status != http.StatusOK {
			t.Fatalf("snapshot a volume%s: %d (%v)", want.iops, status, snapshot)
		}
		parent, _ := snapshot["parent_volume"].(map[string]any)
		if got, _ := parent["type"].(string); got != want.typ {
			t.Errorf("the snapshot's parent_volume.type is %q, want the parent's %q", got, want.typ)
		}
	}
}

// The snapshot answers the `public` it was asked for (#651).
//
// This test replaces one that asserted the opposite, and the reason is worth
// keeping: `scw block snapshot get -o json` on a real account answers no
// `public` field, so a measurement concluded the emulator had invented one and
// took it out. The CLI serialises the SDK struct it decodes into, and the
// vendored block SDK (v1alpha1) has no Public — what that command shows is
// what the CLI sees, not what the cloud sent.
//
// block-v1.yml settles it: `public` is on the Snapshot schema and in its
// REQUIRED list, "True if the snapshot can be used by anyone to create a
// volume". The `fields` leg said so too, from the other side, as soon as the
// field went missing from the answer.
//
// So the field is served, and it carries what the client asked rather than a
// constant: the omission gate checks that a field is present, never that it
// means anything.
func TestABlockSnapshotAnswersThePublicItWasAsked(t *testing.T) {
	ts := newTestServer(t)

	status, volume := do(t, ts, "POST", blockURL+"/volumes",
		`{"name":"root","from_empty":{"size":5000000000}}`)
	if status != http.StatusOK {
		t.Fatalf("create a volume: %d (%v)", status, volume)
	}
	volumeID, _ := volume["id"].(string)

	for _, want := range []bool{true, false} {
		body := `{"name":"reference","volume_id":"` + volumeID + `","public":false}`
		if want {
			body = `{"name":"reference","volume_id":"` + volumeID + `","public":true}`
		}
		status, snapshot := do(t, ts, "POST", blockURL+"/snapshots", body)
		if status != http.StatusOK {
			t.Fatalf("create a block snapshot: %d (%v)", status, snapshot)
		}
		got, present := snapshot["public"].(bool)
		if !present {
			t.Fatalf("the snapshot answers no `public`, and block-v1.yml requires it (%v)", snapshot)
		}
		if got != want {
			t.Errorf("a snapshot asked public=%v answers %v", want, got)
		}
		snapshotID, _ := snapshot["id"].(string)
		status, read := do(t, ts, "GET", blockURL+"/snapshots/"+snapshotID, "")
		if status != http.StatusOK {
			t.Fatalf("read the snapshot back: %d", status)
		}
		if back, _ := read["public"].(bool); back != want {
			t.Errorf("read back, a snapshot asked public=%v answers %v", want, back)
		}
	}
}

// A restored 15k volume still answers sbs_15k (#651, and #542's lesson).
//
// The type is computed from perf_iops, and Attrs crosses encoding/json on every
// snapshot: the uint32 a create stores comes back float64. A type assertion
// yields zero there, and zero is below the 15k threshold, so the volume would
// come back from a snapshot as sbs_5k — a client's own record of what it paid
// for, changed by a restart. internal/cli's TestNoPackReadsAStoredNumberByAssertion
// caught the assertion before this test did, and this is the behaviour behind it.
func TestARestoredVolumeKeepsItsPerformanceType(t *testing.T) {
	env := scalewayEnv()
	srv, err := emulator.NewServer(env, scaleway.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	status, made := do(t, ts, "POST", blockURL+"/volumes",
		`{"name":"fast","from_empty":{"size":5000000000},"perf_iops":15000}`)
	if status != http.StatusOK {
		t.Fatalf("create a 15k volume: %d (%v)", status, made)
	}
	volumeID, _ := made["id"].(string)
	if got, _ := made["type"].(string); got != "sbs_15k" {
		t.Fatalf("the fresh volume is %q, not sbs_15k: this test measures nothing", got)
	}

	var saved bytes.Buffer
	if err := env.Store.Snapshot(&saved); err != nil {
		t.Fatalf("take the snapshot: %v", err)
	}
	restored := store.New()
	if err := restored.Restore(bytes.NewReader(saved.Bytes())); err != nil {
		t.Fatalf("restore it: %v", err)
	}
	// The precondition, asserted: without the float64 the case is not staged.
	stored, found := restored.Get(scaleway.Name, "block/volume", volumeID)
	if !found {
		t.Fatal("the restored store lost the volume: nothing below measures anything")
	}
	if _, isFloat := stored.Attrs["perf_iops"].(float64); !isFloat {
		t.Fatalf("the restored perf_iops is %T, not the float64 JSON produces: "+
			"this test is not reproducing the case it names", stored.Attrs["perf_iops"])
	}

	next := scalewayEnv()
	next.Store = restored
	revived, err := emulator.NewServer(next, scaleway.New(next))
	if err != nil {
		t.Fatalf("build the revived emulator: %v", err)
	}
	after := httptest.NewServer(revived.Handler())
	t.Cleanup(after.Close)

	status, read := do(t, after, "GET", blockURL+"/volumes/"+volumeID, "")
	if status != http.StatusOK {
		t.Fatalf("read the restored volume: %d (%v)", status, read)
	}
	if got, _ := read["type"].(string); got != "sbs_15k" {
		t.Errorf("a restored 15k volume answers %q: a restart changed what the client paid for", got)
	}
}
