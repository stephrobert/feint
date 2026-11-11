package emulator_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
)

// restartingPack implements BOTH halves, and records what each was handed.
type restartingPack struct {
	stubPack
	gone    [][]string
	missing [][]string
	back    machine.Reconciled
}

func (p *restartingPack) ReconcileGone(_ context.Context, gone []*resource.Resource) machine.Reconciled {
	p.gone = append(p.gone, idsOf(gone))
	return machine.Reconciled{}
}

func (p *restartingPack) ReconcileMissing(_ context.Context, records []*resource.Resource,
	held []string,
) machine.Reconciled {
	// The pack decides which of its records is missing, with its own key.
	// Here the key is "machine", which is what put() writes.
	var missing []*resource.Resource
	for _, res := range records {
		name := res.Runtime["machine"]
		if name == "" {
			continue
		}
		found := false
		for _, candidate := range held {
			if candidate == name {
				found = true
			}
		}
		if !found {
			missing = append(missing, res)
		}
	}
	p.missing = append(p.missing, idsOf(missing))
	return p.back
}

func idsOf(resources []*resource.Resource) []string {
	var out []string
	for _, res := range resources {
		out = append(out, res.ID)
	}
	return out
}

// surveyingDriver answers what the host holds, and nothing else.
type surveyingDriver struct {
	machine.Noop
	held []string
}

func (d surveyingDriver) Survey(context.Context) (machine.Leftovers, error) {
	return machine.Leftovers{Machines: d.held}, nil
}

// TestAResetStartsAgainWhatTheHostNoLongerHolds is cited by missingOnHost and
// by the three packs' ReconcileMissing.
//
// The question a reset has to ask is not what the store says — the store is
// exactly what just changed, and it says the server is running. It is whether
// anything backs it, and only the runtime can answer.
func TestAResetStartsAgainWhatTheHostNoLongerHolds(t *testing.T) {
	acme := &restartingPack{
		stubPack: stubPack{name: "acme"},
		back:     machine.Reconciled{Recreated: []string{"feint-acme-alive"}},
	}
	server, held := labServerWithRuntime(t, machine.Use(surveyingDriver{held: []string{"feint-acme-alive"}}), acme)

	// Two records: one whose machine the host holds, one whose machine it does
	// not. Only the second is missing.
	put(t, held, "acme", "instance/server", "alive")
	put(t, held, "acme", "instance/server", "vanished")
	if code, body := checkpointPost(t, server.URL+"/_feint/checkpoint/baseline"); code != http.StatusOK {
		t.Fatalf("checkpoint: %d %v", code, body)
	}

	code, body := checkpointPost(t, server.URL+"/_feint/reset/baseline")
	if code != http.StatusOK {
		t.Fatalf("reset: %d %v", code, body)
	}
	if len(acme.missing) != 1 || len(acme.missing[0]) != 1 || acme.missing[0][0] != "vanished" {
		t.Fatalf("the pack was asked to start %v; only the record the host does not back is missing",
			acme.missing)
	}
	if back, _ := body["recreated"].([]any); len(back) != 1 {
		t.Errorf("recreated %v", body["recreated"])
	}
	// And the record it DOES back was not touched, which is the direction
	// that would start a second container beside one already running.
	if len(acme.gone) != 0 {
		t.Errorf("a reset that changed nothing asked for destructions: %v", acme.gone)
	}
}

// TestAResetAsksNothingOfAHostNobodyCanSurvey is the refusal half.
//
// A driver that cannot be surveyed answers asked=false, and then nothing is
// reported missing. Claiming a machine is absent because nobody could be asked
// would have a reset start containers on a host that already holds them —
// which is the expensive direction, and the one an undeclared capability is
// supposed to prevent everywhere else in this repository.
func TestAResetAsksNothingOfAHostNobodyCanSurvey(t *testing.T) {
	acme := &restartingPack{stubPack: stubPack{name: "acme"}}
	// The zero runtime: Noop, which is not a Surveyor.
	server, held := labServer(t, acme)
	put(t, held, "acme", "instance/server", "alive")
	if code, _ := checkpointPost(t, server.URL+"/_feint/checkpoint/baseline"); code != http.StatusOK {
		t.Fatal("checkpoint refused")
	}
	if code, _ := checkpointPost(t, server.URL+"/_feint/reset/baseline"); code != http.StatusOK {
		t.Fatal("reset refused")
	}
	if len(acme.missing) != 0 {
		t.Errorf("a host nobody can survey was taken for an empty one: %v", acme.missing)
	}
}
