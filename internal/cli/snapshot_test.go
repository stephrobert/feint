package cli_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What is guarded here is a name becoming a path.
//
// A snapshot name is joined onto a directory this tool owns. A name carrying a
// separator or a parent reference writes outside it — over a file the operator
// never named — and `rm` would then delete it. The refusal is not a nicety, and
// it has to hold for every verb that turns a name into a path.
func TestSnapshotRefusesANameThatIsAPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", "")

	for _, name := range []string{"../escape", "sub/dir", "..", ".", `back\slash`} {
		for _, verb := range []string{"save", "load", "rm"} {
			code, out, errOut := run("snapshot", verb, name)
			if code == 0 {
				t.Fatalf("snapshot %s %q exited 0, want a refusal: %s%s", verb, name, out, errOut)
			}
			if !strings.Contains(errOut, "is not a snapshot name") {
				t.Fatalf("snapshot %s %q refused for the wrong reason: %s", verb, name, errOut)
			}
		}
	}
}

// A verb that needs a name and gets none must say so rather than act on
// whatever the first flag happens to be.
func TestSnapshotNeedsANameBeforeItsFlags(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", "")

	for _, args := range [][]string{
		{"snapshot"},
		{"snapshot", "save"},
		{"snapshot", "save", "--addr", "127.0.0.1:4599"},
		{"snapshot", "nonsense"},
	} {
		code, _, errOut := run(args...)
		if code != 1 {
			t.Fatalf("%v exited %d, want 1", args, code)
		}
		if errOut == "" {
			t.Fatalf("%v failed without saying why", args)
		}
	}
}

// The round trip, against a server that answers the way the emulator does.
//
// This is the part no unit test of a helper would catch: that save writes what
// the endpoint returned, that load sends it back unchanged, and that list counts
// the entries of the file rather than trusting whoever wrote it.
func TestSnapshotRoundTrip(t *testing.T) {
	state := `{"format":"feint-snapshot","version":1,"resources":[{"id":"a","kind":"ip"},{"id":"b","kind":"server"}]}`
	var received string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_feint/state" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(state))
		case http.MethodPut:
			// io.ReadAll, not a single Read into a ContentLength-sized buffer:
			// one Read is allowed to return fewer bytes than asked for, so that
			// version worked by luck on a small fixture and would have gone
			// intermittent the day one grew.
			body, _ := io.ReadAll(r.Body)
			received = string(body)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "resources": 2})
		}
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", "")

	if code, out, errOut := run("snapshot", "save", "fixture", "--addr", addr); code != 0 {
		t.Fatalf("save exited %d: %s%s", code, out, errOut)
	}

	saved, err := os.ReadFile(filepath.Join(home, "feint", "snapshots", "fixture.json"))
	if err != nil {
		t.Fatalf("the snapshot was not written where list looks for it: %v", err)
	}
	if string(saved) != state {
		t.Fatalf("saved bytes differ from what the emulator returned:\n%s", saved)
	}

	code, out, errOut := run("snapshot", "list")
	if code != 0 {
		t.Fatalf("list exited %d: %s", code, errOut)
	}
	if !strings.Contains(out, "fixture") || !strings.Contains(out, "2") {
		t.Fatalf("list does not report the snapshot and its count:\n%s", out)
	}

	if code, _, errOut := run("snapshot", "load", "fixture", "--addr", addr); code != 0 {
		t.Fatalf("load exited %d: %s", code, errOut)
	}
	if received != state {
		t.Fatalf("load sent something other than what save wrote:\n%s", received)
	}

	if code, _, errOut := run("snapshot", "rm", "fixture"); code != 0 {
		t.Fatalf("rm exited %d: %s", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(home, "feint", "snapshots", "fixture.json")); !os.IsNotExist(err) {
		t.Fatal("rm left the file behind")
	}
}

// Saving over an existing snapshot without being asked to would destroy the
// fixture a suite depends on, and the caller would learn it from the test that
// fails afterwards.
func TestSnapshotSaveRefusesToOverwriteWithoutForce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"format":"feint-snapshot","version":1,"resources":[{"id":"a"}]}`))
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", "")

	if code, _, errOut := run("snapshot", "save", "fixture", "--addr", addr); code != 0 {
		t.Fatalf("the first save exited %d: %s", code, errOut)
	}
	code, _, errOut := run("snapshot", "save", "fixture", "--addr", addr)
	if code == 0 {
		t.Fatal("the second save overwrote silently")
	}
	if !strings.Contains(errOut, "--force") {
		t.Fatalf("the refusal does not say how to proceed: %s", errOut)
	}
	if code, _, errOut := run("snapshot", "save", "fixture", "--addr", addr, "--force"); code != 0 {
		t.Fatalf("--force was refused: %d %s", code, errOut)
	}
}

// The failure everybody hits first: no emulator is running. "connection
// refused" alone does not say which command starts one.
func TestSnapshotSaysHowToStartAnEmulatorWhenNoneAnswers(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", "")

	code, _, errOut := run("snapshot", "save", "fixture", "--addr", "127.0.0.1:1")
	if code != 1 {
		t.Fatalf("exited %d, want 1", code)
	}
	if !strings.Contains(errOut, "feint start") {
		t.Fatalf("the error does not say what to do about it: %s", errOut)
	}
}
