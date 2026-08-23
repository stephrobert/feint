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

// No image this pack can serve carries a field ReadImages declines.
//
// This replaces the test that guarded the empty catalogue mapping until a
// snapshot backed it, and which went with the decline it protected: #389
// filled the mapping by backing the catalogue with a snapshot the pack really
// holds, so the thing that test protected no longer exists. What it protected
// was right about one thing, and that half is kept here in a form that
// measures instead of asserting a constant.
//
// The rule, and it is the general finding of #389 rather than a fact about
// Bsu: a field decline is written against an OPERATION, and ReadImages answers
// two kinds of object — the fixed catalogue, and an image a client cut from its
// own snapshot. A decline true for one kind and false for the other is fiction
// whichever way the code is arranged, and tools/conformance/score.sh says so on
// four legs at once ("field declines whose field the emulator now serves").
//
// So the property is not "the mapping is empty" but "nothing this pack can
// produce carries a declined field". Both producers are checked: the catalogue
// table, and the defaults createImage fills in — the second by naming the keys
// it writes, since a value a client supplies is refused at intake
// (TestCreateImageRefusesAnIopsItCannotHonour).
func TestNoImageThisPackServesCarriesADeclinedField(t *testing.T) {
	declined := map[string]bool{}
	for _, d := range (&Pack{}).DeclinedFields() {
		if d.Operation != "osc/Client.ReadImages" {
			continue
		}
		if key, ok := strings.CutPrefix(d.Path, "Images[].BlockDeviceMappings[].Bsu."); ok {
			declined[key] = true
		}
	}
	if len(declined) == 0 {
		t.Skip("ReadImages declines nothing of Bsu, so there is nothing to keep honest")
	}

	// Producer one: the fixed catalogue.
	for _, image := range images {
		id, _ := image["ImageId"].(string)
		mappings, _ := image["BlockDeviceMappings"].([]any)
		if len(mappings) == 0 {
			t.Errorf("%s publishes no device mapping; #389 backed the catalogue with a snapshot precisely so it could", id)
			continue
		}
		for _, raw := range mappings {
			mapping, _ := raw.(map[string]any)
			bsu, _ := mapping["Bsu"].(map[string]any)
			for key := range bsu {
				if declined[key] {
					t.Errorf("%s serves Bsu.%s and ReadImages declines it", id, key)
				}
			}
		}
	}

	// Producer two: the keys createImage writes on a mapping of its own.
	for _, key := range createImageBsuKeys {
		if declined[key] {
			t.Errorf("createImage writes Bsu.%s and ReadImages declines it: score.sh fails every leg that creates an image", key)
		}
	}
}

// createImageBsuKeys names what createImage puts in a mapping's Bsu.
//
// Written here rather than derived, because deriving it would mean calling the
// handler, and the point of the list is to be read beside the handler when
// somebody adds a key to it. The test above is what makes forgetting to update
// it cost something, and TestCreateImageRefusesAnIopsItCannotHonour is what
// proves the list is not merely optimistic — it drives the real handler and
// reads the answer back.
var createImageBsuKeys = []string{"SnapshotId", "VolumeSize", "VolumeType", "DeleteOnVmDeletion"}

// The catalogue's identifiers stay out of the corpus sanitiser's minting space.
//
// #395 measured what happens otherwise: the sanitiser hands out prefixed
// identifiers as a shared counter in eight hexadecimal digits, so ami-00000001
// came out naming a catalogue image of this emulator and two corpus exemptions
// exist to carry the confusion. #389 added three snapshots and three volume
// identifiers to this file; numbering them the same way would have widened a
// known defect for free.
//
// The test is on the objects #389 added, not on the pre-existing ami- ones:
// those are #395's to move, and asserting on them here would make this test
// fail for somebody else's issue.
func TestTheCatalogueIdentifiersStayOutOfTheMintingSpace(t *testing.T) {
	if len(catalogueSnapshots) == 0 {
		t.Fatal("the catalogue holds no snapshot, so this test measures nothing")
	}
	for _, snapshot := range catalogueSnapshots {
		for _, key := range []string{"SnapshotId", "VolumeId"} {
			id, _ := snapshot[key].(string)
			_, hex, ok := strings.Cut(id, "-")
			if !ok {
				t.Errorf("%s %q carries no prefix", key, id)
				continue
			}
			// The mint counts up from one, so a value the counter can reach in
			// any plausible recording is one with leading zeros.
			if strings.HasPrefix(hex, "0000") {
				t.Errorf("%s %q sits in the sanitiser's minting space, which is #395", key, id)
			}
		}
	}
}

// Every catalogue image is paired with the snapshot it was cut from, and the
// pairing is by position.
//
// The init in catalog.go panics when the two tables differ in length, which is
// the mechanical half. This is the half that says what the pairing means: the
// image's mapping names its own snapshot and no other, so a fourth image cannot
// silently borrow the third one's provenance.
func TestEveryCatalogueImageNamesItsOwnSnapshot(t *testing.T) {
	seen := map[string]string{}
	for i, image := range images {
		id, _ := image["ImageId"].(string)
		mappings, _ := image["BlockDeviceMappings"].([]any)
		if len(mappings) == 0 {
			t.Fatalf("%s publishes no device mapping", id)
		}
		mapping, _ := mappings[0].(map[string]any)
		bsu, _ := mapping["Bsu"].(map[string]any)
		got, _ := bsu["SnapshotId"].(string)
		want, _ := catalogueSnapshots[i]["SnapshotId"].(string)
		if got != want {
			t.Errorf("%s names %q where its own snapshot is %q", id, got, want)
		}
		if other, taken := seen[got]; taken {
			t.Errorf("%s and %s name the same snapshot %q", other, id, got)
		}
		seen[got] = id
	}
}
