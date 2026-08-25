package machine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// The peering half of #456, staged against a daemon that answers the way the
// real one was measured to answer.
//
// Two facts of Incus 7.2 drive everything here, and both were measured on this
// station with three real OVN networks and nothing concurrent, before a line of
// this file existed. They are stated in the fake below because a model that got
// either wrong would make these tests agree with a driver that cannot work.
//
//  1. A create completes a peering by looking for a *pending* half aiming at the
//     network the create lands on, and the lookup names only that target — not
//     which network holds the row (v7.2.0, driver_ovn.go, PeerCreate). So:
//
//     incus network peer create A B B  -> pending                        rc=0
//     incus network peer create C B B  -> pending                        rc=0
//     incus network peer create B A A  -> Error: Failed creating peer:
//     More than one matching network peer was found                      rc=1
//
//     (A,B) and (C,B) are different pairs. A lock keyed by the pair would have
//     allowed exactly this, which is why the driver locks each end.
//
//  2. Deleting one peer clears the target of every row aiming at the network the
//     delete runs on, whatever pair it belongs to. In a three-network mesh,
//     `incus network peer delete B C` left A holding
//     {"name":"B","target_network":null,"status":"Errored"} — and the runtime
//     then answers "A peer for that name already exists" to any attempt to
//     declare that half again, which the code before #456 read as success.

const (
	peerPending = "Pending"
	peerErrored = "Errored"
)

// peerRecord is one row of the daemon's networks_peers table, in the two states
// the API distinguishes plus the wreck a delete leaves.
type peerRecord struct {
	name   string
	target string // the target's name while pending; blanked when the row is wrecked
	status string
}

// fakePeerd is the peering half of the daemon.
type fakePeerd struct {
	mu       sync.Mutex
	rows     map[string][]peerRecord
	networks []string
	// unlabelled names the networks that carry no label of this emulator's: an
	// operator's own, which happen to be spelled like ours.
	unlabelled map[string]bool
	calls      []string

	// beat is how long a create that only left a pending half takes to answer.
	// It is the window a second declaration has to arrive in.
	beat time.Duration
	// gate makes that window deterministic: a pending create waits for a second
	// one instead of hoping to meet it. With the driver's locks in place no
	// second one can arrive for a network already being paired, so the wait
	// costs the beat; without them the two meet, and the state the runtime
	// refuses is reached every time rather than sometimes.
	gate    bool
	waiters []chan struct{}
}

func newFakePeerd(networks ...string) *fakePeerd {
	f := &fakePeerd{
		rows:       map[string][]peerRecord{},
		networks:   networks,
		unlabelled: map[string]bool{},
		beat:       100 * time.Millisecond,
	}
	return f
}

func peerDriver(f *fakePeerd) *Incus {
	d := NewIncus()
	d.runner = f.run
	d.OVN = true
	return d
}

func (f *fakePeerd) run(_ context.Context, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	f.mu.Lock()
	f.calls = append(f.calls, joined)
	f.mu.Unlock()

	switch {
	case joined == "query /1.0/networks?recursion=1":
		return f.networkList()
	case strings.HasPrefix(joined, "query /1.0/networks/") && strings.HasSuffix(joined, "/peers?recursion=1"):
		name := strings.TrimSuffix(strings.TrimPrefix(joined, "query /1.0/networks/"), "/peers?recursion=1")
		return f.peerList(name)
	case strings.HasPrefix(joined, "query /1.0/networks/"):
		// The existence probe IsolateNetwork makes before it edits anything.
		return []byte(`{"type": "ovn"}`), nil
	case strings.HasPrefix(joined, "network peer create "):
		return f.peerCreate(args[3], args[4], args[5])
	case strings.HasPrefix(joined, "network peer delete "):
		return f.peerDelete(args[3], args[4])
	case strings.HasPrefix(joined, "network get ") && strings.Contains(joined, "user."+LabelKey):
		if f.unlabelled[args[2]] {
			return []byte("\n"), nil
		}
		return []byte("outscale\n"), nil
	case strings.HasPrefix(joined, "network acl show "), strings.HasPrefix(joined, "network acl delete "):
		// No rule set stands in these fixtures: the isolation half is measured
		// in incus_ovn_isolate_test.go, and an absent one is what both the
		// create path and RemoveFirewall are written for.
		return nil, errors.New("Error: Network ACL not found")
	}
	return []byte("{}"), nil
}

func (f *fakePeerd) networkList() ([]byte, error) {
	type wire struct {
		Name   string            `json:"name"`
		Type   string            `json:"type"`
		Config map[string]string `json:"config"`
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]wire, 0, len(f.networks))
	for i, name := range f.networks {
		out = append(out, wire{Name: name, Type: "ovn", Config: map[string]string{
			"ipv4.address": fmt.Sprintf("10.185.%d.1/24", i+1),
		}})
	}
	return json.Marshal(out)
}

func (f *fakePeerd) peerList(network string) ([]byte, error) {
	type wire struct {
		Name   string  `json:"name"`
		Target *string `json:"target_network"`
		Status string  `json:"status"`
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := f.rows[network]
	out := make([]wire, 0, len(rows))
	for _, row := range rows {
		item := wire{Name: row.name, Status: row.status}
		if row.target != "" {
			target := row.target
			item.Target = &target
		}
		out = append(out, item)
	}
	return json.Marshal(out)
}

// peerCreate is fact 1 above, in code.
func (f *fakePeerd) peerCreate(from, to, name string) ([]byte, error) {
	f.mu.Lock()
	for _, row := range f.rows[from] {
		if row.name == name {
			f.mu.Unlock()
			return nil, errors.New("Error: Failed creating peer: A peer for that name already exists")
		}
		if row.target == to {
			f.mu.Unlock()
			return nil, errors.New("Error: Failed creating peer: A peer for that target network already exists")
		}
	}
	// Every pending half aiming at the network this create lands on, wherever it
	// lives. This is the filter with no clause on the row's own network.
	matches := 0
	holder, at := "", -1
	for network, rows := range f.rows {
		for i, row := range rows {
			if row.status == peerPending && row.target == from {
				matches++
				holder, at = network, i
			}
		}
	}
	if matches > 1 {
		f.mu.Unlock()
		return nil, errors.New("Error: Failed creating peer: More than one matching network peer was found")
	}
	if matches == 1 {
		f.rows[holder][at].status = peerCreated
		f.rows[from] = append(f.rows[from], peerRecord{name: name, target: to, status: peerCreated})
		f.mu.Unlock()
		return []byte("created"), nil
	}
	f.rows[from] = append(f.rows[from], peerRecord{name: name, target: to, status: peerPending})
	wait := f.pendingWindowLocked()
	f.mu.Unlock()
	wait()
	return []byte("pending"), nil
}

// pendingWindowLocked returns the wait a create that left a pending half takes
// before it answers. The caller holds the lock; the wait runs without it.
func (f *fakePeerd) pendingWindowLocked() func() {
	if !f.gate {
		beat := f.beat
		return func() { time.Sleep(beat) }
	}
	mine := make(chan struct{})
	f.waiters = append(f.waiters, mine)
	if len(f.waiters) > 1 {
		for _, waiter := range f.waiters {
			close(waiter)
		}
		f.waiters = nil
	}
	beat := f.beat
	return func() {
		select {
		case <-mine:
		case <-time.After(beat):
		}
	}
}

// peerDelete is fact 2 above, in code: the row goes, and every row aiming at
// this network loses its target.
func (f *fakePeerd) peerDelete(network, name string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := f.rows[network]
	at := -1
	for i, row := range rows {
		if row.name == name {
			at = i
			break
		}
	}
	if at < 0 {
		return nil, errors.New("Error: Network peer not found")
	}
	f.rows[network] = append(rows[:at:at], rows[at+1:]...)
	for _, other := range f.rows {
		for i := range other {
			if other[i].status == peerCreated && other[i].target == network {
				other[i].target = ""
				other[i].status = peerErrored
			}
		}
	}
	return []byte("deleted"), nil
}

// established is what the driver claims when PeerNetworks returns: both ends
// hold a row aiming at the other, and the daemon calls both Created.
func (f *fakePeerd) established(a, b string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.halfLocked(a, b) && f.halfLocked(b, a)
}

func (f *fakePeerd) halfLocked(from, to string) bool {
	for _, row := range f.rows[from] {
		if row.target == to && row.status == peerCreated {
			return true
		}
	}
	return false
}

func (f *fakePeerd) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakePeerd) table() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b strings.Builder
	for _, network := range f.networks {
		for _, row := range f.rows[network] {
			fmt.Fprintf(&b, "  on %s: name=%s target=%q %s\n", network, row.name, row.target, row.status)
		}
	}
	if b.Len() == 0 {
		return "  (no peering at all)\n"
	}
	return b.String()
}

// mesh drives what ReconcileIsolation drives: every member of one Net declares
// every other as its peer, and the members appear at once because a stack
// creates its subnets in parallel.
func mesh(t *testing.T, d *Incus, members []string) []error {
	t.Helper()
	var wg sync.WaitGroup
	failures := make(chan error, len(members))
	for i, member := range members {
		wg.Add(1)
		go func(i int, member string) {
			defer wg.Done()
			peers := make([]string, 0, len(members)-1)
			for j, other := range members {
				if i != j {
					peers = append(peers, other)
				}
			}
			if err := d.PeerNetworks(context.Background(), member, peers); err != nil {
				failures <- fmt.Errorf("PeerNetworks(%s, %v): %w", member, peers, err)
			}
		}(i, member)
	}
	wg.Wait()
	close(failures)
	var errs []error
	for err := range failures {
		errs = append(errs, err)
	}
	return errs
}

// TestConcurrentPairsOfOneNetDoNotCollideOnAPendingHalf is #456 itself: a Net
// with three subnets, whose networks are declared at once.
//
// Twelve of these lines were logged in one apply of a stranger's stack, naming
// six subnets, and the emulator carried on at ERROR — so the API kept describing
// a Net whose subnets route to each other while the runtime had no peering at
// all between them.
//
// Two subnets never showed it, and that is the shape of the defect rather than
// luck: one pair has one pending half at a time, and it takes two of them aiming
// at one network to make a create fail. The gate in the fake makes the meeting
// certain instead of likely.
func TestConcurrentPairsOfOneNetDoNotCollideOnAPendingHalf(t *testing.T) {
	members := []string{"fnt-aaa", "fnt-bbb", "fnt-ccc"}
	f := newFakePeerd(members...)
	f.gate = true
	d := peerDriver(f)

	for _, err := range mesh(t, d, members) {
		t.Errorf("a subnet of the Net could not be peered: %v", err)
	}

	for i, a := range members {
		for _, b := range members[i+1:] {
			if !f.established(a, b) {
				t.Errorf("%s and %s are not peered at both ends; the Net's subnets do not reach each other:\n%s",
					a, b, f.table())
			}
		}
	}
}

// The accepting half, and the reason the exclusion is per network rather than
// one lock over all peering: a Net's subnets are created in parallel on purpose.
//
// Two pairs that share no network must not wait for each other. Serialised, the
// pair below costs two creates each, one after the other; per network, it costs
// one round. This is the bound #348 was measured against, one layer along.
func TestPeeringTwoDisjointPairsDoesNotQueue(t *testing.T) {
	f := newFakePeerd("fnt-aaa", "fnt-bbb", "fnt-ccc", "fnt-ddd")
	f.beat = 300 * time.Millisecond
	d := peerDriver(f)

	var wg sync.WaitGroup
	start := time.Now()
	for _, pair := range [][2]string{{"fnt-aaa", "fnt-bbb"}, {"fnt-ccc", "fnt-ddd"}} {
		wg.Add(1)
		go func(pair [2]string) {
			defer wg.Done()
			if err := d.PeerNetworks(context.Background(), pair[0], []string{pair[1]}); err != nil {
				t.Errorf("peer %s with %s: %v", pair[0], pair[1], err)
			}
		}(pair)
	}
	wg.Wait()
	elapsed := time.Since(start)

	if limit := f.beat * 3 / 2; elapsed > limit {
		t.Fatalf("two peerings sharing no network took %v, over %v: they queued behind one lock, "+
			"which is the global lock serialise.go refuses — a Net's subnets are declared in parallel", elapsed, limit)
	}
	if !f.established("fnt-aaa", "fnt-bbb") || !f.established("fnt-ccc", "fnt-ddd") {
		t.Errorf("the pairs were fast and not peered:\n%s", f.table())
	}
}

// TestAWreckedHalfIsRebuiltRatherThanReportedApplied holds the second measured
// fact: a half whose target the runtime blanked cannot be declared again under
// its own name, and the answer it gives says "already exists", which the driver
// used to accept as the work being done.
//
// The fixture is what a station holds after one subnet of a Net is deleted: the
// pair that was not named in the delete, with one end still Created and the
// other wrecked.
func TestAWreckedHalfIsRebuiltRatherThanReportedApplied(t *testing.T) {
	f := newFakePeerd("fnt-aaa", "fnt-bbb")
	f.rows["fnt-aaa"] = []peerRecord{{name: "fnt-bbb", target: "", status: peerErrored}}
	f.rows["fnt-bbb"] = []peerRecord{{name: "fnt-aaa", target: "fnt-aaa", status: peerCreated}}
	d := peerDriver(f)

	if err := d.PeerNetworks(context.Background(), "fnt-aaa", []string{"fnt-bbb"}); err != nil {
		t.Fatalf("peer: %v", err)
	}
	if !f.established("fnt-aaa", "fnt-bbb") {
		t.Fatalf("the peering was reported applied and is not there:\n%s\nthe driver ran:\n%s",
			f.table(), strings.Join(f.commands(), "\n"))
	}
}

// TestASecondPendingHalfIsReconciledRatherThanReported is the belt the lock does
// not replace: a pending half left on the host by a crashed run, or by a version
// of this driver without the locks, makes every create on the network it aims at
// fail — and nothing a user can type repairs it, because the runtime refuses the
// create that would.
func TestASecondPendingHalfIsReconciledRatherThanReported(t *testing.T) {
	f := newFakePeerd("fnt-aaa", "fnt-bbb", "fnt-ccc")
	// The residue: fnt-ccc declared a half aiming at fnt-bbb and nothing ever
	// completed it.
	f.rows["fnt-ccc"] = []peerRecord{{name: "fnt-bbb", target: "fnt-bbb", status: peerPending}}
	d := peerDriver(f)

	if err := d.PeerNetworks(context.Background(), "fnt-aaa", []string{"fnt-bbb"}); err != nil {
		t.Fatalf("peer: %v", err)
	}
	if !f.established("fnt-aaa", "fnt-bbb") {
		t.Fatalf("the pair was not peered:\n%s\nthe driver ran:\n%s",
			f.table(), strings.Join(f.commands(), "\n"))
	}
	// And the half that was cleared is one that carried nothing: pending grants
	// no traffic, and the reconciliation that owns it declares it again.
	for _, row := range f.rows["fnt-ccc"] {
		if row.status == peerPending && row.target == "fnt-bbb" {
			t.Errorf("the pending half that blocked the create is still there:\n%s", f.table())
		}
	}
}

// TestPeeringRefusesToRebuildOnAStrangersNetworkNamedLikeOurs is the guard on
// the destructive half of the repair, and it is the one a name cannot answer.
//
// `fnt-` is a prefix anybody may type. An operator with an OVN network of their
// own under that name, peered with one of ours by hand, must not have this
// driver delete peerings on theirs to rebuild ours — which is the guard #455
// added to the sweep, on a path of exactly this family.
func TestPeeringRefusesToRebuildOnAStrangersNetworkNamedLikeOurs(t *testing.T) {
	f := newFakePeerd("fnt-aaa", "fnt-lab")
	f.unlabelled["fnt-lab"] = true
	// Both ends carry a half that must be rebuilt: ours is a wreck, theirs holds
	// the name the rebuild needs.
	f.rows["fnt-aaa"] = []peerRecord{{name: "fnt-lab", target: "", status: peerErrored}}
	f.rows["fnt-lab"] = []peerRecord{{name: "fnt-aaa", target: "fnt-aaa", status: peerCreated}}
	d := peerDriver(f)

	err := d.PeerNetworks(context.Background(), "fnt-aaa", []string{"fnt-lab"})
	if err == nil {
		t.Error("a peering the driver could not make was reported as made")
	}
	for _, cmd := range f.commands() {
		if strings.HasPrefix(cmd, "network peer delete fnt-lab") {
			t.Errorf("the driver deleted a peering on a network nobody here created: %q\n"+
				"the name carries our prefix and the network is not ours; only the label says so", cmd)
		}
	}
}

// The accepting half of the same guard, in the same run: a network that does
// carry the label is rebuilt, or the refusal above would be indistinguishable
// from a driver that repairs nothing.
func TestPeeringRebuildsOnOurOwnNetwork(t *testing.T) {
	f := newFakePeerd("fnt-aaa", "fnt-bbb")
	f.rows["fnt-aaa"] = []peerRecord{{name: "fnt-bbb", target: "", status: peerErrored}}
	f.rows["fnt-bbb"] = []peerRecord{{name: "fnt-aaa", target: "fnt-aaa", status: peerCreated}}
	d := peerDriver(f)

	if err := d.PeerNetworks(context.Background(), "fnt-aaa", []string{"fnt-bbb"}); err != nil {
		t.Fatalf("peer: %v", err)
	}
	if len(matchingIn(f.commands(), "network peer delete fnt-bbb fnt-aaa")) == 0 {
		t.Errorf("the far half was never taken down, so the rebuild had nothing to declare:\n%s",
			strings.Join(f.commands(), "\n"))
	}
	if !f.established("fnt-aaa", "fnt-bbb") {
		t.Errorf("the pair was not rebuilt:\n%s", f.table())
	}
}

// A pair both ends already call Created costs two reads and no command at all.
// Without this, every reconciliation pass would tear a working mesh down and
// build it again, which is a peering that flaps for as long as the Net lives.
func TestAnEstablishedPeeringIsLeftAlone(t *testing.T) {
	f := newFakePeerd("fnt-aaa", "fnt-bbb")
	f.rows["fnt-aaa"] = []peerRecord{{name: "fnt-bbb", target: "fnt-bbb", status: peerCreated}}
	f.rows["fnt-bbb"] = []peerRecord{{name: "fnt-aaa", target: "fnt-aaa", status: peerCreated}}
	d := peerDriver(f)

	if err := d.PeerNetworks(context.Background(), "fnt-aaa", []string{"fnt-bbb"}); err != nil {
		t.Fatalf("peer: %v", err)
	}
	for _, cmd := range f.commands() {
		if strings.HasPrefix(cmd, "network peer delete") || strings.HasPrefix(cmd, "network peer create") {
			t.Errorf("an established peering was rebuilt: %q", cmd)
		}
	}
}

func matchingIn(commands []string, substr string) []string {
	var out []string
	for _, cmd := range commands {
		if strings.Contains(cmd, substr) {
			out = append(out, cmd)
		}
	}
	return out
}
