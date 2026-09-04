package machine

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// A Plan is a recipe, and two of its steps can claim the same property without
// anything noticing (#667).
//
// RouteAddress lays a machine's default route towards the routed next hop
// (routedNetworkConfig, repairRoutedInterface). installGuestDefaultRoute
// replaces it with the gateway of the network Plan.Egress names. Both run
// inside the Reconciler, both succeed, every exec returns 0 — and on a Scaleway
// server holding a public address AND a private NIC the second overwrote the
// first. Measured 2026-09-04 under `--vm incus-ovn`:
//
//	default via 10.77.0.1 dev eth0        <- the private gateway
//	eth0 carries 10.77.0.2/24 and 203.0.113.2/32
//	from the station: nothing
//
// #660 is that failure seen from the far end, two steps later, as "nothing
// answers at 203.0.113.4:443". Its fix lives in the pack: egressNetworkOf
// claims nothing when a public address is present. The layer had no guard, so
// a fourth pack — or a change in this one — reintroduces the contradiction and
// no test moves.
//
// This file is the guard, in two halves. Expect walks the plan and records
// WHO claims WHAT; a property with more than one claimant refuses the plan
// before the runtime is asked for anything, which makes it the cheapest of the
// three controls the series builds. And what Expect returns on a consistent
// plan is the list of claims a later reading answers (#668), each with its own
// tolerance built in, so that a lease renewed or a metric changed never reads
// as a defect.
//
// No provider is named here and none can be: the plan is the pack's, the
// dialect is the driver's, and this file knows only the two.

// Outcome is what one claim answered against a Shape. Three, never two: a
// read that failed is neither held nor broken, and folding it onto either is
// how a verifier starts lying.
type Outcome int

const (
	// Held: the runtime carries what the plan claims.
	Held Outcome = iota
	// Broken: it carries something else, and the verdict says what.
	Broken
	// Unreadable: the fact could not be read, so nothing is known about it.
	Unreadable
)

// String names the outcome the way the counters on /_feint/health spell it.
func (o Outcome) String() string {
	switch o {
	case Held:
		return "held"
	case Broken:
		return "broken"
	case Unreadable:
		return "unreadable"
	}
	return fmt.Sprintf("outcome(%d)", int(o))
}

// Verdict is one claim's answer.
type Verdict struct {
	// Claim names the claim, as its String() spells it.
	Claim string
	// Outcome is what the comparison found.
	Outcome Outcome
	// Want is what the plan claimed, rendered.
	Want string
	// Got is what the runtime carried instead, or why it could not be read.
	Got string
}

// String renders a verdict the way the log and the resource cite it.
func (v Verdict) String() string {
	switch v.Outcome {
	case Held:
		return v.Claim + " held"
	case Unreadable:
		return v.Claim + " unreadable: " + v.Got
	}
	return v.Claim + " broken: want " + v.Want + ", got " + v.Got
}

func held(c Claim) Verdict { return Verdict{Claim: c.String(), Outcome: Held} }

func broken(c Claim, want, got string) Verdict {
	return Verdict{Claim: c.String(), Outcome: Broken, Want: want, Got: got}
}

func unreadable(c Claim, why string) Verdict {
	return Verdict{Claim: c.String(), Outcome: Unreadable, Got: why}
}

// Claim is one property a plan states about the machine. Each kind carries
// its own comparator, and the tolerance lives in the comparator rather than
// in a shared ignore list: this is where "a route DHCP laid arrives later" and
// "a metric differs" stop being differences.
type Claim interface {
	// Check answers the claim against what the runtime reported.
	Check(Shape) Verdict
	// String names the claim, the way a verdict cites it.
	String() string
}

// Dialect is what a driver mode knows about itself and the derivation needs:
// a field, not a convention, and no provider name anywhere near it.
//
// Pure on purpose. The first sketch carried a Gateway func here, so that a
// claim could resolve a network's gateway while it was derived; that would
// have made Expect query the runtime, and #667 is the control that exists
// because it asks the runtime for nothing. The gateways travel in the Shape
// instead (Shape.Gateways, read by the observer), and a claim that needs one
// resolves it at Check time.
type Dialect struct {
	// RoutedNextHop is the link-local next hop a routed NIC reaches the host
	// through: 169.254.0.1 on Incus.
	RoutedNextHop netip.Addr
	// Aggregates are the private blocks this mode routes towards the network's
	// own router on every managed interface — the three RFC 1918 aggregates
	// under OVN (installGuestPrivateRoutes), nothing under a bridge, which has
	// no router of its own. A list rather than a flag, so the claim compares
	// what the driver lays and not a copy of it.
	Aggregates []netip.Prefix
	// LaysDefaultRoute says whether RouteEgress and DropEgress do anything in
	// this mode. Under a bridge both answer nil without touching the guest —
	// the host routes for it — so a default-route claim there would judge a
	// route nobody was asked to lay.
	LaysDefaultRoute bool
}

// Dialected is the optional half of a driver that declares its Dialect, on
// the model of Capable and EgressRouter: a driver that does not is read as a
// zero Dialect, which claims nothing mode-dependent and refuses nothing less.
type Dialected interface {
	Dialect() Dialect
}

// dialect asks the runtime for its Dialect, or answers the zero one.
func (r Reconciler) dialect() Dialect {
	d, ok := r.binding().driver.(Dialected)
	if !ok {
		return Dialect{}
	}
	return d.Dialect()
}

// propertyDefaultRoute names the property two steps of the recipe claimed on
// 2026-09-04.
const propertyDefaultRoute = "the default route"

// Expect derives the claims a plan makes, and refuses a plan whose steps
// contradict each other — before the runtime is asked for anything.
//
// Who may claim the default route, and why a public address claims it with no
// value beside it. A machine holding a public address has a way out already:
// on the routed shape RouteAddress lays it, and on the shape where the address
// rides a managed NIC the network's own router carries it. Which door a reply
// leaves by on each shape is a MEASUREMENT, not a deduction, and one of the two
// shapes has never been measured (#672). So the public address is registered
// as the route's owner — which is what refuses a second owner — and no claim
// states its value. Silence, not a guess: a verifier that invented its expected
// values would be a second reconciler, wrong with the same confidence as the
// first.
//
// TestAPlanWithTwoClaimantsToTheDefaultRouteIsRefusedBeforeTheRuntimeIsAsked
// fails without the refusal, and TestAPublicAddressAloneClaimsNoValueForTheDefaultRoute
// holds the silence.
func (r Reconciler) Expect(plan Plan, d Dialect) ([]Claim, error) {
	var claims []Claim
	owners := ledger{}

	// One interface per network. Boot and Memberships are two doors onto one
	// shape, and the same network reached through both is one interface asked
	// for twice: the first would ride the launch as the primary, the second
	// would be attached after it, and nothing says which address it carries.
	var networks []string
	for _, att := range plan.attachments() {
		if att.Network == "" {
			// No backing network: the membership is true in the store and the
			// interface arrives with a later boot, exactly as attach reads it.
			continue
		}
		owners.claim("the interface on "+att.Network, att.describe())
		if slices.Contains(networks, att.Network) {
			continue
		}
		networks = append(networks, att.Network)
		if att.Address == "" {
			// DHCP owns this interface (#202): the claim is an address of the
			// block, never a particular one.
			claims = append(claims, leases{Network: att.Network})
			continue
		}
		for _, address := range append([]string{att.Address}, att.Secondary...) {
			addr, err := netip.ParseAddr(address)
			if err != nil {
				// A stored address is untrusted input; one that is not an
				// address refuses the plan here rather than reaching a device
				// key.
				return nil, fmt.Errorf("the attachment on %s names %q, which is not an address: %w",
					att.Network, address, err)
			}
			claims = append(claims, carries{Network: att.Network, Address: addr, Bits: att.PrefixLen})
		}
	}

	// The promised public addresses. Each is carried as a /32 wherever it
	// lands — the routed NIC's own, or the filtered NIC it moved onto (#548) —
	// so the claim names no interface. An address outside the pack's block is
	// one the layer never routes (route() refuses it), so it claims nothing.
	var publics []string
	for _, address := range plan.Publics {
		addr, err := netip.ParseAddr(address)
		if err != nil || !r.emulated(address) {
			continue
		}
		if slices.Contains(publics, addr.String()) {
			continue
		}
		publics = append(publics, addr.String())
		claims = append(claims, carries{Address: addr, Bits: 32})
	}
	if len(publics) > 0 {
		owners.claim(propertyDefaultRoute, "the public address "+strings.Join(publics, ", "))
	}

	// The way out the pack states, in the three-state model #660 gave the
	// plan: a network to leave through, a refusal, or silence. Only the first
	// two claim; and they claim a value only where the driver lays one.
	if plan.Egress != "" {
		owners.claim(propertyDefaultRoute, "egress through "+plan.Egress)
		if d.LaysDefaultRoute {
			claims = append(claims, defaultRoute{Network: plan.Egress})
		}
	}
	if plan.NoEgress {
		owners.claim(propertyDefaultRoute, "the refusal of any way out (NoEgress)")
		if d.LaysDefaultRoute {
			claims = append(claims, noDefaultRoute{})
		}
	}

	// The private aggregates, on every mode that lays them, once per machine
	// rather than once per interface: two managed NICs each lay the same three
	// blocks and the kernel keeps the first, so the claim is that each block
	// rides A gateway of the plan, not every one.
	if len(d.Aggregates) > 0 && len(networks) > 0 {
		claims = append(claims, reachesAggregates{Blocks: d.Aggregates, Networks: networks})
	}

	if err := owners.contradiction(); err != nil {
		return nil, err
	}
	return claims, nil
}

// attachments is every interface the plan declares, the launch's first.
func (p Plan) attachments() []Attachment {
	out := make([]Attachment, 0, len(p.Boot)+len(p.Memberships))
	out = append(out, p.Boot...)
	return append(out, p.Memberships...)
}

// describe names an attachment as a claimant: the network and the address, so
// a contradiction reads as what it is.
func (a Attachment) describe() string {
	if a.Address == "" {
		return "an attachment on " + a.Network + " leased by DHCP"
	}
	return "an attachment on " + a.Network + " at " + a.Address
}

// ledger records who claims which property. It exists so that the refusal
// names every claimant rather than the last one found.
type ledger map[string][]string

func (l ledger) claim(property, who string) {
	if slices.Contains(l[property], who) {
		// The same claimant twice is a duplicate, not a contradiction: a
		// plan listing one attachment two times asks for one interface.
		return
	}
	l[property] = append(l[property], who)
}

// contradiction names every property with more than one claimant, or nothing.
func (l ledger) contradiction() error {
	properties := make([]string, 0, len(l))
	for property := range l {
		properties = append(properties, property)
	}
	slices.Sort(properties)
	var parts []string
	for _, property := range properties {
		claimants := l[property]
		if len(claimants) < 2 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s is claimed %d times, by %s",
			property, len(claimants), strings.Join(claimants, " and by ")))
	}
	if len(parts) == 0 {
		return nil
	}
	return fmt.Errorf("the plan contradicts itself: %s", strings.Join(parts, "; "))
}

// carries claims that an interface carries an address. Network empty means
// any interface; Bits zero means the plan did not say the mask, which leaves
// it unjudged rather than guessed — and a mask the plan did say is compared
// exactly, because /24 and /32 are not the same address to the kernel: a /32
// has no connected route to its own subnet (restorePinnedAddress).
type carries struct {
	Network string
	Address netip.Addr
	Bits    int
}

func (c carries) String() string {
	where := c.Network
	if where == "" {
		where = "any interface"
	}
	if c.Bits == 0 {
		return fmt.Sprintf("carries(%s, %s)", where, c.Address)
	}
	return fmt.Sprintf("carries(%s, %s/%d)", where, c.Address, c.Bits)
}

func (c carries) want() string {
	if c.Bits == 0 {
		return c.Address.String()
	}
	return fmt.Sprintf("%s/%d", c.Address, c.Bits)
}

func (c carries) Check(s Shape) Verdict {
	var candidates []string
	for _, name := range s.interfaceNames() {
		iface := s.Interfaces[name]
		if c.Network != "" && iface.Network != c.Network {
			continue
		}
		candidates = append(candidates, name)
		for _, p := range iface.Addresses {
			if p.Addr() == c.Address && (c.Bits == 0 || p.Bits() == c.Bits) {
				return held(c)
			}
		}
	}
	if len(candidates) == 0 {
		return broken(c, c.want(), "no interface on "+c.Network)
	}
	return broken(c, c.want(), s.carriedBy(candidates))
}

// leases claims that the interface on a network carries an address of that
// network's block — any address: DHCP chose it, and the plan never said which.
type leases struct {
	Network string
}

func (c leases) String() string { return "leases(" + c.Network + ")" }

func (c leases) Check(s Shape) Verdict {
	gateway, read := s.Gateways[c.Network]
	if !read {
		return unreadable(c, "the block of "+c.Network+" was not read")
	}
	block := gateway.Masked()
	dev := s.interfaceOn(c.Network)
	if dev == "" {
		return broken(c, "an address of "+block.String(), "no interface on "+c.Network)
	}
	for _, p := range s.Interfaces[dev].Addresses {
		if block.Contains(p.Addr()) {
			return held(c)
		}
	}
	return broken(c, "an address of "+block.String()+" on "+dev, s.carriedBy([]string{dev}))
}

// defaultRoute claims that the machine's default route leaves through the
// gateway of a network, on the interface that sits on it — the value
// RouteEgress lays (#647).
type defaultRoute struct {
	Network string
}

func (c defaultRoute) String() string { return "default route" }

func (c defaultRoute) Check(s Shape) Verdict {
	gateway, read := s.Gateways[c.Network]
	if !read {
		return unreadable(c, "the gateway of "+c.Network+" was not read")
	}
	dev := s.interfaceOn(c.Network)
	if dev == "" {
		return broken(c, "via "+gateway.Addr().String()+" on the interface of "+c.Network,
			"no interface on "+c.Network)
	}
	want := Route{Dst: defaultDst, Via: gateway.Addr(), Dev: dev}
	got, has := s.defaultRoute()
	if !has {
		return broken(c, want.String(), "no default route")
	}
	if got.Via == want.Via && got.Dev == want.Dev {
		return held(c)
	}
	return broken(c, want.String(), got.String())
}

// noDefaultRoute claims that the machine has no way out at all — the state
// DropEgress leaves, for a machine the cloud lets nowhere (#660).
type noDefaultRoute struct{}

func (noDefaultRoute) String() string { return "no default route" }

func (c noDefaultRoute) Check(s Shape) Verdict {
	if got, has := s.defaultRoute(); has {
		return broken(c, "no default route", got.String())
	}
	return held(c)
}

// reachesAggregates claims that every private aggregate rides a gateway of one
// of the plan's networks, on that network's interface: the routes towards the
// peered subnets (#549), announced by the network and laid by the driver.
type reachesAggregates struct {
	Blocks   []netip.Prefix
	Networks []string
}

func (c reachesAggregates) String() string { return "private aggregates" }

func (c reachesAggregates) Check(s Shape) Verdict {
	// Which next hops are legitimate: each gateway on its own interface.
	doors := map[netip.Addr]string{}
	for _, network := range c.Networks {
		gateway, read := s.Gateways[network]
		if !read {
			return unreadable(c, "the gateway of "+network+" was not read")
		}
		if dev := s.interfaceOn(network); dev != "" {
			doors[gateway.Addr()] = dev
		}
	}
	want := "each of " + prefixes(c.Blocks) + " via a gateway of " + strings.Join(c.Networks, ", ")
	if len(doors) == 0 {
		return broken(c, want, "no interface on any of "+strings.Join(c.Networks, ", "))
	}
	var missing []string
	for _, block := range c.Blocks {
		if !slices.ContainsFunc(s.Routes, func(r Route) bool {
			dev, legitimate := doors[r.Via]
			return r.Dst == block && legitimate && r.Dev == dev
		}) {
			missing = append(missing, block.String()+": "+s.routesTo(block))
		}
	}
	if len(missing) > 0 {
		return broken(c, want, strings.Join(missing, "; "))
	}
	return held(c)
}

// prefixes renders a list of blocks for a verdict.
func prefixes(blocks []netip.Prefix) string {
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, b.String())
	}
	return strings.Join(out, ", ")
}

// door claims which door a reply sent from an address leaves by, towards a
// destination: the answer to `ip route get <To> from <From>`, compared on
// `via` and `dev` alone. The comparator exists for the reading half (#668);
// nothing derives a door claim yet, because the value is a measurement per
// machine shape and one of the two shapes has none (#672).
type door struct {
	From, To netip.Addr
	Via      netip.Addr
	Dev      string
}

func (c door) String() string { return "door(" + c.From.String() + ")" }

// door is what the reading half asks the runtime for on this claim's behalf.
func (c door) door() (from, to netip.Addr) { return c.From, c.To }

func (c door) Check(s Shape) Verdict {
	got, read := s.Doors[c.From]
	if !read {
		return unreadable(c, "the door of "+c.From.String()+" was not read")
	}
	want := Route{Via: c.Via, Dev: c.Dev}
	if got.Dev == "" {
		return broken(c, want.String(), "no route")
	}
	if got.Via == c.Via && got.Dev == c.Dev {
		return held(c)
	}
	return broken(c, want.String(), got.String())
}

// wears claims the rule sets the interface on a network carries: exactly Sets,
// and none at all when Sets is empty — an interface on a network the pack
// declared outside its security groups' reach (Attachment.Unfiltered, #574).
// The contents of the sets are EnsureFirewall's and the network suites';
// this compares the binding alone.
type wears struct {
	Network string
	Sets    []string
}

func (c wears) String() string { return "wears(" + c.Network + ")" }

func (c wears) want(dev string) string {
	if len(c.Sets) == 0 {
		return "no rule set on " + dev
	}
	return strings.Join(c.Sets, ", ") + " on " + dev
}

func (c wears) Check(s Shape) Verdict {
	dev := s.interfaceOn(c.Network)
	if dev == "" {
		return broken(c, c.want("the interface of "+c.Network), "no interface on "+c.Network)
	}
	got := s.Interfaces[dev].RuleSets
	if len(got) == 0 && len(c.Sets) == 0 {
		return held(c)
	}
	if slices.Equal(got, c.Sets) {
		return held(c)
	}
	rendered := "none"
	if len(got) > 0 {
		rendered = strings.Join(got, ", ")
	}
	return broken(c, c.want(dev), rendered+" on "+dev)
}
