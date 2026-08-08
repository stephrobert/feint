package emulator

import (
	"fmt"
	"net/http"
)

// Mounted is one route and the pack that owns it.
type Mounted struct {
	Provider string
	Route    Route
}

// Table answers one question: which upstream operation does this request
// address?
//
// It exists because two readers need that answer from opposite sides. NewServer
// asks it while mounting handlers, from inside the process that will answer; and
// `feint proxy` (internal/proxy, X-2) asks it about an exchange it only watches
// go past, between a real client and a real cloud it does not serve.
//
// A second table written by hand in the proxy would be a copy of the packs' own
// route lists, and copies diverge silently. The failure would be quiet and
// misleading rather than loud: the day a pack moved a path, the proxy would
// report an empty operation for that exchange — and an empty operation already
// means something here, "a real client walked a route no pack claims", which is
// a finding this repository acts on. A copy would manufacture that finding.
//
// The matching is net/http's own rather than a prefix comparison of this
// package's invention. Route.Path is a 1.22 pattern
// ("/instance/v1/zones/{zone}/servers"), so ServeMux already resolves a concrete
// path to the pattern serving it, wildcards and precedence included; Handler
// reports which pattern matched, and that is the key back to the route.
//
// TestTheTableNamesTheOperationAServeMuxWouldRoute fails without this.
type Table struct {
	mux *http.ServeMux
	at  map[string]Mounted
}

// NewTable indexes every route the packs serve.
//
// It fails when two packs claim the same route, which is the check that used to
// live inline in NewServer: without it net/http panics at a random point in
// start-up, naming the pattern and neither of the packs that wanted it.
func NewTable(packs ...Pack) (*Table, error) {
	t := &Table{mux: http.NewServeMux(), at: make(map[string]Mounted)}
	for _, p := range packs {
		for _, r := range p.Routes() {
			pattern := r.Method + " " + r.Path
			if owner, dup := t.at[pattern]; dup {
				return nil, fmt.Errorf("route %q claimed by both %q and %q", pattern, owner.Provider, p.Name())
			}
			t.at[pattern] = Mounted{Provider: p.Name(), Route: r}
			// The handler is never called: this mux is consulted with Handler,
			// which resolves and returns rather than serves. Registering the
			// pack's real handler here would give the table a second live copy of
			// every route, and the proxy — which must never answer a request —
			// would be holding one.
			t.mux.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
		}
	}
	return t, nil
}

// Lookup names the route mounted at a request's address.
//
// The second return is false when nothing is mounted there, which the caller
// must not confuse with a route whose Operation is empty: rule 2 forbids the
// latter and TestEveryRouteDeclaresAnOperation enforces it, so in practice
// "false" is the only way an operation can be unknown.
func (t *Table) Lookup(r *http.Request) (Mounted, bool) {
	_, pattern := t.mux.Handler(r)
	m, ok := t.at[pattern]
	return m, ok
}

// All returns every mounted route, keyed by the "METHOD /path" pattern it is
// mounted under.
func (t *Table) All() map[string]Mounted {
	out := make(map[string]Mounted, len(t.at))
	for pattern, m := range t.at {
		out[pattern] = m
	}
	return out
}
