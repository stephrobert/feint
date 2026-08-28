package outscale

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/core/resource"
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
//
// # The third answer, and why the sentence above needed a type to be true
//
// The sentence "applied or refused, never ignored" was written here and the code
// beneath it ignored four filters for a year (#566). The mechanism is
// measurement-integrity's shape one storey down: reading a filter has *three*
// answers — applied, absent, unreadable — and the reader kept only two, folding
// the unreadable one onto "absent", which is precisely the reading that produces
// a silent 200 with the whole inventory in it.
//
// Measured on 2026-08-28 against `main@2879888`, and wider than #566 stated:
//
//	Filters.VolumeSizes  [40]    -> 200, both volumes  (the type the API declares)
//	Filters.VolumeSizes  ["40"]  -> 200, one volume    (a type it does not)
//	Filters.VolumeIds    "vol-x" -> 200, both volumes  (a string where an array goes)
//	Filters.Progresses   [7]     -> 200, four snapshots of Progress 100
//	Filters.AccountIds   ["…0"]  -> 200, four snapshots of AccountId …1
//
// The first two lines are the inversion worth keeping in mind: the only shape
// this pack could read was the one the API does not declare. The last two are
// not decode failures at all — `snapshotFilters` named three filters
// `snapshotMatches` never mentioned, so they were declared applied and never
// compared, which no decoder could have caught.
//
// Two things changed, and each has its own control:
//
//   - a filter now carries its kind (filterSpec below), taken from the type
//     `contracts/outscale.json` declares for it, and a value that is not
//     written that way is refused at the door by refuseFilters rather than
//     dropped. TestAFilterOfTheWrongShapeIsRefusedRatherThanIgnored fails
//     without it, and TestEveryDeclaredFilterKindIsTheOneTheContractDeclares
//     holds the kinds against the contract so the table cannot drift from its
//     source.
//   - the matchers below fail *closed* on an unreadable value, and that half is
//     defence in depth rather than the guard. Measured by running the
//     falsification: neutralising any of the three `err != nil` branches leaves
//     TestAnUnreadableFilterMatchesNothingRatherThanEverything green, because
//     json.Unmarshal leaves the slice empty on a failure and a present filter
//     with no accepted values already matches nothing. The property is
//     structural; the branches state it, and cost nothing, and their comment
//     must not claim a test they cannot redden — which is the exact defect this
//     repository names in "un commentaire n'est pas un contrôle", found here in
//     the code that fixes it.
//
// What no type can catch is the third line of the measurement — a filter
// declared and never compared. TestEveryDeclaredFilterCanExcludeSomething is
// the witness for that one: every declared filter is sent a value nothing in
// the store carries, and the answer must be empty.
type filterSet map[string]json.RawMessage

// filterKind is how a filter's value is written on the wire.
//
// Not a taste: `contracts/outscale.json` declares a type for every one of the
// 247 filters in the document, and 56 of them are not arrays of strings. Four
// of those 56 are served here.
type filterKind uint8

const (
	// stringList is `{"type":"array","items":{"type":"string"}}`, which is 191
	// of the 247.
	stringList filterKind = iota
	// intList is `{"type":"array","items":{"type":"integer"}}`. Two are served:
	// VolumeSizes (ReadVolumes, ReadSnapshots) and Progresses (ReadSnapshots).
	intList
	// boolean is a bare `{"type":"boolean"}` — not a list, which is why it has
	// its own matcher. Two are served: LinkRouteTableMain and
	// LinkVolumeDeleteOnVmDeletion.
	boolean
)

// describe names the shape in the words a refusal can use.
func (k filterKind) describe() string {
	switch k {
	case intList:
		return "a list of whole numbers"
	case boolean:
		return "true or false"
	default:
		return "a list of strings"
	}
}

// filterSpec is one filter a handler applies, and the shape its value takes.
//
// The kind travels with the name rather than living in a table of its own
// because the same name is not always the same type upstream: CpuGenerations is
// a list of integers on one schema and a list of strings on another. A global
// map keyed by name would be right until the day the second one is served,
// which is the exemption-whose-key-does-not-match-its-subject shape.
type filterSpec struct {
	Name string
	Kind filterKind
}

// stringFilters, intFilters and boolFilters declare a handler's filters by kind.
// They read as a sentence at the call site — `stringFilters("VolumeIds", …)`,
// `intFilters("VolumeSizes")` — so the declaration and the shape cannot drift
// apart in the way a parallel list of "the numeric ones" would.
func stringFilters(names ...string) []filterSpec { return specsOf(stringList, names) }
func intFilters(names ...string) []filterSpec    { return specsOf(intList, names) }
func boolFilters(names ...string) []filterSpec   { return specsOf(boolean, names) }

func specsOf(kind filterKind, names []string) []filterSpec {
	out := make([]filterSpec, 0, len(names))
	for _, name := range names {
		out = append(out, filterSpec{Name: name, Kind: kind})
	}
	return out
}

// joinFilters concatenates the groups a handler declares.
func joinFilters(groups ...[]filterSpec) []filterSpec {
	var out []filterSpec
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}

// filterNames is the declared names, sorted, for a message a caller reads.
func filterNames(specs []filterSpec) []string {
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		out = append(out, spec.Name)
	}
	sort.Strings(out)
	return out
}

// filtersByAction names, for every action of this pack that reads Filters, the
// list its handler applies.
//
// The handlers keep referring to the variables directly, so the compiler still
// holds that half; this map exists so a control can *enumerate* the
// declarations, which is the half nothing had. Three tests read it:
//
//   - TestEveryFilteringOperationDeclaresItsFilters, which walks the mounted
//     routes whose request schema carries a Filters property in
//     contracts/outscale.json and fails on one missing here — so a read added
//     later cannot escape the two controls below by not being listed;
//   - TestEveryDeclaredFilterKindIsTheOneTheContractDeclares, which holds each
//     kind against the type that document declares for it;
//   - TestEveryDeclaredFilterCanExcludeSomething, the witness that catches what
//     no type can: a filter declared here and compared nowhere. Three of
//     snapshotFilters' seven were in exactly that state until #566.
var filtersByAction = map[string][]filterSpec{
	"ReadDhcpOptions":            dhcpOptionsFilters,
	"ReadImages":                 imageFilters,
	"ReadInternetServices":       internetServiceFilters,
	"ReadKeypairs":               keypairFilters,
	"ReadLoadBalancers":          loadBalancerFilters,
	"ReadNatServices":            natServiceFilters,
	"ReadNetAccessPointServices": serviceFilters,
	"ReadNetPeerings":            netPeeringFilters,
	"ReadNets":                   netFilters,
	"ReadNics":                   nicFilters,
	"ReadPublicIps":              publicIPFilters,
	"ReadRouteTables":            routeTableFilters,
	"ReadSecurityGroups":         securityGroupFilters,
	"ReadSnapshots":              snapshotFilters,
	"ReadSubnets":                subnetFilters,
	"ReadSubregions":             subregionFilters,
	"ReadTags":                   tagFilters,
	"ReadVmTypes":                vmTypeFilters,
	"ReadVms":                    vmFilters,
	"ReadVmsState":               vmStateFilters,
	"ReadVolumes":                volumeFilters,
}

// errUnreadableFilter is the third answer: the filter is there, and this pack
// could not read its value. It is deliberately distinct from "absent", which is
// the fold that produced #566.
var errUnreadableFilter = errors.New("the filter's value is not written the way the API declares it")

// strings reads a list-of-strings filter. A filter present but empty matches
// nothing, which is what the API does: asking for an empty set of ids is not
// asking for everything.
//
// Three returns, not two: present says whether the client sent the filter, and
// err says whether its value could be read. Folding the second onto the first
// is what let a decode failure answer "no filter" and pass every candidate.
func (f filterSet) strings(name string) ([]string, bool, error) {
	raw, ok := f[name]
	if !ok {
		return nil, false, nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, true, errUnreadableFilter
	}
	return out, true, nil
}

// ints reads a list-of-integers filter — VolumeSizes and Progresses, the two
// this pack serves that the API declares as `items: {type: integer}`.
//
// json.Unmarshal into []int refuses a string, so `["40"]` is unreadable here
// rather than silently coerced. That is the contract's own type, and coercing
// would put this pack back in the business of guessing what a client meant.
func (f filterSet) ints(name string) ([]int, bool, error) {
	raw, ok := f[name]
	if !ok {
		return nil, false, nil
	}
	var out []int
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, true, errUnreadableFilter
	}
	return out, true, nil
}

// boolean reads a bare boolean filter (LinkRouteTableMain, and the volume's
// LinkVolumeDeleteOnVmDeletion).
func (f filterSet) boolean(name string) (bool, bool, error) {
	raw, ok := f[name]
	if !ok {
		return false, false, nil
	}
	var out bool
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, true, errUnreadableFilter
	}
	return out, true, nil
}

// read decodes one filter according to its declared kind, and answers whether
// the client sent it and whether it could be read.
func (f filterSet) read(spec filterSpec) (bool, error) {
	switch spec.Kind {
	case intList:
		_, present, err := f.ints(spec.Name)
		return present, err
	case boolean:
		_, present, err := f.boolean(spec.Name)
		return present, err
	default:
		_, present, err := f.strings(spec.Name)
		return present, err
	}
}

// unsupported names the filters a client sent that this pack does not apply.
func (f filterSet) unsupported(supported []filterSpec) []string {
	known := make(map[string]bool, len(supported))
	for _, spec := range supported {
		known[spec.Name] = true
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

// unreadable names the filters a client sent that this pack applies and could
// not read.
func (f filterSet) unreadable(supported []filterSpec) []filterSpec {
	var out []filterSpec
	for _, spec := range supported {
		if _, err := f.read(spec); err != nil {
			out = append(out, spec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// refuseFilters answers the client when a filter it sent cannot be applied, and
// reports whether it did. Two reasons, one door:
//
//   - the pack does not apply that filter at all — the original refusal, which
//     names the fields and what is served, because a filter refused without
//     saying which is a 400 a caller cannot act on;
//   - the pack applies it and cannot read the value — #566's third answer. The
//     refusal names the filter and the shape the API declares for it, so the
//     caller can fix the call rather than believe a 200 that filtered nothing.
//
// The second half is a refusal where the emulator used to answer 200, so it is
// the half that could diverge: it is right if the real API refuses a value its
// own document types otherwise, and `contracts/outscale.json` is the evidence
// for the type, not for the refusal. No recording under corpus/ carries a
// wrongly-typed filter, so that half is reasoned, not measured — and it is
// reasoned from this file's own rule, which leaves no third place for such a
// value to go.
//
// TestAnUnsupportedFilterIsRefused and
// TestAFilterOfTheWrongShapeIsRefusedRatherThanIgnored fail without this.
func (p *Pack) refuseFilters(w http.ResponseWriter, f filterSet, supported []filterSpec) bool {
	if unknown := f.unsupported(supported); len(unknown) > 0 {
		p.badRequest(w, "the filter(s) "+strings.Join(unknown, ", ")+
			" are not emulated; this call filters on "+strings.Join(filterNames(supported), ", "))
		return true
	}
	if unreadable := f.unreadable(supported); len(unreadable) > 0 {
		said := make([]string, 0, len(unreadable))
		for _, spec := range unreadable {
			said = append(said, spec.Name+" ("+spec.Kind.describe()+")")
		}
		p.badRequest(w, "the filter(s) "+strings.Join(said, ", ")+
			" carry a value this API does not declare; the shape in brackets is the one "+
			"contracts/outscale.json describes, and a filter that cannot be read is refused "+
			"rather than ignored")
		return true
	}
	return false
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
	wanted, present, err := f.strings(name)
	if err != nil {
		// refuseFilters should mean this is unreachable, and it is stated for
		// the day a handler forgets the gate: an unreadable filter that matches
		// nothing is a defect somebody reports, and one that matches everything
		// is #566, which nobody reported for a year.
		//
		// It is not the guard, and saying so is the point. Neutralised in a
		// copy of the tree, every test stays green: the reader answers
		// (nil, present, err) and a present filter with no accepted values
		// already matches nothing, so the property survives this line's
		// removal. Kept because it costs nothing and because a future reader of
		// filterSet.strings could change what an unreadable value looks like;
		// not cited as though a test held it.
		return false
	}
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

// matchesInts reports whether any of a resource's numbers passes a numeric
// filter — the second half of #566.
//
// A resource that carries no number for the filter passes it in nothing: a
// volume whose Attrs hold no Size cannot answer VolumeSizes, and saying so is
// the honest reading. Call it with no values for that case.
func matchesInts(f filterSet, name string, values ...int) bool {
	wanted, present, err := f.ints(name)
	if err != nil {
		return false // fail closed, as matchesAny does and with the same caveat
	}
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
	wanted, present, err := f.boolean(name)
	if err != nil {
		return false // fail closed, as matchesAny does and with the same caveat
	}
	if !present {
		return true
	}
	return wanted == value
}

// numbersOf reads the number a rendered view publishes under a key, so a filter
// compares what the client can see rather than something stored beside it.
//
// Through resource.Number, never a type assertion: Attrs crosses encoding/json
// on every snapshot, so a size written as an int comes back a float64 and
// `.(int)` yields zero — the defect #542 measured and
// TestNoPackReadsAStoredNumberByAssertion now refuses in every pack.
//
// Presence is asked of the map and not of the reader, because resource.Number
// answers 0 for "absent", for "not a number" and for "zero" alike, and a filter
// is exactly the caller that must tell those apart. A key that is absent, or
// holds something that is not a number, yields no value at all: the object then
// matches no numeric filter, which is the honest reading of "this emulator does
// not know that number for this object".
func numbersOf(view map[string]any, key string) []int {
	value, present := view[key]
	if !present {
		return nil
	}
	switch value.(type) {
	case nil, string, bool, map[string]any, []any:
		return nil
	}
	return []int{int(resource.Number(value))}
}
