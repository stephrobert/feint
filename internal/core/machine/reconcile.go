package machine

import (
	"context"

	"github.com/stephrobert/feint/internal/core/resource"
)

// Reconciling the host with a store that just changed under it (#853).
//
// `PUT /_feint/state` and `feint snapshot load` restore the control plane
// verbatim and touch nothing else, which is the gap #853 names: a resource
// that disappears from the store leaves its container running, and the API no
// longer mentions it. Nobody destroys it, and the next `incus list` is the
// first time anybody finds out.
//
// ## Why the destroying half lives here and the recreating half does not
//
// Destroying is the same gesture for every pack: take the machine the record
// names, check it is ours, remove it. Written three times it would be fixed
// once, which is the defect CLAUDE.md names and this repository has already
// paid for on the lifecycle.
//
// Recreating is NOT the same gesture. Starting a machine needs a Boot — the
// image, the login, the cloud-init a pack renders for its own product — and
// that is pack knowledge by construction. #853 hedges in the same place, with
// "missing checkpoint resources recreated where the runtime contract supports
// it", so the recreating half belongs to each pack and is bounded by what that
// pack can actually boot.
//
// ## Why it goes through Remove rather than the driver
//
// Remove answers the ownership question before the shape one: `Binding.ours`
// refuses a machine name outside this binding's prefix, and the driver's
// mustOwn reads the label the emulator itself wrote. A restored store is
// untrusted input — snapshot.go documents the format as meant to outlive its
// instance — so a reconciliation driven by names that came out of a file is
// exactly the path that must not shortcut either check.

// Reconciled reports what a reconciliation did, so a caller can say it rather
// than assume it.
type Reconciled struct {
	// Destroyed names the machines taken down because their record is gone.
	Destroyed []string
	// Skipped names records whose machine this binding does not own, or which
	// name no machine at all. Reported rather than silently passed over: a
	// record nobody could act on is what an operator needs to hear about.
	Skipped []string
}

// ReconcileGone destroys the machines of records that no longer exist.
//
// The input is what the store held BEFORE the restore and no longer holds. The
// caller computes it, because only the caller saw both sides, and it is a
// plain list of resources so that nothing about a provider reaches this
// package.
//
// A binding with no runtime destroys nothing and says so through Skipped: the
// metadata-only mode is the default, and a reconciliation that claimed to have
// removed containers nobody started would be the lie this project exists to
// avoid.
//
// There is no ownership branch here, and that absence is deliberate: Remove
// already asks the question, and `ours` already decided what the answer does
// — fall back to the derived name rather than refuse. A second guard beside a
// working one is how two answers to one question start disagreeing.
//
// TestReconcileDestroysOnlyTheMachinesItOwns fails without this.
func (b Binding) ReconcileGone(ctx context.Context, gone []*resource.Resource) Reconciled {
	var done Reconciled
	for _, res := range gone {
		if res == nil {
			continue
		}
		name := res.Runtime[b.RuntimeKey]
		if name == "" {
			// Nothing was ever started for it. Not an error and not a skip
			// worth reporting: a record with no machine is the ordinary case
			// for an address, a volume or a security group.
			continue
		}
		if b.driver == nil {
			// Metadata only, which is the default: nothing was started, so
			// nothing is destroyed and the caller is told rather than left to
			// read an empty Destroyed as a success.
			done.Skipped = append(done.Skipped, name)
			continue
		}
		// Remove answers the ownership question itself, and answers it the way
		// this package already decided: `ours` does not refuse a foreign name,
		// it falls back to the one the emulator could have created, because a
		// record may well have a machine and that is the only name it could
		// ever have had. An ownership branch here would have been a second
		// guard beside a working one, which is how two answers to one question
		// start disagreeing.
		b.Remove(ctx, res.ID, name)
		done.Destroyed = append(done.Destroyed, name)
	}
	return done
}
