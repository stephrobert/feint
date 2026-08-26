package scaleway_test

import (
	"context"
	"errors"
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

// fakeRuntime is a machine.Driver that records what it was asked to do and
// refuses a name it has already given out, the way Incus refuses to launch an
// instance whose name exists. That refusal is the whole point: it is what turned
// a second poweron into a "stopped" server with a container still running.
type fakeRuntime struct {
	mu       sync.Mutex
	machines map[string]bool
	// specs keeps what each start was asked to boot, so a test can assert on
	// the image and the login rather than on a state name (#83).
	specs []machine.Spec
	// detached keeps every "machine network" pair Detach was called with (#426).
	detached []string

	starts atomic.Int32
	// entered is signalled on the way into Start, before it blocks, so a test
	// can tell "the second caller reached the runtime" from "the second caller
	// is waiting for the first".
	entered chan string
	// release holds every Start until the test closes it, which is what makes
	// the overlap a fact rather than a probability.
	release chan struct{}
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{
		machines: map[string]bool{},
		entered:  make(chan string, 8),
		release:  make(chan struct{}),
	}
}

func (f *fakeRuntime) Name() string                   { return "fake" }
func (f *fakeRuntime) Available(context.Context) bool { return true }
func (f *fakeRuntime) Stop(_ context.Context, n string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.machines[n] = false
	return nil
}

func (f *fakeRuntime) Remove(_ context.Context, n string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.machines, n)
	return nil
}

func (f *fakeRuntime) Start(_ context.Context, spec machine.Spec) (machine.Machine, error) {
	f.starts.Add(1)
	select {
	case f.entered <- spec.Name:
	default:
	}
	<-f.release

	f.mu.Lock()
	defer f.mu.Unlock()
	f.specs = append(f.specs, spec)
	if _, taken := f.machines[spec.Name]; taken {
		return machine.Machine{}, errors.New(`Failed creating instance record: Add instance info to the database: This "instances" entry already exists`)
	}
	f.machines[spec.Name] = true
	return machine.Machine{Name: spec.Name, IP: "10.42.0.7", Running: true}, nil
}

func (f *fakeRuntime) Inspect(_ context.Context, n string) (machine.Machine, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	running, ok := f.machines[n]
	if !ok {
		return machine.Machine{}, false, nil
	}
	return machine.Machine{Name: n, IP: "10.42.0.7", Running: running}, true, nil
}

func (f *fakeRuntime) EnsureNetwork(context.Context, machine.NetworkSpec) error { return nil }
func (f *fakeRuntime) Attach(context.Context, string, machine.Attachment) error { return nil }
func (f *fakeRuntime) RemoveNetwork(context.Context, string) error              { return nil }

// Detach records the pairs rather than ignoring them, because #426 was a
// destruction that was never emitted at all: the store forgot the NIC, the API
// answered 204, and the device stayed on the container. A test that reads a
// status code cannot tell those apart; one that reads this can.
func (f *fakeRuntime) Detach(_ context.Context, name, network string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detached = append(f.detached, name+" "+network)
	return nil
}

// detaches reports what the runtime was actually asked to take apart.
func (f *fakeRuntime) detaches() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.detached...)
}

// running reports what the runtime actually holds, which is the half of the
// truth the API cannot be trusted to report on its own.
func (f *fakeRuntime) running() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.machines))
	for name, up := range f.machines {
		if up {
			out = append(out, name)
		}
	}
	return out
}

// newRuntimeTestServer is newTestServer with a machine runtime behind it, and an
// id generator that is safe to call from two requests at once — the sequential
// one races under -race the moment a test issues concurrent calls.
func newRuntimeTestServer(t testing.TB, drv machine.Driver) *httptest.Server {
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
	return ts
}

// call issues one request from a goroutine, where t.Fatalf is not allowed.
func call(ts *httptest.Server, method, path, body string) (int, error) {
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-Auth-Token", "irrelevant-the-emulator-ignores-it")
	resp, err := ts.Client().Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

// Two poweron on one server must start one machine.
//
// The audit of 2026-07-29 read the race off the code and the project's own
// instructions already prescribed the fix, per-target locking, which is exactly
// how a fix stays unwritten for months: nothing failed.
//
// The test falsifies both halves of the correction at once. Remove the
// Serialise in serverAction and the second request reaches the runtime while the
// first is still inside it, which the entered channel catches. Remove the
// already-running short-circuit and the second request launches once the first
// has finished, which the start count catches. Either way the emulator ends up
// describing as "stopped" a container that is running, which is the one answer
// this project exists not to give.
func TestConcurrentPowerOnStartsTheMachineOnce(t *testing.T) {
	rt := newFakeRuntime()
	ts := newRuntimeTestServer(t, rt)
	const zone = "/instance/v1/zones/fr-par-1"

	status, out := do(t, ts, "POST", zone+"/servers", `{"name":"demo","commercial_type":"DEV1-S"}`)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d", status)
	}
	server, _ := out["server"].(map[string]any)
	id, _ := server["id"].(string)

	action := zone + "/servers/" + id + "/action"
	first := make(chan int, 1)
	second := make(chan int, 1)

	go func() {
		status, err := call(ts, "POST", action, `{"action":"poweron"}`)
		if err != nil {
			t.Error(err)
		}
		first <- status
	}()

	// The first request is inside the runtime and holds the target.
	select {
	case <-rt.entered:
	case <-time.After(5 * time.Second):
		close(rt.release)
		t.Fatal("the first poweron never reached the runtime")
	}

	go func() {
		status, err := call(ts, "POST", action, `{"action":"poweron"}`)
		if err != nil {
			t.Error(err)
		}
		second <- status
	}()

	select {
	case name := <-rt.entered:
		close(rt.release)
		t.Fatalf("a second poweron reached the runtime for %s while the first was still launching", name)
	case <-time.After(300 * time.Millisecond):
		// Waiting on the target, which is what it should be doing.
	}

	close(rt.release)
	if status := <-first; status != http.StatusAccepted {
		t.Fatalf("first poweron: status %d", status)
	}
	if status := <-second; status != http.StatusAccepted {
		t.Fatalf("second poweron: status %d", status)
	}

	if starts := rt.starts.Load(); starts != 1 {
		t.Fatalf("the runtime was asked to start %d machines for one server, want 1", starts)
	}

	// And the API describes what the runtime holds, which is the property all
	// of this is for.
	status, out = do(t, ts, "GET", zone+"/servers/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("get: status %d", status)
	}
	view, _ := out["server"].(map[string]any)
	state, _ := view["state"].(string)
	if state != "running" {
		t.Fatalf("the API describes the server as %q while a container of its name is running", state)
	}
	if got := rt.running(); len(got) != 1 {
		t.Fatalf("the runtime holds %v, want exactly one machine", got)
	}
}

// The sequential half of the same property, and the one a reader can follow: a
// server that is up stays up when a client asks again. Terraform and `scw` both
// retry an action they did not see the answer to.
func TestPowerOnIsIdempotentOnARunningServer(t *testing.T) {
	rt := newFakeRuntime()
	close(rt.release) // nothing to hold: this test is sequential
	ts := newRuntimeTestServer(t, rt)
	const zone = "/instance/v1/zones/fr-par-1"

	status, out := do(t, ts, "POST", zone+"/servers", `{"name":"demo","commercial_type":"DEV1-S"}`)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d", status)
	}
	server, _ := out["server"].(map[string]any)
	id, _ := server["id"].(string)

	for range 2 {
		if status, _ := do(t, ts, "POST", zone+"/servers/"+id+"/action", `{"action":"poweron"}`); status != http.StatusAccepted {
			t.Fatalf("poweron: status %d", status)
		}
	}

	if starts := rt.starts.Load(); starts != 1 {
		t.Fatalf("two poweron produced %d launches, want 1", starts)
	}

	status, out = do(t, ts, "GET", zone+"/servers/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("get: status %d", status)
	}
	view, _ := out["server"].(map[string]any)
	if state, _ := view["state"].(string); state != "running" {
		t.Fatalf("the server is %q after a repeated poweron, want running", state)
	}
}
