package outscale_test

import (
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/providers/outscale"
)

// The witness no type could be. #566's third defect was a filter declared and
// compared nowhere: `snapshotFilters` named AccountIds, Progresses and
// VolumeSizes, and `snapshotMatches` mentioned none of the three. AccountIds is
// an ordinary list of strings, so no decoder, no kind and no contract check
// could ever have seen it — only asking the emulator can.
//
// The question this asks is the one a filter exists to answer: **can it exclude
// anything at all?** Every declared filter is sent a value nothing in the store
// carries, and the answer must be empty. A filter compared nowhere passes every
// candidate and fails here.
//
// Measured before the fix, on 2026-08-28: ReadSnapshots with
// `{"AccountIds":["000000000000"]}` answered 200 and four snapshots whose
// AccountId is 000000000001, and `{"Progresses":[7]}` answered 200 and four
// snapshots whose Progress is 100.
//
// Two properties of the harness matter more than the assertion:
//
//   - the unfiltered read must answer something first. A control whose success
//     is "nothing came back" is indistinguishable from one that looked at an
//     empty store, and every action below would pass vacuously on a store
//     nobody populated.
//   - the population comes from the pack's own declarations
//     (outscale.DeclaredFilters), not from a list written here, so a filter
//     added later is covered without anybody remembering this file.
//
// Boolean filters are out of reach of this shape and say so: `true` and `false`
// are both in the domain, so no value witnesses their absence. The two this
// pack serves have tests of their own —
// TestARootVolumeAnswersItsDeleteOnVmDeletionFilter and the route-table suite.
func TestEveryDeclaredFilterCanExcludeSomething(t *testing.T) {
	ts := newServer(t)
	inventory(t, ts)

	declared := outscale.DeclaredFilters()
	if len(declared) == 0 {
		t.Fatal("the pack declares no filters at all: the export is broken, not the pack")
	}

	// The result key of every read that filters. An action missing here is an
	// action this sweep would skip in silence, so it is an error rather than a
	// continue.
	answers := map[string]string{
		"ReadDhcpOptions":            "DhcpOptionsSets",
		"ReadImages":                 "Images",
		"ReadInternetServices":       "InternetServices",
		"ReadKeypairs":               "Keypairs",
		"ReadLoadBalancers":          "LoadBalancers",
		"ReadNatServices":            "NatServices",
		"ReadNetAccessPointServices": "Services",
		"ReadNetPeerings":            "NetPeerings",
		"ReadNets":                   "Nets",
		"ReadNics":                   "Nics",
		"ReadPublicIps":              "PublicIps",
		"ReadRouteTables":            "RouteTables",
		"ReadSecurityGroups":         "SecurityGroups",
		"ReadSnapshots":              "Snapshots",
		"ReadSubnets":                "Subnets",
		"ReadSubregions":             "Subregions",
		"ReadTags":                   "Tags",
		"ReadVmTypes":                "VmTypes",
		"ReadVms":                    "Vms",
		"ReadVmsState":               "VmStates",
		"ReadVolumes":                "Volumes",
	}

	actions := make([]string, 0, len(declared))
	for action := range declared {
		actions = append(actions, action)
	}
	sort.Strings(actions)

	witnessed := 0
	for _, action := range actions {
		key, known := answers[action]
		if !known {
			t.Errorf("%s declares filters and this sweep does not know its result key, so nothing "+
				"here ever asked whether they exclude", action)
			continue
		}
		t.Run(action, func(t *testing.T) {
			// The witness: this read answers something before any filter is
			// applied, or the assertions below measure an empty store.
			status, out := post(t, ts, action, `{}`)
			if status != 200 {
				t.Fatalf("the unfiltered read answered %d: %v", status, out)
			}
			all, _ := out[key].([]any)
			if len(all) == 0 {
				t.Fatalf("the unfiltered read answered no %s, so every filter below would pass "+
					"on an empty answer: the inventory this test builds is what is wrong", key)
			}

			for _, filter := range declared[action] {
				if filter.Absent == "" {
					continue // a boolean has no value outside its own domain
				}
				body := `{"Filters":{"` + filter.Name + `":` + filter.Absent + `}}`
				status, out := post(t, ts, action, body)
				if status != 200 {
					t.Errorf("%s refused %s: %v", action, body, out)
					continue
				}
				witnessed++
				if got, _ := out[key].([]any); len(got) != 0 {
					t.Errorf("%s answered %d %s for a %s nothing carries: this filter cannot "+
						"exclude, which is the shape of #566 — declared applied, compared nowhere",
						action, len(got), key, filter.Name)
				}
			}
		})
	}
	if witnessed == 0 {
		t.Fatal("no filter was witnessed: the sweep ran nothing")
	}
	// Named, so a refactor that stopped enumerating the three filters this test
	// was written for cannot leave it green.
	for _, want := range []struct{ action, filter string }{
		{"ReadSnapshots", "AccountIds"},
		{"ReadSnapshots", "Progresses"},
		{"ReadSnapshots", "VolumeSizes"},
		{"ReadVolumes", "VolumeSizes"},
	} {
		found := false
		for _, filter := range declared[want.action] {
			if filter.Name == want.filter {
				found = true
			}
		}
		if !found {
			t.Errorf("%s no longer declares %s, so #566's own three are out of this sweep's population",
				want.action, want.filter)
		}
	}
}

// inventory creates one of everything the reads above answer, so the sweep has
// something to exclude. It asserts nothing: what it builds is asserted by the
// unfiltered read at the top of each subtest, which is where an empty answer
// has to fail.
func inventory(t *testing.T, ts *httptest.Server) {
	t.Helper()

	netID, subnetID := netAndSubnet(t, ts, "10.90.0.0/16", "10.90.1.0/24")
	_, other := post(t, ts, "CreateNet", `{"IpRange":"10.91.0.0/16"}`)
	otherNet, _ := other["Net"].(map[string]any)
	otherNetID, _ := otherNet["NetId"].(string)

	_, images := post(t, ts, "ReadImages", `{}`)
	imageList, _ := images["Images"].([]any)
	if len(imageList) == 0 {
		t.Fatal("the image catalogue is empty, so no machine can be created here")
	}
	first, _ := imageList[0].(map[string]any)
	imageID, _ := first["ImageId"].(string)

	post(t, ts, "CreateVms",
		`{"ImageId":"`+imageID+`","SubnetId":"`+subnetID+`","VmType":"tinav6.c2r2p2"}`)

	_, volume := post(t, ts, "CreateVolume", `{"SubregionName":"eu-west-2a","Size":40,"VolumeType":"standard"}`)
	vol, _ := volume["Volume"].(map[string]any)
	volumeID, _ := vol["VolumeId"].(string)
	post(t, ts, "CreateSnapshot", `{"VolumeId":"`+volumeID+`","Description":"a snapshot"}`)

	post(t, ts, "CreateNic", `{"SubnetId":"`+subnetID+`","Description":"a nic"}`)
	post(t, ts, "CreateSecurityGroup", `{"SecurityGroupName":"web","Description":"a group","NetId":"`+netID+`"}`)
	post(t, ts, "CreateKeypair", `{"KeypairName":"mine","PublicKey":`+quote(publicKey)+`}`)
	post(t, ts, "CreateRouteTable", `{"NetId":"`+netID+`"}`)
	post(t, ts, "CreateDhcpOptions", `{"DomainName":"feint.example"}`)
	post(t, ts, "CreateNetPeering", `{"SourceNetId":"`+netID+`","AccepterNetId":"`+otherNetID+`"}`)
	post(t, ts, "CreateTags", `{"ResourceIds":["`+netID+`"],"Tags":[{"Key":"name","Value":"one"}]}`)

	_, gateway := post(t, ts, "CreateInternetService", `{}`)
	igw, _ := gateway["InternetService"].(map[string]any)
	if id, _ := igw["InternetServiceId"].(string); id != "" {
		post(t, ts, "LinkInternetService", `{"InternetServiceId":"`+id+`","NetId":"`+netID+`"}`)
	}

	_, address := post(t, ts, "CreatePublicIp", `{}`)
	ip, _ := address["PublicIp"].(map[string]any)
	ipID, _ := ip["PublicIpId"].(string)
	post(t, ts, "CreateNatService", `{"SubnetId":"`+subnetID+`","PublicIpId":"`+ipID+`"}`)

	post(t, ts, "CreateLoadBalancer",
		`{"LoadBalancerName":"feint-lb","Listeners":[{"BackendPort":80,"LoadBalancerPort":80,`+
			`"LoadBalancerProtocol":"TCP"}],"Subnets":["`+subnetID+`"]}`)
}

// A public key of the shape sshkey.Parse accepts, so CreateKeypair answers 200
// and ReadKeypairs has something to filter.
const publicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIr6pEFlAFO3YU0DNW/r8SkpjdbptN9ockkO2BtIolSD conformance@feint"

// The volume-size filter excludes a volume of another size.
//
// #566 names this as the test that was missing, and names why: it passes today
// and would pass with the filter deleted, because FiltersVolume declares
// VolumeSizes as a list of integers and the pack read it as a list of strings —
// so the decode failed, the failure was reported as "filter absent", and every
// volume came back with a 200.
//
// Measured on 2026-08-28 against main@2879888, with a 40 GiB and a 10 GiB
// volume in the store:
//
//	{"Filters":{"VolumeSizes":[40]}}    -> 200, both volumes
//	{"Filters":{"VolumeSizes":["40"]}}  -> 200, the 40 GiB one
//
// The second line is the inversion worth remembering: the only shape the pack
// could read was the one the API does not declare, so the filter appeared to
// work for anybody who happened to send strings and worked for nobody sending
// what their own client sends.
func TestAVolumeSizeFilterExcludesAVolumeOfAnotherSize(t *testing.T) {
	ts := newServer(t)

	_, big := post(t, ts, "CreateVolume", `{"SubregionName":"eu-west-2a","Size":40,"VolumeType":"standard"}`)
	bigID, _ := big["Volume"].(map[string]any)["VolumeId"].(string)
	_, small := post(t, ts, "CreateVolume", `{"SubregionName":"eu-west-2a","Size":10,"VolumeType":"standard"}`)
	smallID, _ := small["Volume"].(map[string]any)["VolumeId"].(string)
	if bigID == "" || smallID == "" {
		t.Fatalf("the two volumes were not created: %v %v", big, small)
	}

	ids := func(t *testing.T, body string) []string {
		t.Helper()
		status, out := post(t, ts, "ReadVolumes", body)
		if status != 200 {
			t.Fatalf("ReadVolumes %s answered %d: %v", body, status, out)
		}
		list, _ := out["Volumes"].([]any)
		var got []string
		for _, raw := range list {
			volume, _ := raw.(map[string]any)
			id, _ := volume["VolumeId"].(string)
			got = append(got, id)
		}
		sort.Strings(got)
		return got
	}

	if got := ids(t, `{}`); len(got) != 2 {
		t.Fatalf("the unfiltered read answered %d volume(s), want 2: nothing below would measure anything", len(got))
	}
	if got := ids(t, `{"Filters":{"VolumeSizes":[40]}}`); len(got) != 1 || got[0] != bigID {
		t.Errorf("VolumeSizes [40] answered %v, want only the 40 GiB volume (%s): a filter that "+
			"cannot exclude is a filter that filters nothing", got, bigID)
	}
	if got := ids(t, `{"Filters":{"VolumeSizes":[10]}}`); len(got) != 1 || got[0] != smallID {
		t.Errorf("VolumeSizes [10] answered %v, want only the 10 GiB volume (%s)", got, smallID)
	}
	// A size no volume carries excludes both, and a size list carrying two
	// sizes answers both: a filter that always matches and one that never
	// matches are equally useless.
	if got := ids(t, `{"Filters":{"VolumeSizes":[7]}}`); len(got) != 0 {
		t.Errorf("VolumeSizes [7] answered %v, want nothing", got)
	}
	if got := ids(t, `{"Filters":{"VolumeSizes":[10,40]}}`); len(got) != 2 {
		t.Errorf("VolumeSizes [10,40] answered %v, want both volumes", got)
	}
}

// A filter whose value is not written the way the API declares it is refused,
// not ignored.
//
// The third answer of #566, made visible. `filters.go` opens on "a filter is
// either applied or refused, never ignored" and the reader beneath it folded
// "could not read this" onto "the client sent nothing", which is the one
// reading that answers 200 with the whole inventory.
//
// The shapes below are refused because contracts/outscale.json declares what
// each filter holds: VolumeSizes and Progresses are `items: {type: integer}`,
// the identifier filters are `items: {type: string}`, and
// LinkRouteTableMain is a bare boolean. The refusal names the filter and the
// shape, because a 400 that does not say which field is one a caller cannot act
// on — the same reason the unsupported-filter refusal names its fields.
func TestAFilterOfTheWrongShapeIsRefusedRatherThanIgnored(t *testing.T) {
	ts := newServer(t)
	post(t, ts, "CreateVolume", `{"SubregionName":"eu-west-2a","Size":40,"VolumeType":"standard"}`)
	post(t, ts, "CreateVolume", `{"SubregionName":"eu-west-2a","Size":10,"VolumeType":"standard"}`)

	for _, probe := range []struct{ action, body, names string }{
		// Strings where the document says integers. This one used to filter,
		// which is the inversion: the readable shape was the wrong shape.
		{"ReadVolumes", `{"Filters":{"VolumeSizes":["40"]}}`, "VolumeSizes"},
		{"ReadSnapshots", `{"Filters":{"Progresses":["100"]}}`, "Progresses"},
		// A bare string where a list goes, which every identifier filter of
		// this pack used to accept and ignore.
		{"ReadVolumes", `{"Filters":{"VolumeIds":"vol-12345678"}}`, "VolumeIds"},
		// A list where a bare boolean goes.
		{"ReadRouteTables", `{"Filters":{"LinkRouteTableMain":["true"]}}`, "LinkRouteTableMain"},
		// An object, which is neither.
		{"ReadVolumes", `{"Filters":{"VolumeStates":{"eq":"available"}}}`, "VolumeStates"},
	} {
		status, out := post(t, ts, probe.action, probe.body)
		if status == 200 {
			list, _ := out["Volumes"].([]any)
			t.Errorf("%s answered 200 to %s (%d volume(s)): an unreadable filter must be refused, "+
				"never ignored", probe.action, probe.body, len(list))
			continue
		}
		if status != 400 {
			t.Errorf("%s answered %d to %s, want 400", probe.action, status, probe.body)
			continue
		}
		errs, _ := out["Errors"].([]any)
		if len(errs) == 0 {
			t.Errorf("%s refused %s without an Errors array: %v", probe.action, probe.body, out)
			continue
		}
		firstError, _ := errs[0].(map[string]any)
		details, _ := firstError["Details"].(string)
		if !strings.Contains(details, probe.names) {
			t.Errorf("%s refused %s without naming the filter: %q", probe.action, probe.body, details)
		}
	}

	// The accepting half: the shape the document declares is served, or this
	// test is satisfied by a pack that refuses everything.
	if status, out := post(t, ts, "ReadVolumes", `{"Filters":{"VolumeSizes":[40]}}`); status != 200 {
		t.Errorf("the declared shape was refused with %d: %v", status, out)
	}
}
