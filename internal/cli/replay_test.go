package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/proxy"
	"github.com/stephrobert/feint/internal/replay"
	"github.com/stephrobert/feint/internal/shape"
)

// The identity case, and #73 puts it first for a reason: if replay cannot agree
// with the emulator about the emulator, no claim it makes about a real cloud is
// worth reading.
//
// The recording is made the way an operator makes one — through `feint proxy`,
// pointed at an emulator — and it is replayed against a *second, empty*
// emulator. That second half is what makes it a test rather than a tautology:
// none of the identifiers the recording carries exists in the instance being
// replayed against, so a run with zero divergences is only reachable because
// the replay rebinds them.
func TestATranscriptOfThisEmulatorReplaysAgainstAFreshOneWithoutDivergence(t *testing.T) {
	recording := recordAgainstAFreshEmulator(t)

	target := freshEmulator(t)
	out, errs, code := runReplay(t, recording, "--endpoint", target.URL)
	if code != exitOK {
		t.Fatalf("the identity case exited %d, want 0.\nstdout:\n%s\nstderr:\n%s", code, out, errs)
	}
	if strings.Contains(out, "DIFF") {
		t.Fatalf("a transcript of this emulator diverged against this emulator:\n%s", out)
	}
	if !strings.Contains(out, "instance/v1/API.CreateServer") {
		t.Fatalf("the recording did not exercise the create it is built around:\n%s", out)
	}
	if strings.Contains(out, "0 recorded identifier(s) rebound") {
		t.Fatalf("nothing was rebound, so the replay addressed the recording's own identifiers "+
			"and this proves nothing about a fresh instance:\n%s", out)
	}
}

// The mutation test, #73's second criterion: one recorded response is altered in
// one place and the replay names that field and that operation, with exit 2.
//
// Held against the *same* recording as the identity case above, so the only
// difference between a clean run and this one is the mutation.
func TestAMutatedRecordedResponseIsNamedAndExitsTwo(t *testing.T) {
	recording := recordAgainstAFreshEmulator(t)
	mutate(t, recording, `"public_ip"`, `"publicIp"`)

	target := freshEmulator(t)
	out, errs, code := runReplay(t, recording, "--endpoint", target.URL)
	if code != exitDrift {
		t.Fatalf("a mutated recording exited %d, want %d (drift).\nstdout:\n%s\nstderr:\n%s",
			code, exitDrift, out, errs)
	}
	if !strings.Contains(out, "publicIp") {
		t.Fatalf("the run does not name the mutated field:\n%s", out)
	}
	if !strings.Contains(out, "instance/v1/API.CreateServer") {
		t.Fatalf("the run does not name the operation the mutation is in:\n%s", out)
	}
}

// An operation this emulator does not serve is reported and does not fail the
// run. Asserted explicitly, because the day it fails the run is the day somebody
// stops recording — which is the one behaviour that would make #74 unfeedable.
func TestAnUnservedOperationIsReportedAndDoesNotFailTheRun(t *testing.T) {
	dir := t.TempDir()
	recording := filepath.Join(dir, "unserved.jsonl")
	// A product this emulator publishes an error envelope for and mounts no
	// route of. Walked by a recording, it is #74's work queue rather than this
	// run's verdict.
	line := `{"method":"POST","path":"/k8s/v1/regions/fr-par/clusters","status":200,"mounted":false,` +
		`"res":{"body":{"cluster":{"id":"11111111-1111-1111-1111-111111111111"}}}}` + "\n"
	if err := os.WriteFile(recording, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	target := freshEmulator(t)
	out, errs, code := runReplay(t, recording, "--endpoint", target.URL)
	if code != exitOK {
		t.Fatalf("an unserved operation exited %d, want 0.\nstdout:\n%s\nstderr:\n%s", code, out, errs)
	}
	if !strings.Contains(out, "NOT SERVED") {
		t.Fatalf("the unserved operation is not reported:\n%s", out)
	}
}

// A recording is the inventory of a real account (docs/proxy.md). The report
// names a path, a type and a position; it never republishes a value it read.
//
// The identifiers here are this emulator's own, which is exactly the point: the
// assertion is on what the tool prints, and it cannot tell whose account it is
// reading.
func TestTheReplayReportRepublishesNoValueFromTheRecording(t *testing.T) {
	recording := recordAgainstAFreshEmulator(t)
	raw, err := os.ReadFile(recording) //nolint:gosec // a file this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	mutate(t, recording, `"public_ip"`, `"publicIp"`)

	target := freshEmulator(t)
	out, _, _ := runReplay(t, recording, "--endpoint", target.URL, "--format", "json")

	for _, value := range distinctiveValues(t, raw) {
		if strings.Contains(out, value) {
			t.Errorf("the report republishes %q, a value it read out of the recording", value)
		}
	}

	// And the property itself, not only this recording's strings. A replay
	// rebinds identifiers before it issues a request, so the paths it reports
	// carry the *target's* identifiers, which are different strings from the
	// recording's — the loop above cannot see that leak, and the first version
	// of this test could not: the falsification of 2026-08-20 removed the
	// anonymisation and the test stayed green.
	var rep replay.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) == 0 {
		t.Fatal("the run reported nothing, so this assertion has no subject")
	}
	for _, r := range rep.Results {
		for _, segment := range strings.Split(r.Path, "/") {
			if shape.IsUUID(segment) {
				t.Errorf("the report names the path %q, which carries an identifier: "+
					"a replay says where it went without republishing what it went to", r.Path)
			}
		}
	}
}

// distinctiveValues gathers every identifier-shaped and address-shaped string a
// recording carries, which is what docs/proxy.md's table says a transcript holds
// of somebody's account.
func distinctiveValues(t *testing.T, raw []byte) []string {
	t.Helper()
	seen := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch value := v.(type) {
		case map[string]any:
			for _, nested := range value {
				walk(nested)
			}
		case []any:
			for _, item := range value {
				walk(item)
			}
		case string:
			// A UUID, or a dotted address. Both are inventory; a state name is
			// not, and holding the report to those would fail on words like
			// "running" that belong to the API rather than to the account.
			if len(value) == 36 && strings.Count(value, "-") == 4 {
				seen[value] = true
			}
			if strings.Count(value, ".") == 3 && !strings.ContainsAny(value, " /:") {
				seen[value] = true
			}
		}
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var x any
		if err := json.Unmarshal([]byte(line), &x); err != nil {
			t.Fatalf("the recording this test wrote is not JSON Lines: %v", err)
		}
		walk(x)
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	if len(out) == 0 {
		t.Fatal("the recording carries no identifier at all: the assertion below would prove nothing")
	}
	return out
}

// recordAgainstAFreshEmulator drives a small Scaleway scenario through `feint
// proxy` at an emulator, and returns the transcript's path.
//
// The scenario is the one #320 is about: two addresses reserved, a server
// created naming them in an order of its own, then read back. It is what makes
// the ordering invariant this pack declares reachable at all.
func recordAgainstAFreshEmulator(t *testing.T) string {
	t.Helper()
	source := freshEmulator(t)

	path := filepath.Join(t.TempDir(), "self.jsonl")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	writer := proxy.NewWriter(file, 0)
	upstream, err := url.Parse(source.URL)
	if err != nil {
		t.Fatal(err)
	}
	table, err := emulator.NewTable(mustPacks(t)...)
	if err != nil {
		t.Fatal(err)
	}
	px, err := proxy.New(proxy.Options{Upstream: upstream, Writer: writer, Table: table})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(px)
	defer front.Close()

	zone := "/instance/v1/zones/fr-par-1"
	first := idOf(t, callJSON(t, front.URL, "POST", zone+"/ips", `{"type":"routed_ipv4"}`), "ip")
	second := idOf(t, callJSON(t, front.URL, "POST", zone+"/ips", `{"type":"routed_ipv4"}`), "ip")
	// Named second-then-first on purpose: an emulator serving store order would
	// answer them the other way round, which is #320 exactly.
	created := callJSON(t, front.URL, "POST", zone+"/servers",
		`{"name":"replay-fixture","commercial_type":"DEV1-S","image":"ubuntu_jammy",`+
			`"public_ips":["`+second+`","`+first+`"]}`)
	server := idOf(t, created, "server")
	callJSON(t, front.URL, "GET", zone+"/servers/"+server, "")
	callJSON(t, front.URL, "GET", zone+"/servers", "")

	if err := writer.Close(); err != nil {
		t.Fatalf("close the transcript writer: %v", err)
	}
	return path
}

func freshEmulator(t *testing.T) *httptest.Server {
	t.Helper()
	srv, _, err := newServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func mustPacks(t *testing.T) []emulator.Pack {
	t.Helper()
	packs, err := packsFor(emulator.DefaultEnv())
	if err != nil {
		t.Fatal(err)
	}
	return packs
}

func callJSON(t *testing.T, base, method, path, body string) map[string]any {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, base+path, reader) //nolint:noctx // a test against a local server
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode >= 400 {
		t.Fatalf("%s %s answered %d: %s", method, path, resp.StatusCode, raw)
	}
	var out map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s %s answered something that is not JSON: %s", method, path, raw)
		}
	}
	return out
}

func idOf(t *testing.T, body map[string]any, key string) string {
	t.Helper()
	nested, ok := body[key].(map[string]any)
	if !ok {
		t.Fatalf("the answer carries no %q object: %v", key, body)
	}
	id, ok := nested["id"].(string)
	if !ok || id == "" {
		t.Fatalf("the %q object carries no identifier: %v", key, nested)
	}
	return id
}

func mutate(t *testing.T, path, from, to string) {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // a file this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(from)) {
		t.Fatalf("the recording carries no %s to mutate; the fixture no longer exercises it", from)
	}
	if err := os.WriteFile(path, bytes.ReplaceAll(raw, []byte(from), []byte(to)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runReplay(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := replayCommand(args, &out, &errb)
	return out.String(), errb.String(), code
}

// A declaration that names nothing served is an intention written in the past
// tense, which is the shape this repository has paid the most for. The same
// rule the orphan-route check applies to Route.Operation, and the same rule
// `feint shapes --check` applies to a stale field decline.
func TestEveryReplayInvariantNamesAServedOperation(t *testing.T) {
	packs := mustPacks(t)
	served := map[string]bool{}
	for _, p := range packs {
		for _, r := range p.Routes() {
			served[r.Operation] = true
		}
	}
	declared := 0
	for _, p := range packs {
		for _, inv := range emulator.InvariantsOf(p) {
			declared++
			if !served[inv.Operation] {
				t.Errorf("%s declares the replay invariant %s on %s, and no route serves that operation: "+
					"a declaration nothing can evaluate is a comment", p.Name(), inv.Path, inv.Operation)
			}
		}
	}
	// Assert the subject rather than skip on its absence. With no declaration at
	// all the loop above passes trivially, and a guard that is green because it
	// looked at nothing is the failure mode this test exists inside a repository
	// that measured three times.
	if declared == 0 {
		t.Fatal("no pack declares a replay invariant, so this test proves nothing; " +
			"#320's ordering is the one that must be here")
	}
}

// The packs' own declarations face the guard before a single request goes out.
// TestAnInvariantWithoutAUsableReasonIsRefused (internal/core/emulator) covers
// the guard; this covers the wiring, which is the half that is usually missing.
func TestThePacksDeclarationsPassTheirOwnGuard(t *testing.T) {
	var errb bytes.Buffer
	_, invariants, code := replayDeclarations(mustPacks(t), &errb)
	if code != exitOK {
		t.Fatalf("the packs' own declarations are refused by the guard they face:\n%s", errb.String())
	}
	if len(invariants) == 0 {
		t.Fatal("no invariant reached the replay, so every value and order comparison is unreachable")
	}
}

// #320, end to end and through the binary's own path: the order the create
// named is the order the replay holds the emulator to.
//
// The recording is made against this emulator, so the two sides agree; the
// assertion is that the check *ran*, which is what an order comparison silently
// skipping its subject would fail. Without the count, a replay that never
// evaluated an invariant reads exactly like one where every invariant held.
func TestTheDeclaredOrderIsActuallyEvaluatedOnARecordedRun(t *testing.T) {
	recording := recordAgainstAFreshEmulator(t)
	target := freshEmulator(t)
	out, errs, code := runReplay(t, recording, "--endpoint", target.URL, "--format", "json")
	if code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", code, errs)
	}
	var rep replay.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Orders == 0 {
		t.Fatalf("no declared *order* was evaluated on a run that creates a server with two addresses: "+
			"the check that would have caught #320 reads as held and ran on nothing.\n%s", out)
	}
	if rep.Values == 0 {
		t.Fatalf("no declared *value* was evaluated on a run that names a server: "+
			"the check for an argument the API accepted and ignored ran on nothing.\n%s", out)
	}
}
