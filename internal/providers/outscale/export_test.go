package outscale

import "strconv"

// What the pack lets its own tests see, and nothing else.
//
// TestEveryDeclaredFilterCanExcludeSomething needs two things this package
// keeps to itself: which filters each read declares, and a value of the right
// type that nothing in a store can carry. It lives in the external test package
// because it has to drive a populated emulator through HTTP, which is where the
// pack's server helpers are.

// DeclaredFilter is one filter a read declares, plus a value of its own type
// that no object can hold.
type DeclaredFilter struct {
	Name string
	// Absent is the JSON value to send, written in the filter's declared shape.
	// Empty when no such value exists: a boolean filter has two values and both
	// are in the domain, so it cannot be witnessed this way and is covered by a
	// test of its own (TestARootVolumeAnswersItsDeleteOnVmDeletionFilter).
	Absent string
}

// impossibleText is a string no identifier, name, address, state or description
// this pack mints can equal. impossibleNumber is the same for a size, a
// progress or a count.
const (
	impossibleText   = "feint-nothing-carries-this-value"
	impossibleNumber = 987654
)

// DeclaredFilters is filtersByAction in the terms above.
func DeclaredFilters() map[string][]DeclaredFilter {
	out := make(map[string][]DeclaredFilter, len(filtersByAction))
	for action, specs := range filtersByAction {
		row := make([]DeclaredFilter, 0, len(specs))
		for _, spec := range specs {
			row = append(row, DeclaredFilter{Name: spec.Name, Absent: absentValue(spec.Kind)})
		}
		out[action] = row
	}
	return out
}

func absentValue(kind filterKind) string {
	switch kind {
	case intList:
		return "[" + strconv.Itoa(impossibleNumber) + "]"
	case boolean:
		return ""
	default:
		return `["` + impossibleText + `"]`
	}
}
