package machine

import (
	"context"
	"errors"
	"log/slog"
)

// ErrNetworkGone reports that the network an isolation call names is no longer
// there: a delete of it landed first.
//
// It exists because "the network is gone" is not the same answer as "the rules
// could not be written", and the difference is the whole of #386. A driver that
// swallowed it would report success for work it never did; one that reported it
// as a plain failure would cry wolf on every parallel destroy, which is the
// ordinary way a subnet and its neighbour's reconciliation cross. So it is a
// value the caller can recognise, and ReconcileIsolation below says so in the
// log rather than staying quiet.
//
// A driver returns it only when it has established the network's absence — not
// when a call merely failed in a way whose prose mentions it. Incus answers this
// through d.gone, which asks the daemon.
var ErrNetworkGone = errors.New("the network was removed while its isolation was being applied")

// Two emulated subnets are two real subnets, and upstream they do not reach each
// other. Runtimes disagree on which way round that is: some join networks by
// default and must be separated by rules, others keep them apart and must be
// joined on request. The two interfaces below are that fork, and a pack asks
// which one it is talking to rather than assuming.
//
// The Incus halves of both are in incus_isolate.go, with the measurements that
// made the OVN mode necessary.

// isolator is the optional half of a driver whose networks are born joined,
// and that can keep them apart with rules.
type isolator interface {
	// IsolateNetwork rejects traffic from the network towards each foreign
	// block, and leaves everything else alone. Called again with a different
	// list, it replaces the previous one: blocks appear and disappear as
	// subnets are created and deleted.
	IsolateNetwork(ctx context.Context, network string, foreign []string) error
}

// peerer is the optional half of a driver whose networks are born separate
// and joined on request — the exact inverse of the isolator. The layer asks
// NativeIsolation to know which of the two it is talking to: with a peerer
// that answers true, reject rules against foreign blocks are dead weight, and
// reachability is granted by peering instead.
type peerer interface {
	// NativeIsolation reports whether two networks of this driver are
	// unreachable from each other unless peered.
	NativeIsolation() bool
	// PeerNetworks makes the network reach exactly the given peers: missing
	// peerings are created, in both their halves, and stale ones removed.
	PeerNetworks(ctx context.Context, network string, peers []string) error
}

// IsolationMember is one network of a pack in a reconciliation pass.
type IsolationMember struct {
	// ID names the pack's resource, for the log line when the driver refuses.
	ID string
	// Network is the runtime network backing the resource. Empty means no
	// backing network exists yet: the member is skipped as a target and never
	// listed as a peer, because there is nothing to configure or to reach.
	Network string
	// Block is the CIDR a foreign list carries for this member. Empty means
	// the member has no block to keep out (an unmanaged range), which excludes
	// it from foreign lists only.
	Block string
}

// ReconcileIsolation applies what every member may reach, over the whole set.
//
// Three packs had written this loop — vpc.go, isolate.go, privatenetworks.go —
// and only the reachability predicate differed: same routed VPC for Scaleway,
// same Net for Outscale, nothing for Exoscale, whose networks are each their
// own segment upstream. The predicate is the provider knowledge, so it is the
// parameter; the rest is the mechanism, and a control living in two packs out
// of three is a control the third forgets, which is what #201 measured.
//
// A runtime with native isolation gets peer lists — its networks reach nothing
// until joined, so the question is what to let in. One whose networks are born
// joined gets each member's foreign blocks to keep out. Reconciled over all
// members rather than patched for the one that moved: a patch has to be right
// about what changed, and this only has to be right about what is.
//
// Failure is logged, never fatal: the control plane must keep answering when
// no runtime is configured, which is the default and what CI uses. The return
// says what happened — native peering, rule-set isolation, or nothing, when
// the driver has neither capability — because at least one pack does extra
// work (a security-group resync) only in the rule-set case.
func ReconcileIsolation(ctx context.Context, d driver, log *slog.Logger, noun string,
	members []IsolationMember, reachable func(from, to int) bool) (native, applied bool) {
	if log == nil {
		log = slog.Default()
	}

	if peer, ok := d.(peerer); ok && peer.NativeIsolation() {
		for i, m := range members {
			if m.Network == "" {
				continue
			}
			peers := make([]string, 0, len(members))
			for j, other := range members {
				if j == i || other.Network == "" {
					continue
				}
				if reachable(i, j) {
					peers = append(peers, other.Network)
				}
			}
			if err := peer.PeerNetworks(ctx, m.Network, peers); err != nil {
				report(log, noun, m, err, "peer")
			}
		}
		return true, true
	}

	iso, ok := d.(isolator)
	if !ok {
		return false, false
	}
	for i, m := range members {
		if m.Network == "" {
			continue
		}
		foreign := make([]string, 0, len(members))
		for j, other := range members {
			if j == i || reachable(i, j) {
				continue
			}
			if other.Block != "" {
				foreign = append(foreign, other.Block)
			}
		}
		if err := iso.IsolateNetwork(ctx, m.Network, foreign); err != nil {
			report(log, noun, m, err, "isolate")
		}
	}
	return false, true
}

// ReconcileIsolation is the binding's door onto the pass above, and the only
// one a provider pack has: the package-level form takes a driver, which is
// precisely the value #511 took out of every pack's reach, and it stays
// exported for the core's own tests and for nothing else.
//
// The pack still owns what it alone knows — the noun for the log line, the
// members, and the reachability predicate. The driver and the logger come from
// the binding it already declared.
func (b Binding) ReconcileIsolation(ctx context.Context, noun string,
	members []IsolationMember, reachable func(from, to int) bool) (native, applied bool) {
	return ReconcileIsolation(ctx, b.driver, b.logger(), noun, members, reachable)
}

// report says what a reconciliation could not do, and separates the two reasons
// it can fail.
//
// A network deleted under the pass is the expected outcome of a parallel
// destroy: the member was in the store when the list was taken and its delete
// landed before its turn came. Nothing is wrong with the host and nothing is
// left to apply, so it is not an error — but it is not a success either, and it
// is exactly the event whose invisibility made #386 take three issues to find.
// It is logged at warn, naming the network, so a run that produced one says so.
//
// TestAVanishedNetworkIsReportedRatherThanSwallowed fails when this reports
// nothing.
func report(log *slog.Logger, noun string, m IsolationMember, err error, verb string) {
	if errors.Is(err, ErrNetworkGone) {
		log.Warn("the "+noun+"'s network was removed while its isolation was being reconciled, so none was applied",
			noun, m.ID, "network", m.Network, "error", err)
		return
	}
	log.Error("could not "+verb+" the "+noun+"'s network",
		noun, m.ID, "network", m.Network, "error", err)
}
