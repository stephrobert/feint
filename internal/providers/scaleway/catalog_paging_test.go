package scaleway_test

import (
	"net/http"
	"testing"
)

// The two catalogue endpoints used to ignore the pagination their contract
// declares — page/per_page on ListServersTypes, page/page_size on
// ListVolumeTypes. No real client saw it, because both catalogues fit under
// every default page size; the contract still promised a parameter the handler
// dropped, which is the class #271 names. These tests page below the catalogue
// size, which is the request the defect answered wrong.

func TestServerTypesArePaged(t *testing.T) {
	ts := newTestServer(t)
	const url = "/instance/v1/zones/fr-par-1/products/servers"

	status, all := do(t, ts, "GET", url, "")
	if status != http.StatusOK {
		t.Fatalf("unpaged: expected 200, got %d (%v)", status, all)
	}
	types, _ := all["servers"].(map[string]any)
	total := int(all["total_count"].(float64))
	if len(types) != total || total < 2 {
		t.Fatalf("the unpaged catalogue answers %d of %d types", len(types), total)
	}

	// A window smaller than the catalogue really is a window, and total_count
	// still names the whole stock so the SDK's pagination loop terminates.
	status, page := do(t, ts, "GET", url+"?page=1&per_page=2", "")
	if status != http.StatusOK {
		t.Fatalf("paged: expected 200, got %d", status)
	}
	first, _ := page["servers"].(map[string]any)
	if len(first) != 2 {
		t.Errorf("per_page=2 answered %d types", len(first))
	}
	if got := int(page["total_count"].(float64)); got != total {
		t.Errorf("the page reports total_count %d, want %d", got, total)
	}

	// The second page is different stock, not the first again: a page that
	// repeats itself is a list that never ends to a client walking it.
	_, second := do(t, ts, "GET", url+"?page=2&per_page=2", "")
	for name := range second["servers"].(map[string]any) {
		if _, dup := first[name]; dup {
			t.Errorf("type %q appears on page 1 and page 2", name)
		}
	}

	// Past the stock the window is empty, which is how the SDK loop stops.
	_, past := do(t, ts, "GET", url+"?page=99&per_page=2", "")
	if got, _ := past["servers"].(map[string]any); len(got) != 0 {
		t.Errorf("page 99 still answers %d types", len(got))
	}
}

func TestBlockVolumeTypesArePaged(t *testing.T) {
	ts := newTestServer(t)
	const url = "/block/v1/zones/fr-par-1/volume-types"

	status, all := do(t, ts, "GET", url, "")
	if status != http.StatusOK {
		t.Fatalf("unpaged: expected 200, got %d (%v)", status, all)
	}
	if types, _ := all["volume_types"].([]any); len(types) != 1 {
		t.Fatalf("the catalogue answers %d volume types, want 1", len(types))
	}

	// One entry, so page 2 must be empty — it answered the same entry again.
	status, past := do(t, ts, "GET", url+"?page=2&page_size=1", "")
	if status != http.StatusOK {
		t.Fatalf("paged: expected 200, got %d", status)
	}
	if types, _ := past["volume_types"].([]any); len(types) != 0 {
		t.Errorf("page 2 of a one-entry catalogue answers %d entries", len(types))
	}
	if got := int(past["total_count"].(float64)); got != 1 {
		t.Errorf("the page reports total_count %d, want 1", got)
	}
}
