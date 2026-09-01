package drift_test

import (
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/drift"
)

func docWith(names ...string) *contract.Doc {
	ops := make(map[string]contract.Operation, len(names))
	for _, n := range names {
		ops[n] = contract.Operation{}
	}
	return &contract.Doc{Provider: "scaleway", Operations: ops}
}

// The two naming conventions meet, or the gate reports forty-two differences
// nobody caused and gets switched off in a week.
//
// The SDK writes instance/v1/API.CreateIP where the document writes
// instance/v1.CreateIp, and account/v3/ContractAPI.X where the document writes
// account/v3.X. Neither is wrong; they are two spellings of one operation.
func TestContractOnlyFoldsTheTwoNamingConventions(t *testing.T) {
	upstream := []drift.Operation{
		{Name: "instance/v1/API.CreateIP"},
		{Name: "account/v3/ProjectAPI.CreateProject"},
	}
	if only := drift.ContractOnly(upstream, docWith("instance/v1.CreateIp", "account/v3.CreateProject")); len(only) != 0 {
		t.Errorf("a naming difference was reported as a missing operation: %v", only)
	}
}

// And the other half, or a comparison that folds everything reports nothing and
// looks exactly like a clean inventory.
func TestContractOnlyReportsWhatTheScanDoesNotHave(t *testing.T) {
	upstream := []drift.Operation{{Name: "instance/v1/API.UpdateVolume"}}
	only := drift.ContractOnly(upstream, docWith("instance/v1.UpdateVolume", "instance/v1.SetVolume"))
	if len(only) != 1 || only[0] != "instance/v1.SetVolume" {
		t.Fatalf("want the operation the SDK never wrapped, got %v", only)
	}
}

// A document that names no product matches on the method alone, which is how
// Outscale (ReadVms against osc/Client.ReadVms) and Exoscale (list-instances
// against exoscale/v2.list-instances) come out clean rather than wholly missing.
func TestContractOnlyMatchesADocumentThatNamesNoProduct(t *testing.T) {
	upstream := []drift.Operation{{Name: "osc/Client.ReadVms"}, {Name: "exoscale/v2.list-instances"}}
	if only := drift.ContractOnly(upstream, docWith("ReadVms", "list-instances")); len(only) != 0 {
		t.Errorf("a document naming no product was reported as entirely missing: %v", only)
	}
}

// The property the count rests on, stated as a test rather than as a promise:
// the folding can only merge, so what this returns is a lower bound. An
// operation it reports is one no reading of the names could reconcile.
func TestContractOnlyOnlyEverMerges(t *testing.T) {
	// Same method under a different product: merged, so not reported. That is
	// the bound being lower, and it is the price of never crying wolf.
	upstream := []drift.Operation{{Name: "instance/v1/API.CreateImage"}}
	if only := drift.ContractOnly(upstream, docWith("block/v1.CreateImage")); len(only) != 0 {
		t.Errorf("the folding split two spellings apart instead of merging them: %v", only)
	}
}

// An empty document reports nothing rather than everything: a provider whose
// contract failed to load must not read as a provider whose whole surface is
// missing.
func TestContractOnlyIsSilentWithoutADocument(t *testing.T) {
	if only := drift.ContractOnly([]drift.Operation{{Name: "a/v1/API.X"}}, nil); only != nil {
		t.Errorf("a nil document produced findings: %v", only)
	}
}

// The real files, because a unit test over fixtures proves the function and not
// the inventory. Six on Scaleway is the measurement #622 published; zero on the
// two others is what makes it a Scaleway-shaped problem rather than a general
// one — their SDK is generated from the same document the contract comes from.
func TestTheCommittedInventoriesAgreeExceptWhereTheyAreDocumented(t *testing.T) {
	for _, c := range []struct {
		provider string
		want     int
	}{{"outscale", 0}, {"exoscale", 0}} {
		doc, err := contract.Load("../../contracts/" + c.provider + ".json")
		if err != nil {
			t.Fatalf("%s: %v", c.provider, err)
		}
		upstream, err := drift.ScanContract(doc)
		if c.provider == "outscale" {
			upstream, err = drift.ScanOutscaleSDK("../../.upstream/osc-sdk-go")
			if err != nil {
				t.Skipf("no Outscale SDK checkout: %v", err)
			}
		}
		if err != nil {
			t.Fatalf("%s: %v", c.provider, err)
		}
		if only := drift.ContractOnly(upstream, doc); len(only) != c.want {
			t.Errorf("%s: %d contract-only operation(s), want %d: %s",
				c.provider, len(only), c.want, strings.Join(only, ", "))
		}
	}
}
