package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A recording is written the way the proxy writes one. The values are invented —
// no line here comes from a real account — so the file is safe to commit as a
// test fixture; what is asserted is the shape, never a value.
const cliSample = `{"method":"POST","path":"/api/v1/ReadSecurityGroups","status":200,"mounted":false,"res":{"body":{"SecurityGroups":[{"SecurityGroupId":"sg-1","Description":"web"}]}}}
{"method":"POST","path":"/api/v1/ReadVolumes","operation":"osc/Client.ReadVolumes","status":200,"mounted":true,"res":{"body":{"Volumes":[{"VolumeId":"vol-1","SnapshotId":"snap-9"}]}}}
`

const cliEmuSample = `{"method":"POST","path":"/api/v1/ReadVolumes","operation":"osc/Client.ReadVolumes","status":200,"mounted":true,"res":{"body":{"Volumes":[{"VolumeId":"vol-1"}]}}}
`

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runTranscript(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := transcriptCommand(args, &out, &errb)
	return out.String(), errb.String(), code
}

func TestTranscriptQueueListsUnservedOperations(t *testing.T) {
	file := writeTemp(t, "rec.jsonl", cliSample)
	out, errs, code := runTranscript(t, file)
	if code != exitOK {
		t.Fatalf("exit %d, stderr: %s", code, errs)
	}
	if !strings.Contains(out, "/api/v1/ReadSecurityGroups") {
		t.Fatalf("the unserved operation is not in the queue:\n%s", out)
	}
	if !strings.Contains(out, "osc/Client.ReadVolumes") {
		t.Fatalf("the served operation is not shown for comparison:\n%s", out)
	}
}

// The file comes first, before the flags. Go's flag package stops at the first
// positional, so this only works because the command peels the file off the
// front; a regression there would put --shape into NArg and fail.
func TestTranscriptTakesTheFileBeforeTheFlags(t *testing.T) {
	file := writeTemp(t, "rec.jsonl", cliSample)
	out, errs, code := runTranscript(t, file, "--shape", "ReadSecurityGroups")
	if code != exitOK {
		t.Fatalf("exit %d, stderr: %s", code, errs)
	}
	if !strings.Contains(out, "SecurityGroups[].Description") {
		t.Fatalf("shape output missing a known field:\n%s", out)
	}
}

func TestTranscriptDiffNamesAFieldTheEmulatorOmits(t *testing.T) {
	real := writeTemp(t, "real.jsonl", cliSample)
	emu := writeTemp(t, "emu.jsonl", cliEmuSample)
	out, errs, code := runTranscript(t, real, "--shape", "ReadVolumes", "--against", emu)
	if code != exitOK {
		t.Fatalf("exit %d, stderr: %s", code, errs)
	}
	if !strings.Contains(out, "SnapshotId") || !strings.Contains(out, "absent") {
		t.Fatalf("diff did not report the omitted field:\n%s", out)
	}
}

func TestTranscriptRefusesAgainstWithoutShape(t *testing.T) {
	file := writeTemp(t, "rec.jsonl", cliSample)
	_, errs, code := runTranscript(t, file, "--against", file)
	if code != exitError {
		t.Fatal("--against without --shape was accepted")
	}
	if !strings.Contains(errs, "--shape") {
		t.Fatalf("the refusal does not point at --shape: %s", errs)
	}
}

func TestTranscriptNeedsAFile(t *testing.T) {
	_, errs, code := runTranscript(t, "--shape", "ReadVms")
	if code != exitError {
		t.Fatal("a bare flag with no file was accepted")
	}
	if !strings.Contains(errs, "recording file first") {
		t.Fatalf("unhelpful error for a missing file: %s", errs)
	}
}

func TestTranscriptUnknownOperationFails(t *testing.T) {
	file := writeTemp(t, "rec.jsonl", cliSample)
	_, errs, code := runTranscript(t, file, "--shape", "ReadNothing")
	if code != exitError {
		t.Fatal("an unknown operation returned success")
	}
	if !strings.Contains(errs, "ReadNothing") {
		t.Fatalf("error does not name the operation: %s", errs)
	}
}
