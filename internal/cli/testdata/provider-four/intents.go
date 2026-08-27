package providerfour

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"

	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/store"
)

// Everything Provider Four lets a user ask for, in the order a user asks it.
//
// There are no routes here and no wire dialect: this pack is driven by Go
// calls, because what #517 measures is whether the runtime contract suffices
// for a newcomer, and a JSON shape would measure this file's own invention
// instead. Each operation below is one intent — declare a segment, hand a
// barrier over, boot a node, publish an address — and the comment on it says
// what the pack had to supply and what the shared layer did on its own.

// ErrNoSuchResource is what this pack answers for an identifier it does not
// hold. A real pack would translate it into its provider's error dialect,
// which is the one part of a pack that must never be shared.
var ErrNoSuchResource = errors.New("four: no such resource")

// ---- Segments: a private network, and keeping it apart --------------------

// CreateSegment declares a private network and gives it a real one on the
// host.
//
// What the pack supplies: the block, whether the runtime should reserve a
// gateway in it, whether machines on it reach outward, and the label a sweep
// finds an orphan by. What it never supplies is the network's host-side name —
// EnsureBackingNetwork derives it, hands it to the runtime, and records it on
// the resource only once the runtime accepted it. A pack writing that key
// first keeps, on failure, a Runtime entry naming a network that does not
// exist, and the delete path then tries to remove it.
//
// The isolation pass runs on every create, not on the new segment alone: a
// reconciliation only has to be right about what is, where a patch has to be
// right about what changed.
func (p *Pack) CreateSegment(ctx context.Context, name, cidr, realm string) (*resource.Resource, error) {
	block, err := netip.ParsePrefix(cidr)
	if err != nil {
		return nil, fmt.Errorf("four: %q is not a block: %w", cidr, err)
	}
	res := resource.New(p.env.NewID(), KindSegment, tenant(), StateUp, p.env.Now())
	res.Attrs["name"] = name
	res.Attrs["block"] = block.Masked().String()
	res.Attrs["realm"] = realm

	if err := p.binding().EnsureBackingNetwork(ctx, res, machine.BackingNetwork{
		Key:     runtimeSegmentKey,
		CIDR:    block,
		Gateway: true,
		NAT:     false,
		Marker:  segmentMarker,
	}); err != nil {
		return nil, fmt.Errorf("four: the segment has no network: %w", err)
	}
	p.env.Store.Put(res)
	p.ReconcileSegments(ctx)
	return res, nil
}

// DeleteSegment takes the network away and forgets its name.
//
// The error comes back rather than being swallowed: reporting a segment gone
// while the runtime still holds its block is the kind of half-truth this
// emulator exists to avoid. Whether to refuse the delete or to log and proceed
// is client-visible surface, so the shared layer leaves the choice here; this
// pack refuses.
func (p *Pack) DeleteSegment(ctx context.Context, id string) error {
	res, found := p.env.Store.Get(Name, KindSegment, id)
	if !found {
		return ErrNoSuchResource
	}
	if err := p.binding().RemoveBackingNetwork(ctx, res, runtimeSegmentKey); err != nil {
		return fmt.Errorf("four: the segment's network is still there: %w", err)
	}
	p.env.Store.Delete(Name, KindSegment, id)
	p.ReconcileSegments(ctx)
	return nil
}

// ReconcileSegments applies what every segment may reach, over the whole set.
//
// Provider Four's model, and the only part of this that is its own: two
// segments reach each other when they declare the same realm. The pass itself
// — which runtimes need peering and which need reject rules, what a vanished
// network means, what to log — belongs to the shared layer, and there is one
// writer for it.
func (p *Pack) ReconcileSegments(ctx context.Context) (native, applied bool) {
	segments := p.env.Store.List(KindSegment, tenant())
	sort.Slice(segments, func(i, j int) bool { return segments[i].ID < segments[j].ID })

	members := make([]machine.IsolationMember, 0, len(segments))
	for _, segment := range segments {
		members = append(members, machine.IsolationMember{
			ID:      segment.ID,
			Network: segment.Runtime[runtimeSegmentKey],
			Block:   attrString(segment, "block"),
		})
	}
	return p.binding().ReconcileIsolation(ctx, KindSegment, members, func(from, to int) bool {
		return attrString(segments[from], "realm") == attrString(segments[to], "realm")
	})
}

// ---- Barriers: a rule set, and what it means ------------------------------

// Rule is one rule of a barrier, in Provider Four's own words.
//
// SourceBarrier is the half that makes a rule set more than a list of blocks:
// a rule may name another barrier instead of a block, and then it means every
// node wearing that barrier. It is also the half a fourth pack forgets — see
// SyncReferrers below, and #475, which is exactly this sentence never
// containing the machines it talks about.
type Rule struct {
	Direction     string
	Action        string
	Protocol      string
	Source        string
	SourceBarrier string
	PortFrom      int
	PortTo        int
}

// CreateBarrier declares an empty rule set. Nothing reaches the runtime yet:
// an empty barrier nobody wears has nothing to enforce.
func (p *Pack) CreateBarrier(name string) *resource.Resource {
	res := resource.New(p.env.NewID(), KindBarrier, tenant(), StateUp, p.env.Now())
	res.Attrs["name"] = name
	res.Attrs["rules"] = []Rule{}
	p.env.Store.Put(res)
	return res
}

// AddRule adds one rule and hands the whole set to the runtime.
//
// The hand-off is the line #475 was born from: without it the API describes a
// rule nothing enforces, every gate stays green, and the defect lives for
// months. Nothing in the contract can force this call — the pack is the only
// party that knows its rules changed — so it is held by a named test instead:
// internal/cli's TestTheFourthPacksRuleChangeReachesTheRuntime, which is red
// when this line is removed.
//
// The rules are replaced rather than appended in place: resource.Clone shares
// nested values with the store, so a pack that appended to the stored slice
// would be writing through the store's own copy.
func (p *Pack) AddRule(ctx context.Context, barrierID string, rule Rule) error {
	res, found := p.env.Store.Get(Name, KindBarrier, barrierID)
	if !found {
		return ErrNoSuchResource
	}
	base := res.Clone()
	previous := rulesOf(res)
	next := make([]Rule, 0, len(previous)+1)
	next = append(next, previous...)
	next = append(next, rule)
	res.Attrs["rules"] = next
	if !p.env.Store.Commit(base, res, p.env.Now()) {
		return ErrNoSuchResource
	}
	p.groups().SyncGroup(ctx, res, nil)
	return nil
}

// DeleteBarrier removes the rule set from the runtime and forgets it.
func (p *Pack) DeleteBarrier(ctx context.Context, id string) error {
	res, found := p.env.Store.Get(Name, KindBarrier, id)
	if !found {
		return ErrNoSuchResource
	}
	p.groups().Drop(ctx, machine.FirewallName(rulesetPrefix, res.ID))
	p.env.Store.Delete(Name, KindBarrier, id)
	return nil
}

// barrierSpec translates one barrier into the rule set the runtime takes. This
// is the pack's whole contribution to the firewall: its own vocabulary, its
// own defaults, and its own idea of what a rule naming another barrier means.
//
// fresh wins over the store's copy of the same resource, and that threading is
// not this pack's cleverness — the skeleton hands it down. The boot that
// triggers a re-expansion runs before its own commit, so the store does not
// yet carry the address the expansion is being run for, and reading the store
// alone here would silently miss the very node that booted.
func (p *Pack) barrierSpec(group, fresh *resource.Resource) machine.FirewallSpec {
	spec := machine.FirewallSpec{
		Name:           machine.FirewallName(rulesetPrefix, group.ID),
		DefaultIngress: "drop",
		DefaultEgress:  "allow",
	}
	for _, rule := range rulesOf(group) {
		shape := machine.FirewallRule{
			Direction: rule.Direction,
			Action:    rule.Action,
			Protocol:  rule.Protocol,
			PortFrom:  rule.PortFrom,
			PortTo:    rule.PortTo,
		}
		if rule.SourceBarrier == "" {
			shape.Source = rule.Source
			shape.Destination = ""
			spec.Rules = append(spec.Rules, shape)
			continue
		}
		for _, address := range p.addressesWearing(rule.SourceBarrier, fresh) {
			member := shape
			member.Source = address + "/32"
			spec.Rules = append(spec.Rules, member)
		}
	}
	return spec
}

// barrierWearers lists the nodes wearing a barrier, so a changed set is
// replayed onto every one of them.
func (p *Pack) barrierWearers(group *resource.Resource) []*resource.Resource {
	var wearers []*resource.Resource
	for _, node := range p.nodes() {
		for _, id := range wornBarriers(node) {
			if id == group.ID {
				wearers = append(wearers, node)
				break
			}
		}
	}
	return wearers
}

// wornBarriers lists the barriers one node wears.
func (p *Pack) wornBarriers(res *resource.Resource) []string { return wornBarriers(res) }

// barrier resolves a worn identifier to its stored barrier.
func (p *Pack) barrier(id string) (*resource.Resource, bool) {
	return p.env.Store.Get(Name, KindBarrier, id)
}

// barrierReferrers lists the barriers one of whose rules names one of the
// given barriers as a member source — the half of AfterBoot that makes a
// tiering statement contain the machines it talks about.
func (p *Pack) barrierReferrers(named map[string]bool) []*resource.Resource {
	var referrers []*resource.Resource
	for _, group := range p.env.Store.List(KindBarrier, tenant()) {
		for _, rule := range rulesOf(group) {
			if rule.SourceBarrier != "" && named[rule.SourceBarrier] {
				referrers = append(referrers, group)
				break
			}
		}
	}
	sort.Slice(referrers, func(i, j int) bool { return referrers[i].ID < referrers[j].ID })
	return referrers
}

// addressesWearing lists the private addresses of every node wearing a
// barrier, with fresh preferred over its stale store copy.
func (p *Pack) addressesWearing(barrierID string, fresh *resource.Resource) []string {
	var addresses []string
	for _, node := range p.nodes() {
		if fresh != nil && node.ID == fresh.ID {
			node = fresh
		}
		worn := false
		for _, id := range wornBarriers(node) {
			if id == barrierID {
				worn = true
				break
			}
		}
		if !worn {
			continue
		}
		if address := p.binding().AddressOf(node); address != "" {
			addresses = append(addresses, address)
		}
	}
	sort.Strings(addresses)
	return addresses
}

// ---- Nodes: the machine, and its boot -------------------------------------

// NodeRequest is what a user asks for when it asks for a machine.
type NodeRequest struct {
	Name string
	// Image is an identifier of this pack's own catalogue. One that resolves
	// to nothing is refused at boot rather than substituted, which is the
	// shared layer's rule: a stack that asked for one distribution and got
	// another boots and then fails at its first package install.
	Image string
	// HomeSegment is the segment the node is born on, empty for a node born
	// bare. It rides the launch rather than being attached afterwards.
	HomeSegment string
	// Barriers are the rule sets it wears.
	Barriers []string
	// Public asks for an anchor allocated at create, so the address rides the
	// boot instead of being routed onto a live interface.
	Public bool
	// UserData replaces the generated boot configuration when the user
	// supplied its own.
	UserData string
}

// CreateNode stores the machine. Nothing runs yet: a node is born down, and
// the boot is a separate intent, which is what every one of these clouds does.
func (p *Pack) CreateNode(ctx context.Context, req NodeRequest) (*resource.Resource, error) {
	res := resource.New(p.env.NewID(), KindNode, tenant(), StateDown, p.env.Now())
	res.Attrs["name"] = req.Name
	res.Attrs["image"] = req.Image
	res.Attrs["barriers"] = append([]string(nil), req.Barriers...)
	res.Attrs["segments"] = []string{}
	res.Attrs["addresses"] = map[string]string{}
	res.Attrs["user_data"] = req.UserData

	if req.HomeSegment != "" {
		segment, found := p.env.Store.Get(Name, KindSegment, req.HomeSegment)
		if !found {
			return nil, ErrNoSuchResource
		}
		address, ok := p.allocate(segment, res.ID)
		if !ok {
			return nil, fmt.Errorf("four: the segment %s has no address left", segment.ID)
		}
		res.Attrs["home_segment"] = req.HomeSegment
		res.Attrs["addresses"] = map[string]string{req.HomeSegment: address}
	}
	p.env.Store.Put(res)

	if req.Public {
		anchor, err := p.CreateAnchor()
		if err != nil {
			return nil, err
		}
		if err := p.AttachAnchor(ctx, anchor.ID, res.ID); err != nil {
			return nil, err
		}
	}
	stored, found := p.env.Store.Get(Name, KindNode, res.ID)
	if !found {
		return nil, ErrNoSuchResource
	}
	return stored, nil
}

// StartNode boots the node on its declared plan.
//
// Three things the pack does not do here, and each is a defect it therefore
// cannot have. It does not publish a state: Binding.PowerOn writes the one the
// effect produced, so a start that failed answers "broken" and not "up". It
// does not name its own machine: the shared layer derives it from the prefix
// and records it out of reach of the API. And it does not order the boot: the
// promised addresses, then the memberships, then the firewall is a property of
// the runtime, and Reconciler.PowerOn is where it lives.
//
// Boot.Attachments and Boot.PublicAddresses are deliberately not set: the
// reconciler fills them from the plan, and a pack setting them would be
// declaring its interface shape in two places.
func (p *Pack) StartNode(ctx context.Context, id string) error {
	return p.binding().Transition(p.env.Store, p.env.Now, KindNode, id, func(res *resource.Resource) {
		image, requested := p.image(res)
		p.reconciler().PowerOn(ctx, res, machine.Boot{
			Image:     image.Ref,
			Requested: requested,
			User:      image.User,
			Hostname:  attrString(res, "name"),
			CloudInit: attrString(res, "user_data"),
			Labels:    map[string]string{"feint.node": res.ID},
		})
	})
}

// StopNode stops the machine and withdraws its address.
//
// The withdrawal is the shared layer's, and it is not tidiness: an address the
// API publishes and nothing answers on is the defect this project exists to
// avoid, and a stopped machine produces exactly that if nobody removes it.
//
// The state is the pack's here, where the start's was not, and the asymmetry
// is real rather than an oversight: a stop that the runtime refused still
// leaves the API describing a machine the user asked to be down. That gap is
// the residue #514 lists honestly rather than hides.
func (p *Pack) StopNode(ctx context.Context, id string) error {
	return p.binding().Transition(p.env.Store, p.env.Now, KindNode, id, func(res *resource.Resource) {
		p.binding().PowerOff(ctx, res)
		res.State = StateDown
	})
}

// DeleteNode destroys the machine and forgets the node.
//
// The last call is the one a fourth pack forgets: a deleted node changes what
// every barrier naming its barriers means, and nothing in the contract can
// know that. SyncReferrers is the door, and internal/cli's
// TestTheFourthPacksDeleteReExpandsTheBarriersThatNameIt is red without it.
func (p *Pack) DeleteNode(ctx context.Context, id string) error {
	res, found := p.env.Store.Get(Name, KindNode, id)
	if !found {
		return ErrNoSuchResource
	}
	worn := wornBarriers(res)
	name := res.Runtime[p.binding().RuntimeKey]
	for _, address := range p.anchorsOf(res.ID) {
		p.reconciler().Unroute(ctx, name, address)
	}
	p.binding().Destroy(ctx, res)
	p.env.Store.Delete(Name, KindNode, id)
	p.releaseAnchors(id)
	p.groups().SyncReferrers(ctx, worn, nil)
	return nil
}

// ReadNode answers what the store holds, having first asked the host for an
// address the boot did not deliver in time.
//
// A container answers immediately; a virtual machine answers tens of seconds
// later. So a start never waits and a read fills the gap, under the same
// conditional write-back every other path uses.
func (p *Pack) ReadNode(ctx context.Context, id string) (*resource.Resource, error) {
	res, err := p.binding().Observe(p.env.Store, p.env.Now, KindNode, id, func(res *resource.Resource) bool {
		changed := p.binding().RefreshIfRunning(ctx, res)
		if changed {
			// The late-address door: a virtual machine's agent answers long
			// after the boot returned, and the guest half of its routes can
			// only land then.
			p.reconciler().ReplayAddresses(ctx, res)
		}
		return changed
	})
	if errors.Is(err, store.ErrNotFound) {
		// The core reports one absence; a pack answers it in its own dialect,
		// which is the half of a pack that must never be shared.
		return nil, ErrNoSuchResource
	}
	return res, err
}

// AddressOfNode is the private address the machine answers on, empty when
// nothing is running.
func (p *Pack) AddressOfNode(res *resource.Resource) string {
	return p.binding().AddressOf(res)
}

// image resolves the identifier the user sent through this pack's own
// catalogue, then through the operator's declarations. Unresolved returns the
// zero value, which the shared layer refuses to boot rather than substituting.
func (p *Pack) image(res *resource.Resource) (machine.Image, string) {
	requested := attrString(res, "image")
	if image, found := images[requested]; found {
		return image, requested
	}
	return machine.Image{}, requested
}

// nodes lists this pack's machines, in a stable order.
func (p *Pack) nodes() []*resource.Resource {
	nodes := p.env.Store.List(KindNode, tenant())
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	return nodes
}

// ---- Memberships: joining a running machine to a segment ------------------

// JoinSegment puts a running node on one more segment.
//
// The hot half of a membership, which cannot fold into a boot-time plan: a
// cloud attaches an interface to a machine that is running. The pack allocates
// the address and writes the membership; the shared layer attaches the
// interface and resyncs the firewall, because the new interface carries the
// node's rule sets like every other one.
func (p *Pack) JoinSegment(ctx context.Context, nodeID, segmentID string) error {
	segment, found := p.env.Store.Get(Name, KindSegment, segmentID)
	if !found {
		return ErrNoSuchResource
	}
	res, found := p.env.Store.Get(Name, KindNode, nodeID)
	if !found {
		return ErrNoSuchResource
	}
	address, ok := p.allocate(segment, nodeID)
	if !ok {
		return fmt.Errorf("four: the segment %s has no address left", segmentID)
	}

	base := res.Clone()
	res.Attrs["segments"] = appendUnique(membershipsOf(res), segmentID)
	res.Attrs["addresses"] = withAddress(addressesOf(res), segmentID, address)
	if !p.env.Store.Commit(base, res, p.env.Now()) {
		return ErrNoSuchResource
	}

	att, ok := p.attachment(res, segmentID)
	if !ok {
		return nil
	}
	return p.reconciler().Join(ctx, res, att)
}

// LeaveSegment takes the node off a segment. The exact undo of Join's attach
// half; the membership and the address go from the store either way.
func (p *Pack) LeaveSegment(ctx context.Context, nodeID, segmentID string) error {
	segment, found := p.env.Store.Get(Name, KindSegment, segmentID)
	if !found {
		return ErrNoSuchResource
	}
	res, found := p.env.Store.Get(Name, KindNode, nodeID)
	if !found {
		return ErrNoSuchResource
	}
	base := res.Clone()
	res.Attrs["segments"] = without(membershipsOf(res), segmentID)
	res.Attrs["addresses"] = withoutAddress(addressesOf(res), segmentID)
	if !p.env.Store.Commit(base, res, p.env.Now()) {
		return ErrNoSuchResource
	}
	p.reconciler().Leave(ctx, res, segment.Runtime[runtimeSegmentKey])
	return nil
}

// ---- Anchors: a public address ---------------------------------------------

// CreateAnchor allocates one public address out of this pack's own block.
func (p *Pack) CreateAnchor() (*resource.Resource, error) {
	taken := map[string]bool{}
	for _, anchor := range p.env.Store.List(KindAnchor, tenant()) {
		taken[attrString(anchor, "address")] = true
	}
	address, ok := freeAddress(publicBlock, 1, taken)
	if !ok {
		return nil, errors.New("four: the public block is exhausted")
	}
	res := resource.New(p.env.NewID(), KindAnchor, tenant(), StateUp, p.env.Now())
	res.Attrs["address"] = address
	res.Attrs["node"] = ""
	p.env.Store.Put(res)
	return res, nil
}

// AttachAnchor makes the address reach the node.
//
// The pack says which address must reach which machine and nothing else. The
// block guard is the shared layer's — a stored address is untrusted input, and
// routing one from outside this pack's own block would send the host's traffic
// for it into a container — and so is the question of whether the runtime can
// route at all.
func (p *Pack) AttachAnchor(ctx context.Context, anchorID, nodeID string) error {
	anchor, found := p.env.Store.Get(Name, KindAnchor, anchorID)
	if !found {
		return ErrNoSuchResource
	}
	node, found := p.env.Store.Get(Name, KindNode, nodeID)
	if !found {
		return ErrNoSuchResource
	}
	base := anchor.Clone()
	anchor.Attrs["node"] = nodeID
	if !p.env.Store.Commit(base, anchor, p.env.Now()) {
		return ErrNoSuchResource
	}
	p.reconciler().Route(ctx, node, attrString(anchor, "address"))
	return nil
}

// DetachAnchor takes the route back.
//
// It takes the machine name rather than the resource, because the machine may
// already be gone and the route may still be there — on a runtime whose
// addresses outlive their machine, withdrawing it is the whole point.
func (p *Pack) DetachAnchor(ctx context.Context, anchorID string) error {
	anchor, found := p.env.Store.Get(Name, KindAnchor, anchorID)
	if !found {
		return ErrNoSuchResource
	}
	name := ""
	if node, found := p.env.Store.Get(Name, KindNode, attrString(anchor, "node")); found {
		name = node.Runtime[p.binding().RuntimeKey]
	}
	p.reconciler().Unroute(ctx, name, attrString(anchor, "address"))

	base := anchor.Clone()
	anchor.Attrs["node"] = ""
	if !p.env.Store.Commit(base, anchor, p.env.Now()) {
		return ErrNoSuchResource
	}
	return nil
}

// anchorsOf lists the public addresses promised to a node.
func (p *Pack) anchorsOf(nodeID string) []string {
	var addresses []string
	for _, anchor := range p.env.Store.List(KindAnchor, tenant()) {
		if attrString(anchor, "node") == nodeID {
			addresses = append(addresses, attrString(anchor, "address"))
		}
	}
	sort.Strings(addresses)
	return addresses
}

// releaseAnchors unlinks every anchor a deleted node held, so no address stays
// published against a machine that is gone.
func (p *Pack) releaseAnchors(nodeID string) {
	for _, anchor := range p.env.Store.List(KindAnchor, tenant()) {
		if attrString(anchor, "node") != nodeID {
			continue
		}
		base := anchor.Clone()
		anchor.Attrs["node"] = ""
		p.env.Store.Commit(base, anchor, p.env.Now())
	}
}

// ---- Spreaders: a balancer -------------------------------------------------

// CreateSpreader declares a balancer on a segment and hands it to the runtime.
func (p *Pack) CreateSpreader(ctx context.Context, name, segmentID string, port int) (*resource.Resource, error) {
	if _, found := p.env.Store.Get(Name, KindSegment, segmentID); !found {
		return nil, ErrNoSuchResource
	}
	res := resource.New(p.env.NewID(), KindSpreader, tenant(), StateUp, p.env.Now())
	res.Attrs["name"] = name
	res.Attrs["segment"] = segmentID
	res.Attrs["port"] = port
	res.Attrs["backends"] = []string{}
	p.env.Store.Put(res)
	p.deliver(ctx, res)
	return res, nil
}

// RegisterBackend puts one node behind the spreader and hands the whole
// balancer over again.
//
// Whole, and not patched member by member: replacing is what makes a backend
// removed upstream actually stop receiving connections.
func (p *Pack) RegisterBackend(ctx context.Context, spreaderID, nodeID string) error {
	res, found := p.env.Store.Get(Name, KindSpreader, spreaderID)
	if !found {
		return ErrNoSuchResource
	}
	base := res.Clone()
	res.Attrs["backends"] = appendUnique(backendsOf(res), nodeID)
	if !p.env.Store.Commit(base, res, p.env.Now()) {
		return ErrNoSuchResource
	}
	p.deliver(ctx, res)
	return nil
}

// DeleteSpreader withdraws the balancer and forgets it.
func (p *Pack) DeleteSpreader(ctx context.Context, id string) error {
	res, found := p.env.Store.Get(Name, KindSpreader, id)
	if !found {
		return ErrNoSuchResource
	}
	segment, segmentFound := p.env.Store.Get(Name, KindSegment, attrString(res, "segment"))
	if segmentFound {
		listen, ok := p.listenAddress(segment)
		if ok {
			if err := p.binding().RemoveBalancer(ctx, segment.Runtime[runtimeSegmentKey], listen); err != nil {
				p.env.Log.Error("could not withdraw the spreader", "spreader", id, "error", err)
			}
		}
	}
	p.env.Store.Delete(Name, KindSpreader, id)
	return nil
}

// deliver hands the whole balancer over and records what the host actually
// took.
//
// Three outcomes, never two, and the middle one is the reason: a runtime that
// does not balance answers a stated limit rather than an empty delivery, so
// this pack leaves its balancer a record instead of reporting nothing
// distributed with no reason beside it. What came back is written onto the
// resource, because the state published must be the one the effect produced —
// a host distributing to nobody while the API describes two healthy backends
// is #483, and it was found by a person reading the host.
func (p *Pack) deliver(ctx context.Context, res *resource.Resource) {
	binding := p.binding()
	if !binding.Balances() {
		return
	}
	segment, found := p.env.Store.Get(Name, KindSegment, attrString(res, "segment"))
	if !found {
		return
	}
	listen, ok := p.listenAddress(segment)
	if !ok {
		return
	}
	spec := machine.BalancerSpec{
		Name:      attrString(res, "name"),
		Network:   segment.Runtime[runtimeSegmentKey],
		Listen:    listen,
		Listeners: []machine.BalancerListener{{Protocol: "tcp", Listen: portOf(res), Backend: portOf(res)}},
		Targets:   p.backendAddresses(res, segment.ID),
	}
	delivery, err := binding.EnsureBalancer(ctx, spec)
	switch {
	case errors.Is(err, machine.ErrBalancerNotDistributed):
		p.env.Log.Warn("this runtime does not distribute a spreader of this shape",
			"spreader", res.ID, "error", err)
		return
	case err != nil:
		p.env.Log.Error("could not hand the spreader to the runtime", "spreader", res.ID, "error", err)
		return
	}
	distributed, undistributed := delivery.Lines()
	machine.RecordBalancerDelivery(p.env.Store, res, p.env.Now(), distributed, undistributed)
}

// backendAddresses lists the addresses of the nodes behind a spreader, on the
// segment it lives on.
func (p *Pack) backendAddresses(res *resource.Resource, segmentID string) []string {
	var targets []string
	for _, nodeID := range backendsOf(res) {
		node, found := p.env.Store.Get(Name, KindNode, nodeID)
		if !found {
			continue
		}
		if address := addressesOf(node)[segmentID]; address != "" {
			targets = append(targets, address)
		}
	}
	sort.Strings(targets)
	return targets
}

// listenAddress is the address a spreader answers on: the second host address
// of its segment's block, the runtime having reserved the first.
func (p *Pack) listenAddress(segment *resource.Resource) (string, bool) {
	block, err := netip.ParsePrefix(attrString(segment, "block"))
	if err != nil {
		return "", false
	}
	return hostAddress(block, 2)
}

// allocate hands the node its address on a segment: the lowest host address
// from the tenth on that no node of this segment already holds.
//
// Deterministic and stored, rather than derived at read time: an address that
// moved under a live machine because a neighbour was deleted would be a defect
// nobody could see from the API. Address planning is the pack's own — the
// runtime contract has no opinion on it.
func (p *Pack) allocate(segment *resource.Resource, nodeID string) (string, bool) {
	block, err := netip.ParsePrefix(attrString(segment, "block"))
	if err != nil {
		return "", false
	}
	taken := map[string]bool{}
	for _, node := range p.nodes() {
		if node.ID == nodeID {
			continue
		}
		if address := addressesOf(node)[segment.ID]; address != "" {
			taken[address] = true
		}
	}
	return freeAddress(block, 10, taken)
}

// ---- Reading what the store holds -------------------------------------------

func attrString(res *resource.Resource, key string) string {
	if res == nil {
		return ""
	}
	s, _ := res.Attrs[key].(string)
	return s
}

func rulesOf(res *resource.Resource) []Rule {
	rules, _ := res.Attrs["rules"].([]Rule)
	return rules
}

func wornBarriers(res *resource.Resource) []string {
	ids, _ := res.Attrs["barriers"].([]string)
	return ids
}

func membershipsOf(res *resource.Resource) []string {
	ids, _ := res.Attrs["segments"].([]string)
	return ids
}

// portOf reads back the port a spreader answers on, tolerating the float64 a
// snapshot restore produces.
//
// The plain `res.Attrs["port"].(int)` was what this pack wrote first, and it is
// wrong in a way nothing here would have shown: Attrs is decoded as
// map[string]any, so every stored number comes back a float64 and the int
// assertion yields zero — a balancer silently listening on port 0 after a
// `feint snapshot load`. Measured on 2026-08-27, and it is a rediscovery
// rather than a discovery: exoscale/privatenetworks.go carries the same helper
// with the same sentence, and two of the three real packs do not. That is the
// #475 shape one storey down — a lesson living in one pack of three, which a
// fourth pack has no way to inherit.
func portOf(res *resource.Resource) int {
	switch n := res.Attrs["port"].(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

func backendsOf(res *resource.Resource) []string {
	ids, _ := res.Attrs["backends"].([]string)
	return ids
}

func addressesOf(res *resource.Resource) map[string]string {
	addresses, _ := res.Attrs["addresses"].(map[string]string)
	if addresses == nil {
		return map[string]string{}
	}
	return addresses
}

// The four helpers below all copy before writing, for one reason:
// resource.Clone shares nested values with the store, so a pack that mutated a
// slice or a map read out of Attrs would be writing through the store's own
// copy, outside every lock and every conditional write-back.

func appendUnique(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return append([]string(nil), list...)
		}
	}
	return append(append([]string(nil), list...), value)
}

func without(list []string, value string) []string {
	out := make([]string, 0, len(list))
	for _, existing := range list {
		if existing != value {
			out = append(out, existing)
		}
	}
	return out
}

func withAddress(addresses map[string]string, segmentID, address string) map[string]string {
	out := make(map[string]string, len(addresses)+1)
	for key, value := range addresses {
		out[key] = value
	}
	out[segmentID] = address
	return out
}

func withoutAddress(addresses map[string]string, segmentID string) map[string]string {
	out := make(map[string]string, len(addresses))
	for key, value := range addresses {
		if key != segmentID {
			out[key] = value
		}
	}
	return out
}

// hostAddress is the nth host address of a block, false when the block is too
// small to hold it.
func hostAddress(block netip.Prefix, offset int) (string, bool) {
	addr := block.Masked().Addr()
	for i := 0; i < offset; i++ {
		addr = addr.Next()
		if !addr.IsValid() || !block.Contains(addr) {
			return "", false
		}
	}
	return addr.String(), true
}

// freeAddress is the lowest host address from the given offset that nobody
// holds.
func freeAddress(block netip.Prefix, offset int, taken map[string]bool) (string, bool) {
	for i := offset; ; i++ {
		address, ok := hostAddress(block, i)
		if !ok {
			return "", false
		}
		if !taken[address] {
			return address, true
		}
	}
}
