package outscale

import (
	"context"
	"net/http"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Net peerings: the link between two Nets this emulator itself created — which
// is what pulled the family off the declined list, where an audit had found it
// swept in with the carrier-connectivity block. Both ends are local, and
// machine.PeerNetworks exists to back the reachability an accepted peering
// grants.
//
// The lifecycle is a state machine, and every name below is read from the SDK
// (NetPeeringStateName in osc-sdk-go client.gen.go): pending-acceptance,
// active, rejected, failed, expired, deleted. What each operation accepts as a
// starting state is read from the operation docs in the same file:
//
//   - CreateNetPeering leaves the peering in pending-acceptance; two Nets with
//     overlapping IP ranges make it failed instead.
//   - AcceptNetPeering and RejectNetPeering demand pending-acceptance.
//   - DeleteNetPeering takes active or pending-acceptance, and refuses
//     rejected, failed and expired.
//
// `expired` is unreachable here and saying so matters: upstream it is what
// seven days of not answering produce, and this emulator's clock advances no
// state on its own. A test that wants an expired peering cannot make one.
//
// Mono-tenancy makes the actors indistinguishable, and this is stated rather
// than faked: upstream, AcceptNetPeering is for the owner of the accepter Net,
// RejectNetPeering likewise, and a pending-acceptance peering is deletable only
// by the owner of the source Net. The emulator has one account (accountID),
// which owns both ends of every peering it can create, so every one of those
// identity conditions is satisfied by construction and none of them can be
// exercised. What remains testable — and refused — is the state machine.

const kindNetPeering = "netpeering"

// The SDK's NetPeeringStateName values. Never invented: the enum is in
// client.gen.go and the Terraform provider branches on the exact strings.
const (
	statePeeringPending  = "pending-acceptance"
	statePeeringActive   = "active"
	statePeeringRejected = "rejected"
	statePeeringFailed   = "failed"
	statePeeringExpired  = "expired"
	statePeeringDeleted  = "deleted"
)

// peeringMessage is the State.Message a state carries. Two spellings are
// measured, from the response examples in Outscale's own API document:
// "Pending acceptance by <account>" and "Active". The other three follow the
// same capitalisation; no recorded answer carries them, and the api.yaml
// examples stop at the two above.
func peeringMessage(state string) string {
	switch state {
	case statePeeringPending:
		return "Pending acceptance by " + accountID
	case statePeeringActive:
		return "Active"
	case statePeeringRejected:
		return "Rejected"
	case statePeeringFailed:
		return "Failed: the IP ranges of the two Nets overlap"
	case statePeeringDeleted:
		return "Deleted"
	}
	return ""
}

// peeringExpiry is the window the SDK documents on CreateNetPeering: "If the
// owner of the peer Net does not accept the request within 7 days, the state
// of the Net peering becomes `expired`." The date is published; the clock that
// would enforce it does not exist here (see the file comment on `expired`).
const peeringExpiry = 7 * 24 * time.Hour

// expirationFormat matches the ISO 8601 date-time the API documents,
// `2020-06-14T00:00:00.000Z`, which is also what the Terraform provider's
// iso8601 decoder parses.
const expirationFormat = "2006-01-02T15:04:05.000Z"

type createNetPeeringRequest struct {
	AccepterNetID   string `json:"AccepterNetId"`
	AccepterOwnerID string `json:"AccepterOwnerId"`
	SourceNetID     string `json:"SourceNetId"`
	DryRun          *bool  `json:"DryRun"`
}

type netPeeringIDRequest struct {
	NetPeeringID string `json:"NetPeeringId"`
	DryRun       *bool  `json:"DryRun"`
}

type readNetPeeringsRequest struct {
	Filters        filterSet `json:"Filters"`
	ResultsPerPage int       `json:"ResultsPerPage"`
	DryRun         *bool     `json:"DryRun"`
}

// netPeeringFilters is what a stored peering can answer. ExpirationDates and
// the tag filters are refused rather than silently matched, the same triage
// as every other Read* of this pack; the Terraform provider's own read sends
// NetPeeringIds and nothing else (resource_net_peering.go, v1.8.0).
var netPeeringFilters = []string{
	"NetPeeringIds",
	"AccepterNetAccountIds", "AccepterNetIpRanges", "AccepterNetNetIds",
	"SourceNetAccountIds", "SourceNetIpRanges", "SourceNetNetIds",
	"StateMessages", "StateNames",
}

func (p *Pack) createNetPeering(w http.ResponseWriter, r *http.Request) {
	var req createNetPeeringRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.SourceNetID == "" || req.AccepterNetID == "" {
		p.badRequest(w, "SourceNetId and AccepterNetId are both required")
		return
	}
	// AccepterOwnerId names the account owning the peer Net. This emulator has
	// exactly one account, so a foreign owner names a Net that cannot exist
	// here, and the honest answer is the one an unknown Net gets.
	if req.AccepterOwnerID != "" && req.AccepterOwnerID != accountID {
		p.notFound(w, "Net", req.AccepterNetID+" (owned by "+req.AccepterOwnerID+")")
		return
	}
	source, found := p.env.Store.Get(Name, kindNet, req.SourceNetID)
	if !found {
		p.notFound(w, "Net", req.SourceNetID)
		return
	}
	accepter, found := p.env.Store.Get(Name, kindNet, req.AccepterNetID)
	if !found {
		p.notFound(w, "Net", req.AccepterNetID)
		return
	}

	state := statePeeringPending
	// "The two Nets must not have overlapping IP ranges. Otherwise, the Net
	// peering is in the `failed` state." (CreateNetPeering doc, osc-sdk-go.)
	// The create still answers 200: the refusal is a state, not an error.
	// A peering of a Net with itself lands here too, its range overlapping
	// its own.
	sourceRange, sourceErr := prefixOf(source, "IpRange")
	accepterRange, accepterErr := prefixOf(accepter, "IpRange")
	if sourceErr == nil && accepterErr == nil && sourceRange.Overlaps(accepterRange) {
		state = statePeeringFailed
	}
	// "If an A-to-B connection is already created and accepted, creating a
	// B-to-A connection is not necessary and would be automatically rejected."
	// (Same doc.) Born rejected, not refused: the record exists and says why.
	if state == statePeeringPending && p.activePeeringBetween(req.AccepterNetID, req.SourceNetID) {
		state = statePeeringRejected
	}

	now := p.env.Now()
	res := &resource.Resource{
		ID:      newID("pcx", p.env.NewID()),
		Kind:    kindNetPeering,
		Tenant:  resource.Tenant{Provider: Name},
		State:   state,
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"SourceNetId":     req.SourceNetID,
			"SourceIpRange":   stringOf(source.Attrs["IpRange"]),
			"AccepterNetId":   req.AccepterNetID,
			"AccepterIpRange": stringOf(accepter.Attrs["IpRange"]),
			// Stored once at create, so every read answers the same date:
			// a timestamp recomputed per read is a permanent Terraform diff.
			"ExpirationDate": now.UTC().Add(peeringExpiry).Format(expirationFormat),
			"Tags":           []any{},
		},
	}
	p.env.Store.Put(res)

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"NetPeering":      netPeeringView(res),
		"ResponseContext": p.context(),
	})
}

func (p *Pack) acceptNetPeering(w http.ResponseWriter, r *http.Request) {
	var req netPeeringIDRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	res, found := p.env.Store.Get(Name, kindNetPeering, req.NetPeeringID)
	if !found {
		p.notFound(w, "Net peering", req.NetPeeringID)
		return
	}
	// "The Net peering must be in the `pending-acceptance` state" — accepting
	// a rejected, failed or deleted one is the state conflict the real cloud
	// answers, in this pack's dialect (409 ResourceConflict, errors.go).
	if res.State != statePeeringPending {
		p.conflict(w, "the Net peering "+res.ID+" is "+res.State+
			", only a pending-acceptance one can be accepted")
		return
	}
	res.State = statePeeringActive
	if !p.env.Store.Commit(res, p.env.Now()) {
		p.notFound(w, "Net peering", req.NetPeeringID)
		return
	}

	// "When an A-to-B peering connection is accepted, any pending B-to-A
	// peering connection is automatically rejected as redundant."
	// (AcceptNetPeering doc, osc-sdk-go.)
	sourceID := stringOf(res.Attrs["SourceNetId"])
	accepterID := stringOf(res.Attrs["AccepterNetId"])
	for _, other := range p.env.Store.List(kindNetPeering, resource.Tenant{Provider: Name}) {
		if other.State == statePeeringPending &&
			stringOf(other.Attrs["SourceNetId"]) == accepterID &&
			stringOf(other.Attrs["AccepterNetId"]) == sourceID {
			other.State = statePeeringRejected
			p.env.Store.Commit(other, p.env.Now())
		}
	}

	// The reachability the acceptance granted, on the runtime, outside any
	// lock: the store already says active, and a failing runtime degrades the
	// data plane without taking the control plane down (same policy as the
	// sibling packs' reconcilers, vpc.go and privatenetworks.go).
	p.reconcilePeerings(r.Context())

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"NetPeering":      netPeeringView(res),
		"ResponseContext": p.context(),
	})
}

func (p *Pack) rejectNetPeering(w http.ResponseWriter, r *http.Request) {
	var req netPeeringIDRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	res, found := p.env.Store.Get(Name, kindNetPeering, req.NetPeeringID)
	if !found {
		p.notFound(w, "Net peering", req.NetPeeringID)
		return
	}
	// "The Net peering must be in the `pending-acceptance` state to be
	// rejected." An active one is not un-accepted by rejecting it — the real
	// API refuses, and so does this one.
	if res.State != statePeeringPending {
		p.conflict(w, "the Net peering "+res.ID+" is "+res.State+
			", only a pending-acceptance one can be rejected")
		return
	}
	res.State = statePeeringRejected
	if !p.env.Store.Commit(res, p.env.Now()) {
		p.notFound(w, "Net peering", req.NetPeeringID)
		return
	}
	// The response carries no NetPeering: RejectNetPeeringResponse is
	// {ResponseContext} alone in the API document.
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

func (p *Pack) deleteNetPeering(w http.ResponseWriter, r *http.Request) {
	var req netPeeringIDRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	res, found := p.env.Store.Get(Name, kindNetPeering, req.NetPeeringID)
	if !found {
		p.notFound(w, "Net peering", req.NetPeeringID)
		return
	}
	// "If it is in the `rejected`, `failed`, or `expired` states, it cannot be
	// deleted." A deleted one is refused for the same reason: there is nothing
	// left to delete, and answering success would say the call did something.
	switch res.State {
	case statePeeringRejected, statePeeringFailed, statePeeringExpired, statePeeringDeleted:
		p.conflict(w, "the Net peering "+res.ID+" is "+res.State+" and cannot be deleted")
		return
	}
	// The record stays, in the deleted state, which is a state the SDK's own
	// StateNames filter enumerates: the real API keeps answering a deleted
	// peering for a while rather than forgetting it, and a client reading it
	// back must find the state, not a hole.
	res.State = statePeeringDeleted
	if !p.env.Store.Commit(res, p.env.Now()) {
		p.notFound(w, "Net peering", req.NetPeeringID)
		return
	}

	// Whatever reachability the peering granted goes with it.
	p.reconcilePeerings(r.Context())

	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

func (p *Pack) readNetPeerings(w http.ResponseWriter, r *http.Request) {
	var req readNetPeeringsRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refuseUnsupported(w, req.Filters, netPeeringFilters...) {
		return
	}

	out := make([]map[string]any, 0)
	for _, res := range p.env.Store.List(kindNetPeering, resource.Tenant{Provider: Name}) {
		if !matchesStrings(req.Filters, "NetPeeringIds", res.ID) ||
			!matchesStrings(req.Filters, "AccepterNetAccountIds", accountID) ||
			!matchesStrings(req.Filters, "AccepterNetIpRanges", stringOf(res.Attrs["AccepterIpRange"])) ||
			!matchesStrings(req.Filters, "AccepterNetNetIds", stringOf(res.Attrs["AccepterNetId"])) ||
			!matchesStrings(req.Filters, "SourceNetAccountIds", accountID) ||
			!matchesStrings(req.Filters, "SourceNetIpRanges", stringOf(res.Attrs["SourceIpRange"])) ||
			!matchesStrings(req.Filters, "SourceNetNetIds", stringOf(res.Attrs["SourceNetId"])) ||
			!matchesStrings(req.Filters, "StateMessages", peeringMessage(res.State)) ||
			!matchesStrings(req.Filters, "StateNames", res.State) {
			continue
		}
		out = append(out, netPeeringView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"NetPeerings":     page(out, req.ResultsPerPage),
		"ResponseContext": p.context(),
	})
}

// ---- Views and plumbing ------------------------------------------------------

// netPeeringView is the NetPeering shape of the API document. The nested maps
// are built fresh on every call rather than stored, so no answer aliases the
// store's own values.
//
// The account on both ends is the emulator's single account: mono-tenancy
// again, and the honest value — inventing a second account id would claim a
// tenant this process cannot hold.
func netPeeringView(res *resource.Resource) map[string]any {
	out := map[string]any{
		"NetPeeringId": res.ID,
		"SourceNet": map[string]any{
			"AccountId": accountID,
			"IpRange":   stringOf(res.Attrs["SourceIpRange"]),
			"NetId":     stringOf(res.Attrs["SourceNetId"]),
		},
		"AccepterNet": map[string]any{
			"AccountId": accountID,
			"IpRange":   stringOf(res.Attrs["AccepterIpRange"]),
			"NetId":     stringOf(res.Attrs["AccepterNetId"]),
		},
		"State": map[string]any{
			"Name":    res.State,
			"Message": peeringMessage(res.State),
		},
		"Tags": res.Attrs["Tags"],
	}
	if date := stringOf(res.Attrs["ExpirationDate"]); date != "" {
		out["ExpirationDate"] = date
	}
	return out
}

// activePeeringBetween reports whether an active peering already joins the two
// Nets in the given direction.
func (p *Pack) activePeeringBetween(sourceID, accepterID string) bool {
	for _, res := range p.env.Store.List(kindNetPeering, resource.Tenant{Provider: Name}) {
		if res.State == statePeeringActive &&
			stringOf(res.Attrs["SourceNetId"]) == sourceID &&
			stringOf(res.Attrs["AccepterNetId"]) == accepterID {
			return true
		}
	}
	return false
}

// reconcilePeerings makes the runtime reachability match the store: every
// subnet's backing network is peered with the backing networks of every subnet
// of every Net an *active* peering joins to its own — and with nothing else,
// because machine.PeerNetworks reconciles rather than appends, so a deleted
// peering separates the Nets again on the same call.
//
// Only a runtime whose networks are born separate has anything to do here. On
// a Peerer with native isolation (OVN) the peering is what joins two Nets; on
// bridges nothing was ever separated, which is exactly the bridge-mode limit
// docs/limits.md records — a suite must gate on `capabilities.isolation`, and
// no document says "peered" without naming the mode.
//
// Errors are logged, not answered: the store is committed by the time this
// runs, and a runtime failure degrades the data plane without taking the
// control plane down (the same policy as vpc.go's isolateNetworks).
func (p *Pack) reconcilePeerings(ctx context.Context) {
	peerer, ok := p.env.Machines.(machine.Peerer)
	if !ok || !peerer.NativeIsolation() {
		return
	}

	// Which Nets each Net reaches, through active peerings, in both
	// directions: a peering "works both ways" (SDK doc), whoever requested it.
	reaches := map[string]map[string]bool{}
	join := func(a, b string) {
		if reaches[a] == nil {
			reaches[a] = map[string]bool{}
		}
		reaches[a][b] = true
	}
	for _, pcx := range p.env.Store.List(kindNetPeering, resource.Tenant{Provider: Name}) {
		if pcx.State != statePeeringActive {
			continue
		}
		source := stringOf(pcx.Attrs["SourceNetId"])
		accepter := stringOf(pcx.Attrs["AccepterNetId"])
		join(source, accepter)
		join(accepter, source)
	}

	// The backing networks per Net, from the runtime name each subnet carries.
	backing := map[string][]string{}
	subnets := p.env.Store.List(kindSubnet, resource.Tenant{Provider: Name})
	for _, subnet := range subnets {
		if name := subnet.Runtime[runtimeNetworkKey]; name != "" {
			netID := stringOf(subnet.Attrs["NetId"])
			backing[netID] = append(backing[netID], name)
		}
	}

	for _, subnet := range subnets {
		name := subnet.Runtime[runtimeNetworkKey]
		if name == "" {
			continue
		}
		peers := make([]string, 0, 4)
		for peerNet := range reaches[stringOf(subnet.Attrs["NetId"])] {
			peers = append(peers, backing[peerNet]...)
		}
		if err := peerer.PeerNetworks(ctx, name, peers); err != nil {
			p.logger().Error("could not reconcile the peering of a subnet's network",
				"subnet", subnet.ID, "network", name, "error", err)
		}
	}
}
