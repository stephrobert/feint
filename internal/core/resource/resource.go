// Package resource holds the provider-neutral shape of an emulated cloud resource.
//
// Every provider pack stores its objects as a Resource: the emulator core never
// knows what a Scaleway server or an Exoscale instance actually is. Attrs carries
// the wire representation the pack serves back, so adding a field to a provider
// API never requires a change here.
package resource

import "time"

// Tenant is the isolation boundary a resource belongs to. Providers name these
// differently (Scaleway: project + zone, Outscale: account + region, Exoscale:
// organization + zone), so the core keeps neutral names and each pack maps onto
// them. An empty field means the provider does not scope by it.
type Tenant struct {
	Provider string
	Project  string
	Zone     string
}

// Resource is one emulated object.
//
// Attrs is the provider-shaped body: the pack owns its keys and the core only
// ever copies it. State is kept out of Attrs because the core needs it for
// lifecycle transitions, and packs mirror it into Attrs when they serialize.
//
// # What may go into Attrs
//
// The JSON shape, never the Go shape (#567). The store's snapshot is JSON —
// the door `feint snapshot load` and `PUT /_feint/state` both go through, and
// the one snapshot.go documents as meant to outlive its instance — so a
// restore hands back only what encoding/json produces:
//
//	nil   bool   string   float64   []any   map[string]any
//
// Write anything else and the value silently becomes something else the first
// time a snapshot is taken. Measured on 2026-08-27 on the pack built to be
// copied, internal/cli/testdata/provider-four:
//
//	[]Rule            -> nil               a node wearing no rule set
//	[]string          -> []any             a node joining no segment
//	map[string]string -> map[string]any    a node with no address
//
// while the API went on describing all three. And it is not only a newcomer's
// mistake: the first run of the control below over Scaleway's own barrage,
// 2026-08-28, reported 82 resources holding a []string in Attrs["tags"].
// Nothing was broken by those, because two files in that pack carry a
// hand-written type switch tolerating both shapes — which is the habit, and a
// habit is exactly what the next pack cannot inherit.
//
// Numbers are the one exception, and they are an exception because they have
// an answer: an int, an int64 and a uint32 all come back float64, and Number,
// Int, Int64 and Uint64 in attrs.go read them back for every pack there will
// ever be (#542). A []Rule has no such answer — recovering one means knowing
// Rule, which is the pack's type and must not enter internal/core (rule 5) —
// so the core repairs the number and refuses the shape.
//
// The rule is checked rather than only written down:
// storetest.GoShapes reports every stored value a snapshot cannot give back as
// itself, every pack runs it over its own barrage, and internal/cli's
// TestEveryPackRunsTheSharedBarrage is what makes a fourth pack fail a control
// it never had to remember to write.
type Resource struct {
	ID      string
	Kind    string
	Tenant  Tenant
	State   string
	Created time.Time
	Updated time.Time
	Attrs   map[string]any
	// Runtime holds emulator-side bookkeeping that must survive a restart but
	// must never reach a client: the backing container name, for instance. Packs
	// read it; views never serialize it.
	Runtime map[string]string
}

// New returns a resource stamped at now, with Created and Updated aligned and
// an empty attribute map ready to fill.
//
// It exists because every pack opened every create with the same seven-line
// literal, and the next pack would have copied it again. Only the invariant
// part is taken: the identifier, the kind, the tenant, the state and the two
// clocks — set from one moment, because a resource whose Created and Updated
// disagree at birth reads as already modified. Attrs stays the caller's to
// fill, since its keys are the provider's wire shape and no helper may know
// them (rule 5).
func New(id, kind string, t Tenant, state string, now time.Time) *Resource {
	return &Resource{
		ID:      id,
		Kind:    kind,
		Tenant:  t,
		State:   state,
		Created: now,
		Updated: now,
		Attrs:   map[string]any{},
	}
}

// Clone returns a deep-enough copy: callers mutate the result without racing
// against the store. Nested values inside Attrs are shared, so packs must treat
// them as immutable once stored.
func (r *Resource) Clone() *Resource {
	if r == nil {
		return nil
	}
	out := *r
	out.Attrs = make(map[string]any, len(r.Attrs))
	for k, v := range r.Attrs {
		out.Attrs[k] = v
	}
	if r.Runtime != nil {
		out.Runtime = make(map[string]string, len(r.Runtime))
		for k, v := range r.Runtime {
			out.Runtime[k] = v
		}
	}
	return &out
}

// Matches reports whether the resource belongs to the given tenant. Empty fields
// in the filter match anything, so a pack can list per-zone or across zones.
func (r *Resource) Matches(t Tenant) bool {
	if t.Provider != "" && t.Provider != r.Tenant.Provider {
		return false
	}
	if t.Project != "" && t.Project != r.Tenant.Project {
		return false
	}
	if t.Zone != "" && t.Zone != r.Tenant.Zone {
		return false
	}
	return true
}
