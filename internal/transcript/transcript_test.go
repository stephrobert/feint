package transcript_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/trace"
	"github.com/stephrobert/feint/internal/transcript"
)

// A small transcript, written the way the proxy writes one: one JSON object per
// line, unserved calls carrying mounted:false and no operation, served ones
// carrying both. The bodies are invented — no value here comes from a real
// account — but the field names and nesting are the shapes Outscale returns, so
// the shape and diff assertions mean something.
const sample = `
{"t":"2026-08-08T19:00:00Z","method":"POST","path":"/api/v1/ReadNics","status":200,"mounted":false,"res":{"body":{"Nics":[{"NicId":"eni-1","MacAddress":"aa:bb","PrivateIps":[{"IsPrimary":true,"PrivateIp":"10.0.0.5"}]}],"ResponseContext":{"RequestId":"r1"}}}}
{"t":"2026-08-08T19:00:01Z","method":"POST","path":"/api/v1/ReadSecurityGroups","status":200,"mounted":false,"res":{"body":{"SecurityGroups":[{"SecurityGroupId":"sg-1"}]}}}
{"t":"2026-08-08T19:00:02Z","method":"POST","path":"/api/v1/ReadSecurityGroups","status":200,"mounted":false,"res":{"body":{"SecurityGroups":[{"SecurityGroupId":"sg-2","Description":"web"}]}}}
{"t":"2026-08-08T19:00:03Z","method":"POST","path":"/api/v1/ReadVolumes","operation":"osc/Client.ReadVolumes","status":200,"mounted":true,"res":{"body":{"Volumes":[{"VolumeId":"vol-1","Size":10,"SnapshotId":"snap-9"}]}}}
`

// emuSample is the same served operation as the emulator answers it: no
// SnapshotId, and Size as a string where the real cloud sends a number. Both are
// the kinds of defect the diff exists to surface, and both are invented for the
// test.
const emuSample = `
{"t":"2026-08-08T19:00:03Z","method":"POST","path":"/api/v1/ReadVolumes","operation":"osc/Client.ReadVolumes","status":200,"mounted":true,"res":{"body":{"Volumes":[{"VolumeId":"vol-1","Size":"10"}]}}}
`

func load(t *testing.T, s string) []trace.Exchange {
	t.Helper()
	exs, err := transcript.Load(strings.NewReader(s))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return exs
}

func TestLoadSkipsBlankLinesAndCountsExchanges(t *testing.T) {
	exs, err := transcript.Load(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(exs) != 4 {
		t.Fatalf("got %d exchanges, want 4 (blank lines skipped)", len(exs))
	}
}

func TestLoadReportsTheLineOfAMalformedRecord(t *testing.T) {
	_, err := transcript.Load(strings.NewReader("{\"path\":\"/ok\"}\nnot json\n"))
	if err == nil {
		t.Fatal("Load accepted a malformed line")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("error does not name the bad line: %v", err)
	}
}

func TestUnservedRanksByCallsThenBytes(t *testing.T) {
	unserved := transcript.Unserved(load(t, sample))
	// ReadSecurityGroups was called twice, ReadNics once: calls first.
	if len(unserved) != 2 {
		t.Fatalf("got %d unserved ops, want 2: %+v", len(unserved), unserved)
	}
	if unserved[0].Path != "/api/v1/ReadSecurityGroups" {
		t.Fatalf("ranking wrong: %s came first, want ReadSecurityGroups", unserved[0].Path)
	}
	if unserved[0].Calls != 2 {
		t.Fatalf("ReadSecurityGroups Calls = %d, want 2", unserved[0].Calls)
	}
	// The served operation must not appear among the unserved.
	for _, op := range unserved {
		if op.Path == "/api/v1/ReadVolumes" {
			t.Fatal("a served operation leaked into the unserved queue")
		}
	}
}

func TestServedCarriesTheOperationName(t *testing.T) {
	served := transcript.Served(load(t, sample))
	if len(served) != 1 || served[0].Operation != "osc/Client.ReadVolumes" {
		t.Fatalf("Served = %+v, want one op named osc/Client.ReadVolumes", served)
	}
	if served[0].Display() != "osc/Client.ReadVolumes" {
		t.Fatalf("Display = %q, want the operation name", served[0].Display())
	}
}

func TestUnservedDisplayFallsBackToPath(t *testing.T) {
	unserved := transcript.Unserved(load(t, sample))
	if got := unserved[0].Display(); got != "/api/v1/ReadSecurityGroups" {
		t.Fatalf("Display of an unnamed op = %q, want its path", got)
	}
}

func TestShapeMergesFieldsAcrossElementsAndCalls(t *testing.T) {
	fields, ok := transcript.Shape(load(t, sample), "ReadSecurityGroups")
	if !ok {
		t.Fatal("Shape did not find ReadSecurityGroups")
	}
	got := map[string]string{}
	for _, f := range fields {
		got[f.Path] = f.Type
	}
	// Description appears on only one of the two calls; the union must carry it.
	if got["SecurityGroups[].Description"] != "string" {
		t.Fatalf("Description not merged into the shape: %v", got)
	}
	// The array container and its element are distinct paths.
	if got["SecurityGroups"] != "array" {
		t.Fatalf("SecurityGroups should be array, got %q", got["SecurityGroups"])
	}
	if got["SecurityGroups[]"] != "object" {
		t.Fatalf("SecurityGroups[] should be object, got %q", got["SecurityGroups[]"])
	}
}

func TestShapeIsMissingForAnUnknownOperation(t *testing.T) {
	if _, ok := transcript.Shape(load(t, sample), "ReadNothing"); ok {
		t.Fatal("Shape claimed to find an operation the transcript never carried")
	}
}

func TestShapeSelectorMatchesActionOrPathOrOperation(t *testing.T) {
	exs := load(t, sample)
	for _, sel := range []string{"ReadVolumes", "/api/v1/ReadVolumes", "osc/Client.ReadVolumes"} {
		if _, ok := transcript.Shape(exs, sel); !ok {
			t.Fatalf("selector %q did not match the served ReadVolumes", sel)
		}
	}
}

func TestDiffReportsAbsentFieldAndTypeMismatch(t *testing.T) {
	diffs, ok := transcript.Diff(load(t, sample), load(t, emuSample), "ReadVolumes")
	if !ok {
		t.Fatal("Diff did not find ReadVolumes in the real transcript")
	}
	got := map[string]transcript.FieldDiff{}
	for _, d := range diffs {
		got[d.Path] = d
	}
	// SnapshotId is in the real response and absent from the emulator's.
	snap, ok := got["Volumes[].SnapshotId"]
	if !ok || snap.Emu != "" {
		t.Fatalf("SnapshotId should be reported absent, got %+v", snap)
	}
	// Size is a number upstream and a string in the emulator: a type mismatch,
	// not an absence.
	size, ok := got["Volumes[].Size"]
	if !ok || size.Real != "number" || size.Emu != "string" {
		t.Fatalf("Size should be number-vs-string, got %+v", size)
	}
	// VolumeId matches on both sides and must not be reported.
	if _, reported := got["Volumes[].VolumeId"]; reported {
		t.Fatal("a field that matches was reported as a difference")
	}
}

// A populated array on one side and an empty one on the other must not read as a
// type change on the container: that false positive is exactly what the walk fix
// removed. The element paths still differ, which is honest — an empty array has
// no element to show — but the container is an array on both sides.
func TestDiffDoesNotFlagAContainerWhoseArrayIsEmptyOnOneSide(t *testing.T) {
	realT := `{"method":"POST","path":"/api/v1/ReadVolumes","operation":"osc/Client.ReadVolumes","status":200,"mounted":true,"res":{"body":{"Volumes":[{"VolumeId":"vol-1"}]}}}`
	emuEmpty := `{"method":"POST","path":"/api/v1/ReadVolumes","operation":"osc/Client.ReadVolumes","status":200,"mounted":true,"res":{"body":{"Volumes":[]}}}`
	diffs, ok := transcript.Diff(load(t, realT), load(t, emuEmpty), "ReadVolumes")
	if !ok {
		t.Fatal("Diff did not find ReadVolumes")
	}
	for _, d := range diffs {
		if d.Path == "Volumes" {
			t.Fatalf("the array container was flagged as differing: %+v", d)
		}
	}
}

func TestDiffIsMissingWhenTheRealTranscriptLacksTheOperation(t *testing.T) {
	if _, ok := transcript.Diff(load(t, emuSample), load(t, sample), "ReadNics"); ok {
		t.Fatal("Diff reported on an operation absent from the real transcript")
	}
}

// A number kept as json.Number by UseNumber must be typed "number", not left as
// the Go type name: the shape is a schema, and a 19-digit identifier read
// through float64 would also have changed the value it reported on.
func TestShapeTypesANumberAsNumber(t *testing.T) {
	fields, _ := transcript.Shape(load(t, sample), "ReadVolumes")
	for _, f := range fields {
		if f.Path == "Volumes[].Size" && f.Type != "number" {
			t.Fatalf("Size typed %q, want number", f.Type)
		}
	}
}

// A map from inventory name to one repeated shape is recognised as a
// dictionary, so its keys are read as values rather than as fields.
//
// The subject is `GET /instance/v1/zones/{zone}/products/servers`: fr-par-1
// publishes 136 commercial types and the emulated catalogue serves 18 on
// purpose. Read as fields, the 118 that differ were 127 of the 136 findings the
// first real corpus produced, and they buried everything else it had to say.
func TestADictionaryOfInventoryIsRecognised(t *testing.T) {
	catalogue := map[string]any{
		"DEV1-S":  map[string]any{"ncpus": 2, "ram": 2, "arch": "x86_64"},
		"GP1-XS":  map[string]any{"ncpus": 4, "ram": 16, "arch": "x86_64"},
		"PRO2-XS": map[string]any{"ncpus": 2, "ram": 8, "arch": "x86_64"},
	}
	if !transcript.DataKeyedObject(catalogue) {
		t.Errorf("three entries of one shape under names of the cloud's own inventory were read as "+
			"three fields: %v", catalogue)
	}
}

// THE SECOND DIRECTION. An object of the API's own vocabulary is not a
// dictionary, and its absent fields stay findings.
//
// Four shapes that must all answer false, because each one is a way the rule
// could widen into silencing a real omission: fields of different shapes, a
// parent holding a scalar beside an object, two matching children (a `min` and
// a `max` under one parent is an ordinary schema), and children whose key sets
// merely overlap.
func TestAnObjectOfTheAPIsOwnVocabularyIsNotADictionary(t *testing.T) {
	for name, obj := range map[string]map[string]any{
		"fields of different shapes": {
			"image":  map[string]any{"id": "x", "name": "y"},
			"volume": map[string]any{"size": 1},
			"ip":     map[string]any{"address": "a", "dynamic": true, "id": "i"},
		},
		"a scalar beside the objects": {
			"a":     map[string]any{"n": 1},
			"b":     map[string]any{"n": 2},
			"c":     map[string]any{"n": 3},
			"total": 3,
		},
		"only two entries": {
			"min": map[string]any{"n": 1},
			"max": map[string]any{"n": 2},
		},
		"key sets that only overlap": {
			"a": map[string]any{"n": 1, "extra": 0},
			"b": map[string]any{"n": 2},
			"c": map[string]any{"n": 3},
		},
	} {
		if transcript.DataKeyedObject(obj) {
			t.Errorf("%s was read as a dictionary, so every field of it missing from an emulator "+
				"would go unreported", name)
		}
	}
}

// An empty object is not a dictionary either, which is the degenerate case a
// rule written as "all children agree" answers true for.
func TestAnEmptyObjectIsNotADictionary(t *testing.T) {
	if transcript.DataKeyedObject(map[string]any{}) {
		t.Error("an object with no children was read as a dictionary")
	}
	if transcript.DataKeyed(nil) {
		t.Error("no children at all was read as a dictionary")
	}
	if transcript.DataKeyed([][]string{{}, {}, {}}) {
		t.Error("three children with no keys of their own were read as a dictionary")
	}
}

// A value the caller calls replaced contributes no path at all — not the path
// with the replacement's type, which is the failure this guards.
//
// FieldsOfObserved is the grammar; which values count as replaced belongs to
// whoever recorded them, so it arrives as a predicate. internal/shape passes
// shape.IsRedacted; this test passes a predicate of its own, which is what
// proves the parameter is honoured rather than a rule of this package.
func TestAReplacedValueContributesNoPath(t *testing.T) {
	body := map[string]any{
		"Keypairs":  "GONE",
		"Nested":    map[string]any{"Flag": "GONE", "Name": "kept"},
		"Numbers":   []any{json.Number("1")},
		"Untouched": true,
	}
	replaced := func(v any) bool { s, ok := v.(string); return ok && s == "GONE" }

	got := map[string]string{}
	for _, f := range transcript.FieldsOfObserved(body, replaced) {
		got[f.Path] = f.Type
	}
	for _, path := range []string{"Keypairs", "Nested.Flag"} {
		if typ, present := got[path]; present {
			t.Errorf("%s was learned as %q from a replaced value", path, typ)
		}
	}
	// The witness, in both directions: the walk must still reach a sibling of a
	// replaced field, and a container above one must survive.
	for path, want := range map[string]string{
		"Nested": "object", "Nested.Name": "string", "Numbers": "array", "Untouched": "bool",
	} {
		if got[path] != want {
			t.Errorf("%s = %q, want %q: the walk stopped short of what it must still see", path, got[path], want)
		}
	}
	// And FieldsOf, which passes no predicate, learns everything as before.
	plain := map[string]string{}
	for _, f := range transcript.FieldsOf(body) {
		plain[f.Path] = f.Type
	}
	if plain["Keypairs"] != "string" {
		t.Errorf("FieldsOf lost Keypairs: a nil predicate must change nothing")
	}
}
