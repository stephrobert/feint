package outscale_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/providers/outscale"
)

// The public-IP plane, asserted through the runtime's arguments: a linked
// address and a routed one look identical in JSON, and their difference is the
// whole reason LinkPublicIp stopped being control-plane-only.

// routedRuntime runs machines instantly and records what was booted, routed
// and withdrawn.
type routedRuntime struct {
	mu       sync.Mutex
	machines map[string]bool
	specs    []machine.Spec
	routed   []machine.AddressSpec
	withdrew []string
}

func newRoutedRuntime() *routedRuntime {
	return &routedRuntime{machines: map[string]bool{}}
}

func (r *routedRuntime) Name() string                   { return "routed-fake" }
func (r *routedRuntime) Available(context.Context) bool { return true }

func (r *routedRuntime) Start(_ context.Context, spec machine.Spec) (machine.Machine, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.specs = append(r.specs, spec)
	r.machines[spec.Name] = true
	return machine.Machine{Name: spec.Name, Addresses: []string{"10.209.84.9"}, Running: true}, nil
}

func (r *routedRuntime) Stop(_ context.Context, n string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.machines[n] = false
	return nil
}

func (r *routedRuntime) Remove(_ context.Context, n string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.machines, n)
	return nil
}

func (r *routedRuntime) Inspect(_ context.Context, n string) (machine.Machine, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	running, found := r.machines[n]
	if !found {
		return machine.Machine{}, false, nil
	}
	return machine.Machine{Name: n, Addresses: []string{"10.209.84.9"}, Running: running}, true, nil
}

func (r *routedRuntime) EnsureNetwork(context.Context, machine.NetworkSpec) error { return nil }
func (r *routedRuntime) Attach(context.Context, string, machine.Attachment) error { return nil }
func (r *routedRuntime) RemoveNetwork(context.Context, string) error              { return nil }

func (r *routedRuntime) RouteAddress(_ context.Context, spec machine.AddressSpec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routed = append(r.routed, spec)
	return nil
}

func (r *routedRuntime) UnrouteAddress(_ context.Context, _, address string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.withdrew = append(r.withdrew, address)
	return nil
}

func (r *routedRuntime) routedAddresses() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.routed))
	for _, spec := range r.routed {
		out = append(out, spec.Address)
	}
	return out
}

func (r *routedRuntime) bootedAddresses() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []string{}
	for _, spec := range r.specs {
		out = append(out, spec.PublicAddresses...)
	}
	return out
}

func (r *routedRuntime) withdrawn() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.withdrew...)
}

func newRoutedServer(t *testing.T) (*httptest.Server, *routedRuntime, *emulator.Env) {
	t.Helper()
	env := emulator.DefaultEnv()
	rt := newRoutedRuntime()
	env.UseMachines(machine.Use(rt))
	srv, err := emulator.NewServer(env, outscale.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, rt, env
}

func has(list []string, want string) bool {
	for _, have := range list {
		if have == want {
			return true
		}
	}
	return false
}

// aLinkedVm builds one running Vm carrying one linked public IP, and returns
// both identifiers with the address.
func aLinkedVm(t *testing.T, ts *httptest.Server) (vmID, ipID, address string) {
	t.Helper()
	_, out := post(t, ts, "CreatePublicIp", `{}`)
	ip, _ := out["PublicIp"].(map[string]any)
	ipID, _ = ip["PublicIpId"].(string)
	address, _ = ip["PublicIp"].(string)
	if address == "" {
		t.Fatal("no address allocated")
	}
	_, out = post(t, ts, "CreateVms", `{"ImageId":"ami-00000001","VmType":"tinav6.c1r1p2"}`)
	vms, _ := out["Vms"].([]any)
	vm, _ := vms[0].(map[string]any)
	vmID, _ = vm["VmId"].(string)
	if status, body := post(t, ts, "LinkPublicIp",
		`{"PublicIpId":"`+ipID+`","VmId":"`+vmID+`"}`); status != http.StatusOK {
		t.Fatalf("link: %d %v", status, body)
	}
	return vmID, ipID, address
}

// A linked address reaches the machine: the link routes it at once, and the
// next boot carries it as a launch key rather than a live edit.
func TestLinkPublicIpRoutesTheAddress(t *testing.T) {
	ts, rt, _ := newRoutedServer(t)
	vmID, _, address := aLinkedVm(t, ts)

	if !has(rt.routedAddresses(), address) {
		t.Errorf("%s was linked and never routed: %v", address, rt.routedAddresses())
	}

	post(t, ts, "StopVms", `{"VmIds":["`+vmID+`"]}`)
	post(t, ts, "StartVms", `{"VmIds":["`+vmID+`"]}`)
	if !has(rt.bootedAddresses(), address) {
		t.Errorf("the boot does not carry %s: a live route edit re-plugs an OVN NIC", address)
	}
}

// Terminating the Vm releases its address, upstream's own behaviour: the
// address stays allocated, stops naming the machine, and stops being routed —
// on OVN the uplink route would otherwise outlive the machine.
func TestTerminateReleasesTheLinkedPublicIp(t *testing.T) {
	ts, rt, _ := newRoutedServer(t)
	vmID, ipID, address := aLinkedVm(t, ts)

	if status, body := post(t, ts, "DeleteVms", `{"VmIds":["`+vmID+`"]}`); status != http.StatusOK {
		t.Fatalf("delete: %d %v", status, body)
	}
	if !has(rt.withdrawn(), address) {
		t.Errorf("the address was not withdrawn on terminate: %v", rt.withdrawn())
	}
	_, out := post(t, ts, "ReadPublicIps", `{}`)
	ips, _ := out["PublicIps"].([]any)
	for _, entry := range ips {
		ip, _ := entry.(map[string]any)
		if ip["PublicIpId"] == ipID && ip["VmId"] != nil {
			t.Errorf("the address still names the terminated Vm: %v", ip)
		}
	}
}

// A stored address is untrusted input: PUT /_feint/state restores it verbatim,
// and routing an arbitrary value would send the host's traffic for that
// address into a container. emulatedPublicIP is the authorisation half.
func TestAPoisonedPublicIpIsNeverRouted(t *testing.T) {
	ts, rt, env := newRoutedServer(t)

	const poison = "10.76.154.1" // a host bridge gateway, well-formed and not ours

	_, out := post(t, ts, "CreatePublicIp", `{}`)
	ip, _ := out["PublicIp"].(map[string]any)
	ipID, _ := ip["PublicIpId"].(string)
	stored, found := env.Store.Get(outscale.Name, "publicip", ipID)
	if !found {
		t.Fatal("the public IP is not in the store")
	}
	base := stored.Clone()
	stored.Attrs["PublicIp"] = poison
	env.Store.Commit(base, stored, env.Now())

	_, out = post(t, ts, "CreateVms", `{"ImageId":"ami-00000001","VmType":"tinav6.c1r1p2"}`)
	vms, _ := out["Vms"].([]any)
	vm, _ := vms[0].(map[string]any)
	vmID, _ := vm["VmId"].(string)
	post(t, ts, "LinkPublicIp", `{"PublicIpId":"`+ipID+`","VmId":"`+vmID+`"}`)
	post(t, ts, "StopVms", `{"VmIds":["`+vmID+`"]}`)
	post(t, ts, "StartVms", `{"VmIds":["`+vmID+`"]}`)

	if has(rt.routedAddresses(), poison) {
		t.Errorf("the poisoned address reached the driver: %v", rt.routedAddresses())
	}
	if has(rt.bootedAddresses(), poison) {
		t.Errorf("the poisoned address rode a boot: %v", rt.bootedAddresses())
	}
}

// Detach completes the machine package's driver contract; *routedRuntime needs no behaviour here.
func (r *routedRuntime) Detach(context.Context, string, string) error { return nil }
