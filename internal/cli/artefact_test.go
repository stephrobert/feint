package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/drift"
	"github.com/stephrobert/feint/internal/providers/exoscale"
)

// The committed coverage artefact repeats what the packs declare, and #298
// measured the cost of comparing the copy with nothing: the decline reason #260
// rewrote in internal/providers/scaleway/pack.go took four days to reach
// coverage/scaleway-coverage.json, so the versioned verdict for 24 operations
// stayed a sentence the code had already corrected — while drift:check passed,
// because the baseline compares operation names, and docs:check passed, because
// the README regenerates from the same stale artefact. Two gates agreeing with
// each other, both disagreeing with the code.
//
// This test is the comparison that was missing, on the source of record: every
// mounted pack, judged against its own committed artefact — reasons, statuses,
// and declines the artefact never recorded. It runs in `mise run check`, so a
// reason edited without `mise run drift:update` fails the prepush hook rather
// than surfacing as collateral in somebody else's regeneration.
//
// It iterates the packs rather than naming them: the defect is structural — any
// pack that edits a reason — and a control that names Scaleway is a control the
// fourth pack ships without. Which is also why a pack with no committed
// artefact fails rather than being skipped: skipping is how the fourth pack
// would never join the comparison.
//
// tools/falsify/specs/artefact-reasons.json proves it bites, by re-editing the
// very sentence #260 rewrote.
func TestTheCommittedArtefactCarriesWhatThePacksDeclare(t *testing.T) {
	env := emulator.DefaultEnv()
	packs, err := packsFor(env)
	if err != nil {
		t.Fatalf("build the packs: %v", err)
	}
	if len(packs) == 0 {
		t.Fatal("no pack mounted, so this test would pass while measuring nothing")
	}

	for _, p := range packs {
		path := filepath.Join("..", "..", "coverage", p.Name()+"-coverage.json")
		f, err := os.Open(path)
		if err != nil {
			t.Errorf("pack %q has no committed coverage artefact at %s; write it with `mise run drift:update` and put it under tools/drift/gate.sh", p.Name(), path)
			continue
		}
		committed, err := drift.LoadCoverage(f)
		_ = f.Close()
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			continue
		}
		if len(committed.Entries) == 0 {
			t.Errorf("%s carries no entries, so comparing it proves nothing", path)
			continue
		}

		var served []string
		for _, r := range p.Routes() {
			served = append(served, r.Operation)
		}
		declined := map[string]string{}
		for _, d := range p.Declined() {
			declined[d.Operation] = d.Reason
		}
		for _, line := range drift.ArtefactSkew(committed, served, declined) {
			t.Errorf("%s: %s (the artefact follows the code; regenerate it: mise run drift:update)", p.Name(), line)
		}
	}
}

// reasonEdited is a real pack with one decline reason rewritten — the exact
// move #260 made, replayed against the committed artefact.
type reasonEdited struct{ emulator.Pack }

func (p reasonEdited) Declined() []emulator.Decline {
	declines := p.Pack.Declined()
	out := make([]emulator.Decline, len(declines))
	copy(out, declines)
	out[0].Reason = "an edited sentence the committed artefact does not carry yet, which is the #298 defect replayed on purpose"
	return out
}

// coverage() must exit 2 when the committed artefact lags the pack, and this
// proves the call site rather than the comparison: ArtefactSkew has its own
// tests, and this repository has already watched an audit delete a guard's call
// site in coverage() while the guard's own test stayed green. packsFor is the
// seam, exactly as in TestCoverageRefusesAPackWithUnusableRefusals; the Exoscale
// pack is the subject because its upstream surface is a committed contract, so
// the test needs no SDK checkout and cannot skip.
func TestCoverageExitsTwoWhenTheArtefactLagsThePack(t *testing.T) {
	original := packsFor
	t.Cleanup(func() { packsFor = original })

	contractPath := filepath.Join("..", "..", "contracts", "exoscale.json")
	artefactPath := filepath.Join("..", "..", "coverage", "exoscale-coverage.json")
	args := []string{"--provider", "exoscale", "--contract", contractPath, "--artefact", artefactPath}

	packsFor = func(env *emulator.Env) ([]emulator.Pack, error) {
		return []emulator.Pack{reasonEdited{Pack: exoscale.New(env)}}, nil
	}
	var out, errOut strings.Builder
	if rc := coverage(args, &out, &errOut); rc != exitDrift {
		t.Fatalf("coverage exited %d on an artefact lagging its pack, want %d\nstderr: %s", rc, exitDrift, errOut.String())
	}
	if !strings.Contains(errOut.String(), "disagrees with what the pack declares") {
		t.Fatalf("the skew was not named: %q", errOut.String())
	}

	// And the accepting half: the untouched pack against the same artefact is
	// agreement, or the flag would only ever be a way to fail.
	packsFor = func(env *emulator.Env) ([]emulator.Pack, error) {
		return []emulator.Pack{exoscale.New(env)}, nil
	}
	out.Reset()
	errOut.Reset()
	if rc := coverage(args, &out, &errOut); rc != exitOK {
		t.Fatalf("coverage exited %d on a fresh artefact, want %d\nstderr: %s", rc, exitOK, errOut.String())
	}
}
