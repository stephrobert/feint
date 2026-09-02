package scaleway

import (
	"net/http"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// instance/v2alpha1 reads and writes the same placement groups instance/v1
// does — one store, two shapes, the private-network-interface precedent.
//
// The client that forces this door open is the same one that forced the NIC
// one: Terraform provider 2.81.0 moved the placement group resource's CRUD
// onto v2alpha1 (placement_group.go at tag v2.81.0 imports
// api/instance/v2alpha1 for Create/Get/Update/Delete) while still reaching
// through v1 for the two fields the alpha does not carry, policy_mode and
// policy_respected (fetchPlacementGroupV1, called on every read). A client
// that mixes both halves on one resource is the shape to survive.
//
// Where the two views differ is exactly where the SDKs differ: v1 spells the
// owner `project` and carries policy_mode and policy_respected; v2alpha1
// spells it `project_id`, drops both policy fields, and adds created_at and
// updated_at. The alpha's create has no policy_mode at all — the provider
// PATCHes it through v1 right after creating — so a group born through this
// door starts with v1's own default, optional.
const placementGroupsV2Path = "/instance/v2alpha1/zones/{zone}/placement-groups"

func (p *Pack) listPlacementGroupsV2(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	ids := csvValues(q, "placement_group_ids")
	tags := csvValues(q, "tags")
	name := q.Get("name")
	project := q.Get("project_id")

	all := p.env.Store.List(kindPlacementGroup, resource.Tenant{Provider: Name, Zone: zone})
	matched := make([]*resource.Resource, 0, len(all))
	for _, res := range all {
		if project != "" && res.Tenant.Project != project {
			continue
		}
		if len(ids) > 0 && !contains(ids, res.ID) {
			continue
		}
		if name != "" && !strings.Contains(textOf(res.Attrs["name"]), name) {
			continue
		}
		if len(tags) > 0 && !hasEveryTag(res, tags) {
			continue
		}
		matched = append(matched, res)
	}

	if !orderResources(w, r, "order_by", "created_at_desc", map[string]resourceCmp{
		"created_at": cmpCreated,
		"updated_at": cmpUpdated,
	}, matched) {
		return
	}

	start, end, next := tokenPage(r, len(matched))
	view := make([]map[string]any, 0, end-start)
	for _, res := range matched[start:end] {
		view = append(view, p.placementGroupV2View(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"placement_groups": view,
		"next_page_token":  next,
		"total_count":      len(matched),
	})
}

func (p *Pack) createPlacementGroupV2(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	var req struct {
		ProjectID  string   `json:"project_id"`
		Name       string   `json:"name"`
		PolicyType string   `json:"policy_type"`
		Tags       []string `json:"tags"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.Name == "" {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "name", Reason: "required"})
		return
	}
	// unknown_policy_type is the alpha enum's zero value, an absence rather
	// than a policy: it falls back to v1's default the way an empty string
	// does, so the same stored group answers both doors without a third state.
	policyType := req.PolicyType
	if policyType == "unknown_policy_type" {
		policyType = ""
	}
	policy, ok := placementEnum(w, "policy_type", policyType, "max_availability", placementPolicyTypes)
	if !ok {
		return
	}

	if p.refuseUnknownProject(w, req.ProjectID, projectDeniedToInstance) {
		return
	}
	project, organization := projectOf(req.ProjectID)
	res := resource.New(p.env.NewID(), kindPlacementGroup, resource.Tenant{Provider: Name, Project: project, Zone: zone}, "available", p.env.Now())
	res.Attrs = map[string]any{
		"name":         req.Name,
		"organization": organization,
		"project":      project,
		"tags":         orEmpty(req.Tags),
		"policy_mode":  "optional",
		"policy_type":  policy,
	}
	p.env.Store.Put(res)
	emulator.WriteJSON(w, http.StatusCreated, p.placementGroupV2View(res))
}

func (p *Pack) getPlacementGroupV2(w http.ResponseWriter, r *http.Request) {
	res, ok := p.placementGroupOf(w, r)
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.placementGroupV2View(res))
}

func (p *Pack) updatePlacementGroupV2(w http.ResponseWriter, r *http.Request) {
	res, ok := p.placementGroupOf(w, r)
	if !ok {
		return
	}
	var req struct {
		Name       *string   `json:"name"`
		PolicyType string    `json:"policy_type"`
		Tags       *[]string `json:"tags"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	policyType := req.PolicyType
	if policyType == "unknown_policy_type" {
		policyType = ""
	}
	policy := ""
	if policyType != "" {
		if policy, ok = placementEnum(w, "policy_type", policyType, "max_availability", placementPolicyTypes); !ok {
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
		if policy != "" {
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
	emulator.WriteJSON(w, http.StatusOK, p.placementGroupV2View(updated))
}

func (p *Pack) deletePlacementGroupV2(w http.ResponseWriter, r *http.Request) {
	p.deletePlacementGroup(w, r)
}

// placementGroupV2View is the v2alpha1 PlacementGroup shape, field for field
// (instance/v2alpha1/instance_sdk.go): no policy_mode, no policy_respected —
// the alpha dropped both, and the provider reads them through v1 instead.
func (p *Pack) placementGroupV2View(res *resource.Resource) map[string]any {
	return map[string]any{
		"id":          res.ID,
		"project_id":  res.Tenant.Project,
		"name":        res.Attrs["name"],
		"policy_type": res.Attrs["policy_type"],
		"tags":        res.Attrs["tags"],
		"created_at":  res.Created.Format(time.RFC3339),
		"updated_at":  res.Updated.Format(time.RFC3339),
		"zone":        res.Tenant.Zone,
	}
}
