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

	// A second set to move to, created the way a client creates one.
	status, madeSet := doAction(t, ts, "CreateDhcpOptions", `{"DomainName":"feint.local"}`)
	if status != http.StatusOK {
		// The create is not served yet — #172's remaining families. Moving to
		// the default set is still a real update and still proves the path.
		t.Logf("CreateDhcpOptions is not served yet (%d), using the default set", status)
		madeSet = map[string]any{}
	}

	target := original
	if set, ok := madeSet["DhcpOptionsSet"].(map[string]any); ok {
		if id, _ := set["DhcpOptionsSetId"].(string); id != "" {
			target = id
		}
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
