package scaleway_test

import (
	"net/http"
	"strings"
	"testing"
)

// The offer table is served, and it is a window rather than one page repeated
// (#658).
//
// It was declined on two claims. One has stopped being true: a generated
// Ansible collection reads it through an `_info` module, whose whole purpose is
// to answer "what is on offer". The other, the ListVolumesTypes argument, points
// the other way here once the create is closed beside it.
func TestTheLBOfferTableIsServedAndPaged(t *testing.T) {
	ts := newTestServer(t)

	status, all := do(t, ts, "GET", "/lb/v1/zones/fr-par-1/lb-types", "")
	if status != http.StatusOK {
		t.Fatalf("lb-types: expected 200, got %d (%v)", status, all)
	}
	types, _ := all["lb_types"].([]any)
	if len(types) != 4 {
		t.Fatalf("the table answers %d types, want the 4 a real account answers", len(types))
	}
	first, _ := types[0].(map[string]any)
	// Field for field against the reading of a real account, 2026-09-03.
	for field, want := range map[string]any{
		"name":         "lb-s",
		"stock_status": "available",
		"bandwidth":    float64(200000000),
		"multicloud":   false,
		"region":       "fr-par",
		"zone":         "fr-par-1",
	} {
		if got := first[field]; got != want {
			t.Errorf("lb-s.%s is %v, want %v", field, got, want)
		}
	}
	if _, present := first["description"]; !present {
		t.Error("the entry carries no description, and the SDK declares one")
	}

	// A window of two, and the second page is the OTHER two.
	_, one := do(t, ts, "GET", "/lb/v1/zones/fr-par-1/lb-types?page=1&page_size=2", "")
	_, two := do(t, ts, "GET", "/lb/v1/zones/fr-par-1/lb-types?page=2&page_size=2", "")
	firstPage, _ := one["lb_types"].([]any)
	secondPage, _ := two["lb_types"].([]any)
	if len(firstPage) != 2 || len(secondPage) != 2 {
		t.Fatalf("a window of two answered %d then %d entries", len(firstPage), len(secondPage))
	}
	a, _ := firstPage[0].(map[string]any)
	b, _ := secondPage[0].(map[string]any)
	if a["name"] == b["name"] {
		t.Errorf("page 1 and page 2 both start at %v: the window repeats instead of sliding", a["name"])
	}
	// The count is the stock, not the window.
	if got, _ := two["total_count"].(float64); got != 4 {
		t.Errorf("total_count is %v, want 4", got)
	}
	// Past the stock, empty: how the SDK's own loop stops.
	_, past := do(t, ts, "GET", "/lb/v1/zones/fr-par-1/lb-types?page=99&page_size=2", "")
	if got, _ := past["lb_types"].([]any); len(got) != 0 {
		t.Errorf("page 99 answers %d entries", len(got))
	}
}

// Every type the table lists is one the create accepts (#658).
//
// This is the ListVolumesTypes test, and it is the reason the decline could be
// lifted: that argument refuses a catalogue whose items the create then
// rejects, and none of these four are rejected. Asserted rather than assumed,
// because a row added later without a create behind it would turn this table
// into exactly the menu that argument warns about.
//
// The converse is NOT asserted, and lbtypes.go says why: the create accepts
// types the table does not list, because the committed corpus replays CreateLB
// with a redacted type and a value check refuses the replay itself. Closing the
// offer needs the anonymiser to preserve the field first.
//
// Which means this test CANNOT FAIL today, and saying so is the point. A
// mutation renaming lb-s to a type nothing takes was written and reported TEST
// STILL PASSED: with the create open, no table can list a type it refuses. The
// test is here for the day the create closes on a list, and until then it is a
// statement of the property rather than a measurement of it. The spec beside it
// (tools/falsify/specs/lb-offer-table.json) carries the same sentence, so the
// falsification harness is not asked to pretend otherwise.
func TestEveryOfferedLBTypeIsOneTheCreateAccepts(t *testing.T) {
	ts := newTestServer(t)

	status, all := do(t, ts, "GET", "/lb/v1/zones/fr-par-1/lb-types", "")
	if status != http.StatusOK {
		t.Fatalf("lb-types: %d (%v)", status, all)
	}
	types, _ := all["lb_types"].([]any)
	if len(types) == 0 {
		t.Fatal("the table is empty, so this proves nothing")
	}
	for _, listed := range types {
		entry, _ := listed.(map[string]any)
		name, _ := entry["name"].(string)

		status, ip := do(t, ts, "POST", "/lb/v1/zones/fr-par-1/ips", `{}`)
		if status != http.StatusOK {
			t.Fatalf("create an ip: %d (%v)", status, ip)
		}
		ipID, _ := ip["id"].(string)

		// Both spellings a client uses: the table answers lowercase, every
		// example and provider configuration writes LB-S.
		for _, spelling := range []string{name, strings.ToUpper(name)} {
			status, made := do(t, ts, "POST", "/lb/v1/zones/fr-par-1/lbs",
				`{"name":"offered","type":"`+spelling+`","ip_ids":["`+ipID+`"]}`)
			if status != http.StatusOK {
				t.Errorf("the table offers %q but creating it as %q answered %d (%v)",
					name, spelling, status, made)
			}
			// One balancer per IP: the second spelling gets its own.
			status, ip = do(t, ts, "POST", "/lb/v1/zones/fr-par-1/ips", `{}`)
			if status != http.StatusOK {
				t.Fatalf("create an ip: %d (%v)", status, ip)
			}
			ipID, _ = ip["id"].(string)
		}
	}
}
