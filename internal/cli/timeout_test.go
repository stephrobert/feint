package cli

import (
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/machine"
)

// The write deadline follows the runtime, because the sentence that justified
// sixty seconds — every response is small and local — is only true without
// one. Under a runtime, a subnet create provisions a real network, fifteen of
// them queue, and a deadline below the queue makes the emulator close the
// connection on work that then succeeds: the client's retry meets its own
// subnet as a conflict (#473, links 2 and 3).
func TestTheWriteDeadlineFollowsTheRuntime(t *testing.T) {
	if got := writeTimeoutFor(machine.Use(machine.Noop{})); got != 60*time.Second {
		t.Errorf("with no runtime the deadline is %v; sixty seconds is the measured figure for small local responses", got)
	}
	withRuntime := writeTimeoutFor(machine.Use(machine.NewIncusOVN()))
	if withRuntime <= 60*time.Second {
		t.Errorf("under a machine runtime the deadline is %v; anything at or under a minute cuts a fifteen-subnet apply (#473)", withRuntime)
	}
}
