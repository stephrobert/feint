package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/machine"
)

// An emulator that leaves its machines behind is worse than one that starts
// none. Everything it creates on the runtime carries a label, so this removes
// exactly its own work: an operator's own instances and bridges are never
// touched, whatever their names.
//
// It exists as a command because the situation it fixes is one a user lands in
// without doing anything wrong, a killed process being enough, and because the
// conformance suite needs the same sweep and should not reimplement it in shell.
func clean(args []string, stdout io.Writer) error {
	fs := newFlagSet("clean")
	vm := fs.String("vm", "incus", "machine runtime to sweep: incus, incus-vm, incus-ovn")
	check := fs.Bool("check", false, "report what this user cannot remove and remove nothing; exit 1 if anything is stuck")
	format := fs.String("format", "text", "output format: text, or json for one aggregatable line per object found")
	doorstep := fs.Bool("doorstep", false, "also refuse a machine or network of an earlier run; only true before a run starts, because a run in flight owns both")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *format != "text" && *format != "json" {
		return fmt.Errorf("unknown format %q: text or json", *format)
	}
	led := newLedger(stdout, *format == "json", time.Now())
	if *check {
		return reportStuckLeftovers(stdout, led, *vm, *doorstep)
	}

	// The state directories go first, and deliberately before the runtime is
	// even resolved: they are swept on every station, including the ones with no
	// machine runtime installed at all. Put after the driver, this would never
	// run for anybody using --vm off, which is the default and the majority.
	//
	// TestCleanReapsWithoutAMachineRuntime fails without this ordering.
	reaped, err := reapStaleInstances()
	if reaped > 0 {
		led.prose("removed %d stale instance record(s)\n", reaped)
	}
	if err != nil {
		return err
	}

	driver, err := resolveDriver(*vm, stdout)
	if err != nil {
		return err
	}
	pruner, ok := driver.(machine.Pruner)
	if !ok {
		// --vm off has no runtime, so there is nothing to sweep and that is not a
		// failure. It became worth distinguishing when this command started
		// collecting instance records as well: an operator with no machine
		// runtime — the default, and the majority — did the work, saw it
		// reported, and still got exit 1. A success that exits like a failure is
		// the ambiguity this project refuses everywhere else.
		//
		// TestCleanSucceedsWithNoRuntimeToSweep fails without this.
		if _, noRuntime := driver.(machine.Noop); noRuntime {
			// The same sentence as the swept case, because it is just as true
			// with no runtime and network.sh asserts on it. An early return that
			// stayed silent would make the line depend on the mode, and a caller
			// checking it would read "no runtime" as "dirty runtime".
			led.prose("nothing was left behind on the runtime\n")
			// Still swept: the leftover is a process, not a runtime object, so
			// it is findable and endable with no runtime answering at all.
			return sweepLeftoverDHCP(stdout, led, *vm)
		}
		return fmt.Errorf("the %s runtime cannot be swept", driver.Name())
	}

	// Read before, so the sweep can be judged on the host rather than on its own
	// return value. #426 is why: a destruction that answers success and leaves
	// the object standing is invisible to every count the remover produces, and
	// that is the shape this repository has now met twice.
	ctx := context.Background()
	before, surveyable, surveyErr := surveyRuntime(ctx, driver)
	if surveyErr != nil {
		// Never an empty list on a failed read. "I could not look" and "there is
		// nothing" are different facts, and reporting the first as the second is
		// how an inventory once called a live account empty.
		led.record(leftoverRecord{Kind: "survey", Name: driver.Name(), Attribution: "none",
			Stage: stageSweep, Why: whyUnreadable, Action: actionNone})
	}

	pruned, err := pruner.Prune(ctx)
	// Reported either way: a partial sweep still removed something, and saying
	// what went is what tells the operator whether to look further.
	led.prose("removed %d machine(s), %d network(s), %d rule set(s)\n",
		pruned.Machines, pruned.Networks, pruned.Firewalls)
	if err != nil {
		led.recordAll(before, stageSweep, whyRefused, actionNone)
		return err
	}
	// And read again. Anything still standing was asked to go, said nothing, and
	// is still there: the case no return code reveals.
	// TestTheSweepNamesWhatSurvivedItsOwnSuccessfulDelete fails without this.
	if surveyable && surveyErr == nil {
		after, _, afterErr := surveyRuntime(ctx, driver)
		switch {
		case afterErr != nil:
			led.record(leftoverRecord{Kind: "survey", Name: driver.Name(), Attribution: "none",
				Stage: stageSweep, Why: whyUnreadable, Action: actionNone})
		default:
			led.recordAll(survivors(before, after), stageSweep, whySurvived, actionNone)
		}
	}
	// This line is about the runtime and nothing else, and it stays that way.
	// tools/conformance/scaleway/network.sh asserts on it to decide whether the
	// runtime is clean; making it depend on the instance records too would have
	// it report a dirty runtime because a directory was collected, which is a
	// different question with a different answer.
	if pruned.Total() == 0 {
		led.prose("nothing was left behind on the runtime\n")
	}
	// After the prune, deliberately: a leftover whose network object still
	// exists is reaped by the runtime with that object, and only what survives
	// the ordinary sweep is a process the runtime no longer knows about.
	return sweepLeftoverDHCP(stdout, led, *vm)
}

// The seams doctor and the tests share. Wired to internal/core/machine, and
// replaced in tests because the real scan reads the tester's own /proc: a
// clean test run on a dirty host would fail for the host's reasons, and the
// refusal half needs a foreign process no test should go create.
var (
	findLeftoverDHCP = machine.LeftoverDHCP
	endLeftoverDHCP  = machine.TerminateLeftover
	canEndLeftover   = machine.CanEndLeftover
	// resolveDriver is the same seam one level up, so the doorstep of #426 can
	// be driven against a runtime holding known objects. Without it the test
	// would have to create real bridges on the tester's host to assert that a
	// run refuses to start beside them.
	resolveDriver = machineDriver
)

// reportStuckLeftovers is `feint clean --check` (#375): the question the sweep
// answers by doing, asked without doing it.
//
// The measurement behind it. The runtime leg of `mise run evidence:update`
// failed three times in a row, in the same place each time: the sweep found a
// dnsmasq whose interface had outlived its network, named it exactly, printed
// `sudo kill <pid>` — and exited 1, because the process belongs to the incus
// user and this one may not signal it. Every run died there until a human read
// the log and typed the line. A gate whose only remedy is a manual step
// somebody has to notice is a gate that gets worked around, which is how
// `--no-verify` is learned.
//
// What it deliberately is not is an escalation. A conformance suite that
// acquired the right to end a daemon it did not start would be a worse defect
// than the one it works around, and it is the question mustOwn asks of the
// driver, one layer up: a process nobody here created is not ours to end. So
// this reports, and the elevation is a command the operator runs, on purpose,
// with their own hands.
//
// The sentences below say "a run" and not "an earlier run", and the difference
// was measured on 2026-08-21. #316 found these as debris of a previous run;
// the machines-on leg of `mise run evidence:update` produced one of its own,
// mid-run, in bridge mode: a network it had created minutes earlier went
// unmanaged in the runtime while its bridge and its dnsmasq stayed up, and the
// emulator's own log names the moment ("open
// /var/lib/incus/networks/<name>/dnsmasq.raw: no such file or directory").
// Two consequences worth carrying: telling the operator to look for an earlier
// run would send them to the wrong place, and no doorstep can prevent what the
// run itself creates — what this makes cheap is the diagnosis and the remedy,
// not the birth of the leftover.
//
// Three verdicts rather than two, because "nothing found" and "found, and you
// can clear it yourself" are different facts with different remedies:
//
//   - nothing at all: said out loud, exit 0. A check silent on the case it
//     exists for reads as green when it never ran.
//   - found, and this user may end them: named, exit 0. The ordinary sweep
//     clears them, and every suite runs it before it starts; refusing here
//     would be a doorstep that fires on a host nothing was going to fail on,
//     which is how a doorstep gets disarmed.
//   - found, and this user may not: named with their pid, the one command
//     named too, exit 1.
//
// TestCleanCheckRefusesAHostWhoseLeftoverThisUserCannotEnd and
// TestCleanCheckPassesWhenTheSweepItselfWouldClearThem fail without it.
func reportStuckLeftovers(stdout io.Writer, led *ledger, vm string, doorstep bool) error {
	// The runtime half first, and #426 is why it exists at all.
	//
	// Before this, the doorstep asked one question — is there a DHCP service
	// this user cannot end — and answered "no" on a host holding three of this
	// emulator's bridges, three of its rule sets and the three dnsmasq those
	// bridges own. Those dnsmasq are not orphans: their networks are still
	// there, so the orphan scan is right to ignore them, and the run started
	// anyway and died thirty steps later on "Address already in use".
	//
	// It also disproves what internal/cli/leftovers.go says about networks, and
	// the disproof is written there too: a leftover emulated network is *not*
	// reused by the next run. The name is derived from the new resource's id, so
	// the next run asks for a different name carrying the same block, and the
	// runtime refuses it — measured on 2026-08-24, twice in a row, after a run
	// that had exited 0.
	//
	// Asked only before a run starts, and that boundary is the whole of #426's
	// second half. `guard_leftovers` is called from two places: once by
	// `mise run conformance` before `feint start`, and again mid-run by each
	// network suite. A DHCP orphan is debris by construction, so asking about it
	// is safe in both. A machine and a network are not: mid-run they are the
	// running emulator's own, and refusing on them fails a run for owning what
	// it just created — measured on 2026-08-24, when leg 2 of
	// `mise run evidence:update` died naming `fnt-default` and an Outscale VM
	// the same run had booted minutes earlier. That is the "doorstep that fires
	// on a host nothing was going to fail on" this file already warns about, and
	// it is how a doorstep gets disarmed.
	//
	// TestTheDoorstepRefusesAHostHoldingAPreviousRunsNetwork and
	// TestTheLeftoverCheckMidRunIgnoresTheRunsOwnObjects fail without this.
	if doorstep {
		if err := refuseRuntimeLeftovers(stdout, led, vm); err != nil {
			return err
		}
	}

	leftovers, err := findLeftoverDHCP()
	if err != nil {
		return fmt.Errorf("could not look for leftover DHCP services: %w", err)
	}
	if len(leftovers) == 0 {
		led.prose("no DHCP service of this emulator's outlives its network on this host\n")
		return nil
	}
	var stuck []machine.DHCPLeftover
	for _, leftover := range leftovers {
		if err := canEndLeftover(leftover); err != nil {
			// The reason is relayed rather than named, because the probe knows
			// it and this does not. Permission is the case that matters — the
			// runtime starts its own DHCP services under the incus account —
			// but a pid the scan no longer attributes answers here too, and
			// calling that "another user" would be a sentence nobody measured.
			led.record(dhcpRecord(leftover, stageDoorstep, whySurvived, actionLeftStuck))
			led.prose("a DHCP service a run left behind, which this user cannot end: %s\n", leftover)
			led.prose("  %v\n", err)
			stuck = append(stuck, leftover)
			continue
		}
		led.record(dhcpRecord(leftover, stageDoorstep, whySurvived, actionReported))
		led.prose("a DHCP service a run left behind, which this user can end: %s\n", leftover)
	}
	if len(stuck) == 0 {
		led.prose("  → feint clean --vm %s ends them\n", vm)
		return nil
	}
	for _, leftover := range stuck {
		if leftover.InterfaceAlive {
			// Named here as well as in the sweep, because it is the second half
			// of the remedy and the operator is reading this one: ending the
			// service leaves the bridge holding the same address, and nothing
			// on the host proves this emulator created that bridge.
			led.prose("  the bridge %s survived its network too, and nothing proves this emulator created it — `sudo ip link delete %s` if it is yours\n",
				leftover.Interface, leftover.Interface)
		}
	}
	led.prose("  → sudo feint clean --vm %s\n", vm)
	return fmt.Errorf("%d DHCP service(s) left behind by a run hold their block and this user cannot end them; "+
		"nothing here may signal what it does not own", len(stuck))
}

// sweepLeftoverDHCP ends the DHCP services a run left holding an
// address block (#316, #342): dnsmasq processes attributable to this emulator
// whose network is gone — the interface with it, or the network object alone.
// They are invisible to `ip addr` and to the runtime's own listings — only
// `ss -lnp` shows them — and the block they hold fails the next run minutes
// in on "Address already in use".
//
// Attribution comes first and lives in internal/core/machine: a process that
// cannot be attributed to this emulator is never named here, let alone
// signalled. The common refusal is not foreignness but permission — the
// runtime's dnsmasq belongs to the incus user, not the operator — and then
// the sweep reports the exact command instead of failing in silence.
//
// When the interface survived its network (#342), the sweep ends the service
// and says what it will not touch: the bridge left standing carries no label
// any more, so nothing on the host proves the emulator created it, and a
// bridge nobody here created is not ours to delete. Saying so beats deleting
// it and beats silence: the operator gets the exact command and the decision.
// TestCleanSaysWhatItWillNotTouchWhenTheBridgeSurvived fails without the line.
func sweepLeftoverDHCP(stdout io.Writer, led *ledger, vm string) error {
	leftovers, err := findLeftoverDHCP()
	if err != nil {
		// An operator who cannot be told "there is a leftover" must at least
		// be told "I could not look"; the sweep already ran, so the runtime
		// half of the report above stays valid either way.
		return fmt.Errorf("could not look for leftover DHCP services: %w", err)
	}
	var stuck []string
	for _, leftover := range leftovers {
		err := endLeftoverDHCP(leftover)
		switch {
		case err == nil:
			led.record(dhcpRecord(leftover, stageSweep, whySurvived, actionRemoved))
			led.prose("ended a DHCP service a run left behind: %s\n", leftover)
			if leftover.InterfaceAlive {
				led.prose("  left untouched: the bridge %s survived its network, and nothing proves this emulator created it — `sudo ip link delete %s` if it is yours\n",
					leftover.Interface, leftover.Interface)
			}
		case errors.Is(err, os.ErrPermission):
			led.record(dhcpRecord(leftover, stageSweep, whySurvived, actionLeftStuck))
			led.prose("a DHCP service a run left behind belongs to another user: %s\n", leftover)
			// Two commands, and the order matters (#375). `sudo kill` is exact
			// and demands that a pid be read out of a log and retyped, which is
			// the manual step that failed to happen three runs in a row; the
			// sweep re-run as root is one line, needs nothing copied, and
			// re-asks every ownership question at the moment of the signal, so
			// root ends only what this emulator can prove is its own.
			led.prose("  → sudo feint clean --vm %s   (or: sudo kill %d)\n", vm, leftover.PID)
			stuck = append(stuck, leftover.String())
		default:
			led.record(dhcpRecord(leftover, stageSweep, whySurvived, actionNone))
			led.prose("could not end a leftover DHCP service: %s: %v\n", leftover, err)
			stuck = append(stuck, leftover.String())
		}
	}
	if len(stuck) > 0 {
		// An exit 0 here would say "clean" about a host whose next run fails
		// at the bind, which is the ambiguity this command exists to remove.
		return fmt.Errorf("%d leftover DHCP service(s) still hold their block: %s; `sudo feint clean --vm %s` ends them",
			len(stuck), strings.Join(stuck, "; "), vm)
	}
	return nil
}
