package machine

import (
	"context"
	"encoding/json"
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
