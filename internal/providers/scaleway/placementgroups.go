package scaleway

import (
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Placement groups, served as the record they are (#285).
//
// A placement group is a scheduling constraint, and this emulator has no
// scheduler: with --vm off nothing runs, and with a runtime every machine is a
// container on the single host that started feint. The family was declined on
// that ground, with a reason that named the real risk: "any policy would be
// reported satisfied whatever it asked".
//
// The measurement that reversed the decision looked at what a client actually
// does with the group. The Terraform provider — 2.43.0, the pin of the surveyed
// terraform-talos stack, and 2.81.0, the conformance pin — creates the group,
// reads it back, and stores `policy_respected` as a Computed attribute it never
// gates on (placement_group.go in both tags: setPlacementGroupState only calls
// d.Set). Nothing a driven client does depends on machines actually landing
// apart; everything depends on the record round-tripping. That is the exact
// profile of a security group without a runtime, which this pack serves.
//
// What remains of the old reason is an obligation, not a refusal: the one field
// that could turn the record into a promise, `policy_respected`, must tell the
// truth about the single host instead of flattering the policy. The rule is
// placementPolicyRespected, and docs/limits.md carries the limit.
const kindPlacementGroup = "instance/placement-group"

// attrServerPlacementGroup is the server attribute carrying the ID of the
// placement group the server belongs to, or nil. The membership lives on the
// server, never on the group, the way the Exoscale pack stores anti-affinity:
// the group computes its members by scanning, so a deleted server cannot leave
// a stale entry behind.
//
// The key is spelled exactly like the response field on purpose: view()
// replaces the stored ID with the object shape the SDK declares
// (Server.PlacementGroup is *PlacementGroup, instance/v1/instance_sdk.go).
const attrServerPlacementGroup = "placement_group"

// The two enums, from the v1 SDK (PlacementGroupPolicyMode and
// PlacementGroupPolicyType in instance_sdk.go). v2alpha1 adds
// unknown_policy_type as a zero value; it is an absence, not a third policy.
var (
	placementPolicyModes = []string{"optional", "enforced"}
	placementPolicyTypes = []string{"max_availability", "low_latency"}
)

type createPlacementGroupRequest struct {
	Name         string   `json:"name"`
	Organization string   `json:"organization"`
	Project      string   `json:"project"`
	Tags         []string `json:"tags"`
	PolicyMode   string   `json:"policy_mode"`
	PolicyType   string   `json:"policy_type"`
}

type updatePlacementGroupRequest struct {
	Name       *string   `json:"name"`
	Tags       *[]string `json:"tags"`
	PolicyMode *string   `json:"policy_mode"`
	PolicyType *string   `json:"policy_type"`
}

// setPlacementGroupRequest is the PUT shape (SetPlacementGroupRequest in the
// SDK): every field is a value, and what is absent is replaced by its default,
// which is what `scw instance placement-group set` sends.
type setPlacementGroupRequest struct {
	Name         string    `json:"name"`
	Organization string    `json:"organization"`
	Project      string    `json:"project"`
	Tags         *[]string `json:"tags"`
	PolicyMode   string    `json:"policy_mode"`
	PolicyType   string    `json:"policy_type"`
}

type placementGroupServersRequest struct {
	Servers *[]string `json:"servers"`
}

func (p *Pack) listPlacementGroups(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()

	scope := resource.Tenant{Provider: Name, Zone: zone}
	if project := q.Get("project"); project != "" {
		scope.Project = project
	}
	// An organization filter scopes to the whole account: one organization
	// lives here (scopeOf's rule), so it widens nothing the empty project
	// scope above does not already include — read rather than dropped, and
	// never compared against the pack's constant, which would deny a client
	// its own groups for a configuration detail (projectOf says why).
	_ = q.Get("organization")
	all := p.env.Store.List(kindPlacementGroup, scope)
	if name := q.Get("name"); name != "" {
		// Substring, not equality: the API document's own example says
		// "cluster1" returns cluster100 and cluster1.
		all = filterResources(all, func(res *resource.Resource) bool {
			return strings.Contains(textOf(res.Attrs["name"]), name)
		})
	}
	if tags := csvValues(q, "tags"); len(tags) > 0 {
		all = filterResources(all, func(res *resource.Resource) bool {
			return hasEveryTag(res, tags)
		})
	}
	sortByCreationThenID(all)

	page := parsePage(r)
	start, end := page.slice(len(all))
	groups := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		groups = append(groups, p.placementGroupView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"placement_groups": groups,
		"total_count":      len(all),
	})
}

func (p *Pack) createPlacementGroup(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	var req createPlacementGroupRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.Name == "" {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "name", Reason: "required"})
		return
	}
	mode, ok := placementEnum(w, "policy_mode", req.PolicyMode, "optional", placementPolicyModes)
	if !ok {
		return
	}
	policy, ok := placementEnum(w, "policy_type", req.PolicyType, "max_availability", placementPolicyTypes)
	if !ok {
		return
	}

	project, organization := projectOf(req.Project)
	res := resource.New(p.env.NewID(), kindPlacementGroup, resource.Tenant{Provider: Name, Project: project, Zone: zone}, "available", p.env.Now())
	res.Attrs = map[string]any{
		"name":         req.Name,
		"organization": organization,
		"project":      project,
		"tags":         orEmpty(req.Tags),
		"policy_mode":  mode,
		"policy_type":  policy,
	}
	p.env.Store.Put(res)
	emulator.WriteJSON(w, http.StatusCreated, map[string]any{"placement_group": p.placementGroupView(res)})
}

func (p *Pack) getPlacementGroup(w http.ResponseWriter, r *http.Request) {
	res, ok := p.placementGroupOf(w, r)
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"placement_group": p.placementGroupView(res)})
}

// setPlacementGroupFull is the PUT door. Ownership is not transferable here:
// organization and project are decoded so the report of unread fields stays
// honest, and the stored tenant keeps ruling — moving a resource between
// projects is not a rename, and no driven client asks for it.
func (p *Pack) setPlacementGroupFull(w http.ResponseWriter, r *http.Request) {
	res, ok := p.placementGroupOf(w, r)
	if !ok {
		return
	}
	var req setPlacementGroupRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.Name == "" {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "name", Reason: "required"})
		return
	}
	mode, ok := placementEnum(w, "policy_mode", req.PolicyMode, "optional", placementPolicyModes)
	if !ok {
		return
	}
	policy, ok := placementEnum(w, "policy_type", req.PolicyType, "max_availability", placementPolicyTypes)
	if !ok {
		return
	}
	var updated *resource.Resource
	err := p.env.Store.Update(Name, kindPlacementGroup, res.ID, func(stored *resource.Resource) error {
		stored.Attrs["name"] = req.Name
		stored.Attrs["policy_mode"] = mode
		stored.Attrs["policy_type"] = policy
		if req.Tags != nil {
			stored.Attrs["tags"] = orEmpty(*req.Tags)
		} else {
			stored.Attrs["tags"] = []any{}
		}
		stored.Updated = p.env.Now()
		updated = stored
		return nil
	})
	if err != nil {
		writeNotFound(w, "placement_group", res.ID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"placement_group": p.placementGroupView(updated)})
}

func (p *Pack) updatePlacementGroup(w http.ResponseWriter, r *http.Request) {
	res, ok := p.placementGroupOf(w, r)
	if !ok {
		return
	}
	var req updatePlacementGroupRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	mode := ""
	if req.PolicyMode != nil {
		if mode, ok = placementEnum(w, "policy_mode", *req.PolicyMode, "optional", placementPolicyModes); !ok {
			return
		}
	}
	policy := ""
	if req.PolicyType != nil {
		if policy, ok = placementEnum(w, "policy_type", *req.PolicyType, "max_availability", placementPolicyTypes); !ok {
			return
		}
	}
	var updated *resource.Resource
	err := p.env.Store.Update(Name, kindPlacementGroup, res.ID, func(stored *resource.Resource) error {
		if req.Name != nil {
			stored.Attrs["name"] = *req.Name
		}
		if req.Tags != nil {
			stored.Attrs["tags"] = orEmpty(*req.Tags)
		}
		if req.PolicyMode != nil {
			stored.Attrs["policy_mode"] = mode
		}
		if req.PolicyType != nil {
			stored.Attrs["policy_type"] = policy
		}
		stored.Updated = p.env.Now()
		updated = stored
		return nil
	})
	if err != nil {
		writeNotFound(w, "placement_group", res.ID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"placement_group": p.placementGroupView(updated)})
}

// deletePlacementGroup removes the group and releases its members. The servers
// survive the group — deleting a constraint never deletes what it constrained —
// so their membership attribute is cleared rather than left dangling: a stored
// reference to a dead group would round-trip through a snapshot and make every
// later reader re-decide what a missing group means.
func (p *Pack) deletePlacementGroup(w http.ResponseWriter, r *http.Request) {
	res, ok := p.placementGroupOf(w, r)
	if !ok {
		return
	}
	for _, member := range p.placementGroupMembers(res.ID) {
		_ = p.env.Store.Update(Name, kindServer, member.ID, func(stored *resource.Resource) error {
			stored.Attrs[attrServerPlacementGroup] = nil
			return nil
		})
	}
	p.env.Store.Delete(Name, kindPlacementGroup, res.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (p *Pack) getPlacementGroupServers(w http.ResponseWriter, r *http.Request) {
	res, ok := p.placementGroupOf(w, r)
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"servers": p.placementGroupServerViews(res)})
}

// setPlacementGroupServers replaces the whole membership (PUT). PATCH goes
// through the same replacement when the list is present, which is what the SDK
// request shapes say: both carry `servers`, an array of instance UUIDs, and the
// difference upstream is that PATCH may omit it.
func (p *Pack) setPlacementGroupServers(w http.ResponseWriter, r *http.Request) {
	p.replacePlacementGroupServers(w, r, true)
}

func (p *Pack) updatePlacementGroupServers(w http.ResponseWriter, r *http.Request) {
	p.replacePlacementGroupServers(w, r, false)
}

func (p *Pack) replacePlacementGroupServers(w http.ResponseWriter, r *http.Request, required bool) {
	res, ok := p.placementGroupOf(w, r)
	if !ok {
		return
	}
	var req placementGroupServersRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.Servers == nil {
		if required {
			writeInvalidArguments(w, ArgumentError{ArgumentName: "servers", Reason: "required"})
			return
		}
		emulator.WriteJSON(w, http.StatusOK, map[string]any{"servers": p.placementGroupServerViews(res)})
		return
	}

	// Every named server is resolved before anything is written, the same
	// all-or-nothing the flexible IPs of createServer apply: a 404 halfway
	// through a membership rewrite would leave a state no client sent.
	wanted := *req.Servers
	members := make([]*resource.Resource, 0, len(wanted))
	for _, id := range wanted {
		server, found := p.env.Store.Get(Name, kindServer, id)
		if !found || server.Tenant.Zone != res.Tenant.Zone {
			writeNotFound(w, "server", id)
			return
		}
		members = append(members, server)
	}
	for _, member := range p.placementGroupMembers(res.ID) {
		if !slices.Contains(wanted, member.ID) {
			_ = p.env.Store.Update(Name, kindServer, member.ID, func(stored *resource.Resource) error {
				stored.Attrs[attrServerPlacementGroup] = nil
				return nil
			})
		}
	}
	for _, member := range members {
		_ = p.env.Store.Update(Name, kindServer, member.ID, func(stored *resource.Resource) error {
			stored.Attrs[attrServerPlacementGroup] = res.ID
			return nil
		})
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"servers": p.placementGroupServerViews(res)})
}

// placementGroupOf resolves the {id} of a placement group path within its zone.
func (p *Pack) placementGroupOf(w http.ResponseWriter, r *http.Request) (*resource.Resource, bool) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return nil, false
	}
	id := r.PathValue("id")
	res, found := p.env.Store.Get(Name, kindPlacementGroup, id)
	if !found || res.Tenant.Zone != zone {
		writeNotFound(w, "placement_group", id)
		return nil, false
	}
	return res, true
}

// placementGroupMembers lists the servers wearing the group, oldest first.
func (p *Pack) placementGroupMembers(groupID string) []*resource.Resource {
	all := p.env.Store.List(kindServer, resource.Tenant{Provider: Name})
	members := make([]*resource.Resource, 0)
	for _, res := range all {
		if id, _ := res.Attrs[attrServerPlacementGroup].(string); id == groupID {
			members = append(members, res)
		}
	}
	sortByCreationThenID(members)
	return members
}

// sortByCreationThenID orders oldest first, with the ID as a tiebreak: two
// reads of the same list must agree even when a pinned clock makes every
// Created equal, because an order that moves between reads is a diff Terraform
// cannot explain.
func sortByCreationThenID(list []*resource.Resource) {
	sort.Slice(list, func(i, j int) bool {
		if !list[i].Created.Equal(list[j].Created) {
			return list[i].Created.Before(list[j].Created)
		}
		return list[i].ID < list[j].ID
	})
}

// placementPolicyRespected answers the one question that decides whether this
// resource is a record or a lie: is the group's policy true of the machines
// this emulator actually runs?
//
// Every emulated machine runs on the single host that started feint. So:
//
//   - low_latency asks for machines grouped on the same hardware, and one host
//     delivers exactly that, vacuously or not — true.
//   - max_availability asks for machines spread across hardware, which one
//     host can never deliver once two of the group's servers are running. A
//     server that is not running sits on no hypervisor at all (the server view
//     already answers `location: null` for it), so it violates nothing.
//
// The declined era's reason was "any policy would be reported satisfied
// whatever it asked". This function is what made serving possible: the spread
// policy of a group with two running members reads false, never a flattering
// true. TestASpreadPolicyOnTheSingleHostReadsUnrespected fails without it.
func (p *Pack) placementPolicyRespected(res *resource.Resource) bool {
	if policy, _ := res.Attrs["policy_type"].(string); policy == "low_latency" {
		return true
	}
	running := 0
	for _, member := range p.placementGroupMembers(res.ID) {
		if member.State == "running" {
			running++
		}
	}
	return running < 2
}

// placementGroupView renders the group in the v1 PlacementGroup shape
// (instance/v1/instance_sdk.go): id, name, organization, project, tags,
// policy_mode, policy_type, policy_respected, zone. The SDK's own comment on
// PolicyRespected says the placement group endpoints carry the correct value —
// here, correct means true of the single host, computed at read time.
func (p *Pack) placementGroupView(res *resource.Resource) map[string]any {
	return map[string]any{
		"id":               res.ID,
		"name":             res.Attrs["name"],
		"organization":     res.Attrs["organization"],
		"project":          res.Attrs["project"],
		"tags":             res.Attrs["tags"],
		"policy_mode":      res.Attrs["policy_mode"],
		"policy_type":      res.Attrs["policy_type"],
		"policy_respected": p.placementPolicyRespected(res),
		"zone":             res.Tenant.Zone,
	}
}

// placementGroupServerViews renders the members in the PlacementGroupServer
// shape: {id, name, policy_respected}. The per-server bit carries the group's
// own truth — on one host there is no per-server placement to distinguish.
func (p *Pack) placementGroupServerViews(group *resource.Resource) []any {
	respected := p.placementPolicyRespected(group)
	out := make([]any, 0)
	for _, member := range p.placementGroupMembers(group.ID) {
		out = append(out, map[string]any{
			"id":               member.ID,
			"name":             member.Attrs["name"],
			"policy_respected": respected,
		})
	}
	return out
}

// serverPlacementGroupView renders Server.placement_group: the group object,
// or nil for a server in none.
//
// policy_respected is always false here, and that is not this emulator's
// choice: "In the server endpoints the value is always false as it is
// deprecated. In the placement group endpoints the value is correct."
// (instance/v1/instance_sdk.go, PlacementGroup.PolicyRespected). Serving the
// computed value instead would diverge from the recorded API on the one field
// this family had to get right.
func (p *Pack) serverPlacementGroupView(server *resource.Resource) any {
	id, _ := server.Attrs[attrServerPlacementGroup].(string)
	if id == "" {
		return nil
	}
	group, found := p.env.Store.Get(Name, kindPlacementGroup, id)
	if !found {
		return nil
	}
	view := p.placementGroupView(group)
	view["policy_respected"] = false
	return view
}

// placementEnum validates one of the two policy fields against its SDK enum,
// applying the SDK's own default when the field is empty.
func placementEnum(w http.ResponseWriter, field, value, fallback string, allowed []string) (string, bool) {
	if value == "" {
		return fallback, true
	}
	if slices.Contains(allowed, value) {
		return value, true
	}
	writeInvalidArguments(w, ArgumentError{
		ArgumentName: field,
		Reason:       "constraint",
		HelpMessage:  "unknown " + field + " " + value,
	})
	return "", false
}
