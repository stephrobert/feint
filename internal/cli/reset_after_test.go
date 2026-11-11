package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// `feint run --reset-after` (#853's crash behaviour), in the parts a unit test
// can reach.
//
// What it asks for is a reset attempted on command success, command failure
// and a relayed signal, and it concedes the fourth case in the same breath: a
// hard kill cannot promise anything. So these tests hold the decisions, and
// the three ways out are driven against the built binary where the signal is
// real.

// TestResetAfterAndKeepOnFailureAreRefusedTogether is cited by runCommand.
//
// Two flags that mean opposite things about a failed run: one erases the
// state, the other keeps it for inspection. Refused rather than silently
// ordered, because either order is wrong for somebody and a reader cannot tell
// which they got.
func TestResetAfterAndKeepOnFailureAreRefusedTogether(t *testing.T) {
	server := reportServer(t, door("scaleway", "fr-par"))
	var stdout, stderr bytes.Buffer
	code := runCommand([]string{
		"--endpoint", server.URL, "--reset-after", "--keep-on-failure", "--", "true",
	}, &stdout, &stderr)
	if code != exitError {
		t.Fatalf("both flags together were accepted (%d)", code)
	}
	for _, want := range []string{"--reset-after", "--keep-on-failure", "pick one"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the refusal does not mention %q: %s", want, stderr.String())
		}
	}

	// And each alone is not refused, so this cannot be satisfied by refusing
	// the pair's members.
	for _, flag := range []string{"--reset-after", "--keep-on-failure"} {
		stdout.Reset()
		stderr.Reset()
		if code := runCommand([]string{
			"--endpoint", server.URL, flag, "--", "true",
		}, &stdout, &stderr); code == exitError && strings.Contains(stderr.String(), "pick one") {
			t.Errorf("%s alone was refused as a pair: %s", flag, stderr.String())
		}
	}
}

// TestAFailedResetAfterIsReportedAndNotRaised is cited by resetToSession.
//
// The child has already run when a reset is attempted, and its exit code is
// what a caller builds on. A reset that could not be attempted must be
// reported rather than swallowed, and rather than raised into the child's
// verdict where it would read as a failing test. The four words are not
// interchangeable: they send a reader to four different places.
func TestAFailedResetAfterIsReportedAndNotRaised(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	const addr = "127.0.0.1:4599"

	t.Run("no session checkpoint recorded", func(t *testing.T) {
		var stderr bytes.Buffer
		verdict, left := resetToSession("http://"+addr, "feint-lab-1", addr, &stderr)
		if verdict != "not attempted" {
			t.Errorf("verdict %q", verdict)
		}
		if len(left) != 0 {
			t.Errorf("leftovers %v for a reset that never ran", left)
		}
		if stderr.Len() == 0 {
			t.Error("nothing was reported")
		}
	})

	t.Run("the emulator refuses the reset", func(t *testing.T) {
		refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"the control plane could not be restored"}`))
		}))
		defer refusing.Close()
		if err := writeRecordedCheckpoint(addr, recordedCheckpoint{
			SchemaVersion: checkpointStateVersion, Name: "feint-lab-1",
		}); err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		verdict, _ := resetToSession(refusing.URL, "feint-lab-1", addr, &stderr)
		if verdict != "failed" {
			t.Errorf("verdict %q", verdict)
		}
		// The emulator's own words, not a summary of them: a reset refused
		// because the store could not be restored and one refused because the
		// bytes could not be are different problems.
		if !strings.Contains(stderr.String(), "control plane could not be restored") {
			t.Errorf("the emulator's reason was dropped: %s", stderr.String())
		}
	})

	t.Run("a session that is not this run's is not reset", func(t *testing.T) {
		// Two runs against one shared emulator must not reset each other's
		// state. Since the audit, records are indexed by name, so the
		// isolation is the lookup itself: a run asks for ITS session and
		// finds nothing when only another run's was recorded.
		//
		// A fresh runtime directory, because the subtests above recorded
		// their own sessions here and the first version of this one found
		// one of them — the test passing for the wrong reason, which is what
		// the per-name index changed underneath it.
		t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
		if err := writeRecordedCheckpoint(addr, recordedCheckpoint{
			SchemaVersion: checkpointStateVersion, Name: "feint-lab-OTHER",
		}); err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		verdict, _ := resetToSession("http://"+addr, "feint-lab-1", addr, &stderr)
		if verdict != "not attempted" {
			t.Errorf("verdict %q for another run's session", verdict)
		}
		// And the other run's record is untouched: this one did not reset it
		// and did not delete it either.
		if back, err := readRecordedCheckpoint(addr, "feint-lab-OTHER"); err != nil || back.Name != "feint-lab-OTHER" {
			t.Errorf("another run's record was disturbed: %+v %v", back, err)
		}
	})
}

// TestASessionCheckpointIsNamedAfterItsRun is cited by takeSessionCheckpoint.
//
// The name is the lab's own session identity and not one the operator typed,
// so that two runs against one shared emulator cannot reset each other's
// state. A fixed name would let them, and the failure would look like a
// flaky test rather than a collision.
func TestASessionCheckpointIsNamedAfterItsRun(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	const addr = "127.0.0.1:4599"

	var asked []string
	lab := checkpointingLab(t, &asked)
	if err := takeSessionCheckpoint(lab.URL, "feint-lab-4021", addr); err != nil {
		t.Fatalf("take the session checkpoint: %v", err)
	}
	if len(asked) != 1 || asked[0] != "/_feint/checkpoint/feint-lab-4021" {
		t.Fatalf("the emulator was asked for %v", asked)
	}
	recorded, err := readRecordedCheckpoint(addr, "")
	if err != nil {
		t.Fatalf("read the record back: %v", err)
	}
	if recorded.Name != "feint-lab-4021" {
		t.Errorf("the record names %q", recorded.Name)
	}
	if recorded.Taken == "" {
		t.Error("the record carries no time, so two runs cannot be told apart in a log")
	}
}

// checkpointingLab answers the endpoints takeSessionCheckpoint drives, and
// records which paths were asked for.
func checkpointingLab(t *testing.T, asked *[]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/_feint/checkpoint/", func(w http.ResponseWriter, r *http.Request) {
		*asked = append(*asked, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":1,"name":"x","resources":0,"bytes":false}`))
	})
	mux.HandleFunc("/_feint/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"machines":"none","capabilities":null,"enforced":{}}`))
	})
	mux.HandleFunc("/_feint/resources", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"count":0,"resources":[]}`))
	})
	mux.HandleFunc("/_feint/s3", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"schema_version":2,"enabled":false,"doors":[],"http_requests":{}}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestARunWhoseResetDidNotConvergeFails is cited by runCommand's exit code.
//
// The expensive case, and the one the falsification caught my tests not
// covering: a shared emulator left dirty by a run that reported success. The
// caller asked for the lab to be left as found, and a quiet 0 is how the next
// test inherits the mess and blames itself.
//
// The child passes on purpose. A reset's failure must not be hidden behind a
// failing test, and it must not be hidden behind a passing one either.
func TestARunWhoseResetDidNotConvergeFails(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	// A lab that answers one more resource after the reset than it did at the
	// checkpoint: the reset ran, the emulator said ok, and the state did not
	// come back. That is exactly the shape #853 refuses to call a success.
	var resourceReads int
	mux := http.NewServeMux()
	mux.HandleFunc("/_feint/resources", func(w http.ResponseWriter, _ *http.Request) {
		resourceReads++
		if resourceReads == 1 {
			_, _ = w.Write([]byte(`{"count":0,"resources":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"count":1,"resources":[` +
			`{"id":"leftover","kind":"instance/server","state":"running"}]}`))
	})
	mux.HandleFunc("/_feint/checkpoint/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"schema_version":1,"name":"x","resources":0,"bytes":false}`))
	})
	mux.HandleFunc("/_feint/reset/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"schema_version":1,"name":"x","resources":0,` +
			`"destroyed":[],"skipped":[],"recreated":[],"recreates":false}`))
	})
	mux.HandleFunc("/_feint/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"machines":"none","capabilities":null,"enforced":{}}`))
	})
	mux.HandleFunc("/_feint/trace", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"schema_version":1,"count":0,"kept":256,"exchanges":[]}`))
	})
	mux.HandleFunc("/_feint/s3", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"schema_version":2,"enabled":true,` +
			`"max_blob_bytes":1073741824,"max_object_bytes":268435456,` +
			`"http_requests":{"GET":1},"doors":[{"provider":"scaleway","region":"fr-par",` +
			`"path":"/_feint/s3/scaleway/fr-par/"}]}`))
	})
	mux.HandleFunc("/_feint/s3/scaleway/fr-par/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<ListAllMyBucketsResult><Buckets></Buckets></ListAllMyBucketsResult>"))
	})
	lab := httptest.NewServer(mux)
	defer lab.Close()

	var stdout, stderr bytes.Buffer
	code := runCommand([]string{
		"--endpoint", lab.URL, "--reset-after", "--json", "-", "--", "true",
	}, &stdout, &stderr)
	if code == exitOK {
		t.Fatalf("a run whose reset left a resource behind reported success\nstdout:%s\nstderr:%s",
			stdout.String(), stderr.String())
	}

	// And the receipt says which it was, because "the test failed" and "the
	// lab was left dirty" send a reader to two different places.
	var receipt runReceipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
		t.Fatalf("stdout is not the receipt alone: %v\n%s", err, stdout.String())
	}
	if receipt.Reset != "did not converge" {
		t.Errorf("the receipt reports reset=%q", receipt.Reset)
	}
	if receipt.Exit != exitOK {
		t.Errorf("the receipt blames the child, which passed: exit=%d", receipt.Exit)
	}
	if len(receipt.Unreconciled) == 0 {
		t.Error("the receipt names nothing it could not reconcile, which is what #853 asks for")
	}
}
