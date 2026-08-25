package machine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// The balancer path, held to the three refusals and the one shape (#315).
//
// Every assertion here is about an argument the driver emits or refuses to
// emit, which is the only level at which "it will not go dark" and "it will not
// touch somebody else's network" can be checked without a runtime.

func ovnDriver(f *fakeRuntime) *Incus {
	d := newFakeDriver(f)
	d.OVN = true
	return d
}

// answerExactly answers a call by its whole argument line, which the map-based
// fixture cannot do here: the collection path is a prefix of the item path, so
// a substring match would answer one for the other depending on map order.
func answerExactly(answers map[string]string) func(int, []string) ([]byte, error, bool) {
	return func(_ int, args []string) ([]byte, error, bool) {
		if answer, known := answers[strings.Join(args, " ")]; known {
			return []byte(answer), nil, true
		}
		return nil, nil, false
	}
}

// emptyCollection is what a network with no balancer answers: a list, and a
// success. Every EnsureBalancer asks it first (balancerExists).
const emptyCollection = "query /1.0/networks/fnt-a/load-balancers"

// A balancer whose address is not inside the network's own block is refused.
//
// This is the measurement, turned into a guard. #315 delegated a VIP through
// the uplink's routes and watched it answer 6/6 at t0, 6/6 at t+60s and 0/6
// from t+180s onwards: the runtime announces such an address with a burst of
// gratuitous ARPs at creation and never again. Configuring one would hand a
// user a balancer that passes their test and fails three minutes later, which
// is worse than refusing.
//
// The emitted commands are asserted too, not just the error: a refusal that has
// already written the object is not a refusal.
func TestABalancerOutsideTheNetworksBlockIsRefused(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"network get fnt-a ipv4.address": "10.61.1.1/24\n",
		"network get fnt-a user.":        "outscale\n",
	}, hook: answerExactly(map[string]string{emptyCollection: "[]"})}
	d := ovnDriver(f)

	err := d.EnsureBalancer(context.Background(), BalancerSpec{
		Name:      "lbu-x",
		Network:   "fnt-a",
		Listen:    "198.51.100.240",
		Listeners: []BalancerListener{{Protocol: "tcp", Listen: 80, Backend: 8080}},
		Targets:   []string{"10.61.1.10"},
	})
	if err == nil {
		t.Fatalf("a VIP outside the network's block was accepted; commands: %v", f.commands())
	}
	if !strings.Contains(err.Error(), "10.61.1.0/24") {
		t.Errorf("the refusal must name the block the address had to be in, got %v", err)
	}
	if wrote := f.matching("load-balancers"); len(wrote) != 0 {
		t.Errorf("the refusal still wrote to the runtime: %v", wrote)
	}
}

// A network the emulator did not create is never written to.
//
// The rule every destructive and reconfiguring path here follows: a balancer
// left on an operator's own network survives every sweep, and the sweep is what
// makes this emulator safe to run on a working station.
func TestABalancerOnAForeignNetworkIsRefused(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"network get fnt-a ipv4.address": "10.61.1.1/24\n",
		"network get fnt-a user.":        "\n",
	}}
	d := ovnDriver(f)

	err := d.EnsureBalancer(context.Background(), BalancerSpec{
		Name:      "lbu-x",
		Network:   "fnt-a",
		Listen:    "10.61.1.240",
		Listeners: []BalancerListener{{Protocol: "tcp", Listen: 80}},
	})
	if err == nil {
		t.Fatalf("a foreign network was written to; commands: %v", f.commands())
	}
	if wrote := f.matching("load-balancers"); len(wrote) != 0 {
		t.Errorf("the refusal still wrote to the runtime: %v", wrote)
	}
}

// Deleting a balancer somebody else created is refused.
//
// The create paths are guarded by habit; the destroy paths are the ones that
// destroy. An operator's own OVN load balancer sharing a listen address with an
// emulated one is not a hypothesis worth taking: the ownership marker is the
// description this driver writes, and it is read back before the DELETE.
func TestRemovingAForeignBalancerIsRefused(t *testing.T) {
	f := &fakeRuntime{hook: answerExactly(map[string]string{
		emptyCollection: `["/1.0/networks/fnt-a/load-balancers/10.61.1.240"]`,
		"query /1.0/networks/fnt-a/load-balancers/10.61.1.240": `{"description":"the operator's own","listen_address":"10.61.1.240"}`,
	})}
	d := ovnDriver(f)

	err := d.RemoveBalancer(context.Background(), "fnt-a", "10.61.1.240")
	if err == nil {
		t.Fatalf("a foreign balancer was deleted; commands: %v", f.commands())
	}
	if deleted := f.matching("-X DELETE"); len(deleted) != 0 {
		t.Errorf("the refusal still issued a delete: %v", deleted)
	}
}

// The body sent is the runtime's own shape, and the ports are strings.
//
// Read off the wire on 2026-08-20 rather than taken from a document:
// listen_port and target_port are strings there, and a number would be refused.
// The backend names the ports reference must be the names the backends carry,
// which is why one function mints both.
func TestTheBalancerBodyCarriesTheRuntimesOwnShape(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"network get fnt-a ipv4.address": "10.61.1.1/24\n",
		"network get fnt-a user.":        "outscale\n",
	}, hook: answerExactly(map[string]string{emptyCollection: "[]"})}
	d := ovnDriver(f)

	err := d.EnsureBalancer(context.Background(), BalancerSpec{
		Name:    "lbu-x",
		Network: "fnt-a",
		Listen:  "10.61.1.240",
		Listeners: []BalancerListener{
			{Protocol: "tcp", Listen: 80, Backend: 8080},
			{Protocol: "tcp", Listen: 443, Backend: 8443},
		},
		Targets: []string{"10.61.1.10", "10.61.1.11"},
	})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}

	written := f.matching("load-balancers")
	if len(written) == 0 {
		t.Fatalf("nothing was written; commands: %v", f.commands())
	}
	var body lbBody
	if err := json.Unmarshal([]byte(payloadOf(t, f, "load-balancers")), &body); err != nil {
		t.Fatalf("decode the payload: %v", err)
	}

	// Both listeners, because a balancer serving half of what the API describes
	// is the failure this shape has to make impossible.
	if len(body.Ports) != 2 {
		t.Fatalf("expected one port per listener, got %d: %+v", len(body.Ports), body.Ports)
	}
	for _, port := range body.Ports {
		if port.Protocol != "tcp" {
			t.Errorf("the runtime distributes tcp and udp; got protocol %q", port.Protocol)
		}
		if port.ListenPort == "" {
			t.Errorf("listen_port must be a string the runtime accepts, got %q", port.ListenPort)
		}
	}
	// Two machines, two backend ports: four records, and every name a port
	// references must exist.
	if len(body.Backends) != 4 {
		t.Fatalf("expected one backend per machine per backend port, got %d: %+v", len(body.Backends), body.Backends)
	}
	names := map[string]bool{}
	for _, backend := range body.Backends {
		names[backend.Name] = true
		if backend.TargetPort == "" {
			t.Errorf("target_port must be a string the runtime accepts, got %q", backend.TargetPort)
		}
	}
	for _, port := range body.Ports {
		for _, referenced := range port.TargetBackend {
			if !names[referenced] {
				t.Errorf("port %s references the backend %q, which the body does not carry: %+v",
					port.ListenPort, referenced, body.Backends)
			}
		}
	}
	if body.Description != balancerDescription+" lbu-x" {
		t.Errorf("the balancer carries no ownership marker, so nothing can tell it from an operator's: %q", body.Description)
	}
}

// A bridge-backed run refuses rather than writing something that cannot work.
//
// The declared capability already keeps a pack from calling this, and the
// refusal is the same statement at the point of use: a managed bridge has no
// load balancer primitive at all.
func TestABridgeBackedRunHasNoBalancer(t *testing.T) {
	f := &fakeRuntime{}
	d := newFakeDriver(f) // not OVN

	if err := d.EnsureBalancer(context.Background(), BalancerSpec{
		Name: "lbu-x", Network: "fnt-a", Listen: "10.61.1.240",
		Listeners: []BalancerListener{{Protocol: "tcp", Listen: 80}},
	}); err == nil {
		t.Fatalf("a bridge-backed driver accepted a balancer; commands: %v", f.commands())
	}
	if len(f.commands()) != 0 {
		t.Errorf("a refusal on the mode still called the runtime: %v", f.commands())
	}
	if CapabilitiesOf(d).Balancing {
		t.Error("a bridge-backed driver declares balancing, and a suite keying on it would assert what it cannot deliver")
	}
}

// A protocol the runtime cannot distribute is refused, never dropped.
//
// The pack translates HTTP, HTTPS, TCP and SSL to the transport they all ride,
// because only the pack knows its provider's vocabulary. If one ever reaches
// here untranslated, the balancer must not come up serving the listeners it did
// recognise and silently missing the rest: that is a working balancer with a
// hole in it, and nothing in the response would say so.
func TestAnUndistributableProtocolIsRefused(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"network get fnt-a ipv4.address": "10.61.1.1/24\n",
		"network get fnt-a user.":        "outscale\n",
	}, hook: answerExactly(map[string]string{emptyCollection: "[]"})}
	d := ovnDriver(f)

	err := d.EnsureBalancer(context.Background(), BalancerSpec{
		Name: "lbu-x", Network: "fnt-a", Listen: "10.61.1.240",
		Listeners: []BalancerListener{
			{Protocol: "tcp", Listen: 80, Backend: 8080},
			{Protocol: "https", Listen: 443, Backend: 8443},
		},
		Targets: []string{"10.61.1.10"},
	})
	if err == nil {
		t.Fatalf("an untranslated protocol was accepted; commands: %v", f.commands())
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("the refusal must name the protocol it could not distribute, got %v", err)
	}
	if wrote := f.matching("load-balancers"); len(wrote) != 0 {
		t.Errorf("the refusal still wrote to the runtime: %v", wrote)
	}
}

// A balancer the collection does not hold is created, not updated.
//
// This shipped broken once and the failure is worth keeping named: the first
// version wrote a PUT and read the refusal to decide whether to POST instead.
// The daemon answers "Network load balancer not found", which no phrase list
// here matches, so every first write failed hard and the balancer was never
// created — the emulated load balancer answered on an address nothing was
// listening on, and the conformance suite that found it reported six timeouts
// out of six.
//
// The verb is asserted, not the outcome: a POST to the collection carrying the
// listen address, and no PUT.
func TestABalancerIsCreatedWhenTheCollectionDoesNotHoldIt(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"network get fnt-a ipv4.address": "10.61.1.1/24\n",
		"network get fnt-a user.":        "outscale\n",
	}, hook: answerExactly(map[string]string{emptyCollection: "[]"})}
	d := ovnDriver(f)

	if err := d.EnsureBalancer(context.Background(), BalancerSpec{
		Name: "lbu-x", Network: "fnt-a", Listen: "10.61.1.240",
		Listeners: []BalancerListener{{Protocol: "tcp", Listen: 80}},
		Targets:   []string{"10.61.1.10"},
	}); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if posted := f.matching("-X POST"); len(posted) != 1 {
		t.Fatalf("expected one POST to the collection, got %v", f.commands())
	}
	if put := f.matching("-X PUT"); len(put) != 0 {
		t.Errorf("a balancer that does not exist was updated: %v", put)
	}
	var body lbBody
	if err := json.Unmarshal([]byte(payloadOf(t, f, "-X POST")), &body); err != nil {
		t.Fatalf("decode the payload: %v", err)
	}
	if body.ListenAddress != "10.61.1.240" {
		t.Errorf("a create must name the address it listens on, got %q", body.ListenAddress)
	}
}

// And one the collection does hold is replaced whole, never created twice.
func TestAnExistingBalancerIsReplacedWhole(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"network get fnt-a ipv4.address": "10.61.1.1/24\n",
		"network get fnt-a user.":        "outscale\n",
	}, hook: answerExactly(map[string]string{
		emptyCollection: `["/1.0/networks/fnt-a/load-balancers/10.61.1.240"]`,
	})}
	d := ovnDriver(f)

	if err := d.EnsureBalancer(context.Background(), BalancerSpec{
		Name: "lbu-x", Network: "fnt-a", Listen: "10.61.1.240",
		Listeners: []BalancerListener{{Protocol: "tcp", Listen: 80}},
		Targets:   []string{"10.61.1.10"},
	}); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if put := f.matching("-X PUT"); len(put) != 1 {
		t.Fatalf("expected one PUT, got %v", f.commands())
	}
	if posted := f.matching("-X POST"); len(posted) != 0 {
		t.Errorf("an existing balancer was created a second time: %v", posted)
	}
}

// payloadOf returns the --data argument of the first call matching substr.
func payloadOf(t *testing.T, f *fakeRuntime, substr string) string {
	t.Helper()
	for _, call := range f.calls {
		if !strings.Contains(strings.Join(call, " "), substr) {
			continue
		}
		for i, arg := range call {
			if arg == "--data" && i+1 < len(call) {
				return call[i+1]
			}
		}
	}
	t.Fatalf("no call matching %q carried a payload: %v", substr, f.commands())
	return ""
}

// A backend outside the balancer's own network is refused, whole.
//
// #457, and it is the ordinary two-tier architecture rather than an exotic
// shape: a load balancer on the public subnet, the machines on the private one.
// Before this guard the listen address was checked and each target was only
// parsed, so the specification passed every refusal here and died inside the
// runtime — in the middle of an update, which leaves the balancer standing on
// the backends it already had while the API describes the new set.
//
// The measurements that make refusing the honest answer rather than laziness
// are in balancer.go: the runtime refuses such a backend, peering the two
// networks does not relax it, and the placement that would serve the shape is
// refused on its listen address instead.
//
// The commands are asserted, not only the error: a refusal that has already
// written the object is not a refusal.
func TestABalancerWithATargetOutsideTheNetworkIsRefused(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"network get fnt-a ipv4.address": "10.61.1.1/24\n",
		"network get fnt-a user.":        "outscale\n",
	}, hook: answerExactly(map[string]string{emptyCollection: "[]"})}
	d := ovnDriver(f)

	err := d.EnsureBalancer(context.Background(), BalancerSpec{
		Name:      "lbu-public",
		Network:   "fnt-a",
		Listen:    "10.61.1.5",
		Listeners: []BalancerListener{{Protocol: "tcp", Listen: 80, Backend: 8080}},
		// One backend the network holds and one it does not: the guard has to
		// look at every target, not at the first.
		Targets: []string{"10.61.1.10", "10.61.5.10"},
	})
	if err == nil {
		t.Fatalf("a backend on another subnet was accepted; commands: %v", f.commands())
	}
	if !errors.Is(err, ErrBalancerNotDistributed) {
		t.Errorf("a shape this runtime never distributes must be tellable from a runtime failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "10.61.5.10") {
		t.Errorf("the refusal must name the backend it could not take, got %v", err)
	}
	if !strings.Contains(err.Error(), "10.61.1.0/24") {
		t.Errorf("the refusal must name the block the backend had to be in, got %v", err)
	}
	if wrote := f.matching("load-balancers"); len(wrote) != 0 {
		t.Errorf("the refusal still wrote to the runtime: %v", wrote)
	}
	// The accepting half of the same guard, and it is the half that keeps the
	// product: nothing here withdrew the claim, because nothing reached the
	// host to refuse it.
	if !d.Capabilities().Balancing {
		t.Error("this driver's own refusal withdrew a published claim; only the host's refusal may")
	}
	if err := d.EnsureBalancer(context.Background(), BalancerSpec{
		Name:      "lbu-internal",
		Network:   "fnt-a",
		Listen:    "10.61.1.5",
		Listeners: []BalancerListener{{Protocol: "tcp", Listen: 80, Backend: 8080}},
		Targets:   []string{"10.61.1.10", "10.61.1.11"},
	}); err != nil {
		t.Fatalf("backends of the network's own block must still be served: %v", err)
	}
}

// A balancer with no backend yet is written without a port.
//
// The ordinary Terraform order — the balancer created, its machines registered
// afterwards — and it was failing on every stack that builds one. A port that
// names no backend is refused by the runtime, "Failed applying OVN load
// balancer: Missing VIP target(s)" (Incus 7.2, measured 2026-08-25); the same
// body with no port at all is accepted, and the register that follows PUTs the
// ports in. So the error is removed rather than logged more quietly.
//
// Both halves are asserted: no port while there is nothing to send to it, and
// the ports back the moment there is.
func TestABalancerWithNoBackendIsWrittenWithoutPorts(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"network get fnt-a ipv4.address": "10.61.1.1/24\n",
		"network get fnt-a user.":        "outscale\n",
	}, hook: answerExactly(map[string]string{emptyCollection: "[]"})}
	d := ovnDriver(f)

	if err := d.EnsureBalancer(context.Background(), BalancerSpec{
		Name: "lbu-x", Network: "fnt-a", Listen: "10.61.1.240",
		Listeners: []BalancerListener{{Protocol: "tcp", Listen: 80, Backend: 8080}},
	}); err != nil {
		t.Fatalf("a balancer with no backend must still reach the runtime: %v", err)
	}
	var created lbBody
	if err := json.Unmarshal([]byte(payloadOf(t, f, "-X POST")), &created); err != nil {
		t.Fatalf("decode the payload: %v", err)
	}
	if len(created.Ports) != 0 {
		t.Errorf("a port naming no backend is refused by the runtime (\"Missing VIP target(s)\"), got %+v", created.Ports)
	}
	if len(created.Backends) != 0 {
		t.Errorf("a balancer with no target carries no backend, got %+v", created.Backends)
	}

	// And the register that follows brings the listener back, on a balancer the
	// collection now holds.
	second := &fakeRuntime{answers: map[string]string{
		"network get fnt-a ipv4.address": "10.61.1.1/24\n",
		"network get fnt-a user.":        "outscale\n",
	}, hook: answerExactly(map[string]string{
		emptyCollection: `["/1.0/networks/fnt-a/load-balancers/10.61.1.240"]`,
	})}
	d = ovnDriver(second)
	if err := d.EnsureBalancer(context.Background(), BalancerSpec{
		Name: "lbu-x", Network: "fnt-a", Listen: "10.61.1.240",
		Listeners: []BalancerListener{{Protocol: "tcp", Listen: 80, Backend: 8080}},
		Targets:   []string{"10.61.1.10"},
	}); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	var registered lbBody
	if err := json.Unmarshal([]byte(payloadOf(t, second, "-X PUT")), &registered); err != nil {
		t.Fatalf("decode the payload: %v", err)
	}
	if len(registered.Ports) != 1 || len(registered.Ports[0].TargetBackend) != 1 {
		t.Fatalf("the first backend must bring the listener with it, got %+v", registered.Ports)
	}
}

// A write the host refuses withdraws the claim that this process balances.
//
// firewallRefused's twin (#454, #181), and the same argument one plane over:
// `/_feint/health` telling a suite `capabilities.balancing: true` while the
// daemon holds no balancer is the lying 200 this project exists to refuse, and
// this repository tells every consumer to key on the capability rather than on
// a mode name.
//
// The accepting half is asserted first, and it matters: a driver that published
// balancing=false from the start would pass the withdrawal assertion and claim
// nothing at all. And the last half is the #454 rule itself — a shape this
// driver refused on its own never reached the daemon, and it arrives from a
// restorable snapshot, so it must not be a switch on a published claim.
func TestABalancerWriteTheHostRefusesWithdrawsTheCapability(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"network get fnt-a ipv4.address": "10.61.1.1/24\n",
		"network get fnt-a user.":        "outscale\n",
	}, fail: map[string]error{
		"-X POST": errors.New("incus query: Error: Failed creating load balancer: something new"),
	}, hook: answerExactly(map[string]string{emptyCollection: "[]"})}
	var log bytes.Buffer
	d := ovnDriver(f)
	d.Log = slog.New(slog.NewTextHandler(&log, nil))

	if !d.Capabilities().Balancing {
		t.Fatal("an OVN-backed driver must claim balancing before anything refuses it")
	}
	if err := d.EnsureBalancer(context.Background(), BalancerSpec{
		Name: "lbu-x", Network: "fnt-a", Listen: "10.61.1.240",
		Listeners: []BalancerListener{{Protocol: "tcp", Listen: 80}},
		Targets:   []string{"10.61.1.10"},
	}); err == nil {
		t.Fatal("the refusal must reach the caller")
	}
	if d.Capabilities().Balancing {
		t.Error("the host refused a load balancer and the process still claims to distribute connections")
	}
	if !strings.Contains(log.String(), "capabilities.balancing") {
		t.Errorf("the withdrawal must be said, not only published, got %q", log.String())
	}

	clean := ovnDriver(&fakeRuntime{answers: map[string]string{
		"network get fnt-a ipv4.address": "10.61.1.1/24\n",
		"network get fnt-a user.":        "outscale\n",
	}, hook: answerExactly(map[string]string{emptyCollection: "[]"})})
	if err := clean.EnsureBalancer(context.Background(), BalancerSpec{
		Name: "lbu-x", Network: "fnt-a", Listen: "10.61.1.240",
		Listeners: []BalancerListener{{Protocol: "tcp", Listen: 80}},
		Targets:   []string{"10.62.9.10"},
	}); err == nil {
		t.Fatal("a backend outside the network must still be refused")
	}
	if !clean.Capabilities().Balancing {
		t.Error("a refusal by this driver's own guard must not withdraw the host's capability")
	}
}
