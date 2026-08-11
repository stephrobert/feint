package scaleway

import (
	"net/http"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
)

// A local emulator has no inventory of its own, but it cannot decline the
// catalogue either: `scw instance server create` reads the server types and the
// default image BEFORE it creates anything, and gives up on a 404. Declining
// these endpoints makes the official CLI unusable, which defeats the purpose.
//
// So the catalogue is served from a small, fixed table. It is fiction, and it is
// labelled as such in the docs, but it is the fiction the clients need.

// serverType mirrors instance.ServerType. Only the fields the CLI and the
// Terraform provider actually read are populated.
type serverType struct {
	MonthlyPrice float32  `json:"monthly_price"`
	HourlyPrice  float32  `json:"hourly_price"`
	AltNames     []string `json:"alt_names"`
	Ncpus        uint32   `json:"ncpus"`
	Gpu          uint64   `json:"gpu"`
	RAM          uint64   `json:"ram"`
	Arch         string   `json:"arch"`
	Baremetal    bool     `json:"baremetal"`
	Network      struct {
		Interfaces           []any  `json:"interfaces"`
		SumInternalBandwidth uint64 `json:"sum_internal_bandwidth"`
		SumInternetBandwidth uint64 `json:"sum_internet_bandwidth"`
		IPv6Support          bool   `json:"ipv6_support"`
	} `json:"network"`
	VolumesConstraint struct {
		MinSize uint64 `json:"min_size"`
		MaxSize uint64 `json:"max_size"`
	} `json:"volumes_constraint"`
	Capabilities struct {
		BlockStorage bool     `json:"block_storage"`
		BootTypes    []string `json:"boot_types"`
		// The four below were absent until #93, and every type the real cloud
		// publishes carries them — measured in shapes/scaleway.json, not read
		// from the SDK. PlacementGroups and PrivateNetwork are the two a client
		// reads before it acts: one decides whether a placement group can be
		// asked for at all, the other how many private networks a type takes.
		HotSnapshotsLocalVolume bool   `json:"hot_snapshots_local_volume"`
		MaxFileSystems          uint64 `json:"max_file_systems"`
		PlacementGroups         bool   `json:"placement_groups"`
		PrivateNetwork          uint64 `json:"private_network"`
	} `json:"capabilities"`
	// PerVolumeConstraint is the sibling of VolumesConstraint, and it was
	// missing while its twin carried the trap this pack documents: the CLI sums
	// local volumes against volumes_constraint. A client reading the per-volume
	// limits found nothing.
	//
	// Serialised as an empty object rather than a populated one: this emulator
	// attaches no local volume, so it has no per-volume limit to state, and
	// inventing one would put a bound in a client's arithmetic that nothing
	// enforces.
	PerVolumeConstraint map[string]any `json:"per_volume_constraint"`
	// BlockBandwidth, EndOfService and the scratch fields complete what the
	// real answer carries. GpuInfo and MigProfile are null there too — a type
	// with no GPU has neither — so null is the honest value rather than an
	// empty object pretending there is something to read.
	BlockBandwidth                uint64 `json:"block_bandwidth"`
	EndOfService                  bool   `json:"end_of_service"`
	GpuInfo                       any    `json:"gpu_info"`
	MigProfile                    any    `json:"mig_profile"`
	ScratchStorageMaxSize         uint64 `json:"scratch_storage_max_size"`
	ScratchStorageMaxVolumesCount uint64 `json:"scratch_storage_max_volumes_count"`
}

func newServerType(cpus uint32, ramGiB uint64, hourly float32) *serverType {
	st := &serverType{
		MonthlyPrice: hourly * 24 * 30,
		HourlyPrice:  hourly,
		AltNames:     []string{},
		Ncpus:        cpus,
		RAM:          ramGiB << 30,
		Arch:         "x86_64",
	}
	st.Network.Interfaces = []any{}
	st.Network.SumInternalBandwidth = 100_000_000
	st.Network.SumInternetBandwidth = 100_000_000
	st.Network.IPv6Support = true
	// min_size stays at 0: the CLI sums the LOCAL volumes of a create request and
	// refuses anything below the minimum. Modern Scaleway types boot from block
	// storage and carry no local volume, so a non-zero minimum here would make
	// `scw instance server create` fail with "total local volume size must be
	// between ...". Types that really require local storage are not emulated.
	st.VolumesConstraint.MinSize = 0
	st.VolumesConstraint.MaxSize = 200_000_000_000
	st.Capabilities.BlockStorage = true
	st.Capabilities.BootTypes = []string{"local", "rescue"}
	// Placement groups are served by this pack, so the capability says so; a
	// client that checks it before asking would otherwise never ask.
	st.Capabilities.PlacementGroups = true
	st.Capabilities.PrivateNetwork = 8
	st.Capabilities.MaxFileSystems = 0
	st.Capabilities.HotSnapshotsLocalVolume = false
	// Empty rather than populated: no local volume is attached here, so there
	// is no per-volume bound to state. The key exists because the real answer
	// has it and a client reads it; its emptiness is the honest content.
	st.PerVolumeConstraint = map[string]any{}
	st.BlockBandwidth = 209_715_200
	st.EndOfService = false
	st.ScratchStorageMaxSize = 0
	st.ScratchStorageMaxVolumesCount = 0
	return st
}

// catalogue is the fixed set of types the emulator accepts. Names are real
// Scaleway commercial types so copied documentation and existing Terraform code
// work unchanged.
var catalogue = map[string]*serverType{
	"PLAY2-PICO": newServerType(1, 2, 0.0086),
	"PLAY2-NANO": newServerType(2, 4, 0.0172),
	"DEV1-S":     newServerType(2, 2, 0.0084),
	"DEV1-M":     newServerType(3, 4, 0.0168),
	"DEV1-L":     newServerType(4, 8, 0.0336),
	"GP1-XS":     newServerType(4, 16, 0.0771),
	"PRO2-XXS":   newServerType(2, 8, 0.0301),
}

// defaultImageLabel is the image the CLI asks for when none is given.
const defaultImageLabel = "ubuntu_jammy"

// marketplaceImages is the emulated marketplace: which label answers which
// stable UUID, and what the machine driver boots for it — login included,
// because an image resolved without its login is a machine nobody can enter.
// Scaleway provisions root on every image (`scw instance server ssh` documents
// username=root), so every row carries the same login; that is this cloud's own
// shape, where Exoscale's login varies per template.
//
// The table is fiction, like the rest of this catalogue (docs/limits.md), and
// exact on purpose: image labels used to be matched by substring with a
// fallback, which turned ubuntu_focal, centos, rocky — every label the table
// does not list — into a silent Ubuntu 22.04 (#83). An identifier outside this
// table now resolves to nothing and the shared binding refuses the boot.
//
// One UUID per label, so a client that resolves a label through the
// marketplace and sends the UUID back still names the distribution it chose;
// a single shared UUID is how `image = "debian_bookworm"` used to boot an
// Ubuntu under Terraform. The UUIDs are fixed because Terraform keeps them in
// state, and ubuntu_jammy keeps the UUID this emulator has always answered.
var marketplaceImages = map[string]struct {
	ID   string
	Boot machine.Image
}{
	"ubuntu_noble":    {"55555555-5555-4555-8555-555555555555", machine.Image{Ref: "ubuntu:24.04", User: DefaultUser}},
	"ubuntu_jammy":    {"22222222-2222-4222-8222-222222222222", machine.Image{Ref: "ubuntu:22.04", User: DefaultUser}},
	"debian_bookworm": {"66666666-6666-4666-8666-666666666666", machine.Image{Ref: "debian:12", User: DefaultUser}},
	"debian_trixie":   {"77777777-7777-4777-8777-777777777777", machine.Image{Ref: "debian:13", User: DefaultUser}},
	// Not a label the real marketplace lists today; kept because this emulator
	// has always booted an Alpine for it — previously by accident of substring
	// matching, now by decision — and the question that raised #83 was
	// precisely "does it boot the OS I asked for", asked about Alpine.
	"alpine": {"88888888-8888-4888-8888-888888888888", machine.Image{Ref: "alpine:3.21", User: DefaultUser}},
}

// labelByID is the reverse of marketplaceImages, for the UUID a client sends
// after resolving a label through the marketplace.
var labelByID = func() map[string]string {
	out := make(map[string]string, len(marketplaceImages))
	for label, entry := range marketplaceImages {
		out[entry.ID] = label
	}
	return out
}()

func (p *Pack) listServerTypes(w http.ResponseWriter, r *http.Request) {
	if _, ok := zoneOf(w, r); !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"servers":     catalogue,
		"total_count": len(catalogue),
	})
}

// listLocalImages answers the marketplace lookup the CLI performs to resolve an
// image label into an ID. Any label is accepted and answers 200, because
// refusing an unknown one would block a workflow the emulator has no opinion
// about (docs/limits.md) — but an unknown label maps onto unknownImageID, which
// no boot resolves, rather than onto an image the client never named (#83).
func (p *Pack) listLocalImages(w http.ResponseWriter, r *http.Request) {
	label := r.URL.Query().Get("image_label")
	if label == "" {
		label = defaultImageLabel
	}
	zone := r.URL.Query().Get("zone")
	if zone == "" {
		zone = "fr-par-1"
	}

	compatible := make([]string, 0, len(catalogue))
	for name := range catalogue {
		compatible = append(compatible, name)
	}

	// Fixed UUIDs, one per label: Terraform stores the image ID in state, and a
	// value that changed between two runs would show up as a permanent diff.
	imageID := unknownImageID
	if entry, known := marketplaceImages[label]; known {
		imageID = entry.ID
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"local_images": []map[string]any{{
			"id":                          imageID,
			"compatible_commercial_types": compatible,
			"arch":                        "x86_64",
			"zone":                        zone,
			"label":                       label,
			"type":                        "instance_sbs",
		}},
		"total_count": 1,
	})
}
