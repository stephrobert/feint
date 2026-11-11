package emulator_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/objectstore"
	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/store"
)

// Named checkpoints and the reset that returns to one (#853).
//
// What these tests hold is the half the issue says cannot be skipped: a reset
// is not successful because JSON was restored. So the assertion is never "the
// call answered 200", it is which machines the pack was asked to take down.

// reconcilingPack is a stubPack that can reconcile, and records what it was
// handed. A pack that drives no machine does not implement the interface at
// all, which is the other population below.
type reconcilingPack struct {
	stubPack
	got  [][]string
	done machine.Reconciled
}

func (p *reconcilingPack) ReconcileGone(_ context.Context, gone []*resource.Resource) machine.Reconciled {
	var names []string
	for _, res := range gone {
		names = append(names, res.ID)
	}
	p.got = append(p.got, names)
	return p.done
}

func labServer(t *testing.T, packs ...emulator.Pack) (*httptest.Server, *store.Store) {
	t.Helper()
	return labServerWithBytes(t, nil, packs...)
}

// labServerWithBytes is the same lab with an optional byte store, because a
// checkpoint's two halves cannot both be asserted against a lab that has one
// of them. The falsification found that: skipping the byte half changed
// nothing in a test whose emulator served no objects.
func labServerWithBytes(t *testing.T, objects *objectstore.Store, packs ...emulator.Pack) (*httptest.Server, *store.Store) {
	t.Helper()
	return labServerWith(t, objects, nil, packs...)
}

// labServerWithRuntime is the same lab with a runtime that can be surveyed,
// which is the only way to measure what a reset considers MISSING: the store
// is what just changed, so whether anything backs a record is a question only
// the host can answer.
func labServerWithRuntime(t *testing.T, rt machine.Runtime, packs ...emulator.Pack) (*httptest.Server, *store.Store) {
	t.Helper()
	return labServerWith(t, nil, &rt, packs...)
}

func labServerWith(t *testing.T, objects *objectstore.Store, rt *machine.Runtime, packs ...emulator.Pack) (*httptest.Server, *store.Store) {
	t.Helper()
	held := store.New()
	env := &emulator.Env{
		Store:   held,
		Objects: objects,
		Now:     func() time.Time { return time.Unix(1700000000, 0).UTC() },
		NewID:   func() string { return "00000000-0000-4000-8000-000000000001" },
	}
	if rt != nil {
		env.UseMachines(*rt)
	}
	srv, err := emulator.NewServer(env, packs...)
	if err != nil {
		t.Fatalf("build the emulator: %v", err)
	}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	return server, held
}

func checkpointPost(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	res, err := http.Post(url, "application/json", nil) //nolint:noctx // a loopback test server
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() { _ = res.Body.Close() }()
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	return res.StatusCode, body
}

func put(t *testing.T, held *store.Store, provider, kind, id string) {
	t.Helper()
	res := resource.New(id, kind, resource.Tenant{Provider: provider, Project: "p", Zone: "z"},
		"running", time.Unix(1700000000, 0).UTC())
	res.Runtime = map[string]string{"machine": "feint-" + provider + "-" + id}
	held.Put(res)
}

// TestACheckpointTakesBothHalvesOrNeither is cited by handleCheckpointSave.
//
// A checkpoint whose store was taken and whose bytes were not is a checkpoint
// a reset cannot honour, and it would fail later, somewhere else, with no
// mention of this call. With no byte store there is only one half to take, and
// the answer says which — a caller that reset expecting bytes back would
// otherwise find none and blame the reset.
func TestACheckpointTakesBothHalvesOrNeither(t *testing.T) {
	server, held := labServer(t, stubPack{name: "acme"})
	put(t, held, "acme", "instance/server", "a")

	code, body := checkpointPost(t, server.URL+"/_feint/checkpoint/baseline")
	if code != http.StatusOK {
		t.Fatalf("checkpoint: %d %v", code, body)
	}
	if body["name"] != "baseline" {
		t.Errorf("name %v", body["name"])
	}
	if count, _ := body["resources"].(float64); int(count) != 1 {
		t.Errorf("resources %v", body["resources"])
	}
	// Said rather than guessed: this lab has no byte store, so a reset will
	// restore no bytes, and the caller is told here rather than discovering it
	// after a reset that looked clean.
	if carried, _ := body["bytes"].(bool); carried {
		t.Error("a lab with no byte store reported carrying bytes")
	}
	if version, _ := body["schema_version"].(float64); int(version) != 1 {
		t.Errorf("schema_version %v", body["schema_version"])
	}

	// With a byte store armed, both halves are taken and the answer says so.
	// Asserted on a SECOND lab rather than by widening the first, because the
	// absence of a byte store is the ordinary case and must keep its own
	// assertion above.
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	bytesStore, err := objectstore.Open(t.Context(), filepath.Join(root, "objects"))
	if err != nil {
		t.Fatalf("open the byte store: %v", err)
	}
	t.Cleanup(func() {
		if err := bytesStore.Close(); err != nil {
			t.Error(err)
		}
	})
	withBytes, _ := labServerWithBytes(t, bytesStore, stubPack{name: "acme"})
	code, body = checkpointPost(t, withBytes.URL+"/_feint/checkpoint/baseline")
	if code != http.StatusOK {
		t.Fatalf("checkpoint with bytes: %d %v", code, body)
	}
	if carried, _ := body["bytes"].(bool); !carried {
		t.Error("a lab with a byte store reported carrying none")
	}
	// And the byte checkpoint really exists, which is the half a status code
	// cannot show: resetting it must find something to restore.
	if err := bytesStore.ResetCheckpoint(t.Context(), "baseline"); err != nil {
		t.Errorf("the checkpoint named the store and did not take its bytes: %v", err)
	}

	// A name that is a path component before it is a name. Refused at the
	// door, because the byte store writes a directory per checkpoint and this
	// repository's order is a refusal at intake rather than escaping later.
	for _, bad := range []string{"..", ".", "a/b", "a b", ""} {
		if code, _ := checkpointPost(t, server.URL+"/_feint/checkpoint/"+bad); code == http.StatusOK {
			t.Errorf("the name %q was accepted", bad)
		}
	}
}

// TestAResetDestroysWhatTheCheckpointDoesNotHold is cited by handleReset.
//
// The assertion is on what the PACK WAS ASKED to take down, not on the status
// code: a reset that restored the store and told nobody about the container
// still running is the exact gap #853 names.
func TestAResetDestroysWhatTheCheckpointDoesNotHold(t *testing.T) {
	acme := &reconcilingPack{
		stubPack: stubPack{name: "acme"},
		done:     machine.Reconciled{Destroyed: []string{"feint-acme-later"}},
	}
	// A second pack that cannot reconcile, mounted on purpose: the loop must
	// skip it rather than fail, because a pack that drives no machine is the
	// ordinary case.
	server, held := labServer(t, acme, stubPack{name: "quiet"})

	put(t, held, "acme", "instance/server", "kept")
	// A second record SHARING that identifier across kinds, which is the
	// population a narrower comparison cannot see. The falsification found
	// this gap: a difference computed on identifiers alone passed every
	// assertion here until these two existed, because every id in the test
	// was distinct and a narrowed key still told them apart.
	put(t, held, "acme", "instance/ip", "kept")
	if code, body := checkpointPost(t, server.URL+"/_feint/checkpoint/baseline"); code != http.StatusOK {
		t.Fatalf("checkpoint: %d %v", code, body)
	}

	// The lab moves on: one more resource of each pack, and one that shares
	// its identifier with a record the checkpoint DOES hold, on another kind.
	put(t, held, "acme", "instance/server", "later")
	put(t, held, "acme", "instance/volume", "kept")
	put(t, held, "quiet", "instance/server", "alsolater")

	code, body := checkpointPost(t, server.URL+"/_feint/reset/baseline")
	if code != http.StatusOK {
		t.Fatalf("reset: %d %v", code, body)
	}

	// The store is back to the two the checkpoint held.
	if count, _ := body["resources"].(float64); int(count) != 2 {
		t.Errorf("resources after the reset: %v", body["resources"])
	}
	// And the pack was handed exactly the records the checkpoint does not
	// hold — not the ones it does, which is the direction that would destroy a
	// machine the checkpoint expects to keep. TWO of them, and the second
	// shares its identifier with a kept record: a comparison that dropped the
	// kind would have taken it for the one that survived and left its machine
	// running.
	if len(acme.got) != 1 || len(acme.got[0]) != 2 {
		t.Fatalf("the pack was asked to reconcile %v", acme.got)
	}
	handed := append([]string(nil), acme.got[0]...)
	sort.Strings(handed)
	if handed[0] != "kept" || handed[1] != "later" {
		t.Fatalf("the pack was handed %v, want the volume sharing an id and the extra server", handed)
	}
	if destroyed, _ := body["destroyed"].([]any); len(destroyed) != 1 || destroyed[0] != "feint-acme-later" {
		t.Errorf("destroyed %v", body["destroyed"])
	}
	// Recreation IS attempted now, and the answer says so. Nothing was
	// recreated here because this lab has no runtime: the records the
	// checkpoint holds name machines the survey cannot be asked about, and a
	// reset that started containers because nobody could be asked would be
	// the opposite defect.
	if recreates, _ := body["recreates"].(bool); !recreates {
		t.Error("the reset says it does not recreate, which stopped being true")
	}
	if back, _ := body["recreated"].([]any); len(back) != 0 {
		t.Errorf("a lab with no runtime recreated %v", back)
	}

	// Twice is a no-op, which is #853's own criterion: nothing is left to
	// reconcile, so the pack is not asked again.
	if code, body := checkpointPost(t, server.URL+"/_feint/reset/baseline"); code != http.StatusOK {
		t.Fatalf("second reset: %d %v", code, body)
	}
	if len(acme.got) != 1 {
		t.Errorf("a second reset asked for more reconciliation: %v", acme.got)
	}

	// An unknown checkpoint is refused by name, and the refusal says how to
	// make one: a 404 alone sends the reader to the source.
	code, body = checkpointPost(t, server.URL+"/_feint/reset/nothing-here")
	if code != http.StatusNotFound {
		t.Fatalf("reset of an unknown checkpoint: %d %v", code, body)
	}
	if message, _ := body["error"].(string); message == "" ||
		!strings.Contains(message, "nothing-here") || !strings.Contains(message, "/_feint/checkpoint/") {
		t.Errorf("the refusal does not say what to do: %v", body["error"])
	}
}
