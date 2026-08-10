package exoscale

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Elastic IPs, in the measured shape: {addressfamily, cidr, description, id,
// ip}, description omitted when empty. Attach and detach are actions on the IP
// naming the instance, and the operation they mint refers to the elastic IP,
// not the instance — that is what the recording shows the provider waiting on.
//
// The addresses come from 203.0.113.0/24 (TEST-NET-3): fixed, documented as
// never routable, and disjoint from the machine runtime's own ranges, so an
// elastic IP can never collide with an address a real machine answers on.

const (
	kindElasticIP = "elastic-ip"
	nounElasticIP = "elastic-ip"
)

type createElasticIPRequest struct {
	Description string `json:"description"`
	// Declared because their API does; the emulated pool is inet4 only and says
	// so rather than allocating an inet6 prefix it could not honour.
	AddressFamily string            `json:"addressfamily"`
	Healthcheck   map[string]any    `json:"healthcheck"`
	Labels        map[string]string `json:"labels"`
}

func (p *Pack) createElasticIP(w http.ResponseWriter, r *http.Request) {
	var req createElasticIPRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.AddressFamily == "inet6" {
		writeError(w, http.StatusBadRequest, "this emulator allocates inet4 elastic IPs only")
		return
	}
	ip, ok := p.freeElasticAddress()
	if !ok {
		writeError(w, http.StatusBadRequest, "no elastic IP left in the emulated pool")
		return
	}
	now := p.env.Now()
	res := &resource.Resource{
		ID:      p.env.NewID(),
		Kind:    kindElasticIP,
		Tenant:  resource.Tenant{Provider: Name},
		State:   "present",
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"ip":            ip,
			"addressfamily": "inet4",
			"description":   req.Description,
		},
	}
	if req.Healthcheck != nil {
		res.Attrs["healthcheck"] = req.Healthcheck
	}
	p.env.Store.Put(res)
	p.writeOperation(w, p.operationReferring(nounElasticIP, res.ID))
}

// freeElasticAddress hands out the lowest unused address of the pool. Computed
// from what exists rather than counted, so a deleted IP returns to the pool.
func (p *Pack) freeElasticAddress() (string, bool) {
	used := map[string]bool{}
	for _, res := range p.env.Store.List(kindElasticIP, resource.Tenant{Provider: Name}) {
		if ip, _ := res.Attrs["ip"].(string); ip != "" {
			used[ip] = true
		}
	}
	for host := 1; host <= 254; host++ {
		ip := fmt.Sprintf("203.0.113.%d", host)
		if !used[ip] {
			return ip, true
		}
	}
	return "", false
}

func (p *Pack) listElasticIPs(w http.ResponseWriter, _ *http.Request) {
	list := p.env.Store.List(kindElasticIP, resource.Tenant{Provider: Name})
	sort.Slice(list, func(i, j int) bool { return list[i].Created.Before(list[j].Created) })
	ips := make([]map[string]any, 0, len(list))
	for _, res := range list {
		ips = append(ips, elasticIPView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"elastic-ips": ips})
}

func (p *Pack) getElasticIP(w http.ResponseWriter, r *http.Request) {
	res, ok := p.env.Store.Get(Name, kindElasticIP, r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	emulator.WriteJSON(w, http.StatusOK, elasticIPView(res))
}

type updateElasticIPRequest struct {
	Description *string           `json:"description"`
	Healthcheck map[string]any    `json:"healthcheck"`
	Labels      map[string]string `json:"labels"`
}

func (p *Pack) updateElasticIP(w http.ResponseWriter, r *http.Request) {
	var req updateElasticIPRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id := r.PathValue("id")
	err := p.env.Store.Update(Name, kindElasticIP, id, func(stored *resource.Resource) error {
		if req.Description != nil {
			stored.Attrs["description"] = *req.Description
		}
		if req.Healthcheck != nil {
			stored.Attrs["healthcheck"] = req.Healthcheck
		}
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.writeOperation(w, p.operationReferring(nounElasticIP, id))
}

func (p *Pack) deleteElasticIP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := p.env.Store.Get(Name, kindElasticIP, id); !ok {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	// The address is withdrawn from every instance that publishes it before the
	// IP goes: a view naming a deleted IP would be published by nothing.
	for _, inst := range p.env.Store.List(kindInstance, resource.Tenant{Provider: Name}) {
		ids := stringList(inst.Attrs[attrElasticIPIDs])
		if !contains(ids, id) {
			continue
		}
		_ = p.env.Store.Update(Name, kindInstance, inst.ID, func(stored *resource.Resource) error {
			stored.Attrs[attrElasticIPIDs] = without(stringList(stored.Attrs[attrElasticIPIDs]), id)
			return nil
		})
	}
	p.env.Store.Delete(Name, kindElasticIP, id)
	p.writeOperation(w, p.operationReferring(nounElasticIP, id))
}

func (p *Pack) attachInstanceToElasticIP(w http.ResponseWriter, r *http.Request) {
	p.changeInstanceMembership(w, r, kindElasticIP, nounElasticIP, attrElasticIPIDs, true)
}

func (p *Pack) detachInstanceFromElasticIP(w http.ResponseWriter, r *http.Request) {
	p.changeInstanceMembership(w, r, kindElasticIP, nounElasticIP, attrElasticIPIDs, false)
}

func elasticIPView(res *resource.Resource) map[string]any {
	ip, _ := res.Attrs["ip"].(string)
	out := map[string]any{
		"id":            res.ID,
		"ip":            ip,
		"cidr":          ip + "/32",
		"addressfamily": res.Attrs["addressfamily"],
	}
	if description, _ := res.Attrs["description"].(string); description != "" {
		out["description"] = description
	}
	if healthcheck, ok := res.Attrs["healthcheck"].(map[string]any); ok {
		out["healthcheck"] = healthcheck
	}
	return out
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// without returns the list minus one value, in the []any form attrs store.
func without(list []string, drop string) []any {
	out := make([]any, 0, len(list))
	for _, item := range list {
		if item != drop {
			out = append(out, item)
		}
	}
	return out
}
