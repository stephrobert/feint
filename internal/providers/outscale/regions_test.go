package outscale_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/outscale"
)

// The region is a datum, not a constant (#290). At Outscale every region is
// served by the same API — a region is a property of the deployment, i.e.
// which endpoint a client points at — so an emulator with one endpoint
// chooses its region at construction instead of hardwiring eu-west-2. What
// #269 paid for must survive the choice: the catalogue and every write path
// answer the same thing, whichever region is in force. Every test here runs
// in a NON-default region on purpose; the default-region suite next door
// (subregions_test.go) is exactly the population that let the constant ship,
// because a constant and a datum are indistinguishable from eu-west-2.

// newServerInRegion mounts the pack in a named region, the way the CLI does
// when FEINT_OUTSCALE_REGION is set.
func newServerInRegion(t *testing.T, region string) *httptest.Server {
	t.Helper()
	env := emulator.DefaultEnv()
	pack, err := outscale.NewInRegion(env, region)
	if err != nil {
		t.Fatalf("build the pack in %s: %v", region, err)
	}
	srv, err := emulator.NewServer(env, pack)
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// A pack built for cloudgouv-eu-west-1 — the SecNumCloud region, the audience
// this project exists for, and the region the ztiac stack (#290's witness)
// targets — agrees with itself everywhere: the region read, the subregion
// catalogue, and every write path that validates a SubregionName. The zones
// and their LocationCodes are Outscale's published ones (docs.outscale.com,
// "About Regions and Subregions", fetched 2026-08-18: cloudgouv-eu-west-1a/b/c
// on SEC1/SEC2/SEC3).
func TestANonDefaultRegionAgreesWithItself(t *testing.T) {
	ts := newServerInRegion(t, "cloudgouv-eu-west-1")
	doc := contractDoc(t)

	regions := call(t, ts, doc, "ReadRegions", `{}`)
	rows, _ := regions["Regions"].([]any)
	if len(rows) != 1 {
		t.Fatalf("ReadRegions answered %d regions, want 1: %v", len(rows), regions)
	}
	region, _ := rows[0].(map[string]any)
	if got, _ := region["RegionName"].(string); got != "cloudgouv-eu-west-1" {
		t.Errorf("ReadRegions names %q, want cloudgouv-eu-west-1", got)
	}

	// The catalogue declares the region's own subregions — and never a union
	// across regions, which would be a catalogue lying in the other direction:
	// zones it declared and every write path refused.
	subregions := call(t, ts, doc, "ReadSubregions", `{}`)
	declared, _ := subregions["Subregions"].([]any)
	want := map[string]string{
		"cloudgouv-eu-west-1a": "SEC1",
		"cloudgouv-eu-west-1b": "SEC2",
		"cloudgouv-eu-west-1c": "SEC3",
	}
	if len(declared) != len(want) {
		t.Fatalf("ReadSubregions declares %d Subregions, want %d: %v", len(declared), len(want), subregions)
	}
	for _, raw := range declared {
		row, _ := raw.(map[string]any)
		name, _ := row["SubregionName"].(string)
		code, wanted := want[name]
		if !wanted {
			t.Errorf("ReadSubregions declares %q, a zone cloudgouv-eu-west-1 does not publish", name)
			continue
		}
		if got, _ := row["LocationCode"].(string); got != code {
			t.Errorf("%s: LocationCode = %q, want %q", name, got, code)
		}
		if got, _ := row["RegionName"].(string); got != "cloudgouv-eu-west-1" {
			t.Errorf("%s: RegionName = %q, want cloudgouv-eu-west-1", name, got)
		}
	}

	// Every write path that validates a SubregionName accepts what the
	// catalogue above declared, and what it stores reads back verbatim — the
	// #269 invariant, in the region actually in force.
	_, netOut := post(t, ts, "CreateNet", `{"IpRange":"10.66.0.0/16"}`)
	n, _ := netOut["Net"].(map[string]any)
	netID, _ := n["NetId"].(string)

	subnet := call(t, ts, doc, "CreateSubnet",
		`{"NetId":"`+netID+`","IpRange":"10.66.1.0/24","SubregionName":"cloudgouv-eu-west-1c"}`)
	created, _ := subnet["Subnet"].(map[string]any)
	if got, _ := created["SubregionName"].(string); got != "cloudgouv-eu-west-1c" {
		t.Errorf("the created Subnet reads back in %q, want cloudgouv-eu-west-1c", got)
	}

	volume := call(t, ts, doc, "CreateVolume", `{"SubregionName":"cloudgouv-eu-west-1b","Size":7}`)
	vol, _ := volume["Volume"].(map[string]any)
	if got, _ := vol["SubregionName"].(string); got != "cloudgouv-eu-west-1b" {
		t.Errorf("the created Volume reads back in %q, want cloudgouv-eu-west-1b", got)
	}

	vm := call(t, ts, doc, "CreateVms",
		`{"ImageId":"ami-00000001","BootOnCreation":false,`+
			`"Placement":{"SubregionName":"cloudgouv-eu-west-1c"}}`)
	assertPlacement(t, vm, "cloudgouv-eu-west-1c", "default", "a Vm placed in the region's third zone")
	id := firstVMID(t, vm)
	read := call(t, ts, doc, "ReadVms", `{"Filters":{"VmIds":["`+id+`"]}}`)
	assertPlacement(t, read, "cloudgouv-eu-west-1c", "default", "ReadVms in cloudgouv-eu-west-1")

	// A Vm with nothing said lands in the region's own first zone, never in
	// the default region's.
	silent := call(t, ts, doc, "CreateVms", `{"ImageId":"ami-00000001","BootOnCreation":false}`)
	assertPlacement(t, silent, "cloudgouv-eu-west-1a", "default", "a Vm with no Placement")

	// And the default region's zones are refused here, by every door: this
	// deployment is cloudgouv-eu-west-1, and eu-west-2a is exactly as foreign
	// to it as cloudgouv-eu-west-1a was to eu-west-2 in #269.
	for _, probe := range []struct{ action, body string }{
		{"CreateSubnet", `{"NetId":"` + netID + `","IpRange":"10.66.2.0/24","SubregionName":"eu-west-2a"}`},
		{"CreateVolume", `{"SubregionName":"eu-west-2a","Size":7}`},
		{"CreateVms", `{"ImageId":"ami-00000001","BootOnCreation":false,"Placement":{"SubregionName":"eu-west-2a"}}`},
	} {
		status, refused := post(t, ts, probe.action, probe.body)
		if status != http.StatusBadRequest {
			t.Errorf("%s answered %d for the default region's zone in cloudgouv-eu-west-1: %v",
				probe.action, status, refused)
		}
	}
}

// A region Outscale does not publish is refused at construction, naming what
// would have been accepted. Refusing beats defaulting: an emulator that
// answered eu-west-2 to an operator who asked for something else would be
// #268's lie moved to startup.
func TestARegionOutscaleDoesNotPublishIsRefused(t *testing.T) {
	env := emulator.DefaultEnv()
	if _, err := outscale.NewInRegion(env, "eu-mars-1"); err == nil {
		t.Fatal("NewInRegion accepted eu-mars-1, a region Outscale does not publish")
	} else {
		if !strings.Contains(err.Error(), "eu-mars-1") {
			t.Errorf("the refusal does not name the region asked for: %v", err)
		}
		if !strings.Contains(err.Error(), "cloudgouv-eu-west-1") {
			t.Errorf("the refusal does not name what would have been accepted: %v", err)
		}
	}
}
