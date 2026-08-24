package outscale_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/outscale"
)

// The in-place half of resources this pack already served (#172).
//
// Create, read and delete of Nets, Subnets, Nics and route-table links were
// driven by the provider's own fixture; the change a *second* `terraform apply`
// makes was not, so a plan modifying a Net this emulator created died on an
// operation nobody had decided about.
//
// Every case below asserts the refusal beside the change. An update that
// accepted an unknown identifier would store a relation the real cloud refuses,
// and the apply would succeed while the drift appeared later — worse than a 400,
// because nothing goes red at the moment of the mistake.

func updatesServer(t *testing.T) *httptest.Server {
	t.Helper()
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, outscale.New(env))
	if err != nil {
		t.Fatalf("mount the outscale pack: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// doAction posts an action and returns the status and the decoded body.
// Named around the package's own `call`, which takes a contract and answers
// something else: two helpers with one name is how a test file starts fighting
// its neighbours.
func doAction(t *testing.T, ts *httptest.Server, action, body string) (int, map[string]any) {
	t.Helper()
	resp, err := ts.Client().Post(ts.URL+"/api/v1/"+action, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", action, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestUpdateNetRepointsItsDhcpOptions(t *testing.T) {
	ts := updatesServer(t)

	status, created := doAction(t, ts, "CreateNet", `{"IpRange":"10.30.0.0/16"}`)
	if status != http.StatusOK {
		t.Fatalf("create the net: %d (%v)", status, created)
	}
	net, _ := created["Net"].(map[string]any)
	netID, _ := net["NetId"].(string)
	original, _ := net["DhcpOptionsSetId"].(string)
	if netID == "" || original == "" {
		t.Fatalf("the created Net carries no identifier or no options set: %v", net)
	}

	// A second set to move to, created the way a client creates one. This used
	// to fall back to the default set while CreateDhcpOptions was untriaged;
	// now that it is served, the fallback would only hide a regression, so the
	// create is required — and the move below is to a genuinely different set.
	status, madeSet := doAction(t, ts, "CreateDhcpOptions", `{"DomainName":"feint.local"}`)
	if status != http.StatusOK {
		t.Fatalf("create the second options set: %d (%v)", status, madeSet)
	}
	set, _ := madeSet["DhcpOptionsSet"].(map[string]any)
	target, _ := set["DhcpOptionsSetId"].(string)
	if target == "" || target == original {
		t.Fatalf("the created set is not a second one: %v (the Net's own is %s)", set, original)
	}

	status, updated := doAction(t, ts, "UpdateNet",
		`{"NetId":"`+netID+`","DhcpOptionsSetId":"`+target+`"}`)
	if status != http.StatusOK {
		t.Fatalf("update the net: %d (%v)", status, updated)
	}
	after, _ := updated["Net"].(map[string]any)
	if got, _ := after["DhcpOptionsSetId"].(string); got != target {
		t.Errorf("the answer carries %q, want %q", got, target)
	}

	// And the change survives a read, which is what a second plan compares
	// against: an update that answers correctly and stores nothing produces a
	// permanent diff.
	_, read := doAction(t, ts, "ReadNets", `{}`)
	nets, _ := read["Nets"].([]any)
	for _, raw := range nets {
		entry, _ := raw.(map[string]any)
		if id, _ := entry["NetId"].(string); id != netID {
			continue
		}
		if got, _ := entry["DhcpOptionsSetId"].(string); got != target {
			t.Errorf("the read answers %q after the update, want %q", got, target)
		}
	}

	// The refusing half, and the reason it exists: an unknown set stored would
	// be a relation nobody can resolve.
	// 400 with InvalidResource, not 404: that is this pack's own dialect for an
	// identifier that does not exist (errors.go), and a client branches on the
	// type field rather than on the status.
	status, refused := doAction(t, ts, "UpdateNet",
		`{"NetId":"`+netID+`","DhcpOptionsSetId":"dopt-does-not-exist"}`)
	if status != http.StatusBadRequest || !refusesUnknownResource(refused) {
		t.Errorf("an unknown options set answered %d (%v), want 400 InvalidResource", status, refused)
	}
	status, refused = doAction(t, ts, "UpdateNet", `{"NetId":"vpc-nope","DhcpOptionsSetId":"`+target+`"}`)
	if status != http.StatusBadRequest || !refusesUnknownResource(refused) {
		t.Errorf("an unknown Net answered %d (%v), want 400 InvalidResource", status, refused)
	}
	status, _ = doAction(t, ts, "UpdateNet", `{"NetId":"`+netID+`"}`)
	if status != http.StatusBadRequest {
		t.Errorf("a missing DhcpOptionsSetId answered %d, want 400", status)
	}
}

func TestUpdateSubnetFlipsItsPublicAddressing(t *testing.T) {
	ts := updatesServer(t)

	_, created := doAction(t, ts, "CreateNet", `{"IpRange":"10.31.0.0/16"}`)
	net, _ := created["Net"].(map[string]any)
	netID, _ := net["NetId"].(string)

	status, madeSubnet := doAction(t, ts, "CreateSubnet",
		`{"NetId":"`+netID+`","IpRange":"10.31.1.0/24"}`)
	if status != http.StatusOK {
		t.Fatalf("create the subnet: %d (%v)", status, madeSubnet)
	}
	subnet, _ := madeSubnet["Subnet"].(map[string]any)
	subnetID, _ := subnet["SubnetId"].(string)

	status, updated := doAction(t, ts, "UpdateSubnet",
		`{"SubnetId":"`+subnetID+`","MapPublicIpOnLaunch":true}`)
	if status != http.StatusOK {
		t.Fatalf("update the subnet: %d (%v)", status, updated)
	}
	after, _ := updated["Subnet"].(map[string]any)
	if flag, _ := after["MapPublicIpOnLaunch"].(bool); !flag {
		t.Errorf("the flag did not take: %v", after)
	}

	// Absent is not false. A missing field read as false would silently turn the
	// flag off on every call that forgot it, which is the shape of a defect a
	// client never sees until a machine comes up unreachable.
	status, _ = doAction(t, ts, "UpdateSubnet", `{"SubnetId":"`+subnetID+`"}`)
	if status != http.StatusBadRequest {
		t.Errorf("a missing MapPublicIpOnLaunch answered %d, want 400", status)
	}
	status, refused := doAction(t, ts, "UpdateSubnet", `{"SubnetId":"subnet-nope","MapPublicIpOnLaunch":true}`)
	if status != http.StatusBadRequest || !refusesUnknownResource(refused) {
		t.Errorf("an unknown subnet answered %d (%v), want 400 InvalidResource", status, refused)
	}
}

func TestUpdateNicChangesOnlyWhatIsSent(t *testing.T) {
	ts := updatesServer(t)

	_, created := doAction(t, ts, "CreateNet", `{"IpRange":"10.32.0.0/16"}`)
	net, _ := created["Net"].(map[string]any)
	netID, _ := net["NetId"].(string)
	_, madeSubnet := doAction(t, ts, "CreateSubnet", `{"NetId":"`+netID+`","IpRange":"10.32.1.0/24"}`)
	subnet, _ := madeSubnet["Subnet"].(map[string]any)
	subnetID, _ := subnet["SubnetId"].(string)

	status, madeNic := doAction(t, ts, "CreateNic",
		`{"SubnetId":"`+subnetID+`","Description":"first"}`)
	if status != http.StatusOK {
		t.Fatalf("create the nic: %d (%v)", status, madeNic)
	}
	nic, _ := madeNic["Nic"].(map[string]any)
	nicID, _ := nic["NicId"].(string)
	privateIP, _ := nic["PrivateIp"].(string)

	status, updated := doAction(t, ts, "UpdateNic",
		`{"NicId":"`+nicID+`","Description":"second"}`)
	if status != http.StatusOK {
		t.Fatalf("update the nic: %d (%v)", status, updated)
	}
	after, _ := updated["Nic"].(map[string]any)
	if got, _ := after["Description"].(string); got != "second" {
		t.Errorf("the description did not take: %v", after)
	}
	// What was not sent was not touched. An update that reset the rest would be
	// the emulator answering a request nobody made, and Terraform sends exactly
	// the attributes that changed.
	if got, _ := after["PrivateIp"].(string); got != privateIP {
		t.Errorf("an unrelated field moved: PrivateIp %q became %q", privateIP, got)
	}

	status, refused := doAction(t, ts, "UpdateNic", `{"NicId":"eni-nope","Description":"x"}`)
	if status != http.StatusBadRequest || !refusesUnknownResource(refused) {
		t.Errorf("an unknown nic answered %d (%v), want 400 InvalidResource", status, refused)
	}
	// An interface that is not attached has no attachment to update, and saying
	// so beats writing a LinkNic that never existed.
	status, _ = doAction(t, ts, "UpdateNic",
		`{"NicId":"`+nicID+`","LinkNic":{"DeleteOnVmDeletion":true}}`)
	if status != http.StatusBadRequest {
		t.Errorf("updating the attachment of an unattached nic answered %d, want 400", status)
	}
}

func TestUpdateRouteTableLinkMovesTheLinkAndNothingElse(t *testing.T) {
	ts := updatesServer(t)

	_, created := doAction(t, ts, "CreateNet", `{"IpRange":"10.33.0.0/16"}`)
	net, _ := created["Net"].(map[string]any)
	netID, _ := net["NetId"].(string)
	_, madeSubnet := doAction(t, ts, "CreateSubnet", `{"NetId":"`+netID+`","IpRange":"10.33.1.0/24"}`)
	subnet, _ := madeSubnet["Subnet"].(map[string]any)
	subnetID, _ := subnet["SubnetId"].(string)

	_, first := doAction(t, ts, "CreateRouteTable", `{"NetId":"`+netID+`"}`)
	firstTable, _ := first["RouteTable"].(map[string]any)
	firstID, _ := firstTable["RouteTableId"].(string)
	_, second := doAction(t, ts, "CreateRouteTable", `{"NetId":"`+netID+`"}`)
	secondTable, _ := second["RouteTable"].(map[string]any)
	secondID, _ := secondTable["RouteTableId"].(string)

	status, linked := doAction(t, ts, "LinkRouteTable",
		`{"RouteTableId":"`+firstID+`","SubnetId":"`+subnetID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("link the route table: %d (%v)", status, linked)
	}
	linkID, _ := linked["LinkRouteTableId"].(string)
	if linkID == "" {
		t.Fatalf("the link carries no identifier: %v", linked)
	}

	status, moved := doAction(t, ts, "UpdateRouteTableLink",
		`{"LinkRouteTableId":"`+linkID+`","RouteTableId":"`+secondID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("move the link: %d (%v)", status, moved)
	}

	// The link is on the second table and gone from the first: a move that left
	// it on both would route one subnet two ways, which the real cloud cannot do.
	_, read := doAction(t, ts, "ReadRouteTables", `{}`)
	holders := map[string]bool{}
	for _, raw := range read["RouteTables"].([]any) {
		table, _ := raw.(map[string]any)
		id, _ := table["RouteTableId"].(string)
		for _, rawLink := range listOrEmpty(table["LinkRouteTables"]) {
			link, _ := rawLink.(map[string]any)
			if got, _ := link["LinkRouteTableId"].(string); got == linkID {
				holders[id] = true
			}
		}
	}
	if !holders[secondID] {
		t.Error("the link is not on the table it was moved to")
	}
	if holders[firstID] {
		t.Error("the link is still on the table it was moved from, so one subnet " +
			"is routed by two tables at once")
	}

	status, refused := doAction(t, ts, "UpdateRouteTableLink",
		`{"LinkRouteTableId":"rtbassoc-nope","RouteTableId":"`+secondID+`"}`)
	if status != http.StatusBadRequest || !refusesUnknownResource(refused) {
		t.Errorf("an unknown link answered %d (%v), want 400 InvalidResource", status, refused)
	}
}

// The main link, moved whole. This is the path the Terraform resource
// `outscale_main_route_table_link` walks: its Create re-points the Net's main
// link at another table with UpdateRouteTableLink, then reads the main table
// back through the LinkRouteTableMain=true filter and finds its link by
// LinkRouteTableId. A move that rebuilt the link with Main:false — which is
// what this pack first did — leaves the Net with no main table at all, and the
// provider dies on an empty read right after a call that answered 200.
func TestUpdateRouteTableLinkMovesTheMainLinkWhole(t *testing.T) {
	ts := updatesServer(t)

	_, created := doAction(t, ts, "CreateNet", `{"IpRange":"10.34.0.0/16"}`)
	net, _ := created["Net"].(map[string]any)
	netID, _ := net["NetId"].(string)

	// The main link every Net is born with, found the way the provider finds
	// it: on the table the LinkRouteTableMain filter answers.
	_, read := doAction(t, ts, "ReadRouteTables",
		`{"Filters":{"NetIds":["`+netID+`"],"LinkRouteTableMain":true}}`)
	tables := listOrEmpty(read["RouteTables"])
	if len(tables) != 1 {
		t.Fatalf("a fresh Net should have exactly one main table, got %v", read)
	}
	mainTable, _ := tables[0].(map[string]any)
	links := listOrEmpty(mainTable["LinkRouteTables"])
	if len(links) != 1 {
		t.Fatalf("the main table should carry exactly its main link, got %v", mainTable)
	}
	mainLink, _ := links[0].(map[string]any)
	linkID, _ := mainLink["LinkRouteTableId"].(string)

	_, made := doAction(t, ts, "CreateRouteTable", `{"NetId":"`+netID+`"}`)
	targetTable, _ := made["RouteTable"].(map[string]any)
	targetID, _ := targetTable["RouteTableId"].(string)

	status, moved := doAction(t, ts, "UpdateRouteTableLink",
		`{"LinkRouteTableId":"`+linkID+`","RouteTableId":"`+targetID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("move the main link: %d (%v)", status, moved)
	}

	// The main table is now the target, and the link on it is still the main
	// link: same identifier, Main still true, NetId still carried, and no
	// SubnetId key — the measured main link has none, and inventing one on the
	// move is inventing a subnet.
	_, after := doAction(t, ts, "ReadRouteTables",
		`{"Filters":{"NetIds":["`+netID+`"],"LinkRouteTableMain":true}}`)
	afterTables := listOrEmpty(after["RouteTables"])
	if len(afterTables) != 1 {
		t.Fatalf("the Net lost its main table in the move: %v", after)
	}
	nowMain, _ := afterTables[0].(map[string]any)
	if id, _ := nowMain["RouteTableId"].(string); id != targetID {
		t.Fatalf("the main table is %s, want the move target %s", id, targetID)
	}
	movedLinks := listOrEmpty(nowMain["LinkRouteTables"])
	if len(movedLinks) != 1 {
		t.Fatalf("the target table should carry exactly the moved link, got %v", nowMain)
	}
	got, _ := movedLinks[0].(map[string]any)
	if id, _ := got["LinkRouteTableId"].(string); id != linkID {
		t.Errorf("the moved link changed identity: %v, want %s", got["LinkRouteTableId"], linkID)
	}
	if main, _ := got["Main"].(bool); !main {
		t.Error("the moved main link lost Main:true, so the Net has no main table")
	}
	if id, _ := got["NetId"].(string); id != netID {
		t.Errorf("the moved link lost its NetId: %v, want %s", got["NetId"], netID)
	}
	if _, present := got["SubnetId"]; present {
		t.Errorf("the moved main link gained a SubnetId key the real cloud omits: %v", got)
	}
	if id, _ := got["RouteTableId"].(string); id != targetID {
		t.Errorf("the moved link does not name its new table: %v, want %s", got["RouteTableId"], targetID)
	}
}

func listOrEmpty(v any) []any {
	out, _ := v.([]any)
	return out
}

// refusesUnknownResource reports whether the body is this pack's refusal for an
// identifier that does not exist. The type is what a client branches on, so it
// is what these tests assert rather than the status alone.
func refusesUnknownResource(body map[string]any) bool {
	errs, _ := body["Errors"].([]any)
	for _, raw := range errs {
		entry, _ := raw.(map[string]any)
		if kind, _ := entry["Type"].(string); kind == "InvalidResource" {
			return true
		}
	}
	return false
}

// No volume this pack answers carries a TaskId, on either operation that
// declines it.
//
// The property the decline depends on, and the reason it is a test rather than
// a sentence: a decline is written against an OPERATION, and ReadVolumes
// answers many volumes where UpdateVolume answers one. If any volume the pack
// serves ever grew a TaskId, the decline would be true of the object somebody
// measured and fiction for the next -- which is precisely how #389 cost a
// release, and why the Iops decline beside this one carries its own count.
//
// The recording that produced these declines is the counter-evidence too: 7 of
// the 8 volume records a real account answered carry NO TaskId, so the field is
// a property of a volume with a task in flight and not of every volume. This
// emulator has no tasks at all.
func TestNoVolumeThisPackServesCarriesATask(t *testing.T) {
	declined := map[string]bool{}
	for _, d := range outscale.New(nil).DeclinedFields() {
		switch d.Operation {
		case "osc/Client.ReadVolumes", "osc/Client.UpdateVolume":
			declined[d.Operation] = true
		}
	}
	if len(declined) != 2 {
		t.Fatalf("expected both volume operations to decline something, got %v", declined)
	}

	ts := updatesServer(t)

	_, created := post(t, ts, "CreateVolume", `{"SubregionName":"eu-west-2a","Size":10}`)
	volume, _ := created["Volume"].(map[string]any)
	volID, _ := volume["VolumeId"].(string)
	if volID == "" {
		t.Fatalf("no VolumeId in %v", created)
	}
	if _, carries := volume["TaskId"]; carries {
		t.Errorf("CreateVolume answered a TaskId (%v); this emulator has no tasks", volume["TaskId"])
	}

	// The update is the operation whose upstream answer names a task.
	_, updated := post(t, ts, "UpdateVolume", `{"VolumeId":"`+volID+`","Size":12}`)
	after, _ := updated["Volume"].(map[string]any)
	if after == nil {
		t.Fatalf("UpdateVolume answered no Volume: %v", updated)
	}
	if _, carries := after["TaskId"]; carries {
		t.Errorf("UpdateVolume answered a TaskId (%v), so its decline is fiction", after["TaskId"])
	}

	// And the read, which answers every volume rather than one.
	_, listed := post(t, ts, "ReadVolumes", `{}`)
	volumes, _ := listed["Volumes"].([]any)
	if len(volumes) == 0 {
		t.Fatalf("ReadVolumes answered none: %v", listed)
	}
	for _, raw := range volumes {
		v, _ := raw.(map[string]any)
		if _, carries := v["TaskId"]; carries {
			t.Errorf("a volume in ReadVolumes carries a TaskId (%v), so the decline is true of "+
				"some volumes and fiction for others", v["TaskId"])
		}
	}
}
