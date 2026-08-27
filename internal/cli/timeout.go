package cli

import (
	"time"

	"github.com/stephrobert/feint/internal/core/machine"
)

// writeTimeoutFor is the serve deadline on writing one response, and it
// depends on whether a machine runtime is configured.
//
// The timeout exists for the client that opens a response and stops reading
// it: such a client holds a handler goroutine, and before the store learned to
// encode outside its lock, held the whole emulator with it. The lock is fixed;
// the timeout is the second line of defence.
//
// Sixty seconds was chosen when "every response this emulator produces is
// small and local" — true with no machine runtime, and false under one, where
// a create provisions real networks and machines. #473 measured what the gap
// does: fifteen OVN subnets queued past the deadline, the emulator closed the
// connection on creates that then *succeeded*, and each client retry met the
// subnet its own cut call had created as a 409 conflict — the emulator telling
// the client less than it knew, which is the lie this project exists to avoid.
// So under a runtime the ceiling moves above the slowest legitimate handler
// (an image pull on a cold host is minutes) while stalled-reader protection
// stays; without one, the original figure keeps its measured justification.
//
// TestTheWriteDeadlineFollowsTheRuntime fails if either mode loses its value.
func writeTimeoutFor(rt machine.Runtime) time.Duration {
	if !rt.Runs() {
		return 60 * time.Second
	}
	return 10 * time.Minute
}
