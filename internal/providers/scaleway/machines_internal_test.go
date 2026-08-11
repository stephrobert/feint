package scaleway

import (
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
)

// One case per row of the marketplace table, spelled out rather than iterated
// from the table itself: a guard that refused everything would pass a loop
// over an emptied table and break the product (#83).
func TestScalewayImageResolutionIsExact(t *testing.T) {
	rows := map[string]machine.Image{
		"ubuntu_noble":    {Ref: "ubuntu:24.04", User: "root"},
		"ubuntu_jammy":    {Ref: "ubuntu:22.04", User: "root"},
		"debian_bookworm": {Ref: "debian:12", User: "root"},
		"debian_trixie":   {Ref: "debian:13", User: "root"},
		"alpine":          {Ref: "alpine:3.21", User: "root"},
	}
	if len(rows) != len(marketplaceImages) {
		t.Fatalf("the table serves %d labels and this test knows %d: add the row here, one per label", len(marketplaceImages), len(rows))
	}
	for label, want := range rows {
		got, known := imageFor(label)
		if !known {
			t.Errorf("%s: a served label stopped resolving", label)
			continue
		}
		if got != want {
			t.Errorf("%s: resolved to %+v, want %+v — image and login travel together", label, got, want)
		}
	}

	// The substring victims: every one of these used to become Ubuntu 22.04
	// silently, including the labels that merely contain a served word.
	for _, label := range []string{"ubuntu_focal", "centos", "rocky", "fedora", "alpine_edge", "my_ubuntu_jammy_copy", ""} {
		if img, known := imageFor(label); known {
			t.Errorf("%q: resolved to %+v, want no resolution at all", label, img)
		}
	}
}

// The identifiers a client can send round-trip through the marketplace: a
// label resolves to its own UUID, and that UUID resolves back to the same
// boot. A single shared UUID is how `image = "debian_bookworm"` used to boot
// an Ubuntu under Terraform, which resolves labels through the marketplace
// and sends the UUID back.
func TestResolveImageRoundTripsTheMarketplace(t *testing.T) {
	for label, entry := range marketplaceImages {
		id, display, boot := resolveImage(label)
		if id != entry.ID || display != label || boot != label {
			t.Errorf("%s: resolved to (%s, %s, %s), want (%s, %s, %s)", label, id, display, boot, entry.ID, label, label)
		}
		id, display, boot = resolveImage(entry.ID)
		if id != entry.ID || display != label || boot != label {
			t.Errorf("%s: the marketplace UUID resolved to (%s, %s, %s), want its own label back", entry.ID, id, display, boot)
		}
	}
	if id, display, boot := resolveImage(""); id != marketplaceImages[defaultImageLabel].ID || display != defaultImageLabel || boot != defaultImageLabel {
		t.Errorf("no image requested: resolved to (%s, %s, %s), want the default", id, display, boot)
	}
}

// An identifier no catalogue holds is still accepted — docs/limits.md declares
// identifiers unchecked, deliberately — but resolves to no bootable label, so
// a runtime refuses the boot instead of substituting.
func TestResolveImageLeavesAnUnknownIdentifierUnresolved(t *testing.T) {
	if id, display, boot := resolveImage("rocky"); id != unknownImageID || display != "rocky" || boot != "" {
		t.Errorf("unknown label: resolved to (%s, %s, %q), want (%s, rocky, empty)", id, display, boot, unknownImageID)
	}
	const foreign = "de305d54-75b4-431b-adb2-eb6b9e546014"
	if id, display, boot := resolveImage(foreign); id != foreign || display != defaultImageLabel || boot != "" {
		t.Errorf("foreign UUID: resolved to (%s, %s, %q), want the UUID kept, the default display, no boot", id, display, boot)
	}
}
