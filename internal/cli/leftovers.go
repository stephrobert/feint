package cli

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/machine"
)

// A killed emulator leaves its labelled machines running, and until #135 its
// next life said nothing about them: the operator's next apply ran beside
// zombie machines holding addresses on emulator-owned bridges, and the policy
// — die, leave labelled leftovers; restart, ignore; clean, sweep — was only
// visible to whoever assembled it from four files. This is the one moment the
// policy can be observed cheaply, so it is where the emulator says it.
//
// Named, never adopted. The store that gave those machines meaning died with
// the previous process; resurrecting them from the runtime would trust state
// that no longer has an owner, which is the same mistake as trusting a
// restored snapshot without revalidating it.
//
// The notice keys on machines, not on networks or rule sets alone, and that is
// still right *here*: this fires on every start, and a warning that fires on
// every healthy restart is read by nobody.
//
// But the reason once written here for it was wrong, and #426 disproved it by
// measurement on 2026-08-24. It said an empty emulated network is plumbing,
// because "the next run reuses it under the same name or refuses the block
// conflict out loud". Neither half holds. The name is derived from the *new*
// resource's id (machine.NetworkName), so the next run never asks for the old
// name: it asks for a new name carrying the same block, and the runtime refuses
// that at the DHCP bind, minutes in, with "Address already in use" — a message
// that names the block and nothing that produced it. Measured three runs in a
// row of tools/conformance/stacks.sh, the first of which exited 0.
//
// So a leftover network is not plumbing, it is the next run's failure. What
// changed is where the question is asked rather than how loud this is: the
// doorstep — `feint clean --check`, refuseRuntimeLeftovers — refuses before a
// run starts, which is the one place the answer is actionable and cannot be
// mistaken for noise. Do not restore the old sentence here without re-running
// that measurement.

// reportLeftovers names the labelled machines a previous run left on the
// runtime. TestStartupNamesTheLeftoversItDidNotAdopt fails without it.
func reportLeftovers(rt machine.Runtime, log *slog.Logger) {
	// Bounded: a hung runtime must delay the listener by seconds, not hold it.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	left, asked, err := rt.Survey(ctx)
	if !asked {
		return
	}
	if err != nil {
		// Silence on error would be the defect this file exists to remove: an
		// operator who cannot be told "there are leftovers" must at least be
		// told "I could not look".
		log.Warn("could not look for a previous run's leftovers", "error", err)
		return
	}
	if len(left.Machines) == 0 {
		return
	}
	log.Warn("labelled machines from a previous run exist; nothing was adopted, `feint clean` removes them",
		"machines", strings.Join(left.Machines, " "),
		"networks", len(left.Networks),
		"rule_sets", len(left.Firewalls))
}
