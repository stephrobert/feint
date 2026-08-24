package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/machine"
)

// The doorstep and the ledger of #426.
//
// What these hold, measured on 2026-08-24 rather than argued. Three runs of
// tools/conformance/stacks.sh under --vm incus: the first exited 0 and left
// three of this emulator's bridges, three of its rule sets and three dnsmasq on
// the host; the second and third then failed on "Address already in use" for
// the blocks those bridges hold. `feint clean --check` exited 0 the whole time,
// because it only ever asked about orphaned DHCP services, and those dnsmasq
// were not orphans — their networks were still there.

// sweptDriver is a runtime holding exactly what a test says it holds. It is
// a Surveyor and a Pruner, so both halves of a sweep can be driven.
type sweptDriver struct {
	machine.Noop
	left machine.Leftovers
	// keeps names the objects Prune reports as removed and does not remove,
	// which is the "success that left the object standing" shape.
	keeps bool
	// blind makes Survey fail, which must never read as a clean host.
	blind bool
}

func (d *sweptDriver) Name() string { return "surveying" }

func (d *sweptDriver) Survey(context.Context) (machine.Leftovers, error) {
	if d.blind {
		return machine.Leftovers{}, errors.New("the daemon did not answer")
	}
	return d.left, nil
}

func (d *sweptDriver) Prune(context.Context) (machine.Pruned, error) {
	pruned := machine.Pruned{
		Machines:  len(d.left.Machines),
		Networks:  len(d.left.Networks),
		Firewalls: len(d.left.Firewalls),
	}
	if !d.keeps {
		d.left = machine.Leftovers{}
	}
	return pruned, nil
}

// withDriver points the doorstep and the sweep at a runtime a test controls.
func withDriver(t *testing.T, d machine.Driver) {
	t.Helper()
	previous := resolveDriver
	resolveDriver = func(string, io.Writer) (machine.Driver, error) { return d, nil }
	t.Cleanup(func() { resolveDriver = previous })
}

// quietDHCP silences the real /proc scan: these tests are about runtime
// objects, and a leftover on the tester's own station would fail them for the
// station's reasons.
func quietDHCP(t *testing.T) {
	t.Helper()
	swapLeftoverSeams(t, func() ([]machine.DHCPLeftover, error) { return nil, nil }, machine.TerminateLeftover)
}

// TestTheDoorstepRefusesAHostHoldingAPreviousRunsNetwork is the requirement
// itself: a run may not start while the host still holds what an earlier one
// made, and it must say what it found and what to run.
//
// The witness is the second half. A doorstep that refuses everything would pass
// the first assertion and break every clean host, which is how a doorstep gets
// disarmed — so the same code path is driven against an empty runtime and must
// accept it.
func TestTheDoorstepRefusesAHostHoldingAPreviousRunsNetwork(t *testing.T) {
	quietDHCP(t)

	held := &sweptDriver{left: machine.Leftovers{
		Networks:  []string{"fnt-5df8d7080c7"},
		Firewalls: []string{"iso-fnt-5df8d7080c7"},
	}}
	withDriver(t, held)

	var out bytes.Buffer
	err := reportStuckLeftovers(&out, newLedger(&out, false, time.Now()), "incus")
	if err == nil {
		t.Fatal("the doorstep accepted a host still holding a network of an earlier run: " +
			"the run starts, takes minutes, and dies on \"Address already in use\" for that block (#426)")
	}
	// Named, not counted. "1 network" sends nobody anywhere.
	if !strings.Contains(out.String(), "fnt-5df8d7080c7") {
		t.Errorf("the refusal never named the network it found; it printed: %q", out.String())
	}
	// And the remedy, as one command with nothing to retype — the shape #375
	// measured the cost of getting wrong.
	if !strings.Contains(out.String(), "feint clean --vm incus") {
		t.Errorf("the refusal named no runnable remedy; it printed: %q", out.String())
	}

	// The accepting half, on the same path.
	withDriver(t, &sweptDriver{})
	var clean bytes.Buffer
	if err := reportStuckLeftovers(&clean, newLedger(&clean, false, time.Now()), "incus"); err != nil {
		t.Fatalf("the doorstep refused a runtime holding nothing: %v (%q)", err, clean.String())
	}
}

// TestTheDoorstepSaysItCouldNotLookRatherThanCallingTheHostClean is the third
// outcome every reader in this repository owes. A survey that failed and a
// survey that found nothing produce the same empty list, and reporting the
// first as the second is how an inventory once called a live account empty for
// forty minutes.
func TestTheDoorstepSaysItCouldNotLookRatherThanCallingTheHostClean(t *testing.T) {
	quietDHCP(t)
	withDriver(t, &sweptDriver{blind: true})

	var out bytes.Buffer
	led := newLedger(&out, true, time.Now())
	if err := reportStuckLeftovers(&out, led, "incus"); err == nil {
		t.Fatal("a runtime that could not be surveyed was reported as a clean host")
	}
	if why := whyOfLines(t, out.String()); !why[whyUnreadable] {
		t.Errorf("the ledger did not record that nothing could be read; it printed: %q", out.String())
	}
}

// TestTheSweepNamesWhatSurvivedItsOwnSuccessfulDelete holds the case the
// coordinator of #426 called the least visible, and it is the reason the sweep
// reads the host twice instead of trusting its own counts.
//
// The driver here reports three objects removed and removes none, which is
// exactly what was measured on the emulator itself the same week: DELETE on a
// private network answered 204 while `incus network list` still showed the
// bridge. Nothing in a return value can say that.
func TestTheSweepNamesWhatSurvivedItsOwnSuccessfulDelete(t *testing.T) {
	quietDHCP(t)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", "")

	withDriver(t, &sweptDriver{
		keeps: true,
		left:  machine.Leftovers{Networks: []string{"fnt-5df8d7080c7"}},
	})

	var out bytes.Buffer
	if err := clean([]string{"--vm", "incus", "--format", "json"}, &out); err != nil {
		t.Fatalf("clean: %v (%q)", err, out.String())
	}
	lines := ledgerLines(t, out.String())
	var survived *leftoverRecord
	for i := range lines {
		if lines[i].Why == whySurvived && lines[i].Name == "fnt-5df8d7080c7" {
			survived = &lines[i]
		}
	}
	if survived == nil {
		t.Fatalf("the sweep reported a network removed, the network is still there, and the ledger says nothing: "+
			"a destruction that answers success and leaves the object standing is invisible to every count "+
			"the remover produces (#426). It printed: %q", out.String())
	}
	// The four columns that make the line actionable rather than decorative.
	if survived.Kind != "network" {
		t.Errorf("kind %q, want network", survived.Kind)
	}
	if !strings.HasPrefix(survived.Attribution, "name-prefix:") {
		t.Errorf("attribution %q says nothing about how this run knows the object is ours", survived.Attribution)
	}
	if survived.Stage != stageSweep {
		t.Errorf("stage %q, want %q", survived.Stage, stageSweep)
	}
	if survived.Run == "" {
		t.Error("the line carries no run identity, so two runs cannot be counted apart")
	}

	// The witness: the same sweep against a runtime that really removes must
	// record no survivor. Without it this test would pass on code that labels
	// every object a survivor.
	withDriver(t, &sweptDriver{left: machine.Leftovers{Networks: []string{"fnt-5df8d7080c7"}}})
	var honest bytes.Buffer
	if err := clean([]string{"--vm", "incus", "--format", "json"}, &honest); err != nil {
		t.Fatalf("clean on a runtime that removes: %v", err)
	}
	for _, rec := range ledgerLines(t, honest.String()) {
		if rec.Why == whySurvived {
			t.Fatalf("a runtime that removed everything still produced a survivor line: %+v", rec)
		}
	}
}

// TestTheLedgerAnswersWhichMechanismProducesTheWaste is the question the ledger
// exists to make answerable by a command. It is asserted because a trace nobody
// can query is an artefact to keep up to date and nothing else.
func TestTheLedgerAnswersWhichMechanismProducesTheWaste(t *testing.T) {
	quietDHCP(t)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", "")

	withDriver(t, &sweptDriver{
		keeps: true,
		left: machine.Leftovers{
			Machines:  []string{"feint-scw-a"},
			Networks:  []string{"fnt-a", "fnt-b"},
			Firewalls: []string{"iso-fnt-a"},
		},
	})

	var out bytes.Buffer
	if err := clean([]string{"--vm", "incus", "--format", "json"}, &out); err != nil {
		t.Fatalf("clean: %v", err)
	}
	byKind := map[string]int{}
	for _, rec := range ledgerLines(t, out.String()) {
		if rec.Why == whySurvived {
			byKind[rec.Kind]++
		}
	}
	if byKind["network"] != 2 || byKind["machine"] != 1 || byKind["rule-set"] != 1 {
		t.Fatalf("the ledger cannot be grouped by kind: got %v, want 2 networks, 1 machine, 1 rule set", byKind)
	}
}

// ledgerLines decodes the JSON-Lines output, and fails on a line that is not
// one: a ledger that silently drops a malformed record undercounts exactly the
// objects it exists to count.
func ledgerLines(t *testing.T, out string) []leftoverRecord {
	t.Helper()
	var recs []leftoverRecord
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			// The command's own prose lines, which JSON mode leaves alone.
			continue
		}
		var rec leftoverRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("ledger line is not JSON: %q (%v)", line, err)
		}
		recs = append(recs, rec)
	}
	return recs
}

func whyOfLines(t *testing.T, out string) map[string]bool {
	t.Helper()
	seen := map[string]bool{}
	for _, rec := range ledgerLines(t, out) {
		seen[rec.Why] = true
	}
	return seen
}
