package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// How Incus finds and removes the emulator's own work. The contract, and why a
// label rather than a name convention, are in prune.go.

// Prune implements Pruner.
//
// Order matters and errors do not stop the sweep: a leftover the runtime
// refuses to remove must not prevent the next one from going, or one stuck
// resource keeps the whole environment dirty.
func (d *Incus) Prune(ctx context.Context) (Pruned, error) {
	var pruned Pruned
	var failures []string

	machines, err := d.labelled(ctx, "/1.0/instances?recursion=1")
	if err != nil {
		return pruned, err
	}
	for _, name := range machines {
		if _, err := d.run(ctx, "delete", "--force", name); err != nil && !isNotFound(err) {
			failures = append(failures, fmt.Sprintf("machine %s: %v", name, err))
			continue
		}
		pruned.Machines++
	}

	networks, err := d.labelled(ctx, "/1.0/networks?recursion=1")
	if err != nil {
		return pruned, err
	}
	// The uplink is not an ordinary network, and treating it as one is what made
	// #455 permanent. It carries the label like everything else, so it was in
	// this list, and the loop below unset its ipv4.routes before a delete that
	// then failed because the orphans were still attached. From that instant
	// every management path of those orphans fails validation — "Uplink network
	// doesn't contain 10.2.4.0/24 in its routes" — which is the detach they
	// needed in order to go. The sweep was the eraser, not the next run.
	//
	// So it leaves the ordinary path entirely, its routes are never unset, and
	// it is deleted last, when the networks drawing from it are gone and its
	// routes go with the object.
	// TestPruneNeverStripsTheUplinkRoutesWhileItsNetworksStand fails without it.
	uplink := d.uplinkName()
	ordinary := make([]string, 0, len(networks))
	uplinkPresent := false
	for _, name := range networks {
		if name == uplink {
			uplinkPresent = true
			continue
		}
		ordinary = append(ordinary, name)
	}

	// And the repair of a host a previous sweep already stripped, which needs no
	// privilege: a network whose block the uplink no longer carries refuses
	// every update, so the block goes back before anything is asked of it. This
	// is the manual step that measurably freed one of the two stuck rule sets on
	// the maintainer's station, automated.
	//
	// Not gated on d.OVN: the issue's own reproduction sweeps the trapped host
	// with `feint clean --vm incus`, and whether the uplink is stripped is a
	// fact about the host rather than about the mode this process runs in.
	if uplinkPresent {
		if err := d.restoreDelegations(ctx); err != nil {
			failures = append(failures, fmt.Sprintf("uplink %s: %v", uplink, err))
		}
	}

	// Two passes rather than a dependency order: a network peered with another
	// may still be held when its turn comes, and the list carries no such order.
	// The first pass removes everything removable, the second retries what was
	// only blocked by a neighbour still standing in the first.
	remaining := ordinary
	for pass := 0; pass < 2 && len(remaining) > 0; pass++ {
		var blocked []string
		for _, name := range remaining {
			// Peerings, rule sets, routed addresses and forwards all keep a
			// network alive; all four are the emulator's own doing, so they go
			// with it — and the order below is the only one Incus accepts.
			d.dropPeerings(ctx, name)
			// The rule set and its network hold each other: the rule set is "in
			// use" by the network and the network is "in use" by the rule set,
			// and deleting in either order is refused. Detaching first is the
			// escape, and nothing did it — the ACL loop below just retried the
			// delete twice and reported it stuck. Put back when the delete then
			// fails, because a network that survives with its rule set detached
			// is a firewall disarmed rather than a subnet swept.
			reattach := d.detachRuleSets(ctx, name)
			_, _ = d.run(ctx, "network", "unset", name, "ipv4.routes")
			for _, listen := range d.forwardsOf(ctx, name) {
				_, _ = d.run(ctx, "network", "forward", "delete", name, listen)
			}
			if _, err := d.run(ctx, "network", "delete", name); err != nil && !isNotFound(err) {
				reattach(ctx)
				if pass == 0 {
					blocked = append(blocked, name)
				} else {
					failures = append(failures, fmt.Sprintf("network %s: %v", name, err))
				}
				continue
			}
			pruned.Networks++
		}
		remaining = blocked
	}

	if uplinkPresent {
		for _, listen := range d.forwardsOf(ctx, uplink) {
			_, _ = d.run(ctx, "network", "forward", "delete", uplink, listen)
		}
		if _, err := d.run(ctx, "network", "delete", uplink); err != nil && !isNotFound(err) {
			failures = append(failures, fmt.Sprintf("network %s: %v", uplink, err))
		} else {
			pruned.Networks++
		}
	}

	// Rule sets carry no config of their own, so they are recognised by the
	// description EnsureFirewall writes.
	//
	// Two passes, like the networks above and for the same reason: a rule set
	// still attached to a machine or a network answers "Cannot delete an ACL
	// that is in use", and whatever holds it is being removed in this same
	// sweep. One pass reported a failure for something that was free a moment
	// later, which failed the conformance run on a runtime that ended up clean.
	remainingACLs := d.ownedACLs(ctx)
	for pass := 0; pass < 2 && len(remainingACLs) > 0; pass++ {
		var blocked []string
		for _, name := range remainingACLs {
			if _, err := d.run(ctx, "network", "acl", "delete", name); err != nil && !isNotFound(err) {
				if pass == 0 {
					blocked = append(blocked, name)
				} else {
					failures = append(failures, fmt.Sprintf("firewall %s: %v", name, err))
				}
				continue
			}
			pruned.Firewalls++
		}
		remainingACLs = blocked
	}

	if len(failures) > 0 {
		return pruned, fmt.Errorf("could not remove %d resource(s): %s",
			len(failures), strings.Join(failures, "; "))
	}
	return pruned, nil
}

// Survey implements Surveyor: the same three listings Prune starts from, and
// nothing else. Every command it issues is a read (`query`); the test
// TestSurveyFindsOnlyTheEmulatorsWorkAndTouchesNothing fails if a mutating
// verb ever creeps in.
func (d *Incus) Survey(ctx context.Context) (Leftovers, error) {
	var left Leftovers
	machines, err := d.labelled(ctx, "/1.0/instances?recursion=1")
	if err != nil {
		return left, err
	}
	left.Machines = machines

	networks, err := d.labelled(ctx, "/1.0/networks?recursion=1")
	if err != nil {
		return left, err
	}
	left.Networks = networks
	left.Firewalls = d.ownedACLs(ctx)
	return left, nil
}

// labelled returns the names of the resources at the endpoint carrying the
// emulator's label. Incus stores labels as user.* config keys.
func (d *Incus) labelled(ctx context.Context, endpoint string) ([]string, error) {
	out, err := d.run(ctx, "query", endpoint)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", endpoint, err)
	}
	var items []struct {
		Name   string            `json:"name"`
		Config map[string]string `json:"config"`
	}
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, fmt.Errorf("decode %s: %w", endpoint, err)
	}

	names := make([]string, 0, len(items))
	for _, item := range items {
		if item.Config["user."+LabelKey] != "" {
			names = append(names, item.Name)
		}
	}
	return names, nil
}

func (d *Incus) forwardsOf(ctx context.Context, network string) []string {
	out, err := d.run(ctx, "query", "/1.0/networks/"+network+"/forwards?recursion=1")
	if err != nil {
		return nil
	}
	var forwards []struct {
		ListenAddress string `json:"listen_address"`
	}
	if err := json.Unmarshal(out, &forwards); err != nil {
		return nil
	}
	addresses := make([]string, 0, len(forwards))
	for _, forward := range forwards {
		addresses = append(addresses, forward.ListenAddress)
	}
	return addresses
}

func (d *Incus) ownedACLs(ctx context.Context) []string {
	out, err := d.run(ctx, "query", "/1.0/network-acls?recursion=1")
	if err != nil {
		return nil
	}
	var acls []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(out, &acls); err != nil {
		return nil
	}
	names := make([]string, 0, len(acls))
	for _, acl := range acls {
		// By description, plus the isolation names, which are the one case that
		// can exist without one: the description is written by the PUT that
		// follows the create, so an interrupted run leaves a rule set invisible
		// to the description check. isolationOwned matches the emulator's own
		// network prefix rather than a bare iso-, which used to claim an
		// operator's rule sets too.
		if strings.HasPrefix(acl.Description, aclDescription) || isolationOwned(acl.Name) {
			names = append(names, acl.Name)
		}
	}
	return names
}

// dropPeerings removes both halves of every peering a network holds.
//
// The second half is the whole point, and nothing did it before #455. Peer rows
// name their target by *id*, and the runtime's schema cascades the reference to
// the source network but not the one to the target: delete network B and the
// row on A survives, holding an id that resolves to nothing. A then refuses
// every management path — including the peer delete that would remove the row —
// so A, the rule set attached to it and the uplink they sit on are permanent by
// every means a user has. Recreating B under the same name does not satisfy it
// either, which is what proves the reference is an id.
//
// The target-side half is named after this network, because ensurePeerHalf
// declares each half under the name of its target.
//
// The target's name comes from the runtime and becomes the argument of a
// destructive command aimed at *another* network, so it answers both questions
// before it is used: well formed, and one of the emulator's own. A peering an
// operator made by hand between one of our networks and one of theirs would
// otherwise have this delete a peering on theirs.
// TestPruneRefusesToTouchAPeeringHalfOnAForeignNetwork fails without the check.
func (d *Incus) dropPeerings(ctx context.Context, network string) {
	peers, err := d.networkPeers(ctx, network)
	if err != nil {
		return
	}
	for _, peer := range peers {
		if d.ownsPeerTarget(ctx, peer.Target) {
			_, _ = d.run(ctx, "network", "peer", "delete", peer.Target, network)
		}
		_, _ = d.run(ctx, "network", "peer", "delete", network, peer.Name)
	}
}

// ownsPeerTarget decides whether the network on the far end of a peering may be
// reconfigured by this sweep, and it asks the label rather than stopping at the
// name.
//
// The prefix is necessary and it is not the answer. `fnt-` is a prefix anybody
// may type, which is the maintainer's own sentence about the database repair in
// #455 — and the reach here is of the same family, a destructive command aimed
// at a network that is *not* the one being removed. An operator whose own OVN
// network happens to be called `fnt-lab` and who peered it with one of ours by
// hand would otherwise have this delete a peering on theirs, from a check that
// looked thorough because it had three clauses.
//
// So the cheap questions come first and refuse without a round trip — form, then
// the prefix NetworkName writes — and mustOwn reads the label EnsureNetwork
// wrote, which is the only one of the three that cannot be typed by somebody
// else. A target that has already gone answers no, which is right: there is no
// half left on a network that no longer exists.
// TestPruneRefusesAPeeringHalfOnAStrangersNetworkNamedLikeOurs fails without
// the label, and TestPruneDropsTheSurvivingHalfOfEveryPeering fails if it
// refuses our own.
func (d *Incus) ownsPeerTarget(ctx context.Context, target string) bool {
	if target == "" || !safeName.MatchString(target) || !ownedNetwork(target) {
		return false
	}
	return d.mustOwn(ctx, target) == nil
}

// detachRuleSets takes a network's rule sets off it and returns what puts them
// back. An empty attachment, or a read that fails, gives a no-op: this exists to
// break a deadlock, not to reconfigure anything on the way past.
//
// The restore matters as much as the detach. `network unset <net> security.acls`
// is the primitive an audit used to disarm a firewall rather than isolate a
// subnet, and a delete that then fails would leave exactly that state on a
// network which is still standing.
func (d *Incus) detachRuleSets(ctx context.Context, network string) func(context.Context) {
	nothing := func(context.Context) {}
	out, err := d.run(ctx, "network", "get", network, "security.acls")
	if err != nil {
		return nothing
	}
	attached := strings.TrimSpace(string(out))
	if attached == "" {
		return nothing
	}
	if _, err := d.run(ctx, "network", "unset", network, "security.acls"); err != nil {
		return nothing
	}
	return func(ctx context.Context) {
		_, _ = d.run(ctx, "network", "set", network, "security.acls", attached)
	}
}

// restoreDelegations puts back the block of every labelled OVN network still
// drawing from the uplink.
//
// It is the automated form of the one manual step that measurably freed a
// trapped station (#455): with the block absent from the uplink's ipv4.routes,
// the runtime refuses every update of the network carrying it, so nothing can
// detach its rule set, remove its peerings or delete it. Idempotent — a block
// already delegated is left alone, and every write to the uplink makes the
// runtime rebuild its firewall chains.
func (d *Incus) restoreDelegations(ctx context.Context) error {
	d.uplinkMu.Lock()
	defer d.uplinkMu.Unlock()

	kept, err := d.liveDelegations(ctx)
	if err != nil {
		return err
	}
	for _, block := range kept {
		if err := d.addUplinkRoute(ctx, block); err != nil {
			return fmt.Errorf("restore %s on uplink %s: %w", block, d.uplinkName(), err)
		}
	}
	return nil
}
