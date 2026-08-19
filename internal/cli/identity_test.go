package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// foreignEmulator serves a health payload that identifies as somebody else's
// process. This is the wire shape of the incident #309 reproduced: a stale
// `feint serve` from a previous run, healthy and articulate, on the address a
// fresh run believes it just claimed.
func foreignEmulator(t *testing.T, pid int, started string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_feint/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		payload := map[string]any{
			"status":    "ok",
			"providers": []string{"scaleway"},
		}
		if pid != 0 {
			payload["instance"] = map[string]any{"pid": pid, "started_at": started}
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("encode health: %v", err)
		}
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// selfInstance builds an Instance whose pid is this test process, so that
// alive() reports Alive and awaitHealthy keeps polling instead of declaring
// the child dead. In practice the fake emulator answers the first probe, so
// awaitHealthy returns before consulting alive() at all — the Binary is only
// there for the losing side of that race. Without /proc (macOS is in the CI
// matrix) alive()'s own fallback is the health probe, which the fake answers.
func selfInstance(t *testing.T, addr string) *Instance {
	t.Helper()
	name := "feint"
	if comm, err := os.ReadFile("/proc/self/comm"); err == nil {
		name = strings.TrimSpace(string(comm))
	}
	return &Instance{
		PID:    os.Getpid(),
		Addr:   addr,
		Binary: "/x/" + name,
		Log:    "/dev/null",
	}
}

// TestStartRefusesAForeignAnswerOnItsAddress is the guard #309 exists for.
//
// Reproduced before the fix, 2026-08-19: a stale emulator held 127.0.0.1:4620,
// a second checkout's `feint start` spawned a child that died on the bind
// error, and awaitHealthy took the stale process's health answer as the
// child's — `start` printed "listening (pid N)" about a dead pid, `wait` said
// ready, and the suites measured the previous build's catalogue. The answer
// must be matched against the process this command spawned, and a mismatch
// must be a refusal, not a success.
func TestStartRefusesAForeignAnswerOnItsAddress(t *testing.T) {
	inst := selfInstance(t, "")
	foreignPID := inst.PID + 1
	ts := foreignEmulator(t, foreignPID, "2026-08-19T08:00:00Z")
	inst.Addr = strings.TrimPrefix(ts.URL, "http://")

	err := awaitHealthy(inst, 3*time.Second)
	if err == nil {
		t.Fatal("a foreign emulator answered on the address and awaitHealthy accepted it as the child it was waiting for")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("pid %d", foreignPID)) {
		t.Fatalf("the refusal does not name the process that answered: %v", err)
	}
}

// TestStartRefusesAnAnswerThatCarriesNoIdentity covers the other stale shape:
// an emulator built before schema_version 3 answers health without `instance`.
// Whatever it serves, it is not the binary this checkout just built, and
// accepting it would be the same wrong measurement with an older accomplice.
func TestStartRefusesAnAnswerThatCarriesNoIdentity(t *testing.T) {
	inst := selfInstance(t, "")
	ts := foreignEmulator(t, 0, "")
	inst.Addr = strings.TrimPrefix(ts.URL, "http://")

	err := awaitHealthy(inst, 3*time.Second)
	if err == nil {
		t.Fatal("an identity-less health answer was accepted as the child this command started")
	}
	if !strings.Contains(err.Error(), "predates identity") {
		t.Fatalf("the refusal does not say what answered: %v", err)
	}
}

// TestStartAcceptsTheEmulatorItStarted is the half that keeps the guard from
// being noise: when the answering process is the one this command spawned, the
// wait succeeds. A guard that fires on a legitimate run teaches people to
// disarm it, which is worse than no guard — tools/falsify/falsify.py carries
// that reasoning for the same repository.
func TestStartAcceptsTheEmulatorItStarted(t *testing.T) {
	inst := selfInstance(t, "")
	ts := foreignEmulator(t, inst.PID, "2026-08-19T08:00:00Z")
	inst.Addr = strings.TrimPrefix(ts.URL, "http://")

	if err := awaitHealthy(inst, 3*time.Second); err != nil {
		t.Fatalf("awaitHealthy refused the very emulator it was waiting for: %v", err)
	}
}

// TestServeRefusesAnAddressAnotherEmulatorClaims holds the second door: a
// foreground `serve` (or the detached child of `start`) must refuse an address
// where an emulator already answers, naming the incumbent, rather than dying
// on a bind error a wrapper reads past. Only the decision is tested, for the
// reason checkListenAddr documents: with the refusal removed, serve serves,
// and a test driving it would hang instead of failing.
func TestServeRefusesAnAddressAnotherEmulatorClaims(t *testing.T) {
	ts := foreignEmulator(t, 4242, "2026-08-19T08:00:00Z")
	addr := strings.TrimPrefix(ts.URL, "http://")

	err := checkAddrUnclaimed(addr)
	if err == nil {
		t.Fatal("an address already served by an emulator was declared free")
	}
	if !strings.Contains(err.Error(), "pid 4242") {
		t.Fatalf("the refusal does not name the incumbent: %v", err)
	}

	// The other half: an address nobody answers on stays usable, or every
	// legitimate serve is refused and the guard is disarmed within the week.
	ts.Close()
	if err := checkAddrUnclaimed(addr); err != nil {
		t.Fatalf("a free address was refused: %v", err)
	}
}
