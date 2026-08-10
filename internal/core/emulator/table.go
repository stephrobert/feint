package emulator

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
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
// One extension of this package's own, because net/http cannot express it: a
// trailing "{id}:action" segment. REST dialects that put the verb in the same
// path segment as the identifier (Exoscale writes PUT /instance/{id}:stop) do
// not fit a ServeMux wildcard, which always spans the whole segment. Such a
// route is indexed here under its declared pseudo-pattern, mounted under the
// base pattern ("PUT /instance/{id}"), and resolved by the suffix after the
// first colon of the final segment. The parsing is dialect-neutral — nothing
// here knows which provider writes its verbs that way.
//
// TestTheTableNamesTheOperationAServeMuxWouldRoute holds the plain half;
// TestActionSuffixRoutesAreNamedAndServed holds the extension.
type Table struct {
	mux *http.ServeMux
	at  map[string]Mounted
	// groups indexes what is mounted at one base pattern: the plain route under
	// action "", and one entry per ":action" suffix.
	groups map[string]*actionGroup
}

// actionGroup is everything mounted at one "METHOD /path/{wildcard}" pattern.
type actionGroup struct {
	// wildcard is the name of the trailing path wildcard, so a dispatcher can
	// re-set its value once the action suffix is stripped. Empty when the base
	// pattern does not end in a wildcard, in which case no action route can
	// exist under it either.
	wildcard string
	routes   map[string]Mounted
}

// actionSegment matches a final "{id}:action" path segment: a whole-segment
// wildcard, a colon, and the action name.
var actionSegment = regexp.MustCompile(`^\{([A-Za-z_][A-Za-z0-9_]*)\}:([A-Za-z0-9-]+)$`)

// plainWildcard matches a final "{id}" segment of an ordinary pattern.
var plainWildcard = regexp.MustCompile(`^\{([A-Za-z_][A-Za-z0-9_]*)\}$`)

// splitAction cuts a route path into the pattern net/http can mount and the
// action suffix this package resolves itself. Action is empty for an ordinary
// path.
func splitAction(path string) (base, wildcard, action string) {
	i := strings.LastIndex(path, "/")
	segment := path[i+1:]
	if m := actionSegment.FindStringSubmatch(segment); m != nil {
		return path[:i+1] + "{" + m[1] + "}", m[1], m[2]
	}
	if m := plainWildcard.FindStringSubmatch(segment); m != nil {
		return path, m[1], ""
	}
	return path, "", ""
}

// NewTable indexes every route the packs serve.
//
// It fails when two packs claim the same route, which is the check that used to
// live inline in NewServer: without it net/http panics at a random point in
// start-up, naming the pattern and neither of the packs that wanted it.
func NewTable(packs ...Pack) (*Table, error) {
	t := &Table{mux: http.NewServeMux(), at: make(map[string]Mounted), groups: make(map[string]*actionGroup)}
	for _, p := range packs {
		for _, r := range p.Routes() {
			pattern := r.Method + " " + r.Path
			if owner, dup := t.at[pattern]; dup {
				return nil, fmt.Errorf("route %q claimed by both %q and %q", pattern, owner.Provider, p.Name())
			}
			t.at[pattern] = Mounted{Provider: p.Name(), Route: r}

			base, wildcard, action := splitAction(r.Path)
			basePattern := r.Method + " " + base
			g := t.groups[basePattern]
			if g == nil {
				g = &actionGroup{routes: make(map[string]Mounted)}
				t.groups[basePattern] = g
				// The handler is never called: this mux is consulted with Handler,
				// which resolves and returns rather than serves. Registering the
				// pack's real handler here would give the table a second live copy of
				// every route, and the proxy — which must never answer a request —
				// would be holding one.
				t.mux.HandleFunc(basePattern, func(http.ResponseWriter, *http.Request) {})
			}
			if wildcard != "" {
				g.wildcard = wildcard
			}
			if owner, dup := g.routes[action]; dup {
				return nil, fmt.Errorf("route %q claimed by both %q and %q", pattern, owner.Provider, p.Name())
			}
			g.routes[action] = Mounted{Provider: p.Name(), Route: r}
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
	g, ok := t.groups[pattern]
	if !ok {
		return Mounted{}, false
	}
	m, ok := g.routes[requestAction(g, r.URL.Path)]
	return m, ok
}

// requestAction extracts the action suffix of a request path, for a group that
// serves at least one action. A group with none never parses the path at all,
// so a colon inside an ordinary identifier keeps meaning nothing.
func requestAction(g *actionGroup, path string) string {
	if len(g.routes) == 1 {
		if _, plainOnly := g.routes[""]; plainOnly {
			return ""
		}
	}
	segment := path[strings.LastIndex(path, "/")+1:]
	_, action, _ := strings.Cut(segment, ":")
	return action
}

// All returns every mounted route, keyed by the "METHOD /path" pattern the pack
// declared — the pseudo-pattern, for a route with an action suffix.
func (t *Table) All() map[string]Mounted {
	out := make(map[string]Mounted, len(t.at))
	for pattern, m := range t.at {
		out[pattern] = m
	}
	return out
}
