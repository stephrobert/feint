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
func isolationOwned(name string) bool {
	return strings.HasPrefix(name, "iso-"+NetworkPrefix+"-")
}

// permissiveACL is the rule set that keeps a machine with no restrictive group
// open on an OVN network that carries the emulator's isolation set. One per
// host, because it says the same thing for every such machine: everything
// passes, in both directions, as catch-all allow rules at the runtime's rule
// priority (300 in acl_ovn.go at v7.2.0).
//
// It exists because attaching any ACL to an OVN network makes the runtime add a
// default-action rule to every NIC of that network — reject, at priority
// 100/111 — and the isolation set no longer carries the catch-all allow that
// used to compensate (that allow, at 300, was #491: it outranked every
// NIC-level default deny). A machine whose groups enforce nothing attaches
// nothing, so without this set it would lose all traffic the moment its
// network gained a second subnet.
func permissiveACL() string {
	return "opn-" + NetworkPrefix
}

// permissiveSpec is the content of that rule set: allow everything, stated as
// rules rather than as NIC default-action keys, because security.acls is the
// only ACL key an OVN NIC updates in place (UpdatableFields, nic_ovn.go at
// v7.2.0) and the keys would cost the guest its addresses on every change.
func permissiveSpec() FirewallSpec {
	return FirewallSpec{
		Name:           permissiveACL(),
		DefaultIngress: "allow",
		DefaultEgress:  "allow",
		Rules: []FirewallRule{
			{Direction: "ingress", Action: "allow"},
			{Direction: "egress", Action: "allow"},
		},
	}
}

// coreOwnedACL reports whether a rule set is one this layer names by
// construction — an isolation set or the permissive posture set — so the
// ownership checks accept them before their description exists.
func coreOwnedACL(name string) bool {
	return isolationOwned(name) || name == permissiveACL()
}

// IsolateNetwork implements Isolator.
func (d *Incus) IsolateNetwork(ctx context.Context, network string, foreign []string) error {
	// This used to return early under OVN, on the grounds that "an OVN network
	// reaches nothing it is not peered with". That was false and never asserted:
	// both networks carry their block on the uplink this driver creates, so the
	// host routes between them. Measured, ICMP and TCP, on a station publishing
	// capabilities.isolation: true. See incus_ovn_isolate.go for the measurement
	// and for what applies this on the OVN path.
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
	// Exclusive on this network, then asked whether it is still there. Both
	// halves, because either alone leaves the race open (#386):
	//
	//   - the lock alone still lets a delete that won it run first, and the
	//     update that follows lands on a network the daemon has forgotten;
	//   - the question alone is a time-of-check that a delete crosses between
	//     the answer and the config edit it authorises.
	//
	// The answer comes from the daemon rather than from the prose of a failed
	// command, and it is the network object that is asked about: an interface
	// still standing proves nothing, since a network can outlive its object and
	// that is the leftover shape this exists to stop producing.
	//
	// TestIsolationRefusesANetworkWhoseDeleteAlreadyRan fails without the
	// question; TestAnIsolationDetachDoesNotOrphanTheNetworkBeingDeleted fails
	// without the lock.
	release := d.networkLock(network)
	defer release()
	gone, err := d.gone(ctx, "/1.0/networks/"+network)
	if err != nil {
		return fmt.Errorf("inspect network %s: %w", network, err)
	}
	if gone {
		return fmt.Errorf("%w: %s", ErrNetworkGone, network)
	}
	name := isolationACL(network)

	if len(foreign) == 0 {
		// Nothing to keep out: detach and drop the rule set rather than leave an
		// empty one attached, so `incus network show` says what is true.
		if _, err := d.run(ctx, "network", "unset", network, "security.acls"); err != nil {
			return fmt.Errorf("detach isolation from %s: %w", network, err)
		}
		// The permissive posture set was only there because the network carried
		// an ACL; with the network bare again, a NIC without one filters
		// nothing, which is the state the set was imitating. After the unset,
		// so the machines it covered never pass through a closed instant.
		if d.OVN {
			d.withdrawPermissive(ctx, network)
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
		Description: FirewallDescription,
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
	if !d.OVN {
		// Everything else passes: on a bridge this rule set exists to keep
		// subnets apart, not to filter, and it applies at the bridge-host
		// boundary where the NICs' own rule sets are a separate mechanism.
		//
		// Bridge mode only. Under OVN a network ACL applies to every NIC of the
		// network, in the single pipeline where a rule's priority comes from
		// its action (acl_ovn.go at v7.2.0: allow 300, reject 400, drop 500,
		// NIC default action 100/111) — so this catch-all allow at 300
		// outranked every security group's default deny at 100/111, and a port
		// no rule opens answered from the station on any multi-subnet run
		// (#491). Without it, unmatched traffic falls to the NIC default,
		// reject, which is exactly a group's default-deny; the machines that
		// deserve openness carry the permissive set instead (spreadPermissive).
		// The rejects above sit at 400 either way, above every allow a group
		// can state, which is what keeps two VPCs apart whatever their groups
		// say — the two halves #491 requires at once.
		//
		// TestAnOVNIsolationSetCarriesNoCatchAllAllow fails if the allows come
		// back under OVN; TestABridgeIsolationSetKeepsItsCatchAll fails if they
		// leave the bridge path.
		body.Egress = append(body.Egress, aclRule{Action: "allow", State: "enabled"})
		body.Ingress = append(body.Ingress, aclRule{Action: "allow", State: "enabled"})
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode isolation of %s: %w", network, err)
	}
	if _, err := d.run(ctx, "query", "-X", "PUT", "--data", string(encoded),
		"/1.0/network-acls/"+name); err != nil {
		return fmt.Errorf("write isolation of %s: %w", network, err)
	}

	// Before the network attach, so a machine with no restrictive group never
	// sits on an isolated network without its permissive set: the attach is
	// what makes the runtime add the default-deny to every NIC, and the set
	// must already be there when that happens.
	if d.OVN {
		d.spreadPermissive(ctx, network)
	}

	if _, err := d.run(ctx, "network", "set", network, "security.acls", name); err != nil {
		return fmt.Errorf("attach isolation to %s: %w", network, err)
	}
	return nil
}

// networkNIC is one instance interface of the emulator's own machines, as the
// permissive sweep sees it.
type networkNIC struct {
	instance  string
	device    string
	inherited bool
	acls      string
}

// instanceNICsOn lists the NICs sitting on the named network, restricted to
// instances the emulator created.
//
// The restriction is ownership, not shape: the instance list comes from the
// host, which also runs the operator's own containers, and the sweep edits
// network devices. Only instances carrying the label Binding writes at create
// (user.feint.provider) are listed, so no command this sweep emits can name
// anybody else's machine. Names are still checked for form before becoming
// arguments, because well formed and authorised are two different questions.
func (d *Incus) instanceNICsOn(ctx context.Context, network string) ([]networkNIC, error) {
	out, err := d.run(ctx, "query", "/1.0/instances?recursion=1")
	if err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}
	var raw []struct {
		Name            string                       `json:"name"`
		Config          map[string]string            `json:"config"`
		ExpandedDevices map[string]map[string]string `json:"expanded_devices"`
		Devices         map[string]map[string]string `json:"devices"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("decode the instance list: %w", err)
	}
	var nics []networkNIC
	for _, inst := range raw {
		if inst.Config["user."+LabelKey] == "" || !safeName.MatchString(inst.Name) {
			continue
		}
		for name, device := range inst.ExpandedDevices {
			if device["type"] != "nic" || device["network"] != network || !safeName.MatchString(name) {
				continue
			}
			_, own := inst.Devices[name]
			nics = append(nics, networkNIC{
				instance:  inst.Name,
				device:    name,
				inherited: !own,
				acls:      device["security.acls"],
			})
		}
	}
	return nics, nil
}

// spreadPermissive puts the permissive posture set on every rule-set-less NIC
// of the emulator's machines on the named network, before the network gains an
// ACL. ApplyFirewall establishes the same invariant when a machine's groups
// change; this covers the machines that were already applied when the network
// became isolated. Failures are logged rather than fatal: the isolation itself
// must still land, and a machine left closed is visible in the log where a
// silent skip is not.
//
// TestIsolatingANetworkSpreadsThePermissiveSetToSetlessNICs fails without it.
func (d *Incus) spreadPermissive(ctx context.Context, network string) {
	nics, err := d.instanceNICsOn(ctx, network)
	if err != nil {
		d.logger().Error("could not list the network's interfaces before isolating it; "+
			"machines without a security group may be left closed",
			"network", network, "error", err)
		return
	}
	ensured := false
	for _, nic := range nics {
		if nic.acls != "" {
			continue
		}
		if !ensured {
			if err := d.EnsureFirewall(ctx, permissiveSpec()); err != nil {
				d.logger().Error("could not write the permissive posture set",
					"network", network, "error", err)
				return
			}
			ensured = true
		}
		verb := "set"
		if nic.inherited {
			verb = "override"
		}
		if _, err := d.run(ctx, "config", "device", verb, nic.instance, nic.device,
			"security.acls="+permissiveACL()); err != nil {
			d.logger().Error("could not keep a group-less machine open on an isolated network",
				"network", network, "machine", nic.instance, "device", nic.device, "error", err)
		}
	}
}

// withdrawPermissive is the exact undo, after the network's ACL is gone: a NIC
// carrying only the permissive set goes back to carrying none, so a later
// probe of the host reads the same state as before the network was isolated.
// NICs carrying anything else — a security group, with or without company —
// are none of this sweep's business.
func (d *Incus) withdrawPermissive(ctx context.Context, network string) {
	nics, err := d.instanceNICsOn(ctx, network)
	if err != nil {
		d.logger().Error("could not list the network's interfaces after de-isolating it",
			"network", network, "error", err)
		return
	}
	for _, nic := range nics {
		if nic.acls != permissiveACL() {
			continue
		}
		verb := "set"
		if nic.inherited {
			verb = "override"
		}
		if _, err := d.run(ctx, "config", "device", verb, nic.instance, nic.device,
			"security.acls="); err != nil {
			d.logger().Error("could not take the permissive posture set off an interface",
				"network", network, "machine", nic.instance, "device", nic.device, "error", err)
		}
	}
}
