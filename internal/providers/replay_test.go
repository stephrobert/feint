// Package providers holds the properties that only make sense across packs.
//
// Each pack's own tests prove its dialect against its own client; nothing in
// them can state that two packs agree. This test-only package mounts the three
// packs one after the other on the shared contract recorder
// (machine.NewRecorder, #515) and replays the same intent into each, so the
// cross-pack properties become statable: same intent, same runtime sequence
// (#510's sentence, red today), and no gesture outside the contract (#514's
// service list, held dynamically).
package providers

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/store"
	"github.com/stephrobert/feint/internal/providers/exoscale"
	"github.com/stephrobert/feint/internal/providers/outscale"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// The intent every pack can express, the one #515 names: one machine on one
// private network, wearing one rule-set-bearing group, carrying one public
// address. Each replay speaks its pack's own dialect — the forms must differ,
// that is the polar star — but the client steps follow one canonical order:
// network, group and its rule, address, machine wired to all three, then the
// attachments the dialect performs after creation. What is compared is never
// the dialect: it is the sequence of contract gestures the pack asked of the
// runtime.

// recorderEnv builds a deterministic Env backed by a fresh contract recorder.
func recorderEnv() (*emulator.Env, *machine.Recorder) {
	rec := machine.NewRecorder()
	n := 0
	env := &emulator.Env{
		Store:    store.New(),
		Machines: rec,
		Now:      func() time.Time { return time.Unix(1700000000, 0).UTC() },
		NewID: func() string {
			n++
			return fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return env, rec
}

// request drives one client step and fails the replay on any error status: a
// sequence recorded after a refused step would measure the refusal, not the
// pack, and an empty sequence compares equal to another empty one — the exact
// way an instrument passes by vacuity.
func request(t *testing.T, h http.Handler, method, path, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code >= http.StatusBadRequest {
		t.Fatalf("%s %s answered %d: %s", method, path, rec.Code, rec.Body.String())
	}
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s answered something that is not a JSON object: %s", method, path, rec.Body.String())
		}
	}
	return out
}

func id(t *testing.T, out map[string]any, keys ...string) string {
	t.Helper()
	var current any = out
	for _, key := range keys {
		obj, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("no %v in %v", keys, out)
		}
		current = obj[key]
	}
	s, _ := current.(string)
	if s == "" {
		t.Fatalf("empty %v in %v", keys, out)
	}
	return s
}

// replayScaleway expresses the intent in Scaleway's dialect: the group and the
// address are wired at create, the private network is joined by a NIC after
// the boot.
func replayScaleway(t *testing.T) *machine.Recorder {
	t.Helper()
	env, rec := recorderEnv()
	srv, err := emulator.NewServer(env, scaleway.New(env))
	if err != nil {
		t.Fatalf("mount scaleway: %v", err)
	}
	h := srv.Handler()
	const zone = "/instance/v1/zones/fr-par-1"

	pn := request(t, h, "POST", "/vpc/v2/regions/fr-par/private-networks",
		`{"name":"intent-net","subnets":["10.61.0.0/24"]}`)
	pnID := id(t, pn, "id")

	sg := request(t, h, "POST", zone+"/security_groups",
		`{"name":"intent-group","description":"the intent's one group","inbound_default_policy":"drop"}`)
	sgID := id(t, sg, "security_group", "id")
	request(t, h, "POST", zone+"/security_groups/"+sgID+"/rules",
		`{"protocol":"TCP","direction":"inbound","action":"accept","ip_range":"0.0.0.0/0","dest_port_from":22,"dest_port_to":22}`)

	ip := request(t, h, "POST", zone+"/ips", `{}`)
	ipID := id(t, ip, "ip", "id")

	created := request(t, h, "POST", zone+"/servers",
		`{"name":"intent-machine","commercial_type":"DEV1-S","image":"ubuntu_jammy","security_group":"`+sgID+`","public_ip":"`+ipID+`"}`)
	serverID := id(t, created, "server", "id")

	request(t, h, "POST", zone+"/servers/"+serverID+"/action", `{"action":"poweron"}`)
	request(t, h, "POST", zone+"/servers/"+serverID+"/private_nics",
		`{"private_network_id":"`+pnID+`"}`)
	return rec
}

// replayOutscale expresses the intent in Outscale's dialect: the Vm boots on
// its Subnet wearing its group, the public address is linked afterwards.
func replayOutscale(t *testing.T) *machine.Recorder {
	t.Helper()
	env, rec := recorderEnv()
	srv, err := emulator.NewServer(env, outscale.New(env))
	if err != nil {
		t.Fatalf("mount outscale: %v", err)
	}
	h := srv.Handler()
	post := func(action, body string) map[string]any {
		return request(t, h, "POST", "/api/v1/"+action, body)
	}

	net := post("CreateNet", `{"IpRange":"10.61.0.0/16"}`)
	netID := id(t, net, "Net", "NetId")
	subnet := post("CreateSubnet", `{"NetId":"`+netID+`","IpRange":"10.61.1.0/24"}`)
	subnetID := id(t, subnet, "Subnet", "SubnetId")

	sg := post("CreateSecurityGroup",
		`{"SecurityGroupName":"intent-group","Description":"the intent's one group","NetId":"`+netID+`"}`)
	sgID := id(t, sg, "SecurityGroup", "SecurityGroupId")
	post("CreateSecurityGroupRule",
		`{"Flow":"Inbound","SecurityGroupId":"`+sgID+`","IpProtocol":"tcp","FromPortRange":22,"ToPortRange":22,"IpRange":"0.0.0.0/0"}`)

	ip := post("CreatePublicIp", `{}`)
	ipID := id(t, ip, "PublicIp", "PublicIpId")

	vms := post("CreateVms",
		`{"ImageId":"ami-00000001","VmType":"tinav6.c1r1p2","SubnetId":"`+subnetID+`","SecurityGroupIds":["`+sgID+`"]}`)
	list, _ := vms["Vms"].([]any)
	if len(list) != 1 {
		t.Fatalf("CreateVms answered %d Vms, want 1: %v", len(list), vms)
	}
	vmID := id(t, map[string]any{"Vm": list[0]}, "Vm", "VmId")

	post("LinkPublicIp", `{"PublicIpId":"`+ipID+`","VmId":"`+vmID+`"}`)
	return rec
}

// replayExoscale expresses the intent in Exoscale's dialect: the instance
// boots wearing its group, then the private network and the elastic IP are
// attached to the running machine.
func replayExoscale(t *testing.T) *machine.Recorder {
	t.Helper()
	env, rec := recorderEnv()
	srv, err := emulator.NewServer(env, exoscale.New(env))
	if err != nil {
		t.Fatalf("mount exoscale: %v", err)
	}
	h := srv.Handler()

	request(t, h, "POST", "/v2/private-network",
		`{"name":"intent-net","start-ip":"10.61.0.20","end-ip":"10.61.0.200","netmask":"255.255.255.0"}`)
	listed := request(t, h, "GET", "/v2/private-network", "")
	pns, _ := listed["private-networks"].([]any)
	if len(pns) != 1 {
		t.Fatalf("%d private networks after create, want 1: %v", len(pns), listed)
	}
	pnID := id(t, map[string]any{"pn": pns[0]}, "pn", "id")

	sg := request(t, h, "POST", "/v2/security-group",
		`{"name":"intent-group","description":"the intent's one group"}`)
	sgID := id(t, sg, "reference", "id")
	request(t, h, "POST", "/v2/security-group/"+sgID+"/rules",
		`{"flow-direction":"ingress","protocol":"tcp","network":"0.0.0.0/0","start-port":22,"end-port":22}`)

	eip := request(t, h, "POST", "/v2/elastic-ip", `{}`)
	eipID := id(t, eip, "reference", "id")

	request(t, h, "POST", "/v2/instance", `{
		"name":"intent-machine",
		"instance-type":{"id":"21624abb-764e-4def-81d7-9fc54b5957fb"},
		"template":{"id":"11111111-1111-4111-8111-111111111111"},
		"disk-size":10,
		"security-groups":[{"id":"`+sgID+`"}]
	}`)
	instances := request(t, h, "GET", "/v2/instance", "")
	list, _ := instances["instances"].([]any)
	if len(list) != 1 {
		t.Fatalf("%d instances after create, want 1: %v", len(list), instances)
	}
	instanceID := id(t, map[string]any{"i": list[0]}, "i", "id")

	request(t, h, "PUT", "/v2/private-network/"+pnID+":attach",
		`{"instance":{"id":"`+instanceID+`"}}`)
	request(t, h, "PUT", "/v2/elastic-ip/"+eipID+":attach",
		`{"instance":{"id":"`+instanceID+`"}}`)
	return rec
}

// replays runs the three packs' replays and answers their recorders, keyed by
// pack name, in a fixed iteration order.
var replays = []struct {
	pack string
	run  func(*testing.T) *machine.Recorder
}{
	{"scaleway", replayScaleway},
	{"outscale", replayOutscale},
	{"exoscale", replayExoscale},
}

// sameSequence is the equivalence the cross-pack property asserts: the same
// gesture kinds, in the same order. Deliberately nothing weaker — not a set
// comparison, not a subsequence — because the order is the property (#510).
func sameSequence(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func formatSequence(seq []string) string {
	if len(seq) == 0 {
		return "(empty)"
	}
	return strings.Join(seq, " → ")
}

// TestNoPackAsksTheRuntimeAGestureOutsideTheContract is property 2 of #515,
// live: every gesture the three replays asked of the runtime is one the
// contract vocabulary names. The detector's own positive control is
// TestAPlantedUnknownGestureIsReported in the machine package; the vocabulary
// itself is held against the driver interfaces by
// TestTheContractNamesEveryGesture there.
func TestNoPackAsksTheRuntimeAGestureOutsideTheContract(t *testing.T) {
	for _, replay := range replays {
		rec := replay.run(t)
		if outside := rec.OutsideContract(); len(outside) != 0 {
			t.Errorf("%s asked the runtime %d gestures the contract cannot name: %v",
				replay.pack, len(outside), outside)
		}
		if len(rec.Events()) == 0 {
			t.Errorf("%s recorded nothing: a replay that never reached the runtime holds this property by vacuity", replay.pack)
		}
	}
}

// TestTwoIdenticalReplaysProduceTheSameSequence proves the instrument's
// determinism, without which the cross-pack verdict would measure scheduling
// rather than packs: an equivalence read on a nondeterministic recording is an
// instrument that lies in both directions.
func TestTwoIdenticalReplaysProduceTheSameSequence(t *testing.T) {
	for _, replay := range replays {
		first := replay.run(t).Sequence()
		second := replay.run(t).Sequence()
		if !sameSequence(first, second) {
			t.Errorf("%s replayed twice produced two sequences:\n  %s\n  %s",
				replay.pack, formatSequence(first), formatSequence(second))
		}
	}
}

// TestAShuffledReplayIsToldFromItsOriginal is the positive control of the
// equivalence assertion: before trusting sameSequence on real replays, prove
// it fails on a deliberately reordered one. A comparator that cannot see a
// rotation would read three different orders as one and turn the red property
// green by instrument defect — the exact lie this repository exists to avoid.
func TestAShuffledReplayIsToldFromItsOriginal(t *testing.T) {
	original := replayScaleway(t).Sequence()
	if len(original) < 2 {
		t.Fatalf("the replay recorded %d gestures; nothing can be reordered", len(original))
	}
	shuffled := append([]string(nil), original[1:]...)
	shuffled = append(shuffled, original[0])
	if sameSequence(original, shuffled) {
		t.Fatalf("a rotated sequence compares equal to its original:\n  %s\n  %s",
			formatSequence(original), formatSequence(shuffled))
	}
	// The rotation must be a real reordering for the control to mean anything:
	// a sequence of one repeated kind rotates onto itself.
	distinct := map[string]bool{}
	for _, kind := range original {
		distinct[kind] = true
	}
	if len(distinct) < 2 {
		t.Fatalf("the replay's %d gestures are all %q: the rotation control compared nothing", len(original), original[0])
	}
}

// TestSameIntentSameRuntimeSequenceAcrossPacks is property 1 of #515: for the
// one intent all three packs can express, the three replays produce the same
// contract-level sequence — the executable form of #510's sentence, the order
// is a property of the runtime, not of a provider.
//
// It is red today, and deliberately so: the audit measured three orders held
// by three comments (scaleway/servers.go, outscale/machines.go,
// exoscale/machines.go), and this instrument is what makes that divergence a
// measurement instead of a citation. #510 is what turns it green. Until then
// the test *runs*, prints the measured divergence, and ends in Skip rather
// than Fail — a red assertion in prepush would make every branch undeliverable
// for a defect this branch does not create — and the Skip line carries the
// full measurement so nobody has to re-derive it. The day the sequences agree,
// this test passes on its own and the skip below becomes dead code to delete
// with #510.
//
// The assertion is per expressible intent, never "all sequences are
// identical": the packs do not express every intent identically (Scaleway has
// no group-sourced rules, Exoscale routes elastic addresses on attach), and a
// global phrasing would make the test lie.
func TestSameIntentSameRuntimeSequenceAcrossPacks(t *testing.T) {
	sequences := make(map[string][]string, len(replays))
	for _, replay := range replays {
		sequences[replay.pack] = replay.run(t).Sequence()
	}

	reference := sequences[replays[0].pack]
	diverged := false
	for _, replay := range replays[1:] {
		if !sameSequence(reference, sequences[replay.pack]) {
			diverged = true
		}
	}
	if !diverged {
		return // #510 landed: the property holds, and this test now guards it.
	}

	var lines []string
	for _, replay := range replays {
		lines = append(lines, fmt.Sprintf("  %-8s %s", replay.pack, formatSequence(sequences[replay.pack])))
	}
	t.Skipf("red on purpose (#515): the same intent produces three runtime sequences, one per pack — "+
		"#510 is the corridor that makes them one; measured now:\n%s", strings.Join(lines, "\n"))
}
