package cli

import (
	"testing"

	"github.com/stephrobert/feint/internal/transcript"
)

// An element shape the emulator did not produce is not a missing field.
//
// A list that came back empty carries no element, so `Vms[]` and everything
// under it is absent — because the store is empty, not because the view forgot
// anything. Reported, it flagged nine operations on the first real run and
// drowned the findings that mattered. A gate that cries wolf on an empty store
// is a gate somebody turns off.
func TestAnEmptyListIsNotAMissingField(t *testing.T) {
	want := []transcript.Field{
		{Path: "Vms", Type: "array"},
		{Path: "Vms[]", Type: "object"},
		{Path: "Vms[].VmId", Type: "string"},
		{Path: "Vms[].Placement", Type: "object"},
	}
	// The emulator answered {"Vms": []}: the container is there, no element is.
	got := map[string]string{"Vms": "array"}

	if gaps := absentFrom(want, got); len(gaps) != 0 {
		t.Errorf("an empty list reported %d missing field(s): %v", len(gaps), gaps)
	}

	// And the accepting half, without which this rule would hide every real
	// omission: with an element present, a field it lacks is reported.
	got["Vms[]"] = "object"
	got["Vms[].VmId"] = "string"
	gaps := absentFrom(want, got)
	if len(gaps) != 1 || gaps[0].Path != "Vms[].Placement" {
		t.Errorf("a genuinely missing field was not reported: %v", gaps)
	}
}

// A dictionary's absent keys are data, not fields.
//
// `/products/servers` answers a map keyed by product name: about ninety entries
// on the real cloud, four in the emulated catalogue, deliberately —
// docs/limits.md calls the catalogue fiction. Counted as fields, those keys
// produced 46 of 113 findings on the first run.
//
// A dictionary is recognised by its values sharing one sub-shape, which is what
// separates it from a schema whose fields differ from one another.
func TestADictionarysKeysAreNotMissingFields(t *testing.T) {
	// Four products, each with the same three fields: a dictionary.
	var want []transcript.Field
	want = append(want, transcript.Field{Path: "servers", Type: "object"})
	for _, name := range []string{"DEV1-S", "DEV1-M", "GP1-XS", "PRO2-XXS"} {
		want = append(want,
			transcript.Field{Path: "servers." + name, Type: "object"},
			transcript.Field{Path: "servers." + name + ".ncpus", Type: "number"},
			transcript.Field{Path: "servers." + name + ".ram", Type: "number"},
			transcript.Field{Path: "servers." + name + ".arch", Type: "string"},
		)
	}
	// The emulator publishes one of the four, fully.
	got := map[string]string{
		"servers": "object", "servers.DEV1-S": "object",
		"servers.DEV1-S.ncpus": "number", "servers.DEV1-S.ram": "number",
		"servers.DEV1-S.arch": "string",
	}
	if gaps := absentFrom(want, got); len(gaps) != 0 {
		t.Errorf("dictionary keys were reported as missing fields: %v", gaps)
	}

	// The accepting half, and it is the one that matters: a field missing from
	// a key the emulator does publish is still reported. Without it this rule
	// would hide exactly the class of defect the gate exists for — #93 is that
	// case, eleven fields absent from products the emulator has.
	delete(got, "servers.DEV1-S.arch")
	gaps := absentFrom(want, got)
	if len(gaps) != 1 || gaps[0].Path != "servers.DEV1-S.arch" {
		t.Errorf("a field missing from a published key was not reported: %v", gaps)
	}
}
