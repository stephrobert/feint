package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// What a graceful exit gives back to the host, and why it is three objects and
// not one (#521, then the measurement of 2026-08-28).
//
// #521 named the family with one member: the uplink. Every emulated resource
// goes when a client deletes it, so a run whose clients cleaned up after
// themselves still left `feint-uplink` standing, and the next run's doorstep
// refused the host on it. The exit learned to release it.
//
// The incus-ovn leg of runtime-proof.yml then failed at the same doorstep with
// four objects rather than one, on a runner nothing had touched, and the
// reproduction on this station (2026-08-28) named them and their causes:
//
//	feint-uplink       host plumbing; released since #521, and it stayed
//	                   because `fnt-default` still drew from it
//	fnt-default        host plumbing: DefaultMachineNetwork, created the first
//	                   time a machine boots with no attachment of its own (an
//	                   Outscale Vm outside a Net), owned by no resource, so no
//	                   client's delete ever reaches it
//	scw-<digest>       the rule set of the Scaleway project's default security
//	                   group — permissive, therefore attached to nothing
//	                   (EnforcesNothing), and undeletable by a client: the API
//	                   answers "the default security group of a project cannot
//	                   be deleted". Its identifier is minted per run, so one
//	                   such rule set accumulates on the host per session
//	exo-<digest>       the same for the Exoscale account's default group, whose
//	                   identifier is fixed, so it survives rather than piles up
//
// The common property is what makes them one family and what makes releasing
// them honest rather than tidy: **no client call can remove any of them**, so
// leaving them measures nothing about the suites. That is exactly the sentence
// #521 wrote for the uplink, and it is why this is not `--cleanup`: a machine
// or a network a client was supposed to delete and did not is a leak, it stays,
// and the doorstep after the stop fails the run that leaked it.
//
// Everything here answers both questions of cli.go before it issues a
// destructive command — is the object the emulator's own, and is anything still
// drawing from it — because the second is what separates this from the
// `network unset security.acls` primitive an audit used to disarm a firewall.
// A machine left running by a stop without --cleanup keeps its rule set for
// exactly that reason.

// ReleasePlumbing implements PlumbingReleaser.
//
// The order is the one the runtime imposes and is not cosmetic: a rule set
// attached to a network keeps that network alive, and a network drawing from
// the uplink keeps the uplink alive. Released the other way round, every step
// fails "in use" and the exit gives back nothing — which is precisely what was
// measured before this existed, `feint-uplink` refusing to go because
// `fnt-default` sat on it.
//
// Failures are collected rather than returned at the first one: a rule set the
// runtime will not part with must not stop the network behind it from going.
// TestAGracefulExitReleasesEveryPieceOfPlumbingItHolds fails without the two
// new members; TestTheReleaseOrderIsRuleSetsThenNetworkThenUplink fails when
// the order is reversed.
func (d *Incus) ReleasePlumbing(ctx context.Context) ([]string, error) {
	var released []string
	var failures []string

	sets, err := d.releaseUnusedRuleSets(ctx)
	released = append(released, sets...)
	if err != nil {
		failures = append(failures, err.Error())
	}

	if gone, err := d.releaseDefaultNetwork(ctx); err != nil {
		failures = append(failures, err.Error())
	} else if gone {
		released = append(released, DefaultMachineNetwork)
	}

	if gone, err := d.releaseUplink(ctx); err != nil {
		failures = append(failures, err.Error())
	} else if gone {
		released = append(released, d.uplinkName())
	}

	if len(failures) > 0 {
		return released, fmt.Errorf("could not release %d piece(s) of plumbing: %s",
			len(failures), strings.Join(failures, "; "))
	}
	return released, nil
}

// releaseUnusedRuleSets removes the rule sets this emulator wrote that nothing
// on the host references any more.
//
// Why they are the exit's business at all. A rule set is written for a security
// group, and a group a client deletes takes its rule set with it
// (GroupSync.Drop). What no client deletes is a *default* group: Scaleway
// refuses it by precondition, Exoscale's is seeded before any call, and both
// are what a machine wears when the client named no group — which is what every
// ssh suite does. Their rule sets therefore outlive every client and every run,
// and the store that gave them meaning dies with this process.
//
// Why `used_by` is read first rather than the delete simply being attempted.
// The runtime does refuse a delete of a referenced rule set ("Cannot delete an
// ACL that is in use", measured on this station on 2026-08-28), so the read is
// not what makes this safe. What it makes is the difference between asking a
// question and issuing a destructive command against a machine's live firewall
// and being saved by the answer. The in-use tolerance below stays as the race
// window's net: something may attach between the read and the delete.
//
// TestAReleaseKeepsTheRuleSetOfAMachineLeftRunning fails without the check.
func (d *Incus) releaseUnusedRuleSets(ctx context.Context) ([]string, error) {
	out, err := d.run(ctx, "query", "/1.0/network-acls?recursion=1")
	if err != nil {
		// Could not look. Never an empty list on a failed read: "there is
		// nothing to release" and "nobody could tell" are different facts, and
		// reporting the first as the second is how an inventory once called a
		// live account empty.
		return nil, fmt.Errorf("list the rule sets to release: %w", err)
	}
	var acls []struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		UsedBy      []string `json:"used_by"`
	}
	if err := json.Unmarshal(out, &acls); err != nil {
		return nil, fmt.Errorf("decode the rule sets to release: %w", err)
	}

	var released []string
	var failures []string
	for _, acl := range acls {
		// Ours, by the same two marks ownedACLs sweeps by: the description this
		// driver writes, or the isolation names it derives. An operator's rule
		// set carries neither and is never named here, whatever it is called.
		if !strings.HasPrefix(acl.Description, FirewallDescription) && !coreOwnedACL(acl.Name) {
			continue
		}
		if len(acl.UsedBy) > 0 {
			continue
		}
		// RemoveFirewall rather than a bare delete, so ownership is re-derived
		// at the moment of the command and not taken from the listing above: a
		// name that made a round trip is an input, not a permission.
		if err := d.RemoveFirewall(ctx, acl.Name); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "in use") {
				continue
			}
			failures = append(failures, fmt.Sprintf("rule set %s: %v", acl.Name, err))
			continue
		}
		released = append(released, acl.Name)
	}
	if len(failures) > 0 {
		return released, fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return released, nil
}

// releaseDefaultNetwork removes DefaultMachineNetwork when it is this
// emulator's and nothing sits on it.
//
// No holder pid is recorded on this one, unlike the uplink, and `used_by` is
// what stands in for it: two emulators sharing a host share this network, and
// the one exiting first must not take it from under the other's machines. A
// machine attached to it makes `used_by` non-empty, which is the same fact a
// pid would have carried and one the runtime maintains itself.
//
// TestAReleaseLeavesTheDefaultNetworkAMachineStillSitsOn and
// TestAReleaseNeverTouchesAnUnlabelledNetworkUnderTheDefaultName fail without
// the two refusals; TestAGracefulExitReleasesEveryPieceOfPlumbingItHolds fails
// without the accepting half.
func (d *Incus) releaseDefaultNetwork(ctx context.Context) (bool, error) {
	name := DefaultMachineNetwork
	out, err := d.run(ctx, "query", "/1.0/networks/"+name)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect default network %s: %w", name, err)
	}
	var existing struct {
		Config map[string]string `json:"config"`
		UsedBy []string          `json:"used_by"`
	}
	if err := json.Unmarshal(out, &existing); err != nil {
		return false, fmt.Errorf("decode default network %s: %w", name, err)
	}
	if existing.Config["user."+LabelKey] == "" {
		// An operator's own network under this name. ensureDefaultNetwork
		// already refuses to reuse one; the exit refuses to remove one, which
		// is the same refusal read backwards.
		return false, nil
	}
	if len(existing.UsedBy) > 0 {
		return false, nil
	}
	if _, err := d.run(ctx, "network", "delete", name); err != nil {
		if isNotFound(err) {
			return false, nil
		}
		if strings.Contains(strings.ToLower(err.Error()), "in use") {
			return false, nil
		}
		return false, fmt.Errorf("release default network %s: %w", name, err)
	}
	return true, nil
}
