package outscale

import (
	"strings"
	"testing"
)

// Every catalogue image carries the three fields the Terraform provider
// dereferences without a nil guard.
//
// Reported by @vde-dis on #86: at outscale/outscale 1.7.0,
// data_source_outscale_images.go reads *BlockDeviceMappings, StateComment
// .StateCode and *PermissionsToLaunch.AccountIds in one loop where every
// neighbouring field goes through ptr.From. Missing them, the plugin segfaults
// and OpenTofu reports "Plugin did not respond" — a message naming neither a
// field nor a call, which is what made it hard to place.
//
// The contract gate cannot catch this: none of the three is `required` in
// Outscale's own api.yaml, so the answer without them is legal (#88). Only a
// real client, or the shape of a real answer, shows it — and the real cloud
// carries all three, measured in shapes/outscale.json.
func TestTheImageCatalogueCarriesWhatTheProviderDereferences(t *testing.T) {
	if len(images) == 0 {
		t.Fatal("the catalogue is empty, so this test measures nothing")
	}
	for _, image := range images {
		id, _ := image["ImageId"].(string)

		// A nil interface and an absent key both crash the provider: it takes
		// the address of the field either way. So presence is not enough, the
		// value has to be non-nil.
		for _, field := range []string{"BlockDeviceMappings", "StateComment", "PermissionsToLaunch"} {
			value, present := image[field]
			if !present || value == nil {
				t.Errorf("%s: %s is absent or nil, which segfaults the provider", id, field)
			}
		}

		perms, ok := image["PermissionsToLaunch"].(map[string]any)
		if !ok {
			t.Errorf("%s: PermissionsToLaunch is not an object", id)
			continue
		}
		// The provider dereferences AccountIds inside it, so an empty object
		// there moves the crash rather than fixing it.
		if ids, present := perms["AccountIds"]; !present || ids == nil {
			t.Errorf("%s: PermissionsToLaunch.AccountIds is absent, so the crash only moves", id)
		}
	}
}

// The catalogue's device mapping stays empty until a snapshot backs it.
//
// #383 asked for a filled mapping and this is why it is not filled: with one,
// no state of the pack satisfies all three of its own controls at once.
// Measured on 2026-08-21, each by running it:
//
//   - filled, keys omitted        -> shapes:check: missing Bsu.{SnapshotId,Iops}
//   - filled, keys declined       -> score.sh: "declines whose field the
//     emulator now serves", on the terraform, opentofu, oapi-cli and fields
//     legs at once, because CreateImage answers both
//   - filled, declined, and       -> terraform.sh: "the registered image does
//     CreateImage omitting them      not name the snapshot it was cut from"
//
// The third is a real client refusing, which outranks the other two. So the
// mapping stays empty, ReadImages declines nothing of Bsu, and #389 is what
// makes the filled mapping possible: a snapshot the pack really holds, which
// lets the catalogue name one without inventing it (rule 4).
//
// This test fails the day somebody fills the mapping without doing #389 first,
// which is the whole point of writing it down here rather than in a comment.
func TestTheCatalogueMappingStaysEmptyUntilASnapshotBacksIt(t *testing.T) {
	if len(images) == 0 {
		t.Fatal("the catalogue is empty, so this test measures nothing")
	}
	for _, image := range images {
		mappings, ok := image["BlockDeviceMappings"].([]any)
		if !ok {
			t.Errorf("%v: BlockDeviceMappings is absent or not a list, which segfaults the provider", image["ImageId"])
			continue
		}
		if len(mappings) != 0 {
			t.Errorf("%v: the mapping is filled; see #389 — a filled mapping needs a snapshot this pack really holds, or one of shapes:check, score.sh and terraform.sh will refuse it", image["ImageId"])
		}
	}

	// The other half, and the one that makes this a pair rather than a rule:
	// nothing of Bsu may be declined while CreateImage answers it.
	for _, d := range (&Pack{}).DeclinedFields() {
		if d.Operation == "osc/Client.ReadImages" && strings.Contains(d.Path, "Bsu.") {
			t.Errorf("%s is declined while CreateImage serves it: score.sh fails every leg that creates an image", d.Path)
		}
	}
}
