package scaleway

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
	"unicode"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// The refusals the attachment paths answer from inside the store lock, where
// the attachment they judge cannot move underneath them (#295).
var (
	errIPOnStandardResource = errors.New("the IP is attached to a standard resource")
	errIPNotAttached        = errors.New("the IP is not attached to a resource")
	errIPOnAnotherResource  = errors.New("the IP is attached to another resource")
)

// IPAM is how a client learns the address of a private NIC, and serving it is
// not optional: instance/v1.PrivateNIC carries no address at all. It carries
// `ipam_ip_ids`, and the client follows them here.
//
// This emulator got that wrong first: it served invented `private_ip` and
// `private_ips` fields on the NIC, and its own conformance suite read them back,
// so the suite proved the emulator against itself rather than against the API.
// That is the one mistake this project cannot afford, since its whole claim is
// that the official client sees no difference.
//
// Since SW-4 the product is read-write: BookIP reserves an address out of a
// Private Network's own subnet, the same pool the NIC allocator draws from, so a
// booked address and an allocated one can never collide. The Terraform provider
// depends on the whole lifecycle (measured in its services/ipam/ip.go): BookIP,
// GetIP, UpdateIP, MoveIP, DetachIP, ReleaseIP.
//
// Shapes come from the SDK (api/ipam/v1/ipam_sdk.go): IP, Source, Resource, and
// ListIPsResponse. Note the address is an scw.IPNet, so it carries its mask;
// serving a bare address makes the SDK fail to decode what it just received.

const kindIPAMIP = "ipam/ip"

// runtimeNICKey links an IPAM address to the private NIC holding it.
const runtimeNICKey = "nic"

// resourceTypePrivateNIC is what the SDK calls a NIC of an Instance server.
const resourceTypePrivateNIC = "instance_private_nic"

// resourceTypeCustom is the SDK's name for a resource IPAM does not manage: a
// MAC and an optional name, attached through AttachIP rather than by a product.
const resourceTypeCustom = "custom"

// The two product holders this pack serves beside the NICs, named exactly as
// ipam/v1's ResourceType enum spells them (ipam_sdk.go: ResourceTypeLBServer,
// ResourceTypeVpcGatewayNetwork). The Terraform provider filters ListIPs on
// these strings, so a different spelling would answer an empty list.
const (
	resourceTypeLBServer       = "lb_server"
	resourceTypeGatewayNetwork = "vpc_gateway_network"
)

// attrBooked marks an address a client reserved through BookIP, as opposed to
// one the pack allocated for a NIC. The difference decides what a NIC delete
// does: a booked address is detached and survives, an allocated one goes back
// to the pool by disappearing. Never rendered by the view: upstream has no such
// field, the distinction is this pack's own bookkeeping.
const attrBooked = "booked"

// Custom-resource attachment, the AttachIP/DetachIP/MoveIP half of the product.
// Stored as attrs because the wire shape rebuilds the resource object from them.
const (
	attrCustomMAC  = "custom_mac"
	attrCustomName = "custom_name"
)

// Product-holder attachment: an address held by another emulated product — a
// Load Balancer attached to a Private Network (resource type `lb_server`), or
// a Public Gateway's connection (`vpc_gateway_network`). Stored as attrs for
// the same reason the custom attachment is: the wire shape rebuilds the
// resource object from them, and the Terraform provider reads these addresses
// back through ListIPs filtered by resource_id + resource_type (measured in
// its services/vpcgw/helpers.go setPrivateIPs and services/lb/helpers_lb.go
// getLBPrivateIPs).
const (
	attrHolderType = "holder_type" // an ipam/v1 ResourceType name, e.g. lb_server
	attrHolderID   = "holder_id"   // ID of the holding resource
	attrHolderName = "holder_name" // name the holder publishes, when it has one
	attrHolderMAC  = "holder_mac"  // MAC of the holder's interface in the network
)

// ipamBookStatus is what a successful BookIP answers with.
//
// 200, not the 201 a create writes by habit. Measured on the wire on
// 2026-08-24: `scw ipam ip create` against a real fr-par account answered 200,
// recorded through `feint proxy` into corpus/scaleway/scw-free-shapes.jsonl,
// and `feint corpus --check` reported this pack's 201 against it (#427). Read
// off the transcript rather than off the CLI's exit code, which shows neither.
//
// Only ipam/v1's own create was measured, so only it is touched — the same
// bound vpcCreateStatus states for vpc/v2. A status is part of the answer, and
// an invented one is an invented format (rule 4).
//
// TestBookIPAnswersWhatTheRealCloudAnswers fails without it.
const ipamBookStatus = http.StatusOK

type ipamSourceRequest struct {
	PrivateNetworkID *string `json:"private_network_id"`
	SubnetID         *string `json:"subnet_id"`
	Zonal            *string `json:"zonal"`
	VpcID            *string `json:"vpc_id"`
	Regional         *bool   `json:"regional"`
}

type customResourceRequest struct {
	MacAddress string  `json:"mac_address"`
	Name       *string `json:"name"`
}

type bookIPRequest struct {
	ProjectID string                 `json:"project_id"`
	Source    *ipamSourceRequest     `json:"source"`
	IsIPv6    bool                   `json:"is_ipv6"`
	Address   *string                `json:"address"`
	Tags      []string               `json:"tags"`
	Resource  *customResourceRequest `json:"resource"`
}

type updateIPAMIPRequest struct {
	Tags     *[]string        `json:"tags"`
	Reverses []map[string]any `json:"reverses"`
}

type attachIPRequest struct {
	Resource *customResourceRequest `json:"resource"`
}

type moveIPRequest struct {
	FromResource *customResourceRequest `json:"from_resource"`
	ToResource   *customResourceRequest `json:"to_resource"`
}

type releaseIPSetRequest struct {
	IPIDs []string `json:"ip_ids"`
}

func (p *Pack) listIPAMIPs(w http.ResponseWriter, r *http.Request) {
	region, ok := regionOf(w, r)
	if !ok {
		return
	}

	all := p.env.Store.List(kindIPAMIP, p.regionScopeOf(r, region))
	q := r.URL.Query()
	// The filters a client actually sends, measured: the Terraform provider
	// resolves a NIC's addresses with resource_id + resource_type +
	// private_network_id + project_id, and `scw ipam ip list` adds the rest.
	if id := q.Get("resource_id"); id != "" {
		all = filterResources(all, func(res *resource.Resource) bool {
			return res.Runtime[runtimeNICKey] == id || res.Attrs[attrHolderID] == id
		})
	}
	if id := q.Get("private_network_id"); id != "" {
		all = filterResources(all, func(res *resource.Resource) bool {
			return res.Attrs["private_network_id"] == id
		})
	}
	if id := q.Get("subnet_id"); id != "" {
		all = filterResources(all, func(res *resource.Resource) bool {
			return res.Attrs["subnet_id"] == id
		})
	}
	// Both spellings land on the same store field: vpc_id filters by the VPC of
	// the resource holding the IP, source_vpc_id by the VPC of the source pool,
	// and every address served here has both in the same place.
	for _, key := range []string{"vpc_id", "source_vpc_id"} {
		if id := q.Get(key); id != "" {
			all = filterResources(all, func(res *resource.Resource) bool {
				return res.Attrs["vpc_id"] == id
			})
		}
	}
	if attached := q.Get("attached"); attached != "" {
		want := attached == "true"
		all = filterResources(all, func(res *resource.Resource) bool {
			return ipamAttached(res) == want
		})
	}
	if rt := q.Get("resource_type"); rt != "" && rt != "unknown_type" {
		all = filterResources(all, func(res *resource.Resource) bool {
			return ipamResourceType(res) == rt
		})
	}
	// The plural spellings of the two filters above: an IP matches when its
	// holder is any of the named resources or types.
	if ids := idSet(q, "resource_ids"); ids != nil {
		all = filterResources(all, func(res *resource.Resource) bool {
			held, _ := res.Attrs[attrHolderID].(string)
			return ids[res.Runtime[runtimeNICKey]] || (held != "" && ids[held])
		})
	}
	if types := csvValues(q, "resource_types"); len(types) > 0 {
		all = filterResources(all, func(res *resource.Resource) bool {
			return contains(types, ipamResourceType(res))
		})
	}
	// "IPs attached to a resource with this string within their name". The
	// holders that carry a name here are the custom resources a client attached
	// by name and the product holders that publish one (a Load Balancer's name);
	// a NIC's holder publishes name: null, and null contains nothing.
	if name := q.Get("resource_name"); name != "" {
		all = filterResources(all, func(res *resource.Resource) bool {
			held, _ := res.Attrs[attrCustomName].(string)
			if held == "" {
				held, _ = res.Attrs[attrHolderName].(string)
			}
			return held != "" && strings.Contains(held, name)
		})
	}
	// "One or more matching tags": a disjunction, unlike instance/v1's.
	if tags := csvValues(q, "tags"); len(tags) > 0 {
		all = filterResources(all, func(res *resource.Resource) bool {
			return hasAnyTag(res, tags)
		})
	}
	if mac := q.Get("mac_address"); mac != "" {
		all = filterResources(all, func(res *resource.Resource) bool {
			return ipamMAC(res) == mac
		})
	}
	if ids := q["ip_ids"]; len(ids) > 0 {
		wanted := make(map[string]bool, len(ids))
		for _, id := range ids {
			wanted[id] = true
		}
		all = filterResources(all, func(res *resource.Resource) bool {
			return wanted[res.ID]
		})
	}
	// Every address served here is IPv4 and regional: a zonal filter matches
	// nothing, an is_ipv6=true filter matches nothing, and their opposites keep
	// everything.
	if q.Get("is_ipv6") == "true" || q.Get("zonal") != "" {
		all = all[:0]
	}

	// Four of the five orders their enum declares are served; attached_at is
	// refused because this emulator records no attachment time, and sorting by
	// a stand-in would answer an order nobody asked for (docs/limits.md).
	if by := q.Get("order_by"); by == "attached_at_asc" || by == "attached_at_desc" {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "order_by",
			Reason:       "constraint",
			HelpMessage:  "feint records no attachment time; order by created_at, updated_at, ip_address or mac_address instead",
		})
		return
	}
	if !orderResources(w, r, "order_by", "created_at_desc", map[string]resourceCmp{
		"created_at":  cmpCreated,
		"updated_at":  cmpUpdated,
		"ip_address":  cmpIPAMAddress,
		"mac_address": func(a, b *resource.Resource) int { return strings.Compare(ipamMAC(a), ipamMAC(b)) },
	}, all) {
		return
	}

	page := parsePage(r)
	start, end := page.slice(len(all))
	ips := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		ips = append(ips, p.ipamIPView(res))
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"ips":         ips,
		"total_count": len(all),
	})
}

// cmpIPAMAddress orders by the address itself, numerically: "10.0.0.9" sorts
// before "10.0.0.10", which a string comparison would invert.
func cmpIPAMAddress(a, b *resource.Resource) int {
	pa, errA := netip.ParsePrefix(textOf(a.Attrs["address"]))
	pb, errB := netip.ParsePrefix(textOf(b.Attrs["address"]))
	if errA != nil || errB != nil {
		return strings.Compare(textOf(a.Attrs["address"]), textOf(b.Attrs["address"]))
	}
	return pa.Addr().Compare(pb.Addr())
}

func (p *Pack) getIPAMIP(w http.ResponseWriter, r *http.Request) {
	res, ok := p.resourceOf(w, r, kindIPAMIP, "ipID", "ip")
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.ipamIPView(res))
}

// bookIP reserves an address in a Private Network's subnet, which is the same
// pool the NIC allocator draws from: both go through allocatorFor under
// p.lockAddresses(), so a booked address and an allocated one cannot collide.
//
// Only the Private Network source is served. Upstream says the same thing of
// itself — "not all sources are available for reservation", and picking a
// specific address is documented as Private-Network-only — and the emulator has
// no zonal or regional public pool to book from: its public addresses belong to
// the instance product.
func (p *Pack) bookIP(w http.ResponseWriter, r *http.Request) {
	region, ok := regionOf(w, r)
	if !ok {
		return
	}

	var req bookIPRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.IsIPv6 {
		// The Private Network publishes an IPv6 subnet (#270), but the machine
		// runtime and the allocator behind it are IPv4: booking from the IPv6
		// block would hand out an address no emulated machine ever carries,
		// which is the exact lie this project exists to avoid.
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "is_ipv6",
			Reason:       "constraint",
			HelpMessage:  "the emulated machines carry IPv4 only; the IPv6 subnet is published but no address can be booked from it",
		})
		return
	}
	pn, ok := p.bookSourceOf(w, region, req.Source)
	if !ok {
		return
	}

	// The Project must match the Private Network's, which is upstream's own
	// documented constraint on BookIP.
	project, _ := projectOf(req.ProjectID)
	if pnProject, _ := pn.Attrs["project_id"].(string); pnProject != project {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "project_id",
			Reason:       "constraint",
			HelpMessage:  "the project must match the private network's project",
		})
		return
	}

	// The custom resource is optional on a book: validated only when present.
	var custom *customResourceRequest
	if req.Resource != nil {
		var ok bool
		custom, ok = customResourceOf(w, req.Resource, "resource")
		if !ok {
			return
		}
	}

	// Held from rebuild to Put, like every allocation in this pack: released
	// earlier, a concurrent create rebuilds from a store that does not know
	// about this address yet and hands it out again.
	unlock := p.lockAddresses()
	defer unlock()

	alloc, err := p.allocatorFor(pn)
	if err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "source", Reason: "constraint", HelpMessage: err.Error()})
		return
	}

	var addr netip.Addr
	if req.Address != nil && *req.Address != "" {
		addr, err = netip.ParseAddr(*req.Address)
		if err != nil {
			writeInvalidArguments(w, ArgumentError{ArgumentName: "address", Reason: "format", HelpMessage: err.Error()})
			return
		}
		// Reserve refuses an address outside the subnet, one of the reserved
		// bottom addresses, and one already handed out. All three are the
		// client's request being impossible, not a server fault.
		if err := alloc.Reserve(addr); err != nil {
			writeInvalidArguments(w, ArgumentError{ArgumentName: "address", Reason: "constraint", HelpMessage: err.Error()})
			return
		}
	} else {
		addr, err = alloc.Allocate()
		if err != nil {
			// An exhausted block is a real answer: the client asked for one more
			// address than the subnet holds. Same shape as the NIC path.
			writePrecondition(w, "private_network", pn.ID,
				"no address left in "+alloc.Prefix().String())
			return
		}
	}

	now := p.env.Now()
	res := resource.New(p.env.NewID(), kindIPAMIP, resource.Tenant{Provider: Name, Project: project, Zone: region}, "available", now)
	res.Attrs = map[string]any{
		"address":            netip.PrefixFrom(addr, alloc.Prefix().Bits()).String(),
		"project_id":         project,
		"is_ipv6":            false,
		"tags":               orEmpty(req.Tags),
		"private_network_id": pn.ID,
		"vpc_id":             pn.Attrs["vpc_id"],
		"subnet_id":          subnetIDOf(pn.ID),
		attrBooked:           true,
	}
	if custom != nil {
		res.Attrs[attrCustomMAC] = custom.MacAddress
		if custom.Name != nil {
			res.Attrs[attrCustomName] = *custom.Name
		}
	}
	p.env.Store.Put(res)

	emulator.WriteJSON(w, ipamBookStatus, p.ipamIPView(res))
}

// bookSourceOf resolves the Private Network a BookIP names, through either of
// the two spellings a client uses: the network itself, or its subnet.
func (p *Pack) bookSourceOf(w http.ResponseWriter, region string, source *ipamSourceRequest) (*resource.Resource, bool) {
	if source == nil || (source.PrivateNetworkID == nil && source.SubnetID == nil) {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "source",
			Reason:       "constraint",
			HelpMessage:  "only a private network source can be booked here: name private_network_id or subnet_id",
		})
		return nil, false
	}
	if source.PrivateNetworkID != nil {
		pn, found := p.env.Store.Get(Name, kindPrivateNetwork, *source.PrivateNetworkID)
		if !found || pn.Tenant.Zone != region {
			writeNotFound(w, "private_network", *source.PrivateNetworkID)
			return nil, false
		}
		return pn, true
	}
	for _, pn := range p.env.Store.List(kindPrivateNetwork, resource.Tenant{Provider: Name}) {
		if subnetIDOf(pn.ID) == *source.SubnetID && pn.Tenant.Zone == region {
			return pn, true
		}
	}
	writeNotFound(w, "subnet", *source.SubnetID)
	return nil, false
}

// customResourceOf validates the custom resource of an attach-family request.
// The MAC has to parse, and the name must carry no control character: it is
// stored and rendered back, and a value that cannot ride a single line has no
// business in a name (the same intake rule the SSH keys learned the hard way).
func customResourceOf(w http.ResponseWriter, req *customResourceRequest, argument string) (*customResourceRequest, bool) {
	if req == nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: argument, Reason: "required"})
		return nil, false
	}
	if _, err := net.ParseMAC(req.MacAddress); err != nil {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: argument + ".mac_address",
			Reason:       "format",
			HelpMessage:  err.Error(),
		})
		return nil, false
	}
	if req.Name != nil && strings.ContainsFunc(*req.Name, unicode.IsControl) {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: argument + ".name",
			Reason:       "format",
			HelpMessage:  "the name must not contain control characters",
		})
		return nil, false
	}
	return req, true
}

func (p *Pack) releaseIPAMIP(w http.ResponseWriter, r *http.Request) {
	res, ok := p.resourceOf(w, r, kindIPAMIP, "ipID", "ip")
	if !ok {
		return
	}
	if !p.releaseOne(w, res) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *Pack) releaseIPAMIPSet(w http.ResponseWriter, r *http.Request) {
	region, ok := regionOf(w, r)
	if !ok {
		return
	}

	var req releaseIPSetRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	// Checked before anything is deleted: a set that fails halfway would leave
	// the client with no way to know which half.
	batch := make([]*resource.Resource, 0, len(req.IPIDs))
	for _, id := range req.IPIDs {
		res, found := p.env.Store.Get(Name, kindIPAMIP, id)
		if !found || res.Tenant.Zone != region {
			writeNotFound(w, "ip", id)
			return
		}
		if ipamAttached(res) {
			writePrecondition(w, "ip", res.ID, "IP is still attached to a resource; detach it first")
			return
		}
		batch = append(batch, res)
	}
	// The same lock, for the same reason releaseOne takes it: a delete frees an
	// address, and freeing one while another request is allocating hands it out
	// twice. Re-read under the lock, since the checks above ran outside it.
	unlock := p.lockAddresses()
	for _, res := range batch {
		current, found := p.env.Store.Get(Name, kindIPAMIP, res.ID)
		if !found || ipamAttached(current) {
			continue
		}
		p.env.Store.Delete(Name, kindIPAMIP, current.ID)
	}
	unlock()
	w.WriteHeader(http.StatusNoContent)
}

// releaseOne refuses to release an attached address, which is what makes a
// wrong destroy order retryable instead of silently breaking the NIC still
// using the address. TestReleasingAnAttachedIPIsRefused fails without it.
func (p *Pack) releaseOne(w http.ResponseWriter, res *resource.Resource) bool {
	// The allocator's lock, held across the attachment check and the delete.
	//
	// bookIP and createPrivateNIC hold it from rebuild to Put, and releasing did
	// not hold it at all — which is the half that makes an address available
	// again. An audit walked the sequence: this reads an unattached address, a
	// NIC create attaches it under the lock, this deletes the IPAM resource, and
	// the next Allocate hands the same address to a second NIC. Two interfaces,
	// one address; under --vm, two machines configured alike.
	//
	// allocatorFor rebuilds occupancy from the IPAM resources alone, so deleting
	// one is exactly what frees an address: the read and the delete belong in the
	// same critical section as the allocation they race.
	// TestAReleasedAddressCannotBeTakenWhileItIsBeingAttached fails without this.
	unlock := p.lockAddresses()
	defer unlock()

	// Re-read under the lock. The caller resolved this resource outside it, and
	// an attachment that landed in between is invisible to a stale clone.
	current, found := p.env.Store.Get(Name, kindIPAMIP, res.ID)
	if !found {
		return true
	}
	if ipamAttached(current) {
		writePrecondition(w, "ip", current.ID, "IP is still attached to a resource; detach it first")
		return false
	}
	p.env.Store.Delete(Name, kindIPAMIP, current.ID)
	return true
}

func (p *Pack) updateIPAMIP(w http.ResponseWriter, r *http.Request) {
	res, ok := p.resourceOf(w, r, kindIPAMIP, "ipID", "ip")
	if !ok {
		return
	}

	var req updateIPAMIPRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	// Inside the store lock: the clone-mutate-Commit shape erased a concurrent
	// write to another field of the same address — the attachment another
	// handler had just acknowledged — after its 200 (#295). Update also keeps
	// an address released while this PATCH was decoding released, which is
	// what Commit was here for (TestCommitDoesNotResurrectAReleasedAddress
	// holds that store-level half).
	var updated *resource.Resource
	err := p.env.Store.Update(Name, kindIPAMIP, res.ID, func(stored *resource.Resource) error {
		if req.Tags != nil {
			stored.Attrs["tags"] = orEmpty(*req.Tags)
		}
		if req.Reverses != nil {
			reverses := make([]any, 0, len(req.Reverses))
			for _, reverse := range req.Reverses {
				reverses = append(reverses, map[string]any{
					"hostname": reverse["hostname"],
					"address":  reverse["address"],
				})
			}
			stored.Attrs["reverses"] = reverses
		}
		stored.Updated = p.env.Now()
		updated = stored
		return nil
	})
	if err != nil {
		writeNotFound(w, "ip", res.ID)
		return
	}

	emulator.WriteJSON(w, http.StatusOK, p.ipamIPView(updated))
}

// attachIPAMIP attaches a custom resource — a MAC the emulator does not manage.
// A standard resource cannot arrive here: the SDK documents that AttachIP fails
// for those, and the products attach their own NICs through their own APIs.
func (p *Pack) attachIPAMIP(w http.ResponseWriter, r *http.Request) {
	res, ok := p.resourceOf(w, r, kindIPAMIP, "ipID", "ip")
	if !ok {
		return
	}

	var req attachIPRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	custom, ok := customResourceOf(w, req.Resource, "resource")
	if !ok {
		return
	}
	if ipamAttached(res) {
		writePrecondition(w, "ip", res.ID, "IP is already attached to a resource")
		return
	}
	if !p.setCustomAttachment(w, res, custom) {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.ipamIPView(res))
}

func (p *Pack) detachIPAMIP(w http.ResponseWriter, r *http.Request) {
	res, ok := p.resourceOf(w, r, kindIPAMIP, "ipID", "ip")
	if !ok {
		return
	}

	var req attachIPRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if !p.detachCustom(w, res, req.Resource, "resource") {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.ipamIPView(res))
}

func (p *Pack) moveIPAMIP(w http.ResponseWriter, r *http.Request) {
	res, ok := p.resourceOf(w, r, kindIPAMIP, "ipID", "ip")
	if !ok {
		return
	}

	var req moveIPRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if !p.detachCustom(w, res, req.FromResource, "from_resource") {
		return
	}
	// A move with no destination is a detach, which detachCustom already
	// persisted.
	if req.ToResource != nil {
		custom, ok := customResourceOf(w, req.ToResource, "to_resource")
		if !ok {
			return
		}
		if !p.setCustomAttachment(w, res, custom) {
			return
		}
	}
	emulator.WriteJSON(w, http.StatusOK, p.ipamIPView(res))
}

// detachCustom validates that the named custom resource is the one attached and
// removes the attachment. The MAC has to match what is attached: detaching by
// id alone would let a caller holding a stale view detach somebody else's
// attachment without noticing.
func (p *Pack) detachCustom(w http.ResponseWriter, res *resource.Resource, req *customResourceRequest, argument string) bool {
	custom, ok := customResourceOf(w, req, argument)
	if !ok {
		return false
	}
	// The checks and the removal run on the stored address, under the store
	// lock, never on the caller's clone: checked outside and committed
	// wholesale, this erased a concurrent write to another field of the same
	// address — its tags — after their 200 (#295). Update also keeps an
	// address released while this was deciding released, which is what the
	// Commit it replaces was here for.
	err := p.env.Store.Update(Name, kindIPAMIP, res.ID, func(stored *resource.Resource) error {
		if stored.Runtime[runtimeNICKey] != "" {
			return errIPOnStandardResource
		}
		current, _ := stored.Attrs[attrCustomMAC].(string)
		if current == "" {
			return errIPNotAttached
		}
		if current != custom.MacAddress {
			return errIPOnAnotherResource
		}
		delete(stored.Attrs, attrCustomMAC)
		delete(stored.Attrs, attrCustomName)
		stored.Updated = p.env.Now()
		return nil
	})
	switch {
	case errors.Is(err, errIPOnStandardResource):
		writePrecondition(w, "ip", res.ID, "IP is attached to a standard resource, managed by its own product")
		return false
	case errors.Is(err, errIPNotAttached):
		writePrecondition(w, "ip", res.ID, "IP is not attached to a resource")
		return false
	case errors.Is(err, errIPOnAnotherResource):
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: argument + ".mac_address",
			Reason:       "constraint",
			HelpMessage:  "the IP is not attached to this resource",
		})
		return false
	case err != nil:
		// Released while this was deciding, which is not an error: the client
		// asked for a detached address and the address is gone entirely.
		return true
	}
	// The caller's clone keeps rendering the view, so it follows the store.
	delete(res.Attrs, attrCustomMAC)
	delete(res.Attrs, attrCustomName)
	return true
}

// setCustomAttachment writes the attachment and reports whether the address
// was still free to take. The exclusivity check runs inside the same critical
// section as the write: on the caller's clone, two concurrent attaches both
// passed it and the loser silently overwrote the winner (#295).
func (p *Pack) setCustomAttachment(w http.ResponseWriter, res *resource.Resource, custom *customResourceRequest) bool {
	err := p.env.Store.Update(Name, kindIPAMIP, res.ID, func(stored *resource.Resource) error {
		if ipamAttached(stored) {
			mac, _ := stored.Attrs[attrCustomMAC].(string)
			if mac != custom.MacAddress {
				return errIPOnAnotherResource
			}
		}
		stored.Attrs[attrCustomMAC] = custom.MacAddress
		delete(stored.Attrs, attrCustomName)
		if custom.Name != nil {
			stored.Attrs[attrCustomName] = *custom.Name
		}
		stored.Updated = p.env.Now()
		return nil
	})
	switch {
	case errors.Is(err, errIPOnAnotherResource):
		writePrecondition(w, "ip", res.ID, "IP is already attached to a resource")
		return false
	case err != nil:
		writeNotFound(w, "ip", res.ID)
		return false
	}
	// The caller's clone keeps rendering the view, so it follows the store.
	res.Attrs[attrCustomMAC] = custom.MacAddress
	delete(res.Attrs, attrCustomName)
	if custom.Name != nil {
		res.Attrs[attrCustomName] = *custom.Name
	}
	return true
}

// newIPAMIP records the address a private NIC received, as the resource a
// client resolves it through.
func (p *Pack) newIPAMIP(region, project string, address netip.Prefix, nic, pn *resource.Resource) *resource.Resource {
	now := p.env.Now()
	return &resource.Resource{
		ID:      p.env.NewID(),
		Kind:    kindIPAMIP,
		Tenant:  resource.Tenant{Provider: Name, Project: project, Zone: region},
		State:   "available",
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"address":            address.String(),
			"project_id":         project,
			"is_ipv6":            false,
			"tags":               []string{},
			"private_network_id": pn.ID,
			"vpc_id":             pn.Attrs["vpc_id"],
			"subnet_id":          subnetIDOf(pn.ID),
			"mac_address":        nic.Attrs["mac_address"],
			"zone":               nic.Tenant.Zone,
		},
		Runtime: map[string]string{runtimeNICKey: nic.ID},
	}
}

// newHeldIPAMIP records the address a product resource received in a Private
// Network — a Load Balancer attachment, a Public Gateway connection — as the
// resource the Terraform provider resolves it through (ListIPs filtered by
// resource_id + resource_type). The caller holds the allocation lock and has
// already reserved the address.
func (p *Pack) newHeldIPAMIP(region, project string, address netip.Prefix, pn *resource.Resource, holderType, holderID, holderName, mac string) *resource.Resource {
	now := p.env.Now()
	return &resource.Resource{
		ID:      p.env.NewID(),
		Kind:    kindIPAMIP,
		Tenant:  resource.Tenant{Provider: Name, Project: project, Zone: region},
		State:   "available",
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"address":            address.String(),
			"project_id":         project,
			"is_ipv6":            false,
			"tags":               []string{},
			"private_network_id": pn.ID,
			"vpc_id":             pn.Attrs["vpc_id"],
			"subnet_id":          subnetIDOf(pn.ID),
			attrHolderType:       holderType,
			attrHolderID:         holderID,
			attrHolderName:       holderName,
			attrHolderMAC:        mac,
		},
	}
}

// holdIPAMIP marks a booked address as held by a product resource, refusing an
// address that is already someone else's. The exclusivity check runs inside
// the store's own critical section, the #295 discipline: on a clone, two
// concurrent holders both pass it and the loser silently overwrites the winner.
func (p *Pack) holdIPAMIP(id, holderType, holderID, holderName, mac string) error {
	return p.env.Store.Update(Name, kindIPAMIP, id, func(stored *resource.Resource) error {
		if ipamAttached(stored) && stored.Attrs[attrHolderID] != holderID {
			return errIPOnAnotherResource
		}
		stored.Attrs[attrHolderType] = holderType
		stored.Attrs[attrHolderID] = holderID
		stored.Attrs[attrHolderName] = holderName
		stored.Attrs[attrHolderMAC] = mac
		stored.Updated = p.env.Now()
		return nil
	})
}

// releaseHeldIPAMIPs undoes what an attachment did to IPAM: an address the
// pack created for the holder disappears, an address the client booked first
// survives, unheld, exactly as a NIC delete treats a booked address.
func (p *Pack) releaseHeldIPAMIPs(holderID string) {
	unlock := p.lockAddresses()
	defer unlock()
	for _, res := range p.env.Store.List(kindIPAMIP, resource.Tenant{Provider: Name}) {
		if held, _ := res.Attrs[attrHolderID].(string); held != holderID {
			continue
		}
		if booked, _ := res.Attrs[attrBooked].(bool); booked {
			_ = p.env.Store.Update(Name, kindIPAMIP, res.ID, func(stored *resource.Resource) error {
				delete(stored.Attrs, attrHolderType)
				delete(stored.Attrs, attrHolderID)
				delete(stored.Attrs, attrHolderName)
				delete(stored.Attrs, attrHolderMAC)
				stored.Updated = p.env.Now()
				return nil
			})
			continue
		}
		p.env.Store.Delete(Name, kindIPAMIP, res.ID)
	}
}

// ipamIPsOf returns the addresses held by a NIC, which is what the NIC view
// publishes as ipam_ip_ids.
func (p *Pack) ipamIPsOf(nicID string) []*resource.Resource {
	all := p.env.Store.List(kindIPAMIP, resource.Tenant{Provider: Name})
	out := make([]*resource.Resource, 0, len(all))
	for _, res := range all {
		if res.Runtime[runtimeNICKey] == nicID {
			out = append(out, res)
		}
	}
	return out
}

// ipamIPsOnNetwork returns every address living in a Private Network's subnet,
// booked or NIC-held. This is what the allocator is rebuilt from: rebuilding
// from the NICs alone would hand a booked address to the next NIC.
// TestABookedAddressIsNotHandedToTheNextNIC fails without it.
func (p *Pack) ipamIPsOnNetwork(privateNetworkID string) []*resource.Resource {
	all := p.env.Store.List(kindIPAMIP, resource.Tenant{Provider: Name})
	out := make([]*resource.Resource, 0, len(all))
	for _, res := range all {
		if res.Attrs["private_network_id"] == privateNetworkID {
			out = append(out, res)
		}
	}
	return out
}

// addressOfNIC returns the address a NIC holds, empty when it holds none. The
// pack needs it to drive the machine; a client goes through IPAM instead.
func (p *Pack) addressOfNIC(nicID string) string {
	for _, ip := range p.ipamIPsOf(nicID) {
		if address, _ := ip.Attrs["address"].(string); address != "" {
			if prefix, err := netip.ParsePrefix(address); err == nil {
				return prefix.Addr().String()
			}
		}
	}
	return ""
}

func ipamAttached(res *resource.Resource) bool {
	if res.Runtime[runtimeNICKey] != "" {
		return true
	}
	if holder, _ := res.Attrs[attrHolderType].(string); holder != "" {
		return true
	}
	mac, _ := res.Attrs[attrCustomMAC].(string)
	return mac != ""
}

func ipamResourceType(res *resource.Resource) string {
	if res.Runtime[runtimeNICKey] != "" {
		return resourceTypePrivateNIC
	}
	if holder, _ := res.Attrs[attrHolderType].(string); holder != "" {
		return holder
	}
	if mac, _ := res.Attrs[attrCustomMAC].(string); mac != "" {
		return resourceTypeCustom
	}
	return ""
}

func ipamMAC(res *resource.Resource) string {
	if res.Runtime[runtimeNICKey] != "" {
		mac, _ := res.Attrs["mac_address"].(string)
		return mac
	}
	if holder, _ := res.Attrs[attrHolderType].(string); holder != "" {
		mac, _ := res.Attrs[attrHolderMAC].(string)
		return mac
	}
	mac, _ := res.Attrs[attrCustomMAC].(string)
	return mac
}

func (p *Pack) ipamIPView(res *resource.Resource) map[string]any {
	out := map[string]any{
		"id":         res.ID,
		"region":     res.Tenant.Zone,
		"created_at": res.Created.Format(time.RFC3339),
		"updated_at": res.Updated.Format(time.RFC3339),
		"reverses":   []any{},
	}
	for _, key := range []string{"address", "project_id", "is_ipv6", "tags", "zone"} {
		out[key] = res.Attrs[key]
	}
	if reverses, ok := res.Attrs["reverses"].([]any); ok {
		out["reverses"] = reverses
	}

	// Source says where the address comes from, Resource says what holds it.
	// Both are objects the SDK decodes; flattening them is how a client ends up
	// with zero values it never notices.
	out["source"] = map[string]any{
		"private_network_id": res.Attrs["private_network_id"],
		"subnet_id":          res.Attrs["subnet_id"],
		"vpc_id":             res.Attrs["vpc_id"],
	}
	// An unattached address carries no resource, which the SDK reads as a nil
	// pointer. An object of zero values instead would tell a client the IP is
	// held by a nameless something.
	switch holderType := ipamResourceType(res); holderType {
	case resourceTypePrivateNIC:
		out["resource"] = map[string]any{
			"type":        resourceTypePrivateNIC,
			"id":          res.Runtime[runtimeNICKey],
			"mac_address": res.Attrs["mac_address"],
			"name":        nil,
		}
	case resourceTypeLBServer, resourceTypeGatewayNetwork:
		var name any
		if v, ok := res.Attrs[attrHolderName].(string); ok && v != "" {
			name = v
		}
		out["resource"] = map[string]any{
			"type":        holderType,
			"id":          res.Attrs[attrHolderID],
			"mac_address": res.Attrs[attrHolderMAC],
			"name":        name,
		}
	case resourceTypeCustom:
		var name any
		if v, ok := res.Attrs[attrCustomName].(string); ok {
			name = v
		}
		out["resource"] = map[string]any{
			"type":        resourceTypeCustom,
			"id":          "",
			"mac_address": res.Attrs[attrCustomMAC],
			"name":        name,
		}
	default:
		out["resource"] = nil
	}
	return out
}

func filterResources(all []*resource.Resource, keep func(*resource.Resource) bool) []*resource.Resource {
	out := all[:0]
	for _, res := range all {
		if keep(res) {
			out = append(out, res)
		}
	}
	return out
}
