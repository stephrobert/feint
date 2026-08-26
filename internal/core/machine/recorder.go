package machine

import (
	"context"
	"sync"
)

// The shared contract recorder (#515).
//
// The repository proves argv-level facts through the injectable Incus runner,
// and contract-level facts through per-suite, unexported fakes — fifteen-plus
// of them, each seeing one suite's slice of the driver. None of them can state
// a property across packs, and it was one of those homemade recorders that
// found #508's severed peering, which is the measure of what recording the
// sequence is worth. This file is that recorder written once, in the machine
// package's own vocabulary, so a property like "the same intent produces the
// same runtime sequence whatever the pack" becomes statable at all.
//
// Two disciplines, both held by tests rather than by this comment:
//
//   - the vocabulary below is closed, and TestTheContractNamesEveryGesture
//     holds it against the driver interfaces in both directions: a gesture the
//     contract cannot name is a violation, and a name the interfaces no longer
//     carry is fiction;
//   - the detector for a gesture outside the vocabulary is proven able to find
//     one — TestAPlantedUnknownGestureIsReported plants one — because a control
//     that looks for absence and never found anything is indistinguishable
//     from a control that looked nowhere.

// Gesture is one contract-level call a pack asked of the runtime. (Event was
// the natural name and is already taken by the watcher's type in watch.go;
// gesture is the word #514 uses for exactly this.)
type Gesture struct {
	// Kind names the gesture: the Driver (or optional-half) method that was
	// called. It must be a key of the contract vocabulary.
	Kind string
	// Resource is what the gesture acted on — the machine, network, rule set
	// or balancer it names, or the address for the routing pair.
	Resource string
	// Args carries the full argument, for the assertions that need more than
	// the name: the boot Spec, the NetworkSpec, the FirewallSpec, and so on.
	Args any
}

// contractGestures is the closed vocabulary of the driver contract: every
// method of Driver and its optional halves (Router, Firewaller, Peerer,
// Isolator, Balancer, Capable), and nothing else. The value says whether the
// gesture changes the host: true is a gesture the Recorder records, false is a
// read, deliberately left out of the recording — a read changes nothing on the
// host, and packs legitimately differ in how often they look.
//
// This list is the service list of #514 §1 at driver level. A method added to
// an interface without a row here fails TestTheContractNamesEveryGesture; a
// row without a method fails it the other way.
var contractGestures = map[string]bool{
	// Driver.
	"Name":          false,
	"Available":     false,
	"Start":         true,
	"Stop":          true,
	"Remove":        true,
	"Inspect":       false,
	"EnsureNetwork": true,
	"Attach":        true,
	"Detach":        true,
	"RemoveNetwork": true,
	// Router.
	"RouteAddress":   true,
	"UnrouteAddress": true,
	// Firewaller.
	"EnsureFirewall": true,
	"ApplyFirewall":  true,
	"RemoveFirewall": true,
	// Peerer.
	"NativeIsolation": false,
	"PeerNetworks":    true,
	// Isolator.
	"IsolateNetwork": true,
	// Balancer.
	"EnsureBalancer": true,
	"RemoveBalancer": true,
	// Capable.
	"Capabilities": false,
}

// KnownGesture reports whether the contract can name this gesture. An event
// whose Kind it refuses is what Recorder.OutsideContract returns.
func KnownGesture(kind string) bool {
	_, known := contractGestures[kind]
	return known
}

// Recorder is the shared fake runtime: it implements Driver and every optional
// half, runs machines instantly, and records each host-changing gesture as a
// typed Gesture, in call order.
//
// It exists for the properties no per-suite fake can state — the same intent
// produces the same runtime sequence in every pack, and no pack asks the
// runtime a gesture outside the contract. A suite asserting a driver-internal
// fact keeps its own fake; nothing forces a migration.
//
// Safe for concurrent use, like the contract it implements.
type Recorder struct {
	// Joined declares the runtime's networks born joined, the bridge shape:
	// NativeIsolation answers false and packs take their Isolator path. The
	// zero value is the OVN shape — networks born separate, joined by peering —
	// because that is the mode the stack replays run under and the only one
	// that delivers isolation.
	Joined bool

	mu       sync.Mutex
	events   []Gesture
	machines map[string]recordedMachine
}

// recordedMachine is what the Recorder holds for one started machine.
type recordedMachine struct {
	ip      string
	running bool
}

// NewRecorder returns a Recorder ready to stand behind an emulator.Env.
func NewRecorder() *Recorder {
	return &Recorder{machines: map[string]recordedMachine{}}
}

// Record appends one gesture. It is the seam every gesture below goes through,
// and it is exported on purpose: a positive control plants a gesture the
// vocabulary cannot name and requires OutsideContract to report it, because an
// unknown-gesture detector that never found one proves nothing.
func (r *Recorder) Record(g Gesture) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, g)
}

// Events returns every recorded gesture, in call order.
func (r *Recorder) Events() []Gesture {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Gesture(nil), r.events...)
}

// Sequence returns the recorded gesture kinds, in call order. It is the value
// the cross-pack equivalence property compares: same intent, same sequence.
func (r *Recorder) Sequence() []string {
	events := r.Events()
	kinds := make([]string, 0, len(events))
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	return kinds
}

// OutsideContract returns the recorded gestures whose Kind the contract cannot
// name. A non-empty answer is a contract violation: a gesture reached the
// runtime that the service list of #514 does not know.
func (r *Recorder) OutsideContract() []Gesture {
	var out []Gesture
	for _, e := range r.Events() {
		if _, known := contractGestures[e.Kind]; !known {
			out = append(out, e)
		}
	}
	return out
}

// Name implements Driver.
func (r *Recorder) Name() string { return "recorder" }

// Available implements Driver.
func (r *Recorder) Available(context.Context) bool { return true }

// Start implements Driver: the machine runs at once, on the address its first
// attachment fixes, or on a stable placeholder when the pack fixed none.
func (r *Recorder) Start(_ context.Context, spec Spec) (Machine, error) {
	ip := "10.230.0.10"
	if len(spec.Attachments) > 0 && spec.Attachments[0].Address != "" {
		ip = spec.Attachments[0].Address
	}
	r.mu.Lock()
	r.machines[spec.Name] = recordedMachine{ip: ip, running: true}
	r.mu.Unlock()
	r.Record(Gesture{Kind: "Start", Resource: spec.Name, Args: spec})
	return Machine{Name: spec.Name, IP: ip, Running: true}, nil
}

// Stop implements Driver.
func (r *Recorder) Stop(_ context.Context, name string) error {
	r.mu.Lock()
	if m, found := r.machines[name]; found {
		m.running = false
		r.machines[name] = m
	}
	r.mu.Unlock()
	r.Record(Gesture{Kind: "Stop", Resource: name})
	return nil
}

// Remove implements Driver. It succeeds when nothing is there, as the contract
// requires.
func (r *Recorder) Remove(_ context.Context, name string) error {
	r.mu.Lock()
	delete(r.machines, name)
	r.mu.Unlock()
	r.Record(Gesture{Kind: "Remove", Resource: name})
	return nil
}

// Inspect implements Driver. A read, so it is not recorded.
func (r *Recorder) Inspect(_ context.Context, name string) (Machine, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, found := r.machines[name]
	if !found {
		return Machine{}, false, nil
	}
	return Machine{Name: name, IP: m.ip, Running: m.running}, true, nil
}

// EnsureNetwork implements Driver.
func (r *Recorder) EnsureNetwork(_ context.Context, spec NetworkSpec) error {
	r.Record(Gesture{Kind: "EnsureNetwork", Resource: spec.Name, Args: spec})
	return nil
}

// Attach implements Driver.
func (r *Recorder) Attach(_ context.Context, name string, att Attachment) error {
	r.Record(Gesture{Kind: "Attach", Resource: name, Args: att})
	return nil
}

// Detach implements Driver.
func (r *Recorder) Detach(_ context.Context, name, network string) error {
	r.Record(Gesture{Kind: "Detach", Resource: name, Args: network})
	return nil
}

// RemoveNetwork implements Driver.
func (r *Recorder) RemoveNetwork(_ context.Context, name string) error {
	r.Record(Gesture{Kind: "RemoveNetwork", Resource: name})
	return nil
}

// RouteAddress implements Router.
func (r *Recorder) RouteAddress(_ context.Context, spec AddressSpec) error {
	r.Record(Gesture{Kind: "RouteAddress", Resource: spec.Address, Args: spec})
	return nil
}

// UnrouteAddress implements Router.
func (r *Recorder) UnrouteAddress(_ context.Context, machine, address string) error {
	r.Record(Gesture{Kind: "UnrouteAddress", Resource: address, Args: machine})
	return nil
}

// EnsureFirewall implements Firewaller.
func (r *Recorder) EnsureFirewall(_ context.Context, spec FirewallSpec) error {
	r.Record(Gesture{Kind: "EnsureFirewall", Resource: spec.Name, Args: spec})
	return nil
}

// ApplyFirewall implements Firewaller.
func (r *Recorder) ApplyFirewall(_ context.Context, machine string, binding FirewallBinding) error {
	r.Record(Gesture{Kind: "ApplyFirewall", Resource: machine, Args: binding})
	return nil
}

// RemoveFirewall implements Firewaller.
func (r *Recorder) RemoveFirewall(_ context.Context, name string) error {
	r.Record(Gesture{Kind: "RemoveFirewall", Resource: name})
	return nil
}

// NativeIsolation implements Peerer. A read, so it is not recorded.
func (r *Recorder) NativeIsolation() bool { return !r.Joined }

// PeerNetworks implements Peerer.
func (r *Recorder) PeerNetworks(_ context.Context, network string, peers []string) error {
	r.Record(Gesture{Kind: "PeerNetworks", Resource: network, Args: append([]string(nil), peers...)})
	return nil
}

// IsolateNetwork implements Isolator, for a Recorder declared Joined.
func (r *Recorder) IsolateNetwork(_ context.Context, network string, foreign []string) error {
	r.Record(Gesture{Kind: "IsolateNetwork", Resource: network, Args: append([]string(nil), foreign...)})
	return nil
}

// EnsureBalancer implements Balancer: the spec is taken whole.
func (r *Recorder) EnsureBalancer(_ context.Context, spec BalancerSpec) (BalancerDelivery, error) {
	r.Record(Gesture{Kind: "EnsureBalancer", Resource: spec.Listen, Args: spec})
	return BalancerDelivery{Distributed: append([]string(nil), spec.Targets...)}, nil
}

// RemoveBalancer implements Balancer.
func (r *Recorder) RemoveBalancer(_ context.Context, network, listen string) error {
	r.Record(Gesture{Kind: "RemoveBalancer", Resource: listen, Args: network})
	return nil
}

// Capabilities implements Capable: the Recorder claims everything, so every
// enforced path a pack has is taken and recorded. A read, so it is not
// recorded.
func (r *Recorder) Capabilities() Capabilities {
	return Capabilities{
		Machines:           true,
		Addresses:          true,
		Firewall:           true,
		FirewallPublicOnly: true,
		Isolation:          true,
		OwnKernel:          true,
		Balancing:          true,
		PrivateFromHost:    true,
	}
}
