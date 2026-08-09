package outscale

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// Filters are where an Outscale read says what it wants, and where this pack
// used to say nothing at all.
//
// The API description declares 66 filters on a Vm, 9 on a Subnet, 8 on a Net and
// 7 on a keypair. The pack read four of them and returned the whole inventory
// for every other one, with a 200. An audit sent
// `--Filters.SubnetIds[] subnet-deadbeef` against seven machines and got all
// seven back. That is worse than a refusal, because it is indistinguishable from
// success: a script that deletes what a filter matched deletes everything.
//
// So a filter is either applied or refused, never ignored. The set of applied
// ones is declared per action beside the handler that reads them, and anything
// else a client sends is a 400 naming the field — which is what the real API
// does for a filter it does not know, and what a client can act on.
//
// This lives in the pack rather than in the core because `Filters` is Outscale's
// shape: Scaleway filters through query parameters and Exoscale barely filters
// at all. What is shared is the rule, not the code.
// It is a map rather than a struct on purpose. The unread-field report walks
// request types by reflection and reports every key a struct does not declare;
// a map accepts every key by construction, so nothing under Filters is reported
// any more. That would be a loss if the report were still the only thing
// watching these fields — it is not. A filter is now refused at the door when
// this pack does not apply it, which is stronger than a report: the client
// learns immediately, in the answer, rather than an operator learning later
// from a counter. TestAnUnsupportedFilterIsRefused holds that, and the
// conformance suite drives it with the real client.
type filterSet map[string]json.RawMessage

// strings reads a list-of-strings filter. A filter present but empty matches
// nothing, which is what the API does: asking for an empty set of ids is not
// asking for everything.
func (f filterSet) strings(name string) ([]string, bool) {
	raw, ok := f[name]
	if !ok {
		return nil, false
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false
	}
	return out, true
}

// unsupported names the filters a client sent that this pack does not apply.
func (f filterSet) unsupported(supported ...string) []string {
	known := make(map[string]bool, len(supported))
	for _, name := range supported {
		known[name] = true
	}
	var out []string
	for name := range f {
		if !known[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out) // stable message, so a test can assert on it
	return out
}

// refuseUnsupported answers the client when it asked for a filter this pack
// does not apply, and reports whether it did.
//
// The message names the fields and what is served, because a filter refused
// without saying which is a 400 a caller cannot act on.
//
// TestAnUnsupportedFilterIsRefused fails without this.
func (p *Pack) refuseUnsupported(w http.ResponseWriter, f filterSet, supported ...string) bool {
	unknown := f.unsupported(supported...)
	if len(unknown) == 0 {
		return false
	}
	sort.Strings(supported)
	p.badRequest(w, "the filter(s) "+strings.Join(unknown, ", ")+
		" are not emulated; this call filters on "+strings.Join(supported, ", "))
	return true
}

// matchesStrings reports whether a value passes a list filter. An absent filter
// passes everything, which is the only case where "no filter" and "match all"
// are the same thing.
func matchesStrings(f filterSet, name, value string) bool {
	return matchesAny(f, name, value)
}

// matchesAny is matchesStrings for a resource that holds several values under
// one filter name: a route table matches RouteDestinationIpRanges when ANY of
// its routes carries the destination asked for.
//
// It exists because the Terraform provider reads a nested resource back by
// filtering on the nested field, and the first version of these handlers
// declared only the top-level filters. The provider created the route, then
// asked for it by destination, and the pack answered 400 — an apply that dies at
// resource eleven of thirteen with every earlier resource correct. No unit test
// saw it; the fixture did, immediately.
func matchesAny(f filterSet, name string, values ...string) bool {
	wanted, present := f.strings(name)
	if !present {
		return true
	}
	for _, candidate := range wanted {
		for _, value := range values {
			if candidate == value {
				return true
			}
		}
	}
	return false
}

// matchesBool reports whether a boolean value passes a boolean filter, for the
// handful Outscale declares that way (LinkRouteTableMain, Default).
func matchesBool(f filterSet, name string, value bool) bool {
	raw, ok := f[name]
	if !ok {
		return true
	}
	var wanted bool
	if err := json.Unmarshal(raw, &wanted); err != nil {
		return true
	}
	return wanted == value
}
