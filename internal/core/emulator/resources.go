package emulator

import (
	"net/http"
	"time"

	"github.com/stephrobert/feint/internal/core/resource"
)

// The inventory: everything the session has created, read from the store.
//
// It is provider-neutral by construction rather than by discipline. The store
// holds resource.Resource, whose provider, kind and state are values rather than
// types, so this endpoint lists a pack nobody has written yet — proven by
// TestTheInventoryShowsAPackThePageHasNeverHeardOf, which mounts a stub pack
// with an invented provider name and finds its resource here.
//
// That is worth more than a console per provider: it reads the source of truth
// instead of a copy of it, so it cannot drift from what the packs actually
// stored, and it needs no maintenance when a pack gains a kind.
//
// Attrs are served whole, never a chosen subset. A hand-picked list of
// interesting fields is a list somebody has to maintain, and the day it goes
// stale is the day it hides the field somebody was looking for.

// ResourceView is one stored resource, as the inventory publishes it.
type ResourceView struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	Provider string            `json:"provider"`
	Project  string            `json:"project,omitempty"`
	Zone     string            `json:"zone,omitempty"`
	State    string            `json:"state,omitempty"`
	Created  time.Time         `json:"created"`
	Updated  time.Time         `json:"updated"`
	Attrs    map[string]any    `json:"attrs"`
	Runtime  map[string]string `json:"runtime,omitempty"`
}

// handleResources answers the inventory.
//
// On Runtime, and the decision is deliberate. resource.Resource documents that
// the field "must never reach a client: the backing container name, for
// instance", and that rule is about the provider views — what scw, the SDKs and
// Terraform receive, where an emulator-side name would be a field the real cloud
// does not have and a client could come to depend on.
//
// /_feint/* is not a provider API. It is the emulator's own control plane, it is
// mounted on the loopback interface only, and it already publishes internals no
// cloud has: the route table and the conformance counters. Naming the container
// that actually backs a machine is the whole value of the machines region, and
// hiding it here would send an operator to `incus list` to find out which
// container belongs to which server.
//
// The opening is therefore local to this endpoint, and it is held there by
// TestNoProviderViewSerializesRuntime, which stamps every stored resource and
// drives every GET route of every pack looking for the stamp. Without that test
// this comment would be exactly the thing CLAUDE.md warns about: an intention
// written in the past tense that reads like a proof.
func (s *Server) handleResources(w http.ResponseWriter, _ *http.Request) {
	stored := s.env.Store.All()
	out := make([]ResourceView, 0, len(stored))
	for _, r := range stored {
		out = append(out, resourceView(r))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"count":     len(out),
		"resources": out,
	})
}

func resourceView(r *resource.Resource) ResourceView {
	attrs := r.Attrs
	if attrs == nil {
		// An absent map encodes as null, and a page reading its length would
		// have to test for that everywhere. An empty object says the same thing
		// and reads the same as the ninety-nine resources that have attributes.
		attrs = map[string]any{}
	}
	return ResourceView{
		ID:       r.ID,
		Kind:     r.Kind,
		Provider: r.Tenant.Provider,
		Project:  r.Tenant.Project,
		Zone:     r.Tenant.Zone,
		State:    r.State,
		Created:  r.Created,
		Updated:  r.Updated,
		Attrs:    attrs,
		Runtime:  r.Runtime,
	}
}
