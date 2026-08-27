package scaleway_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/store"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// A block volume's size survives the door a snapshot really travels through
// (#542).
//
// This pack was not in #542's inventory, and that is the finding rather than a
// footnote: the issue's sweep looked for `Attrs[…].(int)` and
// `Attrs[…].(float64)`, and this pack spells the same field `uint64` — so the
// grep reported "Scaleway and Exoscale have no `.(int)` read of a stored
// number", which is true and reads as absolution. The AST scan that replaced it
// (internal/cli's TestNoPackReadsAStoredNumberByAssertion) found four more
// sites here on its first run: this shrink refusal, the size a volume created
// from a snapshot inherits, the size a snapshot records, and a backend health
// check's forward port as an int32.
//
// Attrs is a map[string]any and store.Restore decodes the snapshot with
// encoding/json, so every one of them came back a float64 and every assertion
// answered zero. Exoscale had already found and fixed this exact pair of
// readers for its own block volumes; nothing carried it here, which is what the
// shared reader and the discipline test are for.
func revivedScaleway(t *testing.T, size int) (*httptest.Server, string, *store.Store) {
	t.Helper()
	env := scalewayEnv()
	srv, err := emulator.NewServer(env, scaleway.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	volumeID := blockVolumeWith(t, ts, "restored", size)

	var saved bytes.Buffer
	if err := env.Store.Snapshot(&saved); err != nil {
		t.Fatalf("take the snapshot: %v", err)
	}
	restored := store.New()
	if err := restored.Restore(bytes.NewReader(saved.Bytes())); err != nil {
		t.Fatalf("restore it: %v", err)
	}

	next := scalewayEnv()
	next.Store = restored
	revived, err := emulator.NewServer(next, scaleway.New(next))
	if err != nil {
		t.Fatalf("build the revived emulator: %v", err)
	}
	after := httptest.NewServer(revived.Handler())
	t.Cleanup(after.Close)

	// The precondition, asserted: the record survived and the store really did
	// convert its size. A test that passed because the restore lost the volume
	// would read exactly like the fix working.
	stored, found := restored.Get(scaleway.Name, "block/volume", volumeID)
	if !found {
		t.Fatal("the restored store lost the block volume: nothing below measures anything")
	}
	if _, isFloat := stored.Attrs["size"].(float64); !isFloat {
		t.Fatalf("the restored size is %T, not the float64 the JSON decoding produces: this test "+
			"is not reproducing the case it names", stored.Attrs["size"])
	}
	return after, volumeID, restored
}

func scalewayEnv() *emulator.Env {
	var seq int
	return &emulator.Env{
		Store: store.New(),
		Now:   func() time.Time { return time.Unix(1700000000, 0).UTC() },
		NewID: func() string {
			seq++
			return fmt.Sprintf("00000000-0000-4000-8000-%012d", seq)
		},
	}
}

// A restored block volume still refuses to shrink.
func TestARestoredBlockVolumeStillRefusesToShrink(t *testing.T) {
	ts, volumeID, _ := revivedScaleway(t, 40)

	status, out := do(t, ts, "PATCH", blockURL+"/volumes/"+volumeID, `{"size":1}`)
	if status == http.StatusOK {
		t.Errorf("a restored block volume shrank from 40 to %v with a %d: the size it compares "+
			"against was read as zero, so the refusal is gone", out["size"], status)
	}

	// The accepting half: a guard that refused every resize would pass the
	// assertion above and break the product.
	status, out = do(t, ts, "PATCH", blockURL+"/volumes/"+volumeID, `{"size":80}`)
	if status != http.StatusOK {
		t.Fatalf("a restored block volume refused to grow to 80: status %d, %v", status, out)
	}
	if size, _ := out["size"].(float64); size != 80 {
		t.Errorf("the grown volume answers size %v, want 80", out["size"])
	}
}

// A snapshot of a restored block volume inherits its size, and a volume created
// from that snapshot inherits it in turn.
//
// The two other uint64 reads this pack held, one on each side of the same
// number: scaleway/snapshots.go took the source volume's size and
// scaleway/block.go took the snapshot's. Both answered zero over a restored
// store, so a snapshot chain taken after a `feint snapshot load` propagated the
// zero forward with a 200 at every step.
func TestASnapshotChainTakenAfterARestoreKeepsItsSize(t *testing.T) {
	ts, volumeID, _ := revivedScaleway(t, 40)

	status, snapshot := do(t, ts, "POST", blockURL+"/snapshots",
		`{"volume_id":"`+volumeID+`","name":"after-restore"}`)
	if status != http.StatusOK {
		t.Fatalf("create a snapshot of the restored volume: status %d, %v", status, snapshot)
	}
	if size, _ := snapshot["size"].(float64); size != 40 {
		t.Errorf("a snapshot taken of a restored volume records size %v, want 40", snapshot["size"])
	}
	snapshotID, _ := snapshot["id"].(string)

	status, volume := do(t, ts, "POST", blockURL+"/volumes",
		`{"name":"from-snapshot","from_snapshot":{"snapshot_id":"`+snapshotID+`"}}`)
	if status != http.StatusOK {
		t.Fatalf("create a volume from that snapshot: status %d, %v", status, volume)
	}
	if size, _ := volume["size"].(float64); size != 40 {
		t.Errorf("a volume created from that snapshot answers size %v, want 40: the zero would "+
			"propagate through every restore of it", volume["size"])
	}
}

// An instance snapshot of a restored volume inherits its size.
//
// A separate test from the block one above, and the falsification is why it
// exists rather than a note: the first version of this file asserted the block
// snapshot chain and was credited with the instance path too. Replaying the
// mutation on scaleway/snapshots.go reported STILL GREEN —
// createBlockSnapshot copies the size verbatim and never asserted anything, so
// the block test could not have been measuring the site it was credited with.
// Two paths, two tests.
func TestAnInstanceSnapshotOfARestoredVolumeInheritsItsSize(t *testing.T) {
	const instanceZone = "/instance/v1/zones/fr-par-1"

	env := scalewayEnv()
	srv, err := emulator.NewServer(env, scaleway.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	status, created := do(t, ts, "POST", instanceZone+"/volumes",
		`{"name":"restored","volume_type":"b_ssd","size":10000000000}`)
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("create the instance volume: status %d, %v", status, created)
	}
	volume, _ := created["volume"].(map[string]any)
	volumeID, _ := volume["id"].(string)
	if volumeID == "" {
		t.Fatalf("no volume id in %v", created)
	}

	var saved bytes.Buffer
	if err := env.Store.Snapshot(&saved); err != nil {
		t.Fatalf("take the snapshot: %v", err)
	}
	restored := store.New()
	if err := restored.Restore(bytes.NewReader(saved.Bytes())); err != nil {
		t.Fatalf("restore it: %v", err)
	}
	stored, found := restored.Get(scaleway.Name, "instance/volume", volumeID)
	if !found {
		t.Fatal("the restored store lost the instance volume: nothing below measures anything")
	}
	if _, isFloat := stored.Attrs["size"].(float64); !isFloat {
		t.Fatalf("the restored size is %T, not the float64 the JSON decoding produces: this test "+
			"is not reproducing the case it names", stored.Attrs["size"])
	}

	next := scalewayEnv()
	next.Store = restored
	revived, err := emulator.NewServer(next, scaleway.New(next))
	if err != nil {
		t.Fatalf("build the revived emulator: %v", err)
	}
	after := httptest.NewServer(revived.Handler())
	defer after.Close()

	status, snapshot := do(t, after, "POST", instanceZone+"/snapshots",
		`{"name":"after-restore","volume_id":"`+volumeID+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("snapshot the restored volume: status %d, %v", status, snapshot)
	}
	taken, _ := snapshot["snapshot"].(map[string]any)
	if size, _ := taken["size"].(float64); size != 10000000000 {
		t.Errorf("an instance snapshot of a restored volume records size %v, want 10000000000",
			taken["size"])
	}
}

// A restored backend's health check falls back to the forward port it really
// carries.
//
// The fourth site the AST scan found in this pack, and the only one of the
// seven that is not a size: forward_port is stored as an int32 and read back to
// fill a health check whose own port the client left at zero. Over a restored
// store the assertion answered zero too, so a health check that was meant to
// probe the backend's port probed port 0 — a check that can only ever fail,
// with a 200 on the call that installed it.
//
// The path is the API's own default rather than an edge: the SDK's
// UpdateHealthCheck takes a port, and a client that does not name one is asking
// for the backend's.
func TestARestoredBackendsHealthCheckKeepsItsForwardPort(t *testing.T) {
	env := scalewayEnv()
	srv, err := emulator.NewServer(env, scaleway.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ipID, _ := lbIP(t, ts, `{}`)
	status, lb := do(t, ts, "POST", lbURL+"/lbs",
		`{"name":"restored","type":"LB-S","ip_ids":["`+ipID+`"]}`)
	if status != http.StatusOK {
		t.Fatalf("create lb: status %d, %v", status, lb)
	}
	lbID, _ := lb["id"].(string)
	status, backend := do(t, ts, "POST", lbURL+"/lbs/"+lbID+"/backends",
		`{"name":"api","forward_protocol":"tcp","forward_port":6443,
		  "health_check":{"port":6443,"check_delay":30000,"check_timeout":5000,"check_max_retries":2,
		                  "tcp_config":{}}}`)
	if status != http.StatusOK {
		t.Fatalf("create backend: status %d, %v", status, backend)
	}
	backendID, _ := backend["id"].(string)

	var saved bytes.Buffer
	if err := env.Store.Snapshot(&saved); err != nil {
		t.Fatalf("take the snapshot: %v", err)
	}
	restored := store.New()
	if err := restored.Restore(bytes.NewReader(saved.Bytes())); err != nil {
		t.Fatalf("restore it: %v", err)
	}
	stored, found := restored.Get(scaleway.Name, "lb/backend", backendID)
	if !found {
		t.Fatal("the restored store lost the backend: nothing below measures anything")
	}
	if _, isFloat := stored.Attrs["forward_port"].(float64); !isFloat {
		t.Fatalf("the restored forward_port is %T, not the float64 the JSON decoding produces: "+
			"this test is not reproducing the case it names", stored.Attrs["forward_port"])
	}

	next := scalewayEnv()
	next.Store = restored
	revived, err := emulator.NewServer(next, scaleway.New(next))
	if err != nil {
		t.Fatalf("build the revived emulator: %v", err)
	}
	after := httptest.NewServer(revived.Handler())
	defer after.Close()

	// port omitted, which is the client asking for the backend's own.
	status, updated := do(t, after, "PUT", lbURL+"/backends/"+backendID+"/healthcheck",
		`{"check_delay":30000,"check_timeout":5000,"check_max_retries":2,"tcp_config":{}}`)
	if status != http.StatusOK {
		t.Fatalf("update the health check: status %d, %v", status, updated)
	}
	check, _ := updated["health_check"].(map[string]any)
	if check == nil {
		check = updated
	}
	if port, _ := check["port"].(float64); port != 6443 {
		t.Errorf("a restored backend's health check falls back to port %v, want 6443: a check on "+
			"port 0 can only ever fail, and the call that installed it answered 200", check["port"])
	}
}
