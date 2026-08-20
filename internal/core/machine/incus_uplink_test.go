package machine

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The uplink is one bridge shared by every OVN network and every routed
// address, and the daemon rebuilds its firewall on each reconfiguration —
// including the create of an OVN network attached to it. Everything in this
// file is #341: the serialisation of those rebuilds, the withdrawal of a
// deleted network's delegated block, the adoption that drops a dead run's
// routes, and the refusal to share the uplink with another live emulator.

// ourUplinkJSON renders the uplink as the runtime reports it, labelled and
// held by the given pid ("" for an unclaimed one).
func ourUplinkJSON(holder, routes string) string {
	cfg := `"user.` + LabelKey + `":"feint","ipv4.routes":"` + routes + `"`
	if holder != "" {
		cfg += `,"user.` + UplinkHolderKey + `":"` + holder + `"`
	}
	return `{"type":"bridge","config":{` + cfg + `}}`
}

// TestConcurrentSubnetAndMachineNetworkCreatesSerialiseOnTheUplink holds the
// serialisation that closes #341's crash. Measured with the daemon's debug
// log on Incus 7.2: a `network set feint-uplink ipv4.routes=…` (delegating a
// subnet's block) interleaved with the POST creating fnt-default, both paths
// cleared the uplink's nftables chains, and the loser died on `Failed
// deleting nftables chain "fwd.feint-uplink" … No such file or directory`.
// The two operations here are exactly those two; the runner fails the test if
// any two uplink-rebuilding commands are ever in flight at once.
func TestConcurrentSubnetAndMachineNetworkCreatesSerialiseOnTheUplink(t *testing.T) {
	self := strconv.Itoa(os.Getpid())
	uplink := ourUplinkJSON(self, "")
	var inRebuild atomic.Int32
	var overlapped atomic.Bool
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		key := strings.Join(args, " ")
		// The commands that make the daemon clear and rebuild the uplink's
		// firewall: a config write on the uplink, and an OVN network create.
		if strings.HasPrefix(key, "network set feint-uplink") || strings.HasPrefix(key, "network create") {
			if inRebuild.Add(1) > 1 {
				overlapped.Store(true)
			}
			time.Sleep(10 * time.Millisecond)
			inRebuild.Add(-1)
		}
		switch {
		case strings.HasPrefix(key, "query /1.0/networks/feint-uplink"):
			return []byte(uplink), nil
		case strings.HasPrefix(key, "query /1.0/networks?recursion=1"):
			return []byte("[]"), nil
		case strings.HasPrefix(key, "query /1.0/networks/"):
			return nil, errors.New("Network not found")
		case strings.HasPrefix(key, "network get feint-uplink ipv4.routes"):
			return []byte("\n"), nil
		}
		return nil, nil
	}
	d := NewIncus()
	d.runner = runner
	d.OVN = true

	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, spec := range []NetworkSpec{
		{Name: "fnt-subnet00001", CIDR: "10.70.1.0/24"},
		{Name: "fnt-default", CIDR: "10.209.84.0/24"},
	} {
		wg.Add(1)
		go func(s NetworkSpec) {
			defer wg.Done()
			<-start
			if err := d.EnsureNetwork(context.Background(), s); err != nil {
				t.Errorf("create %s: %v", s.Name, err)
			}
		}(spec)
	}
	close(start)
	wg.Wait()

	if overlapped.Load() {
		t.Fatal("two uplink rebuilds were in flight at once; the loser of that race dies on 'Failed deleting nftables chain' (#341)")
	}
}

// TestRemoveNetworkWithdrawsTheDelegatedBlock holds the leftover half of
// #341: seven blocks of long-deleted networks were measured still delegated
// to one station's uplink, each a real host route pointed at it.
func TestRemoveNetworkWithdrawsTheDelegatedBlock(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"network get fnt-abc123 ipv4.address":  "10.70.1.1/24\n",
		"network get feint-uplink ipv4.routes": "10.209.84.0/24,10.70.1.0/24\n",
	}}
	d := newFakeDriver(f)
	d.OVN = true

	if err := d.RemoveNetwork(context.Background(), "fnt-abc123"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	deletes := f.matching("network delete fnt-abc123")
	withdraws := f.matching("network set feint-uplink ipv4.routes=10.209.84.0/24")
	if len(deletes) != 1 || len(withdraws) != 1 {
		t.Fatalf("expected one delete and one withdrawal, got:\n%s", strings.Join(f.commands(), "\n"))
	}
	// Withdrawn after the delete: a network still standing keeps its block.
	cmds := f.commands()
	deleteAt, withdrawAt := -1, -1
	for i, cmd := range cmds {
		if strings.HasPrefix(cmd, "network delete") {
			deleteAt = i
		}
		if strings.HasPrefix(cmd, "network set feint-uplink") {
			withdrawAt = i
		}
	}
	if withdrawAt < deleteAt {
		t.Fatalf("the block was withdrawn before the delete:\n%s", strings.Join(cmds, "\n"))
	}
}

// Bridge mode has no uplink, and its RemoveNetwork must not grow one.
func TestRemoveNetworkLeavesTheUplinkAloneOffOVN(t *testing.T) {
	f := &fakeRuntime{}
	d := newFakeDriver(f)

	if err := d.RemoveNetwork(context.Background(), "fnt-abc123"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if touched := f.matching("feint-uplink"); len(touched) != 0 {
		t.Fatalf("bridge mode touched the uplink:\n%s", strings.Join(touched, "\n"))
	}
}

// TestAdoptedUplinkDropsADeadRunsRoutes holds the adoption: nothing else of a
// dead emulator is adopted, so its delegations are not either. What stays is
// exactly the blocks of the labelled OVN networks still standing, because the
// next run reuses those by name.
func TestAdoptedUplinkDropsADeadRunsRoutes(t *testing.T) {
	networks := `[
	  {"name":"fnt-survivor001","type":"ovn","config":{"user.` + LabelKey + `":"feint","network":"feint-uplink","ipv4.address":"10.209.84.1/24"}},
	  {"name":"incusbr0","type":"bridge","config":{}}
	]`
	f := &fakeRuntime{answers: map[string]string{
		"query /1.0/networks/feint-uplink": ourUplinkJSON("999", "10.182.0.0/24,10.183.0.0/24,10.209.84.0/24"),
		"query /1.0/networks?recursion=1":  networks,
	}, fail: map[string]error{
		"query /1.0/networks/fnt-fresh000001": errors.New("Network not found"),
	}}
	d := newFakeDriver(f)
	d.OVN = true
	d.holderProbe = func(int) bool { return false } // pid 999 is dead

	if err := d.EnsureNetwork(context.Background(), NetworkSpec{
		Name: "fnt-fresh000001", CIDR: "10.70.1.0/24",
	}); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	adopts := f.matching("user." + UplinkHolderKey + "=" + strconv.Itoa(os.Getpid()))
	if len(adopts) != 1 {
		t.Fatalf("the uplink was not claimed exactly once:\n%s", strings.Join(f.commands(), "\n"))
	}
	if !strings.Contains(adopts[0], "ipv4.routes=10.209.84.0/24") {
		t.Fatalf("the dead run's routes were not dropped to the survivor's block:\n%s", adopts[0])
	}
}

// TestEnsureUplinkRefusesAnUplinkHeldByALiveEmulator holds the refusal that
// replaces corruption: uplinkMu serialises one process, so a second live
// emulator on the same uplink would race the first outside any lock — the
// exact interleaving #341 measured, minus the luck.
func TestEnsureUplinkRefusesAnUplinkHeldByALiveEmulator(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"query /1.0/networks/feint-uplink": ourUplinkJSON("31337", ""),
	}, fail: map[string]error{
		"query /1.0/networks/fnt-fresh000001": errors.New("Network not found"),
	}}
	d := newFakeDriver(f)
	d.OVN = true
	d.holderProbe = func(int) bool { return true } // pid 31337 is a live feint

	err := d.EnsureNetwork(context.Background(), NetworkSpec{
		Name: "fnt-fresh000001", CIDR: "10.70.1.0/24",
	})
	if err == nil {
		t.Fatal("an uplink held by a live emulator was shared")
	}
	if !strings.Contains(err.Error(), "31337") {
		t.Fatalf("the refusal does not name the holder: %v", err)
	}
	for _, cmd := range f.commands() {
		if strings.HasPrefix(cmd, "network set feint-uplink") || strings.HasPrefix(cmd, "network create") {
			t.Fatalf("the refusal still touched the uplink: %s", cmd)
		}
	}
}

// TestANetworkCreateFailureNamesAForeignHolderWithoutClaimingIt is the "of
// ours or otherwise" half of #342's question, at the one moment the wanted
// block is known. Named without a remedy that touches it: `feint clean` must
// not be advertised against a process nobody attributed to the emulator.
func TestANetworkCreateFailureNamesAForeignHolderWithoutClaimingIt(t *testing.T) {
	f := &fakeRuntime{fail: map[string]error{
		"query /1.0/networks/fnt-99109f524b2": errors.New("Network not found"),
		"network create":                      errors.New("Address already in use"),
	}}
	d := newFakeDriver(f)
	d.leftoverScan = func() []DHCPLeftover { return nil }
	d.holderScan = func(_ netip.Prefix) []BlockHolder {
		return []BlockHolder{{PID: 3187, Command: "dnsmasq", Address: "10.76.154.1"}}
	}

	err := d.EnsureNetwork(context.Background(), NetworkSpec{
		Name: "fnt-99109f524b2", CIDR: "10.76.154.0/24", Gateway: "10.76.154.1",
	})
	if err == nil {
		t.Fatal("the failed create reported success")
	}
	for _, fact := range []string{"3187", "10.76.154.1", "not the emulator's to end"} {
		if !strings.Contains(err.Error(), fact) {
			t.Errorf("the error does not carry %q:\n%v", fact, err)
		}
	}
	if strings.Contains(err.Error(), "feint clean") {
		t.Fatalf("the error offers the sweep against a process it could not attribute:\n%v", err)
	}
}
