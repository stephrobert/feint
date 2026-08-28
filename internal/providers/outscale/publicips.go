package outscale

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Public IPs: allocate, list, release.
//
// The addresses come from 198.51.100.0/24 — TEST-NET-2, reserved by RFC 5737
// for documentation and never routed. An emulator that handed out addresses
// from a real public block would let a test half-work against the real
// internet, which is worse than an address that visibly goes nowhere.
// ReadPublicIpRanges publishes the same block, so the catalogue and the
// allocator cannot disagree.
//
// TEST-NET-2 and not TEST-NET-3, which every pack used to draw from: a linked
// address is routed on the host now, and one host serves three emulated
// clouds, so two packs handing out the same "public" /32 would route it to
// two machines and let ARP order pick the winner. RFC 5737 reserves exactly
// three blocks; each pack owns one (Scaleway keeps TEST-NET-3, Exoscale takes
// TEST-NET-1), and the guard of each refuses the other two.
//
// LinkPublicIp and UnlinkPublicIp move the record AND the packet: a linked
// address is routed to the Vm's machine the way a Scaleway flexible IP is, so
// `ssh outscale@<PublicIp>` opens a shell — that is the address a real
// Outscale user logs into, and for a long time this pack only moved the
// record. The limit was real while machines sat on the operator's default
// profile bridge, which the driver rightly refuses to route through; the
// machines live on emulator-owned networks now, and the limit went with it.
// (They were declined even earlier, which failed the provider's own
// examples/net_vm fixture partway through its apply.)
//
// Shape measured (X-2 sweep, 2026-08-08): an unlinked address carries exactly
// PublicIp, PublicIpId and Tags — no Vm/Nic/NatService keys, not even empty.
// One consumed by a NAT service gains NatServiceId and LinkPublicIpId; one
// linked to a machine gains VmId, NicId, NicAccountId and PrivateIp.

const kindPublicIP = "publicip"

// publicIPBase is the fictional block addresses are allocated from,
// sequentially from .1: deterministic, so a test can pin the first address.
const publicIPBase = "198.51.100."

// publicIPBlock is the same block as a prefix, for the guard below and for the
// bound of the allocator's sweep.
//
// It is a /28 — fourteen addresses — and not the /24 it was until 2026-08-25.
// The assertion this block exists to hold is that allocation refuses past the
// last address with a typed 9029, and that a released address returns to the
// pool; neither depends on how many addresses there are. Exhausting a /24 cost
// the Outscale conformance suite 254 `DeletePublicIp` calls, and at ~730 ms of
// client startup each that was 64% of the suite and the larger half of an
// eleven-minute workflow. The measured peak of addresses held at once, across
// every suite and fixture in this repository, is two.
const publicIPBlock = "198.51.100.0/28"

// publicIPPrefix is the block parsed once, for the Reconciler's route guard.
var publicIPPrefix = netip.MustParsePrefix(publicIPBlock)

// publicIPHosts is how many addresses publicIPBlock holds, derived from the
// prefix rather than written a second time. A bound that does not follow the
// prefix is exactly how an allocator and the catalogue it publishes come to
// disagree — which is why TestTheAllocatorStopsWhereTheCatalogueSaysItDoes
// walks the published range to its end and asserts the refusal lands on the
// address after it, and fails if either side is edited alone.
var publicIPHosts = func() int {
	prefix, err := netip.ParsePrefix(publicIPBlock)
	if err != nil {
		panic("publicIPBlock is not a prefix: " + err.Error())
	}
	// Minus the network and broadcast addresses, which nobody is handed.
	return 1<<(32-prefix.Bits()) - 2
}()

// emulatedPublicIP reports whether an address is one this pack can have handed
// out: inside publicIPBlock. It is the authorisation half on the way to the
// driver — a stored address is restored verbatim by PUT /_feint/state and
// `feint snapshot load`, and routing an arbitrary value would send the host's
// traffic for that address into a container.
// TestAPoisonedPublicIpIsNeverRouted fails without it.
func emulatedPublicIP(address string) bool {
	prefix, err := netip.ParsePrefix(publicIPBlock)
	if err != nil {
		return false
	}
	addr, err := netip.ParseAddr(address)
	if err != nil {
		return false
	}
	return prefix.Contains(addr)
}

// routeLinkedIP makes a linked public address reach the Vm's machine, the way
// the real cloud routes an EIP to its instance. LinkPublicIp used to move the
// record and no packet — a stated limit while nothing on the host could carry
// the address; the machines sit on emulator-owned networks now, so the limit
// stopped being one. Degrades quietly, like every runtime call in this pack.
// TestLinkPublicIpRoutesTheAddress fails without it.
func (p *Pack) routeLinkedIP(ctx context.Context, address string, vm *resource.Resource) {
	// Through the shared Reconciler, which holds the block guard and the
	// router assertion once for the three packs (#510). This pack's own half
	// stays at the control plane: a linked address moves only when the request
	// passes AllowRelink, the real API's default (#213).
	p.reconciler().Route(ctx, vm, address)
}

// unrouteLinkedIP takes the route back. The machine may already be gone; the
// driver treats that as nothing left to undo, and on OVN it still withdraws
// the uplink route, which outlives the machine.
func (p *Pack) unrouteLinkedIP(ctx context.Context, address string, vm *resource.Resource) {
	machineName := ""
	if vm != nil {
		machineName = vm.Runtime[p.binding().RuntimeKey]
	}
	p.reconciler().Unroute(ctx, machineName, address)
}

// publicBootAddresses is what a Vm's machine must answer on from its first
// boot: the public address linked to it, when one is. On the launch rather
// than routed afterwards, because editing a live OVN NIC re-plugs it.
func (p *Pack) publicBootAddresses(vmID string) []string {
	if address := p.publicIPOf(vmID); emulatedPublicIP(address) {
		return []string{address}
	}
	return nil
}

// releaseVmPublicIPs unlinks and unroutes every public address a Vm holds,
// which is what terminating the Vm does upstream: the address stays allocated,
// and stops naming a machine that no longer exists.
// TestTerminateReleasesTheLinkedPublicIp fails without it.
func (p *Pack) releaseVmPublicIPs(ctx context.Context, vm *resource.Resource) {
	for _, address := range p.env.Store.List(kindPublicIP, resource.Tenant{Provider: Name}) {
		if stringOf(address.Attrs["VmId"]) != vm.ID {
			continue
		}
		p.unrouteLinkedIP(ctx, stringOf(address.Attrs["PublicIp"]), vm)
		p.releasePublicIP(address)
	}
}

func (p *Pack) createPublicIP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DryRun *bool `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}

	// Serialized like every other allocation: two concurrent creates must not
	// pick the same address.
	unlock := p.lockAddresses()
	taken := make(map[string]bool, 8)
	for _, res := range p.env.Store.List(kindPublicIP, resource.Tenant{Provider: Name}) {
		taken[stringOf(res.Attrs["PublicIp"])] = true
	}
	address := ""
	for host := 1; host <= publicIPHosts; host++ {
		candidate := fmt.Sprintf("%s%d", publicIPBase, host)
		if !taken[candidate] {
			address = candidate
			break
		}
	}
	if address == "" {
		unlock()
		p.conflict(w, "the emulated public block "+publicIPBlock+" is exhausted; release an address first")
		return
	}
	now := p.env.Now()
	res := resource.New(newID("eipalloc", p.env.NewID()), kindPublicIP, resource.Tenant{Provider: Name}, "available", now)
	res.Attrs = map[string]any{
		"PublicIp": address,
		"Tags":     []any{},
	}
	p.env.Store.Put(res)
	unlock()

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"PublicIp":        publicIPView(res),
		"ResponseContext": p.context(),
	})
}

// publicIPFilters are what a stored address can answer. LinkPublicIpIds is here
// because the Terraform provider reads the link back by its own id right after
// creating it — without it, `outscale_public_ip_link` fails the apply.
var publicIPFilters = stringFilters("PublicIpIds", "PublicIps", "LinkPublicIpIds", "VmIds", "NicIds")

func (p *Pack) readPublicIPs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filters        filterSet `json:"Filters"`
		ResultsPerPage *int      `json:"ResultsPerPage"`
		DryRun         *bool     `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refusePageSize(w, req.ResultsPerPage) {
		return
	}
	if p.refuseFilters(w, req.Filters, publicIPFilters) {
		return
	}

	out := make([]map[string]any, 0)
	for _, res := range p.env.Store.List(kindPublicIP, resource.Tenant{Provider: Name}) {
		if !matchesStrings(req.Filters, "PublicIpIds", res.ID) ||
			!matchesStrings(req.Filters, "PublicIps", stringOf(res.Attrs["PublicIp"])) ||
			!matchesStrings(req.Filters, "LinkPublicIpIds", stringOf(res.Attrs["LinkPublicIpId"])) ||
			!matchesStrings(req.Filters, "VmIds", stringOf(res.Attrs["VmId"])) ||
			!matchesStrings(req.Filters, "NicIds", stringOf(res.Attrs["NicId"])) {
			continue
		}
		out = append(out, publicIPView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"PublicIps":       page(out, pageSize(req.ResultsPerPage)),
		"ResponseContext": p.context(),
	})
}

func (p *Pack) deletePublicIP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PublicIPID string `json:"PublicIpId"`
		PublicIP   string `json:"PublicIp"`
		DryRun     *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	// The request names the address by id or by value, and the API accepts
	// either; by id wins when both are sent.
	var res *resource.Resource
	if req.PublicIPID != "" {
		res, _ = p.env.Store.Get(Name, kindPublicIP, req.PublicIPID)
	} else if req.PublicIP != "" {
		for _, candidate := range p.env.Store.List(kindPublicIP, resource.Tenant{Provider: Name}) {
			if stringOf(candidate.Attrs["PublicIp"]) == req.PublicIP {
				res = candidate
				break
			}
		}
	}
	if res == nil {
		p.notFound(w, "public IP", orDefault(req.PublicIPID, req.PublicIP))
		return
	}
	// An address a NAT service holds does not go; the real API answers the same
	// and the NAT service's teardown is what releases it.
	if natID := stringOf(res.Attrs["NatServiceId"]); natID != "" {
		p.conflict(w, "the public IP "+res.ID+" is held by "+natID)
		return
	}
	// A linked address is unrouted before its record vanishes, or the machine
	// keeps answering on an address the API no longer describes.
	if vmID := stringOf(res.Attrs["VmId"]); vmID != "" {
		if vm, found := p.env.Store.Get(Name, kindVM, vmID); found {
			p.unrouteLinkedIP(r.Context(), stringOf(res.Attrs["PublicIp"]), vm)
		}
	}
	p.env.Store.Delete(Name, kindPublicIP, res.ID)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

// publicIPView emits a holder's fields only when there is a holder: measured, an
// unlinked address carries PublicIp, PublicIpId and Tags and nothing else, while
// one linked to a machine gains VmId, NicId, NicAccountId and PrivateIp, and one
// consumed by a NAT service gains NatServiceId. Absent and empty are not the
// same claim.
func publicIPView(res *resource.Resource) map[string]any {
	out := map[string]any{
		"PublicIpId": res.ID,
		"PublicIp":   stringOf(res.Attrs["PublicIp"]),
		"Tags":       res.Attrs["Tags"],
	}
	if natID := stringOf(res.Attrs["NatServiceId"]); natID != "" {
		out["NatServiceId"] = natID
		out["LinkPublicIpId"] = stringOf(res.Attrs["LinkPublicIpId"])
	}
	if vmID := stringOf(res.Attrs["VmId"]); vmID != "" {
		out["VmId"] = vmID
		out["LinkPublicIpId"] = stringOf(res.Attrs["LinkPublicIpId"])
		out["NicId"] = stringOf(res.Attrs["NicId"])
		out["NicAccountId"] = accountID
		if privateIP := stringOf(res.Attrs["PrivateIp"]); privateIP != "" {
			out["PrivateIp"] = privateIP
		}
	}
	return out
}

// linkPublicIP attaches an address to a machine, and routes it there: with a
// runtime on, the linked address answers from the host, filtered like any
// other traffic to the machine. The address still comes from the
// documented-fictional block, so it answers this host and nothing beyond it.
//
// The reason it is served rather than declined is measured: the provider's own
// examples/net_vm fixture holds an `outscale_public_ip_link`, so declining it
// fails the apply on the eleventh resource of thirteen — and an apply that
// cannot complete proves nothing about the twelve that worked.
//
// The fields it fills come from the read sweep: an address linked to a machine
// answers with VmId, NicId, NicAccountId and PrivateIp beside its own.
func (p *Pack) linkPublicIP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PublicIPID  string `json:"PublicIpId"`
		PublicIP    string `json:"PublicIp"`
		VMID        string `json:"VmId"`
		NicID       string `json:"NicId"`
		AllowRelink *bool  `json:"AllowRelink"`
		DryRun      *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	address := p.publicIPByRef(req.PublicIPID, req.PublicIP)
	if address == nil {
		p.notFound(w, "public IP", orDefault(req.PublicIPID, req.PublicIP))
		return
	}
	if natID := stringOf(address.Attrs["NatServiceId"]); natID != "" {
		p.conflict(w, "the public IP "+address.ID+" is held by "+natID)
		return
	}
	// AllowRelink false is the API's own default, and moving an address that is
	// already placed must be asked for rather than assumed.
	if holder := stringOf(address.Attrs["VmId"]); holder != "" && holder != req.VMID && !boolOr(req.AllowRelink, false) {
		p.conflict(w, "the public IP "+address.ID+" is already linked to "+holder+"; pass AllowRelink to move it")
		return
	}
	if req.VMID == "" && req.NicID == "" {
		p.badRequest(w, "VmId or NicId is required")
		return
	}

	privateIP, nicID := "", req.NicID
	if req.VMID != "" {
		vm, found := p.env.Store.Get(Name, kindVM, req.VMID)
		if !found || vm.State == stateTerminated {
			p.notFound(w, "Vm", req.VMID)
			return
		}
		privateIP = stringOf(vm.Attrs["PrivateIp"])
		if nicID == "" && stringOf(vm.Attrs["SubnetId"]) != "" {
			// The machine's primary interface, named the way readNics derives it.
			nicID = "eni-" + strings.TrimPrefix(vm.ID, "i-")
		}
	}
	// One address per machine, which the real API enforces: a second link on a
	// machine that already has one is a conflict, not a silent second address.
	for _, other := range p.env.Store.List(kindPublicIP, resource.Tenant{Provider: Name}) {
		if other.ID != address.ID && req.VMID != "" && stringOf(other.Attrs["VmId"]) == req.VMID {
			p.conflict(w, "the Vm "+req.VMID+" already carries "+other.ID)
			return
		}
	}

	// Moving the address means taking it back first: the route lives on the
	// previous machine, and two machines claiming one /32 answer by ARP order.
	if holder := stringOf(address.Attrs["VmId"]); holder != "" && holder != req.VMID {
		if previous, found := p.env.Store.Get(Name, kindVM, holder); found {
			p.unrouteLinkedIP(r.Context(), stringOf(address.Attrs["PublicIp"]), previous)
		}
	}

	linkID := newID("eipassoc", p.env.NewID())
	// The link is written in one critical section held by the store: as a
	// Get-mutate-Commit sequence it erased a concurrent write to another field
	// of the same address — its tags — after their 200 (#295). The NAT hold is
	// re-checked in the same section, because it can appear between the check
	// above and this write.
	err := p.env.Store.Update(Name, kindPublicIP, address.ID, func(stored *resource.Resource) error {
		if natID := stringOf(stored.Attrs["NatServiceId"]); natID != "" {
			return errAddressHeldByNat
		}
		stored.Attrs["VmId"] = req.VMID
		stored.Attrs["NicId"] = nicID
		stored.Attrs["LinkPublicIpId"] = linkID
		if privateIP != "" {
			stored.Attrs["PrivateIp"] = privateIP
		}
		stored.Updated = p.env.Now()
		return nil
	})
	switch {
	case errors.Is(err, errAddressHeldByNat):
		p.conflict(w, "the public IP "+address.ID+" is held by a NAT service")
		return
	case err != nil:
		p.notFound(w, "public IP", address.ID)
		return
	}
	// The record moved; now the packet does too. A link to a bare NicId names
	// no machine, so only the VmId form routes.
	if req.VMID != "" {
		if vm, found := p.env.Store.Get(Name, kindVM, req.VMID); found {
			p.routeLinkedIP(r.Context(), stringOf(address.Attrs["PublicIp"]), vm)
		}
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"LinkPublicIpId":  linkID,
		"ResponseContext": p.context(),
	})
}

func (p *Pack) unlinkPublicIP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LinkPublicIPID string `json:"LinkPublicIpId"`
		PublicIP       string `json:"PublicIp"`
		DryRun         *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	for _, address := range p.env.Store.List(kindPublicIP, resource.Tenant{Provider: Name}) {
		linked := stringOf(address.Attrs["LinkPublicIpId"])
		if linked == "" || stringOf(address.Attrs["VmId"]) == "" {
			continue
		}
		if linked != req.LinkPublicIPID && stringOf(address.Attrs["PublicIp"]) != req.PublicIP {
			continue
		}
		// The route goes with the record: an address that stays routed after
		// its unlink shadows the next machine it is linked to.
		if vm, found := p.env.Store.Get(Name, kindVM, stringOf(address.Attrs["VmId"])); found {
			p.unrouteLinkedIP(r.Context(), stringOf(address.Attrs["PublicIp"]), vm)
		}
		p.releasePublicIP(address)
		emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
		return
	}
	p.notFound(w, "public IP link", orDefault(req.LinkPublicIPID, req.PublicIP))
}

// releasePublicIP drops the machine-side link, leaving the address allocated.
//
// Inside the store lock, on the stored address: releasing over the caller's
// clone wrote the whole resource back and erased a concurrent write to another
// field — a tag landing while a Vm terminates — after its 200 (#295).
func (p *Pack) releasePublicIP(address *resource.Resource) {
	_ = p.env.Store.Update(Name, kindPublicIP, address.ID, func(stored *resource.Resource) error {
		delete(stored.Attrs, "VmId")
		delete(stored.Attrs, "NicId")
		delete(stored.Attrs, "LinkPublicIpId")
		delete(stored.Attrs, "PrivateIp")
		stored.Updated = p.env.Now()
		return nil
	})
}

// publicIPByRef resolves an address by id or by value, the two ways every
// public-IP request may name one.
func (p *Pack) publicIPByRef(id, value string) *resource.Resource {
	if id != "" {
		res, _ := p.env.Store.Get(Name, kindPublicIP, id)
		return res
	}
	if value == "" {
		return nil
	}
	for _, res := range p.env.Store.List(kindPublicIP, resource.Tenant{Provider: Name}) {
		if stringOf(res.Attrs["PublicIp"]) == value {
			return res
		}
	}
	return nil
}

// publicIPOf is the address linked to a machine, empty when it has none. The Vm
// view publishes it, because a real Vm carrying an EIP answers with one and the
// Terraform provider reads it back.
func (p *Pack) publicIPOf(vmID string) string {
	for _, res := range p.env.Store.List(kindPublicIP, resource.Tenant{Provider: Name}) {
		if stringOf(res.Attrs["VmId"]) == vmID {
			return stringOf(res.Attrs["PublicIp"])
		}
	}
	return ""
}
