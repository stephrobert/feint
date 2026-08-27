package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
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
	if coreOwnedACL(name) {
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
//
// All-or-nothing is what makes the two halves below load-bearing (#454). A rule
// the daemon refuses does not fail alone: it fails the PUT, and every rule of
// the group with it, while the API keeps describing all of them. So a rule this
// driver cannot express is dropped here and *said*, and a write the host still
// refuses withdraws the claim that this runtime enforces anything.
func (d *Incus) EnsureFirewall(ctx context.Context, spec FirewallSpec) error {
	if !safeName.MatchString(spec.Name) {
		return fmt.Errorf("invalid firewall name %q", spec.Name)
	}

	if _, err := d.run(ctx, "network", "acl", "show", spec.Name); err != nil {
		if !isNotFound(err) {
			return d.firewallRefused(fmt.Errorf("inspect firewall %s: %w", spec.Name, err))
		}
		if _, err := d.run(ctx, "network", "acl", "create", spec.Name); err != nil {
			return d.firewallRefused(fmt.Errorf("create firewall %s: %w", spec.Name, err))
		}
	}

	body := aclBody{
		Description: aclDescription,
		Ingress:     []aclRule{},
		Egress:      []aclRule{},
		Config:      map[string]string{},
	}
	var dropped []string
	for _, rule := range spec.Rules {
		converted := toACLRules(rule)
		if len(converted) == 0 {
			dropped = append(dropped, describeRule(rule))
			continue
		}
		if rule.Direction == "egress" {
			body.Egress = append(body.Egress, converted...)
		} else {
			body.Ingress = append(body.Ingress, converted...)
		}
	}
	if len(dropped) > 0 {
		// "Visibly absent" was the word the contract used, and nothing was
		// saying it: a rule the runtime cannot express left no trace at all,
		// while the API went on serving it. TestADroppedRuleIsReported fails
		// without this line.
		d.logger().Warn("some rules of a security group are not expressible by this runtime "+
			"and were left out of the rule set; the API still describes them",
			"firewall", spec.Name, "rules", strings.Join(dropped, "; "))
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode firewall %s: %w", spec.Name, err)
	}
	if _, err := d.run(ctx, "query", "-X", "PUT", "--data", string(encoded),
		"/1.0/network-acls/"+spec.Name); err != nil {
		return d.firewallRefused(fmt.Errorf("write firewall %s: %w", spec.Name, err))
	}
	return nil
}

// firewallRefused records that the host refused a rule set this driver had
// already accepted, and returns the error unchanged so no caller has to know.
//
// The claim goes and does not come back. `capabilities.firewall` says "a
// security group's rules are enforced on the machine's interface", and a refused
// write is the host answering that they are not — for that group, now, and for
// any group whose rules meet the same refusal. #181 settled the direction for
// this exact question: what the host answered wins over what the flag promised,
// and a false capability is strictly worse than none, because this project tells
// every consumer to key on the capability rather than on a mode name.
//
// Only a refusal *by the host* counts. A name this driver refused itself never
// reached the daemon, and it arrives from a restorable snapshot — letting it
// flip a published claim would hand that switch to a crafted state file.
//
// TestAFirewallWriteTheHostRefusesWithdrawsTheCapability fails without this.
func (d *Incus) firewallRefused(err error) error {
	if d.firewallDenied.CompareAndSwap(false, true) {
		d.logger().Warn("the runtime refused a security group's rule set, so this process no "+
			"longer claims to enforce firewalls (capabilities.firewall is now false)",
			"error", err)
	}
	return err
}

// ApplyFirewall implements Firewaller. It sets the rule sets, and the default
// actions, on every NIC of the machine the binding covers.
//
// A machine can carry NICs of both kinds at once — the runtime's default
// profile hands out a bridged eth0 next to the OVN interfaces the pack
// attached — so the split below is per NIC, not per driver mode.
//
// "Every NIC" was the whole sentence until 2026-08-27, and it was the defect
// (#574): a pack whose security groups do not reach inside its private
// networks got them written onto the membership NIC all the same, so two
// machines of one segment could not reach each other. What a group covers is
// declared per network by the pack (Attachment.Unfiltered) and arrives here on
// FirewallBinding.Unfiltered.
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
	networks := map[string]networkACLInfo{}
	permissiveEnsured := false
	var escaped []string
	for _, device := range devices {
		// What this interface is asked to wear, which is not always what the
		// machine wears (#574). A pack declares the networks its security
		// groups do not cover — Exoscale's private networks, where upstream
		// says in as many words that "security group rules do not apply" —
		// and an interface on one of them is bound as if the machine wore no
		// group at all.
		//
		// Emptied rather than skipped, deliberately, and each half is
		// measured. Skipping would leave a rule set a previous version of this
		// code had already written standing on the NIC, so the defect would
		// survive a restart of the very machine that fixes it. And the empty
		// binding is not "write nothing": under OVN a network carrying the
		// emulator's isolation set forces the reject default onto every NIC
		// attached to it, so the branch below dresses a set-less NIC in the
		// permissive posture instead — an unfiltered interface must end up
		// open inside its own segment, which is the entire point.
		//
		// TestNoRuleSetIsBoundToAnUnfilteredInterface and
		// TestAnUnfilteredInterfaceStaysOpenOnAnIsolatedNetwork fail without
		// this; TestApplyFirewallCoversEveryInterface holds the accepting
		// half.
		attached := joined
		if !binding.covers(device.network) {
			attached = ""
		}
		// A routed NIC takes no rule set at all (#337). Measured on Incus 7.2
		// and confirmed by the 7.3 wording of the same refusal: every security
		// option is an invalid device option there — security.acls, its two
		// default actions, and both filtering flags — so there is no mechanism
		// to enforce with, only keys to be refused on.
		//
		// Detaching skips it: no rule set has ever been attached to a routed
		// NIC, so an empty binding has nothing to take back. A binding that
		// names rule sets is refused with the typed error instead of being
		// half-sent: until #337 the doomed keys were sent, the failure was
		// logged as an operational ERROR, and the control plane answered as if
		// the group were enforced — a green light over rules that existed
		// nowhere. The other interfaces still get their rule sets first; a
		// machine with a private NIC beside its routed one keeps its private
		// plane covered.
		//
		// Every escaping interface is named, with the addresses it carries,
		// and that is #548 rather than tidiness. The refusal used to keep the
		// first one and drop the rest, and to name the device alone — so an
		// operator reading the log learned that "eth0" was uncovered on a
		// machine whose interfaces they cannot see, when what they needed was
		// *which published address answers with no rule set on it*. Measured
		// on 2026-08-27 under `--vm incus-ovn`, on the shape
		// examples/stacks/scaleway produces and reproduced from the API alone:
		// a server created with its flexible IP keeps a routed eth0 carrying
		// 203.0.113.2 beside a filtered eth1, and port 22 — which the group's
		// drop default never opened — answered from the station while the
		// same port on the private address was refused.
		//
		// TestApplyFirewallRefusesAGroupOnARoutedNIC and
		// TestApplyFirewallDetachIgnoresARoutedNIC fail without this, and
		// TestTheUnenforceableRefusalNamesTheAddressThatEscapes fails without
		// the addresses.
		if device.routed {
			if attached != "" {
				escaped = append(escaped, device.describe())
			}
			continue
		}
		// "set" for a device the instance owns, "override" for one it inherits
		// from a profile. Incus refuses the first on an inherited device, and
		// the second copies it locally rather than editing the profile every
		// other instance shares.
		verb := "set"
		if device.inherited {
			verb = "override"
		}
		args := []string{"config", "device", verb, machine, device.name, "security.acls=" + attached}
		switch {
		case d.isOVNNetwork(ctx, device.network, networks):
			// Only security.acls: it is the one ACL key an OVN NIC updates in
			// place (UpdatableFields in nic_ovn.go at v7.2.0). Any other key
			// makes Incus remove and re-add the device, and the guest loses
			// every address the interface carried — measured on 7.2, with the
			// machine unreachable afterwards. Unmatched traffic then falls to
			// the NIC's own default, reject; a permissive group states its
			// openness as a catch-all allow rule inside the rule set, which
			// EnsureFirewall replaces live.
			//
			// One case takes more than the joined list: no rule set to attach,
			// on a network that carries the emulator's isolation set. That
			// network ACL forces the reject default onto every NIC (a network's
			// security.acls "apply to NICs connected to this network"), so a
			// bare detach would close a machine whose groups enforce nothing —
			// the exact opposite of what "enforces nothing" means. The NIC
			// wears the permissive posture set instead, and the isolation's
			// rejects at 400 still outrank its catch-all allows at 300, so the
			// machine stays open to everything but the foreign subnets (#491).
			//
			// TestAMachineWithoutAGroupStaysOpenOnAnIsolatedNetwork fails
			// without this; TestAnEmptyBindingStillClearsANICOnAnUnisolatedNetwork
			// holds the other half.
			if attached == "" && networks[device.network].acls != "" {
				if !permissiveEnsured {
					if err := d.EnsureFirewall(ctx, permissiveSpec()); err != nil {
						return fmt.Errorf("apply firewall to %s/%s: %w", machine, device.name, err)
					}
					permissiveEnsured = true
				}
				args[len(args)-1] = "security.acls=" + permissiveACL()
			}
		case attached != "":
			// The default actions only mean something while a rule set is
			// attached, and Incus rejects them on a NIC that carries none.
			args = append(args,
				"security.acls.default.ingress.action="+orDrop(binding.DefaultIngress),
				"security.acls.default.egress.action="+orDrop(binding.DefaultEgress))
		}
		// runUntilFree, not run: this edit makes the daemon re-ensure every
		// rule set the NIC references in OVN, and two such edits crossing —
		// the two machines of one group applied concurrently, or an address
		// route holding the instance — fail in one of the transient shapes
		// isTransientConflict names. Measured: an OVSDB port-group collision
		// left one NIC with no rule set at all while the API said applied,
		// which is a machine open (or closed) against everything the control
		// plane claims. TestApplyFirewallRidesOutADuplicatePortGroupRace
		// fails without the retry.
		if _, err := d.runUntilFree(ctx, args...); err != nil {
			return fmt.Errorf("apply firewall to %s/%s: %w", machine, device.name, err)
		}
	}
	if len(escaped) > 0 {
		slices.Sort(escaped)
		return fmt.Errorf("apply firewall to %s: a routed NIC accepts no security option, "+
			"so what it carries is covered by nothing: %s: %w",
			machine, strings.Join(escaped, ", "), ErrFirewallUnenforceable)
	}
	return nil
}

// networkACLInfo is what one network lookup answers ApplyFirewall: its type,
// and whether the network itself carries rule sets — which under OVN is what
// decides the posture of a NIC that attaches none of its own.
type networkACLInfo struct {
	kind string
	acls string
}

// isOVNNetwork reports whether a network is OVN-typed, caching the answer for
// the duration of one call: a machine's NICs often share a network, and each
// lookup is a runtime round trip.
func (d *Incus) isOVNNetwork(ctx context.Context, network string, cache map[string]networkACLInfo) bool {
	if network == "" {
		return false
	}
	info, known := cache[network]
	if !known {
		out, err := d.run(ctx, "query", "/1.0/networks/"+network)
		if err == nil {
			var raw struct {
				Type   string            `json:"type"`
				Config map[string]string `json:"config"`
			}
			if json.Unmarshal(out, &raw) == nil {
				info = networkACLInfo{kind: raw.Type, acls: raw.Config["security.acls"]}
			}
		}
		cache[network] = info
	}
	return info.kind == "ovn"
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
// whether it comes from a profile, whether it is a routed NIC — the one kind
// that sits on no network and takes no security option — and the addresses it
// delivers.
type nicDevice struct {
	name      string
	network   string
	inherited bool
	routed    bool
	// addresses are what this interface makes the machine answer on: the ones
	// the launch pinned (ipv4.address) and the ones routed onto it afterwards
	// (ipv4.routes). Read only to be *named*, never to decide anything — a
	// refusal that says "eth0" tells an operator nothing they can check, and a
	// refusal that says "eth0 (203.0.113.2)" names the address a client is
	// about to connect to (#548).
	addresses []string
}

// describe is how one escaping interface is named in a refusal: the device,
// and what it makes the machine answer on. An interface with no address is
// named alone rather than with an empty pair of brackets.
func (n nicDevice) describe() string {
	if len(n.addresses) == 0 {
		return n.name
	}
	return n.name + " (" + strings.Join(n.addresses, ", ") + ")"
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
		devices = append(devices, nicDevice{
			name:      name,
			network:   device["network"],
			inherited: !own,
			routed:    device["nictype"] == "routed",
			addresses: carriedAddresses(device),
		})
	}
	return devices, nil
}

// carriedAddresses is what one device makes the machine answer on: the pinned
// addresses of the launch and the routes added onto it afterwards, the mask
// dropped so the value reads as an address a client would dial.
//
// Both keys, because a public address reaches a machine through either — the
// launch writes ipv4.address on a routed NIC, routeOntoRoutedNIC writes
// ipv4.routes on the same one — and a refusal that named only half of them
// would be silent about exactly the addresses attached after the boot.
func carriedAddresses(device map[string]string) []string {
	out := make([]string, 0, 2)
	for _, key := range []string{"ipv4.address", "ipv4.routes"} {
		for _, entry := range splitList(device[key]) {
			address, _, _ := strings.Cut(entry, "/")
			if address != "" && !slices.Contains(out, address) {
				out = append(out, address)
			}
		}
	}
	return out
}

// toACLRules converts one emulated rule into the rules the runtime can express.
//
// It returns nothing for a rule the runtime cannot express, which the caller
// drops — and says so — rather than approximating: a rule enforced differently
// from what the API describes is worse than one visibly absent. Dropping *that
// rule* is the whole point. EnsureFirewall writes the set in one PUT, so a rule
// this function let through and the daemon then refused cost every rule beside
// it: a group whose API described two rules enforced one (#454).
//
// It can also return two, because the runtime has no single ICMP protocol.
// icmp4 and icmp6 are separate protocols there, each refusing the other
// family's addresses, so a rule that names no address at all means both.
func toACLRules(rule FirewallRule) []aclRule {
	switch rule.Action {
	// allow-stateless is a real runtime action, and the translation of a
	// stateless security group: dropping it here would silently turn the
	// group's rules back into stateful ones.
	case "allow", "allow-stateless", "drop", "reject":
	default:
		return nil
	}

	protocols, ok := aclProtocols(rule)
	if !ok {
		return nil
	}

	out := make([]aclRule, 0, len(protocols))
	for _, protocol := range protocols {
		converted := aclRule{
			Action:      rule.Action,
			State:       "enabled",
			Protocol:    protocol,
			Source:      rule.Source,
			Destination: rule.Destination,
		}
		// Ports only exist for TCP and UDP; Incus rejects the field otherwise.
		if protocol == "tcp" || protocol == "udp" {
			converted.DestinationPort = portRange(rule.PortFrom, rule.PortTo)
		}
		out = append(out, converted)
	}
	return out
}

// aclProtocols reports the runtime protocols one rule becomes, and false when
// no protocol expresses it.
//
// The ICMP half is where #454 lived: the protocol used to be chosen from the
// rule's own name — `case "icmp", "icmp4"` — and the address family was never
// read, so an ICMP rule sourced from an IPv6 block was written as icmp4 and the
// daemon refused the whole PUT with `Cannot use IPv6 source addresses with
// "icmp4" protocol`. The family of the addresses is the fact that decides here;
// the name only decides when the addresses fix nothing.
// TestAnICMPRuleWithAnIPv6SourceKeepsItsGroup fails without this.
func aclProtocols(rule FirewallRule) ([]string, bool) {
	switch name := strings.ToLower(strings.TrimSpace(rule.Protocol)); name {
	case "", "any":
		// The empty protocol is "every protocol" on the wire, and it takes no
		// family: an "any" rule carries whatever addresses it was given.
		return []string{""}, true
	case "tcp", "udp":
		return []string{name}, true
	default:
		named, isICMP := icmpFamilyOfName(name)
		if !isICMP {
			return nil, false
		}
		return icmpProtocols(named, rule.Source, rule.Destination)
	}
}

// icmpFamilyOfName reports the address family an ICMP spelling fixes — 4, 6, or
// 0 for a spelling that fixes none — and false for a name that is not ICMP.
//
// "icmp4" fixes nothing, which reads odd and is deliberate: it is this package's
// own documented spelling for ICMP (see FirewallRule.Protocol), taken from the
// runtime's wire name rather than written as an assertion about a family, and
// the only ICMP value Scaleway has is the family-agnostic "ICMP". Reading it as
// a claim about IPv4 would drop exactly the rules #454 is about. The v6
// spellings do fix one: a caller that writes icmpv6 has named the family, and
// three vocabularies spell it three ways.
func icmpFamilyOfName(name string) (int, bool) {
	switch name {
	case "icmp", "icmp4", "icmpv4":
		return 0, true
	case "icmp6", "icmpv6", "ipv6-icmp":
		return 6, true
	}
	return 0, false
}

// icmpProtocols picks between the runtime's two ICMP protocols from the family
// of the rule's own addresses, falling back to the family its name fixed.
func icmpProtocols(named int, source, destination string) ([]string, bool) {
	sourceV4, sourceV6, sourceRead := addressFamilies(source)
	destinationV4, destinationV6, destinationRead := addressFamilies(destination)
	if !sourceRead || !destinationRead {
		// An address this cannot read is not an address that is absent. Picking
		// a family for it would send the daemon a value it is about to refuse,
		// and the refusal costs the whole group.
		return nil, false
	}

	v4 := sourceV4 || destinationV4
	v6 := sourceV6 || destinationV6
	switch {
	case v4 && v6:
		// One rule, both families, and no ICMP protocol covers both.
		return nil, false
	case v4:
		if named == 6 {
			// The name says one family and the addresses say the other. Never
			// approximated: the rule goes, alone and named in the log.
			return nil, false
		}
		return []string{"icmp4"}, true
	case v6:
		if named == 4 {
			return nil, false
		}
		return []string{"icmp6"}, true
	case named == 4:
		return []string{"icmp4"}, true
	case named == 6:
		return []string{"icmp6"}, true
	default:
		// Nothing fixes a family: "ICMP from anywhere" means both of them, and
		// half of it enforced silently is the same defect one size smaller.
		return []string{"icmp4", "icmp6"}, true
	}
}

// addressFamilies reports the families an ACL address field names, and whether
// it could be read at all.
//
// Incus accepts a comma-separated list of CIDR blocks, bare addresses and
// dash-separated ranges in `source` and `destination`, so each member is read on
// its own. The third result is the third outcome a reader owes its caller: an
// unreadable address is not an absent one, and mapping it onto "no family" would
// silently pick a protocol for a value the daemon will reject.
func addressFamilies(field string) (v4, v6, readable bool) {
	if strings.TrimSpace(field) == "" {
		return false, false, true
	}
	for _, member := range strings.Split(field, ",") {
		member = strings.TrimSpace(member)
		if member == "" {
			return false, false, false
		}
		for _, bound := range strings.SplitN(member, "-", 2) {
			addr, ok := parseAddressOrPrefix(strings.TrimSpace(bound))
			if !ok {
				return false, false, false
			}
			if addr.Is4() || addr.Is4In6() {
				v4 = true
			} else {
				v6 = true
			}
		}
	}
	return v4, v6, true
}

// parseAddressOrPrefix reads one member of an address field: "10.0.0.0/8",
// "::/0" or a bare address.
func parseAddressOrPrefix(s string) (netip.Addr, bool) {
	if prefix, err := netip.ParsePrefix(s); err == nil {
		return prefix.Addr(), true
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr, true
}

// describeRule renders a rule for the line that reports it was dropped. A rule
// the API still serves is only "visibly absent" if something is saying so.
func describeRule(rule FirewallRule) string {
	parts := []string{rule.Direction, rule.Action}
	if rule.Protocol != "" {
		parts = append(parts, rule.Protocol)
	}
	if rule.Source != "" {
		parts = append(parts, "from "+rule.Source)
	}
	if rule.Destination != "" {
		parts = append(parts, "to "+rule.Destination)
	}
	if ports := portRange(rule.PortFrom, rule.PortTo); ports != "" {
		parts = append(parts, "port "+ports)
	}
	return strings.Join(parts, " ")
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
