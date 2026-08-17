package outscale_test

import "testing"

// TestTagsReachEveryResourceTheEmulatorServes drives the defect #99 reported:
// CreateTags answered "the resource igw-… does not exist" about a resource the
// emulator had just created and was serving, because the prefix table had
// never grown past the four kinds the pack had when it was written.
//
// The table checks in tags_internal_test.go prove the tables are complete; this
// proves they are true, because a row naming a kind the store never holds would
// satisfy those checks and still fail a client.
func TestTagsReachEveryResourceTheEmulatorServes(t *testing.T) {
	ts := newServer(t)

	id := func(action, body, envelope, field string) string {
		t.Helper()
		_, out := post(t, ts, action, body)
		obj, _ := out[envelope].(map[string]any)
		got, _ := obj[field].(string)
		if got == "" {
			t.Fatalf("%s returned no %s: %v", action, field, out)
		}
		return got
	}

	net := id("CreateNet", `{"IpRange":"10.71.0.0/16"}`, "Net", "NetId")
	subnet := id("CreateSubnet", `{"NetId":"`+net+`","IpRange":"10.71.1.0/24"}`, "Subnet", "SubnetId")
	vol := id("CreateVolume", `{"SubregionName":"eu-west-2a","Size":10}`, "Volume", "VolumeId")

	for _, resourceID := range []string{
		net,
		subnet,
		vol,
		id("CreateInternetService", `{}`, "InternetService", "InternetServiceId"),
		id("CreateRouteTable", `{"NetId":"`+net+`"}`, "RouteTable", "RouteTableId"),
		id("CreateSecurityGroup",
			`{"NetId":"`+net+`","SecurityGroupName":"tagged","Description":"tagged"}`,
			"SecurityGroup", "SecurityGroupId"),
		id("CreatePublicIp", `{}`, "PublicIp", "PublicIpId"),
		id("CreateNic", `{"SubnetId":"`+subnet+`"}`, "Nic", "NicId"),
		id("CreateSnapshot", `{"VolumeId":"`+vol+`"}`, "Snapshot", "SnapshotId"),
	} {
		status, out := post(t, ts, "CreateTags",
			`{"ResourceIds":["`+resourceID+`"],"Tags":[{"Key":"Name","Value":"x"}]}`)
		if status != 200 {
			t.Errorf("CreateTags on %s answered %d: %v", resourceID, status, out["Errors"])
		}
	}
}

// TestReadTagsPublishesTheTypesTheSDKDeclares is the wire half of the second
// defect: the pack published ResourceType "net" and "vm", where Outscale's own
// enum says "vpc" and "instance". A client filtering ReadTags on `instance`
// matched nothing, and no contract could see it — their OpenAPI declares
// ResourceType as a bare string.
func TestReadTagsPublishesTheTypesTheSDKDeclares(t *testing.T) {
	ts := newServer(t)

	_, out := post(t, ts, "CreateNet", `{"IpRange":"10.72.0.0/16"}`)
	net, _ := out["Net"].(map[string]any)
	netID, _ := net["NetId"].(string)
	post(t, ts, "CreateTags", `{"ResourceIds":["`+netID+`"],"Tags":[{"Key":"Name","Value":"n"}]}`)

	_, out = post(t, ts, "ReadTags", `{}`)
	tags, _ := out["Tags"].([]any)
	if len(tags) != 1 {
		t.Fatalf("expected one tag, got %d", len(tags))
	}
	first, _ := tags[0].(map[string]any)
	if first["ResourceType"] != "vpc" {
		t.Fatalf("ReadTags published ResourceType %q for a Net: the SDK's "+
			"TagResourceType enum says \"vpc\"", first["ResourceType"])
	}

	// And the filter a client sends must match that same value.
	_, out = post(t, ts, "ReadTags", `{"Filters":{"ResourceTypes":["vpc"]}}`)
	if tags, _ := out["Tags"].([]any); len(tags) != 1 {
		t.Fatalf("filtering on the type the API publishes matched %d tags", len(tags))
	}
}

// TestReadTagsOmitsAKindUpstreamDoesNotName holds the one honest gap in place.
// An internet service carries Tags in Outscale's own schema — the Terraform
// provider sets them, which is how #99 was found — and the SDK enum names no
// type for it. So the tag is readable on the resource and absent from the flat
// view. Serving a row with an invented ResourceType is what this asserts
// against.
func TestReadTagsOmitsAKindUpstreamDoesNotName(t *testing.T) {
	ts := newServer(t)

	_, out := post(t, ts, "CreateInternetService", `{}`)
	svc, _ := out["InternetService"].(map[string]any)
	igw, _ := svc["InternetServiceId"].(string)

	if status, out := post(t, ts, "CreateTags",
		`{"ResourceIds":["`+igw+`"],"Tags":[{"Key":"Name","Value":"gateway"}]}`); status != 200 {
		t.Fatalf("tagging an internet service answered %d: %v", status, out["Errors"])
	}

	// Readable where the client puts it: on the resource.
	_, out = post(t, ts, "ReadInternetServices", `{}`)
	services, _ := out["InternetServices"].([]any)
	if len(services) != 1 {
		t.Fatalf("expected one internet service, got %d", len(services))
	}
	first, _ := services[0].(map[string]any)
	if entries, _ := first["Tags"].([]any); len(entries) != 1 {
		t.Fatalf("the tag is not on the internet service: %v", first["Tags"])
	}

	// And absent from the flat view, rather than carrying a name nobody
	// upstream declares.
	_, out = post(t, ts, "ReadTags", `{}`)
	tags, _ := out["Tags"].([]any)
	for _, entry := range tags {
		tag, _ := entry.(map[string]any)
		if tag["ResourceId"] == igw {
			t.Fatalf("ReadTags published %q for an internet service, a ResourceType "+
				"the SDK enum does not declare", tag["ResourceType"])
		}
	}
}

// A tagged NIC reads back its tags (#250).
//
// `storedNicView` published `"Tags": []` as a constant, so CreateTags on an
// interface answered 200, stored the tag, and ReadNics denied it. For a client
// that is worse than a refusal: a Terraform configuration tagging an interface
// planned the same change on every run, for ever.
//
// The shape was right and the fact was wrong, which is why no schema check saw
// it — an empty list is exactly what an untagged NIC should publish. Found by
// applying a realistic stack and reading the second plan, and the suite had
// tagged Nets, Vms and volumes without ever tagging an interface.
func TestATaggedNicReadsBackItsTags(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	netID, subnetID := netAndSubnet(t, ts, "10.80.0.0/16", "10.80.1.0/24")
	_ = netID

	created := call(t, ts, doc, "CreateNic", `{"SubnetId":"`+subnetID+`"}`)
	nic, _ := created["Nic"].(map[string]any)
	nicID, _ := nic["NicId"].(string)
	if nicID == "" {
		t.Fatalf("no NicId in the create response: %v", created)
	}
	// Empty rather than absent on a fresh interface: that is the honest answer,
	// and a client ranging over null is a client that stops.
	if tags, ok := nic["Tags"].([]any); !ok || len(tags) != 0 {
		t.Errorf("a fresh NIC publishes %v for Tags, want an empty list", nic["Tags"])
	}

	call(t, ts, doc, "CreateTags", `{
		"ResourceIds":["`+nicID+`"],
		"Tags":[{"Key":"Name","Value":"platform-app-services"}]
	}`)

	read := call(t, ts, doc, "ReadNics", `{"Filters":{"NicIds":["`+nicID+`"]}}`)
	nics, _ := read["Nics"].([]any)
	if len(nics) != 1 {
		t.Fatalf("the NIC is not readable after tagging: %v", read)
	}
	found, _ := nics[0].(map[string]any)
	tags, _ := found["Tags"].([]any)
	if len(tags) != 1 {
		t.Fatalf("the tagged NIC publishes %d tag(s): the write was accepted and denied", len(tags))
	}
	tag, _ := tags[0].(map[string]any)
	if tag["Key"] != "Name" || tag["Value"] != "platform-app-services" {
		t.Errorf("the NIC carries %v, not the tag it was given", tags[0])
	}
}
