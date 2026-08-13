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
	// Two passes rather than a dependency order: an OVN network must go before
	// the uplink it draws from, and the list carries no such order. The first
	// pass removes everything removable, the second retries what was only
	// blocked by a neighbour still standing in the first.
	remaining := networks
	for pass := 0; pass < 2 && len(remaining) > 0; pass++ {
		var blocked []string
		for _, name := range remaining {
			// Routed addresses, forwards and peerings keep a network alive;
			// all three are the emulator's own doing, so they go with it.
			_, _ = d.run(ctx, "network", "unset", name, "ipv4.routes")
			for _, listen := range d.forwardsOf(ctx, name) {
				_, _ = d.run(ctx, "network", "forward", "delete", name, listen)
			}
			if peers, err := d.networkPeers(ctx, name); err == nil {
				for _, peer := range peers {
					_, _ = d.run(ctx, "network", "peer", "delete", name, peer.Name)
				}
			}
			if _, err := d.run(ctx, "network", "delete", name); err != nil && !isNotFound(err) {
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
