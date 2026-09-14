package scaleway_test

import (
	"net/http"
	"testing"
)

const volumeProducts = "/instance/v1/zones/fr-par-1/products/volumes"

// TestVolumeTypesAnswerTheRecordedCatalogue holds every figure against the
// transcript, because a catalogue is exactly the kind of answer that is easy to
// invent and impossible to notice.
//
// The route was declined until 2026-09-14 on the ground that its per-type
// constraints "would have to be invented rather than measured". They are not
// invented: #758 carries a `feint proxy` capture of
// GET /instance/v1/zones/fr-par-1/products/volumes against a real account on
// 2026-09-10 — 200, 290 bytes, two entries. Each assertion below is one line of
// that body.
func TestVolumeTypesAnswerTheRecordedCatalogue(t *testing.T) {
	ts := newTestServer(t)

	status, body := do(t, ts, "GET", volumeProducts, "")
	if status != http.StatusOK {
		t.Fatalf("GET %s answered %d, want 200", volumeProducts, status)
	}

	volumes, _ := body["volumes"].(map[string]any)
	if len(volumes) != 2 {
		t.Fatalf("the catalogue lists %d type(s), the recording carries exactly two: %v",
			len(volumes), body)
	}

	// `total_count` is in the SDK's response struct, in neither the API
	// description nor the recorded body. Serving it would be the invention this
	// route was declined for.
	if _, present := body["total_count"]; present {
		t.Error("total_count is answered; the description does not declare it and the recording does not carry it")
	}

	for name, want := range map[string]struct {
		display  string
		snapshot bool
		min, max float64
	}{
		"l_ssd":   {"Local SSD", true, 1_000_000_000, 800_000_000_000},
		"scratch": {"Scratch Storage", false, 1_000_000_000, 60_000_000_000_000},
	} {
		entry, ok := volumes[name].(map[string]any)
		if !ok {
			t.Errorf("%s is absent from the catalogue the cloud answered", name)
			continue
		}
		if got, _ := entry["display_name"].(string); got != want.display {
			t.Errorf("%s: display_name = %q, the recording says %q", name, got, want.display)
		}
		capabilities, _ := entry["capabilities"].(map[string]any)
		if got, _ := capabilities["snapshot"].(bool); got != want.snapshot {
			t.Errorf("%s: capabilities.snapshot = %v, the recording says %v", name, got, want.snapshot)
		}
		constraints, _ := entry["constraints"].(map[string]any)
		if got, _ := constraints["min"].(float64); got != want.min {
			t.Errorf("%s: constraints.min = %.0f, the recording says %.0f", name, got, want.min)
		}
		if got, _ := constraints["max"].(float64); got != want.max {
			t.Errorf("%s: constraints.max = %.0f, the recording says %.0f", name, got, want.max)
		}
	}

	// b_ssd is retired upstream and must not come back through the menu: a
	// client that picked it from here would meet a create that refuses it.
	if _, present := volumes["b_ssd"]; present {
		t.Error("b_ssd is listed; it is retired, and volumetypes.go refuses to mint one")
	}
}

// TestTheMenuAndTheMintAreOneFact. The catalogue and what POST /volumes accepts
// are derived from one table on purpose, so a menu cannot offer a type the
// create refuses — which is the shape of every "the client picked it and it
// failed" report.
func TestTheMenuAndTheMintAreOneFact(t *testing.T) {
	ts := newTestServer(t)

	_, body := do(t, ts, "GET", volumeProducts, "")
	volumes, _ := body["volumes"].(map[string]any)

	for name := range volumes {
		status, out := do(t, ts, "POST", "/instance/v1/zones/fr-par-1/volumes",
			`{"name":"menu-`+name+`","volume_type":"`+name+`","size":10000000000}`)
		if status != http.StatusCreated {
			t.Errorf("the menu offers %s and the create answered %d: %v", name, status, out)
		}
	}
}

// TestVolumeTypesArePaged: the contract declares page and per_page on this
// operation, and the whole catalogue fits under any default page size, so
// nothing but a test would ever notice the parameters being dropped. That is
// the class #271 names, and listServerTypes already carries its twin.
func TestVolumeTypesArePaged(t *testing.T) {
	ts := newTestServer(t)

	status, body := do(t, ts, "GET", volumeProducts+"?per_page=1", "")
	if status != http.StatusOK {
		t.Fatalf("a paged read answered %d", status)
	}
	first, _ := body["volumes"].(map[string]any)
	if len(first) != 1 {
		t.Fatalf("per_page=1 answered %d entries: the parameter is dropped", len(first))
	}

	_, body = do(t, ts, "GET", volumeProducts+"?per_page=1&page=2", "")
	second, _ := body["volumes"].(map[string]any)
	if len(second) != 1 {
		t.Fatalf("page 2 answered %d entries, want the other one", len(second))
	}
	for name := range first {
		if _, repeated := second[name]; repeated {
			t.Errorf("page 2 repeats %s from page 1: the page is not a window", name)
		}
	}
}
