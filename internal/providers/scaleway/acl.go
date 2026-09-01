package scaleway

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/network"
	"github.com/stephrobert/feint/internal/core/resource"
)

// The Network ACL of a VPC: GetACL and SetACL, the two operations the official
// clients reach and this pack used to refuse.
//
// # Why these two and not "the rest of vpc"
//
// They were declined with the ingress rules, under one reason — *"no runtime
// mode enforces a rule at the VPC edge yet, and a filter recorded but never
// applied is indistinguishable from protection"*. The refusal was written from
// the SDK's shape rather than from a measurement, and the measurement, made on
// 2026-08-21 and recorded, says three things (#343):
//
//   - **`scw` calls GetACL.** `scw vpc rule get vpc-id=… is-ipv6=false` issues
//     `GET /vpc/v2/regions/fr-par/vpcs/{id}/acl-rules` and took a 501 from this
//     emulator. Recorded through `feint proxy --record` and ranked by
//     `feint coverage --observed`, which is what that flag exists for.
//   - **The official Terraform provider 2.81.0 ships `scaleway_vpc_acl`**, as a
//     resource and as a data source, so a stack reaches these two through
//     Terraform as well as through the CLI.
//   - **A real third-party module declares it.**
//     tf-scaleway-modules/terraform-scaleway-network @ 99f390bb writes
//     `resource "scaleway_vpc_acl" "this"` into its own `complete` example.
//
// The five ingress-rule operations stay declined and keep the reason, because
// the measurement found **no** client calling them: `scw` has no ingress-rule
// subcommand, and no surveyed stack names `scaleway_vpc_ingress_rule`. That is
// the split #343 asks for — take what a client calls, leave the rest declined,
// and say which is which.
//
// # What is served here, stated rather than implied
//
// A record. No runtime mode programs a filter at the VPC edge, exactly as no
// runtime mode programs a custom route's nexthop, and `docs/limits.md` says so
// in the same words for both. A rule set here round-trips for its client and is
// not a packet filter. The honest form of the original refusal is this sentence
// in the place a reader meets it, not a 501 that stops the stack that was only
// ever going to read the rules back.
//
// # Where the shapes come from
//
// The SDK (`api/vpc/v2/vpc_sdk.go`): `ACLRule`, `GetACLResponse`, `SetACLRequest`,
// `SetACLResponse`, and the two enums `Action` and `ACLRuleProtocol`. The wire
// answer carries `rules` and `default_policy` and no identifier — an ACL is not
// an object with a name upstream, it is the pair (VPC, address family) — which
// is why the stored resource's ID is derived rather than minted and is never
// published.
//
// # The empty answer is measured, not invented
//
// `scw vpc rule get` is a read a client makes before it has ever set anything,
// so the emulator has to answer for a VPC with no ACL. Read from the real cloud
// on 2026-08-21, against the account's own default VPC and creating nothing:
//
//	$ scw vpc rule get vpc-id=<default> region=fr-par is-ipv6=false -o json
//	{"rules":[],"default_policy":"accept"}
//	$ scw vpc rule get vpc-id=<default> region=fr-par is-ipv6=true -o json
//	{"rules":[],"default_policy":"accept"}
//
// Both families answer the same. The SDK's own default for `Action` is
// `unknown_action`, which is the protobuf zero and not what the wire carries —
// taking it would have been the invented value CLAUDE.md's rule 4 is about.
// TestAnUnsetACLAnswersWhatTheCloudAnswers pins the measured pair.

const kindVPCACL = "vpc/acl"

// aclDefaultPolicy is what a VPC nobody has set an ACL on answers. Measured,
// see the package comment above.
const aclDefaultPolicy = "accept"

// aclActions is the closed set SetACL accepts for a policy or an action.
// `unknown_action` is in the SDK's enum and is refused here: it is the zero a
// client sends by forgetting the field, and storing it would answer a policy
// that decides nothing.
var aclActions = map[string]bool{"accept": true, "drop": true}

// aclProtocols is ACLRuleProtocol's enum, spelled as the SDK spells it —
// upper case, and `ANY` rather than an empty string.
var aclProtocols = map[string]bool{"ANY": true, "TCP": true, "UDP": true, "ICMP": true}

// aclRule mirrors SDK ACLRule. Description is a pointer there and a pointer
// here: the field is nullable on the wire, and flattening it to "" would answer
// a description the client never sent.
type aclRule struct {
	Protocol    string  `json:"protocol"`
	Source      string  `json:"source"`
	SrcPortLow  uint32  `json:"src_port_low"`
	SrcPortHigh uint32  `json:"src_port_high"`
	Destination string  `json:"destination"`
	DstPortLow  uint32  `json:"dst_port_low"`
	DstPortHigh uint32  `json:"dst_port_high"`
	Action      string  `json:"action"`
	Description *string `json:"description"`
}

// setACLRequest mirrors SDK SetACLRequest, minus the two fields the SDK marks
// `json:"-"` because they travel in the path.
type setACLRequest struct {
	Rules         []aclRule `json:"rules"`
	IsIPv6        bool      `json:"is_ipv6"`
	DefaultPolicy string    `json:"default_policy"`
}

// aclID is the stored resource's key: derived from the VPC and the address
// family rather than minted.
//
// Derived on purpose. An ACL has no identifier upstream — SetACL is a PUT on
// the pair (VPC, family) and the answer carries none — so a minted id would
// need a find-or-create, and two concurrent PUTs on the same VPC would then
// race to create two ACLs for one pair, of which a later read would answer
// whichever the store listed first. A derived key makes the write an upsert on
// a stable slot and removes the race rather than locking around it.
//
// TestTwoConcurrentSetACLsLeaveOneACL fails without this.
func aclID(vpcID string, ipv6 bool) string {
	if ipv6 {
		return vpcID + "/acl/ipv6"
	}
	return vpcID + "/acl/ipv4"
}

// getACL answers the rule set of a VPC for one address family.
func (p *Pack) getACL(w http.ResponseWriter, r *http.Request) {
	vpc, ok := p.resourceOf(w, r, kindVPC, "vpc_id", "vpc")
	if !ok {
		return
	}
	// The SDK sends is_ipv6 as a query parameter on the GET (the contract
	// declares it there too). Absent means IPv4, which is the SDK's own zero
	// for a non-pointer bool and what `scw` sends when the flag is false.
	ipv6, _, err := queryBool(r.URL.Query(), "is_ipv6")
	if err != nil {
		writeParseFailure(w, "is_ipv6", err)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.aclView(vpc.ID, ipv6))
}

// setACL replaces the rule set of a VPC for one address family.
func (p *Pack) setACL(w http.ResponseWriter, r *http.Request) {
	vpc, ok := p.resourceOf(w, r, kindVPC, "vpc_id", "vpc")
	if !ok {
		return
	}
	var req setACLRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if !aclActions[req.DefaultPolicy] {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "default_policy",
			Reason:       "constraint",
			HelpMessage:  "default_policy must be accept or drop",
		})
		return
	}
	stored := make([]any, 0, len(req.Rules))
	for i, rule := range req.Rules {
		normalised, bad := validateACLRule(rule, i)
		if bad != nil {
			writeInvalidArguments(w, *bad)
			return
		}
		stored = append(stored, normalised)
	}

	now := p.env.Now()
	id := aclID(vpc.ID, req.IsIPv6)
	// The key is derived, so this asks "has this pair been set before" rather
	// than "which object is it": there is exactly one slot, and both branches
	// below write it whole.
	if _, found := p.env.Store.Get(Name, kindVPCACL, id); !found {
		res := resource.New(id, kindVPCACL, vpc.Tenant, "available", now)
		res.Attrs = map[string]any{
			"vpc_id":         vpc.ID,
			"is_ipv6":        req.IsIPv6,
			"default_policy": req.DefaultPolicy,
			"rules":          stored,
		}
		p.env.Store.Put(res)
		emulator.WriteJSON(w, http.StatusOK, p.aclView(vpc.ID, req.IsIPv6))
		return
	}
	// Update rather than mutate-then-Put, the rule every write to an existing
	// resource here follows: a Put from a stale clone resurrects an ACL whose
	// VPC was deleted while this PUT was decoding (#289).
	if err := p.env.Store.Update(Name, kindVPCACL, id, func(s *resource.Resource) error {
		s.Attrs["default_policy"] = req.DefaultPolicy
		s.Attrs["rules"] = stored
		s.Updated = now
		return nil
	}); err != nil {
		writeNotFound(w, "vpc", vpc.ID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.aclView(vpc.ID, req.IsIPv6))
}

// validateACLRule refuses what the API's own enums and types refuse, and
// returns the rule as it will be stored.
//
// Refused at the door rather than echoed: a rule whose source is not a CIDR is
// a rule no real API accepts, and answering 200 to it is the "a 200 that lies"
// family this repository measures. The index is carried into the argument name
// so a client with twenty rules is told which one.
func validateACLRule(rule aclRule, index int) (map[string]any, *ArgumentError) {
	at := func(field string) string {
		return "rules." + strconv.Itoa(index) + "." + field
	}
	protocol := strings.ToUpper(strings.TrimSpace(rule.Protocol))
	if protocol == "" {
		// ACLRuleProtocol.String() answers ANY for the empty value, so an
		// omitted protocol is ANY rather than an error. Read from the SDK
		// (vpc_sdk.go, ACLRuleProtocol.String), not assumed.
		protocol = "ANY"
	}
	if !aclProtocols[protocol] {
		return nil, &ArgumentError{ArgumentName: at("protocol"), Reason: "constraint",
			HelpMessage: "protocol must be one of ANY, TCP, UDP, ICMP"}
	}
	if !aclActions[rule.Action] {
		return nil, &ArgumentError{ArgumentName: at("action"), Reason: "constraint",
			HelpMessage: "action must be accept or drop"}
	}
	if _, err := network.ParseCIDR(rule.Source); err != nil {
		return nil, &ArgumentError{ArgumentName: at("source"), Reason: "format", HelpMessage: err.Error()}
	}
	if _, err := network.ParseCIDR(rule.Destination); err != nil {
		return nil, &ArgumentError{ArgumentName: at("destination"), Reason: "format", HelpMessage: err.Error()}
	}
	for _, port := range []struct {
		lowName, highName string
		low, high         uint32
	}{
		{lowName: "src_port_low", highName: "src_port_high", low: rule.SrcPortLow, high: rule.SrcPortHigh},
		{lowName: "dst_port_low", highName: "dst_port_high", low: rule.DstPortLow, high: rule.DstPortHigh},
	} {
		if port.low > 65535 || port.high > 65535 {
			return nil, &ArgumentError{ArgumentName: at(port.lowName), Reason: "constraint",
				HelpMessage: "a port is between 0 and 65535"}
		}
		// A high of zero is "unset", not "port 0": both ports are non-pointer
		// uint32 in the SDK, so a client that names only the low port sends a
		// zero high, and refusing that pair would refuse the commonest rule
		// there is. What this catches is the inverted range a client wrote by
		// hand. Whether the real API narrows this further is unmeasured, and
		// being permissive where the measurement is missing is the safe side:
		// a refusal invented here is a defect a client meets, an acceptance is
		// a check this emulator does not make.
		if port.high != 0 && port.low > port.high {
			return nil, &ArgumentError{ArgumentName: at(port.highName), Reason: "constraint",
				HelpMessage: "the high port of a range is not below its low port"}
		}
	}
	out := map[string]any{
		"protocol":      protocol,
		"source":        rule.Source,
		"src_port_low":  float64(rule.SrcPortLow),
		"src_port_high": float64(rule.SrcPortHigh),
		"destination":   rule.Destination,
		"dst_port_low":  float64(rule.DstPortLow),
		"dst_port_high": float64(rule.DstPortHigh),
		"action":        rule.Action,
		// Nullable on the wire and nullable here: the SDK types Description as
		// *string, and a client that sends null reads null back.
		"description": nil,
	}
	if rule.Description != nil {
		out["description"] = *rule.Description
	}
	return out, nil
}

// aclView renders GetACLResponse and SetACLResponse, which are the same shape.
//
// The two fields the SDK declares and nothing else: no identifier, no
// timestamps, no vpc_id. A view that published the derived key would publish an
// identifier the real API does not have, and a client storing it would carry a
// value no real cloud ever answers.
func (p *Pack) aclView(vpcID string, ipv6 bool) map[string]any {
	res, found := p.env.Store.Get(Name, kindVPCACL, aclID(vpcID, ipv6))
	if !found {
		return map[string]any{
			"rules":          []any{},
			"default_policy": aclDefaultPolicy,
		}
	}
	rules, _ := res.Attrs["rules"].([]any)
	if rules == nil {
		rules = []any{}
	}
	policy, _ := res.Attrs["default_policy"].(string)
	if policy == "" {
		policy = aclDefaultPolicy
	}
	return map[string]any{
		"rules":          rules,
		"default_policy": policy,
	}
}

// aclsOfVPC is the pair of rule sets a VPC may carry, for the delete to sweep.
//
// A VPC delete that left its ACLs behind would leave two rows in the store
// addressed by an identifier nothing can reach any more, and the next VPC
// minted with the same identifier — a restored snapshot, a seeded run — would
// inherit somebody else's filter. The store is the only thing that remembers,
// so the delete is where the sweep belongs.
//
// TestDeletingAVPCTakesItsACLsWithIt fails without this.
func (p *Pack) aclsOfVPC(vpcID string) []string {
	var out []string
	for _, ipv6 := range []bool{false, true} {
		id := aclID(vpcID, ipv6)
		if _, found := p.env.Store.Get(Name, kindVPCACL, id); found {
			out = append(out, id)
		}
	}
	return out
}
