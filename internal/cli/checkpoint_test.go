package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestACheckpointNameIsTakenBeforeTheFlags is cited by checkpointArgName.
//
// It exists because the first version of these commands let Go's flag package
// parse the name, and flag stops at the first non-flag argument: every flag
// behind the name became a positional. The first real measurement of
// `feint checkpoint baseline --addr 127.0.0.1:4621` was refused by the
// command's own usage line, which is a defect no unit test of the parsing
// would have found, because I would have written the test with the flags
// first.
func TestACheckpointNameIsTakenBeforeTheFlags(t *testing.T) {
	var stderr bytes.Buffer

	name, rest, ok := checkpointArgName([]string{"baseline", "--addr", "127.0.0.1:4621"}, &stderr, "checkpoint")
	if !ok {
		t.Fatalf("a name followed by flags was refused: %s", stderr.String())
	}
	if name != "baseline" {
		t.Errorf("name %q", name)
	}
	if len(rest) != 2 || rest[0] != "--addr" {
		t.Errorf("the flags did not survive the split: %v", rest)
	}

	// No name at all, and a flag where the name should be: both are the same
	// mistake from the operator's side and both must say what to type.
	for _, probe := range [][]string{nil, {"--addr", "127.0.0.1:4599"}} {
		stderr.Reset()
		if _, _, ok := checkpointArgName(probe, &stderr, "checkpoint"); ok {
			t.Errorf("%v was accepted as a name", probe)
		}
		if !strings.Contains(stderr.String(), "<name>") {
			t.Errorf("the refusal for %v does not say what to type: %s", probe, stderr.String())
		}
	}

	// A name that is a path component before it is a name. The emulator
	// refuses the same shapes at its own door; refusing here too means the
	// operator hears it before a request leaves.
	for _, bad := range []string{".", "..", "a/b", `a\b`} {
		stderr.Reset()
		if _, _, ok := checkpointArgName([]string{bad}, &stderr, "checkpoint"); ok {
			t.Errorf("the name %q was accepted", bad)
		}
		if !strings.Contains(stderr.String(), "path separators") {
			t.Errorf("the refusal of %q does not say why: %s", bad, stderr.String())
		}
	}
}

// TestAResetRefusesToJudgeAgainstAnotherCheckpointsState is the other half of
// the verdict's honesty.
//
// The recorded observable state belongs to ONE checkpoint. Comparing the
// emulator's restore of one against the observation of another would report
// differences that are not the reset's fault, and an operator would chase
// them. So a name that disagrees with the record is refused rather than
// guessed.
func TestAResetRefusesToJudgeAgainstAnotherCheckpointsState(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	const addr = "127.0.0.1:4599"
	if err := writeRecordedCheckpoint(addr, recordedCheckpoint{
		SchemaVersion: checkpointStateVersion, Name: "baseline",
	}); err != nil {
		t.Fatalf("record a checkpoint: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if code := resetCommand([]string{"something-else", "--addr", addr}, &stdout, &stderr); code != exitError {
		t.Fatalf("a reset against another checkpoint's state was allowed (%d)", code)
	}
	for _, want := range []string{"baseline", "something-else", "compare the wrong states"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the refusal does not mention %q: %s", want, stderr.String())
		}
	}

	// And with no record at all it says how to make one, rather than failing
	// on a missing file an operator did not know existed.
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	stderr.Reset()
	if code := resetCommand([]string{"--addr", addr}, &stdout, &stderr); code != exitError {
		t.Fatalf("a reset with no checkpoint was allowed (%d)", code)
	}
	if !strings.Contains(stderr.String(), "feint checkpoint") {
		t.Errorf("the refusal does not say how to take one: %s", stderr.String())
	}
}

// TestARecordedCheckpointRefusesAVersionItCannotRead is the forward
// compatibility half.
//
// The file is a published shape — a suite that resets between tests reads it —
// so a binary meeting a version it does not know must say so rather than
// decode what it can and compare against a half-read state. A verdict built
// on a partially understood record is worse than no verdict.
func TestARecordedCheckpointRefusesAVersionItCannotRead(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	const addr = "127.0.0.1:4599"
	if err := writeRecordedCheckpoint(addr, recordedCheckpoint{
		SchemaVersion: checkpointStateVersion + 1, Name: "from-the-future",
	}); err != nil {
		t.Fatalf("record a checkpoint: %v", err)
	}
	if _, err := readRecordedCheckpoint(addr); err == nil {
		t.Fatal("a record from a later version was read anyway")
	} else if !strings.Contains(err.Error(), "version") {
		t.Errorf("the refusal does not say it is a version: %v", err)
	}

	// The round trip of the current version, so this cannot be satisfied by
	// refusing everything.
	if err := writeRecordedCheckpoint(addr, recordedCheckpoint{
		SchemaVersion: checkpointStateVersion, Name: "baseline",
		State: observed{Machines: []string{"feint-scw-a"}},
	}); err != nil {
		t.Fatalf("record a checkpoint: %v", err)
	}
	back, err := readRecordedCheckpoint(addr)
	if err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if back.Name != "baseline" || len(back.State.Machines) != 1 {
		t.Errorf("the record did not survive the round trip: %+v", back)
	}
}
