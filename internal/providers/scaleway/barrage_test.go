package scaleway_test

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/stephrobert/feint/internal/core/store/storetest"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// newBarrageServer is newRuntimeTestServer with a handle on the store, so the
// sweep can read what the barrage left behind.
//
// The identifier generator is atomic for the reason newRuntimeTestServer already
// gives beside its own: the sequential one races the moment a test issues
// concurrent calls, and a barrage that hands out one identifier twice would make
// the sweep report the harness rather than the pack. Measured, not assumed —
// this repository's most expensive recurring defect is the instrument, not the
// subject.
func newBarrageServer(t *testing.T, drv machine.Driver) (*httptest.Server, *store.Store) {
	t.Helper()

	var seq atomic.Int64
	seq.Store(1_000_000)
	st := store.New()
	env := &emulator.Env{
		Store:    st,
		Machines: drv,
		Now:      func() time.Time { return time.Unix(1700000000, 0).UTC() },
		NewID: func() string {
			return fmt.Sprintf("00000000-0000-4000-8000-%012d", seq.Add(1))
		},
	}
	srv, err := emulator.NewServer(env, scaleway.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

// barrageRuntime is a driver that takes a moment and refuses a name it has
// already given out, the way Incus refuses an instance whose name exists.
//
// Both properties are load-bearing, and falsification proved it. The store-side
// barrage alone could not see machine.Binding.Serialise disappear: with no
// runtime there is nothing slow between the read and the write, so two poweron
// requests write the same value and nothing is lost. Give the start a moment and
// a duplicate-name refusal, and the missing lock becomes a server the API
// reports as failed with a machine still running — the orphan CLAUDE.md records.
//
// It never blocks: a barrage that waits to be released measures the release.
type barrageRuntime struct {
	mu       sync.Mutex
	machines map[string]bool
	starts   atomic.Int32
}

func newBarrageRuntime() *barrageRuntime {
	return &barrageRuntime{machines: map[string]bool{}}
}

func (b *barrageRuntime) Name() string                   { return "barrage" }
func (b *barrageRuntime) Available(context.Context) bool { return true }

func (b *barrageRuntime) Start(_ context.Context, spec machine.Spec) (machine.Machine, error) {
	b.starts.Add(1)
	// The window the lock exists to close. Short enough that the suite stays
	// fast, long enough that a scheduler running ten workers interleaves.
	time.Sleep(time.Millisecond)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.machines[spec.Name] {
		return machine.Machine{}, errors.New(`Failed creating instance record: Add instance info to the database: This "instances" entry already exists`)
	}
	b.machines[spec.Name] = true
	return machine.Machine{Name: spec.Name, IP: "10.42.0.7", Running: true}, nil
}

func (b *barrageRuntime) Stop(_ context.Context, n string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.machines[n] = false
	return nil
}

func (b *barrageRuntime) Remove(_ context.Context, n string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.machines, n)
	return nil
}

func (b *barrageRuntime) Inspect(_ context.Context, n string) (machine.Machine, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	running, known := b.machines[n]
	if !known {
		return machine.Machine{}, false, nil
	}
	return machine.Machine{Name: n, IP: "10.42.0.7", Running: running}, true, nil
}

func (b *barrageRuntime) EnsureNetwork(context.Context, machine.NetworkSpec) error { return nil }
func (b *barrageRuntime) Attach(context.Context, string, machine.Attachment) error { return nil }
func (b *barrageRuntime) RemoveNetwork(context.Context, string) error              { return nil }

// leftRunning reports the machines the runtime still holds, which after a
// barrage that deleted everything must be none.
func (b *barrageRuntime) leftRunning() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for name, running := range b.machines {
		if running {
			out = append(out, name)
		}
	}
	return out
}

// doRaw is do() without the testing.T: a goroutine may not call t.Fatalf, and a
// helper that does turns a failure into undefined behaviour.
func doRaw(ts *httptest.Server, method, path, body string) (int, map[string]any) {
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if err != nil {
		return -1, nil
	}
	req.Header.Set("X-Auth-Token", "irrelevant-the-emulator-ignores-it")
	resp, err := ts.Client().Do(req)
	if err != nil {
		return -1, nil
	}
	defer func() { _ = resp.Body.Close() }()
	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp.StatusCode, decoded
}

// The barrage: what a Terraform apply does, which no other test here stages.
//
// Every concurrency test in this pack races one path against itself —
// TestConcurrentPowerOnStartsTheMachineOnce here, its equivalents in
// internal/providers/outscale and internal/providers/exoscale, and the rest. They hold defects that were once real and they are worth having.
// What none of them can see, by construction, is two *different* paths, each
// correctly locked on its own target, jointly breaking an invariant that spans
// resources: an address the flexible pool issued and the subnet allocator issued
// too, an identifier minted twice under load, a machine name claimed by two
// servers.
//
// So this drives the served routes the way a client does — parallelism 10 is
// Terraform's default — and then asks one question of the whole store, through
// the provider-neutral sweep in internal/core/store/storetest.
//
// It is not a load test. Nothing here measures throughput, and the counts are
// the smallest that make an interleaving likely rather than the largest a laptop
// survives: a scenario nobody runs is a scenario nobody fixes.

// IPAM is regional, and it is the product that hands out the addresses this
// barrage contends for.
const ipamURL = "/ipam/v1/regions/fr-par"

const (
	barrageWorkers = 10
	barrageRounds  = 4
)

// TestABarrageLeavesTheStoreCoherent fails without the per-target locking the
// three packs share (machine.Binding.Serialise) and without the address
// allocator's own lock.
func TestABarrageLeavesTheStoreCoherent(t *testing.T) {
	runtime := newBarrageRuntime()
	ts, st := newBarrageServer(t, runtime)

	var wg sync.WaitGroup
	// Errors are collected rather than reported from the goroutines: t.Fatalf
	// off the test goroutine is undefined, and this pack has already paid for a
	// failure path that hung instead of failing.
	problems := make(chan string, barrageWorkers*barrageRounds*8)

	for w := range barrageWorkers {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for round := range barrageRounds {
				tag := fmt.Sprintf("w%d-r%d", worker, round)
				barrageCycle(ts, tag, problems)
			}
		}(w)
	}
	wg.Wait()
	close(problems)

	// A barrage where every request failed would sweep clean and prove nothing.
	// The count of refusals is therefore part of the verdict, not noise.
	var refused []string
	for p := range problems {
		refused = append(refused, p)
	}
	if len(refused) > 0 {
		t.Errorf("%d request(s) the barrage did not expect to fail:\n%s",
			len(refused), strings.Join(firstFew(refused, 5), "\n"))
	}

	if found := storetest.Sweep(st.All(), nil, nil); len(found) != 0 {
		t.Errorf("the store is incoherent after the barrage:\n%s", strings.Join(found, "\n"))
	}

	// An exclusive resource has one live owner. Sweep cannot ask that on its own
	// — it does not know which key names an owner — so the pack declares it and
	// the invariant lives in the core (#214, #215).
	if found := storetest.Orphans(st.All(), scaleway.Owns, nil); len(found) != 0 {
		t.Errorf("the barrage left resources naming an owner that is gone:\n%s",
			strings.Join(found, "\n"))
	}

	// The other half of the same invariant, and the one the store cannot answer
	// on its own: the API describes nothing, so nothing must be running. An
	// orphan here is a machine on the operator's host that no client can find,
	// which is what the per-target lock exists to prevent.
	if left := runtime.leftRunning(); len(left) != 0 {
		t.Errorf("the API describes no server and %d machine(s) are still running: %v", len(left), left)
	}
}

// barrageCycle is what one worker does: reserve an address, create a server
// carrying it, take a snapshot of its root volume, then tear the lot down. Every
// step goes through the served routes, because a barrage against internal
// functions would prove the functions and not the API.
func barrageCycle(ts *httptest.Server, tag string, problems chan<- string) {
	status, ip := doRaw(ts, "POST", zoneURL+"/ips", `{}`)
	if status != http.StatusCreated {
		problems <- fmt.Sprintf("%s: ip create answered %d", tag, status)
		return
	}
	ipObj, _ := ip["ip"].(map[string]any)
	ipID, _ := ipObj["id"].(string)

	status, created := doRaw(ts, "POST", zoneURL+"/servers",
		`{"name":"barrage-`+tag+`","commercial_type":"DEV1-S","public_ips":["`+ipID+`"]}`)
	if status != http.StatusCreated {
		problems <- fmt.Sprintf("%s: server create answered %d", tag, status)
		return
	}
	serverObj, _ := created["server"].(map[string]any)
	serverID, _ := serverObj["id"].(string)
	volumes, _ := serverObj["volumes"].(map[string]any)
	root, _ := volumes["0"].(map[string]any)
	rootID, _ := root["id"].(string)

	// A private network and a NIC on it: the second address pool.
	//
	// Correction, because the claim that stood here was false. It said this
	// block made the NIC allocator's lock falsifiable. An audit re-ran the
	// mutation and got thirty green runs, and found the structural reason: every
	// worker-round takes its own /24 through subnetOctet, so two requests never
	// dispute one pool and no contention defect can show here, whatever the
	// block drives.
	//
	// What holds that lock is TestConcurrentAttachmentsGetDistinctAddresses,
	// which does fail under the same mutation. This block still earns its place —
	// it exercises the route under load and feeds the sweep — but it proves
	// coverage, not exclusion.
	status, pn := doRaw(ts, "POST", regionURL+"/private-networks",
		`{"name":"barrage-`+tag+`","subnets":["10.`+subnetOctet(tag)+`.0.0/24"]}`)
	pnID := ""
	if status == http.StatusOK {
		pnID, _ = pn["id"].(string)
	}
	if pnID != "" {
		if status, _ := doRaw(ts, "POST", zoneURL+"/servers/"+serverID+"/private_nics",
			`{"private_network_id":"`+pnID+`"}`); status != http.StatusCreated {
			problems <- fmt.Sprintf("%s: private nic create answered %d", tag, status)
		}
	}

	// A second path on the same resources, which is where a cross-path defect
	// would live: snapshotting the root volume while other workers create and
	// delete servers.
	if rootID != "" {
		if status, _ := doRaw(ts, "POST", zoneURL+"/snapshots",
			`{"name":"barrage-`+tag+`","volume_id":"`+rootID+`"}`); status != http.StatusCreated {
			problems <- fmt.Sprintf("%s: snapshot create answered %d", tag, status)
		}
	}

	// Two concurrent power-ons of the same server, which is the shape CLAUDE.md
	// records as once-real: the loser used to overwrite the winner's state and
	// leave an orphan the API called "stopped".
	var inner sync.WaitGroup
	for range 2 {
		inner.Add(1)
		go func() {
			defer inner.Done()
			doRaw(ts, "POST", zoneURL+"/servers/"+serverID+"/action", `{"action":"poweron"}`)
		}()
	}
	inner.Wait()

	// Both power-ons are asked for, so the server must be running: the loser is
	// serialised behind the winner and finds the machine already there. Without
	// the per-target lock the loser reaches the runtime, is refused the name,
	// and the server ends in a failed state — which nothing here noticed until
	// falsification showed the barrage staying green with the lock removed. The
	// published state is the one the effect produced, and asserting it is what
	// makes that a control rather than a sentence.
	if status, got := doRaw(ts, "GET", zoneURL+"/servers/"+serverID, ""); status == http.StatusOK {
		server, _ := got["server"].(map[string]any)
		if state, _ := server["state"].(string); state != "running" {
			problems <- fmt.Sprintf("%s: after two concurrent poweron the server is %q, want running", tag, state)
		}
	}

	doRaw(ts, "POST", zoneURL+"/servers/"+serverID+"/action", `{"action":"poweroff"}`)
	if status, _ := doRaw(ts, "DELETE", zoneURL+"/servers/"+serverID, ""); status >= 400 {
		problems <- fmt.Sprintf("%s: server delete answered %d", tag, status)
	}
	doRaw(ts, "DELETE", zoneURL+"/ips/"+ipID, "")
	if pnID != "" {
		doRaw(ts, "DELETE", regionURL+"/private-networks/"+pnID, "")
	}
}

// subnetOctet gives each worker-round its own /24 inside 10.0.0.0/8, so the
// barrage measures concurrent allocation rather than the overlap refusal, which
// has its own test.
func subnetOctet(tag string) string {
	sum := 0
	for _, c := range tag {
		sum = (sum*31 + int(c)) % 250
	}
	return fmt.Sprintf("%d", sum+1)
}

func firstFew(all []string, n int) []string {
	if len(all) <= n {
		return all
	}
	return append(all[:n:n], fmt.Sprintf("… and %d more", len(all)-n))
}

// A restore landing in the middle of live traffic must end in one of the two
// worlds, never a merge of both.
//
// This is the case no per-path test can stage: PUT /_feint/state replaces the
// whole store while creates are in flight, and the two writers are correct on
// their own. What must not happen is a store holding half the snapshot and half
// the traffic — a world no client ever asked for, and one where a resource can
// reference another that the other world deleted.
func TestARestoreDuringTrafficLandsInOneWorld(t *testing.T) {
	runtime := newBarrageRuntime()
	ts, st := newBarrageServer(t, runtime)

	// A world worth restoring, captured before the traffic starts.
	_, before := doRaw(ts, "POST", zoneURL+"/ips", `{}`)
	beforeIP, _ := before["ip"].(map[string]any)
	beforeID, _ := beforeIP["id"].(string)

	var snapshot bytes.Buffer
	if err := st.Snapshot(&snapshot); err != nil {
		t.Fatalf("take the snapshot to restore: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	// Traffic, until the restore has landed and a little after.
	for w := range 4 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				doRaw(ts, "POST", zoneURL+"/servers",
					fmt.Sprintf(`{"name":"during-%d-%d","commercial_type":"DEV1-S"}`, worker, i))
			}
		}(w)
	}

	// The restore, through the served route rather than the store directly:
	// that is the path an operator takes, and the one that has to hold.
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/_feint/state", bytes.NewReader(snapshot.Bytes()))
	if err != nil {
		close(stop)
		wg.Wait()
		t.Fatalf("build the restore: %v", err)
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		close(stop)
		wg.Wait()
		t.Fatalf("restore during traffic: %v", err)
	}
	_ = res.Body.Close()
	status := res.StatusCode
	close(stop)
	wg.Wait()

	if status != http.StatusOK {
		t.Fatalf("the restore answered %d, so this test measured nothing", status)
	}

	// Whatever the interleaving produced, the store must be coherent: no
	// identifier issued twice, no address held twice, no runtime object claimed
	// twice. A merge of two worlds is exactly what breaks those.
	if found := storetest.Sweep(st.All(), nil, nil); len(found) != 0 {
		t.Errorf("a restore during traffic left the store incoherent:\n%s", strings.Join(found, "\n"))
	}

	// And the restored world is present: the snapshot's own resource survived,
	// or was overwritten by later traffic — but the store is not empty, which
	// would mean the restore landed and then lost to a racing write of the
	// whole map.
	if _, found := st.Get("scaleway", "instance/ip", beforeID); !found && st.Len() == 0 {
		t.Error("the restore landed in no world at all: the store is empty")
	}
}

// The contention the first barrage could not stage.
//
// Its workers each took their own /24, so two requests never disputed one pool
// and no allocator defect could show. An audit proved the blindness by removing
// the private-NIC lock: thirty green runs. It also found the defect the blindness
// was hiding — releasing an address took no lock at all, while booking one did.
//
// So this one puts every worker on a single network, and drives the write half
// of ipam/v1 that nothing exercised: book, attach through a NIC, release.
func TestASharedNetworkUnderBarrageNeverHandsOutOneAddressTwice(t *testing.T) {
	runtime := newBarrageRuntime()
	ts, st := newBarrageServer(t, runtime)

	// One network for everybody. A /24 leaves 250-odd addresses, so exhaustion
	// is not what this measures.
	status, pn := doRaw(ts, "POST", regionURL+"/private-networks",
		`{"name":"contended","subnets":["10.199.0.0/24"]}`)
	if status != http.StatusOK {
		t.Fatalf("create the shared network: %d (%v)", status, pn)
	}
	pnID, _ := pn["id"].(string)

	var wg sync.WaitGroup
	problems := make(chan string, barrageWorkers*barrageRounds*6)

	for w := range barrageWorkers {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for round := range barrageRounds {
				tag := fmt.Sprintf("w%d-r%d", worker, round)

				// A booked address, released again — the path that frees an
				// address, run against the paths that take one.
				status, booked := doRaw(ts, "POST", ipamURL+"/ips",
					`{"source":{"private_network_id":"`+pnID+`"}}`)
				if status != http.StatusOK && status != http.StatusCreated {
					problems <- fmt.Sprintf("%s: book answered %d", tag, status)
					continue
				}
				bookedID, _ := booked["id"].(string)

				// A server with a NIC on the same network, which allocates from
				// the very pool the release is handing back to.
				status, server := doRaw(ts, "POST", zoneURL+"/servers",
					`{"name":"contend-`+tag+`","commercial_type":"DEV1-S"}`)
				if status != http.StatusCreated {
					problems <- fmt.Sprintf("%s: server create answered %d", tag, status)
					doRaw(ts, "DELETE", ipamURL+"/ips/"+bookedID, "")
					continue
				}
				srv, _ := server["server"].(map[string]any)
				serverID, _ := srv["id"].(string)

				// The NIC takes the address this worker booked, through
				// ipam_ip_ids — what the Terraform provider does. Without it the
				// NIC allocates a fresh address, the release disputes nothing,
				// and the barrage is blind: proved by falsification, five green
				// runs with the release lock removed.
				//
				// The release then races the attach on the *same* address, which
				// is the sequence an audit walked and no test staged.
				var inner sync.WaitGroup
				inner.Add(2)
				go func() {
					defer inner.Done()
					doRaw(ts, "POST", zoneURL+"/servers/"+serverID+"/private_nics",
						`{"private_network_id":"`+pnID+`","ipam_ip_ids":["`+bookedID+`"]}`)
				}()
				go func() {
					defer inner.Done()
					doRaw(ts, "DELETE", ipamURL+"/ips/"+bookedID, "")
				}()
				inner.Wait()
				doRaw(ts, "POST", zoneURL+"/servers/"+serverID+"/action", `{"action":"poweroff"}`)
				doRaw(ts, "DELETE", zoneURL+"/servers/"+serverID, "")
			}
		}(w)
	}
	wg.Wait()
	close(problems)

	var refused []string
	for p := range problems {
		refused = append(refused, p)
	}
	if len(refused) > 0 {
		t.Errorf("%d request(s) the barrage did not expect to fail:\n%s",
			len(refused), strings.Join(firstFew(refused, 5), "\n"))
	}

	// The invariant the contention exists to test: one address, one resource.
	if found := storetest.Sweep(st.All(), nil, nil); len(found) != 0 {
		t.Errorf("one pool under contention produced an incoherent store:\n%s",
			strings.Join(found, "\n"))
	}
}
