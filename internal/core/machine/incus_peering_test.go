package machine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The ordering discipline that stops a sweep from trapping its own host (#455).
//
// What was measured. Fifteen third-party stacks replayed under --vm incus-ovn
// left two OVN networks, two rule sets and the uplink on the maintainer's
// station, and *no* incus command could remove any of them. The eraser was not
// the next run: it was the sweep, three times over.
//
//  1. Prune treated feint-uplink as an ordinary network — it carries the same
//     label — and unset its ipv4.routes before a delete that then failed. From
//     that instant every management path of the OVN networks drawing from it
//     answered "Uplink network doesn't contain 10.2.4.0/24 in its routes".
//  2. Nothing detached security.acls before deleting a network, so the rule set
//     and the network held each other with no exit.
//  3. Nothing removed the half a peering leaves on its surviving target, so a
//     network deleted while a neighbour still pointed at it left that neighbour
//     holding an id resolving to nothing — permanently unmanageable, because the
//     runtime's schema cascades two of that table's three references and not
//     the one to the target.

// peeredWorld is one OVN network of the emulator's, peered with another of its
// own and with a network an operator created, sitting on the uplink.
func peeredWorld() map[string]string {
	world := pruneWorld()
	world["/1.0/networks?recursion=1"] = `[
	  {"name": "fnt-aaaa", "project": "default", "type": "ovn",
	   "config": {"user.feint.provider": "scaleway", "network": "feint-uplink",
	              "ipv4.address": "10.2.4.1/24", "security.acls": "iso-fnt-aaaa"}},
	  {"name": "feint-uplink", "project": "default", "type": "bridge",
	   "config": {"user.feint.provider": "feint", "ipv4.routes": "10.2.4.0/24"}},
	  {"name": "incusbr0", "project": "default", "type": "bridge", "config": {}}
	]`
	world["/1.0/networks/fnt-aaaa/peers?recursion=1"] = `[
	  {"name": "fnt-bbbb", "target_network": "fnt-bbbb"},
	  {"name": "incusbr0", "target_network": "incusbr0"}
	]`
	world["network get feint-uplink ipv4.routes"] = "10.2.4.0/24\n"
	world["network get fnt-aaaa security.acls"] = "iso-fnt-aaaa\n"
	// The label on the far end of the peering. Stated rather than defaulted,
	// because whether the target carries it is the whole question ownsPeerTarget
	// asks, and a fixture that answered "ours" for every name would make the
	// refusal below unmeasurable.
	world["network get fnt-bbbb user."] = "scaleway\n"
	return world
}

// TestPruneNeverStripsTheUplinkRoutesWhileItsNetworksStand is the first of the
// three, and the one that made the residue permanent rather than merely untidy.
//
// The fixture is the measured shape: an OVN network that refuses to go, and the
// uplink it draws from. Before the fix the sweep unset the uplink's routes on
// its way past, the delete of the uplink then failed on "in use", and the
// orphan could no longer be edited by anything at all.
func TestPruneNeverStripsTheUplinkRoutesWhileItsNetworksStand(t *testing.T) {
	f := &fakeRuntime{answers: peeredWorld(), fail: map[string]error{
		"network delete fnt-aaaa": errors.New("Error: The network is currently in use"),
	}}
	d := ovnDriver(f)

	if _, err := d.Prune(context.Background()); err == nil {
		t.Fatal("a network the runtime refused to delete was reported as swept")
	}
	for _, cmd := range f.commands() {
		if strings.Contains(cmd, "unset feint-uplink ipv4.routes") {
			t.Errorf("the sweep stripped the uplink's routes: %q\n"+
				"every management path of the networks still drawing from it now fails validation", cmd)
		}
	}
	// And the accepting half: a rule set detached on the way to a delete that
	// then failed must go back on, or the sweep has disarmed a firewall on a
	// network which is still standing.
	if len(f.matching("network set fnt-aaaa security.acls iso-fnt-aaaa")) == 0 {
		t.Errorf("the rule set was detached and never put back after the delete failed; the driver ran:\n%s",
			strings.Join(f.commands(), "\n"))
	}
}

// The other direction: with nothing left drawing from it, the uplink is swept
// like everything else. A sweep that spared it would leave a bridge holding a
// block on every station that ever ran --vm incus-ovn.
func TestPruneStillRemovesTheUplinkOnceItsNetworksAreGone(t *testing.T) {
	f := &fakeRuntime{answers: peeredWorld()}
	d := ovnDriver(f)

	pruned, err := d.Prune(context.Background())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(f.matching("network delete feint-uplink")) != 1 {
		t.Fatalf("the uplink was never removed; the driver ran:\n%s", strings.Join(f.commands(), "\n"))
	}
	if pruned.Networks != 2 {
		t.Errorf("swept %d network(s), want the OVN network and the uplink", pruned.Networks)
	}
	// Last, after the network drawing from it: the other order is the one Incus
	// refuses, and it is what put the sweep in the state above.
	commands := f.commands()
	var deletedNetwork, deletedUplink int
	for i, cmd := range commands {
		switch {
		case strings.Contains(cmd, "network delete fnt-aaaa"):
			deletedNetwork = i
		case strings.Contains(cmd, "network delete feint-uplink"):
			deletedUplink = i
		}
	}
	if deletedUplink < deletedNetwork {
		t.Errorf("the uplink went before the network drawing from it:\n%s", strings.Join(commands, "\n"))
	}
}

// TestPruneDropsTheSurvivingHalfOfEveryPeering is the third lock, and the one
// that manufactures the state nothing can repair.
func TestPruneDropsTheSurvivingHalfOfEveryPeering(t *testing.T) {
	f := &fakeRuntime{answers: peeredWorld()}
	d := ovnDriver(f)

	if _, err := d.Prune(context.Background()); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(f.matching("network peer delete fnt-bbbb fnt-aaaa")) == 0 {
		t.Fatalf("the half of the peering living on the surviving target was left behind; the driver ran:\n%s\n"+
			"that row holds an id, so recreating the network under the same name does not satisfy it",
			strings.Join(f.commands(), "\n"))
	}
}

// TestPruneRefusesToTouchAPeeringHalfOnAForeignNetwork is the guard on the new
// command, and it is needed because the target's name comes out of the runtime
// and becomes an argument aimed at *another* network.
//
// `incusbr0` is well formed, and that was the whole problem the last time this
// distinction was missed: an audit obtained `incus network delete incusbr0`
// from a name that passed every syntactic check there was.
func TestPruneRefusesToTouchAPeeringHalfOnAForeignNetwork(t *testing.T) {
	f := &fakeRuntime{answers: peeredWorld()}
	d := ovnDriver(f)

	if _, err := d.Prune(context.Background()); err != nil {
		t.Fatalf("prune: %v", err)
	}
	for _, cmd := range f.commands() {
		if strings.Contains(cmd, "peer delete incusbr0") {
			t.Errorf("the sweep reached into a peering on the host's own bridge: %q", cmd)
		}
	}
}

// The rule set and its network hold each other, and detaching is the only order
// Incus accepts. Before this, the sweep just retried `acl delete` twice and
// reported both stuck.
func TestPruneDetachesTheRuleSetHoldingANetwork(t *testing.T) {
	f := &fakeRuntime{answers: peeredWorld()}
	d := ovnDriver(f)

	if _, err := d.Prune(context.Background()); err != nil {
		t.Fatalf("prune: %v", err)
	}
	detach := -1
	remove := -1
	for i, cmd := range f.commands() {
		switch {
		case strings.Contains(cmd, "network unset fnt-aaaa security.acls"):
			detach = i
		case strings.Contains(cmd, "network delete fnt-aaaa"):
			remove = i
		}
	}
	if detach < 0 {
		t.Fatalf("the rule set was never detached; the driver ran:\n%s", strings.Join(f.commands(), "\n"))
	}
	if remove < detach {
		t.Errorf("the delete came before the detach, which is the order Incus refuses:\n%s",
			strings.Join(f.commands(), "\n"))
	}
}

// A host a previous sweep already stripped is repaired by the next one, with no
// privilege at all: the block goes back on the uplink before anything is asked
// of the network carrying it. This is the manual step that measurably freed one
// of the two stuck rule sets on the maintainer's station.
func TestPruneRestoresABlockTheUplinkLost(t *testing.T) {
	world := peeredWorld()
	world["network get feint-uplink ipv4.routes"] = "\n"
	f := &fakeRuntime{answers: world}
	d := ovnDriver(f)

	if _, err := d.Prune(context.Background()); err != nil {
		t.Fatalf("prune: %v", err)
	}
	restore := -1
	detach := -1
	for i, cmd := range f.commands() {
		switch {
		case strings.Contains(cmd, "network set feint-uplink ipv4.routes=10.2.4.0/24"):
			restore = i
		case strings.Contains(cmd, "network unset fnt-aaaa security.acls"):
			detach = i
		}
	}
	if restore < 0 {
		t.Fatalf("the sweep never put the missing block back, so every edit of fnt-aaaa still fails:\n%s",
			strings.Join(f.commands(), "\n"))
	}
	if detach >= 0 && detach < restore {
		t.Errorf("the detach was attempted before the block came back, which is the call that fails:\n%s",
			strings.Join(f.commands(), "\n"))
	}
}

// TestRemoveNetworkDeletesTheSurvivingHalfOfItsPeerings is the same third lock
// on the ordinary teardown path, where the client asks for the delete.
//
// Before the delete rather than after a refusal: a delete that *succeeds* while
// a neighbour still points here is exactly what leaves the neighbour holding an
// id that resolves to nothing.
func TestRemoveNetworkDeletesTheSurvivingHalfOfItsPeerings(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/networks/fnt-aaaa/peers?recursion=1": `[
		  {"name": "fnt-bbbb", "target_network": "fnt-bbbb"},
		  {"name": "incusbr0", "target_network": "incusbr0"}
		]`,
		"network get fnt-aaaa ipv4.address": "10.2.4.1/24\n",
		"network get fnt-bbbb user.":        "scaleway\n",
	}}
	d := ovnDriver(f)

	if err := d.RemoveNetwork(context.Background(), "fnt-aaaa"); err != nil {
		t.Fatalf("remove network: %v", err)
	}
	drop := -1
	remove := -1
	for i, cmd := range f.commands() {
		switch {
		case strings.Contains(cmd, "network peer delete fnt-bbbb fnt-aaaa"):
			drop = i
		case strings.Contains(cmd, "network delete fnt-aaaa"):
			remove = i
		}
	}
	if drop < 0 {
		t.Fatalf("the half on the surviving target was left holding an id that no longer resolves:\n%s",
			strings.Join(f.commands(), "\n"))
	}
	if remove < drop {
		t.Errorf("the network went before the half pointing at it, which is what makes the row dangle:\n%s",
			strings.Join(f.commands(), "\n"))
	}
	// The same guard as the sweep: an operator's bridge on the far end of a
	// peering is not this code's to reconfigure.
	for _, cmd := range f.commands() {
		if strings.Contains(cmd, "peer delete incusbr0") {
			t.Errorf("the teardown reached into a peering on the host's own bridge: %q", cmd)
		}
	}
}

// And the rule set half of the same path, with the restore that keeps a network
// which survives from losing its isolation in silence.
func TestRemoveNetworkDetachesTheRuleSetThatHoldsIt(t *testing.T) {
	f := &fakeRuntime{
		answers: map[string]string{
			"network get fnt-aaaa security.acls": "iso-fnt-aaaa\n",
			"network get fnt-aaaa ipv4.address":  "10.2.4.1/24\n",
		},
		fail: map[string]error{
			"network delete fnt-aaaa": errors.New("Error: The network is currently in use"),
		},
	}
	d := newFakeDriver(f)

	if err := d.RemoveNetwork(context.Background(), "fnt-aaaa"); err == nil {
		t.Fatal("a network the runtime refuses to delete was reported as removed")
	}
	if len(f.matching("network unset fnt-aaaa security.acls")) == 0 {
		t.Fatalf("the rule set holding the network was never detached; the driver ran:\n%s",
			strings.Join(f.commands(), "\n"))
	}
	// The accepting half, and the one that matters most here: the delete still
	// failed, so the network is standing, and a standing network whose rule set
	// this code silently took off is a firewall disarmed.
	if len(f.matching("network set fnt-aaaa security.acls iso-fnt-aaaa")) == 0 {
		t.Errorf("a network that survived the delete was left with its isolation off:\n%s",
			strings.Join(f.commands(), "\n"))
	}
}

// TestPruneRefusesAPeeringHalfOnAStrangersNetworkNamedLikeOurs is the guard one
// level below the one above, and it is the one a name check cannot answer.
//
// `incusbr0` is refused by the prefix. `fnt-lab` is not: it is well formed, it
// carries the prefix NetworkName writes, and it belongs to somebody else — an
// operator who created an OVN network under that name and peered it with one of
// ours by hand. "fnt-" is a prefix anybody may type, which is the maintainer's
// own sentence about the database repair of #455, and this reach is of the same
// family: a destructive command aimed at a network that is not the one being
// removed.
//
// So the label EnsureNetwork wrote decides, and nothing else can.
func TestPruneRefusesAPeeringHalfOnAStrangersNetworkNamedLikeOurs(t *testing.T) {
	world := peeredWorld()
	world["/1.0/networks/fnt-aaaa/peers?recursion=1"] = `[
	  {"name": "fnt-lab", "target_network": "fnt-lab"}
	]`
	// The stranger's network answers the ownership probe with nothing: it was
	// never created by this emulator, so it carries no label of ours.
	world["network get fnt-lab user."] = ""
	f := &fakeRuntime{answers: world}
	d := ovnDriver(f)

	if _, err := d.Prune(context.Background()); err != nil {
		t.Fatalf("prune: %v", err)
	}
	for _, cmd := range f.commands() {
		if strings.Contains(cmd, "peer delete fnt-lab") {
			t.Errorf("the sweep reconfigured a peering on a network nobody here created: %q\n"+
				"the name carries our prefix and the network is not ours; only the label says so", cmd)
		}
	}
	// The accepting half stays measured in the same run: our own side of that
	// peering is still withdrawn, or a network of ours would keep a half nothing
	// ever removes.
	if len(f.matching("network peer delete fnt-aaaa fnt-lab")) == 0 {
		t.Errorf("our own half of the peering was left behind; the driver ran:\n%s",
			strings.Join(f.commands(), "\n"))
	}
}

// The permissive posture set belongs to no resource, so nothing's delete ever
// dropped it: one full `feint up`/`feint down` cycle left `opn-fnt` on the
// host, used_by=0, beside the operator's own rule sets (measured 2026-08-26).
// A network delete now drops it opportunistically — and "in use" is not an
// error, because machines of another network may still wear it.
func TestANetworkDeleteTakesTheUnusedPermissiveSetWithIt(t *testing.T) {
	f := &fakeRuntime{
		answers: map[string]string{
			"network get fnt-aaaa ipv4.address": "10.2.4.1/24\n",
		},
	}
	d := newFakeDriver(f)

	if err := d.RemoveNetwork(context.Background(), "fnt-aaaa"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(f.matching("network acl delete opn-fnt")) == 0 {
		t.Fatalf("the permissive set was left behind; the driver ran:\n%s",
			strings.Join(f.commands(), "\n"))
	}
}

// The other half: a permissive set still worn by machines of another network
// must not fail the delete of this one.
func TestAPermissiveSetStillInUseDoesNotFailANetworkDelete(t *testing.T) {
	f := &fakeRuntime{
		answers: map[string]string{
			"network get fnt-aaaa ipv4.address": "10.2.4.1/24\n",
		},
		fail: map[string]error{
			"network acl delete opn-fnt": errors.New("Error: Cannot delete an ACL that is in use"),
		},
	}
	d := newFakeDriver(f)

	if err := d.RemoveNetwork(context.Background(), "fnt-aaaa"); err != nil {
		t.Fatalf("a network delete failed on the permissive set another machine still wears: %v", err)
	}
}
