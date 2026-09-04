package outscale_test

import (
	"net/http"
	"testing"
)

// The three tag filters select, exclude, and hand the object back WITH its tags
// (#618).
//
// Measured against a real account on 2026-08-30: eleven calls carrying
// Filters.Tags, Filters.TagKeys or Filters.TagValues, every one accepted, and
// this pack refused all of them with a 4001 naming what it did accept. A
// published course tags everything and then looks things up by tag; its capstone
// could not complete.
//
// Three filters rather than one with three spellings, which is the first thing
// #618 says an implementation must not get wrong:
//
//	Tags        Key=Value, the pair
//	TagKeys     the key alone
//	TagValues   the value alone
func TestTagFiltersSelectAndExclude(t *testing.T) {
	ts := newServer(t)

	made := func(ipRange string) string {
		status, net := post(t, ts, "CreateNet", `{"IpRange":"`+ipRange+`"}`)
		if status != http.StatusOK {
			t.Fatalf("CreateNet: %d (%v)", status, net)
		}
		inner, _ := net["Net"].(map[string]any)
		id, _ := inner["NetId"].(string)
		return id
	}
	tagged, plain := made("10.90.0.0/16"), made("10.91.0.0/16")
	if status, out := post(t, ts, "CreateTags",
		`{"ResourceIds":["`+tagged+`"],"Tags":[{"Key":"env","Value":"survey"}]}`); status != http.StatusOK {
		t.Fatalf("CreateTags: %d (%v)", status, out)
	}

	read := func(filters string) []any {
		t.Helper()
		status, out := post(t, ts, "ReadNets", filters)
		if status != http.StatusOK {
			t.Fatalf("ReadNets %s: %d (%v)", filters, status, out)
		}
		nets, _ := out["Nets"].([]any)
		return nets
	}

	// Selecting: one of the two, by each of the three filters.
	for _, filter := range []string{
		`{"Filters":{"Tags":["env=survey"]}}`,
		`{"Filters":{"TagKeys":["env"]}}`,
		`{"Filters":{"TagValues":["survey"]}}`,
	} {
		nets := read(filter)
		if len(nets) != 1 {
			t.Fatalf("%s answered %d Nets, want the one that carries the tag", filter, len(nets))
		}
		net, _ := nets[0].(map[string]any)
		if got, _ := net["NetId"].(string); got != tagged {
			t.Errorf("%s answered %q, want %q", filter, got, tagged)
		}
		// The second thing #618 says must not go wrong: an object that matched
		// a tag filter and came back without its tags is a shape divergence on
		// top of the missing filter.
		tags, _ := net["Tags"].([]any)
		if len(tags) != 1 {
			t.Fatalf("%s answered the object without its Tags: %v", filter, net)
		}
		tag, _ := tags[0].(map[string]any)
		if tag["Key"] != "env" || tag["Value"] != "survey" {
			t.Errorf("%s answered Tags %v", filter, tags)
		}
	}

	// Excluding: a tag nothing carries answers nothing, on each filter. This is
	// the half that catches a filter declared and compared nowhere, which is
	// what #566 was.
	for _, filter := range []string{
		`{"Filters":{"Tags":["env=absent"]}}`,
		`{"Filters":{"TagKeys":["absent"]}}`,
		`{"Filters":{"TagValues":["absent"]}}`,
	} {
		if nets := read(filter); len(nets) != 0 {
			t.Errorf("%s answered %d Nets, want none", filter, len(nets))
		}
	}

	// And the pair is matched whole. A key that matches with the wrong value
	// selects nothing: #618 records that the partial case was never measured
	// upstream, so the emulator takes the stricter of the two readings rather
	// than inventing the more permissive one.
	if nets := read(`{"Filters":{"Tags":["env=other"]}}`); len(nets) != 0 {
		t.Errorf("Tags env=other answered %d Nets: the pair is being matched on its key alone", len(nets))
	}
	// And a bare key in Tags selects nothing either, which is the same property
	// from the other side. Written because a mutation that added the bare key
	// beside the pair went unnoticed by the assertion above: env=other misses a
	// bare-key match too, so it could not tell the two readings apart.
	if nets := read(`{"Filters":{"Tags":["env"]}}`); len(nets) != 0 {
		t.Errorf("Tags env answered %d Nets: TagKeys is the filter for a key, and Tags is the pair", len(nets))
	}

	// Unfiltered, both are there: a filter that answered nothing at all would
	// pass every exclusion above.
	if nets := read(`{}`); len(nets) < 2 {
		t.Errorf("an unfiltered read answers %d Nets, want at least the two created (%s, %s)",
			len(nets), tagged, plain)
	}
}

// The same three filters on the other reads the recording exercised (#618).
//
// One assertion each, because the mechanism is shared: what is worth holding
// per operation is that the filter is DECLARED and reaches the comparison, and
// an operation that forgot to call it answers the whole inventory here.
func TestTagFiltersReachEveryReadTheRecordingExercised(t *testing.T) {
	ts := newServer(t)

	_, subnetID := netAndSubnet(t, ts, "10.92.0.0/16", "10.92.1.0/24")
	status, vm := post(t, ts, "CreateVms",
		`{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","BootOnCreation":false}`)
	if status != http.StatusOK {
		t.Fatalf("CreateVms: %d (%v)", status, vm)
	}
	status, volume := post(t, ts, "CreateVolume", `{"SubregionName":"eu-west-2a","Size":10}`)
	if status != http.StatusOK {
		t.Fatalf("CreateVolume: %d (%v)", status, volume)
	}
	status, ip := post(t, ts, "CreatePublicIp", `{}`)
	if status != http.StatusOK {
		t.Fatalf("CreatePublicIp: %d (%v)", status, ip)
	}

	// A tag no object carries: every read must answer an empty list rather than
	// its inventory.
	for _, read := range []struct{ action, key string }{
		{"ReadVms", "Vms"},
		{"ReadNets", "Nets"},
		{"ReadSubnets", "Subnets"},
		{"ReadVolumes", "Volumes"},
		{"ReadPublicIps", "PublicIps"},
		{"ReadRouteTables", "RouteTables"},
		{"ReadNatServices", "NatServices"},
		{"ReadNetPeerings", "NetPeerings"},
		{"ReadImages", "Images"},
	} {
		status, out := post(t, ts, read.action, `{"Filters":{"TagKeys":["nothing-carries-this"]}}`)
		if status != http.StatusOK {
			t.Errorf("%s refused a tag filter: %d (%v)", read.action, status, out)
			continue
		}
		got, _ := out[read.key].([]any)
		if len(got) != 0 {
			t.Errorf("%s answered %d %s for a tag nothing carries: the filter is declared and compared nowhere",
				read.action, len(got), read.key)
		}
	}
}
