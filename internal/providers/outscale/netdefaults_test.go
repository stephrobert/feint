package outscale_test

import (
	"strings"
	"testing"
)

// What a Net is born with, measured rather than read off the SDK: a read-only
// sweep of a real account through `feint proxy` (X-2, 2026-08-08) recorded the
// pristine default security group, the main route table and the account's
// default DHCP options set. The values asserted here are the emulator's own
// invented ones; the field sets, and above all their CONDITIONALITY — which
// keys a pristine object omits — are the measurement. The contract cannot
// check any of that: Outscale's Volume-family schemas declare no required
// fields, so an omitted key never fails validation. These tests are the only
// thing holding it.

func TestACreatedNetCarriesItsDefaults(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	created := call(t, ts, doc, "CreateNet", `{"IpRange":"10.7.0.0/16"}`)
	net, _ := created["Net"].(map[string]any)
	netID, _ := net["NetId"].(string)

	// The Net references the account's DHCP set. Found missing by
	// `feint transcript --against` on a real account: the real Net carries it,
	// the emulated one did not, and no contract could see the omission.
	dhcpID, _ := net["DhcpOptionsSetId"].(string)
	if !strings.HasPrefix(dhcpID, "dopt-") {
		t.Fatalf("Net.DhcpOptionsSetId = %q, want a dopt- id", dhcpID)
	}

	// The default DHCP set, as measured: Default true, the regional domain,
	// and the OutscaleProvidedDNS keyword rather than an address.
	sets := call(t, ts, doc, "ReadDhcpOptions", `{}`)
	dhcp := firstOf(t, sets, "DhcpOptionsSets")
	if def, _ := dhcp["Default"].(bool); !def {
		t.Fatalf("the default DHCP set does not say Default:true: %v", dhcp)
	}
	if dhcp["DhcpOptionsSetId"] != dhcpID {
		t.Fatalf("the Net references %s but the account's set is %v", dhcpID, dhcp["DhcpOptionsSetId"])
	}
	if servers, _ := dhcp["DomainNameServers"].([]any); len(servers) != 1 || servers[0] != "OutscaleProvidedDNS" {
		t.Fatalf("DomainNameServers = %v, want the OutscaleProvidedDNS keyword", dhcp["DomainNameServers"])
	}

	// The default security group, filtered by the Net — which also proves the
	// NetIds filter is applied, not ignored.
	groups := call(t, ts, doc, "ReadSecurityGroups", `{"Filters":{"NetIds":["`+netID+`"]}}`)
	sg := firstOf(t, groups, "SecurityGroups")
	if sg["SecurityGroupName"] != "default" || sg["Description"] != "default security group" {
		t.Fatalf("the default group is not the measured one: %v", sg)
	}

	// The pristine inbound rule: everything, from the group itself. Measured
	// detail one: the member carries AccountId and SecurityGroupId only, never
	// SecurityGroupName. Measured detail two: the rule has NO IpRanges key —
	// omitted, not empty.
	inbound, _ := sg["InboundRules"].([]any)
	if len(inbound) != 1 {
		t.Fatalf("a pristine group has one inbound rule, got %v", sg["InboundRules"])
	}
	rule, _ := inbound[0].(map[string]any)
	if _, present := rule["IpRanges"]; present {
		t.Fatalf("the pristine inbound rule carries an IpRanges key the real cloud omits: %v", rule)
	}
	members, _ := rule["SecurityGroupsMembers"].([]any)
	if len(members) != 1 {
		t.Fatalf("the inbound rule does not point at the group itself: %v", rule)
	}
	member, _ := members[0].(map[string]any)
	if member["SecurityGroupId"] != sg["SecurityGroupId"] {
		t.Fatalf("the member names %v, want the group itself %v", member["SecurityGroupId"], sg["SecurityGroupId"])
	}
	if _, present := member["SecurityGroupName"]; present {
		t.Fatalf("the member carries SecurityGroupName, which the real cloud omits: %v", member)
	}

	// The pristine outbound rule: everything, to everywhere — and no
	// SecurityGroupsMembers key.
	outbound, _ := sg["OutboundRules"].([]any)
	if len(outbound) != 1 {
		t.Fatalf("a pristine group has one outbound rule, got %v", sg["OutboundRules"])
	}
	out, _ := outbound[0].(map[string]any)
	if ranges, _ := out["IpRanges"].([]any); len(ranges) != 1 || ranges[0] != "0.0.0.0/0" {
		t.Fatalf("the outbound rule does not open to 0.0.0.0/0: %v", out)
	}
	if _, present := out["SecurityGroupsMembers"]; present {
		t.Fatalf("the outbound rule carries a members key the real cloud omits: %v", out)
	}

	// The main route table: one local route over the Net's own block, one link
	// with Main:true and no SubnetId key. The local route carries no
	// NatServiceId key: conditional again.
	tables := call(t, ts, doc, "ReadRouteTables", `{"Filters":{"NetIds":["`+netID+`"]}}`)
	rtb := firstOf(t, tables, "RouteTables")
	routes, _ := rtb["Routes"].([]any)
	if len(routes) != 1 {
		t.Fatalf("the main table has one route, got %v", rtb["Routes"])
	}
	route, _ := routes[0].(map[string]any)
	if route["GatewayId"] != "local" || route["DestinationIpRange"] != "10.7.0.0/16" || route["State"] != "active" {
		t.Fatalf("the local route is not the measured one: %v", route)
	}
	if _, present := route["NatServiceId"]; present {
		t.Fatalf("the local route carries a NatServiceId key the real cloud omits: %v", route)
	}
	links, _ := rtb["LinkRouteTables"].([]any)
	if len(links) != 1 {
		t.Fatalf("the main table has one link, got %v", rtb["LinkRouteTables"])
	}
	link, _ := links[0].(map[string]any)
	if main, _ := link["Main"].(bool); !main {
		t.Fatalf("the link does not say Main:true: %v", link)
	}
	if _, present := link["SubnetId"]; present {
		t.Fatalf("the main link carries a SubnetId key the real cloud omits: %v", link)
	}

	// Identity is stable: a second read returns the same identifiers, which is
	// what Terraform stores and diffs on.
	again := firstOf(t, call(t, ts, doc, "ReadSecurityGroups", `{"Filters":{"NetIds":["`+netID+`"]}}`), "SecurityGroups")
	if again["SecurityGroupId"] != sg["SecurityGroupId"] {
		t.Fatalf("the default group changed id between two reads: %v then %v", sg["SecurityGroupId"], again["SecurityGroupId"])
	}
}

func TestDeletingANetTakesItsDefaultsWithIt(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	created := call(t, ts, doc, "CreateNet", `{"IpRange":"10.8.0.0/16"}`)
	net, _ := created["Net"].(map[string]any)
	netID, _ := net["NetId"].(string)

	call(t, ts, doc, "DeleteNet", `{"NetId":"`+netID+`"}`)

	groups := call(t, ts, doc, "ReadSecurityGroups", `{"Filters":{"NetIds":["`+netID+`"]}}`)
	if list, _ := groups["SecurityGroups"].([]any); len(list) != 0 {
		t.Fatalf("the default group survived its Net: %v", list)
	}
	tables := call(t, ts, doc, "ReadRouteTables", `{"Filters":{"NetIds":["`+netID+`"]}}`)
	if list, _ := tables["RouteTables"].([]any); len(list) != 0 {
		t.Fatalf("the main route table survived its Net: %v", list)
	}
}

func TestAVmInANetHasANic(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	netID, subnetID := netAndSubnet(t, ts, "10.9.0.0/16", "10.9.1.0/24")
	created := call(t, ts, doc, "CreateVms",
		`{"ImageId":"ami-00000001","VmType":"tinav6.c1r1p2","SubnetId":"`+subnetID+`","BootOnCreation":false}`)
	vms, _ := created["Vms"].([]any)
	vm, _ := vms[0].(map[string]any)
	vmID, _ := vm["VmId"].(string)

	nics := call(t, ts, doc, "ReadNics", `{"Filters":{"SubnetIds":["`+subnetID+`"]}}`)
	nic := firstOf(t, nics, "Nics")

	// The identifiers are a pure function of the VmId, so they cannot move
	// between reads: i-1234abcd owns eni-1234abcd.
	wantNic := "eni-" + strings.TrimPrefix(vmID, "i-")
	if nic["NicId"] != wantNic {
		t.Fatalf("NicId = %v, want %s derived from %s", nic["NicId"], wantNic, vmID)
	}
	linkNic, _ := nic["LinkNic"].(map[string]any)
	if linkNic["VmId"] != vmID || linkNic["State"] != "attached" {
		t.Fatalf("LinkNic does not name the machine: %v", linkNic)
	}
	if nic["NetId"] != netID {
		t.Fatalf("NicId %v is not placed in %s: %v", nic["NicId"], netID, nic)
	}

	// The primary address is the machine's, under the measured DNS shape.
	ips, _ := nic["PrivateIps"].([]any)
	if len(ips) != 1 {
		t.Fatalf("one primary address expected: %v", nic["PrivateIps"])
	}
	primary, _ := ips[0].(map[string]any)
	if isPrimary, _ := primary["IsPrimary"].(bool); !isPrimary {
		t.Fatalf("the address does not say IsPrimary: %v", primary)
	}
	dns, _ := nic["PrivateDnsName"].(string)
	if !strings.HasPrefix(dns, "ip-") || !strings.HasSuffix(dns, ".compute.internal") {
		t.Fatalf("PrivateDnsName %q is not the measured ip-a-b-c-d.<region>.compute.internal shape", dns)
	}

	// The interface carries the Net's default group: a machine is never
	// group-less in a Net, whether or not the client named one.
	groups, _ := nic["SecurityGroups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("the NIC does not carry the Net's default group: %v", nic["SecurityGroups"])
	}

	// A machine outside any Net has no NIC to manage, same as the real cloud.
	all := call(t, ts, doc, "ReadNics", `{}`)
	for _, raw := range all["Nics"].([]any) {
		candidate, _ := raw.(map[string]any)
		if got, _ := candidate["LinkNic"].(map[string]any); got["VmId"] != vmID {
			t.Fatalf("a NIC appeared for a machine outside the subnet: %v", candidate)
		}
	}
}

// firstOf unwraps the single element every test in this file expects a list to
// hold, and fails with the whole payload when the expectation is wrong.
func firstOf(t *testing.T, payload map[string]any, key string) map[string]any {
	t.Helper()
	list, _ := payload[key].([]any)
	if len(list) != 1 {
		t.Fatalf("%s: want exactly one element, got %v", key, payload[key])
	}
	out, _ := list[0].(map[string]any)
	return out
}
