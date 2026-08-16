package outscale_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/providers/outscale"
)

// carrySecondary reconfigures an interface on a live machine, and both its
// callers held the pack's address lock across the call (#216).
//
// The function's own comment said the opposite — "the call is made outside the
// store lock… launching or reconfiguring a machine takes seconds where the lock
// takes microseconds" — which makes it the family CLAUDE.md names: a comment that
// describes a discipline the code beside it does not follow, in the very function
// that denies it.
//
// What it cost is measurable rather than arguable, and that is what this asserts:
// every address allocation in the pack — CreateNet, CreateSubnet, CreateNic,
// LinkPublicIp, CreateVms, UpdateNet, every one of them takes the same lock — was
// blocked for as long as one incus exec.

// slowAttach answers every Attach only once released, so a handler that reaches
// the runtime while holding a lock keeps holding it for as long as the test says.
type slowAttach struct {
	mu       sync.Mutex
	machines map[string]bool

	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *slowAttach) Name() string                   { return "fake" }
func (s *slowAttach) Available(context.Context) bool { return true }

func (s *slowAttach) Start(_ context.Context, spec machine.Spec) (machine.Machine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.machines[spec.Name] = true
	return machine.Machine{Name: spec.Name, Running: true}, nil
}

func (s *slowAttach) Stop(_ context.Context, n string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.machines[n] = false
	return nil
}

func (s *slowAttach) Remove(_ context.Context, n string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.machines, n)
	return nil
}

func (s *slowAttach) Inspect(_ context.Context, n string) (machine.Machine, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	running, found := s.machines[n]
	return machine.Machine{Name: n, Running: running}, found, nil
}

func (s *slowAttach) EnsureNetwork(context.Context, machine.NetworkSpec) error { return nil }
func (s *slowAttach) RemoveNetwork(context.Context, string) error              { return nil }

// The one slow call. Everything else answers at once, so what the test measures
// is where this one sits relative to the lock and nothing else.
func (s *slowAttach) Attach(context.Context, string, machine.Attachment) error {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return nil
}

func newSlowAttachServer(t *testing.T) (*httptest.Server, *slowAttach) {
	t.Helper()
	rt := &slowAttach{
		machines: map[string]bool{},
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	env := emulator.DefaultEnv()
	env.Machines = rt
	srv, err := emulator.NewServer(env, outscale.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		// Never leave the blocked handler waiting: a test that fails must fail,
		// not hang, and this pack has already paid once for a failure path that
		// hung instead of failing.
		select {
		case <-rt.release:
		default:
			close(rt.release)
		}
		ts.Close()
	})
	return ts, rt
}

func TestAnAddressLinkDoesNotHoldTheAddressLockAcrossTheRuntime(t *testing.T) {
	ts, rt := newSlowAttachServer(t)

	_, subnet := netWithSubnet(t, ts, "10.41.0.0/16", "10.41.1.0/24")

	// A machine, so the NIC has something to be carried on: carrySecondary is a
	// no-op for a NIC linked to nothing, and a test that measured that would
	// measure the absence of the call rather than its placement.
	status, doc := doAction(t, ts, "CreateVms",
		`{"ImageId":"ami-12345678","VmType":"tinav4.c1r1p2","SubnetId":"`+subnet+`"}`)
	if status != http.StatusOK {
		t.Fatalf("CreateVms answered %d: %v", status, doc)
	}
	vms, _ := doc["Vms"].([]any)
	vm, _ := vms[0].(map[string]any)
	vmID, _ := vm["VmId"].(string)

	nic := createNic(t, ts, subnet)
	if status, doc := doAction(t, ts, "LinkNic",
		`{"NicId":"`+nic+`","VmId":"`+vmID+`","DeviceNumber":1}`); status != http.StatusOK {
		t.Fatalf("LinkNic answered %d: %v", status, doc)
	}

	// One link that will reach the runtime and stay there.
	blocked := make(chan int, 1)
	go func() {
		status, _ := postRaw(ts, "LinkPrivateIps",
			`{"NicId":"`+nic+`","PrivateIps":["10.41.1.40"]}`)
		blocked <- status
	}()

	select {
	case <-rt.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the link never reached the runtime, so this test measured nothing")
	}

	// While it is in there, the rest of the pack must still be able to allocate.
	// CreateSubnet takes the same lock, and it is the plainest thing a second
	// client would be doing.
	done := make(chan int, 1)
	go func() {
		status, _ := postRaw(ts, "CreateSubnet",
			`{"NetId":"`+netOf(t, ts, subnet)+`","IpRange":"10.41.2.0/24"}`)
		done <- status
	}()

	select {
	case status := <-done:
		if status != http.StatusOK {
			t.Errorf("the concurrent allocation answered %d", status)
		}
	case <-time.After(3 * time.Second):
		t.Error("a second allocation waited on a lock held across a runtime call: " +
			"one interface reconfiguration blocks every address this pack hands out")
	}

	close(rt.release)
	if status := <-blocked; status != http.StatusOK {
		t.Errorf("the link itself answered %d", status)
	}
}

// netOf reads back the Net a Subnet belongs to, so the test does not have to
// thread it through every helper.
func netOf(t *testing.T, ts *httptest.Server, subnetID string) string {
	t.Helper()
	_, doc := doAction(t, ts, "ReadSubnets", `{"Filters":{"SubnetIds":["`+subnetID+`"]}}`)
	subnets, _ := doc["Subnets"].([]any)
	if len(subnets) == 0 {
		t.Fatalf("no subnet %s: %v", subnetID, doc)
	}
	first, _ := subnets[0].(map[string]any)
	netID, _ := first["NetId"].(string)
	return netID
}
