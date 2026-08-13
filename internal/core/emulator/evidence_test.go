package emulator_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
)

// The evidence record is a set of independent axes, and every test here holds
// one axis apart from its neighbours: driven is not probed, "no violation" is
// not "checked and clean", a runtime behind the run is not a mode name, and an
// absent shapes catalogue is not an unobserved operation. Each distinction
// exists because collapsing it produced a real half-truth (#123, #116, #83).

const evidenceContract = `{
  "provider": "stub",
  "operations": {
    "OpA": {"method": "GET", "path": "/stub/a", "response": "AView"}
  },
  "schemas": {
    "AView": {"closed": true, "properties": {"ok": {"type": "boolean"}}}
  }
}`

// evidenceServer mounts one pack with two routes: /stub/a answers what its
// handler is told to, /stub/b answers an empty object with no contract entry.
func evidenceServer(t *testing.T, env *emulator.Env, body string) *emulator.Server {
	t.Helper()
	pack := stubPack{name: "stub", routes: []emulator.Route{
		{Method: "GET", Path: "/stub/a", Operation: "stub/v1/API.OpA",
			Handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}},
		{Method: "GET", Path: "/stub/b", Operation: "stub/v1/API.OpB", Handler: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{}"))
		}},
	}}
	srv, err := emulator.NewServer(env, pack)
	if err != nil {
		t.Fatalf("mount the stub pack: %v", err)
	}
	return srv
}

func contractEnv(t *testing.T) *emulator.Env {
	t.Helper()
	doc, err := contract.Read(strings.NewReader(evidenceContract))
	if err != nil {
		t.Fatalf("read the stub contract: %v", err)
	}
	env := emulator.DefaultEnv()
	env.Contracts = map[string]*contract.Doc{"stub": doc}
	return env
}

func viewOf(t *testing.T, ts *httptest.Server) emulator.ConformanceView {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + "/_feint/conformance")
	if err != nil {
		t.Fatalf("conformance: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var view emulator.ConformanceView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return view
}

func driveRoute(t *testing.T, ts *httptest.Server, path string, probe bool) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil) //nolint:noctx // a local test server
	if err != nil {
		t.Fatal(err)
	}
	if probe {
		req.Header.Set(emulator.ProbeHeader, "1")
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

func TestEvidenceKeepsDrivenAndProbedApart(t *testing.T) {
	srv := evidenceServer(t, contractEnv(t), `{"ok": true}`)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	driveRoute(t, ts, "/stub/a", false) // a real client
	driveRoute(t, ts, "/stub/b", true)  // the probe

	ev := viewOf(t, ts).Evidence
	a, b := ev["stub/v1/API.OpA"], ev["stub/v1/API.OpB"]
	if !a.Driven || a.Probed {
		t.Errorf("OpA was driven by a client, not probed: %+v", a)
	}
	if b.Driven || !b.Probed {
		t.Errorf("OpB was probed, and a probe must never count as driven: %+v", b)
	}
}

func TestEvidenceContractCleanNeedsACheckNotSilence(t *testing.T) {
	srv := evidenceServer(t, contractEnv(t), `{"ok": true}`)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	driveRoute(t, ts, "/stub/a", false)

	ev := viewOf(t, ts).Evidence
	if got := ev["stub/v1/API.OpA"].Contract; got != emulator.ContractClean {
		t.Errorf("a validated, conformant answer must read clean, got %q", got)
	}
	// OpB was never driven, so nothing of it was ever validated. "unchecked"
	// is the honest answer; "clean" here would promote silence to proof.
	if got := ev["stub/v1/API.OpB"].Contract; got != emulator.ContractUnchecked {
		t.Errorf("an unvalidated operation must read unchecked, got %q", got)
	}
}

func TestEvidenceContractViolationIsLouderThanACheck(t *testing.T) {
	srv := evidenceServer(t, contractEnv(t), `{"ok": true, "invented": "x"}`)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	driveRoute(t, ts, "/stub/a", false)

	if got := viewOf(t, ts).Evidence["stub/v1/API.OpA"].Contract; got != emulator.ContractViolating {
		t.Errorf("an invented field must read violating, got %q", got)
	}
}

func TestEvidenceWithoutAContractIsUncheckedNotClean(t *testing.T) {
	srv := evidenceServer(t, emulator.DefaultEnv(), `{"ok": true}`)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	driveRoute(t, ts, "/stub/a", false)

	if got := viewOf(t, ts).Evidence["stub/v1/API.OpA"].Contract; got != emulator.ContractUnchecked {
		t.Errorf("driven with no contract loaded must read unchecked, got %q", got)
	}
}

func TestEvidenceShapeDistinguishesUnknownFromUnobserved(t *testing.T) {
	srv := evidenceServer(t, contractEnv(t), `{"ok": true}`)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// No catalogue handed in: the record must say "unknown", never demote to
	// "unobserved" — an installed binary without shapes/ has not measured
	// anything, and absence of measurement is not a measurement of absence.
	if got := viewOf(t, ts).Evidence["stub/v1/API.OpA"].Shape; got != emulator.ShapeUnknown {
		t.Errorf("no catalogue must read unknown, got %q", got)
	}

	srv.SetShapeCovered([]string{"stub/v1/API.OpA"})
	ev := viewOf(t, ts).Evidence
	if got := ev["stub/v1/API.OpA"].Shape; got != emulator.ShapeObserved {
		t.Errorf("a covered operation must read observed, got %q", got)
	}
	if got := ev["stub/v1/API.OpB"].Shape; got != emulator.ShapeUnobserved {
		t.Errorf("with a catalogue present, an uncovered operation reads unobserved, got %q", got)
	}
}

// namedDriver is a runtime that is not "none", by declaration — the axis must
// read the driver's own name, never compare a mode string.
type namedDriver struct{ machine.Noop }

func (namedDriver) Name() string { return "stub-runtime" }

func (namedDriver) Available(context.Context) bool { return true }

func TestEvidenceDataplaneFollowsTheDriversOwnDeclaration(t *testing.T) {
	env := contractEnv(t)
	env.Machines = namedDriver{}
	srv := evidenceServer(t, env, `{"ok": true}`)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	driveRoute(t, ts, "/stub/a", false)
	driveRoute(t, ts, "/stub/b", true)

	view := viewOf(t, ts)
	if view.Machines != "stub-runtime" {
		t.Errorf("the record must carry the runtime it depends on, got %q", view.Machines)
	}
	if !view.Evidence["stub/v1/API.OpA"].Dataplane {
		t.Error("driven with a runtime configured must read dataplane")
	}
	// A probe is not a client: it must not earn the dataplane axis either.
	if view.Evidence["stub/v1/API.OpB"].Dataplane {
		t.Error("a probed-only operation must not read dataplane")
	}
}

func TestEvidenceDataplaneIsFalseWithoutARuntime(t *testing.T) {
	srv := evidenceServer(t, contractEnv(t), `{"ok": true}`)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	driveRoute(t, ts, "/stub/a", false)

	view := viewOf(t, ts)
	if view.Machines != "none" {
		t.Errorf("metadata-only machines must read none, got %q", view.Machines)
	}
	if view.Evidence["stub/v1/API.OpA"].Dataplane {
		t.Error("driven without a runtime must not read dataplane")
	}
}
