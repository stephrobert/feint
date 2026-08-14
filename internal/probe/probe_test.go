package probe_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/probe"
	"github.com/stephrobert/feint/internal/providers/exoscale"
	"github.com/stephrobert/feint/internal/providers/outscale"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// The probe runs against the emulator in this process, so it is the half of
// conformance that needs no client installed and runs in CI on `go test`. The
// other half — the suites under tools/conformance/ — drives the real clients and
// proves behaviour, and neither replaces the other.

func packOf(t *testing.T, provider string, env *emulator.Env) emulator.Pack {
	t.Helper()
	switch provider {
	case scaleway.Name:
		return scaleway.New(env)
	case outscale.Name:
		return outscale.New(env)
	case exoscale.Name:
		return exoscale.New(env)
	}
	t.Fatalf("no pack named %q", provider)
	return nil
}

func runProbe(t *testing.T, provider string) probe.Report {
	t.Helper()

	doc, err := contract.Load(filepath.Join("..", "..", "contracts", provider+".json"))
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}

	env := emulator.DefaultEnv()
	env.Contracts = map[string]*contract.Doc{provider: doc}
	pack := packOf(t, provider, env)

	srv, err := emulator.NewServer(env, pack)
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	mounted := make([]contract.MountedRoute, 0, len(pack.Routes()))
	for _, r := range pack.Routes() {
		mounted = append(mounted, contract.MountedRoute{
			Method: r.Method, Path: r.Path, Operation: r.Operation,
		})
	}

	runner := &probe.Runner{Doc: doc, Base: ts.URL, Client: ts.Client()}
	report, err := runner.Run(context.Background(), mounted)
	if err != nil {
		t.Fatalf("%s: %v", provider, err)
	}
	return report
}

// TestEveryRouteAnswersItsContract is the check the probe exists to be. A route
// that answers a shape its provider does not define fails here, without anybody
// having written a case for it — which is what makes the remaining hundred and
// fifty operations affordable.
func TestEveryRouteAnswersItsContract(t *testing.T) {
	for _, provider := range []string{outscale.Name, exoscale.Name} {
		report := runProbe(t, provider)
		for _, failure := range report.Failures() {
			switch {
			case failure.Err != nil:
				t.Errorf("%s %s: %v", provider, failure.Operation, failure.Err)
			case len(failure.Violations) > 0:
				t.Errorf("%s %s does not match the contract: %v",
					provider, failure.Operation, failure.Violations)
			default:
				t.Errorf("%s %s answered %d to a request built from its own schema",
					provider, failure.Operation, failure.Status)
			}
		}
		t.Logf("%s", report)
	}
}

// Scaleway is probed like the others; what it cannot prove is the family of
// operations whose exchanges the document gives no schema for (the 204
// deletes, the user-data files). Failures here are contract disagreements the
// seeded run provoked — the block lists ignoring per_page were found exactly
// this way.
func TestScalewayProbesWhatItsDocumentAllows(t *testing.T) {
	report := runProbe(t, scaleway.Name)
	for _, failure := range report.Failures() {
		t.Errorf("scaleway %s: status %d, %v, %v",
			failure.Operation, failure.Status, failure.Violations, failure.Err)
	}
	if report.Probed() == 0 {
		t.Error("nothing was probed at all, which means the plan is empty rather than limited")
	}
	t.Logf("%s", report)
}

// A probe must never make the score go up. The backlog of routes nobody has
// proven is computed from real client calls alone, and this is the assertion
// that keeps it that way — the one thing that stops this package from becoming
// the defect it was built to avoid.
func TestProbingDoesNotCountAsProven(t *testing.T) {
	doc, err := contract.Load(filepath.Join("..", "..", "contracts", outscale.Name+".json"))
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}

	env := emulator.DefaultEnv()
	env.Contracts = map[string]*contract.Doc{outscale.Name: doc}
	pack := outscale.New(env)
	srv, err := emulator.NewServer(env, pack)
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	mounted := make([]contract.MountedRoute, 0, len(pack.Routes()))
	for _, r := range pack.Routes() {
		mounted = append(mounted, contract.MountedRoute{
			Method: r.Method, Path: r.Path, Operation: r.Operation,
		})
	}
	runner := &probe.Runner{Doc: doc, Base: ts.URL, Client: ts.Client()}
	report, err := runner.Run(context.Background(), mounted)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if report.Probed() == 0 {
		t.Fatal("the probe reached nothing, so this proves nothing either")
	}

	status, body := get(t, ts, "/_feint/conformance")
	if status != 200 {
		t.Fatalf("conformance report answered %d", status)
	}
	if !strings.Contains(body, `"exercised":0`) {
		t.Errorf("probing moved the exercised count, which must only ever be a "+
			"real client: %s", body)
	}
	if strings.Contains(body, `"probed":0`) {
		t.Error("the probe reached routes and none was counted as probed")
	}
}

// stubDoc is a contract small enough to read whole: one plain operation, one
// paged one, and the error shape a refusal must have.
const stubDoc = `{
  "provider": "stub",
  "errorSchema": "StubError",
  "operations": {
    "GetThing": {"method": "GET", "path": "/thing", "response": "ThingView"},
    "ListThings": {
      "method": "GET", "path": "/things", "response": "ThingsView",
      "query": {"per_page": {"type": "integer"}}
    }
  },
  "schemas": {
    "ThingView": {"closed": true, "properties": {"ok": {"type": "boolean"}}},
    "ThingsView": {"closed": true, "properties": {"things": {"type": "array", "items": {"type": "string"}}}},
    "StubError": {"closed": false, "required": ["message"], "properties": {"message": {"type": "string"}}}
  }
}`

// runStub probes a hand-built server against stubDoc, optionally with the
// error shape stripped from it.
func runStub(t *testing.T, withErrorShape bool, handler http.Handler, routes ...contract.MountedRoute) probe.Report {
	t.Helper()
	doc, err := contract.Read(strings.NewReader(stubDoc))
	if err != nil {
		t.Fatalf("read the stub contract: %v", err)
	}
	if !withErrorShape {
		doc.ErrorSchema = ""
	}
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	runner := &probe.Runner{Doc: doc, Base: ts.URL, Client: ts.Client()}
	report, err := runner.Run(context.Background(), routes)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return report
}

func plainText404(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "404 page not found", http.StatusNotFound)
}

// TestARefusedProbeIsReadAgainstTheErrorShape is the check #156 was filed for:
// a 4xx used to return before any validation, so text/plain "404 page not
// found" — the exact answer #74 measured on an unmounted corner — counted the
// same as a typed error envelope. Now the refusal body is held to the error
// shape the provider declares: not JSON fails, the bare {} the issue names
// fails on the missing required field, and only the declared envelope passes.
func TestARefusedProbeIsReadAgainstTheErrorShape(t *testing.T) {
	route := contract.MountedRoute{Method: "GET", Path: "/thing", Operation: "stub/v1/API.GetThing"}

	cases := []struct {
		name    string
		handler http.HandlerFunc
		fails   bool
	}{
		{"text-plain", plainText404, true},
		{"bare-object", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{}`))
		}, true},
		{"declared-envelope", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message": "no such thing"}`))
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report := runStub(t, true, c.handler, route)
			failures := report.Failures()
			if c.fails && len(failures) == 0 {
				t.Fatalf("a refusal off the declared error shape must fail the probe: %s", report)
			}
			if !c.fails && len(failures) > 0 {
				t.Fatalf("a refusal in the declared shape is not a failure: %s", report)
			}
			res := report.Results[0]
			if !res.Refused || res.RefusalUnread {
				t.Errorf("every case here is a refusal that was read: %+v", res)
			}
		})
	}
}

// TestAnUnreadRefusalIsNotAValidation pins the honest half of the same rule:
// with no declared error shape there is nothing to hold a refusal to, and the
// result must say "unread" rather than dress the silence up as a pass — a
// single flag covering "validated" and "nobody looked" is the defect #156
// names, moved one step over.
func TestAnUnreadRefusalIsNotAValidation(t *testing.T) {
	route := contract.MountedRoute{Method: "GET", Path: "/thing", Operation: "stub/v1/API.GetThing"}
	report := runStub(t, false, http.HandlerFunc(plainText404), route)

	if len(report.Failures()) > 0 {
		t.Fatalf("nothing upstream to disagree with, so nothing may fail: %s", report)
	}
	res := report.Results[0]
	if !res.Refused || !res.RefusalUnread {
		t.Errorf("a refusal nothing could be checked against must read unread: %+v", res)
	}
	if report.Probed() != 0 {
		t.Error("an unread refusal must not count as probed")
	}
}

// TestAnAnswerIgnoringPageSizeIsAViolation exercises the one parameter family
// the probe sends: an operation declaring per_page is called with per_page=1,
// and an answer carrying two items has ignored it — which no schema check can
// see, because the array is still an array.
func TestAnAnswerIgnoringPageSizeIsAViolation(t *testing.T) {
	route := contract.MountedRoute{Method: "GET", Path: "/things", Operation: "stub/v1/API.ListThings"}

	deaf := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"things": ["a", "b"]}`)) // whatever was asked
	}
	report := runStub(t, true, http.HandlerFunc(deaf), route)
	failures := report.Failures()
	if len(failures) == 0 {
		t.Fatalf("two items where one was asked is per_page ignored, and must fail: %s", report)
	}
	if failures[0].PageSized != "per_page" {
		t.Errorf("the result names the parameter it exercised, got %q", failures[0].PageSized)
	}

	paged := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("per_page") == "1" {
			_, _ = w.Write([]byte(`{"things": ["a"]}`))
			return
		}
		_, _ = w.Write([]byte(`{"things": ["a", "b"]}`))
	}
	report = runStub(t, true, http.HandlerFunc(paged), route)
	if len(report.Failures()) > 0 {
		t.Fatalf("an answer within its bound passes: %s", report)
	}
	// The plan runs a read twice, so the operation shows up once per pass;
	// both must have been probed with the parameter exercised.
	if report.Probed() != len(report.Results) || report.Results[0].PageSized != "per_page" {
		t.Errorf("the paged operation was probed with its page size exercised: %+v", report.Results[0])
	}
}

func get(t *testing.T, ts *httptest.Server, path string) (int, string) {
	t.Helper()
	res, err := ts.Client().Get(ts.URL + path) //nolint:noctx // test client
	if err != nil {
		t.Fatalf("get %s: %v", path, err)
	}
	defer func() { _ = res.Body.Close() }()
	buf := make([]byte, 8192)
	n, _ := res.Body.Read(buf)
	return res.StatusCode, string(buf[:n])
}
