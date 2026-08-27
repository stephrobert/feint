package machine

import "context"

// An emulator that leaves its machines behind is worse than one that starts
// none. The leftovers are not merely untidy: a bridge kept from a killed run
// holds a block the next run wants, and an instance whose network was swept
// underneath it lands in a state the runtime reports as ERROR. Both were
// observed on this project before this existed.
//
// Everything the emulator creates carries a label, so it can find its own work
// again without guessing from names, and without ever touching what an operator
// created. The Incus half is in incus_prune.go.

// LabelKey is the label every resource feint creates carries, so a sweep can
// tell its own work from an operator's.
const LabelKey = "feint.provider"

// Pruned counts what a sweep removed, so the caller can report it rather than
// claim success in silence.
type Pruned struct {
	Machines  int
	Networks  int
	Firewalls int
}

// Total reports whether anything was found at all.
func (p Pruned) Total() int { return p.Machines + p.Networks + p.Firewalls }

// Pruner is the optional half of a driver that can find and remove everything
// the emulator created through it.
type Pruner interface {
	// Prune removes the machines, networks and rule sets carrying the label,
	// in that order: a network cannot go while a machine sits on it.
	Prune(ctx context.Context) (Pruned, error)
}

// Leftovers names the labelled work a runtime still holds, without touching
// it. It is the read-only half of a sweep: what Prune would remove, found and
// named so a restart can say it out loud instead of serving beside it (#135).
type Leftovers struct {
	Machines  []string
	Networks  []string
	Firewalls []string
}

// Total reports whether anything was found at all.
func (l Leftovers) Total() int { return len(l.Machines) + len(l.Networks) + len(l.Firewalls) }

// Surveyor is the optional half of a driver that can name the emulator's
// labelled work without acting on it. A restart uses it to notice what a
// previous life left behind; adoption is deliberately not on offer, because
// the store that gave those objects meaning died with the process that
// created them.
type Surveyor interface {
	// Survey lists the machines, networks and rule sets carrying the
	// emulator's mark. It must issue no mutating command: naming is the whole
	// contract, TestSurveyFindsOnlyTheEmulatorsWorkAndTouchesNothing holds it.
	Survey(ctx context.Context) (Leftovers, error)
}

// UplinkReleaser is the optional half of a driver whose networks share one
// piece of host plumbing no resource delete will ever remove: the uplink.
// Every emulated resource goes when a client deletes it, so a run whose
// clients cleaned up after themselves still leaves exactly one labelled
// network standing — and the doorstep of the next run refuses exactly that
// (#521, measured twice after two green conformance runs). The process that
// set the uplink up is the one that takes it down, on its way out.
type UplinkReleaser interface {
	// ReleaseUplink removes the uplink when, and only when, this process is
	// the one holding it and nothing draws from it any more. It reports
	// whether the uplink went; an uplink another run left, one an operator
	// named, or one that networks still sit on is left standing, so the sweep
	// and the doorstep keep naming what this path must not hide.
	ReleaseUplink(ctx context.Context) (bool, error)
}

// A leftover the sweep can still remove is untidy. A leftover no ordinary
// command of the runtime can remove any more is something else, and #455
// measured it: an OVN network whose peering points at a network that no longer
// exists refuses every management path, including the one that would remove the
// peering, so the network, the rule set attached to it and the uplink they sit
// on are permanent by every means a user has.
//
// A Trap is that second family. It is named apart from Leftovers because the
// remedy is different in kind: a leftover is swept, a trap is either prevented
// or repaired by a step the operator asks for by name.

// Trap kinds, closed on purpose so a report can group them.
const (
	// TrapDanglingPeer is a peering row whose target network is gone. The
	// runtime's own schema is what makes it possible: the reference to the
	// target carries no cascade, so deleting the target leaves the row, and
	// from then on every operation on the *source* network fails loading a
	// target that resolves to nothing.
	TrapDanglingPeer = "dangling-peer"
	// TrapStrippedUplink is a network of the emulator's whose block is no
	// longer delegated to the uplink it draws from. The runtime then refuses
	// every update of that network — including the detach that would free it —
	// with a message that names the block rather than the uplink.
	TrapStrippedUplink = "stripped-uplink"
	// TrapHeldFirewall is a rule set of the emulator's attached to a network
	// that is itself trapped: neither can go, because the rule set is "in use"
	// by the network and the network is "in use" by the rule set, and the
	// detach that breaks the cycle is one of the operations the trap refuses.
	TrapHeldFirewall = "held-firewall"
)

// Trap is one such state, named so a check can report it and an operator can
// decide.
type Trap struct {
	// Kind is one of the constants above.
	Kind string
	// Name is the object in the runtime's own terms.
	Name string
	// Why says what is stuck and what it blocks, in one sentence.
	Why string
	// Repairable says whether Repair can clear this one. False means the
	// ordinary sweep clears it now that it knows how, so nothing privileged is
	// needed and nothing here should offer any.
	Repairable bool
	// Row is what a repair would remove, verbatim and complete, so an operator
	// can put it back. Empty for a trap Repair does not touch.
	Row string
}

// Repairer is the optional half of a driver that can name the states its own
// sweep cannot get out of, and clear the ones no ordinary command reaches.
//
// The split matters. Traps is a read and is always safe to run; Repair reaches
// past the runtime's own commands and is therefore only ever run when an
// operator asks for it by name.
type Repairer interface {
	// Traps names what holds this runtime. It must issue no mutating command,
	// for the same reason Survey must not.
	Traps(ctx context.Context) ([]Trap, error)
	// Repair clears the traps that Repair can clear, and returns those it
	// cleared. It re-derives ownership itself and never trusts a Trap it was
	// handed: a name that made a round trip is an input, not a permission.
	Repair(ctx context.Context) ([]Trap, error)
}
