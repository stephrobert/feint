package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// The controls behind evidenceAxis.earner, which decides whether a reason about
// clients is allowed to explain a zero.
//
// It is the same hazard Route.Unearnable carries and the same answer: a
// declaration that removes work from a queue is measured, and the prose only
// explains it. Until #445 there was no such declaration at all — every zero of
// an undriven operation was filed "declared, no path exists" with the route's
// client-shaped reason printed beside it, on all seven axes at once, including
// the one the contract-driven probe earns with no client whatsoever.
//
// Two independent witnesses, and neither is sufficient alone:
//
//   - the mechanism (TestOnlyAClientEarnsAClientBorneAxis) drives one synthetic
//     and one real exchange against a live emulator and reads the axes back. It
//     measures what the observer actually does, whatever the record happens to
//     hold today.
//   - the record (TestNoClientBorneAxisIsEarnedWithoutAClient) refuses a
//     declaration the committed artefact contradicts. It is what makes the
//     declaration expire on its own the day an axis changes hands.

// probedAxisName is the axis the probe alone earns, named once so a rename
// breaks the tests rather than silently emptying them.
const probedAxisName = "probed"

// A synthetic exchange earns nothing a client is supposed to earn — measured
// against a running emulator, not read off the axis names.
//
// Both halves are required. "The probe earned no client axis" is equally true
// of a probe that never ran, which is the vacuous pass this repository has paid
// for more than once, so the test also requires the synthetic exchange to have
// earned something. And the real client call is there to prove the same server,
// in the same run, does move the client axes: without it, a server that recorded
// nothing at all would pass.
func TestOnlyAClientEarnsAClientBorneAxis(t *testing.T) {
	docs, err := loadContracts(filepath.Join("..", "..", "contracts"))
	if err != nil {
		t.Fatalf("load the contracts: %v", err)
	}
	srv, _, err := newServer(docs)
	if err != nil {
		t.Fatalf("build the emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	const (
		probeOp  = "instance/v1/API.ListServers"
		clientOp = "instance/v1/API.ListIPs"
	)
	mounted := map[string]string{}
	for _, r := range srv.AllRoutes() {
		mounted[r.Operation] = r.Path
	}
	for _, op := range []string{probeOp, clientOp} {
		if mounted[op] == "" {
			t.Fatalf("%s is not mounted, so this test would measure nothing", op)
		}
	}

	// One synthetic exchange, marked exactly the way the contract-driven probe
	// marks its own, and one exchange with no marking at all.
	// The page size is asked for on purpose: a paged operation earns `probed`
	// only through the call that carried it (noteProbe), so without it the
	// synthetic exchange earns `contract` alone and this witness never exercises
	// the axis a client can never reach.
	get(t, ts.URL+"/instance/v1/zones/fr-par-1/servers?per_page=1", true)
	get(t, ts.URL+"/instance/v1/zones/fr-par-1/ips", false)

	var view emulator.ConformanceView
	body := get(t, ts.URL+"/_feint/conformance", false)
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decode the conformance view: %v", err)
	}
	probed, ok := view.Evidence[probeOp]
	if !ok {
		t.Fatalf("the view holds no evidence for %s", probeOp)
	}
	driven, ok := view.Evidence[clientOp]
	if !ok {
		t.Fatalf("the view holds no evidence for %s", clientOp)
	}

	// The witness: the synthetic call was measured. Without it, every assertion
	// below is true of an exchange that never happened.
	var earnedByTheProbe []string
	for _, a := range evidenceAxisList() {
		if a.earned(probed) {
			earnedByTheProbe = append(earnedByTheProbe, a.Name)
		}
	}
	if len(earnedByTheProbe) == 0 {
		t.Fatalf("the synthetic exchange on %s earned no axis at all, so this test proves "+
			"nothing about which axes it cannot earn", probeOp)
	}
	// And it earned the one axis no client can reach. Asserted by name rather
	// than left to the count above: an exchange that earned `contract` alone
	// would satisfy the witness and say nothing about `probed`, which is the
	// axis #445 is about.
	if !namedAxis(t, probedAxisName).earned(probed) {
		t.Fatalf("the synthetic exchange on %s did not earn `%s`, so nothing here shows a "+
			"client-less exchange reaching it", probeOp, probedAxisName)
	}

	for _, a := range evidenceAxisList() {
		if a.earner != earnedByAClient {
			continue
		}
		if a.earned(probed) {
			t.Errorf("%s is declared earned by a client alone, and a synthetic exchange earned it "+
				"on %s: the declaration is what lets a client-shaped reason retire a zero there",
				a.Name, probeOp)
		}
	}

	// The other side of the same server: a real client does move the client
	// axes, and does not move the probe's.
	if !driven.Driven {
		t.Fatalf("a plain request to %s left it undriven, so the comparison above measures "+
			"a broken observer rather than a boundary", clientOp)
	}
	for _, a := range evidenceAxisList() {
		if a.Name == probedAxisName && a.earned(driven) {
			t.Errorf("a client with no probe marking earned `%s` on %s", a.Name, clientOp)
		}
		if a.earner == earnedByARecording && (a.earned(driven) || a.earned(probed)) {
			t.Errorf("%s is declared earned by a recording, and traffic earned it", a.Name)
		}
	}
	sort.Strings(earnedByTheProbe)
	t.Logf("the synthetic exchange on %s earned: %s", probeOp, strings.Join(earnedByTheProbe, ", "))
}

// The committed record must not contradict the declaration.
//
// This is the half that makes the declaration expire on its own. An axis
// declared earnable by a client alone, that an operation no client drove has
// earned, is a declaration the record disproves — and it is the declaration
// that lets `--gaps` file a zero as "not work" with a sentence about `exo` or
// `scw` beside it.
//
// The converse is asserted too, and it is not decoration: without it a
// declaration marking every axis as probe-borne would pass, which is a control
// that has stopped measuring while staying green. The record holds 16
// operations no client drove that earned `probed`, 14 that earned `contract`
// and one — exoscale/v2.get-operation — that earned `shape`.
func TestNoClientBorneAxisIsEarnedWithoutAClient(t *testing.T) {
	art, err := loadEvidenceArtefact(filepath.Join("..", "..", "coverage", "evidence.json"))
	if err != nil {
		t.Fatalf("read the evidence artefact: %v", err)
	}
	if art == nil || len(art.Operations) == 0 {
		t.Fatal("the evidence artefact is empty, so this test would pass while measuring nothing")
	}
	var undriven []string
	for op, ev := range art.Operations {
		if !ev.Driven {
			undriven = append(undriven, op)
		}
	}
	if len(undriven) == 0 {
		t.Fatal("the record holds no undriven operation, so it can neither confirm nor " +
			"contradict any declaration and this test would pass on any code")
	}
	sort.Strings(undriven)

	earnedWithoutAClient := map[string][]string{}
	for _, a := range evidenceAxisList() {
		for _, op := range undriven {
			if a.earned(art.Operations[op]) {
				earnedWithoutAClient[a.Name] = append(earnedWithoutAClient[a.Name], op)
			}
		}
	}

	witnessed := 0
	for _, a := range evidenceAxisList() {
		got := earnedWithoutAClient[a.Name]
		if a.earner == earnedByAClient {
			if len(got) > 0 {
				t.Errorf("`%s` is declared earned by a client alone, and %d operation(s) no client "+
					"drove have earned it: %s\nA zero on that axis is then retired by a reason about "+
					"clients, and the record says clients are not what earns it.",
					a.Name, len(got), strings.Join(got, ", "))
			}
			continue
		}
		witnessed += len(got)
	}
	if witnessed == 0 {
		t.Fatal("no axis declared earnable without a client was earned by any undriven operation, " +
			"so a declaration marking every axis client-borne would pass this test unchanged")
	}
}

// No zero the probe can close is filed as a decision nobody can act on.
//
// The defect, measured on the committed record before the fix: seven Exoscale
// operations sat at zero on `probed` and nine on `contract`, every one of them
// filed `declared` — "not work: no path exists to close this zero" — with a
// sentence about the `exo` CLI printed beside it. The probe needs no CLI. #429
// then proved the point from the other side: 31 Scaleway operations earned
// `contract` and 29 earned `probed` from a single fix to the contract
// extraction, with no client and no pack code touched at all — and had those
// carried an Undriven reason, this queue would have called all sixty "not work".
//
// The assertion is over the committed record rather than a fixture, because the
// misfiled population is a property of the packs and the record together, and a
// fixture would have let the two drift apart. The non-vacuity guard is what
// stops it passing on a record where nobody is in that state.
func TestAClientShapedReasonNeverExplainsAProbeSideZero(t *testing.T) {
	art, err := loadEvidenceArtefact(filepath.Join("..", "..", "coverage", "evidence.json"))
	if err != nil {
		t.Fatalf("read the evidence artefact: %v", err)
	}
	if art == nil || len(art.Operations) == 0 {
		t.Fatal("the evidence artefact is empty, so this test would pass while measuring nothing")
	}
	owners, providers, err := operationOwners()
	if err != nil {
		t.Fatal(err)
	}
	reasons, err := undrivenReasons()
	if err != nil {
		t.Fatal(err)
	}
	unearnable, err := unearnableReasons()
	if err != nil {
		t.Fatal(err)
	}

	// The population the defect lived in, derived from the two inputs and never
	// from the report under test: undriven, carrying a client-shaped reason, and
	// at zero on an axis no client earns.
	exposed := map[string][]string{}
	for op, ev := range art.Operations {
		if _, owned := owners[op]; !owned || ev.Driven || reasons[op] == "" {
			continue
		}
		for _, a := range evidenceAxisList() {
			if a.earner == earnedByAClient || a.earned(ev) || unearnable[op][a.Name] != "" {
				continue
			}
			exposed[a.Name] = append(exposed[a.Name], op)
		}
	}
	if len(exposed) == 0 {
		t.Fatal("no operation is undriven, declared, and at zero on an axis a client does not " +
			"earn, so the state this test describes is unreachable on this record and it would " +
			"pass on any code")
	}

	report, err := buildGaps("coverage/evidence.json", art, owners, reasons, unearnable, providers, "", "")
	if err != nil {
		t.Fatal(err)
	}
	filed := map[string]map[string]gapEntry{}
	for _, g := range report.Groups {
		for _, e := range g.Entries {
			if filed[e.Axis] == nil {
				filed[e.Axis] = map[string]gapEntry{}
			}
			filed[e.Axis][e.Operation] = e
		}
	}

	seen := 0
	for axis, ops := range exposed {
		sort.Strings(ops)
		for _, op := range ops {
			e, queued := filed[axis][op]
			if !queued {
				t.Errorf("%s is at zero on `%s` and the queue does not name it", op, axis)
				continue
			}
			seen++
			if e.Kind == gapKindNames[gapDeclared] {
				t.Errorf("%s is filed `declared` on `%s` and the only reason anybody wrote for it "+
					"is about clients:\n    %s\nA client is not what earns `%s`.", op, axis, e.Reason, axis)
			}
			if e.Reason != "" {
				t.Errorf("%s is filed `%s` on `%s` and still prints a reason: %s",
					op, e.Kind, axis, e.Reason)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no entry of the exposed population was examined, so this test measured nothing")
	}
}

// get issues one request and returns the body, marking it synthetic the way the
// contract-driven probe does when asked.
func get(t *testing.T, url string, synthetic bool) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	if synthetic {
		req.Header.Set(emulator.ProbeHeader, "1")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	if resp.StatusCode >= 300 {
		t.Fatalf("GET %s answered %d: %s", url, resp.StatusCode, body)
	}
	return body
}
