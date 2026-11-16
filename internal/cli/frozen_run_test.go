package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/s3"
	"github.com/stephrobert/feint/internal/transcript"
)

// The `feint run` receipt is the eighth frozen surface (#918).
//
// It is built from a real `feint run`: runCommand, the function the binary
// dispatches to, driven in process against a real emulator — the same one the
// seven other surfaces are read from, built the same way. What cannot happen
// in process is an OWNED lab, because `start` re-execs os.Executable(), which
// under `go test` is the test binary. So the run attaches with --endpoint, and
// everything after the spawn — the campaign, the child, the fault state, the
// reset, the receipt — is the code a user runs.
//
// The traffic is driven by this test while the child waits, not by the child.
// The child is a POSIX shell that waits for a file; the receipt reads the LAB,
// whoever called it, so the shape is the same, and the alternative (a client
// binary on the station's PATH) would make the fixture depend on what is
// installed. Measured: the hang line below must still be in flight when
// readFaultState runs, and a connection opened by the child would close when
// the child exits.
//
// The population is the point. Every key the receipt can carry must be in it
// with a value, or a rename of that key is invisible to both gates: #851 froze
// `fired[].transport` as a name with no contents and stayed green through a
// deliberate rename. TestTheRunReceiptPopulationHoldsEveryKey asserts it, by
// walking the receipt's own type, so a key added later without a population
// fails here rather than being frozen as absent.

// frozenRunReceipt runs `feint run` once per test binary and keeps the JSON
// receipt it wrote.
var frozenRunReceipt = func() func(t *testing.T) []byte {
	var once sync.Once
	var receipt []byte
	var buildErr error
	return func(t *testing.T) []byte {
		t.Helper()
		once.Do(func() {
			// The session checkpoint --reset-after takes is written under the
			// instance directory; a directory of this test's own keeps it off
			// the operator's.
			state := privateStorageDir(t)
			t.Setenv("XDG_RUNTIME_DIR", state)
			t.Setenv("XDG_STATE_HOME", state)
			receipt, buildErr = runForTheFrozenReceipt(t)
		})
		if buildErr != nil {
			t.Fatalf("drive `feint run` for the frozen receipt: %v", buildErr)
		}
		return receipt
	}
}()

// runForTheFrozenReceipt is the population, step by step.
func runForTheFrozenReceipt(t *testing.T) ([]byte, error) {
	t.Helper()
	// A runtime that declares every capability, so `capabilities` and
	// `runtime_proof` carry elements. Under the default no-op driver both are
	// empty arrays, and an empty array freezes a key with no element type.
	env := emulator.DefaultEnv()
	env.UseMachines(machine.Use(machine.NewRecorder()))
	packs, err := packsFor(env)
	if err != nil {
		return nil, err
	}
	srv, err := emulator.NewServer(env, packs...)
	if err != nil {
		return nil, err
	}
	// The inventory --reset-after compares rides with the page, which `serve`
	// mounts on a loopback address and only there. Without it the run's
	// checkpoint cannot read the lab (measured: the first version of this test
	// built the server without it, and the reset reported "converged").
	if !srv.MountUI(emulator.UI{Addr: "127.0.0.1:4599"}) {
		return nil, fmt.Errorf("the page and its inventory did not mount on a loopback address")
	}
	// Byte doors, so the run has something to intercept: `intercepted` is
	// empty without them.
	flags := &objectStorageFlags{root: filepath.Join(privateStorageDir(t), "bytes"),
		budgetMiB: 3, objectMiB: 1, doors: defaultS3Doors}
	doors, store, err := mountObjectStorage(t.Context(), srv, flags, env.Log)
	if err != nil {
		return nil, err
	}
	defer func() { _ = store.Close() }()
	ts := httptest.NewServer(unreconcilable(doors))
	defer ts.Close()

	work := t.TempDir()
	campaign := filepath.Join(work, "campaign.json")
	// Three rules, each for one optional key of `faults[]`: a status rule
	// with no bound, fired past the receipt's cap so `faults_dropped` is not
	// zero; a close rule for `transport`; a hang held open across the read
	// for `in_flight`.
	if err := os.WriteFile(campaign, []byte(`{"faults":[`+
		`{"operation":"instance/v1/API.GetServer","status":503},`+
		`{"operation":"instance/v1/API.ListIPs","transport":"close"},`+
		`{"operation":"instance/v1/API.ListSecurityGroups","transport":"hang"}`+
		`]}`), 0o600); err != nil {
		return nil, err
	}
	release := filepath.Join(work, "release")
	receiptPath := filepath.Join(work, "receipt.json")

	traffic := make(chan error, 1)
	var held net.Conn
	go func() {
		// The child waits on this file; it is created on every path out of
		// here, so a failed drive ends the child instead of hanging the run.
		defer func() { _ = os.WriteFile(release, nil, 0o600) }()
		conn, err := driveTheFrozenTraffic(ts)
		held = conn
		traffic <- err
	}()

	var stdout, stderr bytes.Buffer
	code := runCommand([]string{
		"--endpoint", ts.URL, "--reset-after", "--fault", campaign, "--json", receiptPath, "--",
		"sh", "-c", `while [ ! -e "$1" ]; do sleep 0.05; done`, "sh", release,
	}, &stdout, &stderr)
	if err := <-traffic; err != nil {
		return nil, fmt.Errorf("the traffic: %w\nstderr: %s", err, stderr.String())
	}
	if held != nil {
		defer func() { _ = held.Close() }()
	}
	raw, err := os.ReadFile(receiptPath) //nolint:gosec // a path this test made
	if err != nil {
		return nil, fmt.Errorf("the run wrote no receipt (exit %d): %w\nstderr: %s", code, err, stderr.String())
	}
	return raw, nil
}

// driveTheFrozenTraffic waits for the campaign to be armed, then fires each
// rule, and returns the connection the hang rule holds.
func driveTheFrozenTraffic(ts *httptest.Server) (net.Conn, error) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		document, err := callFaults(http.MethodGet, ts.URL+"/_feint/faults", nil)
		if err != nil {
			return nil, err
		}
		if len(document.Faults) == 3 {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the campaign was not armed after 10s: %+v", document.Faults)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The close rule's error IS the rule firing, so an answer is the failure.
	if res, err := ts.Client().Get(ts.URL + "/instance/v1/zones/fr-par-1/ips"); err == nil {
		_ = res.Body.Close()
		return nil, fmt.Errorf("the close rule answered %d instead of dropping the connection", res.StatusCode)
	}
	held, err := net.DialTimeout("tcp", ts.Listener.Addr().String(), 5*time.Second)
	if err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(held,
		"GET /instance/v1/zones/fr-par-1/security_groups HTTP/1.1\r\nHost: feint\r\n\r\n"); err != nil {
		return held, err
	}
	if err := waitForAHeldLine(ts); err != nil {
		return held, err
	}
	// Past the receipt's cap, so the emulator counts what it could not hold.
	for range 260 {
		res, err := ts.Client().Get(ts.URL + "/instance/v1/zones/fr-par-1/servers/11111111-1111-1111-1111-111111111111")
		if err != nil {
			return held, err
		}
		_ = res.Body.Close()
	}
	// A byte written during the run and not in the session checkpoint, through
	// the door the run intercepts.
	req, err := http.NewRequest(http.MethodPut, ts.URL+s3.DoorPath("scaleway", "fr-par")+"frozen-bucket", nil)
	if err != nil {
		return held, err
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		return held, err
	}
	_ = res.Body.Close()
	return held, nil
}

// unreconcilable is the one interposition on the population, and it exists
// for one key: `unreconciled`.
//
// A reset that does not converge is, in practice, a runtime object the reset
// could not bring back — a machine still running, a network left behind. An
// in-process lab has no runtime to leave anything behind, and its store and
// bytes come back exactly (measured: a bucket written during the run was
// reconciled, and the first run of this test reported "converged" with no
// `unreconciled` key at all). So the inventory read AFTER the reset gains one
// resource the checkpoint did not hold, which is the shape
// TestARunWhoseResetDidNotConvergeFails builds by hand. Every other answer,
// including the reset itself, is the emulator's own.
func unreconcilable(next http.Handler) http.Handler {
	var mu sync.Mutex
	reset := false
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/_feint/reset/") {
			mu.Lock()
			reset = true
			mu.Unlock()
		}
		mu.Lock()
		afterReset := reset
		mu.Unlock()
		if !afterReset || r.Method != http.MethodGet || r.URL.Path != "/_feint/resources" {
			next.ServeHTTP(w, r)
			return
		}
		recorded := httptest.NewRecorder()
		next.ServeHTTP(recorded, r)
		var view map[string]any
		if err := json.Unmarshal(recorded.Body.Bytes(), &view); err != nil {
			http.Error(w, "the inventory did not decode: "+err.Error(), http.StatusInternalServerError)
			return
		}
		resources, _ := view["resources"].([]any)
		view["resources"] = append(resources, map[string]any{
			"id": "left-behind-by-the-runtime", "kind": "instance/server", "state": "running",
		})
		view["count"] = len(resources) + 1
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(view)
	})
}

// runReceiptShape is the canonical content of the receipt: its field tree.
func runReceiptShape(t *testing.T) json.RawMessage {
	t.Helper()
	var decoded any
	if err := json.Unmarshal(frozenRunReceipt(t), &decoded); err != nil {
		t.Fatalf("decode the run receipt: %v", err)
	}
	content, err := json.Marshal(map[string]any{"fields": transcript.FieldsOf(decoded)})
	if err != nil {
		t.Fatalf("marshal the run receipt's shape: %v", err)
	}
	return content
}

// TestTheRunReceiptPopulationHoldsEveryKey is what keeps the freeze from going
// blind: every JSON path runReceipt can emit must be observed in the run the
// fixture is built from, with a scalar or an element under it.
//
// It walks the TYPE, not a list. A key added to runReceipt later, with no
// population that sets it, fails here by name — rather than entering the
// fixture as nothing, which is the state in which a rename stays green.
func TestTheRunReceiptPopulationHoldsEveryKey(t *testing.T) {
	var decoded any
	if err := json.Unmarshal(frozenRunReceipt(t), &decoded); err != nil {
		t.Fatalf("decode the run receipt: %v", err)
	}
	// The witness first: a walk that cannot find these finds nothing, and its
	// silence would read as a complete population (measured: a walk that
	// stopped at slices left this test green).
	leaves := jsonLeaves(reflect.TypeFor[runReceipt](), "")
	for _, witness := range []string{"faults[].in_flight", "unreconciled[]", "fault_rules[].hits", "schema_version"} {
		if !slices.Contains(leaves, witness) {
			t.Fatalf("the walk over runReceipt does not reach %q, so it cannot judge the population: %v", witness, leaves)
		}
	}
	observed := map[string]string{}
	for _, f := range transcript.FieldsOf(decoded) {
		observed[f.Path] = f.Type
	}
	var missing []string
	for _, path := range leaves {
		// Presence alone: a null leaf cannot occur, every scalar field is a value
		// and a null slice has no `[]` path, which is what is demanded here.
		if _, seen := observed[path]; !seen {
			missing = append(missing, path)
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		t.Errorf("the frozen run receipt's population holds no value for %s,\n"+
			"so a rename of these keys would leave the frozen gates green. Make the run in "+
			"frozen_run_test.go set them.\nreceipt: %s", strings.Join(missing, ", "), frozenRunReceipt(t))
	}
}

// jsonLeaves lists the paths, in transcript.FieldsOf's grammar, at which a
// value of type typ carries a scalar.
func jsonLeaves(typ reflect.Type, prefix string) []string {
	switch typ.Kind() {
	case reflect.Pointer:
		return jsonLeaves(typ.Elem(), prefix)
	case reflect.Slice, reflect.Array:
		return jsonLeaves(typ.Elem(), prefix+"[]")
	case reflect.Struct:
		var leaves []string
		for field := range typ.Fields() {
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "-" || !field.IsExported() {
				continue
			}
			if name == "" {
				name = field.Name
			}
			path := name
			if prefix != "" {
				path = prefix + "." + name
			}
			leaves = append(leaves, jsonLeaves(field.Type, path)...)
		}
		return leaves
	default:
		return []string{prefix}
	}
}
