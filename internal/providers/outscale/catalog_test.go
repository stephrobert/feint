package outscale

import "testing"

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
