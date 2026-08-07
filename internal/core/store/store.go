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
	"sort"
	"sync"
	"time"

	"github.com/stephrobert/feint/internal/core/resource"
)

// Store is safe for concurrent use by the HTTP handlers.
type Store struct {
	mu    sync.RWMutex
	items map[string]*resource.Resource
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
	defer s.mu.Unlock()
	s.items[key(r.Tenant.Provider, r.Kind, r.ID)] = r.Clone()
}

// Get returns a clone of the resource, or false when it does not exist.
func (s *Store) Get(provider, kind, id string) (*resource.Resource, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.items[key(provider, kind, id)]
	if !ok {
		return nil, false
	}
	return r.Clone(), true
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
}

// Commit writes back a resource that was worked on outside the lock, and reports
// whether it still exists.
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
// What this does not do is merge. State, Runtime and Attrs are taken from the
// copy wholesale, so a concurrent write to a *different* field of the same
// resource is still lost. That is a narrower race than resurrection and it needs
// per-field intent to fix; Update is there for a caller that has it.
func (s *Store) Commit(res *resource.Resource, now time.Time) bool {
	err := s.Update(res.Tenant.Provider, res.Kind, res.ID, func(stored *resource.Resource) error {
		stored.State = res.State
		stored.Runtime = res.Runtime
		stored.Attrs = res.Attrs
		stored.Updated = now
		return nil
	})
	return err == nil
}

// Delete removes a resource and reports whether it existed.
func (s *Store) Delete(provider, kind, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(provider, kind, id)
	_, ok := s.items[k]
	delete(s.items, k)
	return ok
}

// List returns the resources of one kind matching the tenant filter, oldest
// first. The order is stable across calls (creation time, then ID) because
// Terraform and the CLIs page through these lists and a shuffling order would
// produce phantom diffs.
func (s *Store) List(kind string, t resource.Tenant) []*resource.Resource {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*resource.Resource, 0, len(s.items))
	for _, r := range s.items {
		if r.Kind == kind && r.Matches(t) {
			out = append(out, r.Clone())
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created.Equal(out[j].Created) {
			return out[i].ID < out[j].ID
		}
		return out[i].Created.Before(out[j].Created)
	})
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

// Snapshot writes the whole store as JSON. The format is an implementation
// detail of the emulator and carries no compatibility promise.
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
	err := enc.Encode(list)
	s.mu.RUnlock()

	if err != nil {
		return fmt.Errorf("snapshot store: %w", err)
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}
	return nil
}

// Restore replaces the store content with a snapshot produced by Snapshot.
func (s *Store) Restore(r io.Reader) error {
	var list []*resource.Resource
	if err := json.NewDecoder(r).Decode(&list); err != nil {
		return fmt.Errorf("restore store: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = make(map[string]*resource.Resource, len(list))
	for _, res := range list {
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
