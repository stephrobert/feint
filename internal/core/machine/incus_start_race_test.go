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

// The third call site of #493's daemon behaviour, measured on a live host
// before being staged (2026-08-28, Incus 7.2, OVN mode): `incus start` reads
// the ACL names each NIC's network references as it begins, and resolves them
// to IDs as it sets the OVN ports up, with no lock shared with the daemon's
// own ACL paths. An isolation detach landing between the two steps — a
// peering acceptance empties every foreign list mid-apply, so IsolateNetwork
// unsets and deletes the rule sets while the stack's machines are starting —
// kills the start with:
//
//	Failed to start device "eth0": Failed setting up OVN port: Failed
//	ensuring security ACLs are configured in OVN for instance: Cannot find
//	security ACL ID for "iso-fnt-…"
//
// Staged on the host: with the detach fired 50–300 ms into a ~600 ms
// container start, 12 of 14 starts died exactly so; fired before the start
// had read its config, none did; and the attach direction — the rule set
// created and referenced while the start was in flight — produced it in 0 of
// 14 tries. The reference can only dangle behind a delete, which is why
// retrying the wording is still wrong (TestANonTransientFailureIsNotRetried)
// and the ordering is still the fix. The failed start is terminal:
// abandonStart removes the instance, the pack publishes its failed state, and
// nothing retries — a machine left 'stopped' behind an apply that returned 0
// (tools/conformance/functional.sh outscale, 9 red runs in 13).
//
// Same medicine as the two device-edit call sites one file over
// (incus_unroute_race_test.go): the start holds the lock of every network it
// plugs, the detach already holds it, so the two take turns.

const (
	racedStartNet     = "fnt-b2c3d4e5f60"
	racedStartNet2    = "fnt-c3d4e5f6071"
	racedStartMachine = "feint-osc-i-9"
)

// fakeStartd is the daemon's start-time ACL resolution as observed, not a
// line-by-line replay: an operation that plugs an OVN port (an instance
// start, a NIC add on a running instance) snapshots the ACLs its networks
// reference when it begins, resolves them when the port comes up, and a
// delete landing in between fails the operation. The channels make the
// interleaving a fact rather than a probability, exactly as in fakeOVNd one
// file over.
type fakeStartd struct {
	mu sync.Mutex
	// networkACLs is what each network references in security.acls.
	networkACLs map[string]string
	acls        map[string]bool
	exists      bool
	running     bool
	devices     map[string]map[string]string
	failures    []string

	// landed closes when the ACL delete has been applied, so a plug in
	// flight can meet it instead of hoping to.
	landed chan struct{}
	// plugBegun closes when the staged plugging operation has begun: the
	// signal for the concurrent detach to start, with the ordering guard in
	// place and with it removed.
	plugBegun chan struct{}
	// plugOn names the network whose plug is staged against the detach;
	// plugs of other networks resolve without waiting.
	plugOn    string
	landOnce  sync.Once
	begunOnce sync.Once
	// beat is how long a plug waits for a delete to cross it: long enough
	// that a racing delete is inside rather than merely likely to be.
	beat time.Duration
}

func newFakeStartd(exists bool) *fakeStartd {
	f := &fakeStartd{
		networkACLs: map[string]string{
			racedStartNet:  isolationACL(racedStartNet),
			racedStartNet2: isolationACL(racedStartNet2),
		},
		acls: map[string]bool{
			isolationACL(racedStartNet):  true,
			isolationACL(racedStartNet2): true,
		},
		exists:    exists,
		plugOn:    racedStartNet,
		landed:    make(chan struct{}),
		plugBegun: make(chan struct{}),
		beat:      200 * time.Millisecond,
	}
	f.devices = map[string]map[string]string{}
	if exists {
		f.devices["eth0"] = map[string]string{"type": "nic", "network": racedStartNet}
	}
	return f
}

func (f *fakeStartd) instanceJSON() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	status := "Stopped"
	if f.running {
		status = "Running"
	}
	return fmt.Sprintf(`{"name":%q,"status":%q,"state":{"network":{}}}`,
		racedStartMachine, status)
}

func (f *fakeStartd) devicesJSON() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := make([]string, 0, len(f.devices))
	for name, cfg := range f.devices {
		entry := fmt.Sprintf(`%q:{"type":%q,"network":%q`, name, cfg["type"], cfg["network"])
		if cfg["ipv4.address"] != "" {
			entry += fmt.Sprintf(`,"ipv4.address":%q`, cfg["ipv4.address"])
		}
		parts = append(parts, entry+"}")
	}
	body := "{" + strings.Join(parts, ",") + "}"
	return fmt.Sprintf(`{"name":%q,"devices":%s,"expanded_devices":%s}`,
		racedStartMachine, body, body)
}

// plug is the daemon's OVN port setup as observed: the referenced rule sets
// are read when the operation begins and resolved when the port comes up, and
// a delete landing between the two fails the operation.
func (f *fakeStartd) plug(networks ...string) error {
	f.mu.Lock()
	snapshots := map[string]string{}
	staged := false
	for _, network := range networks {
		snapshots[network] = f.networkACLs[network]
		if network == f.plugOn {
			staged = true
		}
	}
	f.mu.Unlock()

	if staged {
		f.begunOnce.Do(func() { close(f.plugBegun) })
		select {
		case <-f.landed:
		case <-time.After(f.beat):
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	for _, network := range networks {
		snapshot := snapshots[network]
		if snapshot != "" && !f.acls[snapshot] {
			failure := fmt.Sprintf(
				`Failed to start device "eth0": Failed setting up OVN port: Cannot find security ACL ID for %q`,
				snapshot)
			f.failures = append(f.failures, failure)
			return errors.New("Error: " + failure)
		}
	}
	return nil
}

func (f *fakeStartd) run(_ context.Context, args ...string) ([]byte, error) {
	key := strings.Join(args, " ")

	switch {
	case key == "list "+racedStartMachine+" --format json":
		f.mu.Lock()
		exists := f.exists
		f.mu.Unlock()
		if !exists {
			return []byte("[]"), nil
		}
		return []byte("[" + f.instanceJSON() + "]"), nil

	case key == "query /1.0/instances/"+racedStartMachine:
		f.mu.Lock()
		exists := f.exists
		f.mu.Unlock()
		if !exists {
			return nil, errors.New("Error: Instance not found")
		}
		return []byte(f.devicesJSON()), nil

	case key == "query /1.0/instances?recursion=1":
		// The permissive sweep of a detach: no NICs of ours to touch.
		return []byte("[]"), nil

	case strings.HasPrefix(key, "query /1.0/networks/"):
		name := strings.TrimPrefix(key, "query /1.0/networks/")
		return []byte(`{"type":"ovn","config":{"ipv4.address":"10.0.9.1/24","user.` +
			LabelKey + `":"outscale"}}`), fakeStartdNetworkErr(name)

	case strings.HasPrefix(key, "network unset ") && strings.HasSuffix(key, " security.acls"):
		name := strings.TrimSuffix(strings.TrimPrefix(key, "network unset "), " security.acls")
		f.mu.Lock()
		f.networkACLs[name] = ""
		f.mu.Unlock()
		return nil, nil

	case strings.HasPrefix(key, "network acl delete "):
		name := strings.TrimPrefix(key, "network acl delete ")
		f.mu.Lock()
		for _, ref := range f.networkACLs {
			if ref == name {
				f.mu.Unlock()
				return nil, errors.New("Error: Cannot delete an ACL that is in use")
			}
		}
		delete(f.acls, name)
		f.mu.Unlock()
		f.landOnce.Do(func() { close(f.landed) })
		return nil, nil

	case strings.HasPrefix(key, "init "):
		f.mu.Lock()
		f.exists = true
		f.mu.Unlock()
		return nil, nil

	case key == "start "+racedStartMachine:
		f.mu.Lock()
		devices := make([]string, 0, len(f.devices))
		for _, cfg := range f.devices {
			if cfg["type"] == "nic" && cfg["network"] != "" {
				devices = append(devices, cfg["network"])
			}
		}
		f.mu.Unlock()
		if len(devices) == 0 {
			// The cold path: eth0 is the --network of the init.
			devices = []string{racedStartNet}
		}
		if err := f.plug(devices...); err != nil {
			return nil, err
		}
		f.mu.Lock()
		f.running = true
		f.mu.Unlock()
		return nil, nil

	case strings.HasPrefix(key, "config device add "+racedStartMachine+" ") &&
		strings.Contains(key, " nic network="):
		network := key[strings.Index(key, " nic network=")+len(" nic network="):]
		if err := f.plug(network); err != nil {
			return nil, err
		}
		f.mu.Lock()
		f.devices[fmt.Sprintf("eth%d", len(f.devices))] =
			map[string]string{"type": "nic", "network": network}
		f.mu.Unlock()
		return nil, nil

	case strings.HasPrefix(key, "config device set "+racedStartMachine+" ") &&
		strings.Contains(key, " ipv4.address="):
		// As measured on the station: ipv4.address is not a key an OVN NIC
		// updates in place, so the daemon removes and re-adds the device,
		// which re-plugs its OVN port and resolves the network's ACL
		// references exactly as an add does.
		fields := strings.Fields(key)
		device := fields[4]
		address := strings.TrimPrefix(fields[5], "ipv4.address=")
		f.mu.Lock()
		network := f.devices[device]["network"]
		f.mu.Unlock()
		if err := f.plug(network); err != nil {
			return nil, err
		}
		f.mu.Lock()
		f.devices[device]["ipv4.address"] = address
		f.mu.Unlock()
		return nil, nil
	}
	return nil, nil
}

// fakeStartdNetworkErr keeps every emulator network present: the detach's
// gone-question must answer "still there", or it would take the ErrNetworkGone
// door and never reach the delete this race is about.
func fakeStartdNetworkErr(string) error { return nil }

func (f *fakeStartd) plugFailures() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.failures...)
}

func (f *fakeStartd) isRunning() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}

// The reproduction of the suite's red runs: a first boot (init, then `start`,
// which plugs eth0's OVN port) and the isolation detach of the machine's
// network, issued concurrently the way a peering acceptance meets a burst of
// Vm creates, must take turns: the start must never resolve a rule set the
// detach deleted inside it.
//
// The interleaving is staged: the detach starts at the exact moment the plug
// begins, and the plug waits long enough for the detach's ACL delete to land
// inside it if nothing orders the two. With the ordering in place the detach
// waits its turn, and both orders of the two winners are acceptable — what
// must never happen is the third order, the delete inside the plug.
func TestAStartAndAnIsolationDetachTakeTurns(t *testing.T) {
	f := newFakeStartd(false)
	d := NewIncusOVN()
	d.runner = f.run

	var wg sync.WaitGroup
	var startErr, detachErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, startErr = d.Start(context.Background(), Spec{
			Name:        racedStartMachine,
			Image:       "alpine:3.21",
			Attachments: []Attachment{{Network: racedStartNet}},
		})
	}()
	go func() {
		defer wg.Done()
		<-f.plugBegun
		detachErr = d.IsolateNetwork(context.Background(), racedStartNet, nil)
	}()
	wg.Wait()

	if failures := f.plugFailures(); len(failures) != 0 {
		t.Fatalf("the isolation detach landed inside the start:\n%s\nstart: %v\ndetach: %v",
			strings.Join(failures, "\n"), startErr, detachErr)
	}
	if startErr != nil {
		t.Fatalf("the start failed: %v", startErr)
	}
	if detachErr != nil && !errors.Is(detachErr, ErrNetworkGone) {
		t.Fatalf("the detach failed for a reason other than the network being gone: %v", detachErr)
	}
	if !f.isRunning() {
		t.Fatal("the machine never came up")
	}
}

// The same turn-taking on the poweron door: a machine that already exists is
// started again through the same `incus start`, which re-plugs every one of
// its OVN ports and resolves the same references. Every pack's poweron
// arrives through this branch (#549), so a detach must wait for it too.
func TestARestartAndAnIsolationDetachTakeTurns(t *testing.T) {
	f := newFakeStartd(true)
	d := NewIncusOVN()
	d.runner = f.run
	// The route restoration that follows a poweron polls the guest; the fake
	// has no guest, so let it give up immediately — its failure is logged,
	// never fatal, and is not what this test is about.
	d.routePoll = time.Millisecond
	d.routeBudget = 2 * time.Millisecond

	var wg sync.WaitGroup
	var startErr, detachErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, startErr = d.Start(context.Background(), Spec{
			Name:        racedStartMachine,
			Image:       "alpine:3.21",
			Attachments: []Attachment{{Network: racedStartNet}},
		})
	}()
	go func() {
		defer wg.Done()
		<-f.plugBegun
		detachErr = d.IsolateNetwork(context.Background(), racedStartNet, nil)
	}()
	wg.Wait()

	if failures := f.plugFailures(); len(failures) != 0 {
		t.Fatalf("the isolation detach landed inside the restart:\n%s\nstart: %v\ndetach: %v",
			strings.Join(failures, "\n"), startErr, detachErr)
	}
	if startErr != nil {
		t.Fatalf("the restart failed: %v", startErr)
	}
	if detachErr != nil && !errors.Is(detachErr, ErrNetworkGone) {
		t.Fatalf("the detach failed for a reason other than the network being gone: %v", detachErr)
	}
}

// And on the extra interfaces: the second attachment is added after the
// start, onto a machine that is by then running, so the add plugs an OVN
// port on its own network and resolves that network's references the same
// way. A detach of that network must wait for the add.
func TestAnExtraInterfaceAndAnIsolationDetachTakeTurns(t *testing.T) {
	f := newFakeStartd(false)
	f.plugOn = racedStartNet2
	d := NewIncusOVN()
	d.runner = f.run

	var wg sync.WaitGroup
	var startErr, detachErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, startErr = d.Start(context.Background(), Spec{
			Name:  racedStartMachine,
			Image: "alpine:3.21",
			Attachments: []Attachment{
				{Network: racedStartNet},
				{Network: racedStartNet2},
			},
		})
	}()
	go func() {
		defer wg.Done()
		<-f.plugBegun
		detachErr = d.IsolateNetwork(context.Background(), racedStartNet2, nil)
	}()
	wg.Wait()

	if failures := f.plugFailures(); len(failures) != 0 {
		t.Fatalf("the isolation detach landed inside the interface add:\n%s\nstart: %v\ndetach: %v",
			strings.Join(failures, "\n"), startErr, detachErr)
	}
	if startErr != nil {
		t.Fatalf("the start failed: %v", startErr)
	}
	if detachErr != nil && !errors.Is(detachErr, ErrNetworkGone) {
		t.Fatalf("the detach failed for a reason other than the network being gone: %v", detachErr)
	}
}

// The fourth call site of the same daemon behaviour, and the one the packs'
// Join drives: Attach adds a NIC to a machine that is already running, so the
// add plugs an OVN port and resolves the network's ACL references inside it.
// Measured raw on the station before being staged (2026-08-28, Incus 7.2,
// OVN): a detach fired 25–200 ms into a ~400 ms `config device add` killed 11
// adds of 14 with `Cannot find security ACL ID for "iso-…"`. The attach lock
// Attach already holds is per machine; the detach never takes it, so it
// orders nothing here — only the network's lock does.
func TestAHotAttachAndAnIsolationDetachTakeTurns(t *testing.T) {
	f := newFakeStartd(true)
	f.running = true
	f.plugOn = racedStartNet2
	d := NewIncusOVN()
	d.runner = f.run

	var wg sync.WaitGroup
	var attachErr, detachErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		attachErr = d.Attach(context.Background(), racedStartMachine,
			Attachment{Network: racedStartNet2})
	}()
	go func() {
		defer wg.Done()
		<-f.plugBegun
		detachErr = d.IsolateNetwork(context.Background(), racedStartNet2, nil)
	}()
	wg.Wait()

	if failures := f.plugFailures(); len(failures) != 0 {
		t.Fatalf("the isolation detach landed inside the hot attach:\n%s\nattach: %v\ndetach: %v",
			strings.Join(failures, "\n"), attachErr, detachErr)
	}
	if attachErr != nil {
		t.Fatalf("the attach failed: %v", attachErr)
	}
	if detachErr != nil && !errors.Is(detachErr, ErrNetworkGone) {
		t.Fatalf("the detach failed for a reason other than the network being gone: %v", detachErr)
	}
}

// And on Attach's other mutating branch: moving a NIC to a different address
// is a `config device set ipv4.address=…`, which under OVN removes and
// re-adds the device — a re-plug that resolves the same references (measured
// raw: 6 moves of 8 killed by a detach fired 50 ms or later into a ~10 s
// set). The detach of that network must wait for the move too.
func TestAnAddressMoveAndAnIsolationDetachTakeTurns(t *testing.T) {
	f := newFakeStartd(true)
	f.running = true
	f.plugOn = racedStartNet2
	f.devices["eth1"] = map[string]string{
		"type": "nic", "network": racedStartNet2, "ipv4.address": "10.0.9.20",
	}
	d := NewIncusOVN()
	d.runner = f.run

	var wg sync.WaitGroup
	var attachErr, detachErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		attachErr = d.Attach(context.Background(), racedStartMachine,
			Attachment{Network: racedStartNet2, Address: "10.0.9.30"})
	}()
	go func() {
		defer wg.Done()
		<-f.plugBegun
		detachErr = d.IsolateNetwork(context.Background(), racedStartNet2, nil)
	}()
	wg.Wait()

	if failures := f.plugFailures(); len(failures) != 0 {
		t.Fatalf("the isolation detach landed inside the address move:\n%s\nattach: %v\ndetach: %v",
			strings.Join(failures, "\n"), attachErr, detachErr)
	}
	if attachErr != nil {
		t.Fatalf("the attach failed: %v", attachErr)
	}
	if detachErr != nil && !errors.Is(detachErr, ErrNetworkGone) {
		t.Fatalf("the detach failed for a reason other than the network being gone: %v", detachErr)
	}
}
