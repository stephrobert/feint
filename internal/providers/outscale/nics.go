package outscale

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// NICs: the primary interface of every Vm, derived; the secondary ones a client
// creates, stored.
//
// The primary is derived rather than stored, because the fact already exists — a
// Vm holds its SubnetId and PrivateIp — and a second copy would drift (the
// one-shape-one-owner rule). Its identifiers are a pure function of the VmId, so
// they cannot move between reads: i-1234abcd owns eni-1234abcd, device 0, always
// attached. Terraform stores these; a derived id that moved would be a
// permanent diff.
//
// A secondary interface is a resource of its own: CreateNic allocates it an
// address in the Subnet the same way a Vm gets one, LinkNic attaches it at a
// device number of 1 or more, and it is these that a client detaches and
// deletes. The two kinds render through the same filters, matched on the view
// rather than on where they came from.
//
// Shapes measured against a real account (read sweep and lifecycle sweep,
// 2026-08-08): PrivateDnsName is "ip-<a-b-c-d>.<region>.compute.internal", on the
// NIC and on each PrivateIps entry; a fresh CreateNic carries neither LinkNic
// nor LinkPublicIp; an attached interface carries State "in-use" and a LinkNic
// with DeviceNumber from 1, State "attached" and VmAccountId the account's own.
//
// The two states are not the same word and this comment said they were, which
// is how the wrong one survived: the interface is "in-use", the link is
// "attached".

const kindNic = "nic"

// nicFilters are what an interface can answer. The group filters are here for
// the same reason they are on a Vm: `terraform destroy` asks which interfaces
// still wear a security group before it removes one, and a filter it sends and
// this pack refuses fails the destroy after a successful apply.
var nicFilters = []string{
	"NicIds", "LinkNicVmIds", "SubnetIds", "NetIds",
	"SecurityGroupIds", "SecurityGroupNames",
}

func (p *Pack) readNics(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filters        filterSet `json:"Filters"`
		ResultsPerPage int       `json:"ResultsPerPage"`
		DryRun         *bool     `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refuseUnsupported(w, req.Filters, nicFilters...) {
		return
	}

	out := make([]map[string]any, 0)
	// The primary interface of every Vm in a Net, derived from the Vm — device
	// 0, always attached, never a resource of its own.
	for _, vm := range p.env.Store.List(kindVM, resource.Tenant{Provider: Name}) {
		if vm.State == stateTerminated {
			continue
		}
		subnetID := stringOf(vm.Attrs["SubnetId"])
		if subnetID == "" {
			// A Vm outside a Net has no NIC a client can manage; the real public
			// cloud hides those interfaces from ReadNics too.
			continue
		}
		subnet, found := p.env.Store.Get(Name, kindSubnet, subnetID)
		if !found {
			continue
		}
		netID := stringOf(subnet.Attrs["NetId"])
		nicID := "eni-" + strings.TrimPrefix(vm.ID, "i-")
		view := p.nicView(vm, nicID, subnetID, netID)
		if p.nicMatches(view, req.Filters) {
			out = append(out, view)
		}
	}
	// The secondary interfaces a client created, which are resources of their
	// own — device >= 1, attachable and detachable.
	for _, nic := range p.env.Store.List(kindNic, resource.Tenant{Provider: Name}) {
		view := p.storedNicView(nic)
		if p.nicMatches(view, req.Filters) {
			out = append(out, view)
		}
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Nics":            page(out, req.ResultsPerPage),
		"ResponseContext": p.context(),
	})
}

// nicMatches applies the four filters against a rendered NIC. A derived NIC and
// a stored one filter the same way because both are matched on the view, not on
// where they came from.
func (p *Pack) nicMatches(view map[string]any, f filterSet) bool {
	vmID := ""
	if link, ok := view["LinkNic"].(map[string]any); ok {
		vmID = stringOf(link["VmId"])
	}
	var groupIDs, groupNames []string
	for _, raw := range listOf(view["SecurityGroups"]) {
		group, _ := raw.(map[string]any)
		groupIDs = append(groupIDs, stringOf(group["SecurityGroupId"]))
		groupNames = append(groupNames, stringOf(group["SecurityGroupName"]))
	}
	return matchesStrings(f, "NicIds", stringOf(view["NicId"])) &&
		matchesStrings(f, "LinkNicVmIds", vmID) &&
		matchesStrings(f, "SubnetIds", stringOf(view["SubnetId"])) &&
		matchesStrings(f, "NetIds", stringOf(view["NetId"])) &&
		matchesAny(f, "SecurityGroupIds", groupIDs...) &&
		matchesAny(f, "SecurityGroupNames", groupNames...)
}

func (p *Pack) nicView(vm *resource.Resource, nicID, subnetID, netID string) map[string]any {
	privateIP := stringOf(vm.Attrs["PrivateIp"])
	dns := privateDNSName(privateIP)

	// The groups on the interface are the machine's, resolved the same way the
	// Vm view resolves them: what it asked for, or the Net's default group.
	groups := p.effectiveSecurityGroups(vm)
	if groups == nil {
		groups = []any{}
	}

	out := map[string]any{
		"NicId":               nicID,
		"AccountId":           accountID,
		"Description":         "",
		"IsSourceDestChecked": true,
		"MacAddress":          macOf(nicID),
		"NetId":               netID,
		"SubnetId":            subnetID,
		"SubregionName":       subregionName,
		"State":               "in-use",
		"PrivateDnsName":      dns,
		"PrivateIps": []any{map[string]any{
			"IsPrimary":      true,
			"PrivateDnsName": dns,
			"PrivateIp":      privateIP,
		}},
		"LinkNic": map[string]any{
			"DeleteOnVmDeletion": true,
			"DeviceNumber":       0,
			"LinkNicId":          "eni-attach-" + strings.TrimPrefix(nicID, "eni-"),
			"State":              "attached",
			"VmAccountId":        accountID,
			"VmId":               vm.ID,
		},
		"SecurityGroups": groups,
		"Tags":           []any{},
	}
	// The address the machine carries, which the real cloud publishes on the
	// interface as well as on the address itself. Found missing by the
	// real-against-emulated diff, not by any test here.
	if linked := p.linkPublicIPView(vm.ID); linked != nil {
		out["LinkPublicIp"] = linked
	}
	return out
}

// ---- Lifecycle (secondary interfaces) ---------------------------------------

// storedNicView renders a created secondary interface. LinkNic and LinkPublicIp
// are emitted only when present — measured: a fresh CreateNic carries neither.
func (p *Pack) storedNicView(nic *resource.Resource) map[string]any {
	netID := stringOf(nic.Attrs["NetId"])
	privateIP := stringOf(nic.Attrs["PrivateIp"])
	dns := privateDNSName(privateIP)

	groups := make([]any, 0, 1)
	for _, id := range stringsOf(nic.Attrs["SecurityGroupIds"]) {
		if sg, found := p.env.Store.Get(Name, kindSecurityGroup, id); found {
			groups = append(groups, map[string]any{
				"SecurityGroupId":   sg.ID,
				"SecurityGroupName": stringOf(sg.Attrs["SecurityGroupName"]),
			})
		}
	}
	if len(groups) == 0 {
		for _, sg := range p.securityGroupsOf(netID) {
			if stringOf(sg.Attrs["SecurityGroupName"]) == "default" {
				groups = append(groups, map[string]any{
					"SecurityGroupId":   sg.ID,
					"SecurityGroupName": "default",
				})
			}
		}
	}

	out := map[string]any{
		"NicId":               nic.ID,
		"AccountId":           accountID,
		"Description":         stringOf(nic.Attrs["Description"]),
		"IsSourceDestChecked": true,
		"MacAddress":          macOf(nic.ID),
		"NetId":               netID,
		"SubnetId":            stringOf(nic.Attrs["SubnetId"]),
		"SubregionName":       subregionName,
		"State":               stringOf(nic.Attrs["State"]),
		"PrivateDnsName":      dns,
		// The primary entry, plus every secondary address linked since
		// (#172). Rebuilding the list from PrivateIp alone published one
		// address whatever the NIC held, so LinkPrivateIps answered 200 and
		// nothing changed — a write the API accepted and never showed.
		"PrivateIps":     privateIPEntries(nic, dns, privateIP),
		"SecurityGroups": groups,
		"Tags":           []any{},
	}
	// The link appears only when attached, in the measured shape: DeviceNumber
	// from 1 (0 is the derived primary), VmAccountId the account's own.
	//
	// `attached`, not `in-use`. Those are two different fields: `in-use` is the
	// state of the *interface*, set above from Attrs, and `attached` is the
	// state of the *link*. This spot published the interface's word for the
	// link's field, while the primary NIC rendered inside a Vm published
	// `attached` four dozen lines up — the same field, two spellings, in one
	// file.
	//
	// Nothing could see it. No contract does: their OpenAPI types LinkNic.State
	// as a bare string, and the value only appears in their examples. No unit
	// test did either, because the pack's own tests read back what the pack
	// wrote. It took the Terraform provider, which polls ReadNics until the
	// link reports `attached` and gave up with "unexpected state 'in-use',
	// wanted target 'attached, detached, failed'" — leaving the apply half done
	// and the destroy unable to finish.
	// TestAnAttachedNicReportsTheLinkStateTheProviderWaitsFor fails without this.
	if vmID := stringOf(nic.Attrs["LinkVmId"]); vmID != "" {
		out["LinkNic"] = map[string]any{
			"DeleteOnVmDeletion": false,
			"DeviceNumber":       numOf(nic.Attrs["DeviceNumber"]),
			"LinkNicId":          stringOf(nic.Attrs["LinkNicId"]),
			"State":              "attached",
			"VmAccountId":        accountID,
			"VmId":               vmID,
		}
		// An address linked to the machine shows on its interface too, which is
		// where the real cloud puts it and where the diff found it missing.
		if linked := p.linkPublicIPView(vmID); linked != nil {
			out["LinkPublicIp"] = linked
		}
	}
	return out
}

func (p *Pack) createNic(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SubnetID         string   `json:"SubnetId"`
		Description      string   `json:"Description"`
		SecurityGroupIDs []string `json:"SecurityGroupIds"`
		DryRun           *bool    `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.SubnetID == "" {
		p.badRequest(w, "SubnetId is required")
		return
	}
	if !p.checkVMSecurityGroups(w, req.SecurityGroupIDs) {
		return
	}

	// Under the addressing lock, and the address reserved before release: a NIC
	// takes an address in the Subnet exactly as a Vm does, and two creates must
	// not pick the same one.
	unlock := p.lockAddresses()
	place, err := p.placeInSubnet(req.SubnetID)
	if err != nil {
		unlock()
		if errors.Is(err, errUnknownSubnet) {
			p.notFound(w, "Subnet", req.SubnetID)
		} else {
			p.badRequest(w, err.Error())
		}
		return
	}
	now := p.env.Now()
	nic := resource.New(newID("eni", p.env.NewID()), kindNic, resource.Tenant{Provider: Name}, "available", now)
	nic.Attrs = map[string]any{
		"SubnetId":    place.SubnetID,
		"NetId":       place.NetID,
		"PrivateIp":   place.Address.String(),
		"Description": req.Description,
		"State":       "available",
	}
	if len(req.SecurityGroupIDs) > 0 {
		nic.Attrs["SecurityGroupIds"] = req.SecurityGroupIDs
	}
	p.env.Store.Put(nic)
	unlock()

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Nic":             p.storedNicView(nic),
		"ResponseContext": p.context(),
	})
}

func (p *Pack) deleteNic(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NicID  string `json:"NicId"`
		DryRun *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	nic, found := p.env.Store.Get(Name, kindNic, req.NicID)
	if !found {
		p.notFound(w, "network interface", req.NicID)
		return
	}
	// An attached interface does not go; the unlink comes first, which is the
	// order a real destroy walks.
	if vmID := stringOf(nic.Attrs["LinkVmId"]); vmID != "" {
		p.conflict(w, "the network interface "+nic.ID+" is still attached to "+vmID)
		return
	}
	p.env.Store.Delete(Name, kindNic, req.NicID)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

func (p *Pack) linkNic(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NicID        string `json:"NicId"`
		VMID         string `json:"VmId"`
		DeviceNumber *int   `json:"DeviceNumber"`
		DryRun       *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	// DeviceNumber 0 is the primary, which is the Vm's own derived interface and
	// not a slot a secondary NIC may take.
	if req.DeviceNumber == nil || *req.DeviceNumber < 1 {
		p.badRequest(w, "DeviceNumber must be 1 or greater; 0 is the primary interface")
		return
	}
	nic, found := p.env.Store.Get(Name, kindNic, req.NicID)
	if !found {
		p.notFound(w, "network interface", req.NicID)
		return
	}
	vm, found := p.env.Store.Get(Name, kindVM, req.VMID)
	if !found {
		p.notFound(w, "Vm", req.VMID)
		return
	}
	if holder := stringOf(nic.Attrs["LinkVmId"]); holder != "" {
		p.conflict(w, "the network interface "+nic.ID+" is already attached to "+holder)
		return
	}
	// A secondary interface joins the Vm's own Subnet, which the real API
	// requires: an interface in another Subnet cannot attach.
	if stringOf(vm.Attrs["SubnetId"]) != stringOf(nic.Attrs["SubnetId"]) {
		p.badRequest(w, "the interface "+nic.ID+" is not in the Subnet of "+vm.ID)
		return
	}
	// The device number is exclusive per Vm, primary included.
	for _, other := range p.env.Store.List(kindNic, resource.Tenant{Provider: Name}) {
		if stringOf(other.Attrs["LinkVmId"]) == req.VMID &&
			int(numOf(other.Attrs["DeviceNumber"])) == *req.DeviceNumber {
			p.conflict(w, "device number "+strconv.Itoa(*req.DeviceNumber)+" is already used on "+req.VMID)
			return
		}
	}
	linkID := "eni-attach-" + strings.TrimPrefix(nic.ID, "eni-")
	nic.Attrs["LinkVmId"] = req.VMID
	nic.Attrs["DeviceNumber"] = *req.DeviceNumber
	nic.Attrs["LinkNicId"] = linkID
	nic.Attrs["State"] = "in-use"
	if !p.env.Store.Commit(nic, p.env.Now()) {
		p.notFound(w, "network interface", req.NicID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"LinkNicId":       linkID,
		"ResponseContext": p.context(),
	})
}

func (p *Pack) unlinkNic(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LinkNicID string `json:"LinkNicId"`
		DryRun    *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	for _, nic := range p.env.Store.List(kindNic, resource.Tenant{Provider: Name}) {
		if stringOf(nic.Attrs["LinkNicId"]) != req.LinkNicID {
			continue
		}
		delete(nic.Attrs, "LinkVmId")
		delete(nic.Attrs, "DeviceNumber")
		delete(nic.Attrs, "LinkNicId")
		nic.Attrs["State"] = "available"
		if !p.env.Store.Commit(nic, p.env.Now()) {
			p.notFound(w, "network interface", nic.ID)
			return
		}
		emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
		return
	}
	p.notFound(w, "network interface attachment", req.LinkNicID)
}

// detachNicsOf releases every secondary interface attached to a Vm, leaving the
// interface itself in place. Called from the Vm delete path.
func (p *Pack) detachNicsOf(vmID string) {
	for _, nic := range p.env.Store.List(kindNic, resource.Tenant{Provider: Name}) {
		if stringOf(nic.Attrs["LinkVmId"]) != vmID {
			continue
		}
		delete(nic.Attrs, "LinkVmId")
		delete(nic.Attrs, "DeviceNumber")
		delete(nic.Attrs, "LinkNicId")
		nic.Attrs["State"] = "available"
		p.env.Store.Commit(nic, p.env.Now())
	}
}

// publicDNSName renders the name the real cloud derives from a public address:
// ows-203-0-113-1.<region>.compute.outscale.com, measured. Empty for no address,
// because a name for an address that does not exist is an invention.
func publicDNSName(ip string) string {
	if ip == "" {
		return ""
	}
	return "ows-" + strings.ReplaceAll(ip, ".", "-") + "." + regionName + ".compute.outscale.com"
}

// linkPublicIPView is the LinkPublicIp an interface carries when an address is
// linked to it, in the full shape (the standalone Nic schema). Nil when there is
// none: measured, an unlinked interface has no such key.
func (p *Pack) linkPublicIPView(vmID string) map[string]any {
	for _, res := range p.env.Store.List(kindPublicIP, resource.Tenant{Provider: Name}) {
		if stringOf(res.Attrs["VmId"]) != vmID {
			continue
		}
		address := stringOf(res.Attrs["PublicIp"])
		return map[string]any{
			"LinkPublicIpId":    stringOf(res.Attrs["LinkPublicIpId"]),
			"PublicIpId":        res.ID,
			"PublicIp":          address,
			"PublicDnsName":     publicDNSName(address),
			"PublicIpAccountId": accountID,
		}
	}
	return nil
}

// nicLightView is the interface as a Vm embeds it: NicLight, which drops the
// interface's own Tags and SubregionName and carries LinkNic in its Light shape
// (no VmId — the machine is the object holding it). Its nested LinkPublicIp is
// LinkPublicIpLightForVm, three fields rather than five.
//
// A separate rendering rather than a filtered copy of nicView, because the two
// schemas genuinely differ and a filtered copy would drift the first time one of
// them gains a field.
func (p *Pack) nicLightView(full map[string]any, vmID string) map[string]any {
	light := map[string]any{
		"NicId":               full["NicId"],
		"AccountId":           full["AccountId"],
		"Description":         full["Description"],
		"IsSourceDestChecked": full["IsSourceDestChecked"],
		"MacAddress":          full["MacAddress"],
		"NetId":               full["NetId"],
		"SubnetId":            full["SubnetId"],
		"State":               full["State"],
		"PrivateDnsName":      full["PrivateDnsName"],
		"SecurityGroups":      full["SecurityGroups"],
	}
	if link, ok := full["LinkNic"].(map[string]any); ok {
		light["LinkNic"] = map[string]any{
			"DeleteOnVmDeletion": link["DeleteOnVmDeletion"],
			"DeviceNumber":       link["DeviceNumber"],
			"LinkNicId":          link["LinkNicId"],
			"State":              link["State"],
		}
	}
	// The address, in the three-field shape this schema declares.
	if linked := p.linkPublicIPView(vmID); linked != nil {
		forVM := map[string]any{
			"PublicIp":          linked["PublicIp"],
			"PublicDnsName":     linked["PublicDnsName"],
			"PublicIpAccountId": linked["PublicIpAccountId"],
		}
		light["LinkPublicIp"] = forVM
		// And on the primary address inside it, which is where the real cloud
		// repeats it.
		if ips, ok := full["PrivateIps"].([]any); ok && len(ips) > 0 {
			out := make([]any, 0, len(ips))
			for _, raw := range ips {
				ip, _ := raw.(map[string]any)
				entry := map[string]any{
					"IsPrimary":      ip["IsPrimary"],
					"PrivateIp":      ip["PrivateIp"],
					"PrivateDnsName": ip["PrivateDnsName"],
				}
				if primary, _ := ip["IsPrimary"].(bool); primary {
					entry["LinkPublicIp"] = forVM
				}
				out = append(out, entry)
			}
			light["PrivateIps"] = out
			return light
		}
	}
	light["PrivateIps"] = full["PrivateIps"]
	return light
}

// nicsOfVM is every interface a machine carries, in the Light shape the Vm
// schema declares: the derived primary first, then any attached secondary.
func (p *Pack) nicsOfVM(vm *resource.Resource) []any {
	subnetID := stringOf(vm.Attrs["SubnetId"])
	if subnetID == "" {
		return nil
	}
	subnet, found := p.env.Store.Get(Name, kindSubnet, subnetID)
	if !found {
		return nil
	}
	netID := stringOf(subnet.Attrs["NetId"])
	out := make([]any, 0, 2)
	primary := p.nicView(vm, "eni-"+strings.TrimPrefix(vm.ID, "i-"), subnetID, netID)
	out = append(out, p.nicLightView(primary, vm.ID))
	for _, nic := range p.env.Store.List(kindNic, resource.Tenant{Provider: Name}) {
		if stringOf(nic.Attrs["LinkVmId"]) != vm.ID {
			continue
		}
		out = append(out, p.nicLightView(p.storedNicView(nic), vm.ID))
	}
	return out
}

// privateDNSName renders the name the real cloud derives from an address:
// ip-10-0-1-10.<region>.compute.internal, measured. An empty address gives an
// empty name rather than a name claiming an address that does not exist.
func privateDNSName(ip string) string {
	if ip == "" {
		return ""
	}
	return "ip-" + strings.ReplaceAll(ip, ".", "-") + "." + regionName + ".compute.internal"
}

// macOf derives a stable, locally-administered MAC from the NIC id's hex
// suffix. 0a:.. rather than a real vendor prefix: nothing on any wire ever
// carries it, but it must not collide with a vendor's space anyway.
func macOf(nicID string) string {
	hexPart := strings.TrimPrefix(nicID, "eni-")
	for len(hexPart) < 8 {
		hexPart += "0"
	}
	return "0a:00:" + hexPart[0:2] + ":" + hexPart[2:4] + ":" + hexPart[4:6] + ":" + hexPart[6:8]
}

// privateIPEntries renders the address list a NIC publishes: the primary first,
// then the secondary ones in the order the store holds them.
//
// The primary is derived rather than read back, because it is the one address a
// NIC cannot lose: it comes with the interface and UnlinkPrivateIps refuses it.
func privateIPEntries(nic *resource.Resource, dns, primary string) []any {
	entries := []any{map[string]any{
		"IsPrimary":      true,
		"PrivateDnsName": dns,
		"PrivateIp":      primary,
	}}
	for _, address := range secondaryAddresses(nic) {
		entries = append(entries, map[string]any{
			"IsPrimary":      false,
			"PrivateDnsName": privateDNSName(address),
			"PrivateIp":      address,
		})
	}
	return entries
}
