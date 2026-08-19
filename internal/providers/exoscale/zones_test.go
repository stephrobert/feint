package exoscale_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/exoscale"
)

// The zone is a datum, not a constant (#278). At Exoscale every zone is
// served from its own endpoint (api-<zone>.exoscale.com), so an emulator with
// a single endpoint chooses its zone at construction instead of hardwiring
// ch-dk-2 — the same correction the Outscale pack received for its region
// (#290). Every test here runs in a NON-default zone on purpose; the
// default-zone suite next door (pack_test.go) is exactly the population that
// let the constant ship, because a constant and a datum are indistinguishable
// from ch-dk-2.

// serveInZone mounts the pack in a named zone, the way the CLI does when
// FEINT_EXOSCALE_ZONE is set.
func serveInZone(t *testing.T, zone string) http.Handler {
	t.Helper()
	env := emulator.DefaultEnv()
	pack, err := exoscale.NewInZone(env, zone)
	if err != nil {
		t.Fatalf("build the pack in %s: %v", zone, err)
	}
	srv, err := emulator.NewServer(env, pack)
	if err != nil {
		t.Fatalf("build the server: %v", err)
	}
	return srv.Handler()
}

// A pack built for ch-gva-2 — the zone the eu-data-platform stack hardcodes
// and the one openshift4-exoscale's DNS client resolves (#262, #278) — agrees
// with itself everywhere: the zone list, what every catalogue entry declares
// it is available in, and the write paths that resolve through that
// catalogue. Never ch-dk-2 anywhere, and never a union: a catalogue declaring
// zones this deployment does not serve is the #269 contradiction in this
// pack's dialect.
func TestANonDefaultZoneAgreesWithItself(t *testing.T) {
	h := serveInZone(t, "ch-gva-2")

	// The zone list, which is where every other call gets its address:
	// exactly one row, and it names the zone in force.
	rec, body := call(t, h, "GET", "/v2/zone", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v2/zone: status %d, want 200", rec.Code)
	}
	zones, _ := body["zones"].([]any)
	if len(zones) != 1 {
		t.Fatalf("%d zones point at this emulator, want exactly 1: %v", len(zones), body)
	}
	row, _ := zones[0].(map[string]any)
	if name, _ := row["name"].(string); name != "ch-gva-2" {
		t.Errorf("the zone list names %q, want ch-gva-2", name)
	}
	if endpoint, _ := row["api-endpoint"].(string); !strings.HasSuffix(endpoint, "/v2") ||
		strings.Contains(endpoint, "exoscale.com") {
		t.Errorf("api-endpoint %q must point back at this emulator, /v2 included", endpoint)
	}

	// Every catalogue entry declares it is available exactly here. A client
	// filtering by the zone it was told about must find the entry; one
	// declaring ch-dk-2 in a ch-gva-2 deployment declares a zone every other
	// route contradicts.
	templateID, typeID := "", ""
	for _, route := range []struct {
		path, key string
		id        *string
	}{
		{"/v2/template", "templates", &templateID},
		{"/v2/instance-type", "instance-types", &typeID},
	} {
		_, listing := call(t, h, "GET", route.path, "")
		entries, _ := listing[route.key].([]any)
		if len(entries) == 0 {
			t.Fatalf("GET %s answered no entries: %v", route.path, listing)
		}
		for _, raw := range entries {
			entry, _ := raw.(map[string]any)
			declared, _ := entry["zones"].([]any)
			if len(declared) != 1 || declared[0] != "ch-gva-2" {
				t.Errorf("%s entry %v declares zones %v, want [ch-gva-2]", route.path, entry["id"], declared)
			}
		}
		first, _ := entries[0].(map[string]any)
		*route.id, _ = first["id"].(string)
	}

	// The write paths accept what the catalogue above declared, and what they
	// store reads back — a create resolved through this zone's own catalogue,
	// which is exactly how `exo compute instance create --zone ch-gva-2`
	// arrives here.
	rec, created := call(t, h, "POST", "/v2/instance",
		`{"name":"gva","disk-size":10,"template":{"id":"`+templateID+`"},"instance-type":{"id":"`+typeID+`"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v2/instance with this zone's own catalogue ids: status %d: %v", rec.Code, created)
	}
	// The create answers an operation; the instance's own id rides its
	// reference, which is where the CLI reads it.
	reference, _ := created["reference"].(map[string]any)
	instanceID, _ := reference["id"].(string)
	if instanceID == "" {
		t.Fatalf("the create's operation names no instance: %v", created)
	}
	rec, read := call(t, h, "GET", "/v2/instance/"+instanceID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("the created instance does not read back: status %d: %v", rec.Code, read)
	}
	if tpl, _ := read["template"].(map[string]any); tpl["id"] != templateID {
		t.Errorf("the instance reads back template %v, want %s", tpl["id"], templateID)
	}

	// The pool path validates its template through the same catalogue
	// (templateByID): refusing here what the list offered would be the two
	// halves of the pack contradicting each other.
	rec, pool := call(t, h, "POST", "/v2/instance-pool",
		`{"name":"gva-pool","size":1,"disk-size":10,"template":{"id":"`+templateID+`"},"instance-type":{"id":"`+typeID+`"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v2/instance-pool refused this zone's own template: status %d: %v", rec.Code, pool)
	}
}

// A zone Exoscale does not publish is refused at construction, naming what
// would have been accepted. Refusing beats defaulting: an emulator that
// answered ch-dk-2 to an operator who asked for something else would be
// #268's lie moved to startup.
func TestAZoneExoscaleDoesNotPublishIsRefused(t *testing.T) {
	env := emulator.DefaultEnv()
	if _, err := exoscale.NewInZone(env, "ch-mars-1"); err == nil {
		t.Fatal("NewInZone accepted ch-mars-1, a zone Exoscale does not publish")
	} else {
		if !strings.Contains(err.Error(), "ch-mars-1") {
			t.Errorf("the refusal does not name the zone asked for: %v", err)
		}
		if !strings.Contains(err.Error(), "ch-gva-2") {
			t.Errorf("the refusal does not name what would have been accepted: %v", err)
		}
	}
}
