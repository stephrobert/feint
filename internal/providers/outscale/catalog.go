package outscale

import (
	"net/http"

	"github.com/stephrobert/feint/internal/core/emulator"
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
// this project a day on Scaleway's volumes_constraint.
var vmTypes = []map[string]any{
	{"VmTypeName": "tinav6.c1r1p2", "VcoreCount": 1, "MemorySize": 1.0, "VolumeCount": 0, "MaxPrivateIps": 4, "Eth": 1, "BsuOptimized": false},
	{"VmTypeName": "tinav6.c2r2p2", "VcoreCount": 2, "MemorySize": 2.0, "VolumeCount": 0, "MaxPrivateIps": 8, "Eth": 1, "BsuOptimized": false},
	{"VmTypeName": "tinav6.c4r8p2", "VcoreCount": 4, "MemorySize": 8.0, "VolumeCount": 0, "MaxPrivateIps": 16, "Eth": 2, "BsuOptimized": true},
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

var images = []map[string]any{
	{"ImageId": "ami-00000001", "ImageName": "Ubuntu-24.04-2025.01", "Architecture": "x86_64", "State": "available", "RootDeviceType": "bsu", "SecureBoot": false, "AccountId": accountID, "ProductCodes": []any{linuxProductCode}},
	{"ImageId": "ami-00000002", "ImageName": "Debian-12-2025.01", "Architecture": "x86_64", "State": "available", "RootDeviceType": "bsu", "SecureBoot": false, "AccountId": accountID, "ProductCodes": []any{linuxProductCode}},
	{"ImageId": "ami-00000003", "ImageName": "Alpine-3.21-2025.01", "Architecture": "x86_64", "State": "available", "RootDeviceType": "bsu", "SecureBoot": false, "AccountId": accountID, "ProductCodes": []any{linuxProductCode}},
}

// runtimeImages maps an emulated OMI onto what the machine driver boots. Kept
// apart from the catalogue on purpose: it is not API surface, and holding it in
// the same map is how it ended up in a response.
var runtimeImages = map[string]string{
	"ami-00000001": "ubuntu:24.04",
	"ami-00000002": "debian:12",
	"ami-00000003": "alpine:3.21",
}

// accountID is the one account this emulator has. Outscale account IDs are
// twelve digits, like AWS's.
const accountID = "000000000001"

// defaultImageID is what the machine driver falls back to. It is not a default
// the API applies: CreateVms requires an ImageId, and the emulator refuses
// without one exactly as Outscale does.
const defaultImageID = "ami-00000001"

func (p *Pack) readVmTypes(w http.ResponseWriter, _ *http.Request) {
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"VmTypes":         vmTypes,
		"ResponseContext": p.context(),
	})
}

func (p *Pack) readImages(w http.ResponseWriter, _ *http.Request) {
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Images":          images,
		"ResponseContext": p.context(),
	})
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
