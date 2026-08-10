package shape

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/trace"
)

// exchange builds one recorded answer. The values are invented: a shape
// catalogue stores paths and types, so a fixture needs no real account and must
// not borrow one.
func exchange(op string, body any) trace.Exchange {
	return trace.Exchange{
		Method: "POST", Path: "/api/v1/" + op, Operation: "osc/Client." + op,
		Status: 200, Res: &trace.Message{Body: body},
	}
}

// A sparse recording never erases what a richer one saw, whichever order they
// are applied in.
//
// This is the property the whole catalogue rests on. An account with an
// unattached volume shows no LinkedVolumes; an account with an attached one
// does. If the poorer run overwrote the richer, the gate built on these files
// would report a regression that is only a thinner sample — and it would do so
// intermittently, depending on which recording ran last, which is the worst
// kind of failure to diagnose.
func TestMergingIsCommutative(t *testing.T) {
	rich := []trace.Exchange{exchange("ReadVolumes", map[string]any{
		"Volumes": []any{map[string]any{
			"VolumeId":      "vol-1",
			"SnapshotId":    "snap-1",
			"LinkedVolumes": []any{map[string]any{"VmId": "i-1"}},
		}},
	})}
	sparse := []trace.Exchange{exchange("ReadVolumes", map[string]any{
		"Volumes": []any{map[string]any{"VolumeId": "vol-2"}},
	})}

	first, second := New("outscale"), New("outscale")
	first.Merge(rich)
	first.Merge(sparse)
	second.Merge(sparse)
	second.Merge(rich)

	a, b := render(t, first), render(t, second)
	if a != b {
		t.Errorf("merge order changed the catalogue:\n--- rich then sparse ---\n%s\n--- sparse then rich ---\n%s", a, b)
	}

	// And the union survived, rather than the last writer winning.
	for _, want := range []string{"Volumes[].SnapshotId", "Volumes[].LinkedVolumes"} {
		if !hasField(first, "osc/Client.ReadVolumes", want) {
			t.Errorf("%s was lost by the sparse recording", want)
		}
	}
}

// A null observed once and a concrete type observed later is that type, not a
// conflict: JSON null carries no type information, and a field the cloud leaves
// null on one resource is not a different field.
//
// It is also what makes the merge commutative — without it, null-then-string
// and string-then-null disagree, and TestMergingIsCommutative fails for a
// reason that looks like ordering rather than like this.
func TestNullYieldsToAConcreteType(t *testing.T) {
	for _, order := range [][]any{
		{map[string]any{"PrivateIp": nil}, map[string]any{"PrivateIp": "10.0.0.1"}},
		{map[string]any{"PrivateIp": "10.0.0.1"}, map[string]any{"PrivateIp": nil}},
	} {
		c := New("outscale")
		for _, body := range order {
			c.Merge([]trace.Exchange{exchange("ReadVms", body)})
		}
		if got := fieldType(c, "osc/Client.ReadVms", "PrivateIp"); got != "string" {
			t.Errorf("PrivateIp settled on %q, want string", got)
		}
	}
}

// Two concrete types that genuinely disagree are kept, not silently resolved:
// it means the API is polymorphic there or that one recording is wrong, and a
// reader has to see it. Kept sorted so it does not depend on order either.
func TestAGenuineTypeConflictIsKept(t *testing.T) {
	c := New("outscale")
	c.Merge([]trace.Exchange{exchange("ReadVms", map[string]any{"Size": "10"})})
	c.Merge([]trace.Exchange{exchange("ReadVms", map[string]any{"Size": 10.0})})
	if got := fieldType(c, "osc/Client.ReadVms", "Size"); got != "number|string" {
		t.Errorf("a real conflict settled on %q, want number|string", got)
	}
}

// An operation that answered an empty list is recorded as observed.
//
// "Observed and empty" and "never observed" are different facts, and a gate
// that conflated them would report a missing field where the account simply had
// nothing to show.
func TestAnEmptyAnswerIsStillAnObservation(t *testing.T) {
	c := New("outscale")
	c.Merge([]trace.Exchange{exchange("ReadNets", map[string]any{"Nets": []any{}})})

	op, known := c.Operations["osc/Client.ReadNets"]
	if !known {
		t.Fatal("an operation that answered an empty list was not recorded at all")
	}
	if len(op.Fields) == 0 {
		t.Error("the container itself was not recorded")
	}
	if !hasField(c, "osc/Client.ReadNets", "Nets") {
		t.Error("Nets is missing, so a reader cannot tell empty from unobserved")
	}
	// And no element shape was invented from a list that had none.
	if hasField(c, "osc/Client.ReadNets", "Nets[]") {
		t.Error("an element shape appeared for a list with no elements")
	}
}

// An error body is not the operation's shape.
//
// Measured on the first real run of tools/shapes/record.sh: several Outscale
// calls answered a 4xx, and the catalogue happily recorded `Errors[].Code` as a
// field of ReadVolumes. It is not — an error body is a different schema, which
// is the same rule internal/contract states for its own check.
//
// The operation is still recorded, with its status, because "called and
// refused" is a different fact from "never called": it tells a reader the shape
// is unobserved rather than empty.
func TestAnErrorBodyIsNotTheOperationsShape(t *testing.T) {
	c := New("outscale")
	bad := exchange("ReadVolumes", map[string]any{
		"Errors":          []any{map[string]any{"Code": "4120", "Type": "InvalidParameterValue"}},
		"ResponseContext": map[string]any{"RequestId": "abc"},
	})
	bad.Status = 400
	c.Merge([]trace.Exchange{bad})

	op, known := c.Operations["osc/Client.ReadVolumes"]
	if !known {
		t.Fatal("a refused call was dropped; a reader cannot tell it from one never made")
	}
	if len(op.Fields) != 0 {
		t.Errorf("an error body became the operation's shape: %v", op.Fields)
	}
	if !containsInt(op.Statuses, 400) {
		t.Error("the refusal status was not recorded, so nothing says the shape is unobserved")
	}
	// And the file says `[]`, never null: a null would make "answered and
	// carried nothing" indistinguishable from "refused", which is precisely
	// what Statuses exists to tell apart.
	if strings.Contains(render(t, c), `"fields": null`) {
		t.Error("a refused operation wrote a null field list")
	}

	// And the accepting half: a 200 for the same operation is folded in, so
	// this is a rule about failure rather than a guard that records nothing.
	c.Merge([]trace.Exchange{exchange("ReadVolumes", map[string]any{
		"Volumes": []any{map[string]any{"VolumeId": "vol-1"}},
	})})
	if !hasField(c, "osc/Client.ReadVolumes", "Volumes[].VolumeId") {
		t.Error("a successful answer was not recorded")
	}
	if hasField(c, "osc/Client.ReadVolumes", "Errors") {
		t.Error("the error body survived alongside the real shape")
	}
}

// Saving the same catalogue twice produces the same bytes.
//
// The file is committed and read by `git diff`: map iteration order leaking
// into it would make every refresh report a change that is only Go's
// randomisation, and a signal that cries wolf every time gets ignored — the
// exact reason coverage/ carries no scan date.
func TestSavingTwiceProducesTheSameBytes(t *testing.T) {
	c := New("outscale")
	c.Merge([]trace.Exchange{
		exchange("ReadVms", map[string]any{"Vms": []any{map[string]any{"VmId": "i-1", "State": "running"}}}),
		exchange("ReadNets", map[string]any{"Nets": []any{map[string]any{"NetId": "vpc-1"}}}),
	})
	// Two separate renders, held in variables: comparing the call to itself
	// reads as a tautology to a linter and to a human, and the property being
	// checked is that writing twice gives the same bytes.
	first := render(t, c)
	second := render(t, c)
	if first != second {
		t.Errorf("two writes of one catalogue differ:\n%s\n---\n%s", first, second)
	}
}

// An operation nothing claims is kept under its route rather than dropped.
// A path a real client walked and no pack serves is the most interesting line
// a recording produces; losing it for lack of a name would defeat the purpose.
func TestAnUnnamedOperationIsKeptUnderItsRoute(t *testing.T) {
	c := New("scaleway")
	c.Merge([]trace.Exchange{{
		Method: "GET", Path: "/block/v1alpha1/zones/fr-par-1/volumes/x",
		Status: 200, Res: &trace.Message{Body: map[string]any{"id": "x"}},
	}})
	if _, known := c.Operations["GET /block/v1alpha1/zones/fr-par-1/volumes/x"]; !known {
		t.Errorf("an unnamed operation was dropped; catalogue holds %v", keys(c))
	}
}

// A round trip through the file keeps everything, or a refresh would quietly
// shed what earlier runs learned.
func TestACatalogueSurvivesItsOwnFile(t *testing.T) {
	c := New("exoscale")
	c.Merge([]trace.Exchange{exchange("ListInstances", map[string]any{
		"instances": []any{map[string]any{"id": "1", "name": "a"}},
	})})

	var buf bytes.Buffer
	if err := c.Save(&buf); err != nil {
		t.Fatalf("save: %v", err)
	}
	back, err := Load(&buf)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if render(t, back) != render(t, c) {
		t.Error("a catalogue changed by being written and read back")
	}
}

func render(t *testing.T, c *Catalogue) string {
	t.Helper()
	var buf bytes.Buffer
	if err := c.Save(&buf); err != nil {
		t.Fatalf("save: %v", err)
	}
	return buf.String()
}

func fieldType(c *Catalogue, op, path string) string {
	o, known := c.Operations[op]
	if !known {
		return ""
	}
	for _, f := range o.Fields {
		if f.Path == path {
			return f.Type
		}
	}
	return ""
}

func hasField(c *Catalogue, op, path string) bool { return fieldType(c, op, path) != "" }

func keys(c *Catalogue) []string {
	out := make([]string, 0, len(c.Operations))
	for k := range c.Operations {
		out = append(out, k)
	}
	return out
}
