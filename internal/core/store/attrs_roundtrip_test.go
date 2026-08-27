package store_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/store"
)

// What a snapshot does to every Go type a pack writes into Attrs (#542).
//
// This is the measurement two other controls are drawn from, and it exists so
// that neither of them rests on a recollection. resource.Int and its
// neighbours tolerate the numbers; internal/cli's
// TestNoPackReadsAStoredNumberByAssertion refuses a numeric assertion in a
// pack and deliberately leaves `.(string)` and `.(bool)` alone. That boundary
// is only honest if strings and bools really do cross unchanged, and "JSON
// round-trips a bool as a bool" was written in #542 as a supposition, marked
// not established.
//
// It is established here. The door is the real one: store.Snapshot then
// store.Restore, which is what `feint snapshot load` and `PUT /_feint/state`
// perform, and the format snapshot.go documents as meant to outlive its
// instance.
func TestEveryGoTypeAPackWritesCrossesTheSnapshotAsMeasured(t *testing.T) {
	st := store.New()
	res := resource.New("r-1", "thing", resource.Tenant{Provider: "p"}, "ready", time.Unix(0, 0).UTC())
	res.Attrs = map[string]any{
		"anInt":      40,
		"anInt64":    int64(40),
		"aUint32":    uint32(40),
		"aUint64":    uint64(40),
		"anInt32":    int32(40),
		"aFloat64":   40.5,
		"aTrue":      true,
		"aFalse":     false,
		"aString":    "forty",
		"nested":     map[string]any{"n": 7, "flag": true, "name": "seven"},
		"collection": []any{1, "two", false},
	}
	st.Put(res)

	var saved bytes.Buffer
	if err := st.Snapshot(&saved); err != nil {
		t.Fatalf("take the snapshot: %v", err)
	}
	// Into a fresh store, which is the case the format is designed for.
	next := store.New()
	if err := next.Restore(bytes.NewReader(saved.Bytes())); err != nil {
		t.Fatalf("restore it: %v", err)
	}
	back, found := next.Get("p", "thing", "r-1")
	if !found {
		t.Fatal("the restored store lost the resource: nothing below measures anything")
	}

	// Every number, whatever it was written as, comes back a float64 — so a
	// single-type assertion on any of them yields ok=false and zero.
	for _, key := range []string{"anInt", "anInt64", "aUint32", "aUint64", "anInt32", "aFloat64"} {
		if _, isFloat := back.Attrs[key].(float64); !isFloat {
			t.Errorf("%s came back %T, not float64: the readers of resource/attrs.go are built on "+
				"this and the discipline test's boundary is drawn from it", key, back.Attrs[key])
		}
	}
	// Which is the defect, stated as the assertion a pack used to write.
	if n, ok := back.Attrs["anInt"].(int); ok || n != 0 {
		t.Errorf("the int assertion on a restored int yielded (%v, %v), want (0, false): #542 has "+
			"no subject if this holds", n, ok)
	}
	// And the readers answer it anyway, which is the accepting half.
	for _, key := range []string{"anInt", "anInt64", "aUint32", "aUint64", "anInt32"} {
		if got := resource.Int(back, key); got != 40 {
			t.Errorf("resource.Int(restored, %s) = %d, want 40", key, got)
		}
	}
	if got := resource.Uint64(back, "aUint64"); got != 40 {
		t.Errorf("resource.Uint64(restored, aUint64) = %d, want 40", got)
	}
	if got := resource.Int64(back, "anInt64"); got != 40 {
		t.Errorf("resource.Int64(restored, anInt64) = %d, want 40", got)
	}

	// Strings and bools cross unchanged. This is the half that keeps the
	// discipline test from refusing correct code, and it was the open question
	// #542 recorded rather than measured.
	if s, ok := back.Attrs["aString"].(string); !ok || s != "forty" {
		t.Errorf("a stored string came back (%v, %v): the string assertions in every pack rest on this", s, ok)
	}
	for key, want := range map[string]bool{"aTrue": true, "aFalse": false} {
		got, ok := back.Attrs[key].(bool)
		if !ok || got != want {
			t.Errorf("%s came back (%v, %v), want (%v, true): a bool assertion on Attrs is only "+
				"correct because of this", key, got, ok, want)
		}
	}

	// A number nested inside a stored map is converted too, which is the shape
	// no grep for `Attrs[…].(int)` can see: the assertion is written against
	// the inner map, one hop from the attribute.
	nested, ok := back.Attrs["nested"].(map[string]any)
	if !ok {
		t.Fatalf("a stored map came back %T", back.Attrs["nested"])
	}
	if _, isFloat := nested["n"].(float64); !isFloat {
		t.Errorf("a number nested in a stored map came back %T, not float64: resource.Number is "+
			"the value form precisely for this", nested["n"])
	}
	if flag, ok := nested["flag"].(bool); !ok || !flag {
		t.Errorf("a bool nested in a stored map came back (%v, %v)", flag, ok)
	}
	if name, ok := nested["name"].(string); !ok || name != "seven" {
		t.Errorf("a string nested in a stored map came back (%v, %v)", name, ok)
	}
}

// resource.Number answers the same value whatever width the pack wrote, and
// answers zero rather than guessing for anything that is not a number.
//
// Table-driven and here rather than in internal/core/resource because what it
// is really pinning is the set of shapes the snapshot above produces; a reader
// tested only against the types its author remembered is the seventh
// hand-written copy with a different name.
func TestTheStoredNumberReaderCoversEveryWidthAndRefusesTheRest(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  float64
	}{
		{"int", 40, 40},
		{"int8", int8(40), 40},
		{"int16", int16(40), 40},
		{"int32", int32(40), 40},
		{"int64", int64(40), 40},
		{"uint", uint(40), 40},
		{"uint8", uint8(40), 40},
		{"uint16", uint16(40), 40},
		{"uint32", uint32(40), 40},
		{"uint64", uint64(40), 40},
		{"float32", float32(40), 40},
		{"float64", float64(40), 40},
		{"the float64 a restore produces", any(float64(40)), 40},
		{"a string that looks like one", "40", 0},
		{"a bool", true, 0},
		{"nil", nil, 0},
		{"a map", map[string]any{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resource.Number(tc.value); got != tc.want {
				t.Errorf("resource.Number(%#v) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}

	// A negative number never reaches an unsigned field: a snapshot is designed
	// to be loaded into another instance, so what comes back through Restore is
	// untrusted input, and uint64(-1) is a volume of eighteen exabytes.
	res := &resource.Resource{Attrs: map[string]any{"size": float64(-1)}}
	if got := resource.Uint64(res, "size"); got != 0 {
		t.Errorf("resource.Uint64 on a stored -1 answered %d, want 0", got)
	}
	// And an absent key, and a nil resource, answer zero rather than panicking:
	// every caller replaced a comma-ok assertion that did the same.
	if got := resource.Int(res, "absent"); got != 0 {
		t.Errorf("resource.Int on an absent key answered %d, want 0", got)
	}
	if got := resource.Int(nil, "size"); got != 0 {
		t.Errorf("resource.Int on a nil resource answered %d, want 0", got)
	}
}
