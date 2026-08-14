package outscale

import (
	"net/http"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Every client reads the inventory before it creates anything, and an emulator
// that declines it breaks creation rather than listing. The Scaleway pack proved
// this the expensive way: `scw instance server create` fetches the server types,
// the default image and the resolved image before it posts anything, and a 404
// on any one of them fails the command.
//
// So the inventory is small, fixed and honest about being fixed. The values are
// real Outscale names — tinav6.c1r1p2 is a type they sell, eu-west-2 a region
// they run — because a client that filters on a name it knows must find it. The
// numbers beside them are plausible rather than measured, and docs/limits.md
// says so: an emulator has no capacity to report.

// Region is where the emulated account lives. One region, because a second one
// would need its own store scoping and buys nothing until something tests it.
const (
	regionName    = "eu-west-2"
	subregionName = "eu-west-2a"
)

// vmTypes is the emulated catalogue. Three sizes of the same family: enough for
// a client to pick one, few enough that nobody mistakes it for Outscale's real
// offering.
//
// VolumeCount stays at zero on purpose. It is the count of local disks a type
// attaches, and the emulator attaches none; a non-zero value would have a client
// wait for a disk that never appears, which is the shape of the bug that cost
// this project a day on Scaleway's volumes_constraint. VolumeSize, its sibling,
// is zero for the same reason and was simply absent until #95.
//
// Gpu and EphemeralsType are present and empty: the real cloud sends both on
// every type, and a client choosing a type reads them. Zero GPUs and no
// ephemeral storage is what this emulator honestly offers.
var vmTypes = []map[string]any{
	{"VmTypeName": "tinav6.c1r1p2", "VcoreCount": 1, "MemorySize": 1.0, "VolumeCount": 0, "VolumeSize": 0, "MaxPrivateIps": 4, "Eth": 1, "BsuOptimized": false, "Gpu": 0, "EphemeralsType": ""},
	{"VmTypeName": "tinav6.c2r2p2", "VcoreCount": 2, "MemorySize": 2.0, "VolumeCount": 0, "VolumeSize": 0, "MaxPrivateIps": 8, "Eth": 1, "BsuOptimized": false, "Gpu": 0, "EphemeralsType": ""},
	{"VmTypeName": "tinav6.c4r8p2", "VcoreCount": 4, "MemorySize": 8.0, "VolumeCount": 0, "VolumeSize": 0, "MaxPrivateIps": 16, "Eth": 2, "BsuOptimized": true, "Gpu": 0, "EphemeralsType": ""},
}

// images is the emulated OMI catalogue. The identifiers are stable across runs
// so a client can hardcode one.
//
// No OsFamily field: that one belongs to Vm, not to Image, and the first run of
// the contract check said so. Which image boots which distribution is emulator
// business and lives in runtimeImages below, out of anything a client reads.
// linuxProductCode is Outscale's own code for a Linux image, and the field whose
// absence sends the Terraform provider to ReadAdminPassword on every machine it
// reads back — a Windows call, on a Linux instance, because an absent list reads
// as "unknown". The Vm view publishes the same value for the same reason.
const linuxProductCode = "0001"

// imageStructure is the three fields the Terraform provider dereferences
// without a nil guard, published so it does not segfault.
//
// Reported by @vde-dis on #86, from the provider's own sources: at v1.7.0,
// data_source_outscale_images.go reads `*image.BlockDeviceMappings` (:289),
// `image.StateComment.StateCode` (:291) and `*image.PermissionsToLaunch
// .AccountIds` (:292) in one loop where every neighbouring field goes through
// ptr.From and survives a nil. The catalogue published none of the three, so
// each was a nil pointer and the plugin died — "Plugin did not respond", with
// nothing naming a field or a call.
//
// It is also a missing check on their side: none of the three is `required` in
// their own api.yaml, which is why `--contracts` passes the answer. But the
// emulator is the side that can fix it without waiting for anybody, and a real
// OMI carries all three — confirmed against the real cloud rather than assumed,
// in shapes/outscale.json: Images[].BlockDeviceMappings[].Bsu.{VolumeSize,
// VolumeType,SnapshotId,Iops,DeleteOnVmDeletion}, Images[].PermissionsToLaunch
// .{AccountIds,GlobalPermission}, Images[].StateComment.
//
// Empty is enough and is what is served: the reporter injected an empty
// BlockDeviceMappings through a proxy, watched the crash move to StateComment,
// then to PermissionsToLaunch, then stop. Inventing a device mapping would put
// a disk in a catalogue that has none, which is the invented format rule 4
// refuses.
//
// TestTheImageCatalogueCarriesWhatTheProviderDereferences fails without this.
func imageStructure(name string) map[string]any {
	return map[string]any{
		// The three of #86, without which the provider dereferences nil.
		"BlockDeviceMappings": []any{},
		"StateComment":        map[string]any{},
		"PermissionsToLaunch": map[string]any{
			"AccountIds":       []any{},
			"GlobalPermission": false,
		},
		// The nine of #95: present on every image the real cloud returns, and
		// absent here — measured, not guessed, in shapes/outscale.json. None of
		// them has crashed anything yet, which is the only difference between
		// them and the three above.
		//
		// Tags is the one to watch: this pack serves tags on other resources,
		// so a client filtering images by tag had nothing to filter on.
		// BootModes and TpmMandatory decide whether a client offers UEFI or
		// secure boot at all.
		"AccountAlias":   "feint",
		"BootModes":      []any{"legacy", "uefi"},
		"CreationDate":   catalogueDate,
		"Description":    name,
		"FileLocation":   "",
		"ImageType":      "machine",
		"RootDeviceName": "/dev/sda1",
		"Tags":           []any{},
		"TpmMandatory":   false,
	}
}

// catalogueDate is when the fixed catalogue's entries claim to have been made.
//
// Fixed rather than "now": the catalogue is committed fiction, and a date that
// moved every run would put a value in a client's plan that changes for no
// reason — the same reason coverage/ carries no scan date.
const catalogueDate = "2025-01-01T00:00:00.000Z"

var images = func() []map[string]any {
	out := []map[string]any{
		{"ImageId": "ami-00000001", "ImageName": "Ubuntu-24.04-2025.01", "Architecture": "x86_64", "State": "available", "RootDeviceType": "bsu", "SecureBoot": false, "AccountId": accountID, "ProductCodes": []any{linuxProductCode}},
		{"ImageId": "ami-00000002", "ImageName": "Debian-12-2025.01", "Architecture": "x86_64", "State": "available", "RootDeviceType": "bsu", "SecureBoot": false, "AccountId": accountID, "ProductCodes": []any{linuxProductCode}},
		{"ImageId": "ami-00000003", "ImageName": "Alpine-3.21-2025.01", "Architecture": "x86_64", "State": "available", "RootDeviceType": "bsu", "SecureBoot": false, "AccountId": accountID, "ProductCodes": []any{linuxProductCode}},
	}
	// Added here rather than written into each literal: three copies of the
	// same structure is three places for it to drift, and a fourth image would
	// be added without them.
	for _, image := range out {
		name, _ := image["ImageName"].(string)
		for key, value := range imageStructure(name) {
			image[key] = value
		}
	}
	return out
}()

// runtimeImages maps an emulated OMI onto what the machine driver boots and
// the login Outscale provisions on it — one value, because the right
// distribution with the wrong login is still a machine nobody can enter. Kept
// apart from the catalogue on purpose: it is not API surface, and holding it in
// the same map is how it ended up in a response.
//
// An identifier outside this map resolves to nothing, and the shared binding
// refuses the boot rather than substituting an OS the client never named (#83).
var runtimeImages = map[string]machine.Image{
	"ami-00000001": {Ref: "ubuntu:24.04", User: DefaultUser},
	"ami-00000002": {Ref: "debian:12", User: DefaultUser},
	"ami-00000003": {Ref: "alpine:3.21", User: DefaultUser},
}

// accountID is the one account this emulator has. Outscale account IDs are
// twelve digits, like AWS's.
const accountID = "000000000001"

// readVmTypes pages like every other Read*: the catalogue is fixed, but a
// client asking for N rows must still not be handed more. This was the one
// list of the pack that ignored its ResultsPerPage — found the day the probe
// started holding paged answers to the size they asked, which is the check
// that fails without this (TestEveryRouteAnswersItsContract, #156).
func (p *Pack) readVmTypes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResultsPerPage int   `json:"ResultsPerPage"`
		DryRun         *bool `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"VmTypes":         page(vmTypes, req.ResultsPerPage),
		"ResponseContext": p.context(),
	})
}

// imageFilters are what an image can answer. ImageIds is the one a client
// actually sends on the path to a create — it resolves the image it was given
// before posting anything.
var imageFilters = []string{"ImageIds", "ImageNames", "AccountIds", "States", "Architectures", "RootDeviceTypes"}

// readImages serves the fixed catalogue and everything a client registered on
// top of it. Both, always: an image a client made and could not then read back
// is the shape of bug that makes a Terraform plan diff for ever.
func (p *Pack) readImages(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filters        filterSet `json:"Filters"`
		ResultsPerPage int       `json:"ResultsPerPage"`
		DryRun         *bool     `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refuseUnsupported(w, req.Filters, imageFilters...) {
		return
	}

	out := make([]map[string]any, 0, len(images))
	for _, image := range images {
		if imageMatches(image, req.Filters) {
			out = append(out, image)
		}
	}
	for _, res := range p.env.Store.List(kindImage, resource.Tenant{Provider: Name}) {
		if view := imageView(res); imageMatches(view, req.Filters) {
			out = append(out, view)
		}
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Images":          page(out, req.ResultsPerPage),
		"ResponseContext": p.context(),
	})
}

// imageMatches filters a rendered image, so a catalogue entry and a registered
// one are filtered by exactly the same rules.
func imageMatches(image map[string]any, f filterSet) bool {
	accountID, _ := image["AccountId"].(string)
	return matchesStrings(f, "ImageIds", stringOf(image["ImageId"])) &&
		matchesStrings(f, "ImageNames", stringOf(image["ImageName"])) &&
		matchesStrings(f, "AccountIds", accountID) &&
		matchesStrings(f, "States", stringOf(image["State"])) &&
		matchesStrings(f, "Architectures", stringOf(image["Architecture"])) &&
		matchesStrings(f, "RootDeviceTypes", stringOf(image["RootDeviceType"]))
}

func (p *Pack) readRegions(w http.ResponseWriter, r *http.Request) {
	// The endpoint a client would call next is this emulator, not Outscale.
	// Answering api.eu-west-2.outscale.com would send the next request to the
	// real cloud, which is the one thing an emulator must never do.
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Regions": []map[string]any{
			{"RegionName": regionName, "Endpoint": emulator.EndpointOf(r)},
		},
		"ResponseContext": p.context(),
	})
}

func (p *Pack) readSubregions(w http.ResponseWriter, _ *http.Request) {
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Subregions": []map[string]any{
			{
				"SubregionName": subregionName,
				"RegionName":    regionName,
				"LocationCode":  "PAR1",
				"State":         "available",
			},
		},
		"ResponseContext": p.context(),
	})
}

// netAccessPointServices is the catalogue of services a Net access point can
// reach, fixed like the rest of this file. The seven names mirror the seven a
// real region publishes (measured, X-2 sweep, 2026-08-08:
// com.outscale.<region>.{api,fcu,lbu,eim,icu,oos,directlink}), templated on the
// emulated region so a client that builds the name from its own region finds
// it. The ids are stable so a client can hardcode one, and the ranges are
// TEST-NET blocks (RFC 5737): a documented-fictional address for a fictional
// facility, matching what ReadPublicIpRanges publishes.
var netAccessPointServices = []map[string]any{
	{"ServiceId": "pl-00000001", "ServiceName": "com.outscale." + regionName + ".api", "IpRanges": []any{"192.0.2.0/24"}},
	{"ServiceId": "pl-00000002", "ServiceName": "com.outscale." + regionName + ".fcu", "IpRanges": []any{"192.0.2.0/24"}},
	{"ServiceId": "pl-00000003", "ServiceName": "com.outscale." + regionName + ".lbu", "IpRanges": []any{"192.0.2.0/24"}},
	{"ServiceId": "pl-00000004", "ServiceName": "com.outscale." + regionName + ".eim", "IpRanges": []any{"192.0.2.0/24"}},
	{"ServiceId": "pl-00000005", "ServiceName": "com.outscale." + regionName + ".icu", "IpRanges": []any{"192.0.2.0/24"}},
	{"ServiceId": "pl-00000006", "ServiceName": "com.outscale." + regionName + ".oos", "IpRanges": []any{"198.51.100.0/24"}},
	{"ServiceId": "pl-00000007", "ServiceName": "com.outscale." + regionName + ".directlink", "IpRanges": []any{"198.51.100.0/24"}},
}

func (p *Pack) readNetAccessPointServices(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filters        filterSet `json:"Filters"`
		ResultsPerPage int       `json:"ResultsPerPage"`
		DryRun         *bool     `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refuseUnsupported(w, req.Filters, "ServiceIds", "ServiceNames") {
		return
	}
	out := make([]map[string]any, 0, len(netAccessPointServices))
	for _, service := range netAccessPointServices {
		if !matchesStrings(req.Filters, "ServiceIds", service["ServiceId"].(string)) ||
			!matchesStrings(req.Filters, "ServiceNames", service["ServiceName"].(string)) {
			continue
		}
		out = append(out, service)
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Services":        page(out, req.ResultsPerPage),
		"ResponseContext": p.context(),
	})
}

// readPublicIpRanges publishes the block createPublicIP allocates from, and
// nothing else: the catalogue and the allocator answering from the same
// constant is what keeps them from disagreeing. The response is a list of
// strings, not objects — measured, and easy to get wrong from the field name.
func (p *Pack) readPublicIPRanges(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResultsPerPage int   `json:"ResultsPerPage"`
		DryRun         *bool `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"PublicIps":       []any{publicIPBase + "0/24"},
		"ResponseContext": p.context(),
	})
}
