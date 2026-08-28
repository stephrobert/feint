package storetest

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/stephrobert/feint/internal/core/resource"
)

// What a pack put in Attrs, judged by what a snapshot can give back (#567).
//
// Attrs is a map[string]any and the store's snapshot is JSON, so what a
// restore hands back is what encoding/json produces and nothing else: nil, a
// bool, a string, a float64, a []any, a map[string]any. A pack that stored any
// other Go type stored something the door cannot return.
//
// Measured on 2026-08-27, on the fourth pack, through store.Snapshot then
// store.Restore into a fresh store:
//
//	[]Rule            (barriers) -> nil
//	[]string          (segments) -> []any
//	map[string]string (addresses) -> map[string]any
//
// A restored node therefore wore no barriers, joined no segments, and a
// spreader had no backends, while the API went on describing all three.
//
// Numbers are deliberately not reported, and that boundary is measured rather
// than assumed. An int, an int64 and a uint32 all come back float64 — but a
// number has exactly one right answer on the way back, resource.Number gives
// it, and internal/cli's TestNoPackReadsAStoredNumberByAssertion already
// refuses the gesture that ignores it (#542). A []Rule has no such answer:
// recovering one means knowing the pack's own type, which internal/core must
// not (rule 5). So the shared layer repairs the number and refuses the shape.
//
// It lives here, beside Sweep, for the reason Sweep does: the invariant is the
// core's, the traffic is the pack's. A pack drives it over its own barrage —
// internal/cli's TestEveryPackRunsTheSharedBarrage is what makes a fourth pack
// fail a control it never had to remember to write, which is the half of #567
// that survives making the model pack correct.
//
// What it cannot see, stated rather than left to be discovered: it judges what
// a barrage produced, so a write on a path no barrage drives is outside it —
// Sweep's blind spot exactly, and this one has already been paid. The first
// run of this control, on 2026-08-28, reported 82 Scaleway resources holding a
// []string in Attrs["tags"] and said nothing about Exoscale, whose pools,
// block volumes and load balancers stored a map[string]string on paths that
// barrage does not reach. Those were found by reading the packs' sources for
// the same gesture. A pack whose barrage grows inherits the coverage; one
// whose barrage stays narrow inherits the silence.

// jsonShape reports what a value is, in the vocabulary a JSON decoder answers
// in, and whether that vocabulary contains it at all.
//
// The numeric cases are listed rather than reflected on, so that adding a type
// to this control is a decision somebody writes down. json.Number is here
// because a decoder configured with UseNumber produces one and resource.Number
// reads it; the store's own Restore produces float64.
func jsonShape(v any) (kind string, shaped bool) {
	switch v.(type) {
	case nil:
		return "nil", true
	case bool:
		return "bool", true
	case string:
		return "string", true
	case float64, float32,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		json.Number:
		// A number, repaired on the way out by resource.Number (#542).
		return "number", true
	case []any:
		return "[]any", true
	case map[string]any:
		return "map[string]any", true
	}
	return fmt.Sprintf("%T", v), false
}

// GoShapes reports every stored value a snapshot cannot give back as itself.
//
// One line per offending value, naming the resource, the path inside Attrs and
// the Go type, sorted so two runs of the same failure read the same. Empty
// means every attribute in the store is written in the shape the door returns.
//
// Nested values are walked, because the defect hides one hop in: a pack that
// stores a map[string]any whose values are []string has written the wrong
// shape just as surely as one that stores the []string directly, and no grep
// on the assignment can see it.
func GoShapes(resources []*resource.Resource) []string {
	var found []string
	for _, res := range resources {
		if res == nil {
			continue
		}
		keys := make([]string, 0, len(res.Attrs))
		for key := range res.Attrs {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			walkShape(res, key, res.Attrs[key], &found)
		}
	}
	sort.Strings(found)
	return found
}

// walkShape reports one value and, when the shape is one a decoder produces,
// everything inside it.
//
// depth is bounded by the data: Attrs holds what a handler decoded or built,
// and neither can be cyclic — a cycle would already have hung json.Marshal in
// Snapshot, which every pack's tests run.
func walkShape(res *resource.Resource, path string, v any, found *[]string) {
	kind, shaped := jsonShape(v)
	if !shaped {
		*found = append(*found, fmt.Sprintf(
			"%s/%s %s: Attrs[%s] holds a %s, which a snapshot gives back as %s",
			res.Tenant.Provider, res.Kind, res.ID, path, kind, restoredAs(v)))
		return
	}
	switch inner := v.(type) {
	case []any:
		for i, item := range inner {
			walkShape(res, fmt.Sprintf("%s[%d]", path, i), item, found)
		}
	case map[string]any:
		keys := make([]string, 0, len(inner))
		for key := range inner {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			walkShape(res, path+"."+key, inner[key], found)
		}
	}
}

// restoredAs says what the door will hand back instead, because the failure is
// only actionable if it names the value the next reader will actually meet.
//
// It asks encoding/json rather than reasoning about it: the answer is whatever
// a marshal-then-unmarshal produces, which is exactly what Snapshot and Restore
// do. A value json cannot marshal at all is worse than a reshaped one and says
// so — Snapshot would fail on it.
func restoredAs(v any) string {
	encoded, err := json.Marshal(v)
	if err != nil {
		return "nothing: json.Marshal refuses it, so Snapshot itself fails"
	}
	var back any
	if err := json.Unmarshal(encoded, &back); err != nil {
		return "nothing: the encoding does not decode"
	}
	kind, _ := jsonShape(back)
	if back == nil {
		return "nil"
	}
	return kind
}
