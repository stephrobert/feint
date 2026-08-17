package exoscale_test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// Block Storage (EXO-4, #12): a volume, its attachment, its resize and its
// snapshots.
//
// The tests here are the refusals and the relations, not the CRUD: `exo compute
// block-storage` drives create, list, show, update and delete end to end in the
// conformance suite, and a unit test repeating that would assert what a real
// client already proves. What a client does *not* provoke on its own is the
// wrong order — a delete under an attachment, a shrink, a second attach — and
// those are the paths where an emulator diverges from the cloud in silence.

func aBlockVolume(t *testing.T, h http.Handler, name string, size int) string {
	t.Helper()
	status, out := callRaw(h, "POST", "/v2/block-storage", `{
		"name": "`+name+`",
		"size": `+itoa(size)+`
	}`)
	if status != http.StatusOK {
		t.Fatalf("create %s: status %d (%v)", name, status, out)
	}
	ref, _ := out["reference"].(map[string]any)
	id, _ := ref["id"].(string)
	if id == "" {
		t.Fatalf("the create operation names no resource: %v", out)
	}
	return id
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

// A volume grows and refuses to shrink.
//
// The refusal is the half that matters: a filesystem does not survive its disk
// getting smaller, upstream refuses it, and an emulator that accepted it would
// let a plan through that the real cloud stops. The Outscale pack refuses the
// same thing on UpdateVolume, which is the measure of how provider-neutral this
// rule is.
func TestABlockVolumeGrowsAndRefusesToShrink(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)
	id := aBlockVolume(t, h, "grows", 10)

	if status, out := callRaw(h, "PUT", "/v2/block-storage/"+id+":resize-volume", `{"size": 20}`); status != http.StatusOK {
		t.Fatalf("grow: status %d (%v)", status, out)
	}
	_, volume := callRaw(h, "GET", "/v2/block-storage/"+id, "")
	if size, _ := volume["size"].(float64); size != 20 {
		t.Errorf("the volume answers size %v after growing to 20", volume["size"])
	}

	status, _ := callRaw(h, "PUT", "/v2/block-storage/"+id+":resize-volume", `{"size": 5}`)
	if status == http.StatusOK {
		t.Error("the volume shrank, which loses a filesystem")
	}
	// And the refusal changed nothing: a rejected resize that already wrote is
	// worse than one that accepted, because the client believes neither.
	_, volume = callRaw(h, "GET", "/v2/block-storage/"+id, "")
	if size, _ := volume["size"].(float64); size != 20 {
		t.Errorf("the refused shrink still moved the size to %v", volume["size"])
	}
}

// A volume is attached to one instance, and says no to a second.
//
// Block storage is not shared storage: upstream binds a volume to exactly one
// machine. The case a client actually produces is a retry that re-sends the
// attach — which must succeed against the same instance and fail against
// another, because those are two different mistakes.
func TestAnAttachedBlockVolumeRefusesASecondInstance(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)
	id := aBlockVolume(t, h, "attached", 10)
	first := anInstance(t, h, "holder")
	second := anInstance(t, h, "other")

	if status, out := callRaw(h, "PUT", "/v2/block-storage/"+id+":attach",
		`{"instance": {"id": "`+first+`"}}`); status != http.StatusOK {
		t.Fatalf("attach: status %d (%v)", status, out)
	}
	_, volume := callRaw(h, "GET", "/v2/block-storage/"+id, "")
	instance, _ := volume["instance"].(map[string]any)
	if held, _ := instance["id"].(string); held != first {
		t.Errorf("the volume publishes instance %v, want %s", volume["instance"], first)
	}

	// The same instance again: a retry, not an error.
	if status, _ := callRaw(h, "PUT", "/v2/block-storage/"+id+":attach",
		`{"instance": {"id": "`+first+`"}}`); status != http.StatusOK {
		t.Error("re-attaching to the instance already holding it was refused; a client's retry fails")
	}

	// Another instance: refused, and the first attachment survives.
	if status, _ := callRaw(h, "PUT", "/v2/block-storage/"+id+":attach",
		`{"instance": {"id": "`+second+`"}}`); status == http.StatusOK {
		t.Error("the volume was attached to a second instance; block storage is not shared storage")
	}
	_, volume = callRaw(h, "GET", "/v2/block-storage/"+id, "")
	instance, _ = volume["instance"].(map[string]any)
	if held, _ := instance["id"].(string); held != first {
		t.Errorf("the refused attach moved the volume to %v", volume["instance"])
	}

	// Detaching an unattached volume is refused too: answering 200 would tell a
	// client it undid something it never did.
	if status, _ := callRaw(h, "PUT", "/v2/block-storage/"+id+":detach", ""); status != http.StatusOK {
		t.Fatal("detaching an attached volume was refused")
	}
	status, refusal := callRaw(h, "PUT", "/v2/block-storage/"+id+":detach", "")
	if status == http.StatusOK {
		t.Error("detaching a volume attached to nothing answered success")
	}
	// And the sentence is part of the refusal, not decoration around it.
	//
	// The Terraform provider's destroy calls detach unconditionally and tells a
	// tolerable refusal from a real failure by reading the message:
	// `strings.HasSuffix(err.Error(), "Volume not attached")`. Reworded, the
	// refusal still refuses and every destroy of a never-attached volume dies on
	// "unable to detach volume" — measured against the patched provider, because
	// `exo` never detaches before deleting and cannot show this.
	if message, _ := refusal["message"].(string); !strings.HasSuffix(message, "Volume not attached") {
		t.Errorf("the refusal reads %q; the provider matches on the suffix \"Volume not attached\" "+
			"and treats anything else as a failed destroy", message)
	}
}

// A volume refuses to be deleted under its instance or under its snapshots.
//
// Both refusals are upstream's, and both are steps a `terraform destroy` walks
// in a specific order. A destroy that meets neither learns nothing; one that
// meets them in the wrong order fails on the real cloud after passing here,
// which is the divergence this pack exists to avoid.
func TestABlockVolumeRefusesToBeDeletedUnderItsInstanceOrItsSnapshots(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)
	id := aBlockVolume(t, h, "busy", 10)
	instance := anInstance(t, h, "holder")

	if status, _ := callRaw(h, "PUT", "/v2/block-storage/"+id+":attach",
		`{"instance": {"id": "`+instance+`"}}`); status != http.StatusOK {
		t.Fatal("attach was refused")
	}
	if status, _ := callRaw(h, "DELETE", "/v2/block-storage/"+id, ""); status == http.StatusOK {
		t.Error("an attached volume was deleted; a client would lose the machine's disk")
	}
	if status, _ := callRaw(h, "PUT", "/v2/block-storage/"+id+":detach", ""); status != http.StatusOK {
		t.Fatal("detach was refused")
	}

	// Now a snapshot holds it instead.
	status, out := callRaw(h, "POST", "/v2/block-storage/"+id+":create-snapshot", `{"name": "keep"}`)
	if status != http.StatusOK {
		t.Fatalf("create-snapshot: status %d (%v)", status, out)
	}
	ref, _ := out["reference"].(map[string]any)
	snapshot, _ := ref["id"].(string)

	// The volume publishes it, computed from the store rather than maintained
	// beside it: this is the relation a client reads to know what it owns.
	_, volume := callRaw(h, "GET", "/v2/block-storage/"+id, "")
	snapshots, _ := volume["block-storage-snapshots"].([]any)
	if len(snapshots) != 1 {
		t.Errorf("the volume publishes %d snapshot(s), want 1: %v", len(snapshots), volume)
	}

	if status, _ := callRaw(h, "DELETE", "/v2/block-storage/"+id, ""); status == http.StatusOK {
		t.Error("a snapshotted volume was deleted, leaving its snapshots naming nothing")
	}

	// Snapshot first, then the volume: the order a destroy takes.
	if status, _ := callRaw(h, "DELETE", "/v2/block-storage-snapshot/"+snapshot, ""); status != http.StatusOK {
		t.Fatal("the snapshot refused its own delete")
	}
	_, volume = callRaw(h, "GET", "/v2/block-storage/"+id, "")
	if snapshots, _ := volume["block-storage-snapshots"].([]any); len(snapshots) != 0 {
		t.Errorf("the deleted snapshot survives in the volume's list: %v", volume)
	}
	if status, _ := callRaw(h, "DELETE", "/v2/block-storage/"+id, ""); status != http.StatusOK {
		t.Error("the volume refused its delete with nothing holding it")
	}
}

// The refusals still hold on a store that came back from a snapshot.
//
// A size written as int64 comes back as float64: `feint snapshot` and
// `PUT /_feint/state` round-trip the whole store through JSON, and JSON has one
// number type. A type assertion on int64 alone answers zero on the restored
// value, and the two readers of this size are the shrink refusal and the size a
// snapshot inherits — so a restored emulator would accept a shrink (5 is not
// less than 0) and hand out zero-sized snapshots, with nothing red anywhere.
//
// CLAUDE.md states the rule as "a restored state is untrusted input, on the same
// terms as a request body". This is the same rule read one notch further in: not
// only the values a restore carries, but their *types*.
func TestABlockVolumeSurvivesASnapshotRestore(t *testing.T) {
	h, st := newExoscaleBarrageServer(t)
	id := aBlockVolume(t, h, "restored", 40)

	var snapshot bytes.Buffer
	if err := st.Snapshot(&snapshot); err != nil {
		t.Fatalf("take the snapshot: %v", err)
	}
	if err := st.Restore(&snapshot); err != nil {
		t.Fatalf("restore it: %v", err)
	}

	// The volume is still 40 after the round trip, which is what makes the rest
	// of this test measure the refusals rather than a lost record.
	_, volume := callRaw(h, "GET", "/v2/block-storage/"+id, "")
	if size, _ := volume["size"].(float64); size != 40 {
		t.Fatalf("the restored volume answers size %v, want 40", volume["size"])
	}

	if status, _ := callRaw(h, "PUT", "/v2/block-storage/"+id+":resize-volume", `{"size": 5}`); status == http.StatusOK {
		t.Error("a restored volume accepted a shrink: the size it compares against was read as zero")
	}

	// And a snapshot taken after the restore inherits the real size rather than
	// zero, which is the same read from the other side.
	_, out := callRaw(h, "POST", "/v2/block-storage/"+id+":create-snapshot", `{"name": "after-restore"}`)
	ref, _ := out["reference"].(map[string]any)
	snapID, _ := ref["id"].(string)
	_, snap := callRaw(h, "GET", "/v2/block-storage-snapshot/"+snapID, "")
	if size, _ := snap["size"].(float64); size != 40 {
		t.Errorf("a snapshot taken after a restore carries size %v, want 40", snap["size"])
	}
}

// A volume restored from a snapshot takes the snapshot's size.
//
// The client sends both a snapshot reference and, sometimes, a size; upstream
// sizes the restored volume from its source. An emulator that took the client's
// number would answer a volume the real cloud would not have made, and the
// difference only shows up when somebody restores a snapshot bigger than the
// size they typed.
func TestARestoredBlockVolumeTakesItsSnapshotsSize(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)
	source := aBlockVolume(t, h, "source", 50)

	_, out := callRaw(h, "POST", "/v2/block-storage/"+source+":create-snapshot", `{"name": "from-50"}`)
	ref, _ := out["reference"].(map[string]any)
	snapshot, _ := ref["id"].(string)
	if snapshot == "" {
		t.Fatalf("no snapshot in the operation: %v", out)
	}

	status, created := callRaw(h, "POST", "/v2/block-storage", `{
		"name": "restored",
		"size": 10,
		"block-storage-snapshot": {"id": "`+snapshot+`"}
	}`)
	if status != http.StatusOK {
		t.Fatalf("restore: status %d (%v)", status, created)
	}
	ref, _ = created["reference"].(map[string]any)
	restored, _ := ref["id"].(string)

	_, volume := callRaw(h, "GET", "/v2/block-storage/"+restored, "")
	if size, _ := volume["size"].(float64); size != 50 {
		t.Errorf("the restored volume answers size %v; upstream restores the snapshot's 50", volume["size"])
	}
}
