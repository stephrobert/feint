package scaleway

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"sort"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
)

// A local emulator has no inventory of its own, but it cannot decline the
// catalogue either: `scw instance server create` reads the server types and the
// default image BEFORE it creates anything, and gives up on a 404. Declining
// these endpoints makes the official CLI unusable, which defeats the purpose.
//
// So the catalogue is served from a small, fixed table. Since #279 its rows
// are an excerpt of what the real cloud publishes rather than invented values,
// because the table turned out to be a whitelist, not scenery: the Terraform
// provider validates a server's type against it before creating anything.
// What stays fiction is everything around the rows — one table for every
// zone, nothing enforced — and docs/limits.md states it.

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

// publishedServerTypes is a verbatim excerpt of the table the real cloud
// publishes: GET https://api.scaleway.com/instance/v1/zones/fr-par-1/products/servers
// (no authentication required), captured 2026-08-19. The values used to be
// invented by a constructor; #279 measured that the table is not scenery — the
// Terraform provider validates a server's type against it before creating
// anything, so every row is a compatibility claim, and an invented row is a
// claim nothing published. The file is the measurement; the two deviations
// from it are applied in code below, where they can be read and tested.
//
//go:embed catalog_servers.json
var publishedServerTypes []byte

// catalogue is the fixed set of types the emulator accepts, loaded from the
// published excerpt above.
//
// How much of the real table to carry was #279's first question. The answer:
// every size of every family already carried (PLAY2, DEV1, GP1, PRO2 — a stack
// that outgrows GP1-XS should not die on GP1-S), plus what the surveyed stacks
// name (STARDUST1-S, the kiwinet-infra-cloud witness). Not the whole table:
// the real one is 136 rows in fr-par-1 alone, varies per zone, and includes
// GPU and local-storage families whose published constraints this emulator
// would falsify (see the min_size trap below). A family outside the excerpt
// joins it when a stack asks, values captured from the same endpoint — #279
// itself is the template for that.
//
// COPARM1-* is refused, not missing: the terraform-talos survey witness
// defaults to COPARM1-2C-8G, and the family is absent from all nine zones of
// the real catalogue (measured 2026-08-19, same endpoint, every page), while
// genuinely end-of-service families — START1, VC1, X64 — are still listed
// with end_of_service:true. Scaleway withdrew it. Serving it here would let a
// plan pass that production refuses, the #268 class of lie in the more
// dangerous direction. TestTheRetiredArmFamilyStaysRetired holds the line.
var catalogue = func() map[string]*serverType {
	var published struct {
		Servers map[string]*serverType `json:"servers"`
	}
	if err := json.Unmarshal(publishedServerTypes, &published); err != nil {
		// A compile-time asset: unreachable unless the embedded file is
		// edited into invalidity, and TestServerTypesArePaged would say so.
		panic("scaleway: catalog_servers.json: " + err.Error())
	}
	for _, st := range published.Servers {
		// Deviation 1 — per_volume_constraint is served empty, not with the
		// published l_ssd bounds: this emulator attaches no local volume, so
		// a bound would enter the client's size arithmetic with nothing
		// behind it. DeclinedFields() declares this to the shapes gate.
		st.PerVolumeConstraint = map[string]any{}
	}
	// What is deliberately NOT rewritten here: volumes_constraint.min_size.
	// It must stay 0 — the CLI sums the LOCAL volumes of a create request and
	// refuses anything below the minimum with "total local volume size must
	// be between ...", and this catalogue attaches none. Every type in the
	// excerpt publishes 0 already, so the constraint is carried verbatim, and
	// TestCatalogueKeepsTheLocalVolumeTrapDisarmed makes pasting a future
	// type whose real minimum is non-zero fail there, loudly, instead of as a
	// CLI refusal three tools away. Zeroing it silently here would hide the
	// exact measurement that test exists to surface.
	return published.Servers
}()

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

// listServerTypes pages like every other list here: the SDK declares page and
// per_page on ListServersTypes, and the handler used to ignore both — the whole
// catalogue fits under any client's default page size, so no real client saw
// it, but a parameter the contract declares and a handler drops is the class
// #271 names. The response's `servers` is an object keyed by type name in
// their SDK, so the page is a window over the sorted names.
//
// TestServerTypesArePaged fails without it.
func (p *Pack) listServerTypes(w http.ResponseWriter, r *http.Request) {
	if _, ok := zoneOf(w, r); !ok {
		return
	}
	names := make([]string, 0, len(catalogue))
	for name := range catalogue {
		names = append(names, name)
	}
	sort.Strings(names)
	start, end := parsePage(r).slice(len(names))
	page := make(map[string]*serverType, end-start)
	for _, name := range names[start:end] {
		page[name] = catalogue[name]
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"servers":     page,
		"total_count": len(catalogue),
	})
}

// listLocalImages answers the marketplace lookup the CLI performs to resolve an
// image label into an ID. Any label is accepted and answers 200, because
// refusing an unknown one would block a workflow the emulator has no opinion
// about (docs/limits.md) — but an unknown label maps onto unknownImageID, which
// no boot resolves, rather than onto an image the client never named (#83).
func (p *Pack) listLocalImages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	label := q.Get("image_label")
	if label == "" {
		label = defaultImageLabel
	}
	zone := q.Get("zone")
	if zone == "" {
		zone = "fr-par-1"
	}
	// The one local image answers as x86_64 and instance_sbs, so the two
	// declared filters are equalities against those published values: asking
	// for arm64 or instance_local truthfully finds nothing, where dropping the
	// filter answered an image of the wrong architecture with a 200 (#277).
	// unknown_type is ListLocalImagesRequest's own zero value, not a filter.
	if arch := q.Get("arch"); arch != "" && arch != "x86_64" {
		writeEmptyLocalImages(w)
		return
	}
	if imageType := q.Get("type"); imageType != "" && imageType != "unknown_type" && imageType != "instance_sbs" {
		writeEmptyLocalImages(w)
		return
	}
	// One image, so every declared order serves it already — but the value is
	// still validated against the enum, because "unknown order_by" must not
	// become "silently unsorted" the day this catalogue grows a second entry.
	switch q.Get("order_by") {
	case "", "type_asc", "type_desc", "created_at_asc", "created_at_desc":
	default:
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "order_by",
			Reason:       "constraint",
			HelpMessage:  "unknown order_by " + q.Get("order_by"),
		})
		return
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

	images := []map[string]any{{
		"id":                          imageID,
		"compatible_commercial_types": compatible,
		"arch":                        "x86_64",
		"zone":                        zone,
		"label":                       label,
		"type":                        "instance_sbs",
	}}
	// Paged like every list: page=2 must answer empty, not the same image
	// again, or the SDK's pagination loop never terminates.
	start, end := parsePage(r).slice(len(images))
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"local_images": images[start:end],
		"total_count":  len(images),
	})
}

// writeEmptyLocalImages is the truthful answer to a filter the emulated
// catalogue cannot match.
func writeEmptyLocalImages(w http.ResponseWriter) {
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"local_images": []map[string]any{},
		"total_count":  0,
	})
}

// Catalogue is what a client reads here before it can create anything, declared
// so the cross-pack guard can drive it (#218).
//
// The list is the sequence `scw instance server create` actually walks, measured
// with `scw -D`: the server types it sizes from, the marketplace image it
// defaults to, and the image endpoint it resolves that default through. A 404 on
// any one of them fails the command before it posts a server, with an error that
// names none of this.
func (p *Pack) Catalogue() []emulator.CatalogueEntry {
	const zones = "/instance/v1/zones/{zone}"
	return []emulator.CatalogueEntry{
		{
			Method:     "GET",
			Path:       zones + "/products/servers",
			Reads:      "the server types `scw instance server create` sizes a machine from",
			Collection: "servers",
		},
		{
			Method:     "GET",
			Path:       "/marketplace/v2/local-images",
			Reads:      "the image the CLI defaults to when the caller names none",
			Collection: "local_images",
		},
		{
			Method:     "GET",
			Path:       zones + "/images",
			Reads:      "the images a client lists, and resolves a named one through",
			Collection: "images",
		},
	}
}
