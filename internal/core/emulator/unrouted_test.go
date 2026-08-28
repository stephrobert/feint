package emulator_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/outscale"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// A request no route claims used to get net/http's own answer, "404 page not
// found" in text/plain. No SDK parses that: the Scaleway one checks the content
// type first and, finding it is not JSON, drops the body and reports the bare
// status. So a caller met a decoding failure where an API error belonged, and
// with most of both surfaces still unserved that is the likeliest answer of all.

// unroutedPack owns a prefix and answers in a dialect of its own, which is what
// the core must delegate to without knowing anything about it.
type unroutedPack struct{ stubPack }

func (unroutedPack) Name() string       { return "stub" }
func (unroutedPack) Prefixes() []string { return []string{"/stub/v1/"} }
func (unroutedPack) Routes() []emulator.Route {
	return []emulator.Route{{
		Method: "GET", Path: "/stub/v1/things", Operation: "stub/v1.ListThings",
		Handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
	}}
}
func (unroutedPack) NotFound(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_, _ = w.Write([]byte(`{"dialect":"stub"}`))
}

func serve(t *testing.T, packs ...emulator.Pack) *httptest.Server {
	t.Helper()
	srv, err := emulator.NewServer(emulator.DefaultEnv(), packs...)
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func get(t *testing.T, ts *httptest.Server, path string) (int, string, string) {
	t.Helper()
	res, err := http.Get(ts.URL + path) //nolint:noctx // test client
	if err != nil {
		t.Fatalf("get %s: %v", path, err)
	}
	defer func() { _ = res.Body.Close() }()
	body := make([]byte, 512)
	n, _ := res.Body.Read(body)
	return res.StatusCode, res.Header.Get("Content-Type"), string(body[:n])
}

func TestUnroutedRequestGetsThePackDialect(t *testing.T) {
	ts := serve(t, unroutedPack{})

	status, contentType, body := get(t, ts, "/stub/v1/nothing-here")
	if status != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", status)
	}
	if !strings.HasPrefix(contentType, "application/json") {
		t.Errorf("content type = %q, want application/json: an SDK drops the body otherwise", contentType)
	}
	if !strings.Contains(body, `"dialect":"stub"`) {
		t.Errorf("body = %q, want the pack's own error", body)
	}
}

func TestAMountedRouteStillWins(t *testing.T) {
	// "/" matches everything no other pattern does. If it ever shadowed a real
	// route the emulator would serve nothing at all, so this is worth pinning.
	ts := serve(t, unroutedPack{})
	if status, _, _ := get(t, ts, "/stub/v1/things"); status != http.StatusOK {
		t.Fatalf("the mounted route answered %d: the catch-all shadowed it", status)
	}
}

func TestAPathNoPackClaimsKeepsThePlainAnswer(t *testing.T) {
	// Nothing owns it, so there is no dialect to answer in and pretending
	// otherwise would attribute the request to a provider at random.
	ts := serve(t, unroutedPack{})
	status, _, _ := get(t, ts, "/nothing/at/all")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

func TestEachPackAnswersItsOwnSpace(t *testing.T) {
	// Two packs mounted together must not answer for each other, and each must
	// answer in the dialect and with the status its own client handles best.
	//
	// The statuses differ on purpose, and it is a measurement rather than an
	// oversight. 501 says what is true — the operation exists, this server does
	// not implement it — and the Scaleway SDK has no retry policy, so it costs
	// nothing there. oapi-cli treats 501 as retryable and spends 12 seconds on
	// three backed-off attempts before giving up, so Outscale answers 404 and
	// returns at once. Same defect fixed, two clients, two right answers.
	env := emulator.DefaultEnv()
	ts := serve(t, scaleway.New(env), outscale.New(env))

	cases := []struct {
		path   string
		status int
		marker string
	}{
		{"/instance/v1/zones/fr-par-1/nope", http.StatusNotImplemented, "not_emulated"},
		{"/api/v1/ReadNets", http.StatusNotFound, "OperationNotEmulated"},
	}
	for _, tc := range cases {
		status, contentType, body := get(t, ts, tc.path)
		if status != tc.status {
			t.Errorf("%s: status = %d, want %d", tc.path, status, tc.status)
		}
		if !strings.HasPrefix(contentType, "application/json") {
			t.Errorf("%s: content type = %q, want application/json", tc.path, contentType)
		}
		if !strings.Contains(body, tc.marker) {
			t.Errorf("%s: body = %q, want it to carry %q", tc.path, body, tc.marker)
		}
		if !json.Valid([]byte(body)) {
			t.Errorf("%s: body is not valid JSON: %q", tc.path, body)
		}
	}
}

// Every refusal a pack makes for its own space carries the out-of-band marker,
// and no served answer does (#477).
//
// Why the marker is a header and not a status. Replaying the register's best
// Exoscale stack — seven resources applied, empty second plan, seven destroyed,
// green end to end — the recorder showed three refusals of
// `GET /v2/reverse-dns/elastic-ip/{id}`, an operation that pack declines, and
// nothing anywhere said so: a bare 404 is also what the cloud answers for an
// elastic IP with no reverse record, so the refusal and the ordinary empty
// answer were the same bytes to a program.
//
// The obvious remedy was measured and refused. Answering 501 there, the way the
// Scaleway pack does in its own space, is legible and costs `exo` 1.95.1
// nothing in latency — 22, 21 and 19 ms against the 19 ms of a served route —
// and it FAILS `exo compute instance create`, which calls
// GET /v2/reverse-dns/instance/{id} after every create and treats anything but
// a 404 as fatal. Measured on 2026-08-28: the exo-cli conformance leg died at
// "instance create rejected" under 501 and passes under 404. A refusal loud
// enough to fail a client the real cloud would have served is the polar star
// inverted, and it is the symmetric defect of the one #477 reports.
//
// So the marker goes where no client trips over it. Here rather than in each
// pack: a control copied into three packs is a control the fourth forgets, and
// the three spellings of a refusal are exactly what #477 is about.
//
// The value is the pack's own name, read out of this process's mount table.
// Never the path: what a client chose does not become a value this emulator
// writes, which is the same rule the "pointing without a served prefix" warning
// below the call site already follows.
func TestAnUnroutedAnswerCarriesTheNotEmulatedHeader(t *testing.T) {
	env := emulator.DefaultEnv()
	ts := serve(t, scaleway.New(env), outscale.New(env), unroutedPack{})

	header := func(t *testing.T, path string) (int, string) {
		t.Helper()
		res, err := http.Get(ts.URL + path) //nolint:noctx // test client
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		defer func() { _ = res.Body.Close() }()
		return res.StatusCode, res.Header.Get(emulator.NotEmulatedHeader)
	}

	// Three packs, three dialects, one marker — including the pack whose
	// refusal is a bare 404 that no body field can distinguish.
	for path, want := range map[string]string{
		"/instance/v1/zones/fr-par-1/nope": "scaleway",
		"/api/v1/ReadNotAnOperation":       "outscale",
		"/stub/v1/nothing-here":            "stub",
	} {
		status, got := header(t, path)
		if got != want {
			t.Errorf("%s answered %d with %s=%q, want %q: a program cannot otherwise tell this "+
				"emulator refusing an operation from a cloud answering nothing",
				path, status, emulator.NotEmulatedHeader, got, want)
		}
	}

	// The other direction, twice, because a header set on every answer marks
	// nothing. A served route does not carry it, and neither does a path no
	// pack claims — nothing is refusing an operation there, and attributing it
	// to a provider would be a guess.
	if status, got := header(t, "/stub/v1/things"); got != "" {
		t.Errorf("a served route (%d) carries %s=%q", status, emulator.NotEmulatedHeader, got)
	}
	if status, got := header(t, "/nothing/at/all"); got != "" {
		t.Errorf("a path no pack claims (%d) carries %s=%q", status, emulator.NotEmulatedHeader, got)
	}
}

// overlappingPack claims a space that swallows another pack's.
type overlappingPack struct{ unroutedPack }

func (overlappingPack) Name() string       { return "greedy" }
func (overlappingPack) Prefixes() []string { return []string{"/stub/"} }
func (overlappingPack) Routes() []emulator.Route {
	return []emulator.Route{{
		Method: "GET", Path: "/greedy/v1/things", Operation: "greedy/v1.ListThings",
		Handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
	}}
}

// TestTwoPacksMayNotClaimOverlappingSpaces holds the refusal NewServer makes for
// two packs whose unrouted spaces overlap.
//
// It is the same refusal NewTable makes for two packs claiming one route, one
// level up, and it is needed for the same reason with an extra twist: an
// overlap does not conflict loudly. handleUnrouted resolves by longest prefix,
// so one pack simply wins, decided by the length of a string, and a client
// meets the wrong provider's error dialect for every operation neither serves.
//
// Checked at start-up rather than in a test listing the known spaces, because
// such a list is one a fourth pack is absent from — green while measuring
// nothing of it.
func TestTwoPacksMayNotClaimOverlappingSpaces(t *testing.T) {
	_, err := emulator.NewServer(emulator.DefaultEnv(), unroutedPack{}, overlappingPack{})
	if err == nil {
		t.Fatal("two packs claimed overlapping URL space and the server started anyway")
	}
	for _, want := range []string{"stub", "greedy", "/stub/v1/", "/stub/"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

// And the packs that actually ship must keep starting: a check that refuses
// everything passes every attack test and breaks the product.
func TestTheMountedPacksClaimDisjointSpaces(t *testing.T) {
	env := emulator.DefaultEnv()
	if _, err := emulator.NewServer(env, scaleway.New(env), outscale.New(env)); err != nil {
		t.Fatalf("the shipped packs no longer mount together: %v", err)
	}
}
