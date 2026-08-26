// Package providers holds the properties that only make sense across packs.
//
// Each pack's own tests prove its dialect against its own client; nothing in
// them can state that two packs agree. This test-only package mounts the packs
// on the shared contract recorder (machine.NewRecorder, #515) and replays the
// same intent into each, so the cross-pack properties become statable: same
// intent, same runtime sequence (#510's sentence, green since the Reconciler),
// and no gesture outside the contract (#514's service list, held dynamically).
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

// The equivalence is asserted per expressible intent, never "all sequences are
// identical": the packs do not express every intent identically, because the
// clouds do not. Each intent below names the packs that can express it and the
// written reason the others cannot — a reason must be an upstream difference,
// not a convenience — and every intent is expressed by at least two packs, or
// it would compare nothing. What is compared is never the dialect: it is the
// sequence of contract gestures the pack asked of the runtime.

// packReplay is one pack's expression of an intent, in its own dialect.
type packReplay struct {
	pack string
	run  func(*testing.T) *machine.Recorder
}

// intent is one thing a user asks of a cloud, with every pack that can say it.
type intent struct {
	name string
	// excluded documents, per absent pack, the upstream difference that keeps
	// it out. The map is read by the coverage assertion below: an exclusion
	// without a reason is a restriction nobody argued.
	excluded map[string]string
	replays  []packReplay
}

// intents is the corpus. Two properties are load-bearing and asserted by
// TestSameIntentSameRuntimeSequenceAcrossPacks itself: every pack appears in
// at least one intent, and the union of recorded gestures still covers the
// mutating vocabulary — so no restriction of the statement can silently retire
// a pack or a gesture from the comparison.
var intents = []intent{
	{
		// The richest common intent: one machine booting with its cloud-given
		// public address, wearing one rule-bearing group, joining one private
		// network after boot, gaining one more public address after boot.
		// Scaleway says it with dynamic_ip_required, a hot private NIC and a
		// flexible IP; Exoscale with its instance's own public IP, a private
		// network attach and an elastic IP.
		name: "the machine boots public, joins its network and gains an address afterwards",
		excluded: map[string]string{
			"outscale": "a Vm is born on its Subnet — the membership rides the launch, it cannot be joined " +
				"after boot — and no Vm carries an emulated public address at boot: LinkPublicIp is " +
				"post-create by design, so neither half of this intent is expressible",
		},
		replays: []packReplay{
			{"scaleway", replayScalewayJoinsAfterBoot},
			{"exoscale", replayExoscaleJoinsAfterBoot},
		},
	},
	{
		// The intent every pack can say: one machine wearing one rule-bearing
		// group, its one public address linked after boot, no private network.
		// Outscale's Vm lives outside any Net (its public-cloud shape),
		// Exoscale declares public-ip-assignment none, Scaleway boots without
		// dynamic_ip_required.
		name:     "the machine wears a group and its address is linked after boot",
		excluded: map[string]string{},
		replays: []packReplay{
			{"scaleway", replayScalewayLinkedAfterBoot},
			{"outscale", replayOutscaleLinkedAfterBoot},
			{"exoscale", replayExoscaleLinkedAfterBoot},
		},
	},
}

// allReplays flattens the corpus for the properties that hold per replay.
func allReplays() []packReplay {
	var out []packReplay
	for _, in := range intents {
		out = append(out, in.replays...)
	}
	return out
}

// recorderEnv builds a deterministic Env backed by a fresh contract recorder.
//
// The address placements are forgotten first: they live in a package-level map
// keyed by provider and address — state that must outlive one request cannot
// live in a per-call Binding — and two replays of one dialect hand out the
// same deterministic addresses. Without the reset, the second replay's route
// finds the first replay's machine as the recorded holder and emits an
// UnrouteAddress the first never did: the determinism property would then be
// measuring this suite's own residue, not the pack.
func recorderEnv() (*emulator.Env, *machine.Recorder) {
	for _, provider := range []string{scaleway.Name, outscale.Name, exoscale.Name} {
		machine.Binding{Provider: provider}.ForgetPlacements()
	}
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

// ---- Intent one: boots public, joins afterwards -----------------------------

// replayScalewayJoinsAfterBoot: the dynamic address rides the boot, the
// private NIC is attached to the running server, the flexible IP afterwards.
func replayScalewayJoinsAfterBoot(t *testing.T) *machine.Recorder {
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

	sgID := scalewayGroupWithRule(t, h)

	created := request(t, h, "POST", zone+"/servers",
		`{"name":"intent-machine","commercial_type":"DEV1-S","image":"ubuntu_jammy","security_group":"`+sgID+`","dynamic_ip_required":true}`)
	serverID := id(t, created, "server", "id")
	request(t, h, "POST", zone+"/servers/"+serverID+"/action", `{"action":"poweron"}`)

	request(t, h, "POST", zone+"/servers/"+serverID+"/private_nics",
		`{"private_network_id":"`+pnID+`"}`)

	ip := request(t, h, "POST", zone+"/ips", `{}`)
	request(t, h, "PATCH", zone+"/ips/"+id(t, ip, "ip", "id"), `{"server":"`+serverID+`"}`)
	return rec
}

// replayExoscaleJoinsAfterBoot: the instance's own public IP rides the boot —
// Exoscale's eth0 is the public interface — the private network and the
// elastic IP are attached to the running machine.
func replayExoscaleJoinsAfterBoot(t *testing.T) *machine.Recorder {
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

	sgID := exoscaleGroupWithRule(t, h)
	instanceID := exoscaleInstance(t, h, sgID, "")

	request(t, h, "PUT", "/v2/private-network/"+pnID+":attach",
		`{"instance":{"id":"`+instanceID+`"}}`)

	eip := request(t, h, "POST", "/v2/elastic-ip", `{}`)
	request(t, h, "PUT", "/v2/elastic-ip/"+id(t, eip, "reference", "id")+":attach",
		`{"instance":{"id":"`+instanceID+`"}}`)
	return rec
}

// ---- Intent two: a group, and the address linked after boot -----------------

// replayScalewayLinkedAfterBoot: the server boots wearing its group and
// nothing else; the flexible IP is attached afterwards.
func replayScalewayLinkedAfterBoot(t *testing.T) *machine.Recorder {
	t.Helper()
	env, rec := recorderEnv()
	srv, err := emulator.NewServer(env, scaleway.New(env))
	if err != nil {
		t.Fatalf("mount scaleway: %v", err)
	}
	h := srv.Handler()
	const zone = "/instance/v1/zones/fr-par-1"

	sgID := scalewayGroupWithRule(t, h)
	created := request(t, h, "POST", zone+"/servers",
		`{"name":"intent-machine","commercial_type":"DEV1-S","image":"ubuntu_jammy","security_group":"`+sgID+`"}`)
	serverID := id(t, created, "server", "id")
	request(t, h, "POST", zone+"/servers/"+serverID+"/action", `{"action":"poweron"}`)

	ip := request(t, h, "POST", zone+"/ips", `{}`)
	request(t, h, "PATCH", zone+"/ips/"+id(t, ip, "ip", "id"), `{"server":"`+serverID+`"}`)
	return rec
}

// replayOutscaleLinkedAfterBoot: the Vm lives outside any Net — Outscale's
// public-cloud shape, the one placement where nothing of its network rides the
// launch — wearing its group; the public IP is linked afterwards, which is the
// only moment its dialect can link one.
func replayOutscaleLinkedAfterBoot(t *testing.T) *machine.Recorder {
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

	sg := post("CreateSecurityGroup",
		`{"SecurityGroupName":"intent-group","Description":"the intent's one group"}`)
	sgID := id(t, sg, "SecurityGroup", "SecurityGroupId")
	post("CreateSecurityGroupRule",
		`{"Flow":"Inbound","SecurityGroupId":"`+sgID+`","IpProtocol":"tcp","FromPortRange":22,"ToPortRange":22,"IpRange":"0.0.0.0/0"}`)

	ip := post("CreatePublicIp", `{}`)
	ipID := id(t, ip, "PublicIp", "PublicIpId")

	vms := post("CreateVms",
		`{"ImageId":"ami-00000001","VmType":"tinav6.c1r1p2","SecurityGroupIds":["`+sgID+`"]}`)
	list, _ := vms["Vms"].([]any)
	if len(list) != 1 {
		t.Fatalf("CreateVms answered %d Vms, want 1: %v", len(list), vms)
	}
	vmID := id(t, map[string]any{"Vm": list[0]}, "Vm", "VmId")

	post("LinkPublicIp", `{"PublicIpId":"`+ipID+`","VmId":"`+vmID+`"}`)
	return rec
}

// replayExoscaleLinkedAfterBoot: the instance declares public-ip-assignment
// none — a real upstream setting — so nothing rides the boot but the group;
// the elastic IP is attached afterwards.
func replayExoscaleLinkedAfterBoot(t *testing.T) *machine.Recorder {
	t.Helper()
	env, rec := recorderEnv()
	srv, err := emulator.NewServer(env, exoscale.New(env))
	if err != nil {
		t.Fatalf("mount exoscale: %v", err)
	}
	h := srv.Handler()

	sgID := exoscaleGroupWithRule(t, h)
	instanceID := exoscaleInstance(t, h, sgID, `"public-ip-assignment":"none",`)

	eip := request(t, h, "POST", "/v2/elastic-ip", `{}`)
	request(t, h, "PUT", "/v2/elastic-ip/"+id(t, eip, "reference", "id")+":attach",
		`{"instance":{"id":"`+instanceID+`"}}`)
	return rec
}

// ---- Dialect helpers shared by the intents ----------------------------------

func scalewayGroupWithRule(t *testing.T, h http.Handler) string {
	t.Helper()
	const zone = "/instance/v1/zones/fr-par-1"
	sg := request(t, h, "POST", zone+"/security_groups",
		`{"name":"intent-group","description":"the intent's one group","inbound_default_policy":"drop"}`)
	sgID := id(t, sg, "security_group", "id")
	request(t, h, "POST", zone+"/security_groups/"+sgID+"/rules",
		`{"protocol":"TCP","direction":"inbound","action":"accept","ip_range":"0.0.0.0/0","dest_port_from":22,"dest_port_to":22}`)
	return sgID
}

func exoscaleGroupWithRule(t *testing.T, h http.Handler) string {
	t.Helper()
	sg := request(t, h, "POST", "/v2/security-group",
		`{"name":"intent-group","description":"the intent's one group"}`)
	sgID := id(t, sg, "reference", "id")
	request(t, h, "POST", "/v2/security-group/"+sgID+"/rules",
		`{"flow-direction":"ingress","protocol":"tcp","network":"0.0.0.0/0","start-port":22,"end-port":22}`)
	return sgID
}

func exoscaleInstance(t *testing.T, h http.Handler, sgID, extra string) string {
	t.Helper()
	request(t, h, "POST", "/v2/instance", `{
		"name":"intent-machine",
		"instance-type":{"id":"21624abb-764e-4def-81d7-9fc54b5957fb"},
		"template":{"id":"11111111-1111-4111-8111-111111111111"},
		"disk-size":10,
		`+extra+`
		"security-groups":[{"id":"`+sgID+`"}]
	}`)
	instances := request(t, h, "GET", "/v2/instance", "")
	list, _ := instances["instances"].([]any)
	if len(list) != 1 {
		t.Fatalf("%d instances after create, want 1: %v", len(list), instances)
	}
	return id(t, map[string]any{"i": list[0]}, "i", "id")
}

// ---- The properties ---------------------------------------------------------

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
// live: every gesture the replays asked of the runtime is one the contract
// vocabulary names. The detector's own positive control is
// TestAPlantedUnknownGestureIsReported in the machine package; the vocabulary
// itself is held against the driver interfaces by
// TestTheContractNamesEveryGesture there.
func TestNoPackAsksTheRuntimeAGestureOutsideTheContract(t *testing.T) {
	for _, replay := range allReplays() {
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
	for _, replay := range allReplays() {
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
// rotation would read three different orders as one and turn the property
// green by instrument defect — the exact lie this repository exists to avoid.
func TestAShuffledReplayIsToldFromItsOriginal(t *testing.T) {
	original := replayScalewayJoinsAfterBoot(t).Sequence()
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

// TestSameIntentSameRuntimeSequenceAcrossPacks is property 1 of #515: for each
// intent, every pack that can express it produces the same contract-level
// sequence — the executable form of #510's sentence, the order is a property
// of the runtime, not of a provider. It was red from #515 to #510 — three
// orders, held by three comments — and the Reconciler is what made it green;
// this assertion is now what guards it.
//
// The statement is restricted to the expressible intents, with each
// restriction written where the intent declares it, and the restriction
// itself is fenced: an intent compared alone, a pack retired from every
// intent, a missing reason, or a gesture kind gone from the whole corpus all
// fail here — the ways a comparison quietly relaxes, each one closed.
func TestSameIntentSameRuntimeSequenceAcrossPacks(t *testing.T) {
	packs := map[string]bool{"scaleway": false, "outscale": false, "exoscale": false}
	recorded := map[string]bool{}

	for _, in := range intents {
		if len(in.replays) < 2 {
			t.Fatalf("intent %q is expressed by %d pack(s): it compares nothing", in.name, len(in.replays))
		}
		present := map[string]bool{}
		for _, replay := range in.replays {
			present[replay.pack] = true
		}
		for pack := range packs {
			if !present[pack] && in.excluded[pack] == "" {
				t.Fatalf("intent %q leaves %s out without a written reason", in.name, pack)
			}
			if present[pack] && in.excluded[pack] != "" {
				t.Fatalf("intent %q both includes %s and excuses its absence", in.name, pack)
			}
		}

		sequences := make(map[string][]string, len(in.replays))
		for _, replay := range in.replays {
			seq := replay.run(t).Sequence()
			sequences[replay.pack] = seq
			packs[replay.pack] = true
			for _, kind := range seq {
				recorded[kind] = true
			}
		}
		reference := sequences[in.replays[0].pack]
		for _, replay := range in.replays[1:] {
			if !sameSequence(reference, sequences[replay.pack]) {
				var lines []string
				for _, r := range in.replays {
					lines = append(lines, fmt.Sprintf("  %-8s %s", r.pack, formatSequence(sequences[r.pack])))
				}
				t.Fatalf("intent %q produces diverging runtime sequences:\n%s", in.name, strings.Join(lines, "\n"))
			}
		}
	}

	for pack, seen := range packs {
		if !seen {
			t.Fatalf("%s is compared in no intent: the property no longer says anything about it", pack)
		}
	}
	// The mutating vocabulary the intents must keep exercising: dropping one
	// of these from every replay would shrink what the equivalence proves,
	// which is exactly how a comparison relaxes without touching sameSequence.
	for _, kind := range []string{"EnsureNetwork", "PeerNetworks", "EnsureFirewall", "ApplyFirewall", "Start", "Attach", "RouteAddress"} {
		if !recorded[kind] {
			t.Fatalf("no replay of any intent records %s any more: the compared vocabulary shrank", kind)
		}
	}
}
