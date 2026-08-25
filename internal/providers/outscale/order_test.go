package outscale_test

import (
	"sort"
	"testing"
)

// Two lists a client stores as lists, ordered the way the real API orders them
// (#379).
//
// Both were measured on 2026-08-21 against a real Outscale account, and both
// matter for the same reason: Terraform stores `security_group_ids` and a route
// table's `routes` as **lists**, so an order of this emulator's own is a plan
// diff that never converges. That is the Outscale spelling of #320, the defect
// that cost a pull request on the Scaleway side.

// A machine's security groups come back ordered by identifier.
//
// The rule had two candidates that agree on the obvious sample — the recording
// sent web-then-db and the cloud answered db-then-web, which sorting by name
// and sorting by id both explain — and the account's own two long-lived
// machines settle it by refuting the name:
//
//	machine A  sg-2222aaaa "ssh-only",  sg-3333bbbb "alerting"
//	machine B  sg-2222aaaa "ssh-only",  sg-ffffcccc "open-all"
//
// The two rows above are anonymised, and the anonymisation keeps the only thing
// they are evidence of: the ids ascend, and in neither row is the name order the
// answered one. The real identifiers and group names are a live account's
// inventory and this repository is public — docs/proxy.md states the rule:
// name a path, a type, a status and a position, never a value.
//
// Both are ascending by id, and in neither is the name order the one answered.
//
// This is a unit test rather than a replay invariant, and that is deliberate:
// the order is derived from identifiers the *cloud* minted, no emulator mints
// those, and `feint replay` compares position by position after rebinding — so
// a corpus could only ever have carried a permanent exemption for it.
// internal/providers/outscale/invariants.go states the whole of it.
func TestAMachinesSecurityGroupsAnswerInIdentifierOrder(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	netID, subnetID := netAndSubnet(t, ts, "10.60.0.0/16", "10.60.1.0/24")

	// Two groups, created in one order and named in the opposite one, so that
	// neither creation order nor name order can be mistaken for the rule. The
	// third possible explanation — the order the request named them in — is
	// pinned below, where it is set to the opposite of the expected answer.
	web := call(t, ts, doc, "CreateSecurityGroup",
		`{"NetId":"`+netID+`","SecurityGroupName":"zzz-web","Description":"web"}`)
	webID, _ := web["SecurityGroup"].(map[string]any)["SecurityGroupId"].(string)
	db := call(t, ts, doc, "CreateSecurityGroup",
		`{"NetId":"`+netID+`","SecurityGroupName":"aaa-db","Description":"db"}`)
	dbID, _ := db["SecurityGroup"].(map[string]any)["SecurityGroupId"].(string)

	// The REQUEST names them in descending id order, deliberately, and that is
	// what makes this test bite every time instead of half the time.
	//
	// An Outscale id here is a prefix and eight hexadecimal characters of a
	// freshly generated UUID (newID in pack.go), so which of two groups sorts
	// first is a coin toss. Naming them in creation order therefore left the
	// request already ascending on about half of all runs, and on those runs
	// the answer is ascending whether anything sorts it or not.
	//
	// Measured on 2026-08-25 while replaying every falsification: with
	// `sort.Strings(sorted)` neutralised in securitygroups.go — the mutation
	// tools/falsify/specs/outscale-answers-what-the-cloud-answers.json applies —
	// this test came back GREEN 8 times out of 20. A guard whose falsification
	// is a coin toss is a guard nobody can trust the day it matters, and it read
	// exactly like the guard working on the other twelve.
	//
	// Sending the descending order removes the chance: the ascending answer can
	// then come from nowhere but the sort.
	want := []string{webID, dbID}
	sort.Strings(want)
	asked := []string{want[1], want[0]}

	created := call(t, ts, doc, "CreateVms",
		`{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","BootOnCreation":false,`+
			`"SecurityGroupIds":["`+asked[0]+`","`+asked[1]+`"]}`)
	vms, _ := created["Vms"].([]any)
	if len(vms) != 1 {
		t.Fatalf("no machine was created: %v", created)
	}
	vmID, _ := vms[0].(map[string]any)["VmId"].(string)

	read := call(t, ts, doc, "ReadVms", `{"Filters":{"VmIds":["`+vmID+`"]}}`)
	updated := call(t, ts, doc, "UpdateVm",
		`{"VmId":"`+vmID+`","SecurityGroupIds":["`+asked[0]+`","`+asked[1]+`"]}`)

	for _, where := range []struct {
		what string
		got  []string
	}{
		{"CreateVms", groupIDs(t, vms[0])},
		{"ReadVms", groupIDs(t, firstVM(t, read))},
		{"UpdateVm", groupIDs(t, updated["Vm"])},
	} {
		if len(where.got) != 2 {
			t.Fatalf("%s answered %d group(s), want 2: %v", where.what, len(where.got), where.got)
		}
		if where.got[0] != want[0] || where.got[1] != want[1] {
			t.Errorf("%s answered the groups %v, want %v (ascending by identifier): a client that "+
				"stores this as a list re-plans for ever", where.what, where.got, want)
		}
	}
}

// A route table answers its routes in destination order on a READ, and in
// append order on the CREATE — because that is what the cloud does.
//
// Measured on 2026-08-21 against a real account, on one table carrying the
// Net's own local route and one route to an internet service:
//
//	CreateRoute       [ <the Net's range>, 0.0.0.0/0 ]   append order
//	ReadRouteTables   [ 0.0.0.0/0, <the Net's range> ]   destination order
//
// The two disagreeing is the cloud's behaviour, not something to improve on
// here: an emulator that tidied it up would be hiding the plan diff a real
// stack meets.
func TestARouteTableAnswersItsRoutesInDestinationOrder(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	// A Net whose own range sorts after "0.0.0.0/0" as a string, and whose
	// local route is created first — so append order and destination order are
	// opposites here, exactly as they were on the account.
	netID, _ := netAndSubnet(t, ts, "10.61.0.0/16", "10.61.1.0/24")

	table := call(t, ts, doc, "CreateRouteTable", `{"NetId":"`+netID+`"}`)
	rtbID, _ := table["RouteTable"].(map[string]any)["RouteTableId"].(string)
	gw := call(t, ts, doc, "CreateInternetService", `{}`)
	gwID, _ := gw["InternetService"].(map[string]any)["InternetServiceId"].(string)
	call(t, ts, doc, "LinkInternetService", `{"InternetServiceId":"`+gwID+`","NetId":"`+netID+`"}`)

	created := call(t, ts, doc, "CreateRoute",
		`{"RouteTableId":"`+rtbID+`","DestinationIpRange":"0.0.0.0/0","GatewayId":"`+gwID+`"}`)
	if got := destinations(t, created["RouteTable"]); len(got) != 2 ||
		got[0] != "10.61.0.0/16" || got[1] != "0.0.0.0/0" {
		t.Errorf("the create answered %v, want the table as it stood plus what was added: "+
			"[10.61.0.0/16 0.0.0.0/0]", got)
	}

	read := call(t, ts, doc, "ReadRouteTables", `{"Filters":{"RouteTableIds":["`+rtbID+`"]}}`)
	tables, _ := read["RouteTables"].([]any)
	if len(tables) != 1 {
		t.Fatalf("the table was not read back: %v", read)
	}
	if got := destinations(t, tables[0]); len(got) != 2 ||
		got[0] != "0.0.0.0/0" || got[1] != "10.61.0.0/16" {
		t.Errorf("the read answered %v, want destination order: [0.0.0.0/0 10.61.0.0/16]; a client "+
			"that stores a route table's routes as a list re-plans for ever", got)
	}
}

// A machine created with neither user data nor tags answers both keys anyway
// (#378).
//
// Measured on a real account on 2026-08-21: the cloud writes `"UserData": ""`
// and `"Tags": []` on every machine, and this pack wrote them only when it had
// something to put in them. A client reading a string or iterating a list gets
// nothing here where the cloud gives it an empty one — Terraform's outscale_vm
// reads both.
//
// This is not the rule PrivateIp follows, and the difference is what the cloud
// does rather than a preference: an absent PrivateIp says this emulator models
// no address, while an absent UserData says nothing at all.
func TestAMachineAnswersUserDataAndTagsEvenWithNeither(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	_, subnetID := netAndSubnet(t, ts, "10.63.0.0/16", "10.63.1.0/24")
	created := call(t, ts, doc, "CreateVms",
		`{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","BootOnCreation":false}`)
	vms, _ := created["Vms"].([]any)
	if len(vms) != 1 {
		t.Fatalf("no machine was created: %v", created)
	}
	vm, _ := vms[0].(map[string]any)
	vmID, _ := vm["VmId"].(string)

	read := call(t, ts, doc, "ReadVms", `{"Filters":{"VmIds":["`+vmID+`"]}}`)
	updated := call(t, ts, doc, "UpdateVm", `{"VmId":"`+vmID+`"}`)

	for what, answer := range map[string]any{
		"CreateVms": vm,
		"ReadVms":   firstVM(t, read),
		"UpdateVm":  updated["Vm"],
	} {
		m, _ := answer.(map[string]any)
		data, present := m["UserData"]
		if !present {
			t.Errorf("%s answered no UserData at all, where the cloud answers an empty string", what)
		} else if s, _ := data.(string); s != "" {
			t.Errorf("%s answered UserData %q, want the empty string", what, s)
		}
		tags, present := m["Tags"]
		if !present {
			t.Errorf("%s answered no Tags at all, where the cloud answers an empty list", what)
			continue
		}
		list, isList := tags.([]any)
		if !isList {
			t.Errorf("%s answered Tags %T, want a list", what, tags)
		} else if len(list) != 0 {
			t.Errorf("%s answered %d tag(s) on a machine created with none", what, len(list))
		}
	}
}

// groupIDs reads a machine's SecurityGroups in the order it answered them.
func groupIDs(t *testing.T, vm any) []string {
	t.Helper()
	m, _ := vm.(map[string]any)
	groups, _ := m["SecurityGroups"].([]any)
	out := make([]string, 0, len(groups))
	for _, raw := range groups {
		g, _ := raw.(map[string]any)
		id, _ := g["SecurityGroupId"].(string)
		out = append(out, id)
	}
	return out
}

// firstVM reads the one machine a filtered ReadVms answered.
func firstVM(t *testing.T, answer map[string]any) any {
	t.Helper()
	vms, _ := answer["Vms"].([]any)
	if len(vms) != 1 {
		t.Fatalf("want one machine, got %d: %v", len(vms), answer)
	}
	return vms[0]
}

// A rule whose source is another security group publishes that group's account
// and name, not just the identifier the client sent (#382).
//
// It is the shape examples/stacks/outscale/ writes as a tiering rule — the data
// tier accepting the web tier and nobody else — and a client reads the member
// back to decide whether the rule is cross-account. `AccountId` is what carries
// that, which is the whole reason SecurityGroupAccountIdToLink exists on the
// request, and this pack copied the request's member through verbatim.
//
// Measured on a real account on 2026-08-21, and it is the family of #371 on the
// Exoscale side, one provider out.
func TestARuleThatNamesAnotherGroupPublishesItsAccountAndName(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	netID, _ := netAndSubnet(t, ts, "10.62.0.0/16", "10.62.1.0/24")
	web := call(t, ts, doc, "CreateSecurityGroup",
		`{"NetId":"`+netID+`","SecurityGroupName":"web","Description":"web tier"}`)
	webID, _ := web["SecurityGroup"].(map[string]any)["SecurityGroupId"].(string)
	db := call(t, ts, doc, "CreateSecurityGroup",
		`{"NetId":"`+netID+`","SecurityGroupName":"db","Description":"data tier"}`)
	dbID, _ := db["SecurityGroup"].(map[string]any)["SecurityGroupId"].(string)

	// The Rules form, naming the other group by identifier alone. This is the
	// path that copied the member through unchanged; SecurityGroupNameToLink
	// already filled it in, which is why the gap was invisible.
	created := call(t, ts, doc, "CreateSecurityGroupRule",
		`{"SecurityGroupId":"`+dbID+`","Flow":"Inbound","Rules":[{"IpProtocol":"tcp",`+
			`"FromPortRange":5432,"ToPortRange":5432,`+
			`"SecurityGroupsMembers":[{"SecurityGroupId":"`+webID+`"}]}]}`)

	read := call(t, ts, doc, "ReadSecurityGroups", `{"Filters":{"SecurityGroupIds":["`+dbID+`"]}}`)
	groups, _ := read["SecurityGroups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("the group was not read back: %v", read)
	}

	for what, group := range map[string]any{
		"CreateSecurityGroupRule": created["SecurityGroup"],
		"ReadSecurityGroups":      groups[0],
	} {
		member := firstMember(t, group)
		if member["SecurityGroupId"] != webID {
			t.Errorf("%s: the member names %v, want %s", what, member["SecurityGroupId"], webID)
		}
		if account, _ := member["AccountId"].(string); account == "" {
			t.Errorf("%s: the member carries no AccountId, so a client cannot tell a group of its "+
				"own account from one shared in: %v", what, member)
		}
		if name, _ := member["SecurityGroupName"].(string); name != "web" {
			t.Errorf("%s: the member's name is %q, want %q", what, name, "web")
		}
	}
}

// firstMember reads the one SecurityGroupsMembers entry of a group's one
// inbound rule that has any.
func firstMember(t *testing.T, group any) map[string]any {
	t.Helper()
	g, _ := group.(map[string]any)
	rules, _ := g["InboundRules"].([]any)
	for _, raw := range rules {
		rule, _ := raw.(map[string]any)
		members, _ := rule["SecurityGroupsMembers"].([]any)
		if len(members) == 0 {
			continue
		}
		member, _ := members[0].(map[string]any)
		return member
	}
	t.Fatalf("no inbound rule of the group names another group: %v", group)
	return nil
}

// destinations reads a route table's routes in the order it answered them.
func destinations(t *testing.T, table any) []string {
	t.Helper()
	m, _ := table.(map[string]any)
	routes, _ := m["Routes"].([]any)
	out := make([]string, 0, len(routes))
	for _, raw := range routes {
		r, _ := raw.(map[string]any)
		d, _ := r["DestinationIpRange"].(string)
		out = append(out, d)
	}
	return out
}
