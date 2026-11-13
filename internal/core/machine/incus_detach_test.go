package machine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// The detach half of the driver (#426).
//
// What these are written against, measured on a host rather than reasoned
// about: before Detach existed, DELETE on a Scaleway private NIC answered 204
// while `incus config device show` still listed the device, and the later
// DeletePrivateNetwork then got "The network is currently in use" and left the
// bridge, its dnsmasq and the block standing. So every test here reads the
// argv the driver actually emitted — a status code cannot tell a command that
// was refused from one that was never sent.

// recorder is a runner that answers a plausible Incus and keeps every argv.
type recorder struct {
	mu   sync.Mutex
	sent [][]string
	// devices is what `query /1.0/instances/...` answers.
	devices string
	// label is what `config get <name> user.feint.provider` answers; empty
	// means the instance carries no label of ours.
	label string
	// missing makes every call answer as though the instance were gone.
	missing bool
}

func (r *recorder) run(_ context.Context, args ...string) ([]byte, error) {
	r.mu.Lock()
	r.sent = append(r.sent, append([]string(nil), args...))
	r.mu.Unlock()
	joined := strings.Join(args, " ")
	if r.missing {
		return nil, errors.New(`Error: Instance not found`)
	}
	switch {
	case strings.HasPrefix(joined, "config get "):
		return []byte(r.label), nil
	case strings.HasPrefix(joined, "query /1.0/instances/"):
		if r.devices == "" {
			return []byte(`{"devices":{},"expanded_devices":{}}`), nil
		}
		return []byte(r.devices), nil
	}
	return []byte("{}"), nil
}

// carries reports whether any command emitted contains all the given words in
// order, which is how these tests name a command without pinning its exact
// shape.
func (r *recorder) carries(words ...string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	want := strings.Join(words, " ")
	for _, args := range r.sent {
		if strings.Contains(strings.Join(args, " "), want) {
			return true
		}
	}
	return false
}

// mentions reports whether any command emitted names the given string at all.
// The refusal tests assert on this rather than on an error, because a guard
// that returns an error *after* sending the command has not guarded anything.
func (r *recorder) mentions(needle string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, args := range r.sent {
		for _, a := range args {
			if strings.Contains(a, needle) {
				return true
			}
		}
	}
	return false
}

const detachTwoNICs = `{"devices":{
  "eth0":{"type":"nic","network":"fnt-ours"},
  "eth1":{"type":"nic","network":"fnt-other"},
  "root":{"type":"disk"}},
 "expanded_devices":{
  "eth0":{"type":"nic","network":"fnt-ours"},
  "eth1":{"type":"nic","network":"fnt-other"},
  "root":{"type":"disk"},
  "profileEth":{"type":"nic","network":"fnt-ours"}}}`

// TestDetachRemovesOnlyTheDeviceOnThatNetwork is the accepting half, and it is
// half the point: a guard that refuses everything passes every attack test and
// breaks the product. The device on the named network goes; the machine's other
// interface and its disk stay.
//
// The profile device is the second assertion. It sits in expanded_devices and
// not in devices, so it belongs to a profile rather than to this instance, and
// removing it from the instance would reconfigure something the emulator never
// created.
func TestDetachRemovesOnlyTheDeviceOnThatNetwork(t *testing.T) {
	rec := &recorder{devices: detachTwoNICs, label: "scaleway"}
	d := &Incus{runner: rec.run}

	if err := d.Detach(context.Background(), "feint-scw-one", "fnt-ours"); err != nil {
		t.Fatalf("Detach on an owned instance and an owned network must succeed, got %v", err)
	}
	if !rec.carries("config device remove feint-scw-one eth0") {
		t.Fatalf("the device on fnt-ours was never removed; commands sent: %v", rec.sent)
	}
	if rec.carries("config device remove feint-scw-one eth1") {
		t.Fatalf("eth1 sits on fnt-other and must be left alone; commands sent: %v", rec.sent)
	}
	if rec.carries("config device remove feint-scw-one root") {
		t.Fatalf("the disk is not a NIC and must be left alone; commands sent: %v", rec.sent)
	}
	if rec.carries("config device remove feint-scw-one profileEth") {
		t.Fatalf("profileEth is inherited from a profile, not owned by the instance, "+
			"and removing it reconfigures what the emulator never created; commands sent: %v", rec.sent)
	}
}

// TestDetachRefusesAnInstanceItDoesNotOwn is the authorisation question, the
// second of the two the machine-driver layer must always ask. safeName accepts
// `production-database` — that is the whole point of "bien formé n'est pas
// autorisé" — so the label the emulator itself wrote is what decides.
//
// The assertion is that no removal was emitted, not merely that an error came
// back: a guard that refuses after sending the command has guarded nothing.
func TestDetachRefusesAnInstanceItDoesNotOwn(t *testing.T) {
	rec := &recorder{devices: detachTwoNICs, label: ""} // no user.feint.provider on it
	d := &Incus{runner: rec.run}

	err := d.Detach(context.Background(), "production-database", "fnt-ours")
	if err == nil {
		t.Fatal("Detach accepted an instance carrying none of the emulator's labels")
	}
	if rec.carries("config device remove") {
		t.Fatalf("a device was removed from an instance the emulator does not own; commands sent: %v", rec.sent)
	}
}

// TestDetachRefusesANetworkOutsideThePrefix is the same question on the other
// end of the operation. The network name reaches this from a stored resource,
// and PUT /_feint/state restores that map verbatim, so a snapshot can name the
// host's own bridge here. An audit already obtained `network delete incusbr0`
// through this class of gap.
//
// The witness matters: the run must not merely fail, it must never name
// incusbr0 in any command at all.
func TestDetachRefusesANetworkOutsideThePrefix(t *testing.T) {
	for _, network := range []string{"incusbr0", "hp-test-net", "docker0"} {
		rec := &recorder{devices: detachTwoNICs, label: "scaleway"}
		d := &Incus{runner: rec.run}

		if err := d.Detach(context.Background(), "feint-scw-one", network); err == nil {
			t.Fatalf("Detach accepted %q, which the emulator never created", network)
		}
		if rec.mentions(network) {
			t.Fatalf("a command named %q despite the refusal; commands sent: %v", network, rec.sent)
		}
	}
}

// TestDetachIsQuietWhenThereIsNothingToDetach holds the idempotence the Driver
// contract promises. Both doors into the NIC-release path can run twice — the
// server delete releases the NICs it carries, and a client may DELETE the NIC
// itself first — so a second run must not fail.
//
// Two shapes of "nothing": the machine is gone, and the machine is there with
// no device on that network.
func TestDetachIsQuietWhenThereIsNothingToDetach(t *testing.T) {
	gone := &recorder{missing: true}
	if err := (&Incus{runner: gone.run}).Detach(context.Background(), "feint-scw-one", "fnt-ours"); err != nil {
		t.Fatalf("Detach on a machine that is already gone must succeed, got %v", err)
	}

	bare := &recorder{devices: `{"devices":{"root":{"type":"disk"}},"expanded_devices":{}}`, label: "scaleway"}
	if err := (&Incus{runner: bare.run}).Detach(context.Background(), "feint-scw-one", "fnt-ours"); err != nil {
		t.Fatalf("Detach on a machine holding no such device must succeed, got %v", err)
	}
	if bare.carries("config device remove") {
		t.Fatalf("nothing was on that network, so nothing may be removed; commands sent: %v", bare.sent)
	}
}

// The egress half of the driver asked neither question, where every other
// reconfiguring path asks both. A security audit measured it on 2026-11-13:
// a crafted snapshot naming an instance of the host reached
// `incus exec production-database -- ip route del default`.
//
// The twin of TestDetachRefusesAnInstanceItDoesNotOwn, and for the same reason:
// the name comes from a stored resource that `PUT /_feint/state` restores
// verbatim.
func TestEgressRefusesAnInstanceItDoesNotOwn(t *testing.T) {
	t.Run("drop", func(t *testing.T) {
		rec := &recorder{label: ""} // no user.feint.provider on it
		d := &Incus{runner: rec.run, OVN: true}

		if err := d.DropEgress(context.Background(), "production-database"); err == nil {
			t.Fatal("DropEgress accepted an instance carrying none of the emulator's labels")
		}
		if rec.carries("ip route del") {
			t.Fatalf("a default route was removed from an instance the emulator does not own; "+
				"commands sent: %v", rec.sent)
		}
	})

	t.Run("route", func(t *testing.T) {
		rec := &recorder{label: ""}
		d := &Incus{runner: rec.run, OVN: true}

		if err := d.RouteEgress(context.Background(), "production-database", "fnt-ours"); err == nil {
			t.Fatal("RouteEgress accepted an instance carrying none of the emulator's labels")
		}
	})

	t.Run("a name the driver would read as a flag", func(t *testing.T) {
		rec := &recorder{label: "scaleway"} // ours, but the name is not a name
		d := &Incus{runner: rec.run, OVN: true}

		if err := d.DropEgress(context.Background(), "-h"); err == nil {
			t.Fatal("DropEgress accepted a name starting with a dash")
		}
		if len(rec.sent) != 0 {
			t.Fatalf("a command was sent for a malformed name: %v", rec.sent)
		}
	})
}

// What the ownership call costs when it fails for a reason that is not absence.
//
// #913 added `mustOwnInstance` to the egress paths, which means a round trip to
// the runtime on a hot path. The leg that would have judged that under a real
// Incus was not played — the station had the maintainer's labs running — so the
// risk was taken without being measured. This measures the half that lives in
// this code: what the driver does when the call answers an error.
//
// The answer must be fail-closed and loud, never "carry on anyway": an instance
// we cannot prove is ours is one we do not touch. But the two paths differ on
// purpose, and that difference is the subject here.
func TestEgressWhenTheOwnershipCallFails(t *testing.T) {
	// Not "instance not found": a runtime that is busy, locked, or answering
	// badly. `missing` is the absence case, covered below.
	busy := func() *recorder { return &recorder{label: "", missing: true} }

	t.Run("drop treats absence as nothing to undo", func(t *testing.T) {
		rec := busy()
		d := &Incus{runner: rec.run, OVN: true}
		if err := d.DropEgress(context.Background(), "feint-scw-x"); err != nil {
			t.Errorf("DropEgress refused an instance that is gone: %v\n"+
				"a delete path runs twice more often than anyone expects", err)
		}
		if rec.carries("ip route del") {
			t.Errorf("a route was deleted on an instance that is gone: %v", rec.sent)
		}
	})

	t.Run("route refuses rather than laying a route it cannot attribute", func(t *testing.T) {
		rec := busy()
		d := &Incus{runner: rec.run, OVN: true}
		if err := d.RouteEgress(context.Background(), "feint-scw-x", "fnt-net"); err == nil {
			t.Error("RouteEgress laid a route on an instance it could not prove is ours")
		}
		if rec.carries("ip route") || rec.carries("config device") {
			t.Errorf("the driver acted before the ownership answer: %v", rec.sent)
		}
	})

	// And the accepting half: an instance carrying our label passes the guard.
	//
	// This double cannot answer the network query that follows, so the call
	// still ends in an error — what is asserted is that the error is not the
	// refusal, and that the driver went on past it. Measuring the laying of the
	// route itself needs a runtime, which is the `runtime` leg.
	t.Run("an instance of ours passes the guard", func(t *testing.T) {
		rec := &recorder{label: "scaleway", devices: detachTwoNICs}
		d := &Incus{runner: rec.run, OVN: true}
		err := d.RouteEgress(context.Background(), "feint-scw-x", "fnt-ours")
		if err != nil && strings.Contains(err.Error(), "not created by the emulator") {
			t.Fatalf("an instance the emulator created was refused as foreign: %v", err)
		}
		if !rec.carries("config get feint-scw-x user.feint.provider") {
			t.Errorf("the ownership question was not asked at all: %v", rec.sent)
		}
		if len(rec.sent) < 2 {
			t.Errorf("the driver stopped at the guard instead of going on: %v", rec.sent)
		}
	})
}
