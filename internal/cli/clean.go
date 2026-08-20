package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

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
	if err := fs.Parse(args); err != nil {
		return err
	}

	// The state directories go first, and deliberately before the runtime is
	// even resolved: they are swept on every station, including the ones with no
	// machine runtime installed at all. Put after the driver, this would never
	// run for anybody using --vm off, which is the default and the majority.
	//
	// TestCleanReapsWithoutAMachineRuntime fails without this ordering.
	reaped, err := reapStaleInstances()
	if reaped > 0 {
		fmt.Fprintf(stdout, "removed %d stale instance record(s)\n", reaped)
	}
	if err != nil {
		return err
	}

	driver, err := machineDriver(*vm, stdout)
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
			fmt.Fprintln(stdout, "nothing was left behind on the runtime")
			// Still swept: the leftover is a process, not a runtime object, so
			// it is findable and endable with no runtime answering at all.
			return sweepLeftoverDHCP(stdout)
		}
		return fmt.Errorf("the %s runtime cannot be swept", driver.Name())
	}

	pruned, err := pruner.Prune(context.Background())
	// Reported either way: a partial sweep still removed something, and saying
	// what went is what tells the operator whether to look further.
	fmt.Fprintf(stdout, "removed %d machine(s), %d network(s), %d rule set(s)\n",
		pruned.Machines, pruned.Networks, pruned.Firewalls)
	if err != nil {
		return err
	}
	// This line is about the runtime and nothing else, and it stays that way.
	// tools/conformance/scaleway/network.sh asserts on it to decide whether the
	// runtime is clean; making it depend on the instance records too would have
	// it report a dirty runtime because a directory was collected, which is a
	// different question with a different answer.
	if pruned.Total() == 0 {
		fmt.Fprintln(stdout, "nothing was left behind on the runtime")
	}
	// After the prune, deliberately: a leftover whose network object still
	// exists is reaped by the runtime with that object, and only what survives
	// the ordinary sweep is a process the runtime no longer knows about.
	return sweepLeftoverDHCP(stdout)
}

// The seams doctor and the tests share. Wired to internal/core/machine, and
// replaced in tests because the real scan reads the tester's own /proc: a
// clean test run on a dirty host would fail for the host's reasons, and the
// refusal half needs a foreign process no test should go create.
var (
	findLeftoverDHCP = machine.LeftoverDHCP
	endLeftoverDHCP  = machine.TerminateLeftover
)

// sweepLeftoverDHCP ends the DHCP services an interrupted run left holding an
// address block (#316): dnsmasq processes whose fnt- interface no longer
// exists. They are invisible to `ip addr` and to the runtime's own listings —
// only `ss -lnp` shows them — and the block they hold fails the next run
// minutes in on "Address already in use".
//
// Attribution comes first and lives in internal/core/machine: a process that
// cannot be attributed to this emulator is never named here, let alone
// signalled. The common refusal is not foreignness but permission — the
// runtime's dnsmasq belongs to the incus user, not the operator — and then
// the sweep reports the exact command instead of failing in silence.
func sweepLeftoverDHCP(stdout io.Writer) error {
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
			fmt.Fprintf(stdout, "ended a DHCP service an interrupted run left behind: %s\n", leftover)
		case errors.Is(err, os.ErrPermission):
			fmt.Fprintf(stdout, "a DHCP service an interrupted run left behind belongs to another user: %s\n", leftover)
			fmt.Fprintf(stdout, "  → sudo kill %d\n", leftover.PID)
			stuck = append(stuck, leftover.String())
		default:
			fmt.Fprintf(stdout, "could not end a leftover DHCP service: %s: %v\n", leftover, err)
			stuck = append(stuck, leftover.String())
		}
	}
	if len(stuck) > 0 {
		// An exit 0 here would say "clean" about a host whose next run fails
		// at the bind, which is the ambiguity this command exists to remove.
		return fmt.Errorf("%d leftover DHCP service(s) still hold their block: %s",
			len(stuck), strings.Join(stuck, "; "))
	}
	return nil
}
