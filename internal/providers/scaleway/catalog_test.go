package scaleway_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

const serverTypesURL = "/instance/v1/zones/fr-par-1/products/servers"

// The catalogue is a whitelist, not scenery: the Terraform provider validates
// a server's type against /products/servers before it creates anything, so a
// missing entry fails a stack at plan (#279, measured on two of the five
// surveyed Scaleway stacks). These tests hold what the fix decided.

// The type the kiwinet-infra-cloud survey witness names is served, with the
// values the real cloud publishes for it — captured from
// GET /instance/v1/zones/fr-par-1/products/servers on 2026-08-19, the excerpt
// embedded as catalog_servers.json.
func TestTheSurveyedTypeIsServedWithItsPublishedValues(t *testing.T) {
	ts := newTestServer(t)
	status, body := do(t, ts, "GET", serverTypesURL, "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d (%v)", status, body)
	}
	types, _ := body["servers"].(map[string]any)
	entry, ok := types["STARDUST1-S"].(map[string]any)
	if !ok {
		t.Fatalf("STARDUST1-S is not in the catalogue: %v", sortedKeys(types))
	}
	published := map[string]any{
		"arch":         "x86_64",
		"ncpus":        float64(1),
		"ram":          float64(1073741824),
		"hourly_price": 0.0006,
		"gpu":          float64(0),
	}
	for field, want := range published {
		if got := entry[field]; got != want {
			t.Errorf("STARDUST1-S %s = %v, want the published %v", field, got, want)
		}
	}
	vc, _ := entry["volumes_constraint"].(map[string]any)
	if vc["max_size"] != float64(10_000_000_000) {
		t.Errorf("volumes_constraint.max_size = %v, want the published 10000000000", vc["max_size"])
	}
}

// Two fields of every served type keep `scw instance server create` alive, and
// both are deviations or near-deviations from the published table, so they are
// pinned here rather than trusted to a comment:
//
//   - volumes_constraint.min_size must be 0. The CLI sums the LOCAL volumes of
//     a create request against it and refuses anything below the minimum with
//     "total local volume size must be between ...", and this catalogue
//     attaches no local volume. Every type in the embedded excerpt publishes 0
//     today; a future type pasted with a non-zero real minimum fails here,
//     loudly, instead of as a CLI refusal three tools away.
//   - per_volume_constraint must be empty. The real table states l_ssd bounds
//     on several families; served here they would enter the client's size
//     arithmetic with nothing behind them. DeclinedFields() declares the gap
//     to the shapes gate; this test proves the load-time strip does it.
func TestCatalogueKeepsTheLocalVolumeTrapDisarmed(t *testing.T) {
	ts := newTestServer(t)
	status, body := do(t, ts, "GET", serverTypesURL, "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	types, _ := body["servers"].(map[string]any)
	if len(types) == 0 {
		t.Fatal("the catalogue is empty")
	}
	for name, raw := range types {
		entry, _ := raw.(map[string]any)
		vc, _ := entry["volumes_constraint"].(map[string]any)
		if got := vc["min_size"]; got != float64(0) {
			t.Errorf("%s volumes_constraint.min_size = %v, want 0: the CLI sums local volumes against it and this catalogue attaches none", name, got)
		}
		pvc, ok := entry["per_volume_constraint"].(map[string]any)
		if !ok || len(pvc) != 0 {
			t.Errorf("%s per_volume_constraint = %v, want an empty object: a bound for local volumes never attached", name, raw)
		}
	}
}

// COPARM1-* is refused, not forgotten. The terraform-talos survey witness
// defaults to COPARM1-2C-8G, and the family is absent from all nine zones of
// the real catalogue — measured 2026-08-19 on
// GET /instance/v1/zones/{zone}/products/servers, every page, while genuinely
// end-of-service families (START1, VC1, X64) are still listed with
// end_of_service:true. Scaleway withdrew it. Resurrecting it here would let a
// plan pass that production refuses — the #268 class of lie, in the direction
// that hurts: acceptance where the real cloud rejects.
func TestTheRetiredArmFamilyStaysRetired(t *testing.T) {
	ts := newTestServer(t)
	status, body := do(t, ts, "GET", serverTypesURL+"?per_page=100", "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	types, _ := body["servers"].(map[string]any)
	for name := range types {
		if strings.HasPrefix(name, "COPARM1-") {
			t.Errorf("%s is served, but the real cloud withdrew the COPARM1 family; an entry here accepts what production refuses", name)
		}
	}
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The bound the catalogue does not publish is declined where the replay looks
// for it, and not only where the shapes gate does.
//
// Two gates read DeclinedFields() and each joins on its own spelling: `feint
// shapes --check` on the catalogue key ("GET /instance/v1/zones/fr-par-1/
// products/servers"), the live field gate and the corpus replay on the mounted
// operation name ("instance/v1/API.ListServersTypes"). The decision was spelled
// once, for the first, so the replay met no refusal and graded the nine bounds
// the real cloud publishes as nine divergences — a third of what
// corpus/accepted.json carried until #355.
//
// A decline that matches nothing is refused elsewhere as stale; a decline that
// exists in only one dialect is the failure this holds, and nothing else can:
// both spellings are strings, and a typo in either is invisible until a gate
// stops excusing.
func TestTheCatalogueBoundIsDeclinedWhereTheReplayJoins(t *testing.T) {
	const (
		path      = "servers.STARDUST1-S.per_volume_constraint.l_ssd"
		operation = "instance/v1/API.ListServersTypes"
		catalogue = "GET /instance/v1/zones/fr-par-1/products/servers"
	)
	declines := emulator.FieldDeclinesOf(scaleway.New(emulator.DefaultEnv()))
	for _, spelling := range []string{operation, catalogue} {
		found := false
		for _, d := range declines {
			if d.Matches(spelling, path) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no field decline covers %s under %q: the gate that joins on that spelling "+
				"reads the omission as a divergence", path, spelling)
		}
	}
}
