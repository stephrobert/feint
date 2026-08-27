package scaleway

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/network"
	"github.com/stephrobert/feint/internal/core/resource"
)

// The VPC product is where an emulated network stops being a record and becomes
// a network. A Private Network carries subnets, a subnet carries a block, and
// that block is what the machine driver turns into a real bridge with a real
// address range. Everything a client reads back afterwards has to agree with it.
//
// Shapes come from the SDK (api/vpc/v2/vpc_sdk.go): VPC, PrivateNetwork, Subnet
// and their list envelopes. Note the field names differ from the instance
// product: project_id and organization_id here, project and organization there.
// Mixing them up produces a body the SDK decodes into zero values without
// complaining.
//
// The product is regional, not zonal: /vpc/v2/regions/{region}/... So the
// tenant's zone field carries the region for these resources. The core treats it
// as an opaque isolation key, which is exactly what it is.

const (
	kindVPC            = "vpc/vpc"
	kindPrivateNetwork = "vpc/private-network"
)

// runtimeNetworkKey is the driver-side network name backing a Private Network.
const runtimeNetworkKey = "network"

// knownRegions is the closed list the emulator accepts, mirroring knownZones.
var knownRegions = map[string]bool{
	"fr-par": true, "nl-ams": true, "pl-waw": true, "it-mil": true,
}

// privateNetworkMask is the size the emulator hands out when a client does not
// choose one. Scaleway allocates a /22 from a private range; the emulator keeps
// the same shape so an address computed here looks like one from up there.
const privateNetworkMask = 22

// privateNetworkV6Mask is the size of a Private Network's IPv6 subnet.
//
// Upstream, every Private Network is dual-stack: creation allocates the IPv4
// block and an IPv6 /64 without being asked, and the SDK carries both in the
// one subnets list (vpc_sdk.go: PrivateNetwork.Subnets, []*Subnet — there is no
// separate ipv6 field on the wire; the Terraform provider splits ipv4_subnet
// from ipv6_subnets by address family, client-side). An emulator serving only
// the IPv4 half made `one(pn.ipv6_subnets).subnet` die on null, apply and
// destroy both (#270).
//
// Measured, no longer inferred. On 2026-08-20 a Private Network was created on
// a real fr-par account with `subnets: null` — nothing asked for a block — and
// the answer carried two: 172.16.4.0/22 and fdb2:1bb5:120a:9b::/64. So the
// unasked allocation, the unique-local fd00::/8 range and the /64 size are all
// observed, and the read that follows carries the same pair unchanged. The
// field tree of that read is in shapes/scaleway.json under
// `GET /vpc/v2/regions/fr-par/private-networks/{id}`; the catalogue keeps paths
// and types, so this comment is where the observed prefix itself lives.
const privateNetworkV6Mask = 64

// reservedPerSubnet is what the runtime keeps at the bottom of a Private Network
// block: the network address, and the gateway the managed bridge answers on.
const reservedPerSubnet = 2

// vpcCreateStatus is what a successful vpc/v2 create answers with.
//
// 200, not the 201 every other create in this pack writes. Measured on the wire
// on 2026-08-20: both of vpc/v2's creates — CreateVPC and CreatePrivateNetwork
// — answered 200 on a real fr-par account, read off a `feint proxy` transcript
// rather than off the CLI's exit code, which shows neither. No other product
// was measured, so no other product is touched: what is claimed here is what
// was seen and nothing beyond it.
//
// CreateRoute joined them on 2026-08-24, the same way and for the same reason:
// `corpus/scaleway/scw-free-shapes.jsonl` holds a real fr-par CreateRoute
// answering 200, and `feint corpus --check` reported this pack's 201 against it
// (#427). The third vpc/v2 create measured is the third to answer 200, which is
// the first evidence that the status is a property of the product rather than
// of the two operations that happened to be recorded first.
//
// It changes nothing for a client that tests 2xx, which is what the SDK and the
// Terraform provider do. It changes the rule this repository works under, which
// is that a status is part of the answer and an invented one is an invented
// format. TestTheVpcCreatesAnswerWhatTheRealCloudAnswers fails without it.
const vpcCreateStatus = http.StatusOK

type createVPCRequest struct {
	Name      string   `json:"name"`
	ProjectID string   `json:"project_id"`
	Tags      []string `json:"tags"`
	// A pointer because the absent field and the explicit false are two
	// different requests here, and the Go zero conflated them (#497). See the
	// create for the measurement on the real cloud.
	EnableRouting *bool `json:"enable_routing"`
	// Sent by the Terraform provider on every CreateVPC since it grew the
	// enable_transitivity attribute. The unread-field gate caught it on the
	// first conformance run of SW-4: honoured as the stored flag the SDK's
	// VPC.TransitivityEnabled reads back.
	EnableTransitivity bool `json:"enable_transitivity"`
}

type updateVPCRequest struct {
	Name *string   `json:"name"`
	Tags *[]string `json:"tags"`
}

type createPrivateNetworkRequest struct {
	Name                           string   `json:"name"`
	ProjectID                      string   `json:"project_id"`
	Tags                           []string `json:"tags"`
	Subnets                        []string `json:"subnets"`
	VpcID                          *string  `json:"vpc_id"`
	DefaultRoutePropagationEnabled *bool    `json:"default_route_propagation_enabled"`
}

// updatePrivateNetworkRequest mirrors vpc/v2.UpdatePrivateNetworkRequest, which
// carries exactly these three: a subnet cannot be changed upstream, so there is
// no field here to refuse.
type updatePrivateNetworkRequest struct {
	Name                           *string   `json:"name"`
	Tags                           *[]string `json:"tags"`
	DefaultRoutePropagationEnabled *bool     `json:"default_route_propagation_enabled"`
}

// ---- VPCs -------------------------------------------------------------------

func (p *Pack) listVPCs(w http.ResponseWriter, r *http.Request) {
	region, ok := regionOf(w, r)
	if !ok {
		return
	}
	p.ensureDefaultVPC(region, p.projectOfRequest(r))

	all := p.env.Store.List(kindVPC, p.regionScopeOf(r, region))
	q := r.URL.Query()
	// "Only VPCs with names containing this string", the house LIKE.
	if name := q.Get("name"); name != "" {
		all = filterResources(all, func(res *resource.Resource) bool {
			return strings.Contains(textOf(res.Attrs["name"]), name)
		})
	}
	// "One or more matching tags": vpc/v2 disjoins.
	if tags := csvValues(q, "tags"); len(tags) > 0 {
		all = filterResources(all, func(res *resource.Resource) bool {
			return hasAnyTag(res, tags)
		})
	}
	if wantDefault, present := queryBool(q, "is_default"); present {
		all = filterResources(all, func(res *resource.Resource) bool {
			isDefault, _ := res.Attrs["is_default"].(bool)
			return isDefault == wantDefault
		})
	}
	if wantRouting, present := queryBool(q, "routing_enabled"); present {
		all = filterResources(all, func(res *resource.Resource) bool {
			enabled, _ := res.Attrs["routing_enabled"].(bool)
			return enabled == wantRouting
		})
	}
	// No VPC here integrates with Object Storage — the product is not emulated
	// (docs/limits.md) — so true truthfully matches nothing.
	if wantS3, present := objectStorageFilter(q); present && wantS3 {
		all = all[:0]
	}
	if !orderResources(w, r, "order_by", "created_at_asc", map[string]resourceCmp{
		"created_at": cmpCreated,
		"name":       cmpName,
	}, all) {
		return
	}

	page := parsePage(r)
	start, end := page.slice(len(all))
	vpcs := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		vpcs = append(vpcs, p.vpcView(res))
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{"vpcs": vpcs, "total_count": len(all)})
}

func (p *Pack) createVPC(w http.ResponseWriter, r *http.Request) {
	region, ok := regionOf(w, r)
	if !ok {
		return
	}

	var req createVPCRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.Name == "" {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "name", Reason: "required"})
		return
	}

	project, _ := projectOf(req.ProjectID)
	res := p.newVPC(region, project, req.Name, false)
	res.Attrs["tags"] = orEmpty(req.Tags)
	// A VPC created without the field routes, which is what the real cloud
	// answers (#497). Measured on a real account on 2026-08-26, the test VPC
	// deleted afterwards:
	//
	//	$ scw vpc vpc create name=feint-premise-routing   # no enable_routing
	//	RoutingEnabled                  true
	//
	// This line used to store the request field as-is, so the Go zero became
	// the default — the inverse of upstream. That is not a cosmetic
	// difference: reachableFrom keys on it, so the web and app Private
	// Networks of one workload VPC were left unpeered and `app→web:443` read
	// `connect_ex=111` while the web group accepted 0.0.0.0/0, which is what
	// the audit measured on examples/stacks/scaleway. That stack has written
	// `enable_routing = true` out on its workload VPC since #503 for exactly
	// this reason; its management VPC still says nothing, and read back false
	// until here. newVPC already carried true for the lazily provisioned
	// default VPC; only the client-created path disagreed with it.
	//
	// An explicit value is still the client's, in either direction: the
	// measurement above covers the absent field and nothing else, and
	// inventing an answer for `enable_routing: false` would be the guess this
	// repository refuses. TestAVPCCreatedWithoutEnableRoutingRoutes fails
	// without this, in both directions.
	if req.EnableRouting != nil {
		res.Attrs["routing_enabled"] = *req.EnableRouting
	}
	res.Attrs["transitivity_enabled"] = req.EnableTransitivity
	p.env.Store.Put(res)

	emulator.WriteJSON(w, vpcCreateStatus, p.vpcView(res))
}

func (p *Pack) getVPC(w http.ResponseWriter, r *http.Request) {
	res, ok := p.resourceOf(w, r, kindVPC, "vpc_id", "vpc")
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.vpcView(res))
}

func (p *Pack) updateVPC(w http.ResponseWriter, r *http.Request) {
	res, ok := p.resourceOf(w, r, kindVPC, "vpc_id", "vpc")
	if !ok {
		return
	}

	var req updateVPCRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.Name != nil {
		res.Attrs["name"] = *req.Name
	}
	if req.Tags != nil {
		res.Attrs["tags"] = orEmpty(*req.Tags)
	}
	res.Updated = p.env.Now()
	p.env.Store.Put(res)

	emulator.WriteJSON(w, http.StatusOK, p.vpcView(res))
}

func (p *Pack) deleteVPC(w http.ResponseWriter, r *http.Request) {
	res, ok := p.resourceOf(w, r, kindVPC, "vpc_id", "vpc")
	if !ok {
		return
	}
	// The API refuses to delete the default VPC, and to delete one that still
	// holds Private Networks. Terraform depends on both: without them a destroy
	// silently removes what later resources need.
	if isDefault, _ := res.Attrs["is_default"].(bool); isDefault {
		writePrecondition(w, "vpc", res.ID, "cannot delete the default VPC of the project")
		return
	}
	if used := p.privateNetworksOf(res.ID); len(used) > 0 {
		writePrecondition(w, "vpc", res.ID,
			fmt.Sprintf("VPC still holds %d private network(s) and cannot be deleted", len(used)))
		return
	}
	if routes := p.routesOfVPC(res.ID); len(routes) > 0 {
		writePrecondition(w, "vpc", res.ID,
			fmt.Sprintf("VPC still holds %d route(s) and cannot be deleted", len(routes)))
		return
	}
	// The ACLs go with it. They are addressed by a key derived from this VPC's
	// identifier, so leaving them behind would make the next VPC to carry that
	// identifier — a restored snapshot, a seeded run — inherit a filter it
	// never set. TestDeletingAVPCTakesItsACLsWithIt fails without this.
	for _, id := range p.aclsOfVPC(res.ID) {
		p.env.Store.Delete(Name, kindVPCACL, id)
	}
	p.env.Store.Delete(Name, kindVPC, res.ID)
	w.WriteHeader(http.StatusNoContent)
}

// ---- Private Networks -------------------------------------------------------

func (p *Pack) listPrivateNetworks(w http.ResponseWriter, r *http.Request) {
	region, ok := regionOf(w, r)
	if !ok {
		return
	}

	all := p.env.Store.List(kindPrivateNetwork, p.regionScopeOf(r, region))
	q := r.URL.Query()
	if vpcID := q.Get("vpc_id"); vpcID != "" {
		all = filterResources(all, func(res *resource.Resource) bool {
			return res.Attrs["vpc_id"] == vpcID
		})
	}
	if name := q.Get("name"); name != "" {
		all = filterResources(all, func(res *resource.Resource) bool {
			return strings.Contains(textOf(res.Attrs["name"]), name)
		})
	}
	if tags := csvValues(q, "tags"); len(tags) > 0 {
		all = filterResources(all, func(res *resource.Resource) bool {
			return hasAnyTag(res, tags)
		})
	}
	if ids := idSet(q, "private_network_ids"); ids != nil {
		all = filterResources(all, func(res *resource.Resource) bool {
			return ids[res.ID]
		})
	}
	// Every network here runs managed DHCP — createPrivateNetwork writes the
	// field and a legacy one is upgraded on read — so the filter is an
	// equality against that stored truth.
	if wantDHCP, present := queryBool(q, "dhcp_enabled"); present {
		all = filterResources(all, func(res *resource.Resource) bool {
			enabled, _ := res.Attrs["dhcp_enabled"].(bool)
			return enabled == wantDHCP
		})
	}
	// Same answer as ListVPCs: no Object Storage integration exists here.
	if wantS3, present := objectStorageFilter(q); present && wantS3 {
		all = all[:0]
	}
	if !orderResources(w, r, "order_by", "created_at_asc", map[string]resourceCmp{
		"created_at": cmpCreated,
		"name":       cmpName,
	}, all) {
		return
	}

	page := parsePage(r)
	start, end := page.slice(len(all))
	networks := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		networks = append(networks, privateNetworkView(res))
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"private_networks": networks,
		"total_count":      len(all),
	})
}

func (p *Pack) createPrivateNetwork(w http.ResponseWriter, r *http.Request) {
	region, ok := regionOf(w, r)
	if !ok {
		return
	}

	var req createPrivateNetworkRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}

	project, _ := projectOf(req.ProjectID)
	vpc := p.ensureDefaultVPC(region, project)
	if req.VpcID != nil && *req.VpcID != "" {
		found, ok := p.env.Store.Get(Name, kindVPC, *req.VpcID)
		if !ok || found.Tenant.Zone != region {
			writeNotFound(w, "vpc", *req.VpcID)
			return
		}
		vpc = found
	}

	// Held from the moment a block is chosen until the network that holds it is
	// stored. Choosing and checking overlap without it meant two concurrent
	// creates read the same state, picked the same free block, both passed
	// FirstOverlap, and both took it — and Terraform creates ten resources at a
	// time by default, which is the case p.lockAddresses() was introduced for.
	unlock := p.lockAddresses()
	defer unlock()

	// The block is validated, not stored blindly. floci accepts any CIDR, never
	// checks the mask, never checks overlap, and reports a fixed address count
	// whatever the prefix; the addressing plan is then decorative.
	prefix, err := p.resolveSubnet(req.Subnets)
	if err != nil {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "subnets",
			Reason:       "constraint",
			HelpMessage:  err.Error(),
		})
		return
	}
	if other, clash := network.FirstOverlap(prefix, p.usedPrefixes(region)); clash {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "subnets",
			Reason:       "constraint",
			HelpMessage:  "subnet " + prefix.String() + " overlaps " + other.String(),
		})
		return
	}

	// The IPv6 half, allocated whether or not the client asked: upstream every
	// Private Network is dual-stack (see privateNetworkV6Mask). The id is drawn
	// first because it seeds the derivation, so the block is a function of the
	// resource and nothing else.
	id := p.env.NewID()
	prefix6, err := p.resolveSubnetV6(req.Subnets, project, id)
	if err != nil {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "subnets",
			Reason:       "constraint",
			HelpMessage:  err.Error(),
		})
		return
	}
	if other, clash := network.FirstOverlap(prefix6, p.usedPrefixes(region)); clash {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "subnets",
			Reason:       "constraint",
			HelpMessage:  "subnet " + prefix6.String() + " overlaps " + other.String(),
		})
		return
	}

	now := p.env.Now()
	res := resource.New(id, kindPrivateNetwork, resource.Tenant{Provider: Name, Project: project, Zone: region}, "available", now)
	res.Attrs = map[string]any{
		"name":                              orDefault(req.Name, "pn-"+prefix.Addr().String()),
		"project_id":                        project,
		"organization_id":                   defaultOrganization,
		"vpc_id":                            vpc.ID,
		"tags":                              orEmpty(req.Tags),
		"dhcp_enabled":                      true,
		"default_route_propagation_enabled": deref(req.DefaultRoutePropagationEnabled, false),
		"subnet":                            prefix.String(),
		// Stored, not recomputed at read time: what a client was told once is
		// what every later read and every restored snapshot must repeat.
		"subnet_ipv6": prefix6.String(),
	}
	// The backing network is created before the resource is stored, and a
	// failure is fatal to the request rather than logged.
	//
	// This is the one place where degrading quietly would be wrong. Everywhere
	// else a missing runtime costs a machine that does not boot, and the control
	// plane still describes something true. Here it would hand back a network
	// that exists nowhere, with addresses no machine can carry, which is exactly
	// the defect this emulator exists to avoid.
	//
	// The common cause is a block already used on the operator's host, by a lab
	// bridge or a VPN. Upstream has the same notion, hence ListSubnetOverlaps in
	// the SDK, so refusing on an overlap is what a client already expects.
	if err := p.ensureBackingNetwork(r.Context(), res, prefix); err != nil {
		p.logger().Error("could not create the backing network",
			"private_network", res.ID, "subnet", prefix, "error", err)
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "subnets",
			Reason:       "constraint",
			HelpMessage: "the machine runtime could not provide a network for " + prefix.String() +
				"; the block is most likely already in use on this host: " + err.Error(),
		})
		return
	}
	p.env.Store.Put(res)
	p.isolateNetworks(r.Context())

	emulator.WriteJSON(w, vpcCreateStatus, privateNetworkView(res))
}

func (p *Pack) getPrivateNetwork(w http.ResponseWriter, r *http.Request) {
	res, ok := p.resourceOf(w, r, kindPrivateNetwork, "pnID", "private_network")
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, privateNetworkView(res))
}

func (p *Pack) updatePrivateNetwork(w http.ResponseWriter, r *http.Request) {
	res, ok := p.resourceOf(w, r, kindPrivateNetwork, "pnID", "private_network")
	if !ok {
		return
	}

	var req updatePrivateNetworkRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	// Inside Store.Update rather than mutate-then-Put: Put resurrects a network
	// deleted while this PATCH was decoding, and its whole-Attrs write-back from
	// a stale clone loses a concurrent writer's field (#289).
	var final *resource.Resource
	err := p.env.Store.Update(Name, kindPrivateNetwork, res.ID, func(stored *resource.Resource) error {
		if req.Name != nil {
			stored.Attrs["name"] = *req.Name
		}
		if req.Tags != nil {
			stored.Attrs["tags"] = orEmpty(*req.Tags)
		}
		if req.DefaultRoutePropagationEnabled != nil {
			stored.Attrs["default_route_propagation_enabled"] = *req.DefaultRoutePropagationEnabled
		}
		stored.Updated = p.env.Now()
		final = stored
		return nil
	})
	if err != nil {
		writeNotFound(w, "private_network", res.ID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, privateNetworkView(final))
}

func (p *Pack) deletePrivateNetwork(w http.ResponseWriter, r *http.Request) {
	res, ok := p.resourceOf(w, r, kindPrivateNetwork, "pnID", "private_network")
	if !ok {
		return
	}
	if nics := p.nicsOnNetwork(res.ID); len(nics) > 0 {
		writePrecondition(w, "private_network", res.ID,
			fmt.Sprintf("private network still has %d attached NIC(s)", len(nics)))
		return
	}
	// A booked address or a custom route naming this network would dangle if
	// the network vanished under it. Terraform destroys in reverse dependency
	// order, so a correct plan never sees these; a wrong one gets a refusal it
	// can retry rather than a corrupted read later.
	for _, ip := range p.ipamIPsOnNetwork(res.ID) {
		if isBooked, _ := ip.Attrs[attrBooked].(bool); isBooked {
			writePrecondition(w, "private_network", res.ID,
				"an IP is still booked in this private network; release it first")
			return
		}
	}
	for _, route := range p.env.Store.List(kindVPCRoute, resource.Tenant{Provider: Name}) {
		if route.Attrs["nexthop_private_network_id"] == res.ID {
			writePrecondition(w, "private_network", res.ID,
				"a route still uses this private network as its nexthop; delete it first")
			return
		}
	}
	// The runtime half is fatal to the request, exactly as it is on the create
	// path above, and #426 is why it stopped being a logged warning.
	//
	// What the swallow produced, read on the host rather than deduced: DELETE
	// answered 204, the store forgot the network, and `incus network list` still
	// showed the bridge — with its dnsmasq holding the block. The next run then
	// failed on "Address already in use" for a network the API had reported gone
	// minutes earlier, which is the exact lie this emulator exists to avoid: a
	// create that succeeds while nothing exists, and a delete that succeeds
	// while everything does, are the same defect in two directions.
	//
	// The refusal reuses writePrecondition, so it is the same shape this handler
	// already answers for a still-attached NIC: a precondition the client can act
	// on and retry, rather than an opaque failure. The usual cause is the same
	// one, a machine still holding a device, which RemoveNetwork's own contract
	// says it must refuse rather than cut off.
	//
	// TestAPrivateNetworkTheRuntimeKeptIsNotReportedDeleted fails without this.
	if err := p.binding().RemoveBackingNetwork(r.Context(), res, runtimeNetworkKey); err != nil {
		p.logger().Error("could not remove the backing network",
			"private_network", res.ID, "network", res.Runtime[runtimeNetworkKey], "error", err)
		writePrecondition(w, "private_network", res.ID,
			"the machine runtime still holds the network backing this private network, "+
				"so deleting it here would report gone something that still holds its block: "+err.Error())
		return
	}
	p.env.Store.Delete(Name, kindPrivateNetwork, res.ID)
	p.isolateNetworks(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// ---- Subnets ----------------------------------------------------------------

// listSubnets answers the region's subnets as first-class objects. They are the
// same records ListPrivateNetworks already carries on each network — one per
// address family — served flat because the SDK offers this second door and
// the Terraform provider matches a booked address's subnet_id against it.
func (p *Pack) listSubnets(w http.ResponseWriter, r *http.Request) {
	region, ok := regionOf(w, r)
	if !ok {
		return
	}

	networks := p.env.Store.List(kindPrivateNetwork, p.regionScopeOf(r, region))
	q := r.URL.Query()
	if vpcID := q.Get("vpc_id"); vpcID != "" {
		networks = filterResources(networks, func(res *resource.Resource) bool {
			return res.Attrs["vpc_id"] == vpcID
		})
	}
	// created_at is the only field this operation's enum orders by, and a
	// subnet's created_at is its network's — so ordering the networks before
	// flattening orders the subnets. Sorted here, while they are still
	// resources, because that is the shape the shared helper reads.
	if !orderResources(w, r, "order_by", "created_at_asc", map[string]resourceCmp{
		"created_at": cmpCreated,
	}, networks) {
		return
	}

	// Flattened before filtering and paging: a Private Network carries two
	// subnets now, and this door serves subnets, not networks — a page size or
	// a subnet_ids filter counts records of the thing being listed.
	subnets := make([]map[string]any, 0, 2*len(networks))
	for _, pn := range networks {
		subnets = append(subnets, subnetViews(pn)...)
	}
	if ids := q["subnet_ids"]; len(ids) > 0 {
		wanted := make(map[string]bool, len(ids))
		for _, id := range ids {
			wanted[id] = true
		}
		filtered := subnets[:0]
		for _, s := range subnets {
			if id, _ := s["id"].(string); wanted[id] {
				filtered = append(filtered, s)
			}
		}
		subnets = filtered
	}

	page := parsePage(r)
	start, end := page.slice(len(subnets))
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"subnets":     subnets[start:end],
		"total_count": len(subnets),
	})
}

// subnetViews is the wire shape of a Private Network's subnets, one record per
// family, and the same objects privateNetworkView embeds: two doors, one
// builder. The SDK has no ipv6 field here — both families ride the one
// subnets list (vpc_sdk.go: PrivateNetwork.Subnets), and a client tells them
// apart by address family.
func subnetViews(pn *resource.Resource) []map[string]any {
	subnet, _ := pn.Attrs["subnet"].(string)
	out := []map[string]any{subnetRecord(pn, subnetIDOf(pn.ID), subnet)}
	// Absent on a resource restored from a snapshot taken before the emulator
	// allocated IPv6: serve what the store holds rather than invent a block at
	// read time that no create path ever validated.
	if subnet6, _ := pn.Attrs["subnet_ipv6"].(string); subnet6 != "" {
		out = append(out, subnetRecord(pn, subnetV6IDOf(pn.ID), subnet6))
	}
	return out
}

func subnetRecord(pn *resource.Resource, id, block string) map[string]any {
	return map[string]any{
		"id":                 id,
		"subnet":             block,
		"project_id":         pn.Attrs["project_id"],
		"private_network_id": pn.ID,
		"vpc_id":             pn.Attrs["vpc_id"],
		"region":             pn.Tenant.Zone,
		"created_at":         pn.Created.Format(time.RFC3339),
		"updated_at":         pn.Updated.Format(time.RFC3339),
	}
}

// ---- The enable family ------------------------------------------------------

// enableRouting turns on routing between the VPC's Private Networks, and it is
// not a stored flag: reachableFrom reads it, so the isolation the machine
// driver enforces is reconciled the moment it flips. One-way upstream — there
// is no disable — and one-way here.
func (p *Pack) enableRouting(w http.ResponseWriter, r *http.Request) {
	res, ok := p.resourceOf(w, r, kindVPC, "vpc_id", "vpc")
	if !ok {
		return
	}
	// Update rather than Put, like every write to an existing resource: a Put
	// here resurrected a VPC deleted meanwhile, routing enabled (#289).
	var final *resource.Resource
	err := p.env.Store.Update(Name, kindVPC, res.ID, func(stored *resource.Resource) error {
		stored.Attrs["routing_enabled"] = true
		stored.Updated = p.env.Now()
		final = stored
		return nil
	})
	if err != nil {
		writeNotFound(w, "vpc", res.ID)
		return
	}
	// What was isolated may now be reachable: the rule sets carried by the
	// backing networks must say what the control plane just said.
	p.isolateNetworks(r.Context())
	emulator.WriteJSON(w, http.StatusOK, p.vpcView(final))
}

// enableDHCP exists for Private Networks created before DHCP was the default.
// Every network created here has dhcp_enabled from the start, so this can only
// confirm; it is served because a client that calls it expects the network
// back, not a 501.
func (p *Pack) enableDHCP(w http.ResponseWriter, r *http.Request) {
	res, ok := p.resourceOf(w, r, kindPrivateNetwork, "pnID", "private_network")
	if !ok {
		return
	}
	// Update rather than Put, same reasoning as enableRouting above (#289).
	var final *resource.Resource
	err := p.env.Store.Update(Name, kindPrivateNetwork, res.ID, func(stored *resource.Resource) error {
		stored.Attrs["dhcp_enabled"] = true
		stored.Updated = p.env.Now()
		final = stored
		return nil
	})
	if err != nil {
		writeNotFound(w, "private_network", res.ID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, privateNetworkView(final))
}

// ---- Default inventory ------------------------------------------------------

// ensureDefaultVPC provisions the VPC a project already owns. Like the default
// security group, it is lazy: the emulator has no notion of "a project was
// created", so the first read is the only moment it can happen.
func (p *Pack) ensureDefaultVPC(region, project string) *resource.Resource {
	unlock := p.lockDefaults()
	defer unlock()

	scope := resource.Tenant{Provider: Name, Project: project, Zone: region}
	for _, res := range p.env.Store.List(kindVPC, scope) {
		if isDefault, _ := res.Attrs["is_default"].(bool); isDefault {
			return res
		}
	}
	res := p.newVPC(region, project, "default", true)
	res.Attrs["tags"] = []string{defaultVPCTag}
	p.env.Store.Put(res)
	return res
}

// defaultVPCTag is what the real default VPC carries, and it is a measurement
// rather than a convention: `scw vpc vpc list` against a real fr-par account on
// 2026-08-21 answered tags ["default"] on the VPC the project was born with,
// and the corpus recorded the same list through `feint proxy`. A fresh emulator
// answered [], which is the first defect the committed corpus surfaced (#355)
// and the smallest — invisible to every other control here, because no client
// reads the tag and a contract states the type of `tags` rather than what the
// cloud puts in it.
//
// Written where the default VPC is provisioned and nowhere else. A VPC a client
// creates carries the tags the client sent: writing this on those would invent
// a value the API never answered, which is the failure this gate exists to
// catch rather than to commit.
//
// TestTheDefaultVPCCarriesTheDefaultTag fails without this, in both directions.
const defaultVPCTag = "default"

func (p *Pack) newVPC(region, project, name string, isDefault bool) *resource.Resource {
	now := p.env.Now()
	return &resource.Resource{
		ID:      p.env.NewID(),
		Kind:    kindVPC,
		Tenant:  resource.Tenant{Provider: Name, Project: project, Zone: region},
		State:   "available",
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"name":            name,
			"project_id":      project,
			"organization_id": defaultOrganization,
			"tags":            []string{},
			"is_default":      isDefault,
			"routing_enabled": true,
			// Present from the start so the fields serialize on every read.
			// Propagation would be flipped by EnableCustomRoutesPropagation,
			// which stays declined until the portal document describes it;
			// transitivity is what the client asked at create, read back.
			"custom_routes_propagation_enabled": false,
			"transitivity_enabled":              false,
		},
	}
}

// isolate keeps the emulated subnets apart, which two managed bridges on one
// host are not by themselves.
//
// What counts as foreign is a Scaleway question, and it is the only part
// written here: two Private Networks of one VPC are routed to each other
// upstream when the VPC has routing enabled, so they stay reachable;
// everything else, another VPC or another project, is rejected. The
// reconciliation itself — peer lists under native isolation, foreign blocks
// otherwise, over every network because a new subnet changes what its
// neighbours must keep out — is machine.ReconcileIsolation, shared with the
// two other packs rather than copied a third time (#201 measured what the
// copies cost).
//
// Coalesced like the Outscale pack's (#473, the measurement and the guarantee
// are on isolateNetworks there): the pass is O(N) over every network and a
// client creates its networks concurrently, so a burst shares its passes
// instead of each request paying a full one. The pass re-reads the store when
// it runs, and every caller returns only once a pass that read its own change
// has completed.
func (p *Pack) isolateNetworks(ctx context.Context) {
	ctx = context.WithoutCancel(ctx)
	p.isolation.Run(func() { p.isolationPass(ctx) })
}

// isolationPass is one full reconciliation, reading the store at the moment it
// runs. Only the Coalescer calls it.
func (p *Pack) isolationPass(ctx context.Context) {
	all := p.env.Store.List(kindPrivateNetwork, resource.Tenant{Provider: Name})
	members := make([]machine.IsolationMember, len(all))
	for i, pn := range all {
		block, _ := pn.Attrs["subnet"].(string)
		members[i] = machine.IsolationMember{
			ID:      pn.ID,
			Network: pn.Runtime[runtimeNetworkKey],
			Block:   block,
		}
	}
	native, applied := p.binding().ReconcileIsolation(ctx, "private_network",
		members, func(from, to int) bool { return p.reachableFrom(all[from], all[to]) })
	if native || !applied {
		// No group resync under native isolation: the rule sets carry no
		// foreign blocks, so a subnet coming or going changes nothing in them.
		return
	}

	// The rule sets carried by the machines say the same thing, and they are the
	// ones measured to hold: a network rule set alone did not separate two VPCs
	// once the NICs carried a group of their own.
	for _, group := range p.env.Store.List(kindSecurityGroup, resource.Tenant{Provider: Name}) {
		p.syncSecurityGroup(ctx, group)
	}
}

// reachableFrom reports whether one Private Network may reach another: same
// project, same VPC, and a VPC that routes between its networks.
func (p *Pack) reachableFrom(from, to *resource.Resource) bool {
	if from.Tenant.Project != to.Tenant.Project {
		return false
	}
	if from.Attrs["vpc_id"] != to.Attrs["vpc_id"] {
		return false
	}
	// Read with the comma-ok form like everywhere else in this file. The bare
	// assertion panicked on a resource whose vpc_id was absent or nil, which the
	// comparison above lets through when both sides are nil.
	vpcID, _ := from.Attrs["vpc_id"].(string)
	vpc, found := p.env.Store.Get(Name, kindVPC, vpcID)
	if !found {
		return false
	}
	routing, _ := vpc.Attrs["routing_enabled"].(bool)
	return routing
}

// ---- Addressing -------------------------------------------------------------

// resolveSubnet validates the block a client asked for, or picks a free one.
//
// Scaleway hands out a block when the client names none, and a client that names
// one expects it back unchanged. Both paths go through the same validation, so a
// generated block is as legal as a requested one.
func (p *Pack) resolveSubnet(requested []string) (netip.Prefix, error) {
	if len(requested) == 0 {
		return p.freePrefix(), nil
	}
	// This resolves the IPv4 block only; the IPv6 one goes through
	// resolveSubnetV6. Requesting IPv6 alone stays an error, as upstream
	// requires the IPv4 half.
	for _, raw := range requested {
		prefix, err := network.ParseCIDR(raw)
		if err != nil {
			return netip.Prefix{}, err
		}
		if !prefix.Addr().Is4() {
			continue
		}
		if err := network.CheckMask(prefix, 16, 28); err != nil {
			return netip.Prefix{}, err
		}
		return prefix, nil
	}
	return netip.Prefix{}, fmt.Errorf("no IPv4 subnet in the request")
}

// resolveSubnetV6 validates the IPv6 block a client asked for, or derives one.
//
// A client that named an fd…/64 expects it back unchanged, exactly like the
// IPv4 path. A client that named none still gets one, because upstream
// allocates it unasked — that is the whole of #270. The derived block is a
// unique-local /64 seeded by the resource id, inside the /48 the project's
// networks share: deterministic between two reads, and stored anyway, so it
// also survives a snapshot into another instance.
func (p *Pack) resolveSubnetV6(requested []string, project, id string) (netip.Prefix, error) {
	for _, raw := range requested {
		prefix, err := network.ParseCIDR(raw)
		if err != nil {
			return netip.Prefix{}, err
		}
		if prefix.Addr().Is4() {
			continue
		}
		if err := network.CheckMask(prefix, privateNetworkV6Mask, privateNetworkV6Mask); err != nil {
			return netip.Prefix{}, err
		}
		return prefix, nil
	}
	// Nothing requested: derive inside the project's own /48, and dodge the
	// blocks already held. The project is the space because that is what the
	// real cloud was measured to group by — two networks of one project came
	// back under one fd…/48 (see network.ULA64Within) — and a clash is then a
	// 16-bit subnet collision rather than an astronomical one, so the loop
	// earns its keep. Salting the seed moves the subnet ID and keeps the /48,
	// which is what makes it terminate in the right prefix; the result is
	// stored, so later reads do not re-run it.
	taken := p.usedPrefixes("")
	seed := id
	for {
		candidate := network.ULA64Within(project, seed)
		if _, clash := network.FirstOverlap(candidate, taken); !clash {
			return candidate, nil
		}
		seed += "+"
	}
}

// freePrefix picks a block from the private range that no Private Network holds.
func (p *Pack) freePrefix() netip.Prefix {
	taken := p.usedPrefixes("")
	// 172.16.0.0/22, 172.16.4.0/22, 172.16.8.0/22, ... the shape Scaleway hands
	// out. A /22 spans four /24s, so the step is four on the third octet: a step
	// on the last one produces the same masked block every time, which is a
	// generator that hands out one block and then refuses everything as
	// overlapping.
	for third := 0; third < 256; third += 4 {
		for second := 16; second < 32; second++ {
			addr := netip.AddrFrom4([4]byte{172, byte(second), byte(third), 0})
			candidate := netip.PrefixFrom(addr, privateNetworkMask).Masked()
			if _, clash := network.FirstOverlap(candidate, taken); !clash {
				return candidate
			}
		}
	}
	// Every block taken is not a state a local emulator reaches; falling back
	// keeps the create working rather than failing on an impossible case.
	return netip.MustParsePrefix("172.16.0.0/22")
}

// usedPrefixes lists the blocks already held, so a new one cannot overlap. An
// empty region means every region: two Private Networks of different regions
// still map onto two bridges of one host.
func (p *Pack) usedPrefixes(region string) []netip.Prefix {
	all := p.env.Store.List(kindPrivateNetwork, resource.Tenant{Provider: Name})
	out := make([]netip.Prefix, 0, len(all))
	for _, res := range all {
		if region != "" && res.Tenant.Zone != region {
			continue
		}
		if prefix, err := prefixOf(res); err == nil {
			out = append(out, prefix)
		}
		// The IPv6 blocks too, so a requested fd…/64 cannot land on a sibling's.
		// Harmless to the IPv4 callers: Overlaps is false across families.
		if raw, _ := res.Attrs["subnet_ipv6"].(string); raw != "" {
			if prefix6, err := network.ParseCIDR(raw); err == nil {
				out = append(out, prefix6)
			}
		}
	}
	return out
}

func prefixOf(res *resource.Resource) (netip.Prefix, error) {
	raw, _ := res.Attrs["subnet"].(string)
	return network.ParseCIDR(raw)
}

// allocatorFor rebuilds the allocator of a Private Network from what is already
// handed out. It is rebuilt rather than kept in memory so a restart, which
// reloads the store from a snapshot, cannot hand out an address twice.
func (p *Pack) allocatorFor(res *resource.Resource) (*network.Allocator, error) {
	prefix, err := prefixOf(res)
	if err != nil {
		return nil, err
	}
	alloc, err := network.NewAllocator(prefix, reservedPerSubnet)
	if err != nil {
		return nil, err
	}
	// Rebuilt from IPAM, which is where the addresses live: the NIC itself
	// carries none, exactly as upstream. From every IPAM address of the
	// network, not from the NICs: an address booked through BookIP has no NIC
	// yet, and a rebuild that could not see it would hand it out again.
	// TestABookedAddressIsNotHandedToTheNextNIC fails without this.
	for _, ip := range p.ipamIPsOnNetwork(res.ID) {
		if raw, _ := ip.Attrs["address"].(string); raw != "" {
			// netip.ParsePrefix, not network.ParseCIDR: this is a host address
			// carrying its mask, and ParseCIDR refuses host bits by design.
			if taken, err := netip.ParsePrefix(raw); err == nil {
				_ = alloc.Reserve(taken.Addr())
			}
		}
	}
	return alloc, nil
}

// ensureBackingNetwork asks the machine driver for a real network carrying the
// block. Failure is logged, never fatal: the control plane must keep answering
// when no runtime is available, which is the default configuration. The
// mechanics — the derived name, the labels, recording the name only once the
// driver accepted it — are Binding.EnsureBackingNetwork's, shared with the two
// other packs (#510).
func (p *Pack) ensureBackingNetwork(ctx context.Context, res *resource.Resource, prefix netip.Prefix) error {
	return p.binding().EnsureBackingNetwork(ctx, res, machine.BackingNetwork{
		Key:     runtimeNetworkKey,
		CIDR:    prefix,
		Gateway: true,
		NAT:     true,
		Marker:  "feint.private-network",
	})
}

// ---- Plumbing ---------------------------------------------------------------

func (p *Pack) privateNetworksOf(vpcID string) []*resource.Resource {
	all := p.env.Store.List(kindPrivateNetwork, resource.Tenant{Provider: Name})
	out := make([]*resource.Resource, 0, len(all))
	for _, res := range all {
		if res.Attrs["vpc_id"] == vpcID {
			out = append(out, res)
		}
	}
	return out
}

// regionOfZone strips the trailing index of a zone: fr-par-1 belongs to fr-par.
// Regional and zonal products name the same place differently, and a resource
// created through one is read through the other.
func regionOfZone(zone string) string {
	if i := strings.LastIndex(zone, "-"); i > 0 {
		return zone[:i]
	}
	return zone
}

// regionOf validates the {region} path segment, mirroring zoneOf.
func regionOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	region := r.PathValue("region")
	if !knownRegions[region] {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "region",
			Reason:       "constraint",
			HelpMessage:  "unknown region " + region,
		})
		return "", false
	}
	return region, true
}

// projectOfRequest reads the project a regional request names. The VPC product
// spells it project_id, where the instance product spells it project.
func (p *Pack) projectOfRequest(r *http.Request) string {
	q := r.URL.Query()
	return orDefault(orDefault(q.Get("project_id"), q.Get("project")), defaultProject)
}

func (p *Pack) regionScopeOf(r *http.Request, region string) resource.Tenant {
	q := r.URL.Query()
	if q.Get("organization_id") != "" && q.Get("project_id") == "" {
		return resource.Tenant{Provider: Name, Zone: region}
	}
	return resource.Tenant{Provider: Name, Project: p.projectOfRequest(r), Zone: region}
}

// resourceOf resolves a regional resource by path segment, writing the error.
func (p *Pack) resourceOf(w http.ResponseWriter, r *http.Request, kind, segment, label string) (*resource.Resource, bool) {
	region, ok := regionOf(w, r)
	if !ok {
		return nil, false
	}
	id := r.PathValue(segment)
	res, found := p.env.Store.Get(Name, kind, id)
	if !found || res.Tenant.Zone != region {
		writeNotFound(w, label, id)
		return nil, false
	}
	return res, true
}

// writePrecondition answers the shape scw/errors.go actually decodes.
//
// It used to send {message, resource, resource_id}, and the SDK reads
// {precondition, help_message}: every precondition error reached the client with
// its reason stripped — `scaleway-sdk-go: precondition failed: ` with nothing
// after the colon. An audit reproduced it twice, on a duplicate NIC and on
// deleting a default security group. errors.go says the field names mirror the
// SDK structs; for this family they did not.
//
// TestAPreconditionRendersAsASentence fails without this.
func writePrecondition(w http.ResponseWriter, kind, id, message string) {
	// The SDK's Precondition field is a short machine-readable token, and
	// PreconditionFailedError.Error() switches on exactly three of them:
	// unknown_precondition, resource_still_in_use, attribute_must_be_set. A
	// token outside that set renders as the empty string, so `scw instance
	// security-group delete <default>` printed "precondition failed: " with
	// nothing after the colon — an audit reproduced it against the first
	// version of this function, which built "<kind>_resource_still_in_use".
	// The kind is carried by the resource field instead, which is where the
	// HTTP API puts it.
	//
	// TestAPreconditionRendersAsASentence fails without this.
	emulator.WriteJSON(w, http.StatusBadRequest, map[string]any{
		"type":         "precondition_failed",
		"precondition": "resource_still_in_use",
		"help_message": message,
		// Kept beside the SDK fields rather than instead of them: the HTTP API
		// carries them and a client reading the raw body still finds them.
		"resource":    kind,
		"resource_id": id,
	})
}

func (p *Pack) vpcView(res *resource.Resource) map[string]any {
	out := map[string]any{
		"id":                    res.ID,
		"region":                res.Tenant.Zone,
		"created_at":            res.Created.Format(time.RFC3339),
		"updated_at":            res.Updated.Format(time.RFC3339),
		"private_network_count": len(p.privateNetworksOf(res.ID)),
		// Measured on 2026-08-20: the earlier recording of ListVPCs was taken
		// on an account holding no VPC, so its element shape was never
		// observed and this omission was invisible. It is served for the same
		// reason its private-network counterpart is — the five operations that
		// could attach an Object Storage endpoint are declined in pack.go —
		// and listVPCs already answers `s3_integration_enabled=true` with an
		// empty list, so the flag and the filter now say the same thing.
		//
		// TestTheObjectStorageFlagsAreServedOnEveryDoor fails without it.
		"s3_integration_enabled": false,
	}
	for k, v := range res.Attrs {
		out[k] = v
	}
	return out
}

func privateNetworkView(res *resource.Resource) map[string]any {
	out := map[string]any{
		"id":         res.ID,
		"region":     res.Tenant.Zone,
		"created_at": res.Created.Format(time.RFC3339),
		"updated_at": res.Updated.Format(time.RFC3339),
		// Carried on the wire by every real answer, and dropped by `scw` on the
		// way to its own output — which is why reading the CLI rather than the
		// recording would have missed it. Measured on 2026-08-20 against a real
		// fr-par account (shapes/scaleway.json, GET
		// /vpc/v2/regions/fr-par/private-networks/{id}); the emulator omitted
		// it, and the contract has always declared it.
		//
		// Computed here rather than stored, and always false: it says an Object
		// Storage endpoint is attached, and the five operations that could
		// attach one are declined in pack.go with their reason. A stored flag
		// would be a value nothing can ever change.
		//
		// TestTheObjectStorageFlagsAreServedOnEveryDoor fails without it.
		"has_s3_integration": false,
	}
	for k, v := range res.Attrs {
		if k == "subnet" || k == "subnet_ipv6" {
			continue
		}
		out[k] = v
	}
	// The wire shape carries a list of subnet objects, not the bare blocks the
	// store keeps: a client decodes them into vpc.Subnet. The same objects
	// ListSubnets serves flat — one builder, so the two doors cannot disagree.
	out["subnets"] = subnetViews(res)
	return out
}

// subnetIDOf derives the subnet identifier from its Private Network.
//
// Derived rather than random so it survives a restart, and distinct from the
// network's own id because they are two resources: a client that stores both
// would otherwise hold one UUID for two things, and could not tell which it was
// looking at.
func subnetIDOf(privateNetworkID string) string {
	return derivedID("subnet:" + privateNetworkID)
}

// subnetV6IDOf is the IPv6 subnet's identifier, distinct from the IPv4 one for
// the same reason the IPv4 one is distinct from the network's: two records,
// two ids, or a client holding both cannot tell which it stored.
func subnetV6IDOf(privateNetworkID string) string {
	return derivedID("subnet-v6:" + privateNetworkID)
}

// derivedID builds a UUID-shaped identifier from a seed, deterministically.
// Shared by the subnet and the computed route ids: same reasons, same shape.
//
// The derivation itself moved to [resource.DerivedID] when the Exoscale pack
// needed the same thing for its zone identifiers: an object with no row of its
// own still has to answer the same id twice. This wrapper stays because the
// seeds are Scaleway's and the callers read better for it.
func derivedID(seed string) string {
	return resource.DerivedID(seed)
}
