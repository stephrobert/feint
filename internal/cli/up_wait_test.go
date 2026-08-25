package cli

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/environment"
)

// The decision, tested apart from the act.
//
// `up`'s waiting half cannot be driven through Run: spawning the emulator is a
// re-exec of this binary, and in a test that binary is the test binary. The
// lesson is the one go-production-engineer records — separate the decision from
// the act and test the decision — so what is measured here is `waitReady`
// against a server that answers exactly what a real one would, and the whole
// verb is proved end to end by tools/conformance/environment/up.sh and by the
// three example stacks.

// declFor builds a declaration pointed at a test server.
func declFor(t *testing.T, addr string, ready ...string) *environment.File {
	t.Helper()
	src := "version: 1\nemulator:\n  addr: " + addr + "\nready:\n"
	for _, item := range ready {
		src += "  - " + item + "\n"
	}
	decl, err := environment.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return decl
}

// A condition that is met is said out loud both ways: announced before the wait
// and confirmed after it. A silent wait is indistinguishable from a hang.
func TestAReadyConditionThatIsMetIsSaidOutLoudBothWays(t *testing.T) {
	inventory := `{"count":2,"resources":[{"kind":"instance/server"},{"kind":"instance/server"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_feint/resources" {
			_, _ = w.Write([]byte(inventory))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	decl := declFor(t, strings.TrimPrefix(srv.URL, "http://"),
		"http:/instance/v1/zones/fr-par-1/servers", "resource:instance/server:2")
	var out bytes.Buffer
	if err := waitReady(decl, 5*time.Second, &out); err != nil {
		t.Fatalf("conditions the server satisfies were not met: %v\n%s", err, out.String())
	}
	for _, want := range []string{"waiting:", "ok: http:/instance", "ok: resource:instance/server:2"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the wait never printed %q:\n%s", want, out.String())
		}
	}
}

// The other direction, and the one #190 asks for by name: a condition that
// cannot be met fails **within its deadline**, naming what it was waiting for.
// It must not hang, because a hang with no output reads as a broken emulator
// and blocks everything behind it.
func TestAReadyConditionThatCannotBeMetFailsWithinItsDeadlineNamingIt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_feint/resources" {
			_, _ = w.Write([]byte(`{"count":0,"resources":[]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	decl := declFor(t, strings.TrimPrefix(srv.URL, "http://"), "resource:instance/server:1")
	var out bytes.Buffer
	started := time.Now()
	err := waitReady(decl, 300*time.Millisecond, &out)
	if err == nil {
		t.Fatal("a condition nothing satisfies was reported met")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("the wait took %s for a 300ms deadline: it is hanging, not failing", elapsed)
	}
	if !strings.Contains(err.Error(), "resource:instance/server:1") {
		t.Errorf("the failure never names the condition: %v", err)
	}
	// And it says how far it got, which is the difference between "not yet" and
	// "the emulator is broken".
	if !strings.Contains(err.Error(), "the emulator holds 0") {
		t.Errorf("the failure never says what it saw: %v", err)
	}
}

// Three outcomes, never two. An inventory that cannot be read is a failure with
// a reason, never a count of zero: a silent zero reads exactly like an emulator
// nobody has used yet, which is the family measurement-integrity names first.
func TestAnUnreadableInventoryIsAnErrorAndNeverAZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("this is not JSON"))
	}))
	defer srv.Close()

	_, err := countKindInInventory(srv.URL, "instance/server")
	if err == nil {
		t.Fatal("an unreadable inventory was read as a count, and a wrong count reads exactly like a right one")
	}
	// The witness: the same reader finds what is there when the body is good.
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"count":1,"resources":[{"kind":"instance/server"}]}`))
	}))
	defer good.Close()
	held, err := countKindInInventory(good.URL, "instance/server")
	if err != nil || held != 1 {
		t.Fatalf("the reader found %d, %v on an inventory holding one", held, err)
	}
}

// The declaration renders into the flags `start` already takes, one direction
// only: a field that named no flag would be a second source for something the
// binary already decides.
func TestTheDeclarationRendersIntoTheFlagsStartAlreadyTakes(t *testing.T) {
	decl, err := environment.Parse("version: 1\nemulator:\n  addr: 127.0.0.1:4610\n" +
		"  state: state.json\n  contracts: contracts\n  log_level: debug\n  cleanup: true\n" +
		"runtime:\n  mode: incus\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := strings.Join(startArgs(decl), " ")
	for _, want := range []string{
		"--addr 127.0.0.1:4610", "--vm incus", "--log-level debug",
		"--state state.json", "--contracts contracts", "--cleanup",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("start would not receive %q: %s", want, got)
		}
	}
	// The flags `start` refuses are never rendered, because the schema refuses
	// them at load and there is nothing here that could invent one.
	for _, never := range []string{"--coverage", "--shapes", "--expose-to-network"} {
		if strings.Contains(got, never) {
			t.Errorf("start refuses %s and up rendered it: %s", never, got)
		}
	}
}

// The engine's environment comes from the pack rather than from a field, which
// is what keeps the endpoint form — Exoscale carries its /v2 path inside the
// value, Scaleway does not — a fact with one owner.
func TestTheEngineGetsThePacksOwnEnvironmentAndTheDeclaredVariables(t *testing.T) {
	for _, tc := range []struct {
		provider string
		wants    string
	}{
		{"scaleway", "SCW_API_URL="},
		{"exoscale", "EXOSCALE_API_ENDPOINT="},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			decl, err := environment.Parse("version: 1\ncloud:\n  provider: " + tc.provider +
				"\nemulator:\n  addr: 127.0.0.1:4611\niac:\n  engine: terraform\n  vars:\n    endpoint: ${feint.endpoint}\n")
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			env, err := engineEnvironment(decl)
			if err != nil {
				t.Fatalf("engineEnvironment: %v", err)
			}
			joined := strings.Join(env, "\n")
			if !strings.Contains(joined, tc.wants) {
				t.Errorf("the engine never receives %s: the pack's own exports are missing", tc.wants)
			}
			// TF_VAR_ rather than -var, and the value carries the endpoint the
			// declaration wrote once.
			if !strings.Contains(joined, "TF_VAR_endpoint=http://127.0.0.1:4611") {
				t.Errorf("the endpoint variable never reached the engine:\n%s", joined)
			}
			if !strings.Contains(joined, "TF_INPUT=0") {
				t.Error("the engine could open a prompt on a stdin nothing is watching")
			}
		})
	}
}

// The refusal that costs the most when it comes late, and the property that
// makes it a refusal rather than a message: **nothing was started**.
//
// That distinction is not theoretical. The first version of this test named a
// runtime mode instead of a provider, and it stayed green with the preflight
// call removed — twice over. The schema refuses an unknown mode at load, so
// preflight never saw it; and even where it did, `start` spawned a child, the
// child died on the same mode, and the refusal arrived from the wrong side
// looking identical. Two different ways of measuring the wrong subject, both
// found by the falsification run rather than by reading.
//
// So the subject here is a provider this binary does not serve — a preflight
// question, host-independent, at the same call site as the runtime check — and
// the evidence is the log file a spawn creates before its child can even fail.
// The runtime check is proven by the same mutation, because it is the same call.
//
// TestUpRefusesBeforeStartingAnything fails when the preflight call is removed
// from up.
func TestUpRefusesBeforeStartingAnything(t *testing.T) {
	// A free port, so the instance directory read below belongs to this run and
	// to no other: an assertion about a shared path is only as good as the
	// guarantee that nobody else is using it.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free port: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release the port: %v", err)
	}
	dir, err := instanceDir(addr)
	if err != nil {
		t.Fatalf("instance directory: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
		// Under the mutation this verb really does start something, and a child
		// left holding a port is how one test starts reporting another's
		// timing.
		var discard strings.Builder
		stop([]string{"--addr", addr}, &discard, &discard)
	})

	work := t.TempDir()
	declaration := "version: 1\ncloud:\n  provider: azure\nemulator:\n  addr: " + addr + "\n"
	if err := os.WriteFile(filepath.Join(work, environment.DefaultFile), []byte(declaration), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)

	var out, errOut bytes.Buffer
	if code := Run([]string{"feint", "up"}, &out, &errOut); code != exitError {
		t.Fatalf("exited %d, want %d: a provider this binary does not serve must be refused", code, exitError)
	}
	// Named, and with the list: a refusal that does not say what is available
	// leaves the reader guessing.
	if !strings.Contains(errOut.String(), "azure") || !strings.Contains(errOut.String(), "scaleway") {
		t.Errorf("the refusal names neither the provider asked for nor the ones served: %q", errOut.String())
	}
	// The property. `spawn` creates the instance directory and its log before
	// the child can fail, so the log's absence is the evidence that the refusal
	// came first.
	if _, err := os.Stat(filepath.Join(dir, "feint.log")); err == nil {
		t.Errorf("up spawned an emulator on %s before refusing what it could see beforehand", addr)
	}
}

// The accepting half, without which a preflight that refused everything would
// pass the test above and serve nobody: a declaration this host can deliver
// gets past every check preflight makes.
func TestPreflightPassesADeclarationThisHostCanDeliver(t *testing.T) {
	decl, err := environment.Parse("version: 1\ncloud:\n  provider: scaleway\nruntime:\n  mode: off\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var out bytes.Buffer
	if err := preflight(decl, true, &out); err != nil {
		t.Fatalf("preflight refused a declaration nothing is wrong with: %v", err)
	}
}
