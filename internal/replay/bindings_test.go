package replay

import (
	"reflect"
	"testing"
)

// The three values the fixtures below turn on. The recorded side spells
// organization_id and project_id the same, which is not a contrivance: it is
// what a Scaleway account with a single project answers, and it is what #352
// recorded (corpus/scaleway/*.jsonl).
const (
	recordedID   = "11111111-1111-4111-8111-111111111111"
	recordedBoth = "22222222-2222-4222-8222-222222222222"

	mintedID           = "33333333-3333-4333-8333-333333333333"
	mintedOrganisation = "44444444-4444-4444-8444-444444444444"
	mintedProject      = "55555555-5555-4555-8555-555555555555"
)

func recordedAnswer() (want, got map[string]any) {
	return map[string]any{
			"id":              recordedID,
			"organization_id": recordedBoth,
			"project_id":      recordedBoth,
		}, map[string]any{
			"id":              mintedID,
			"organization_id": mintedOrganisation,
			"project_id":      mintedProject,
		}
}

// One recorded value, two field names, two answers — and the field name is what
// decides which one a later request carries.
//
// Without this the replay had a map from value to value, so the two candidates
// fought over one slot and whichever field the walk reached first won. The cost
// was not theoretical: when the organisation won, the create that followed filed
// its private network under a project the unfiltered list does not cover, and
// vpc/v2/API.ListPrivateNetworks came back divergent — a divergence the replay
// had manufactured itself.
func TestOneRecordedValueUnderTwoFieldsBindsByFieldName(t *testing.T) {
	b := newBindings()
	want, got := recordedAnswer()
	b.learn("", want, got)

	for field, expect := range map[string]string{
		"project_id":      mintedProject,
		"organization_id": mintedOrganisation,
	} {
		if to := b.applyValue(field, recordedBoth); to != expect {
			t.Errorf("a request body's %s carried %v, want the one this emulator minted for that field (%s)", field, to, expect)
		}
		if to := b.applyQuery(field + "=" + recordedBoth); to != field+"="+expect {
			t.Errorf("a query filter on %s became %q, want %q", field, to, field+"="+expect)
		}
	}

	// The ambiguity is counted rather than silently resolved: a reader is
	// entitled to know the recording said less than the replay needed.
	if n := b.ambiguities(); n != 1 {
		t.Errorf("ambiguities() = %d, want 1: one recorded value had two candidate bindings", n)
	}
	// And a value with only one candidate still resolves with no field name at
	// all, which is what a path segment has.
	if to, bound := b.resolve("", recordedID); !bound || to != mintedID {
		t.Errorf("an unambiguous identifier resolved to %q (%v) with no field to scope it, want %s", to, bound, mintedID)
	}
}

// The same recording binds the same way on every run.
//
// Go randomises map iteration, and the first version of [bindings.learn] ranged
// over the recorded object: the sibling that reached the map first claimed a
// value both carried. Six replays of corpus/scaleway/scw-cli.jsonl against six
// fresh emulators graded the same operation divergent three times and matched
// three times (corpus/README.md). A gate whose verdict flaps is a gate that gets
// disarmed the first time it seems to lie, so this asserts the whole map, many
// times: an unsorted walk survives one comparison with probability one half.
func TestTheSameRecordingBindsTheSameWayOnEveryRun(t *testing.T) {
	want, got := recordedAnswer()
	// Sibling keys spread across the alphabet, so no single ordering is
	// accidentally the sorted one.
	want["zone_project"] = recordedBoth
	got["zone_project"] = "66666666-6666-4666-8666-666666666666"
	want["account_project"] = recordedBoth
	got["account_project"] = "77777777-7777-4777-8777-777777777777"

	first := newBindings()
	first.learn("", want, got)
	for i := range 200 {
		again := newBindings()
		again.learn("", want, got)
		if !reflect.DeepEqual(again.to, first.to) {
			t.Fatalf("run %d bound the unscoped map differently: %v, first run had %v", i, again.to, first.to)
		}
		if !reflect.DeepEqual(again.byField, first.byField) {
			t.Fatalf("run %d bound the field-scoped map differently", i)
		}
	}
}

// A field name that never appeared falls back to what the value bound
// elsewhere, rather than to nothing.
//
// The second direction, and it is the one that keeps the scoping from breaking
// what already worked: a create answers `id` and the read that follows puts that
// identifier in its *path*, where there is no field name at all. Scoping without
// this fallback would leave every path unrebound and every read answering 404.
func TestAValueWithNoFieldOfItsOwnStillResolves(t *testing.T) {
	b := newBindings()
	want, got := recordedAnswer()
	b.learn("", want, got)

	if path := b.applyPath("/vpc/v2/regions/fr-par/vpcs/" + recordedID); path != "/vpc/v2/regions/fr-par/vpcs/"+mintedID {
		t.Errorf("applyPath left the recorded identifier in place: %s", path)
	}
	if to := b.applyValue("vpc_id", recordedID); to != mintedID {
		t.Errorf("a body field nothing was learned under resolved to %v, want the unscoped binding %s", to, mintedID)
	}
}
