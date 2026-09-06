package scaleway_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Elastic Metal's one route, the inventory's (#631).
//
// The wall these tests stand in front of is a 501: `scw baremetal server
// list` made one call, GET /baremetal/v1/zones/{zone}/servers, and the
// unrouted prefix refused it, so a fleet inventory could not describe its bare
// metal against this emulator at all. The assertions are what the two
// measured clients do with the answer: the CLI decodes it into the SDK's
// Server, and the Python SDK's list_servers_all pages with `page` alone until
// a page comes back empty, then reads `ips[].version` to sort addresses by
// family.
//
// There is no CreateServer, so every server here enters through the state
// door, the way an operator's would.

const baremetalListURL = "/baremetal/v1/zones/fr-par-1/servers"

// Identifiers outside the sanitiser's minting space (#395): a seed that looked
// minted would be indistinguishable from a recording. The project is the one
// fake-credentials.env configures every client with.
const (
	metalProject   = "11111111-1111-1111-1111-111111111111"
	metalElsewhere = "22222222-2222-4222-8222-222222222222"
	metalServerA   = "6f1d2c3b-4a5e-4f60-8b7c-9d0e1f2a3b4c"
	metalServerB   = "7a2e3d4c-5b6f-4a71-9c8d-0e1f2a3b4c5d"
	metalOption    = "8b3f4e5d-6c70-4b82-8d9e-1f2a3b4c5d6e"
)

// metalSeed is one server as an operator writes it: the store's own fields
// around the SDK's field names. The second address carries no version, which
// is the case the view has to settle from the address itself.
func metalSeed(id, project, name, created, tags string, withOption bool) string {
	options := "[]"
	if withOption {
		options = `[{"id": "` + metalOption + `", "name": "Private Network", "status": "option_status_enable", "manageable": true}]`
	}
	return `{
	  "ID": "` + id + `",
	  "Kind": "baremetal/server",
	  "Tenant": {"Provider": "scaleway", "Project": "` + project + `", "Zone": "fr-par-1"},
	  "State": "ready",
	  "Created": "` + created + `",
	  "Updated": "` + created + `",
	  "Attrs": {
	    "name": "` + name + `",
	    "description": "the database host",
	    "offer_id": "9d4c5e6f-7a80-4b93-8caf-2b3c4d5e6f70",
	    "offer_name": "EM-A210R-HDD",
	    "tags": [` + tags + `],
	    "ips": [
	      {"id": "9c4a5f6e-7d81-4c93-9eaf-2a3b4c5d6e7f", "address": "203.0.113.10", "reverse": "db-1.example.net", "version": "IPv4", "reverse_status": "active"},
	      {"id": "ad5b6a7f-8e92-4da4-8fb0-3b4c5d6e7f80", "address": "2001:db8::10"}
	    ],
	    "boot_type": "normal",
	    "ping_status": "ping_status_up",
	    "options": ` + options + `,
	    "protected": false
	  }
	}`
}

// seedState writes a whole snapshot through the state door, the real one an
// operator's `--state` file goes through.
func seedState(t *testing.T, ts *httptest.Server, resources ...string) {
	t.Helper()
	body := `{"format": "feint-snapshot", "version": 1, "resources": [` + strings.Join(resources, ",") + `]}`
	status, answer := do(t, ts, "PUT", "/_feint/state", body)
	if status != http.StatusOK {
		t.Fatalf("seed through the state door: expected 200, got %d (%v)", status, answer)
	}
}

func metalServers(t *testing.T, ts *httptest.Server, query string) (int, []map[string]any, map[string]any) {
	t.Helper()
	status, body := do(t, ts, "GET", baremetalListURL+query, "")
	raw, _ := body["servers"].([]any)
	servers := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		server, _ := item.(map[string]any)
		servers = append(servers, server)
	}
	return status, servers, body
}

func metalNames(servers []map[string]any) string {
	names := make([]string, 0, len(servers))
	for _, server := range servers {
		name, _ := server["name"].(string)
		names = append(names, name)
	}
	return strings.Join(names, ",")
}

// The door answers before anything is seeded, on the exact query `scw` sends:
// total_count 0 and an empty array, never null, which is what the SDK decodes
// into an empty list rather than a nil it would page again.
func TestAnEmptyElasticMetalFleetListsAsEmpty(t *testing.T) {
	ts := newTestServer(t)

	status, _, body := metalServers(t, ts, "?order_by=created_at_asc&page=1")
	if status != http.StatusOK {
		t.Fatalf("list: expected 200, got %d (%v)", status, body)
	}
	raw, ok := body["servers"].([]any)
	if !ok || len(raw) != 0 {
		t.Errorf("servers must be an empty array, got %v", body["servers"])
	}
	if total, _ := body["total_count"].(float64); int(total) != 0 {
		t.Errorf("total_count is %v for an empty fleet", body["total_count"])
	}
}

// Field for field against the SDK's Server: a client that decodes into it
// must find every key, null included, and an inventory must find the family
// on every address — the one the seed named and the one it did not.
func TestASeededElasticMetalServerListsWithTheFieldsAnInventoryReads(t *testing.T) {
	ts := newTestServer(t)
	seedState(t, ts, metalSeed(metalServerA, metalProject, "db-1", "2026-09-01T10:00:00Z", `"db", "prod"`, true))

	status, servers, body := metalServers(t, ts, "")
	if status != http.StatusOK || len(servers) != 1 {
		t.Fatalf("list: expected 200 and one server, got %d (%v)", status, body)
	}
	got := servers[0]
	for _, key := range []string{"id", "organization_id", "project_id", "name", "description", "updated_at", "created_at", "status", "offer_id", "offer_name", "tags", "ips", "domain", "boot_type", "zone", "install", "ping_status", "options", "rescue_server", "protected", "user_data"} {
		if _, present := got[key]; !present {
			t.Errorf("Server omits %s, which the SDK declares: %v", key, got)
		}
	}
	want := map[string]any{
		"id": metalServerA, "name": "db-1", "status": "ready", "zone": "fr-par-1",
		"project_id": metalProject, "organization_id": emulatedOrganization,
		"offer_name": "EM-A210R-HDD", "boot_type": "normal", "ping_status": "ping_status_up",
		"created_at": "2026-09-01T10:00:00Z", "protected": false,
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s is %v, want %v", key, got[key], value)
		}
	}
	if got["install"] != nil || got["user_data"] != nil || got["rescue_server"] != nil {
		t.Errorf("a seed that set no install, user data or rescue answers null for each, got %v / %v / %v", got["install"], got["user_data"], got["rescue_server"])
	}
	tags, _ := got["tags"].([]any)
	if len(tags) != 2 || tags[0] != "db" || tags[1] != "prod" {
		t.Errorf("tags are %v, and the inventory filters on them", got["tags"])
	}
	ips, _ := got["ips"].([]any)
	if len(ips) != 2 {
		t.Fatalf("ips are %v, want the two seeded addresses", got["ips"])
	}
	first, _ := ips[0].(map[string]any)
	second, _ := ips[1].(map[string]any)
	if first["version"] != "IPv4" || first["address"] != "203.0.113.10" || first["reverse_status"] != "active" {
		t.Errorf("the seeded IPv4 lost something: %v", first)
	}
	if second["version"] != "IPv6" {
		t.Errorf("an address whose seed named no version must carry the family of the address, 2001:db8::10 is IPv6, got %v", second)
	}
	if second["reverse_status"] != "unknown" {
		t.Errorf("a reverse nobody set has the enum's own unknown status, got %v", second["reverse_status"])
	}
	for _, key := range []string{"id", "address", "reverse", "version", "reverse_status", "reverse_status_message"} {
		if _, present := second[key]; !present {
			t.Errorf("IP omits %s, which the SDK declares: %v", key, second)
		}
	}
	if total, _ := body["total_count"].(float64); int(total) != 1 {
		t.Errorf("total_count is %v", body["total_count"])
	}
}

// The Python SDK's list_servers_all sends page=1, then page=2, and stops on
// the first page that comes back empty: a list that ignored `page` would
// answer the same server forever. page_size is honoured the same way.
func TestTheSecondPageOfAnElasticMetalListIsEmpty(t *testing.T) {
	ts := newTestServer(t)
	seedState(t, ts,
		metalSeed(metalServerA, metalProject, "db-1", "2026-09-01T10:00:00Z", `"db"`, false),
		metalSeed(metalServerB, metalProject, "db-2", "2026-09-01T11:00:00Z", `"db"`, false))

	_, servers, body := metalServers(t, ts, "?page=1")
	if len(servers) != 2 {
		t.Fatalf("page 1 holds %d servers, want 2 (%v)", len(servers), body)
	}
	_, servers, body = metalServers(t, ts, "?page=2")
	if len(servers) != 0 {
		t.Errorf("page 2 of a two-server fleet must be empty, or the SDK's loop never ends: %v", body)
	}
	if total, _ := body["total_count"].(float64); int(total) != 2 {
		t.Errorf("total_count on an empty page still counts the fleet, got %v", body["total_count"])
	}
	for page, want := range map[string]string{"1": "db-1", "2": "db-2", "3": ""} {
		_, servers, _ = metalServers(t, ts, "?page_size=1&page="+page)
		if got := metalNames(servers); got != want {
			t.Errorf("page_size=1&page=%s answers %q, want %q", page, got, want)
		}
	}
}

// The product exists in six zones, the SDK's Zones() and the document's enum
// agree, and the four other Scaleway zones are refused by name rather than
// answered empty — an empty answer for a zone the product does not serve is
// the confusing silence zoneOf exists to prevent.
func TestAnElasticMetalZoneOutsideTheProductIsRefused(t *testing.T) {
	ts := newTestServer(t)

	for _, zone := range []string{"fr-par-3", "nl-ams-3", "pl-waw-1", "it-mil-1"} {
		status, body := do(t, ts, "GET", "/baremetal/v1/zones/"+zone+"/servers", "")
		if status != http.StatusBadRequest || body["type"] != "invalid_arguments" {
			t.Errorf("%s: expected 400 invalid_arguments, got %d (%v)", zone, status, body)
			continue
		}
		details, _ := body["details"].([]any)
		if len(details) == 0 {
			t.Errorf("%s: the refusal names no argument: %v", zone, body)
			continue
		}
		if detail, _ := details[0].(map[string]any); detail["argument_name"] != "zone" {
			t.Errorf("%s: the refusal names %v, want zone", zone, detail["argument_name"])
		}
	}
	for _, zone := range []string{"fr-par-1", "fr-par-2", "nl-ams-1", "nl-ams-2", "pl-waw-2", "pl-waw-3"} {
		if status, body := do(t, ts, "GET", "/baremetal/v1/zones/"+zone+"/servers", ""); status != http.StatusOK {
			t.Errorf("%s: the product exists there, got %d (%v)", zone, status, body)
		}
	}
}

// Every filter the operation declares narrows the fleet the way its SDK
// comment says: tags conjoin, name is a substring, status and option_id
// match, the two scopes partition, and the order is the one asked for or a
// refusal — never some other order.
func TestElasticMetalFiltersNarrowTheFleet(t *testing.T) {
	ts := newTestServer(t)
	seedState(t, ts,
		metalSeed(metalServerA, metalProject, "db-1", "2026-09-01T10:00:00Z", `"db", "prod"`, true),
		metalSeed(metalServerB, metalElsewhere, "cache-1", "2026-09-01T11:00:00Z", `"cache", "prod"`, false))

	cases := []struct{ query, want string }{
		{"?tags=prod", "db-1,cache-1"},
		{"?tags=db&tags=prod", "db-1"},
		{"?tags=db,prod", "db-1"},
		{"?tags=db&tags=cache", ""},
		{"?name=db", "db-1"},
		{"?name=-1", "db-1,cache-1"},
		{"?status=ready", "db-1,cache-1"},
		{"?status=stopped", ""},
		{"?status=stopped&status=ready", "db-1,cache-1"},
		{"?option_id=" + metalOption, "db-1"},
		{"?project_id=" + metalElsewhere, "cache-1"},
		{"?organization_id=" + emulatedOrganization, "db-1,cache-1"},
		{"?organization_id=" + metalElsewhere, ""},
		{"?order_by=created_at_asc", "db-1,cache-1"},
		{"?order_by=created_at_desc", "cache-1,db-1"},
	}
	for _, c := range cases {
		status, servers, body := metalServers(t, ts, c.query)
		if status != http.StatusOK {
			t.Errorf("%s: expected 200, got %d (%v)", c.query, status, body)
			continue
		}
		if got := metalNames(servers); got != c.want {
			t.Errorf("%s answers %q, want %q", c.query, got, c.want)
		}
	}
	for _, query := range []string{"?order_by=name_asc", "?organization_id=not-a-uuid"} {
		if status, body := do(t, ts, "GET", baremetalListURL+query, ""); status != http.StatusBadRequest || body["type"] != "invalid_arguments" {
			t.Errorf("%s: expected 400 invalid_arguments, got %d (%v)", query, status, body)
		}
	}
}

// `scw baremetal server list` joins the offer catalogue after the servers and
// prints the catalogue's name for each server's offer_id, so the catalogue is
// what the fleet declares: one offer per distinct offer_id among the seeded
// servers, nothing in stock, and the SDK's whole Offer field set.
func TestTheOfferCatalogueIsWhatTheFleetDeclares(t *testing.T) {
	ts := newTestServer(t)
	const offersURL = "/baremetal/v1/zones/fr-par-1/offers"

	// Before any seed: an empty catalogue, on the exact query the CLI sends.
	status, body := do(t, ts, "GET", offersURL+"?page=1&subscription_period=unknown_subscription_period", "")
	if status != http.StatusOK {
		t.Fatalf("list offers: expected 200, got %d (%v)", status, body)
	}
	if raw, ok := body["offers"].([]any); !ok || len(raw) != 0 {
		t.Errorf("an empty fleet declares no offer, and answers an array: %v", body["offers"])
	}

	seedState(t, ts,
		metalSeed(metalServerA, metalProject, "db-1", "2026-09-01T10:00:00Z", `"db"`, false),
		metalSeed(metalServerB, metalProject, "db-2", "2026-09-01T11:00:00Z", `"db"`, false))
	status, body = do(t, ts, "GET", offersURL+"?page=1&subscription_period=unknown_subscription_period", "")
	if status != http.StatusOK {
		t.Fatalf("list offers: expected 200, got %d (%v)", status, body)
	}
	offers, _ := body["offers"].([]any)
	if len(offers) != 1 {
		t.Fatalf("two servers on one offer declare one offer, got %v", body)
	}
	offer, _ := offers[0].(map[string]any)
	for _, key := range []string{"id", "name", "stock", "bandwidth", "max_bandwidth", "commercial_range", "price_per_hour", "price_per_month", "disks", "enable", "cpus", "memories", "quota_name", "persistent_memories", "raid_controllers", "incompatible_os_ids", "subscription_period", "operation_path", "fee", "options", "private_bandwidth", "shared_bandwidth", "tags", "gpus", "monthly_offer_id", "zone"} {
		if _, present := offer[key]; !present {
			t.Errorf("Offer omits %s, which the SDK declares: %v", key, offer)
		}
	}
	if offer["id"] != "9d4c5e6f-7a80-4b93-8caf-2b3c4d5e6f70" || offer["name"] != "EM-A210R-HDD" {
		t.Errorf("the offer is the seed's id and name, got %v / %v", offer["id"], offer["name"])
	}
	if offer["stock"] != "empty" || offer["enable"] != false || offer["price_per_hour"] != nil {
		t.Errorf("nothing here is in stock, and the offer must say so: stock %v, enable %v, price %v", offer["stock"], offer["enable"], offer["price_per_hour"])
	}
	if total, _ := body["total_count"].(float64); int(total) != 1 {
		t.Errorf("total_count is %v", body["total_count"])
	}

	for query, want := range map[string]int{
		"?name=EM-A210R-HDD":          1,
		"?name=EM-B112X-SSD":          0,
		"?subscription_period=hourly": 0,
		"?page=2":                     0,
	} {
		status, body := do(t, ts, "GET", offersURL+query, "")
		got, _ := body["offers"].([]any)
		if status != http.StatusOK || len(got) != want {
			t.Errorf("%s: expected 200 and %d offer(s), got %d and %v", query, want, status, body["offers"])
		}
	}
	if status, body := do(t, ts, "GET", offersURL+"?subscription_period=weekly", ""); status != http.StatusBadRequest || body["type"] != "invalid_arguments" {
		t.Errorf("a period outside the enum is refused by name, got %d (%v)", status, body)
	}
}

// A restored resource is an untrusted input. A seed whose values have the
// wrong type is still listed, with those values dropped rather than served
// under the SDK's field names, and the fields the SDK declares stay present.
func TestAnElasticMetalSeedOfTheWrongShapeIsServedTyped(t *testing.T) {
	ts := newTestServer(t)
	seedState(t, ts, `{
	  "ID": "`+metalServerA+`", "Kind": "baremetal/server",
	  "Tenant": {"Provider": "scaleway", "Project": "`+metalProject+`", "Zone": "fr-par-1"},
	  "State": "ready", "Created": "2026-09-01T10:00:00Z", "Updated": "2026-09-01T10:00:00Z",
	  "Attrs": {"name": 42, "tags": "db", "ips": 7, "protected": "yes"}
	}`)

	status, servers, body := metalServers(t, ts, "")
	if status != http.StatusOK || len(servers) != 1 {
		t.Fatalf("list: expected 200 and one server, got %d (%v)", status, body)
	}
	got := servers[0]
	if got["name"] != "" || got["protected"] != false {
		t.Errorf("a name that is a number and a protection that is a word are not served as such: %v", got)
	}
	if tags, ok := got["tags"].([]any); !ok || len(tags) != 0 {
		t.Errorf("tags of the wrong type are an empty array, got %v", got["tags"])
	}
	if ips, ok := got["ips"].([]any); !ok || len(ips) != 0 {
		t.Errorf("ips of the wrong type are an empty array, got %v", got["ips"])
	}
	if got["boot_type"] != "unknown_boot_type" || got["ping_status"] != "ping_status_unknown" {
		t.Errorf("enums the seed did not set are the enums' own unknown members, got %v / %v", got["boot_type"], got["ping_status"])
	}
}
