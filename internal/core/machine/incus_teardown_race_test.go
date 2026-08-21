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

// The teardown race of #386, staged rather than bet on.
//
// `mise run evidence:update` failed twice in bridge mode, each time on a subnet
// of examples/stacks/outscale/main.tf (10.50.1.0/24, then 10.50.2.0/24), and
// each failure was preceded in the emulator log by:
//
//	detach isolation from fnt-…: open /var/lib/incus/networks/…/dnsmasq.raw:
//	no such file or directory
//
// One request's isolation reconciliation reaching a network another request had
// already deleted. `terraform destroy` removes subnets in parallel: request A
// deletes its subnet from the store and calls RemoveNetwork; request B listed
// the store before that delete, so its reconciliation still names A's network
// and reaches it afterwards. The config edit then lands on a network the daemon
// no longer knows, and what it leaves behind is the interface and its dnsmasq —
// the leftover #316 sweeps, #342 taught doctor to name and #375 refused on the
// doorstep, none of which touched what produces it.
//
// fakeNetd below is the daemon's behaviour as it was observed, not a line-by-
// line replay of it: an update re-applies the network — bridge up, DHCP service
// (re)started — and only then opens dnsmasq.raw inside the state directory. A
// delete that landed in between has taken the directory with it, so the open
// fails, and nothing puts back down what the re-apply had just brought up. That
// is the one property the model needs: after the failed update, a service is
// running for a network that no longer exists.

// fakeNetd is the half of the Incus daemon the race lives in.
type fakeNetd struct {
	mu sync.Mutex
	// exists is the network object together with its state directory under
	// /var/lib/incus/networks; a delete removes both at once.
	exists map[string]bool
	// service is a dnsmasq bound to the network's bridge.
	service map[string]bool
	calls   []string

	// landed closes when a delete has been applied, so an update in flight can
	// meet it instead of hoping to.
	landed chan struct{}
	// touched closes on the very first command this driver issues, whichever
	// path issues it. It is what lets the delete start at the moment the
	// isolation pass has begun, with the fix in place and with it removed: the
	// first command is the existence probe in one case and the config edit in
	// the other, and either is the signal.
	touched   chan struct{}
	landOnce  sync.Once
	touchOnce sync.Once
	// beat is how long an update waits for a delete to cross it. Long enough
	// that a racing delete is inside rather than merely likely to be, short
	// enough that the serialised run costs it only once.
	beat time.Duration
}

func newFakeNetd(networks ...string) *fakeNetd {
	f := &fakeNetd{
		exists:  map[string]bool{},
		service: map[string]bool{},
		landed:  make(chan struct{}),
		touched: make(chan struct{}),
		beat:    200 * time.Millisecond,
	}
	for _, name := range networks {
		f.exists[name] = true
		f.service[name] = true
	}
	return f
}

func (f *fakeNetd) run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, strings.Join(args, " "))
	f.mu.Unlock()
	f.touchOnce.Do(func() { close(f.touched) })

	joined := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(joined, "query /1.0/networks/"):
		name := strings.TrimPrefix(joined, "query /1.0/networks/")
		if !f.has(name) {
			return nil, errors.New("Error: Network not found")
		}
		return []byte(`{"type":"bridge","config":{"ipv4.address":"10.50.2.1/24","user.feint":"outscale"}}`), nil

	case len(args) == 3 && args[0] == "network" && args[1] == "delete":
		f.mu.Lock()
		f.exists[args[2]] = false
		f.service[args[2]] = false
		f.mu.Unlock()
		f.landOnce.Do(func() { close(f.landed) })
		return nil, nil

	case len(args) >= 3 && args[0] == "network" && (args[1] == "set" || args[1] == "unset"):
		return f.update(args[2])
	}
	return nil, nil
}

// update is the daemon's config edit: the network is re-applied first, and its
// DHCP configuration written second.
func (f *fakeNetd) update(name string) ([]byte, error) {
	select {
	case <-f.landed:
	case <-time.After(f.beat):
	}
	f.mu.Lock()
	// Brought back up by the re-apply, whether or not the object survives, and
	// never taken down again by the failure below.
	f.service[name] = true
	alive := f.exists[name]
	f.mu.Unlock()
	if !alive {
		return nil, fmt.Errorf("open /var/lib/incus/networks/%s/dnsmasq.raw: no such file or directory", name)
	}
	return nil, nil
}

func (f *fakeNetd) has(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exists[name]
}

// orphans names every network whose DHCP service outlived it: the leftover, as
// `feint doctor` and `feint clean --check` describe it.
func (f *fakeNetd) orphans() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for name, running := range f.service {
		if running && !f.exists[name] {
			out = append(out, name)
		}
	}
	return out
}

func (f *fakeNetd) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeNetd) issued(substr string) bool {
	for _, cmd := range f.commands() {
		if strings.Contains(cmd, substr) {
			return true
		}
	}
	return false
}

func fakeNetdDriver(f *fakeNetd) *Incus {
	d := NewIncus()
	d.runner = f.run
	return d
}

// The reproduction. A detach and a delete of one network, issued concurrently
// the way two parallel destroys issue them, must not leave a DHCP service for a
// network that is gone — whichever of the two runs first.
//
// Both orders are covered by the one assertion, and both are reachable here:
// with the exclusion in place the detach may win and complete against a live
// network, or the delete may win and the detach then finds nothing to detach
// from. What must never happen is the third order, where the delete lands
// inside the detach.
func TestAnIsolationDetachDoesNotOrphanTheNetworkBeingDeleted(t *testing.T) {
	const network = "fnt-a1b2c3d4e5f"

	f := newFakeNetd(network)
	d := fakeNetdDriver(f)

	var wg sync.WaitGroup
	var detachErr, deleteErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		// Empty foreign list: the detach branch, which is the one the log line
		// of #386 names ("detach isolation from fnt-…"). It is what a
		// reconciliation applies to a subnet whose only neighbours are its own
		// Net's, which is every subnet of examples/stacks/outscale.
		detachErr = d.IsolateNetwork(context.Background(), network, nil)
	}()
	go func() {
		defer wg.Done()
		<-f.touched
		deleteErr = d.RemoveNetwork(context.Background(), network)
	}()
	wg.Wait()

	if orphans := f.orphans(); len(orphans) != 0 {
		t.Fatalf("a DHCP service outlived its network: %v\ncommands:\n%s\ndetach: %v\ndelete: %v",
			orphans, strings.Join(f.commands(), "\n"), detachErr, deleteErr)
	}
	// The delete is the operation that must not be defeated: a network the
	// control plane says is gone has to be gone.
	if deleteErr != nil {
		t.Fatalf("the delete failed: %v", deleteErr)
	}
	if f.has(network) {
		t.Fatalf("the network survived its own delete:\n%s", strings.Join(f.commands(), "\n"))
	}
	// And the detach either did its work or said it could not, never both and
	// never neither.
	if detachErr != nil && !errors.Is(detachErr, ErrNetworkGone) {
		t.Fatalf("the detach failed for a reason other than the network being gone: %v", detachErr)
	}
}

// The other half, sequential and therefore exact: a detach that arrives after
// the delete has already run issues no config edit at all, and says so.
//
// The lock cannot cover this one — the delete is over, there is nothing left to
// exclude — which is why the question is asked as well as the order kept.
func TestIsolationRefusesANetworkWhoseDeleteAlreadyRan(t *testing.T) {
	const network = "fnt-a1b2c3d4e5f"

	f := newFakeNetd(network)
	d := fakeNetdDriver(f)

	if err := d.RemoveNetwork(context.Background(), network); err != nil {
		t.Fatalf("remove: %v", err)
	}

	err := d.IsolateNetwork(context.Background(), network, nil)
	if !errors.Is(err, ErrNetworkGone) {
		t.Fatalf("isolate after delete = %v, want ErrNetworkGone: a detach that could not happen "+
			"must be reported, never reported as done", err)
	}
	if f.issued("network unset " + network) {
		t.Fatalf("the driver edited a deleted network anyway:\n%s", strings.Join(f.commands(), "\n"))
	}
	if f.issued("network set " + network) {
		t.Fatalf("the driver edited a deleted network anyway:\n%s", strings.Join(f.commands(), "\n"))
	}
	if orphans := f.orphans(); len(orphans) != 0 {
		t.Fatalf("a DHCP service outlived its network: %v\n%s", orphans, strings.Join(f.commands(), "\n"))
	}
}

// The attach branch takes the same refusal. It is the one that writes a rule set
// before it touches the network, so a driver that only guarded the detach would
// still create an ACL for a network nobody can attach it to.
func TestIsolationWithForeignBlocksRefusesADeletedNetworkToo(t *testing.T) {
	const network = "fnt-a1b2c3d4e5f"

	f := newFakeNetd(network)
	d := fakeNetdDriver(f)

	if err := d.RemoveNetwork(context.Background(), network); err != nil {
		t.Fatalf("remove: %v", err)
	}
	err := d.IsolateNetwork(context.Background(), network, []string{"10.50.1.0/24"})
	if !errors.Is(err, ErrNetworkGone) {
		t.Fatalf("isolate after delete = %v, want ErrNetworkGone", err)
	}
	if f.issued("network acl create") {
		t.Fatalf("a rule set was written for a network that is gone:\n%s", strings.Join(f.commands(), "\n"))
	}
}

// The rule set goes down with the network it isolated (#386). Nothing
// reconciles a network that no longer exists, and since the pass that used to
// drop it now refuses to run against a deleted one, the teardown has to.
func TestRemoveNetworkDropsTheIsolationRuleSetWithIt(t *testing.T) {
	const network = "fnt-a1b2c3d4e5f"

	f := newFakeNetd(network)
	d := fakeNetdDriver(f)

	if err := d.RemoveNetwork(context.Background(), network); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !f.issued("network acl delete " + isolationACL(network)) {
		t.Fatalf("the isolation rule set was left behind:\n%s", strings.Join(f.commands(), "\n"))
	}
	// After the network, never before: a rule set a live network still carries
	// cannot be deleted, and Incus refuses it as in use.
	cmds := f.commands()
	deleteAt, aclAt := -1, -1
	for i, cmd := range cmds {
		if cmd == "network delete "+network {
			deleteAt = i
		}
		if strings.HasPrefix(cmd, "network acl delete ") {
			aclAt = i
		}
	}
	if aclAt < deleteAt {
		t.Fatalf("the rule set was dropped before the network:\n%s", strings.Join(cmds, "\n"))
	}
}
