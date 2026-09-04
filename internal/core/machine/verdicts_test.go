package machine

import (
	"bytes"
	"context"
	"log/slog"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/store"
)

// Where a verdict goes (#670): the log, the resource, the counters — and
// never the state.

// pinnedPlan is the plan of a machine carrying one pinned address on fnt-x.
var pinnedPlan = Plan{Boot: []Attachment{{Network: "fnt-x", Address: "10.30.1.10", PrefixLen: 24}}}

// publicPlan is pinnedPlan with a promised public address, for the tests
// that count the replay through the address it routes.
var publicPlan = Plan{Boot: pinnedPlan.Boot, Publics: []string{"203.0.113.2"}}

// withPublic is pinnedOn with the public /32 beside the pinned address, the
// way the replay leaves a machine of publicPlan.
func withPublic(shape Shape) Shape {
	eth0 := shape.Interfaces["eth0"]
	eth0.Addresses = append(eth0.Addresses, netip.MustParsePrefix("203.0.113.2/32"))
	shape.Interfaces["eth0"] = eth0
	return shape
}

// TestABrokenVerdictReachesTheResourceAndNotItsState: the state is the one
// the boot produced, the verdict is the one the reading produced, and neither
// stands in for the other.
func TestABrokenVerdictReachesTheResourceAndNotItsState(t *testing.T) {
	b := newGroupSyncBench()
	o := &observingRecorder{Recorder: b.rec, shape: pinnedOn("fnt-x", "10.30.1.11/24")}
	vm := b.machine("m", "")
	r := observingReconciler(b, o, pinnedPlan)

	if !r.PowerOn(context.Background(), vm, Boot{Image: "ubuntu:24.04"}) {
		t.Fatalf("the boot did not start; sequence: %v", b.rec.Sequence())
	}
	if vm.State != "running" {
		t.Fatalf("a broken reading moved the state to %q, and the machine IS running", vm.State)
	}
	want := "broken: carries(fnt-x, 10.30.1.10/24) want 10.30.1.10/24, got eth0: 10.30.1.11/24"
	if got := vm.Runtime[VerifiedKey]; got != want {
		t.Fatalf("Runtime[%s] = %q, want %q", VerifiedKey, got, want)
	}

	// And the accepting half, on the same runtime with the divergence gone.
	o.shape = pinnedOn("fnt-x", "10.30.1.10/24")
	vm.State = "stopped"
	if !r.PowerOn(context.Background(), vm, Boot{Image: "ubuntu:24.04"}) {
		t.Fatal("the second boot did not start")
	}
	if got := vm.Runtime[VerifiedKey]; got != "held" {
		t.Fatalf("Runtime[%s] = %q after a boot that carries its plan, want held", VerifiedKey, got)
	}
}

// The key lands through Commit and merges per key: a writer to another field
// of the same resource, landing while the machine boots, keeps its field and
// the verdict keeps its own (the mergeRuntime property, #295).
func TestABrokenVerdictSurvivesAConcurrentWriteToAnotherKey(t *testing.T) {
	st := store.New()
	now := func() time.Time { return time.Unix(1700000000, 0).UTC() }
	res := resource.New("m", "machine", resource.Tenant{Provider: "bench"}, "stopped", now())
	res.Attrs = map[string]any{}
	st.Put(res)

	b := newGroupSyncBench()
	o := &observingRecorder{Recorder: b.rec, shape: pinnedOn("fnt-x", "10.30.1.11/24")}
	r := observingReconciler(b, o, pinnedPlan)

	err := r.binding().Transition(st, now, "machine", "m", func(res *resource.Resource) {
		// The other writer lands while this one works outside the lock.
		if err := st.Update("bench", "machine", "m", func(stored *resource.Resource) error {
			if stored.Runtime == nil {
				stored.Runtime = map[string]string{}
			}
			stored.Runtime["tag"] = "kept"
			return nil
		}); err != nil {
			t.Fatalf("the concurrent write failed: %v", err)
		}
		r.PowerOn(context.Background(), res, Boot{Image: "ubuntu:24.04"})
	})
	if err != nil {
		t.Fatalf("transition: %v", err)
	}
	stored, found := st.Get("bench", "machine", "m")
	if !found {
		t.Fatal("the resource is gone")
	}
	if stored.Runtime["tag"] != "kept" {
		t.Errorf("the concurrent writer's key was lost: %v", stored.Runtime)
	}
	if !strings.HasPrefix(stored.Runtime[VerifiedKey], "broken: carries(fnt-x, 10.30.1.10/24)") {
		t.Errorf("the verdict did not reach the store: %v", stored.Runtime)
	}
	if stored.State != "running" {
		t.Errorf("stored state %q, want running", stored.State)
	}
}

// ERROR for a broken claim, WARN for an unreadable one: a failure and a
// degradation, in the two words the repository holds for the two levels.
func TestABrokenVerdictIsLoggedAsAFailure(t *testing.T) {
	var log bytes.Buffer
	b := newGroupSyncBench()
	o := &observingRecorder{Recorder: b.rec, shape: pinnedOn("fnt-x", "10.30.1.11/24")}
	vm := b.machine("m", "")
	r := observingReconciler(b, o, pinnedPlan)
	r.Groups.Binding.Log = slog.New(slog.NewTextHandler(&log, nil))

	r.PowerOn(context.Background(), vm, Boot{Image: "ubuntu:24.04"})
	lines := log.String()
	if !strings.Contains(lines, "level=ERROR") ||
		!strings.Contains(lines, "claim=\"carries(fnt-x, 10.30.1.10/24)\"") ||
		!strings.Contains(lines, "got=\"eth0: 10.30.1.11/24\"") {
		t.Fatalf("a broken claim was not logged as a failure naming the claim and both values:\n%s", lines)
	}

	log.Reset()
	o.shape = Shape{}
	o.err = errUnreadable
	vm.State = "stopped"
	r.PowerOn(context.Background(), vm, Boot{Image: "ubuntu:24.04"})
	lines = log.String()
	if strings.Contains(lines, "level=ERROR") || !strings.Contains(lines, "level=WARN") ||
		!strings.Contains(lines, "why=\"the agent isn't currently running\"") {
		t.Fatalf("an unreadable reading was not logged as a degradation:\n%s", lines)
	}
	if got := vm.Runtime[VerifiedKey]; !strings.HasPrefix(got, "unreadable: carries(fnt-x, 10.30.1.10/24): ") {
		t.Errorf("Runtime[%s] = %q", VerifiedKey, got)
	}
}

// The four counters follow the verdicts, on the handle the runtime was bound
// through, so /_feint/health reads what every pack of one emulator counted.
func TestTheCountersFollowTheVerdicts(t *testing.T) {
	b := newGroupSyncBench()
	o := &observingRecorder{Recorder: b.rec, shape: pinnedOn("fnt-x", "10.30.1.10/24")}
	rt := Use(o)
	vm := b.machine("m", "")
	r := observingReconciler(b, o, pinnedPlan)
	r.Groups.Binding = r.Groups.Binding.WithRuntime(rt)

	if got := rt.Verification(); got != (Verification{}) {
		t.Fatalf("a fresh runtime counts %+v", got)
	}
	r.PowerOn(context.Background(), vm, Boot{Image: "ubuntu:24.04"})
	if got := rt.Verification(); got != (Verification{Held: 1}) {
		t.Fatalf("after a held boot: %+v", got)
	}
	// Broken, and unrepaired: the machine stays wrong on the second reading.
	o.shape = pinnedOn("fnt-x", "10.30.1.11/24")
	vm.State = "stopped"
	r.PowerOn(context.Background(), vm, Boot{Image: "ubuntu:24.04"})
	if got := rt.Verification(); got != (Verification{Held: 1, Broken: 1}) {
		t.Fatalf("after a broken boot: %+v", got)
	}
	o.err = errUnreadable
	vm.State = "stopped"
	r.PowerOn(context.Background(), vm, Boot{Image: "ubuntu:24.04"})
	if got := rt.Verification(); got != (Verification{Held: 1, Broken: 1, Unreadable: 1}) {
		t.Fatalf("after an unreadable boot: %+v", got)
	}
	// The zero Runtime, which every test without Use builds, counts nowhere
	// and answers zero rather than panicking.
	if got := (Runtime{}).Verification(); got != (Verification{}) {
		t.Errorf("the zero runtime counts %+v", got)
	}
}

// TestARepairIsAttemptedOnceAndCounted: a broken first reading replays the
// post-boot order once and reads again; a machine that then carries its plan
// is published held and counted repaired; one that does not is published
// broken after exactly two readings, never three.
func TestARepairIsAttemptedOnceAndCounted(t *testing.T) {
	broken, held := withPublic(pinnedOn("fnt-x", "10.30.1.11/24")), withPublic(pinnedOn("fnt-x", "10.30.1.10/24"))

	b := newGroupSyncBench()
	o := &observingRecorder{Recorder: b.rec, queue: []Shape{broken, held}}
	rt := Use(o)
	vm := b.machine("m", "")
	r := observingReconciler(b, o, publicPlan)
	r.Groups.Binding = r.Groups.Binding.WithRuntime(rt)

	var log bytes.Buffer
	r.Groups.Binding.Log = slog.New(slog.NewTextHandler(&log, nil))
	r.PowerOn(context.Background(), vm, Boot{Image: "ubuntu:24.04"})
	if o.reads != 2 {
		t.Fatalf("a broken reading was followed by %d reading(s), want exactly one more", o.reads-1)
	}
	if got := vm.Runtime[VerifiedKey]; got != "held" {
		t.Fatalf("Runtime[%s] = %q after a repair, want held", VerifiedKey, got)
	}
	if got := rt.Verification(); got != (Verification{Held: 2, Repaired: 1}) {
		t.Fatalf("after a repaired boot: %+v", got)
	}
	// The replay ran twice: the promised address was routed once by the boot
	// and once by the repair, on one start.
	routes, starts := 0, 0
	for _, kind := range b.rec.Sequence() {
		switch kind {
		case "RouteAddress":
			routes++
		case "Start":
			starts++
		}
	}
	if routes != 2 || starts != 1 {
		t.Errorf("the repair replayed %d route(s) on %d start(s), want 2 on 1: %v", routes, starts, b.rec.Sequence())
	}
	if !strings.Contains(log.String(), "level=WARN") || !strings.Contains(log.String(), "second time") {
		t.Errorf("a repair was not logged as a degradation naming the first reading:\n%s", log.String())
	}

	// A machine that stays wrong: two readings and no more, however many
	// broken shapes are queued behind them.
	b = newGroupSyncBench()
	o = &observingRecorder{Recorder: b.rec, queue: []Shape{broken, broken, held}}
	rt = Use(o)
	vm = b.machine("m", "")
	r = observingReconciler(b, o, publicPlan)
	r.Groups.Binding = r.Groups.Binding.WithRuntime(rt)
	r.PowerOn(context.Background(), vm, Boot{Image: "ubuntu:24.04"})
	if o.reads != 2 {
		t.Fatalf("a machine that stays wrong was read %d times, want exactly two", o.reads)
	}
	if got := vm.Runtime[VerifiedKey]; !strings.HasPrefix(got, "broken: ") {
		t.Fatalf("Runtime[%s] = %q after a failed repair", VerifiedKey, got)
	}
	if got := rt.Verification(); got != (Verification{Held: 1, Broken: 1}) {
		t.Fatalf("after a failed repair: %+v", got)
	}
}

// An unreadable reading is not retried: replaying onto a machine whose agent
// has not answered would not make it readable, and the late-address door
// reads it again when it can.
func TestAnUnreadableReadingIsNotReplayedOnto(t *testing.T) {
	b := newGroupSyncBench()
	o := &observingRecorder{Recorder: b.rec, err: errUnreadable}
	vm := b.machine("m", "")
	r := observingReconciler(b, o, publicPlan)
	r.PowerOn(context.Background(), vm, Boot{Image: "ubuntu:24.04"})
	if o.reads != 1 {
		t.Fatalf("an unreadable reading was followed by %d more", o.reads-1)
	}
	routes := 0
	for _, kind := range b.rec.Sequence() {
		if kind == "RouteAddress" {
			routes++
		}
	}
	if routes != 1 {
		t.Errorf("the replay ran %d times onto an unreadable machine, want once", routes)
	}
}

// TestTheLateAddressDoorVerifiesOnceItCanRead: the late-address door sits on
// a read path, so it reads only while nothing readable has been published —
// the virtual machine whose boot was unreadable — and never again after a
// reading held or broke.
func TestTheLateAddressDoorVerifiesOnceItCanRead(t *testing.T) {
	b := newGroupSyncBench()
	o := &observingRecorder{Recorder: b.rec, err: errUnreadable}
	vm := b.machine("m", "")
	r := observingReconciler(b, o, publicPlan)
	r.PowerOn(context.Background(), vm, Boot{Image: "ubuntu:24.04"})
	if o.reads != 1 || !strings.HasPrefix(vm.Runtime[VerifiedKey], "unreadable") {
		t.Fatalf("the boot: reads=%d verified=%q", o.reads, vm.Runtime[VerifiedKey])
	}

	// The agent answers: the door reads, and the verdict replaces the
	// unreadable one.
	o.err = nil
	o.shape = withPublic(pinnedOn("fnt-x", "10.30.1.10/24"))
	r.ReplayAddresses(context.Background(), vm)
	if o.reads != 2 || vm.Runtime[VerifiedKey] != "held" {
		t.Fatalf("the late door: reads=%d verified=%q", o.reads, vm.Runtime[VerifiedKey])
	}

	// And not again: a GET is not a reason to exec into a machine.
	r.ReplayAddresses(context.Background(), vm)
	r.ReplayAddresses(context.Background(), vm)
	if o.reads != 2 {
		t.Fatalf("the late door read %d times in all, want 2: a verified machine is read again on every GET", o.reads)
	}
}

// The hot doors — an address routed, a network joined — change what the
// machine carries, and each is read back.
func TestAHotJoinAndAHotRouteAreReadBack(t *testing.T) {
	b := newGroupSyncBench()
	o := &observingRecorder{Recorder: b.rec, shape: pinnedOn("fnt-x", "10.30.1.10/24")}
	vm := b.machine("m", "")
	r := observingReconciler(b, o, pinnedPlan)
	r.PowerOn(context.Background(), vm, Boot{Image: "ubuntu:24.04"})
	if o.reads != 1 {
		t.Fatalf("the boot read %d times", o.reads)
	}
	_ = r.Join(context.Background(), vm, Attachment{Network: "fnt-y"})
	if o.reads != 2 {
		t.Fatalf("a hot join was not read back: reads=%d", o.reads)
	}
	r.Route(context.Background(), vm, "203.0.113.2")
	if o.reads != 3 {
		t.Fatalf("a hot route was not read back: reads=%d", o.reads)
	}
}

// The summary a reader branches on: broken first, then unreadable, then held.
func TestTheSummaryLeadsWithWhatIsBroken(t *testing.T) {
	c := carries{Network: "fnt-x", Address: netip.MustParseAddr("10.30.1.10"), Bits: 24}
	d := defaultRoute{Network: "fnt-x"}
	if got := summary([]Verdict{held(c), broken(d, "via 10.30.1.1 dev eth0", "no default route"), unreadable(c, "x")}); got !=
		"broken: default route want via 10.30.1.1 dev eth0, got no default route" {
		t.Errorf("summary %q", got)
	}
	if got := summary([]Verdict{held(c), unreadable(d, "the gateway of fnt-x was not read")}); got !=
		"unreadable: default route: the gateway of fnt-x was not read" {
		t.Errorf("summary %q", got)
	}
	if got := summary([]Verdict{held(c), held(d)}); got != "held" {
		t.Errorf("summary %q", got)
	}
}

var errUnreadable = errorString("the agent isn't currently running")

type errorString string

func (e errorString) Error() string { return string(e) }
