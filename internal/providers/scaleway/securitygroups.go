package scaleway

import (
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Security groups are the control plane of the Scaleway firewall: a group holds
// a default policy per direction, and an ordered list of rules that override it.
//
// What the emulator does with them depends on the mode, and saying so is the
// point. With no machine runtime the whole product is served and none of it is
// applied: a client creates groups and rules, reads them back identically and
// drives Terraform through a full apply/destroy, and no packet is filtered.
// With --vm the rules are attached to the interfaces and do filter, within the
// bounds docs/limits.md states.
//
// This comment used to claim the first case unconditionally, months after the
// second became true — the half-truth-without-naming-the-mode that this
// repository's own instructions single out.
//
// Shapes come from the SDK (api/instance/v1/instance_sdk.go): SecurityGroup,
// SecurityGroupRule, and the Create/List/Get/Update request and response types.
// Two asymmetries there cost time if missed. A create takes `security_group` as
// a bare ID string on the server request, while a read returns it as a
// SecurityGroupSummary object. And a rule carries no reference to its group in
// its wire shape, so the link lives in Runtime, which views never serialize.

const (
	kindSecurityGroup     = "instance/security-group"
	kindSecurityGroupRule = "instance/security-group-rule"
)

// runtimeGroupKey links a rule to the group that owns it, out of Attrs so it
// cannot leak into a response the SDK would fail to decode.
const runtimeGroupKey = "security_group"

// defaultSecurityGroupName is what a project gets before anyone creates one.
// Provisioning it is not a convenience: every client reads the existing groups
// before it creates anything, and a project with none is a state the real API
// never returns.
const defaultSecurityGroupName = "Default security group"

// The emulated default group accepts everything in both directions. Scaleway's
// own default is more subtle, and its exact rule set cannot be read from the
// SDK, which carries shapes and not values. Accepting everything is the only
// honest choice for a runtime that filters nothing: a restrictive policy here
// would show a client rules that no packet ever meets.
const (
	policyAccept = "accept"
	policyDrop   = "drop"
)

var (
	validPolicies   = map[string]bool{policyAccept: true, policyDrop: true}
	validDirections = map[string]bool{"inbound": true, "outbound": true}
	validActions    = map[string]bool{policyAccept: true, policyDrop: true}
	validProtocols  = map[string]bool{"TCP": true, "UDP": true, "ICMP": true, "ANY": true}
)

type createSecurityGroupRequest struct {
	Name                  string   `json:"name"`
	Description           string   `json:"description"`
	Project               string   `json:"project"`
	Organization          string   `json:"organization"`
	Tags                  []string `json:"tags"`
	OrganizationDefault   *bool    `json:"organization_default"`
	ProjectDefault        *bool    `json:"project_default"`
	Stateful              *bool    `json:"stateful"`
	InboundDefaultPolicy  string   `json:"inbound_default_policy"`
	OutboundDefaultPolicy string   `json:"outbound_default_policy"`
	EnableDefaultSecurity *bool    `json:"enable_default_security"`
}

type updateSecurityGroupRequest struct {
	Name                  *string   `json:"name"`
	Description           *string   `json:"description"`
	Tags                  *[]string `json:"tags"`
	ProjectDefault        *bool     `json:"project_default"`
	OrganizationDefault   *bool     `json:"organization_default"`
	Stateful              *bool     `json:"stateful"`
	InboundDefaultPolicy  string    `json:"inbound_default_policy"`
	OutboundDefaultPolicy string    `json:"outbound_default_policy"`
	EnableDefaultSecurity *bool     `json:"enable_default_security"`
}

// ruleRequest covers create, update and one entry of a set: the three carry the
// same fields, only their optionality differs. Ports are pointers because the
// API distinguishes "unset" from zero, and Terraform reads that difference back.
type ruleRequest struct {
	ID           *string `json:"id"`
	Protocol     string  `json:"protocol"`
	Direction    string  `json:"direction"`
	Action       string  `json:"action"`
	IPRange      string  `json:"ip_range"`
	DestPortFrom *uint32 `json:"dest_port_from"`
	DestPortTo   *uint32 `json:"dest_port_to"`
	Position     *uint32 `json:"position"`
	Editable     *bool   `json:"editable"`
	// Zone is sent by the CLI and by Terraform on every rule of a
	// SetSecurityGroupRules call, and was silently dropped. It is read now and
	// checked rather than honoured: a rule belongs to the group, and the group
	// to a zone, so a rule naming another one is a request the emulator has no
	// honest answer for.
	Zone string `json:"zone"`
}

type setSecurityGroupRulesRequest struct {
	Rules []ruleRequest `json:"rules"`
}

// rulesBelongTo reports the first rule naming a zone other than the group's.
// Empty means every rule agrees, which is what a client sends.
func rulesBelongTo(zone string, rules []ruleRequest) string {
	for _, rule := range rules {
		if rule.Zone != "" && rule.Zone != zone {
			return rule.Zone
		}
	}
	return ""
}

// ---- Groups -----------------------------------------------------------------

func (p *Pack) listSecurityGroups(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	project := orDefault(q.Get("project"), defaultProject)
	p.ensureDefaultSecurityGroup(zone, project)

	// Scoped to the project, not just to the zone. The SDK sends the configured
	// project on every list, and a client that names one must not be shown
	// another one's groups: it would read several groups each claiming to be the
	// project default, which is what a per-project lazy default produces as soon
	// as a second project is named.
	scope := resource.Tenant{Provider: Name, Project: project, Zone: zone}
	// An organization filter alone scopes to the whole account instead —
	// scopeOf's rule, one organization lives here — never an equality against
	// the pack's constant, which would deny a client its own groups for a
	// configuration detail (projectOf says why the identifier is not compared).
	if q.Get("organization") != "" && q.Get("project") == "" {
		scope.Project = ""
	}
	all := p.env.Store.List(kindSecurityGroup, scope)
	if name := q.Get("name"); name != "" {
		filtered := all[:0]
		for _, res := range all {
			if res.Attrs["name"] == name {
				filtered = append(filtered, res)
			}
		}
		all = filtered
	}
	// "With these exact tags", same conjunction as the servers list.
	if tags := csvValues(q, "tags"); len(tags) > 0 {
		all = filterResources(all, func(res *resource.Resource) bool {
			return hasEveryTag(res, tags)
		})
	}
	if wantDefault, present := queryBool(q, "project_default"); present {
		all = filterResources(all, func(res *resource.Resource) bool {
			isDefault, _ := res.Attrs["project_default"].(bool)
			return isDefault == wantDefault
		})
	}

	page := parsePage(r)
	start, end := page.slice(len(all))
	groups := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		groups = append(groups, p.securityGroupView(res))
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"security_groups": groups,
		"total_count":     len(all),
	})
}

func (p *Pack) createSecurityGroup(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}

	var req createSecurityGroupRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.Name == "" {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "name", Reason: "required"})
		return
	}
	inbound, ok := policyOrDefault(w, "inbound_default_policy", req.InboundDefaultPolicy)
	if !ok {
		return
	}
	outbound, ok := policyOrDefault(w, "outbound_default_policy", req.OutboundDefaultPolicy)
	if !ok {
		return
	}

	project, _ := projectOf(req.Project)
	res := p.newSecurityGroup(zone, project, req.Name, req.Description, inbound, outbound)
	res.Attrs["tags"] = orEmpty(req.Tags)
	res.Attrs["stateful"] = deref(req.Stateful, true)
	res.Attrs["enable_default_security"] = deref(req.EnableDefaultSecurity, true)

	// A group asking to become the project default demotes the previous one:
	// two defaults in a project is a state the API cannot return, and a client
	// that reads both has no way to choose.
	if deref(req.ProjectDefault, false) || deref(req.OrganizationDefault, false) {
		p.clearProjectDefault(zone, project)
		res.Attrs["project_default"] = true
		res.Attrs["organization_default"] = true
	}
	p.env.Store.Put(res)

	emulator.WriteJSON(w, http.StatusCreated, map[string]any{"security_group": p.securityGroupView(res)})
}

func (p *Pack) getSecurityGroup(w http.ResponseWriter, r *http.Request) {
	res, ok := p.securityGroupOf(w, r)
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"security_group": p.securityGroupView(res)})
}

func (p *Pack) updateSecurityGroup(w http.ResponseWriter, r *http.Request) {
	res, ok := p.securityGroupOf(w, r)
	if !ok {
		return
	}

	var req updateSecurityGroupRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.InboundDefaultPolicy != "" && !validPolicies[req.InboundDefaultPolicy] {
		writePolicyError(w, "inbound_default_policy", req.InboundDefaultPolicy)
		return
	}
	if req.OutboundDefaultPolicy != "" && !validPolicies[req.OutboundDefaultPolicy] {
		writePolicyError(w, "outbound_default_policy", req.OutboundDefaultPolicy)
		return
	}
	becomesDefault := deref(req.ProjectDefault, false) || deref(req.OrganizationDefault, false)
	if becomesDefault {
		// A group asking to become the project default demotes the previous
		// one first, exactly as on create: two defaults in a project is a
		// state the API cannot return.
		p.clearProjectDefault(res.Tenant.Zone, res.Tenant.Project)
	}

	// Written inside Store.Update rather than mutate-the-clone-then-Put: Put
	// re-inserts unconditionally, so it resurrected a group deleted while this
	// PATCH was decoding, and a concurrent writer of another field — the tags,
	// say — was erased by the stale whole-Attrs write-back (#289).
	var final *resource.Resource
	err := p.env.Store.Update(Name, kindSecurityGroup, res.ID, func(stored *resource.Resource) error {
		if req.InboundDefaultPolicy != "" {
			stored.Attrs["inbound_default_policy"] = req.InboundDefaultPolicy
		}
		if req.OutboundDefaultPolicy != "" {
			stored.Attrs["outbound_default_policy"] = req.OutboundDefaultPolicy
		}
		if req.Name != nil {
			stored.Attrs["name"] = *req.Name
		}
		if req.Description != nil {
			stored.Attrs["description"] = *req.Description
		}
		if req.Tags != nil {
			stored.Attrs["tags"] = orEmpty(*req.Tags)
		}
		if req.Stateful != nil {
			stored.Attrs["stateful"] = *req.Stateful
		}
		if req.EnableDefaultSecurity != nil {
			stored.Attrs["enable_default_security"] = *req.EnableDefaultSecurity
		}
		if becomesDefault {
			stored.Attrs["project_default"] = true
			stored.Attrs["organization_default"] = true
		}
		stored.Updated = p.env.Now()
		final = stored
		return nil
	})
	if err != nil {
		writeNotFound(w, "security_group", res.ID)
		return
	}
	p.syncSecurityGroup(r.Context(), final)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"security_group": p.securityGroupView(final)})
}

func (p *Pack) deleteSecurityGroup(w http.ResponseWriter, r *http.Request) {
	res, ok := p.securityGroupOf(w, r)
	if !ok {
		return
	}
	// The API refuses to delete the project default, and Terraform depends on
	// that error: without it a destroy silently removes the group every later
	// server creation needs.
	if isProjectDefault(res) {
		writePrecondition(w, "security_group", res.ID, "the default security group of a project cannot be deleted")
		return
	}
	if servers := p.serversUsing(res); len(servers) > 0 {
		writePrecondition(w, "security_group", res.ID,
			"the security group is still attached to "+servers[0].name+" and cannot be deleted")
		return
	}

	for _, rule := range p.rulesOf(res.ID) {
		p.env.Store.Delete(Name, kindSecurityGroupRule, rule.ID)
	}
	p.env.Store.Delete(Name, kindSecurityGroup, res.ID)
	// The runtime rule set goes with the group that justified it. Left behind it
	// would outlive its group until the next sweep, and a later group could not
	// reuse its name.
	p.removeFirewall(r.Context(), res)
	w.WriteHeader(http.StatusNoContent)
}

// ---- Rules ------------------------------------------------------------------

func (p *Pack) listSecurityGroupRules(w http.ResponseWriter, r *http.Request) {
	group, ok := p.securityGroupOf(w, r)
	if !ok {
		return
	}

	all := p.rulesOf(group.ID)
	page := parsePage(r)
	start, end := page.slice(len(all))
	rules := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		rules = append(rules, ruleView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"rules": rules, "total_count": len(all)})
}

// The rule set Scaleway applies to every security group of a zone, served at
// the door their own SDK names it by.
//
// `ListDefaultSecurityGroupRules` was declined, and the decline said what would
// retire it: "Trade it for real values the day someone measures them against
// the real API." They were measured on 2026-08-24, against a real fr-par
// account, and corpus/scaleway/scw-free-shapes.jsonl seq 20 carries what the
// cloud answered. Nothing below is invented: six outbound TCP drops on the
// three submission ports, over both address families, none of them editable.
//
// It is the account-wide SMTP block, and the SDK says so in its own words —
// "Lists the default rules applied to all the security groups"
// (instance/v1/instance_sdk.go). It is not a group's own rule set, which is why
// these are served here and NOT folded into listSecurityGroupRules: nothing in
// this emulator filters an outbound packet, and a rule inside a group's list is
// a rule a client can try to delete. docs/limits.md carries that distinction.
//
// The identifiers are fixed and repeated-digit, the convention catalog.go
// already uses, and deliberately outside the space the corpus sanitiser mints
// (a counter under 00000000-0000-4000-8000-…): a synthetic identifier that
// collides with a minted one turns a replay into a measurement of the
// instrument, which is #395.
//
// TestTheDefaultRuleSegmentIsARuleSetAndNotAGroup fails without this.
var defaultSecurityGroupRules = []struct {
	id       string
	ipRange  string
	destPort uint32
}{
	{"dddddddd-dddd-4ddd-8ddd-dddddddddd01", "0.0.0.0/0", 25},
	{"dddddddd-dddd-4ddd-8ddd-dddddddddd02", "0.0.0.0/0", 465},
	{"dddddddd-dddd-4ddd-8ddd-dddddddddd03", "0.0.0.0/0", 587},
	{"dddddddd-dddd-4ddd-8ddd-dddddddddd04", "::/0", 25},
	{"dddddddd-dddd-4ddd-8ddd-dddddddddd05", "::/0", 465},
	{"dddddddd-dddd-4ddd-8ddd-dddddddddd06", "::/0", 587},
}

// listDefaultSecurityGroupRules answers GET /security_groups/default/rules.
//
// `default` is a literal segment of the SDK's own path and never an identifier,
// which is what made this worth a route of its own: the segment matched {id} on
// the neighbouring route, found no group, and answered 404 — so a decline
// written against one operation was invisible to a client asking for another,
// and the coverage record said "declined" while a live route answered wrong.
func (p *Pack) listDefaultSecurityGroupRules(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	rules := make([]map[string]any, 0, len(defaultSecurityGroupRules))
	for i, rule := range defaultSecurityGroupRules {
		rules = append(rules, map[string]any{
			"id":        rule.id,
			"protocol":  "TCP",
			"direction": "outbound",
			"action":    "drop",
			"ip_range":  rule.ipRange,
			// Null on every rule the recording carries, on this door and on a
			// group's own: no client sends one and the cloud answers none.
			"dest_ip_range":  nil,
			"dest_port_from": rule.destPort,
			"dest_port_to":   nil,
			"position":       uint32(i + 1),
			// False, and it is the whole point: a client cannot edit these.
			"editable": false,
			"zone":     zone,
		})
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"rules": rules, "total_count": len(rules)})
}

func (p *Pack) createSecurityGroupRule(w http.ResponseWriter, r *http.Request) {
	group, ok := p.securityGroupOf(w, r)
	if !ok {
		return
	}

	var req ruleRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	res, ok := p.newRule(w, group, req, nextPosition(len(p.rulesOf(group.ID))))
	if !ok {
		return
	}
	p.env.Store.Put(res)
	p.syncSecurityGroup(r.Context(), group)

	emulator.WriteJSON(w, http.StatusCreated, map[string]any{"rule": ruleView(res)})
}

// setSecurityGroupRules replaces the whole list in one call. Terraform uses it
// for every rule change, so the response must describe the final state: rules
// that were not sent are gone, and the ones that were keep their ID when the
// request carried it.
func (p *Pack) setSecurityGroupRules(w http.ResponseWriter, r *http.Request) {
	group, ok := p.securityGroupOf(w, r)
	if !ok {
		return
	}

	var req setSecurityGroupRulesRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if other := rulesBelongTo(group.Tenant.Zone, req.Rules); other != "" {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "rules.zone",
			Reason:       "constraint",
			HelpMessage:  "rule in zone " + other + " on a group in " + group.Tenant.Zone,
		})
		return
	}

	kept := make(map[string]bool, len(req.Rules))
	built := make([]*resource.Resource, 0, len(req.Rules))
	for i, rule := range req.Rules {
		res, ok := p.newRule(w, group, rule, nextPosition(i))
		if !ok {
			return
		}
		// An ID is honoured only when it names a rule of this group. Otherwise a
		// caller could hand over the ID of another group's rule and take it
		// over: the rule would be rewritten under the thief's group, and it
		// would vanish from the one that owns it.
		if rule.ID != nil && *rule.ID != "" {
			existing, found := p.env.Store.Get(Name, kindSecurityGroupRule, *rule.ID)
			if found && existing.Runtime[runtimeGroupKey] == group.ID {
				res.ID = existing.ID
				res.Created = existing.Created
			}
		}
		kept[res.ID] = true
		built = append(built, res)
	}

	// Drop first, then store: a rule kept under its own ID must survive, and a
	// rule the caller left out must not.
	for _, existing := range p.rulesOf(group.ID) {
		if !kept[existing.ID] {
			p.env.Store.Delete(Name, kindSecurityGroupRule, existing.ID)
		}
	}
	rules := make([]map[string]any, 0, len(built))
	for _, res := range built {
		p.env.Store.Put(res)
		rules = append(rules, ruleView(res))
	}

	p.syncSecurityGroup(r.Context(), group)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"rules": rules})
}

func (p *Pack) getSecurityGroupRule(w http.ResponseWriter, r *http.Request) {
	res, ok := p.securityGroupRuleOf(w, r)
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"rule": ruleView(res)})
}

func (p *Pack) updateSecurityGroupRule(w http.ResponseWriter, r *http.Request) {
	res, ok := p.securityGroupRuleOf(w, r)
	if !ok {
		return
	}

	var req ruleRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.Protocol != "" && !validProtocols[req.Protocol] {
		writeEnumError(w, "protocol", req.Protocol)
		return
	}
	if req.Direction != "" && !validDirections[req.Direction] {
		writeEnumError(w, "direction", req.Direction)
		return
	}
	if req.Action != "" && !validActions[req.Action] {
		writeEnumError(w, "action", req.Action)
		return
	}
	ipRange := ""
	if req.IPRange != "" {
		normalized, ok := normalizeIPRange(req.IPRange)
		if !ok {
			writeInvalidArguments(w, ArgumentError{
				ArgumentName: "ip_range",
				Reason:       "constraint",
				HelpMessage:  "not an IP network: " + req.IPRange,
			})
			return
		}
		ipRange = normalized
	}

	// Written inside Store.Update rather than mutate-the-clone-then-Put: Put
	// re-inserts unconditionally, so it resurrected a rule deleted while this
	// PATCH was decoding, and it wrote back a whole Attrs built from a stale
	// read, which is the lost-update shape #289 measured on the pack next door.
	var final *resource.Resource
	err := p.env.Store.Update(Name, kindSecurityGroupRule, res.ID, func(stored *resource.Resource) error {
		if req.Protocol != "" {
			stored.Attrs["protocol"] = req.Protocol
		}
		if req.Direction != "" {
			stored.Attrs["direction"] = req.Direction
		}
		if req.Action != "" {
			stored.Attrs["action"] = req.Action
		}
		if req.IPRange != "" {
			stored.Attrs["ip_range"] = ipRange
		}
		// A zero port unsets the bound, which the SDK documents on the update
		// request. Anything else would leave a rule Terraform cannot narrow back.
		if req.DestPortFrom != nil {
			stored.Attrs["dest_port_from"] = portOrNil(req.DestPortFrom)
		}
		if req.DestPortTo != nil {
			stored.Attrs["dest_port_to"] = portOrNil(req.DestPortTo)
		}
		if req.Position != nil {
			stored.Attrs["position"] = *req.Position
		}
		stored.Updated = p.env.Now()
		final = stored
		return nil
	})
	if err != nil {
		writeNotFound(w, "security_group_rule", res.ID)
		return
	}
	if group, found := p.env.Store.Get(Name, kindSecurityGroup, final.Runtime[runtimeGroupKey]); found {
		p.syncSecurityGroup(r.Context(), group)
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"rule": ruleView(final)})
}

func (p *Pack) deleteSecurityGroupRule(w http.ResponseWriter, r *http.Request) {
	res, ok := p.securityGroupRuleOf(w, r)
	if !ok {
		return
	}
	groupID := res.Runtime[runtimeGroupKey]
	p.env.Store.Delete(Name, kindSecurityGroupRule, res.ID)
	if group, found := p.env.Store.Get(Name, kindSecurityGroup, groupID); found {
		p.syncSecurityGroup(r.Context(), group)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Default inventory ------------------------------------------------------

// ensureDefaultSecurityGroup provisions the group a fresh project already has.
//
// It is lazy on purpose: the emulator has no notion of "a project was created",
// so the first read of a zone is the only moment it can happen. The mutex is
// what keeps two concurrent reads from provisioning it twice; the store offers
// no compare-and-set, and a duplicated default is a state no client expects.
func (p *Pack) ensureDefaultSecurityGroup(zone, project string) *resource.Resource {
	unlock := p.lockDefaults()
	defer unlock()

	for _, res := range p.env.Store.List(kindSecurityGroup, resource.Tenant{Provider: Name, Project: project, Zone: zone}) {
		if isProjectDefault(res) {
			return res
		}
	}

	res := p.newSecurityGroup(zone, project, defaultSecurityGroupName,
		"Default security group", policyAccept, policyAccept)
	res.Attrs["project_default"] = true
	res.Attrs["organization_default"] = true
	p.env.Store.Put(res)
	return res
}

// defaultSecurityGroupSummary is what a server carries: the {id, name} pair the
// SDK decodes into SecurityGroupSummary, never the full group.
func (p *Pack) defaultSecurityGroupSummary(zone, project string) map[string]any {
	res := p.ensureDefaultSecurityGroup(zone, project)
	return summaryOf(res)
}

// securityGroupSummary resolves the group a create request asked for by ID,
// falling back to the project default. An unknown ID is reported rather than
// silently replaced: a server attached to a group the caller did not ask for is
// worse than a clear error.
func (p *Pack) securityGroupSummary(zone, project, id string) (map[string]any, bool) {
	if id == "" {
		return p.defaultSecurityGroupSummary(zone, project), true
	}
	res, found := p.env.Store.Get(Name, kindSecurityGroup, id)
	// The project is checked as well as the zone. A project is an isolation
	// boundary, which scopeOf defends on every list, and checking only the zone
	// here let a client attach another project's group to its own server —
	// crossing the one boundary the rest of this pack is careful about.
	if !found || res.Tenant.Zone != zone || res.Tenant.Project != project {
		return nil, false
	}
	return summaryOf(res), true
}

// ---- Plumbing ---------------------------------------------------------------

func (p *Pack) newSecurityGroup(zone, project, name, description, inbound, outbound string) *resource.Resource {
	now := p.env.Now()
	return &resource.Resource{
		ID:      p.env.NewID(),
		Kind:    kindSecurityGroup,
		Tenant:  resource.Tenant{Provider: Name, Project: project, Zone: zone},
		State:   "available",
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"name":                    name,
			"description":             description,
			"organization":            defaultOrganization,
			"project":                 project,
			"tags":                    []string{},
			"inbound_default_policy":  inbound,
			"outbound_default_policy": outbound,
			"enable_default_security": true,
			"stateful":                true,
			"project_default":         false,
			"organization_default":    false,
			"servers":                 []any{},
		},
	}
}

// newRule validates a rule request and builds the resource, without storing it:
// set-rules needs to reject the whole batch before it changes anything.
func (p *Pack) newRule(w http.ResponseWriter, group *resource.Resource, req ruleRequest, position uint32) (*resource.Resource, bool) {
	protocol := orDefault(req.Protocol, "TCP")
	if !validProtocols[protocol] {
		writeEnumError(w, "protocol", protocol)
		return nil, false
	}
	direction := orDefault(req.Direction, "inbound")
	if !validDirections[direction] {
		writeEnumError(w, "direction", direction)
		return nil, false
	}
	action := orDefault(req.Action, policyAccept)
	if !validActions[action] {
		writeEnumError(w, "action", action)
		return nil, false
	}
	ipRange, ok := normalizeIPRange(req.IPRange)
	if !ok {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "ip_range",
			Reason:       "constraint",
			HelpMessage:  "not an IP network: " + req.IPRange,
		})
		return nil, false
	}
	if req.Position != nil && *req.Position > 0 {
		position = *req.Position
	}

	now := p.env.Now()
	res := resource.New(p.env.NewID(), kindSecurityGroupRule, group.Tenant, "available", now)
	res.Attrs = map[string]any{
		"protocol":  protocol,
		"direction": direction,
		"action":    action,
		// The SDK decodes ip_range into scw.IPNet, which requires a CIDR:
		// a bare address makes the client fail on the response it just got.
		"ip_range": ipRange,
		// Null, and present. A real fr-par account answers this key on every
		// rule it hands back and never with a value: no client of the recording
		// sends one, and the cloud answers null on the create, the read, the
		// update and both lists (corpus/scaleway/scw-free-shapes.jsonl seq
		// 21-26). Their own Go SDK has no field for it and their published
		// document does not declare it either, so nothing that reads a document
		// could have found this — only the wire.
		//
		// Present-with-null rather than absent, because that is what was
		// measured: an omitted key is a shape the cloud never answers, and a
		// client walking the JSON sees a rule with no such notion at all.
		//
		// TestARuleAnswersTheDestinationRangeKeyTheCloudAnswers fails without
		// this.
		"dest_ip_range":  nil,
		"dest_port_from": portOrNil(req.DestPortFrom),
		"dest_port_to":   portOrNil(req.DestPortTo),
		"position":       position,
		"editable":       deref(req.Editable, true),
	}
	res.Runtime = map[string]string{runtimeGroupKey: group.ID}
	return res, true
}

// normalizeIPRange validates a rule's ip_range the way the SDK reads it: a bare
// address gets its host mask, anything unparseable is refused.
//
// Refusing matters beyond hygiene. The runtime rejects a rule set holding an
// invalid block wholesale, so a stored-but-unparseable ip_range would leave the
// machine enforcing the previous rules while the API describes the new ones,
// which is the exact class of lie this emulator exists to avoid. Measured on
// incus 7.2: one bad rule and the whole PUT is refused.
func normalizeIPRange(raw string) (string, bool) {
	if raw == "" {
		return "0.0.0.0/0", true
	}
	if !strings.Contains(raw, "/") {
		addr, err := netip.ParseAddr(raw)
		switch {
		case err != nil:
			return "", false
		case addr.Is4():
			return raw + "/32", true
		default:
			return raw + "/128", true
		}
	}
	if _, err := netip.ParsePrefix(raw); err != nil {
		return "", false
	}
	return raw, true
}

// rulesOf returns the rules of a group, ordered by position: the API serves an
// ordered list, and a client that reorders rules reads the order back.
func (p *Pack) rulesOf(groupID string) []*resource.Resource {
	all := p.env.Store.List(kindSecurityGroupRule, resource.Tenant{Provider: Name})
	rules := make([]*resource.Resource, 0, len(all))
	for _, res := range all {
		if res.Runtime[runtimeGroupKey] == groupID {
			rules = append(rules, res)
		}
	}
	sort.SliceStable(rules, func(i, j int) bool {
		return positionOf(rules[i]) < positionOf(rules[j])
	})
	return rules
}

// serverSummary is what the group's servers list carries per entry: the
// ServerSummary pair the SDK declares. The id was missing until the field gate
// measured it against a real account's answer (#88).
type serverSummary struct {
	id   string
	name string
}

// serversUsing names the servers still attached to a group, so a delete can
// refuse with the same precondition the API enforces.
func (p *Pack) serversUsing(group *resource.Resource) []serverSummary {
	var out []serverSummary
	for _, res := range p.env.Store.List(kindServer, p.tenant(group.Tenant.Zone)) {
		summary, _ := res.Attrs["security_group"].(map[string]any)
		if summary != nil && summary["id"] == group.ID {
			name, _ := res.Attrs["name"].(string)
			out = append(out, serverSummary{id: res.ID, name: name})
		}
	}
	return out
}

func (p *Pack) clearProjectDefault(zone, project string) {
	for _, res := range p.env.Store.List(kindSecurityGroup, resource.Tenant{Provider: Name, Project: project, Zone: zone}) {
		if isProjectDefault(res) {
			// Update rather than Put: a Put here resurrected a group deleted
			// between the List and the write, and erased any field a
			// concurrent request had just stored on it (#289).
			_ = p.env.Store.Update(Name, kindSecurityGroup, res.ID, func(stored *resource.Resource) error {
				stored.Attrs["project_default"] = false
				stored.Attrs["organization_default"] = false
				stored.Updated = p.env.Now()
				return nil
			})
		}
	}
}

// securityGroupOf resolves the {id} segment, writing the error itself. It is
// what keeps every handler above a single early return.
func (p *Pack) securityGroupOf(w http.ResponseWriter, r *http.Request) (*resource.Resource, bool) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return nil, false
	}
	id := r.PathValue("id")
	res, found := p.env.Store.Get(Name, kindSecurityGroup, id)
	if !found || res.Tenant.Zone != zone {
		writeNotFound(w, "security_group", id)
		return nil, false
	}
	return res, true
}

func (p *Pack) securityGroupRuleOf(w http.ResponseWriter, r *http.Request) (*resource.Resource, bool) {
	group, ok := p.securityGroupOf(w, r)
	if !ok {
		return nil, false
	}
	id := r.PathValue("ruleID")
	res, found := p.env.Store.Get(Name, kindSecurityGroupRule, id)
	// A rule read through the wrong group is not found, not someone else's rule.
	if !found || res.Runtime[runtimeGroupKey] != group.ID {
		writeNotFound(w, "security_group_rule", id)
		return nil, false
	}
	return res, true
}

func (p *Pack) securityGroupView(res *resource.Resource) map[string]any {
	out := make(map[string]any, len(res.Attrs)+6)
	for k, v := range res.Attrs {
		out[k] = v
	}
	out["id"] = res.ID
	out["zone"] = res.Tenant.Zone
	out["state"] = res.State
	out["creation_date"] = res.Created.Format(time.RFC3339)
	out["modification_date"] = res.Updated.Format(time.RFC3339)
	// servers is computed rather than stored: a server attached after the group
	// was created would otherwise never show up here.
	servers := make([]any, 0)
	for _, srv := range p.serversUsing(res) {
		servers = append(servers, map[string]any{"id": srv.id, "name": srv.name})
	}
	out["servers"] = servers
	return out
}

func ruleView(res *resource.Resource) map[string]any {
	out := make(map[string]any, len(res.Attrs)+2)
	for k, v := range res.Attrs {
		out[k] = v
	}
	out["id"] = res.ID
	out["zone"] = res.Tenant.Zone
	return out
}

func summaryOf(res *resource.Resource) map[string]any {
	return map[string]any{"id": res.ID, "name": res.Attrs["name"]}
}

func isProjectDefault(res *resource.Resource) bool {
	def, _ := res.Attrs["project_default"].(bool)
	return def
}

// nextPosition is the position a rule receives when it follows count others.
// Positions are 1-based, and the cap keeps the conversion from wrapping whatever
// a caller passes.
func nextPosition(count int) uint32 {
	const maxPosition = 1 << 20
	if count < 0 {
		return 1
	}
	if count+1 > maxPosition {
		return maxPosition
	}
	return uint32(count + 1) //nolint:gosec // bounded by maxPosition above
}

func positionOf(res *resource.Resource) uint32 {
	switch v := res.Attrs["position"].(type) {
	case uint32:
		return v
	// A restored snapshot comes back through JSON, where every number is a
	// float64. Without this case the order is lost on restart.
	case float64:
		return uint32(v)
	default:
		return 0
	}
}

// portOrNil maps an unset or zero port onto null, which is what the API returns
// for a rule that covers every port.
func portOrNil(port *uint32) any {
	if port == nil || *port == 0 {
		return nil
	}
	return *port
}

func policyOrDefault(w http.ResponseWriter, field, value string) (string, bool) {
	if value == "" {
		return policyAccept, true
	}
	if !validPolicies[value] {
		writePolicyError(w, field, value)
		return "", false
	}
	return value, true
}

func writePolicyError(w http.ResponseWriter, field, value string) {
	writeInvalidArguments(w, ArgumentError{
		ArgumentName: field,
		Reason:       "constraint",
		HelpMessage:  "must be " + policyAccept + " or " + policyDrop + ", got " + value,
	})
}

func writeEnumError(w http.ResponseWriter, field, value string) {
	writeInvalidArguments(w, ArgumentError{
		ArgumentName: field,
		Reason:       "constraint",
		HelpMessage:  "unsupported value " + value,
	})
}
