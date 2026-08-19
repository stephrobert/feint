package emulator_test

import (
	"path/filepath"
	"testing"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/exoscale"
	"github.com/stephrobert/feint/internal/providers/outscale"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// Every route of every pack that has a contract is checked against it: the
// operation exists, the method matches, and the path is the one the API serves.
//
// The drift report joins on the operation name alone. It never looks at the path
// or the method, which is what an SDK actually addresses, so a route with the
// right name at the wrong path was invisible to every mechanism here. Both packs
// that now have a contract had their names wrong when one was first run against
// them — seven of seven for Outscale, eight of eight for Exoscale — so this is
// not a hypothetical failure mode.
func TestEveryRouteMatchesItsContract(t *testing.T) {
	env := emulator.DefaultEnv()
	packs := map[string]emulator.Pack{
		scaleway.Name: scaleway.New(env),
		outscale.Name: outscale.New(env),
		exoscale.Name: exoscale.New(env),
	}

	for provider, pack := range packs {
		doc, err := contract.Load(filepath.Join("..", "..", "..", "contracts", provider+".json"))
		if err != nil {
			t.Fatalf("%s: load contract: %v", provider, err)
		}

		mounted := make([]contract.MountedRoute, 0, len(pack.Routes()))
		for _, r := range pack.Routes() {
			mounted = append(mounted, contract.MountedRoute{
				Method: r.Method, Path: r.Path, Operation: r.Operation,
				Legacy: r.Legacy != "",
			})
		}

		for _, m := range doc.CheckRoutes(mounted) {
			t.Errorf("%s: %s", provider, m)
		}
	}
}

// What each contract can and cannot check differs, and the artefact says which.
// Left implicit, a reader would assume all three carry the same weight, and the
// weakest of them is the pack with the most surface.
func TestWhatEachContractGuaranteesIsRecorded(t *testing.T) {
	want := map[string]string{
		// Outscale closed 643 of their 650 schemas themselves, so an unknown
		// field is a violation they wrote down.
		outscale.Name: "declared",
		// Exoscale closed 7 of 299. Refusing an unknown field there is this
		// project's decision, defensible because a field their document does not
		// define is one no client reads.
		exoscale.Name: "assumed",
		// Scaleway is neither, and deliberately so. Their document is a lossier
		// projection of the same internal IDL than their Go SDK: it omits
		// total_count from every list response, which the SDK declares and the
		// wire carries. Assuming its schemas closed would report a violation on
		// every list the emulator answers correctly, so the shape check stays
		// open there and the contract is used for operations and paths only.
		scaleway.Name: "declared",
	}

	for provider, policy := range want {
		doc, err := contract.Load(filepath.Join("..", "..", "..", "contracts", provider+".json"))
		if err != nil {
			t.Errorf("%s: %v", provider, err)
			continue
		}
		if doc.ClosedPolicy != policy {
			t.Errorf("%s: closed policy is %q, want %q", provider, doc.ClosedPolicy, policy)
		}
		closed := 0
		for _, s := range doc.Schemas {
			if s.Closed {
				closed++
			}
		}
		if provider == scaleway.Name && closed != 0 {
			t.Errorf("scaleway: %d closed schemas; its document omits real fields, "+
				"so closing them would accuse correct responses", closed)
		}
		if provider != scaleway.Name && closed == 0 {
			t.Errorf("%s: no closed schema, so no unknown field would ever be reported", provider)
		}
	}
}
