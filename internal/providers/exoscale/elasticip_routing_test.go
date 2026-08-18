package exoscale_test

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/providers/exoscale"
)

// The elastic-IP plane, asserted through the runtime's arguments: an attached
// address and a routed one look identical in JSON, and their difference is the
// whole reason the attach stopped being a membership edit alone.

// routedRuntime is a recordingRuntime that also carries addresses.
type routedRuntime struct {
	recordingRuntime

	mu       sync.Mutex
	routed   []machine.AddressSpec
	withdrew []string
}

func (r *routedRuntime) Inspect(_ context.Context, n string) (machine.Machine, bool, error) {
	// Running, with an address: the packs replay routes on machines that
	// exist, and the base recording runtime reports every machine absent.
	return machine.Machine{Name: n, IP: "10.209.84.9", Running: true}, true, nil
}

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
	r.recordingRuntime.mu.Lock()
	defer r.recordingRuntime.mu.Unlock()
	out := []string{}
	for _, spec := range r.specs {
		out = append(out, spec.PublicAddresses...)
	}
	return out
}

func routedServe(t *testing.T) (http.Handler, *routedRuntime, *emulator.Env) {
	t.Helper()
	env := emulator.DefaultEnv()
	rt := &routedRuntime{}
	env.Machines = rt
	srv, err := emulator.NewServer(env, exoscale.New(env))
	if err != nil {
		t.Fatalf("build the server: %v", err)
	}
	return srv.Handler(), rt, env
}

func has(list []string, want string) bool {
	for _, have := range list {
		if have == want {
			return true
		}
	}
	return false
}

// anAttachedInstance builds one running instance carrying one elastic IP, and
// returns both identifiers with the address.
func anAttachedInstance(t *testing.T, h http.Handler) (instanceID, eipID, address string) {
	t.Helper()
	call(t, h, "POST", "/v2/instance", `{
		"name":"carrier",
		"instance-type":{"id":"21624abb-764e-4def-81d7-9fc54b5957fb"},
		"template":{"id":"11111111-1111-4111-8111-111111111111"},
		"disk-size":10
	}`)
	_, out := call(t, h, "GET", "/v2/instance", "")
	instances, _ := out["instances"].([]any)
	if len(instances) == 0 {
		t.Fatal("no instance after create")
	}
	inst, _ := instances[0].(map[string]any)
	instanceID, _ = inst["id"].(string)

	_, out = call(t, h, "POST", "/v2/elastic-ip", `{}`)
	ref, _ := out["reference"].(map[string]any)
	eipID, _ = ref["id"].(string)
	_, out = call(t, h, "GET", "/v2/elastic-ip/"+eipID, "")
	address, _ = out["ip"].(string)
	if address == "" {
		t.Fatal("the elastic IP has no address")
	}

	if rec, body := call(t, h, "PUT", "/v2/elastic-ip/"+eipID+":attach",
		`{"instance":{"id":"`+instanceID+`"}}`); rec.Code != http.StatusOK {
		t.Fatalf("attach: %d %v", rec.Code, body)
	}
	return instanceID, eipID, address
}

// An attached address reaches the machine: the attach routes it at once, and
// the next boot carries it as a launch key rather than a live edit.
func TestAttachElasticIPRoutesTheAddress(t *testing.T) {
	h, rt, _ := routedServe(t)
	instanceID, eipID, address := anAttachedInstance(t, h)

	if !has(rt.routedAddresses(), address) {
		t.Errorf("%s was attached and never routed: %v", address, rt.routedAddresses())
	}

	call(t, h, "PUT", "/v2/instance/"+instanceID+":stop", "{}")
	call(t, h, "PUT", "/v2/instance/"+instanceID+":start", "{}")
	if !has(rt.bootedAddresses(), address) {
		t.Errorf("the boot does not carry %s: a live route edit re-plugs an OVN NIC", address)
	}

	// The detach takes the route back.
	call(t, h, "PUT", "/v2/elastic-ip/"+eipID+":detach", `{"instance":{"id":"`+instanceID+`"}}`)
	rt.mu.Lock()
	withdrawn := append([]string(nil), rt.withdrew...)
	rt.mu.Unlock()
	if !has(withdrawn, address) {
		t.Errorf("the address was not withdrawn on detach: %v", withdrawn)
	}
}

// A stored address is untrusted input: PUT /_feint/state restores it verbatim,
// and routing an arbitrary value would send the host's traffic for that
// address into a container. emulatedElasticIP is the authorisation half.
func TestAPoisonedElasticIPIsNeverRouted(t *testing.T) {
	h, rt, env := routedServe(t)

	const poison = "10.76.154.1" // a host bridge gateway, well-formed and not ours

	call(t, h, "POST", "/v2/instance", `{
		"name":"victim",
		"instance-type":{"id":"21624abb-764e-4def-81d7-9fc54b5957fb"},
		"template":{"id":"11111111-1111-4111-8111-111111111111"},
		"disk-size":10
	}`)
	_, out := call(t, h, "GET", "/v2/instance", "")
	instances, _ := out["instances"].([]any)
	inst, _ := instances[0].(map[string]any)
	instanceID, _ := inst["id"].(string)

	_, out = call(t, h, "POST", "/v2/elastic-ip", `{}`)
	ref, _ := out["reference"].(map[string]any)
	eipID, _ := ref["id"].(string)
	stored, found := env.Store.Get(exoscale.Name, "elastic-ip", eipID)
	if !found {
		t.Fatal("the elastic IP is not in the store")
	}
	base := stored.Clone()
	stored.Attrs["ip"] = poison
	env.Store.Commit(base, stored, env.Now())

	call(t, h, "PUT", "/v2/elastic-ip/"+eipID+":attach", `{"instance":{"id":"`+instanceID+`"}}`)
	call(t, h, "PUT", "/v2/instance/"+instanceID+":stop", "{}")
	call(t, h, "PUT", "/v2/instance/"+instanceID+":start", "{}")

	if has(rt.routedAddresses(), poison) {
		t.Errorf("the poisoned address reached the driver: %v", rt.routedAddresses())
	}
	if has(rt.bootedAddresses(), poison) {
		t.Errorf("the poisoned address rode a boot: %v", rt.bootedAddresses())
	}
}

// One Elastic IP, two instances, one machine carrying it (#213).
//
// The control plane accepting both attachments is not the defect, and the
// measurement is what settled that. On ch-gva-2, `exo compute instance
// elastic-ip attach` was accepted twice for one address and both instances then
// reported holding 185.19.28.243 — which is the whole point of the healthcheck an
// Elastic IP carries. Refusing the second attach would have made this emulator
// diverge from the cloud it imitates, and it is the second time in this milestone
// that an audit finding asked for exactly that.
//
// What cannot be honoured is the runtime half. Two containers answering ARP for
// one /32 make the host pick arbitrarily while the API describes both as holders,
// so the address goes to the most recent attach and is taken back from the
// previous one. feint has no healthcheck and does not invent an election;
// docs/limits.md names the rule.
func TestAnElasticIPReachesOneMachineAtATime(t *testing.T) {
	h, rt, env := routedServe(t)
	// The placement registry outlives one server, being package state keyed by
	// provider — so a test that did not clear it would read the previous test's
	// holder and pass for the wrong reason.
	binding := machine.Binding{Provider: "exoscale"}
	binding.ForgetPlacements()
	t.Cleanup(binding.ForgetPlacements)
	_ = env

	first, eipID, address := anAttachedInstance(t, h)

	status, created := call(t, h, "POST", "/v2/instance", `{
		"name": "second",
		"instance-type": {"id": "21624abb-764e-4def-81d7-9fc54b5957fb"},
		"template": {"id": "11111111-1111-4111-8111-111111111111"},
		"disk-size": 10
	}`)
	if status.Code != http.StatusOK {
		t.Fatalf("create the second instance: status %d", status.Code)
	}
	ref, _ := created["reference"].(map[string]any)
	second, _ := ref["id"].(string)
	if second == "" {
		t.Fatalf("the create operation names no resource: %v", created)
	}
	if status, _ := call(t, h, "PUT", "/v2/instance/"+second+":start", ""); status.Code != http.StatusOK {
		t.Fatalf("start the second instance: status %d", status.Code)
	}

	before := len(rt.withdrew)
	if status, out := call(t, h, "PUT", "/v2/elastic-ip/"+eipID+":attach",
		`{"instance":{"id":"`+second+`"}}`); status.Code != http.StatusOK {
		t.Fatalf("the second attach was refused with %d (%v); ch-gva-2 accepts it",
			status.Code, out)
	}

	// The control plane keeps both, because the real one does.
	for _, id := range []string{first, second} {
		status, inst := call(t, h, "GET", "/v2/instance/"+id, "")
		if status.Code != http.StatusOK {
			t.Fatalf("get %s: status %d", id, status.Code)
		}
		eips, _ := inst["elastic-ips"].([]any)
		held := false
		for _, raw := range eips {
			if entry, ok := raw.(map[string]any); ok {
				if got, _ := entry["id"].(string); got == eipID {
					held = true
				}
			}
		}
		if !held {
			t.Errorf("instance %s no longer lists the elastic IP; the real cloud lets both hold it", id)
		}
	}

	// And the runtime carries it once: the previous holder was unrouted before
	// the new one was routed, so no instant has two machines on one address.
	if len(rt.withdrew) == before {
		t.Fatal("the address was routed to the second machine without being taken back from the first")
	}
	if !has(rt.withdrew[before:], address) {
		t.Errorf("the withdrawal names %v, not the address that moved (%s)", rt.withdrew[before:], address)
	}
}
