package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/stephrobert/feint/internal/core/machine"
)

// The half of a sweep that is not a sweep (#455).
//
// Everything else this command does is removable by an ordinary command of the
// runtime, and the whole argument of `feint clean` is that the operator never
// has to reach past one. A state measured on the maintainer's station in August
// 2026 broke that: an OVN network whose peering named a network that had been
// deleted refused *every* management path, the peer delete that would repair it
// included, so the network, the rule set attached to it and the uplink they sat
// on were permanent by every means a user has. Two hours of hand work on the
// runtime's own database is what cleared it.
//
// Three consequences shape this file:
//
//   - The prevention is elsewhere and it is the real fix (internal/core/machine:
//     Prune and RemoveNetwork now drop the surviving half of a peering before
//     they delete a network, detach a rule set before deleting the network that
//     holds it, and never strip the uplink's routes). What is here is for the
//     stations already carrying the state.
//   - The report is unconditional. `feint clean --check` answered 0 in silence
//     for the whole duration of the block, because without --doorstep it only
//     asked about orphaned DHCP services. A check whose "all clear" does not
//     cover the objects the sweep handles reads as proof and is not one.
//   - The repair is not. It is behind `--force`, it names every row before it
//     removes one, and it removes a row only when the network that row belongs
//     to carries the label this emulator wrote. That last clause is the whole
//     of it: a --force able to reach a third party's peering row would be a
//     worse defect than the one it repairs.

// whyTrapped is the ledger's word for the third family, beside a leftover that
// was refused and one that survived a successful delete: an object no ordinary
// command of the runtime can remove at all. It is what makes "how often does
// this happen" answerable, which is the whole point of the ledger.
const whyTrapped = "beyond-an-ordinary-command"

// reportRuntimeTraps names what holds the runtime, and removes nothing. It
// returns how many it found, so the caller decides the exit code: this is a
// read, and a read must not choose what a command means.
//
// Distinguishes three outcomes rather than two, like every other reader here: a
// runtime that cannot be asked is not a clean one, and it says so instead of
// answering zero.
func reportRuntimeTraps(out io.Writer, led *ledger, driver machine.Driver) (int, error) {
	repairer, ok := driver.(machine.Repairer)
	if !ok {
		led.prose("the %s runtime cannot be asked what holds it, so nothing was asked\n", driver.Name())
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	traps, err := repairer.Traps(ctx)
	if err != nil {
		led.record(leftoverRecord{Kind: "survey", Name: driver.Name(), Attribution: "none",
			Stage: stageDoorstep, Why: whyUnreadable, Action: actionNone})
		return 0, fmt.Errorf("could not look at what holds the %s runtime, so this host cannot be called clean: %w",
			driver.Name(), err)
	}
	if len(traps) == 0 {
		led.prose("nothing on this runtime is beyond an ordinary sweep\n")
		return 0, nil
	}
	for _, trap := range traps {
		led.record(trapRecord(trap, stageDoorstep, actionReported))
		led.prose("%s %s: %s\n", trap.Kind, trap.Name, trap.Why)
	}
	if !led.asJSON {
		fmt.Fprintf(out, "\nNone of the above goes with an ordinary `incus` command, and the sweep cannot\n"+
			"reach it either. `feint clean --force` clears what it can, naming every row it\n"+
			"removes first; the rest is cleared by the sweep now that it knows the order.\n")
	}
	return len(traps), nil
}

// clearRuntimeTraps is `--force`: it says what it will remove, removes it, and
// says what went.
//
// The announcement is not politeness. What this touches is the runtime's own
// database, so the row is printed whole before it goes and again as it goes,
// and an operator who disagrees has everything needed to put it back.
func clearRuntimeTraps(out io.Writer, led *ledger, driver machine.Driver) error {
	repairer, ok := driver.(machine.Repairer)
	if !ok {
		return fmt.Errorf("--force has nothing to reach on the %s runtime: it cannot be asked what holds it",
			driver.Name())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	traps, err := repairer.Traps(ctx)
	if err != nil {
		led.record(leftoverRecord{Kind: "survey", Name: driver.Name(), Attribution: "none",
			Stage: stageSweep, Why: whyUnreadable, Action: actionNone})
		return fmt.Errorf("could not look at what holds the %s runtime, so nothing was forced: %w",
			driver.Name(), err)
	}
	var repairable []machine.Trap
	for _, trap := range traps {
		if trap.Repairable {
			repairable = append(repairable, trap)
		}
	}
	if len(repairable) == 0 {
		led.prose("nothing on this runtime needs more than the sweep, so --force removed nothing\n")
		return nil
	}

	if !led.asJSON {
		fmt.Fprintf(out, "\n--force will remove %d row(s) from the runtime's own database, through\n"+
			"`incus admin sql`, which is Incus' supported mechanism for exactly this.\n"+
			"Each row is printed whole so it can be put back:\n\n", len(repairable))
	}
	for _, trap := range repairable {
		led.record(trapRecord(trap, stageSweep, actionReported))
		led.prose("  %s %s: %s\n    %s\n", trap.Kind, trap.Name, trap.Why, trap.Row)
	}

	cleared, repairErr := repairer.Repair(ctx)
	for _, trap := range cleared {
		led.record(trapRecord(trap, stageSweep, actionRemoved))
		led.prose("removed %s %s\n", trap.Kind, trap.Name)
	}
	if repairErr != nil {
		return repairErr
	}
	led.prose("--force removed %d row(s) no ordinary command could reach\n", len(cleared))
	return nil
}

// trapRecord turns a trap into a ledger line.
//
// Its attribution is the label and never a name, because that is what entitled
// this process to act: the row was reached through the network it belongs to,
// and that network carries the mark EnsureNetwork wrote. A ledger line saying
// "name-prefix" here would record a permission nobody actually asked for.
func trapRecord(trap machine.Trap, stage, action string) leftoverRecord {
	return leftoverRecord{
		Kind:        trap.Kind,
		Name:        trap.Name,
		Attribution: "label:" + machine.LabelKey,
		Stage:       stage,
		Why:         whyTrapped,
		Action:      action,
		Row:         trap.Row,
	}
}
