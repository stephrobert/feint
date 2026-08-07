package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

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
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
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
			return nil
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
	return nil
}
