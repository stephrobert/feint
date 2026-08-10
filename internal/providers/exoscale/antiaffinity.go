package exoscale

import (
	"net/http"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Anti-affinity groups, in the measured shape: {description, id, instances,
// name}, where instances is always present — an empty array on a group nobody
// wears — and each member is a bare {id}.
//
// Membership is declared at instance create and read here by scanning the
// instances, so a deleted instance cannot leave a stale member: the group never
// stores the relation, it computes it.

const (
	kindAntiAffinityGroup = "anti-affinity-group"
	nounAntiAffinityGroup = "anti-affinity-group"
)

type createAntiAffinityGroupRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (p *Pack) createAntiAffinityGroup(w http.ResponseWriter, r *http.Request) {
	var req createAntiAffinityGroupRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if strings.ContainsAny(req.Name, "\n\r\x00") {
		writeError(w, http.StatusBadRequest, "name carries control characters")
		return
	}
	now := p.env.Now()
	res := &resource.Resource{
		ID:      p.env.NewID(),
		Kind:    kindAntiAffinityGroup,
		Tenant:  resource.Tenant{Provider: Name},
		State:   "present",
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"name":        req.Name,
			"description": req.Description,
		},
	}
	p.env.Store.Put(res)
	p.writeOperation(w, p.operationReferring(nounAntiAffinityGroup, res.ID))
}

func (p *Pack) listAntiAffinityGroups(w http.ResponseWriter, _ *http.Request) {
	list := p.env.Store.List(kindAntiAffinityGroup, resource.Tenant{Provider: Name})
	sort.Slice(list, func(i, j int) bool { return list[i].Created.Before(list[j].Created) })
	groups := make([]map[string]any, 0, len(list))
	for _, res := range list {
		groups = append(groups, p.antiAffinityGroupView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"anti-affinity-groups": groups})
}

func (p *Pack) getAntiAffinityGroup(w http.ResponseWriter, r *http.Request) {
	res, ok := p.env.Store.Get(Name, kindAntiAffinityGroup, r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.antiAffinityGroupView(res))
}

func (p *Pack) deleteAntiAffinityGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := p.env.Store.Get(Name, kindAntiAffinityGroup, id); !ok {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.env.Store.Delete(Name, kindAntiAffinityGroup, id)
	p.writeOperation(w, p.operationReferring(nounAntiAffinityGroup, id))
}

func (p *Pack) antiAffinityGroupView(res *resource.Resource) map[string]any {
	members := make([]any, 0)
	for _, inst := range p.env.Store.List(kindInstance, resource.Tenant{Provider: Name}) {
		for _, ref := range stringList(inst.Attrs[attrAntiAffinityGroupIDs]) {
			if ref == res.ID {
				members = append(members, map[string]any{"id": inst.ID})
			}
		}
	}
	out := map[string]any{
		"id":        res.ID,
		"name":      res.Attrs["name"],
		"instances": members,
	}
	if description, _ := res.Attrs["description"].(string); description != "" {
		out["description"] = description
	}
	return out
}
