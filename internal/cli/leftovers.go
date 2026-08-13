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
// The notice keys on machines, not on networks or rule sets alone. An empty
// emulated network is plumbing: the next run reuses it under the same name or
// refuses the block conflict out loud, and the OVN uplink is deliberately
// kept across runs. A warning that fired on every healthy restart would be
// read by nobody, which this repository has already measured on its gates.

// reportLeftovers names the labelled machines a previous run left on the
// runtime. TestStartupNamesTheLeftoversItDidNotAdopt fails without it.
func reportLeftovers(driver machine.Driver, log *slog.Logger) {
	surveyor, ok := driver.(machine.Surveyor)
	if !ok {
		return
	}
	// Bounded: a hung runtime must delay the listener by seconds, not hold it.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	left, err := surveyor.Survey(ctx)
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
