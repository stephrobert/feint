package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Incus network ACLs are what a security group becomes. Three facts from the
// documentation shape everything here, and each of them was measured on Incus
// 6.0.0 before being relied on.
//
// An ACL attached to a network only filters at the boundary between the bridge
// and the host: it cannot separate two instances of the same bridge, "except
// when applied directly to NICs". So rule sets are attached to NICs, which is
// also where a security group belongs, since it describes a server rather than a
// subnet.
//
// On a NIC, security.acls.default.ingress.action and its egress counterpart
// default to drop. Attaching a rule set therefore denies everything it does not
// allow, which is exactly a security group's semantics, and the reason those
// keys are set explicitly here rather than left out.
//
// Selectors (@internal, ACL names, @network/peer) are not supported on bridges.
// A rule that names another group has to reach this layer already expanded into
// blocks; there is nothing this driver can do about it.

// aclDescription marks a rule set as the emulator's own. Rule sets carry no
// user config, so this is what a sweep recognises them by.
const aclDescription = "feint security group"

// mustOwnACL refuses to touch a rule set the emulator did not create.
//
// It reads the same two markers ownedACLs sweeps by, and for the same reasons:
// the description, written by the PUT that follows the create, and the isolation
// names, which are the one case that can exist without one when a run is
// interrupted. Checking the isolation name first also avoids a round trip on the
// path that deletes one.
//
// A not-found error is returned as-is: whether an absent rule set is a problem is
// the caller's decision, not this function's.
func (d *Incus) mustOwnACL(ctx context.Context, name string) error {
	if isolationOwned(name) {
		return nil
	}
	out, err := d.run(ctx, "query", "/1.0/network-acls/"+name)
	if err != nil {
		return err
	}
	var acl struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(out, &acl); err != nil {
		return fmt.Errorf("read the description of firewall %s: %w", name, err)
	}
	if !strings.HasPrefix(acl.Description, aclDescription) {
		return fmt.Errorf("firewall %s was not created by the emulator; refusing to delete it", name)
	}
	return nil
}

// aclRule is the wire shape Incus expects, which is not FirewallRule: ports are
// a string, "80" or "80-90", and the protocol names differ.
type aclRule struct {
	Action          string `json:"action"`
	State           string `json:"state"`
	Protocol        string `json:"protocol,omitempty"`
	Source          string `json:"source,omitempty"`
	Destination     string `json:"destination,omitempty"`
	DestinationPort string `json:"destination_port,omitempty"`
}

type aclBody struct {
	Description string            `json:"description"`
	Ingress     []aclRule         `json:"ingress"`
	Egress      []aclRule         `json:"egress"`
	Config      map[string]string `json:"config"`
}

// EnsureFirewall implements Firewaller.
//
// The whole set is replaced in one PUT rather than patched rule by rule. A
// security group is edited by replacing its list (Scaleway's own
// SetSecurityGroupRules does exactly that), and replacing here is what makes a
// revoked rule actually disappear.
func (d *Incus) EnsureFirewall(ctx context.Context, spec FirewallSpec) error {
	if !safeName.MatchString(spec.Name) {
		return fmt.Errorf("invalid firewall name %q", spec.Name)
	}

	if _, err := d.run(ctx, "network", "acl", "show", spec.Name); err != nil {
		if !isNotFound(err) {
			return fmt.Errorf("inspect firewall %s: %w", spec.Name, err)
		}
		if _, err := d.run(ctx, "network", "acl", "create", spec.Name); err != nil {
			return fmt.Errorf("create firewall %s: %w", spec.Name, err)
		}
	}

	body := aclBody{
		Description: aclDescription,
		Ingress:     []aclRule{},
		Egress:      []aclRule{},
		Config:      map[string]string{},
	}
	for _, rule := range spec.Rules {
		converted, ok := toACLRule(rule)
		if !ok {
			continue
		}
		if rule.Direction == "egress" {
			body.Egress = append(body.Egress, converted)
		} else {
			body.Ingress = append(body.Ingress, converted)
		}
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode firewall %s: %w", spec.Name, err)
	}
	if _, err := d.run(ctx, "query", "-X", "PUT", "--data", string(encoded),
		"/1.0/network-acls/"+spec.Name); err != nil {
		return fmt.Errorf("write firewall %s: %w", spec.Name, err)
	}
	return nil
}

// ApplyFirewall implements Firewaller. It sets the rule sets, and the default
// actions, on every NIC of the machine.
//
// A machine can carry NICs of both kinds at once — the runtime's default
// profile hands out a bridged eth0 next to the OVN interfaces the pack
// attached — so the split below is per NIC, not per driver mode.
func (d *Incus) ApplyFirewall(ctx context.Context, machine string, binding FirewallBinding) error {
	if !safeName.MatchString(machine) {
		return fmt.Errorf("invalid machine name %q", machine)
	}
	// Well formed is not authorised, and this path had only the first half.
	//
	// safeName answers "could this become a command argument safely". It accepts
	// `production-database` and every other name on the host. The name arrives
	// from Resource.Runtime, which `PUT /_feint/state` and `feint snapshot load`
	// restore verbatim — snapshot.go documents the format as meant to outlive its
	// instance and be loaded into another one — so a crafted snapshot named any
	// instance and this call edited its network devices.
	//
	// RemoveFirewall already asks the question. The list of guarded paths forgot
	// that *installing* a rule set on somebody else's NIC is as much a change as
	// removing one: reconfiguring paths belong in it, not only destructive ones.
	//
	// TestApplyFirewallRefusesAnInstanceTheEmulatorDidNotCreate fails without this.
	if err := d.mustOwnInstance(ctx, machine); err != nil {
		return err
	}
	devices, err := d.networkDevices(ctx, machine)
	if err != nil {
		return err
	}

	joined := strings.Join(binding.Names, ",")
	networkTypes := map[string]string{}
	for _, device := range devices {
		// "set" for a device the instance owns, "override" for one it inherits
		// from a profile. Incus refuses the first on an inherited device, and
		// the second copies it locally rather than editing the profile every
		// other instance shares.
		verb := "set"
		if device.inherited {
			verb = "override"
		}
		args := []string{"config", "device", verb, machine, device.name, "security.acls=" + joined}
		switch {
		case d.isOVNNetwork(ctx, device.network, networkTypes):
			// Only security.acls: it is the one ACL key an OVN NIC updates in
			// place (UpdatableFields in nic_ovn.go at v7.2.0). Any other key
			// makes Incus remove and re-add the device, and the guest loses
			// every address the interface carried — measured on 7.2, with the
			// machine unreachable afterwards. Unmatched traffic then falls to
			// the NIC's own default, reject; a permissive group states its
			// openness as a catch-all allow rule inside the rule set, which
			// EnsureFirewall replaces live.
		case joined != "":
			// The default actions only mean something while a rule set is
			// attached, and Incus rejects them on a NIC that carries none.
			args = append(args,
				"security.acls.default.ingress.action="+orDrop(binding.DefaultIngress),
				"security.acls.default.egress.action="+orDrop(binding.DefaultEgress))
		}
		if _, err := d.run(ctx, args...); err != nil {
			return fmt.Errorf("apply firewall to %s/%s: %w", machine, device.name, err)
		}
	}
	return nil
}

// isOVNNetwork reports whether a network is OVN-typed, caching the answer for
// the duration of one call: a machine's NICs often share a network, and each
// lookup is a runtime round trip.
func (d *Incus) isOVNNetwork(ctx context.Context, network string, cache map[string]string) bool {
	if network == "" {
		return false
	}
	kind, known := cache[network]
	if !known {
		out, err := d.run(ctx, "query", "/1.0/networks/"+network)
		if err == nil {
			var raw struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(out, &raw) == nil {
				kind = raw.Type
			}
		}
		cache[network] = kind
	}
	return kind == "ovn"
}

// orDrop keeps the runtime's own default when the caller says nothing, which is
// the safe direction: an unstated policy denies rather than opens.
func orDrop(action string) string {
	switch action {
	case "allow", "reject", "drop":
		return action
	default:
		return "drop"
	}
}

// RemoveFirewall implements Firewaller.
func (d *Incus) RemoveFirewall(ctx context.Context, name string) error {
	if !safeName.MatchString(name) {
		return fmt.Errorf("invalid firewall name %q", name)
	}
	// Ours, or nothing happens: the name comes from a stored resource, and this
	// path had no check at all. The marker cannot be the name, because a pack
	// chooses that prefix and the core must not know a provider's spelling. It is
	// the description, which is what a sweep already recognises rule sets by.
	//
	// An absent rule set is not a refusal: deleting one twice is the normal path
	// when a security group is removed after a sweep.
	if err := d.mustOwnACL(ctx, name); err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}
	if _, err := d.run(ctx, "network", "acl", "delete", name); err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete firewall %s: %w", name, err)
	}
	return nil
}

// FirewallName derives a rule-set name from a resource ID, the way NetworkName
// does for networks. ACL names do not become interfaces, so the limit is milder,
// but a stable short name still beats a raw UUID in a log line.
func FirewallName(prefix, id string) string {
	return NetworkName(prefix, id)
}

// nicDevice is one interface of a machine: its name, the network it sits on,
// and whether it comes from a profile.
type nicDevice struct {
	name      string
	network   string
	inherited bool
}

// networkDevices lists every NIC of a machine, its own and the ones it inherits.
//
// Inherited NICs matter: an instance launched without an explicit network gets
// eth0 from the default profile, carries an address on it, and answers there. A
// firewall that skipped it would leave the machine reachable on an address the
// control plane never published, which is the exact defect this project is
// built to avoid. They are overridden rather than edited, so the profile every
// other instance shares stays untouched.
func (d *Incus) networkDevices(ctx context.Context, machine string) ([]nicDevice, error) {
	out, err := d.run(ctx, "query", "/1.0/instances/"+machine)
	if err != nil {
		return nil, fmt.Errorf("inspect devices of %s: %w", machine, err)
	}
	var raw struct {
		ExpandedDevices map[string]map[string]string `json:"expanded_devices"`
		Devices         map[string]map[string]string `json:"devices"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("decode devices of %s: %w", machine, err)
	}

	devices := make([]nicDevice, 0, len(raw.ExpandedDevices))
	for name, device := range raw.ExpandedDevices {
		if device["type"] != "nic" {
			continue
		}
		_, own := raw.Devices[name]
		devices = append(devices, nicDevice{name: name, network: device["network"], inherited: !own})
	}
	return devices, nil
}

// toACLRule converts one emulated rule. It reports false for a rule the runtime
// cannot express, which the caller drops rather than approximating: a rule
// enforced differently from what the API describes is worse than one visibly
// absent.
func toACLRule(rule FirewallRule) (aclRule, bool) {
	out := aclRule{Action: rule.Action, State: "enabled"}
	switch rule.Action {
	// allow-stateless is a real runtime action, and the translation of a
	// stateless security group: dropping it here would silently turn the
	// group's rules back into stateful ones.
	case "allow", "allow-stateless", "drop", "reject":
	default:
		return aclRule{}, false
	}

	switch strings.ToLower(rule.Protocol) {
	case "", "any":
		out.Protocol = ""
	case "tcp":
		out.Protocol = "tcp"
	case "udp":
		out.Protocol = "udp"
	case "icmp", "icmp4":
		out.Protocol = "icmp4"
	default:
		return aclRule{}, false
	}

	out.Source = rule.Source
	out.Destination = rule.Destination

	// Ports only exist for TCP and UDP; Incus rejects the field otherwise.
	if out.Protocol == "tcp" || out.Protocol == "udp" {
		out.DestinationPort = portRange(rule.PortFrom, rule.PortTo)
	}
	return out, true
}

func portRange(from, to int) string {
	switch {
	case from <= 0 && to <= 0:
		return ""
	case from <= 0:
		return strconv.Itoa(to)
	case to <= 0 || to == from:
		return strconv.Itoa(from)
	default:
		return strconv.Itoa(from) + "-" + strconv.Itoa(to)
	}
}
