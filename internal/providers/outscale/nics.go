package outscale

import (
	"net/http"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// NICs, read-only and derived: the primary interface of every Vm in a Net.
//
// Derived rather than stored, because the fact already exists — a Vm holds its
// SubnetId and PrivateIp — and a second copy of it would drift (see the
// one-shape-one-owner rule). What must NOT drift between two reads is the
// identifiers, so each is a pure function of the VmId: i-1234abcd owns
// eni-1234abcd and its link eni-attach-1234abcd. Terraform stores these; a
// derived id that moved would be a permanent diff.
//
// Shape measured (X-2 sweep, 2026-08-08): PrivateDnsName is
// "ip-<a-b-c-d>.<region>.compute.internal", both on the NIC and on each entry of
// PrivateIps; LinkPublicIp is omitted when there is none, and SecurityGroups
// carries the groups of the Net — which for this pack is its default group,
// since CreateVms still refuses explicit SecurityGroupIds.
//
// CreateNic, LinkNic and the secondary-interface calls are backlog: a machine
// here has exactly one interface until the runtime grows a second one.

var nicFilters = []string{"NicIds", "LinkNicVmIds", "SubnetIds", "NetIds"}

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
		if !matchesStrings(req.Filters, "NicIds", nicID) ||
			!matchesStrings(req.Filters, "LinkNicVmIds", vm.ID) ||
			!matchesStrings(req.Filters, "SubnetIds", subnetID) ||
			!matchesStrings(req.Filters, "NetIds", netID) {
			continue
		}
		out = append(out, p.nicView(vm, nicID, subnetID, netID))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Nics":            page(out, req.ResultsPerPage),
		"ResponseContext": p.context(),
	})
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

	return map[string]any{
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
