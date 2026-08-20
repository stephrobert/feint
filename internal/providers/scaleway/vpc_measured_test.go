package scaleway_test

import (
	"net/http"
	"testing"
)

// What a real fr-par account answered on 2026-08-20, held against this pack.
//
// The recording behind these assertions is in shapes/scaleway.json (paths and
// types) and in the comments beside the code they hold (the values, which a
// shape catalogue deliberately does not keep). Each one replaced something this
// emulator had inferred: the create status was 201 because every other create
// in the pack is, and the two Object Storage flags were absent because the
// recording that would have arbitrated them was taken on an account with no VPC
// and no Private Network, so neither object's fields were ever observed.

// Both vpc/v2 creates answer 200, which is what the wire carried.
//
// Not 201. The two were read off a `feint proxy` transcript of a real account,
// because neither `scw` nor the Terraform provider shows a status: both accept
// any 2xx, which is exactly why this could sit wrong indefinitely.
//
// Only these two operations are asserted. CreateRoute is vpc/v2 as well and was
// not measured — nothing was created on that account beyond the two free
// objects — so it keeps the pack's 201 rather than inheriting a claim from its
// neighbours.
func TestTheVpcCreatesAnswerWhatTheRealCloudAnswers(t *testing.T) {
	ts := newTestServer(t)

	status, vpc := do(t, ts, "POST", vpcRegion+"/vpcs", `{"name":"measured"}`)
	if status != http.StatusOK {
		t.Errorf("CreateVPC answered %d, and the real cloud answered 200 (%v)", status, vpc)
	}
	status, pn := do(t, ts, "POST", vpcRegion+"/private-networks", `{"name":"measured-pn"}`)
	if status != http.StatusOK {
		t.Errorf("CreatePrivateNetwork answered %d, and the real cloud answered 200 (%v)", status, pn)
	}
}

// The two Object Storage flags are served, on every door.
//
// They are false and can only be false here: the five operations that attach a
// private network to Object Storage are declined in pack.go with their reason.
// That is not an argument for omitting them — a client reading
// `has_s3_integration` off a decoded object gets the zero value either way, and
// a client comparing field sets does not. The contract has always declared
// both; nothing observed them until a Private Network and a VPC existed at the
// moment of a recording.
//
// Every door, because the emulator has more than one: a create, a read, and a
// list, and the list is the one a previous omission survived in.
func TestTheObjectStorageFlagsAreServedOnEveryDoor(t *testing.T) {
	ts := newTestServer(t)

	_, createdVPC := do(t, ts, "POST", vpcRegion+"/vpcs", `{"name":"flagged"}`)
	vpcID, _ := createdVPC["id"].(string)
	if vpcID == "" {
		t.Fatalf("no vpc: %v", createdVPC)
	}
	pnID, createdPN := privateNetwork(t, ts, `{"name":"flagged-pn","vpc_id":"`+vpcID+`"}`)

	present := func(what string, body map[string]any, field string) {
		t.Helper()
		value, carried := body[field]
		if !carried {
			t.Errorf("%s carries no %s, and the real cloud carries it on every answer", what, field)
			return
		}
		if value != false {
			t.Errorf("%s answers %s=%v; nothing here can attach an Object Storage endpoint", what, field, value)
		}
	}

	present("CreateVPC", createdVPC, "s3_integration_enabled")
	present("CreatePrivateNetwork", createdPN, "has_s3_integration")

	_, readVPC := do(t, ts, "GET", vpcRegion+"/vpcs/"+vpcID, "")
	present("GetVPC", readVPC, "s3_integration_enabled")
	_, readPN := do(t, ts, "GET", vpcRegion+"/private-networks/"+pnID, "")
	present("GetPrivateNetwork", readPN, "has_s3_integration")

	_, listedVPCs := do(t, ts, "GET", vpcRegion+"/vpcs", "")
	vpcs, _ := listedVPCs["vpcs"].([]any)
	if len(vpcs) == 0 {
		t.Fatalf("ListVPCs answered none: %v", listedVPCs)
	}
	for _, raw := range vpcs {
		entry, _ := raw.(map[string]any)
		present("ListVPCs", entry, "s3_integration_enabled")
	}

	_, listedPNs := do(t, ts, "GET", vpcRegion+"/private-networks", "")
	networks, _ := listedPNs["private_networks"].([]any)
	if len(networks) == 0 {
		t.Fatalf("ListPrivateNetworks answered none: %v", listedPNs)
	}
	for _, raw := range networks {
		entry, _ := raw.(map[string]any)
		present("ListPrivateNetworks", entry, "has_s3_integration")
	}
}
