package machine

import (
	"context"
	"log/slog"
)

// Two emulated subnets are two real subnets, and upstream they do not reach each
// other. Runtimes disagree on which way round that is: some join networks by
// default and must be separated by rules, others keep them apart and must be
// joined on request. The two interfaces below are that fork, and a pack asks
// which one it is talking to rather than assuming.
//
// The Incus halves of both are in incus_isolate.go, with the measurements that
// made the OVN mode necessary.

// Isolator is the optional half of a Driver whose networks are born joined, and
// that can keep them apart with rules.
type Isolator interface {
	// IsolateNetwork rejects traffic from the network towards each foreign
	// block, and leaves everything else alone. Called again with a different
	// list, it replaces the previous one: blocks appear and disappear as
	// subnets are created and deleted.
	IsolateNetwork(ctx context.Context, network string, foreign []string) error
}

// Peerer is the optional half of a Driver whose networks are born separate
// and joined on request — the exact inverse of Isolator. A pack asks
// NativeIsolation to know which of the two it is talking to: with a Peerer that
// answers true, reject rules against foreign blocks are dead weight, and
// reachability is granted by peering instead.
type Peerer interface {
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
func ReconcileIsolation(ctx context.Context, driver Driver, log *slog.Logger, noun string,
	members []IsolationMember, reachable func(from, to int) bool) (native, applied bool) {
	if log == nil {
		log = slog.Default()
	}

	if peerer, ok := driver.(Peerer); ok && peerer.NativeIsolation() {
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
			if err := peerer.PeerNetworks(ctx, m.Network, peers); err != nil {
				log.Error("could not peer the "+noun+"'s network",
					noun, m.ID, "network", m.Network, "error", err)
			}
		}
		return true, true
	}

	isolator, ok := driver.(Isolator)
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
		if err := isolator.IsolateNetwork(ctx, m.Network, foreign); err != nil {
			log.Error("could not isolate the "+noun+"'s network",
				noun, m.ID, "network", m.Network, "error", err)
		}
	}
	return false, true
}
