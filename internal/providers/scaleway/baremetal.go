package scaleway

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Elastic Metal, as a fleet inventory reads it and nothing more (#631).
//
// The product is not emulated: no offer is in stock here, nothing is ordered,
// installed, rebooted or billed. What is served is the operation an inventory
// needs, ListServers, because a fleet that mixes Instances and bare metal
// could not describe half of its machines against this emulator, and the half
// it could not describe is the half that usually runs the databases — plus the
// catalogue read the official CLI makes right after it, ListOffers, which the
// second measurement below found and the issue could not see.
//
// # What a client calls, measured 2026-09-06
//
// `scw baremetal server list` (2.56.3) makes one call before anything else:
// GET /baremetal/v1/zones/{zone}/servers?order_by=created_at_asc&page=1, and
// stopped on the 501 the unrouted prefix answered until now. Once that call
// answered, it made a second, ListOffers, which the section on the catalogue
// below carries with its reason. The Python
// SDK's list_servers_all (scaleway 2.12.0, what the stephrobert.scaleway
// inventory plugin drives) sends organization_id on every call from the
// client's default, and pages with `page` alone — no page_size — until a page
// comes back empty. So page 2 of a one-server fleet must answer an empty list,
// which is what pageRequest.slice does, and that loop is why a list that
// ignored `page` would never terminate.
//
// # How a server comes to exist
//
// There is no CreateServer here, on purpose: ordering bare metal is a paid
// commitment on a delivery the cloud makes in minutes to hours, and an offer
// catalogue with nothing in stock behind it is the catalogue trap of
// catalog.go one product out. A bare-metal server enters through the state
// door — `serve --state <file>` or `PUT /_feint/state` — with Kind
// "baremetal/server", its zone and project in Tenant, its status in State and
// the SDK's own field names in Attrs. docs/limits.md carries the example, and
// the conformance suite seeds one exactly that way before `scw` lists it.
//
// A restored resource is an untrusted input (CLAUDE.md, "Bien formé n'est pas
// autorisé"), and the view is where that is settled for the wire: Attrs are
// decoded into a typed record, so a value of the wrong type is dropped rather
// than served under the SDK's field name. The family of an address whose seed
// did not say is derived from the address itself, because `ips[].version` is
// the field an inventory turns into host variables.
//
// TestASeededElasticMetalServerListsWithTheFieldsAnInventoryReads,
// TestTheSecondPageOfAnElasticMetalListIsEmpty,
// TestAnElasticMetalZoneOutsideTheProductIsRefused and
// TestElasticMetalFiltersNarrowTheFleet fail without this.

const kindBaremetalServer = "baremetal/server"

// baremetalZones is where the product exists: the SDK's Zones() for
// baremetal/v1 and the document's enum on the {zone} parameter agree on these
// six, measured 2026-09-06. The four other Scaleway zones are refused the way
// zoneOf refuses an unknown one; what the real API answers for a zone outside
// this enum is not recorded, and docs/limits.md says so.
var baremetalZones = map[string]bool{
	"fr-par-1": true, "fr-par-2": true,
	"nl-ams-1": true, "nl-ams-2": true,
	"pl-waw-2": true, "pl-waw-3": true,
}

// baremetalIP is the SDK's IP, the field an inventory reads first: Elastic
// Metal has no public_ip object, it has a list where each entry names its
// family.
type baremetalIP struct {
	ID                   string `json:"id"`
	Address              string `json:"address"`
	Reverse              string `json:"reverse"`
	Version              string `json:"version"`
	ReverseStatus        string `json:"reverse_status"`
	ReverseStatusMessage string `json:"reverse_status_message"`
}

// baremetalRecord is the SDK's Server minus what the store carries itself:
// the identifier, the zone and the project (Tenant), the status (State) and
// the two timestamps. Attrs are decoded into it on every view, which is the
// type check a seeded resource gets.
type baremetalRecord struct {
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	OfferID      string        `json:"offer_id"`
	OfferName    string        `json:"offer_name"`
	Tags         []string      `json:"tags"`
	IPs          []baremetalIP `json:"ips"`
	Domain       string        `json:"domain"`
	BootType     string        `json:"boot_type"`
	Install      any           `json:"install"`
	PingStatus   string        `json:"ping_status"`
	Options      []any         `json:"options"`
	RescueServer any           `json:"rescue_server"`
	Protected    bool          `json:"protected"`
	UserData     *string       `json:"user_data"`
}

// baremetalRecordOf reads a seed the way the wire will be read: through the
// SDK's field names and types. encoding/json skips a value of the wrong type
// and decodes the rest, and the error it then reports is that skip — the
// intended outcome for a seed this build does not understand, never a reason
// to answer nothing.
func baremetalRecordOf(attrs map[string]any) baremetalRecord {
	var rec baremetalRecord
	raw, err := json.Marshal(attrs)
	if err != nil {
		return rec
	}
	_ = json.Unmarshal(raw, &rec)
	return rec
}

// listBaremetalServers answers baremetal/v1's ListServers.
//
// Every parameter the document declares is read: the zone, the two scopes,
// the four filters, the order and the page. The scopes are spelled
// project_id and organization_id here where instance/v1 says project and
// organization, which is the whole reason scopeNamed exists.
func (p *Pack) listBaremetalServers(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneIn(w, r, baremetalZones)
	if !ok {
		return
	}
	scope, ok := p.scopeNamed(w, r, zone, "project_id", "organization_id")
	if !ok {
		return
	}
	all := filterBaremetalServers(p.env.Store.List(kindBaremetalServer, scope), r.URL.Query())
	// The document's enum is created_at_asc and created_at_desc, default
	// created_at_asc — the value `scw` sends on every list.
	if !orderResources(w, r, "order_by", "created_at_asc", map[string]resourceCmp{"created_at": cmpCreated}, all) {
		return
	}
	page := parsePage(r)
	start, end := page.slice(len(all))
	servers := make([]any, 0, end-start)
	for _, res := range all[start:end] {
		servers = append(servers, p.baremetalServerView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"total_count": len(all),
		"servers":     servers,
	})
}

// filterBaremetalServers applies the four filters the operation declares on
// the record itself.
//
// tags is a conjunction, the reading filterServers gives instance/v1's. name
// is a substring, again instance/v1's reading — the SDK's comment on this
// product says "Names to filter for" and no recording settles which; the
// two agree on every name the conformance suite uses. status matches the
// State exactly against the values asked, and option_id any option the
// server carries.
func filterBaremetalServers(all []*resource.Resource, q url.Values) []*resource.Resource {
	name := q.Get("name")
	statuses := csvValues(q, "status")
	tags := csvValues(q, "tags")
	option := q.Get("option_id")
	if name == "" && len(statuses) == 0 && len(tags) == 0 && option == "" {
		return all
	}
	kept := make([]*resource.Resource, 0, len(all))
	for _, res := range all {
		if name != "" && !strings.Contains(textOf(res.Attrs["name"]), name) {
			continue
		}
		if len(statuses) > 0 && !contains(statuses, res.State) {
			continue
		}
		if len(tags) > 0 && !hasEveryTag(res, tags) {
			continue
		}
		if option != "" && !carriesOption(res, option) {
			continue
		}
		kept = append(kept, res)
	}
	return kept
}

func carriesOption(res *resource.Resource, id string) bool {
	for _, opt := range baremetalRecordOf(res.Attrs).Options {
		if fields, ok := opt.(map[string]any); ok && fields["id"] == id {
			return true
		}
	}
	return false
}

// baremetalServerView renders one server with the SDK's Server field set,
// every key present, null where the SDK holds a pointer nothing here set.
//
// The enum defaults are the enums' own "not said" members — unknown,
// unknown_boot_type, ping_status_unknown — rather than a plausible value: a
// seed that did not state how its server boots gets the API's word for that,
// not this emulator's guess. The organization is the one the emulator hosts,
// the rule projectOf carries for every create of this pack.
func (p *Pack) baremetalServerView(res *resource.Resource) map[string]any {
	rec := baremetalRecordOf(res.Attrs)
	ips := make([]any, 0, len(rec.IPs))
	for _, ip := range rec.IPs {
		ips = append(ips, map[string]any{
			"id":                     ip.ID,
			"address":                ip.Address,
			"reverse":                ip.Reverse,
			"version":                orDefault(ip.Version, ipFamilyOf(ip.Address)),
			"reverse_status":         orDefault(ip.ReverseStatus, "unknown"),
			"reverse_status_message": ip.ReverseStatusMessage,
		})
	}
	tags := rec.Tags
	if tags == nil {
		tags = []string{}
	}
	options := rec.Options
	if options == nil {
		options = []any{}
	}
	updated := res.Updated
	if updated.IsZero() {
		updated = res.Created
	}
	return map[string]any{
		"id":              res.ID,
		"organization_id": defaultOrganization,
		"project_id":      orDefault(res.Tenant.Project, defaultProject),
		"name":            rec.Name,
		"description":     rec.Description,
		"updated_at":      updated.Format(time.RFC3339),
		"created_at":      res.Created.Format(time.RFC3339),
		"status":          orDefault(res.State, "unknown"),
		"offer_id":        rec.OfferID,
		"offer_name":      rec.OfferName,
		"tags":            tags,
		"ips":             ips,
		"domain":          rec.Domain,
		"boot_type":       orDefault(rec.BootType, "unknown_boot_type"),
		"zone":            res.Tenant.Zone,
		"install":         rec.Install,
		"ping_status":     orDefault(rec.PingStatus, "ping_status_unknown"),
		"options":         options,
		"rescue_server":   rec.RescueServer,
		"protected":       rec.Protected,
		"user_data":       rec.UserData,
	}
}

// ipFamilyOf names the family of an address the way the SDK's IPVersion
// does, IPv4 or IPv6, for an entry whose seed did not say. An address that
// does not parse is reported IPv4, the enum's first member, and the address
// itself is served as seeded for the operator to see.
func ipFamilyOf(address string) string {
	if ip := net.ParseIP(address); ip != nil && ip.To4() == nil {
		return "IPv6"
	}
	return "IPv4"
}

// The offer catalogue, and why a product that serves one route serves two.
//
// Measured 2026-09-06 on scw 2.56.3, once ListServers answered: `scw
// baremetal server list` joins the catalogue after the servers —
// GET /offers?page=1&subscription_period=unknown_subscription_period, every
// page — and prints offerNameByID[server.OfferID] in place of the server's
// own offer_name (scaleway-cli, internal/namespaces/baremetal/v1/
// custom_server.go, serverListBuilder). A 501 there fails the whole listing;
// an empty catalogue erases the offer name of every server the operator
// seeded. It is the catalogue trap of catalog.go one product out, and the
// same correction #372 made: the client's source says two calls where the
// issue said one.
//
// Nothing here is in stock and nothing is invented to fill a catalogue. An
// offer is one distinct offer_id among the zone's seeded servers, carrying
// the name the seed gave it, stock empty, enable false, no price, no disk,
// no CPU: the SDK's Offer field set with the SDK's own zero for everything
// the operator did not declare on a server. The catalogue is what the fleet
// declares, so `scw baremetal offer list` answers the same offers.
//
// TestTheOfferCatalogueIsWhatTheFleetDeclares fails without this.

// baremetalOfferPeriods is the SDK's OfferSubscriptionPeriod enum; the CLI
// sends the first member on every list, which the API reads as "no filter".
var baremetalOfferPeriods = map[string]bool{"unknown_subscription_period": true, "hourly": true, "monthly": true}

// listBaremetalOffers answers baremetal/v1's ListOffers.
func (p *Pack) listBaremetalOffers(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneIn(w, r, baremetalZones)
	if !ok {
		return
	}
	q := r.URL.Query()
	period := q.Get("subscription_period")
	if period != "" && !baremetalOfferPeriods[period] {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "subscription_period",
			Reason:       "constraint",
			HelpMessage:  period + " is not a subscription period",
		})
		return
	}
	name := q.Get("name")

	offers := []any{}
	seen := map[string]bool{}
	for _, res := range p.env.Store.List(kindBaremetalServer, p.tenant(zone)) {
		rec := baremetalRecordOf(res.Attrs)
		if rec.OfferID == "" || seen[rec.OfferID] {
			continue
		}
		seen[rec.OfferID] = true
		// A seeded offer has no period, so a period asked for by name matches
		// nothing; the CLI's unknown_subscription_period matches everything.
		if period != "" && period != "unknown_subscription_period" {
			continue
		}
		if name != "" && name != rec.OfferName {
			continue
		}
		offers = append(offers, baremetalOfferView(zone, rec))
	}
	page := parsePage(r)
	start, end := page.slice(len(offers))
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"total_count": len(offers),
		"offers":      append([]any{}, offers[start:end]...),
	})
}

// baremetalOfferView renders one offer with the SDK's Offer field set. Every
// pointer nothing here set is null, every list empty, every number zero, and
// the two fields that say whether a client could order it say it cannot.
func baremetalOfferView(zone string, rec baremetalRecord) map[string]any {
	return map[string]any{
		"id":                  rec.OfferID,
		"name":                rec.OfferName,
		"stock":               "empty",
		"bandwidth":           0,
		"max_bandwidth":       0,
		"commercial_range":    "",
		"price_per_hour":      nil,
		"price_per_month":     nil,
		"disks":               []any{},
		"enable":              false,
		"cpus":                []any{},
		"memories":            []any{},
		"quota_name":          "",
		"persistent_memories": []any{},
		"raid_controllers":    []any{},
		"incompatible_os_ids": []any{},
		"subscription_period": "unknown_subscription_period",
		"operation_path":      "",
		"fee":                 nil,
		"options":             []any{},
		"private_bandwidth":   0,
		"shared_bandwidth":    false,
		"tags":                []any{},
		"gpus":                []any{},
		"monthly_offer_id":    nil,
		"zone":                zone,
	}
}
