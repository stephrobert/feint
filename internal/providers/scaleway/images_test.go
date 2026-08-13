package scaleway_test

import (
	"net/http"
	"testing"
)

// An image a client cut reads back as itself, not as the catalogue.
//
// GET /images/{id} answered from the fixed catalogue only until SW-2, so an
// image just created read back under the default label with a different root
// volume. Terraform calls that "Provider produced inconsistent result after
// apply"; `scw instance image get` simply describes the wrong object.
func TestAnImageReadsBackAsItWasCreated(t *testing.T) {
	ts := newTestServer(t)
	_, server := serverWith(t, ts, `{"name":"golden","commercial_type":"DEV1-S"}`)
	snapshotID := snapshotOfVolume(t, ts, "golden-snap", rootVolumeOf(t, server))

	status, created := do(t, ts, "POST", zoneURL+"/images",
		`{"name":"golden-img","root_volume":"`+snapshotID+`","tags":["release"]}`)
	if status != http.StatusCreated {
		t.Fatalf("create image: expected 201, got %d (%v)", status, created)
	}
	image, _ := created["image"].(map[string]any)
	id, _ := image["id"].(string)

	// Not the catalogue's: a client image is the client's, and public=true would
	// tell a caller this came with the emulator.
	if image["public"] != false {
		t.Errorf("public is %v, want false on an image the client cut", image["public"])
	}
	// A recording from a real account shows this key present and null on every
	// image. The SDK alone would have justified omitting it.
	if got, present := image["default_bootscript"]; !present || got != nil {
		t.Errorf("default_bootscript is %v (present=%v), want null and present", got, present)
	}
	root, _ := image["root_volume"].(map[string]any)
	if root == nil || root["id"] != snapshotID {
		t.Errorf("root_volume does not name the snapshot it was cut from: %v", image["root_volume"])
	}

	status, got := do(t, ts, "GET", zoneURL+"/images/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("get image: expected 200, got %d (%v)", status, got)
	}
	read, _ := got["image"].(map[string]any)
	for _, field := range []string{"id", "name", "public", "state", "arch", "creation_date", "zone"} {
		if read[field] != image[field] {
			t.Errorf("%s reads back as %v, created as %v", field, read[field], image[field])
		}
	}
}

// An image cut from a snapshot nobody took is refused.
//
// CreateImageRequest.RootVolume names the snapshot, which reads oddly and is
// what the API does. Recording an image over a snapshot that does not exist
// would answer success about an object with nothing behind it.
func TestAnImageCutFromNothingIsRefused(t *testing.T) {
	ts := newTestServer(t)

	status, got := do(t, ts, "POST", zoneURL+"/images",
		`{"name":"orphan","root_volume":"11111111-1111-4111-8111-111111111111"}`)
	if status != http.StatusNotFound {
		t.Fatalf("image from an unknown snapshot: expected 404, got %d (%v)", status, got)
	}

	// A missing root_volume is the client's mistake, not a missing object, and
	// the API answers it as an argument error.
	status, got = do(t, ts, "POST", zoneURL+"/images", `{"name":"orphan"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("image with no root_volume: expected 400, got %d (%v)", status, got)
	}
}

// Listing answers the client's images and the catalogue's, in that order.
//
// Both, because both are real to a caller: this is how someone checks the image
// they just cut exists, and the catalogue entries are what a create resolves
// against. Answering only one would make the command lie by omission.
func TestListingImagesCarriesTheCatalogueAndTheClients(t *testing.T) {
	ts := newTestServer(t)

	status, before := do(t, ts, "GET", zoneURL+"/images", "")
	if status != http.StatusOK {
		t.Fatalf("list images: expected 200, got %d (%v)", status, before)
	}
	catalogue, _ := before["images"].([]any)
	if len(catalogue) == 0 {
		t.Fatal("listing images answered nothing, and the catalogue is never empty")
	}

	_, server := serverWith(t, ts, `{"name":"golden","commercial_type":"DEV1-S"}`)
	snapshotID := snapshotOfVolume(t, ts, "golden-snap", rootVolumeOf(t, server))
	if status, got := do(t, ts, "POST", zoneURL+"/images",
		`{"name":"golden-img","root_volume":"`+snapshotID+`"}`); status != http.StatusCreated {
		t.Fatalf("create image: expected 201, got %d (%v)", status, got)
	}

	_, after := do(t, ts, "GET", zoneURL+"/images", "")
	items, _ := after["images"].([]any)
	if len(items) != len(catalogue)+1 {
		t.Errorf("listing found %d images after cutting one, want %d", len(items), len(catalogue)+1)
	}

	_, filtered := do(t, ts, "GET", zoneURL+"/images?name=golden-img", "")
	only, _ := filtered["images"].([]any)
	if len(only) != 1 {
		t.Errorf("filtering by name found %d images, want 1: %v", len(only), filtered)
	}
}

// A catalogue image is not the client's to edit, and the emulator says which
// rather than answering 404.
//
// A 404 would read as "gone" on an image `scw instance server create` resolves
// against every run, and would send a client looking for a deletion nobody
// performed. The same answer the Outscale pack gives on its own catalogue.
func TestACatalogueImageIsNotTheClientsToEdit(t *testing.T) {
	ts := newTestServer(t)

	_, listed := do(t, ts, "GET", "/marketplace/v2/local-images", "")
	images, _ := listed["local_images"].([]any)
	if len(images) == 0 {
		t.Fatal("the marketplace answered no image, so there is nothing to test against")
	}
	first, _ := images[0].(map[string]any)
	id, _ := first["id"].(string)
	if id == "" {
		t.Fatalf("the marketplace image carries no id: %v", first)
	}

	// 400 carrying type=precondition_failed, which is where the HTTP API puts
	// it and what the SDK renders as a sentence; writePrecondition carries the
	// reasoning, and the point here is that it is not a 404.
	status, got := do(t, ts, "PATCH", zoneURL+"/images/"+id, `{"name":"mine-now"}`)
	if status != http.StatusBadRequest || got["type"] != "precondition_failed" {
		t.Fatalf("editing a catalogue image: expected 400 precondition_failed, got %d (%v)", status, got)
	}
	status, got = do(t, ts, "DELETE", zoneURL+"/images/"+id, "")
	if status != http.StatusBadRequest || got["type"] != "precondition_failed" {
		t.Fatalf("deleting a catalogue image: expected 400 precondition_failed, got %d (%v)", status, got)
	}

	// And it still resolves, which is what the CLI needs before every create.
	if status, _ := do(t, ts, "GET", zoneURL+"/images/"+id, ""); status != http.StatusOK {
		t.Errorf("the catalogue image stopped resolving: get answered %d", status)
	}
}
