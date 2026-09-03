package scaleway

import (
	"net/http"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// The Load Balancer offer table, and the refusal that makes it mean something.
//
// #658 asked for the table alone, on the argument that this emulator's offer was
// already "closed and enforced": LB-S accepted, LB-GP-M and LB-INEXISTANT
// refused with 400. Measured on 2026-09-03, that was not so. The three refusals
// in the report came from ONE flexible IP reused across three creates, and the
// 400 they carried says as much:
//
//	{"resource":"ip","precondition":"resource_still_in_use",
//	 "help_message":"IP is already attached to a load balancer"}
//
// With a fresh IP each time, LB-S, LB-GP-M and LB-INEXISTANT all answered 200:
// the pack lowercased whatever string it was handed and stored it. So serving a
// one-entry catalogue would have published an offer the create contradicts in
// the other direction, which is the ListVolumesTypes trap read backwards: not a
// menu whose items the create refuses, but a create that accepts what the menu
// never listed.
//
// The whole table rather than one row: the four types are what a real account
// answers, and an emulated balancer forwards no packet, so there is no capacity
// claim behind LB-GP-XL that LB-S does not also make. Every row is a type the
// create accepts, which is the ListVolumesTypes test passed rather than failed,
// and it is the line ListServersTypes already sits on.
//
// # Why the create stays open
//
// Closing it was written, tested and reverted, and the reason is worth the
// paragraph because it constrains every future refusal of a VALUE in this
// repository.
//
// `corpus:check` replays a recording of the real cloud, and that recording is
// committed with every value replaced by a synthetic one of the same shape.
// The Load Balancer type is one of the replaced ones: the two CreateLB calls in
// corpus/scaleway carry `"type": "redacted-180"` and `"type": "redacted-42"`.
// A create that refuses what the table does not list therefore refuses the
// replay itself, and the gate went from 325 knowingly accepted divergences to
// 306 unaccepted ones in a single run, the whole balancer scenario collapsing
// behind the first refusal.
//
// The anonymiser keeps values it recognises (`b_ssd`, `routed_ipv4`, and
// `VPC-GW-S` in one recording of two) and replaces the rest, so what a pack may
// refuse on is bounded by what that tool preserves. createServer sits on the
// same line for the same reason: it requires `commercial_type` and does not
// check it against the catalogue it serves, and the recording's
// `commercial_type` is redacted too.
//
// Working around it inside the pack would mean recognising the shape of a
// redaction token and waving it through, which is a back door into a guard,
// built for a test to pass. Not done. What #658 asks for is the table, and the
// table is here; closing the offer is a separate question that needs the
// anonymiser to preserve the field first.

// publishedLBTypes is a verbatim reading of what a real account answers for
// GET /lb/v1/zones/fr-par-1/lb-types, captured 2026-09-03. Field for field:
// the SDK's LBType and load-balancer-zoned-v1.yml declare exactly name,
// stock_status, bandwidth, multicloud, description, region and zone.
//
// The descriptions are theirs, verbatim, because a client that shows this table
// to a human shows their words.
var publishedLBTypes = []struct {
	Name        string
	Bandwidth   uint64
	Multicloud  bool
	Description string
}{
	{"lb-s", 200000000, false,
		"Load Balancer 100 Mbit/s with internal backend servers only (Dedibox or Scaleway Elements)"},
	{"lb-gp-m", 500000000, false,
		"Load Balancer 500 Mbit/s with internal backend servers only (Dedibox or Scaleway Elements)"},
	{"lb-gp-l", 1000000000, true,
		"Load Balancer MultiCloud 1 Gbit/s with internal or external backend servers"},
	{"lb-gp-xl", 4000000000, true,
		"Load Balancer MultiCloud 4 Gbit/s with internal or external backend servers"},
}

// listLBTypes answers the offer table.
//
// Served rather than declined since #658: the decline argued that "no measured
// client reads it before creating", and that half has stopped being true. An
// Ansible collection generated from the Scaleway OpenAPI documents produces an
// `_info` module per list operation, and answering the question "what is on
// offer" is the whole purpose of that module. The Terraform provider still
// sends the type its configuration names; a Day-2 playbook is a different
// client with a different question.
//
// Paged, for the reason #271 names: the SDK declares page and page_size on
// ListLBTypes, and a handler that drops a declared parameter answers the same
// window for ever to the SDK's own pagination loop.
//
// TestTheLBOfferTableIsServedAndPaged fails without this.
func (p *Pack) listLBTypes(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	start, end := parsePage(r).slice(len(publishedLBTypes))
	page := make([]map[string]any, 0, end-start)
	for _, offered := range publishedLBTypes[start:end] {
		page = append(page, map[string]any{
			"name": offered.Name,
			// Always available: this emulator has no stock to run out of, and
			// answering anything else would be a shortage nobody can observe.
			"stock_status": "available",
			"bandwidth":    offered.Bandwidth,
			"multicloud":   offered.Multicloud,
			"description":  offered.Description,
			"region":       regionOfZone(zone),
			"zone":         zone,
		})
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"lb_types":    page,
		"total_count": len(publishedLBTypes),
	})
}
