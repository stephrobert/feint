// Package store keeps emulated resources in memory, with an optional JSON
// snapshot so a session survives a restart.
//
// The store is deliberately dumb: no schema, no validation, no provider
// knowledge. That is what lets one store serve Scaleway, Outscale and Exoscale
// at once, and what lets a new provider product work without touching it.
package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/stephrobert/feint/internal/core/resource"
)

// Store is safe for concurrent use by the HTTP handlers.
type Store struct {
	mu    sync.RWMutex
	items map[string]*resource.Resource
	// observe, when set, is told about every touch a caller makes. See Observe.
	observe func(Event)
}

// Event is one observed touch of the store: something was created, read,
// updated, deleted, or a kind was listed.
//
// It exists for the behaviour axis of the evidence record (#123): "this
// operation took part in a real lifecycle" must be observed, never declared,
// and the store is the one place every pack's create, read and delete already
// passes through. The event names the resource in the store's own neutral
// coordinates — provider, kind, ID are data here, exactly as they are in the
// snapshot — so no provider knowledge enters this package.
//
// Snapshot and Restore emit nothing: they move state across a persistence
// boundary, they are not a client exercising a lifecycle.
type Event struct {
	// Action is one of the Event* constants below.
	Action string
	// Provider, Kind and ID locate the resource. ID is empty for EventListed,
	// which is about a kind, not one resource.
	Provider, Kind, ID string
}

// The actions an Event can carry.
const (
	EventCreated = "created"
	EventRead    = "read"
	EventUpdated = "updated"
	EventDeleted = "deleted"
	EventListed  = "listed"
)

// Observe registers the single observer told about every subsequent touch.
//
// The callback runs outside the store's lock, on the goroutine that made the
// touch, so it may not call back into the store and must return quickly: it
// runs on the request path.
func (s *Store) Observe(fn func(Event)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observe = fn
}

// notify hands an event to the observer, if any. Called after s.mu is
// released, never under it.
func (s *Store) notify(ev Event) {
	s.mu.RLock()
	fn := s.observe
	s.mu.RUnlock()
	if fn != nil {
		fn(ev)
	}
}

// New returns an empty store.
func New() *Store {
	return &Store{items: make(map[string]*resource.Resource)}
}

func key(provider, kind, id string) string {
	return provider + "|" + kind + "|" + id
}

// Put inserts or replaces a resource. The caller keeps ownership of r: the store
// keeps a clone, so later mutations by the caller are not visible.
func (s *Store) Put(r *resource.Resource) {
	s.mu.Lock()
	k := key(r.Tenant.Provider, r.Kind, r.ID)
	_, existed := s.items[k]
	s.items[k] = r.Clone()
	s.mu.Unlock()

	action := EventCreated
	if existed {
		action = EventUpdated
	}
	s.notify(Event{Action: action, Provider: r.Tenant.Provider, Kind: r.Kind, ID: r.ID})
}

// Get returns a clone of the resource, or false when it does not exist.
func (s *Store) Get(provider, kind, id string) (*resource.Resource, bool) {
	s.mu.RLock()
	r, ok := s.items[key(provider, kind, id)]
	var out *resource.Resource
	if ok {
		out = r.Clone()
	}
	s.mu.RUnlock()

	if !ok {
		return nil, false
	}
	s.notify(Event{Action: EventRead, Provider: provider, Kind: kind, ID: id})
	return out, true
}

// ErrNotFound is what Update reports when the resource is gone. Callers that
// only want to skip a vanished resource compare against it rather than guessing
// from the message.
var ErrNotFound = errors.New("resource not found")

// Update applies a change to a resource while holding the write lock, so the
// read, the change and the write cannot be interleaved with another request.
//
// Get returns a clone and Put replaces the whole resource, so every
// read-modify-write done as two calls loses one of two concurrent writes. That
// is not theoretical here: attaching a private NIC writes the server back with
// the state it read, and a poweron running at the same time takes tens of
// seconds to launch a container between its own read and write — so the
// attachment silently reset a machine that was already running to "stopped".
// Terraform creates ten resources at a time by default.
//
// The function runs under the lock, so it must not call the runtime, sleep, or
// touch the store again. What it is for is the arithmetic: read a field, decide,
// write it back. Anything slow stays outside, which is why Update reports what
// changed rather than doing the slow part itself.
func (s *Store) Update(provider, kind, id string, change func(*resource.Resource) error) error {
	err := func() error {
		s.mu.Lock()
		defer s.mu.Unlock()

		r, ok := s.items[key(provider, kind, id)]
		if !ok {
			return ErrNotFound
		}
		// The change works on a clone: a function that fails must leave the stored
		// resource exactly as it was, not half-modified.
		draft := r.Clone()
		if err := change(draft); err != nil {
			return err
		}
		s.items[key(provider, kind, id)] = draft
		return nil
	}()
	if err != nil {
		return err
	}
	s.notify(Event{Action: EventUpdated, Provider: provider, Kind: kind, ID: id})
	return nil
}

// Commit writes back a resource that was worked on outside the lock, and reports
// whether it still exists. base is the clone the caller read before it started;
// only what the caller changed relative to that clone is written.
//
// This is the shape every pack needs and only one of them had. Starting a
// machine takes tens of seconds, so no pack holds the store lock across it: it
// takes a copy, calls the runtime, and writes the result back. Writing it back
// with Put re-inserts the resource unconditionally, so a delete that landed
// during those seconds is undone and the machine comes back — observed on
// Outscale, fixed there, and left alive in the two other packs until an audit
// found it. Terraform destroys ten resources at a time by default, which is
// exactly the window.
//
// A false return is not an error. It means the client deleted the resource while
// its machine was starting, and the caller should drop it from its answer rather
// than report a state nobody can read back.
//
// The merge is the second half, added by #295 after the first half had shipped
// without it. The old Commit took State, Runtime and Attrs from the copy
// wholesale, and its own comment named the consequence: a concurrent write to a
// *different* field of the same resource was lost. Measured, not feared — a
// CreateTags answered 200 while a lifecycle action was between its read and its
// write, and the following read no longer had the tag: 5 of 200 trials with no
// machine runtime configured, every trial with one, since a launch holds the
// window open for seconds. Per-field intent closes it: a field the caller left
// alone keeps whatever a concurrent writer put there, a field the caller
// changed — set, or deleted — is the caller's to write, because Serialise
// orders the writers that share a field and this merge is for the writers that
// do not.
//
// The diff is per top-level unit: State, each Attrs key, each Runtime key.
// Nested values need no deeper diff because packs never mutate them in place —
// resource.Clone shares them with the store, so an in-place write is already a
// defect on its own (updateNic paid for it, #295) — and a changed nested value
// therefore always arrives as a fresh top-level assignment.
//
// TestCommitKeepsAConcurrentWriteToAnotherField and
// TestCommitDoesNotResurrectADeletedResource fail without the two halves.
func (s *Store) Commit(base, res *resource.Resource, now time.Time) bool {
	err := s.Update(res.Tenant.Provider, res.Kind, res.ID, func(stored *resource.Resource) error {
		if base.State != res.State {
			stored.State = res.State
		}
		mergeMap(stored.Attrs, base.Attrs, res.Attrs)
		if stored.Runtime == nil && len(res.Runtime) > 0 {
			stored.Runtime = map[string]string{}
		}
		mergeRuntime(stored.Runtime, base.Runtime, res.Runtime)
		stored.Updated = now
		return nil
	})
	return err == nil
}

// mergeMap applies to stored every top-level change between base and res:
// a key res added or rewrote wins, a key res deleted goes, and a key res left
// alone keeps the stored value — which is the concurrent writer's, if one came
// through while the caller worked.
func mergeMap(stored, base, res map[string]any) {
	for key, value := range res {
		before, had := base[key]
		if !had || !reflect.DeepEqual(before, value) {
			stored[key] = value
		}
	}
	for key := range base {
		if _, still := res[key]; !still {
			delete(stored, key)
		}
	}
}

// mergeRuntime is mergeMap for the Runtime map, whose values compare as
// strings.
func mergeRuntime(stored, base, res map[string]string) {
	for key, value := range res {
		if before, had := base[key]; !had || before != value {
			stored[key] = value
		}
	}
	for key := range base {
		if _, still := res[key]; !still {
			delete(stored, key)
		}
	}
}

// Delete removes a resource and reports whether it existed.
func (s *Store) Delete(provider, kind, id string) bool {
	s.mu.Lock()
	k := key(provider, kind, id)
	_, ok := s.items[k]
	delete(s.items, k)
	s.mu.Unlock()

	if ok {
		s.notify(Event{Action: EventDeleted, Provider: provider, Kind: kind, ID: id})
	}
	return ok
}

// List returns the resources of one kind matching the tenant filter, oldest
// first. The order is stable across calls (creation time, then ID) because
// Terraform and the CLIs page through these lists and a shuffling order would
// produce phantom diffs.
func (s *Store) List(kind string, t resource.Tenant) []*resource.Resource {
	s.mu.RLock()
	out := make([]*resource.Resource, 0, len(s.items))
	for _, r := range s.items {
		if r.Kind == kind && r.Matches(t) {
			out = append(out, r.Clone())
		}
	}
	s.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		if out[i].Created.Equal(out[j].Created) {
			return out[i].ID < out[j].ID
		}
		return out[i].Created.Before(out[j].Created)
	})
	s.notify(Event{Action: EventListed, Provider: t.Provider, Kind: kind})
	return out
}

// All returns a clone of every resource, whatever its provider or kind, in the
// same stable order List uses.
//
// List cannot answer this: it filters on one kind, and the kinds are each pack's
// own vocabulary, which the core does not know and must not learn. An inventory
// of the whole store is therefore the only provider-neutral way to see what a
// session has created — which is what /_feint/resources serves, and why a fourth
// pack appears there without a line changing.
//
// TestTheInventoryShowsAPackThePageHasNeverHeardOf in internal/core/emulator
// fails without this.
func (s *Store) All() []*resource.Resource {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*resource.Resource, 0, len(s.items))
	for _, r := range s.items {
		out = append(out, r.Clone())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created.Equal(out[j].Created) {
			return out[i].ID < out[j].ID
		}
		return out[i].Created.Before(out[j].Created)
	})
	return out
}

// Len reports how many resources are stored, all providers and kinds together.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}

// Snapshot writes the whole store as JSON, under a format header.
//
// The format is a published surface, not an implementation detail. This comment
// said the opposite until #133, while RELEASING.md listed "the state and
// snapshot formats" among the surfaces whose change is breaking — two sentences
// that could not both be the policy, and the file itself was written as if the
// looser one were true. It is the stricter one: the format is named, versioned,
// and a change to it is a breaking change under SemVer.
//
// The encoding happens into memory under the lock, and the write to w happens
// without it. Encoding straight into w held the read lock for as long as the
// reader took to consume the body, and a reader that simply stopped reading
// froze the whole emulator: Go's RWMutex is fair, so one waiting writer blocks
// every subsequent reader — /_feint/health included. An audit reproduced it by
// opening GET /_feint/state against a 30 MB store and not consuming it.
//
// The cost is one copy of the store in memory, which is bounded by the same
// thing that bounds the store itself.
func (s *Store) Snapshot(w io.Writer) error {
	var buf bytes.Buffer

	s.mu.RLock()
	keys := make([]string, 0, len(s.items))
	for k := range s.items {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	list := make([]*resource.Resource, 0, len(keys))
	for _, k := range keys {
		list = append(list, s.items[k])
	}
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	err := enc.Encode(snapshotFile{
		Format:    snapshotFormat,
		Version:   snapshotVersion,
		Resources: list,
	})
	s.mu.RUnlock()

	if err != nil {
		return fmt.Errorf("snapshot store: %w", err)
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}
	return nil
}

// The snapshot envelope, and the rule it exists to enforce: a snapshot is
// understood or refused, never restored partially in silence.
//
// Until #133 the file was a bare JSON array decoded with a plain Decode, so
// `encoding/json` dropped every field this build does not declare — measured,
// not feared: a resource carrying `encryption_key_ref` restored successfully,
// and the following Snapshot wrote it back without the field. The store was
// coherent, wrong, and green, which is the exact failure this project exists to
// refuse, committed on its own state file.
//
// snapshot.go documents the format as made to outlive its instance and be loaded
// into another one, and RELEASING.md lists it among the surfaces whose change is
// breaking. Both are only true if the file says which version it is.
const (
	snapshotFormat  = "feint-snapshot"
	snapshotVersion = 1
)

// snapshotFile is the envelope. Kept closed on the way in — an unknown key here
// means the file was written by something this build does not understand, and
// guessing which half is safe to keep is the silent partial restore all over
// again.
type snapshotFile struct {
	Format    string               `json:"format"`
	Version   int                  `json:"version"`
	Resources []*resource.Resource `json:"resources"`
}

// Restore replaces the store content with a snapshot produced by Snapshot.
//
// Understood or refused. Four answers, each deliberate:
//
//   - this version, every field known: restored;
//   - a version from the future, or a format that is not ours: refused, naming
//     both what was read and what this build writes, because an operator whose
//     restore fails needs to know which of the two binaries to change;
//   - a bare `[...]`, which is every snapshot written before #133: refused, and
//     the message says how to convert it. Accepting it silently as "version 0"
//     was the other option and it loses the property this change is for — such a
//     file has already passed through a decoder that drops unknown fields, so
//     nobody can say whether it is complete.
//   - an unknown field on a resource: refused, naming the field. Attrs stays
//     open, because its keys are data a pack chose, not schema.
//
// TestASnapshotFromTheFutureIsRefused, TestAnUnknownResourceFieldIsRefused and
// TestALegacyBareArrayIsRefusedWithARemedy fail without this.
func (s *Store) Restore(r io.Reader) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()

	var file snapshotFile
	if err := dec.Decode(&file); err != nil {
		// A bare array is the pre-#133 format, and it is worth telling apart
		// from a corrupt file: the operator has a real snapshot in their hand
		// and needs to know what to do with it, not that their JSON is broken.
		if looksLikeBareArray(err) {
			return fmt.Errorf("restore store: this snapshot has no format header, "+
				"so it was written by feint before the %s envelope existed (#133). "+
				"Its completeness cannot be established — the decoder that wrote it "+
				"dropped fields it did not know. Take a fresh snapshot from the "+
				"instance that holds the state, or wrap the array as "+
				`{"format":%q,"version":%d,"resources":[...]}`,
				snapshotFormat, snapshotFormat, snapshotVersion)
		}
		return fmt.Errorf("restore store: %w", err)
	}
	if file.Format != snapshotFormat {
		return fmt.Errorf("restore store: format is %q, and this build reads %q",
			file.Format, snapshotFormat)
	}
	if file.Version != snapshotVersion {
		return fmt.Errorf("restore store: snapshot version %d, and this build reads version %d: "+
			"restore it with the feint that wrote it, or take a fresh snapshot",
			file.Version, snapshotVersion)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = make(map[string]*resource.Resource, len(file.Resources))
	for _, res := range file.Resources {
		// A snapshot is operator-supplied, and a null element in the array
		// decodes to a nil pointer that panics on the next field read. Skipped
		// rather than refused: one bad entry must not make a session
		// unrestorable.
		if res == nil {
			continue
		}
		s.items[key(res.Tenant.Provider, res.Kind, res.ID)] = res
	}
	return nil
}

// looksLikeBareArray tells the old format from a corrupt file.
//
// By the decoder's own complaint rather than by peeking at the first byte: the
// reader may not be seekable — `PUT /_feint/state` hands a request body straight
// through — and buffering the whole file to look at one character would undo the
// bound snapshot.go puts on it.
func looksLikeBareArray(err error) bool {
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &typeErr) && typeErr.Value == "array"
}
