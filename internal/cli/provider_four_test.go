package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	providerfour "github.com/stephrobert/feint/internal/cli/testdata/provider-four"
	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/store"
)

// The fourth provider, exercised as code (#517).
//
// #509, #510 and #511 prove the three real packs *can stop* reaching past the
// contract. None of them proves the contract *suffices* for a pack that never
// had the old habits: every one of the three was migrated onto it, so each one
// knows what it used to do by hand. testdata/provider-four is the pack that
// does not — an imaginary minimal cloud, written from the dataplane #517 asks
// for and from no cloud at all — and these are the tests that say what it had
// to supply, what it received without writing a line, and what it could still
// forget.
//
// Why the green here is worth less than the red: a fake pack written by
// reading the contract until everything compiles demonstrates that somebody
// can write a conforming pack, never that a stranger could. So every property
// below was planted before it was trusted, and
// tools/falsify/specs/provider-four.json keeps all thirteen plantable — twelve
// of them for the tests here, one for the population they are read in.
//
// The two that matter most are the ones a pack can still get wrong whatever
// the contract does, because only the pack knows they happened: the rule
// change that never reaches the host (#475) and the delete that leaves a
// tiering statement naming a machine that is gone. And one was not planted but
// committed — this pack read a stored port with a plain int assertion on its
// first draft, which a snapshot restore turns into zero; see
// TestTheFourthPacksSpreaderKeepsItsPortAcrossASnapshot and #542.

// fourthPackDir is where the fake fourth pack lives.
//
// Under testdata/, which is not an accident and not a hiding place: Go's
// ./... patterns skip it, so `go build ./...` never builds it and
// `go list ./...` never names it — measured on 2026-08-27, which is what #517
// asks for in place of an assumption — and therefore no coverage, evidence or
// drift artefact can ever count a fourth provider that has no upstream SDK.
// An explicit import still resolves, which is how it compiles at all.
//
// It fails rather than skipping when the directory is gone: the two discipline
// detectors below judge a population this function returns, and a population
// that silently lost a member is exactly the shape of an instrument that
// reports a disciplined repository because it looked nowhere.
func fourthPackDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "internal", "cli", "testdata", "provider-four")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the fourth pack: %v — the discipline detectors judge it beside the three "+
			"real packs, and a missing fourth pack makes them pass by looking at one less thing", err)
	}
	sources := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".go") && !strings.HasSuffix(entry.Name(), "_test.go") {
			sources++
		}
	}
	if sources == 0 {
		t.Fatalf("%s holds no Go source: the fourth pack is the positive control of both "+
			"discipline detectors, and an empty one controls nothing", dir)
	}
	return dir
}

// disciplinedPackDirs is the population the two discipline detectors of #511
// and #516 judge: the three packs the emulator serves, and the fourth that
// nothing serves.
//
// Separate from packDirs on purpose. packDirs answers "which packs does this
// emulator mount", which is what the barrage, the declines and the core's
// no-provider rule are about — and a fake pack has no routes, no Declined()
// and no barrage to run. This answers "which packs must obey the driver
// boundary", and the fourth belongs there precisely because it is the one that
// never had the habits the boundary was written to remove.
func disciplinedPackDirs(t *testing.T) []string {
	t.Helper()
	return append(packDirs(t), fourthPackDir(t))
}

// ---- The population, which is the thing that can quietly stop being true ----

// Both discipline detectors really read the fourth pack.
//
// The reason this exists rather than being obvious: dropping the fourth pack
// from disciplinedPackDirs makes every other test in this file go on passing,
// and both detectors go on passing too — with a population one member smaller
// and nothing saying so. That is the exact shape measurement-integrity calls
// an instrument reporting a disciplined repository because it looked nowhere,
// and the repository has paid for it seven times in a day.
//
// So the membership is asserted, and so is the fact that each scanner sees
// something there: a fourth pack the surface scanner resolves nothing in would
// be in the population and still measure nothing. The floors are well under
// what the pack holds today — 69 reaches, 2 files, 113 literals on 2026-08-27
// — and well over zero.
func TestTheDisciplineDetectorsReadTheFourthPack(t *testing.T) {
	dir := fourthPackDir(t)
	found := false
	for _, candidate := range disciplinedPackDirs(t) {
		if candidate == dir {
			found = true
		}
	}
	if !found {
		t.Fatalf("%s is not in the population the two discipline detectors judge: they would "+
			"pass on three packs that were cleaned up, which is the question #517 exists to stop "+
			"answering", dir)
	}

	pkg := readMachinePackage(t)
	if reaches := driverSurfaceReaches(t, pkg, dir); len(reaches) < 40 {
		t.Errorf("the surface scanner resolves %d reach(es) in the fourth pack: a pack it reads "+
			"as touching nothing is a pack the boundary does not constrain", len(reaches))
	}

	impls := driverImplementations(t, pkg)
	named := map[string]bool{}
	for _, impl := range impls {
		named[impl] = true
	}
	scan := runtimeKnowledgeLeaks(t, dir, runtimeTells(impls), named)
	if scan.files < 2 || scan.literals < 50 {
		t.Errorf("the runtime-knowledge scan read %d file(s) and %d literal(s) of the fourth pack: "+
			"a scan that parsed nothing reads exactly like a runtime-blind pack", scan.files, scan.literals)
	}
	if len(scan.leaks) != 0 {
		t.Errorf("the fourth pack names the runtime %d time(s): %v", len(scan.leaks), scan.leaks)
	}
}

// ---- The harness ------------------------------------------------------------

// fourthPack builds Provider Four over a deterministic environment backed by
// the shared contract recorder.
//
// The placements are forgotten first: they live in a package-level map keyed by
// provider and address — state that must outlive one request cannot live in a
// per-call Binding — and two tests handing out the same deterministic address
// would otherwise see the previous test's machine as its holder, and record an
// UnrouteAddress this one never asked for. That is the residue of a neighbour
// being measured instead of the subject.
func fourthPack(t *testing.T) (*providerfour.Pack, *machine.Recorder, *emulator.Env) {
	t.Helper()
	rec := machine.NewRecorder()
	env := fourthEnv(t, machine.Use(rec))
	return providerfour.New(env), rec, env
}

// fourthEnv is fourthPack's environment half, for the tests that need a
// runtime other than a plain recorder.
func fourthEnv(t *testing.T, runtime machine.Runtime) *emulator.Env {
	t.Helper()
	machine.Binding{Provider: providerfour.Name}.ForgetPlacements()
	n := 0
	env := &emulator.Env{
		Store: store.New(),
		Now:   func() time.Time { return time.Unix(1700000000, 0).UTC() },
		NewID: func() string {
			n++
			return fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	env.UseMachines(runtime)
	return env
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("the fourth pack refused an ordinary intent: %v", err)
	}
}

// gesturesOn reports the recorded kinds acting on one resource, so an assertion
// can name what it looked at rather than the whole run.
func gesturesOn(rec *machine.Recorder, resource string) []string {
	var kinds []string
	for _, event := range rec.Events() {
		if event.Resource == resource {
			kinds = append(kinds, event.Kind)
		}
	}
	return kinds
}

func counts(rec *machine.Recorder) map[string]int {
	out := map[string]int{}
	for _, kind := range rec.Sequence() {
		out[kind]++
	}
	return out
}

// webRule is the rule every scenario below hands over: one port, one block.
var webRule = providerfour.Rule{
	Direction: "ingress",
	Action:    "allow",
	Protocol:  "tcp",
	Source:    "0.0.0.0/0",
	PortFrom:  443,
	PortTo:    443,
}

// ---- What it had to supply, and what it got for nothing ---------------------

// The fourth pack asks the runtime for all eight service families, through the
// contract alone (#517).
//
// # What Provider Four had to supply
//
// Its binding — prefix, login, the two Runtime keys, the two state words, its
// image table. Its plan: which stored field becomes which interface, which
// addresses were promised. Its firewall translation: what a barrier and its
// rules mean, who wears one, which barriers a node wears, which barriers name
// another. Its isolation predicate: what "may reach" means between two of its
// segments. Its balancer's shape and its backends. Its address planning, which
// the runtime contract has no opinion on at all. And every call site: the
// contract can refuse a gesture, never require one.
//
// # What it received without writing a line
//
// The machine's host-side name and the ownership check on it. The boot order —
// addresses, then memberships, the firewall last. The published state being
// the one the effect produced. The refusal to route an address outside its own
// block. The write-back that a delete racing a launch cannot lose. The
// re-expansion of every barrier a booted node's barriers are named by. The
// network name recorded only once the runtime accepted it. The report of what
// a balancer hand-off actually delivered, and the difference between a limit
// and an incident.
//
// # What it can still forget, and this test does not pretend otherwise
//
// Calling any of it. The residue is named in #514 and measured here by the two
// omission tests below, which is the honest form: forgetting is not made
// impossible, it is made visible.
func TestTheFourthPackDrivesTheWholeDataplaneThroughTheContract(t *testing.T) {
	ctx := context.Background()
	pack, rec, env := fourthPack(t)

	// S4 — a network. The pack supplies the block and the label; the host-side
	// name is derived, handed over, and recorded only on acceptance.
	segment, err := pack.CreateSegment(ctx, "front", "10.40.0.0/24", "green")
	must(t, err)
	if segment.Runtime["four-segment"] == "" {
		t.Fatal("the segment carries no backing network name: EnsureBackingNetwork records it on " +
			"the resource, and a pack that had to write that key itself would be the pack that " +
			"writes it before the runtime accepted the network")
	}

	// S6 — a rule set. The translation is the pack's; the hand-off is the
	// skeleton's.
	barrier := pack.CreateBarrier("web")
	must(t, pack.AddRule(ctx, barrier.ID, webRule))

	// S1 and S2 — a machine on its declared plan.
	node, err := pack.CreateNode(ctx, providerfour.NodeRequest{
		Name:        "web-1",
		Image:       "four-linux",
		HomeSegment: segment.ID,
		Barriers:    []string{barrier.ID},
	})
	must(t, err)
	must(t, pack.StartNode(ctx, node.ID))

	node, err = pack.ReadNode(ctx, node.ID)
	must(t, err)
	if node.State != providerfour.StateUp {
		t.Fatalf("the node is %q after a start the runtime accepted, want %q", node.State, providerfour.StateUp)
	}
	name := node.Runtime["four-node"]
	if !strings.HasPrefix(name, "feint-four-") {
		t.Fatalf("the machine is called %q: the pack never names its own machines, the binding "+
			"derives the name from the prefix it declared", name)
	}

	// S5 — a second segment, joined hot.
	second, err := pack.CreateSegment(ctx, "back", "10.41.0.0/24", "green")
	must(t, err)
	must(t, pack.JoinSegment(ctx, node.ID, second.ID))

	// S3 — a public address, published then withdrawn.
	anchor, err := pack.CreateAnchor()
	must(t, err)
	must(t, pack.AttachAnchor(ctx, anchor.ID, node.ID))
	must(t, pack.DetachAnchor(ctx, anchor.ID))

	// S8 — a balancer, handed over whole and reported back.
	spreader, err := pack.CreateSpreader(ctx, "front-443", segment.ID, 443)
	must(t, err)
	must(t, pack.RegisterBackend(ctx, spreader.ID, node.ID))
	stored, found := env.Store.Get(providerfour.Name, providerfour.KindSpreader, spreader.ID)
	if !found {
		t.Fatal("the spreader is gone")
	}
	if stored.Runtime[machine.RuntimeBalancerDistributed] == "" {
		t.Errorf("the spreader records no delivery: the effect a hand-off produced is what the "+
			"API's readers must be able to see, and %v is what the resource holds", stored.Runtime)
	}

	// And the whole thing undone.
	must(t, pack.DeleteSpreader(ctx, spreader.ID))
	must(t, pack.LeaveSegment(ctx, node.ID, second.ID))
	must(t, pack.StopNode(ctx, node.ID))
	must(t, pack.DeleteNode(ctx, node.ID))
	must(t, pack.DeleteBarrier(ctx, barrier.ID))
	must(t, pack.DeleteSegment(ctx, second.ID))
	must(t, pack.DeleteSegment(ctx, segment.ID))

	// Every mutating gesture of the contract except IsolateNetwork, which the
	// isolation test below reaches with a runtime whose networks are born
	// joined. A floor rather than an equality: what matters is that no family
	// of the service list went unexercised, so a service the contract stopped
	// offering would fail here rather than quietly stopping to be reachable.
	seen := counts(rec)
	for _, kind := range []string{
		"EnsureNetwork", "PeerNetworks", "RemoveNetwork",
		"EnsureFirewall", "ApplyFirewall", "RemoveFirewall",
		"Start", "Stop", "Remove",
		"Attach", "Detach",
		"RouteAddress", "UnrouteAddress",
		"EnsureBalancer", "RemoveBalancer",
	} {
		if seen[kind] == 0 {
			t.Errorf("the fourth pack never asked the runtime for %s: a service family it cannot "+
				"reach is a family a real fourth pack would have to reach around", kind)
		}
	}

	// And nothing outside it. This is the half that cannot be got by writing
	// more code: a gesture the contract does not name is a gesture the pack
	// invented, and the recorder is what sees it.
	if outside := rec.OutsideContract(); len(outside) > 0 {
		t.Errorf("the fourth pack asked the runtime for %d gesture(s) the contract cannot name: %v",
			len(outside), outside)
	}
}

// ---- The order, on a pack that never wrote it -------------------------------

// The fourth pack's boot routes, then joins, then applies the firewall.
//
// The same property #510 made true of the three real packs, asserted on a pack
// that never had their comments to copy. It is the sharpest thing this lot can
// say: the order is a property of the runtime, not of a provider, and a
// provider that never heard of it still gets it.
//
// The replay is what is measured, not the first boot: a node that already
// carries its memberships and its promised address is what a restart produces,
// and it is exactly the case where a pack writing its own sequence would get
// it wrong.
func TestTheFourthPacksBootRoutesThenJoinsThenAppliesTheFirewall(t *testing.T) {
	ctx := context.Background()
	pack, rec, _ := fourthPack(t)

	segment, err := pack.CreateSegment(ctx, "front", "10.40.0.0/24", "green")
	must(t, err)
	joined, err := pack.CreateSegment(ctx, "back", "10.41.0.0/24", "green")
	must(t, err)
	barrier := pack.CreateBarrier("web")
	must(t, pack.AddRule(ctx, barrier.ID, webRule))

	node, err := pack.CreateNode(ctx, providerfour.NodeRequest{
		Name:        "web-1",
		Image:       "four-linux",
		HomeSegment: segment.ID,
		Barriers:    []string{barrier.ID},
		Public:      true,
	})
	must(t, err)
	must(t, pack.StartNode(ctx, node.ID))
	must(t, pack.JoinSegment(ctx, node.ID, joined.ID))
	must(t, pack.StopNode(ctx, node.ID))

	// Everything above is setup; the replay is the subject.
	before := len(rec.Events())
	must(t, pack.StartNode(ctx, node.ID))

	var order []string
	for _, event := range rec.Events()[before:] {
		switch event.Kind {
		case "Start", "RouteAddress", "Attach", "ApplyFirewall":
			order = append(order, event.Kind)
		}
	}
	want := []string{"Start", "RouteAddress", "Attach", "ApplyFirewall"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("the fourth pack's boot replay is %v, want %v.\nThe order — the promised "+
			"addresses, then the memberships, the firewall last — is the runtime's property and "+
			"the reconciler's to hold. A pack that had to write it would be the fourth pack to "+
			"write it in a fourth order", order, want)
	}
}

// ---- The two omissions a fourth pack can still commit -----------------------

// A rule the fourth pack adds reaches the runtime.
//
// This is #475 reproduced on a pack that did not exist when #475 was measured:
// two packs of three described rules their host never enforced, for months,
// because the hand-off was a line somebody had to remember to write. Nothing
// in the contract can force it — the pack is the only party that knows its
// rules changed — so the residue is held here, by name, and the proof is that
// removing the SyncGroup call from providerfour.AddRule turns this red:
// tools/falsify/specs/provider-four.json plants exactly that.
func TestTheFourthPacksRuleChangeReachesTheRuntime(t *testing.T) {
	ctx := context.Background()
	pack, rec, _ := fourthPack(t)

	barrier := pack.CreateBarrier("web")
	if got := counts(rec)["EnsureFirewall"]; got != 0 {
		t.Fatalf("%d rule set(s) written for a barrier nobody wears and nothing fills", got)
	}
	must(t, pack.AddRule(ctx, barrier.ID, webRule))

	set := machine.FirewallName("four", barrier.ID)
	if kinds := gesturesOn(rec, set); len(kinds) == 0 {
		t.Fatalf("the barrier gained a rule and the runtime heard nothing about %s. The API "+
			"would describe a rule that filters nothing, every gate would stay green, and that "+
			"is #475 exactly — recorded gestures: %v", set, rec.Sequence())
	}

	// The rule itself, not merely the call: a hand-off that wrote an empty set
	// would satisfy the assertion above and enforce nothing.
	for _, event := range rec.Events() {
		if event.Kind != "EnsureFirewall" || event.Resource != set {
			continue
		}
		spec, ok := event.Args.(machine.FirewallSpec)
		if !ok {
			t.Fatalf("EnsureFirewall carried %T rather than a rule set", event.Args)
		}
		if len(spec.Rules) != 1 || spec.Rules[0].PortFrom != 443 {
			t.Errorf("the rule set handed over carries %v, not the rule the barrier gained", spec.Rules)
		}
		return
	}
	t.Fatal("no rule set was written at all")
}

// A node the fourth pack boots is put into every barrier that names its
// barriers.
//
// The second half of #475, and the one that reads as working when it is not: a
// tiering statement — this barrier accepts that barrier and nobody else —
// contains no machine at all until the machines that wear the named barrier
// have addresses. The re-expansion is the skeleton's; what this holds is that
// the fourth pack goes through it.
func TestTheFourthPacksBootReExpandsTheBarriersThatNameItsOwn(t *testing.T) {
	ctx := context.Background()
	pack, rec, _ := fourthPack(t)

	segment, err := pack.CreateSegment(ctx, "front", "10.40.0.0/24", "green")
	must(t, err)
	front := pack.CreateBarrier("front")
	back := pack.CreateBarrier("back")
	must(t, pack.AddRule(ctx, front.ID, webRule))
	// The tiering statement: the back tier accepts the front tier, nobody else.
	must(t, pack.AddRule(ctx, back.ID, providerfour.Rule{
		Direction:     "ingress",
		Action:        "allow",
		Protocol:      "tcp",
		SourceBarrier: front.ID,
		PortFrom:      5432,
		PortTo:        5432,
	}))

	node, err := pack.CreateNode(ctx, providerfour.NodeRequest{
		Name:        "web-1",
		Image:       "four-linux",
		HomeSegment: segment.ID,
		Barriers:    []string{front.ID},
	})
	must(t, err)
	must(t, pack.StartNode(ctx, node.ID))

	set := machine.FirewallName("four", back.ID)
	var last machine.FirewallSpec
	for _, event := range rec.Events() {
		if event.Kind == "EnsureFirewall" && event.Resource == set {
			last, _ = event.Args.(machine.FirewallSpec)
		}
	}
	if len(last.Rules) == 0 {
		t.Fatalf("the back tier's rule set was never re-expanded after the front tier booted: %v",
			rec.Sequence())
	}
	if last.Rules[0].Source == "" {
		t.Errorf("the back tier accepts %q: the tiering statement names the front tier and "+
			"contains none of its machines, which is #475's own sentence", last.Rules[0].Source)
	}
}

// A node the fourth pack deletes leaves no barrier naming it.
//
// The mirror of the boot half, and the one nothing in this repository held
// before: a tiering statement that still names the address of a machine that
// is gone describes traffic from nowhere, and the address may well be handed
// to somebody else. It is the pack's to say, because only the pack knows a
// node's barriers changed — so it is a named test rather than a guarantee.
func TestTheFourthPacksDeleteReExpandsTheBarriersThatNameIt(t *testing.T) {
	ctx := context.Background()
	pack, rec, _ := fourthPack(t)

	segment, err := pack.CreateSegment(ctx, "front", "10.40.0.0/24", "green")
	must(t, err)
	front := pack.CreateBarrier("front")
	back := pack.CreateBarrier("back")
	must(t, pack.AddRule(ctx, back.ID, providerfour.Rule{
		Direction:     "ingress",
		Action:        "allow",
		Protocol:      "tcp",
		SourceBarrier: front.ID,
		PortFrom:      5432,
		PortTo:        5432,
	}))
	node, err := pack.CreateNode(ctx, providerfour.NodeRequest{
		Name:        "web-1",
		Image:       "four-linux",
		HomeSegment: segment.ID,
		Barriers:    []string{front.ID},
	})
	must(t, err)
	must(t, pack.StartNode(ctx, node.ID))

	set := machine.FirewallName("four", back.ID)
	written := 0
	for _, event := range rec.Events() {
		if event.Kind == "EnsureFirewall" && event.Resource == set {
			written++
		}
	}
	must(t, pack.DeleteNode(ctx, node.ID))

	var last machine.FirewallSpec
	rewritten := 0
	for _, event := range rec.Events() {
		if event.Kind == "EnsureFirewall" && event.Resource == set {
			rewritten++
			last, _ = event.Args.(machine.FirewallSpec)
		}
	}
	if rewritten == written {
		t.Fatalf("the back tier's rule set was not rewritten when the machine it named was "+
			"deleted: it still accepts an address nothing answers on, and the next node to take "+
			"that address inherits the permission. Recorded: %v", rec.Sequence())
	}
	for _, rule := range last.Rules {
		if rule.Source != "" {
			t.Errorf("the back tier still accepts %q after the only front-tier machine was "+
				"deleted", rule.Source)
		}
	}
}

// ---- What the fourth pack received without writing it -----------------------

// refusingRuntime is a runtime that takes every gesture except a start.
//
// It is the only way to ask the question #484 answers: what does the API say
// about a machine the host refused to boot? A recorder that always succeeds
// cannot be asked.
type refusingRuntime struct{ *machine.Recorder }

func (refusingRuntime) Start(context.Context, machine.Spec) (machine.Machine, error) {
	return machine.Machine{}, errors.New("this host refuses to start machines")
}

// The state the fourth pack publishes is the one the effect produced.
//
// #484, obtained without writing a line: Binding.PowerOn answers the pack's own
// FailedState when the host did not deliver, and the pack never touches
// res.State on the start path. Answering "up" because the request asked for a
// start is the exact lie this emulator exists to avoid — a client would wait
// for an address that never comes, on a machine that does not exist.
func TestTheFourthPackPublishesTheStateTheEffectProduced(t *testing.T) {
	ctx := context.Background()
	env := fourthEnv(t, machine.Use(refusingRuntime{machine.NewRecorder()}))
	pack := providerfour.New(env)

	node, err := pack.CreateNode(ctx, providerfour.NodeRequest{Name: "web-1", Image: "four-linux"})
	must(t, err)
	must(t, pack.StartNode(ctx, node.ID))

	stored, found := env.Store.Get(providerfour.Name, providerfour.KindNode, node.ID)
	if !found {
		t.Fatal("the node is gone")
	}
	if stored.State != providerfour.StateBroken {
		t.Errorf("a start the host refused leaves the node %q, want %q: the published state is "+
			"the one the effect produced, never the one the intent visited",
			stored.State, providerfour.StateBroken)
	}
	if stored.Runtime["four-node"] != "" {
		t.Errorf("the node names a machine %q that never started", stored.Runtime["four-node"])
	}
}

// The fourth pack cannot route an address it never handed out.
//
// Obtained for free, and it is the guard a fourth pack would be least likely
// to write: a stored address is untrusted input, because PUT /_feint/state and
// `feint snapshot load` restore Attrs verbatim and the snapshot format is
// designed to be loaded into another instance. Routing an arbitrary value
// would send the host's traffic for that address into a container.
//
// The hostile value is written straight into the store, which is exactly what
// a crafted snapshot does — the pack's own API never produces one.
func TestTheFourthPackRefusesToRouteAnAddressOutsideItsOwnBlock(t *testing.T) {
	ctx := context.Background()
	pack, rec, env := fourthPack(t)

	node, err := pack.CreateNode(ctx, providerfour.NodeRequest{Name: "web-1", Image: "four-linux"})
	must(t, err)
	must(t, pack.StartNode(ctx, node.ID))

	anchor, err := pack.CreateAnchor()
	must(t, err)
	// The address a snapshot could carry: not this pack's block, and a real
	// address on the operator's own network.
	hostile, found := env.Store.Get(providerfour.Name, providerfour.KindAnchor, anchor.ID)
	if !found {
		t.Fatal("the anchor is gone")
	}
	base := hostile.Clone()
	hostile.Attrs["address"] = "192.168.1.1"
	if !env.Store.Commit(base, hostile, time.Unix(1700000000, 0).UTC()) {
		t.Fatal("the crafted anchor could not be written")
	}

	before := counts(rec)["RouteAddress"]
	must(t, pack.AttachAnchor(ctx, anchor.ID, node.ID))
	if after := counts(rec)["RouteAddress"]; after != before {
		t.Errorf("the runtime was asked to route 192.168.1.1, an address outside this pack's "+
			"own block: %v", gesturesOn(rec, "192.168.1.1"))
	}

	// And the accepting half, because a guard that refuses everything passes
	// every attack test and breaks the product.
	honest, err := pack.CreateAnchor()
	must(t, err)
	must(t, pack.AttachAnchor(ctx, honest.ID, node.ID))
	if after := counts(rec)["RouteAddress"]; after != before+1 {
		t.Errorf("an address this pack itself handed out was not routed either: the guard is "+
			"refusing the product, not the attack — %v", rec.Sequence())
	}
}

// The fourth pack's boot attachment rides the launch; its memberships do not.
//
// Two lists in the plan rather than one, and the difference is measured rather
// than aesthetic: editing a live interface re-plugs it and the guest loses its
// lease. A pack declares which is which; nothing else about it is the pack's.
func TestTheFourthPacksBootAttachmentRidesTheLaunchAndItsMembershipsDoNot(t *testing.T) {
	ctx := context.Background()
	pack, rec, _ := fourthPack(t)

	home, err := pack.CreateSegment(ctx, "front", "10.40.0.0/24", "green")
	must(t, err)
	later, err := pack.CreateSegment(ctx, "back", "10.41.0.0/24", "green")
	must(t, err)

	node, err := pack.CreateNode(ctx, providerfour.NodeRequest{
		Name:        "web-1",
		Image:       "four-linux",
		HomeSegment: home.ID,
	})
	must(t, err)
	must(t, pack.StartNode(ctx, node.ID))

	var spec machine.Spec
	for _, event := range rec.Events() {
		if event.Kind == "Start" {
			spec, _ = event.Args.(machine.Spec)
		}
	}
	if len(spec.Attachments) != 1 || spec.Attachments[0].Network != home.Runtime["four-segment"] {
		t.Fatalf("the launch carried %v, not the node's home segment: an attachment that does "+
			"not ride the start is one the driver has to add to a live machine",
			spec.Attachments)
	}
	if attached := counts(rec)["Attach"]; attached != 0 {
		t.Errorf("%d interface(s) were attached to a machine that had just declared them on its "+
			"boot plan", attached)
	}

	must(t, pack.JoinSegment(ctx, node.ID, later.ID))
	if attached := counts(rec)["Attach"]; attached != 1 {
		t.Errorf("a segment joined on a running node produced %d attach(es), want 1", attached)
	}
}

// The fourth pack's segments reach each other only when they share a realm.
//
// The predicate is the pack's — it is the one thing about isolation only a
// provider knows — and everything else is the shared pass: which runtimes need
// peering and which need reject rules, what a network deleted under the pass
// means, and the fact that there is exactly one writer. Two packs of three had
// written this loop before it was shared, and a fourth would have written it a
// fourth time.
func TestTheFourthPacksSegmentsReachEachOtherOnlyInTheSameRealm(t *testing.T) {
	ctx := context.Background()
	pack, rec, _ := fourthPack(t)

	green, err := pack.CreateSegment(ctx, "green-1", "10.40.0.0/24", "green")
	must(t, err)
	blue, err := pack.CreateSegment(ctx, "blue-1", "10.41.0.0/24", "blue")
	must(t, err)
	sameRealm, err := pack.CreateSegment(ctx, "green-2", "10.42.0.0/24", "green")
	must(t, err)

	peers := map[string][]string{}
	for _, event := range rec.Events() {
		if event.Kind != "PeerNetworks" {
			continue
		}
		list, _ := event.Args.([]string)
		peers[event.Resource] = append([]string(nil), list...)
	}
	for _, want := range []struct {
		network string
		peers   []string
	}{
		{green.Runtime["four-segment"], []string{sameRealm.Runtime["four-segment"]}},
		{sameRealm.Runtime["four-segment"], []string{green.Runtime["four-segment"]}},
		{blue.Runtime["four-segment"], nil},
	} {
		got := peers[want.network]
		sort.Strings(got)
		sort.Strings(want.peers)
		if strings.Join(got, ",") != strings.Join(want.peers, ",") {
			t.Errorf("%s is peered with %v, want %v", want.network, got, want.peers)
		}
	}

	// The other half of the fork, on a runtime whose networks are born joined:
	// the same predicate, expressed as reject rules instead of peerings, and
	// the pack says nothing about which.
	joined := machine.NewRecorder()
	joined.Joined = true
	env := fourthEnv(t, machine.Use(joined))
	other := providerfour.New(env)
	first, err := other.CreateSegment(ctx, "green-1", "10.40.0.0/24", "green")
	must(t, err)
	_, err = other.CreateSegment(ctx, "blue-1", "10.41.0.0/24", "blue")
	must(t, err)

	var foreign []string
	for _, event := range joined.Events() {
		if event.Kind == "IsolateNetwork" && event.Resource == first.Runtime["four-segment"] {
			foreign, _ = event.Args.([]string)
		}
	}
	if len(foreign) != 1 || foreign[0] != "10.41.0.0/24" {
		t.Errorf("the green segment keeps %v out, want the blue segment's block: the same "+
			"predicate has to hold on both runtimes, and the pack never chose between them", foreign)
	}
}

// The fourth pack records what the runtime took, and tells a limit from an
// incident.
//
// #481 and #483, both obtained: a host balancer distributing to nobody while
// the API described two healthy backends was found by a person reading the
// host, because the only trace was a WARN. Here the delivery is written onto
// the resource, readable through /_feint/state; and a runtime that does not
// balance answers a named limit rather than an empty delivery, which reads
// exactly like a balancer with no backend — a state every stack passes
// through.
func TestTheFourthPacksSpreaderRecordsTheDeliveryAndNotTheIntent(t *testing.T) {
	ctx := context.Background()
	pack, _, env := fourthPack(t)

	segment, err := pack.CreateSegment(ctx, "front", "10.40.0.0/24", "green")
	must(t, err)
	node, err := pack.CreateNode(ctx, providerfour.NodeRequest{
		Name:        "web-1",
		Image:       "four-linux",
		HomeSegment: segment.ID,
	})
	must(t, err)
	must(t, pack.StartNode(ctx, node.ID))

	spreader, err := pack.CreateSpreader(ctx, "front-443", segment.ID, 443)
	must(t, err)
	must(t, pack.RegisterBackend(ctx, spreader.ID, node.ID))

	stored, found := env.Store.Get(providerfour.Name, providerfour.KindSpreader, spreader.ID)
	if !found {
		t.Fatal("the spreader is gone")
	}
	if got := stored.Runtime[machine.RuntimeBalancerDistributed]; got != "10.40.0.10" {
		t.Errorf("the spreader records %q as distributed, want the backend's own address", got)
	}

	// A runtime that cannot balance: the pack leaves its family a record and
	// asks the host for nothing, rather than reporting nothing distributed
	// with no reason beside it.
	silent := fourthEnv(t, machine.Use(machine.Noop{}))
	quiet := providerfour.New(silent)
	other, err := quiet.CreateSegment(ctx, "front", "10.40.0.0/24", "green")
	must(t, err)
	balancer, err := quiet.CreateSpreader(ctx, "front-443", other.ID, 443)
	must(t, err)
	held, found := silent.Store.Get(providerfour.Name, providerfour.KindSpreader, balancer.ID)
	if !found {
		t.Fatal("the spreader is gone")
	}
	if _, recorded := held.Runtime[machine.RuntimeBalancerDistributed]; recorded {
		t.Errorf("a runtime that does not balance produced a delivery record: %v", held.Runtime)
	}
}

// ---- What the fourth pack rediscovered on its own ---------------------------

// The fourth pack's balancer keeps its port across a snapshot.
//
// This test exists because the fake pack got it wrong first, which makes it the
// most useful thing in this file: `res.Attrs["port"].(int)` is what anybody
// writes, and it is wrong. Attrs is decoded as map[string]any, so a stored
// number comes back a float64 and the int assertion yields zero — a balancer
// listening on port 0 after `feint snapshot load`, with the API still
// describing 443.
//
// It is a rediscovery, not a discovery, and that is the finding:
// exoscale/privatenetworks.go carries intOf with the same one-line explanation,
// and the two other real packs do not — outscale/volumes.go and
// outscale/snapshots.go read Attrs["Size"].(int) at three sites. A lesson
// living in one pack of three is a lesson a fourth pack cannot inherit, which
// is #475's shape one storey below the runtime contract.
func TestTheFourthPacksSpreaderKeepsItsPortAcrossASnapshot(t *testing.T) {
	ctx := context.Background()
	pack, _, env := fourthPack(t)

	segment, err := pack.CreateSegment(ctx, "front", "10.40.0.0/24", "green")
	must(t, err)
	node, err := pack.CreateNode(ctx, providerfour.NodeRequest{
		Name:        "web-1",
		Image:       "four-linux",
		HomeSegment: segment.ID,
	})
	must(t, err)
	must(t, pack.StartNode(ctx, node.ID))
	spreader, err := pack.CreateSpreader(ctx, "front-443", segment.ID, 443)
	must(t, err)

	// Through the door a snapshot really travels: the format is documented as
	// meant to outlive its instance and be loaded into another one.
	var saved bytes.Buffer
	if err := env.Store.Snapshot(&saved); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := store.New()
	if err := restored.Restore(bytes.NewReader(saved.Bytes())); err != nil {
		t.Fatalf("restore: %v", err)
	}

	rec := machine.NewRecorder()
	next := fourthEnv(t, machine.Use(rec))
	next.Store = restored
	revived := providerfour.New(next)
	must(t, revived.RegisterBackend(ctx, spreader.ID, node.ID))

	var spec machine.BalancerSpec
	for _, event := range rec.Events() {
		if event.Kind == "EnsureBalancer" {
			spec, _ = event.Args.(machine.BalancerSpec)
		}
	}
	if len(spec.Listeners) != 1 {
		t.Fatalf("the revived spreader was handed over with %d listener(s): %v", len(spec.Listeners), spec)
	}
	if spec.Listeners[0].Listen != 443 {
		t.Errorf("the revived spreader listens on %d, want 443: a stored number comes back a "+
			"float64, so the plain int assertion yields zero and the host distributes a port "+
			"nothing asked for while the API still describes 443",
			spec.Listeners[0].Listen)
	}
}

// A restored node still wears its barriers, joins its segments and knows its
// addresses (#567).
//
// The numeric half of this is the test above, and it is the half a shared
// reader could fix: a number that crossed JSON has exactly one right answer,
// and resource.Number gives it. These three have none that internal/core may
// give — recovering a []Rule means knowing Rule, which is the pack's own type
// (rule 5) — so the pack stores the shape the door returns instead.
//
// Measured on 2026-08-27, before the fix, through store.Snapshot then
// store.Restore into a fresh store: []Rule came back nil, []string came back
// []any, map[string]string came back map[string]any. Every one of the pack's
// readers asserted the Go type, so a restored node wore no barriers, joined no
// segments and had no address, while the API went on describing all three.
//
// Behaviour rather than shape, deliberately: this asserts what the runtime is
// asked to do with the restored node, not what its Attrs look like. A test
// that compared the maps would pass on a pack that stored the right shape and
// read it back with the wrong assertion.
func TestTheFourthPacksNodeKeepsWhatItWearsAcrossASnapshot(t *testing.T) {
	ctx := context.Background()
	pack, _, env := fourthPack(t)

	home, err := pack.CreateSegment(ctx, "front", "10.40.0.0/24", "green")
	must(t, err)
	later, err := pack.CreateSegment(ctx, "back", "10.41.0.0/24", "green")
	must(t, err)
	barrier := pack.CreateBarrier("web")
	must(t, pack.AddRule(ctx, barrier.ID, providerfour.Rule{
		Direction: "ingress", Action: "allow", Protocol: "tcp", Source: "0.0.0.0/0",
		PortFrom: 443, PortTo: 443,
	}))
	node, err := pack.CreateNode(ctx, providerfour.NodeRequest{
		Name:        "web-1",
		Image:       "four-linux",
		HomeSegment: home.ID,
		Barriers:    []string{barrier.ID},
	})
	must(t, err)
	must(t, pack.StartNode(ctx, node.ID))
	must(t, pack.JoinSegment(ctx, node.ID, later.ID))

	// The door a snapshot really travels: the format is documented as meant to
	// outlive its instance and be loaded into another one.
	var saved bytes.Buffer
	if err := env.Store.Snapshot(&saved); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := store.New()
	if err := restored.Restore(bytes.NewReader(saved.Bytes())); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// A reboot on the revived pack replays the whole post-boot order, which is
	// what reads all four stored collections at once.
	rec := machine.NewRecorder()
	next := fourthEnv(t, machine.Use(rec))
	next.Store = restored
	revived := providerfour.New(next)
	must(t, revived.RebootNode(ctx, node.ID))

	var booted machine.Spec
	var attached []machine.Attachment
	var binding machine.FirewallBinding
	var spec machine.FirewallSpec
	for _, event := range rec.Events() {
		switch event.Kind {
		case "Start":
			booted, _ = event.Args.(machine.Spec)
		case "Attach":
			if att, ok := event.Args.(machine.Attachment); ok {
				attached = append(attached, att)
			}
		case "ApplyFirewall":
			binding, _ = event.Args.(machine.FirewallBinding)
		case "EnsureFirewall":
			spec, _ = event.Args.(machine.FirewallSpec)
		}
	}

	// addresses: the home segment rides the launch, at the address the store
	// promised for it.
	if len(booted.Attachments) != 1 || booted.Attachments[0].Address != "10.40.0.10" {
		t.Errorf("the revived node booted with %v, want one interface at 10.40.0.10: a restored "+
			"map[string]string read as map[string]string is empty, so the interface comes up "+
			"with no address while the API still publishes one", booted.Attachments)
	}

	// segments: and the membership joined afterwards is joined again.
	if len(attached) != 1 || attached[0].Address != "10.41.0.10" {
		t.Errorf("the revived node was attached to %v, want the second segment at 10.41.0.10: a "+
			"restored []string read as []string is nil, so the node joins nothing while the API "+
			"still lists its segments", attached)
	}

	// barriers: the rule set the node wears reaches the runtime with it.
	if len(binding.Names) != 1 {
		t.Errorf("the revived node was bound to %d rule set(s), want 1: a restored []string read "+
			"as []string is nil, so the machine wears nothing while the API still says it does "+
			"— %v", len(binding.Names), binding.Names)
	}

	// rules: and the set itself still carries what was declared into it.
	if len(spec.Rules) != 1 {
		t.Fatalf("the revived rule set carries %d rule(s), want 1: a restored []Rule read as "+
			"[]Rule is nil, so an empty set is handed over under a name the API describes as "+
			"filtering — %v", len(spec.Rules), spec.Rules)
	}
	if spec.Rules[0].PortFrom != 443 || spec.Rules[0].Source != "0.0.0.0/0" {
		t.Errorf("the revived rule is %+v, want the 443 rule that was declared", spec.Rules[0])
	}
}

// ---- The fourth pack is a resource like any other ---------------------------

// The fourth pack's own store use obeys the shared write-back.
//
// Not a machine property, and it is here because it is the trap the contract
// cannot see: resource.Clone shares nested values with the store, so a pack
// writing into a map it read out of Attrs writes through the store's own copy,
// outside every lock and every conditional write-back. updateNic paid for this
// once (#295); a fourth pack has no scar to remember it by.
//
// The map rather than the slice, and that distinction is measured rather than
// assumed: appending to a slice read out of Attrs reallocates whenever the
// capacity is exhausted, so the aliasing shows up only sometimes and a test
// built on it passes for the wrong reason. Writing a key into a shared map
// aliases every time.
func TestTheFourthPacksNestedAttributesAreNeverWrittenThroughTheStore(t *testing.T) {
	ctx := context.Background()
	pack, _, env := fourthPack(t)

	home, err := pack.CreateSegment(ctx, "front", "10.40.0.0/24", "green")
	must(t, err)
	later, err := pack.CreateSegment(ctx, "back", "10.41.0.0/24", "green")
	must(t, err)
	node, err := pack.CreateNode(ctx, providerfour.NodeRequest{
		Name:        "web-1",
		Image:       "four-linux",
		HomeSegment: home.ID,
	})
	must(t, err)
	must(t, pack.StartNode(ctx, node.ID))

	// The copy a client is holding — a read that has already answered — while
	// another request joins a second segment.
	stored, found := env.Store.Get(providerfour.Name, providerfour.KindNode, node.ID)
	if !found {
		t.Fatal("the node is gone")
	}
	// map[string]any, because that is the shape the pack stores since #567 —
	// what this measures is the map's aliasing, not its element type.
	held, _ := stored.Attrs["addresses"].(map[string]any)
	if len(held) != 1 {
		t.Fatalf("the node carries %d address(es) on one segment, want 1: %v", len(held), held)
	}

	must(t, pack.JoinSegment(ctx, node.ID, later.ID))

	if len(held) != 1 {
		t.Errorf("the answer already given to a reader now carries %d addresses: the pack wrote "+
			"into a map the store still shares, outside the lock and outside the conditional "+
			"write-back — %v", len(held), held)
	}
	after, found := env.Store.Get(providerfour.Name, providerfour.KindNode, node.ID)
	if !found {
		t.Fatal("the node is gone")
	}
	if addresses, _ := after.Attrs["addresses"].(map[string]any); len(addresses) != 2 {
		t.Errorf("the store holds %d address(es) after a second segment was joined", len(addresses))
	}
}
