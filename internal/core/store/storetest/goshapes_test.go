package storetest_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/store"
	"github.com/stephrobert/feint/internal/core/store/storetest"
)

// A rule struct of the kind a pack owns, so the report can be checked against
// the shape #567 measured rather than against a stand-in.
type barrier struct {
	Direction string
	PortFrom  int
}

// Both halves, because a control that reports everything passes every attack
// test and makes the barrage useless — and one that reports nothing reads
// exactly like a repository where every pack stores the JSON shape.

// The refusing half: the three shapes #567 measured, plus the one that hides a
// hop inside a legitimate container.
func TestGoShapesReportsWhatASnapshotCannotGiveBack(t *testing.T) {
	res := &resource.Resource{
		ID:     "n-1",
		Kind:   "node",
		Tenant: resource.Tenant{Provider: "four"},
		Attrs: map[string]any{
			"barriers":  []barrier{{Direction: "ingress", PortFrom: 443}},
			"segments":  []string{"seg-1"},
			"addresses": map[string]string{"seg-1": "10.40.0.10"},
			// One hop in: the container is right and its content is not, which
			// is the shape no grep on the assignment can see.
			"spreader": map[string]any{"backends": []string{"n-2"}},
		},
	}

	found := storetest.GoShapes([]*resource.Resource{res})
	if len(found) != 4 {
		t.Fatalf("want four reports, got %d:\n%s", len(found), strings.Join(found, "\n"))
	}
	report := strings.Join(found, "\n")
	for _, want := range []string{
		"Attrs[barriers] holds a []storetest_test.barrier",
		"Attrs[segments] holds a []string, which a snapshot gives back as []any",
		"Attrs[addresses] holds a map[string]string, which a snapshot gives back as map[string]any",
		"Attrs[spreader.backends] holds a []string",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report never says %q, so it does not name what the next reader will meet:\n%s",
				want, report)
		}
	}
	// The resource is named, because a report that says only "a []string" sends
	// the reader looking through every pack.
	if !strings.Contains(report, "four/node n-1") {
		t.Errorf("the report names no resource:\n%s", report)
	}
}

// The accepting half, and the larger one: everything a pack legitimately
// writes, including every numeric width, must produce no report at all.
//
// The numbers are the part worth asserting rather than assuming. They really
// do change type across a snapshot — that is #542 — and they are deliberately
// not reported here, because resource.Number reads every one of them back and
// internal/cli's TestNoPackReadsAStoredNumberByAssertion already refuses the
// gesture that ignores it. A control that also reported them would make every
// pack in this repository red for a defect the shared layer has already
// answered.
func TestGoShapesStaysSilentOnEverythingAPackLegitimatelyWrites(t *testing.T) {
	res := &resource.Resource{
		ID:     "s-1",
		Kind:   "server",
		Tenant: resource.Tenant{Provider: "scaleway"},
		Attrs: map[string]any{
			"name":     "web",
			"absent":   nil,
			"on":       true,
			"off":      false,
			"anInt":    40,
			"anInt64":  int64(40),
			"aUint32":  uint32(40),
			"aUint64":  uint64(40),
			"aFloat":   40.5,
			"tags":     []any{"a", "b"},
			"nested":   map[string]any{"port": 443, "name": "http", "on": true},
			"deep":     []any{map[string]any{"rules": []any{map[string]any{"port": 22}}}},
			"emptyAny": []any{},
			"emptyMap": map[string]any{},
		},
	}
	if found := storetest.GoShapes([]*resource.Resource{res}); len(found) != 0 {
		t.Errorf("a resource written entirely in the JSON shape produced %d report(s), so this "+
			"control would cry wolf on every barrage:\n%s", len(found), strings.Join(found, "\n"))
	}
	// And a nil member is not a crash: Sweep's population is whatever the store
	// handed over.
	if found := storetest.GoShapes([]*resource.Resource{nil}); len(found) != 0 {
		t.Errorf("a nil resource produced %v", found)
	}
}

// And the report is true: what it says a snapshot gives back is what the
// snapshot gives back.
//
// This is the half that makes the control more than an opinion about Go types.
// The message names a value the next reader will actually meet, so it is
// checked against store.Snapshot then store.Restore — the same door #567 was
// measured through — rather than against a belief about encoding/json.
func TestGoShapesNamesWhatTheDoorReallyReturns(t *testing.T) {
	st := store.New()
	res := resource.New("n-1", "node", resource.Tenant{Provider: "four"}, "up", time.Unix(0, 0).UTC())
	res.Attrs["barriers"] = []barrier{{Direction: "ingress", PortFrom: 443}}
	res.Attrs["segments"] = []string{"seg-1"}
	res.Attrs["addresses"] = map[string]string{"seg-1": "10.40.0.10"}
	st.Put(res)

	var saved bytes.Buffer
	if err := st.Snapshot(&saved); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	next := store.New()
	if err := next.Restore(bytes.NewReader(saved.Bytes())); err != nil {
		t.Fatalf("restore: %v", err)
	}
	back, found := next.Get("four", "node", "n-1")
	if !found {
		t.Fatal("the restored store lost the resource: nothing below measures anything")
	}

	// What the report promises, and what actually came back.
	for key, want := range map[string]string{
		"segments":  "[]interface {}",
		"addresses": "map[string]interface {}",
	} {
		if got := typeName(back.Attrs[key]); got != want {
			t.Errorf("%s came back %s, want %s: the report's wording would be a guess", key, got, want)
		}
	}
	// The named type is the loud one: it does not come back reshaped, it comes
	// back as a list of anonymous objects, and the pack's own reader yields
	// nil for it.
	list, isList := back.Attrs["barriers"].([]any)
	if !isList {
		t.Fatalf("barriers came back %s", typeName(back.Attrs["barriers"]))
	}
	if _, stillGo := back.Attrs["barriers"].([]barrier); stillGo {
		t.Error("the restored value is still a []barrier: #567 has no subject if this holds")
	}
	if len(list) != 1 {
		t.Fatalf("barriers came back with %d member(s)", len(list))
	}
	if _, isObject := list[0].(map[string]any); !isObject {
		t.Errorf("a restored rule is %s, not the object the report names", typeName(list[0]))
	}
}

func typeName(v any) string {
	switch v.(type) {
	case []any:
		return "[]interface {}"
	case map[string]any:
		return "map[string]interface {}"
	case []string:
		return "[]string"
	case map[string]string:
		return "map[string]string"
	case []barrier:
		return "[]barrier"
	case nil:
		return "nil"
	}
	return "something else"
}
