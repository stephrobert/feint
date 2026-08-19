package emulator_test

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// TestHealthNamesItsOwnProcess holds /_feint/health to the identity it serves
// under `instance` (#309): the pid must be this process's, the start time must
// parse and must not lie about the future. The field exists because a stale
// emulator on a shared port once answered a probe with the previous build's
// catalogue, and nothing in the answer could say which process produced it —
// this test is what keeps the identity from becoming decoration.
func TestHealthNamesItsOwnProcess(t *testing.T) {
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, stubPack{name: "stub"})
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/_feint/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var health struct {
		Instance *struct {
			PID     int    `json:"pid"`
			Started string `json:"started_at"`
		} `json:"instance"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("health: decode: %v", err)
	}
	if health.Instance == nil {
		t.Fatal("health carries no instance: nothing can tell this emulator from a stale one on the same port")
	}
	if health.Instance.PID != os.Getpid() {
		t.Fatalf("health names pid %d, but this process is %d: a caller comparing pids would refuse its own emulator",
			health.Instance.PID, os.Getpid())
	}
	started, err := time.Parse(time.RFC3339, health.Instance.Started)
	if err != nil {
		t.Fatalf("started_at %q is not RFC3339: %v", health.Instance.Started, err)
	}
	if started.After(time.Now().Add(time.Minute)) {
		t.Fatalf("started_at %s is in the future", health.Instance.Started)
	}
}
