package replay_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/replay"
	"github.com/stephrobert/feint/internal/trace"
)

// stubPack mounts the two routes these tests replay against. It is a pack
// rather than a hand-written route list because the table under test is the
// one `feint proxy` and `feint replay` both build from mounted packs, and a
// list written here would be a second answer to "what is served".
type stubPack struct {
	routes []emulator.Route
}

func (p stubPack) Name() string             { return "stub" }
func (p stubPack) Routes() []emulator.Route { return p.routes }
func (p stubPack) Declined() []emulator.Decline {
	return nil
}
func (p stubPack) Env(string) emulator.Environment { return emulator.Environment{} }

const (
	serverPath = "/instance/v1/zones/fr-par-1/servers"
	ipPath     = "/instance/v1/zones/fr-par-1/ips"
)

func table(t *testing.T) *emulator.Table {
	t.Helper()
	tab, err := emulator.NewTable(stubPack{routes: []emulator.Route{
		{Method: "POST", Path: serverPath, Operation: "instance/v1/API.CreateServer",
			Handler: func(http.ResponseWriter, *http.Request) {}},
		{Method: "GET", Path: serverPath + "/{id}", Operation: "instance/v1/API.GetServer",
			Handler: func(http.ResponseWriter, *http.Request) {}},
		{Method: "POST", Path: ipPath, Operation: "instance/v1/API.CreateIP",
			Handler: func(http.ResponseWriter, *http.Request) {}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return tab
}

// answers is an emulator stand-in that replies with a canned body per path
// prefix, so a test states exactly what the emulator answers without having to
// make a real pack answer it.
func answers(t *testing.T, reply func(r *http.Request) (int, string)) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, body := reply(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func exchange(t *testing.T, line string) trace.Exchange {
	t.Helper()
	var x trace.Exchange
	dec := json.NewDecoder(strings.NewReader(line))
	dec.UseNumber()
	if err := dec.Decode(&x); err != nil {
		t.Fatalf("decode the recorded line: %v", err)
	}
	return x
}

func run(t *testing.T, exs []trace.Exchange, endpoint string, invariants ...emulator.Invariant) replay.Report {
	t.Helper()
	rep, err := replay.Run(context.Background(), exs, replay.Options{
		Endpoint:   endpoint,
		Client:     &http.Client{Timeout: 5 * time.Second},
		Table:      table(t),
		Invariants: invariants,
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	return rep
}

// The values below are invented. No line of this file comes from a real
// account, which is the rule docs/proxy.md states for a fixture built from an
// observed shape — and the addresses are TEST-NET-3 (203.0.113.0/24), which is
// also the block this emulator itself hands out, so nothing here could be
// mistaken for a routable address somebody owns.
const (
	recordedID    = "11111111-1111-1111-1111-111111111111"
	recordedIPOne = "22222222-2222-2222-2222-222222222222"
	recordedIPTwo = "33333333-3333-3333-3333-333333333333"
	freshID       = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	freshIPOne    = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	freshIPTwo    = "cccccccc-cccc-cccc-cccc-cccccccccccc"
)

func createLine(body string) string {
	return `{"method":"POST","path":"` + serverPath + `","operation":"instance/v1/API.CreateServer",` +
		`"status":201,"mounted":true,` +
		`"req":{"headers":{"Content-Type":"application/json"},"body":{"name":"web-1","commercial_type":"DEV1-S"}},` +
		`"res":{"body":` + body + `}}`
}

const recordedServer = `{"server":{"id":"` + recordedID + `","name":"web-1","commercial_type":"DEV1-S",` +
	`"public_ip":{"id":"` + recordedIPOne + `","address":"203.0.113.1"},` +
	`"public_ips":[{"id":"` + recordedIPOne + `","address":"203.0.113.1"},{"id":"` + recordedIPTwo + `","address":"203.0.113.2"}]}}`

// The same server as this emulator would answer it: every identifier and every
// address is different, and nothing else is.
const freshServer = `{"server":{"id":"` + freshID + `","name":"web-1","commercial_type":"DEV1-S",` +
	`"public_ip":{"id":"` + freshIPOne + `","address":"10.0.0.1"},` +
	`"public_ips":[{"id":"` + freshIPOne + `","address":"10.0.0.1"},{"id":"` + freshIPTwo + `","address":"10.0.0.2"}]}}`

// The second direction of the falsification, and the one that is forgotten: a
// value the recording carries and this emulator is allowed to mint differently
// must produce no finding at all. Without it the tool is noise and nobody runs
// it twice.
func TestADynamicIdentifierProducesNoFalsePositive(t *testing.T) {
	ts := answers(t, func(*http.Request) (int, string) { return 201, freshServer })
	rep := run(t, []trace.Exchange{exchange(t, createLine(recordedServer))}, ts.URL)
	if rep.Divergent != 0 {
		t.Fatalf("a run whose only differences are minted identifiers reported %d divergence(s): %+v",
			rep.Divergent, rep.Results[0].Findings)
	}
	if rep.Matched != 1 {
		t.Fatalf("matched %d, want 1", rep.Matched)
	}
}

// The first direction: a recorded response mutated in one place is named.
// #73's own criterion, with its own example — public_ip renamed to publicIp.
func TestARenamedFieldInTheRecordingProducesADiff(t *testing.T) {
	mutated := strings.Replace(recordedServer, `"public_ip":`, `"publicIp":`, 1)
	ts := answers(t, func(*http.Request) (int, string) { return 201, freshServer })
	rep := run(t, []trace.Exchange{exchange(t, createLine(mutated))}, ts.URL)
	if rep.Divergent != 1 {
		t.Fatalf("a renamed recorded field produced %d divergence(s), want 1", rep.Divergent)
	}
	if !namesFinding(rep, replay.KindAbsent, "server.publicIp") {
		t.Fatalf("the diff does not name server.publicIp: %+v", rep.Results[0].Findings)
	}
}

// #270: two vpc/v2 creates answered 201 where the cloud answers 200. A replay
// names it without a Scaleway account, and the status line is exact.
func TestAStatusThatDiffersIsNamed(t *testing.T) {
	ts := answers(t, func(*http.Request) (int, string) { return 200, freshServer })
	rep := run(t, []trace.Exchange{exchange(t, createLine(recordedServer))}, ts.URL)
	if !namesFinding(rep, replay.KindStatus, "") {
		t.Fatalf("a 201 recorded against a 200 answer produced no status finding: %+v", rep.Results[0].Findings)
	}
	if got := findingOf(rep, replay.KindStatus); got.Want != "201" || got.Got != "200" {
		t.Fatalf("status finding says want %q got %q, expected 201 and 200", got.Want, got.Got)
	}
}

// #320: order *is* the contract on public_ips, and a replay that ignored
// ordering everywhere would have missed it.
//
// The recording is the real shape of that defect: two POST /ips reserve the
// addresses, then one create names them in an order of its own. This emulator
// mints its own identifiers for both, so the order check only means something
// once those two creates have bound the recording's identifiers to them — which
// is why the bindings are learned after each exchange is compared and not
// before.
func TestAReorderedListIsNamedWhereThePackDeclaredTheOrder(t *testing.T) {
	order := emulator.Invariant{
		Operation: "instance/v1/API.CreateServer",
		Path:      "server.public_ips[].id",
		Kind:      emulator.InvariantOrder,
		Reason:    "Terraform stores public_ips as a list and a reordered read is a plan diff that never converges",
	}

	// The honest half first: the same order answers clean, and the check ran.
	ts := answers(t, ipsThenServer(freshServer))
	clean := run(t, ipOrderRecording(), ts.URL, order)
	if clean.Divergent != 0 {
		t.Fatalf("the declared order held and the replay still reported %d divergence(s): %+v",
			clean.Divergent, clean.Results)
	}
	if clean.Orders != 1 {
		t.Fatalf("%d order check(s) evaluated, want 1: an order check that never ran must not read as a pass",
			clean.Orders)
	}

	swapped := `{"server":{"id":"` + freshID + `","name":"web-1","commercial_type":"DEV1-S",` +
		`"public_ip":{"id":"` + freshIPOne + `","address":"10.0.0.1"},` +
		`"public_ips":[{"id":"` + freshIPTwo + `","address":"10.0.0.2"},{"id":"` + freshIPOne + `","address":"10.0.0.1"}]}}`
	swappedTS := answers(t, ipsThenServer(swapped))
	rep := run(t, ipOrderRecording(), swappedTS.URL, order)
	if !namesFinding(rep, replay.KindOrder, "server.public_ips[].id") {
		t.Fatalf("a swapped public_ips produced no order finding: %+v", rep.Results)
	}
	if got := findingOf(rep, replay.KindOrder); got.Want != "0,1" || got.Got != "1,0" {
		t.Fatalf("order finding says want %q got %q, expected 0,1 and 1,0", got.Want, got.Got)
	}
}

// ipOrderRecording is the #320 scenario as a proxy would have written it: two
// reservations, then a create naming them. Every value is invented.
func ipOrderRecording() []trace.Exchange {
	lines := []string{
		`{"method":"POST","path":"` + ipPath + `","operation":"instance/v1/API.CreateIP","status":201,` +
			`"mounted":true,"req":{"body":{"type":"routed_ipv4"}},` +
			`"res":{"body":{"ip":{"id":"` + recordedIPOne + `","address":"203.0.113.1"}}}}`,
		`{"method":"POST","path":"` + ipPath + `","operation":"instance/v1/API.CreateIP","status":201,` +
			`"mounted":true,"req":{"body":{"type":"routed_ipv4"}},` +
			`"res":{"body":{"ip":{"id":"` + recordedIPTwo + `","address":"203.0.113.2"}}}}`,
		`{"method":"POST","path":"` + serverPath + `","operation":"instance/v1/API.CreateServer","status":201,` +
			`"mounted":true,"req":{"headers":{"Content-Type":"application/json"},` +
			`"body":{"name":"web-1","commercial_type":"DEV1-S","public_ips":["` + recordedIPOne + `","` + recordedIPTwo + `"]}},` +
			`"res":{"body":` + recordedServer + `}}`,
	}
	out := make([]trace.Exchange, 0, len(lines))
	for _, line := range lines {
		out = append(out, mustExchange(line))
	}
	return out
}

// ipsThenServer answers the two reservations with this emulator's own
// identifiers, then the create with the body the test is about.
func ipsThenServer(server string) func(*http.Request) (int, string) {
	minted := 0
	return func(r *http.Request) (int, string) {
		if r.URL.Path == ipPath {
			minted++
			if minted == 1 {
				return 201, `{"ip":{"id":"` + freshIPOne + `","address":"10.0.0.1"}}`
			}
			return 201, `{"ip":{"id":"` + freshIPTwo + `","address":"10.0.0.2"}}`
		}
		return 201, server
	}
}

// The other half of the same rule, and the one that stops a later version from
// comparing order everywhere and inventing a contract the cloud never stated.
//
// Two cases, and the second is the one a mutation can reach. "No declaration at
// all" is unfalsifiable by construction — the comparison loops over what the
// packs declare, so there is no guard to remove — while "declared for another
// operation" has a real one, and tools/falsify/specs/replay-compares.json
// removes it.
func TestAnOrderIsComparedOnlyWhereItWasDeclared(t *testing.T) {
	swapped := `{"server":{"id":"` + freshID + `","name":"web-1","commercial_type":"DEV1-S",` +
		`"public_ip":{"id":"` + freshIPOne + `","address":"10.0.0.1"},` +
		`"public_ips":[{"id":"` + freshIPTwo + `","address":"10.0.0.2"},{"id":"` + freshIPOne + `","address":"10.0.0.1"}]}}`
	undeclared := answers(t, ipsThenServer(swapped))
	if rep := run(t, ipOrderRecording(), undeclared.URL); rep.Divergent != 0 {
		t.Fatalf("a list nobody declared ordered was compared anyway: %+v", rep.Results)
	}

	// A second stand-in, not the first: ipsThenServer counts the reservations it
	// has answered, so a second run against the same one hands both addresses
	// the same identifier — both recorded identifiers then map onto one, the
	// order check cannot follow the sequence, and it evaluates nothing while
	// looking like a pass. Measured: the falsification of this very guard came
	// back STILL GREEN for that reason and for no other.
	elsewhereTS := answers(t, ipsThenServer(swapped))
	elsewhere := emulator.Invariant{
		// The same path, on an operation this exchange is not.
		Operation: "instance/v1/API.GetServer",
		Path:      "server.public_ips[].id",
		Kind:      emulator.InvariantOrder,
		Reason:    "Terraform stores public_ips as a list and a reordered read is a plan diff that never converges",
	}
	rep := run(t, ipOrderRecording(), elsewhereTS.URL, elsewhere)
	if rep.Divergent != 0 {
		t.Fatalf("an order declared for another operation was applied here: %+v", rep.Results)
	}
	if rep.Orders != 0 {
		t.Fatalf("%d order check(s) ran for an operation none was declared on", rep.Orders)
	}
}

// A value the request itself named has to come back. This is the unread-request
// -field family — an argument the API accepted and then ignored — and it is the
// one value comparison worth having.
func TestADeclaredValueThatDoesNotComeBackIsNamed(t *testing.T) {
	renamed := strings.Replace(freshServer, `"name":"web-1"`, `"name":"whatever-the-emulator-preferred"`, 1)
	ts := answers(t, func(*http.Request) (int, string) { return 201, renamed })
	value := emulator.Invariant{
		Operation: "instance/v1/API.CreateServer",
		Path:      "server.name",
		Kind:      emulator.InvariantValue,
		Reason:    "the client names the server in the request and the answer describes what was created",
	}
	rep := run(t, []trace.Exchange{exchange(t, createLine(recordedServer))}, ts.URL, value)
	if !namesFinding(rep, replay.KindValue, "server.name") {
		t.Fatalf("a name the request carried and the answer dropped produced no value finding: %+v",
			rep.Results[0].Findings)
	}
}

// The rebinding, asserted where it happens: the recorded read addresses the
// identifier the recording's create minted, and what goes out addresses the one
// this emulator minted. Without it the identity case of #73 is unreachable —
// every read of a fresh emulator would answer 404.
func TestARecordedIdentifierIsReboundBeforeTheRequestGoesOut(t *testing.T) {
	var asked []string
	ts := answers(t, func(r *http.Request) (int, string) {
		asked = append(asked, r.URL.Path)
		if r.Method == "POST" {
			return 201, freshServer
		}
		return 200, freshServer
	})
	get := `{"method":"GET","path":"` + serverPath + `/` + recordedID + `","operation":"instance/v1/API.GetServer",` +
		`"status":200,"mounted":true,"res":{"body":` + recordedServer + `}}`
	rep := run(t, []trace.Exchange{
		exchange(t, createLine(recordedServer)),
		exchange(t, get),
	}, ts.URL)

	if len(asked) != 2 {
		t.Fatalf("the emulator saw %d request(s), want 2", len(asked))
	}
	if strings.Contains(asked[1], recordedID) {
		t.Fatalf("the read went out addressing the recorded identifier %q: nothing was rebound", asked[1])
	}
	if !strings.HasSuffix(asked[1], freshID) {
		t.Fatalf("the read addressed %q, expected the identifier this emulator minted", asked[1])
	}
	if rep.Rebound == 0 {
		t.Fatalf("the report claims no identifier was rebound, and two were")
	}
}

// An operation no route serves is a work item, not a failure. Asserted
// explicitly, because the day it counts as a failure is the day somebody stops
// recording.
func TestAnUnservedOperationIsNotADivergence(t *testing.T) {
	var reached int
	ts := answers(t, func(*http.Request) (int, string) { reached++; return 200, `{}` })
	line := `{"method":"POST","path":"/lb/v1/zones/fr-par-1/lbs","status":200,"mounted":false,"res":{"body":{"lb":{"id":"x"}}}}`
	rep := run(t, []trace.Exchange{exchange(t, line)}, ts.URL)
	if rep.Unserved != 1 || rep.Divergent != 0 {
		t.Fatalf("unserved %d divergent %d, want 1 and 0", rep.Unserved, rep.Divergent)
	}
	if reached != 0 {
		t.Fatalf("the request went out anyway: an unserved route must not be driven, or the emulator's own "+
			"conformance record gains an exchange nobody made (%d call(s))", reached)
	}
}

// A recording is the inventory of a real account (docs/proxy.md). Nothing that
// comes out of this package may republish one, so the assertion is on the
// rendered report and not on an intention.
func TestNoFindingCarriesAValueFromTheRecording(t *testing.T) {
	// Every distinctive string of the recording, including the ones a naive
	// diff would print: the identifiers, the addresses, the machine name.
	secrets := []string{recordedID, recordedIPOne, recordedIPTwo, "203.0.113.1", "203.0.113.2", "web-1"}
	ts := answers(t, func(*http.Request) (int, string) {
		return 200, `{"server":{"id":"` + freshID + `","commercial_type":42,` +
			`"public_ips":[{"id":"` + freshIPTwo + `"},{"id":"` + freshIPOne + `"}]}}`
	})
	rep := run(t, []trace.Exchange{exchange(t, createLine(recordedServer))}, ts.URL,
		emulator.Invariant{Operation: "instance/v1/API.CreateServer", Path: "server.name",
			Kind: emulator.InvariantValue, Reason: "the client names the server and the answer describes what was created"},
		emulator.Invariant{Operation: "instance/v1/API.CreateServer", Path: "server.public_ips[].id",
			Kind: emulator.InvariantOrder, Reason: "Terraform stores this as a list and a reordered read never converges"})

	if rep.Divergent != 1 {
		t.Fatalf("the fixture is meant to diverge; it reported %d divergence(s)", rep.Divergent)
	}
	rendered, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if strings.Contains(string(rendered), secret) {
			t.Errorf("the report republishes %q, which it read out of the recording", secret)
		}
	}
}

// A recorded exchange with no answer is counted out loud rather than dropped.
// A total that shrinks in silence is the failure `feint shapes --check` already
// has a paragraph about.
func TestAnExchangeWithNoRecordedAnswerIsCountedNotDropped(t *testing.T) {
	ts := answers(t, func(*http.Request) (int, string) { return 201, freshServer })
	line := `{"method":"POST","path":"` + serverPath + `","operation":"instance/v1/API.CreateServer","status":201,"mounted":true}`
	rep := run(t, []trace.Exchange{exchange(t, line)}, ts.URL)
	if rep.Skipped != 1 || len(rep.Results) != 1 {
		t.Fatalf("skipped %d over %d result(s), want 1 and 1", rep.Skipped, len(rep.Results))
	}
	if rep.Matched != 0 || rep.Divergent != 0 {
		t.Fatalf("an exchange with nothing to compare was counted as matched or divergent")
	}
}

// A field a pack declines is excused with its reason and does not redden the
// run — the same doctrine `feint shapes --check` applies one level up — and it
// is still printed, so what the verdict subtracts stays visible.
func TestADeclinedFieldIsExcusedAndStillReported(t *testing.T) {
	ts := answers(t, func(*http.Request) (int, string) {
		return 201, `{"server":{"id":"` + freshID + `","name":"web-1","commercial_type":"DEV1-S",` +
			`"public_ips":[{"id":"` + freshIPOne + `","address":"10.0.0.1"},{"id":"` + freshIPTwo + `","address":"10.0.0.2"}]}}`
	})
	rep, err := replay.Run(context.Background(), []trace.Exchange{exchange(t, createLine(recordedServer))}, replay.Options{
		Endpoint: ts.URL,
		Client:   &http.Client{Timeout: 5 * time.Second},
		Table:    table(t),
		Declined: []emulator.FieldDecline{{
			Operation: "instance/v1/API.CreateServer",
			Path:      "server.public_ip",
			Reason:    "the first address is served through public_ips, and a second copy of it would go stale the moment one is attached elsewhere",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Divergent != 0 {
		t.Fatalf("a declined field reddened the run: %+v", rep.Results[0].Findings)
	}
	if rep.Excused != 1 {
		t.Fatalf("excused %d, want 1: what the verdict subtracts has to stay visible", rep.Excused)
	}
}

// A subtree this emulator does not serve is named once, at its root. The first
// version flattened both documents into path sets, and one catalogue answering
// four commercial types against a recording holding ninety produced several
// hundred lines, none of which was a distinct decision.
func TestAMissingSubtreeIsNamedOnceAtItsRoot(t *testing.T) {
	ts := answers(t, func(*http.Request) (int, string) {
		return 201, `{"server":{"id":"` + freshID + `","name":"web-1","commercial_type":"DEV1-S"}}`
	})
	rep := run(t, []trace.Exchange{exchange(t, createLine(recordedServer))}, ts.URL)
	if got := absentFindings(rep); got != 2 {
		t.Fatalf("%d absent finding(s), want 2 (public_ip and public_ips, each once at its root): %+v",
			got, rep.Results[0].Findings)
	}
}

// The same rule on the branch that carries it: an emulator answering an empty
// list where the recording saw elements is named once, at the element, and
// never once per field of the element. The shape underneath was not observed,
// it was not omitted, and reporting it drowned every real finding the first
// time this was measured (nine operations flagged for "RouteTables[]: object").
func TestAnEmptyListIsNamedAtItsElementAndNotUnderIt(t *testing.T) {
	ts := answers(t, func(*http.Request) (int, string) {
		return 201, `{"server":{"id":"` + freshID + `","name":"web-1","commercial_type":"DEV1-S",` +
			`"public_ip":{"id":"` + freshIPOne + `","address":"10.0.0.1"},"public_ips":[]}}`
	})
	rep := run(t, []trace.Exchange{exchange(t, createLine(recordedServer))}, ts.URL)
	if got := absentFindings(rep); got != 1 {
		t.Fatalf("%d absent finding(s), want exactly 1 at server.public_ips[]: %+v",
			got, rep.Results[0].Findings)
	}
	if !namesFinding(rep, replay.KindAbsent, "server.public_ips[]") {
		t.Fatalf("the empty list is not named at its element: %+v", rep.Results[0].Findings)
	}
}

// The other half, at the reader: a value the recorder replaced has no recorded
// type, so comparing it would manufacture a divergence out of the proxy's own
// hygiene. The field's *presence* is still checked, and the skip is counted, for
// the reason an excused field is counted — what a verdict subtracts has to stay
// visible.
func TestARedactedValueIsNotComparedAndIsCounted(t *testing.T) {
	recorded := `{"server":{"id":"` + recordedID + `","name":"web-1","commercial_type":"DEV1-S",` +
		`"kms_key_id":"REDACTED"}}`
	ts := answers(t, func(*http.Request) (int, string) {
		return 201, `{"server":{"id":"` + freshID + `","name":"web-1","commercial_type":"DEV1-S",` +
			`"kms_key_id":null}}`
	})
	rep := run(t, []trace.Exchange{exchange(t, createLine(recorded))}, ts.URL)
	if rep.Divergent != 0 {
		t.Fatalf("a redacted value was compared as a string against a null: %+v", rep.Results[0].Findings)
	}
	if rep.Redacted != 1 {
		t.Fatalf("%d redacted field(s) counted, want 1: a comparison that skips in silence "+
			"reports \"all matched\" over a recording it half read", rep.Redacted)
	}

	// And the presence check still bites: a redacted field the emulator does not
	// serve at all is still absent, which is the half that must not be lost.
	absent := answers(t, func(*http.Request) (int, string) {
		return 201, `{"server":{"id":"` + freshID + `","name":"web-1","commercial_type":"DEV1-S"}}`
	})
	gap := run(t, []trace.Exchange{exchange(t, createLine(recorded))}, absent.URL)
	if !namesFinding(gap, replay.KindAbsent, "server.kms_key_id") {
		t.Fatalf("a redacted field the emulator omits entirely stopped being a finding: %+v",
			gap.Results[0].Findings)
	}
}

func absentFindings(rep replay.Report) int {
	n := 0
	for _, r := range rep.Results {
		for _, f := range r.Findings {
			if f.Kind == replay.KindAbsent {
				n++
			}
		}
	}
	return n
}

// mustExchange is exchange without a *testing.T, for the fixtures a helper
// builds. A malformed line here is a defect in this file, not in a run.
func mustExchange(line string) trace.Exchange {
	var x trace.Exchange
	dec := json.NewDecoder(strings.NewReader(line))
	dec.UseNumber()
	if err := dec.Decode(&x); err != nil {
		panic(err)
	}
	return x
}

func namesFinding(rep replay.Report, kind replay.FindingKind, path string) bool {
	for _, r := range rep.Results {
		for _, f := range r.Findings {
			if f.Kind == kind && f.Path == path {
				return true
			}
		}
	}
	return false
}

func findingOf(rep replay.Report, kind replay.FindingKind) replay.Finding {
	for _, r := range rep.Results {
		for _, f := range r.Findings {
			if f.Kind == kind {
				return f
			}
		}
	}
	return replay.Finding{}
}
