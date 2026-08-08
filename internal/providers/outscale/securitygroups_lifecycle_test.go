package outscale_test

import (
	"net/http"
	"testing"
)

// The security-group lifecycle, held against the recorded creations of
// 2026-08-08 (X-2 lifecycle sweep against a real account — values invented
// here, shapes and conditionality measured there):
//
//   - a fresh non-default group is born with InboundRules PRESENT and empty,
//     and one outbound allow-all rule carrying its own sgr- id;
//   - a created rule comes back {FromPortRange, IpProtocol, IpRanges,
//     SecurityGroupRuleId, ToPortRange} and nothing else;
//   - a machine wears the groups it asked for, and rules follow the group.

func TestASecurityGroupLifecycleMatchesTheRecordedShapes(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	netCreated := call(t, ts, doc, "CreateNet", `{"IpRange":"10.20.0.0/16"}`)
	netID, _ := netCreated["Net"].(map[string]any)["NetId"].(string)

	created := call(t, ts, doc, "CreateSecurityGroup",
		`{"SecurityGroupName":"web","Description":"front row","NetId":"`+netID+`"}`)
	sg, _ := created["SecurityGroup"].(map[string]any)
	sgID, _ := sg["SecurityGroupId"].(string)

	// Measured: a fresh group's InboundRules is present and empty — not
	// omitted, which is what the pristine DEFAULT group's rule-level keys do.
	// The two conditionalities are different and both were recorded.
	inbound, present := sg["InboundRules"].([]any)
	if !present || len(inbound) != 0 {
		t.Fatalf("a fresh group should carry InboundRules []: %v", sg)
	}
	outbound, _ := sg["OutboundRules"].([]any)
	if len(outbound) != 1 {
		t.Fatalf("a fresh group carries one outbound allow-all rule: %v", sg)
	}
	outRule, _ := outbound[0].(map[string]any)
	if ranges, _ := outRule["IpRanges"].([]any); len(ranges) != 1 || ranges[0] != "0.0.0.0/0" {
		t.Fatalf("the outbound rule is not the recorded allow-all: %v", outRule)
	}
	if id, _ := outRule["SecurityGroupRuleId"].(string); len(id) < 5 {
		t.Fatalf("the outbound rule carries no sgr- id: %v", outRule)
	}

	// The flat rule form, as oapi-cli sends it. The response is the whole
	// group, rule included, in the recorded shape.
	withRule := call(t, ts, doc, "CreateSecurityGroupRule",
		`{"Flow":"Inbound","SecurityGroupId":"`+sgID+`","IpProtocol":"tcp","FromPortRange":22,"ToPortRange":22,"IpRange":"198.51.100.0/24"}`)
	rules, _ := withRule["SecurityGroup"].(map[string]any)["InboundRules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("the rule did not land: %v", withRule)
	}
	rule, _ := rules[0].(map[string]any)
	if rule["IpProtocol"] != "tcp" {
		t.Fatalf("not the rule that was asked: %v", rule)
	}
	if _, present := rule["SecurityGroupsMembers"]; present {
		t.Fatalf("a CIDR rule carries a members key the real cloud omits: %v", rule)
	}

	// The same rule again is a conflict, which a retrying client converges on.
	if status, body := post(t, ts, "CreateSecurityGroupRule",
		`{"Flow":"Inbound","SecurityGroupId":"`+sgID+`","IpProtocol":"tcp","FromPortRange":22,"ToPortRange":22,"IpRange":"198.51.100.0/24"}`); status != http.StatusConflict {
		t.Fatalf("a duplicate rule was accepted: %d %v", status, body)
	}

	// Deleting a rule is saying it again, not naming its id.
	call(t, ts, doc, "DeleteSecurityGroupRule",
		`{"Flow":"Inbound","SecurityGroupId":"`+sgID+`","IpProtocol":"tcp","FromPortRange":22,"ToPortRange":22,"IpRange":"198.51.100.0/24"}`)
	after := firstOf(t, call(t, ts, doc, "ReadSecurityGroups", `{"Filters":{"SecurityGroupIds":["`+sgID+`"]}}`), "SecurityGroups")
	if rules, _ := after["InboundRules"].([]any); len(rules) != 0 {
		t.Fatalf("the rule survived its delete: %v", after)
	}

	// A machine wears the group it asked for, and the view resolves the pair.
	_, _ = post(t, ts, "CreateSubnet", `{"NetId":"`+netID+`","IpRange":"10.20.1.0/24"}`)
	vmCreated := call(t, ts, doc, "CreateVms",
		`{"ImageId":"ami-00000001","VmType":"tinav6.c1r1p2","SecurityGroupIds":["`+sgID+`"]}`)
	vms, _ := vmCreated["Vms"].([]any)
	vm, _ := vms[0].(map[string]any)
	worn, _ := vm["SecurityGroups"].([]any)
	if len(worn) != 1 {
		t.Fatalf("the machine does not wear its group: %v", vm["SecurityGroups"])
	}
	pair, _ := worn[0].(map[string]any)
	if pair["SecurityGroupId"] != sgID || pair["SecurityGroupName"] != "web" {
		t.Fatalf("the worn group is not resolved to {id, name}: %v", pair)
	}
	vmID, _ := vm["VmId"].(string)

	// A worn group does not go; a released one does.
	if status, body := post(t, ts, "DeleteSecurityGroup", `{"SecurityGroupId":"`+sgID+`"}`); status != http.StatusConflict {
		t.Fatalf("a worn group was deleted: %d %v", status, body)
	}
	call(t, ts, doc, "DeleteVms", `{"VmIds":["`+vmID+`"]}`)
	call(t, ts, doc, "DeleteSecurityGroup", `{"SecurityGroupId":"`+sgID+`"}`)

	// The default group refuses by name what the Net's delete does silently.
	if status, body := post(t, ts, "DeleteSecurityGroup", `{"SecurityGroupName":"default"}`); status != http.StatusConflict {
		t.Fatalf("the default group was deleted directly: %d %v", status, body)
	}

	// An unknown group on a create is a refusal, not a machine without rules.
	if status, body := post(t, ts, "CreateVms",
		`{"ImageId":"ami-00000001","VmType":"tinav6.c1r1p2","SecurityGroupIds":["sg-00000000"]}`); status == http.StatusOK {
		t.Fatalf("an unknown security group was accepted on a create: %v", body)
	}

	// A duplicate name in the same scope is a conflict, which
	// create-before-destroy depends on.
	call(t, ts, doc, "CreateSecurityGroup", `{"SecurityGroupName":"web","Description":"again","NetId":"`+netID+`"}`)
	if status, body := post(t, ts, "CreateSecurityGroup",
		`{"SecurityGroupName":"web","Description":"twice","NetId":"`+netID+`"}`); status != http.StatusConflict {
		t.Fatalf("a duplicate group name was accepted: %d %v", status, body)
	}

	// The group-sourced rule form, member in the recorded shape: AccountId and
	// SecurityGroupId, no name.
	source := call(t, ts, doc, "CreateSecurityGroup",
		`{"SecurityGroupName":"peers","Description":"the members","NetId":"`+netID+`"}`)
	target := firstOf(t, call(t, ts, doc, "ReadSecurityGroups", `{"Filters":{"SecurityGroupNames":["web"],"NetIds":["`+netID+`"]}}`), "SecurityGroups")
	targetID, _ := target["SecurityGroupId"].(string)
	linked := call(t, ts, doc, "CreateSecurityGroupRule",
		`{"Flow":"Inbound","SecurityGroupId":"`+targetID+`","SecurityGroupNameToLink":"peers"}`)
	linkedRules, _ := linked["SecurityGroup"].(map[string]any)["InboundRules"].([]any)
	linkedRule, _ := linkedRules[0].(map[string]any)
	members, _ := linkedRule["SecurityGroupsMembers"].([]any)
	if len(members) != 1 {
		t.Fatalf("the group-sourced rule carries no member: %v", linkedRule)
	}
	member, _ := members[0].(map[string]any)
	sourceID, _ := source["SecurityGroup"].(map[string]any)["SecurityGroupId"].(string)
	if member["SecurityGroupId"] != sourceID {
		t.Fatalf("the member does not name the linked group: %v", member)
	}
	if _, present := member["SecurityGroupName"]; present {
		t.Fatalf("the member carries a name key the recorded member omits: %v", member)
	}
}
