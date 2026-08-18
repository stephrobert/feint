package outscale_test

import (
	"net/http"
	"testing"
)

// The subregion is a datum, not a constant. #268 and #269 were one weakness
// seen through two doors: CreateVms accepted Placement.SubregionName and every
// read answered the pack's constant, while ReadSubregions declared one zone
// and the write paths accepted any string. Every test here uses a NON-default
// zone on purpose — the whole family shipped because the suite only ever
// created resources in the default one, so the constant and the datum were
// indistinguishable.

// placementOf digs the Placement out of a Vms answer.
func placementOf(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	vms, _ := out["Vms"].([]any)
	if len(vms) == 0 {
		t.Fatalf("no Vms in the answer: %v", out)
	}
	vm, _ := vms[0].(map[string]any)
	placement, ok := vm["Placement"].(map[string]any)
	if !ok {
		t.Fatalf("the Vm carries no Placement: %v", vm)
	}
	return placement
}

func assertPlacement(t *testing.T, out map[string]any, subregion, tenancy, when string) {
	t.Helper()
	placement := placementOf(t, out)
	if got, _ := placement["SubregionName"].(string); got != subregion {
		t.Errorf("%s: SubregionName = %q, want %q", when, got, subregion)
	}
	if got, _ := placement["Tenancy"].(string); got != tenancy {
		t.Errorf("%s: Tenancy = %q, want %q", when, got, tenancy)
	}
}

// A Vm created with a Placement reads back that Placement, on every door that
// publishes it: the create's own answer, ReadVms, ReadVmsState, and the
// SubregionNames filters. Issue #268 measured the lie this replaces — the
// client asked eu-west-2b, the 200 answered eu-west-2a, and a multi-AZ
// Terraform stack re-planned the same change for ever. Both values are
// non-default, Tenancy included, because #268 called out that Tenancy was the
// same constant and would have lied identically for `dedicated`.
func TestAVmCreatedInANamedSubregionReadsBackInIt(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	created := call(t, ts, doc, "CreateVms",
		`{"ImageId":"ami-00000001","BootOnCreation":false,`+
			`"Placement":{"SubregionName":"eu-west-2b","Tenancy":"dedicated"}}`)
	assertPlacement(t, created, "eu-west-2b", "dedicated", "the create's own answer")
	id := firstVMID(t, created)

	read := call(t, ts, doc, "ReadVms", `{"Filters":{"VmIds":["`+id+`"]}}`)
	assertPlacement(t, read, "eu-west-2b", "dedicated", "ReadVms")

	// The state view was a second copy of the same constant (readVmsState).
	states := call(t, ts, doc, "ReadVmsState", `{"AllVms":true,"Filters":{"VmIds":["`+id+`"]}}`)
	rows, _ := states["VmStates"].([]any)
	if len(rows) != 1 {
		t.Fatalf("ReadVmsState answered %d rows for one Vm: %v", len(rows), states)
	}
	row, _ := rows[0].(map[string]any)
	if got, _ := row["SubregionName"].(string); got != "eu-west-2b" {
		t.Errorf("ReadVmsState: SubregionName = %q, want eu-west-2b", got)
	}

	// The filters answer from the same stored fact. Both directions, because a
	// filter that always matches and one that never matches are equally useless.
	matched := call(t, ts, doc, "ReadVms", `{"Filters":{"SubregionNames":["eu-west-2b"]}}`)
	if vms, _ := matched["Vms"].([]any); len(vms) != 1 {
		t.Errorf("filtering ReadVms on the Vm's own zone answered %d Vms", len(vms))
	}
	other := call(t, ts, doc, "ReadVms", `{"Filters":{"SubregionNames":["eu-west-2a"]}}`)
	if vms, _ := other["Vms"].([]any); len(vms) != 0 {
		t.Errorf("filtering ReadVms on the other zone answered %d Vms, want 0", len(vms))
	}
	stateRows := call(t, ts, doc, "ReadVmsState", `{"AllVms":true,"Filters":{"SubregionNames":["eu-west-2a"]}}`)
	if rows, _ := stateRows["VmStates"].([]any); len(rows) != 0 {
		// SubregionNames was declared to ReadVmsState's refusal list and
		// applied nowhere — the declared-and-unread blind spot, again.
		t.Errorf("ReadVmsState filtered on the other zone answered %d rows, want 0", len(rows))
	}
}

// A Vm created in a Subnet without a Placement of its own inherits the
// Subnet's zone — request → store → response holds for the Subnet's half of
// the fact too. And an interface sits where its Subnet sits: the NIC's
// SubregionName was the same constant one resource over, merely not
// client-writable.
func TestAVmInheritsItsSubnetsSubregion(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	_, out := post(t, ts, "CreateNet", `{"IpRange":"10.63.0.0/16"}`)
	n, _ := out["Net"].(map[string]any)
	netID, _ := n["NetId"].(string)
	created := call(t, ts, doc, "CreateSubnet",
		`{"NetId":"`+netID+`","IpRange":"10.63.1.0/24","SubregionName":"eu-west-2b"}`)
	subnet, _ := created["Subnet"].(map[string]any)
	subnetID, _ := subnet["SubnetId"].(string)
	if got, _ := subnet["SubregionName"].(string); got != "eu-west-2b" {
		t.Fatalf("the created Subnet reads back in %q, want eu-west-2b", got)
	}

	vm := call(t, ts, doc, "CreateVms",
		`{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","BootOnCreation":false}`)
	assertPlacement(t, vm, "eu-west-2b", "default", "a Vm placed by its Subnet alone")

	// The Subnet answers the zone filter from the stored fact.
	subnets := call(t, ts, doc, "ReadSubnets", `{"Filters":{"SubregionNames":["eu-west-2b"]}}`)
	if rows, _ := subnets["Subnets"].([]any); len(rows) != 1 {
		t.Errorf("filtering ReadSubnets on eu-west-2b answered %d Subnets, want 1", len(rows))
	}

	// A NIC created on that Subnet sits in the Subnet's zone.
	nicOut := call(t, ts, doc, "CreateNic", `{"SubnetId":"`+subnetID+`"}`)
	nic, _ := nicOut["Nic"].(map[string]any)
	if got, _ := nic["SubregionName"].(string); got != "eu-west-2b" {
		t.Errorf("the created NIC reads back in %q, want eu-west-2b", got)
	}
}

// A Vm created with nothing said still reads back the default — the one half
// of the old behaviour that was honest, kept honest.
func TestAVmWithoutAPlacementReadsBackTheDefault(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	created := call(t, ts, doc, "CreateVms", `{"ImageId":"ami-00000001","BootOnCreation":false}`)
	assertPlacement(t, created, "eu-west-2a", "default", "a Vm with no Placement")
}

// ReadSubregions declares the zones eu-west-2 really has — both of them, per
// Outscale's own published reference. The index [1] is asserted directly
// because it is the exact expression #269 measured failing: a stack that asks
// the API where it may put things (`data "outscale_subregions"`, then
// `subregions[1].subregion_name`) died on "list of object with 1 element"
// while a stack hardcoding its zone sailed through.
func TestReadSubregionsDeclaresTheRegionsSubregions(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	out := call(t, ts, doc, "ReadSubregions", `{}`)
	rows, _ := out["Subregions"].([]any)
	if len(rows) != 2 {
		t.Fatalf("ReadSubregions declares %d Subregions, want 2: %v", len(rows), out)
	}
	second, _ := rows[1].(map[string]any)
	if got, _ := second["SubregionName"].(string); got != "eu-west-2b" {
		t.Errorf("Subregions[1].SubregionName = %q, want eu-west-2b", got)
	}
	if got, _ := second["RegionName"].(string); got != "eu-west-2" {
		t.Errorf("Subregions[1].RegionName = %q, want eu-west-2", got)
	}

	// The filters FiltersSubregion declares are applied, not ignored.
	filtered := call(t, ts, doc, "ReadSubregions", `{"Filters":{"SubregionNames":["eu-west-2b"]}}`)
	rows, _ = filtered["Subregions"].([]any)
	if len(rows) != 1 {
		t.Fatalf("filtering on one zone answered %d, want 1: %v", len(rows), filtered)
	}
	only, _ := rows[0].(map[string]any)
	if got, _ := only["SubregionName"].(string); got != "eu-west-2b" {
		t.Errorf("the filtered answer is %q, want eu-west-2b", got)
	}
	none := call(t, ts, doc, "ReadSubregions", `{"Filters":{"RegionNames":["cloudgouv-eu-west-1"]}}`)
	if rows, _ := none["Subregions"].([]any); len(rows) != 0 {
		t.Errorf("a region this emulator does not run answered %d Subregions", len(rows))
	}
}

// What a create accepts and what the catalogue declares agree, in both
// directions: every declared zone is accepted by every write path that takes
// one, and an undeclared zone is refused by all of them. #269 measured the
// contradiction this closes — CreateSubnet stored `cloudgouv-eu-west-1a`
// verbatim while ReadSubregions declared a single zone.
func TestWhatACreateAcceptsTheCatalogueDeclares(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	declared := call(t, ts, doc, "ReadSubregions", `{}`)
	rows, _ := declared["Subregions"].([]any)
	if len(rows) == 0 {
		t.Fatal("the catalogue declares no Subregion, so this test measures nothing")
	}

	_, out := post(t, ts, "CreateNet", `{"IpRange":"10.64.0.0/16"}`)
	n, _ := out["Net"].(map[string]any)
	netID, _ := n["NetId"].(string)

	for i, raw := range rows {
		row, _ := raw.(map[string]any)
		zone, _ := row["SubregionName"].(string)
		status, created := post(t, ts, "CreateSubnet",
			`{"NetId":"`+netID+`","IpRange":"10.64.`+string(rune('1'+i))+`.0/24","SubregionName":"`+zone+`"}`)
		if status != http.StatusOK {
			t.Errorf("CreateSubnet refuses %s, a zone the catalogue declares: %v", zone, created)
		}
	}

	// An undeclared zone is refused by every door that takes one, with the
	// refusal naming the zone so the caller can act on it.
	for _, probe := range []struct{ action, body string }{
		{"CreateSubnet", `{"NetId":"` + netID + `","IpRange":"10.64.9.0/24","SubregionName":"cloudgouv-eu-west-1a"}`},
		{"CreateVolume", `{"SubregionName":"cloudgouv-eu-west-1a","Size":10}`},
		{"CreateVms", `{"ImageId":"ami-00000001","BootOnCreation":false,"Placement":{"SubregionName":"cloudgouv-eu-west-1a"}}`},
	} {
		status, refused := post(t, ts, probe.action, probe.body)
		if status != http.StatusBadRequest {
			t.Errorf("%s answered %d for a zone the catalogue never declared: %v",
				probe.action, status, refused)
		}
	}
}

// A Placement naming a zone its own Subnet does not sit in is refused, and the
// refusal leaves nothing behind: answering 200 while storing one of two
// contradicting facts is the lie either read would then tell.
func TestAPlacementContradictingTheSubnetIsRefused(t *testing.T) {
	ts := newServer(t)
	_, subnetID := netAndSubnet(t, ts, "10.65.0.0/16", "10.65.1.0/24") // default zone

	status, out := post(t, ts, "CreateVms",
		`{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","BootOnCreation":false,`+
			`"Placement":{"SubregionName":"eu-west-2b"}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("a Placement contradicting the Subnet answered %d: %v", status, out)
	}

	_, read := post(t, ts, "ReadVms", `{}`)
	if vms, _ := read["Vms"].([]any); len(vms) != 0 {
		t.Errorf("the refused create left %d Vms behind", len(vms))
	}
}
