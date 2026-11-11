package scaleway_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/objectstore"
	"github.com/stephrobert/feint/internal/core/store"
	"github.com/stephrobert/feint/internal/providers/scaleway"
	"github.com/stephrobert/feint/internal/s3"
)

// Importing an instance snapshot from Object Storage, now that there is some.
//
// The subject is the refusal this replaced, and WHY it had to go rather than
// be relaxed: it was unconditional, and it told the client that "importing a
// snapshot reads Object Storage, which this emulator does not serve" while the
// object sat in the local door, named by the request being refused.
//
// It was found by replaying a stranger's stack — ioandev/scaleway-flatcar-k3s,
// recorded in examples/stacks/surveyed.md — whose image pipeline uploads a
// qcow2 through scaleway_object and then imports it. The upload landed in the
// door; the import refused on the premise the upload had just falsified. Eight
// resources of that stack sit behind this one call.

// storeBackedServer is newTestServer with a byte store armed, because the
// property under test is what the handler does when there IS one.
func storeBackedServer(t testing.TB) (*httptest.Server, *objectstore.Store) {
	t.Helper()

	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	objects, err := objectstore.Open(t.Context(), filepath.Join(root, "objects"))
	if err != nil {
		t.Fatalf("open the byte store: %v", err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})

	var seq int
	env := &emulator.Env{
		Store:   store.New(),
		Objects: objects,
		Now:     func() time.Time { return time.Unix(1700000000, 0).UTC() },
		NewID: func() string {
			seq++
			return fmt.Sprintf("00000000-0000-4000-8000-%012d", seq)
		},
	}
	srv, err := emulator.NewServer(env, scaleway.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, objects
}

// TestASnapshotImportTakesTheSizeOfTheObjectItNames is cited by
// importedSnapshotSize.
//
// The size is the assertion and not an afterthought: it is the ONE fact about
// the imported object a client can check, and a snapshot that answered a
// default size would be the half-success this project exists to avoid — green
// here, and a wrong disk there.
//
// The bucket is addressed through s3.StoreNamespace rather than spelled out,
// so that a door that changes its scope breaks this test instead of silently
// making the handler find nothing.
func TestASnapshotImportTakesTheSizeOfTheObjectItNames(t *testing.T) {
	ts, objects := storeBackedServer(t)

	const bucket, key = "flatcar-image-import", "flatcar.qcow2"
	scope := s3.StoreNamespace("scaleway", "fr-par") + "/" + bucket
	if err := objects.CreateBucket(t.Context(), scope); err != nil {
		t.Fatalf("create the bucket: %v", err)
	}
	const body = "not a real qcow2, and the size is what the import reads"
	if _, err := objects.PutObject(t.Context(), scope, key,
		strings.NewReader(body), objectstore.PutOptions{Size: int64(len(body))}); err != nil {
		t.Fatalf("put the object: %v", err)
	}

	status, got := do(t, ts, "POST", zoneURL+"/snapshots",
		`{"name":"flatcar-stable","bucket":"`+bucket+`","key":"`+key+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("import of an object that is there: expected 201, got %d (%v)", status, got)
	}
	snapshot, _ := got["snapshot"].(map[string]any)
	if snapshot == nil {
		t.Fatalf("no snapshot in the answer: %v", got)
	}
	if size, _ := snapshot["size"].(float64); int(size) != len(body) {
		t.Errorf("the snapshot took size %v, not the object's %d", snapshot["size"], len(body))
	}
	if name, _ := snapshot["name"].(string); name != "flatcar-stable" {
		t.Errorf("the snapshot lost its name: %v", snapshot["name"])
	}

	// And it is readable afterwards, because an import that answers 201 and
	// stores nothing is the same lie in a slower form.
	_, listed := do(t, ts, "GET", zoneURL+"/snapshots", "")
	if items, _ := listed["snapshots"].([]any); len(items) != 1 {
		t.Errorf("the imported snapshot was not recorded: %v", listed)
	}
}

// TestASnapshotImportIsRefusedWithoutTheObject is cited by
// importedSnapshotSize, and it is the half that keeps the fix from becoming a
// rubber stamp.
//
// Two populations, because they are refused for DIFFERENT reasons and only one
// of them is about this run's configuration:
//
//   - a byte store is armed and the object is not in it: the real API refuses
//     an object that is not there, and so does this;
//   - no byte store at all: nothing can be read, so nothing is asserted about
//     bytes. The message names the flags rather than the limits page, because
//     the operator can fix this one.
func TestASnapshotImportIsRefusedWithoutTheObject(t *testing.T) {
	armed, objects := storeBackedServer(t)
	scope := s3.StoreNamespace("scaleway", "fr-par") + "/present"
	if err := objects.CreateBucket(t.Context(), scope); err != nil {
		t.Fatalf("create the bucket: %v", err)
	}

	for _, probe := range []struct{ name, body, expect string }{
		{"an absent key in a bucket that exists",
			`{"name":"x","bucket":"present","key":"nothing.qcow2"}`, "nothing to import"},
		{"a bucket that does not exist",
			`{"name":"x","bucket":"absent","key":"disk.qcow2"}`, "nothing to import"},
		{"an import naming no key at all",
			`{"name":"x","bucket":"present"}`, ""},
	} {
		t.Run(probe.name, func(t *testing.T) {
			status, got := do(t, armed, "POST", zoneURL+"/snapshots", probe.body)
			if status != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (%v)", status, got)
			}
			if probe.expect != "" && !strings.Contains(fmt.Sprint(got), probe.expect) {
				t.Errorf("the refusal does not say what is missing: %v", got)
			}
		})
	}

	// Nothing was recorded by any of the three.
	_, listed := do(t, armed, "GET", zoneURL+"/snapshots", "")
	if items, _ := listed["snapshots"].([]any); len(items) != 0 {
		t.Errorf("a refused import was recorded anyway: %v", listed)
	}

	// And with no byte store, the refusal names the flags that arm one rather
	// than claiming the emulator cannot do this at all.
	unarmed := newTestServer(t)
	status, got := do(t, unarmed, "POST", zoneURL+"/snapshots",
		`{"name":"x","bucket":"present","key":"disk.qcow2"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("unarmed import: expected 400, got %d (%v)", status, got)
	}
	for _, want := range []string{"--objectstore", "--s3-doors", "scaleway/fr-par"} {
		if !strings.Contains(fmt.Sprint(got), want) {
			t.Errorf("the unarmed refusal does not name %q: %v", want, got)
		}
	}
}

// TestAnImportedSnapshotHasNoBaseVolumeAndReplansEmpty is cited by
// createSnapshot, and it holds a defect the unit tests could not have found.
//
// The first version of the import left the route's default base volume in
// place, an empty {id, name}. Everything passed: the snapshot was created, its
// size was right, it read back. Then the stack replay answered
// `2 to add, 0 to change, 2 to destroy` on its second plan, because the
// provider reads an empty base as volume_id "fr-par-1/" and that forces
// replacement of the snapshot and of the image cut from it.
//
// So this asserts the shape on BOTH sides of the round trip, which is the rule
// the provider pack lives by: whatever a create returns, the following read
// returns identically. A null here is not tidiness, it is the difference
// between an example that converges and one that churns two resources on every
// plan forever.
func TestAnImportedSnapshotHasNoBaseVolumeAndReplansEmpty(t *testing.T) {
	ts, objects := storeBackedServer(t)

	const bucket, key = "images", "disk.qcow2"
	scope := s3.StoreNamespace("scaleway", "fr-par") + "/" + bucket
	if err := objects.CreateBucket(t.Context(), scope); err != nil {
		t.Fatalf("create the bucket: %v", err)
	}
	if _, err := objects.PutObject(t.Context(), scope, key,
		strings.NewReader("bytes"), objectstore.PutOptions{Size: 5}); err != nil {
		t.Fatalf("put the object: %v", err)
	}

	status, created := do(t, ts, "POST", zoneURL+"/snapshots",
		`{"name":"imported","bucket":"`+bucket+`","key":"`+key+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("import: expected 201, got %d (%v)", status, created)
	}
	snapshot, _ := created["snapshot"].(map[string]any)
	base, present := snapshot["base_volume"]
	if !present {
		t.Fatalf("base_volume is absent from the create; the client expects the field: %v", snapshot)
	}
	if base != nil {
		t.Errorf("an imported snapshot answered a base volume %v; an empty one reads back "+
			"as volume_id \"fr-par-1/\" and forces replacement on every later plan", base)
	}

	// The read, which is the half that actually drives the diff.
	id, _ := snapshot["id"].(string)
	status, read := do(t, ts, "GET", zoneURL+"/snapshots/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("read back: expected 200, got %d (%v)", status, read)
	}
	again, _ := read["snapshot"].(map[string]any)
	if base, present := again["base_volume"]; !present || base != nil {
		t.Errorf("the read does not agree with the create about base_volume: %v", again)
	}

	// And a snapshot taken of a real volume still HAS one, so this cannot be
	// satisfied by nulling the field for everybody.
	ofVolume := snapshotOfVolume(t, ts, "of-a-volume", instanceVolumeOf(t, ts))
	status, normal := do(t, ts, "GET", zoneURL+"/snapshots/"+ofVolume, "")
	if status != http.StatusOK {
		t.Fatalf("read the ordinary snapshot: expected 200, got %d (%v)", status, normal)
	}
	snap, _ := normal["snapshot"].(map[string]any)
	if base, _ := snap["base_volume"].(map[string]any); base == nil || base["id"] == "" {
		t.Errorf("an ordinary snapshot lost its base volume: %v", snap)
	}
}

// TestABlockImportRefusesAnObjectTheDoorDoesNotHold is cited by
// importBlockSnapshotFromObjectStorage and by importedObjectSize.
//
// It exists because the two imports of this pack must not disagree about what
// they do when a byte store IS armed. The instance one was fixed first, after
// a stranger's stack walked into it; writing the same reading a second time
// for block is exactly the shape this repository has already paid for, so the
// reader is shared and this test holds the block end of it.
//
// What the two still do differently is deliberate and asserted below: with no
// byte store, the block import records an intention with a size of zero, which
// is #807's own contract, while the instance one refuses. Their prior
// contracts differed, and silently aligning them would change a served route's
// behaviour for an operator who armed nothing.
func TestABlockImportRefusesAnObjectTheDoorDoesNotHold(t *testing.T) {
	ts, objects := storeBackedServer(t)

	const bucket, key = "disks", "root.qcow2"
	scope := s3.StoreNamespace("scaleway", "fr-par") + "/" + bucket
	if err := objects.CreateBucket(t.Context(), scope); err != nil {
		t.Fatalf("create the bucket: %v", err)
	}
	const body = "seven!!"
	if _, err := objects.PutObject(t.Context(), scope, key,
		strings.NewReader(body), objectstore.PutOptions{Size: int64(len(body))}); err != nil {
		t.Fatalf("put the object: %v", err)
	}

	// Armed and present: the size is the object's, not zero.
	status, got := do(t, ts, "POST", blockURL+"/snapshots/import-from-object-storage",
		`{"name":"from-object","bucket":"`+bucket+`","key":"`+key+`"}`)
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("import of an object that is there: got %d (%v)", status, got)
	}
	snapshot, _ := got["snapshot"].(map[string]any)
	if snapshot == nil {
		snapshot = got
	}
	if size, _ := snapshot["size"].(float64); int(size) != len(body) {
		t.Errorf("the block import took size %v, not the object's %d", snapshot["size"], len(body))
	}

	// Armed and absent: refused, because the real API refuses it and an
	// intention recorded here would be a snapshot of bytes nobody has.
	status, got = do(t, ts, "POST", blockURL+"/snapshots/import-from-object-storage",
		`{"name":"from-nothing","bucket":"`+bucket+`","key":"absent.qcow2"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("import of an absent object: expected 400, got %d (%v)", status, got)
	}

	// Unarmed: #807's intention stands, unchanged.
	unarmed := newTestServer(t)
	status, got = do(t, unarmed, "POST", blockURL+"/snapshots/import-from-object-storage",
		`{"name":"intention","bucket":"`+bucket+`","key":"`+key+`"}`)
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("unarmed import: expected the #807 intention, got %d (%v)", status, got)
	}
}
