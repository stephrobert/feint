package outscale

import (
	"net/http"

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
	ResultsPerPage int       `json:"ResultsPerPage"`
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
		"SecurityGroups":  page(out, req.ResultsPerPage),
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
