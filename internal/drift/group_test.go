package drift_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/drift"
)

// The upstream grouping exists because the product view lied by layout: one
// `osc` row of 236 operations next to six Scaleway rows read as a difference in
// depth when it was only a difference in SDK generation. These tests hold the
// three legs of the fix: the contract scan carries the group, the SDK scan can
// borrow it, and the artefact publishes it.

// flatWithGroups is the shape of a provider whose SDK layout is one product but
// whose document files operations under groups — Exoscale exactly.
func flatWithGroups() []drift.Operation {
	return []drift.Operation{
		{Name: "exoscale/v2.list-instances", Product: "exoscale", Group: "compute"},
		{Name: "exoscale/v2.start-instance", Product: "exoscale", Group: "compute"},
		{Name: "exoscale/v2.create-dbaas-service", Product: "exoscale", Group: "dbaas"},
		// Untagged upstream: no group, and it must count under the product
		// rather than under a name this project made up.
		{Name: "exoscale/v2.get-impact-report", Product: "exoscale"},
	}
}

func TestGroupsCountByUpstreamGroupAndFallBackToTheProduct(t *testing.T) {
	rep := drift.Compare("exoscale", flatWithGroups(),
		[]string{"exoscale/v2.list-instances"},
		map[string]string{"exoscale/v2.create-dbaas-service": "managed databases are refused"},
	)

	groups := rep.Groups()
	byName := make(map[string]drift.GroupCount, len(groups))
	total := 0
	for _, g := range groups {
		byName[g.Group] = g
		total += g.Total
	}

	if got := byName["compute"]; got.Total != 2 || got.Implemented != 1 || got.Unknown != 1 {
		t.Fatalf("unexpected compute counts: %+v", got)
	}
	if got := byName["dbaas"]; got.Total != 1 || got.Declined != 1 {
		t.Fatalf("the block refusal must stay visible under its own group: %+v", got)
	}
	// The fallback: an operation the upstream files nowhere counts under the
	// product, so the group sums always reconcile with the report totals.
	if got := byName["exoscale"]; got.Total != 1 || got.Unknown != 1 {
		t.Fatalf("expected the untagged operation under its product: %+v", got)
	}
	if total != rep.Total {
		t.Fatalf("groups sum to %d, the report says %d: a grouping that loses operations", total, rep.Total)
	}
}

func TestTheArtefactCarriesTheGroups(t *testing.T) {
	rep := drift.Compare("exoscale", flatWithGroups(),
		[]string{"exoscale/v2.list-instances"},
		map[string]string{"exoscale/v2.create-dbaas-service": "managed databases are refused"},
	)

	var buf bytes.Buffer
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatalf("write json: %v", err)
	}
	file, err := drift.LoadCoverage(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(file.Groups) == 0 {
		t.Fatalf("the artefact carries no groups; the README renderer would fall back to the product fourre-tout")
	}
	names := make([]string, 0, len(file.Groups))
	for _, g := range file.Groups {
		names = append(names, g.Group)
	}
	if strings.Join(names, ",") != "compute,dbaas,exoscale" {
		t.Fatalf("expected sorted groups compute,dbaas,exoscale, got %s", strings.Join(names, ","))
	}

	// The per-entry group travels too: the route reference files its sections
	// from the entries, not from the summary.
	var raw struct {
		Entries []map[string]any `json:"entries"`
	}
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	seen := false
	for _, e := range raw.Entries {
		if e["operation"] == "exoscale/v2.list-instances" {
			seen = true
			if e["group"] != "compute" {
				t.Fatalf("entry lost its group: %v", e)
			}
		}
		if e["operation"] == "exoscale/v2.get-impact-report" {
			if _, present := e["group"]; present {
				t.Fatalf("an untagged operation must not be given a group on the wire: %v", e)
			}
		}
	}
	if !seen {
		t.Fatalf("expected entry not found in the artefact")
	}
}

// groupedContract is a minimal contract artefact whose document files ReadVms
// under Vm and leaves ListSomething ungrouped, the way the extractor writes
// contracts/outscale.json from their api.yaml.
const groupedContract = `{
  "provider": "outscale",
  "apiVersion": "1.35.3",
  "pathPrefix": "/api/v1",
  "closedPolicy": "declared",
  "operations": {
    "ReadVms": {"path": "/ReadVms", "method": "POST", "group": "Vm"},
    "ListSomething": {"path": "/ListSomething", "method": "POST"}
  },
  "schemas": {"Vm": {"closed": true}}
}`

func TestScanContractCarriesTheUpstreamGroup(t *testing.T) {
	doc, err := contract.Read(strings.NewReader(groupedContract))
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	ops, err := drift.ScanContract(doc)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	byName := make(map[string]drift.Operation, len(ops))
	for _, op := range ops {
		byName[op.Name] = op
	}
	if got := byName["outscale/api/v1.ReadVms"].Group; got != "Vm" {
		t.Fatalf("expected the document's group Vm, got %q", got)
	}
	if got := byName["outscale/api/v1.ListSomething"].Group; got != "" {
		t.Fatalf("an ungrouped operation must stay ungrouped, got %q", got)
	}
}

// The SDK scan cannot know the groups — oapi-codegen flattens every operation
// onto one Client and drops the document's tags — so Regroup joins what the SDK
// found with where the document files it. Named by Regroup's own comment: this
// is the test that fails if the join stops filling groups or starts inventing
// them.
func TestRegroupTakesGroupsFromTheContract(t *testing.T) {
	doc, err := contract.Read(strings.NewReader(groupedContract))
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	ops := []drift.Operation{
		{Name: "osc/Client.ReadVms", Product: "osc"},
		// A service the document does not cover — oks ships no OpenAPI document
		// here — must keep its empty group and fall back to its product, not be
		// filed under a name nobody upstream wrote.
		{Name: "oks/Client.CreateCluster", Product: "oks"},
	}
	drift.Regroup(ops, doc)

	if ops[0].Group != "Vm" {
		t.Fatalf("expected ReadVms filed under Vm, got %q", ops[0].Group)
	}
	if ops[1].Group != "" {
		t.Fatalf("an operation the document does not know must stay ungrouped, got %q", ops[1].Group)
	}
}
