package machine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// The teardown race of #493, staged rather than bet on.
//
// A parallel destroy left a running machine, its network and its attached rule
// set on the host while `terraform destroy` said complete and `feint down`
// exited 0 — reproduced twice, and both times the survivor was the machine
// carrying a public address, the one whose teardown withdraws that address
// through a *device edit* on its OVN NIC. The emulator's own journal carries
// the chain:
//
//	unroute … from feint-osc-…/eth0: … Failed setting up OVN port: …
//	Cannot find security ACL ID for "iso-fnt-8b667bd77cd"
//	stop instance …: Error: Instance is busy running a "update" operation
//
// The daemon re-ensures every rule set the NIC's network references while it
// re-plugs the device, resolving names to IDs with no lock shared with its own
// ACL paths; a neighbouring subnet teardown deletes the isolation set between
// the two steps, the failed update operation holds the instance busy, and the
// stop and remove that follow gave up on the first refusal.
//
// fakeOVNd below is that behaviour as it was observed, not a line-by-line
// replay: a device edit snapshots the ACLs its network references when it
// begins, resolves them when it commits, and a delete landing in between fails
// the edit and leaves the instance busy for a while. The channels are what
// make the interleaving a fact rather than a probability, exactly as in
// fakeNetd one file over.
type fakeOVNd struct {
	mu sync.Mutex
	// networkACLs is what network fnt-… references in security.acls.
	networkACLs string
	networkGone bool
	acls        map[string]bool
	instance    struct {
		exists    bool
		busyUntil time.Time
	}
	deviceRoutes string // eth0's ipv4.routes.external
	uplinkRoutes string
	calls        []string
	editFailures []string

	// landed closes when the ACL delete has been applied, so an edit in
	// flight can meet it instead of hoping to.
	landed chan struct{}
	// editStarted closes when the racing device edit has begun: the signal
	// for the concurrent detach to start, with the ordering guard in place
	// and with it removed.
	editStarted chan struct{}
	landOnce    sync.Once
	startOnce   sync.Once
	// beat is how long an edit waits for a delete to cross it: long enough
	// that a racing delete is inside rather than merely likely to be.
	beat time.Duration
	// busyFor is how long the failed update operation holds the instance.
	busyFor time.Duration
}

const (
	racedNetwork = "fnt-a1b2c3d4e5f"
	racedMachine = "feint-osc-i-1"
	racedAddress = "198.51.100.3"
)

func newFakeOVNd() *fakeOVNd {
	f := &fakeOVNd{
		networkACLs:  isolationACL(racedNetwork),
		acls:         map[string]bool{isolationACL(racedNetwork): true},
		deviceRoutes: racedAddress + "/32",
		uplinkRoutes: racedAddress + "/32",
		landed:       make(chan struct{}),
		editStarted:  make(chan struct{}),
		beat:         200 * time.Millisecond,
		busyFor:      100 * time.Millisecond,
	}
	f.instance.exists = true
	return f
}

func (f *fakeOVNd) instanceJSON() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	devices := fmt.Sprintf(`{"eth0":{"type":"nic","network":%q,"ipv4.routes.external":%q}}`,
		racedNetwork, f.deviceRoutes)
	return fmt.Sprintf(`{"name":%q,"config":{%q:"outscale"},"devices":%s,"expanded_devices":%s}`,
		racedMachine, "user."+LabelKey, devices, devices)
}

func (f *fakeOVNd) run(_ context.Context, args ...string) ([]byte, error) {
	key := strings.Join(args, " ")
	f.mu.Lock()
	f.calls = append(f.calls, key)
	f.mu.Unlock()

	switch {
	case key == "config get "+racedMachine+" user."+LabelKey:
		return []byte("outscale\n"), nil

	case key == "network get "+racedNetwork+" user."+LabelKey:
		return []byte("outscale\n"), nil

	case key == "query /1.0/instances/"+racedMachine:
		f.mu.Lock()
		exists := f.instance.exists
		f.mu.Unlock()
		if !exists {
			return nil, errors.New("Error: Instance not found")
		}
		return []byte(f.instanceJSON()), nil

	case key == "query /1.0/instances?recursion=1":
		f.mu.Lock()
		exists := f.instance.exists
		f.mu.Unlock()
		if !exists {
			return []byte("[]"), nil
		}
		return []byte("[" + f.instanceJSON() + "]"), nil

	case key == "query /1.0/networks/"+racedNetwork:
		f.mu.Lock()
		gone := f.networkGone
		f.mu.Unlock()
		if gone {
			return nil, errors.New("Error: Network not found")
		}
		return []byte(`{"type":"ovn","config":{"ipv4.address":"10.0.1.1/24","user.` + LabelKey + `":"outscale"}}`), nil

	case key == "network unset "+racedNetwork+" security.acls":
		f.mu.Lock()
		f.networkACLs = ""
		f.mu.Unlock()
		return nil, nil

	case strings.HasPrefix(key, "network acl delete "):
		name := strings.TrimPrefix(key, "network acl delete ")
		f.mu.Lock()
		if f.networkACLs == name {
			f.mu.Unlock()
			return nil, errors.New("Error: Cannot delete an ACL that is in use")
		}
		delete(f.acls, name)
		f.mu.Unlock()
		f.landOnce.Do(func() { close(f.landed) })
		return nil, nil

	case strings.HasPrefix(key, "config device set "+racedMachine+" eth0 ipv4.routes.external="):
		return f.deviceEdit(key)

	case strings.HasPrefix(key, "stop --force ") || strings.HasPrefix(key, "delete --force "):
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.instance.exists {
			return nil, errors.New("Error: Instance not found")
		}
		if time.Now().Before(f.instance.busyUntil) {
			return nil, errors.New(`Error: Instance is busy running a "update" operation`)
		}
		if strings.HasPrefix(key, "delete --force ") {
			f.instance.exists = false
		}
		return nil, nil

	case key == "network get feint-uplink ipv4.routes":
		f.mu.Lock()
		defer f.mu.Unlock()
		return []byte(f.uplinkRoutes + "\n"), nil

	case strings.HasPrefix(key, "network set feint-uplink ipv4.routes="):
		f.mu.Lock()
		f.uplinkRoutes = strings.TrimPrefix(key, "network set feint-uplink ipv4.routes=")
		f.mu.Unlock()
		return nil, nil

	case strings.HasPrefix(key, "config get "+racedMachine+" volatile."):
		// The interface never carried a recorded address: the repair then has
		// nothing to put back, which keeps the model to the racing edit.
		return []byte("\n"), nil
	}
	return nil, nil
}

// deviceEdit is the daemon's OVN NIC re-plug as observed: the referenced rule
// sets are read when the edit begins and resolved when the port is set back
// up, and a delete landing between the two fails the edit — with the failed
// update operation holding the instance.
func (f *fakeOVNd) deviceEdit(key string) ([]byte, error) {
	f.mu.Lock()
	snapshot := f.networkACLs
	f.mu.Unlock()
	f.startOnce.Do(func() { close(f.editStarted) })

	select {
	case <-f.landed:
	case <-time.After(f.beat):
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if snapshot != "" && !f.acls[snapshot] {
		failure := fmt.Sprintf(
			`Failed to start device "eth0": Failed setting up OVN port: Cannot find security ACL ID for %q`,
			snapshot)
		f.editFailures = append(f.editFailures, failure)
		f.instance.busyUntil = time.Now().Add(f.busyFor)
		return nil, errors.New("Error: " + failure)
	}
	f.deviceRoutes = strings.TrimPrefix(key, "config device set "+racedMachine+" eth0 ipv4.routes.external=")
	return nil, nil
}

func (f *fakeOVNd) failures() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.editFailures...)
}

func (f *fakeOVNd) machineExists() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.instance.exists
}

// The reproduction of #493's first link. A public-address withdrawal (a device
// edit on the machine's OVN NIC) and the isolation detach of the machine's
// network, issued concurrently the way a parallel destroy issues them, must
// take turns: the edit must never resolve a rule set the detach deleted inside
// it, and the machine must be removable afterwards.
//
// The interleaving is staged: the detach starts at the exact moment the device
// edit begins, and the edit waits long enough for the detach's ACL delete to
// land inside it if nothing orders the two. With the ordering in place the
// detach waits its turn instead, and both orders of the two winners are
// acceptable — what must never happen is the third order, the delete inside
// the edit.
func TestAPublicAddressEditAndAnIsolationDetachTakeTurns(t *testing.T) {
	f := newFakeOVNd()
	d := NewIncusOVN()
	d.runner = f.run
	d.busyPoll = 5 * time.Millisecond

	var wg sync.WaitGroup
	var unrouteErr, detachErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		unrouteErr = d.UnrouteAddress(context.Background(), racedMachine, racedAddress)
	}()
	go func() {
		defer wg.Done()
		<-f.editStarted
		detachErr = d.IsolateNetwork(context.Background(), racedNetwork, nil)
	}()
	wg.Wait()

	if failures := f.failures(); len(failures) != 0 {
		t.Fatalf("the isolation detach landed inside the device edit (#493):\n%s\nunroute: %v\ndetach: %v",
			strings.Join(failures, "\n"), unrouteErr, detachErr)
	}
	if unrouteErr != nil {
		t.Fatalf("the address withdrawal failed: %v", unrouteErr)
	}
	if detachErr != nil && !errors.Is(detachErr, ErrNetworkGone) {
		t.Fatalf("the detach failed for a reason other than the network being gone: %v", detachErr)
	}

	// And the teardown completes: the machine the control plane will stop
	// describing must actually be removable.
	if err := d.Remove(context.Background(), racedMachine); err != nil {
		t.Fatalf("remove after the race: %v", err)
	}
	if f.machineExists() {
		t.Fatal("the machine survived its own teardown (#493's tail)")
	}
}

// The same turn-taking, on the granting side: routing a public address onto
// the NIC is the same re-plugging device edit, issued by LinkPublicIp while a
// neighbouring teardown may be detaching the network's isolation set. The
// staging is identical; the edit must never resolve a rule set deleted inside
// it.
func TestARouteAddressEditAndAnIsolationDetachTakeTurns(t *testing.T) {
	f := newFakeOVNd()
	d := NewIncusOVN()
	d.runner = f.run

	const granted = "198.51.100.4"
	var wg sync.WaitGroup
	var routeErr, detachErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		routeErr = d.RouteAddress(context.Background(), AddressSpec{Machine: racedMachine, Address: granted})
	}()
	go func() {
		defer wg.Done()
		<-f.editStarted
		detachErr = d.IsolateNetwork(context.Background(), racedNetwork, nil)
	}()
	wg.Wait()

	if failures := f.failures(); len(failures) != 0 {
		t.Fatalf("the isolation detach landed inside the route edit (#493):\n%s\nroute: %v\ndetach: %v",
			strings.Join(failures, "\n"), routeErr, detachErr)
	}
	if routeErr != nil {
		t.Fatalf("the address grant failed: %v", routeErr)
	}
	if detachErr != nil && !errors.Is(detachErr, ErrNetworkGone) {
		t.Fatalf("the detach failed for a reason other than the network being gone: %v", detachErr)
	}
}

// The second link of #493 on its own: an instance still held by another
// operation refuses its stop and its delete with "Instance is busy", the hold
// ends, and the teardown must wait it out rather than give up on the first
// refusal — one try each is exactly what left a running machine behind a
// `feint down` that exited 0.
func TestRemoveWaitsOutABusyInstance(t *testing.T) {
	f := newFakeOVNd()
	f.mu.Lock()
	f.instance.busyUntil = time.Now().Add(60 * time.Millisecond)
	f.mu.Unlock()

	d := NewIncusOVN()
	d.runner = f.run
	d.busyPoll = 5 * time.Millisecond

	if err := d.Remove(context.Background(), racedMachine); err != nil {
		t.Fatalf("remove gave up on a busy instance: %v", err)
	}
	if f.machineExists() {
		t.Fatal("the instance survived: the delete was never retried past the busy window")
	}
}

// The budget is a budget: an instance that stays busy is reported, never
// waited on for ever and never silently dropped.
func TestABusyInstanceBeyondTheBudgetIsReported(t *testing.T) {
	f := newFakeOVNd()
	f.mu.Lock()
	f.instance.busyUntil = time.Now().Add(time.Hour)
	f.mu.Unlock()

	d := NewIncusOVN()
	d.runner = f.run
	d.busyPoll = 2 * time.Millisecond
	d.busyBudget = 30 * time.Millisecond

	err := d.Remove(context.Background(), racedMachine)
	if err == nil {
		t.Fatal("a permanently busy instance was reported removed")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "instance is busy") {
		t.Fatalf("the report does not name the hold: %v", err)
	}
}
