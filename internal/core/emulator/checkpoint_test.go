package emulator_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
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
	held := store.New()
	env := &emulator.Env{
		Store: held,
		Now:   func() time.Time { return time.Unix(1700000000, 0).UTC() },
		NewID: func() string { return "00000000-0000-4000-8000-000000000001" },
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
	if code, body := checkpointPost(t, server.URL+"/_feint/checkpoint/baseline"); code != http.StatusOK {
		t.Fatalf("checkpoint: %d %v", code, body)
	}

	// The lab moves on: one more resource of each pack.
	put(t, held, "acme", "instance/server", "later")
	put(t, held, "quiet", "instance/server", "alsolater")

	code, body := checkpointPost(t, server.URL+"/_feint/reset/baseline")
	if code != http.StatusOK {
		t.Fatalf("reset: %d %v", code, body)
	}

	// The store is back to one resource.
	if count, _ := body["resources"].(float64); int(count) != 1 {
		t.Errorf("resources after the reset: %v", body["resources"])
	}
	// And the pack was handed exactly the resource the checkpoint does not
	// hold — not the one it does, which is the direction that would destroy a
	// machine the checkpoint expects to keep.
	if len(acme.got) != 1 || len(acme.got[0]) != 1 || acme.got[0][0] != "later" {
		t.Fatalf("the pack was asked to reconcile %v", acme.got)
	}
	if destroyed, _ := body["destroyed"].([]any); len(destroyed) != 1 || destroyed[0] != "feint-acme-later" {
		t.Errorf("destroyed %v", body["destroyed"])
	}
	// Recreation is not attempted, and the answer says so: a quiet success
	// would read as "the host matches the checkpoint".
	if recreates, _ := body["recreates"].(bool); recreates {
		t.Error("the reset claims it recreates missing machines")
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
