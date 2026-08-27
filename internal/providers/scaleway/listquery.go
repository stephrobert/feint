package scaleway

import (
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/core/resource"
)

// The rule these helpers exist for is #277's: a query parameter an operation
// declares is served or refused, never dropped. Thirteen list operations of
// this pack read their page and nothing else, so `?order_by=created_at_desc`
// answered ascending with a 200 — an answer shaped exactly like the right one.
// The per-operation gate — internal/core/emulator's
// TestNoDeclaredQueryParameterIsDroppedByItsHandler — fails any handler whose
// call graph never names a declared parameter; these helpers are how a handler
// names them honestly.

// resourceCmp compares two resources on one sortable field, ascending; the
// _desc spelling negates it at sort time.
type resourceCmp func(a, b *resource.Resource) int

func cmpCreated(a, b *resource.Resource) int { return a.Created.Compare(b.Created) }
func cmpUpdated(a, b *resource.Resource) int { return a.Updated.Compare(b.Updated) }
func cmpName(a, b *resource.Resource) int {
	return strings.Compare(textOf(a.Attrs["name"]), textOf(b.Attrs["name"]))
}

// orderResources sorts items by the order the client asked for, in place.
//
// param is the parameter's spelling on this operation — `order_by` everywhere
// except instance/v1's ListServers, which says `order`. fallback is the
// default the SDK documents for the operation ("Default value: created_at_asc"
// and friends): an absent or empty parameter means that default, and empty
// happens on every instance/v1 list, whose non-pointer enum marshals its zero
// value onto the wire (`order=`).
//
// A value outside the operation's declared enum is refused with a 400 naming
// the parameter, never served under some other order: sorted-by-something-else
// is indistinguishable from sorted-as-asked in a short list, which is how this
// class survived every suite (#277).
//
// The sort is stable, so equal keys keep store order and two reads answer
// identically — anything a client stores must read back the same.
func orderResources(w http.ResponseWriter, r *http.Request, param, fallback string, fields map[string]resourceCmp, items []*resource.Resource) bool {
	base, descending, ok := orderAsked(w, r, param, fallback, func(field string) bool {
		_, known := fields[field]
		return known
	})
	if !ok {
		return false
	}
	cmp := fields[base]
	sort.SliceStable(items, func(i, j int) bool {
		c := cmp(items[i], items[j])
		if descending {
			return c > 0
		}
		return c < 0
	})
	return true
}

// orderAsked reads and validates the ordering the client asked for, and writes
// the refusal itself when the value is outside the operation's declared enum.
//
// Split out of orderResources for the lists this pack answers without a store
// behind them: account/v3's projects are a fixed inventory, like the catalogue,
// so there is no []*resource.Resource to sort — and a handler that simply
// ignored order_by would be dropping a declared parameter, which is the class
// #277 measured and internal/core/emulator's
// TestNoDeclaredQueryParameterIsDroppedByItsHandler holds.
func orderAsked(w http.ResponseWriter, r *http.Request, param, fallback string, known func(string) bool) (field string, descending, ok bool) {
	value := r.URL.Query().Get(param)
	if value == "" {
		value = fallback
	}
	base, descending := orderSpelling(value)
	if !known(base) {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: param,
			Reason:       "constraint",
			HelpMessage:  "unknown " + param + " " + value,
		})
		return "", false, false
	}
	return base, descending, true
}

// orderSpelling splits created_at_desc into its field and its direction. A
// value with neither suffix keeps its own spelling as the field, so it fails
// the enum lookup and gets refused by name.
func orderSpelling(value string) (field string, descending bool) {
	if base, ok := strings.CutSuffix(value, "_desc"); ok {
		return base, true
	}
	if base, ok := strings.CutSuffix(value, "_asc"); ok {
		return base, false
	}
	return value, false
}

// queryBool reads a boolean filter: present only when the client sent a
// value, true only on the spelling the SDK writes (fmt.Sprint on a bool).
func queryBool(q url.Values, key string) (value, present bool) {
	raw := q.Get(key)
	if raw == "" {
		return false, false
	}
	return raw == "true", true
}

// objectStorageFilter reads the "only VPCs (or Private Networks) whose Object
// Storage private access is on" filter, under both names it has had.
//
// Scaleway renamed the whole family on 2026-08-25 — the five S3-endpoint
// operations became *ObjectStoragePrivateAccess (triaged in pack.go the same
// day) and the query parameter went with them: the SDK now emits
// `object_storage_private_access_enabled` (vpc_sdk.go:1978 and :2179) and the
// portal's document declares only that spelling. The old one is what every
// client built before the rename still sends, and dropping it would silently
// widen a filter that used to narrow.
//
// Both, first one present wins, which is exactly the per_page/page_size idiom
// parsePage already carries and for the same reason: reading one spelling only
// answers a filtered list with everything.
//
// TestBothSpellingsOfTheObjectStorageFilterNarrow fails without this.
func objectStorageFilter(q url.Values) (value, present bool) {
	for _, key := range []string{"object_storage_private_access_enabled", "s3_integration_enabled"} {
		if v, ok := queryBool(q, key); ok {
			return v, true
		}
	}
	return false, false
}

// idSet turns the repeated values of an identifier filter into a lookup —
// volume_ids, private_network_ids, subnet_ids, servers. csvValues reads both
// encodings a client uses: repeated keys (what parameter.AddToQuery emits for
// a slice) and one comma-joined value (what the API documents for
// instance/v1's `servers`).
func idSet(q url.Values, key string) map[string]bool {
	values := csvValues(q, key)
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]bool, len(values))
	for _, v := range values {
		out[v] = true
	}
	return out
}
