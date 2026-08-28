package outscale_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/providers/outscale"
)

// The defects a whole-pack audit found on paths the conformance suite does not
// walk. Each test fails without its fix; each fix was falsified in a copy
// outside the repository.
//
// They live in one file on purpose: they are not a feature, they are the record
// of what the suite could not see, and a later reader deciding whether the suite
// is broad enough should be able to read them together.

// post is a raw call, without the contract check the other file's helper does:
// these tests assert on refusals and on state, and several of them expect a
// non-200.
func post(t *testing.T, ts *httptest.Server, action, body string) (int, map[string]any) {
	t.Helper()
	res, err := http.Post(ts.URL+"/api/v1/"+action, "application/json", strings.NewReader(body)) //nolint:noctx // test client
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	defer func() { _ = res.Body.Close() }()
	var decoded map[string]any
	_ = json.NewDecoder(res.Body).Decode(&decoded)
	return res.StatusCode, decoded
}

func netAndSubnet(t *testing.T, ts *httptest.Server, cidr, subnet string) (netID, subnetID string) {
	t.Helper()
	_, out := post(t, ts, "CreateNet", `{"IpRange":"`+cidr+`"}`)
	n, _ := out["Net"].(map[string]any)
	netID, _ = n["NetId"].(string)
	_, out = post(t, ts, "CreateSubnet", `{"NetId":"`+netID+`","IpRange":"`+subnet+`"}`)
	s, _ := out["Subnet"].(map[string]any)
	subnetID, _ = s["SubnetId"].(string)
	return netID, subnetID
}

// Two concurrent creates must not receive the same address.
//
// The pack carries a mutex whose comment names this exact case — "Terraform
// creates ten resources at a time by default" — and the Vm path never took it,
// while the Net and Subnet paths always did. An audit ran twelve concurrent
// creates and got one address twice; with a runtime that is two containers
// configured with the same static IP, which defeats the addressing claim this
// pack is built on.
func TestConcurrentCreatesDoNotShareAnAddress(t *testing.T) {
	ts := newServer(t)
	_, subnetID := netAndSubnet(t, ts, "10.42.0.0/16", "10.42.1.0/24")

	const creates = 12
	var wg sync.WaitGroup
	addresses := make([]string, creates)
	for i := range creates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, out := post(t, ts, "CreateVms",
				`{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","BootOnCreation":false}`)
			vms, _ := out["Vms"].([]any)
			if len(vms) == 0 {
				return
			}
			vm, _ := vms[0].(map[string]any)
			addresses[i], _ = vm["PrivateIp"].(string)
		}()
	}
	wg.Wait()

	seen := map[string]int{}
	for _, a := range addresses {
		if a != "" {
			seen[a]++
		}
	}
	for address, n := range seen {
		if n > 1 {
			t.Errorf("%s was handed to %d machines", address, n)
		}
	}
	if len(seen) < creates {
		t.Errorf("only %d of %d creates produced an address", len(seen), creates)
	}
}

// A Subnet must not delete under the machines placed in it.
//
// deleteNet has always refused while subnets remained; deleteSubnet checked
// nothing, so an audit deleted a subnet under a running Vm and then its Net,
// while ReadVms went on naming both. With a runtime it tore down the backing
// network under attached machines.
func TestASubnetDoesNotDeleteUnderAVm(t *testing.T) {
	ts := newServer(t)
	_, subnetID := netAndSubnet(t, ts, "10.43.0.0/16", "10.43.1.0/24")

	post(t, ts, "CreateVms", `{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","BootOnCreation":false}`)

	status, out := post(t, ts, "DeleteSubnet", `{"SubnetId":"`+subnetID+`"}`)
	if status == http.StatusOK {
		t.Fatalf("the subnet deleted under a live Vm: %v", out)
	}

	// And it is still there, rather than half deleted.
	_, out = post(t, ts, "ReadSubnets", `{}`)
	subnets, _ := out["Subnets"].([]any)
	if len(subnets) != 1 {
		t.Errorf("the refused delete changed the store: %d subnets", len(subnets))
	}
}

// An error answer must not leave machines running behind it.
//
// The create loop stored and booted per iteration and returned mid-way with no
// unwind: an audit asked for fourteen machines in a /28 that holds eleven, got
// an error, and found eleven running machines the client was never told about.
func TestAFailedCreateLeavesNothingBehind(t *testing.T) {
	ts := newServer(t)
	_, subnetID := netAndSubnet(t, ts, "10.44.0.0/16", "10.44.1.0/28")

	status, _ := post(t, ts, "CreateVms",
		`{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","MaxVmsCount":14,"BootOnCreation":false}`)
	if status == http.StatusOK {
		t.Skip("the subnet was large enough; nothing to unwind")
	}

	_, out := post(t, ts, "ReadVms", `{}`)
	vms, _ := out["Vms"].([]any)
	if len(vms) != 0 {
		t.Errorf("an error answer left %d machines running", len(vms))
	}
}

// DryRun must change nothing.
//
// It does not validate: the answer is issued before the handler runs, so a dry
// run of a malformed request still answers 200. That is a known gap over the
// real API, recorded in docs/limits.md rather than claimed away here — the
// property this test holds is the one that matters for a host, which is that
// nothing happens.
//
// The field was declared on six request structs and read by none of them, so
// `CreateNet --DryRun` created the Net and `CreateSubnet --DryRun` created a
// bridge on the operator's host. A declared-but-unread field is invisible to the
// unread-field report, which is what makes this class worth its own test.
func TestDryRunChangesNothing(t *testing.T) {
	ts := newServer(t)

	status, out := post(t, ts, "CreateNet", `{"IpRange":"10.45.0.0/16","DryRun":true}`)
	if status != http.StatusOK {
		t.Fatalf("a dry run should validate, not refuse: %d %v", status, out)
	}
	if _, created := out["Net"]; created {
		t.Error("the dry run answered with a Net it should not have created")
	}
	_, out = post(t, ts, "ReadNets", `{}`)
	nets, _ := out["Nets"].([]any)
	if len(nets) != 0 {
		t.Errorf("the dry run created %d Net(s)", len(nets))
	}
}

// AvailableIpsCount must follow the machines.
//
// It was built from a fresh allocator with nothing reserved, so a /24 holding
// three machines still answered 251 — while the view's own comment claimed the
// count is computed from the mask *and* what is allocated. The suite only ever
// read empty subnets, so it proved the half that worked.
func TestAvailableIpsCountFollowsTheMachines(t *testing.T) {
	ts := newServer(t)
	_, subnetID := netAndSubnet(t, ts, "10.46.0.0/16", "10.46.1.0/24")

	_, out := post(t, ts, "ReadSubnets", `{}`)
	subnets, _ := out["Subnets"].([]any)
	first, _ := subnets[0].(map[string]any)
	empty, _ := first["AvailableIpsCount"].(float64)

	for range 3 {
		post(t, ts, "CreateVms", `{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","BootOnCreation":false}`)
	}

	_, out = post(t, ts, "ReadSubnets", `{}`)
	subnets, _ = out["Subnets"].([]any)
	after, _ := subnets[0].(map[string]any)
	got, _ := after["AvailableIpsCount"].(float64)

	if got != empty-3 {
		t.Errorf("AvailableIpsCount is %v after three machines, want %v", got, empty-3)
	}
}

// A keypair must refuse what is not a key.
//
// Anything was accepted, including a multi-line value that cloud-init refuses
// later — so the create succeeded and the machine booted holding the wrong bytes
// and refusing every login. The Scaleway pack refuses at entry; this is the same
// control, one API over, which is where the factorisation rule puts it.
func TestAKeypairRefusesWhatIsNotAKey(t *testing.T) {
	ts := newServer(t)
	for _, bad := range []string{
		"definitely not a key",
		"ssh-rsa AAAA\nruncmd:\n  - touch /tmp/pwned",
		// Well-shaped and not a key: the algorithm is real, the material is not
		// base64. Scaleway refused it and Outscale took it, which is what a
		// duplicated check does over time — and this test did not cover it, so
		// the mutation that removes the check survived until it did.
		"ssh-ed25519 !!!!not-base64-at-all!!!! user@host",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI= trailing-garbage-in-material",
		"",
	} {
		if bad == "" {
			continue // an absent key is legitimate: the API generates one
		}
		if status, _ := post(t, ts, "CreateKeypair", `{"KeypairName":"k","PublicKey":`+quote(bad)+`}`); status == http.StatusOK {
			t.Errorf("accepted %q as a public key", bad)
		}
	}
	// The accepting half: a real key must pass, or the check would only be a way
	// to refuse.
	// A real key, from ssh-keygen. The first version of this test used a
	// plausible-looking string whose material is not valid base64, and it
	// passed — because the pack did not check. Its own fixture was made of the
	// defect it was meant to hold.
	good := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIr6pEFlAFO3YU0DNW/r8SkpjdbptN9ockkO2BtIolSD conformance@feint"
	if status, out := post(t, ts, "CreateKeypair", `{"KeypairName":"good","PublicKey":`+quote(good)+`}`); status != http.StatusOK {
		t.Errorf("refused a real key: %d %v", status, out)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// A dry run must reach no handler at all.
//
// The first fix honoured DryRun in six request structs; Outscale declares it on
// all twenty served actions, so an audit ran `DeleteVms {"DryRun":true}` and
// watched the machine be destroyed. A control implemented per handler was
// missing from exactly the destructive ones.
func TestDryRunReachesNoHandler(t *testing.T) {
	ts := newServer(t)
	_, subnetID := netAndSubnet(t, ts, "10.47.0.0/16", "10.47.1.0/24")

	_, out := post(t, ts, "CreateVms", `{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","BootOnCreation":false}`)
	vms, _ := out["Vms"].([]any)
	vm, _ := vms[0].(map[string]any)
	vmID, _ := vm["VmId"].(string)

	// The destructive one, which the per-handler version did not cover.
	if status, _ := post(t, ts, "DeleteVms", `{"VmIds":["`+vmID+`"],"DryRun":true}`); status != http.StatusOK {
		t.Fatalf("a dry run should answer, not refuse: %d", status)
	}
	_, out = post(t, ts, "ReadVms", `{}`)
	if vms, _ = out["Vms"].([]any); len(vms) != 1 {
		t.Errorf("the dry run destroyed the machine: %d left", len(vms))
	}

	// And the creating one.
	if status, _ := post(t, ts, "CreateVms", `{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","DryRun":true}`); status != http.StatusOK {
		t.Fatalf("dry run create: status %d", status)
	}
	_, out = post(t, ts, "ReadVms", `{}`)
	if vms, _ = out["Vms"].([]any); len(vms) != 1 {
		t.Errorf("the dry run created a machine: %d now", len(vms))
	}

	// The answer must be a shape the contract allows: ResponseContext alone.
	// The first version added a top-level "DryRun", which the closed response
	// schemas reject.
	_, out = post(t, ts, "CreateNet", `{"IpRange":"10.48.0.0/16","DryRun":true}`)
	if _, invented := out["DryRun"]; invented {
		t.Error("the answer carries a field the response schema does not define")
	}
	if _, ok := out["ResponseContext"]; !ok {
		t.Error("the answer has no ResponseContext")
	}
}

// And it must not delete under a machine that lands during the check.
//
// The guard above reads the store, then deletes. Outside the addressing lock,
// a create sitting between its placement and its Put is invisible to that read:
// the Subnet goes out, the Vm lands a microsecond later, and the pack is back
// in the state the guard exists to prevent. Racing them is the only way to see
// it — the sequential test above passes either way, which is what made this
// worth its own case.
func TestASubnetDoesNotDeleteUnderARace(t *testing.T) {
	for round := range 60 {
		ts := newServer(t)
		_, subnetID := netAndSubnet(t, ts, "10.49.0.0/16", "10.49.1.0/24")

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			// Eight per batch: the widest window this test can open between a Vm's
			// placement and its Put. Still not wide enough to catch the missing
			// lock, which is why the comment above says so.
			post(t, ts, "CreateVms",
				`{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","MaxVmsCount":8,"BootOnCreation":false}`)
		}()
		go func() {
			defer wg.Done()
			post(t, ts, "DeleteSubnet", `{"SubnetId":"`+subnetID+`"}`)
		}()
		wg.Wait()

		// The invariant, whichever of the two won: no machine names a Subnet the
		// pack no longer serves.
		_, out := post(t, ts, "ReadSubnets", `{}`)
		subnets, _ := out["Subnets"].([]any)
		live := map[string]bool{}
		for _, s := range subnets {
			m, _ := s.(map[string]any)
			id, _ := m["SubnetId"].(string)
			live[id] = true
		}
		_, out = post(t, ts, "ReadVms", `{}`)
		vms, _ := out["Vms"].([]any)
		for _, v := range vms {
			m, _ := v.(map[string]any)
			if id, _ := m["SubnetId"].(string); id != "" && !live[id] {
				vmID, _ := m["VmId"].(string)
				t.Fatalf("round %d: %s sits in %s, which was deleted under it", round, vmID, id)
			}
		}
	}
}

// A create whose resource disappears leaves no machine behind.
//
// createVms started the machine outside the per-target lock every other
// lifecycle path takes, and when the commit failed it merely skipped the answer:
// the container went on running while the control plane described nothing. An
// audit obtained it with `PUT /_feint/state` carrying an empty list — a designed
// path, since internal/cli/snapshot.go documents the format as meant to be
// loaded into another instance.
//
// A machine the control plane does not describe is a machine nobody thinks to
// stop, which is the worst outcome this layer can produce.
//
// The fixture names a catalogue OMI on purpose. It used to say ami-12345678 as
// a placeholder, and the fallback booted the default image for it; since #83 an
// unknown identifier never reaches the runtime at all, so this test — whose
// subject is the vanished resource, not the resolution — must ask for an image
// that boots.
func TestACreateWhoseResourceVanishesLeavesNoMachineBehind(t *testing.T) {
	runtime := newBlockingRuntime()
	ts := newRuntimeServer(t, machine.Use(runtime))
	_, subnetID := netAndSubnet(t, ts, "10.51.0.0/16", "10.51.1.0/24")

	done := make(chan struct{})
	go func() {
		defer close(done)
		post(t, ts, "CreateVms", `{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`"}`)
	}()

	// The machine is starting; empty the store under it.
	<-runtime.entered
	// The snapshot envelope, not a bare array: a file with no format header is
	// refused since #133, and this fixture was one.
	empty := `{"format":"feint-snapshot","version":1,"resources":[]}`
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/_feint/state", strings.NewReader(empty))
	if err != nil {
		close(runtime.release)
		t.Fatalf("build the restore: %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		close(runtime.release)
		t.Fatalf("restore an empty state: %v", err)
	}
	_ = res.Body.Close()
	// Checked, because a refused restore would leave this test measuring
	// nothing while looking green.
	//
	// Every failure path above releases the runtime first, and that is not
	// tidiness: t.Fatalf unwinds this goroutine, the create is still blocked
	// inside Start, and httptest's Close waits for requests in flight. So a
	// refused restore used to hang the whole package until the test binary's
	// timeout — ten minutes of silence instead of one red line, met exactly
	// once, when #133 made this very restore start failing.
	if res.StatusCode != http.StatusOK {
		close(runtime.release)
		t.Fatalf("the restore answered %d; the store was not emptied", res.StatusCode)
	}
	close(runtime.release)
	<-done

	if left := runtime.running(); len(left) != 0 {
		t.Errorf("the API describes no machine and %v is still running on the host", left)
	}
}

// blockingRuntime holds Start until the test lets it go, so "while it was
// starting" is a fact rather than a probability.
type blockingRuntime struct {
	mu       sync.Mutex
	machines map[string]bool
	entered  chan string
	release  chan struct{}
}

func newBlockingRuntime() *blockingRuntime {
	return &blockingRuntime{
		machines: map[string]bool{},
		entered:  make(chan string, 8),
		release:  make(chan struct{}),
	}
}

func (f *blockingRuntime) Name() string                   { return "fake" }
func (f *blockingRuntime) Available(context.Context) bool { return true }

func (f *blockingRuntime) Start(_ context.Context, spec machine.Spec) (machine.Machine, error) {
	select {
	case f.entered <- spec.Name:
	default:
	}
	<-f.release
	f.mu.Lock()
	defer f.mu.Unlock()
	f.machines[spec.Name] = true
	return machine.Machine{Name: spec.Name, Running: true}, nil
}

func (f *blockingRuntime) Stop(_ context.Context, n string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.machines[n] = false
	return nil
}

func (f *blockingRuntime) Remove(_ context.Context, n string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.machines, n)
	return nil
}

func (f *blockingRuntime) Inspect(_ context.Context, n string) (machine.Machine, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if running, found := f.machines[n]; found {
		return machine.Machine{Name: n, Running: running}, true, nil
	}
	return machine.Machine{}, false, nil
}

func (f *blockingRuntime) EnsureNetwork(context.Context, machine.NetworkSpec) error { return nil }
func (f *blockingRuntime) Attach(context.Context, string, machine.Attachment) error { return nil }
func (f *blockingRuntime) RemoveNetwork(context.Context, string) error              { return nil }

func (f *blockingRuntime) running() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for name, up := range f.machines {
		if up {
			out = append(out, name)
		}
	}
	return out
}

func newRuntimeServer(t *testing.T, drv machine.Runtime) *httptest.Server {
	t.Helper()
	env := emulator.DefaultEnv()
	env.UseMachines(drv)
	srv, err := emulator.NewServer(env, outscale.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// Two concurrent starts reach the runtime once.
//
// The pack takes a per-target lock on its lifecycle paths, and its comment names
// a measured defect: two concurrent StartVms each launched a container, and the
// API described as stopped a machine that was running. Nothing held that.
// Removing both Serialise calls left the whole suite green, which is the state
// CLAUDE.md calls an intention written in the past tense.
func TestTwoConcurrentStartsReachTheRuntimeOnce(t *testing.T) {
	runtime := newCountingRuntime()
	runtime.blockStarts = make(chan struct{})
	ts := newRuntimeServer(t, machine.Use(runtime))
	_, subnetID := netAndSubnet(t, ts, "10.52.0.0/16", "10.52.1.0/24")

	_, out := post(t, ts, "CreateVms",
		`{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","BootOnCreation":false}`)
	vms, _ := out["Vms"].([]any)
	vm, _ := vms[0].(map[string]any)
	vmID, _ := vm["VmId"].(string)

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			post(t, ts, "StartVms", `{"VmIds":["`+vmID+`"]}`)
		}()
	}
	// The first start is inside the runtime. The pause is for the second to
	// reach either the lock (correct) or the runtime (the defect) before
	// anything is released: letting go as soon as the first enters lets the
	// second arrive after the first has committed, where the "already running"
	// short-circuit hides the missing lock. Measured: without the pause this
	// test passes with both Serialise calls removed.
	<-runtime.startEntered
	time.Sleep(50 * time.Millisecond)
	close(runtime.blockStarts)
	wg.Wait()

	if n := runtime.starts(vmID); n > 1 {
		t.Errorf("the runtime was asked to start %s %d times", vmID, n)
	}
	// And the machine really is running: a lock that refused both starts would
	// pass the check above.
	_, out = post(t, ts, "ReadVms", `{}`)
	vms, _ = out["Vms"].([]any)
	vm, _ = vms[0].(map[string]any)
	if state, _ := vm["State"].(string); state != "running" {
		t.Errorf("the machine is %q after two starts, want running", state)
	}
}

// UpdateVm refuses what CreateVms refuses.
//
// The cap and the keypair check lived in the create only, so an update took a
// 600 KiB user data and a keypair no keypair answers to. At the next start the
// machine boots with no key, nobody logs in, and the API states one is attached.
func TestUpdateVmValidatesWhatCreateValidates(t *testing.T) {
	ts := newServer(t)
	_, subnetID := netAndSubnet(t, ts, "10.53.0.0/16", "10.53.1.0/24")
	_, out := post(t, ts, "CreateVms",
		`{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","BootOnCreation":false}`)
	vms, _ := out["Vms"].([]any)
	vm, _ := vms[0].(map[string]any)
	vmID, _ := vm["VmId"].(string)

	if status, _ := post(t, ts, "UpdateVm",
		`{"VmId":"`+vmID+`","KeypairName":"ghost-does-not-exist"}`); status == http.StatusOK {
		t.Error("UpdateVm attached a keypair that does not exist")
	}
	oversized := strings.Repeat("A", 600*1024)
	if status, _ := post(t, ts, "UpdateVm",
		`{"VmId":"`+vmID+`","UserData":"`+oversized+`"}`); status == http.StatusOK {
		t.Error("UpdateVm took a UserData the create refuses")
	}
	// The accepting half, or the check would only be a way to refuse.
	if status, out := post(t, ts, "UpdateVm",
		`{"VmId":"`+vmID+`","UserData":"aGVsbG8="}`); status != http.StatusOK {
		t.Errorf("UpdateVm refused a legitimate change: %d %v", status, out)
	}
}

// A Net does not delete under a Subnet being created.
//
// deleteNet carried the dependency guard and took no lock, so its subnet list
// was empty by construction for the whole window a create was open: the Net
// went, the Subnet landed in it, and its bridge stayed on the host.
//
// Honest limit, measured: this test also passes with deleteNet's lock removed,
// because the sibling fix — reserving the subnet in the store before the runtime
// call rather than after — closed the wide window on its own. What remains is
// the few microseconds between subnetsOf and Delete, which nothing here can
// widen. So the lock is argued from symmetry with deleteSubnet and from that
// residual window; this test is the regression net over the invariant, and
// TestSubnetCreateDoesNotHoldTheAddressingLockAcrossTheRuntime is the one that
// dies under mutation.
func TestANetDoesNotDeleteUnderASubnetBeingCreated(t *testing.T) {
	runtime := newCountingRuntime()
	runtime.blockNetworks = make(chan struct{})
	ts := newRuntimeServer(t, machine.Use(runtime))

	_, out := post(t, ts, "CreateNet", `{"IpRange":"10.54.0.0/16"}`)
	n, _ := out["Net"].(map[string]any)
	netID, _ := n["NetId"].(string)

	done := make(chan struct{})
	go func() {
		defer close(done)
		post(t, ts, "CreateSubnet", `{"NetId":"`+netID+`","IpRange":"10.54.1.0/24"}`)
	}()

	<-runtime.networkEntered
	post(t, ts, "DeleteNet", `{"NetId":"`+netID+`"}`)
	close(runtime.blockNetworks)
	<-done

	// Whoever won, no Subnet may name a Net the pack no longer serves.
	_, out = post(t, ts, "ReadNets", `{}`)
	nets, _ := out["Nets"].([]any)
	live := map[string]bool{}
	for _, entry := range nets {
		m, _ := entry.(map[string]any)
		id, _ := m["NetId"].(string)
		live[id] = true
	}
	_, out = post(t, ts, "ReadSubnets", `{}`)
	subnets, _ := out["Subnets"].([]any)
	for _, entry := range subnets {
		m, _ := entry.(map[string]any)
		if id, _ := m["NetId"].(string); id != "" && !live[id] {
			t.Errorf("%v names %s, a Net the pack no longer serves", m["SubnetId"], id)
		}
	}
}

// Creating a Subnet does not hold the addressing lock across the runtime call.
//
// Holding it put every other create in a queue behind `incus network create`:
// measured at 1.5 s for one unrelated CreateNet with a fake driver, minutes with
// Incus. The rule is the repository's own, and it had been applied to the Vm
// path only.
func TestSubnetCreateDoesNotHoldTheAddressingLockAcrossTheRuntime(t *testing.T) {
	runtime := newCountingRuntime()
	runtime.blockNetworks = make(chan struct{})
	ts := newRuntimeServer(t, machine.Use(runtime))

	_, out := post(t, ts, "CreateNet", `{"IpRange":"10.55.0.0/16"}`)
	n, _ := out["Net"].(map[string]any)
	netID, _ := n["NetId"].(string)

	done := make(chan struct{})
	go func() {
		defer close(done)
		post(t, ts, "CreateSubnet", `{"NetId":"`+netID+`","IpRange":"10.55.1.0/24"}`)
	}()
	<-runtime.networkEntered

	// An unrelated create, while the runtime call is in flight. It must answer
	// rather than wait for it.
	answered := make(chan int, 1)
	go func() {
		status, _ := post(t, ts, "CreateNet", `{"IpRange":"10.56.0.0/16"}`)
		answered <- status
	}()
	select {
	case status := <-answered:
		if status != http.StatusOK {
			t.Errorf("the unrelated create answered %d", status)
		}
	case <-time.After(2 * time.Second):
		t.Error("an unrelated CreateNet waited on a runtime network call: the addressing lock is held across it")
	}
	close(runtime.blockNetworks)
	<-done
}

// countingRuntime records how often each machine was started, and can hold the
// network calls so a test can act while one is in flight.
type countingRuntime struct {
	mu     sync.Mutex
	counts map[string]int

	networkEntered chan string
	blockNetworks  chan struct{}
	networks       atomic.Int32

	// Holding Start is what makes "two at once" a fact rather than a
	// probability: with a fake driver the first start finishes before the
	// second looks, so an unserialised pack passes by luck.
	startEntered chan string
	blockStarts  chan struct{}
}

func newCountingRuntime() *countingRuntime {
	return &countingRuntime{
		counts:         map[string]int{},
		networkEntered: make(chan string, 8),
		startEntered:   make(chan string, 8),
	}
}

func (f *countingRuntime) Name() string                   { return "fake" }
func (f *countingRuntime) Available(context.Context) bool { return true }

func (f *countingRuntime) Start(_ context.Context, spec machine.Spec) (machine.Machine, error) {
	f.mu.Lock()
	f.counts[spec.Name]++
	f.mu.Unlock()
	if f.blockStarts != nil {
		select {
		case f.startEntered <- spec.Name:
		default:
		}
		<-f.blockStarts
	}
	return machine.Machine{Name: spec.Name, Running: true}, nil
}

func (f *countingRuntime) Stop(context.Context, string) error   { return nil }
func (f *countingRuntime) Remove(context.Context, string) error { return nil }

func (f *countingRuntime) Inspect(_ context.Context, n string) (machine.Machine, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.counts[n] > 0 {
		// With an address, the way a real runtime answers once the machine is
		// up. Without one, TestAStoppedVmKeepsItsPrivateAddress skipped itself.
		return machine.Machine{Name: n, Running: true, IP: "10.99.0.7"}, true, nil
	}
	return machine.Machine{}, false, nil
}

func (f *countingRuntime) EnsureNetwork(_ context.Context, spec machine.NetworkSpec) error {
	f.networks.Add(1)
	if f.blockNetworks != nil {
		select {
		case f.networkEntered <- spec.Name:
		default:
		}
		<-f.blockNetworks
	}
	return nil
}

func (f *countingRuntime) Attach(context.Context, string, machine.Attachment) error { return nil }
func (f *countingRuntime) RemoveNetwork(context.Context, string) error              { return nil }

// starts counts by Vm id rather than by machine name, so a test does not have to
// know the prefix the binding uses.
func (f *countingRuntime) starts(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	for name, n := range f.counts {
		if strings.HasSuffix(name, id) {
			return n
		}
	}
	return 0
}

// UpdateVm and a start do not overwrite each other.
//
// Commit replaces State, Runtime and Attrs wholesale. Without the per-target
// lock, an UpdateVm answering 200 wrote its UserData over whatever the start had
// just committed, or the other way round: the client is told the change landed
// and the store does not hold it, so Terraform re-proposes it on every plan.
func TestUpdateVmAndStartVmsDoNotOverwriteEachOther(t *testing.T) {
	runtime := newCountingRuntime()
	runtime.blockStarts = make(chan struct{})
	ts := newRuntimeServer(t, machine.Use(runtime))
	_, subnetID := netAndSubnet(t, ts, "10.57.0.0/16", "10.57.1.0/24")

	_, out := post(t, ts, "CreateVms",
		`{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","BootOnCreation":false}`)
	vms, _ := out["Vms"].([]any)
	vm, _ := vms[0].(map[string]any)
	vmID, _ := vm["VmId"].(string)

	started := make(chan struct{})
	go func() {
		defer close(started)
		post(t, ts, "StartVms", `{"VmIds":["`+vmID+`"]}`)
	}()
	<-runtime.startEntered // the start is in the runtime, holding what it holds

	updated := make(chan int, 1)
	go func() {
		status, _ := post(t, ts, "UpdateVm", `{"VmId":"`+vmID+`","UserData":"aGVsbG8gd29ybGQ="}`)
		updated <- status
	}()
	// Give the update time to land in the middle if it can.
	time.Sleep(50 * time.Millisecond)
	close(runtime.blockStarts)
	<-started
	status := <-updated

	if status != http.StatusOK {
		t.Fatalf("UpdateVm answered %d", status)
	}
	// The write it reported must be the write the store holds.
	_, out = post(t, ts, "ReadVms", `{}`)
	vms, _ = out["Vms"].([]any)
	vm, _ = vms[0].(map[string]any)
	if got, _ := vm["UserData"].(string); got != "aGVsbG8gd29ybGQ=" {
		t.Errorf("UpdateVm answered 200 and the store holds %q: the write was lost", got)
	}
}

// A body the server accepts reaches the handler whole.
//
// The DryRun probe reads the body before the handler does, then puts it back.
// It read a quarter of what emulator.DecodeJSON accepts and restored the
// truncated copy, so a valid 1.2 MiB request came back as
// {"Code":"4001","Details":"unexpected end of JSON input"} — a syntax error
// about a document the client sent whole, introduced by the fix that moved
// DryRun to the mount point.
//
// The probe now uses emulator.MaxBody, the one constant. This test sends more
// than the old bound and asserts the handler's own verdict: the user data cap,
// which only a handler that received the whole body can apply.
func TestABodyTheServerAcceptsReachesTheHandler(t *testing.T) {
	ts := newServer(t)
	_, subnetID := netAndSubnet(t, ts, "10.58.0.0/16", "10.58.1.0/24")
	_, out := post(t, ts, "CreateVms",
		`{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","BootOnCreation":false}`)
	vms, _ := out["Vms"].([]any)
	vm, _ := vms[0].(map[string]any)
	vmID, _ := vm["VmId"].(string)

	// Over the old 1 MiB probe, under the 4 MiB the server accepts.
	body := `{"VmId":"` + vmID + `","UserData":"` + strings.Repeat("A", 1500*1024) + `"}`
	status, answer := post(t, ts, "UpdateVm", body)

	if status != http.StatusBadRequest {
		t.Fatalf("a 1.5 MiB request answered %d, want the handler's 400", status)
	}
	// The details sit in Errors[0], which is the shape the pack's own error
	// writer produces; reading them at the root found "" and made this test
	// fail for the wrong reason.
	errs, _ := answer["Errors"].([]any)
	if len(errs) == 0 {
		t.Fatalf("no Errors in the answer: %v", answer)
	}
	first, _ := errs[0].(map[string]any)
	details, _ := first["Details"].(string)
	if strings.Contains(details, "unexpected end of JSON input") {
		t.Errorf("the body was truncated before the handler saw it: %q", details)
	}
	// The verdict must be the handler's own, which only a whole body produces.
	if !strings.Contains(details, "UserData") {
		t.Errorf("the answer is not the handler's verdict on the field: %q", details)
	}
}

// A filter this pack does not apply is refused, not ignored.
//
// The API description declares 66 filters on a Vm and the pack read one. Every
// other one returned the whole inventory with a 200: an audit sent
// `--Filters.SubnetIds[] subnet-deadbeef` against seven machines and got all
// seven back. A script that deletes what a filter matched would have deleted
// everything, and nothing in the answer would have said so.
func TestAnUnsupportedFilterIsRefused(t *testing.T) {
	ts := newServer(t)
	_, subnetID := netAndSubnet(t, ts, "10.60.0.0/16", "10.60.1.0/24")
	post(t, ts, "CreateVms", `{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","BootOnCreation":false}`)

	for _, probe := range []struct{ action, body string }{
		{"ReadVms", `{"Filters":{"Architectures":["x86_64"]}}`},
		{"ReadVms", `{"Filters":{"Tags":["a=b"]}}`},
		// DhcpOptionsSetIds used to be the probe here; it is served since the
		// DHCP lifecycle landed (#172) — the provider's own delete path filters
		// on it — and TestReadNetsFiltersOnTheDhcpOptionsSet now holds the
		// accepting half.
		{"ReadNets", `{"Filters":{"Tags":["a=b"]}}`},
		// SubregionNames was the probe here until it became a served filter
		// (#269); AvailableIpsCounts is declared by FiltersSubnet upstream and
		// still not applied, so it keeps the refusal measured.
		{"ReadSubnets", `{"Filters":{"AvailableIpsCounts":[251]}}`},
		{"ReadKeypairs", `{"Filters":{"TagKeys":["env"]}}`},
	} {
		status, out := post(t, ts, probe.action, probe.body)
		if status == http.StatusOK {
			t.Errorf("%s applied nothing and answered 200 for %s: %v", probe.action, probe.body, out)
			continue
		}
		// The refusal has to name the field, or a caller cannot act on it.
		errs, _ := out["Errors"].([]any)
		if len(errs) == 0 {
			t.Errorf("%s refused without saying why: %v", probe.action, out)
			continue
		}
		first, _ := errs[0].(map[string]any)
		details, _ := first["Details"].(string)
		if !strings.Contains(details, "filter") {
			t.Errorf("%s: the refusal does not mention the filter: %q", probe.action, details)
		}
	}
}

// The filters that are served actually filter.
//
// A guard that refuses everything passes every attack test and breaks the
// product, so the accepting half is asserted too — and each filter is asserted
// on a value that exists and one that does not, because a filter that always
// matches and a filter that never matches are equally useless.
func TestTheServedFiltersFilter(t *testing.T) {
	ts := newServer(t)
	_, subnetID := netAndSubnet(t, ts, "10.61.0.0/16", "10.61.1.0/24")
	_, out := post(t, ts, "CreateVms",
		`{"ImageId":"ami-11111111","SubnetId":"`+subnetID+`","VmType":"tinav6.c2r2p2","BootOnCreation":false}`)
	vms, _ := out["Vms"].([]any)
	vm, _ := vms[0].(map[string]any)
	vmID, _ := vm["VmId"].(string)
	post(t, ts, "CreateVms", `{"ImageId":"ami-22222222","SubnetId":"`+subnetID+`","BootOnCreation":false}`)

	count := func(body string) int {
		_, out := post(t, ts, "ReadVms", body)
		vms, _ := out["Vms"].([]any)
		return len(vms)
	}
	if n := count(`{}`); n != 2 {
		t.Fatalf("no filter returned %d machines, want 2", n)
	}
	for _, probe := range []struct {
		body string
		want int
	}{
		{`{"Filters":{"VmIds":["` + vmID + `"]}}`, 1},
		{`{"Filters":{"VmIds":["i-does-not-exist"]}}`, 0},
		{`{"Filters":{"ImageIds":["ami-11111111"]}}`, 1},
		{`{"Filters":{"ImageIds":["ami-99999999"]}}`, 0},
		{`{"Filters":{"VmTypes":["tinav6.c2r2p2"]}}`, 1},
		{`{"Filters":{"SubnetIds":["` + subnetID + `"]}}`, 2},
		{`{"Filters":{"SubnetIds":["subnet-deadbeef"]}}`, 0},
		// VmStateNames is what FiltersVm declares. This test drove VmStates for
		// a year — a filter FiltersVmsState declares for ReadVmsState and
		// FiltersVm does not have — so the emulator served an invented name and
		// refused the real one, and this test agreed with it. That is the
		// emulator proving itself against itself, and it is why the kind
		// control reads contracts/outscale.json instead of this file (#566).
		{`{"Filters":{"VmStateNames":["stopped"]}}`, 2},
		{`{"Filters":{"VmStateNames":["running"]}}`, 0},
		// Conjunctive, like upstream: both must hold.
		{`{"Filters":{"ImageIds":["ami-11111111"],"VmTypes":["tinav6.c2r2p2"]}}`, 1},
		{`{"Filters":{"ImageIds":["ami-11111111"],"VmTypes":["nope"]}}`, 0},
	} {
		if n := count(probe.body); n != probe.want {
			t.Errorf("%s returned %d machine(s), want %d", probe.body, n, probe.want)
		}
	}

	// And an empty filter list matches nothing rather than everything: asking
	// for none of a set is not asking for all of it.
	if n := count(`{"Filters":{"VmIds":[]}}`); n != 0 {
		t.Errorf("an empty VmIds matched %d machine(s), want 0", n)
	}
}

// The fingerprint is the one ssh-keygen prints.
//
// It was computed over the whole line — algorithm prefix and comment included —
// so it matched nothing a client could reproduce, and renaming the comment
// changed the fingerprint of the same key. The value below comes from
// `ssh-keygen -l -E md5` on the key above, which is the only authority that
// settles it.
func TestTheFingerprintIsTheOneSshKeygenPrints(t *testing.T) {
	const (
		key  = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIr6pEFlAFO3YU0DNW/r8SkpjdbptN9ockkO2BtIolSD conformance@feint"
		want = "6b:d8:0e:65:b1:58:fd:61:94:3a:b3:42:e6:e1:2c:01"
	)
	ts := newServer(t)
	_, out := post(t, ts, "CreateKeypair", `{"KeypairName":"real","PublicKey":`+quote(key)+`}`)
	pair, _ := out["Keypair"].(map[string]any)
	if pair == nil {
		t.Fatalf("no keypair in the answer: %v", out)
	}
	if got, _ := pair["KeypairFingerprint"].(string); got != want {
		t.Errorf("fingerprint %q, want the one ssh-keygen prints (%q)", got, want)
	}
	// And the type is the key's own, not a constant: every key used to answer
	// ssh-rsa, ed25519 ones included.
	if got, _ := pair["KeypairType"].(string); got != "ssh-ed25519" {
		t.Errorf("KeypairType %q for an ed25519 key", got)
	}
	// The comment must not reach the fingerprint: the same key renamed is the
	// same key.
	renamed := strings.Replace(key, "conformance@feint", "someone@else", 1)
	_, out = post(t, ts, "CreateKeypair", `{"KeypairName":"renamed","PublicKey":`+quote(renamed)+`}`)
	pair, _ = out["Keypair"].(map[string]any)
	if got, _ := pair["KeypairFingerprint"].(string); got != want {
		t.Errorf("renaming the comment changed the fingerprint: %q", got)
	}
}

// A legitimate DryRun: false does not fail the conformance gate.
//
// Outscale declares DryRun on all twenty of its actions and this pack answers it
// at the mount point, so no handler decodes it. The unread-field report
// therefore counted it as a field nobody read, and `tools/conformance/score.sh`
// turns that list into exit 1: a request every client is entitled to send failed
// this project's own gate. It went unnoticed only because no script sent the
// flag.
//
// The fix must not be to declare DryRun on twenty request structs — that
// quietens the report by lying to it, since the handlers still would not read
// it. The middleware says what it read instead.
func TestDryRunFalseDoesNotFailTheGate(t *testing.T) {
	env := emulator.DefaultEnv()
	env.Contracts = map[string]*contract.Doc{"outscale": outscaleContract(t)}
	srv, err := emulator.NewServer(env, outscale.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	if status, _ := post(t, ts, "ReadVms", `{"DryRun":false}`); status != http.StatusOK {
		t.Fatalf("ReadVms with DryRun false answered %d", status)
	}

	res, err := http.Get(ts.URL + "/_feint/conformance") //nolint:noctx // test client
	if err != nil {
		t.Fatalf("read the conformance report: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	var report struct {
		Unread map[string][]string `json:"unread_request_fields"`
	}
	if err := json.NewDecoder(res.Body).Decode(&report); err != nil {
		t.Fatalf("decode the report: %v", err)
	}
	for operation, fields := range report.Unread {
		for _, field := range fields {
			if field == "DryRun" {
				t.Errorf("%s reports DryRun as unread, which fails score.sh for a legitimate request", operation)
			}
		}
	}
}

// outscaleContract loads the API description this pack ships against, the way
// the observer does in a real run.
func outscaleContract(t *testing.T) *contract.Doc {
	t.Helper()
	doc, err := contract.Load(filepath.Join("..", "..", "..", "contracts", "outscale.json"))
	if err != nil {
		t.Fatalf("load the Outscale contract: %v", err)
	}
	return doc
}

// A stopped Vm keeps its private address.
//
// PowerOff clears the runtime binding's address, correctly, since nothing
// answers there any more. A Vm placed in a Subnet was unaffected — its address
// lives in Attrs — and a Vm created without one lost it: one field, two
// behaviours. Terraform reading private_ip saw null after a stop, and Outscale
// keeps the address until the machine is terminated.
func TestAStoppedVmKeepsItsPrivateAddress(t *testing.T) {
	runtime := newCountingRuntime()
	ts := newRuntimeServer(t, machine.Use(runtime))

	// No Subnet: this is the case that had the address only in the binding.
	_, out := post(t, ts, "CreateVms", `{"ImageId":"ami-00000001"}`)
	vms, _ := out["Vms"].([]any)
	vm, _ := vms[0].(map[string]any)
	vmID, _ := vm["VmId"].(string)
	// Published on the read, the way a virtual machine's address is: it arrives
	// tens of seconds after the start, so the create cannot carry it.
	_, out = post(t, ts, "ReadVms", `{"Filters":{"VmIds":["`+vmID+`"]}}`)
	vms, _ = out["Vms"].([]any)
	vm, _ = vms[0].(map[string]any)
	running, _ := vm["PrivateIp"].(string)
	if running == "" {
		t.Fatalf("the running Vm publishes no address, so this test measures nothing: %v", vm)
	}

	post(t, ts, "StopVms", `{"VmIds":["`+vmID+`"]}`)
	_, out = post(t, ts, "ReadVms", `{"Filters":{"VmIds":["`+vmID+`"]}}`)
	vms, _ = out["Vms"].([]any)
	vm, _ = vms[0].(map[string]any)
	if stopped, _ := vm["PrivateIp"].(string); stopped != running {
		t.Errorf("a stopped Vm answers PrivateIp %q, want the %q it had running", stopped, running)
	}
}

// What the Terraform provider needs, and what nothing else asked for.
//
// Every test below covers a call or a field that only the provider walks. The
// pack served the CLI for two releases without any of them, which is why a
// suite that stops at one client proves less than it looks.

// ReadAdminPassword answers, and answers empty.
//
// It is a Windows call, and the provider makes it on every Vm it reads back,
// Linux included: an absent ProductCodes list reads as "unknown", so it asks. A
// 404 killed `terraform apply` on the first machine. The password is empty
// rather than generated — a made-up credential is one a client could try to
// use, and it would work nowhere.
func TestReadAdminPasswordAnswersEmpty(t *testing.T) {
	ts := newServer(t)
	_, subnetID := netAndSubnet(t, ts, "10.62.0.0/16", "10.62.1.0/24")
	_, out := post(t, ts, "CreateVms",
		`{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","BootOnCreation":false}`)
	vms, _ := out["Vms"].([]any)
	vm, _ := vms[0].(map[string]any)
	vmID, _ := vm["VmId"].(string)

	// The field whose absence causes the call in the first place.
	if codes, _ := vm["ProductCodes"].([]any); len(codes) == 0 {
		t.Error("the Vm publishes no ProductCodes, which is what sends the provider here")
	}

	status, answer := post(t, ts, "ReadAdminPassword", `{"VmId":"`+vmID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("ReadAdminPassword answered %d: %v", status, answer)
	}
	password, present := answer["AdminPassword"]
	if !present {
		t.Error("no AdminPassword in the answer")
	}
	if password != "" {
		t.Errorf("an invented password was handed out: %q", password)
	}
	// And an unknown machine is a refusal, not an empty password.
	if status, _ := post(t, ts, "ReadAdminPassword", `{"VmId":"i-00000000"}`); status == http.StatusOK {
		t.Error("a password was answered for a Vm that does not exist")
	}
}

// Tags live on the resource they name.
//
// The provider calls CreateTags on almost every resource, and reads them back
// on the next plan. Two entries for one key, or an order that moves between
// reads, is a permanent diff in somebody's configuration.
func TestTagsAreStoredOnTheResourceTheyName(t *testing.T) {
	ts := newServer(t)
	_, out := post(t, ts, "CreateNet", `{"IpRange":"10.63.0.0/16"}`)
	n, _ := out["Net"].(map[string]any)
	netID, _ := n["NetId"].(string)

	post(t, ts, "CreateTags",
		`{"ResourceIds":["`+netID+`"],"Tags":[{"Key":"name","Value":"one"},{"Key":"env","Value":"test"}]}`)

	tagsOfNet := func() map[string]string {
		_, out := post(t, ts, "ReadNets", `{}`)
		nets, _ := out["Nets"].([]any)
		first, _ := nets[0].(map[string]any)
		entries, _ := first["Tags"].([]any)
		got := map[string]string{}
		for _, entry := range entries {
			tag, _ := entry.(map[string]any)
			key, _ := tag["Key"].(string)
			value, _ := tag["Value"].(string)
			got[key] = value
		}
		return got
	}
	if got := tagsOfNet(); got["name"] != "one" || got["env"] != "test" {
		t.Fatalf("the tags are not on the Net: %v", got)
	}

	// A key given twice replaces rather than repeats: a Tags block is a desired
	// state, and a second apply must not produce two entries for one key.
	post(t, ts, "CreateTags", `{"ResourceIds":["`+netID+`"],"Tags":[{"Key":"name","Value":"two"}]}`)
	if got := tagsOfNet(); got["name"] != "two" || len(got) != 2 {
		t.Errorf("re-tagging produced %v, want name replaced and env kept", got)
	}

	// The flat view, which is a different shape and says what each tag is on.
	_, out = post(t, ts, "ReadTags", `{}`)
	tags, _ := out["Tags"].([]any)
	if len(tags) != 2 {
		t.Fatalf("ReadTags answered %d tag(s), want 2: %v", len(tags), out)
	}
	first, _ := tags[0].(map[string]any)
	// "vpc", not "net": this assertion asked for "net" for three releases and
	// so held the invented value in place, the way a test that asserts the
	// emulator rather than the cloud always does. The name comes from the SDK's
	// TagResourceType enum (osc-sdk-go/pkg/osc/client.gen.go).
	// TestEveryTaggableResourceTypeIsOneTheSDKDeclares now checks the whole
	// table rather than this one row.
	if first["ResourceId"] != netID || first["ResourceType"] != "vpc" {
		t.Errorf("a tag does not name what it is on: %v", first)
	}

	post(t, ts, "DeleteTags", `{"ResourceIds":["`+netID+`"],"Tags":[{"Key":"env","Value":"test"}]}`)
	if got := tagsOfNet(); len(got) != 1 || got["name"] != "two" {
		t.Errorf("after the delete: %v", got)
	}
}

// DeletionProtection refuses the delete, for the whole batch.
//
// The provider sends the flag on every create. Declared nowhere, it was accepted
// and dropped, which told a client its machine was protected when nothing
// protected it — the answer worse than a 400, because it is indistinguishable
// from success.
func TestDeletionProtectionRefusesTheDelete(t *testing.T) {
	ts := newServer(t)
	_, subnetID := netAndSubnet(t, ts, "10.64.0.0/16", "10.64.1.0/24")

	_, out := post(t, ts, "CreateVms",
		`{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","BootOnCreation":false}`)
	vms, _ := out["Vms"].([]any)
	plain, _ := vms[0].(map[string]any)
	plainID, _ := plain["VmId"].(string)

	_, out = post(t, ts, "CreateVms",
		`{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","BootOnCreation":false,"DeletionProtection":true}`)
	vms, _ = out["Vms"].([]any)
	guarded, _ := vms[0].(map[string]any)
	guardedID, _ := guarded["VmId"].(string)
	if protected, _ := guarded["DeletionProtection"].(bool); !protected {
		t.Fatalf("the flag was dropped: %v", guarded)
	}

	if status, _ := post(t, ts, "DeleteVms", `{"VmIds":["`+guardedID+`"]}`); status == http.StatusOK {
		t.Error("a protected Vm was deleted")
	}
	// And a batch containing it deletes nothing: the API refuses the call, not
	// the machine, so a delete of two must not leave one gone.
	post(t, ts, "DeleteVms", `{"VmIds":["`+plainID+`","`+guardedID+`"]}`)
	_, out = post(t, ts, "ReadVms", `{"Filters":{"VmIds":["`+plainID+`"]}}`)
	vms, _ = out["Vms"].([]any)
	if len(vms) == 0 {
		t.Fatalf("the batch deleted the unprotected Vm before refusing")
	}
	first, _ := vms[0].(map[string]any)
	if state, _ := first["State"].(string); state == "terminated" {
		t.Error("the batch terminated the unprotected Vm before refusing the protected one")
	}
}

// ResultsPerPage is honoured.
//
// Every Terraform read sends it. Declared and unread, it told a client its page
// size was applied while the answer carried everything.
func TestResultsPerPageIsHonoured(t *testing.T) {
	ts := newServer(t)
	_, subnetID := netAndSubnet(t, ts, "10.65.0.0/16", "10.65.1.0/24")
	for range 3 {
		post(t, ts, "CreateVms",
			`{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","BootOnCreation":false}`)
	}

	count := func(body string) int {
		_, out := post(t, ts, "ReadVms", body)
		vms, _ := out["Vms"].([]any)
		return len(vms)
	}
	if n := count(`{}`); n != 3 {
		t.Fatalf("no page size returned %d machines, want 3", n)
	}
	if n := count(`{"ResultsPerPage":2}`); n != 2 {
		t.Errorf("ResultsPerPage 2 returned %d machines", n)
	}
	// A page larger than the answer is not an error, and returns what there is.
	if n := count(`{"ResultsPerPage":50}`); n != 3 {
		t.Errorf("ResultsPerPage 50 returned %d machines, want all 3", n)
	}
}

// A terminated Vm stays readable.
//
// The Terraform provider answers DeleteVms by polling ReadVms until the Vm
// reports "terminated". The pack removed the record, so the provider read an
// empty list and the plugin crashed outright — "Plugin did not respond", on
// every destroy, measured with the published provider v1.7.0.
func TestATerminatedVmStaysReadable(t *testing.T) {
	ts := newServer(t)
	_, subnetID := netAndSubnet(t, ts, "10.66.0.0/16", "10.66.1.0/24")
	_, out := post(t, ts, "CreateVms",
		`{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","BootOnCreation":false}`)
	vms, _ := out["Vms"].([]any)
	vm, _ := vms[0].(map[string]any)
	vmID, _ := vm["VmId"].(string)

	post(t, ts, "DeleteVms", `{"VmIds":["`+vmID+`"]}`)

	_, out = post(t, ts, "ReadVms", `{"Filters":{"VmIds":["`+vmID+`"]}}`)
	vms, _ = out["Vms"].([]any)
	if len(vms) != 1 {
		t.Fatalf("the deleted Vm is not readable: %v", out)
	}
	vm, _ = vms[0].(map[string]any)
	if state, _ := vm["State"].(string); state != "terminated" {
		t.Errorf("the deleted Vm reports %q, want terminated", state)
	}

	// And it holds nothing: the Subnet it was in must delete, which is what
	// failed right after the first version of this fix.
	if status, out := post(t, ts, "DeleteSubnet", `{"SubnetId":"`+subnetID+`"}`); status != http.StatusOK {
		t.Errorf("the Subnet refuses to go under a terminated Vm: %d %v", status, out)
	}
}

// A keypair answers to its id and to its name.
//
// Their DeleteKeypairRequest declares KeypairId and KeypairName side by side,
// and the Terraform provider creates by name then destroys by id. The pack read
// only the name, so every destroy failed with "the keypair  does not exist" —
// with the gap where the id should have been.
//
// The first fix added a KeypairId attribute, which was worse: the view already
// published res.ID under that name, so there were two identities and they never
// matched. There is one, and it is the store's.
func TestAKeypairIsAddressableByIdAndByName(t *testing.T) {
	const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIr6pEFlAFO3YU0DNW/r8SkpjdbptN9ockkO2BtIolSD conformance@feint"

	for _, form := range []struct {
		what string
		body func(id, name string) string
	}{
		{"by name", func(_, name string) string { return `{"KeypairName":"` + name + `"}` }},
		{"by id", func(id, _ string) string { return `{"KeypairId":"` + id + `"}` }},
	} {
		t.Run(form.what, func(t *testing.T) {
			ts := newServer(t)
			_, out := post(t, ts, "CreateKeypair", `{"KeypairName":"mine","PublicKey":`+quote(key)+`}`)
			pair, _ := out["Keypair"].(map[string]any)
			id, _ := pair["KeypairId"].(string)
			name, _ := pair["KeypairName"].(string)
			if id == "" {
				t.Fatalf("the keypair carries no id: %v", pair)
			}

			if status, out := post(t, ts, "DeleteKeypair", form.body(id, name)); status != http.StatusOK {
				t.Fatalf("delete %s answered %d: %v", form.what, status, out)
			}
			_, out = post(t, ts, "ReadKeypairs", `{}`)
			pairs, _ := out["Keypairs"].([]any)
			if len(pairs) != 0 {
				t.Errorf("the keypair survived a delete %s: %v", form.what, out)
			}
		})
	}
}

// A volume is created, read, linked and unlinked.
//
// The pack served none, so `outscale_volume` failed on CreateVolume and the plan
// died before the machine it was for. Nothing in the CLI suite creates a volume,
// which is why the gap survived until a Terraform fixture existed.
func TestAVolumeIsCreatedReadAndLinked(t *testing.T) {
	ts := newServer(t)
	_, subnetID := netAndSubnet(t, ts, "10.67.0.0/16", "10.67.1.0/24")

	status, out := post(t, ts, "CreateVolume", `{"SubregionName":"eu-west-2a","Size":10}`)
	if status != http.StatusOK {
		t.Fatalf("CreateVolume answered %d: %v", status, out)
	}
	volume, _ := out["Volume"].(map[string]any)
	volumeID, _ := volume["VolumeId"].(string)
	if volumeID == "" {
		t.Fatalf("no volume id: %v", out)
	}
	if state, _ := volume["State"].(string); state != "available" {
		t.Errorf("a fresh volume is %q, want available", state)
	}

	// The one field their schema marks required.
	if status, _ := post(t, ts, "CreateVolume", `{"Size":10}`); status == http.StatusOK {
		t.Error("a volume was created without a SubregionName")
	}

	_, out = post(t, ts, "CreateVms",
		`{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","BootOnCreation":false}`)
	vms, _ := out["Vms"].([]any)
	vm, _ := vms[0].(map[string]any)
	vmID, _ := vm["VmId"].(string)

	if status, out := post(t, ts, "LinkVolume",
		`{"VolumeId":"`+volumeID+`","VmId":"`+vmID+`","DeviceName":"/dev/xvdb"}`); status != http.StatusOK {
		t.Fatalf("LinkVolume answered %d: %v", status, out)
	}
	_, out = post(t, ts, "ReadVolumes", `{"Filters":{"VolumeIds":["`+volumeID+`"]}}`)
	volumes, _ := out["Volumes"].([]any)
	if len(volumes) != 1 {
		t.Fatalf("the volume is not readable: %v", out)
	}
	volume, _ = volumes[0].(map[string]any)
	links, _ := volume["LinkedVolumes"].([]any)
	if len(links) != 1 {
		t.Fatalf("the link is not published: %v", volume)
	}
	link, _ := links[0].(map[string]any)
	if link["VmId"] != vmID || link["DeviceName"] != "/dev/xvdb" {
		t.Errorf("the link names %v on %v", link["VmId"], link["DeviceName"])
	}

	// A linked volume does not go: a client destroying in the wrong order needs
	// that refusal to retry.
	if status, _ := post(t, ts, "DeleteVolume", `{"VolumeId":"`+volumeID+`"}`); status == http.StatusOK {
		t.Error("a linked volume was deleted")
	}
	post(t, ts, "UnlinkVolume", `{"VolumeId":"`+volumeID+`"}`)
	if status, out := post(t, ts, "DeleteVolume", `{"VolumeId":"`+volumeID+`"}`); status != http.StatusOK {
		t.Errorf("an unlinked volume refuses to go: %d %v", status, out)
	}
}

// A volume grows and does not shrink.
//
// A filesystem does not survive its disk getting smaller, and the real API
// refuses. Accepting it would answer success to a request that destroys data
// everywhere else.
func TestAVolumeDoesNotShrink(t *testing.T) {
	ts := newServer(t)
	_, out := post(t, ts, "CreateVolume", `{"SubregionName":"eu-west-2a","Size":10}`)
	volume, _ := out["Volume"].(map[string]any)
	volumeID, _ := volume["VolumeId"].(string)

	if status, out := post(t, ts, "UpdateVolume", `{"VolumeId":"`+volumeID+`","Size":20}`); status != http.StatusOK {
		t.Fatalf("growing a volume answered %d: %v", status, out)
	}
	if status, _ := post(t, ts, "UpdateVolume", `{"VolumeId":"`+volumeID+`","Size":5}`); status == http.StatusOK {
		t.Error("a volume was shrunk")
	}
	_, out = post(t, ts, "ReadVolumes", `{}`)
	volumes, _ := out["Volumes"].([]any)
	volume, _ = volumes[0].(map[string]any)
	if size, _ := volume["Size"].(float64); size != 20 {
		t.Errorf("the volume is %v after a refused shrink, want 20", size)
	}
}

// ReadVmsState answers running machines by default.
//
// AllVms false is the API's own default, and a client polling for what is up
// must not be handed what is terminated — which now stays readable.
func TestReadVmsStateAnswersRunningByDefault(t *testing.T) {
	ts := newServer(t)
	_, subnetID := netAndSubnet(t, ts, "10.68.0.0/16", "10.68.1.0/24")
	_, out := post(t, ts, "CreateVms", `{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`"}`)
	vms, _ := out["Vms"].([]any)
	vm, _ := vms[0].(map[string]any)
	vmID, _ := vm["VmId"].(string)

	states := func(body string) []any {
		_, out := post(t, ts, "ReadVmsState", body)
		list, _ := out["VmStates"].([]any)
		return list
	}
	if got := states(`{}`); len(got) != 1 {
		t.Fatalf("a running machine is not reported: %v", got)
	}
	post(t, ts, "DeleteVms", `{"VmIds":["`+vmID+`"]}`)
	if got := states(`{}`); len(got) != 0 {
		t.Errorf("a terminated machine is reported as running: %v", got)
	}
	if got := states(`{"AllVms":true}`); len(got) != 1 {
		t.Errorf("AllVms does not include the terminated machine: %v", got)
	}
}

// Detach completes the machine package's driver contract; *blockingRuntime needs no behaviour here.
func (f *blockingRuntime) Detach(context.Context, string, string) error { return nil }

// Detach completes the machine package's driver contract; *countingRuntime needs no behaviour here.
func (f *countingRuntime) Detach(context.Context, string, string) error { return nil }
