package emulator_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/exoscale"
	"github.com/stephrobert/feint/internal/providers/outscale"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// wildcard matches a net/http 1.22 path parameter, "{zone}" or "{id}".
var wildcard = regexp.MustCompile(`\{[^}/]+\}`)

// concrete turns a route pattern into a path a client could actually request.
//
// The value put in each wildcard is deliberately not a valid identifier for
// anything: the table resolves an address, and it must not need the resource to
// exist. If a substitution ever mattered, that would itself be the defect.
func concrete(pattern string) string {
	return wildcard.ReplaceAllString(pattern, "x")
}

// The table names the operation for every address a pack serves.
//
// It is driven from the packs' own route lists rather than from a fixture, so a
// pack that adds a route adds a case here, and a pack that moves a path moves
// its case with it. That is the whole point of the type: `feint proxy` names an
// exchange it only watches, and the answer has to be the one this emulator would
// have given.
//
// The failing half is the one worth stating: with Lookup comparing prefixes
// instead of asking the mux, /instance/v1/zones/x/servers/x resolves to
// ListServers rather than GetServer, because a prefix match cannot tell a
// collection from a member.
func TestTheTableNamesTheOperationAServeMuxWouldRoute(t *testing.T) {
	env := emulator.DefaultEnv()
	packs := []emulator.Pack{scaleway.New(env), outscale.New(env), exoscale.New(env)}

	table, err := emulator.NewTable(packs...)
	if err != nil {
		t.Fatalf("build the table: %v", err)
	}

	var routes int
	for _, p := range packs {
		for _, r := range p.Routes() {
			routes++
			path := concrete(r.Path)
			req := httptest.NewRequest(r.Method, path, nil)
			m, ok := table.Lookup(req)
			if !ok {
				t.Errorf("%s %s: the table names no operation, so a transcript would report a route no pack claims",
					r.Method, path)
				continue
			}
			if m.Route.Operation != r.Operation {
				t.Errorf("%s %s: the table names %q, the pack serves %q",
					r.Method, path, m.Route.Operation, r.Operation)
			}
			if m.Provider != p.Name() {
				t.Errorf("%s %s: the table credits %q, the route belongs to %q",
					r.Method, path, m.Provider, p.Name())
			}
		}
	}
	if routes == 0 {
		t.Fatal("no route to check, so this test measures nothing")
	}

	// The refusing half. An address no pack serves must come back unknown: that
	// is what makes an empty operation in a transcript mean "a real client walked
	// a route nobody claims" rather than "the table gave up".
	for _, unclaimed := range []struct{ method, path string }{
		{http.MethodGet, "/no/such/api"},
		{http.MethodGet, "/instance/v1/zones/fr-par-1/nonexistent"},
		// The method is part of the address. A pack serving GET on a path does
		// not serve TRACE on it, and a table that ignored the verb would name an
		// operation for a call the emulator answers 405 to.
		{http.MethodTrace, "/instance/v1/zones/fr-par-1/servers"},
	} {
		if m, ok := table.Lookup(httptest.NewRequest(unclaimed.method, unclaimed.path, nil)); ok {
			t.Errorf("%s %s: the table names %q for an address no pack serves",
				unclaimed.method, unclaimed.path, m.Route.Operation)
		}
	}
}

// The table refuses a route two packs claim, which is what keeps net/http from
// panicking mid-mount with a message naming neither pack.
func TestTheTableRefusesARouteTwoPacksClaim(t *testing.T) {
	route := emulator.Route{Method: http.MethodGet, Path: "/collision", Operation: "stub/v1.Collide", Handler: noop}
	_, err := emulator.NewTable(
		stubPack{name: "one", routes: []emulator.Route{route}},
		stubPack{name: "two", routes: []emulator.Route{route}},
	)
	if err == nil {
		t.Fatal("two packs claimed the same route and the table accepted both")
	}
	// Both names, because the message exists to say who to talk to. The version
	// this replaced reported the pattern and one pack.
	for _, want := range []string{"one", "two", "/collision"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q, so it cannot be acted on: %v", want, err)
		}
	}
}

// What naming one exchange costs.
//
// `feint proxy` calls this once per request it forwards, so the number belongs
// beside the recorder's own: the alternative design — a hand-written prefix table
// in the proxy — would be faster and would be a copy of the packs' routes, which
// is the trade this type refuses to make. Knowing the price makes the refusal a
// decision rather than an assumption.
func BenchmarkTableLookup(b *testing.B) {
	env := emulator.DefaultEnv()
	table, err := emulator.NewTable(scaleway.New(env), outscale.New(env), exoscale.New(env))
	if err != nil {
		b.Fatalf("build the table: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/instance/v1/zones/fr-par-1/servers/11111111-1111-1111-1111-111111111111", nil)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, ok := table.Lookup(req); !ok {
			b.Fatal("the table named nothing for a route a pack serves")
		}
	}
}
