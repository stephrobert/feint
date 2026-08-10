package exoscale_test

import (
	"fmt"
	"net/http"
	"testing"
)

// Security groups, anti-affinity groups and elastic IPs, asserted against the
// shapes and omission rules the real API was measured answering on 2026-08-10.

// A fresh account already holds the default security group, and an instance
// created without naming any wears it — both measured, neither guessable from
// the API description.
func TestAFreshAccountHoldsTheDefaultSecurityGroup(t *testing.T) {
	h := serve(t)

	_, listed := call(t, h, "GET", "/v2/security-group", "")
	groups, _ := listed["security-groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("a fresh account holds %d groups, want the default one alone: %v", len(groups), listed)
	}
	group, _ := groups[0].(map[string]any)
	if group["name"] != "default" {
		t.Fatalf("the pre-existing group is %v, want name default", group)
	}
	// rules is an omitted key when empty, never an empty array: the measured
	// default group carries none.
	if _, present := group["rules"]; present {
		t.Fatalf("an empty rules key is present: %v", group)
	}

	id := createDemo(t, h)
	_, instance := call(t, h, "GET", "/v2/instance/"+id, "")
	worn, _ := instance["security-groups"].([]any)
	if len(worn) != 1 {
		t.Fatalf("an instance created without groups wears %v, want the default one", worn)
	}
	member, _ := worn[0].(map[string]any)
	if member["id"] != group["id"] {
		t.Fatalf("the instance wears %v, the default group is %v", member, group)
	}
	// Bare {id}, the measured member shape: the CLI resolves names itself.
	if len(member) != 1 {
		t.Fatalf("the member reference is not bare: %v", member)
	}
}

func TestSecurityGroupRulesRoundTrip(t *testing.T) {
	h := serve(t)
	rec, op := call(t, h, "POST", "/v2/security-group", `{"name": "web", "description": "front"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create answered %d: %s", rec.Code, rec.Body.String())
	}
	ref, _ := op["reference"].(map[string]any)
	sgID, _ := ref["id"].(string)
	if ref["command"] != "get-security-group" || sgID == "" {
		t.Fatalf("the operation does not refer to the group's read: %v", op)
	}

	rec, _ = call(t, h, "POST", "/v2/security-group/"+sgID+"/rules",
		`{"flow-direction": "ingress", "protocol": "tcp", "network": "203.0.113.0/24", "start-port": 22, "end-port": 22, "description": "ssh"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rule add answered %d: %s", rec.Code, rec.Body.String())
	}

	_, group := call(t, h, "GET", "/v2/security-group/"+sgID, "")
	rules, _ := group["rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("the rule did not land: %v", group)
	}
	rule, _ := rules[0].(map[string]any)
	ruleID, _ := rule["id"].(string)
	if rule["flow-direction"] != "ingress" || rule["start-port"] != float64(22) || ruleID == "" {
		t.Fatalf("the rule came back wrong: %v", rule)
	}

	rec, _ = call(t, h, "DELETE", "/v2/security-group/"+sgID+"/rules/"+ruleID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("rule delete answered %d", rec.Code)
	}
	_, group = call(t, h, "GET", "/v2/security-group/"+sgID, "")
	if _, present := group["rules"]; present {
		t.Fatalf("an empty rules key survived the delete: %v", group)
	}
}

// The two refusals of the delete path: the default group, and a group an
// instance still wears. Both would otherwise leave a client naming a ghost.
func TestSecurityGroupDeleteRefusals(t *testing.T) {
	h := serve(t)
	_, listed := call(t, h, "GET", "/v2/security-group", "")
	groups, _ := listed["security-groups"].([]any)
	defaultGroup, _ := groups[0].(map[string]any)
	defaultID, _ := defaultGroup["id"].(string)

	rec, _ := call(t, h, "DELETE", "/v2/security-group/"+defaultID, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("deleting the default group answered %d, want 400", rec.Code)
	}

	instanceID := createDemo(t, h)
	rec, op := call(t, h, "POST", "/v2/security-group", `{"name": "worn"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create answered %d", rec.Code)
	}
	ref, _ := op["reference"].(map[string]any)
	sgID, _ := ref["id"].(string)

	rec, _ = call(t, h, "PUT", "/v2/security-group/"+sgID+":attach", `{"instance": {"id": "`+instanceID+`"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("attach answered %d: %s", rec.Code, rec.Body.String())
	}
	rec, _ = call(t, h, "DELETE", "/v2/security-group/"+sgID, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("deleting a worn group answered %d, want 400", rec.Code)
	}

	rec, _ = call(t, h, "PUT", "/v2/security-group/"+sgID+":detach", `{"instance": {"id": "`+instanceID+`"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("detach answered %d", rec.Code)
	}
	rec, _ = call(t, h, "DELETE", "/v2/security-group/"+sgID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete after detach answered %d", rec.Code)
	}
}

// Membership is computed, never stored on the group: the measured view carries
// instances always — empty on a group nobody wears, bare {id} per member.
func TestAntiAffinityGroupMembershipIsComputed(t *testing.T) {
	h := serve(t)
	rec, op := call(t, h, "POST", "/v2/anti-affinity-group", `{"name": "spread", "description": "probe"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create answered %d: %s", rec.Code, rec.Body.String())
	}
	ref, _ := op["reference"].(map[string]any)
	aagID, _ := ref["id"].(string)

	_, group := call(t, h, "GET", "/v2/anti-affinity-group/"+aagID, "")
	members, present := group["instances"].([]any)
	if !present || len(members) != 0 {
		t.Fatalf("a fresh group must carry an empty instances array: %v", group)
	}

	rec, _ = call(t, h, "POST", "/v2/instance", `{
		"name": "spread-1",
		"instance-type": {"id": "21624abb-764e-4def-81d7-9fc54b5957fb"},
		"template": {"id": "11111111-1111-4111-8111-111111111111"},
		"disk-size": 10,
		"anti-affinity-groups": [{"id": "`+aagID+`"}]
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create with group answered %d: %s", rec.Code, rec.Body.String())
	}

	_, group = call(t, h, "GET", "/v2/anti-affinity-group/"+aagID, "")
	members, _ = group["instances"].([]any)
	if len(members) != 1 {
		t.Fatalf("the member did not land: %v", group)
	}

	// The instance's own view resolves the group to the measured member shape,
	// {description, id, name}.
	_, listed := call(t, h, "GET", "/v2/instance", "")
	instances, _ := listed["instances"].([]any)
	instance, _ := instances[len(instances)-1].(map[string]any)
	refs, _ := instance["anti-affinity-groups"].([]any)
	if len(refs) != 1 {
		t.Fatalf("the instance does not carry its group: %v", instance)
	}
	carried, _ := refs[0].(map[string]any)
	if carried["name"] != "spread" || carried["description"] != "probe" {
		t.Fatalf("the carried group is not resolved: %v", carried)
	}
}

// Elastic IPs: distinct addresses, attachment published on the instance, and
// withdrawal on detach and on delete.
func TestElasticIPAttachmentIsPublishedAndWithdrawn(t *testing.T) {
	h := serve(t)
	instanceID := createDemo(t, h)

	create := func() (id, ip string) {
		t.Helper()
		rec, op := call(t, h, "POST", "/v2/elastic-ip", `{"description": "probe"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("create answered %d: %s", rec.Code, rec.Body.String())
		}
		ref, _ := op["reference"].(map[string]any)
		id, _ = ref["id"].(string)
		_, eip := call(t, h, "GET", "/v2/elastic-ip/"+id, "")
		ip, _ = eip["ip"].(string)
		if cidr, _ := eip["cidr"].(string); cidr != ip+"/32" {
			t.Fatalf("cidr %q does not carry the address %q", cidr, ip)
		}
		return id, ip
	}
	firstID, firstIP := create()
	_, secondIP := create()
	if firstIP == secondIP {
		t.Fatalf("two elastic IPs share %s — the fixed-address defect, reintroduced", firstIP)
	}

	rec, op := call(t, h, "PUT", "/v2/elastic-ip/"+firstID+":attach", `{"instance": {"id": "`+instanceID+`"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("attach answered %d: %s", rec.Code, rec.Body.String())
	}
	// The operation refers to the elastic IP, not the instance: that is what
	// the recording shows the polling client waiting on.
	ref, _ := op["reference"].(map[string]any)
	if ref["command"] != "get-elastic-ip" || ref["id"] != firstID {
		t.Fatalf("attach referred to %v", ref)
	}

	_, instance := call(t, h, "GET", "/v2/instance/"+instanceID, "")
	attached, _ := instance["elastic-ips"].([]any)
	if len(attached) != 1 {
		t.Fatalf("the attachment is not on the instance: %v", instance["elastic-ips"])
	}
	// Bare {id}: the live API adds ip beside it, their elastic-ip-ref schema
	// does not declare it, and the contract check enforces the schema — see
	// elasticIPRefs. The CLI resolves the address with its own read.
	entry, _ := attached[0].(map[string]any)
	if entry["id"] != firstID || len(entry) != 1 {
		t.Fatalf("the instance publishes %v, want the bare reference {id: %s}", entry, firstID)
	}

	// Deleting the attached IP withdraws it from the instance: a view naming a
	// deleted IP would be published by nothing.
	rec, _ = call(t, h, "DELETE", "/v2/elastic-ip/"+firstID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete answered %d", rec.Code)
	}
	_, instance = call(t, h, "GET", "/v2/instance/"+instanceID, "")
	attached, _ = instance["elastic-ips"].([]any)
	if len(attached) != 0 {
		t.Fatalf("a deleted elastic IP survives on the instance: %v", attached)
	}
}

// The pool returns a deleted address, so a long session cannot exhaust it.
func TestElasticIPPoolRecyclesAddresses(t *testing.T) {
	h := serve(t)
	rec, op := call(t, h, "POST", "/v2/elastic-ip", "{}")
	if rec.Code != http.StatusOK {
		t.Fatalf("create answered %d", rec.Code)
	}
	ref, _ := op["reference"].(map[string]any)
	firstID := fmt.Sprint(ref["id"])
	_, eip := call(t, h, "GET", "/v2/elastic-ip/"+firstID, "")
	firstIP := eip["ip"]

	call(t, h, "DELETE", "/v2/elastic-ip/"+firstID, "")
	rec, op = call(t, h, "POST", "/v2/elastic-ip", "{}")
	if rec.Code != http.StatusOK {
		t.Fatalf("second create answered %d", rec.Code)
	}
	ref, _ = op["reference"].(map[string]any)
	_, eip = call(t, h, "GET", "/v2/elastic-ip/"+fmt.Sprint(ref["id"]), "")
	if eip["ip"] != firstIP {
		t.Fatalf("the freed address %v did not return to the pool, got %v", firstIP, eip["ip"])
	}
}
