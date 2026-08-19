package scaleway

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// instance/v2alpha1 reads the same interfaces instance/v1 creates.
//
// # Why this file exists
//
// The Terraform provider released version 2.81.0 on 17 August 2026 and, on that
// day, every `terraform apply` against this emulator broke:
//
//	Error: scaleway-sdk-go: http error 501 Not Implemented: feint does not serve
//	/instance/v2alpha1/zones/fr-par-1/private-network-interfaces
//
// The provider still *creates* a NIC through instance/v1
// (POST /servers/{id}/private_nics) and now *reads* the result through
// instance/v2alpha1, where the interface is a top-level resource carrying
// server_id rather than a sub-resource of the server. Half a product moved, and
// a client that mixes both halves is the exact case an emulator has to survive.
//
// This is what the drift machinery is for, and it worked in the direction it was
// built for: the failure named the missing path rather than producing a wrong
// answer. What it could not do is predict a release published four hours before
// it was needed.
//
// # What is served, and what is not
//
// One operation: ListPrivateNetworkInterfaces. Not a guess — `feint proxy`
// recorded the whole apply, and the client sent exactly this, twice:
//
//	GET /instance/v2alpha1/zones/fr-par-1/private-network-interfaces
//	    ?order_by=created_at_desc&project_id=…&server_ids=…
//
// The other four (Create, Get, Update, Delete) are in Declined() with that
// measurement as their reason. Serving an operation no client calls is surface
// nothing proves, and this repository counts routes driven by a real client
// rather than routes mounted.
//
// # One store, two shapes
//
// The interfaces are the resources instance/v1 already keeps: kindPrivateNIC,
// same identifiers, same addresses. Nothing is duplicated, so a NIC created
// through v1 is visible here immediately and a delete through either door
// removes one object. The two views differ where the SDKs differ, and only
// there — v1 says `creation_date` and `state`, v2alpha1 says `created_at`,
// `updated_at` and `status`.

const privateNICsV2Path = "/instance/v2alpha1/zones/{zone}/private-network-interfaces"

// listPrivateNetworkInterfaces answers instance/v2alpha1's flat view of the NICs.
//
// Flat is the point: v1 can only list the NICs of one named server, which is why
// its handler starts by resolving one. Here the server is a filter among
// several, and a client that names none gets the zone's interfaces.
func (p *Pack) listPrivateNetworkInterfaces(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}

	q := r.URL.Query()
	servers := csvValues(q, "server_ids")
	networks := csvValues(q, "private_network_ids")
	tags := csvValues(q, "tags")
	project := q.Get("project_id")

	all := p.env.Store.List(kindPrivateNIC, resource.Tenant{Provider: Name})
	matched := make([]*resource.Resource, 0, len(all))
	for _, res := range all {
		if res.Tenant.Zone != zone {
			continue
		}
		if project != "" && res.Tenant.Project != project {
			continue
		}
		if len(servers) > 0 && !contains(servers, res.Runtime[runtimeServerKey]) {
			continue
		}
		if len(networks) > 0 && !contains(networks, res.Runtime[runtimePrivateNetworkKey]) {
			continue
		}
		if len(tags) > 0 && !hasAnyTag(res, tags) {
			continue
		}
		matched = append(matched, res)
	}

	// created_at_desc is the SDK's own default. Every value the enum declares
	// is honoured — this list used to answer created_at_desc whatever the
	// client asked, which is #277's shape — and a value outside the enum is
	// refused rather than silently reordered.
	if !orderResources(w, r, "order_by", "created_at_desc", map[string]resourceCmp{
		"created_at": cmpCreated,
		"updated_at": cmpUpdated,
	}, matched) {
		return
	}

	start, end, next := tokenPage(r, len(matched))
	view := make([]map[string]any, 0, end-start)
	for _, res := range matched[start:end] {
		view = append(view, p.privateNetworkInterfaceView(res))
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"private_network_interfaces": view,
		"next_page_token":            next,
		"total_count":                len(matched),
	})
}

// createPrivateNetworkInterface attaches a server through the v2alpha1 door.
//
// The whole sequence is attachNIC, shared with instance/v1. What differs is the
// envelope: server_id arrives in the body rather than in the path, the booked
// addresses are spelled `ip_ids` (v1's newest spelling is `ipam_ip_ids`), and
// the answer is the object itself with no wrapper — the SDK decodes straight
// into PrivateNetworkInterface.
func (p *Pack) createPrivateNetworkInterface(w http.ResponseWriter, r *http.Request) {
	if _, ok := zoneOf(w, r); !ok {
		return
	}

	var req struct {
		PrivateNetworkID string   `json:"private_network_id"`
		ProjectID        string   `json:"project_id"`
		ServerID         string   `json:"server_id"`
		IPIDs            []string `json:"ip_ids"`
		Tags             []string `json:"tags"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	// server_id is optional in the SDK's request struct — the API allows an
	// interface with no server yet — but this pack's attachment is a server's
	// attachment: the address it hands out comes from the machine's zone and is
	// carried by the machine. A create without one is refused rather than stored
	// as a NIC belonging to nobody, which no client could then use.
	if req.ServerID == "" {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "server_id",
			Reason:       "required",
			HelpMessage:  "feint attaches interfaces to a server; a detached interface is not emulated",
		})
		return
	}

	res, ok := p.attachNIC(w, r, req.ServerID, createPrivateNICRequest{
		PrivateNetworkID: req.PrivateNetworkID,
		Tags:             req.Tags,
		IPIDs:            req.IPIDs,
	})
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusCreated, p.privateNetworkInterfaceView(res))
}

// getPrivateNetworkInterface reads one interface by its own ID.
//
// No server in the path, which is the shape change v2alpha1 brings: v1 could
// only reach a NIC through the server carrying it, so its handler answers 404
// for a NIC read through the wrong server. Here the interface is addressed
// directly, and the zone is the only scope.
func (p *Pack) getPrivateNetworkInterface(w http.ResponseWriter, r *http.Request) {
	res, ok := p.privateNetworkInterfaceOf(w, r)
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.privateNetworkInterfaceView(res))
}

// updatePrivateNetworkInterface changes the tags, which is all the request
// carries upstream.
func (p *Pack) updatePrivateNetworkInterface(w http.ResponseWriter, r *http.Request) {
	res, ok := p.privateNetworkInterfaceOf(w, r)
	if !ok {
		return
	}

	var req struct {
		Tags *[]string `json:"tags"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	// Inside the store lock: it keeps an update racing a delete of the same
	// interface from putting it back, address and all, and it keeps this write
	// from erasing a concurrent write to another field of the same NIC after
	// its 200 (#295).
	var updated *resource.Resource
	err := p.env.Store.Update(Name, kindPrivateNIC, res.ID, func(stored *resource.Resource) error {
		if req.Tags != nil {
			stored.Attrs["tags"] = orEmpty(*req.Tags)
		}
		stored.Updated = p.env.Now()
		updated = stored
		return nil
	})
	if err != nil {
		writeNotFound(w, "private_nic", res.ID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.privateNetworkInterfaceView(updated))
}

// deletePrivateNetworkInterface detaches through the v2alpha1 door.
//
// releaseNIC, the same function the v1 delete and the server delete call: it
// gives back the allocated address and detaches a booked one instead of dropping
// it, which is what a Terraform destroy order depends on. A second
// implementation here is exactly the copy that made deleting a server leave its
// NICs behind.
func (p *Pack) deletePrivateNetworkInterface(w http.ResponseWriter, r *http.Request) {
	res, ok := p.privateNetworkInterfaceOf(w, r)
	if !ok {
		return
	}

	// The server hold, for the reason attachNIC takes it: releasing an address
	// while a concurrent attach allocates one is the race the address lock inside
	// releaseNIC's callers already closes, and the per-server lock keeps a delete
	// from crossing an attach on the same machine.
	unlock := p.binding().Serialise(res.Runtime[runtimeServerKey])
	defer unlock()

	p.releaseNIC(res)
	w.WriteHeader(http.StatusNoContent)
}

// privateNetworkInterfaceOf resolves the {id} of a v2alpha1 path within its zone.
func (p *Pack) privateNetworkInterfaceOf(w http.ResponseWriter, r *http.Request) (*resource.Resource, bool) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return nil, false
	}
	id := r.PathValue("id")
	res, found := p.env.Store.Get(Name, kindPrivateNIC, id)
	if !found || res.Tenant.Zone != zone {
		writeNotFound(w, "private_nic", id)
		return nil, false
	}
	return res, true
}

// privateNetworkInterfaceView is the v2alpha1 shape of a NIC.
//
// PrivateNetworkInterfaceSummary, field for field. The one decision inside it is
// `status`: v1 has a syncing_error state, which this pack sets when the runtime
// refuses an attachment, and v2alpha1's enum has no error member at all —
// unknown_status, available, attaching, detaching, syncing. So a NIC the machine
// never carried is reported unknown_status here: the only value in the enum that
// does not claim the interface is fine. Mapping it to `available` would publish
// an interface the machine does not have, which is the single failure this
// project exists to avoid.
func (p *Pack) privateNetworkInterfaceView(res *resource.Resource) map[string]any {
	ids := make([]any, 0, 1)
	for _, ip := range p.ipamIPsOf(res.ID) {
		ids = append(ids, ip.ID)
	}

	status := res.State
	if status == "syncing_error" {
		status = "unknown_status"
	}

	out := map[string]any{
		"id":                 res.ID,
		"private_network_id": res.Runtime[runtimePrivateNetworkKey],
		"project_id":         res.Tenant.Project,
		"server_id":          res.Runtime[runtimeServerKey],
		"mac_address":        res.Attrs["mac_address"],
		"status":             status,
		"ip_ids":             ids,
		"tags":               orEmpty(tagsOf(res)),
		"created_at":         res.Created.Format(time.RFC3339),
		"updated_at":         res.Updated.Format(time.RFC3339),
	}
	return out
}

// tokenPage paginates with an opaque page_token, which is what v2alpha1 uses
// where v1 used a page number.
//
// The token is the offset written out, and it is opaque only to the client: the
// SDK treats it as a string it hands back untouched, so an offset satisfies the
// contract while staying readable in a transcript. A token that does not parse
// is ignored rather than refused — the same tolerance parsePage already applies
// to a malformed page, and for the same reason.
//
// next_page_token is nil on the last page, and that is the field the SDK's
// pagination loop terminates on. Returning "" instead would make it ask for one
// more page forever.
func tokenPage(r *http.Request, total int) (start, end int, next any) {
	q := r.URL.Query()
	size := defaultPageSize
	if v, err := strconv.Atoi(q.Get("page_size")); err == nil && v > 0 {
		size = min(v, maxPageSize)
	}
	if v, err := strconv.Atoi(q.Get("page_token")); err == nil && v > 0 {
		start = v
	}
	if start > total {
		start = total
	}
	end = min(start+size, total)
	if end < total {
		return start, end, strconv.Itoa(end)
	}
	return start, end, nil
}

// csvValues reads a repeated-or-comma-joined query parameter.
//
// Both spellings, because the SDK sends both: parameter.AddToQuery joins a
// []string with commas, while a hand-written caller repeats the key. Reading one
// only would silently ignore the other's filter and answer with every interface
// in the zone.
func csvValues(q url.Values, key string) []string {
	out := []string{}
	for _, raw := range q[key] {
		for _, part := range strings.Split(raw, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// tagsOf reads the tags a NIC carries, tolerating the shape a restored snapshot
// hands back: JSON has no []string, so an attr that went through a snapshot
// comes back as []any. A bare assertion would drop every tag after a restore.
func tagsOf(res *resource.Resource) []string {
	switch tags := res.Attrs["tags"].(type) {
	case []string:
		return tags
	case []any:
		out := make([]string, 0, len(tags))
		for _, t := range tags {
			if s, ok := t.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func hasAnyTag(res *resource.Resource, wanted []string) bool {
	for _, tag := range tagsOf(res) {
		if contains(wanted, tag) {
			return true
		}
	}
	return false
}

// hasAllTags is the conjunction the vpc-gw and lb lists document ("gateways
// with these tags"), where ipam/v1's filter is a disjunction.
func hasAllTags(res *resource.Resource, wanted []string) bool {
	held := tagsOf(res)
	for _, tag := range wanted {
		if !contains(held, tag) {
			return false
		}
	}
	return true
}
