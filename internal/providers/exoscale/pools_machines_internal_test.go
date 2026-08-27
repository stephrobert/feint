package exoscale

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/store"
)

// The two halves of #484, reproduced at the level where they live.
//
// Measured on examples/stacks/exoscale under --vm incus-ovn: the standalone
// instance and the pool's first member were both handed 192.0.2.3, the host
// refused the second machine's route, and the API kept calling that instance
// running with no machine behind it. Two defects, two witnesses below.

// gatedDriver blocks every Start until the gate is released, which is what
// holds the allocation window of a slow boot open deterministically: the real
// case is a container launch taking seconds while another create allocates.
type gatedDriver struct {
	gate    chan struct{}
	started chan string

	mu    sync.Mutex
	specs []machine.Spec
}

func (d *gatedDriver) Name() string                   { return "gated" }
func (d *gatedDriver) Available(context.Context) bool { return true }
func (d *gatedDriver) Start(_ context.Context, spec machine.Spec) (machine.Machine, error) {
	d.started <- spec.Name
	<-d.gate
	d.mu.Lock()
	d.specs = append(d.specs, spec)
	d.mu.Unlock()
	return machine.Machine{Name: spec.Name, IP: "10.42.0.9", Running: true}, nil
}
func (d *gatedDriver) Stop(context.Context, string) error   { return nil }
func (d *gatedDriver) Remove(context.Context, string) error { return nil }
func (d *gatedDriver) Inspect(_ context.Context, name string) (machine.Machine, bool, error) {
	return machine.Machine{Name: name}, false, nil
}
func (d *gatedDriver) EnsureNetwork(context.Context, machine.NetworkSpec) error { return nil }
func (d *gatedDriver) Attach(context.Context, string, machine.Attachment) error { return nil }
func (d *gatedDriver) Detach(context.Context, string, string) error             { return nil }
func (d *gatedDriver) RemoveNetwork(context.Context, string) error              { return nil }

// failingDriver refuses every Start, the way the host refused the duplicate
// route: "Failed adding host route … file exists".
type failingDriver struct{ recordingDriver }

func (d *failingDriver) Start(context.Context, machine.Spec) (machine.Machine, error) {
	return machine.Machine{}, fmt.Errorf("incus start: Failed to start device \"eth0\"")
}

// sequencedPack is runtimePack with unique identifiers, which address
// allocation needs: two resources under one constant id are one resource.
func sequencedPack(driver machine.Runtime) *Pack {
	n := 0
	var mu sync.Mutex
	env := &emulator.Env{
		Store: store.New(),
		Now:   func() time.Time { return time.Unix(1700000000, 0).UTC() },
		NewID: func() string {
			mu.Lock()
			defer mu.Unlock()
			n++
			// The prefix differs from defaultSecurityGroupID's on purpose: the
			// first identifier this counter minted used to BE the default
			// group's, and a test asserting "the member wears the pool's group"
			// passed against the default-group fallback — a witness planted on
			// its own control value. The falsification harness caught it:
			// the pool-inheritance mutation stayed green.
			return fmt.Sprintf("0000000f-0000-4000-8000-%012d", n)
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	env.UseMachines(driver)
	return New(env)
}

// createOneInstance drives the real handler, which is where the allocation
// window was.
func createOneInstance(p *Pack, name string) {
	body := `{"name":"` + name + `","disk-size":10,` +
		`"template":{"id":"11111111-1111-4111-8111-111111111111"},` +
		`"instance-type":{"id":"71004023-bb72-4a97-b1e9-bc66dfce9470"}}`
	r := httptest.NewRequest("POST", "/v2/instance", strings.NewReader(body))
	p.createInstance(httptest.NewRecorder(), r)
}

func createOnePool(p *Pack, size int) {
	body := fmt.Sprintf(`{"name":"app","size":%d,`+
		`"template":{"id":"11111111-1111-4111-8111-111111111111"},`+
		`"instance-type":{"id":"71004023-bb72-4a97-b1e9-bc66dfce9470"}}`, size)
	r := httptest.NewRequest("POST", "/v2/instance-pool", strings.NewReader(body))
	p.createInstancePool(httptest.NewRecorder(), r)
}

// publicIPs reads every instance's public address off the store, keyed by the
// address so a duplicate collapses the map.
func publicIPs(p *Pack) (byAddress map[string][]string, total int) {
	byAddress = map[string][]string{}
	for _, res := range p.env.Store.List(kindInstance, resource.Tenant{Provider: Name}) {
		if ip, _ := res.Attrs["public-ip"].(string); ip != "" {
			byAddress[ip] = append(byAddress[ip], res.ID)
			total++
		}
	}
	return byAddress, total
}

// TestAPoolMemberAndAStandaloneInstanceNeverShareAnAddress holds the exact
// window of #484 open: a standalone create whose boot is still running when a
// pool allocates its members. The address the instance chose must already be
// visible to the pool's allocation, which is what the Put inside the lock
// delivers; without it the pool re-hands the instance's address and two
// machines carry one /32.
func TestAPoolMemberAndAStandaloneInstanceNeverShareAnAddress(t *testing.T) {
	driver := &gatedDriver{gate: make(chan struct{}), started: make(chan string, 8)}
	p := sequencedPack(machine.Use(driver))

	done := make(chan struct{})
	go func() {
		defer close(done)
		createOneInstance(p, "platform-web")
	}()

	// The instance's boot is now in flight: its address is chosen, its machine
	// is starting, and the create has not answered.
	select {
	case <-driver.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the standalone create never reached the driver")
	}

	// The pool allocates its two members inside that window. Its member boots
	// will block on the same gate, so drive it from a goroutine and only wait
	// for the allocations, which happen before any start.
	poolDone := make(chan struct{})
	go func() {
		defer close(poolDone)
		createOnePool(p, 2)
	}()
	deadline := time.After(5 * time.Second)
	for {
		if _, total := publicIPs(p); total >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the pool never allocated its two members")
		case <-time.After(10 * time.Millisecond):
		}
	}

	close(driver.gate)
	<-done
	<-poolDone

	byAddress, total := publicIPs(p)
	if total != 3 {
		t.Fatalf("%d addressed instances, want 3", total)
	}
	for ip, holders := range byAddress {
		if len(holders) > 1 {
			t.Errorf("%s was handed to %v: two machines carrying one /32 is what the host "+
				"refuses with \"file exists\", and what a real account never does (#484)", ip, holders)
		}
	}
}

// TestAFailedPoolMemberStartIsPublishedAsError is the second half of #484: the
// state a client reads is the one the effect produced. A member whose launch
// the runtime refused must answer "error" — the state Exoscale's own
// instance-state enum declares — and record no machine, because none exists.
func TestAFailedPoolMemberStartIsPublishedAsError(t *testing.T) {
	p := sequencedPack(machine.Use(&failingDriver{}))
	createOnePool(p, 1)

	members := p.env.Store.List(kindInstance, resource.Tenant{Provider: Name})
	if len(members) != 1 {
		t.Fatalf("%d members, want 1", len(members))
	}
	member := members[0]
	if member.State != "error" {
		t.Fatalf("state %q, want error: `running` over a machine the host refused is the "+
			"lie this project exists to avoid (#484)", member.State)
	}
	if name := member.Runtime["machine"]; name != "" {
		t.Fatalf("runtime names machine %q where nothing started", name)
	}
}

// TestAPoolMemberStartIsRecordedInTheStore is the accepting half: a member
// whose machine did start publishes running, and its runtime carries the
// machine's name and address — which is what every later stop, destroy and
// refresh keys on. Before the fix the start mutated a local copy and the store
// kept "no machine" for the member's whole life.
func TestAPoolMemberStartIsRecordedInTheStore(t *testing.T) {
	driver := &recordingDriver{}
	p := sequencedPack(machine.Use(driver))
	createOnePool(p, 1)

	members := p.env.Store.List(kindInstance, resource.Tenant{Provider: Name})
	if len(members) != 1 {
		t.Fatalf("%d members, want 1", len(members))
	}
	member := members[0]
	if member.State != runningState {
		t.Fatalf("state %q, want running", member.State)
	}
	if got, want := member.Runtime["machine"], "feint-exo-"+member.ID; got != want {
		t.Fatalf("runtime machine %q, want %q", got, want)
	}
	if got := member.Runtime["address"]; got != "10.42.0.9" {
		t.Fatalf("runtime address %q, want the one the driver reported", got)
	}
}

// attachRecordingDriver also records what Attach was called with, so a test
// can assert a member's machine really joined a network rather than trust the
// stored record.
type attachRecordingDriver struct {
	recordingDriver
	mu       sync.Mutex
	attached []string
}

func (d *attachRecordingDriver) Attach(_ context.Context, name string, att machine.Attachment) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.attached = append(d.attached, name+" "+att.Network+" "+att.Address)
	return nil
}

// createManagedNetwork drives the real handler and returns the stored resource.
func createManagedNetwork(t *testing.T, p *Pack, name string) *resource.Resource {
	t.Helper()
	r := httptest.NewRequest("POST", "/v2/private-network",
		strings.NewReader(`{"name":"`+name+`","start-ip":"10.90.2.20","end-ip":"10.90.2.200","netmask":"255.255.255.0"}`))
	p.createPrivateNetwork(httptest.NewRecorder(), r)
	for _, pn := range p.env.Store.List(kindPrivateNetwork, resource.Tenant{Provider: Name}) {
		if got, _ := pn.Attrs["name"].(string); got == name {
			return pn
		}
	}
	t.Fatalf("the private network %q was not stored", name)
	return nil
}

func createPoolOnNetwork(p *Pack, size int, networkID string) {
	body := fmt.Sprintf(`{"name":"app","size":%d,`+
		`"template":{"id":"11111111-1111-4111-8111-111111111111"},`+
		`"instance-type":{"id":"71004023-bb72-4a97-b1e9-bc66dfce9470"},`+
		`"private-networks":[{"id":"%s"}]}`, size, networkID)
	r := httptest.NewRequest("POST", "/v2/instance-pool", strings.NewReader(body))
	p.createInstancePool(httptest.NewRecorder(), r)
}

// TestAPoolMemberJoinsThePoolsPrivateNetworks is #492. The pool stored its
// `private-networks` list and nothing ever turned it into memberships: each
// member booted with its routed public NIC alone, and the rule set of
// examples/stacks/exoscale's app tier stayed at used_by=0 on the host —
// nothing to attach to. Upstream the declaration means membership, measured on
// the real API on 2026-08-26: a member of a pool declaring one network answers
// `private-networks: [{id, mac-address}]` on get-instance.
func TestAPoolMemberJoinsThePoolsPrivateNetworks(t *testing.T) {
	driver := &attachRecordingDriver{}
	p := sequencedPack(machine.Use(driver))
	pn := createManagedNetwork(t, p, "back")

	createPoolOnNetwork(p, 2, pn.ID)

	members := p.env.Store.List(kindInstance, resource.Tenant{Provider: Name})
	if len(members) != 2 {
		t.Fatalf("%d members, want 2", len(members))
	}
	seen := map[string]bool{}
	for _, member := range members {
		atts := instanceAttachmentsOf(member)
		if len(atts) != 1 || atts[0].NetworkID != pn.ID {
			t.Fatalf("member %s memberships = %+v, want the pool's network", member.ID, atts)
		}
		if atts[0].IP == "" {
			t.Fatalf("member %s joined the managed network without a lease", member.ID)
		}
		if seen[atts[0].IP] {
			t.Fatalf("two members were handed one lease: %s", atts[0].IP)
		}
		seen[atts[0].IP] = true

		// And the machine half: the boot replayed the membership onto the
		// runtime, on the network the emulator backs the resource with.
		want := member.Runtime["machine"] + " " + pn.Runtime[runtimeNetworkKey] + " " + atts[0].IP
		found := false
		for _, got := range driver.attached {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("no Attach carried %q; attaches: %v", want, driver.attached)
		}
	}
	if leases := p.leasesOf(pn.ID); len(leases) != 2 {
		t.Fatalf("%d leases on the network, want 2", len(leases))
	}
}

// TestAScaleDownReleasesTheMembersLeases is the other half: a member removed
// by a scale-down releases its lease like an ordinary detach, so the address
// is free for whoever comes next.
func TestAScaleDownReleasesTheMembersLeases(t *testing.T) {
	driver := &attachRecordingDriver{}
	p := sequencedPack(machine.Use(driver))
	pn := createManagedNetwork(t, p, "back")
	createPoolOnNetwork(p, 2, pn.ID)

	pools := p.env.Store.List(kindPool, resource.Tenant{Provider: Name})
	if len(pools) != 1 {
		t.Fatalf("%d pools, want 1", len(pools))
	}

	r := httptest.NewRequest("PUT", "/v2/instance-pool/"+pools[0].ID+":scale",
		strings.NewReader(`{"size":1}`))
	r.SetPathValue("id", pools[0].ID)
	p.scaleInstancePool(httptest.NewRecorder(), r)

	if leases := p.leasesOf(pn.ID); len(leases) != 1 {
		t.Fatalf("%d leases after the scale-down, want 1", len(leases))
	}

	// The freed address is genuinely free: growing back re-leases it instead
	// of walking further into the range.
	r = httptest.NewRequest("PUT", "/v2/instance-pool/"+pools[0].ID+":scale",
		strings.NewReader(`{"size":2}`))
	r.SetPathValue("id", pools[0].ID)
	p.scaleInstancePool(httptest.NewRecorder(), r)

	leases := p.leasesOf(pn.ID)
	if len(leases) != 2 {
		t.Fatalf("%d leases after growing back, want 2", len(leases))
	}
	for _, lease := range leases {
		m, _ := lease.(map[string]any)
		if ip, _ := m["ip"].(string); ip != "10.90.2.20" && ip != "10.90.2.21" {
			t.Fatalf("lease %v walked past the freed address", m)
		}
	}
}
