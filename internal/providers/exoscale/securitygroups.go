package exoscale

import (
	"net/http"
	"net/netip"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Security groups, in the shape the real API returned them on 2026-08-10.
//
// The measured omission rules matter as much as the fields: `rules` and
// `external-sources` are absent keys when empty, never empty arrays, and every
// group answers `visibility: "private"` — the default one included.
//
// A fresh account already holds one group named "default", and an instance
// created without naming any wears it. The emulator does the same, under a
// fixed identifier so two runs answer the same bytes.

const (
	kindSecurityGroup = "security-group"
	nounSecurityGroup = "security-group"

	// defaultSecurityGroupID is stable across runs: a client that recorded it
	// yesterday must find the same group today, the way it would upstream.
	defaultSecurityGroupID = "00000000-0000-4000-8000-000000000001"
)

// ensureDefaultSecurityGroup seeds the group a fresh account starts with.
// Idempotent, and called by every reader, so no path can observe an account
// without it.
func (p *Pack) ensureDefaultSecurityGroup() {
	if _, ok := p.env.Store.Get(Name, kindSecurityGroup, defaultSecurityGroupID); ok {
		return
	}
	now := p.env.Now()
	p.env.Store.Put(&resource.Resource{
		ID:      defaultSecurityGroupID,
		Kind:    kindSecurityGroup,
		Tenant:  resource.Tenant{Provider: Name},
		State:   "present",
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"name":        "default",
			"description": "Default Security Group",
		},
	})
}

type createSecurityGroupRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (p *Pack) createSecurityGroup(w http.ResponseWriter, r *http.Request) {
	var req createSecurityGroupRequest
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
	p.ensureDefaultSecurityGroup()

	now := p.env.Now()
	res := resource.New(p.env.NewID(), kindSecurityGroup, resource.Tenant{Provider: Name}, "present", now)
	res.Attrs = map[string]any{
		"name":        req.Name,
		"description": req.Description,
	}
	p.env.Store.Put(res)
	p.writeOperation(w, p.operationReferring(nounSecurityGroup, res.ID))
}

// listSecurityGroups answers the organisation's own groups, which their document
// calls the private ones: it declares a visibility filter on this operation,
// enum private|public (.upstream/exoscale-openapi.yaml:12540-12547), and the
// handler used to discard the request, so the filter had no effect — the same
// defect as list-templates' (#271).
//
// The default is private, and that is measured rather than chosen: `exo compute
// security-group list` documents private as its default and sends **no**
// parameter for it (exo 1.95.1 through a recording proxy), so the paramless
// answer must be the private one. public names the groups Exoscale itself
// publishes, and this emulator publishes none — the empty list is the honest
// answer, the way listDeployTargets already answers for a resource an emulated
// account does not hold.
//
// TestSecurityGroupVisibilityIsHonoured fails without the filter.
func (p *Pack) listSecurityGroups(w http.ResponseWriter, r *http.Request) {
	groups := make([]map[string]any, 0)
	switch r.URL.Query().Get("visibility") {
	case "", "private":
		p.ensureDefaultSecurityGroup()
		list := p.env.Store.List(kindSecurityGroup, resource.Tenant{Provider: Name})
		sort.Slice(list, func(i, j int) bool { return list[i].Created.Before(list[j].Created) })
		for _, res := range list {
			groups = append(groups, securityGroupView(res))
		}
	case "public":
		// None to list: see above.
	default:
		writeError(w, http.StatusBadRequest, "visibility must be private or public")
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"security-groups": groups})
}

func (p *Pack) getSecurityGroup(w http.ResponseWriter, r *http.Request) {
	p.ensureDefaultSecurityGroup()
	res, ok := p.env.Store.Get(Name, kindSecurityGroup, r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	emulator.WriteJSON(w, http.StatusOK, securityGroupView(res))
}

func (p *Pack) deleteSecurityGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == defaultSecurityGroupID {
		writeError(w, http.StatusBadRequest, "the default security group cannot be deleted")
		return
	}
	if _, ok := p.env.Store.Get(Name, kindSecurityGroup, id); !ok {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	// A group an instance still wears does not go: the real API refuses, and an
	// emulator that let it vanish would leave every wearer naming a ghost.
	for _, inst := range p.env.Store.List(kindInstance, resource.Tenant{Provider: Name}) {
		for _, ref := range stringList(inst.Attrs[attrSecurityGroupIDs]) {
			if ref == id {
				writeError(w, http.StatusBadRequest, "the security group is still used by at least one instance")
				return
			}
		}
	}
	p.env.Store.Delete(Name, kindSecurityGroup, id)
	p.writeOperation(w, p.operationReferring(nounSecurityGroup, id))
}

// addRuleToSecurityGroup reads the fields the CLI was measured sending:
// {description, end-port, flow-direction, network, protocol, start-port} for a
// CIDR rule, plus the group forms their API declares beside it.
type addRuleRequest struct {
	Description   string `json:"description"`
	FlowDirection string `json:"flow-direction"`
	Protocol      string `json:"protocol"`
	Network       string `json:"network"`
	StartPort     int    `json:"start-port"`
	EndPort       int    `json:"end-port"`
	ICMP          *struct {
		Code int `json:"code"`
		Type int `json:"type"`
	} `json:"icmp"`
	SecurityGroup *struct {
		ID string `json:"id"`
	} `json:"security-group"`
}

func (p *Pack) addRuleToSecurityGroup(w http.ResponseWriter, r *http.Request) {
	var req addRuleRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.FlowDirection == "" {
		writeError(w, http.StatusBadRequest, "flow-direction is required")
		return
	}
	id := r.PathValue("id")
	rule := map[string]any{
		"id":             p.env.NewID(),
		"flow-direction": req.FlowDirection,
		"protocol":       orDefault(req.Protocol, "tcp"),
	}
	if req.Description != "" {
		rule["description"] = req.Description
	}
	if req.Network != "" {
		rule["network"] = req.Network
	}
	if req.StartPort > 0 {
		rule["start-port"] = req.StartPort
		rule["end-port"] = req.EndPort
	}
	if req.ICMP != nil {
		rule["icmp"] = map[string]any{"code": req.ICMP.Code, "type": req.ICMP.Type}
	}
	if req.SecurityGroup != nil {
		rule["security-group"] = map[string]any{"id": req.SecurityGroup.ID}
	}
	err := p.env.Store.Update(Name, kindSecurityGroup, id, func(stored *resource.Resource) error {
		rules, _ := stored.Attrs["rules"].([]any)
		stored.Attrs["rules"] = append(rules, rule)
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.writeOperation(w, p.operationReferring(nounSecurityGroup, id))
}

func (p *Pack) deleteRuleFromSecurityGroup(w http.ResponseWriter, r *http.Request) {
	id, ruleID := r.PathValue("id"), r.PathValue("rule")
	found := false
	err := p.env.Store.Update(Name, kindSecurityGroup, id, func(stored *resource.Resource) error {
		rules, _ := stored.Attrs["rules"].([]any)
		kept := make([]any, 0, len(rules))
		for _, raw := range rules {
			rule, _ := raw.(map[string]any)
			if rule["id"] == ruleID {
				found = true
				continue
			}
			kept = append(kept, raw)
		}
		if len(kept) == 0 {
			delete(stored.Attrs, "rules")
		} else {
			stored.Attrs["rules"] = kept
		}
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil || !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.writeOperation(w, p.operationReferring(nounSecurityGroup, id))
}

// instanceTarget is the body of every ":attach"/":detach" action measured: the
// instance as an object, of which the identifier is the part that names it (the
// CLI also sends a zero created-at, which names nothing).
type instanceTarget struct {
	Instance struct {
		ID string `json:"id"`
		// The CLI serialises its whole instance object, so a zero created-at
		// rides along on every attach and detach. Declared so the unread-field
		// report sees it read; a client-side zero timestamp is not an
		// instruction, so reading it is the whole of honouring it.
		CreatedAt string `json:"created-at"`
	} `json:"instance"`
}

func (p *Pack) attachInstanceToSecurityGroup(w http.ResponseWriter, r *http.Request) {
	p.changeInstanceMembership(w, r, kindSecurityGroup, nounSecurityGroup, attrSecurityGroupIDs, true)
}

func (p *Pack) detachInstanceFromSecurityGroup(w http.ResponseWriter, r *http.Request) {
	p.changeInstanceMembership(w, r, kindSecurityGroup, nounSecurityGroup, attrSecurityGroupIDs, false)
}

// changeInstanceMembership adds or removes one group-like resource on one
// instance. Shared by security groups and elastic IPs, whose attach routes are
// the same measured shape with different nouns. It reports the instance it
// touched, so a caller with a side effect to apply — routing an elastic IP —
// knows the membership really changed first.
func (p *Pack) changeInstanceMembership(w http.ResponseWriter, r *http.Request, kind, noun, attr string, add bool) (instanceID string, ok bool) {
	var req instanceTarget
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return "", false
	}
	if req.Instance.ID == "" {
		writeError(w, http.StatusBadRequest, "instance is required")
		return "", false
	}
	id := r.PathValue("id")
	if _, found := p.env.Store.Get(Name, kind, id); !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return "", false
	}
	err := p.env.Store.Update(Name, kindInstance, req.Instance.ID, func(stored *resource.Resource) error {
		ids := stringList(stored.Attrs[attr])
		kept := make([]any, 0, len(ids)+1)
		for _, existing := range ids {
			if existing != id {
				kept = append(kept, existing)
			}
		}
		if add {
			kept = append(kept, id)
		}
		stored.Attrs[attr] = kept
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return "", false
	}
	p.writeOperation(w, p.operationReferring(noun, id))
	return req.Instance.ID, true
}

// externalSourceRequest is the measured body of the ":add-source" and
// ":remove-source" actions — `{"cidr":"203.0.113.0/24"}`, traced from
// `exo compute security-group source add` on 2026-08-14.
type externalSourceRequest struct {
	CIDR string `json:"cidr"`
}

func (p *Pack) addExternalSourceToSecurityGroup(w http.ResponseWriter, r *http.Request) {
	p.changeExternalSources(w, r, true)
}

func (p *Pack) removeExternalSourceFromSecurityGroup(w http.ResponseWriter, r *http.Request) {
	p.changeExternalSources(w, r, false)
}

// changeExternalSources adds or removes one CIDR on one group. The value is
// parsed rather than stored blindly: an external source is a block a firewall
// is meant to match, and a stored string no parser accepts is an addressing
// plan that means nothing. TestAnExternalSourceMustBeACIDR fails without the
// check.
func (p *Pack) changeExternalSources(w http.ResponseWriter, r *http.Request, add bool) {
	var req externalSourceRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := netip.ParsePrefix(req.CIDR); err != nil {
		writeError(w, http.StatusBadRequest, "cidr must be an IP block in CIDR form")
		return
	}
	p.ensureDefaultSecurityGroup()
	id := r.PathValue("id")
	err := p.env.Store.Update(Name, kindSecurityGroup, id, func(stored *resource.Resource) error {
		sources := stringList(stored.Attrs["external-sources"])
		kept := make([]any, 0, len(sources)+1)
		for _, existing := range sources {
			if existing != req.CIDR {
				kept = append(kept, existing)
			}
		}
		if add {
			kept = append(kept, req.CIDR)
		}
		if len(kept) == 0 {
			delete(stored.Attrs, "external-sources")
		} else {
			stored.Attrs["external-sources"] = kept
		}
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.writeOperation(w, p.operationReferring(nounSecurityGroup, id))
}

// securityGroupView is the measured shape: rules and external-sources are
// omitted keys when empty.
//
// The live API also answers `visibility: "private"` on every group, and their
// published schema does not declare the field — the same live-ahead-of-spec gap
// as the operation's resource field. This emulator validates its responses
// against the published contract, so the field stays off the wire; the CLI
// prints groups without it.
func securityGroupView(res *resource.Resource) map[string]any {
	out := map[string]any{
		"id":   res.ID,
		"name": res.Attrs["name"],
	}
	if description, _ := res.Attrs["description"].(string); description != "" {
		out["description"] = description
	}
	if rules, _ := res.Attrs["rules"].([]any); len(rules) > 0 {
		out["rules"] = rules
	}
	if sources, _ := res.Attrs["external-sources"].([]any); len(sources) > 0 {
		out["external-sources"] = sources
	}
	return out
}

// stringList reads a stored []any of strings back as strings, tolerating the
// []string a fresh write may hold and the []any a snapshot restore produces.
func stringList(v any) []string {
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
	}
	return nil
}
