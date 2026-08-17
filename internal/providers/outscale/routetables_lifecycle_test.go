package outscale_test

import (
	"net/http"
	"strings"
	"testing"
)

// The route-table lifecycle, shaped by the recorded creation of 2026-08-08
// (values invented, shapes measured): an explicit CreateRouteTable comes back
// already carrying the local route, LinkRouteTable answers {LinkRouteTableId,
// ResponseContext} alone, and a created route carries CreationMethod
// "CreateRoute" with its target under its own key.
func TestARouteTableLifecycleMatchesTheRecordedShapes(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	netID, subnetID := netAndSubnet(t, ts, "10.30.0.0/16", "10.30.1.0/24")

	created := call(t, ts, doc, "CreateRouteTable", `{"NetId":"`+netID+`"}`)
	rtb, _ := created["RouteTable"].(map[string]any)
	rtbID, _ := rtb["RouteTableId"].(string)
	// Born with the local route, no link: measured.
	routes, _ := rtb["Routes"].([]any)
	if len(routes) != 1 {
		t.Fatalf("a fresh table is born with the local route: %v", rtb)
	}
	if local, _ := routes[0].(map[string]any); local["GatewayId"] != "local" {
		t.Fatalf("the born route is not the local one: %v", routes[0])
	}
	if links, _ := rtb["LinkRouteTables"].([]any); len(links) != 0 {
		t.Fatalf("an explicit table is born unlinked: %v", rtb)
	}

	// LinkRouteTable answers the id alone; the link appears on the next read.
	link := call(t, ts, doc, "LinkRouteTable", `{"RouteTableId":"`+rtbID+`","SubnetId":"`+subnetID+`"}`)
	linkID, _ := link["LinkRouteTableId"].(string)
	if linkID == "" {
		t.Fatalf("LinkRouteTable did not answer a LinkRouteTableId: %v", link)
	}
	if _, present := link["RouteTable"]; present {
		t.Fatalf("LinkRouteTable answered a RouteTable the recording does not: %v", link)
	}

	// A second link on the same subnet is a conflict.
	if status, body := post(t, ts, "LinkRouteTable", `{"RouteTableId":"`+rtbID+`","SubnetId":"`+subnetID+`"}`); status != http.StatusConflict {
		t.Fatalf("a subnet was linked to two tables: %d %v", status, body)
	}

	// A route needs the gateway to be linked to the table's Net.
	gw := call(t, ts, doc, "CreateInternetService", `{}`)
	gwID, _ := gw["InternetService"].(map[string]any)["InternetServiceId"].(string)
	if status, body := post(t, ts, "CreateRoute",
		`{"RouteTableId":"`+rtbID+`","DestinationIpRange":"0.0.0.0/0","GatewayId":"`+gwID+`"}`); status != http.StatusBadRequest {
		t.Fatalf("a route through an unlinked gateway was accepted: %d %v", status, body)
	}
	call(t, ts, doc, "LinkInternetService", `{"InternetServiceId":"`+gwID+`","NetId":"`+netID+`"}`)

	withRoute := call(t, ts, doc, "CreateRoute",
		`{"RouteTableId":"`+rtbID+`","DestinationIpRange":"0.0.0.0/0","GatewayId":"`+gwID+`"}`)
	after, _ := withRoute["RouteTable"].(map[string]any)["Routes"].([]any)
	if len(after) != 2 {
		t.Fatalf("the route did not land beside the local one: %v", withRoute)
	}
	var added map[string]any
	for _, raw := range after {
		route, _ := raw.(map[string]any)
		if route["DestinationIpRange"] == "0.0.0.0/0" {
			added = route
		}
	}
	if added["CreationMethod"] != "CreateRoute" || added["GatewayId"] != gwID || added["State"] != "active" {
		t.Fatalf("the created route is not the recorded shape: %v", added)
	}
	// A NAT-less route carries no NatServiceId key.
	if _, present := added["NatServiceId"]; present {
		t.Fatalf("a gateway route carries a NatServiceId key it should omit: %v", added)
	}

	// The local route cannot be deleted; a duplicate destination cannot be
	// added.
	if status, _ := post(t, ts, "DeleteRoute", `{"RouteTableId":"`+rtbID+`","DestinationIpRange":"10.30.0.0/16"}`); status != http.StatusConflict {
		t.Fatalf("the local route was deleted")
	}
	if status, _ := post(t, ts, "CreateRoute", `{"RouteTableId":"`+rtbID+`","DestinationIpRange":"0.0.0.0/0","GatewayId":"`+gwID+`"}`); status != http.StatusConflict {
		t.Fatalf("a duplicate destination was accepted")
	}

	// The main table and its link are the Net's: neither goes by a client call.
	mainID, mainLinkID := mainRouteTableID(t, call(t, ts, doc, "ReadRouteTables", `{"Filters":{"NetIds":["`+netID+`"]}}`))
	if status, _ := post(t, ts, "DeleteRouteTable", `{"RouteTableId":"`+mainID+`"}`); status != http.StatusConflict {
		t.Fatalf("the main route table was deleted")
	}
	if status, _ := post(t, ts, "UnlinkRouteTable", `{"LinkRouteTableId":"`+mainLinkID+`"}`); status != http.StatusConflict {
		t.Fatalf("the main link was unlinked")
	}

	// The explicit table goes only once unlinked, which is the destroy order.
	if status, _ := post(t, ts, "DeleteRouteTable", `{"RouteTableId":"`+rtbID+`"}`); status != http.StatusConflict {
		t.Fatalf("a linked explicit table was deleted")
	}
	call(t, ts, doc, "DeleteRoute", `{"RouteTableId":"`+rtbID+`","DestinationIpRange":"0.0.0.0/0"}`)
	call(t, ts, doc, "UnlinkRouteTable", `{"LinkRouteTableId":"`+linkID+`"}`)
	call(t, ts, doc, "DeleteRouteTable", `{"RouteTableId":"`+rtbID+`"}`)
}

// mainRouteTableID returns the id and Main-link id of the table carrying the
// Main link, out of a ReadRouteTables answer that may hold several.
func mainRouteTableID(t *testing.T, payload map[string]any) (string, string) {
	t.Helper()
	tables, _ := payload["RouteTables"].([]any)
	for _, raw := range tables {
		table, _ := raw.(map[string]any)
		links, _ := table["LinkRouteTables"].([]any)
		for _, l := range links {
			link, _ := l.(map[string]any)
			if main, _ := link["Main"].(bool); main {
				return table["RouteTableId"].(string), link["LinkRouteTableId"].(string)
			}
		}
	}
	t.Fatal("no main route table in the answer")
	return "", ""
}

// A route can point at a Net peering, which is the only way two peered Nets
// reach each other (#249).
//
// Their SDK declares it — CreateRouteRequest.NetPeeringId, "The ID of a Net
// peering" — and the Terraform provider sends it for `outscale_route` with
// `net_peering_id`. This emulator refused it with "a route needs a target:
// GatewayId, NatServiceId, NicId or VmId" until a realistic stack asked.
//
// Found by writing the Terraform a platform team writes rather than by a test:
// the suite creates peerings and asserts their state machine, and never routes
// through one. That is the gap between proving what somebody thought to assert
// and exercising what somebody actually writes.
func TestARouteCanPointAtANetPeering(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	netID, _ := netAndSubnet(t, ts, "10.70.0.0/16", "10.70.1.0/24")
	other := call(t, ts, doc, "CreateNet", `{"IpRange":"10.71.0.0/16"}`)
	otherNet, _ := other["Net"].(map[string]any)
	otherID, _ := otherNet["NetId"].(string)

	peering := call(t, ts, doc, "CreateNetPeering",
		`{"SourceNetId":"`+netID+`","AccepterNetId":"`+otherID+`"}`)
	pcx, _ := peering["NetPeering"].(map[string]any)
	pcxID, _ := pcx["NetPeeringId"].(string)
	if pcxID == "" {
		t.Fatalf("no peering to route through: %v", peering)
	}

	created := call(t, ts, doc, "CreateRouteTable", `{"NetId":"`+netID+`"}`)
	rtb, _ := created["RouteTable"].(map[string]any)
	rtbID, _ := rtb["RouteTableId"].(string)

	routed := call(t, ts, doc, "CreateRoute", `{
		"RouteTableId":"`+rtbID+`",
		"DestinationIpRange":"10.71.0.0/16",
		"NetPeeringId":"`+pcxID+`"
	}`)
	table, _ := routed["RouteTable"].(map[string]any)
	routes, _ := table["Routes"].([]any)
	found := false
	for _, entry := range routes {
		route, _ := entry.(map[string]any)
		if route["DestinationIpRange"] == "10.71.0.0/16" && route["NetPeeringId"] == pcxID {
			found = true
		}
	}
	if !found {
		t.Errorf("the route does not carry the peering it was created with: %v", routes)
	}

	// And a peering that has neither end in this Net is refused, the way a
	// gateway attached elsewhere is: a route stored against somebody else's
	// peering is a plan that would never work on the real cloud.
	third := call(t, ts, doc, "CreateNet", `{"IpRange":"10.72.0.0/16"}`)
	thirdNet, _ := third["Net"].(map[string]any)
	thirdID, _ := thirdNet["NetId"].(string)
	fourth := call(t, ts, doc, "CreateNet", `{"IpRange":"10.73.0.0/16"}`)
	fourthNet, _ := fourth["Net"].(map[string]any)
	fourthID, _ := fourthNet["NetId"].(string)
	elsewhere := call(t, ts, doc, "CreateNetPeering",
		`{"SourceNetId":"`+thirdID+`","AccepterNetId":"`+fourthID+`"}`)
	elsewherePcx, _ := elsewhere["NetPeering"].(map[string]any)
	elsewhereID, _ := elsewherePcx["NetPeeringId"].(string)

	res, err := http.Post(ts.URL+"/api/v1/CreateRoute", "application/json", strings.NewReader(`{
		"RouteTableId":"`+rtbID+`",
		"DestinationIpRange":"10.73.0.0/16",
		"NetPeeringId":"`+elsewhereID+`"
	}`)) //nolint:noctx // test client
	if err != nil {
		t.Fatalf("CreateRoute: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusOK {
		t.Error("a route was stored through a peering neither of whose ends is this Net")
	}
}
