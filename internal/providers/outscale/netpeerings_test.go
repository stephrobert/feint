package outscale_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/providers/outscale"
)

// The Net peering lifecycle. The states and their spellings come from the
// SDK's NetPeeringStateName enum, and what each operation accepts as a
// starting state from the operation docs beside it; the assertions here hold
// the emulator to exactly those, because the Terraform provider branches on
// the strings (its refresh treats `failed` as an error and waits on
// `pending-acceptance` and `active` by name).

func peeringOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	peering, ok := body["NetPeering"].(map[string]any)
	if !ok {
		t.Fatalf("no NetPeering in %v", body)
	}
	return peering
}

func stateNameOf(t *testing.T, peering map[string]any) string {
	t.Helper()
	state, ok := peering["State"].(map[string]any)
	if !ok {
		t.Fatalf("no State in %v", peering)
	}
	name, _ := state["Name"].(string)
	return name
}

func TestANetPeeringLifecycleMatchesTheContract(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	source, _ := netAndSubnet(t, ts, "10.31.0.0/16", "10.31.1.0/24")
	accepter, _ := netAndSubnet(t, ts, "10.32.0.0/16", "10.32.1.0/24")

	created := call(t, ts, doc, "CreateNetPeering",
		`{"SourceNetId":"`+source+`","AccepterNetId":"`+accepter+`"}`)
	peering := peeringOf(t, created)
	id, _ := peering["NetPeeringId"].(string)
	if id == "" {
		t.Fatalf("no NetPeeringId in %v", peering)
	}
	if got := stateNameOf(t, peering); got != "pending-acceptance" {
		t.Fatalf("a fresh peering is %q, want pending-acceptance", got)
	}
	// The message spelling is measured, from the API document's own response
	// example: "Pending acceptance by <account>".
	state, _ := peering["State"].(map[string]any)
	if got, _ := state["Message"].(string); got != "Pending acceptance by 000000000001" {
		t.Fatalf("pending message = %q", got)
	}
	// Both ends carry the account, the range and the id of their Net.
	src, _ := peering["SourceNet"].(map[string]any)
	if src["NetId"] != source || src["IpRange"] != "10.31.0.0/16" || src["AccountId"] != "000000000001" {
		t.Fatalf("SourceNet = %v", src)
	}
	acc, _ := peering["AccepterNet"].(map[string]any)
	if acc["NetId"] != accepter || acc["IpRange"] != "10.32.0.0/16" {
		t.Fatalf("AccepterNet = %v", acc)
	}
	// The 7-day window the SDK documents is published and stable across reads.
	expiry, _ := peering["ExpirationDate"].(string)
	if expiry == "" {
		t.Fatalf("a pending peering carries no ExpirationDate: %v", peering)
	}

	// Create-then-read round-trips, field for field.
	read := call(t, ts, doc, "ReadNetPeerings",
		`{"Filters":{"NetPeeringIds":["`+id+`"]}}`)
	listed, _ := read["NetPeerings"].([]any)
	if len(listed) != 1 {
		t.Fatalf("ReadNetPeerings found %d, want 1", len(listed))
	}
	if got, _ := listed[0].(map[string]any)["ExpirationDate"].(string); got != expiry {
		t.Fatalf("ExpirationDate moved between reads: %q then %q", expiry, got)
	}

	accepted := call(t, ts, doc, "AcceptNetPeering", `{"NetPeeringId":"`+id+`"}`)
	active := peeringOf(t, accepted)
	if got := stateNameOf(t, active); got != "active" {
		t.Fatalf("accepted peering is %q, want active", got)
	}
	if got, _ := active["State"].(map[string]any)["Message"].(string); got != "Active" {
		t.Fatalf("active message = %q", got)
	}

	// The state filters answer by the SDK's names.
	byState := call(t, ts, doc, "ReadNetPeerings",
		`{"Filters":{"StateNames":["active"],"SourceNetNetIds":["`+source+`"]}}`)
	if rows, _ := byState["NetPeerings"].([]any); len(rows) != 1 {
		t.Fatalf("StateNames active found %d, want 1", len(rows))
	}
	byPending := call(t, ts, doc, "ReadNetPeerings",
		`{"Filters":{"StateNames":["pending-acceptance"]}}`)
	if rows, _ := byPending["NetPeerings"].([]any); len(rows) != 0 {
		t.Fatalf("StateNames pending-acceptance found %d, want 0", len(rows))
	}

	// An active peering deletes, and the record stays readable in the deleted
	// state rather than vanishing.
	call(t, ts, doc, "DeleteNetPeering", `{"NetPeeringId":"`+id+`"}`)
	after := call(t, ts, doc, "ReadNetPeerings",
		`{"Filters":{"NetPeeringIds":["`+id+`"]}}`)
	rows, _ := after["NetPeerings"].([]any)
	if len(rows) != 1 {
		t.Fatalf("a deleted peering vanished from ReadNetPeerings")
	}
	if got := stateNameOf(t, rows[0].(map[string]any)); got != "deleted" {
		t.Fatalf("after delete the peering is %q, want deleted", got)
	}

	// Deleting it twice is a state conflict, not a success saying it did
	// something.
	if status, _ := post(t, ts, "DeleteNetPeering", `{"NetPeeringId":"`+id+`"}`); status != http.StatusConflict {
		t.Fatalf("a second delete answered %d, want 409", status)
	}
}

// aNet creates one Net and returns its id.
func aNet(t *testing.T, ts *httptest.Server, cidr string) string {
	t.Helper()
	_, out := post(t, ts, "CreateNet", `{"IpRange":"`+cidr+`"}`)
	n, _ := out["Net"].(map[string]any)
	id, _ := n["NetId"].(string)
	if id == "" {
		t.Fatalf("no Net was created on %s: %v", cidr, out)
	}
	return id
}

// aPeering creates one pending peering between two fresh Nets and returns its
// id. The blocks must differ per call: CreateNet refuses an overlap.
func aPeering(t *testing.T, ts *httptest.Server, sourceCIDR, accepterCIDR string) string {
	t.Helper()
	source := aNet(t, ts, sourceCIDR)
	accepter := aNet(t, ts, accepterCIDR)
	_, out := post(t, ts, "CreateNetPeering",
		`{"SourceNetId":"`+source+`","AccepterNetId":"`+accepter+`"}`)
	id, _ := peeringOf(t, out)["NetPeeringId"].(string)
	if id == "" {
		t.Fatalf("no peering was created: %v", out)
	}
	return id
}

func errorTypeOf(body map[string]any) string {
	errs, _ := body["Errors"].([]any)
	if len(errs) == 0 {
		return ""
	}
	first, _ := errs[0].(map[string]any)
	kind, _ := first["Type"].(string)
	return kind
}

// TestAcceptingANonPendingNetPeeringIsRefused holds the accept side of the
// state machine: "The Net peering must be in the `pending-acceptance` state".
// Accepting a rejected one is the conflict the real cloud answers.
func TestAcceptingANonPendingNetPeeringIsRefused(t *testing.T) {
	ts := newServer(t)
	id := aPeering(t, ts, "10.33.0.0/16", "10.34.0.0/16")

	if status, _ := post(t, ts, "RejectNetPeering", `{"NetPeeringId":"`+id+`"}`); status != http.StatusOK {
		t.Fatalf("reject of a pending peering answered %d", status)
	}
	status, body := post(t, ts, "AcceptNetPeering", `{"NetPeeringId":"`+id+`"}`)
	if status != http.StatusConflict {
		t.Fatalf("accepting a rejected peering answered %d, want 409", status)
	}
	if got := errorTypeOf(body); got != "ResourceConflict" {
		t.Fatalf("the refusal is typed %q, want ResourceConflict", got)
	}
}

// TestRejectingANonPendingNetPeeringIsRefused holds the reject side: an active
// peering is not un-accepted by rejecting it.
func TestRejectingANonPendingNetPeeringIsRefused(t *testing.T) {
	ts := newServer(t)
	id := aPeering(t, ts, "10.35.0.0/16", "10.36.0.0/16")

	if status, _ := post(t, ts, "AcceptNetPeering", `{"NetPeeringId":"`+id+`"}`); status != http.StatusOK {
		t.Fatalf("accept of a pending peering failed")
	}
	status, body := post(t, ts, "RejectNetPeering", `{"NetPeeringId":"`+id+`"}`)
	if status != http.StatusConflict {
		t.Fatalf("rejecting an active peering answered %d, want 409", status)
	}
	if got := errorTypeOf(body); got != "ResourceConflict" {
		t.Fatalf("the refusal is typed %q, want ResourceConflict", got)
	}
}

// TestDeletingARejectedNetPeeringIsRefused holds the delete triage the SDK
// spells out: rejected, failed and expired cannot be deleted.
func TestDeletingARejectedNetPeeringIsRefused(t *testing.T) {
	ts := newServer(t)
	id := aPeering(t, ts, "10.37.0.0/16", "10.38.0.0/16")

	post(t, ts, "RejectNetPeering", `{"NetPeeringId":"`+id+`"}`)
	status, body := post(t, ts, "DeleteNetPeering", `{"NetPeeringId":"`+id+`"}`)
	if status != http.StatusConflict {
		t.Fatalf("deleting a rejected peering answered %d, want 409", status)
	}
	if got := errorTypeOf(body); got != "ResourceConflict" {
		t.Fatalf("the refusal is typed %q, want ResourceConflict", got)
	}
}

// TestAPeeringOfANetWithItselfIsBornFailed reaches the `failed` state through
// the only door this emulator leaves open: "The two Nets must not have
// overlapping IP ranges. Otherwise, the Net peering is in the `failed` state."
// Two *distinct* Nets can never overlap here — CreateNet refuses the overlap
// outright, because every Net backs a real block on the host — so the
// reachable overlap is a Net peered with itself.
func TestAPeeringOfANetWithItselfIsBornFailed(t *testing.T) {
	ts := newServer(t)
	netID := aNet(t, ts, "10.39.0.0/16")

	status, out := post(t, ts, "CreateNetPeering",
		`{"SourceNetId":"`+netID+`","AccepterNetId":"`+netID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("the create itself answered %d: the refusal is a state, not an error", status)
	}
	peering := peeringOf(t, out)
	if got := stateNameOf(t, peering); got != "failed" {
		t.Fatalf("an overlapping peering is %q, want failed", got)
	}
	// And a failed one cannot be deleted, per the same SDK doc.
	id, _ := peering["NetPeeringId"].(string)
	if status, _ := post(t, ts, "DeleteNetPeering", `{"NetPeeringId":"`+id+`"}`); status != http.StatusConflict {
		t.Fatalf("deleting a failed peering answered %d, want 409", status)
	}
}

// TestAcceptAutoRejectsTheReversePendingPeering: "when an A-to-B peering
// connection is accepted, any pending B-to-A peering connection is
// automatically rejected as redundant" (AcceptNetPeering doc).
func TestAcceptAutoRejectsTheReversePendingPeering(t *testing.T) {
	ts := newServer(t)
	a := aNet(t, ts, "10.40.0.0/16")
	b := aNet(t, ts, "10.41.0.0/16")

	_, first := post(t, ts, "CreateNetPeering", `{"SourceNetId":"`+a+`","AccepterNetId":"`+b+`"}`)
	forward, _ := peeringOf(t, first)["NetPeeringId"].(string)
	_, second := post(t, ts, "CreateNetPeering", `{"SourceNetId":"`+b+`","AccepterNetId":"`+a+`"}`)
	reverse, _ := peeringOf(t, second)["NetPeeringId"].(string)

	post(t, ts, "AcceptNetPeering", `{"NetPeeringId":"`+forward+`"}`)

	_, read := post(t, ts, "ReadNetPeerings", `{"Filters":{"NetPeeringIds":["`+reverse+`"]}}`)
	rows, _ := read["NetPeerings"].([]any)
	if len(rows) != 1 {
		t.Fatalf("the reverse peering vanished")
	}
	if got := stateNameOf(t, rows[0].(map[string]any)); got != "rejected" {
		t.Fatalf("after accepting A-to-B, B-to-A is %q, want rejected", got)
	}
}

// TestCreatingTheReverseOfAnActivePeeringIsBornRejected: "If an A-to-B
// connection is already created and accepted, creating a B-to-A connection is
// not necessary and would be automatically rejected" (CreateNetPeering doc).
func TestCreatingTheReverseOfAnActivePeeringIsBornRejected(t *testing.T) {
	ts := newServer(t)
	a := aNet(t, ts, "10.43.0.0/16")
	b := aNet(t, ts, "10.44.0.0/16")

	_, first := post(t, ts, "CreateNetPeering", `{"SourceNetId":"`+a+`","AccepterNetId":"`+b+`"}`)
	forward, _ := peeringOf(t, first)["NetPeeringId"].(string)
	post(t, ts, "AcceptNetPeering", `{"NetPeeringId":"`+forward+`"}`)

	_, second := post(t, ts, "CreateNetPeering", `{"SourceNetId":"`+b+`","AccepterNetId":"`+a+`"}`)
	if got := stateNameOf(t, peeringOf(t, second)); got != "rejected" {
		t.Fatalf("the reverse of an active peering is born %q, want rejected", got)
	}
}

// TestNetPeeringUnknownIdsAreInvalidResource: an identifier naming nothing is
// a 400 InvalidResource in this dialect, never a 404 — and an AccepterOwnerId
// that is not the emulator's single account names a Net that cannot exist
// here, which gets the same answer.
func TestNetPeeringUnknownIdsAreInvalidResource(t *testing.T) {
	ts := newServer(t)
	netID := aNet(t, ts, "10.45.0.0/16")

	for _, tc := range []struct{ action, body string }{
		{"CreateNetPeering", `{"SourceNetId":"vpc-00000bad","AccepterNetId":"` + netID + `"}`},
		{"CreateNetPeering", `{"SourceNetId":"` + netID + `","AccepterNetId":"vpc-00000bad"}`},
		{"CreateNetPeering", `{"SourceNetId":"` + netID + `","AccepterNetId":"` + netID + `","AccepterOwnerId":"999999999999"}`},
		{"AcceptNetPeering", `{"NetPeeringId":"pcx-00000bad"}`},
		{"RejectNetPeering", `{"NetPeeringId":"pcx-00000bad"}`},
		{"DeleteNetPeering", `{"NetPeeringId":"pcx-00000bad"}`},
	} {
		status, body := post(t, ts, tc.action, tc.body)
		if status != http.StatusBadRequest {
			t.Errorf("%s %s answered %d, want 400", tc.action, tc.body, status)
		}
		if got := errorTypeOf(body); got != "InvalidResource" {
			t.Errorf("%s refusal is typed %q, want InvalidResource", tc.action, got)
		}
	}
}

// TestNetPeeringFiltersAreAppliedOrRefused: a filter is either applied or
// refused with its name, never silently ignored — the pack-wide rule
// filters.go states, held here for the newest Read*.
func TestNetPeeringFiltersAreAppliedOrRefused(t *testing.T) {
	ts := newServer(t)
	id := aPeering(t, ts, "10.46.0.0/16", "10.47.0.0/16")

	status, body := post(t, ts, "ReadNetPeerings", `{"Filters":{"ExpirationDates":["2026-01-01T00:00:00.000Z"]}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("an unapplied filter was accepted: %d %v", status, body)
	}

	_, read := post(t, ts, "ReadNetPeerings", `{"Filters":{"StateMessages":["Pending acceptance by 000000000001"]}}`)
	rows, _ := read["NetPeerings"].([]any)
	if len(rows) != 1 {
		t.Fatalf("StateMessages matched %d peerings, want 1 (%s)", len(rows), id)
	}
}

// TestAPeeringIsTaggable: the Terraform provider calls CreateTags with the
// pcx- identifier it just received, and ReadTags files the tag under the
// SDK enum's own name for the kind, vpc-peering-connection.
func TestAPeeringIsTaggable(t *testing.T) {
	ts := newServer(t)
	id := aPeering(t, ts, "10.48.0.0/16", "10.49.0.0/16")

	if status, body := post(t, ts, "CreateTags",
		`{"ResourceIds":["`+id+`"],"Tags":[{"Key":"Name","Value":"conformance-pcx"}]}`); status != http.StatusOK {
		t.Fatalf("CreateTags on a peering answered %d: %v", status, body)
	}
	_, read := post(t, ts, "ReadTags", `{"Filters":{"ResourceTypes":["vpc-peering-connection"]}}`)
	rows, _ := read["Tags"].([]any)
	if len(rows) != 1 {
		t.Fatalf("ReadTags found %d vpc-peering-connection tags, want 1", len(rows))
	}
	row, _ := rows[0].(map[string]any)
	if row["ResourceId"] != id || row["Key"] != "Name" {
		t.Fatalf("the tag row is %v", row)
	}
}

// ---- The runtime half --------------------------------------------------------

// peererRuntime is a Driver whose networks are born separate, like OVN: it
// records every PeerNetworks reconciliation so a test can assert what an
// accept grants and a delete takes back.
type peererRuntime struct {
	*routedRuntime
	mu    sync.Mutex
	peers map[string][]string
}

func newPeererRuntime() *peererRuntime {
	return &peererRuntime{routedRuntime: newRoutedRuntime(), peers: map[string][]string{}}
}

func (r *peererRuntime) NativeIsolation() bool { return true }

func (r *peererRuntime) PeerNetworks(_ context.Context, network string, peers []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.peers[network] = append([]string(nil), peers...)
	return nil
}

func (r *peererRuntime) peersOf(network string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.peers[network]...)
}

// TestAnAcceptedPeeringPeersTheBackingNetworks: on a natively-isolating
// runtime, accepting a peering peers every backing network of one Net with
// every backing network of the other — in both directions, because a peering
// works both ways — and deleting the peering reconciles both back to nothing.
// A pending peering grants nothing.
func TestAnAcceptedPeeringPeersTheBackingNetworks(t *testing.T) {
	env := emulator.DefaultEnv()
	rt := newPeererRuntime()
	env.Machines = rt
	srv, err := emulator.NewServer(env, outscale.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	netA, subnetA := netAndSubnet(t, ts, "10.50.0.0/16", "10.50.1.0/24")
	netB, subnetB := netAndSubnet(t, ts, "10.51.0.0/16", "10.51.1.0/24")
	networkA := machine.NetworkName(machine.NetworkPrefix, subnetA)
	networkB := machine.NetworkName(machine.NetworkPrefix, subnetB)

	_, out := post(t, ts, "CreateNetPeering", `{"SourceNetId":"`+netA+`","AccepterNetId":"`+netB+`"}`)
	id, _ := peeringOf(t, out)["NetPeeringId"].(string)

	// Pending grants nothing: no reconciliation has named these networks yet.
	if got := rt.peersOf(networkA); len(got) != 0 {
		t.Fatalf("a pending peering already peered %s with %v", networkA, got)
	}

	post(t, ts, "AcceptNetPeering", `{"NetPeeringId":"`+id+`"}`)
	if got := rt.peersOf(networkA); !has(got, networkB) {
		t.Fatalf("after accept, %s peers with %v, want %s", networkA, got, networkB)
	}
	if got := rt.peersOf(networkB); !has(got, networkA) {
		t.Fatalf("after accept, %s peers with %v, want %s", networkB, got, networkA)
	}

	post(t, ts, "DeleteNetPeering", `{"NetPeeringId":"`+id+`"}`)
	if got := rt.peersOf(networkA); len(got) != 0 {
		t.Fatalf("after delete, %s still peers with %v", networkA, got)
	}
	if got := rt.peersOf(networkB); len(got) != 0 {
		t.Fatalf("after delete, %s still peers with %v", networkB, got)
	}
}

// TestACreateSubnetDoesNotSeverAnActivePeering is the audit witness of #508.
//
// Two Nets, one subnet each, a peering accepted: both backing networks peer.
// Then one ordinary CreateSubnet in the source Net. Before the fix, two
// reconcilers wrote this runtime state with two different truths — the
// subnet-side pass knew "same Net, and nothing else", the peering-side pass
// knew "active peering" — and machine.PeerNetworks reconciles rather than
// appends, so the subnet create's pass severed the active peering of the
// existing subnet, and the newborn subnet never joined it. Both properties
// are asserted on the recorder, and the negative control matters as much: a
// third, unpeered Net goes through the same sequence and stays out of every
// peer list.
func TestACreateSubnetDoesNotSeverAnActivePeering(t *testing.T) {
	env := emulator.DefaultEnv()
	rt := newPeererRuntime()
	env.Machines = rt
	srv, err := emulator.NewServer(env, outscale.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	netA, subnetA := netAndSubnet(t, ts, "10.60.0.0/16", "10.60.1.0/24")
	netB, subnetB := netAndSubnet(t, ts, "10.61.0.0/16", "10.61.1.0/24")
	_, subnetC := netAndSubnet(t, ts, "10.62.0.0/16", "10.62.1.0/24")
	networkA := machine.NetworkName(machine.NetworkPrefix, subnetA)
	networkB := machine.NetworkName(machine.NetworkPrefix, subnetB)
	networkC := machine.NetworkName(machine.NetworkPrefix, subnetC)

	_, out := post(t, ts, "CreateNetPeering", `{"SourceNetId":"`+netA+`","AccepterNetId":"`+netB+`"}`)
	id, _ := peeringOf(t, out)["NetPeeringId"].(string)

	// A pass that runs while the peering is only pending must not grant it:
	// the predicate counts *active* peerings and nothing weaker. The probe
	// subnet is what forces a pass to run at exactly that moment.
	post(t, ts, "CreateSubnet", `{"NetId":"`+netA+`","IpRange":"10.60.3.0/24"}`)
	if got := rt.peersOf(networkA); has(got, networkB) {
		t.Fatalf("a subnet create while the peering is pending-acceptance granted it: %s peers with %v", networkA, got)
	}

	post(t, ts, "AcceptNetPeering", `{"NetPeeringId":"`+id+`"}`)
	if got := rt.peersOf(networkA); !has(got, networkB) {
		t.Fatalf("after accept, %s peers with %v, want %s; nothing after this measures the subnet create", networkA, got, networkB)
	}

	// The ordinary create the issue names, in the Net the peering joins.
	_, out = post(t, ts, "CreateSubnet", `{"NetId":"`+netA+`","IpRange":"10.60.2.0/24"}`)
	s, _ := out["Subnet"].(map[string]any)
	newborn, _ := s["SubnetId"].(string)
	if newborn == "" {
		t.Fatalf("CreateSubnet answered no SubnetId: %v", out)
	}
	networkNew := machine.NetworkName(machine.NetworkPrefix, newborn)

	if got := rt.peersOf(networkA); !has(got, networkB) {
		t.Fatalf("an ordinary CreateSubnet in %s severed the active peering: %s peers with %v and no longer with %s", netA, networkA, got, networkB)
	}
	if got := rt.peersOf(networkNew); !has(got, networkB) {
		t.Fatalf("the newborn subnet never joined the active peering: %s peers with %v, want %s", networkNew, got, networkB)
	}
	if got := rt.peersOf(networkB); !has(got, networkA) || !has(got, networkNew) {
		t.Fatalf("the far side does not name both subnets of the peered Net: %s peers with %v", networkB, got)
	}

	// The widening must not leak: the unpeered Net reaches nobody and nobody
	// reaches it, through the exact same sequence.
	if got := rt.peersOf(networkC); len(got) != 0 {
		t.Fatalf("the unpeered Net's subnet gained peers: %s peers with %v", networkC, got)
	}
	for _, network := range []string{networkA, networkNew, networkB} {
		if has(rt.peersOf(network), networkC) {
			t.Fatalf("%s peers with the unpeered Net's %s", network, networkC)
		}
	}
}
