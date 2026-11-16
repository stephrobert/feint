package cli

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
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

// TestAResetNamesTheCheckpointItCouldNotFind replaces a refusal that lost its
// subject to the audit's second finding.
//
// Before, the CLI kept one observation per instance, so asking to reset a
// name it had not recorded last meant comparing one checkpoint's restore to
// another's observation — and the command refused, honestly, while the
// feature was missing. Observations are indexed by name now, so an unknown
// name is simply unknown, and what matters is that the refusal says WHICH
// name and how to make one.
func TestAResetNamesTheCheckpointItCouldNotFind(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	const addr = "127.0.0.1:4599"
	if err := writeRecordedCheckpoint(addr, recordedCheckpoint{
		SchemaVersion: checkpointStateVersion, Name: "baseline",
	}); err != nil {
		t.Fatalf("record a checkpoint: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if code := resetCommand([]string{"something-else", "--addr", addr}, &stdout, &stderr); code != exitError {
		t.Fatalf("a reset of a name nobody recorded was allowed (%d)", code)
	}
	for _, want := range []string{"something-else", "feint checkpoint"} {
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

// TestTwoNamedCheckpointsDoNotOverwriteEachOther is cited by checkpointPath,
// and it is the audit's second finding.
//
// The emulator holds any number of named checkpoints; the CLI wrote a single
// file per instance. So `checkpoint A` then `checkpoint B` made `reset A`
// impossible, because the observation A would have been judged against had
// been overwritten by B's.
func TestTwoNamedCheckpointsDoNotOverwriteEachOther(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	const addr = "127.0.0.1:4599"

	for _, name := range []string{"alpha", "beta"} {
		if err := writeRecordedCheckpoint(addr, recordedCheckpoint{
			SchemaVersion: checkpointStateVersion, Name: name,
			State: observed{Machines: []string{"feint-scw-" + name}},
		}); err != nil {
			t.Fatalf("record %s: %v", name, err)
		}
	}

	// Both are still readable BY NAME, which is what the single file broke.
	for _, name := range []string{"alpha", "beta"} {
		back, err := readRecordedCheckpoint(addr, name)
		if err != nil {
			t.Fatalf("read %s back: %v", name, err)
		}
		if back.Name != name {
			t.Errorf("reading %q answered %q", name, back.Name)
		}
		if len(back.State.Machines) != 1 || back.State.Machines[0] != "feint-scw-"+name {
			t.Errorf("%s carries the wrong observation: %v", name, back.State.Machines)
		}
	}

	// And an empty name means the last taken, so `feint reset` with no
	// argument has something to mean.
	last, err := readRecordedCheckpoint(addr, "")
	if err != nil {
		t.Fatalf("read the last: %v", err)
	}
	if last.Name != "beta" {
		t.Errorf("the last taken reads as %q", last.Name)
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
	if _, err := readRecordedCheckpoint(addr, ""); err == nil {
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
	back, err := readRecordedCheckpoint(addr, "")
	if err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if back.Name != "baseline" || len(back.State.Machines) != 1 {
		t.Errorf("the record did not survive the round trip: %+v", back)
	}
}

// TestAnUnreadableObservationIsAnErrorRatherThanAnEmptyState is cited by
// observe.
//
// The defect was a verdict drawn from an absence: a failed read returned an
// empty observed state, so `feint reset` compared a checkpoint against nothing
// and printed "the lab converged" — and a checkpoint that itself held nothing
// compared equal to the failed read. Every caller already branches on the
// error; before this test the branch could not be reached.
func TestAnUnreadableObservationIsAnErrorRatherThanAnEmptyState(t *testing.T) {
	silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer silent.Close()

	state, _, err := observeFor(silent.URL)
	if err == nil {
		t.Fatalf("an endpoint that answers nothing was read as %d resource(s), 0 error(s)", len(state.Resources))
	}
	if !strings.Contains(err.Error(), "reading the control plane") {
		t.Errorf("the error does not say which read failed: %v", err)
	}
}

// TestAnUnresolvableRuntimeStillReturnsTheReadError is cited by observeFor's
// branch for a runtime this station no longer has.
//
// The comment there calls it "the moment a reset is most needed": the emulator
// is up and answering, the driver that backed it is gone. The host half is
// deliberately allowed to stay empty, but the control-plane read is not: an
// error folded onto a nil here would make that station read "converged" again.
func TestAnUnresolvableRuntimeStillReturnsTheReadError(t *testing.T) {
	restore := resolveRuntime
	resolveRuntime = func(string, io.Writer) (machine.Runtime, error) {
		return machine.Runtime{}, errors.New("the runtime driver is gone")
	}
	t.Cleanup(func() { resolveRuntime = restore })

	lab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_feint/health" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"machines":"incus"}`))
	}))
	defer lab.Close()

	if _, _, err := observeFor(lab.URL); err == nil {
		t.Fatal("a station whose runtime went away read an unreachable lab as an empty state")
	}
}

// refusingReadsLab answers the checkpoint POST and DELETE, and refuses every
// read the observation takes. It counts the two verbs: a checkpoint the lab
// holds and the CLI never recorded is the orphan these tests are about.
func refusingReadsLab(t *testing.T) (endpoint string, posts, deletes *atomic.Int32) {
	t.Helper()
	posts, deletes = &atomic.Int32{}, &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/_feint/checkpoint/") {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodDelete {
			deletes.Add(1)
		} else {
			posts.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":1,"name":"x","resources":0,"bytes":false}`))
	}))
	t.Cleanup(server.Close)
	return server.URL, posts, deletes
}

// TestACheckpointThatCannotBeReadBackLeavesNothingBehind is cited by
// checkpointCommand's read-back branch.
//
// The emulator takes the checkpoint and then refuses the read. The command
// refuses too, and it must remove what it just took: a name the lab holds and
// this CLI has no record of answers a later reset with "no checkpoint named",
// and answers a later checkpoint by re-taking a name nobody can reset to.
func TestACheckpointThatCannotBeReadBackLeavesNothingBehind(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	endpoint, posts, deletes := refusingReadsLab(t)
	addr := strings.TrimPrefix(endpoint, "http://")

	var stdout, stderr bytes.Buffer
	if code := checkpointCommand([]string{"baseline", "--addr", addr}, &stdout, &stderr); code != exitError {
		t.Fatalf("an unreadable checkpoint exited %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "could not be read back") {
		t.Errorf("the refusal does not say what failed: %s", stderr.String())
	}
	if posts.Load() != 1 || deletes.Load() != 1 {
		t.Errorf("the lab saw %d POST(s) and %d DELETE(s); want 1 and 1", posts.Load(), deletes.Load())
	}
	path, err := checkpointPath(addr, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a record was written for a state nobody read: %v", err)
	}
}

// TestAResetThatCannotBeReadBackDoesNotSayItConverged is cited by
// resetCommand, and it is the command-level half of the same defect.
//
// The emulator accepts the reset and then answers nothing about the state it
// left behind. The command must refuse with the reason, never print the
// convergence line: that line is what a CI parses.
func TestAResetThatCannotBeReadBackDoesNotSayItConverged(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/_feint/reset/") {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer silent.Close()
	addr := strings.TrimPrefix(silent.URL, "http://")

	// The recorded state holds nothing, so the old code compared the failed
	// read against it and found no difference at all.
	if err := writeRecordedCheckpoint(addr, recordedCheckpoint{
		SchemaVersion: checkpointStateVersion, Name: "baseline",
	}); err != nil {
		t.Fatalf("record a checkpoint: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := resetCommand([]string{"baseline", "--addr", addr}, &stdout, &stderr)
	if code != exitError {
		t.Fatalf("a reset whose state could not be read back exited %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "converged") {
		t.Fatalf("the command reported convergence it never observed: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "could not be read back") {
		t.Errorf("the refusal does not say what failed: %s", stderr.String())
	}
}
