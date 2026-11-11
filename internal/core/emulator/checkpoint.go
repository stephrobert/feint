package emulator

import (
	"bytes"
	"context"
	"net/http"
	"slices"
	"sort"
	"sync"

	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Named checkpoints of a whole lab, and the reset that returns to one (#853).
//
// `feint snapshot` already persists the control plane, and
// objectstore.SaveCheckpoint already persists bytes. Neither had a caller that
// put them together, and putting them together is the point: #853 asks for a
// LIFECYCLE guarantee rather than two storage primitives, and says what
// separates them in one line — "reset is not successful because JSON was
// restored".
//
// ## Why the reset happens here rather than in the CLI
//
// Restoring the store is the easy half. The half that matters is the host: a
// resource that disappears in a restore leaves its container running while the
// API no longer mentions it, and nobody destroys it. Only this process holds
// both the store and the packs' bindings, so only this process can act on the
// difference.
//
// A CLI-side reset would have to name each pack's delete route to take a
// machine down, which is provider knowledge in the one place this repository
// keeps free of it.
//
// ## Why the VERDICT is not here
//
// This answers what it did. Whether the lab converged is a comparison of what
// a client can observe, and that belongs to whoever asked for the reset —
// internal/cli's observable state. An operation that graded itself would be
// the verdict this repository refuses everywhere else.

// Reconciler is the optional half of a pack that can bring the host back in
// line with its own stored resources.
//
// Optional in the shape the other halves use (EnvHazards, StackHazards,
// FirewallEnforcer): a pack that drives no machine does not implement it, and
// nothing here holds a list of which do.
//
// The input is the pack's OWN resources that no longer exist, so no pack ever
// sees another's, and the core stays a loop over whatever is mounted.
type Reconciler interface {
	ReconcileGone(ctx context.Context, gone []*resource.Resource) machine.Reconciled
}

// MissingReconciler is the other half, and a SEPARATE interface rather than a
// second method on Reconciler.
//
// That is not style. A single interface carrying both means a pack
// implementing one of them satisfies neither, the type assertion fails, and
// the reconciliation silently stops happening — no error, no log, a reset that
// reports a clean pass and reconciles nothing. It happened while this half was
// being written: adding the second method to Reconciler turned every pack into
// a non-implementer, and the only reason it was caught is that a test asserted
// which records a pack was HANDED rather than that the call answered 200.
//
// Two optional halves cannot do that: a pack gains one when it is ready, and
// the one it already had keeps working.
//
// internal/cli's TestAPackThatDrivesMachinesReconcilesBothWays fails without
// both halves being satisfied by every pack that drives machines.
type MissingReconciler interface {
	// ReconcileMissing starts again the machines of this pack's records that
	// the host no longer holds.
	//
	// It takes the records and the machines the host DOES hold, and the pack
	// decides which of its records are missing. The core deliberately does not
	// decide: a record's machine name lives under a key that is the pack's own
	// vocabulary, and a first version of this guessed with a "feint-" prefix
	// in the core. It was wrong twice over — a provider-shaped assumption in
	// the neutral layer, and broad enough to match a test's own bookkeeping
	// stamp, which it then tried to boot.
	ReconcileMissing(ctx context.Context, records []*resource.Resource, held []string) machine.Reconciled
}

// checkpoints holds the control-plane half of each named checkpoint.
//
// In memory, and that is the scope #853 describes: "returns to the session's
// initial state". Bytes are durable because objectstore's checkpoints are, and
// a reader who wants the control plane to outlive the process has
// `feint snapshot save`, which writes a file and is a different promise.
type checkpoints struct {
	mu    sync.Mutex
	saved map[string][]byte
}

func (c *checkpoints) put(name string, state []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.saved == nil {
		c.saved = map[string][]byte{}
	}
	c.saved[name] = state
}

func (c *checkpoints) get(name string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, ok := c.saved[name]
	return state, ok
}

func (c *checkpoints) drop(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.saved[name]
	delete(c.saved, name)
	return ok
}

// handleCheckpointSave records the control plane and the bytes under one name.
//
// Both halves or neither: a checkpoint whose store was taken and whose bytes
// were not is a checkpoint a reset cannot honour, and it would fail later,
// somewhere else, with no mention of this call. So the byte half goes first —
// it is the one that can fail on a disk — and the store half follows only
// when it succeeded.
//
// TestACheckpointTakesBothHalvesOrNeither fails without this.
func (s *Server) handleCheckpointSave(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validCheckpointName(name) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "a checkpoint name is 1 to 64 characters of letters, digits, dash, dot or underscore",
		})
		return
	}
	if s.env.Objects != nil {
		if err := s.env.Objects.SaveCheckpoint(r.Context(), name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": "the bytes could not be checkpointed: " + err.Error(),
			})
			return
		}
	}
	var state bytes.Buffer
	if err := s.env.Store.Snapshot(&state); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "the control plane could not be checkpointed: " + err.Error(),
		})
		return
	}
	s.checkpoints.put(name, state.Bytes())
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": CheckpointSchemaVersion,
		"name":           name,
		"resources":      s.env.Store.Len(),
		"bytes":          s.env.Objects != nil,
	})
}

// handleCheckpointDelete forgets a checkpoint, both halves.
func (s *Server) handleCheckpointDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	held := s.checkpoints.drop(name)
	if s.env.Objects != nil {
		if err := s.env.Objects.DeleteCheckpoint(r.Context(), name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": "the bytes could not be forgotten: " + err.Error(),
			})
			return
		}
	}
	if !held {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "no checkpoint named " + name,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "name": name})
}

// handleReset returns the lab to a checkpoint and reconciles the host with it.
//
// The order is not interchangeable, and each step's reason is its own:
//
//  1. read what the store holds NOW, because after step 2 it is gone and the
//     machines it named would be unreachable;
//  2. restore the control plane, so the target state is in place before
//     anything is destroyed — a failure here leaves the host untouched rather
//     than half swept;
//  3. restore the bytes, which objectstore does atomically;
//  4. destroy the machines of records that no longer exist, through each
//     pack's own binding and therefore through the ownership checks.
//
// What it does NOT do is recreate a machine a checkpoint had and the host no
// longer does. Starting one needs a Boot — an image, a login, the cloud-init a
// pack renders — which is pack knowledge, and #853 bounds it in the same words:
// "where the runtime contract supports it". The answer says so rather than
// leaving a caller to infer it from a quiet success.
//
// TestAResetDestroysWhatTheCheckpointDoesNotHold fails without this.
func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	state, ok := s.checkpoints.get(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "no checkpoint named " + name + "; take one with POST /_feint/checkpoint/" + name,
		})
		return
	}

	before := s.env.Store.All()
	if err := s.env.Store.Restore(bytes.NewReader(state)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "the control plane could not be restored: " + err.Error(),
		})
		return
	}
	if s.env.Objects != nil {
		if err := s.env.Objects.ResetCheckpoint(r.Context(), name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": "the bytes could not be restored: " + err.Error(),
			})
			return
		}
	}

	restored := s.env.Store.All()
	gone := goneAfterRestore(before, restored)
	// What the host actually holds, asked of the runtime rather than deduced
	// from the store: the store is exactly what just changed, it says a server
	// is running, and whether anything backs it is the question. A driver that
	// cannot be surveyed answers asked=false, and then nothing is offered for
	// recreation at all — claiming a machine is absent because nobody could be
	// asked would start containers beside ones already running.
	held, surveyed := heldOnHost(r.Context(), s.env)
	reconciled := map[string]any{}
	var destroyed, skipped, recreated, failed []string
	for _, pack := range s.packs {
		mine := ownedBy(gone, pack.Name())
		records := ownedBy(restored, pack.Name())
		if len(mine) == 0 && (!surveyed || len(records) == 0) {
			continue
		}
		done := machine.Reconciled{}
		if reconciler, ok := pack.(Reconciler); ok && len(mine) != 0 {
			done = reconciler.ReconcileGone(r.Context(), mine)
		}
		if starter, ok := pack.(MissingReconciler); ok && surveyed && len(records) != 0 {
			back := starter.ReconcileMissing(r.Context(), records, held)
			done.Recreated = append(done.Recreated, back.Recreated...)
			done.Failed = append(done.Failed, back.Failed...)
			done.Skipped = append(done.Skipped, back.Skipped...)
			// A recreation rewrites the record's Runtime and State, so the
			// store is written back here rather than left holding the machine
			// name of a container that is gone. Put rather than Commit: the
			// resource came out of this store a moment ago and nothing else
			// writes during a reset, which holds the whole store's lock.
			for _, res := range records {
				s.env.Store.Put(res)
			}
		}
		destroyed = append(destroyed, done.Destroyed...)
		skipped = append(skipped, done.Skipped...)
		recreated = append(recreated, done.Recreated...)
		failed = append(failed, done.Failed...)
		reconciled[pack.Name()] = map[string]any{
			"destroyed": done.Destroyed, "skipped": done.Skipped,
			"recreated": done.Recreated, "failed": done.Failed,
		}
	}
	sort.Strings(destroyed)
	sort.Strings(skipped)
	sort.Strings(recreated)
	sort.Strings(failed)

	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": CheckpointSchemaVersion,
		"name":           name,
		"resources":      s.env.Store.Len(),
		"destroyed":      destroyed,
		"skipped":        skipped,
		"recreated":      recreated,
		// Named apart from skipped: a start that was attempted and refused and
		// a record nobody could act on need different answers from whoever
		// reads this.
		"failed":       failed,
		"per_provider": reconciled,
		"recreates":    true,
	})
}

// goneAfterRestore answers the resources the restore removed.
//
// By identity rather than by count: a restore that replaced one server with
// another of the same kind leaves the count equal and the host wrong, which is
// the comparison a length check would miss.
func goneAfterRestore(before, after []*resource.Resource) []*resource.Resource {
	keys := make([]string, 0, len(after))
	for _, res := range after {
		keys = append(keys, resourceKey(res))
	}
	slices.Sort(keys)
	var gone []*resource.Resource
	for _, res := range before {
		if _, found := slices.BinarySearch(keys, resourceKey(res)); !found {
			gone = append(gone, res)
		}
	}
	return gone
}

func resourceKey(res *resource.Resource) string {
	return res.Tenant.Provider + "\x00" + res.Kind + "\x00" + res.ID
}

// validCheckpointName keeps a name out of a path and out of a shell.
//
// The byte store writes a directory per checkpoint, so a name is a path
// component before it is anything else. Refused at the door rather than
// escaped later: this repository's order of preference is a serialiser first,
// a refusal at intake second, escaping last.
func validCheckpointName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '.', r == '_':
		default:
			return false
		}
	}
	// A name of dots is a path, not a name.
	return name != "." && name != ".."
}

// ownedBy narrows a list of resources to one pack's own, so no pack ever sees
// another's.
func ownedBy(resources []*resource.Resource, provider string) []*resource.Resource {
	out := make([]*resource.Resource, 0, len(resources))
	for _, res := range resources {
		if res.Tenant.Provider == provider {
			out = append(out, res)
		}
	}
	return out
}

// heldOnHost answers the machines the runtime actually holds, and whether it
// could be asked at all.
//
// Through Survey, which names only what the emulator labelled and issues no
// mutating command. The second return is not a convenience: a driver nobody
// can survey is not an empty host, and treating it as one would have a reset
// start a container beside every one already running.
//
// TestAResetAsksNothingOfAHostNobodyCanSurvey fails without this.
func heldOnHost(ctx context.Context, env *Env) ([]string, bool) {
	left, asked, err := env.machines.Survey(ctx)
	if !asked || err != nil {
		return nil, false
	}
	return left.Machines, true
}
