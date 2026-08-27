package outscale_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/providers/outscale"
)

// The pack half of the load-balancer dataplane (#315).
//
// The driver's own guards are held in internal/core/machine; what belongs here
// is the wiring: which balancer specification the pack builds out of what it
// stored, and — the half that decides whether anything is claimed at all —
// whether it asks the runtime when the runtime has not declared it can.

// recordingBalancer is a driver that can balance, and says whether it was
// allowed to. `declares` is the capability, deliberately separate from the
// interface: the two questions are different and conflating them is the defect
// this file's first test exists for.
type recordingBalancer struct {
	*blockingRuntime
	declares bool

	mu      sync.Mutex
	ensured []machine.BalancerSpec
	removed []string
}

func newRecordingBalancer(declares bool) *recordingBalancer {
	return &recordingBalancer{blockingRuntime: newBlockingRuntime(), declares: declares}
}

func (r *recordingBalancer) Capabilities() machine.Capabilities {
	return machine.Capabilities{Machines: true, Addresses: true, Balancing: r.declares}
}

func (r *recordingBalancer) EnsureBalancer(_ context.Context, spec machine.BalancerSpec) (machine.BalancerDelivery, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensured = append(r.ensured, spec)
	// The whole spec distributed: this fake plays the runtime on the happy
	// path, and the partial path has its own fake below.
	return machine.BalancerDelivery{Distributed: append([]string(nil), spec.Targets...)}, nil
}

func (r *recordingBalancer) RemoveBalancer(_ context.Context, network, listen string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removed = append(r.removed, network+" "+listen)
	return nil
}

func (r *recordingBalancer) specs() []machine.BalancerSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]machine.BalancerSpec(nil), r.ensured...)
}

func (r *recordingBalancer) removals() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.removed...)
}

// aBalancedStack creates a Net, a Subnet, two Vms and an internal balancer
// holding both, and returns the balancer's listen address.
func aBalancedStack(t *testing.T, ts *httptest.Server) (vmA, vmB, vip string) {
	t.Helper()
	contractDoc := contractDoc(t)
	doc := call(t, ts, contractDoc, "CreateNet", `{"IpRange":"10.188.0.0/16"}`)
	netID, _ := doc["Net"].(map[string]any)["NetId"].(string)
	doc = call(t, ts, contractDoc, "CreateSubnet", `{"NetId":"`+netID+`","IpRange":"10.188.3.0/24"}`)
	subnet, _ := doc["Subnet"].(map[string]any)["SubnetId"].(string)

	launch := func() string {
		t.Helper()
		out := call(t, ts, contractDoc, "CreateVms",
			`{"ImageId":"ami-00000003","VmType":"tinav6.c1r1p2","SubnetId":"`+subnet+`"}`)
		vms, _ := out["Vms"].([]any)
		if len(vms) == 0 {
			t.Fatalf("CreateVms answered no machine: %v", out)
		}
		id, _ := vms[0].(map[string]any)["VmId"].(string)
		return id
	}
	vmA, vmB = launch(), launch()

	doc = call(t, ts, contractDoc, "CreateLoadBalancer",
		`{"LoadBalancerName":"lbu-data","LoadBalancerType":"internal","Subnets":["`+subnet+`"],`+
			`"Listeners":[{"LoadBalancerPort":80,"LoadBalancerProtocol":"TCP","BackendPort":8080,"BackendProtocol":"TCP"}]}`)
	lb, _ := doc["LoadBalancer"].(map[string]any)
	vip, _ = lb["PrivateIp"].(string)
	if vip == "" {
		t.Fatalf("the balancer came back with no PrivateIp: %v", doc)
	}
	call(t, ts, contractDoc, "RegisterVmsInLoadBalancer",
		`{"LoadBalancerName":"lbu-data","BackendVmIds":["`+vmA+`","`+vmB+`"]}`)
	return vmA, vmB, vip
}

// A runtime that has not declared balancing is never asked to balance.
//
// The interface and the capability are two questions, and the Incus driver is
// exactly why: it implements Balancer in every mode and can only deliver it
// under OVN, and startup verification clears the claim on a host with no OVN
// wiring. A pack that asked the first question only would drive every
// bridge-backed run into a refusal on every register, and the operator would
// read a stack of errors about a feature they never asked for.
//
// An undeclared capability reads as absent: that rule has to hold at the point
// of use, not only in the health payload.
func TestAnUndeclaredBalancingCapabilityIsNeverUsed(t *testing.T) {
	runtime := newRecordingBalancer(false)
	close(runtime.release)
	ts := newRuntimeServer(t, machine.Use(runtime))

	aBalancedStack(t, ts)

	if specs := runtime.specs(); len(specs) != 0 {
		t.Fatalf("the pack handed %d balancer(s) to a runtime that declares none: %+v", len(specs), specs)
	}
}

// What the pack asks for is what it stored, translated once.
//
// Three things are asserted because each was a decision: the listen address is
// the balancer's own PrivateIp on the Subnet's network, the backends are the
// machines' private addresses (never the public ones, which route nowhere), and
// the listener protocol reaching the runtime is the transport — an LBU listener
// speaks HTTP, HTTPS, TCP or SSL, all four ride TCP, and none is terminated
// here.
func TestTheBalancerSpecIsWhatTheApiDescribes(t *testing.T) {
	runtime := newRecordingBalancer(true)
	close(runtime.release)
	ts := newRuntimeServer(t, machine.Use(runtime))

	_, _, vip := aBalancedStack(t, ts)

	specs := runtime.specs()
	if len(specs) == 0 {
		t.Fatalf("nothing was handed to a runtime that declares balancing")
	}
	last := specs[len(specs)-1]
	if last.Listen != vip {
		t.Errorf("the balancer listens on %q, and the API publishes %q", last.Listen, vip)
	}
	if last.Network == "" {
		t.Error("the balancer names no network, so the runtime would not know where to put it")
	}
	if len(last.Listeners) != 1 || last.Listeners[0].Protocol != "tcp" ||
		last.Listeners[0].Listen != 80 || last.Listeners[0].Backend != 8080 {
		t.Errorf("the listener reached the runtime as %+v, want tcp 80 -> 8080", last.Listeners)
	}
	if len(last.Targets) != 2 {
		t.Fatalf("expected both machines as backends, got %v", last.Targets)
	}
	for _, target := range last.Targets {
		if len(target) < 7 || target[:7] != "10.188." {
			t.Errorf("the backend %q is not an address of the Subnet; a balancer inside a network "+
				"distributes to private addresses", target)
		}
	}
}

// An unlinked machine stops being a backend, and a deleted balancer goes.
//
// The replace-not-patch rule, at the level a unit test can hold it: the pack
// must replay the whole set after an unlink, or the runtime keeps sending
// connections to a machine the API has already stopped listing.
func TestUnlinkingAndDeletingReachTheRuntime(t *testing.T) {
	runtime := newRecordingBalancer(true)
	close(runtime.release)
	ts := newRuntimeServer(t, machine.Use(runtime))

	vmA, _, vip := aBalancedStack(t, ts)

	call(t, ts, contractDoc(t), "UnlinkLoadBalancerBackendMachines",
		`{"LoadBalancerName":"lbu-data","BackendVmIds":["`+vmA+`"]}`)
	specs := runtime.specs()
	after := specs[len(specs)-1]
	if len(after.Targets) != 1 {
		t.Fatalf("after an unlink the runtime still holds %v", after.Targets)
	}

	if status, _ := post(t, ts, "DeleteLoadBalancer", `{"LoadBalancerName":"lbu-data"}`); status != http.StatusOK {
		t.Fatalf("DeleteLoadBalancer answered %d", status)
	}
	removals := runtime.removals()
	if len(removals) == 0 {
		t.Fatal("the balancer was deleted and nothing was removed from the runtime; " +
			"it would hold an address the next create could reuse")
	}
	if last := removals[len(removals)-1]; !containsAddress(last, vip) {
		t.Errorf("the removal names %q, and the balancer answered on %q", last, vip)
	}
}

// A balancer that loses its last listener is withdrawn from the runtime.
//
// This is not a corner case, it is the middle of every single-listener port
// change: providers 1.1.3, 1.7.0 and 1.8.0 all delete the departing front port
// before creating the arriving one, so the balancer really does stand with an
// empty listener set for the span of an update (#344).
//
// Before the fix, syncBalancer read the empty set as "nothing to hand over" and
// returned, leaving the runtime distributing connections on a port the API had
// stopped listing — the lie this project exists to refuse, and one only a test
// that empties the set can catch.
func TestEmptyingTheListenersRemovesTheBalancerFromTheRuntime(t *testing.T) {
	runtime := newRecordingBalancer(true)
	close(runtime.release)
	ts := newRuntimeServer(t, machine.Use(runtime))

	_, _, vip := aBalancedStack(t, ts)
	if len(runtime.specs()) == 0 {
		t.Fatal("the balancer never reached the runtime, so this test measures nothing")
	}
	before := len(runtime.removals())

	call(t, ts, contractDoc(t), "DeleteLoadBalancerListeners",
		`{"LoadBalancerName":"lbu-data","LoadBalancerPorts":[80]}`)

	removals := runtime.removals()
	if len(removals) == before {
		t.Fatal("the balancer lost its only listener and nothing was withdrawn from the runtime; " +
			"it would keep distributing on a port the API no longer lists")
	}
	if last := removals[len(removals)-1]; !containsAddress(last, vip) {
		t.Errorf("the withdrawal names %q, and the balancer answered on %q", last, vip)
	}

	// And the arriving port reaches the runtime, which is the other half of the
	// same update: a withdrawal that never came back would be just as wrong.
	call(t, ts, contractDoc(t), "CreateLoadBalancerListeners",
		`{"LoadBalancerName":"lbu-data","Listeners":[{"LoadBalancerPort":8080,"LoadBalancerProtocol":"TCP",`+
			`"BackendPort":8080,"BackendProtocol":"TCP"}]}`)
	specs := runtime.specs()
	last := specs[len(specs)-1]
	if len(last.Listeners) != 1 || last.Listeners[0].Listen != 8080 {
		t.Fatalf("the moved listener reached the runtime as %+v, want one on 8080", last.Listeners)
	}
	if len(last.Targets) != 2 {
		t.Errorf("the backends were lost across the listener change: %v", last.Targets)
	}
}

func containsAddress(line, address string) bool {
	return len(line) >= len(address) && line[len(line)-len(address):] == address
}

// refusingBalancer is a runtime that declares balancing and refuses one shape,
// the way the Incus driver refuses a listen address outside its network's
// block (#315).
type refusingBalancer struct {
	*recordingBalancer
	err error
}

func (r *refusingBalancer) EnsureBalancer(ctx context.Context, spec machine.BalancerSpec) (machine.BalancerDelivery, error) {
	_, _ = r.recordingBalancer.EnsureBalancer(ctx, spec)
	return machine.BalancerDelivery{}, r.err
}

// withholdingBalancer is a runtime that takes part of a spec and names the
// rest, the way the Incus driver withholds a backend outside the balancer's
// own subnet (#457, #483).
type withholdingBalancer struct {
	*recordingBalancer
	withheld map[string]string
}

func (r *withholdingBalancer) EnsureBalancer(ctx context.Context, spec machine.BalancerSpec) (machine.BalancerDelivery, error) {
	_, _ = r.recordingBalancer.EnsureBalancer(ctx, spec)
	delivery := machine.BalancerDelivery{Undistributed: map[string]string{}}
	for _, target := range spec.Targets {
		if reason, held := r.withheld[target]; held {
			delivery.Undistributed[target] = reason
			continue
		}
		delivery.Distributed = append(delivery.Distributed, target)
	}
	return delivery, nil
}

// newLoggedRuntimeServer is newRuntimeServer with somewhere to read the log,
// because the level a line carries is the subject here.
func newLoggedRuntimeServer(t *testing.T, drv machine.Runtime, log *bytes.Buffer) *httptest.Server {
	t.Helper()
	env := emulator.DefaultEnv()
	env.UseMachines(drv)
	env.Log = slog.New(slog.NewTextHandler(log, nil))
	srv, err := emulator.NewServer(env, outscale.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// A shape this runtime does not distribute is a limit, not an incident.
//
// The two-tier architecture — a load balancer in front of machines on another
// subnet — is refused by the driver on every register, for as long as the stack
// lives, and #457 measured why it cannot be served here. Reported at ERROR that
// is a permanent error on a working configuration, and a log whose errors are
// permanent is a log whose errors get skipped, which costs the next real one.
//
// So the level is the assertion, and both halves of it: no ERROR for the limit,
// and an ERROR still there for a runtime that broke.
func TestAnUndistributableShapeIsNotLoggedAsAnError(t *testing.T) {
	var log bytes.Buffer
	runtime := &refusingBalancer{
		recordingBalancer: newRecordingBalancer(true),
		err: fmt.Errorf("%w: balancer lbu-data on fnt-a has the backend 10.188.9.4, which is "+
			"outside that network's own block 10.188.3.0/24", machine.ErrBalancerNotDistributed),
	}
	close(runtime.release)
	ts := newLoggedRuntimeServer(t, machine.Use(runtime), &log)

	aBalancedStack(t, ts)
	if len(runtime.specs()) == 0 {
		t.Fatal("the balancer never reached the runtime, so this test measures nothing")
	}
	if strings.Contains(log.String(), "level=ERROR") {
		t.Errorf("a shape this runtime never distributes was reported as an error: %q", log.String())
	}
	if !strings.Contains(log.String(), "level=WARN") {
		t.Errorf("the limit must still be said, and it was not: %q", log.String())
	}
	if !strings.Contains(log.String(), "goes on describing it") {
		t.Errorf("the line must say what the API keeps describing, got %q", log.String())
	}

	// The other half: a runtime that failed at something it had accepted is not
	// a limit, and a WARN there would hide a real outage.
	var broken bytes.Buffer
	failing := &refusingBalancer{
		recordingBalancer: newRecordingBalancer(true),
		err:               errors.New("incus query: Error: Failed creating load balancer: something new"),
	}
	close(failing.release)
	aBalancedStack(t, newLoggedRuntimeServer(t, machine.Use(failing), &broken))
	if !strings.Contains(broken.String(), "level=ERROR") {
		t.Errorf("a runtime failure must stay an error: %q", broken.String())
	}
}

// balancerRuntimeRecord reads the store's own account of what the runtime
// holds for lbu-data, through the surface an operator or a gate reads it from:
// `/_feint/state`. Reading the store through its public door on purpose — the
// record's whole value is that somebody outside this process can meet it.
func balancerRuntimeRecord(t *testing.T, ts *httptest.Server) map[string]string {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + "/_feint/state") //nolint:noctx // a local test server
	if err != nil {
		t.Fatalf("read the state: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var state struct {
		Resources []struct {
			Kind    string            `json:"Kind"`
			ID      string            `json:"ID"`
			Runtime map[string]string `json:"Runtime"`
		} `json:"resources"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatalf("decode the state: %v", err)
	}
	for _, res := range state.Resources {
		if res.Kind == "loadbalancer" && res.ID == "lbu-data" {
			return res.Runtime
		}
	}
	t.Fatal("the state holds no load balancer, so there is no record to read")
	return nil
}

// A partial delivery is recorded where a reader can meet it, and said at WARN.
//
// #483, measured before this test existed: the ordinary two-tier stack — one
// backend on the balancer's own subnet, one on another — left the host holding
// a balancer with no backend and no port while the API described both backends
// and the run logged zero ERROR lines. One WARN was the only trace, and no
// gate reads a log. The state published must be the state the effect produced:
// the delivered half and the withheld half both go into the resource's
// Runtime, visible through `/_feint/state`, and the WARN names what the API
// goes on describing.
//
// The record has to follow every sync, not only the first: after the withheld
// machine is unlinked, a record still claiming it would be the same lie with
// the sign flipped.
func TestAPartialDeliveryIsRecordedAndSaidAtWarn(t *testing.T) {
	var log bytes.Buffer
	runtime := &withholdingBalancer{recordingBalancer: newRecordingBalancer(true), withheld: map[string]string{}}
	close(runtime.release)
	ts := newLoggedRuntimeServer(t, machine.Use(runtime), &log)

	doc := contractDoc(t)
	out := call(t, ts, doc, "CreateNet", `{"IpRange":"10.188.0.0/16"}`)
	netID, _ := out["Net"].(map[string]any)["NetId"].(string)
	out = call(t, ts, doc, "CreateSubnet", `{"NetId":"`+netID+`","IpRange":"10.188.3.0/24"}`)
	subnet, _ := out["Subnet"].(map[string]any)["SubnetId"].(string)
	launch := func() (id, ip string) {
		t.Helper()
		out := call(t, ts, doc, "CreateVms",
			`{"ImageId":"ami-00000003","VmType":"tinav6.c1r1p2","SubnetId":"`+subnet+`"}`)
		vms, _ := out["Vms"].([]any)
		if len(vms) == 0 {
			t.Fatalf("CreateVms answered no machine: %v", out)
		}
		vm, _ := vms[0].(map[string]any)
		id, _ = vm["VmId"].(string)
		ip, _ = vm["PrivateIp"].(string)
		if id == "" || ip == "" {
			t.Fatalf("a machine came back without an id or an address: %v", vm)
		}
		return id, ip
	}
	vmA, ipA := launch()
	vmB, ipB := launch()
	runtime.withheld[ipB] = "outside fnt-x's own block 10.188.3.0/24, which this runtime cannot distribute to (#457)"

	call(t, ts, doc, "CreateLoadBalancer",
		`{"LoadBalancerName":"lbu-data","LoadBalancerType":"internal","Subnets":["`+subnet+`"],`+
			`"Listeners":[{"LoadBalancerPort":80,"LoadBalancerProtocol":"TCP","BackendPort":8080,"BackendProtocol":"TCP"}]}`)
	call(t, ts, doc, "RegisterVmsInLoadBalancer",
		`{"LoadBalancerName":"lbu-data","BackendVmIds":["`+vmA+`","`+vmB+`"]}`)
	if len(runtime.specs()) == 0 {
		t.Fatal("the balancer never reached the runtime, so this test measures nothing")
	}

	record := balancerRuntimeRecord(t, ts)
	if record[machine.RuntimeBalancerDistributed] != ipA {
		t.Errorf("the record must say exactly what is distributed, got %q want %q",
			record[machine.RuntimeBalancerDistributed], ipA)
	}
	undistributed := record[machine.RuntimeBalancerUndistributed]
	if !strings.Contains(undistributed, ipB) || !strings.Contains(undistributed, "#457") {
		t.Errorf("the record must name the withheld backend and its reason, got %q", undistributed)
	}
	if strings.Contains(log.String(), "level=ERROR") {
		t.Errorf("a partial delivery is a limit, not an incident: %q", log.String())
	}
	if !strings.Contains(log.String(), "level=WARN") || !strings.Contains(log.String(), ipB) {
		t.Errorf("the WARN must exist and name the withheld backend, got %q", log.String())
	}

	// The record follows the next sync: once the withheld machine is
	// unlinked, the API and the host agree again and the record says so.
	call(t, ts, doc, "UnlinkLoadBalancerBackendMachines",
		`{"LoadBalancerName":"lbu-data","BackendVmIds":["`+vmB+`"]}`)
	record = balancerRuntimeRecord(t, ts)
	if record[machine.RuntimeBalancerDistributed] != ipA {
		t.Errorf("the surviving backend must stay on the record, got %q", record[machine.RuntimeBalancerDistributed])
	}
	if record[machine.RuntimeBalancerUndistributed] != "" {
		t.Errorf("a record still claiming a withheld backend after its unlink is the same lie "+
			"with the sign flipped, got %q", record[machine.RuntimeBalancerUndistributed])
	}
}
