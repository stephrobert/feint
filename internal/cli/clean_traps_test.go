package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/machine"
)

// `feint clean --check` and the three states it answered 0 on (#455).
//
// The measurement it is held to: on a station carrying two OVN networks and two
// rule sets that no `incus` command could remove, this command exited 0 and
// said nothing about any of them, because without --doorstep it only ever asked
// about orphaned DHCP services. A check whose "all clear" does not cover the
// objects the sweep handles reads as proof and is not one.

// trappedDriver is a runtime holding states no ordinary command can clear. It
// is a Repairer and nothing else: the sweep half is covered by sweptDriver, and
// mixing the two would let a test pass on the wrong question.
type trappedDriver struct {
	machine.Noop
	traps []machine.Trap
	// blind makes the survey fail, which must never read as a clean host.
	blind bool
	// repaired records what Repair was asked to clear, so the announce-then-act
	// order can be asserted.
	repaired int
}

func (d *trappedDriver) Name() string { return "trapped" }

func (d *trappedDriver) Traps(context.Context) ([]machine.Trap, error) {
	if d.blind {
		return nil, errors.New("the daemon did not answer")
	}
	return d.traps, nil
}

func (d *trappedDriver) Repair(context.Context) ([]machine.Trap, error) {
	var cleared []machine.Trap
	for _, trap := range d.traps {
		if trap.Repairable {
			cleared = append(cleared, trap)
		}
	}
	d.repaired = len(cleared)
	return cleared, nil
}

// checkAgainst runs `feint clean --check` against a runtime a test controls.
func checkAgainst(t *testing.T, driver machine.Runtime) (string, error) {
	t.Helper()
	quietDHCP(t)
	withDriver(t, driver)

	var out bytes.Buffer
	err := reportStuckLeftovers(&out, newLedger(&out, false, time.Now()), "incus", false)
	return out.String(), err
}

// The three states, one test each, and each of them exits non-zero today only
// because this reports them: before the fix all three answered 0.

func TestCleanCheckReportsADanglingPeerRow(t *testing.T) {
	report, err := checkAgainst(t, machine.Use(&trappedDriver{traps: []machine.Trap{{
		Kind:       machine.TrapDanglingPeer,
		Name:       "fnt-c10fedc7f6c/fnt-e41278b8c3a",
		Why:        "its peering names network 2617, which no longer exists",
		Repairable: true,
		Row:        `{"id":401}`,
	}}}))
	if err == nil {
		t.Fatal("a host holding a peering row no command can remove was reported as ready")
	}
	if !strings.Contains(report, "fnt-c10fedc7f6c") {
		t.Errorf("the report never named the network it found:\n%s", report)
	}
	// And the remedy, as one command with nothing to retype — the shape #375
	// measured the cost of getting wrong.
	if !strings.Contains(err.Error()+report, "clean --force") {
		t.Errorf("the refusal named no runnable remedy:\n%s\n%v", report, err)
	}
}

func TestCleanCheckReportsAStrippedUplink(t *testing.T) {
	report, err := checkAgainst(t, machine.Use(&trappedDriver{traps: []machine.Trap{{
		Kind: machine.TrapStrippedUplink,
		Name: "fnt-ad48c26e025",
		Why:  "its block 10.2.2.0/24 is no longer delegated to the uplink feint-uplink",
	}}}))
	if err == nil {
		t.Fatal("a host whose uplink lost the block of a network still standing was reported as ready")
	}
	if !strings.Contains(report, "10.2.2.0/24") {
		t.Errorf("the report never named the block that is missing:\n%s", report)
	}
}

func TestCleanCheckReportsARuleSetHeldByATrappedNetwork(t *testing.T) {
	report, err := checkAgainst(t, machine.Use(&trappedDriver{traps: []machine.Trap{{
		Kind: machine.TrapHeldFirewall,
		Name: "iso-fnt-c10fedc7f6c",
		Why:  "it is attached to fnt-c10fedc7f6c, which is trapped",
	}}}))
	if err == nil {
		t.Fatal("a rule set neither the network nor the sweep can release was reported as ready")
	}
	if !strings.Contains(report, "iso-fnt-c10fedc7f6c") {
		t.Errorf("the report never named the rule set:\n%s", report)
	}
}

// TestCleanCheckStaysQuietOnARuntimeNothingHoldsBeyondItsSweep is the accepting
// half, and it is the one that decides whether this check survives contact with
// a run.
//
// This command is called mid-run by three network suites. A check that refused
// on a healthy runtime would fail every one of them, and the reflex it would
// teach is the reflex `--no-verify` teaches. So the same code path is driven
// against a runtime holding nothing beyond its sweep, and it must accept — and
// say so out loud, because "checked and fine" and "never looked" must not read
// the same.
func TestCleanCheckStaysQuietOnARuntimeNothingHoldsBeyondItsSweep(t *testing.T) {
	report, err := checkAgainst(t, machine.Use(&trappedDriver{}))
	if err != nil {
		t.Fatalf("a runtime holding nothing beyond its sweep was refused: %v\n%s", err, report)
	}
	if !strings.Contains(report, "beyond an ordinary sweep") {
		t.Errorf("the check passed in silence:\n%s", report)
	}
}

// A runtime that cannot be asked is not an empty one. Three outcomes, never two.
func TestCleanCheckRefusesARuntimeItCannotAsk(t *testing.T) {
	report, err := checkAgainst(t, machine.Use(&trappedDriver{blind: true}))
	if err == nil {
		t.Fatalf("a runtime that answered nothing was reported as a clean host:\n%s", report)
	}
}

// TestForceNamesEveryRowBeforeItRemovesIt is the operator's half of a repair
// that reaches the runtime's own database: what is about to go is printed
// whole, so the person who runs it can put it back.
func TestForceNamesEveryRowBeforeItRemovesIt(t *testing.T) {
	quietDHCP(t)
	driver := &trappedDriver{traps: []machine.Trap{
		{
			Kind: machine.TrapDanglingPeer, Name: "fnt-c10fedc7f6c/fnt-e41278b8c3a",
			Why: "its target network 2617 no longer exists", Repairable: true,
			Row: `{"id":401,"network_id":2616,"target_network_id":2617}`,
		},
		{
			Kind: machine.TrapStrippedUplink, Name: "fnt-ad48c26e025",
			Why: "its block is no longer delegated",
		},
	}}
	withDriver(t, machine.Use(driver))

	var out bytes.Buffer
	if err := clearRuntimeTraps(&out, newLedger(&out, false, time.Now()), machine.Use(driver)); err != nil {
		t.Fatalf("--force: %v\n%s", err, out.String())
	}
	report := out.String()
	if !strings.Contains(report, `"target_network_id":2617`) {
		t.Errorf("the row was removed without being printed, so nothing can put it back:\n%s", report)
	}
	// Announced before it went, not after: the sentence naming the row must come
	// before the sentence saying it is gone.
	named := strings.Index(report, `"target_network_id":2617`)
	removed := strings.Index(report, "removed dangling-peer")
	if removed >= 0 && named > removed {
		t.Errorf("the row was announced only after it had gone:\n%s", report)
	}
	if driver.repaired != 1 {
		t.Errorf("--force cleared %d row(s), want the one that needs it", driver.repaired)
	}
	// The state the ordinary sweep repairs must not be dragged into the
	// privileged path: a --force that also does what the sweep does is a --force
	// people start typing by default.
	if strings.Contains(report, "fnt-ad48c26e025") {
		t.Errorf("--force claimed a state the sweep repairs on its own:\n%s", report)
	}
}

// --check and --force mean opposite things and a caller who typed both wants
// neither. Refused rather than resolved in one direction, because guessing here
// either removes a database row somebody meant to be shown or fails to remove
// one somebody meant to go.
func TestCleanRefusesCheckAndForceTogether(t *testing.T) {
	var out bytes.Buffer
	err := clean([]string{"--check", "--force"}, &out)
	if err == nil {
		t.Fatal("--check and --force were accepted together")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("the refusal does not say which flags disagree: %v", err)
	}
}
