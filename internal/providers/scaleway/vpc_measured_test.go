package scaleway_test

import (
	"net/http"
	"testing"

	"github.com/stephrobert/feint/internal/core/sshkey"
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
// Three operations are asserted, and the third is a later measurement.
// CreateRoute used to sit here as the named exception — "vpc/v2 as well and not
// measured, so it keeps the pack's 201 rather than inheriting a claim from its
// neighbours". The condition that comment set has since been met: a route was
// created on a real fr-par account on 2026-08-24 (a free object, on a free
// private network), the answer is in corpus/scaleway/scw-free-shapes.jsonl, and
// it carried 200. The exception is retired by the measurement it asked for, not
// by the guess it refused.
func TestTheVpcCreatesAnswerWhatTheRealCloudAnswers(t *testing.T) {
	ts := newTestServer(t)

	status, vpc := do(t, ts, "POST", vpcRegion+"/vpcs", `{"name":"measured"}`)
	if status != http.StatusOK {
		t.Errorf("CreateVPC answered %d, and the real cloud answered 200 (%v)", status, vpc)
	}
	vpcID, _ := vpc["id"].(string)
	status, pn := do(t, ts, "POST", vpcRegion+"/private-networks", `{"name":"measured-pn","vpc_id":"`+vpcID+`"}`)
	if status != http.StatusOK {
		t.Errorf("CreatePrivateNetwork answered %d, and the real cloud answered 200 (%v)", status, pn)
	}
	pnID, _ := pn["id"].(string)
	status, route := do(t, ts, "POST", vpcRegion+"/routes",
		`{"vpc_id":"`+vpcID+`","destination":"192.168.99.0/24","nexthop_private_network_id":"`+pnID+`"}`)
	if status != http.StatusOK {
		t.Errorf("CreateRoute answered %d, and the real cloud answered 200 (%v)", status, route)
	}
}

// BookIP answers 200, which is what the wire carried.
//
// Same family as the three above and the same reason it could sit wrong: `scw
// ipam ip create` accepts any 2xx and prints no status, so nothing a client
// does would ever have reported the pack's 201. Measured on 2026-08-24 against
// a real fr-par account, into corpus/scaleway/scw-free-shapes.jsonl; a booking
// on a private network is free, which is why it could be measured at all.
//
// ipam/v1 is a different product from vpc/v2, so it gets its own constant and
// its own assertion rather than borrowing vpcCreateStatus: what is claimed is
// what was seen, per product.
func TestBookIPAnswersWhatTheRealCloudAnswers(t *testing.T) {
	ts := newTestServer(t)

	_, vpc := do(t, ts, "POST", vpcRegion+"/vpcs", `{"name":"booked"}`)
	vpcID, _ := vpc["id"].(string)
	_, pn := do(t, ts, "POST", vpcRegion+"/private-networks",
		`{"name":"booked-pn","vpc_id":"`+vpcID+`","subnets":["192.168.42.0/24"]}`)
	pnID, _ := pn["id"].(string)

	status, ip := do(t, ts, "POST", "/ipam/v1/regions/fr-par/ips",
		`{"is_ipv6":false,"source":{"private_network_id":"`+pnID+`"}}`)
	if status != http.StatusOK {
		t.Errorf("BookIP answered %d, and the real cloud answered 200 (%v)", status, ip)
	}
}

// The two Object Storage flags are served, on every door, under the names
// upstream publishes today (#352, renamed by #570).
//
// They are false and can only be false here: the five operations that attach a
// private network to Object Storage are declined in pack.go with their reason.
// That is not an argument for omitting them — a client reading the flag off a
// decoded object gets the zero value either way, and a client comparing field
// sets does not. The contract declares both; nothing observed them until a
// Private Network and a VPC existed at the moment of a recording.
//
// Every door, because the emulator has more than one: a create, a read, and a
// list, and the list is the one a previous omission survived in.
//
// # What the 2026-08-20 recording still arbitrates, and what it no longer does
//
// This file grades against a capture taken from a real fr-par account on
// 2026-08-20, and Scaleway renamed this family on 2026-08-25. The recording is
// therefore historical for one of the two things it used to settle, and saying
// which is the whole point of writing it here rather than moving the divergence
// into the gate:
//
//   - it still settles that the field is **present on every answer** and that
//     its **value is false** on an account with no Object Storage endpoint
//     attached. A rename does not touch either, and nothing else in this
//     repository establishes them;
//   - it no longer settles the **spelling**. That comes from upstream, read on
//     2026-08-28: vpc_sdk.go:1024 and :646, and the published document
//     .upstream/scaleway-openapi/vpc-v2.yml, whose x-properties-order — the
//     document's own exhaustive property list — carries
//     object_storage_private_access_enabled and
//     has_object_storage_private_access, and neither old name.
//
// Re-recording would need a paid account, which this repository does not have
// for vpc/v2 after 2026-08-20, so the split above is the honest form: the
// assertion below names the new fields, and corpus/scaleway/terraform.jsonl
// keeps answering with the old ones. That disagreement is real, it is declared
// in the corpus acceptance list with this reason and this date, and it is what
// `mise run corpus:cloud` would arbitrate the day somebody has an account.
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

	// And the old spelling is gone rather than kept beside the new one. No
	// upstream source declares a deprecation alias — the SDK has one field and
	// the document's x-properties-order has one entry — so a body carrying
	// both would be a shape this emulator invented, which is the one thing a
	// response body may never be.
	absent := func(what string, body map[string]any, field string) {
		t.Helper()
		if _, carried := body[field]; carried {
			t.Errorf("%s still carries %s, renamed by Scaleway on 2026-08-25; nothing upstream "+
				"declares it any more, and a field no source declares is a field this emulator "+
				"invented", what, field)
		}
	}

	present("CreateVPC", createdVPC, "object_storage_private_access_enabled")
	present("CreatePrivateNetwork", createdPN, "has_object_storage_private_access")

	_, readVPC := do(t, ts, "GET", vpcRegion+"/vpcs/"+vpcID, "")
	present("GetVPC", readVPC, "object_storage_private_access_enabled")
	_, readPN := do(t, ts, "GET", vpcRegion+"/private-networks/"+pnID, "")
	present("GetPrivateNetwork", readPN, "has_object_storage_private_access")

	_, listedVPCs := do(t, ts, "GET", vpcRegion+"/vpcs", "")
	vpcs, _ := listedVPCs["vpcs"].([]any)
	if len(vpcs) == 0 {
		t.Fatalf("ListVPCs answered none: %v", listedVPCs)
	}
	for _, raw := range vpcs {
		entry, _ := raw.(map[string]any)
		present("ListVPCs", entry, "object_storage_private_access_enabled")
	}

	_, listedPNs := do(t, ts, "GET", vpcRegion+"/private-networks", "")
	networks, _ := listedPNs["private_networks"].([]any)
	if len(networks) == 0 {
		t.Fatalf("ListPrivateNetworks answered none: %v", listedPNs)
	}
	for _, raw := range networks {
		entry, _ := raw.(map[string]any)
		present("ListPrivateNetworks", entry, "has_object_storage_private_access")
	}

	absent("CreateVPC", createdVPC, "s3_integration_enabled")
	absent("GetVPC", readVPC, "s3_integration_enabled")
	absent("CreatePrivateNetwork", createdPN, "has_s3_integration")
	absent("GetPrivateNetwork", readPN, "has_s3_integration")
}

// The default VPC carries tags ["default"], and one a client creates carries
// only what the client sent.
//
// Measured twice on 2026-08-21: `scw vpc vpc list` against a real fr-par
// account answered tags ["default"] on the VPC the project was born with, and
// the corpus recorded the same list through `feint proxy`. A fresh emulator
// answered an empty list, which made every recorded read of that list carry an
// element this one had none of — the first defect the committed corpus surfaced
// (#355), and the one nothing else could have found: no client reads the tag, so
// no conformance suite fails on it, and a contract states the type of `tags`
// rather than what the cloud puts in it.
//
// Both halves, because the second is what keeps this a measurement instead of a
// decoration: writing "default" on a VPC a client created would invent a value
// the API never answered.
func TestTheDefaultVPCCarriesTheDefaultTag(t *testing.T) {
	ts := newTestServer(t)

	_, listed := do(t, ts, "GET", vpcRegion+"/vpcs", "")
	vpcs, _ := listed["vpcs"].([]any)
	if len(vpcs) == 0 {
		t.Fatalf("the emulator serves no default VPC, so this test measures nothing: %v", listed)
	}
	first, _ := vpcs[0].(map[string]any)
	if isDefault, _ := first["is_default"].(bool); !isDefault {
		t.Fatalf("the first VPC of a fresh project is not the default one: %v", first)
	}
	if got := tagsOf(t, first); len(got) != 1 || got[0] != "default" {
		t.Errorf("the default VPC answers tags %v, and the real cloud answered [\"default\"]", got)
	}

	_, created := do(t, ts, "POST", vpcRegion+"/vpcs", `{"name":"untagged"}`)
	if got := tagsOf(t, created); len(got) != 0 {
		t.Errorf("a VPC created without tags answers %v; the default tag belongs to the default VPC "+
			"and writing it here would invent a value the API never answered", got)
	}
}

func tagsOf(t *testing.T, vpc map[string]any) []string {
	t.Helper()
	raw, present := vpc["tags"]
	if !present {
		t.Fatalf("the VPC carries no tags field at all: %v", vpc)
	}
	list, isList := raw.([]any)
	if !isList {
		t.Fatalf("tags came back %T, want a list: %v", raw, raw)
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		text, isText := item.(string)
		if !isText {
			t.Fatalf("a tag came back %T, want a string", item)
		}
		out = append(out, text)
	}
	return out
}

// CreateSSHKey answers 200, which is what the wire carried.
//
// The same family as the two vpc/v2 creates above, and found the same way: off
// a `feint proxy` transcript of a real fr-par account, because `scw iam ssh-key
// create` accepts any 2xx and shows none. It stayed hidden longer than those
// two because the corpus could not replay the key's lifecycle at all — the
// proxy's own redaction destroyed public_key, so the create answered 400 and
// the 201 sat behind a status finding that blamed the instrument (#355).
//
// Only this operation. The other iam/v1alpha1 creates were not measured, and
// they keep the pack's 201 rather than inheriting a claim from a neighbour.
func TestCreateSSHKeyAnswersTheStatusTheCloudAnswers(t *testing.T) {
	ts := newTestServer(t)

	status, key := do(t, ts, "POST", "/iam/v1alpha1/ssh-keys",
		`{"name":"feint-corpus-key","public_key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOt7Knja0KTVDt1HPz09qrmbCjB8Zf8icc3p2eU9ubqy feint@test"}`)
	if status != http.StatusOK {
		t.Errorf("CreateSSHKey answered %d, and the real cloud answered 200 (%v)", status, key)
	}
}

// A stored SSH key is published without its comment, which is what the cloud
// answers.
//
// Measured twice on 2026-08-21. Directly: a key created on a real fr-par
// account as `ssh-ed25519 <material> feint-corpus-echo` (98 bytes, three
// fields) read back as `ssh-ed25519 <material>` (80 bytes, two fields). And
// from the other side, in the corpus: the recorded request body and the
// recorded answer carried two *different* strings at `public_key`, where this
// emulator echoed one.
//
// No gate here could have caught it, which is why it is asserted rather than
// left to one. `feint replay` compares types and compares a value only where a
// pack declares an invariant, and a contract states that `public_key` is a
// string, not what the cloud puts in it.
//
// The fingerprint is asserted alongside, because it is the half that must not
// move: it is computed over the decoded blob rather than over the line, so
// dropping the comment cannot change it, and a client matching what
// `ssh-keygen -l -E md5` prints still matches.
func TestASSHKeyIsPublishedWithoutItsComment(t *testing.T) {
	ts := newTestServer(t)

	const material = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOt7Knja0KTVDt1HPz09qrmbCjB8Zf8icc3p2eU9ubqy"
	status, created := do(t, ts, "POST", "/iam/v1alpha1/ssh-keys",
		`{"name":"feint-corpus-key","public_key":"`+material+` feint@test"}`)
	if status != http.StatusOK {
		t.Fatalf("create: status %d (%v)", status, created)
	}
	if got := created["public_key"]; got != material {
		t.Errorf("the create answered public_key %q, want %q: the cloud keeps the algorithm and "+
			"the material and drops the comment", got, material)
	}

	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("no id: %v", created)
	}
	_, read := do(t, ts, "GET", "/iam/v1alpha1/ssh-keys/"+id, "")
	if got := read["public_key"]; got != material {
		t.Errorf("the read answered public_key %q, want %q: the create and the read must agree", got, material)
	}
	if created["fingerprint"] != read["fingerprint"] || created["fingerprint"] == "" {
		t.Errorf("the fingerprint moved between create and read, or is empty: %v then %v",
			created["fingerprint"], read["fingerprint"])
	}
	if got := sshkey.FingerprintMD5(material + " a-different-comment"); got != created["fingerprint"] {
		t.Errorf("the fingerprint depends on the comment (%v vs %v); it is computed over the decoded "+
			"blob so that renaming a key cannot change it", got, created["fingerprint"])
	}
}

// The two block/v1alpha1 creates answer 200, which is what the wire carried.
//
// Third product measured the same way, after vpc/v2 and ipam/v1. Same reason it
// could sit wrong: `scw block volume create` accepts any 2xx and prints no
// status. Measured on 2026-08-24 against a real fr-par account, into
// corpus/scaleway/scw-billed-shapes.jsonl -- a 5 GB volume and its snapshot,
// the smallest the API takes, destroyed in the same run with each destruction
// proved by a read.
func TestTheBlockCreatesAnswerWhatTheRealCloudAnswers(t *testing.T) {
	ts := newTestServer(t)
	const blockZone = "/block/v1alpha1/zones/fr-par-1"

	status, vol := do(t, ts, "POST", blockZone+"/volumes",
		`{"name":"measured","perf_iops":5000,"from_empty":{"size":5000000000}}`)
	if status != http.StatusOK {
		t.Errorf("CreateVolume answered %d, and the real cloud answered 200 (%v)", status, vol)
	}
	volID, _ := vol["id"].(string)

	status, snap := do(t, ts, "POST", blockZone+"/snapshots",
		`{"name":"measured-snap","volume_id":"`+volID+`"}`)
	if status != http.StatusOK {
		t.Errorf("CreateSnapshot answered %d, and the real cloud answered 200 (%v)", status, snap)
	}
}
