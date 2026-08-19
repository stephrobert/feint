package outscale_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The LBU family, held to what the surveyed stacks measured (#281): create,
// read, update (health check), register/unlink backends, delete. Every success
// shape goes through call(), so it is validated against the contract; the
// refusals assert the error algebra the providers branch on.

func aLoadBalancer(t *testing.T, ts *httptest.Server, name, body string) map[string]any {
	t.Helper()
	doc := contractDoc(t)
	out := call(t, ts, doc, "CreateLoadBalancer", body)
	lb, ok := out["LoadBalancer"].(map[string]any)
	if !ok {
		t.Fatalf("no LoadBalancer in %v", out)
	}
	if got, _ := lb["LoadBalancerName"].(string); got != name {
		t.Fatalf("created %q, want %q", got, name)
	}
	return lb
}

func lbCreateBody(subnetID, sgID string) string {
	return `{"LoadBalancerName":"two-tier-public-lb",` +
		`"Listeners":[{"BackendPort":80,"BackendProtocol":"TCP","LoadBalancerPort":80,"LoadBalancerProtocol":"TCP"}],` +
		`"Subnets":["` + subnetID + `"],"SecurityGroups":["` + sgID + `"],` +
		`"LoadBalancerType":"internet-facing",` +
		`"Tags":[{"Key":"name","Value":"two-tier-public-lb"}]}`
}

func aSecurityGroup(t *testing.T, ts *httptest.Server, netID, name string) string {
	t.Helper()
	_, out := post(t, ts, "CreateSecurityGroup",
		`{"NetId":"`+netID+`","SecurityGroupName":"`+name+`","Description":"lb"}`)
	sg, _ := out["SecurityGroup"].(map[string]any)
	id, _ := sg["SecurityGroupId"].(string)
	if id == "" {
		t.Fatalf("no SecurityGroupId in %v", out)
	}
	return id
}

// The create-then-read round-trip, field for field: whatever the create
// answered, the read answers identically — the single most common cause of
// "Provider produced inconsistent result after apply".
func TestALoadBalancerRoundTrips(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)
	netID, subnetID := netAndSubnet(t, ts, "10.51.0.0/16", "10.51.1.0/24")
	sgID := aSecurityGroup(t, ts, netID, "sg_public_lb")

	created := aLoadBalancer(t, ts, "two-tier-public-lb", lbCreateBody(subnetID, sgID))

	// The measured DnsName format: <name>-<digits>.<region>.lbu.outscale.com.
	dns, _ := created["DnsName"].(string)
	if !strings.HasPrefix(dns, "two-tier-public-lb-") || !strings.HasSuffix(dns, ".eu-west-2.lbu.outscale.com") {
		t.Fatalf("DnsName = %q, want the measured <name>-<digits>.<region>.lbu.outscale.com", dns)
	}
	if created["NetId"] != netID {
		t.Errorf("NetId = %v, want %s", created["NetId"], netID)
	}
	if created["State"] != "active" {
		t.Errorf("State = %v; a local emulator lingering in provisioning only makes clients wait", created["State"])
	}
	if _, ok := created["PublicIp"].(string); !ok {
		t.Error("an internet-facing load balancer carries a PublicIp")
	}
	// "The primary private IP of the load balancer": the real cloud's recorded
	// answers carry it (the omission gate caught its absence), and it comes
	// from the subnet's own pool.
	if ip, _ := created["PrivateIp"].(string); !strings.HasPrefix(ip, "10.51.1.") {
		t.Errorf("PrivateIp = %q, want an address of the balancer's subnet", created["PrivateIp"])
	}
	// The pristine health check: TCP on the first listener's backend port.
	hc, _ := created["HealthCheck"].(map[string]any)
	if hc["Protocol"] != "TCP" || hc["Port"] != float64(80) {
		t.Errorf("default HealthCheck = %v", hc)
	}

	out := call(t, ts, doc, "ReadLoadBalancers",
		`{"Filters":{"LoadBalancerNames":["two-tier-public-lb"]}}`)
	lbs, _ := out["LoadBalancers"].([]any)
	if len(lbs) != 1 {
		t.Fatalf("read back %d load balancers, want 1", len(lbs))
	}
	got := lbs[0].(map[string]any)
	for _, field := range []string{"DnsName", "LoadBalancerName", "LoadBalancerType", "NetId", "PublicIp", "SecuredCookies", "State"} {
		if got[field] != created[field] {
			t.Errorf("%s changed between create and read: %v != %v", field, got[field], created[field])
		}
	}
	// A second read answers the same DnsName: anything Terraform stores must
	// be deterministic.
	again := call(t, ts, doc, "ReadLoadBalancers", `{}`)
	lbs2, _ := again["LoadBalancers"].([]any)
	if lbs2[0].(map[string]any)["DnsName"] != dns {
		t.Error("DnsName changed between two reads")
	}
}

// An internal load balancer's DNS name carries the internal- prefix, measured
// on a real account (internal-talos-prod-k8s-lb-640339891.eu-west-2...), and
// no public address.
func TestAnInternalLoadBalancerHasNoPublicFace(t *testing.T) {
	ts := newServer(t)
	netID, subnetID := netAndSubnet(t, ts, "10.52.0.0/16", "10.52.1.0/24")
	sgID := aSecurityGroup(t, ts, netID, "sg_internal_lb")

	lb := aLoadBalancer(t, ts, "two-tier-internal-lb",
		`{"LoadBalancerName":"two-tier-internal-lb",`+
			`"Listeners":[{"BackendPort":80,"BackendProtocol":"TCP","LoadBalancerPort":80,"LoadBalancerProtocol":"TCP"}],`+
			`"Subnets":["`+subnetID+`"],"SecurityGroups":["`+sgID+`"],"LoadBalancerType":"internal"}`)

	if dns, _ := lb["DnsName"].(string); !strings.HasPrefix(dns, "internal-two-tier-internal-lb-") {
		t.Errorf("DnsName = %q, want the measured internal- prefix", dns)
	}
	if _, present := lb["PublicIp"]; present {
		t.Error("an internal load balancer answered with a PublicIp")
	}
}

// Register, read back, unlink: the outscale_load_balancer_vms lifecycle. The
// provider drops the resource from state when BackendVmIds comes back empty,
// so the ids must round-trip.
func TestBackendVmsRegisterAndUnlink(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)
	netID, subnetID := netAndSubnet(t, ts, "10.53.0.0/16", "10.53.1.0/24")
	sgID := aSecurityGroup(t, ts, netID, "sg_lb")

	created := call(t, ts, doc, "CreateVms",
		`{"ImageId":"ami-00000001","VmType":"tinav6.c1r1","SubnetId":"`+subnetID+`"}`)
	vms, _ := created["Vms"].([]any)
	vmID, _ := vms[0].(map[string]any)["VmId"].(string)

	aLoadBalancer(t, ts, "two-tier-public-lb", lbCreateBody(subnetID, sgID))

	call(t, ts, doc, "RegisterVmsInLoadBalancer",
		`{"LoadBalancerName":"two-tier-public-lb","BackendVmIds":["`+vmID+`","`+vmID+`"]}`)

	out := call(t, ts, doc, "ReadLoadBalancers", `{}`)
	lb := out["LoadBalancers"].([]any)[0].(map[string]any)
	ids, _ := lb["BackendVmIds"].([]any)
	// "Specifying the same ID several times has no effect" (SDK): once, not twice.
	if len(ids) != 1 || ids[0] != vmID {
		t.Fatalf("BackendVmIds = %v, want exactly [%s]", ids, vmID)
	}

	call(t, ts, doc, "UnlinkLoadBalancerBackendMachines",
		`{"LoadBalancerName":"two-tier-public-lb","BackendVmIds":["`+vmID+`"]}`)
	out = call(t, ts, doc, "ReadLoadBalancers", `{}`)
	lb = out["LoadBalancers"].([]any)[0].(map[string]any)
	if ids, _ := lb["BackendVmIds"].([]any); len(ids) != 0 {
		t.Fatalf("BackendVmIds = %v after unlink, want empty", ids)
	}

	// Provider 1.8.0 attaches through LinkLoadBalancerBackendMachines instead —
	// measured on ztiac, where the 1.1.3 source reading said Register. Both
	// spellings must land the same backend.
	call(t, ts, doc, "LinkLoadBalancerBackendMachines",
		`{"LoadBalancerName":"two-tier-public-lb","BackendVmIds":["`+vmID+`"]}`)
	out = call(t, ts, doc, "ReadLoadBalancers", `{}`)
	lb = out["LoadBalancers"].([]any)[0].(map[string]any)
	if ids, _ := lb["BackendVmIds"].([]any); len(ids) != 1 {
		t.Fatalf("BackendVmIds = %v after link, want one", ids)
	}

	if status, _ := post(t, ts, "RegisterVmsInLoadBalancer",
		`{"LoadBalancerName":"two-tier-public-lb","BackendVmIds":["vm-does-not-exist"]}`); status != http.StatusBadRequest {
		t.Errorf("registering a Vm that does not exist answered %d, want 400", status)
	}
}

// The outscale_load_balancer_attributes path: UpdateLoadBalancer carries the
// health check, and the next read serves it back.
func TestUpdateLoadBalancerReplacesTheHealthCheck(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)
	netID, subnetID := netAndSubnet(t, ts, "10.54.0.0/16", "10.54.1.0/24")
	sgID := aSecurityGroup(t, ts, netID, "sg_lb")
	aLoadBalancer(t, ts, "two-tier-public-lb", lbCreateBody(subnetID, sgID))

	out := call(t, ts, doc, "UpdateLoadBalancer",
		`{"LoadBalancerName":"two-tier-public-lb",`+
			`"HealthCheck":{"HealthyThreshold":2,"CheckInterval":30,"Port":80,"Protocol":"HTTP","Path":"/healthz","Timeout":5,"UnhealthyThreshold":5}}`)
	lb, _ := out["LoadBalancer"].(map[string]any)
	hc, _ := lb["HealthCheck"].(map[string]any)
	if hc["Protocol"] != "HTTP" || hc["Path"] != "/healthz" || hc["HealthyThreshold"] != float64(2) {
		t.Fatalf("HealthCheck after update = %v", hc)
	}

	// The SDK's own ranges refuse, rather than storing what the real API would
	// reject: interval 5-600, thresholds 2-10, timeout 2-60.
	for _, bad := range []string{
		`{"HealthyThreshold":1,"CheckInterval":30,"Port":80,"Protocol":"TCP","Timeout":5,"UnhealthyThreshold":5}`,
		`{"HealthyThreshold":2,"CheckInterval":4,"Port":80,"Protocol":"TCP","Timeout":5,"UnhealthyThreshold":5}`,
		`{"HealthyThreshold":2,"CheckInterval":30,"Port":80,"Protocol":"ICMP","Timeout":5,"UnhealthyThreshold":5}`,
		`{"HealthyThreshold":2,"CheckInterval":30,"Port":80,"Protocol":"TCP","Path":"/x","Timeout":5,"UnhealthyThreshold":5}`,
	} {
		status, _ := post(t, ts, "UpdateLoadBalancer",
			`{"LoadBalancerName":"two-tier-public-lb","HealthCheck":`+bad+`}`)
		if status != http.StatusBadRequest {
			t.Errorf("HealthCheck %s answered %d, want 400", bad, status)
		}
	}
}

// Delete leaves nothing: the provider polls ReadLoadBalancers until the name
// is gone, and the first poll must already answer none.
func TestADeletedLoadBalancerIsGoneAtOnce(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)
	netID, subnetID := netAndSubnet(t, ts, "10.55.0.0/16", "10.55.1.0/24")
	sgID := aSecurityGroup(t, ts, netID, "sg_lb")
	aLoadBalancer(t, ts, "two-tier-public-lb", lbCreateBody(subnetID, sgID))

	call(t, ts, doc, "DeleteLoadBalancer", `{"LoadBalancerName":"two-tier-public-lb"}`)
	out := call(t, ts, doc, "ReadLoadBalancers",
		`{"Filters":{"LoadBalancerNames":["two-tier-public-lb"]}}`)
	if lbs, _ := out["LoadBalancers"].([]any); len(lbs) != 0 {
		t.Fatalf("a deleted load balancer still reads back: %v", lbs)
	}
	if status, _ := post(t, ts, "DeleteLoadBalancer",
		`{"LoadBalancerName":"two-tier-public-lb"}`); status != http.StatusBadRequest {
		t.Error("deleting a load balancer twice did not answer the not-found shape")
	}
}

// The refusal algebra: a duplicate name conflicts, a name the SDK forbids is
// refused, and the resources a balancer stands on cannot be deleted under it.
func TestLoadBalancerRefusals(t *testing.T) {
	ts := newServer(t)
	netID, subnetID := netAndSubnet(t, ts, "10.56.0.0/16", "10.56.1.0/24")
	sgID := aSecurityGroup(t, ts, netID, "sg_lb")
	aLoadBalancer(t, ts, "two-tier-public-lb", lbCreateBody(subnetID, sgID))

	if status, _ := post(t, ts, "CreateLoadBalancer", lbCreateBody(subnetID, sgID)); status != http.StatusConflict {
		t.Errorf("a duplicate name answered %d, want 409", status)
	}
	for _, name := range []string{"", "-leading", "trailing-", "under_score", strings.Repeat("a", 33)} {
		status, _ := post(t, ts, "CreateLoadBalancer",
			`{"LoadBalancerName":"`+name+`","Listeners":[{"BackendPort":80,"LoadBalancerPort":80,"LoadBalancerProtocol":"TCP"}],"Subnets":["`+subnetID+`"]}`)
		if status != http.StatusBadRequest {
			t.Errorf("name %q answered %d, want 400", name, status)
		}
	}

}

// The subnet under a balancer does not delete: the real API refuses, and
// destroy ordering relies on the refusal. Fails without the guard in
// deleteSubnet.
func TestASubnetDoesNotDeleteUnderALoadBalancer(t *testing.T) {
	ts := newServer(t)
	netID, subnetID := netAndSubnet(t, ts, "10.58.0.0/16", "10.58.1.0/24")
	sgID := aSecurityGroup(t, ts, netID, "sg_lb")
	aLoadBalancer(t, ts, "two-tier-public-lb", lbCreateBody(subnetID, sgID))

	if status, _ := post(t, ts, "DeleteSubnet", `{"SubnetId":"`+subnetID+`"}`); status != http.StatusConflict {
		t.Errorf("DeleteSubnet under a load balancer answered %d, want 409", status)
	}
	if status, _ := post(t, ts, "DeleteLoadBalancer", `{"LoadBalancerName":"two-tier-public-lb"}`); status != http.StatusOK {
		t.Fatal("could not delete the load balancer")
	}
	if status, _ := post(t, ts, "DeleteSubnet", `{"SubnetId":"`+subnetID+`"}`); status != http.StatusOK {
		t.Error("the subnet is still blocked after its balancer went")
	}
}

// The security group a balancer carries does not delete either — the sweep the
// provider runs before each group delete exists because of this refusal. Fails
// without the guard in deleteSecurityGroup.
func TestASecurityGroupDoesNotDeleteUnderALoadBalancer(t *testing.T) {
	ts := newServer(t)
	netID, subnetID := netAndSubnet(t, ts, "10.59.0.0/16", "10.59.1.0/24")
	sgID := aSecurityGroup(t, ts, netID, "sg_lb")
	aLoadBalancer(t, ts, "two-tier-public-lb", lbCreateBody(subnetID, sgID))

	if status, _ := post(t, ts, "DeleteSecurityGroup", `{"SecurityGroupId":"`+sgID+`"}`); status != http.StatusConflict {
		t.Errorf("DeleteSecurityGroup under a load balancer answered %d, want 409", status)
	}
	if status, _ := post(t, ts, "DeleteLoadBalancer", `{"LoadBalancerName":"two-tier-public-lb"}`); status != http.StatusOK {
		t.Fatal("could not delete the load balancer")
	}
	if status, _ := post(t, ts, "DeleteSecurityGroup", `{"SecurityGroupId":"`+sgID+`"}`); status != http.StatusOK {
		t.Error("the security group is still blocked after its balancer went")
	}
}

// What is deliberately not served answers a refusal that names the line, not a
// silent 200 that stores what nothing can read back.
func TestLoadBalancerDeclinedParameters(t *testing.T) {
	ts := newServer(t)
	netID, subnetID := netAndSubnet(t, ts, "10.57.0.0/16", "10.57.1.0/24")
	sgID := aSecurityGroup(t, ts, netID, "sg_lb")

	// The public-Cloud form: no surveyed stack takes it, docs/limits.md
	// carries the statement.
	status, _ := post(t, ts, "CreateLoadBalancer",
		`{"LoadBalancerName":"public-cloud-lb","Listeners":[{"BackendPort":80,"LoadBalancerPort":80,"LoadBalancerProtocol":"TCP"}],"SubregionNames":["eu-west-2a"]}`)
	if status != http.StatusBadRequest {
		t.Errorf("the public-Cloud form answered %d, want a refusal naming the line", status)
	}

	aLoadBalancer(t, ts, "two-tier-public-lb", lbCreateBody(subnetID, sgID))
	// Access logs need an OOS bucket nothing here can hold.
	status, _ = post(t, ts, "UpdateLoadBalancer",
		`{"LoadBalancerName":"two-tier-public-lb","AccessLog":{"IsEnabled":true,"OsuBucketName":"logs"}}`)
	if status != http.StatusBadRequest {
		t.Errorf("enabling access logs answered %d, want 400", status)
	}
	// A named policy references CreateLoadBalancerPolicy, which is declined —
	// but the empty list is what the provider's own update path sends.
	status, _ = post(t, ts, "UpdateLoadBalancer",
		`{"LoadBalancerName":"two-tier-public-lb","PolicyNames":["sticky"]}`)
	if status != http.StatusBadRequest {
		t.Errorf("a named policy answered %d, want 400", status)
	}
	status, _ = post(t, ts, "UpdateLoadBalancer",
		`{"LoadBalancerName":"two-tier-public-lb","PolicyNames":[]}`)
	if status != http.StatusOK {
		t.Errorf("the provider's own empty PolicyNames answered %d, want 200", status)
	}
}
