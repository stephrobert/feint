package cli

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// `feint status` reports the number a real client actually drove.
//
// It could not. `internal/cli` declared its own shape for /_feint/conformance —
// `proven_by_a_client`, a key that exists in no other file of this repository,
// and `untouched` as an object where the wire carries an array. The decode
// failed with "cannot unmarshal array into Go struct field", both callers fell
// back to empty, and the "driven by a client" column printed 0 whatever a client
// had driven. The header comment of status.go promised exactly that number.
//
// The test drives provenPerProvider, the function status.go calls, against a
// server that has just answered a real request. Asserting on the type instead
// would prove nothing now that the CLI and the server share one: both sides
// would move together under any rename, and a test that cannot fail is not a
// test. What can still break is the path — a second shape declared somewhere,
// a count read from a key nobody emits — and that is what this drives.
//
// It fails on the code before the fix.
func TestStatusCountsWhatAClientDrove(t *testing.T) {
	env := emulator.DefaultEnv()
	packs, err := packsFor(env)
	if err != nil {
		t.Fatalf("build the packs: %v", err)
	}
	srv, err := emulator.NewServer(env, packs...)
	if err != nil {
		t.Fatalf("build the server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	addr := ts.Listener.Addr().String()

	providers := make([]string, 0, len(srv.Packs()))
	for _, p := range srv.Packs() {
		providers = append(providers, p.Name())
	}

	// Nothing has been driven yet: every provider sits at zero. Without this
	// half, a function that returned a constant would pass.
	for _, p := range provenPerProvider(addr, providers) {
		if p.Proven != 0 {
			t.Fatalf("%s reports %d proven before any client call", p.Provider, p.Proven)
		}
		if p.Routes == 0 {
			t.Fatalf("%s mounts no route, so this test measures nothing", p.Provider)
		}
	}

	// One real call, on a route the pack mounts.
	resp, err := http.Get(ts.URL + "/instance/v1/zones/fr-par-1/servers") //nolint:noctx // a test server
	if err != nil {
		t.Fatalf("drive the emulator: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the route answered %d, so nothing was driven", resp.StatusCode)
	}

	total := 0
	for _, p := range provenPerProvider(addr, providers) {
		total += p.Proven
	}
	if total == 0 {
		t.Fatal("a client drove one route and status still reports 0 proven: the count does not reach the wire")
	}
}
