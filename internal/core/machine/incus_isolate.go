package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// How Incus keeps bridged networks apart. The contracts are in isolate.go, and
// the OVN half of Peerer is in incus_ovn.go.
//
// Two managed bridges on one host are routed to each other, not separated. The
// Incus documentation says so plainly: "traffic between managed bridge networks
// on the same server isn't NATed as it's routed directly between the bridges".
// So an emulated subnet is a real subnet and, without this, a machine of one
// reaches a machine of another whatever the control plane claims.
//
// The fix is a rule set on the network itself, which is where a bridge ACL does
// apply: the documentation limits them to "the boundary between the bridge and
// the Incus host", and traffic towards another bridge crosses exactly that
// boundary. Rules between machines of the same bridge remain a NIC matter, which
// is where security groups live.

// isolationACL is the rule set name for a network's own isolation. Kept apart
// from the security groups so the two never overwrite each other, and prefixed
// like everything else so a sweep recognises it.
func isolationACL(network string) string {
	return "iso-" + network
}

// isolationOwned reports whether a rule set is one of these, so a sweep can drop
// it even when it never got its description.
//
// The description is written by the PUT that follows the create, so an
// interrupted IsolateNetwork leaves a rule set the description check cannot see.
// This covers that window — and only it: the match is the emulator's own network
// prefix, not a bare "iso-", which claimed every rule set on the host named that
// way, an operator's included.
//
// The description cannot simply be set at creation: `network acl create` rejects
// it as a config option ("Invalid config option \"description\""), because it is
// a top-level field of the ACL rather than a config key. Measured, after trying.
//
// The uplink's own rule set (isolateUplink) is named after the uplink, not
// after an emulated network, so it is matched here by name too; an uplink under
// a custom name is covered by the description check alone, which only leaves
// the interrupted-create window open for that case.
func isolationOwned(name string) bool {
	return strings.HasPrefix(name, "iso-"+NetworkPrefix+"-") ||
		name == isolationACL(DefaultUplinkName)
}

// IsolateNetwork implements Isolator.
func (d *Incus) IsolateNetwork(ctx context.Context, network string, foreign []string) error {
	// Under OVN the separation is not built here, out of per-network reject
	// rules: the packs' native-isolation branch peers what must reach each
	// other and never calls this method. The early comment claimed the
	// topology alone provided the separation ("no shared L2 to build reject
	// rules against") and #201 measured that claim false: the shared L2 is the
	// emulator's own uplink, which carries every delegated block as a
	// scope-link host route. isolateUplink closes that path at its boundary,
	// for every network and every machine topology at once, and
	// TestTheUplinkRejectsDelegatedBlocks holds it.
	if d.OVN {
		return nil
	}
	if !safeName.MatchString(network) {
		return fmt.Errorf("invalid network name %q", network)
	}
	// Isolation attaches and detaches ACLs on the named network. Pointed at a
	// network of the operator's, it unsets security.acls there: an audit obtained
	// exactly that on incusbr0, which is a firewall disarmed rather than a subnet
	// isolated.
	if !ownedNetwork(network) {
		return fmt.Errorf("refusing to isolate network %q: not one the emulator created", network)
	}
	name := isolationACL(network)

	if len(foreign) == 0 {
		// Nothing to keep out: detach and drop the rule set rather than leave an
		// empty one attached, so `incus network show` says what is true.
		if _, err := d.run(ctx, "network", "unset", network, "security.acls"); err != nil {
			return fmt.Errorf("detach isolation from %s: %w", network, err)
		}
		return d.RemoveFirewall(ctx, name)
	}

	if _, err := d.run(ctx, "network", "acl", "show", name); err != nil {
		if !isNotFound(err) {
			return fmt.Errorf("inspect isolation of %s: %w", network, err)
		}
		if _, err := d.run(ctx, "network", "acl", "create", name); err != nil {
			return fmt.Errorf("create isolation of %s: %w", network, err)
		}
	}

	// Reject rather than drop: a machine reaching a subnet it must not reach
	// gets an immediate answer instead of a timeout, which is the difference
	// between an obvious topology mistake and a puzzling hang.
	body := aclBody{
		Description: aclDescription,
		Ingress:     []aclRule{},
		Egress:      []aclRule{},
		Config:      map[string]string{},
	}
	for _, block := range foreign {
		body.Egress = append(body.Egress, aclRule{
			Action:      "reject",
			State:       "enabled",
			Destination: block,
		})
		body.Ingress = append(body.Ingress, aclRule{
			Action: "reject",
			State:  "enabled",
			Source: block,
		})
	}
	// Everything else passes: this rule set exists to keep subnets apart, not to
	// filter, and the NIC rule sets are what a security group drives.
	body.Egress = append(body.Egress, aclRule{Action: "allow", State: "enabled"})
	body.Ingress = append(body.Ingress, aclRule{Action: "allow", State: "enabled"})

	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode isolation of %s: %w", network, err)
	}
	if _, err := d.run(ctx, "query", "-X", "PUT", "--data", string(encoded),
		"/1.0/network-acls/"+name); err != nil {
		return fmt.Errorf("write isolation of %s: %w", network, err)
	}

	if _, err := d.run(ctx, "network", "set", network, "security.acls", name); err != nil {
		return fmt.Errorf("attach isolation to %s: %w", network, err)
	}
	return nil
}
