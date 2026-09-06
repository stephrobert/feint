package outscale_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/outscale"
)

// A transient state is observable on demand, so the refusal that guards it can
// fire (#124).
//
// Measured against a real Outscale account on 2026-08-08: CreateVolume answers
// State "creating", a CreateSnapshot issued before the volume settles is
// refused with 409 InvalidVolumeState (code 6007), and a snapshot is born
// "in-queue" with Progress 0 before it is "completed". Here a volume was
// "available" and a snapshot "completed" at once, so that refusal could never
// fire, and a guard for it was tried and reverted for exactly that reason: a
// control written for a state nothing can reach is a comment.
//
// #637 made the state reachable without a clock: a resource carries a pending
// chain the store walks one step per observation, under `feint serve
// --consistency eventual`, and the default stays immediate. These tests hold
// the Outscale half of that: the chains, the refusal that becomes reachable
// with them, and the default that does not move.

// eventualServer is newServer with eventual consistency on.
func eventualServer(t *testing.T) *httptest.Server {
	t.Helper()
	env := emulator.DefaultEnv()
	env.Store.Eventual(true)
	srv, err := emulator.NewServer(env, outscale.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func volumeState(t *testing.T, ts *httptest.Server, id string) string {
	t.Helper()
	_, out := post(t, ts, "ReadVolumes", `{"Filters":{"VolumeIds":["`+id+`"]}}`)
	list, _ := out["Volumes"].([]any)
	if len(list) != 1 {
		t.Fatalf("ReadVolumes answers %d volumes for %s: %v", len(list), id, out)
	}
	state, _ := list[0].(map[string]any)["State"].(string)
	return state
}

func TestAVolumeIsCreatingThenAvailableUnderEventualConsistency(t *testing.T) {
	ts := eventualServer(t)
	status, out := post(t, ts, "CreateVolume", `{"SubregionName":"eu-west-2a","Size":7}`)
	if status != http.StatusOK {
		t.Fatalf("CreateVolume answered %d: %v", status, out)
	}
	volume, _ := out["Volume"].(map[string]any)
	id, _ := volume["VolumeId"].(string)
	// The create itself answers the first state of the chain, as the cloud does.
	if volume["State"] != "creating" {
		t.Errorf("the create answers %v, and the cloud answers creating", volume["State"])
	}
	if got := volumeState(t, ts, id); got != "available" {
		t.Errorf("the read after the create answers %q, want available", got)
	}
	if got := volumeState(t, ts, id); got != "available" {
		t.Errorf("a settled volume moved again: %q", got)
	}
}

func TestASnapshotOfAVolumeStillCreatingIsRefusedWithTheMeasuredConflict(t *testing.T) {
	ts := eventualServer(t)
	_, out := post(t, ts, "CreateVolume", `{"SubregionName":"eu-west-2a","Size":7}`)
	id, _ := out["Volume"].(map[string]any)["VolumeId"].(string)

	// The refusal, verbatim in what was measured: 409, code 6007. The guard
	// reads the volume without observing it, or it would consume the very
	// state it checks and never fire — the defect the reverted version had.
	status, out := post(t, ts, "CreateSnapshot", `{"VolumeId":"`+id+`"}`)
	if status != http.StatusConflict {
		t.Fatalf("a snapshot of a creating volume answered %d, and the cloud answers 409: %v", status, out)
	}
	errs, _ := out["Errors"].([]any)
	if len(errs) != 1 {
		t.Fatalf("the refusal carries %d errors: %v", len(errs), out)
	}
	first, _ := errs[0].(map[string]any)
	if first["Code"] != "6007" || first["Type"] != "InvalidVolumeState" {
		t.Errorf("the refusal is %v/%v, and the measurement says 6007/InvalidVolumeState", first["Code"], first["Type"])
	}

	// One observation settles the volume, and the snapshot then walks its own
	// chain: born in-queue with no progress, completed on the next read.
	if got := volumeState(t, ts, id); got != "available" {
		t.Fatalf("the volume did not settle: %q", got)
	}
	status, out = post(t, ts, "CreateSnapshot", `{"VolumeId":"`+id+`"}`)
	if status != http.StatusOK {
		t.Fatalf("a snapshot of an available volume answered %d: %v", status, out)
	}
	snapshot, _ := out["Snapshot"].(map[string]any)
	if snapshot["State"] != "in-queue" || snapshot["Progress"] != float64(0) {
		t.Errorf("a fresh snapshot is %v at %v%%, and the cloud answers in-queue at 0", snapshot["State"], snapshot["Progress"])
	}
	snapID, _ := snapshot["SnapshotId"].(string)
	_, out = post(t, ts, "ReadSnapshots", `{"Filters":{"SnapshotIds":["`+snapID+`"]}}`)
	list, _ := out["Snapshots"].([]any)
	if len(list) != 1 {
		t.Fatalf("ReadSnapshots answers %d snapshots: %v", len(list), out)
	}
	settled, _ := list[0].(map[string]any)
	if settled["State"] != "completed" || settled["Progress"] != float64(100) {
		t.Errorf("the read after the create answers %v at %v%%, want completed at 100", settled["State"], settled["Progress"])
	}
}

// The default does not move: with the mode off, a volume is available and a
// snapshot completed at once, byte for byte what every suite and every client
// read before #124. Without this half a chain walked in both modes would pass
// the tests above and change what CI and the conformance suite see.
func TestVolumesAndSnapshotsSettleAtOnceByDefault(t *testing.T) {
	ts := newServer(t)
	_, out := post(t, ts, "CreateVolume", `{"SubregionName":"eu-west-2a","Size":7}`)
	volume, _ := out["Volume"].(map[string]any)
	if volume["State"] != "available" {
		t.Errorf("the default create answers %v, want available", volume["State"])
	}
	id, _ := volume["VolumeId"].(string)
	status, out := post(t, ts, "CreateSnapshot", `{"VolumeId":"`+id+`"}`)
	snapshot, _ := out["Snapshot"].(map[string]any)
	if status != http.StatusOK || snapshot["State"] != "completed" || snapshot["Progress"] != float64(100) {
		t.Errorf("the default snapshot answered %d, %v at %v%%, want 200, completed at 100", status, snapshot["State"], snapshot["Progress"])
	}
}
