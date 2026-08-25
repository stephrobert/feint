package machine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The states a sweep cannot get out of, and the one repair that reaches past
// the runtime's own commands (#455).
//
// Every assertion here is about an argument the driver emits or refuses to
// emit, which is the only level at which "it will not touch somebody else's
// peering row" can be checked without a runtime — and it is the level that
// matters, because what this repair reaches is Incus' own database.

// trapWorld is a host holding one network of the emulator's and one of a third
// party's, each with a peering row whose target no longer exists.
//
// The two are indistinguishable by shape and by state: same table, same kind of
// row, same dangling target. The *only* thing that tells them apart is the
// label the emulator wrote on the network the row belongs to — which is exactly
// the discrimination this feature stands or falls on.
func trapWorld() map[string]string {
	return map[string]string{
		"/1.0/networks?recursion=1": `[
		  {"name": "fnt-c10fedc7f6c", "project": "default", "type": "ovn",
		   "config": {"user.feint.provider": "outscale", "network": "feint-uplink",
		              "ipv4.address": "10.2.4.1/24", "security.acls": "iso-fnt-c10fedc7f6c"}},
		  {"name": "feint-uplink", "project": "default", "type": "bridge",
		   "config": {"user.feint.provider": "feint", "ipv4.routes": "10.2.4.0/24"}},
		  {"name": "hp-test-net", "project": "default", "type": "ovn",
		   "config": {"ipv4.address": "192.168.40.1/24"}}
		]`,
		"network get feint-uplink ipv4.routes": "10.2.4.0/24\n",
		// The peering rows, as `incus admin sql global --format json` answers
		// them: numbers as numbers, an unresolved target as a plain id, and the
		// name and project of a target that is gone as null.
		"SELECT id, network_id": `{"type":"select",
		  "columns":["id","network_id","name","target_network_id","target_network_project","target_network_name","description","type"],
		  "rows":[[401,2616,"fnt-e41278b8c3a",2617,null,null,"",0],
		          [777,284,"hp-peer",2618,null,null,"",0]],
		  "rows_affected":0}`,
		// 2617 and 2618 are absent: both rows dangle.
		"SELECT id FROM networks": `{"type":"select","columns":["id"],
		  "rows":[[2616],[284],[1]],"rows_affected":0}`,
		"FROM networks, projects": `{"type":"select","columns":["id","name","name"],
		  "rows":[[2616,"default","fnt-c10fedc7f6c"],[284,"default","hp-test-net"],[1,"default","incusbr0"]],
		  "rows_affected":0}`,
	}
}

// TestForceLeavesAThirdPartysDanglingPeerAlone is the test this whole feature
// hangs on, and it is the one to read first.
//
// A dangling peering row is not rare and not the emulator's speciality: any
// tool that deletes an OVN network out from under a peering leaves one, and an
// operator's own row looks exactly like ours. "Well formed is not authorised" —
// the row belonging to hp-test-net is as dangling as ours and as reachable by
// the same statement, and the only thing that may stop the delete is the label.
//
// Without the ownership filter this is `rm -rf` with a friendly name.
func TestForceLeavesAThirdPartysDanglingPeerAlone(t *testing.T) {
	f := &fakeRuntime{answers: trapWorld()}
	d := ovnDriver(f)

	cleared, err := d.Repair(context.Background())
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	for _, cmd := range f.commands() {
		if strings.Contains(cmd, "id=777") {
			t.Errorf("the repair removed a peering row of a network nobody here created: %q", cmd)
		}
	}
	for _, trap := range cleared {
		if strings.Contains(trap.Name, "hp-") {
			t.Errorf("the repair reported clearing a third party's row: %+v", trap)
		}
	}
	// And the accepting half in the same fixture, so a repair that refuses
	// everything cannot pass this test: ours must have gone.
	if len(cleared) != 1 {
		t.Fatalf("cleared %d row(s), want exactly ours", len(cleared))
	}
}

// TestForceRemovesADanglingPeerOfOurOwn is the other half: a guard that refuses
// everything passes every attack test and ships nothing.
func TestForceRemovesADanglingPeerOfOurOwn(t *testing.T) {
	f := &fakeRuntime{answers: trapWorld()}
	d := ovnDriver(f)

	cleared, err := d.Repair(context.Background())
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if len(f.matching("DELETE FROM networks_peers WHERE id=401")) != 1 {
		t.Fatalf("the dangling row of our own network was not removed; the driver ran:\n%s",
			strings.Join(f.commands(), "\n"))
	}
	if len(cleared) != 1 || cleared[0].Kind != TrapDanglingPeer {
		t.Fatalf("cleared %+v, want one dangling peer", cleared)
	}
	// The row is printed whole, so an operator can put it back. A repair on the
	// runtime's own database that cannot be reversed by the person who ran it is
	// not a repair, it is a bet.
	if !strings.Contains(cleared[0].Row, "2617") {
		t.Errorf("the removed row does not carry what it held: %q", cleared[0].Row)
	}
}

// A repair reaches Incus' database, so it goes through `incus admin sql`, which
// is the runtime's own supported mechanism for it — never a file under
// /var/lib/incus, which would be an edit behind the daemon's back.
func TestTheRepairGoesThroughTheRuntimesOwnMechanism(t *testing.T) {
	f := &fakeRuntime{answers: trapWorld()}
	d := ovnDriver(f)

	if _, err := d.Repair(context.Background()); err != nil {
		t.Fatalf("repair: %v", err)
	}
	for _, call := range f.calls {
		if len(call) > 0 && strings.Contains(strings.Join(call, " "), "DELETE FROM") {
			if call[0] != "admin" || call[1] != "sql" || call[2] != "global" {
				t.Errorf("the repair wrote to the database by another route: %v", call)
			}
		}
	}
}

// Traps names the dangling row of our own network and says what it blocks.
func TestTrapsNameADanglingPeerRowOfOurOwn(t *testing.T) {
	f := &fakeRuntime{answers: trapWorld()}
	d := ovnDriver(f)

	traps, err := d.Traps(context.Background())
	if err != nil {
		t.Fatalf("traps: %v", err)
	}
	var found bool
	for _, trap := range traps {
		if trap.Kind == TrapDanglingPeer {
			found = true
			if !strings.Contains(trap.Name, "fnt-c10fedc7f6c") {
				t.Errorf("the trap does not name the network it holds: %+v", trap)
			}
			if !trap.Repairable {
				t.Errorf("a dangling row was reported as beyond repair: %+v", trap)
			}
		}
		if strings.Contains(trap.Name, "hp-") {
			t.Errorf("a third party's row was reported as this emulator's trap: %+v", trap)
		}
	}
	if !found {
		t.Fatalf("no dangling peering row reported, got %+v", traps)
	}
	// And it must have issued no mutating command: naming is the whole contract
	// of a check, exactly as it is for Survey.
	for _, call := range f.calls {
		switch call[0] {
		case "query", "network", "admin":
			if call[0] == "network" && call[1] != "get" {
				t.Errorf("the trap survey issued a mutating command: %v", call)
			}
			if call[0] == "admin" && strings.Contains(strings.Join(call, " "), "DELETE") {
				t.Errorf("the trap survey wrote to the database: %v", call)
			}
		default:
			t.Errorf("the trap survey issued an unexpected command: %v", call)
		}
	}
}

// A network of the emulator's whose block the uplink no longer carries: the
// state a sweep used to create itself, and the one that makes every management
// path of that network fail validation.
func TestTrapsNameAStrippedUplink(t *testing.T) {
	world := trapWorld()
	// The uplink after the sweep unset its routes.
	world["network get feint-uplink ipv4.routes"] = "\n"
	f := &fakeRuntime{answers: world}
	d := ovnDriver(f)

	traps, err := d.Traps(context.Background())
	if err != nil {
		t.Fatalf("traps: %v", err)
	}
	var stripped *Trap
	for i := range traps {
		if traps[i].Kind == TrapStrippedUplink {
			stripped = &traps[i]
		}
	}
	if stripped == nil {
		t.Fatalf("a network whose block the uplink lost was not reported: %+v", traps)
	}
	if !strings.Contains(stripped.Why, "10.2.4.0/24") {
		t.Errorf("the report does not name the block that is missing: %+v", stripped)
	}
	if stripped.Repairable {
		t.Errorf("the stripped uplink is repaired by the ordinary sweep and must not ask for --force: %+v", stripped)
	}
}

// The rule set that cannot be detached because the network holding it is
// trapped. Reported only in that case — see the accepting half below, which is
// what stops this from firing on every healthy run.
func TestTrapsNameARuleSetHeldByATrappedNetwork(t *testing.T) {
	f := &fakeRuntime{answers: trapWorld()}
	d := ovnDriver(f)

	traps, err := d.Traps(context.Background())
	if err != nil {
		t.Fatalf("traps: %v", err)
	}
	for _, trap := range traps {
		if trap.Kind == TrapHeldFirewall && trap.Name == "iso-fnt-c10fedc7f6c" {
			return
		}
	}
	t.Fatalf("the rule set held by a trapped network was not reported: %+v", traps)
}

// TestTrapsAreSilentOnAHealthyOVNRun is the accepting half, and it is the half
// that decides whether this check survives contact with a run.
//
// The obvious formulation of the third state — "a rule set is attached to a
// network" — is the *normal* state of every isolated OVN network: IsolateNetwork
// ends by running `network set <network> security.acls <name>` on every network
// with a neighbour to keep out. A check that reported that would refuse every
// healthy run, which is precisely how #426's doorstep learned to fire on hosts
// nothing was going to fail on, and how a doorstep gets disarmed.
//
// So the fixture here is a run in mid-flight: two peered networks, both with
// their rule set attached, both blocks delegated, no dangling row. Nothing is
// reported.
func TestTrapsAreSilentOnAHealthyOVNRun(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/networks?recursion=1": `[
		  {"name": "fnt-aaaa", "project": "default", "type": "ovn",
		   "config": {"user.feint.provider": "scaleway", "network": "feint-uplink",
		              "ipv4.address": "10.2.4.1/24", "security.acls": "iso-fnt-aaaa"}},
		  {"name": "fnt-bbbb", "project": "default", "type": "ovn",
		   "config": {"user.feint.provider": "scaleway", "network": "feint-uplink",
		              "ipv4.address": "10.2.5.1/24", "security.acls": "iso-fnt-bbbb"}},
		  {"name": "feint-uplink", "project": "default", "type": "bridge",
		   "config": {"user.feint.provider": "feint", "ipv4.routes": "10.2.4.0/24,10.2.5.0/24"}}
		]`,
		"network get feint-uplink ipv4.routes": "10.2.4.0/24,10.2.5.0/24\n",
		"SELECT id, network_id": `{"type":"select",
		  "columns":["id","network_id","name","target_network_id","target_network_project","target_network_name","description","type"],
		  "rows":[[10,100,"fnt-bbbb",101,"default","fnt-bbbb","",0],
		          [11,101,"fnt-aaaa",100,"default","fnt-aaaa","",0]],
		  "rows_affected":0}`,
		"SELECT id FROM networks": `{"type":"select","columns":["id"],"rows":[[100],[101],[1]],"rows_affected":0}`,
		"FROM networks, projects": `{"type":"select","columns":["id","name","name"],
		  "rows":[[100,"default","fnt-aaaa"],[101,"default","fnt-bbbb"]],"rows_affected":0}`,
	}}
	d := ovnDriver(f)

	traps, err := d.Traps(context.Background())
	if err != nil {
		t.Fatalf("traps: %v", err)
	}
	if len(traps) != 0 {
		t.Fatalf("a healthy run was reported as trapped, which fails every run that follows: %+v", traps)
	}
}

// A database that cannot be read is not an empty one. "I could not look" and
// "there is nothing" are different facts, and reporting the first as the second
// is how an inventory once called a live account empty.
func TestATrapSurveyThatCannotReadSaysSo(t *testing.T) {
	f := &fakeRuntime{answers: trapWorld(), fail: map[string]error{
		"SELECT id, network_id": errors.New("Error: not authorised"),
	}}
	d := ovnDriver(f)

	if _, err := d.Traps(context.Background()); err == nil {
		t.Fatal("a database this process may not read was reported as a host holding nothing")
	}
}

// TestAShortDatabaseAnswerIsInertRatherThanFatal holds the bound on the row the
// repair prints.
//
// Every other reader in this file asks the length before it indexes; the copy
// of the row kept for the operator did not, and reached row[7] directly. The
// answer it indexes comes from another process over JSON — another Incus, a
// column renamed, a table this one does not carry — so a shorter row than the
// SELECT names panicked the one command somebody runs to get a station out of
// trouble. Inert is the right answer: a row whose target cannot even be read
// names nothing that resolves to nothing, so there is nothing to remove.
func TestAShortDatabaseAnswerIsInertRatherThanFatal(t *testing.T) {
	world := trapWorld()
	world["SELECT id, network_id"] = `{"type":"select","columns":["id","network_id"],
	  "rows":[[401,2616],[777,284]],"rows_affected":0}`
	f := &fakeRuntime{answers: world}
	d := ovnDriver(f)

	traps, err := d.Traps(context.Background())
	if err != nil {
		t.Fatalf("traps: %v", err)
	}
	for _, trap := range traps {
		if trap.Kind == TrapDanglingPeer {
			t.Errorf("a row whose target could not be read was called dangling: %+v", trap)
		}
	}
	cleared, err := d.Repair(context.Background())
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if len(cleared) != 0 {
		t.Errorf("the repair removed %d row(s) it could not even read: %+v", len(cleared), cleared)
	}
	for _, cmd := range f.commands() {
		if strings.Contains(cmd, "DELETE FROM") {
			t.Errorf("the repair wrote to the database on an answer it could not read: %q", cmd)
		}
	}
}
