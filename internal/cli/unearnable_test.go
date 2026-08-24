package cli

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/store"
)

// The controls behind Route.Unearnable, which is a declaration that an evidence
// axis can never be earned for an operation.
//
// A declaration like that is the most dangerous kind of entry in this
// repository: it removes work from a queue, it reads like a decision, and
// nothing about the sentence itself is checkable. Route.Undriven has the same
// hazard and carries the same answer — the claim is measured, the prose only
// explains it — and the guard on the stale half has already bitten twice.
//
// So there are two independent halves here, and neither is sufficient alone:
//
//   - the staleness half (TestAnUnearnableAxisIsNotAlreadyEarned) reads the
//     committed record and refuses a declaration for an axis the record says
//     was earned. It is what makes the declaration expire on its own.
//   - the cause half (the three tests below it) checks the mechanism each
//     declaration names, against the emulator or the provider's own contract,
//     never against a list kept here.

// unearnedRoutes are the mounted routes that declare an axis out of reach.
func unearnedRoutes(t *testing.T) []emulator.Route {
	t.Helper()
	srv, _, err := newServer(nil)
	if err != nil {
		t.Fatalf("build the emulator: %v", err)
	}
	var out []emulator.Route
	for _, r := range srv.AllRoutes() {
		if r.Operation != "" && len(r.Unearnable) > 0 {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		t.Skip("no route declares an unearnable axis, so these controls measure nothing")
	}
	return out
}

// A declaration for an axis the record says was earned is a stale excuse.
//
// This is the half that makes the mechanism safe to have at all. Without it,
// declaring an axis out of reach would be a way to empty the queue by writing
// prose: the thing #390 and #408 both refuse. With it, the moment a suite earns
// the axis the declaration goes red and has to be removed.
//
// It also catches the case the causes cannot: CreatePublicIp has exactly the
// one-field request CauseNoRefusableRequest describes, and it IS refusable,
// because the emulator holds a finite address block. The schema check would
// wave that through; this one does not.
func TestAnUnearnableAxisIsNotAlreadyEarned(t *testing.T) {
	artefact, err := loadEvidenceArtefact(filepath.Join("..", "..", "coverage", "evidence.json"))
	if err != nil {
		t.Fatalf("read the evidence artefact: %v", err)
	}
	if artefact == nil || len(artefact.Operations) == 0 {
		t.Fatal("the evidence artefact is empty, so this test would pass while measuring nothing")
	}

	var stale []string
	checked := 0
	for _, route := range unearnedRoutes(t) {
		ev, recorded := artefact.Operations[route.Operation]
		if !recorded {
			// An operation the record has never seen is the freshness check's
			// business, not this one.
			continue
		}
		for _, u := range route.Unearnable {
			checked++
			earned := false
			switch u.Axis {
			case emulator.ProvesBehaviour:
				earned = ev.Behaviour
			case emulator.ProvesNegative:
				earned = ev.Negative
			}
			if earned {
				stale = append(stale, route.Operation+" — "+u.Axis+": "+u.Reason)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no declaration was compared against the record, so this test measured nothing")
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("%d declaration(s) say an axis can never be earned, and the record says it was.\n"+
			"Remove Route.Unearnable there: a reason that outlived its cause is read as a decision.\n"+
			"Stale:\n  %s", len(stale), strings.Join(stale, "\n  "))
	}
}

// A no-store-touch claim is measured by driving the route, not by reading it.
//
// Both halves matter. Zero store events is the claim; a 2xx answer is what
// stops the claim from being true of a call that never ran, which is the
// vacuous-truth failure this repository has already paid for. A route that
// answers 4xx to an empty body cannot be measured this way and says so rather
// than passing.
func TestAnUnearnableNoStoreTouchIsMeasured(t *testing.T) {
	measured := 0
	for _, route := range unearnedRoutes(t) {
		claims := false
		for _, u := range route.Unearnable {
			if u.Cause == emulator.CauseNoStoreTouch {
				claims = true
			}
		}
		if !claims {
			continue
		}

		// A fresh emulator per route: a store another route touched would
		// report events this one did not make.
		srv, env, err := newServer(nil)
		if err != nil {
			t.Fatalf("build the emulator: %v", err)
		}
		var touched []string
		env.Store.Observe(func(ev store.Event) {
			touched = append(touched, ev.Action+" "+ev.Provider+"/"+ev.Kind)
		})
		ts := httptest.NewServer(srv.Handler())

		req, err := http.NewRequest(route.Method, ts.URL+route.Path, strings.NewReader("{}")) //nolint:noctx // test client
		if err != nil {
			t.Fatalf("%s: %v", route.Operation, err)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("%s: %v", route.Operation, err)
		}
		status := res.StatusCode
		_ = res.Body.Close()
		ts.Close()

		if status < 200 || status >= 300 {
			t.Errorf("%s declares it touches no store, and answered %d to an empty body: "+
				"the claim cannot be measured on a call that did not run", route.Operation, status)
			continue
		}
		if len(touched) > 0 {
			t.Errorf("%s declares it touches no store, and the store saw %d touch(es): %s",
				route.Operation, len(touched), strings.Join(touched, ", "))
			continue
		}
		measured++
	}
	if measured == 0 {
		t.Fatal("no no-store-touch declaration was driven, so this test measured nothing")
	}
}

// A nothing-to-refuse claim is checked against the provider's own contract.
//
// The operation's request schema must declare nothing but DryRun: a body of one
// boolean, where every value a client can send is valid and a true one is
// answered 200 by design. Read out of contracts/<provider>.json, which is the
// extraction of the provider's published API description, so the check moves
// when upstream moves.
func TestAnUnearnableNegativeHasNothingToRefuse(t *testing.T) {
	docs := map[string]*contract.Doc{}
	measured := 0
	for _, route := range unearnedRoutes(t) {
		claims := false
		for _, u := range route.Unearnable {
			if u.Cause == emulator.CauseNoRefusableRequest {
				claims = true
			}
		}
		if !claims {
			continue
		}

		provider := providerOfOperation(t, route.Operation)
		doc, loaded := docs[provider]
		if !loaded {
			var err error
			doc, err = contract.Load(filepath.Join("..", "..", "contracts", provider+".json"))
			if err != nil {
				t.Fatalf("load the %s contract: %v", provider, err)
			}
			docs[provider] = doc
		}

		op, _, found := doc.OperationFor(route.Operation)
		if !found {
			t.Errorf("%s declares nothing to refuse and the contract does not describe it", route.Operation)
			continue
		}
		schema, described := doc.Schemas[op.Request]
		if !described {
			t.Errorf("%s: the contract names request schema %q and does not define it", route.Operation, op.Request)
			continue
		}
		var extra []string
		for name := range schema.Properties {
			if name != "DryRun" {
				extra = append(extra, name)
			}
		}
		sort.Strings(extra)
		if len(extra) > 0 {
			t.Errorf("%s declares no supported client can compose a refusable request, and its schema %s "+
				"declares %s beside DryRun", route.Operation, op.Request, strings.Join(extra, ", "))
			continue
		}
		measured++
	}
	if measured == 0 {
		t.Fatal("no nothing-to-refuse declaration was checked, so this test measured nothing")
	}
}

// Every declaration names an axis a suite can claim and a cause a control
// checks. A cause nobody measures is a comment with a constant in front of it.
func TestAnUnearnableDeclarationNamesAKnownAxisAndCause(t *testing.T) {
	known := map[string]bool{
		emulator.CauseNoStoreTouch:       true,
		emulator.CauseNoDestruction:      true,
		emulator.CauseNoRefusableRequest: true,
	}
	for _, route := range unearnedRoutes(t) {
		for _, u := range route.Unearnable {
			if u.Axis != emulator.ProvesBehaviour && u.Axis != emulator.ProvesNegative {
				t.Errorf("%s declares axis %q; only %q and %q are claimed by a suite",
					route.Operation, u.Axis, emulator.ProvesBehaviour, emulator.ProvesNegative)
			}
			if !known[u.Cause] {
				t.Errorf("%s declares cause %q, which no control checks", route.Operation, u.Cause)
			}
		}
	}
}

// A reason reads on its own, next to an operation name, exactly as
// Route.Undriven's does: these lines print side by side in the same report.
func TestAnUnearnableReasonReadsOnItsOwn(t *testing.T) {
	for _, route := range unearnedRoutes(t) {
		for _, u := range route.Unearnable {
			reason := u.Reason
			switch {
			case len(reason) < 30:
				t.Errorf("%s: %q is too short to say why the axis is out of reach", route.Operation, reason)
			case strings.HasSuffix(reason, "."):
				t.Errorf("%s: the reason ends with a full stop; it is printed inside a sentence", route.Operation)
			case strings.ToLower(reason[:1]) != reason[:1]:
				t.Errorf("%s: the reason starts with a capital; it is printed inside a sentence", route.Operation)
			}
		}
	}
}

// providerOfOperation answers which mounted pack claims an operation, from the
// packs in process rather than from a prefix table: a table would be right
// today and silently wrong at the first product rename.
func providerOfOperation(t *testing.T, operation string) string {
	t.Helper()
	owners, _, err := operationOwners()
	if err != nil {
		t.Fatalf("build the ownership map: %v", err)
	}
	owner, ok := owners[operation]
	if !ok {
		t.Fatalf("%s is mounted and no pack claims it", operation)
	}
	return owner
}
