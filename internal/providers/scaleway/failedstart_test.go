package scaleway_test

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/store"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// failingStartRuntime is the ordinary fake with one refusal: a launch that
// cannot happen. An image the host does not hold is the everyday shape of it.
type failingStartRuntime struct{ *fakeRuntime }

func (r *failingStartRuntime) Start(context.Context, machine.Spec) (machine.Machine, error) {
	return machine.Machine{}, errors.New("incus launch: image not found")
}

// TestAFailedPoweronWalksNoChain drives the whole path, through HTTP, on an
// eventual store: a poweron whose Start fails must never narrate `starting`.
//
// The defect this holds (#738) is the one CLAUDE.md names for this layer —
// "l'état publié est celui que l'effet a produit, pas celui que l'intention
// visait" — one step earlier in the chain than the `running` that rule is
// usually quoted about. Measured before the fix, four reads after the action:
//
//	[starting stopped stopped stopped]
//
// so a client watching the action saw a boot that never happened, and the task
// it answered said `success`.
//
// It is driven through the API rather than against transitionTo because the
// unit-level guard was already believed to hold: TestAFailedActionWalksNoChain
// listed four failed states, none of which this pack produces, while its
// accepting half required a chain for the one it does (`stopped`, the
// FailedState of machines.go). A test whose population excludes the only value
// that occurs proves nothing, and only the whole path shows it.
func TestAFailedPoweronWalksNoChain(t *testing.T) {
	rt := &failingStartRuntime{fakeRuntime: newFakeRuntime()}
	// Nothing is held back: the refusal is the subject, not a race.
	close(rt.release)

	var seq atomic.Int64
	st := store.New()
	st.Eventual(true)
	env := &emulator.Env{
		Store: st,
		Now:   func() time.Time { return time.Unix(1700000000, 0).UTC() },
		NewID: func() string {
			return fmt.Sprintf("00000000-0000-4000-8000-%012d", seq.Add(1))
		},
	}
	env.UseMachines(machine.Use(rt))
	srv, err := emulator.NewServer(env, scaleway.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	id, _ := serverWith(t, ts, `{"name":"failer","commercial_type":"DEV1-S"}`)
	if code, _ := do(t, ts, "POST", zoneURL+"/servers/"+id+"/action", `{"action":"poweron"}`); code != 202 {
		t.Fatalf("poweron answered %d", code)
	}

	// Four reads, because the chain is consumed one state per observation: a
	// single read would pass over a chain of two.
	for i := 1; i <= 4; i++ {
		_, body := do(t, ts, "GET", zoneURL+"/servers/"+id, "")
		server, _ := body["server"].(map[string]any)
		state, _ := server["state"].(string)
		if state != "stopped" {
			t.Fatalf("read %d answered %q for a start that failed; the machine never left stopped, "+
				"and narrating a path towards a boot that did not happen is the plausible-wrong "+
				"answer this project exists to avoid (#738)", i, state)
		}
	}
}
