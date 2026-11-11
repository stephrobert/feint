package machine

import (
	"context"
	"slices"

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
	// Recreated names the machines started again because a record the
	// checkpoint holds had none.
	Recreated []string
	// Failed names the records whose machine could not be started again.
	// Separate from Skipped, because a start that was attempted and refused
	// and a record nobody could act on need different answers from the
	// operator.
	Failed []string
	// Reapplied names the rule sets written to the runtime again because the
	// restore brought back the records they are translated from. Separate
	// from Recreated: nothing was started, a set that the runtime already
	// held under the same name was overwritten with the restored rules.
	Reapplied []string
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

// ReconcileMissing starts again the machines of records that have none.
//
// It is the other half of #853's "runtime reconciled to the checkpoint", and
// the issue bounds it in the same breath: "where the runtime contract supports
// it". What that turned out to be here is better than I expected, and it was
// measured rather than assumed.
//
// ## Why a rebuilt boot is the boot the original had
//
// A pack builds its Boot from three places, and a reset has already put all
// three back by the time this runs:
//
//   - the record's own attributes, which the restore brought back verbatim;
//   - the store, for things like the project's SSH keys, which the restore
//     brought back too — the keys are part of the checkpoint;
//   - the image catalogue, which is compiled into the binary and depends on no
//     state at all.
//
// So the fear that sequenced this half last — an image gone from the
// catalogue, a key deleted, a user data that no longer exists — does not
// survive reading where those values come from. What a label the catalogue
// does not know produces is an empty image reference, which is exactly what
// the ORIGINAL poweron produced for that same record.
//
// ## Why the Boot comes in as a function
//
// Building one is pack knowledge: the image table, the login, the cloud-init a
// pack renders for its own product. Taking a builder rather than a Boot keeps
// the loop, the ownership path and the honest reporting in one place while the
// one thing that genuinely varies stays with the pack. Each pack writes one
// line.
//
// ## What it refuses to call a success
//
// PowerOn already answers false when a runtime was configured and did not
// deliver, and sets FailedState rather than Running. This reports those as
// Failed instead of folding them into Recreated: a reset that said it brought
// three machines back when one of them is in FailedState would be the half-
// success this project exists to avoid, and the verdict above it compares the
// observable state anyway — so a lie here would be caught, loudly, in the
// wrong place.
//
// TestReconcileStartsOnlyTheMachinesThatAreMissing fails without this.
func (b Binding) ReconcileMissing(ctx context.Context, records []*resource.Resource,
	held []string, boot func(*resource.Resource) Boot,
) Reconciled {
	var done Reconciled
	for _, res := range records {
		if res == nil || boot == nil {
			continue
		}
		// Which of these records is missing a machine is decided HERE, with
		// this binding's own key, and not by the caller. The core tried to
		// guess it with a "feint-" prefix and was wrong twice: a provider's
		// vocabulary in the neutral layer, and a match broad enough to pick up
		// a test's bookkeeping stamp and try to boot it.
		name := res.Runtime[b.RuntimeKey]
		if name == "" || slices.Contains(held, name) {
			continue
		}
		// And the checkpoint must describe it as UP. A record keeps its
		// machine name after the machine is gone — that is how ReconcileGone
		// finds what to destroy — so "has a name and the host does not hold
		// it" is also, word for word, the description of a server the
		// checkpoint says is stopped. The runtime leg measured it: a reset
		// returned a terminated VM to running and started a container for it,
		// and the verdict then named the resource it had itself changed.
		//
		// The word is the pack's and the comparison is shared, which is the
		// field this binding already carries for PowerOn to write.
		//
		// TestAResetLeavesAStoppedRecordStopped fails without this.
		if res.State != b.RunningState {
			continue
		}
		if b.driver == nil {
			// Metadata only: PowerOn would answer running without starting
			// anything, which is the documented degraded mode and not
			// something to report as a recreation.
			done.Skipped = append(done.Skipped, b.Name(res.ID))
			continue
		}
		if !b.PowerOn(ctx, res, boot(res)) {
			done.Failed = append(done.Failed, b.Name(res.ID))
			continue
		}
		done.Recreated = append(done.Recreated, res.Runtime[b.RuntimeKey])
	}
	return done
}
