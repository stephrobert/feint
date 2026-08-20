package outscale_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
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

func (r *recordingBalancer) EnsureBalancer(_ context.Context, spec machine.BalancerSpec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensured = append(r.ensured, spec)
	return nil
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
	ts := newRuntimeServer(t, runtime)

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
	ts := newRuntimeServer(t, runtime)

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
	ts := newRuntimeServer(t, runtime)

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

func containsAddress(line, address string) bool {
	return len(line) >= len(address) && line[len(line)-len(address):] == address
}
