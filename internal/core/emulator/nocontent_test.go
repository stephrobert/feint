package emulator_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/core/emulator"
)

// An answer that carries no body is still an answer, and #429 is what it cost to
// treat it as nothing.
//
// Thirty-one of the 173 Scaleway operations this emulator serves are DELETEs
// whose own API description reads `204: {description: ''}` — the provider
// stating that the answer carries no body. The observer's contract check
// returned before marking anything as soon as the body was empty, so every one
// of those thirty-one read `unchecked` on the contract axis, which that axis
// defines as "nobody ever looked". A `scw instance server delete` answering
// exactly what Scaleway documents was recorded as unexamined.
//
// The distinction these tests hold apart is the one that makes the fix honest:
// "the document says there is no body" and "the document says nothing" are two
// different silences, and only the first is checkable.

const noContentContract = `{
  "provider": "stub",
  "operations": {
    "Erase":   {"method": "DELETE", "path": "/stub/erase",   "noContent": 204},
    "Silent":  {"method": "DELETE", "path": "/stub/silent"},
    "Bodied":  {"method": "GET",    "path": "/stub/bodied",  "response": "AView"}
  },
  "schemas": {
    "AView": {"closed": true, "properties": {"ok": {"type": "boolean"}}}
  }
}`

// noContentServer mounts three routes whose answers the caller chooses, so one
// test can put a body, a status, or nothing at all against the same document.
func noContentServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	doc, err := contract.Read(strings.NewReader(noContentContract))
	if err != nil {
		t.Fatalf("read the stub contract: %v", err)
	}
	env := emulator.DefaultEnv()
	env.Contracts = map[string]*contract.Doc{"stub": doc}

	answer := func(w http.ResponseWriter, _ *http.Request) {
		if body != "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		if body != "" {
			_, _ = w.Write([]byte(body))
		}
	}
	pack := stubPack{name: "stub", routes: []emulator.Route{
		{Method: "DELETE", Path: "/stub/erase", Operation: "stub/v1/API.Erase", Handler: answer},
		{Method: "DELETE", Path: "/stub/silent", Operation: "stub/v1/API.Silent", Handler: answer},
		{Method: "GET", Path: "/stub/bodied", Operation: "stub/v1/API.Bodied", Handler: answer},
	}}
	srv, err := emulator.NewServer(env, pack)
	if err != nil {
		t.Fatalf("mount the stub pack: %v", err)
	}
	return httptest.NewServer(srv.Handler())
}

func driveMethod(t *testing.T, ts *httptest.Server, method, path string, probe bool) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, nil) //nolint:noctx // a local test server
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

func TestAnAnswerWithNoBodyIsCheckedAgainstTheDocument(t *testing.T) {
	ts := noContentServer(t, http.StatusNoContent, "")
	defer ts.Close()

	driveMethod(t, ts, http.MethodDelete, "/stub/erase", false)

	if got := viewOf(t, ts).Evidence["stub/v1/API.Erase"].Contract; got != emulator.ContractClean {
		t.Errorf("204 with no body, against a document declaring exactly that, must read clean, got %q", got)
	}
}

// The other direction of the same statement. A document saying "no body" is
// violated by a body, and by a status it does not name — a generated SDK
// branches on 204 to decide whether to unmarshal at all.
func TestAnAnswerContradictingTheDeclaredEmptinessIsAViolation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"a body where the document declares none", http.StatusNoContent, `{"ok":true}`},
		{"a status the document does not name", http.StatusOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := noContentServer(t, tc.status, tc.body)
			defer ts.Close()

			driveMethod(t, ts, http.MethodDelete, "/stub/erase", false)

			if got := viewOf(t, ts).Evidence["stub/v1/API.Erase"].Contract; got != emulator.ContractViolating {
				t.Errorf("want violating, got %q", got)
			}
		})
	}
}

// The witness for the half of #429 that must not move. `Silent` declares no
// response schema and no no-content status either: its document is silent, not
// empty. Exoscale has three live examples — list-events answers a top-level
// array of $ref, which this extraction cannot name — and reading their silence
// as "answers nothing" would be the axis inventing a verdict.
func TestAnUndeclaredEmptyAnswerIsStillUnchecked(t *testing.T) {
	ts := noContentServer(t, http.StatusNoContent, "")
	defer ts.Close()

	driveMethod(t, ts, http.MethodDelete, "/stub/silent", false)

	if got := viewOf(t, ts).Evidence["stub/v1/API.Silent"].Contract; got != emulator.ContractUnchecked {
		t.Errorf("an empty answer the document says nothing about must stay unchecked, got %q", got)
	}
}

func TestAnEmptyAnswerEarnsTheProbeOnlyWhereTheDocumentDeclaresIt(t *testing.T) {
	ts := noContentServer(t, http.StatusNoContent, "")
	defer ts.Close()

	driveMethod(t, ts, http.MethodDelete, "/stub/erase", true)
	driveMethod(t, ts, http.MethodDelete, "/stub/silent", true)

	ev := viewOf(t, ts).Evidence
	if got := ev["stub/v1/API.Erase"].Probed; got != emulator.ProbeResponse {
		t.Errorf("a probed 204 held to a document that declares it must read response, got %q", got)
	}
	if got := ev["stub/v1/API.Silent"].Probed; got != emulator.ProbeNone {
		t.Errorf("a probed 204 nothing could be held to must read none, got %q", got)
	}
}

// A violated no-content declaration must not earn the probe axis either: the
// two axes report the same exchange, and one of them saying "validated" while
// the other says "violating" is the half-truth this record exists to refuse.
//
// Both ways of breaking the declaration are here because they leave by
// different doors, and the first version of this test only walked one of them.
// A body sends the answer down the ordinary JSON path, where the existing
// verdict guard already stops it; an empty answer with the wrong status is the
// only case that reaches the bodyless branch with a violation in hand, and
// neutralising that branch's own guard was "STILL GREEN" until this case
// existed. The mutation is in tools/falsify/specs/declared-empty-answer.json,
// labelled "a violating probe still earns the axis".
func TestAProbedAnswerThatBrokeTheDeclaredEmptinessEarnsNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"an empty answer with a status the document does not name", http.StatusOK, ""},
		{"a body where the document declares none", http.StatusNoContent, `{"ok":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := noContentServer(t, tc.status, tc.body)
			defer ts.Close()

			driveMethod(t, ts, http.MethodDelete, "/stub/erase", true)

			ev := viewOf(t, ts).Evidence
			if got := ev["stub/v1/API.Erase"].Probed; got != emulator.ProbeNone {
				t.Errorf("a probe that violated the document must not read as validated, got %q", got)
			}
			if got := ev["stub/v1/API.Erase"].Contract; got != emulator.ContractViolating {
				t.Errorf("want violating, got %q", got)
			}
		})
	}
}

// The third silence, kept apart from the other two on purpose. `Bodied` names a
// response schema, so an empty answer from it is not "what the document says" —
// it is an answer the document does not describe at all. Recording it as clean
// would be the same promotion of silence to proof that #429 is about, in the
// opposite direction, so it stays unchecked and this test is what keeps it
// there if somebody widens the guard to "empty is always fine".
func TestAnEmptyAnswerFromAnOperationThatDeclaresABodyIsUnchecked(t *testing.T) {
	ts := noContentServer(t, http.StatusNoContent, "")
	defer ts.Close()

	driveMethod(t, ts, http.MethodGet, "/stub/bodied", false)

	if got := viewOf(t, ts).Evidence["stub/v1/API.Bodied"].Contract; got != emulator.ContractUnchecked {
		t.Errorf("an empty answer where a body is declared must stay unchecked, got %q", got)
	}
}
