package outscale

import (
	"net/http"
	"sort"

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

// defaultRegionName is where the emulated account lives when nobody chooses:
// one region per emulator instance, because at Outscale a region is not a
// property of the API surface — every region speaks the same API — but of the
// deployment, i.e. which endpoint a client is pointed at. What #290 corrected
// is the *which*: #269 rightly made the catalogue the authority every write
// path checks, then froze it on this constant, which re-created the defect
// #268/#269 were about one level up — a datum written as a constant. The
// region is now the pack's datum (Pack.region), selected at construction, and
// everything regional follows it.
const defaultRegionName = "eu-west-2"

// regionCatalogue is every region Outscale publishes, with its physical zones
// in subregion order. Source: docs.outscale.com, "About Regions and
// Subregions", "Mapping Between Subregions and Physical Zones" (fetched
// 2026-08-18). Subregion names are the region plus a letter, which is the
// published naming for all five regions. The reference also says the
// subregion-to-zone mapping is randomly drawn per account; this account maps
// them in table order, which is one of the mappings a real account can get.
var regionCatalogue = map[string][]string{
	"eu-west-2":           {"PAR1", "PAR4", "PAR7"},
	"us-east-2":           {"NJ1", "NJ2"},
	"us-west-1":           {"SV1", "SV2"},
	"cloudgouv-eu-west-1": {"SEC1", "SEC2", "SEC3"},
	"ap-northeast-1":      {"JPN1", "JPN2"},
}

// regionNames lists what regionCatalogue knows, sorted, for the error a
// misspelt selection gets: a refusal that does not say what would have been
// accepted sends the operator back to the source code.
func regionNames() []string {
	names := make([]string, 0, len(regionCatalogue))
	for name := range regionCatalogue {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// subregionsOf renders the region's subregions in the ReadSubregions shape,
// and reports whether Outscale publishes such a region at all. The rows of
// the region actually in force, never a union across regions: a catalogue
// declaring zones its own write paths refuse would be #269 in the other
// direction.
func subregionsOf(region string) ([]map[string]any, bool) {
	codes, ok := regionCatalogue[region]
	if !ok {
		return nil, false
	}
	out := make([]map[string]any, 0, len(codes))
	for i, code := range codes {
		out = append(out, map[string]any{
			"SubregionName": region + string(rune('a'+i)),
			"RegionName":    region,
			"LocationCode":  code,
			"State":         "available",
		})
	}
	return out, true
}

// knownSubregion reports whether the catalogue declares the subregion. Every
// write path that takes a SubregionName asks this before storing, so the
// catalogue and the creates cannot disagree about which zones exist —
// TestWhatACreateAcceptsTheCatalogueDeclares fails without it, and
// TestANonDefaultRegionAgreesWithItself holds it whichever region is in
// force. #269 measured the contradiction it closes: CreateSubnet accepted
// `cloudgouv-eu-west-1a` verbatim while ReadSubregions declared one AZ, and
// whichever side a client believed, the other one lied to it.
func (p *Pack) knownSubregion(name string) bool {
	for _, subregion := range p.subregions {
		if subregion["SubregionName"] == name {
			return true
		}
	}
	return false
}

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
// without a nil guard, published so it does not segfault, plus the device
// mapping that names the snapshot the image was cut from.
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
// TestTheImageCatalogueCarriesWhatTheProviderDereferences fails without this.
func imageStructure(name string, snapshot map[string]any) map[string]any {
	return map[string]any{
		// The three of #86, without which the provider dereferences nil.
		//
		// The mapping names catalogueSnapshots' entry for this image, and that
		// is the whole of #389: it was empty for a year because filling it with
		// an invented SnapshotId is rule 4 — the exact fiction that killed a
		// conformance run once, when a fictional root VolumeId on a machine was
		// resolved by the Terraform provider. A snapshot ReadSnapshots really
		// answers for removes the choice between lying and omitting.
		//
		// The four keys are the ones a real OMI carries, measured rather than
		// argued: in corpus/outscale/oapi-cli-lifecycle.jsonl, 396 of the 399
		// device mappings the account answered carry exactly DeleteOnVmDeletion,
		// SnapshotId, VolumeSize and VolumeType. Iops is on the other three, and
		// only on those — the three whose VolumeType is the provisioned-IOPS one.
		// declined_fields.go carries that measurement where the decline is.
		//
		// TestACatalogueImageNamesASnapshotReadSnapshotsAnswersFor fails
		// without this.
		"BlockDeviceMappings": []any{
			map[string]any{
				"DeviceName": defaultRootDevice,
				"Bsu": map[string]any{
					// True on 364 of the 399 measured mappings, and true of what
					// this pack does: terminating a Vm deletes the root volume
					// CreateVms cut from this snapshot.
					"DeleteOnVmDeletion": true,
					"SnapshotId":         snapshot["SnapshotId"],
					"VolumeSize":         snapshot["VolumeSize"],
					"VolumeType":         defaultVolumeType,
				},
			},
		},
		"StateComment": map[string]any{},
		// The owner, which for this catalogue is this emulator's own account —
		// the same accountID every other answer carries. An empty list said the
		// image was launchable by nobody, where the real cloud names whoever it
		// is shared with; naming the owner is both true here and the shape a
		// client iterates.
		"PermissionsToLaunch": map[string]any{
			"AccountIds":       []any{accountID},
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
		"RootDeviceName": defaultRootDevice,
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

// catalogueSnapshots is what the catalogue's images were cut from: one
// snapshot per OMI, answered by ReadSnapshots like any other, so the
// SnapshotId an image publishes names an object a client can read back.
//
// This is #389's whole content. The alternative — a SnapshotId in the image
// and nothing behind it — is rule 4, and this repository has already paid for
// it once: a fictional root VolumeId on a machine was resolved by the Terraform
// provider and "volume vol-rooti149 not found" killed a conformance run.
//
// Held here rather than seeded into the store, for three measured reasons, and
// the first two are the ones that decide it:
//
//  1. store.Restore replaces s.items wholesale (internal/core/store/store.go),
//     and snapshot.go documents its format as designed to be loaded into
//     another instance. A seeded snapshot does not survive that, so the
//     catalogue would start naming snapshots nobody can read — the dangling
//     identifier this chain exists to remove, reintroduced by the one path
//     that is explicitly meant to cross instances.
//  2. CreationDate has to be catalogueDate. A store resource takes env.Now(),
//     which moves every run, and this file already says why a catalogue date
//     that moves is a value in a client's plan that changes for no reason.
//  3. The image at the other end of the same link is a fixed table too. One
//     mechanism for one kind of object: what makes a catalogue entry different
//     from a client's is exactly that the client did not create it and cannot
//     destroy it, which isCatalogueImage and isCatalogueSnapshot both say.
//
// The identifiers are deliberately outside the corpus sanitiser's minting
// space. It hands out prefixed identifiers as a shared counter in eight
// hexadecimal digits (internal/corpus/corpus.go), so ami-00000001..3 collide
// with recorded values and #395 carries the two corpus exemptions that
// collision costs. Numbering three more objects the same way would have widened
// a known defect for no gain; fe1a7… is as valid an Outscale identifier and
// cannot be reached by a small counter.
//
// TestTheCatalogueIdentifiersStayOutOfTheMintingSpace fails without that choice.
var catalogueSnapshots = []map[string]any{
	{"SnapshotId": "snap-fe1a7001", "VolumeId": "vol-fe1a7001", "VolumeSize": 10, "Description": "Ubuntu-24.04-2025.01 root"},
	{"SnapshotId": "snap-fe1a7002", "VolumeId": "vol-fe1a7002", "VolumeSize": 10, "Description": "Debian-12-2025.01 root"},
	{"SnapshotId": "snap-fe1a7003", "VolumeId": "vol-fe1a7003", "VolumeSize": 10, "Description": "Alpine-3.21-2025.01 root"},
}

// snapshotStructure completes a catalogue snapshot with what every snapshot of
// a real account carries, measured in the recording rather than assumed:
// State "completed", Progress 100, an empty PermissionsToCreateVolume and an
// empty Tags list (X-2 sweep, 2026-08-08, and shapes/outscale.json).
//
// VolumeId names the volume the image was cut from, and that volume is gone —
// which is the state every OMI of a real account is in, and a state this pack
// already reaches by ordinary means: CreateVolume, CreateSnapshot, DeleteVolume
// leaves exactly it, because deleteSnapshot's own comment holds that provenance
// is history rather than a live reference. It is the one identifier of this
// chain that names nothing, and it is the one no client resolves: the three
// links a response invites a client to follow — image to snapshot, snapshot to
// the volume CreateVms cuts, volume to Vm — all name objects a read answers for.
func snapshotStructure(entry map[string]any) map[string]any {
	out := map[string]any{
		"State":        "completed",
		"Progress":     100,
		"AccountId":    accountID,
		"CreationDate": catalogueDate,
		"PermissionsToCreateVolume": map[string]any{
			"AccountIds":       []any{},
			"GlobalPermission": false,
		},
		"Tags": []any{},
	}
	for key, value := range entry {
		out[key] = value
	}
	return out
}

// catalogueSnapshotViews is the wire shape of every catalogue snapshot, built
// once because the table is fixed.
var catalogueSnapshotViews = func() []map[string]any {
	out := make([]map[string]any, 0, len(catalogueSnapshots))
	for _, entry := range catalogueSnapshots {
		out = append(out, snapshotStructure(entry))
	}
	return out
}()

// isCatalogueSnapshot reports whether an id names one of the fixed snapshots.
// They are the emulator's, not the client's, exactly as isCatalogueImage says
// of the images they back: a delete on one is refused rather than answered with
// a success nothing acted on — and here it would also leave every catalogue
// image naming a snapshot that no longer answers.
//
// TestACatalogueSnapshotRefusesItsDelete fails without this.
func isCatalogueSnapshot(id string) bool {
	for _, snapshot := range catalogueSnapshots {
		if snapshot["SnapshotId"] == id {
			return true
		}
	}
	return false
}

// catalogueSnapshotOf is the snapshot a catalogue image was cut from, by index:
// the two tables are paired, and pairing them by position keeps a fourth image
// from being added without the snapshot that backs it.
func catalogueSnapshotOf(i int) map[string]any { return catalogueSnapshots[i] }

var images = func() []map[string]any {
	out := []map[string]any{
		{"ImageId": "ami-00000001", "ImageName": "Ubuntu-24.04-2025.01", "Architecture": "x86_64", "State": "available", "RootDeviceType": "bsu", "SecureBoot": false, "AccountId": accountID, "ProductCodes": []any{linuxProductCode}},
		{"ImageId": "ami-00000002", "ImageName": "Debian-12-2025.01", "Architecture": "x86_64", "State": "available", "RootDeviceType": "bsu", "SecureBoot": false, "AccountId": accountID, "ProductCodes": []any{linuxProductCode}},
		{"ImageId": "ami-00000003", "ImageName": "Alpine-3.21-2025.01", "Architecture": "x86_64", "State": "available", "RootDeviceType": "bsu", "SecureBoot": false, "AccountId": accountID, "ProductCodes": []any{linuxProductCode}},
	}
	// Added here rather than written into each literal: three copies of the
	// same structure is three places for it to drift, and a fourth image would
	// be added without them.
	//
	// The panic is the pairing made mechanical. A fourth image added without a
	// fourth snapshot would otherwise publish an empty mapping again, silently,
	// and the gate that catches that lives in a test rather than at startup.
	if len(out) != len(catalogueSnapshots) {
		panic("outscale: every catalogue image needs the snapshot it was cut from")
	}
	for i, image := range out {
		name, _ := image["ImageName"].(string)
		for key, value := range imageStructure(name, catalogueSnapshotOf(i)) {
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
			{"RegionName": p.region, "Endpoint": emulator.EndpointOf(r)},
		},
		"ResponseContext": p.context(),
	})
}

// readSubregions serves the region's subregions and applies the three filters
// FiltersSubregion declares (osc-sdk-go, pkg/osc/client.gen.go:5071). It used
// to ignore the body entirely and answer a single fixed zone; the body matters
// because the Terraform datasource is exactly the client that reads this
// before deciding where to place everything else (#269).
func (p *Pack) readSubregions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filters        filterSet `json:"Filters"`
		ResultsPerPage int       `json:"ResultsPerPage"`
		DryRun         *bool     `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refuseUnsupported(w, req.Filters, "SubregionNames", "RegionNames", "States") {
		return
	}
	out := make([]map[string]any, 0, len(p.subregions))
	for _, subregion := range p.subregions {
		if !matchesStrings(req.Filters, "SubregionNames", stringOf(subregion["SubregionName"])) ||
			!matchesStrings(req.Filters, "RegionNames", stringOf(subregion["RegionName"])) ||
			!matchesStrings(req.Filters, "States", stringOf(subregion["State"])) {
			continue
		}
		out = append(out, subregion)
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Subregions":      page(out, req.ResultsPerPage),
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
//
// Built per pack rather than held in a package variable since #290, because
// the region the names carry is the pack's datum, not the package's constant.
func netAccessPointServices(region string) []map[string]any {
	return []map[string]any{
		{"ServiceId": "pl-00000001", "ServiceName": "com.outscale." + region + ".api", "IpRanges": []any{"192.0.2.0/24"}},
		{"ServiceId": "pl-00000002", "ServiceName": "com.outscale." + region + ".fcu", "IpRanges": []any{"192.0.2.0/24"}},
		{"ServiceId": "pl-00000003", "ServiceName": "com.outscale." + region + ".lbu", "IpRanges": []any{"192.0.2.0/24"}},
		{"ServiceId": "pl-00000004", "ServiceName": "com.outscale." + region + ".eim", "IpRanges": []any{"192.0.2.0/24"}},
		{"ServiceId": "pl-00000005", "ServiceName": "com.outscale." + region + ".icu", "IpRanges": []any{"192.0.2.0/24"}},
		{"ServiceId": "pl-00000006", "ServiceName": "com.outscale." + region + ".oos", "IpRanges": []any{"198.51.100.0/24"}},
		{"ServiceId": "pl-00000007", "ServiceName": "com.outscale." + region + ".directlink", "IpRanges": []any{"198.51.100.0/24"}},
	}
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
	services := netAccessPointServices(p.region)
	out := make([]map[string]any, 0, len(services))
	for _, service := range services {
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

// Catalogue is what a client reads here before it can create anything, declared
// so the cross-pack guard can drive it (#218).
//
// Same trap as the Scaleway pack, which learned it the hard way and left the
// warning in this file: decline the inventory and the client fails on its first
// create, on a route nobody thought about.
func (p *Pack) Catalogue() []emulator.CatalogueEntry {
	return []emulator.CatalogueEntry{
		{
			Method:     "POST",
			Path:       pathPrefix + "ReadVmTypes",
			Reads:      "the Vm types a client sizes a machine from",
			Collection: "VmTypes",
		},
		{
			Method:     "POST",
			Path:       pathPrefix + "ReadImages",
			Reads:      "the images a create names, and Terraform resolves through a filter",
			Collection: "Images",
		},
	}
}
