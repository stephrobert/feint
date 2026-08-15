package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// A digest that answers the question it is asked, and no other (#171).
//
// The gate below rests entirely on this: if the digest moved on its own, every
// join would be refused and the gate would be disabled within the week; if it
// stayed still when a suite changed, the criterion it enforces would be a
// sentence again.
func TestTheDigestMovesWithTheInputsAndNotOtherwise(t *testing.T) {
	dir := t.TempDir()
	suites := filepath.Join(dir, "conformance", "scaleway")
	if err := os.MkdirAll(suites, 0o750); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(suites, "scw-cli.sh")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write("assert one\nassert two\n")
	first, err := digestTree(filepath.Join(dir, "conformance"), ".sh")
	if err != nil {
		t.Fatalf("digest: %v", err)
	}

	// Twice on unchanged inputs: the same answer, or the gate refuses joins for
	// no reason and somebody removes it.
	again, err := digestTree(filepath.Join(dir, "conformance"), ".sh")
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if first != again {
		t.Errorf("two digests of unchanged inputs disagree: %q then %q", first, again)
	}

	// An assertion removed from a suite — the exact case the criterion names.
	write("assert one\n")
	fewer, err := digestTree(filepath.Join(dir, "conformance"), ".sh")
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if fewer == first {
		t.Error("removing an assertion from a suite left the digest unchanged, so the " +
			"record could still be joined and the proof would outlive its evidence")
	}

	// A file of another kind is not an input: the digest names what it reads.
	if err := os.WriteFile(filepath.Join(suites, "notes.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	unrelated, err := digestTree(filepath.Join(dir, "conformance"), ".sh")
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if unrelated != fewer {
		t.Error("a file the record never reads moved the digest, which makes the gate " +
			"fire on changes it has no opinion about")
	}

	// An absent directory is stated rather than an error: `feint evidence` runs
	// from trees with no recordings, and refusing there would trade a missing
	// input for a missing command.
	absent, err := digestTree(filepath.Join(dir, "nothing-here"), ".json")
	if err != nil {
		t.Fatalf("an absent directory must not be an error: %v", err)
	}
	if absent != "absent" {
		t.Errorf("an absent directory must say so, got %q", absent)
	}
}

// The join gate, both halves.
//
// This is the control the criterion never had. Removing an assertion from a
// suite moves the suite digest, which makes the previous leg unjoinable, which
// is what stops a level surviving the evidence that earned it.
func TestAJoinRefusesAnArtefactFromOtherInputs(t *testing.T) {
	same := provenance{Contracts: "aaa", Shapes: "bbb", Suites: "ccc"}

	if moved := same.differsFrom(same); len(moved) != 0 {
		t.Errorf("two legs of one regeneration must join, got %v refused", moved)
	}

	for _, tc := range []struct {
		name  string
		other provenance
		want  string
	}{
		{"a suite lost an assertion", provenance{"aaa", "bbb", "different"}, "suites"},
		{"a contract was re-extracted", provenance{"different", "bbb", "ccc"}, "contracts"},
		{"a recording was added", provenance{"aaa", "different", "ccc"}, "shapes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			moved := same.differsFrom(tc.other)
			if len(moved) != 1 || moved[0] != tc.want {
				t.Errorf("the refusal must name what moved, want [%s] got %v", tc.want, moved)
			}
		})
	}

	// And it names every input that moved, not the first: a reader fixing one
	// and re-running only to be refused again learns to distrust the message.
	all := same.differsFrom(provenance{"x", "y", "z"})
	if len(all) != 3 {
		t.Errorf("every moved input must be named, got %v", all)
	}
}

// The written record carries its provenance, and the gate is not vacuous.
//
// This is the test that would have caught the first version of #171: the join
// built a fresh artefact and dropped the field, so every regenerated record
// carried three empty digests — and three empty digests compare equal to three
// empty digests, which means the gate refusing a stale leg would have accepted
// everything while reading like a control. The unit tests passed throughout;
// what found it was looking at the file the tool had just written.
func TestTheJoinedRecordKeepsItsProvenance(t *testing.T) {
	from := provenance{Contracts: "c0ffee", Shapes: "5ha9e5", Suites: "5u17e5"}
	fresh := &evidenceArtefact{
		Format: evidenceFormat, Version: evidenceVersion,
		Machines: []string{"none"}, GeneratedFrom: from,
		Operations: map[string]emulator.Evidence{"a/v1/API.X": {Driven: true}},
	}
	other := &evidenceArtefact{
		Format: evidenceFormat, Version: evidenceVersion,
		Machines: []string{"incus"}, GeneratedFrom: from,
		Operations: map[string]emulator.Evidence{"a/v1/API.X": {Dataplane: true}},
	}

	joined := joinEvidence(fresh, other)
	if joined.GeneratedFrom != from {
		t.Errorf("the join lost the provenance: %+v", joined.GeneratedFrom)
	}
	// And it is not the zero value, which is the shape that made the gate
	// vacuous: a record whose digests are empty matches every other such record.
	if joined.GeneratedFrom == (provenance{}) {
		t.Error("the joined record carries empty digests, so the join gate would " +
			"accept any stale leg while looking like a control")
	}
}

// A record that cannot be read is not a record that proves nothing was dropped.
//
// runtimesLost answered "nothing lost" to every read failure, which made the two
// answers indistinguishable: a first write, and a file this binary cannot parse.
// Bumping evidenceVersion for #171 is exactly the second case, so the guard
// would have gone quiet on the one regeneration where it matters most.
func TestAnUnreadableRecordIsNotAnAbsentOne(t *testing.T) {
	dir := t.TempDir()
	next := &evidenceArtefact{Machines: []string{"none"}}

	// Absent: nothing to compare, and that is not a narrowing.
	lost, err := runtimesLost(filepath.Join(dir, "never-written.json"), next)
	if err != nil || lost != nil {
		t.Errorf("a first write must lose nothing quietly, got %v / %v", lost, err)
	}

	// Present and from another version: the comparison cannot be made, and the
	// caller must hear about it rather than be told nothing was lost.
	other := filepath.Join(dir, "old.json")
	body := `{"format":"feint-evidence","version":1,"machines":["incus"],"operations":{}}`
	if err := os.WriteFile(other, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimesLost(other, next); err == nil {
		t.Error("a record this binary cannot read reported no error, so a narrowing " +
			"would be written unchecked while the guard looked like it ran")
	}
}
