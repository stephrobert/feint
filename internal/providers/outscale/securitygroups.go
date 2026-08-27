package outscale

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Security groups, read-only for now: the one every Net is born with.
//
// The shape is measured, not read off the SDK. A read-only sweep of a real
// account through `feint proxy` (X-2, 2026-08-08, cloudgouv-eu-west-1) recorded
// what a pristine default group actually looks like, and two details of it are
// exactly the kind nothing else would have caught:
//
//   - a rule's IpRanges and SecurityGroupsMembers keys are OMITTED when empty,
//     not sent as [] — the union of both only ever appears across different
//     rules;
//   - the self-referencing member of the default inbound rule carries AccountId
//     and SecurityGroupId only, never SecurityGroupName, even though the schema
//     declares all three.
//
// Values here are invented (the account id is the emulator's own fictional one);
// the field set and its conditionality are the measurement.
//
// CreateSecurityGroup and the rule calls are backlog, not declined: they are
// implementable on this store, and the Terraform provider will want them. What
// is served now is what every Net-holding account has whether it asked or not.

const kindSecurityGroup = "securitygroup"

// defaultSecurityGroup is the group Outscale creates with every Net. Stored as
// its own resource rather than derived on read, because its identifiers must
// survive restarts of nothing but the store: a rule id that changed between two
// reads would show up as a permanent Terraform diff.
func (p *Pack) defaultSecurityGroup(netID string) *resource.Resource {
	now := p.env.Now()
	sgID := newID("sg", p.env.NewID())
	// Measured on a pristine default group: one inbound rule accepting
	// everything from the group itself, one outbound rule to everywhere.
	inbound := []any{map[string]any{
		"FromPortRange":       -1,
		"ToPortRange":         -1,
		"IpProtocol":          "-1",
		"SecurityGroupRuleId": newID("sgr", p.env.NewID()),
		"SecurityGroupsMembers": []any{map[string]any{
			"AccountId":       accountID,
			"SecurityGroupId": sgID,
		}},
	}}
	outbound := []any{map[string]any{
		"FromPortRange":       -1,
		"ToPortRange":         -1,
		"IpProtocol":          "-1",
		"SecurityGroupRuleId": newID("sgr", p.env.NewID()),
		"IpRanges":            []any{"0.0.0.0/0"},
	}}
	return &resource.Resource{
		ID:      sgID,
		Kind:    kindSecurityGroup,
		Tenant:  resource.Tenant{Provider: Name},
		State:   "available",
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"NetId":             netID,
			"SecurityGroupName": "default",
			"Description":       "default security group",
			"InboundRules":      inbound,
			"OutboundRules":     outbound,
			"Tags":              []any{},
		},
	}
}

type readSecurityGroupsRequest struct {
	Filters        filterSet `json:"Filters"`
	ResultsPerPage *int      `json:"ResultsPerPage"`
	DryRun         *bool     `json:"DryRun"`
}

// securityGroupFilters are what a stored group can answer. The API declares 21;
// the rest are refused rather than silently matched, per filters.go.
var securityGroupFilters = []string{"SecurityGroupIds", "SecurityGroupNames", "NetIds", "Descriptions"}

func (p *Pack) readSecurityGroups(w http.ResponseWriter, r *http.Request) {
	var req readSecurityGroupsRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refusePageSize(w, req.ResultsPerPage) {
		return
	}
	if p.refuseUnsupported(w, req.Filters, securityGroupFilters...) {
		return
	}

	out := make([]map[string]any, 0)
	for _, res := range p.env.Store.List(kindSecurityGroup, resource.Tenant{Provider: Name}) {
		name := stringOf(res.Attrs["SecurityGroupName"])
		netID := stringOf(res.Attrs["NetId"])
		description := stringOf(res.Attrs["Description"])
		if !matchesStrings(req.Filters, "SecurityGroupIds", res.ID) ||
			!matchesStrings(req.Filters, "SecurityGroupNames", name) ||
			!matchesStrings(req.Filters, "NetIds", netID) ||
			!matchesStrings(req.Filters, "Descriptions", description) {
			continue
		}
		out = append(out, securityGroupView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"SecurityGroups":  page(out, pageSize(req.ResultsPerPage)),
		"ResponseContext": p.context(),
	})
}

func securityGroupView(res *resource.Resource) map[string]any {
	out := make(map[string]any, len(res.Attrs)+2)
	for k, v := range res.Attrs {
		out[k] = v
	}
	out["SecurityGroupId"] = res.ID
	out["AccountId"] = accountID
	return out
}

// securityGroupsOf lists the groups belonging to one Net.
func (p *Pack) securityGroupsOf(netID string) []*resource.Resource {
	out := make([]*resource.Resource, 0, 1)
	for _, res := range p.env.Store.List(kindSecurityGroup, resource.Tenant{Provider: Name}) {
		if stringOf(res.Attrs["NetId"]) == netID {
			out = append(out, res)
		}
	}
	return out
}

// ---- Lifecycle --------------------------------------------------------------

type createSecurityGroupRequest struct {
	SecurityGroupName string `json:"SecurityGroupName"`
	Description       string `json:"Description"`
	NetID             string `json:"NetId"`
	DryRun            *bool  `json:"DryRun"`
}

// createSecurityGroup makes a group the way the API describes one being made:
// no inbound rule, one outbound rule to everywhere. The outbound default is the
// same rule the measured pristine default group carries, which is the closest
// thing to a measurement a non-default group can have — the sweep's account had
// no pristine non-default group to record.
func (p *Pack) createSecurityGroup(w http.ResponseWriter, r *http.Request) {
	var req createSecurityGroupRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	// Both required by their schema — one of the seven Outscale marks required
	// anywhere — and the real API refuses without them.
	if req.SecurityGroupName == "" || req.Description == "" {
		p.badRequest(w, "SecurityGroupName and Description are required")
		return
	}
	if req.NetID != "" {
		if _, found := p.env.Store.Get(Name, kindNet, req.NetID); !found {
			p.notFound(w, "Net", req.NetID)
			return
		}
	}
	// One name per scope, which Terraform's create-before-destroy depends on
	// being refused, not overwritten.
	for _, other := range p.env.Store.List(kindSecurityGroup, resource.Tenant{Provider: Name}) {
		if stringOf(other.Attrs["NetId"]) == req.NetID &&
			stringOf(other.Attrs["SecurityGroupName"]) == req.SecurityGroupName {
			p.conflict(w, "the name "+req.SecurityGroupName+" is already used by "+other.ID)
			return
		}
	}

	now := p.env.Now()
	res := resource.New(newID("sg", p.env.NewID()), kindSecurityGroup, resource.Tenant{Provider: Name}, "available", now)
	res.Attrs = map[string]any{
		"SecurityGroupName": req.SecurityGroupName,
		"Description":       req.Description,
		"InboundRules":      []any{},
		"OutboundRules": []any{map[string]any{
			"FromPortRange":       -1,
			"ToPortRange":         -1,
			"IpProtocol":          "-1",
			"SecurityGroupRuleId": newID("sgr", p.env.NewID()),
			"IpRanges":            []any{"0.0.0.0/0"},
		}},
		"Tags": []any{},
	}
	if req.NetID != "" {
		res.Attrs["NetId"] = req.NetID
	}
	p.env.Store.Put(res)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"SecurityGroup":   securityGroupView(res),
		"ResponseContext": p.context(),
	})
}

func (p *Pack) deleteSecurityGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SecurityGroupID   string `json:"SecurityGroupId"`
		SecurityGroupName string `json:"SecurityGroupName"`
		DryRun            *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	res := p.securityGroupByRef(req.SecurityGroupID, req.SecurityGroupName)
	if res == nil {
		p.notFound(w, "security group", orDefault(req.SecurityGroupID, req.SecurityGroupName))
		return
	}
	// The default group is the Net's, not the client's: it goes with the Net
	// and only with it, which is what the real API answers.
	if stringOf(res.Attrs["SecurityGroupName"]) == "default" && stringOf(res.Attrs["NetId"]) != "" {
		p.conflict(w, "the default security group of "+stringOf(res.Attrs["NetId"])+" cannot be deleted")
		return
	}
	// A group a machine wears does not go either; the machine releases it
	// first, and the refusal is what destroy ordering retries on.
	// A group a load balancer carries does not go either — the sweep the
	// provider runs before each group delete (ReadLoadBalancers, measured on
	// 1.8.0) exists precisely because the real API refuses this.
	// TestASecurityGroupDoesNotDeleteUnderALoadBalancer fails without it.
	if holders := p.loadBalancersOn("SecurityGroups", res.ID); len(holders) > 0 {
		p.conflict(w, "the security group "+res.ID+" is still carried by the load balancer "+holders[0])
		return
	}
	for _, vm := range p.env.Store.List(kindVM, resource.Tenant{Provider: Name}) {
		// A terminated machine releases its groups here at once, and the real
		// API does NOT (#380). Measured on 2026-08-21: after DeleteVms, the
		// cloud refused this call with 409 ResourceConflict thirty times over
		// about ninety seconds and accepted the thirty-first. So a destroy loop
		// that works against this emulator on the first try needs a retry
		// against the cloud, and the emulated run never exercises it.
		//
		// Not changed here, and the reason is a decision rather than an
		// oversight: reproducing the delay means holding the group for a
		// wall-clock window, which would make every conformance run and every
		// stack teardown wait it out — an emulator nobody wants to drive. And a
		// replay cannot grade the difference either way, because the recording
		// is thirty refusals then an acceptance and a corpus has no ninety
		// seconds in it: the sanitiser normalises every timestamp to one
		// second apart. #380 carries the choice.
		if vm.State == stateTerminated {
			continue
		}
		for _, id := range stringsOf(vm.Attrs["SecurityGroupIds"]) {
			if id == res.ID {
				p.conflict(w, "the security group "+res.ID+" is still worn by "+vm.ID)
				return
			}
		}
	}
	p.env.Store.Delete(Name, kindSecurityGroup, res.ID)
	// The runtime rule set goes with the group, once nothing wears it — the
	// refusals above guarantee that (#475).
	p.removeFirewall(r.Context(), res)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

// securityGroupRuleRequest is both CreateSecurityGroupRule and
// DeleteSecurityGroupRule: their schemas are field-for-field the same shape,
// only the link field's name differs, and the same three forms must be
// understood on both sides — flat, Rules[], and group-to-link.
type securityGroupRuleRequest struct {
	Flow            string           `json:"Flow"`
	SecurityGroupID string           `json:"SecurityGroupId"`
	IPProtocol      string           `json:"IpProtocol"`
	IPRange         string           `json:"IpRange"`
	FromPortRange   *int             `json:"FromPortRange"`
	ToPortRange     *int             `json:"ToPortRange"`
	Rules           []map[string]any `json:"Rules"`
	NameToLink      string           `json:"SecurityGroupNameToLink"`
	AccountToLink   string           `json:"SecurityGroupAccountIdToLink"`
	NameToUnlink    string           `json:"SecurityGroupNameToUnlink"`
	AccountToUnlink string           `json:"SecurityGroupAccountIdToUnlink"`
	DryRun          *bool            `json:"DryRun"`
}

// completedMembers fills in what the API adds to a member the client named by
// identifier alone (#382).
//
// A client sending `Rules[].SecurityGroupsMembers[].SecurityGroupId` — the
// shape examples/stacks/outscale/ writes as a tiering rule, the data tier
// accepting the web tier and nobody else — gets back a member carrying
// `AccountId` and `SecurityGroupName` as well. This pack copied the request's
// member through verbatim, so the answer said less than the cloud's about a
// group the client did not name in full.
//
// `AccountId` is the field with teeth: it is what distinguishes a member group
// of this account from one another account shared in, which is the whole reason
// `SecurityGroupAccountIdToLink` exists on the request. A client that reads the
// member back to decide whether a rule is cross-account read nothing here.
//
// Measured on a real account on 2026-08-21, and it is the family of #371 on the
// Exoscale side — a rule whose target is another group publishes less of that
// group here than upstream — one provider out.
//
// A member naming a group this emulator does not hold keeps what the client
// sent: inventing a name for it would be worse than the omission, and the
// create itself has already validated what it validates.
//
// TestARuleThatNamesAnotherGroupPublishesItsAccountAndName fails without this.
func (p *Pack) completedMembers(members []any) []any {
	out := make([]any, 0, len(members))
	for _, raw := range members {
		member, ok := raw.(map[string]any)
		if !ok {
			out = append(out, raw)
			continue
		}
		filled := make(map[string]any, len(member)+2)
		for k, v := range member {
			filled[k] = v
		}
		if _, present := filled["AccountId"]; !present {
			filled["AccountId"] = accountID
		}
		if _, present := filled["SecurityGroupName"]; !present {
			if sg, found := p.env.Store.Get(Name, kindSecurityGroup, stringOf(member["SecurityGroupId"])); found {
				filled["SecurityGroupName"] = stringOf(sg.Attrs["SecurityGroupName"])
			}
		}
		out = append(out, filled)
	}
	return out
}

// asRules normalises whichever of the three request forms was used into the
// list of rules it means, in the stored shape.
func (p *Pack) asRules(req *securityGroupRuleRequest, mint bool) ([]map[string]any, string) {
	link := orDefault(req.NameToLink, req.NameToUnlink)
	switch {
	case len(req.Rules) > 0:
		out := make([]map[string]any, 0, len(req.Rules))
		for _, raw := range req.Rules {
			rule := map[string]any{}
			for _, key := range []string{"IpProtocol", "FromPortRange", "ToPortRange", "IpRanges", "SecurityGroupsMembers", "ServiceIds"} {
				if v, ok := raw[key]; ok {
					rule[key] = v
				}
			}
			if members, ok := rule["SecurityGroupsMembers"].([]any); ok {
				rule["SecurityGroupsMembers"] = p.completedMembers(members)
			}
			if mint {
				rule["SecurityGroupRuleId"] = newID("sgr", p.env.NewID())
			}
			out = append(out, rule)
		}
		return out, ""
	case link != "":
		// A rule sourced by group: everything, from the members of the named
		// group. The member's shape is the measured one — AccountId and
		// SecurityGroupId, no name.
		member := p.securityGroupByRef("", link)
		if member == nil {
			return nil, link
		}
		rule := map[string]any{
			"FromPortRange": -1,
			"ToPortRange":   -1,
			"IpProtocol":    "-1",
			"SecurityGroupsMembers": []any{map[string]any{
				"AccountId":       accountID,
				"SecurityGroupId": member.ID,
			}},
		}
		if mint {
			rule["SecurityGroupRuleId"] = newID("sgr", p.env.NewID())
		}
		return []map[string]any{rule}, ""
	default:
		rule := map[string]any{
			"IpProtocol": req.IPProtocol,
		}
		if req.FromPortRange != nil {
			rule["FromPortRange"] = *req.FromPortRange
		}
		if req.ToPortRange != nil {
			rule["ToPortRange"] = *req.ToPortRange
		}
		if req.IPRange != "" {
			rule["IpRanges"] = []any{req.IPRange}
		}
		if mint {
			rule["SecurityGroupRuleId"] = newID("sgr", p.env.NewID())
		}
		return []map[string]any{rule}, ""
	}
}

// errRuleExists and errRuleMissing carry a rule check out of a Store.Update
// change function, so the handler can answer the right HTTP shape.
var (
	errRuleExists  = errors.New("the rule already exists")
	errRuleMissing = errors.New("the rule does not exist")
)

func (p *Pack) createSecurityGroupRule(w http.ResponseWriter, r *http.Request) {
	var req securityGroupRuleRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	res, side, ok := p.ruleTarget(w, &req)
	if !ok {
		return
	}
	rules, missing := p.asRules(&req, true)
	if missing != "" {
		p.notFound(w, "security group", missing)
		return
	}
	// The read, the duplicate check and the append happen inside Store.Update,
	// under the store lock. As a Get-clone-append-Commit sequence, two
	// concurrent creates on one group each appended to their own stale copy and
	// the second Commit erased the first: replaying terraform-outscale-k3s
	// measured 8 CreateSecurityGroupRule acknowledged with 200 and 5 rules
	// stored, then destroy died on the phantom (#289).
	// TestConcurrentRuleCreatesKeepEveryAcknowledgedRule fails without this.
	var final *resource.Resource
	err := p.env.Store.Update(Name, kindSecurityGroup, res.ID, func(stored *resource.Resource) error {
		existing, _ := stored.Attrs[side].([]any)
		// A fresh slice, never an append into the stored backing array: clones
		// share the nested values, so in-place growth is a write other readers
		// can see mid-request.
		merged := make([]any, len(existing), len(existing)+len(rules))
		copy(merged, existing)
		for _, rule := range rules {
			// A rule that already exists is a conflict, not a second copy: the
			// real API refuses the duplicate, and a client retrying a create
			// depends on the refusal to converge.
			for _, raw := range merged {
				if sameRule(raw, rule) {
					return errRuleExists
				}
			}
			merged = append(merged, rule)
		}
		stored.Attrs[side] = merged
		stored.Updated = p.env.Now()
		final = stored
		return nil
	})
	switch {
	case errors.Is(err, errRuleExists):
		p.conflict(w, "the rule already exists on "+res.ID)
		return
	case err != nil:
		p.notFound(w, "security group", res.ID)
		return
	}
	// The rule the client just authorised reaches the machines wearing the
	// group: a client authorises a rule after the Vm is running and expects it
	// to take effect (#475).
	p.syncSecurityGroup(r.Context(), final)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"SecurityGroup":   securityGroupView(final),
		"ResponseContext": p.context(),
	})
}

func (p *Pack) deleteSecurityGroupRule(w http.ResponseWriter, r *http.Request) {
	var req securityGroupRuleRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	res, side, ok := p.ruleTarget(w, &req)
	if !ok {
		return
	}
	wanted, missing := p.asRules(&req, false)
	if missing != "" {
		p.notFound(w, "security group", missing)
		return
	}
	// Same ordering as the create: the match and the removal run under the
	// store lock, or a delete racing a create on the other side of the group
	// commits a stale copy and one acknowledged change vanishes (#289).
	var final *resource.Resource
	err := p.env.Store.Update(Name, kindSecurityGroup, res.ID, func(stored *resource.Resource) error {
		existing, _ := stored.Attrs[side].([]any)
		kept := make([]any, 0, len(existing))
		removed := 0
		for _, raw := range existing {
			matched := false
			for _, candidate := range wanted {
				if sameRule(raw, candidate) {
					matched = true
					break
				}
			}
			if matched {
				removed++
				continue
			}
			kept = append(kept, raw)
		}
		if removed == 0 {
			return errRuleMissing
		}
		stored.Attrs[side] = kept
		stored.Updated = p.env.Now()
		final = stored
		return nil
	})
	switch {
	case errors.Is(err, errRuleMissing):
		p.notFound(w, "security group rule on", res.ID)
		return
	case err != nil:
		p.notFound(w, "security group", res.ID)
		return
	}
	// A revoked rule really disappears from the machines: replaying the set is
	// what closes the port the client just closed (#475).
	p.syncSecurityGroup(r.Context(), final)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"SecurityGroup":   securityGroupView(final),
		"ResponseContext": p.context(),
	})
}

// flowInbound and flowOutbound are the two directions a rule can take, and the
// only two values [ruleTarget] accepts. Named rather than written inline
// because PublicVocabulary has to vouch for exactly the values this refuses,
// and a second copy of a closed list is a copy that drifts silently: the
// symptom is a corpus that replays 400 on every rule and reads like an
// emulator defect.
const (
	flowInbound  = "Inbound"
	flowOutbound = "Outbound"
)

// ruleTarget resolves the group and the side of it a rule request addresses.
func (p *Pack) ruleTarget(w http.ResponseWriter, req *securityGroupRuleRequest) (*resource.Resource, string, bool) {
	if req.Flow != flowInbound && req.Flow != flowOutbound {
		p.badRequest(w, "Flow must be Inbound or Outbound")
		return nil, "", false
	}
	if req.SecurityGroupID == "" {
		p.badRequest(w, "SecurityGroupId is required")
		return nil, "", false
	}
	res, found := p.env.Store.Get(Name, kindSecurityGroup, req.SecurityGroupID)
	if !found {
		p.notFound(w, "security group", req.SecurityGroupID)
		return nil, "", false
	}
	side := "InboundRules"
	if req.Flow == flowOutbound {
		side = "OutboundRules"
	}
	return res, side, true
}

// sameRule compares two rules on what identifies one — protocol, ports, ranges
// and members — and never on SecurityGroupRuleId, which the delete request does
// not carry: a client deletes a rule by saying it again, not by naming it.
func sameRule(a, b any) bool {
	am, _ := a.(map[string]any)
	bm, _ := b.(map[string]any)
	if fmt.Sprint(am["IpProtocol"]) != fmt.Sprint(bm["IpProtocol"]) {
		return false
	}
	for _, key := range []string{"FromPortRange", "ToPortRange"} {
		if resource.Number(am[key]) != resource.Number(bm[key]) {
			return false
		}
	}
	if fmt.Sprint(am["IpRanges"]) != fmt.Sprint(bm["IpRanges"]) {
		return false
	}
	return membersOf(am) == membersOf(bm)
}

// membersOf flattens a rule's members to what identifies them: the group ids.
func membersOf(rule map[string]any) string {
	raw, _ := rule["SecurityGroupsMembers"].([]any)
	ids := make([]string, 0, len(raw))
	for _, m := range raw {
		mm, _ := m.(map[string]any)
		ids = append(ids, stringOf(mm["SecurityGroupId"]))
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// securityGroupByRef resolves a group by id first, then by name — the two ways
// every SG request may point at one.
func (p *Pack) securityGroupByRef(id, name string) *resource.Resource {
	if id != "" {
		res, _ := p.env.Store.Get(Name, kindSecurityGroup, id)
		return res
	}
	if name == "" {
		return nil
	}
	for _, res := range p.env.Store.List(kindSecurityGroup, resource.Tenant{Provider: Name}) {
		if stringOf(res.Attrs["SecurityGroupName"]) == name {
			return res
		}
	}
	return nil
}

// checkVMSecurityGroups validates the groups a Vm asks to wear, answering the
// client itself when one names nothing. Shared by CreateVms and UpdateVm so the
// two cannot drift the way keypair validation once did.
func (p *Pack) checkVMSecurityGroups(w http.ResponseWriter, ids []string) bool {
	for _, id := range ids {
		if _, found := p.env.Store.Get(Name, kindSecurityGroup, id); !found {
			p.notFound(w, "security group", id)
			return false
		}
	}
	return true
}

// effectiveSecurityGroups is what a Vm wears, resolved on read: the groups it
// asked for, or its Net's default group when it asked for none — a machine in a
// Net is never group-less, whether or not the client named one (measured: a
// CreateNic without groups came back wearing the default). A Vm outside any Net
// answers nil, and the view omits the key.
func (p *Pack) effectiveSecurityGroups(vm *resource.Resource) []any {
	if ids := stringsOf(vm.Attrs["SecurityGroupIds"]); len(ids) > 0 {
		// By identifier, ascending, and never in the order the request named
		// them (#379). Terraform's outscale_vm stores security_group_ids as a
		// **list**, so an order of this emulator's own is a plan diff that
		// never converges — the Outscale spelling of #320, which cost a pull
		// request on the Scaleway side.
		//
		// The rule is measured rather than assumed, and the measurement had to
		// distinguish two candidates that agree on the obvious sample. On
		// 2026-08-21 a real account was sent `UpdateVm` with web-then-db and
		// answered db-then-web; sorting by name and sorting by id both explain
		// that, because the names sorted the same way as the ids. The account's
		// own two machines settle it, and they refute the name:
		//
		//	machine A  sg-2222aaaa "ssh-only",  sg-3333bbbb "alerting"
		//	machine B  sg-2222aaaa "ssh-only",  sg-ffffcccc "open-all"
		//
		// The two rows above are anonymised, and the anonymisation keeps the only thing
		// they are evidence of: the ids ascend, and in neither row is the name order the
		// answered one. The real identifiers and group names are a live account's
		// inventory and this repository is public — docs/proxy.md states the rule:
		// name a path, a type, a status and a position, never a value.
		//
		// Both are in ascending id order, and in neither is the name order the
		// one answered.
		//
		// Sorted on the ids rather than on the rendered list so the comparison
		// is on the field the API orders by, and on a copy so the stored order
		// — which is the client's own reconciliation list — is left alone.
		sorted := append([]string(nil), ids...)
		sort.Strings(sorted)
		out := make([]any, 0, len(sorted))
		for _, id := range sorted {
			if sg, found := p.env.Store.Get(Name, kindSecurityGroup, id); found {
				out = append(out, map[string]any{
					"SecurityGroupId":   sg.ID,
					"SecurityGroupName": stringOf(sg.Attrs["SecurityGroupName"]),
				})
			}
		}
		return out
	}
	subnetID := stringOf(vm.Attrs["SubnetId"])
	if subnetID == "" {
		return nil
	}
	subnet, found := p.env.Store.Get(Name, kindSubnet, subnetID)
	if !found {
		return nil
	}
	out := make([]any, 0, 1)
	for _, sg := range p.securityGroupsOf(stringOf(subnet.Attrs["NetId"])) {
		if stringOf(sg.Attrs["SecurityGroupName"]) == "default" {
			out = append(out, map[string]any{
				"SecurityGroupId":   sg.ID,
				"SecurityGroupName": "default",
			})
		}
	}
	return out
}

// stringsOf reads a list of strings whatever it crossed: the handler stores
// []string, a snapshot restores []any.
func stringsOf(v any) []string {
	switch list := v.(type) {
	case []string:
		return list
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
