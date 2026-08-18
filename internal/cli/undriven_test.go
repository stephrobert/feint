package cli

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// An operation no client drives says why, and one a client drives says nothing.
//
// #174 counted fifty-three mounted operations that no official client had ever
// reached, and the debt it named was not the number: it was that nothing
// separated the ones a client could reach tomorrow from the ones no client path
// exists for. `instance/v1/API.UpdateServer` — every in-place change a
// `terraform apply` makes — sat in the same undifferentiated list as
// `osc/Client.ReadPublicIpRanges`, a read no fixture has a reason to call.
//
// The rule this enforces has two halves, and only the pair is a control:
//
//   - an operation the recorded run left undriven carries Route.Undriven, so
//     the next reading of coverage/evidence.json separates "no scenario yet"
//     from "no client path exists";
//   - an operation a client does drive carries none, because a reason that
//     outlives its cause reads exactly like a considered decision. The reasons
//     for `block/v1`'s write path were true for nine months and stopped being
//     true the day the Terraform fixture declared a scaleway_block_volume.
//
// The artefact is the input rather than a list written here, for the same
// reason every other figure in this repository is generated: a list would
// disagree with the measurement by the next regeneration, and it would be the
// list that people read.
func TestEveryUndrivenOperationSaysWhy(t *testing.T) {
	artefact, err := loadEvidenceArtefact(filepath.Join("..", "..", "coverage", "evidence.json"))
	if err != nil {
		t.Fatalf("read the evidence artefact: %v", err)
	}
	if artefact == nil || len(artefact.Operations) == 0 {
		t.Fatal("the evidence artefact is empty, so this test would pass while measuring nothing")
	}

	env := emulator.DefaultEnv()
	packs, err := packsFor(env)
	if err != nil {
		t.Fatalf("build the packs: %v", err)
	}
	srv, err := emulator.NewServer(env, packs...)
	if err != nil {
		t.Fatalf("build the emulator: %v", err)
	}

	var unexplained, stale []string
	checked := 0
	for _, route := range srv.AllRoutes() {
		if route.Operation == "" {
			continue
		}
		// An operation with no row is the freshness check's business
		// (TestEveryMountedOperationHasAnEvidenceRow), not this one: judging a
		// route the record has never seen would report a missing reason for
		// something nobody has had the chance to drive.
		ev, recorded := artefact.Operations[route.Operation]
		if !recorded {
			continue
		}
		checked++
		switch {
		case !ev.Driven && route.Undriven == "":
			unexplained = append(unexplained, route.Operation)
		case ev.Driven && route.Undriven != "":
			stale = append(stale, route.Operation+" — "+route.Undriven)
		}
	}
	if checked == 0 {
		t.Fatal("no mounted operation was compared against the record, so this test measured nothing")
	}

	sort.Strings(unexplained)
	sort.Strings(stale)
	if len(unexplained) > 0 {
		t.Errorf("%d of %d mounted operations were driven by no client in the recorded run and say why nowhere.\n"+
			"Either drive them from a conformance suite, or state the reason at the route (Route.Undriven).\n"+
			"Unexplained:\n  %s",
			len(unexplained), checked, strings.Join(unexplained, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("%d operation(s) carry a reason for not being driven, and a client drives them.\n"+
			"Remove Route.Undriven: a reason that outlived its cause is read as a decision.\n"+
			"Stale:\n  %s",
			len(stale), strings.Join(stale, "\n  "))
	}
}

// A reason reads on its own, in a report, next to an operation name.
//
// The same bar Decline.Reason is held to, and for the same reason: these lines
// are printed in docs/routes.md under the pack that owns them, so "see above",
// a bare noun, or a sentence that starts with a capital and ends with a full
// stop all make the page read like three people wrote it — which is what
// happened to the declines before they were folded into one shape.
func TestAnUndrivenReasonReadsOnItsOwn(t *testing.T) {
	env := emulator.DefaultEnv()
	packs, err := packsFor(env)
	if err != nil {
		t.Fatalf("build the packs: %v", err)
	}
	srv, err := emulator.NewServer(env, packs...)
	if err != nil {
		t.Fatalf("build the emulator: %v", err)
	}

	for _, route := range srv.AllRoutes() {
		reason := route.Undriven
		if reason == "" {
			continue
		}
		switch {
		case len(reason) < 30:
			t.Errorf("%s: %q is too short to say why a client cannot reach it", route.Operation, reason)
		case strings.HasSuffix(reason, "."):
			t.Errorf("%s: the reason ends with a full stop; it is printed inside a sentence", route.Operation)
		case strings.ToLower(reason[:1]) != reason[:1]:
			t.Errorf("%s: the reason starts with a capital; it is printed inside a sentence", route.Operation)
		}
	}
}
