package outscale

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/core/emulator"
)

// The controls #566 needed, and none of them is a shape check.
//
// The defect was that "a filter is either applied or refused, never ignored"
// was a sentence in filters.go with four counter-examples underneath it. Three
// separate things had to become measurable, because no one of them would have
// caught the other two:
//
//  1. every read that takes Filters declares what it filters on, so a handler
//     added later cannot escape the two controls below by not being listed;
//  2. every declared kind is the type the provider's own document declares, so
//     the reason a value is refused comes from contracts/outscale.json rather
//     than from somebody's memory;
//  3. an unreadable value fails closed in the matchers, so the one direction
//     that produces a silent success is the one direction the code cannot take.
//
// The witness for the fourth — a filter declared and compared nowhere, which no
// type can see — is TestEveryDeclaredFilterCanExcludeSomething, beside the pack
// tests that can drive a populated store.

func outscaleContract(t *testing.T) *contract.Doc {
	t.Helper()
	doc, err := contract.Load(filepath.Join("..", "..", "..", "contracts", "outscale.json"))
	if err != nil {
		t.Fatalf("load the contract: %v", err)
	}
	return doc
}

// filterSchemaOf resolves an action to the schema of its Filters property.
func filterSchemaOf(doc *contract.Doc, action string) (contract.Schema, bool) {
	op, known := doc.Operations[action]
	if !known {
		return contract.Schema{}, false
	}
	request, known := doc.Schemas[op.Request]
	if !known {
		return contract.Schema{}, false
	}
	filters, declared := request.Properties["Filters"]
	if !declared || filters.Ref == "" {
		return contract.Schema{}, false
	}
	schema, known := doc.Schemas[filters.Ref]
	return schema, known
}

// Every action this pack mounts whose request carries Filters declares what it
// filters on, in filtersByAction.
//
// Without this the two controls below cover whatever somebody remembered to add
// to the map, which is the coverage-by-vigilance that #566 is a year-long
// example of. The population comes from two places that cannot both be wrong in
// the same direction: the pack's own mounted routes, and the provider's
// document saying which of those take a Filters object.
func TestEveryFilteringOperationDeclaresItsFilters(t *testing.T) {
	doc := outscaleContract(t)
	env := emulator.DefaultEnv()
	pack := New(env)

	var missing []string
	mounted := 0
	for _, route := range pack.Routes() {
		action := strings.TrimPrefix(route.Path, pathPrefix)
		if _, takesFilters := filterSchemaOf(doc, action); !takesFilters {
			continue
		}
		mounted++
		if _, declared := filtersByAction[action]; !declared {
			missing = append(missing, action)
		}
	}
	// A control that looks for absence proves it can find first: an empty
	// population here would pass while measuring nothing.
	if mounted == 0 {
		t.Fatal("no mounted route takes a Filters object, so this test compared nothing: " +
			"the contract lookup or the path prefix is what broke, not the pack")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("these mounted actions take Filters and declare none in filtersByAction: %s\n"+
			"a read that is not listed there is a read the kind control and the exclusion "+
			"witness never look at", strings.Join(missing, ", "))
	}

	// And the other direction: an entry naming an action this pack does not
	// mount is an entry nothing exercises.
	served := map[string]bool{}
	for _, route := range pack.Routes() {
		served[strings.TrimPrefix(route.Path, pathPrefix)] = true
	}
	for action := range filtersByAction {
		if !served[action] {
			t.Errorf("filtersByAction names %s, which this pack does not mount", action)
		}
	}
}

// Every declared kind is the type contracts/outscale.json declares.
//
// This is the source rule, applied to the one place #566 got wrong: VolumeSizes
// and Progresses are `items: {type: integer}` upstream and were read as strings,
// so `[40]` failed to decode and was reported as "no filter". The document is
// the authority, and the day one of these types moves upstream this fails
// instead of the filter quietly matching everything again.
func TestEveryDeclaredFilterKindIsTheOneTheContractDeclares(t *testing.T) {
	doc := outscaleContract(t)

	compared := 0
	for action, specs := range filtersByAction {
		schema, known := filterSchemaOf(doc, action)
		if !known {
			t.Errorf("%s: the contract declares no Filters schema, so its declarations are held against nothing", action)
			continue
		}
		for _, spec := range specs {
			property, declared := schema.Properties[spec.Name]
			if !declared {
				t.Errorf("%s declares the filter %s, which the contract's own Filters schema does not have",
					action, spec.Name)
				continue
			}
			compared++
			want := kindOfProperty(property)
			if want != spec.Kind {
				t.Errorf("%s reads %s as %s; the contract declares %s",
					action, spec.Name, spec.Kind.describe(), want.describe())
			}
		}
	}
	if compared == 0 {
		t.Fatal("no filter was compared against the contract: the schema lookup is broken, " +
			"and a green run here would mean nothing")
	}
	// The witness, in the terms of the defect this test exists for: the two
	// integer filters that were read as strings for a year are in the
	// population, and named, so a refactor that stopped enumerating them cannot
	// leave this test green.
	if kind := kindOf(filtersByAction["ReadVolumes"], "VolumeSizes"); kind != intList {
		t.Errorf("ReadVolumes reads VolumeSizes as %s; #566 is the measurement that says it is a list of integers", kind.describe())
	}
	if kind := kindOf(filtersByAction["ReadSnapshots"], "Progresses"); kind != intList {
		t.Errorf("ReadSnapshots reads Progresses as %s; #566 is the measurement that says it is a list of integers", kind.describe())
	}
}

// kindOfProperty reads the contract's declared type as one of this pack's kinds.
func kindOfProperty(p contract.Property) filterKind {
	switch {
	case p.Type == "boolean":
		return boolean
	case p.Type == "array" && p.Items != nil && (p.Items.Type == "integer" || p.Items.Type == "number"):
		return intList
	default:
		return stringList
	}
}

func kindOf(specs []filterSpec, name string) filterKind {
	for _, spec := range specs {
		if spec.Name == name {
			return spec.Kind
		}
	}
	return stringList
}

// An unreadable filter matches nothing, never everything.
//
// refuseFilters should mean the matchers never see one, and this is the second
// line: the whole of #566 is that the code took the other branch, so the branch
// itself has to be pinned. An empty answer is a defect somebody reports on the
// day it appears; a full answer is a defect nobody reported for a year.
func TestAnUnreadableFilterMatchesNothingRatherThanEverything(t *testing.T) {
	set := func(body string) filterSet {
		var f filterSet
		if err := json.Unmarshal([]byte(body), &f); err != nil {
			t.Fatalf("build the filter set: %v", err)
		}
		return f
	}

	// The three shapes, each one wrong for the reader that meets it.
	strings := set(`{"VolumeIds":"vol-42"}`)       // a string where a list goes
	numbers := set(`{"VolumeSizes":["40"]}`)       // strings where integers go
	flag := set(`{"LinkRouteTableMain":["true"]}`) // a list where a bare bool goes
	good := set(`{"VolumeIds":["vol-42"],"VolumeSizes":[40],"LinkRouteTableMain":true}`)

	if matchesStrings(strings, "VolumeIds", "vol-42") {
		t.Error("an unreadable string filter matched: that is the silent 200 with the whole inventory in it")
	}
	if matchesInts(numbers, "VolumeSizes", 40) {
		t.Error("an unreadable integer filter matched")
	}
	if matchesBool(flag, "LinkRouteTableMain", true) {
		t.Error("an unreadable boolean filter matched")
	}

	// The accepting half, because a matcher that refuses everything passes the
	// three assertions above and breaks every read.
	if !matchesStrings(good, "VolumeIds", "vol-42") {
		t.Error("a readable string filter did not match the value it names")
	}
	if !matchesInts(good, "VolumeSizes", 40) {
		t.Error("a readable integer filter did not match the value it names")
	}
	if !matchesBool(good, "LinkRouteTableMain", true) {
		t.Error("a readable boolean filter did not match the value it names")
	}
	// And an absent filter still passes everything, which is the one case where
	// "no filter" and "match all" are the same thing.
	if !matchesStrings(filterSet{}, "VolumeIds", "vol-42") || !matchesInts(filterSet{}, "VolumeSizes", 40) {
		t.Error("an absent filter excluded a candidate")
	}
}
