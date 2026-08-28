package machine

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/serialise"
)

// OVN is the mode where emulated subnets are separate by construction.
//
// Two managed bridges on one host are routed to each other, and every attempt
// to separate them with reject rules eventually leaked (docs/limits.md keeps
// the measurements). An OVN network is a logical network with its own router:
// another OVN network is simply not on it, so isolation is what you get by
// not asking for a connection, instead of what you build out of rules. Two
// networks that must reach each other are peered, which is the runtime's own
// notion (`network peer`), and exactly the shape of a VPC that routes between
// its subnets.
//
// The mode costs a prerequisite: ovn-central, ovn-host and an Open vSwitch
// wired to the local northbound socket. The bridge mode therefore stays the
// default; nothing here runs unless the driver was built with OVN set.
//
// Everything below was measured on Incus 7.2 with OVN 24.03 before being
// relied on; where a behaviour comes from the Incus source at v7.2.0, the
// comment says which file.

const (
	// DefaultUplinkName is the bridge OVN networks reach the outside through.
	// One uplink for every emulated network: the uplink is plumbing, not an
	// emulated resource, and it carries the label so a sweep removes it.
	DefaultUplinkName = "feint-uplink"
	// DefaultUplinkCIDR is the block the uplink carries. Deliberately obscure
	// for the same reason the conformance suite picks 10.181.0.0/24: a
	// collision with a block already routed on the operator's host makes the
	// create fail, and failing is better than capturing someone's traffic.
	DefaultUplinkCIDR = "10.209.83.0/24"
)

// queueUplinkRoute registers one block as waiting for delegation. Called
// before uplinkMu is taken, so the current holder of the lock can drain the
// queue into its own write; delegateQueuedRoutes is the other half.
func (d *Incus) queueUplinkRoute(block string) {
	d.uplinkQueueMu.Lock()
	if d.uplinkQueue == nil {
		d.uplinkQueue = map[string]bool{}
	}
	d.uplinkQueue[block] = true
	d.uplinkQueueMu.Unlock()
}

// delegateQueuedRoutes adds the caller's block, and every block queued behind
// the lock, to the uplink's ipv4.routes in a single write. The caller holds
// uplinkMu — see the field's comment for the measured reason there is no safe
// unserialised edit of the uplink.
//
// An OVN network must sit inside its uplink's routes, and Incus says so in terms
// that name the network rather than the setting: "Uplink network doesn't contain
// 10.191.4.0/24 in its routes". Since a client picks its own address plan, and
// it is never the uplink's own /24, the block has to be delegated as the network
// is created.
//
// The whole queue at once, because every write to the uplink makes the daemon
// clear and rebuild its firewall — measured at ~1 s per write on Incus 7.2 —
// and a terraform apply issues its subnet creates concurrently: one write per
// create put fifteen rebuilds in a serial queue, which is a third of the
// straight line of #473. Draining the queue is an optimisation, never the
// guarantee: a caller's own block is always ensured under its own turn of the
// lock, so a failed batched write costs the others nothing but a retry at
// their turn, where addUplinkRoutes finds the block still missing.
// TestConcurrentOVNCreatesShareTheirUplinkWrites fails without the draining.
//
// Still one block per network, never a blanket range. Delegating 10.0.0.0/8 up
// front looks tidier and fails: Incus turns each route into a real route on the
// host, and a /8 collides with whatever already lives in that space — measured
// on a machine whose own bridge sat at 10.108.0.0/24, where the whole uplink
// then refused to come up.
//
// Idempotent by construction: a block already delegated is left alone, so
// creating the same network twice does not grow the list.
func (d *Incus) delegateQueuedRoutes(ctx context.Context, own string) error {
	d.uplinkQueueMu.Lock()
	blocks := make([]string, 0, len(d.uplinkQueue)+1)
	for block := range d.uplinkQueue {
		blocks = append(blocks, block)
	}
	d.uplinkQueue = nil
	d.uplinkQueueMu.Unlock()
	if !slices.Contains(blocks, own) {
		blocks = append(blocks, own)
	}
	// A block being delegated is a block whose pending withdrawal must be
	// forgotten (#519): a subnet recreated on a deleted subnet's block would
	// otherwise be delegated here and then stripped by the drain the delete
	// left queued — a live network losing its route to a write about a dead
	// one. The cancel is why drainUplinkWithdrawals trusts its queue instead
	// of always withdrawing the caller's own block.
	// TestADelegationCancelsAQueuedWithdrawalOfItsBlock fails without it.
	for _, block := range blocks {
		d.cancelUplinkWithdrawal(block)
	}
	// Sorted so two runs write the same value and a log diff means something.
	slices.Sort(blocks)
	if err := d.addUplinkRoutes(ctx, blocks); err != nil {
		return fmt.Errorf("delegate %s to uplink %s: %w", own, d.uplinkName(), err)
	}
	return nil
}

// queueUplinkWithdrawal registers one block as waiting to leave the uplink's
// routes, after the delete of its network succeeded (#519). It is the delete
// side's queueUplinkRoute: the holder of uplinkMu withdraws the whole queue in
// one write, and drainUplinkWithdrawals is the other half. Queued only after a
// successful delete — the ordering afterNetworkDelete already keeps — so a
// network still standing never has its block here.
func (d *Incus) queueUplinkWithdrawal(block string) {
	if block == "" {
		return
	}
	d.withdrawQueueMu.Lock()
	if d.withdrawQueue == nil {
		d.withdrawQueue = map[string]bool{}
	}
	d.withdrawQueue[block] = true
	d.withdrawQueueMu.Unlock()
}

// cancelUplinkWithdrawal forgets a queued withdrawal: the block's network has
// been recreated, so its delegation must survive whichever drain runs next.
// delegateQueuedRoutes is the caller.
func (d *Incus) cancelUplinkWithdrawal(block string) {
	d.withdrawQueueMu.Lock()
	delete(d.withdrawQueue, block)
	d.withdrawQueueMu.Unlock()
}

// drainUplinkWithdrawals takes every queued block off the uplink's ipv4.routes
// in a single write. The caller holds uplinkMu.
//
// The measurement is #519, and it is #473's mirror. Fifteen concurrent subnet
// deletes were serialised at a flat ~1.3 s each — one `network delete` (~0.2 s)
// plus one uplink write (~1 s, the daemon's firewall rebuild) per network, all
// under uplinkMu — so the fifteenth withdrawal waited for the fourteen before
// it. Draining the queue keeps the rebuilds serialised (#341's rule) and stops
// paying one per network.
//
// An empty queue is the normal case of a shared write: an earlier holder's
// drain already carried this caller's block, or a create re-delegated it and
// cancelled the withdrawal — the one case a "withdraw my own block no matter
// what" would strip a live network's route. On a failed write the blocks go
// back in the queue rather than vanishing, so the callers still waiting retry
// them under their own turn; the holder that saw the failure reports it.
// TestAFailedWithdrawalKeepsItsBlocksQueued fails without the requeue.
func (d *Incus) drainUplinkWithdrawals(ctx context.Context) error {
	d.withdrawQueueMu.Lock()
	blocks := make([]string, 0, len(d.withdrawQueue))
	for block := range d.withdrawQueue {
		blocks = append(blocks, block)
	}
	d.withdrawQueue = nil
	d.withdrawQueueMu.Unlock()
	if len(blocks) == 0 {
		return nil
	}
	// Sorted so two runs write the same value and a log diff means something.
	slices.Sort(blocks)
	if err := d.dropUplinkRoutes(ctx, blocks); err != nil {
		for _, block := range blocks {
			d.queueUplinkWithdrawal(block)
		}
		return fmt.Errorf("withdraw %s from uplink %s: %w",
			strings.Join(blocks, ","), d.uplinkName(), err)
	}
	return nil
}

// uplinkName returns the uplink network name, allowing an override.
func (d *Incus) uplinkName() string {
	if d.Uplink != "" {
		return d.Uplink
	}
	return DefaultUplinkName
}

func (d *Incus) uplinkCIDR() string {
	if d.UplinkCIDR != "" {
		return d.UplinkCIDR
	}
	return DefaultUplinkCIDR
}

// UplinkHolderKey is the config key (under user.) naming the emulator process
// that holds the uplink, as a pid. The uplink is shared host state, and the
// serialisation that keeps its rebuilds safe (uplinkMu) lives in one process:
// a second live emulator editing the same uplink is outside any lock, and the
// measured outcome of two concurrent edits is a corrupted firewall rebuild.
// So sharing refuses instead of corrupting: an uplink held by another live
// feint process is not reused.
const UplinkHolderKey = "feint.holder"

// ensureUplink creates the uplink bridge OVN networks attach to, once. The
// caller holds uplinkMu.
//
// An OVN network refuses to exist without an uplink to allocate its router
// address from (ipv4.ovn.ranges), so this runs before the first network. An
// existing network under the name is only reused when it carries the
// emulator's label: the operator's own bridges are never conscripted. And a
// labelled uplink is only reused when no other live emulator holds it —
// TestEnsureUplinkRefusesAnUplinkHeldByALiveEmulator fails without that
// refusal.
func (d *Incus) ensureUplink(ctx context.Context) error {
	name := d.uplinkName()
	if out, err := d.run(ctx, "query", "/1.0/networks/"+name); err == nil {
		var existing struct {
			Type   string            `json:"type"`
			Config map[string]string `json:"config"`
		}
		if err := json.Unmarshal(out, &existing); err != nil {
			return fmt.Errorf("decode uplink %s: %w", name, err)
		}
		if existing.Type != "bridge" || existing.Config["user."+LabelKey] == "" {
			return fmt.Errorf("network %s exists and is not the emulator's uplink; refusing to reuse it", name)
		}
		return d.adoptUplink(ctx, existing.Config)
	} else if !isNotFound(err) {
		return fmt.Errorf("inspect uplink %s: %w", name, err)
	}

	prefix, err := netip.ParsePrefix(d.uplinkCIDR())
	if err != nil {
		return fmt.Errorf("parse uplink CIDR %q: %w", d.uplinkCIDR(), err)
	}
	dhcp, ovn, err := uplinkRanges(prefix)
	if err != nil {
		return err
	}
	gateway := fmt.Sprintf("%s/%d", prefix.Masked().Addr().Next(), prefix.Bits())
	// NAT on: outbound traffic from an OVN network is first SNATed to the
	// uplink by the OVN router, then to the world by the host. Without the
	// second hop the emulated machines lose outbound access, which is what
	// their NAT-enabled subnets promise.
	if _, err := d.run(ctx, "network", "create", name,
		"ipv4.address="+gateway,
		"ipv4.nat=true",
		"ipv6.address=none",
		"ipv4.dhcp.ranges="+dhcp,
		"ipv4.ovn.ranges="+ovn,
		"user."+LabelKey+"=feint",
		"user."+UplinkHolderKey+"="+strconv.Itoa(os.Getpid())); err != nil {
		return fmt.Errorf("create uplink %s (%s): %w", name, gateway, err)
	}
	// A freshly created uplink has nothing to adopt: burn the once so a later
	// reuse of our own uplink does not "reconcile" away the routed /32s this
	// process adds between two network creates.
	d.uplinkAdopt.Do(func() {})
	return nil
}

// adoptUplink is the reuse half of ensureUplink: the uplink exists, is
// labelled ours, and was left by somebody. Two questions, in order.
//
// Whose is it now? A pid recorded by a live feint process means a second
// emulator would be editing shared host state outside any common lock, which
// is the measured corruption of #341 — so it refuses.
//
// What did the previous life leave on it? Every block a dead run delegated is
// still a real route on the host, captured towards the uplink: seven were
// measured on one station (#341). Nothing of a dead emulator is adopted
// anywhere else, so its routes are not either: the adopt keeps exactly the
// blocks of the labelled OVN networks still standing (the next run reuses
// those by name), drops the rest, and records this process as holder. Once
// per process, because staleness is a fact about what predates it.
// TestAdoptedUplinkDropsADeadRunsRoutes fails without the drop.
func (d *Incus) adoptUplink(ctx context.Context, config map[string]string) error {
	holder := config["user."+UplinkHolderKey]
	self := strconv.Itoa(os.Getpid())
	if holder != "" && holder != self {
		if pid, err := strconv.Atoi(holder); err == nil && d.holderIsAlive(pid) {
			return fmt.Errorf("uplink %s is held by another running emulator (pid %d); "+
				"two emulators corrupt each other's firewall rebuilds, so sharing is refused — "+
				"stop the other emulator, or point this one at another uplink", d.uplinkName(), pid)
		}
	}
	var adoptErr error
	d.uplinkAdopt.Do(func() {
		kept, err := d.liveDelegations(ctx)
		if err != nil {
			adoptErr = err
			return
		}
		args := []string{"network", "set", d.uplinkName(),
			"user." + UplinkHolderKey + "=" + self}
		if current := strings.TrimSpace(config["ipv4.routes"]); current != strings.Join(kept, ",") {
			args = append(args, "ipv4.routes="+strings.Join(kept, ","))
		} else if holder == self {
			return // Nothing stale, and already ours: spare the rebuild.
		}
		if _, err := d.run(ctx, args...); err != nil {
			adoptErr = fmt.Errorf("adopt uplink %s: %w", d.uplinkName(), err)
		}
	})
	return adoptErr
}

// liveDelegations lists the blocks that must stay delegated to the uplink: the
// block of every labelled OVN network still attached to it.
//
// It shares its reading with ovnNetworksOnUplink (incus_traps.go), which needs
// the same list with the names kept. Two readings of "which of our networks
// draw from this uplink" would drift apart, and the sweep now depends on the
// two agreeing: it restores what the adopt would have kept.
func (d *Incus) liveDelegations(ctx context.Context) ([]string, error) {
	views, err := d.networkViews(ctx)
	if err != nil {
		return nil, fmt.Errorf("adopt uplink %s: %w", d.uplinkName(), err)
	}
	networks := d.ovnNetworksOnUplink(views)
	kept := make([]string, 0, len(networks))
	for _, network := range networks {
		kept = append(kept, network.Block)
	}
	if len(kept) == 0 {
		return nil, nil
	}
	return kept, nil
}

// maskedBlock renders an address with its prefix length as the block it sits
// in, which is the form the uplink's ipv4.routes carries: 10.2.4.1/24 is the
// gateway of 10.2.4.0/24, and it is the block that must be delegated.
func maskedBlock(address string) (string, error) {
	prefix, err := netip.ParsePrefix(address)
	if err != nil {
		return "", err
	}
	return prefix.Masked().String(), nil
}

// holderIsAlive reports that the pid recorded on the uplink still belongs to a
// live feint process. Conservative in the direction that refuses: only a
// readable /proc entry whose binary is feint counts as alive, so a recycled
// pid running something else does not hold the uplink hostage.
func (d *Incus) holderIsAlive(pid int) bool {
	if d.holderProbe != nil {
		return d.holderProbe(pid)
	}
	argv, err := procArgv(pid)
	if err != nil || len(argv) == 0 {
		return false
	}
	return strings.HasPrefix(filepath.Base(argv[0]), "feint")
}

// uplinkRanges splits an uplink block into the two ranges Incus wants: DHCP
// for anything bridged directly, ipv4.ovn.ranges for the router addresses OVN
// networks draw. They must not overlap, or the same address is handed out
// twice, so the block is cut in half after the gateway.
func uplinkRanges(prefix netip.Prefix) (dhcp, ovn string, err error) {
	if !prefix.Addr().Is4() {
		return "", "", fmt.Errorf("uplink block %s is not IPv4", prefix)
	}
	if prefix.Bits() > 28 {
		return "", "", fmt.Errorf("uplink block %s is too small, need a /28 or larger", prefix)
	}
	base := binary.BigEndian.Uint32(func() []byte { b := prefix.Masked().Addr().As4(); return b[:] }())
	size := uint32(1) << (32 - prefix.Bits())

	at := func(offset uint32) netip.Addr {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], base+offset)
		return netip.AddrFrom4(b)
	}
	// .0 is the network, .1 the gateway, the top address the broadcast.
	dhcp = fmt.Sprintf("%s-%s", at(2), at(size/2-1))
	ovn = fmt.Sprintf("%s-%s", at(size/2), at(size-2))
	return dhcp, ovn, nil
}

// ---- Peering ----------------------------------------------------------------

// NativeIsolation implements Peerer.
func (d *Incus) NativeIsolation() bool { return d.OVN }

// peerLock excludes every other peering this driver declares on the networks it
// names, until the returned function is called. One lock per network, taken in
// sorted order — which is what makes a set of them deadlock-free — so a pair
// excludes exactly the work that could disturb it and two disjoint pairs still
// run at once.
//
// A lock keyed by the *pair* is not enough, and the reason is in the runtime
// rather than in this driver. Incus completes a peering by looking for a pending
// half aiming at the network the create lands on, and that lookup filters on the
// target network alone — there is no clause on which network holds the row
// (v7.2.0, internal/server/network/driver_ovn.go, PeerCreate: GetNetworkPeers
// with TargetNetworkProject and TargetNetworkName, then "More than one matching
// network peer was found" when it matches twice). So two pending halves aiming
// at one network are fatal to every create on it, whichever pairs they belong
// to. Measured on Incus 7.2, three real OVN networks, nothing concurrent:
//
//	incus network peer create A B B  -> pending                        rc=0
//	incus network peer create C B B  -> pending                        rc=0
//	incus network peer create B A A  -> Error: Failed creating peer:
//	                                    More than one matching network
//	                                    peer was found                 rc=1
//
// (A,B) and (C,B) are two different pairs, so a per-pair key would have let
// precisely that through; a lock on each end lets neither in while the other
// works. That is the shape of #456: a Net of N subnets declares N(N-1) halves
// as its subnets appear, and only a Net with three or more can have two of them
// aiming at one network at once — which is why the two-subnet fixtures never
// tripped it and a stranger's three-subnet Net did.
//
// Never one global lock: a Net's subnets are created in parallel on purpose,
// and serialise.go's rule is that the exclusion is named, not widened.
//
// Its domain is deliberately not networkLock's: PeerNetworks ends in
// IsolateNetwork, which takes that one, and serialise.Lock is not reentrant.
// TestConcurrentPairsOfOneNetDoNotCollideOnAPendingHalf fails without this.
func (d *Incus) peerLock(names ...string) func() {
	ordered := make([]string, 0, len(names))
	for _, name := range names {
		if name == "" || slices.Contains(ordered, name) {
			continue
		}
		ordered = append(ordered, name)
	}
	slices.Sort(ordered)
	releases := make([]func(), 0, len(ordered))
	for _, name := range ordered {
		releases = append(releases, serialise.Lock("incus.peering."+name))
	}
	return func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
}

// PeerNetworks implements Peerer.
//
// Reconciled, not appended: peers the caller no longer names are removed, so
// a VPC that stops routing between its subnets actually separates them again.
// A peering only carries traffic once both networks declare it, so both halves
// are declared here, as one operation under the pair's locks.
func (d *Incus) PeerNetworks(ctx context.Context, network string, peers []string) error {
	if !d.OVN {
		// Bridges on one host are routed together already; there is nothing to
		// join, and IsolateNetwork is what keeps them apart.
		return nil
	}
	if !safeName.MatchString(network) {
		return fmt.Errorf("invalid network name %q", network)
	}
	// Both ends are checked: a peering joins two networks, so naming one of the
	// operator's on either side would route the emulator's traffic into it.
	if !ownedNetwork(network) {
		return fmt.Errorf("refusing to peer network %q: not one the emulator created", network)
	}
	desired := make(map[string]bool, len(peers))
	for _, peer := range peers {
		if !safeName.MatchString(peer) {
			return fmt.Errorf("invalid network name %q", peer)
		}
		if !ownedNetwork(peer) {
			return fmt.Errorf("refusing to peer with network %q: not one the emulator created", peer)
		}
		desired[peer] = true
	}

	existing, err := d.networkPeers(ctx, network)
	if err != nil {
		return err
	}
	for _, row := range existing {
		// An empty target is the wreck of a peering whose other half is gone;
		// it is as stale as one pointing at a network no longer reachable.
		if row.Target != "" && desired[row.Target] {
			continue
		}
		if err := d.removePeerRow(ctx, network, row); err != nil {
			return err
		}
	}
	for _, peer := range peers {
		if err := d.ensurePeering(ctx, network, peer); err != nil {
			return err
		}
	}
	// And the half a peering does not provide. Peering says what may reach this
	// network; nothing said what may not, and the uplink this driver creates
	// routes every one of its OVN subnets to every other. The rule set is
	// applied here rather than in each pack, because a control copied into three
	// packs is a control one pack forgets and a fourth never has.
	return d.isolateOVN(ctx, network, peers)
}

// peerCreated is the runtime's own word for a peering both ends have declared;
// it is the only status under which one carries traffic. Pending and Errored
// are the two ways a row can be there and mean nothing, and telling them apart
// from Created is what stops this driver reporting a peering it did not make.
const peerCreated = "Created"

// ensurePeering makes two networks peer each other, the pair being the unit
// rather than each half on its own.
//
// A half is only worth keeping when the runtime calls it Created. A half whose
// other end has gone is left behind as a row with no target at all, and the
// runtime then answers "A peer for that name already exists" to every attempt
// to declare that half again — which the code before #456 tolerated as success,
// so the peering was reported applied and did not exist. Measured on Incus 7.2,
// on a three-network mesh, with nothing concurrent:
//
//	incus network peer delete B C                                      rc=0
//	incus query /1.0/networks/A/peers  -> {"name":"B","target_network":null,
//	                                       "status":"Errored"}
//	incus network peer create A B B    -> Error: Failed creating peer:
//	                                       A peer for that name already exists
//
// Note which pair broke: the delete named B and C, and it is A's half that was
// wrecked. PeerDelete clears the target of *every* row aiming at the network it
// runs on, so removing one subnet from a Net damages the halves of its
// neighbours too. That is why a pair that is not established at both ends is
// rebuilt rather than patched: the row that looks wrong and the row that looks
// right are the same peering.
//
// The ordinary path costs two reads and nothing else: a pair being declared for
// the first time has no rows to take down, so an apply performs no delete and
// leaves no wreck behind.
// TestAWreckedHalfIsRebuiltRatherThanReportedApplied fails without this.
func (d *Incus) ensurePeering(ctx context.Context, network, peer string) error {
	release := d.peerLock(network, peer)
	defer release()

	near, err := d.networkPeers(ctx, network)
	if err != nil {
		return err
	}
	far, err := d.networkPeers(ctx, peer)
	if err != nil {
		return err
	}
	if peeringEstablished(near, peer) && peeringEstablished(far, network) {
		return nil
	}
	if err := d.dropPeerHalf(ctx, network, near, peer); err != nil {
		return err
	}
	if err := d.dropPeerHalf(ctx, peer, far, network); err != nil {
		return err
	}
	if err := d.declarePeerHalf(ctx, network, peer); err != nil {
		return err
	}
	return d.declarePeerHalf(ctx, peer, network)
}

// peeringEstablished reports that this end of the pair carries traffic: a row
// aiming at the other network which the runtime itself calls Created.
//
// A pending half is not established — it grants nothing, which the network
// conformance suite asserts against the real runtime — and neither is a wreck.
// An empty status is read as established, because a runtime that does not say
// must not have this driver tear a working mesh down on every pass; that is the
// same rule as an undeclared capability counting as absent, pointed the way that
// refuses to act.
func peeringEstablished(rows []peerRow, other string) bool {
	for _, row := range rows {
		if row.Target == other && (row.Status == peerCreated || row.Status == "") {
			return true
		}
	}
	return false
}

// dropPeerHalf takes this end's half of one pair down, whatever state it is in:
// the row aiming at the other network, and the row holding that network's name
// with no target left, which are the same half seen before and after the runtime
// blanked it.
//
// The ownership question is asked here and not at each caller, because this is
// the only place a peering is deleted on the way to declaring one. It is asked
// against the label EnsureNetwork wrote, never the name: `fnt-` is a prefix
// anybody may type, and an operator's own OVN network called `fnt-lab`, peered
// with one of ours by hand, would otherwise have this reach into theirs. That is
// the guard #455 had to add to the sweep, and it belongs on every path that
// deletes a peering.
// TestPeeringRefusesToRebuildOnAStrangersNetworkNamedLikeOurs fails without it.
func (d *Incus) dropPeerHalf(ctx context.Context, network string, rows []peerRow, other string) error {
	doomed := make([]peerRow, 0, 2)
	for _, row := range rows {
		if row.Name == other || row.Target == other {
			doomed = append(doomed, row)
		}
	}
	if len(doomed) == 0 {
		return nil
	}
	if !d.ownsPeerTarget(ctx, network) {
		return fmt.Errorf("refusing to rebuild the peering between %s and %s: %s is not a network the emulator created",
			network, other, network)
	}
	for _, row := range doomed {
		if _, err := d.run(ctx, "network", "peer", "delete", network, row.Name); err != nil && !isNotFound(err) {
			return fmt.Errorf("take down the %s half of the peering with %s: %w", network, other, err)
		}
	}
	return nil
}

// removePeerRow deletes one row of a network's own peer list, under the locks of
// both ends so it cannot cross a declaration of the same pair.
func (d *Incus) removePeerRow(ctx context.Context, network string, row peerRow) error {
	release := d.peerLock(network, row.Target, row.Name)
	defer release()
	if _, err := d.run(ctx, "network", "peer", "delete", network, row.Name); err != nil && !isNotFound(err) {
		return fmt.Errorf("remove stale peering %s of %s: %w", row.Name, network, err)
	}
	return nil
}

// declarePeerHalf declares one direction of a peering, under the target's name
// so the pair is recognisable from either side.
//
// Two answers are not failures. "Already exists" is another declaration of the
// same half arriving first — the pair's locks exclude every other goroutine
// here, so it is a second process, and its work is this call's work. "More than
// one matching network peer was found" is the runtime refusing to guess between
// several pending halves aiming at this network (see peerLock for the source and
// the measurement): the locks stop this process from making that state, but a
// crashed run, or a run of a version without them, leaves it on the host, and
// nothing a user can type repairs it. So it is reconciled and the create is
// re-issued once.
// TestASecondPendingHalfIsReconciledRatherThanReported fails without the branch.
func (d *Incus) declarePeerHalf(ctx context.Context, from, to string) error {
	_, err := d.run(ctx, "network", "peer", "create", from, to, to)
	if err == nil {
		return nil
	}
	said := strings.ToLower(err.Error())
	if strings.Contains(said, "already exists") {
		return nil
	}
	if !strings.Contains(said, "more than one matching network peer") {
		return fmt.Errorf("peer %s with %s: %w", from, to, err)
	}
	dropped, rivalErr := d.clearRivalPendingHalves(ctx, from, to)
	if rivalErr != nil {
		return fmt.Errorf("peer %s with %s: %w (the pending halves aiming at %s could not be read: %w)",
			from, to, err, from, rivalErr)
	}
	if dropped == 0 {
		return fmt.Errorf("peer %s with %s: %w (several pending halves aim at %s and none of them is the emulator's to remove)",
			from, to, err, from)
	}
	if _, err := d.run(ctx, "network", "peer", "create", from, to, to); err != nil &&
		!strings.Contains(strings.ToLower(err.Error()), "already exists") {
		return fmt.Errorf("peer %s with %s, after clearing %d pending half(s) aiming at %s: %w",
			from, to, dropped, from, err)
	}
	return nil
}

// clearRivalPendingHalves removes the pending halves aiming at one network that
// come from anywhere except the peer being declared, and answers how many went.
//
// Only the emulator's own OVN networks are read, and each one is asked for the
// label before anything is deleted on it: a pending half on a network somebody
// else created is somebody else's, and leaving the create to fail is the honest
// outcome. A half taken down here carried no traffic — pending grants nothing —
// and the reconciliation that owns it is level-based, so it is declared again on
// the next pass.
func (d *Incus) clearRivalPendingHalves(ctx context.Context, aimedAt, keep string) (int, error) {
	subnets, err := d.ourOVNSubnets(ctx)
	if err != nil {
		return 0, err
	}
	names := make([]string, 0, len(subnets))
	for name := range subnets {
		if name == aimedAt || name == keep {
			continue
		}
		names = append(names, name)
	}
	// Sorted, so two runs of the same repair emit the same commands.
	slices.Sort(names)

	dropped := 0
	for _, name := range names {
		rows, err := d.networkPeers(ctx, name)
		if err != nil {
			// A network that went while this was being read holds no half.
			continue
		}
		for _, row := range rows {
			if row.Target != aimedAt || row.Status == peerCreated {
				continue
			}
			if !d.ownsPeerTarget(ctx, name) {
				break
			}
			if _, err := d.run(ctx, "network", "peer", "delete", name, row.Name); err != nil && !isNotFound(err) {
				return dropped, fmt.Errorf("clear the pending half %s of %s: %w", row.Name, name, err)
			}
			dropped++
		}
	}
	return dropped, nil
}

type peerRow struct {
	Name string
	// Target is the network on the far end. The runtime blanks it when that
	// network's peer list is edited, which is what a wreck looks like.
	Target string
	// Status is the runtime's own verdict: Created, Pending, or Errored.
	Status string
}

func (d *Incus) networkPeers(ctx context.Context, network string) ([]peerRow, error) {
	out, err := d.run(ctx, "query", "/1.0/networks/"+network+"/peers?recursion=1")
	if err != nil {
		return nil, fmt.Errorf("list peers of %s: %w", network, err)
	}
	var raw []struct {
		Name          string `json:"name"`
		TargetNetwork string `json:"target_network"`
		Status        string `json:"status"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("decode peers of %s: %w", network, err)
	}
	rows := make([]peerRow, 0, len(raw))
	for _, r := range raw {
		rows = append(rows, peerRow{Name: r.Name, Target: r.TargetNetwork, Status: r.Status})
	}
	return rows, nil
}

// ---- Public addresses -------------------------------------------------------

// routeAddressOVN gives a machine a public address through the NIC's external
// routes, which is the bridge path's ipv4.routes translated into OVN's terms.
//
// ipv4.routes.external is what the runtime designed for this: with the
// uplink's default ovn.ingress_mode of l2proxy it becomes a stateless
// dnat_and_snat entry, and OVN then answers ARP requests for the address on
// the uplink (driver_ovn.go at v7.2.0, the l2proxy comment) and delivers
// packets carrying the public destination address, exactly Scaleway's
// routed-IP mode. A network forward was measured first and rejected: its load
// balancer VIP is only announced by a burst of gratuitous ARPs at creation
// time, so the address answered when probed within seconds and went dark
// afterwards — a nondeterminism this emulator cannot ship.
//
// The cost is a re-plug: the route keys are not live-updatable on an OVN NIC
// (UpdatableFields in nic_ovn.go at v7.2.0), so the device is removed and
// re-added, and the guest loses the interface's addresses. The repair below
// puts them back; the observable effect is a brief interface bounce at attach
// time, which real clouds also produce when reshaping a live NIC.
func (d *Incus) routeAddressOVN(ctx context.Context, spec AddressSpec) error {
	if !safeName.MatchString(spec.Machine) {
		return fmt.Errorf("invalid machine name %q", spec.Machine)
	}
	network, device, err := d.interfaceFor(ctx, spec.Machine, spec.Network)
	if err != nil {
		return err
	}
	if err := d.mustOwn(ctx, network); err != nil {
		return err
	}
	// Exclusive on the NIC's network for the whole edit. The route keys are
	// not live-updatable on an OVN NIC, so the set below re-plugs the device,
	// and the daemon then re-ensures every rule set the network references —
	// resolving names to IDs with no lock shared with its own ACL paths. An
	// isolation detach running concurrently (IsolateNetwork holds this same
	// lock) deletes the referenced set between the two steps, and the edit
	// dies on `Cannot find security ACL ID for "iso-fnt-…"` with the failed
	// update operation holding the instance busy — the first link of #493's
	// chain. Same lock, so the edit and the detach take turns.
	// TestARouteAddressEditAndAnIsolationDetachTakeTurns fails without it.
	release := d.networkLock(network)
	defer release()
	devices, err := d.instanceDevices(ctx, spec.Machine)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", spec.Machine, err)
	}

	// The uplink's route list is what the runtime validates external routes
	// against, and what routes the host's own traffic towards the uplink.
	if err := d.setUplinkRoute(ctx, spec.Address, true); err != nil {
		return err
	}

	route := spec.Address + "/32"
	if !routeListContains(devices.own[device]["ipv4.routes.external"], route) {
		merged := appendRoute(devices.own[device]["ipv4.routes.external"], route)
		if _, err := d.run(ctx, "config", "device", "set", spec.Machine, device,
			"ipv4.routes.external="+merged); err != nil {
			return fmt.Errorf("route %s to %s/%s: %w", spec.Address, spec.Machine, device, err)
		}
		if err := d.repairGuestInterface(ctx, spec.Machine, network, device); err != nil {
			return err
		}
	}

	// The machine answers on it, a /32 because the address is a route to this
	// machine, not a subnet it belongs to. Already-there is the second call's
	// success, in whichever wording the guest's `ip` uses (addressAlreadyThere:
	// busybox does not say "file exists", and Alpine ships busybox).
	if _, err := d.run(ctx, "exec", spec.Machine, "--",
		"ip", "address", "add", route, "dev", device); err != nil &&
		!addressAlreadyThere(err) {
		return fmt.Errorf("give %s to %s: %w", spec.Address, spec.Machine, err)
	}
	return nil
}

// unrouteAddressOVN takes a public address back: the mirror of the OVN half
// of RouteAddress, with the same re-plug and the same repair.
func (d *Incus) unrouteAddressOVN(ctx context.Context, machine, address string) error {
	route := address + "/32"
	if machine != "" {
		devices, err := d.instanceDevices(ctx, machine)
		if err != nil && !isNotFound(err) {
			return fmt.Errorf("inspect %s: %w", machine, err)
		}
		for device, cfg := range devices.own {
			if cfg["type"] != "nic" {
				continue
			}
			// A routed NIC in OVN mode (#337): a machine that joins no network
			// has no OVN port, so its extra addresses live in the device's own
			// ipv4.routes, exactly as in bridge mode — and removing one
			// re-plugs the device, so the same repair follows.
			if cfg["nictype"] == "routed" {
				if !routeListContains(cfg["ipv4.routes"], route) {
					continue
				}
				if err := d.setDeviceRoutes(ctx, machine, device, address, false); err != nil {
					return err
				}
				if err := d.repairRoutedInterface(ctx, machine, device); err != nil {
					return err
				}
				continue
			}
			if !routeListContains(cfg["ipv4.routes.external"], route) {
				continue
			}
			if err := d.unrouteOVNDevice(ctx, machine, device, cfg["network"], address); err != nil {
				return err
			}
		}
	}
	return d.setUplinkRoute(ctx, address, false)
}

// unrouteOVNDevice takes one public address off one OVN NIC, holding the
// network's lock across the edit and its repair.
//
// The lock is the ordering half of #493: the set below re-plugs the device,
// the daemon re-ensures every rule set the NIC's network references while
// setting the OVN port back up, and an isolation detach landing between the
// two steps leaves it resolving a rule set that no longer exists — `Cannot
// find security ACL ID for "iso-fnt-…"`, with the failed update operation
// holding the instance busy against the stop and remove that follow. The
// detach holds this same lock (IsolateNetwork), so the two take turns; taken
// per device, because the loop above walks NICs that may sit on different
// networks and each edit only needs its own.
// TestAPublicAddressEditAndAnIsolationDetachTakeTurns fails without it.
func (d *Incus) unrouteOVNDevice(ctx context.Context, machine, device, network, address string) error {
	if network != "" {
		release := d.networkLock(network)
		defer release()
	}
	devices, err := d.instanceDevices(ctx, machine)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("inspect %s: %w", machine, err)
	}
	cfg := devices.own[device]
	route := address + "/32"
	if !routeListContains(cfg["ipv4.routes.external"], route) {
		// Withdrawn while this call waited for the lock: nothing left to undo.
		return nil
	}
	kept := removeRoute(cfg["ipv4.routes.external"], route)
	if _, err := d.run(ctx, "config", "device", "set", machine, device,
		"ipv4.routes.external="+kept); err != nil {
		return fmt.Errorf("unroute %s from %s/%s: %w", address, machine, device, err)
	}
	// The re-plug already dropped every guest address, including the
	// public one; the repair puts back only what stays.
	return d.repairGuestInterface(ctx, machine, network, device)
}

// repairGuestInterface restores what a device re-plug cost the guest: the
// address the interface carried, the link state, and the routes. Measured on
// 7.2: any change to a non-live-updatable key removes and re-adds the NIC,
// and the new interface comes up bare, with no DHCP client watching it.
//
// Two kinds of address, one repair. A pinned one is read off the device key.
// A DHCP-owned one used to be declared unrepairable here, which turned a hot
// route edit into a machine with no address at all — measured: RUNNING, guest
// bare, sshd unreachable. The runtime itself records what the interface
// carried (volatile.<device>.last_state.ip_addresses), the re-plug keeps the
// NIC's hwaddr, and OVN's IPAM ties the address to that MAC — so restoring
// the recorded address statically restores the port's own reservation, and
// the lease's default route comes back with it.
// TestAHotRouteEditRepairsADHCPInterface fails without the second kind.
func (d *Incus) repairGuestInterface(ctx context.Context, machine, network, device string) error {
	devices, err := d.instanceDevices(ctx, machine)
	if err != nil {
		return fmt.Errorf("inspect %s after re-plug: %w", machine, err)
	}
	address := devices.own[device]["ipv4.address"]
	leased := false
	if address == "" {
		address = d.lastKnownAddress(ctx, machine, device)
		leased = true
	}
	if address == "" {
		// The interface never carried an address; there is nothing to put back.
		return nil
	}
	gateway, err := d.networkGateway(ctx, network)
	if err != nil {
		return err
	}
	if err := d.configureGuestAddress(ctx, machine, device,
		fmt.Sprintf("%s/%d", address, gateway.Bits())); err != nil {
		return err
	}
	if leased {
		// The default route died with the lease, and nothing renews either.
		// Restored only for the leased case: a pinned private NIC never had
		// one, and inventing it would route a machine the control plane
		// declared isolated.
		if _, err := d.run(ctx, "exec", machine, "--",
			"ip", "route", "add", "default", "via", gateway.Addr().String(), "dev", device); err != nil &&
			!strings.Contains(strings.ToLower(err.Error()), "file exists") {
			return fmt.Errorf("restore the default route of %s: %w", machine, err)
		}
	}
	// The routes towards the peered subnets died with the interface too.
	return d.installGuestPrivateRoutes(ctx, machine, network, device)
}

// lastKnownAddress is the IPv4 the runtime last saw on an interface, from the
// volatile key Incus maintains per device. Empty when it never carried one.
func (d *Incus) lastKnownAddress(ctx context.Context, machine, device string) string {
	out, err := d.run(ctx, "config", "get", machine, "volatile."+device+".last_state.ip_addresses")
	if err != nil {
		return ""
	}
	for _, field := range strings.Split(strings.TrimSpace(string(out)), ",") {
		if addr, err := netip.ParseAddr(strings.TrimSpace(field)); err == nil && addr.Is4() {
			return addr.String()
		}
	}
	return ""
}

// networkGateway reads a network's gateway address, with its mask, from the
// runtime ("10.181.0.1/24").
func (d *Incus) networkGateway(ctx context.Context, network string) (netip.Prefix, error) {
	out, err := d.run(ctx, "network", "get", network, "ipv4.address")
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("read the block of %s: %w", network, err)
	}
	prefix, err := netip.ParsePrefix(strings.TrimSpace(string(out)))
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("parse the block of %s: %w", network, err)
	}
	return prefix, nil
}

// privateAggregates is where a guest's traffic towards other emulated subnets
// is sent: to the OVN router, which holds the peerings. The three RFC 1918
// blocks rather than the peered subnets themselves, because peers appear and
// disappear with every network create and delete, and enumerating them would
// turn each change into an exec into every machine of the network. The
// aggregate is safe: the NIC's own connected route is more specific and still
// wins, and a destination the router has no peering for dies at the router,
// which is exactly where the isolation verdict belongs.
var privateAggregates = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}

// guestRoutePoll and guestRouteWait bound the wait for a restarted guest to
// have configured its interface again. A container's DHCP client answers in a
// second or two, a virtual machine's in tens of them; past a minute and a half
// the machine has a problem an operator should read about rather than wait
// through.
const (
	guestRoutePoll = 2 * time.Second
	guestRouteWait = 90 * time.Second
)

// restoreGuestNetwork puts back, inside a machine that has just been started
// again, what a boot does not restore: the address its NIC device reserves,
// and the routes towards the peered subnets.
//
// The measurement is #549, and it is the exact shape of "a machine the control
// plane describes correctly and that does not work". A Scaleway server was
// stopped and started through the API; it came back running, on its address,
// answering on its own subnet — and no longer reaching the machine one subnet
// away, while its identical neighbour, never restarted, kept reaching it in the
// same pass. The three RFC 1918 aggregates installGuestPrivateRoutes lays were
// gone from its table: the guest's network had been rebuilt by DHCP, which
// knows nothing about the peerings, and the only two callers that laid them —
// Attach, and the repair of an interface that bounced — are exactly the two a
// poweron does not go through.
//
// So it belongs on the start path and in this layer: no pack can forget it,
// and a fourth pack gets it without writing a line. What is restored is read
// off the runtime rather than off the caller's Spec, because the machine's
// interfaces are the runtime's own answer and a Spec built from a stale store
// would restore the wrong thing — or nothing, which is what a Scaleway server
// whose NICs ride the launch would have got.
//
// The two halves differ by mode and the difference is the modes' own: the
// address is owed back under a bridge exactly as under OVN — measured
// 2026-08-28, a bridge-mode machine came back with 203.0.113.2 on two
// interfaces and no address on its own subnet, and its published address
// stopped answering — while the aggregates exist only because an OVN
// network's peers are reachable through its router alone. A managed bridge
// has no router of its own and no peerings, so there is nothing of that kind
// to put back there.
//
// TestARestartedMachineGetsItsPeeredRoutesBack and
// TestARestartedMachineGetsItsPinnedAddressBack fail without this, the second
// in both modes.
func (d *Incus) restoreGuestNetwork(ctx context.Context, machine string) error {
	devices, names, err := d.ownedManagedNICs(ctx, machine)
	if err != nil {
		return fmt.Errorf("inspect %s to restore its routes: %w", machine, err)
	}
	for _, device := range names {
		// The address first, when the device pins one (#548). A NIC attached
		// to a running machine is configured inside the guest by this driver,
		// never by DHCP — the address is reserved on the device — and nothing
		// inside the guest remembers it across a boot. Measured 2026-08-28
		// under `--vm incus-ovn` on a Scaleway server whose private NIC
		// arrived hot: after a reboot the guest carried nothing on eth1, this
		// function's wait ran its full ninety seconds and gave up, and the
		// machine came back with neither its private address nor the routes
		// below.
		//
		// That was survivable while the public address rode a routed NIC of
		// its own; it stops being survivable the moment the address lives on
		// this interface, because the reply to a station that dialled it is
		// routed by the aggregates the failed wait never laid. So the restart
		// path restores what it reserved instead of waiting for a lease
		// nobody offers.
		// TestARestartedMachineGetsItsPinnedAddressBack fails without this.
		if err := d.restorePinnedAddress(ctx, machine, device, devices.own[device]); err != nil {
			return err
		}
		if !d.OVN {
			continue
		}
		// The wait is the ordering, not politeness: `ip route add … via <gw>
		// dev ethN` is refused while the interface carries no address of that
		// subnet, and a route laid before DHCP finished is a route DHCP
		// replaces. It still runs for a device that pins nothing, which is the
		// DHCP case it was written for.
		if err := d.waitForGuestInterface(ctx, machine, device); err != nil {
			return err
		}
		if err := d.installGuestPrivateRoutes(ctx, machine, devices.own[device]["network"], device); err != nil {
			return err
		}
	}
	return nil
}

// ownedManagedNICs answers the machine's own NIC devices that sit on a network
// this emulator derived, sorted so two runs repair them in the same order and a
// failure names the same device twice.
//
// Shared by the two doors below rather than written twice, which is this
// repository's own rule for anything a second caller would copy: the ownership
// half is what keeps a NIC an operator added by hand out of every command these
// paths emit, and a copy is what one door eventually forgets.
// TestRestoringRoutesLeavesAForeignNICAlone and
// TestAFirstBootLeavesAForeignNICAlone fail without it, one per door.
func (d *Incus) ownedManagedNICs(ctx context.Context, machine string) (instanceView, []string, error) {
	devices, err := d.instanceDevices(ctx, machine)
	if err != nil {
		return devices, nil, err
	}
	names := make([]string, 0, len(devices.own))
	for device, cfg := range devices.own {
		if cfg["type"] != "nic" || cfg["network"] == "" {
			continue
		}
		// Ours only, and the question is asked of the name the emulator itself
		// derives (NetworkName). These calls read a network's gateway and then
		// write inside the guest through it: a NIC an operator added by hand to
		// one of our machines is theirs, and is left exactly as they
		// configured it.
		if !ownedNetwork(cfg["network"]) {
			continue
		}
		names = append(names, device)
	}
	slices.Sort(names)
	return devices, names, nil
}

// settleFirstBoot gives a machine that has just been created the addresses its
// own devices reserve, and the routes that go with them.
//
// WHY THE FIRST BOOT NEEDS THE SAME THING AS A RESTART (#587).
//
// The launch pins `ipv4.address` on the NIC and then trusts the guest's DHCP
// client to ask for it. That trust is not warranted, and the failure is not
// slow — it is permanent, and it depends on which client the image ships:
//
//	image (Outscale catalogue)   address carried after CreateVms answered
//	ubuntu:24.04  (ami-…0001)    27 ms, 32 ms
//	debian:12     (ami-…0002)    46 ms, 35 ms
//	alpine:3.21   (ami-…0003)    never — measured to 45 s, 90 s and 180 s
//
// Measured on the maintainer's station, 2026-08-29, `--vm incus-ovn`, Incus 7.2,
// OVN 24.03.6. The alpine image ships dhcpcd 10.1.0, which ARP-probes the
// offered address before accepting it. The probe is flooded to the network's
// localnet port, reaches the uplink, and the OVN gateway router answers it —
// answering ARP for the block it fronts is that router's job. The guest reads
// its own address as taken and declines its own lease, forever:
//
//	eth0: offered 10.182.9.4 from 10.182.9.1
//	eth0: probing address 10.182.9.4/24
//	eth0: 10:66:6a:88:5e:54(10:66:6a:88:5e:54) claims 10.182.9.4
//	eth0: DAD detected 10.182.9.4
//
// So the address the API published was never on the machine, `wait_until 24` in
// tools/conformance/outscale/network.sh expired at 24.8 s, and no budget could
// have helped: at the end of a 90 s wait the guest's client had given up, while
// a `udhcpc` exchange in that same guest was answered in 97–143 ms. The path was
// open the whole time. Raising the budget would only have made the DAD answer
// more certain, which is why CI's green on the same suite is luck rather than
// proof — its probe wins a race this station loses.
//
// The fix is the one the driver already makes everywhere else it reserves an
// address: Attach configures the guest, and restoreGuestNetwork puts it back
// after a reboot. Only the first boot was left trusting DHCP. It belongs in
// this layer for the reason #549 gives: no pack can forget it, and a fourth
// pack gets it without writing a line.
//
// Devices that pin nothing are left alone — that is the DHCP case, it is
// correct, and inventing an address for it is exactly what this emulator must
// not do. There is no wait here for the same reason: the address this puts on
// the interface is the one it just reserved, so the routes that follow have
// their connected subnet already.
//
// TestAFirstBootGivesTheGuestTheAddressItReserved fails without this.
func (d *Incus) settleFirstBoot(ctx context.Context, machine string) error {
	devices, names, err := d.ownedManagedNICs(ctx, machine)
	if err != nil {
		return fmt.Errorf("inspect %s to settle its interfaces: %w", machine, err)
	}
	for _, device := range names {
		if devices.own[device]["ipv4.address"] == "" {
			continue // DHCP owns this interface, and inventing an address for it is #202
		}
		if err := d.restorePinnedAddress(ctx, machine, device, devices.own[device]); err != nil {
			return fmt.Errorf("settle the first boot of %s: %w", machine, err)
		}
		if d.OVN {
			// A managed bridge has no router and no peerings, so there is
			// nothing of this kind to lay there — the same mode split
			// restoreGuestNetwork makes, for the same reason.
			if err := d.installGuestPrivateRoutes(ctx, machine, devices.own[device]["network"], device); err != nil {
				return fmt.Errorf("settle the first boot of %s: %w", machine, err)
			}
		}
	}
	return nil
}

// restorePinnedAddress gives the guest back the address its NIC device
// reserves, when the device reserves one. A device that pins nothing is left
// to DHCP, which is what the wait beside this call exists for.
//
// The mask comes from the network rather than from the caller: the reservation
// on the device carries no prefix length, and an address configured as a /32
// inside the guest has no connected route to its own subnet.
func (d *Incus) restorePinnedAddress(ctx context.Context, machine, device string, cfg map[string]string) error {
	address := cfg["ipv4.address"]
	if address == "" {
		return nil
	}
	gateway, err := d.networkGateway(ctx, cfg["network"])
	if err != nil {
		return err
	}
	// Wrapped so the failure names the interface: the caller's own report is
	// what an operator reads when a machine comes back unreachable, and
	// "which one" is the first thing they need.
	if err := d.configureGuestAddress(ctx, machine, device,
		fmt.Sprintf("%s/%d", address, gateway.Bits())); err != nil {
		return fmt.Errorf("restore the address of %s/%s: %w", machine, device, err)
	}
	return nil
}

// waitForGuestInterface blocks until the guest carries an IPv4 address on the
// interface behind a device, which is what tells a boot that has finished
// configuring the interface from one still doing it.
func (d *Incus) waitForGuestInterface(ctx context.Context, machine, device string) error {
	poll := d.routePoll
	if poll <= 0 {
		poll = guestRoutePoll
	}
	budget := d.routeBudget
	if budget <= 0 {
		budget = guestRouteWait
	}
	deadline := time.Now().Add(budget)
	var last error
	for {
		iface, err := d.guestInterface(ctx, machine, device)
		if err == nil {
			var carried map[string]bool
			carried, err = d.guestAddresses(ctx, machine, iface)
			if err == nil && len(carried) > 0 {
				return nil
			}
		}
		last = err
		if time.Now().After(deadline) {
			if last == nil {
				last = fmt.Errorf("it carries no IPv4 address")
			}
			return fmt.Errorf("wait for %s/%s to be configured inside the guest: %w", machine, device, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// installGuestPrivateRoutes points the guest's private traffic at the OVN
// router. "file exists" is a previous call's work still standing.
func (d *Incus) installGuestPrivateRoutes(ctx context.Context, machine, network, device string) error {
	gateway, err := d.networkGateway(ctx, network)
	if err != nil {
		return err
	}
	for _, block := range privateAggregates {
		if _, err := d.run(ctx, "exec", machine, "--",
			"ip", "route", "add", block, "via", gateway.Addr().String(), "dev", device); err != nil &&
			!strings.Contains(strings.ToLower(err.Error()), "file exists") {
			return fmt.Errorf("route %s of %s via %s: %w", block, machine, gateway.Addr(), err)
		}
	}
	return nil
}

// appendRoute and removeRoute edit a comma-separated route list, preserving
// the entries they are not about.
func appendRoute(routes, route string) string {
	kept := removeRoute(routes, route)
	if kept == "" {
		return route
	}
	return kept + "," + route
}

func removeRoute(routes, route string) string {
	kept := make([]string, 0, 4)
	for _, existing := range strings.Split(routes, ",") {
		existing = strings.TrimSpace(existing)
		if existing != "" && existing != route {
			kept = append(kept, existing)
		}
	}
	return strings.Join(kept, ",")
}

// setUplinkRoute adds or removes one /32 from the uplink's ipv4.routes, which
// is what makes the host route the address towards OVN. It takes uplinkMu
// itself: its callers are address attachments, which hold no uplink lock of
// their own.
func (d *Incus) setUplinkRoute(ctx context.Context, address string, add bool) error {
	d.uplinkMu.Lock()
	defer d.uplinkMu.Unlock()
	if add {
		if err := d.addUplinkRoute(ctx, address+"/32"); err != nil {
			return fmt.Errorf("set routes of uplink %s: %w", d.uplinkName(), err)
		}
		return nil
	}
	return d.dropUplinkRoute(ctx, address+"/32")
}

// addUplinkRoute puts one entry on the uplink's ipv4.routes, leaving the rest
// alone; an entry already present is left as it is, so nothing is rebuilt for
// nothing. The caller holds uplinkMu.
func (d *Incus) addUplinkRoute(ctx context.Context, route string) error {
	return d.addUplinkRoutes(ctx, []string{route})
}

// addUplinkRoutes is addUplinkRoute for a set: one read, and at most one
// write, whatever the set's size — a write is what makes the daemon rebuild
// the uplink's firewall, so entries already present cause none. The caller
// holds uplinkMu.
func (d *Incus) addUplinkRoutes(ctx context.Context, routes []string) error {
	name := d.uplinkName()
	out, err := d.run(ctx, "network", "get", name, "ipv4.routes")
	if err != nil {
		return fmt.Errorf("read routes on uplink %s: %w", name, err)
	}
	merged := strings.TrimSpace(string(out))
	changed := false
	for _, route := range routes {
		if routeListContains(merged, route) {
			continue
		}
		merged = appendRoute(merged, route)
		changed = true
	}
	if !changed {
		return nil
	}
	if _, err := d.run(ctx, "network", "set", name, "ipv4.routes="+merged); err != nil {
		return err
	}
	return nil
}

// dropUplinkRoute takes one entry off the uplink's ipv4.routes. The caller
// holds uplinkMu.
func (d *Incus) dropUplinkRoute(ctx context.Context, route string) error {
	if route == "" {
		return nil
	}
	return d.dropUplinkRoutes(ctx, []string{route})
}

// dropUplinkRoutes is dropUplinkRoute for a set: one read, and at most one
// write, whatever the set's size — the mirror of addUplinkRoutes (#519). The
// caller holds uplinkMu. An uplink already gone, or an entry already absent,
// is the outcome asked for — and skipping the write in the absent case
// matters, since every write makes the runtime clear and rebuild the uplink's
// firewall.
func (d *Incus) dropUplinkRoutes(ctx context.Context, routes []string) error {
	name := d.uplinkName()
	out, err := d.run(ctx, "network", "get", name, "ipv4.routes")
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("read routes of uplink %s: %w", name, err)
	}
	kept := strings.TrimSpace(string(out))
	changed := false
	for _, route := range routes {
		if !routeListContains(kept, route) {
			continue
		}
		kept = removeRoute(kept, route)
		changed = true
	}
	if !changed {
		return nil
	}
	if _, err := d.run(ctx, "network", "set", name, "ipv4.routes="+kept); err != nil {
		return fmt.Errorf("set routes of uplink %s: %w", name, err)
	}
	return nil
}

// releaseUplink is the uplink half of ReleasePlumbing: the graceful exit takes
// the object no client's delete will ever remove (#521). Two green conformance
// runs each left `feint-uplink` standing — every resource had been deleted by
// the clients that made it, the closing `feint stop` pruned nothing, and the
// next run's own doorstep refused the host on exactly that network.
//
// Both questions, per the rule that well formed is not owned. The label says
// an emulator made it; the holder pid says *this process* is the one whose
// plumbing it is. An uplink a dead run left is not released here — unless this
// process adopted it, in which case the holder is this pid — and an operator's
// bridge under the uplink's name is never touched, which is ensureUplink's
// refusal replayed on the way out.
//
// An uplink that networks still draw from stays, silently: those networks are
// this run's leftovers, the doorstep and the sweep name them, and a release
// that hid them behind a forced teardown would be the sweep this path
// deliberately is not. It is also why ReleasePlumbing gives the default machine
// network back first — that network is what kept this one standing on the leg
// of 2026-08-28.
// TestAShutdownReleaseTakesTheUnusedUplinkOfThisProcess fails without the
// release; TestAReleaseNeverTouchesAnUplinkThisProcessDoesNotHold holds the
// refusing half.
func (d *Incus) releaseUplink(ctx context.Context) (bool, error) {
	if !d.OVN {
		return false, nil
	}
	d.uplinkMu.Lock()
	defer d.uplinkMu.Unlock()
	name := d.uplinkName()
	out, err := d.run(ctx, "query", "/1.0/networks/"+name)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect uplink %s: %w", name, err)
	}
	var existing struct {
		Type   string            `json:"type"`
		Config map[string]string `json:"config"`
	}
	if err := json.Unmarshal(out, &existing); err != nil {
		return false, fmt.Errorf("decode uplink %s: %w", name, err)
	}
	if existing.Type != "bridge" || existing.Config["user."+LabelKey] == "" {
		return false, nil
	}
	if existing.Config["user."+UplinkHolderKey] != strconv.Itoa(os.Getpid()) {
		return false, nil
	}
	if _, err := d.run(ctx, "network", "delete", name); err != nil {
		if isNotFound(err) {
			return false, nil
		}
		if strings.Contains(strings.ToLower(err.Error()), "in use") {
			return false, nil
		}
		return false, fmt.Errorf("release uplink %s: %w", name, err)
	}
	return true, nil
}
