package outscale

import (
	"errors"
	"net/http"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// errPeeringNotPending is every state refusal of this file, answered from
// inside the store lock where the state cannot move underneath the check
// (#295). The message carries the state that was actually found.
var errPeeringNotPending = errors.New("the Net peering is not in a state this operation accepts")

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
	ResultsPerPage *int      `json:"ResultsPerPage"`
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
	res := resource.New(newID("pcx", p.env.NewID()), kindNetPeering, resource.Tenant{Provider: Name}, state, now)
	res.Attrs = map[string]any{
		"SourceNetId":     req.SourceNetID,
		"SourceIpRange":   stringOf(source.Attrs["IpRange"]),
		"AccepterNetId":   req.AccepterNetID,
		"AccepterIpRange": stringOf(accepter.Attrs["IpRange"]),
		// Stored once at create, so every read answers the same date:
		// a timestamp recomputed per read is a permanent Terraform diff.
		"ExpirationDate": now.UTC().Add(peeringExpiry).Format(expirationFormat),
		"Tags":           []any{},
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
	// "The Net peering must be in the `pending-acceptance` state" — accepting
	// a rejected, failed or deleted one is the state conflict the real cloud
	// answers, in this pack's dialect (409 ResourceConflict, errors.go).
	//
	// The state check and the transition are one critical section held by the
	// store: checked on a clone, two concurrent transitions both passed and
	// the wholesale write-back erased a concurrent write to another field —
	// the peering's tags — after its 200 (#295).
	var updated *resource.Resource
	var wrongState string
	err := p.env.Store.Update(Name, kindNetPeering, req.NetPeeringID, func(stored *resource.Resource) error {
		if stored.State != statePeeringPending {
			wrongState = stored.State
			return errPeeringNotPending
		}
		stored.State = statePeeringActive
		stored.Updated = p.env.Now()
		updated = stored
		return nil
	})
	switch {
	case errors.Is(err, errPeeringNotPending):
		p.conflict(w, "the Net peering "+req.NetPeeringID+" is "+wrongState+
			", only a pending-acceptance one can be accepted")
		return
	case err != nil:
		p.notFound(w, "Net peering", req.NetPeeringID)
		return
	}
	res := updated

	// "When an A-to-B peering connection is accepted, any pending B-to-A
	// peering connection is automatically rejected as redundant."
	// (AcceptNetPeering doc, osc-sdk-go.)
	sourceID := stringOf(res.Attrs["SourceNetId"])
	accepterID := stringOf(res.Attrs["AccepterNetId"])
	for _, other := range p.env.Store.List(kindNetPeering, resource.Tenant{Provider: Name}) {
		if other.State != statePeeringPending ||
			stringOf(other.Attrs["SourceNetId"]) != accepterID ||
			stringOf(other.Attrs["AccepterNetId"]) != sourceID {
			continue
		}
		// Re-checked under the lock: the counterpart may have transitioned
		// between the List and this write, and rejecting over the stale clone
		// erased whatever landed meanwhile (#295).
		_ = p.env.Store.Update(Name, kindNetPeering, other.ID, func(stored *resource.Resource) error {
			if stored.State != statePeeringPending {
				return errPeeringNotPending
			}
			stored.State = statePeeringRejected
			stored.Updated = p.env.Now()
			return nil
		})
	}

	// The reachability the acceptance granted, on the runtime, outside any
	// lock: the store already says active, and a failing runtime degrades the
	// data plane without taking the control plane down (same policy as the
	// sibling packs' reconcilers, vpc.go and privatenetworks.go). The same
	// reconciliation as a subnet create, because reachability has one writer:
	// a second one dedicated to peerings erased the subnet writer's truth and
	// vice versa, whichever ran last (#508).
	p.isolateNetworks(r.Context())

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
	// "The Net peering must be in the `pending-acceptance` state to be
	// rejected." An active one is not un-accepted by rejecting it — the real
	// API refuses, and so does this one. Check and transition in one critical
	// section, same reason as acceptNetPeering (#295).
	var wrongState string
	err := p.env.Store.Update(Name, kindNetPeering, req.NetPeeringID, func(stored *resource.Resource) error {
		if stored.State != statePeeringPending {
			wrongState = stored.State
			return errPeeringNotPending
		}
		stored.State = statePeeringRejected
		stored.Updated = p.env.Now()
		return nil
	})
	switch {
	case errors.Is(err, errPeeringNotPending):
		p.conflict(w, "the Net peering "+req.NetPeeringID+" is "+wrongState+
			", only a pending-acceptance one can be rejected")
		return
	case err != nil:
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
	// "If it is in the `rejected`, `failed`, or `expired` states, it cannot be
	// deleted." A deleted one is refused for the same reason: there is nothing
	// left to delete, and answering success would say the call did something.
	//
	// The record stays, in the deleted state, which is a state the SDK's own
	// StateNames filter enumerates: the real API keeps answering a deleted
	// peering for a while rather than forgetting it, and a client reading it
	// back must find the state, not a hole. Check and transition in one
	// critical section, same reason as acceptNetPeering (#295).
	var wrongState string
	err := p.env.Store.Update(Name, kindNetPeering, req.NetPeeringID, func(stored *resource.Resource) error {
		switch stored.State {
		case statePeeringRejected, statePeeringFailed, statePeeringExpired, statePeeringDeleted:
			wrongState = stored.State
			return errPeeringNotPending
		}
		stored.State = statePeeringDeleted
		stored.Updated = p.env.Now()
		return nil
	})
	switch {
	case errors.Is(err, errPeeringNotPending):
		p.conflict(w, "the Net peering "+req.NetPeeringID+" is "+wrongState+" and cannot be deleted")
		return
	case err != nil:
		p.notFound(w, "Net peering", req.NetPeeringID)
		return
	}

	// Whatever reachability the peering granted goes with it — through the one
	// writer, so nothing else's truth is erased on the way (#508).
	p.isolateNetworks(r.Context())

	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

func (p *Pack) readNetPeerings(w http.ResponseWriter, r *http.Request) {
	var req readNetPeeringsRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refusePageSize(w, req.ResultsPerPage) {
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
		"NetPeerings":     page(out, pageSize(req.ResultsPerPage)),
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

// The runtime half of a peering transition is isolateNetworks, the same
// reconciliation a subnet create or delete runs. There used to be a dedicated
// reconciler here, computing peers from the active peerings alone, while
// isolateNetworks computed them from Net membership alone — two writers of one
// runtime state, each stating a partial truth, and machine.PeerNetworks
// reconciles rather than appends, so whichever ran last erased the other's
// half: an ordinary CreateSubnet in a peered Net severed the active peering,
// and the newborn subnet never joined it (#508). The active-peering half now
// lives in the shared predicate (reachableFrom, isolate.go), which is the
// widening CLAUDE.md's "Factoriser" section prescribes — the abstraction
// grows, the pack does not grow a second writer beside it.
