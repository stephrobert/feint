package outscale_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/store"
	"github.com/stephrobert/feint/internal/providers/outscale"
)

// A volume's size survives the door a snapshot really travels through (#542).
//
// Measured on 2026-08-27, before the fix, against this pack over a restored
// store:
//
//	Attrs[Size] is float64 = 40; the int assertion yields 0
//	shrink 40 -> 1 after a restore: status 200, Size now 1
//	CreateSnapshot of a restored volume records VolumeSize=0
//
// Attrs is a map[string]any and store.Restore decodes the snapshot with
// encoding/json, so the `Attrs["Size"].(int)` these two handlers used to write
// answered zero — and a comparison against zero is a refusal that never fires.
// Nothing in the tree was red: no unit test and no conformance suite restores a
// snapshot and then reads a size back, which is why this file exists rather
// than only the one-line fix.
//
// The restore goes into a *fresh* store behind a *second* emulator, because
// that is the case the format is designed for: snapshot.go documents it as
// meant to outlive its instance and be loaded into another one.
func revivedOutscale(t *testing.T, size string) (*httptest.Server, string, *store.Store) {
	t.Helper()
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, outscale.New(env))
	if err != nil {
		t.Fatalf("mount the outscale pack: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	_, created := post(t, ts, "CreateVolume", `{"SubregionName":"eu-west-2a","Size":`+size+`}`)
	volume, _ := created["Volume"].(map[string]any)
	volumeID, _ := volume["VolumeId"].(string)
	if volumeID == "" {
		t.Fatalf("CreateVolume answered no VolumeId: %v", created)
	}

	var saved bytes.Buffer
	if err := env.Store.Snapshot(&saved); err != nil {
		t.Fatalf("take the snapshot: %v", err)
	}
	restored := store.New()
	if err := restored.Restore(bytes.NewReader(saved.Bytes())); err != nil {
		t.Fatalf("restore it: %v", err)
	}

	next := emulator.DefaultEnv()
	next.Store = restored
	revived, err := emulator.NewServer(next, outscale.New(next))
	if err != nil {
		t.Fatalf("mount the revived emulator: %v", err)
	}
	after := httptest.NewServer(revived.Handler())
	t.Cleanup(after.Close)

	// The precondition, asserted rather than assumed: the volume is still there
	// and the store really did convert its size. Without this, a test that
	// passes because the restore lost the record would read as the fix working.
	stored, found := restored.Get(outscale.Name, "volume", volumeID)
	if !found {
		t.Fatalf("the restored store lost the volume: nothing below measures anything")
	}
	if _, isFloat := stored.Attrs["Size"].(float64); !isFloat {
		t.Fatalf("the restored size is %T, not the float64 the JSON decoding produces: this test "+
			"is not reproducing the case it names", stored.Attrs["Size"])
	}
	return after, volumeID, restored
}

// A restored volume still refuses to shrink.
func TestARestoredVolumeStillRefusesToShrink(t *testing.T) {
	ts, volumeID, _ := revivedOutscale(t, "40")

	status, out := post(t, ts, "UpdateVolume", `{"VolumeId":"`+volumeID+`","Size":1}`)
	if status == http.StatusOK {
		volume, _ := out["Volume"].(map[string]any)
		t.Errorf("a restored volume shrank from 40 to %v with a %d: the size it compares against "+
			"was read as zero, so the refusal the API states is gone", volume["Size"], status)
	}

	// The accepting half, without which a handler that refused every update
	// would pass the assertion above and break the product.
	status, out = post(t, ts, "UpdateVolume", `{"VolumeId":"`+volumeID+`","Size":80}`)
	if status != http.StatusOK {
		t.Fatalf("a restored volume refused to grow to 80: status %d, %v", status, out)
	}
	volume, _ := out["Volume"].(map[string]any)
	if size, _ := volume["Size"].(float64); size != 80 {
		t.Errorf("the grown volume answers Size %v, want 80", volume["Size"])
	}
}

// A snapshot of a restored volume inherits its size.
func TestASnapshotOfARestoredVolumeInheritsItsSize(t *testing.T) {
	ts, volumeID, _ := revivedOutscale(t, "40")

	_, out := post(t, ts, "CreateSnapshot", `{"VolumeId":"`+volumeID+`"}`)
	snapshot, _ := out["Snapshot"].(map[string]any)
	if size, _ := snapshot["VolumeSize"].(float64); size != 40 {
		t.Errorf("a snapshot taken of a restored volume records VolumeSize %v, want 40: every volume "+
			"later created from that snapshot inherits the zero", snapshot["VolumeSize"])
	}

	// And the read itself, which is the half #542 got wrong and this records:
	// the size was never missing from a read. volumeView copies Attrs verbatim,
	// so a float64 40 marshals as 40 and every client saw the right number all
	// along. What was broken was the two handlers that compared it.
	_, listed := post(t, ts, "ReadVolumes", `{}`)
	volumes, _ := listed["Volumes"].([]any)
	found := false
	for _, raw := range volumes {
		volume, _ := raw.(map[string]any)
		if volume["VolumeId"] != volumeID {
			continue
		}
		found = true
		if size, _ := volume["Size"].(float64); size != 40 {
			t.Errorf("the restored volume reads back Size %v, want 40", volume["Size"])
		}
	}
	if !found {
		t.Errorf("ReadVolumes did not answer the restored volume at all: %v", listed)
	}
}
