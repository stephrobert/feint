package scaleway_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/store"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// The addressing plane, asserted through the runtime's arguments, because the
// API cannot witness for itself here: a published address and a routed one look
// identical in JSON, and #116 was exactly their difference — an address
// attached at create was published, pinned nowhere, and answered nothing.

// addressRuntime is a fakeRuntime that also records what was booted, routed and
// withdrawn, which is the whole story of a public address.
type addressRuntime struct {
	*fakeRuntime

	mu       sync.Mutex
	specs    []machine.Spec
	routed   []machine.AddressSpec
	withdrew []string
}

func newAddressRuntime() *addressRuntime {
	rt := &addressRuntime{fakeRuntime: newFakeRuntime()}
	close(rt.release) // nothing here needs to block
	return rt
}

func (r *addressRuntime) Start(ctx context.Context, spec machine.Spec) (machine.Machine, error) {
	r.mu.Lock()
	r.specs = append(r.specs, spec)
	r.mu.Unlock()
	return r.fakeRuntime.Start(ctx, spec)
}

func (r *addressRuntime) RouteAddress(_ context.Context, spec machine.AddressSpec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routed = append(r.routed, spec)
	return nil
}

func (r *addressRuntime) UnrouteAddress(_ context.Context, _, address string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.withdrew = append(r.withdrew, address)
	return nil
}

func (r *addressRuntime) routedAddresses() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.routed))
	for _, spec := range r.routed {
		out = append(out, spec.Address)
	}
	return out
}

func (r *addressRuntime) bootedAddresses() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []string{}
	for _, spec := range r.specs {
		out = append(out, spec.PublicAddresses...)
	}
	return out
}

func (r *addressRuntime) withdrawn() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.withdrew...)
}

// newAddressTestServer is newRuntimeTestServer with the env kept in reach, so a
// test can poison the store the way a restored snapshot would.
func newAddressTestServer(t testing.TB, drv machine.Runtime) (*httptest.Server, *emulator.Env) {
	t.Helper()

	var seq atomic.Int64
	env := &emulator.Env{
		Store: store.New(),
		Now:   func() time.Time { return time.Unix(1700000000, 0).UTC() },
		NewID: func() string {
			return fmt.Sprintf("00000000-0000-4000-8000-%012d", seq.Add(1))
		},
	}
	env.UseMachines(drv)
	srv, err := emulator.NewServer(env, scaleway.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, env
}

func contains(list []string, want string) bool {
	for _, have := range list {
		if have == want {
			return true
		}
	}
	return false
}

// An address attached at create must be routed once the machine exists.
//
// This is #116: attachAddress ran while the server had no machine, silently did
// nothing, and nothing replayed it at poweron — so the API published an address
// no packet could reach. Both halves are asserted: the boot carries the address
// (so the route precedes the first boot instead of re-plugging a live NIC), and
// the replay hands the guest its address.
func TestPowerOnRoutesAnAddressAttachedBeforeBoot(t *testing.T) {
	rt := newAddressRuntime()
	ts, _ := newAddressTestServer(t, machine.Use(rt))

	_, out := do(t, ts, "POST", zone+"/ips", `{}`)
	ip, _ := out["ip"].(map[string]any)
	ipID, _ := ip["id"].(string)
	address, _ := ip["address"].(string)
	if address == "" {
		t.Fatal("no address allocated")
	}

	_, out = do(t, ts, "POST", zone+"/servers",
		`{"name":"carrier","commercial_type":"DEV1-S","image":"ubuntu_jammy","public_ip":"`+ipID+`"}`)
	srv, _ := out["server"].(map[string]any)
	id, _ := srv["id"].(string)

	if status, body := do(t, ts, "POST", zone+"/servers/"+id+"/action",
		`{"action":"poweron"}`); status != http.StatusAccepted {
		t.Fatalf("poweron: %d %v", status, body)
	}

	if !contains(rt.bootedAddresses(), address) {
		t.Errorf("the boot does not carry %s: the route would be a live edit, which re-plugs an OVN NIC", address)
	}
	if !contains(rt.routedAddresses(), address) {
		t.Errorf("%s was published and never routed: routed %v", address, rt.routedAddresses())
	}
}

// dynamic_ip_required allocates at poweron, publishes as a dynamic ServerIP,
// never shows in /ips, and releases on stop — which is #117: the flag was
// decoded, echoed back, and read by nobody.
func TestADynamicAddressFollowsThePowerCycle(t *testing.T) {
	rt := newAddressRuntime()
	ts, _ := newAddressTestServer(t, machine.Use(rt))

	_, out := do(t, ts, "POST", zone+"/servers",
		`{"name":"ephemeral","commercial_type":"DEV1-S","image":"ubuntu_jammy","dynamic_ip_required":true}`)
	srv, _ := out["server"].(map[string]any)
	id, _ := srv["id"].(string)

	do(t, ts, "POST", zone+"/servers/"+id+"/action", `{"action":"poweron"}`)

	_, out = do(t, ts, "GET", zone+"/servers/"+id, "")
	srv, _ = out["server"].(map[string]any)
	ips, _ := srv["public_ips"].([]any)
	if len(ips) != 1 {
		t.Fatalf("a powered-on dynamic server publishes %d addresses, want 1: %v", len(ips), srv["public_ips"])
	}
	entry, _ := ips[0].(map[string]any)
	address, _ := entry["address"].(string)
	if dynamic, _ := entry["dynamic"].(bool); !dynamic {
		t.Errorf("the address is not marked dynamic: %v", entry)
	}
	if !strings.HasPrefix(address, "203.0.113.") {
		t.Errorf("dynamic address %q is outside the emulated public block", address)
	}
	if entry["id"] == "" || entry["id"] == nil {
		t.Errorf("the dynamic address carries no id: %v", entry)
	}
	// The singular field the CLI renders follows the list.
	if first, _ := srv["public_ip"].(map[string]any); first == nil || first["address"] != address {
		t.Errorf("public_ip does not carry the dynamic address: %v", srv["public_ip"])
	}
	if !contains(rt.routedAddresses(), address) {
		t.Errorf("the dynamic address was published and never routed")
	}

	// Not a flexible IP: upstream never lists it in /ips.
	_, out = do(t, ts, "GET", zone+"/ips", "")
	if listed, _ := out["ips"].([]any); len(listed) != 0 {
		t.Errorf("the dynamic address leaked into /ips: %v", listed)
	}

	// The allocator sees it: the next flexible IP must not collide.
	_, out = do(t, ts, "POST", zone+"/ips", `{}`)
	flexible, _ := out["ip"].(map[string]any)
	if flexible["address"] == address {
		t.Errorf("a flexible IP received the dynamic address %s: the allocator cannot see dynamic ones", address)
	}

	// Released on stop, withdrawn from the machine, gone from the view.
	do(t, ts, "POST", zone+"/servers/"+id+"/action", `{"action":"poweroff"}`)
	if !contains(rt.withdrawn(), address) {
		t.Errorf("the dynamic address was not withdrawn on poweroff: %v", rt.withdrawn())
	}
	_, out = do(t, ts, "GET", zone+"/servers/"+id, "")
	srv, _ = out["server"].(map[string]any)
	if ips, _ := srv["public_ips"].([]any); len(ips) != 0 {
		t.Errorf("a stopped dynamic server still publishes %v", ips)
	}
	if srv["public_ip"] != nil {
		t.Errorf("public_ip survives the stop: %v", srv["public_ip"])
	}
}

// A stored address is untrusted input: PUT /_feint/state and `feint snapshot
// load` restore it verbatim, and routing it verbatim would send the host's
// traffic for an arbitrary address — an operator's LAN peer, a real service —
// into a container. Well-formed is not authorised; emulatedAddress is the
// authorisation half, and this holds it on both stored paths.
func TestAPoisonedStoredAddressIsNeverRouted(t *testing.T) {
	rt := newAddressRuntime()
	ts, env := newAddressTestServer(t, machine.Use(rt))

	const poison = "10.76.154.1" // a host bridge gateway, well-formed and not ours

	_, out := do(t, ts, "POST", zone+"/servers",
		`{"name":"victim","commercial_type":"DEV1-S","image":"ubuntu_jammy"}`)
	srv, _ := out["server"].(map[string]any)
	id, _ := srv["id"].(string)
	do(t, ts, "POST", zone+"/servers/"+id+"/action", `{"action":"poweron"}`)

	// A flexible IP whose stored address a snapshot rewrote.
	_, out = do(t, ts, "POST", zone+"/ips", `{}`)
	ip, _ := out["ip"].(map[string]any)
	ipID, _ := ip["id"].(string)
	stored, found := env.Store.Get(scaleway.Name, "instance/ip", ipID)
	if !found {
		t.Fatal("the flexible IP is not in the store")
	}
	base := stored.Clone()
	stored.Attrs["address"] = poison
	env.Store.Commit(base, stored, time.Unix(1700000001, 0))

	do(t, ts, "PATCH", zone+"/ips/"+ipID, `{"server":"`+id+`"}`)

	// A dynamic address a snapshot rewrote, replayed by the next poweron.
	server, found := env.Store.Get(scaleway.Name, "instance/server", id)
	if !found {
		t.Fatal("the server is not in the store")
	}
	serverBase := server.Clone()
	server.Runtime["dynamic-ip"] = poison
	env.Store.Commit(serverBase, server, time.Unix(1700000002, 0))
	do(t, ts, "POST", zone+"/servers/"+id+"/action", `{"action":"reboot"}`)

	if contains(rt.routedAddresses(), poison) {
		t.Errorf("the poisoned address %s reached the driver: routed %v", poison, rt.routedAddresses())
	}
	if contains(rt.bootedAddresses(), poison) {
		t.Errorf("the poisoned address %s rode a boot: %v", poison, rt.bootedAddresses())
	}
}
