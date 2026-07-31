package scaleway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// reservedPerSubnet is what the runtime keeps at the bottom of a Private Network
// block: the network address, and the gateway the managed bridge answers on.
const reservedPerSubnet = 2

type createVPCRequest struct {
	Name          string   `json:"name"`
	ProjectID     string   `json:"project_id"`
	Tags          []string `json:"tags"`
	EnableRouting bool     `json:"enable_routing"`
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
	res.Attrs["routing_enabled"] = req.EnableRouting
	p.env.Store.Put(res)

	emulator.WriteJSON(w, http.StatusCreated, p.vpcView(res))
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
	if vpcID := r.URL.Query().Get("vpc_id"); vpcID != "" {
		filtered := all[:0]
		for _, res := range all {
			if res.Attrs["vpc_id"] == vpcID {
				filtered = append(filtered, res)
			}
		}
		all = filtered
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
	// time by default, which is the case p.addresses was introduced for.
	p.addresses.Lock()
	defer p.addresses.Unlock()

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

	now := p.env.Now()
	res := &resource.Resource{
		ID:      p.env.NewID(),
		Kind:    kindPrivateNetwork,
		Tenant:  resource.Tenant{Provider: Name, Project: project, Zone: region},
		State:   "available",
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"name":                              orDefault(req.Name, "pn-"+prefix.Addr().String()),
			"project_id":                        project,
			"organization_id":                   defaultOrganization,
			"vpc_id":                            vpc.ID,
			"tags":                              orEmpty(req.Tags),
			"dhcp_enabled":                      true,
			"default_route_propagation_enabled": deref(req.DefaultRoutePropagationEnabled, false),
			"subnet":                            prefix.String(),
		},
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

	emulator.WriteJSON(w, http.StatusCreated, privateNetworkView(res))
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
	if req.Name != nil {
		res.Attrs["name"] = *req.Name
	}
	if req.Tags != nil {
		res.Attrs["tags"] = orEmpty(*req.Tags)
	}
	if req.DefaultRoutePropagationEnabled != nil {
		res.Attrs["default_route_propagation_enabled"] = *req.DefaultRoutePropagationEnabled
	}

	res.Updated = p.env.Now()
	p.env.Store.Put(res)
	emulator.WriteJSON(w, http.StatusOK, privateNetworkView(res))
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
	if name := res.Runtime[runtimeNetworkKey]; name != "" && p.env.Machines != nil {
		if err := p.env.Machines.RemoveNetwork(r.Context(), name); err != nil {
			p.logger().Error("could not remove the backing network",
				"private_network", res.ID, "network", name, "error", err)
		}
	}
	p.env.Store.Delete(Name, kindPrivateNetwork, res.ID)
	p.isolateNetworks(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// ---- Default inventory ------------------------------------------------------

// ensureDefaultVPC provisions the VPC a project already owns. Like the default
// security group, it is lazy: the emulator has no notion of "a project was
// created", so the first read is the only moment it can happen.
func (p *Pack) ensureDefaultVPC(region, project string) *resource.Resource {
	p.defaults.Lock()
	defer p.defaults.Unlock()

	scope := resource.Tenant{Provider: Name, Project: project, Zone: region}
	for _, res := range p.env.Store.List(kindVPC, scope) {
		if isDefault, _ := res.Attrs["is_default"].(bool); isDefault {
			return res
		}
	}
	res := p.newVPC(region, project, "default", true)
	p.env.Store.Put(res)
	return res
}

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
		},
	}
}

// isolate keeps the emulated subnets apart, which two managed bridges on one
// host are not by themselves.
//
// What counts as foreign is a Scaleway question. Two Private Networks of one VPC
// are routed to each other upstream when the VPC has routing enabled, so they
// stay reachable here; everything else, another VPC or another project, is
// rejected. Reconciled over every network because a new subnet changes what its
// neighbours must keep out.
//
// A runtime with native isolation inverts the work: its networks reach nothing
// until peered, so the question is no longer what to keep out but what to let
// in, and the same reachability rule answers both.
func (p *Pack) isolateNetworks(ctx context.Context) {
	all := p.env.Store.List(kindPrivateNetwork, resource.Tenant{Provider: Name})

	if peerer, ok := p.env.Machines.(machine.Peerer); ok && peerer.NativeIsolation() {
		for _, pn := range all {
			name := pn.Runtime[runtimeNetworkKey]
			if name == "" {
				continue
			}
			peers := make([]string, 0, len(all))
			for _, other := range all {
				if other.ID == pn.ID || other.Runtime[runtimeNetworkKey] == "" {
					continue
				}
				if p.reachableFrom(pn, other) {
					peers = append(peers, other.Runtime[runtimeNetworkKey])
				}
			}
			if err := peerer.PeerNetworks(ctx, name, peers); err != nil {
				p.logger().Error("could not peer the private network",
					"private_network", pn.ID, "network", name, "error", err)
			}
		}
		// No group resync here: with native isolation the rule sets carry no
		// foreign blocks, so a subnet coming or going changes nothing in them.
		return
	}

	isolator, ok := p.env.Machines.(machine.Isolator)
	if !ok {
		return
	}
	for _, pn := range all {
		name := pn.Runtime[runtimeNetworkKey]
		if name == "" {
			continue
		}
		foreign := make([]string, 0, len(all))
		for _, other := range all {
			if other.ID == pn.ID || p.reachableFrom(pn, other) {
				continue
			}
			if block, _ := other.Attrs["subnet"].(string); block != "" {
				foreign = append(foreign, block)
			}
		}
		if err := isolator.IsolateNetwork(ctx, name, foreign); err != nil {
			p.logger().Error("could not isolate the private network",
				"private_network", pn.ID, "network", name, "error", err)
		}
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
	// One IPv4 block per Private Network here. Scaleway also carries an IPv6
	// one, which the allocator does not handle and which no emulated machine
	// would use.
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
	// carries none, exactly as upstream.
	for _, nic := range p.nicsOnNetwork(res.ID) {
		if addr, err := netip.ParseAddr(p.addressOfNIC(nic.ID)); err == nil {
			_ = alloc.Reserve(addr)
		}
	}
	return alloc, nil
}

// ensureBackingNetwork asks the machine driver for a real network carrying the
// block. Failure is logged, never fatal: the control plane must keep answering
// when no runtime is available, which is the default configuration.
func (p *Pack) ensureBackingNetwork(ctx context.Context, res *resource.Resource, prefix netip.Prefix) error {
	if p.env.Machines == nil {
		return nil
	}
	name := machine.NetworkName(machine.NetworkPrefix, res.ID)
	if res.Runtime == nil {
		res.Runtime = map[string]string{}
	}
	res.Runtime[runtimeNetworkKey] = name

	gateway := prefix.Masked().Addr().Next()
	return p.env.Machines.EnsureNetwork(ctx, machine.NetworkSpec{
		Name:    name,
		CIDR:    prefix.String(),
		Gateway: gateway.String(),
		NAT:     true,
		Labels: map[string]string{
			"feint.provider":        Name,
			"feint.private-network": res.ID,
		},
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
	}
	for k, v := range res.Attrs {
		if k == "subnet" {
			continue
		}
		out[k] = v
	}
	// The wire shape carries a list of subnet objects, not the bare block the
	// store keeps: a client decodes them into vpc.Subnet.
	subnet, _ := res.Attrs["subnet"].(string)
	out["subnets"] = []any{map[string]any{
		"id":                 subnetIDOf(res.ID),
		"subnet":             subnet,
		"project_id":         res.Attrs["project_id"],
		"private_network_id": res.ID,
		"vpc_id":             res.Attrs["vpc_id"],
		"region":             res.Tenant.Zone,
		"created_at":         res.Created.Format(time.RFC3339),
		"updated_at":         res.Updated.Format(time.RFC3339),
	}}
	return out
}

// subnetIDOf derives the subnet identifier from its Private Network.
//
// Derived rather than random so it survives a restart, and distinct from the
// network's own id because they are two resources: a client that stores both
// would otherwise hold one UUID for two things, and could not tell which it was
// looking at.
func subnetIDOf(privateNetworkID string) string {
	sum := sha256.Sum256([]byte("subnet:" + privateNetworkID))
	hexed := hex.EncodeToString(sum[:])
	return hexed[0:8] + "-" + hexed[8:12] + "-4" + hexed[13:16] + "-8" + hexed[17:20] + "-" + hexed[20:32]
}
