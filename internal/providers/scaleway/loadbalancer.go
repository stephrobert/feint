package scaleway

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/network"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Load Balancers (lb/v1, ZonedAPI), served exactly as far as the measured
// clients exercise them (#282, #17).
//
// The demand is two of the five surveyed Scaleway stacks (#262): kubic creates
// a `scaleway_lb_ip` for its ingress, sergelogvinov/terraform-talos builds the
// whole chain — two IPs (v4 and v6), the balancer on a Private Network, three
// backends with HTTP/HTTPS health checks, three frontends carrying inline
// ACLs. Scaleway's own LB module adds routes. What the provider calls was read
// in its services/lb (v2.43.0, the talos pin, and v2.81.0 agree on the set):
// CreateIP/GetIP/UpdateIP/ReleaseIP, CreateLB + WaitForLb polling GetLB,
// AttachPrivateNetwork + WaitForLBPN polling ListLBPrivateNetworks,
// CreateBackend/GetBackend/UpdateBackend/SetBackendServers/UpdateHealthCheck/
// DeleteBackend, CreateFrontend/GetFrontend/UpdateFrontend/DeleteFrontend,
// CreateACL/ListACLs/GetACL/UpdateACL/DeleteACL, CreateRoute/GetRoute/
// UpdateRoute/DeleteRoute, DeleteLB, DetachPrivateNetwork — then confirmed on
// the wire with `feint proxy --record`. `scw lb` drives the same families and
// adds the lists.
//
// What this control plane does NOT do, stated rather than implied: no packet
// is forwarded, no backend is probed, no TLS terminates. The balancer records
// the configuration a client asked for and answers it back exactly. That is
// the decision #315 measured for the Outscale LBU (the OVN primitive balances
// for in-network clients but its host-side VIP goes dark in minutes, and the
// runtime exposes no backend health), and the Scaleway case does not differ:
// the same runtime would carry it. GetLBStats and ListBackendStats stay
// declined for exactly that reason — a backend reported UP that nothing
// checked is the lie this project exists to refuse. docs/limits.md carries
// the statement.

const (
	kindLBIP         = "lb/ip"
	kindLB           = "lb/lb"
	kindLBBackend    = "lb/backend"
	kindLBFrontend   = "lb/frontend"
	kindLBACL        = "lb/acl"
	kindLBRoute      = "lb/route"
	kindLBAttachment = "lb/private-network"
)

// lbBlock is where emulated balancer addresses come from: TEST-NET-2, distinct
// from the instance flexible block (203.0.113.0/24) and the gateway block
// (192.0.2.0/24) so no product ever publishes an address another one claims.
// lbV6Block is the IPv6 documentation prefix's slice for the same job: talos
// creates a `scaleway_lb_ip` with is_ipv6 = true before its balancer.
const (
	lbBlock   = "198.51.100.0/24"
	lbV6Block = "2001:db8:0:1::/64"
)

type createLBIPRequest struct {
	OrganizationID *string  `json:"organization_id"`
	ProjectID      *string  `json:"project_id"`
	Reverse        *string  `json:"reverse"`
	IsIPv6         bool     `json:"is_ipv6"`
	Tags           []string `json:"tags"`
}

type updateLBIPRequest struct {
	Reverse *string   `json:"reverse"`
	LBID    *string   `json:"lb_id"`
	Tags    *[]string `json:"tags"`
}

type createLBRequest struct {
	OrganizationID        *string  `json:"organization_id"`
	ProjectID             *string  `json:"project_id"`
	Name                  string   `json:"name"`
	Description           string   `json:"description"`
	IPID                  *string  `json:"ip_id"`
	AssignFlexibleIP      *bool    `json:"assign_flexible_ip"`
	AssignFlexibleIPv6    *bool    `json:"assign_flexible_ipv6"`
	IPIDs                 []string `json:"ip_ids"`
	Tags                  []string `json:"tags"`
	Type                  string   `json:"type"`
	SslCompatibilityLevel string   `json:"ssl_compatibility_level"`
}

type updateLBRequest struct {
	Name                  string   `json:"name"`
	Description           string   `json:"description"`
	Tags                  []string `json:"tags"`
	SslCompatibilityLevel string   `json:"ssl_compatibility_level"`
}

// ---- IPs --------------------------------------------------------------------

func (p *Pack) listLBIPs(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	all := p.env.Store.List(kindLBIP, p.zoneProjectScopeOf(r, zone))
	q := r.URL.Query()
	if address := q.Get("ip_address"); address != "" {
		all = filterResources(all, func(res *resource.Resource) bool {
			return strings.Contains(textOf(res.Attrs["ip_address"]), address)
		})
	}
	if tags := csvValues(q, "tags"); len(tags) > 0 {
		all = filterResources(all, func(res *resource.Resource) bool {
			return hasAllTags(res, tags)
		})
	}
	if kind := q.Get("ip_type"); kind != "" && kind != "all" {
		wantV6 := kind == "ipv6"
		all = filterResources(all, func(res *resource.Resource) bool {
			isV6, _ := res.Attrs["is_ipv6"].(bool)
			return isV6 == wantV6
		})
	}
	page := parsePage(r)
	start, end := page.slice(len(all))
	ips := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		ips = append(ips, p.lbIPView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"ips":         ips,
		"total_count": len(all),
	})
}

func (p *Pack) createLBIP(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	var req createLBIPRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	requested := ""
	if req.ProjectID != nil {
		requested = *req.ProjectID
	}
	project, _ := projectOf(requested)

	unlock := p.lockAddresses()
	defer unlock()
	res, err := p.mintLBIP(zone, project, req.IsIPv6, orEmpty(req.Tags))
	if err != nil {
		writePrecondition(w, "ip", "", err.Error())
		return
	}
	if req.Reverse != nil && *req.Reverse != "" {
		res.Attrs["reverse"] = *req.Reverse
	}
	p.env.Store.Put(res)
	emulator.WriteJSON(w, http.StatusOK, p.lbIPView(res))
}

// mintLBIP allocates an address of the balancer blocks and builds the
// resource. The caller holds the allocation lock and stores the result.
func (p *Pack) mintLBIP(zone, project string, isIPv6 bool, tags []any) (*resource.Resource, error) {
	block := lbBlock
	if isIPv6 {
		block = lbV6Block
	}
	prefix, err := network.ParseCIDR(block)
	if err != nil {
		return nil, err
	}
	taken := make(map[netip.Addr]bool, 8)
	for _, res := range p.env.Store.List(kindLBIP, resource.Tenant{Provider: Name}) {
		if addr, err := netip.ParseAddr(textOf(res.Attrs["ip_address"])); err == nil {
			taken[addr] = true
		}
	}
	// A plain walk of the block rather than core/network's Allocator, which is
	// IPv4-only: the v6 block is walked the same way, and determinism is what
	// matters (anything Terraform stores must read back identically).
	addr := prefix.Masked().Addr().Next()
	for prefix.Contains(addr) {
		if !taken[addr] {
			break
		}
		addr = addr.Next()
	}
	if !prefix.Contains(addr) {
		return nil, fmt.Errorf("the emulated block %s is exhausted; release an IP first", block)
	}
	now := p.env.Now()
	res := resource.New(p.env.NewID(), kindLBIP, resource.Tenant{Provider: Name, Project: project, Zone: zone}, "detached", now)
	res.Attrs = map[string]any{
		"ip_address": addr.String(),
		"project_id": project,
		"is_ipv6":    isIPv6,
		"tags":       tags,
		// Empty until someone measures the real reverse a fresh LB IP carries;
		// the field is a plain string in the SDK, and the provider treats it
		// as computed.
		"reverse": "",
		"lb_id":   "",
	}
	return res, nil
}

func (p *Pack) getLBIP(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindLBIP, "ipID", "ip")
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.lbIPView(res))
}

func (p *Pack) updateLBIP(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindLBIP, "ipID", "ip")
	if !ok {
		return
	}
	var req updateLBIPRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.LBID != nil && *req.LBID != "" {
		if _, found := p.env.Store.Get(Name, kindLB, *req.LBID); !found {
			writeNotFound(w, "lb", *req.LBID)
			return
		}
	}
	err := p.env.Store.Update(Name, kindLBIP, res.ID, func(stored *resource.Resource) error {
		if req.Reverse != nil {
			stored.Attrs["reverse"] = *req.Reverse
		}
		if req.LBID != nil {
			stored.Attrs["lb_id"] = *req.LBID
		}
		if req.Tags != nil {
			stored.Attrs["tags"] = orEmpty(*req.Tags)
		}
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeNotFound(w, "ip", res.ID)
		return
	}
	current, _ := p.env.Store.Get(Name, kindLBIP, res.ID)
	emulator.WriteJSON(w, http.StatusOK, p.lbIPView(current))
}

func (p *Pack) releaseLBIP(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindLBIP, "ipID", "ip")
	if !ok {
		return
	}
	if lbID, _ := res.Attrs["lb_id"].(string); lbID != "" {
		writePrecondition(w, "ip", res.ID, "IP is attached to a load balancer; delete the load balancer first")
		return
	}
	p.env.Store.Delete(Name, kindLBIP, res.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (p *Pack) lbIPView(res *resource.Resource) map[string]any {
	var lbID any
	if id, _ := res.Attrs["lb_id"].(string); id != "" {
		lbID = id
	}
	return map[string]any{
		"id":              res.ID,
		"ip_address":      res.Attrs["ip_address"],
		"organization_id": defaultOrganization,
		"project_id":      res.Attrs["project_id"],
		"lb_id":           lbID,
		"reverse":         res.Attrs["reverse"],
		"tags":            res.Attrs["tags"],
		"region":          regionOfZone(res.Tenant.Zone),
		"zone":            res.Tenant.Zone,
	}
}

// ---- Load balancers -----------------------------------------------------------

func (p *Pack) listLBs(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	all := p.env.Store.List(kindLB, p.zoneProjectScopeOf(r, zone))
	q := r.URL.Query()
	if name := q.Get("name"); name != "" {
		all = filterResources(all, func(res *resource.Resource) bool {
			return strings.Contains(textOf(res.Attrs["name"]), name)
		})
	}
	if tags := csvValues(q, "tags"); len(tags) > 0 {
		all = filterResources(all, func(res *resource.Resource) bool {
			return hasAllTags(res, tags)
		})
	}
	if ids := idSet(q, "lb_ids"); ids != nil {
		all = filterResources(all, func(res *resource.Resource) bool {
			return ids[res.ID]
		})
	}
	if !orderResources(w, r, "order_by", "created_at_asc", map[string]resourceCmp{
		"created_at": cmpCreated,
		"name":       cmpName,
	}, all) {
		return
	}
	page := parsePage(r)
	start, end := page.slice(len(all))
	lbs := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		lbs = append(lbs, p.lbView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"lbs":         lbs,
		"total_count": len(all),
	})
}

func (p *Pack) createLB(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	var req createLBRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.Type == "" {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "type", Reason: "required"})
		return
	}
	requested := ""
	if req.ProjectID != nil {
		requested = *req.ProjectID
	}
	project, _ := projectOf(requested)

	// Addresses and the balancer in one critical section, like every
	// allocation in this pack: the attach below must not race a concurrent
	// create over the same flexible IP.
	unlock := p.lockAddresses()
	defer unlock()

	id := p.env.NewID()
	ipIDs := make([]string, 0, 2)
	seen := map[string]bool{}
	requestedIPs := append([]string{}, req.IPIDs...)
	if req.IPID != nil {
		requestedIPs = append(requestedIPs, *req.IPID)
	}
	for _, ipID := range requestedIPs {
		if ipID == "" || seen[ipID] {
			continue
		}
		seen[ipID] = true
		ip, found := p.env.Store.Get(Name, kindLBIP, ipID)
		if !found || ip.Tenant.Zone != zone {
			writeNotFound(w, "ip", ipID)
			return
		}
		if held, _ := ip.Attrs["lb_id"].(string); held != "" {
			writePrecondition(w, "ip", ip.ID, "IP is already attached to a load balancer")
			return
		}
		ipIDs = append(ipIDs, ipID)
	}
	// "Default value is `true` (assign)" — but only when the request names no
	// address at all, which is how the provider behaves: ip_ids conflicts
	// with assign_flexible_ip in its own schema.
	if len(ipIDs) == 0 && (req.AssignFlexibleIP == nil || *req.AssignFlexibleIP) {
		minted, err := p.mintLBIP(zone, project, false, []any{})
		if err != nil {
			writePrecondition(w, "ip", "", err.Error())
			return
		}
		p.env.Store.Put(minted)
		ipIDs = append(ipIDs, minted.ID)
	}
	if req.AssignFlexibleIPv6 != nil && *req.AssignFlexibleIPv6 {
		minted, err := p.mintLBIP(zone, project, true, []any{})
		if err != nil {
			writePrecondition(w, "ip", "", err.Error())
			return
		}
		p.env.Store.Put(minted)
		ipIDs = append(ipIDs, minted.ID)
	}

	now := p.env.Now()
	res := resource.New(id, kindLB, resource.Tenant{Provider: Name, Project: project, Zone: zone}, "ready", now)
	res.Attrs = map[string]any{
		"name":        orDefault(req.Name, "lb-"+id[:8]),
		"description": req.Description,
		// Lowercase on purpose: "For now API return lowercase lb type", the
		// provider's own comment in services/lb/lb.go, which upper-cases on
		// read. Echoing the request's case would be the invented format.
		"type":       strings.ToLower(req.Type),
		"tags":       orEmpty(req.Tags),
		"project_id": project,
		"ip_ids":     ipIDs,
		"ssl_compatibility_level": orDefault(req.SslCompatibilityLevel,
			"ssl_compatibility_level_intermediate"),
		// The node this balancer publishes as its own. Minted here rather than
		// rendered on the fly so that it is stable across reads: anything
		// Terraform stores has to read back identically.
		"instance_id": p.env.NewID(),
	}
	p.env.Store.Put(res)
	for _, ipID := range ipIDs {
		_ = p.env.Store.Update(Name, kindLBIP, ipID, func(stored *resource.Resource) error {
			stored.Attrs["lb_id"] = id
			stored.Updated = now
			return nil
		})
	}

	current, _ := p.env.Store.Get(Name, kindLB, id)
	emulator.WriteJSON(w, http.StatusOK, p.lbView(current))
}

func (p *Pack) getLB(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindLB, "lbID", "lb")
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.lbView(res))
}

func (p *Pack) updateLB(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindLB, "lbID", "lb")
	if !ok {
		return
	}
	var req updateLBRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	err := p.env.Store.Update(Name, kindLB, res.ID, func(stored *resource.Resource) error {
		stored.Attrs["name"] = orDefault(req.Name, textOf(stored.Attrs["name"]))
		stored.Attrs["description"] = req.Description
		stored.Attrs["tags"] = orEmpty(req.Tags)
		if req.SslCompatibilityLevel != "" {
			stored.Attrs["ssl_compatibility_level"] = req.SslCompatibilityLevel
		}
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeNotFound(w, "lb", res.ID)
		return
	}
	current, _ := p.env.Store.Get(Name, kindLB, res.ID)
	emulator.WriteJSON(w, http.StatusOK, p.lbView(current))
}

func (p *Pack) deleteLB(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindLB, "lbID", "lb")
	if !ok {
		return
	}
	// Deleting the balancer deletes what belongs to it, as upstream does: the
	// frontends, their ACLs and routes, the backends, and the Private Network
	// attachments with the addresses they held. Terraform usually destroys
	// children first, so most of these walks find nothing; a bare
	// `scw lb lb delete` is the client that needs them.
	for _, fe := range p.env.Store.List(kindLBFrontend, resource.Tenant{Provider: Name}) {
		if fe.Attrs["lb_id"] != res.ID {
			continue
		}
		p.deleteFrontendCascade(fe.ID)
	}
	for _, be := range p.env.Store.List(kindLBBackend, resource.Tenant{Provider: Name}) {
		if be.Attrs["lb_id"] == res.ID {
			p.env.Store.Delete(Name, kindLBBackend, be.ID)
		}
	}
	for _, at := range p.env.Store.List(kindLBAttachment, resource.Tenant{Provider: Name}) {
		if at.Attrs["lb_id"] == res.ID {
			p.env.Store.Delete(Name, kindLBAttachment, at.ID)
			p.releaseHeldIPAMIPs(at.ID)
		}
	}
	release := r.URL.Query().Get("release_ip") == "true"
	for _, ipID := range stringsOf(res.Attrs["ip_ids"]) {
		if release {
			p.env.Store.Delete(Name, kindLBIP, ipID)
			continue
		}
		_ = p.env.Store.Update(Name, kindLBIP, ipID, func(stored *resource.Resource) error {
			stored.Attrs["lb_id"] = ""
			stored.Updated = p.env.Now()
			return nil
		})
	}
	p.env.Store.Delete(Name, kindLB, res.ID)
	w.WriteHeader(http.StatusNoContent)
}

// stringsOf reads a stored []string that may have travelled through a JSON
// snapshot and come back as []any.
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
	}
	return nil
}

func (p *Pack) countLBChildren(id string, kind, key string) int {
	count := 0
	for _, res := range p.env.Store.List(kind, resource.Tenant{Provider: Name}) {
		if res.Attrs[key] == id {
			count++
		}
	}
	return count
}

// lbInstancesView publishes the one node a balancer of this size runs on.
//
// It used to be an empty array, on the argument that "publishing invented
// instance IDs would claim machines that do not exist". The recording reversed
// the premise rather than the argument: corpus/scaleway/scw-billed-shapes.jsonl
// seq 7 is a real fr-par balancer, and the one instance it answers carries
// `ip_address: ""`. So the field a client could act on is empty upstream too,
// and what the emulator was withholding was not an address but the shape — a
// balancer that answers no node at all is the divergence, since 17 findings of
// `feint corpus --check` were exactly that one empty array seen through eleven
// operations (a backend nests a balancer, a frontend nests a backend).
//
// The status is "ready" and never "pending": lb_utils.go's waitForLbInstances
// blocks until every instance reaches a terminal status, and this emulator's
// lifecycle transitions are immediate (docs/limits.md).
//
// TestABalancerPublishesTheNodeItRunsOn fails without this.
func (p *Pack) lbInstancesView(res *resource.Resource) []any {
	id := textOf(res.Attrs["instance_id"])
	if id == "" {
		return []any{}
	}
	return []any{map[string]any{
		"id":     id,
		"status": "ready",
		// Empty upstream as well, on the recorded balancer: the node's address
		// is not the balancer's, and no client dials it.
		"ip_address": "",
		"created_at": res.Created.Format(time.RFC3339),
		"updated_at": res.Updated.Format(time.RFC3339),
		"region":     regionOfZone(res.Tenant.Zone),
		"zone":       res.Tenant.Zone,
	}}
}

func (p *Pack) lbView(res *resource.Resource) map[string]any {
	ips := make([]map[string]any, 0, 2)
	for _, ipID := range stringsOf(res.Attrs["ip_ids"]) {
		if ip, found := p.env.Store.Get(Name, kindLBIP, ipID); found {
			ips = append(ips, p.lbIPView(ip))
		}
	}
	routeCount := 0
	for _, route := range p.env.Store.List(kindLBRoute, resource.Tenant{Provider: Name}) {
		if fe, found := p.env.Store.Get(Name, kindLBFrontend, textOf(route.Attrs["frontend_id"])); found && fe.Attrs["lb_id"] == res.ID {
			routeCount++
		}
	}
	return map[string]any{
		"id":                      res.ID,
		"name":                    res.Attrs["name"],
		"description":             res.Attrs["description"],
		"status":                  res.State,
		"instances":               p.lbInstancesView(res),
		"organization_id":         defaultOrganization,
		"project_id":              res.Attrs["project_id"],
		"ip":                      ips,
		"tags":                    res.Attrs["tags"],
		"frontend_count":          p.countLBChildren(res.ID, kindLBFrontend, "lb_id"),
		"backend_count":           p.countLBChildren(res.ID, kindLBBackend, "lb_id"),
		"type":                    res.Attrs["type"],
		"subscriber":              nil,
		"ssl_compatibility_level": res.Attrs["ssl_compatibility_level"],
		"created_at":              res.Created.Format(time.RFC3339),
		"updated_at":              res.Updated.Format(time.RFC3339),
		"private_network_count":   p.countLBChildren(res.ID, kindLBAttachment, "lb_id"),
		"route_count":             routeCount,
		"region":                  regionOfZone(res.Tenant.Zone),
		"zone":                    res.Tenant.Zone,
	}
}

// ---- Private Network attachments ------------------------------------------------

type attachLBPrivateNetworkRequest struct {
	PrivateNetworkID string         `json:"private_network_id"`
	StaticConfig     map[string]any `json:"static_config"`
	DHCPConfig       map[string]any `json:"dhcp_config"`
	IpamConfig       map[string]any `json:"ipam_config"`
	IpamIDs          []string       `json:"ipam_ids"`
}

type detachLBPrivateNetworkRequest struct {
	PrivateNetworkID string `json:"private_network_id"`
}

func (p *Pack) attachLBPrivateNetworkAt(w http.ResponseWriter, r *http.Request, pathPN string) {
	lb, ok := p.zonalResourceOf(w, r, kindLB, "lbID", "lb")
	if !ok {
		return
	}
	var req attachLBPrivateNetworkRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if pathPN != "" {
		// The legacy spelling names the network in the path.
		req.PrivateNetworkID = pathPN
	}
	pn, found := p.env.Store.Get(Name, kindPrivateNetwork, req.PrivateNetworkID)
	if !found {
		writeNotFound(w, "private_network", req.PrivateNetworkID)
		return
	}
	if req.StaticConfig != nil {
		// Deprecated upstream and sent by no measured client: talos and the
		// LB module both attach with ipam_ids or nothing. Refusing beats
		// recording a static address the IPAM half would then contradict.
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "static_config",
			Reason:       "constraint",
			HelpMessage:  "static_config is deprecated upstream and not served here; attach with ipam_ids or let the emulator book an address",
		})
		return
	}

	unlock := p.lockAddresses()
	defer unlock()

	// Attaching twice is upstream's idempotent no-op: the provider retries
	// the call after a wait, and answering a conflict would fail the retry.
	for _, at := range p.env.Store.List(kindLBAttachment, resource.Tenant{Provider: Name}) {
		if at.Attrs["lb_id"] == lb.ID && at.Attrs["private_network_id"] == pn.ID {
			emulator.WriteJSON(w, http.StatusOK, p.lbAttachmentView(at))
			return
		}
	}

	id := p.env.NewID()
	project, _ := pn.Attrs["project_id"].(string)
	lbName := textOf(lb.Attrs["name"])
	ipamIDs := make([]string, 0, 1)
	if len(req.IpamIDs) > 0 {
		for _, ipamID := range req.IpamIDs {
			booked, found := p.env.Store.Get(Name, kindIPAMIP, ipamID)
			if !found {
				writeNotFound(w, "ip", ipamID)
				return
			}
			if booked.Attrs["private_network_id"] != pn.ID {
				writeInvalidArguments(w, ArgumentError{
					ArgumentName: "ipam_ids",
					Reason:       "constraint",
					HelpMessage:  "the booked address does not live in the private network being attached",
				})
				return
			}
			addr, err := netip.ParsePrefix(textOf(booked.Attrs["address"]))
			if err != nil {
				writeInvalidArguments(w, ArgumentError{ArgumentName: "ipam_ids", Reason: "constraint", HelpMessage: err.Error()})
				return
			}
			if err := p.holdIPAMIP(booked.ID, resourceTypeLBServer, lb.ID, lbName, macAddressOf(addr.Addr())); err != nil {
				writePrecondition(w, "ip", booked.ID, "IP is already attached to a resource")
				return
			}
			ipamIDs = append(ipamIDs, booked.ID)
		}
	} else {
		// "When null, a new private IP address is created for the Load
		// Balancer on this Private Network": same pool, same allocator, same
		// lock as the NICs and the booked addresses.
		alloc, err := p.allocatorFor(pn)
		if err != nil {
			writeInvalidArguments(w, ArgumentError{ArgumentName: "private_network_id", Reason: "constraint", HelpMessage: err.Error()})
			return
		}
		addr, err := alloc.Allocate()
		if err != nil {
			writePrecondition(w, "private_network", pn.ID, "no address left in "+alloc.Prefix().String())
			return
		}
		prefix := netip.PrefixFrom(addr, alloc.Prefix().Bits())
		held := p.newHeldIPAMIP(regionOfZone(lb.Tenant.Zone), project, prefix, pn, resourceTypeLBServer, lb.ID, lbName, macAddressOf(addr))
		// Runtime carries the attachment, so a detach releases exactly what
		// this attach created and nothing a client booked.
		held.Runtime = map[string]string{"lb_attachment": id}
		p.env.Store.Put(held)
		ipamIDs = append(ipamIDs, held.ID)
	}

	now := p.env.Now()
	res := resource.New(id, kindLBAttachment, resource.Tenant{Provider: Name, Project: project, Zone: lb.Tenant.Zone}, "ready", now)
	res.Attrs = map[string]any{
		"lb_id":              lb.ID,
		"private_network_id": pn.ID,
		"ipam_ids":           ipamIDs,
	}
	if req.DHCPConfig != nil {
		res.Attrs["dhcp_config"] = req.DHCPConfig
	}
	p.env.Store.Put(res)

	emulator.WriteJSON(w, http.StatusOK, p.lbAttachmentView(res))
}

// attachLBPrivateNetworkLegacy serves the spelling SDK generations up to
// v1.0.0-beta.29 emit, where the Private Network rides in the path instead of
// the body. Measured on the wire: terraform-provider-scaleway v2.43.0 — the
// pin of the surveyed terraform-talos stack — sends
// POST /lbs/{lbID}/private-networks/{pnID}/attach with {"ipam_ids": []}, and
// production accepts it today. The body is decoded by the same struct; only
// the network's origin differs.
func (p *Pack) attachLBPrivateNetworkLegacy(w http.ResponseWriter, r *http.Request) {
	p.attachLBPrivateNetworkAt(w, r, r.PathValue("pnID"))
}

func (p *Pack) attachLBPrivateNetwork(w http.ResponseWriter, r *http.Request) {
	p.attachLBPrivateNetworkAt(w, r, "")
}

// detachLBPrivateNetworkLegacy is the same vintage's detach.
func (p *Pack) detachLBPrivateNetworkLegacy(w http.ResponseWriter, r *http.Request) {
	lb, ok := p.zonalResourceOf(w, r, kindLB, "lbID", "lb")
	if !ok {
		return
	}
	p.detachLBPrivateNetworkOf(w, lb, r.PathValue("pnID"))
}

func (p *Pack) detachLBPrivateNetwork(w http.ResponseWriter, r *http.Request) {
	lb, ok := p.zonalResourceOf(w, r, kindLB, "lbID", "lb")
	if !ok {
		return
	}
	var req detachLBPrivateNetworkRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	p.detachLBPrivateNetworkOf(w, lb, req.PrivateNetworkID)
}

func (p *Pack) detachLBPrivateNetworkOf(w http.ResponseWriter, lb *resource.Resource, privateNetworkID string) {
	for _, at := range p.env.Store.List(kindLBAttachment, resource.Tenant{Provider: Name}) {
		if at.Attrs["lb_id"] != lb.ID || at.Attrs["private_network_id"] != privateNetworkID {
			continue
		}
		p.env.Store.Delete(Name, kindLBAttachment, at.ID)
		// The held addresses go back where they came from: the ones this
		// attach created disappear, the ones the client booked survive,
		// unheld — the split every holder in this pack applies.
		p.releaseAttachmentIPs(lb.ID, at.ID, stringsOf(at.Attrs["ipam_ids"]))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeNotFound(w, "private_network", privateNetworkID)
}

// releaseAttachmentIPs releases the addresses one attachment held. It cannot
// use releaseHeldIPAMIPs alone: the holder recorded on the address is the LB
// (that is what the provider filters ListIPs by), while the lifetime is the
// attachment's.
func (p *Pack) releaseAttachmentIPs(lbID, attachmentID string, ipamIDs []string) {
	unlock := p.lockAddresses()
	defer unlock()
	for _, ipamID := range ipamIDs {
		res, found := p.env.Store.Get(Name, kindIPAMIP, ipamID)
		if !found {
			continue
		}
		if held, _ := res.Attrs[attrHolderID].(string); held != lbID {
			continue
		}
		if res.Runtime["lb_attachment"] == attachmentID {
			p.env.Store.Delete(Name, kindIPAMIP, res.ID)
			continue
		}
		_ = p.env.Store.Update(Name, kindIPAMIP, res.ID, func(stored *resource.Resource) error {
			delete(stored.Attrs, attrHolderType)
			delete(stored.Attrs, attrHolderID)
			delete(stored.Attrs, attrHolderName)
			delete(stored.Attrs, attrHolderMAC)
			stored.Updated = p.env.Now()
			return nil
		})
	}
}

func (p *Pack) listLBPrivateNetworks(w http.ResponseWriter, r *http.Request) {
	lb, ok := p.zonalResourceOf(w, r, kindLB, "lbID", "lb")
	if !ok {
		return
	}
	all := p.env.Store.List(kindLBAttachment, resource.Tenant{Provider: Name})
	all = filterResources(all, func(res *resource.Resource) bool {
		return res.Attrs["lb_id"] == lb.ID
	})
	if !orderResources(w, r, "order_by", "created_at_asc", map[string]resourceCmp{
		"created_at": cmpCreated,
		"updated_at": cmpUpdated,
	}, all) {
		return
	}
	page := parsePage(r)
	start, end := page.slice(len(all))
	attachments := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		attachments = append(attachments, p.lbAttachmentView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"private_network": attachments,
		"total_count":     len(all),
	})
}

func (p *Pack) lbAttachmentView(res *resource.Resource) map[string]any {
	var lbView any
	if lb, found := p.env.Store.Get(Name, kindLB, textOf(res.Attrs["lb_id"])); found {
		lbView = p.lbView(lb)
	}
	out := map[string]any{
		"lb":                 lbView,
		"ipam_ids":           res.Attrs["ipam_ids"],
		"private_network_id": res.Attrs["private_network_id"],
		"status":             res.State,
		"created_at":         res.Created.Format(time.RFC3339),
		"updated_at":         res.Updated.Format(time.RFC3339),
	}
	if dhcp, ok := res.Attrs["dhcp_config"]; ok {
		out["dhcp_config"] = dhcp
	}
	return out
}
