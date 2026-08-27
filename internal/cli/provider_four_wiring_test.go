package cli

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	providerfour "github.com/stephrobert/feint/internal/cli/testdata/provider-four"
	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/store"
)

// What a pack that declares an orchestrator without wiring it is told (#543).
//
// Beside the fourth pack, and for its reason: the three real packs wire all
// five GroupSync fields, so nothing here is broken for them and nothing they
// do could measure this. The pack that can be in this state is the one that
// never had the habits — testdata/provider-four — and the state it can be in
// is the zero value of a struct it has to build, which compiles.
//
// The reproduction, measured on 2026-08-27 before the fix, calling
// Reconciler.PowerOn over a zero GroupSync:
//
//	with an enforcing runtime: PANIC: invalid memory address or nil pointer dereference
//	with machine.Noop:         no panic; PowerOn=true state=up
//
// and through a mounted route rather than a direct call, which was the half
// #543 recorded as read and not run:
//
//	the client saw: Post "http://127.0.0.1:.../repro/v1/boot": EOF
//	the emulator still answers: status 204
//
// So net/http's per-connection recover is indeed the only thing between this
// and a dead process — `grep -rn 'recover()' internal/` finds none, and no
// pack starts a goroutine — and what the operator got was a dropped connection
// and a stack trace naming machine/groupsync.go, with nothing naming their
// pack or the field they forgot.
//
// The asymmetry is what these tests hold rather than the panic. `--vm off` is
// the default, what CI runs and what every conformance leg runs, and it took
// the `enforcer() == nil` branch before anything could touch a nil field: a
// pack that had wired nothing was green everywhere it was ever measured, and
// red on the first poweron of the first operator to attach a real runtime.

// reportingEnv is fourthEnv with a logger a test can read, because "reports
// rather than panics" is an assertion about what was said, and a discarded log
// makes it an assertion that nothing crashed.
func reportingEnv(t *testing.T, runtime machine.Runtime) (*emulator.Env, *bytes.Buffer) {
	t.Helper()
	var log bytes.Buffer
	n := 0
	env := &emulator.Env{
		Store: store.New(),
		Now:   func() time.Time { return time.Unix(1700000000, 0).UTC() },
		NewID: func() string {
			n++
			return fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
		},
		Log: slog.New(slog.NewTextHandler(&log, nil)),
	}
	env.UseMachines(runtime)
	return env, &log
}

// unwiredReconciler is the shape a fourth pack compiles with and forgets: the
// binding is there, because nothing works without it, and the four translation
// fields are not.
func unwiredReconciler(env *emulator.Env) machine.Reconciler {
	return machine.Reconciler{
		Groups: machine.GroupSync{Binding: fourthBinding(env)},
		PlanOf: func(*resource.Resource) machine.Plan { return machine.Plan{} },
	}
}

func fourthBinding(env *emulator.Env) machine.Binding {
	return env.Bind(machine.Binding{
		Provider:     "four",
		Prefix:       "feint-four-",
		RuntimeKey:   "node",
		AddressKey:   "address",
		RunningState: "up",
		FailedState:  "broken",
		Log:          env.Log,
	})
}

func aNode(env *emulator.Env) *resource.Resource {
	res := resource.New("node-1", "node", resource.Tenant{Provider: "four"}, "stopped", env.Now())
	res.Runtime = map[string]string{"node": "feint-four-node-1"}
	env.Store.Put(res)
	return res
}

// A boot under an enforcing runtime reports the omission instead of panicking.
//
// machine.NewRecorder implements Firewaller, so this takes the enforcing branch
// — the one only `--vm incus` and `--vm incus-ovn` used to reach — without
// needing a host.
func TestABootUnderAnEnforcingRuntimeReportsAnUnwiredGroupSync(t *testing.T) {
	env, log := reportingEnv(t, machine.Use(machine.NewRecorder()))
	res := aNode(env)

	// A panic here fails the test by crashing it, which is the honest form: a
	// recover() would turn the defect back into something a test can pass over.
	started := unwiredReconciler(env).PowerOn(context.Background(), res, machine.Boot{Image: "four-linux"})

	// The machine plane is not broken by a firewall omission, which is remedy
	// one: the node really did start.
	if !started {
		t.Errorf("the node did not start: a pack that forgot its firewall translation must still "+
			"boot machines — the log says %q", log.String())
	}
	if res.State != "up" {
		t.Errorf("the node is %q, want up", res.State)
	}

	// And the operator is told, in a sentence naming the pack and the fields.
	said := log.String()
	if !strings.Contains(said, "four") {
		t.Errorf("the report does not name the pack: %q", said)
	}
	for _, field := range []string{"SpecOf", "Wearers", "WornIDs", "Group"} {
		if !strings.Contains(said, field) {
			t.Errorf("the report does not name the missing %s: %q", field, said)
		}
	}
}

// The omission is reported under every runtime, which is the finding.
//
// Not "the panic is gone": the panic was only ever the symptom under one mode.
// What #543 measured is that the mode everybody runs could not see the defect
// at all, so a control that only stopped the crash would leave the fourth pack
// silent in both modes — worse than loud in one, for the reason #475 was
// expensive.
func TestAnUnwiredGroupSyncIsReportedUnderEveryRuntime(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		runtime machine.Runtime
	}{
		{"an enforcing runtime, what --vm incus-ovn gives", machine.Use(machine.NewRecorder())},
		{"machine.Noop, the --vm off default and what CI runs", machine.Use(machine.Noop{})},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			env, log := reportingEnv(t, tc.runtime)
			res := aNode(env)

			unwiredReconciler(env).PowerOn(context.Background(), res, machine.Boot{Image: "four-linux"})

			if !strings.Contains(log.String(), "SpecOf") {
				t.Errorf("under %s the omission was not reported at all: %q — this is the mode "+
					"asymmetry #543 is about, and a fix that only silences the panic reintroduces it",
					tc.mode, log.String())
			}
		})
	}
}

// The report names the pack and exactly the fields that are missing, and says
// nothing about a pack that wired them or declared it wires nothing.
//
// Four cases, and the last two are the accepting half without which a guard
// that refuses everything would pass the three tests above and break all three
// real packs on their next boot.
func TestAPackThatWiredNoGroupSyncIsToldWhichFieldIsMissing(t *testing.T) {
	full := func(env *emulator.Env) machine.GroupSync {
		return machine.GroupSync{
			Binding: fourthBinding(env),
			SpecOf:  func(_, _ *resource.Resource) machine.FirewallSpec { return machine.FirewallSpec{} },
			Wearers: func(*resource.Resource) []*resource.Resource { return nil },
			WornIDs: func(*resource.Resource) []string { return nil },
			Group:   func(string) (*resource.Resource, bool) { return nil, false },
		}
	}

	for _, tc := range []struct {
		name    string
		build   func(*emulator.Env) machine.GroupSync
		names   []string
		silent  bool
		unnamed []string
	}{
		{
			name:  "nothing wired",
			build: func(env *emulator.Env) machine.GroupSync { return machine.GroupSync{Binding: fourthBinding(env)} },
			names: []string{"four", "SpecOf", "Wearers", "WornIDs", "Group"},
		},
		{
			name: "one field forgotten",
			build: func(env *emulator.Env) machine.GroupSync {
				groups := full(env)
				groups.WornIDs = nil
				return groups
			},
			names: []string{"four", "WornIDs"},
			// The others are named in the sentence's tail, which lists what a
			// wiring pack supplies; what must not happen is the report calling
			// a wired field missing, so the assertion is on the prefix.
		},
		{
			name: "a pack that declares it enforces nothing",
			build: func(env *emulator.Env) machine.GroupSync {
				return machine.GroupSync{Binding: fourthBinding(env), EnforcesNothing: true}
			},
			silent: true,
		},
		{
			name:   "a pack that wired all four",
			build:  full,
			silent: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, log := reportingEnv(t, machine.Use(machine.NewRecorder()))
			res := aNode(env)
			rec := machine.Reconciler{
				Groups: tc.build(env),
				PlanOf: func(*resource.Resource) machine.Plan { return machine.Plan{} },
			}
			rec.PowerOn(context.Background(), res, machine.Boot{Image: "four-linux"})

			said := log.String()
			if tc.silent {
				if strings.Contains(said, "the firewall step is skipped") {
					t.Errorf("a pack that wired what it meant to was reported anyway: %q — a control "+
						"that cannot be satisfied is a control somebody works around", said)
				}
				return
			}
			if !strings.Contains(said, "the firewall step is skipped") {
				t.Fatalf("nothing was reported for %s: %q", tc.name, said)
			}
			for _, name := range tc.names {
				if !strings.Contains(said, name) {
					t.Errorf("the report does not name %s: %q", name, said)
				}
			}
			// The message must not accuse a field that is wired: "missing:
			// SpecOf, Wearers, WornIDs, Group" for a pack missing one is a
			// sentence that sends its reader to the wrong four lines.
			if tc.name == "one field forgotten" {
				before, _, _ := strings.Cut(said, "a pack that hands its groups")
				for _, wired := range []string{"SpecOf", "Wearers", "Group"} {
					if strings.Contains(before, wired+",") || strings.Contains(before, wired+":") {
						t.Errorf("the report calls the wired %s missing: %q", wired, before)
					}
				}
			}
		})
	}
}

// A boot with no declared plan is refused, and says so.
//
// PlanOf is the other required func field of the Reconciler, and #543 recorded
// it as untested. It is measured now: it panics under machine.Noop exactly as
// it does under an enforcing runtime, so unlike the GroupSync fields it is not
// mode-dependent and a pack that forgets it fails its own first test. What it
// still lacked was a sentence: the operator got a stack trace in
// machine/plan.go rather than the name of the pack and the field.
//
// It refuses where GroupSync degrades, and plan.go says why: a machine started
// with no plan is a machine on no network that the API would call running.
func TestABootWithNoDeclaredPlanIsRefusedRatherThanPanicking(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		runtime machine.Runtime
	}{
		{"an enforcing runtime", machine.Use(machine.NewRecorder())},
		{"machine.Noop, the --vm off default", machine.Use(machine.Noop{})},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			env, log := reportingEnv(t, tc.runtime)
			res := aNode(env)
			rec := machine.Reconciler{Groups: machine.GroupSync{
				Binding:         fourthBinding(env),
				EnforcesNothing: true,
			}}

			if rec.PowerOn(context.Background(), res, machine.Boot{Image: "four-linux"}) {
				t.Error("a boot with no declared plan reported success: the machine would be on no " +
					"network while the API described it as running")
			}
			said := log.String()
			if !strings.Contains(said, "PlanOf") || !strings.Contains(said, "four") {
				t.Errorf("the refusal does not name the pack and the field: %q", said)
			}
			// The two other doors onto PlanOf, which a fix applied to PowerOn
			// alone would leave dereferencing a nil func.
			rec.ReplayAddresses(context.Background(), res)
			rec.Route(context.Background(), res, "198.18.0.7")
		})
	}
}

// The fourth pack itself wires everything, which is what makes the tests above
// about an omission rather than about the pack.
//
// It is the positive control of the whole file: if provider-four's own
// orchestrators were incomplete, every assertion above would still pass and
// none of them would be measuring the case they name.
func TestTheFourthPacksOwnOrchestratorsAreComplete(t *testing.T) {
	ctx := context.Background()
	var log bytes.Buffer
	pack, _, env := fourthPack(t)
	env.Log = slog.New(slog.NewTextHandler(&log, nil))

	segment, err := pack.CreateSegment(ctx, "front", "10.40.0.0/24", "green")
	must(t, err)
	node, err := pack.CreateNode(ctx, providerfour.NodeRequest{
		Name:        "web-1",
		Image:       "four-linux",
		HomeSegment: segment.ID,
	})
	must(t, err)
	must(t, pack.StartNode(ctx, node.ID))

	if strings.Contains(log.String(), "the firewall step is skipped") ||
		strings.Contains(log.String(), "declares no interface plan") {
		t.Errorf("the fourth pack reports an unwired orchestrator on an ordinary boot: %q", log.String())
	}
}
