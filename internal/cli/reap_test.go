package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Stale instance directories are collected; live ones are not.
//
// Measured on a development station after one day: fourteen directories under
// XDG_RUNTIME_DIR for two live emulators. `stop` clears the record and leaves
// the directory holding the log — right on its own, since a crash is read after
// the fact — but nothing ever collected them, so the directory stopped being
// readable as an answer to "what is running".
//
// The accepting half is the half that matters here. A reaper that removed
// everything would pass any test that only checks that litter goes, and would
// delete the state of an emulator someone left running overnight. So the live
// case is asserted twice: its directory survives, and so does its log.
func TestCleanReapsOnlyDeadInstances(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", home)
	t.Setenv("XDG_STATE_HOME", "")

	// A live instance has to satisfy alive() on both of its paths, because they
	// are chosen by the operating system rather than by the test: on Linux it
	// compares /proc/<pid>/comm with the recorded binary's base name, and where
	// /proc does not exist — macOS, which is in this project's CI matrix — it
	// falls back to probing the recorded address for this emulator's own health.
	//
	// The first version satisfied only the first, so the stand-in was reaped on
	// macOS and the failure read as a broken reaper. It was a broken fixture:
	// alive() was right both times. So the stand-in now answers health for real,
	// and this test drives whichever path the platform takes.
	ts := httptest.NewServer(healthOnly(t))
	t.Cleanup(ts.Close)
	live := writeInstance(t, ts.Listener.Addr().String(), os.Getpid())
	// A pid nothing holds. 4194303 is above the default pid_max on Linux, so it
	// cannot be allocated to anything while this test runs.
	dead := writeInstance(t, "127.0.0.1:4600", 4194303)
	// A directory with a log and no record: what `stop` leaves behind.
	stopped := filepath.Join(home, "feint", "127.0.0.1_4601")
	mkdirWithLog(t, stopped)
	// Snapshots share this root whenever the two XDG homes coincide, and they
	// are work an operator named on purpose.
	snapshots := filepath.Join(home, "feint", snapshotsDirName)
	mkdirWithLog(t, snapshots)

	reaped, err := reapStaleInstances()
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if reaped != 2 {
		t.Errorf("reaped %d directories, want 2 (the dead record and the stopped leftovers)", reaped)
	}

	for _, gone := range []string{dead, stopped} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s survived the sweep", gone)
		}
	}
	for _, kept := range []string{live, snapshots} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s was removed: %v", kept, err)
		}
		if _, err := os.Stat(filepath.Join(kept, "feint.log")); err != nil {
			t.Errorf("%s lost its log: %v", kept, err)
		}
	}
}

// `feint clean` sweeps the state directories even where no machine runtime can
// be resolved.
//
// --vm off is the default and the majority of runs. With the reap placed after
// the driver is resolved, every one of those users would get an error and no
// sweep at all — the directories would accumulate on exactly the stations that
// never start a machine.
func TestCleanReapsWithoutAMachineRuntime(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", home)
	t.Setenv("XDG_STATE_HOME", "")
	mkdirWithLog(t, filepath.Join(home, "feint", "127.0.0.1_4602"))

	var out bytes.Buffer
	// A runtime name nothing can resolve, which is the point: clean must have
	// swept before it fails.
	err := clean([]string{"--vm", "no-such-runtime"}, &out)
	if err == nil {
		t.Fatal("an unknown runtime was accepted, so this test no longer drives the failing path")
	}
	if !strings.Contains(out.String(), "stale instance record") {
		t.Errorf("clean failed on the runtime without sweeping first; it printed: %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, "feint", "127.0.0.1_4602")); !os.IsNotExist(err) {
		t.Error("the stale directory survived a clean that could not resolve a runtime")
	}
}

// `feint clean --vm off` succeeds. There is no runtime to sweep, which is not a
// failure — and since this command now collects instance records too, the
// operator did work, saw it reported, and used to get exit 1 anyway.
//
// --vm off is the default of `serve` and the majority of runs, so this is the
// common path rather than an edge.
func TestCleanSucceedsWithNoRuntimeToSweep(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", home)
	t.Setenv("XDG_STATE_HOME", "")
	stale := filepath.Join(home, "feint", "127.0.0.1_4603")
	mkdirWithLog(t, stale)

	var out bytes.Buffer
	if err := clean([]string{"--vm", "off"}, &out); err != nil {
		t.Fatalf("clean --vm off reported a failure: %v", err)
	}
	if !strings.Contains(out.String(), "stale instance record") {
		t.Errorf("the sweep was not reported: %q", out.String())
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("the stale directory survived")
	}

	// And a runtime that genuinely cannot be swept still fails, or this would be
	// a guard that turned every error into success.
	if err := clean([]string{"--vm", "no-such-runtime"}, &bytes.Buffer{}); err == nil {
		t.Error("an unknown runtime was accepted")
	}
}

// The sentence tools/conformance/scaleway/network.sh greps for is produced
// whatever the mode.
//
// That suite decides the runtime is clean by matching this string, and it skips
// itself when no machine runtime is configured — which is CI, and which is this
// station. So nothing was checking the contract at all: the string could have
// changed under it and the suite would have gone on reporting SKIP. A grep in a
// shell script that only runs on a developer's machine is not a control.
//
// The literal is written out here rather than shared with clean.go on purpose.
// A constant referenced by both would move in lockstep and prove nothing; this
// has to fail when the message changes, because the reader that matters is a
// script this test cannot import.
func TestCleanAlwaysReportsTheRuntimeVerdict(t *testing.T) {
	const asserted = "nothing was left behind" // tools/conformance/scaleway/network.sh:105

	home := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", home)
	t.Setenv("XDG_STATE_HOME", "")

	var out bytes.Buffer
	if err := clean([]string{"--vm", "off"}, &out); err != nil {
		t.Fatalf("clean --vm off: %v", err)
	}
	if !strings.Contains(out.String(), asserted) {
		t.Errorf("network.sh greps for %q and clean printed %q", asserted, out.String())
	}

	// And it still says it after collecting records, because the two verdicts
	// answer different questions: one is about the host's runtime, the other
	// about this tool's own bookkeeping.
	mkdirWithLog(t, filepath.Join(home, "feint", "127.0.0.1_4604"))
	out.Reset()
	if err := clean([]string{"--vm", "off"}, &out); err != nil {
		t.Fatalf("clean --vm off after a reap: %v", err)
	}
	if !strings.Contains(out.String(), asserted) {
		t.Errorf("collecting a record silenced the runtime verdict: %q", out.String())
	}
}

func writeInstance(t *testing.T, addr string, pid int) string {
	t.Helper()
	dir, err := instanceDir(addr)
	if err != nil {
		t.Fatalf("instance dir: %v", err)
	}
	mkdirWithLog(t, dir)
	raw, err := json.Marshal(Instance{PID: pid, Addr: addr, Binary: os.Args[0]})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "instance.json"), raw, 0o600); err != nil {
		t.Fatalf("write instance: %v", err)
	}
	return dir
}

// healthOnly serves what healthy() demands and nothing more: status and
// providers, both checked, because a bare 200 could come from anything.
//
// A real emulator would do, and is not used on purpose — building one drags the
// packs into a test about directory collection, and a fixture that needs the
// whole product to prove a filesystem sweep is a fixture that will be deleted
// the first time it gets in the way.
func healthOnly(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_feint/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"status":    "ok",
			"providers": []string{"scaleway"},
		}); err != nil {
			t.Errorf("encode health: %v", err)
		}
	})
	return mux
}

func mkdirWithLog(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "feint.log"), []byte("listening\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
}
