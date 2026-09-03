package scaleway

import (
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/resource"
)

// A chain is pushed for an action that arrived somewhere the chain knows how to
// reach, and for nothing else.
//
// The case that matters is the failed one. startMachine leaves a start that
// failed in its failed state, and walking a chain from there towards `running`
// would be the plausible-wrong answer this project exists to avoid: the API
// would narrate a boot that never happened, one read at a time, and settle on a
// state no machine reached. The published state is the one the effect produced,
// which is the rule CLAUDE.md states for this whole layer.
func TestAFailedActionWalksNoChain(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	res := func(state string) *resource.Resource {
		return resource.New("id", kindServer, resource.Tenant{Provider: Name}, state, now)
	}

	for _, settled := range []string{"running", "stopped", "stopped in place"} {
		r := res(settled)
		transitionTo(r, settled, "stopping", "starting")
		if len(r.Pending) == 0 {
			t.Errorf("a %s action pushed no chain, so a client waiting on it observes nothing", settled)
		}
		if r.State != settled {
			t.Errorf("a %s action left the resource at %q", settled, r.State)
		}
	}

	// The failed states the machine layer produces. Whatever they are called,
	// they are not states this chain knows how to arrive at.
	for _, failed := range []string{"failed", "starting", "unknown", ""} {
		r := res(failed)
		transitionTo(r, failed, "stopping", "starting")
		if len(r.Pending) != 0 {
			t.Errorf("an action that ended in %q pushed %v: the emulator would narrate a path to a "+
				"state the machine never reached", failed, r.Pending)
		}
		if r.State != failed {
			t.Errorf("an action that ended in %q had its state rewritten to %q", failed, r.State)
		}
	}

	// And an empty chain is a no-op rather than a resource stuck with an empty
	// pending slice, which the store would then treat as settled anyway.
	r := res("running")
	transitionTo(r, "running")
	if len(r.Pending) != 0 {
		t.Errorf("an action with no chain pushed %v", r.Pending)
	}
}
