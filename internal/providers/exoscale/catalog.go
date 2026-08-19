package exoscale

import (
	"net/http"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// The inventory the official client reads before it does anything else.
//
// list-zones used to be declined here, with a comment saying it was not on the
// critical path of a create and would become work the day a client listed zones
// first. That day arrived, and it was measured rather than guessed: pointed at a
// logging server, `exo compute instance list` issues exactly one request before
// anything else, GET /zone, and gives up on a 404. Every subsequent call is
// addressed from what that answer contains.
//
// This is the Scaleway catalogue trap in another dialect. An emulator has no
// inventory, and it must serve one anyway.

// The emulator serves ONE zone per process, and which one is a datum, not a
// constant (#278). Both halves of that sentence were measured:
//
// One zone, because publishing all eight names the API description enumerates
// was tried, and the CLI showed why it is wrong: it queries **every** zone it
// is told about and merges the answers. Eight zones pointing at one emulator
// turned one instance into eight identical rows in `exo compute instance
// list`. A resource duplicated per zone is a defect a user sees immediately.
// At Exoscale a zone is a property of the endpoint (api-<zone>.exoscale.com),
// so one endpoint honestly serves one zone — the same reasoning that fixed the
// Outscale region one pack over (#290).
//
// A datum, because freezing that one zone on ch-dk-2 had a measured cost:
// three of the five surveyed Exoscale stacks target another zone —
// eu-data-platform hardcodes ch-gva-2, PhilippeChepy/platform defaults to
// de-fra-1, openshift4-exoscale's DNS client resolves ch-gva-2 (#262, #278).
// The zone is now Pack.zone, chosen at construction, and everything the
// catalogue declares follows it. A client asking for a zone this deployment
// does not serve still gets a clear "not found in ListZonesResponse" instead
// of a silent duplicate, which docs/limits.md records.

// defaultZoneName is where the emulated account lives when nobody chooses:
// ch-dk-2, because that is the official CLI's own default. Serving any other
// single zone by default makes every unflagged command fail with "find zone:
// not found in ListZonesResponse" before it calls anything.
const defaultZoneName = "ch-dk-2"

// publishedZoneNames is every zone Exoscale publishes, in their document's own
// order. Source: .upstream/exoscale-openapi.yaml, the `zone-name` schema enum,
// which is also — reordered — the `zone` enum of the server URL the same
// document declares (`https://api-{zone}.exoscale.com/v2`); fetched from
// openapi-v2.exoscale.com by `mise run upstream:sync` on 2026-08-18. A
// selection outside this list is refused at construction (NewInZone), naming
// these: silently answering the default to an operator who asked for
// something else would be the #268 lie moved to startup.
var publishedZoneNames = []string{
	"ch-dk-2", "de-muc-1", "ch-gva-2", "at-vie-1", "de-fra-1", "bg-sof-1", "at-vie-2", "hr-zag-1",
}

// publishedZone reports whether Exoscale publishes the zone.
func publishedZone(name string) bool {
	for _, zone := range publishedZoneNames {
		if zone == name {
			return true
		}
	}
	return false
}

// zoneList renders what would have been accepted, sorted, for the error a
// misspelt selection gets: a refusal that does not say what it accepts sends
// the operator back to the source code.
func zoneList() string {
	names := make([]string, len(publishedZoneNames))
	copy(names, publishedZoneNames)
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// apiPrefix is the version segment every Exoscale route sits under, and the
// suffix a client needs on the address it is handed.
const apiPrefix = "/v2"

// listZones answers the first call the official CLI makes.
//
// api-endpoint points back at this emulator, which is the whole reason the route
// exists: the client takes the address from here and sends everything after it
// there. Answering api-ch-gva-2.exoscale.com would hand the client back to the
// real cloud on the second request.
//
// It carries the /v2 prefix, and that was measured too. The official CLI
// concatenates this value with the route it wants — it does not add a version
// segment of its own — so publishing a bare host makes it ask for /instance and
// take the 404 as an empty account. Their own document agrees: the servers it
// declares are https://api-<zone>.exoscale.com/v2.
//
// sos-endpoint is deliberately empty. Object storage is not emulated — the same
// limit Scaleway's S3 has — and pointing it at this server would have a client
// upload to a route that does not exist. An empty string is what "not here"
// looks like; an invented address is a promise the emulator cannot keep.
func (p *Pack) listZones(w http.ResponseWriter, r *http.Request) {
	// One row: the zone this deployment serves (Pack.zone, #278). The real
	// answer also carries an id per zone, measured on 2026-08-10 and absent
	// from their published schema, which this emulator's contract check
	// enforces as closed. The field stays off the wire for the same reason as
	// security-group's visibility: a response the emulator would refuse itself
	// is not one it may send.
	zones := []map[string]any{{
		"name": p.zone,
		// No id, and that is measured rather than an oversight. The live API
		// sends one on every zone — recorded in shapes/exoscale.json — while
		// their published OpenAPI declares no such field on `zone`. Emitting
		// it fails this emulator's own contract check, which is the gate that
		// keeps every other answer honest.
		//
		// Same call as start/stop's `resource` envelope in lifecycle.go: the
		// live API is ahead of its own description in four measured places,
		// and the rule is to serve what clients decode and what the contract
		// accepts. Raised as #94 from a shape diff and closed as not-a-defect
		// on that basis: TestEveryRouteAnswersItsContract in internal/probe fails
		// the moment the field is added.
		"api-endpoint": emulator.EndpointOf(r) + apiPrefix,
		"sos-endpoint": "",
	}}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"zones": zones})
}

// listDeployTargets answers the read the CLI makes while resolving a create.
// An emulated account has no deploy target, and the empty list is the measured
// answer of a real account that has none.
func (p *Pack) listDeployTargets(w http.ResponseWriter, _ *http.Request) {
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"deploy-targets": []any{}})
}

// getDeployTarget can only miss: nothing here ever creates one.
func (p *Pack) getDeployTarget(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "resource not found")
}

// instanceTypes is the emulated offering. Two sizes of the standard family:
// enough for a client to pick one, few enough that nobody mistakes it for
// Exoscale's real catalogue. The identifiers are stable across runs so a client
// can hardcode one.
//
// family and size are closed enums in the API description, so the values here
// are theirs. memory is in bytes, which is the unit their own schema uses; a
// value in mebibytes would display as a machine four thousand times too small.
//
// No "zones" key here: which zone an entry is available in follows the pack's
// datum, stamped at construction (stampedWithZone), so the catalogue cannot
// declare a zone this deployment does not serve.
var instanceTypes = []map[string]any{
	{
		"id": "21624abb-764e-4def-81d7-9fc54b5957fb", "family": "standard", "size": "tiny",
		"cpus": 1, "memory": 1073741824, "gpus": 0, "authorized": true,
	},
	{
		"id": "b6cd1ff5-3a2f-4e9d-a4d1-8988c1191fe8", "family": "standard", "size": "small",
		"cpus": 2, "memory": 2147483648, "gpus": 0, "authorized": true,
	},
}

// listTemplates serves the same table the machine driver boots from, so what a
// client can choose and what the emulator can start cannot drift apart. Holding
// two lists is how a client ends up picking a template that boots nothing.
//
// The two worlds are NOT one list. Their document declares a visibility filter
// on this operation — enum private|public, with a family filter beside it
// (.upstream/exoscale-openapi.yaml:21648-21662) — and the split is the point:
// public is Exoscale's catalogue, private is what the organisation registered
// or promoted. The first version discarded the request (`_ *http.Request`), so
// `?visibility=private` on a fresh store answered the public catalogue, every
// entry contradicting the filter it was inside (#271). No client escaped the
// parameter either: `exo compute instance-template list` always sends
// visibility (its default is public), and sends family beside it when asked —
// measured through a recording proxy on exo 1.95.1.
//
// Absent, visibility defaults to public. That is the one case no real client
// exercises — the CLI and egoscale always name one — and the catalogue is what
// keeps a paramless read (the probe, a curl, the cross-pack catalogue guard)
// meaning "what can I boot here".
//
// TestTemplateVisibilityIsHonoured fails without the filter, and
// TestTemplateFamilyIsHonoured without family.
func (p *Pack) listTemplates(w http.ResponseWriter, r *http.Request) {
	visibility := r.URL.Query().Get("visibility")
	family := r.URL.Query().Get("family")

	matches := func(view map[string]any) bool {
		return family == "" || view["family"] == family
	}

	out := make([]map[string]any, 0, len(p.templates))
	switch visibility {
	case "", "public":
		for _, t := range p.templates {
			if matches(t) {
				out = append(out, t)
			}
		}
	case "private":
		for _, res := range p.storedTemplates() {
			if view := p.templateView(res); matches(view) {
				out = append(out, view)
			}
		}
	default:
		// The enum is closed in their document; a value outside it is a request
		// this emulator cannot answer honestly, so it refuses rather than picking
		// a visibility the client never asked for.
		writeError(w, http.StatusBadRequest, "visibility must be private or public")
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"templates": out})
}

// storedTemplates is the tenant's own, ordered so two reads agree: an order that
// changes between reads is a permanent diff to anything that stores the list.
func (p *Pack) storedTemplates() []*resource.Resource {
	list := p.env.Store.List(kindTemplate, resource.Tenant{Provider: Name})
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}

func (p *Pack) listInstanceTypes(w http.ResponseWriter, _ *http.Request) {
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"instance-types": p.instanceTypes})
}

// getTemplate and getInstanceType answer the by-id reads a client makes once it
// has picked one from the list. A 404 here is the whole create failing, which is
// the Scaleway lesson: the inventory is not one route, it is every route the
// client walks before it posts anything.
func (p *Pack) getTemplate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for _, t := range p.templates {
		if t["id"] == id {
			emulator.WriteJSON(w, http.StatusOK, t)
			return
		}
	}
	// Then the tenant's own, for the reason the list joins them: a client that
	// picked a registered template out of the list reads it back here.
	if res, found := p.env.Store.Get(Name, kindTemplate, id); found {
		emulator.WriteJSON(w, http.StatusOK, p.templateView(res))
		return
	}
	writeError(w, http.StatusNotFound, "no template "+id)
}

func (p *Pack) getInstanceType(w http.ResponseWriter, r *http.Request) {
	for _, t := range p.instanceTypes {
		if t["id"] == r.PathValue("id") {
			emulator.WriteJSON(w, http.StatusOK, t)
			return
		}
	}
	writeError(w, http.StatusNotFound, "no instance type "+r.PathValue("id"))
}

// stampedWithZone renders catalogue rows for the zone in force: each entry
// declares it is available exactly where this deployment lives, because the
// emulator has one store and one zone per process. A client filtering a
// template or a type by zone must find it in the zone it was told about by
// list-zones, and must not find it anywhere else — a catalogue declaring
// zones the deployment does not serve is the #269 contradiction, this pack's
// dialect. Copies, never the base rows: stamping in place would leak one
// pack's zone into the next pack built from the same tables.
func stampedWithZone(rows []map[string]any, zone string) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		entry := make(map[string]any, len(row)+1)
		for key, value := range row {
			entry[key] = value
		}
		entry["zones"] = []any{zone}
		out = append(out, entry)
	}
	return out
}

// quotaResources are the limits `exo limits` prints, and they are counted from
// the store rather than invented.
//
// A quota is one of the few figures an emulator can state honestly: the limit is
// its own claim, the way the catalogue above is, and the usage is a fact it
// holds. `exo limits` is a first-class command of the official CLI and it died
// on a 404 — the quota routes were neither served nor declined, which is the
// least defensible state a route can be in.
//
// The names come from their API description's own examples. The limits are
// deliberately generous: an emulator that refused a create for a quota it made
// up would be inventing a wall, which is the opposite of what this file is for.
//
// TestQuotasAreCountedNotInvented fails without this.
var quotaResources = []struct {
	name  string
	limit int
	kind  string
}{
	{"instance", 100, kindInstance},
	{"ssh-key", 100, kindSSHKey},
	{"snapshot", 100, ""},
	{"template", 100, ""},
}

func (p *Pack) listQuotas(w http.ResponseWriter, _ *http.Request) {
	quotas := make([]map[string]any, 0, len(quotaResources))
	for _, q := range quotaResources {
		quotas = append(quotas, p.quotaOf(q.name, q.limit, q.kind))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"quotas": quotas})
}

func (p *Pack) getQuota(w http.ResponseWriter, r *http.Request) {
	for _, q := range quotaResources {
		if q.name == r.PathValue("name") {
			emulator.WriteJSON(w, http.StatusOK, p.quotaOf(q.name, q.limit, q.kind))
			return
		}
	}
	writeError(w, http.StatusNotFound, "no quota named "+r.PathValue("name"))
}

// quotaOf counts what the store holds for a resource this pack serves, and
// answers zero for one it does not. Zero is the truth here: no snapshot can
// exist while the operation that creates one is declined.
func (p *Pack) quotaOf(name string, limit int, kind string) map[string]any {
	usage := 0
	if kind != "" {
		usage = len(p.env.Store.List(kind, resource.Tenant{Provider: Name}))
	}
	return map[string]any{"resource": name, "limit": limit, "usage": usage}
}

// Catalogue is what a client reads here before it can create anything, declared
// so the cross-pack guard can drive it (#218).
//
// A create names both by id, and `exo compute instance create` resolves both by
// name through these lists first — so a declined inventory here fails the client
// before it posts anything, exactly as it does in the two other packs.
func (p *Pack) Catalogue() []emulator.CatalogueEntry {
	return []emulator.CatalogueEntry{
		{
			Method:     "GET",
			Path:       "/v2/instance-type",
			Reads:      "the instance types a create sizes a machine from",
			Collection: "instance-types",
		},
		{
			Method:     "GET",
			Path:       "/v2/template",
			Reads:      "the templates a create boots from, and which carry the login",
			Collection: "templates",
		},
	}
}
