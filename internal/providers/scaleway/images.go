package scaleway

import (
	"net/http"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// unknownImageID is the UUID answered for an image label no catalogue holds.
// The lookup still succeeds — docs/limits.md declares identifiers unchecked,
// deliberately — but this UUID maps back onto no bootable image, so a runtime
// refuses to boot it instead of substituting a distribution the client never
// named (#83). Fixed, like every catalogue UUID, because Terraform keeps it in
// state.
const unknownImageID = "99999999-9999-4999-8999-999999999999"

// getImage answers the lookup the CLI performs after resolving a label. An
// unknown ID is still served — refusing would break scripts that hardcode a
// real one (docs/limits.md) — under the default label, which is display
// fiction like the rest of the catalogue; a known ID answers its own label.
func (p *Pack) getImage(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	label := defaultImageLabel
	switch l, known := labelByID[id]; {
	case id == "":
		id = marketplaceImages[defaultImageLabel].ID
	case known:
		label = l
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"image": p.imageView(zone, id, label)})
}

// imageView is the shape the SDK decodes into instance.Image, shared by the
// image endpoint and by the image a server carries.
//
// A server must carry the object, never null. The Terraform provider reads the
// image of the server it just created without checking, so a null there does not
// produce a diff: it crashes the plugin, which surfaces as "Plugin did not
// respond" with nothing in the emulator's log.
// imageEpoch is when the emulated images were "built". A fixed instant, because
// the catalogue is fixed.
const imageEpoch = "2025-01-01T00:00:00Z"

func (p *Pack) imageView(zone, id, label string) map[string]any {
	// Fixed, not the wall clock. The catalogue is stable across runs by design,
	// and a real image's dates do not move: stamping each read with Now() gave a
	// client a modification_date that changed every time it looked, which is a
	// permanent diff for anything that compares.
	stamp := imageEpoch
	return map[string]any{
		"id":                id,
		"name":              label,
		"arch":              "x86_64",
		"creation_date":     stamp,
		"modification_date": stamp,
		"organization":      defaultOrganization,
		"project":           defaultProject,
		"public":            true,
		"state":             "available",
		"tags":              []string{},
		"zone":              zone,
		"extra_volumes":     map[string]any{},
		"from_server":       nil,
		"root_volume": map[string]any{
			"id":          "33333333-3333-4333-8333-333333333333",
			"name":        label + "-root",
			"size":        20_000_000_000,
			"volume_type": "b_ssd",
		},
	}
}

// resolveImage maps what a create request put in `image` onto what the
// emulator needs: the ID to publish, the name to display, and the catalogue
// label the machine driver turns into a base image.
//
// Clients send either form. The Terraform provider resolves the label through
// the marketplace first and sends a UUID; the CLI can send the label itself.
// Telling them apart is what lets `image = "debian_bookworm"` boot a Debian
// rather than the default.
//
// label is empty when the identifier is in no catalogue. The create still
// succeeds — deliberate, and documented in docs/limits.md — but a boot refuses
// an empty label rather than substituting a distribution the client never
// named (#83). display keeps the response behaviour of before: the requested
// label verbatim, or the default label for a foreign UUID, because an image
// name in a response is catalogue fiction either way.
func resolveImage(requested string) (id, display, label string) {
	switch {
	case requested == "":
		return marketplaceImages[defaultImageLabel].ID, defaultImageLabel, defaultImageLabel
	case looksLikeUUID(requested):
		if l, known := labelByID[requested]; known {
			return requested, l, l
		}
		return requested, defaultImageLabel, ""
	default:
		if entry, known := marketplaceImages[requested]; known {
			return entry.ID, requested, requested
		}
		return unknownImageID, requested, ""
	}
}

func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}
