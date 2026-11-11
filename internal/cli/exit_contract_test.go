package cli

import (
	"testing"
)

// TestAGuaranteeThatWasNotDeliveredFailsTheRun is cited by runCommand's exit
// code, and it closes a contract error an external audit found in merged code.
//
// The first version failed the run only on "did not converge". So this was a
// possible CI run:
//
//	terraform test   success
//	feint reset      failed
//	feint run        exit 0
//
// and the same held for a lab this command owned and could not stop: the
// receipt said `cleanup: failed` and the exit code stayed at the child's.
//
// The table is written as the full cross product on purpose. Two of the
// verdict words — "unverified" and "not attempted" — are the ones the first
// version let through, and a test that only covered the word it already
// handled would have agreed with the defect.
func TestAGuaranteeThatWasNotDeliveredFailsTheRun(t *testing.T) {
	for _, probe := range []struct {
		name      string
		reset     string
		cleanup   string
		delivered bool
	}{
		// No reset was asked for, so there is nothing to have failed.
		{"nothing asked, clean teardown", "", "clean", true},
		{"nothing asked, lab not ours", "", "not owned", true},
		{"nothing asked, kept on purpose", "", "kept", true},

		// A reset that was asked for and delivered.
		{"reset converged", "converged", "clean", true},

		// Every way a reset can fail to deliver. The first version of this
		// guard treated the last three as success.
		{"reset did not converge", "did not converge", "clean", false},
		{"the emulator refused the reset", "failed", "clean", false},
		{"the reset could not be read back", "unverified", "clean", false},
		{"there was no checkpoint to return to", "not attempted", "clean", false},

		// A teardown this command owed and did not perform. The receipt said
		// so and the exit code did not.
		{"the lab could not be stopped", "", "failed", false},
		{"converged, and the lab leaked anyway", "converged", "failed", false},

		// A word nobody has written yet must not default to success: that is
		// the whole reason the function lists the failures rather than the
		// successes.
		{"an unknown verdict word", "something-new", "clean", false},
		{"an unknown cleanup word", "", "something-new", false},
	} {
		t.Run(probe.name, func(t *testing.T) {
			if got := deliveredWhatWasAsked(probe.reset, probe.cleanup); got != probe.delivered {
				t.Errorf("deliveredWhatWasAsked(%q, %q) = %v, want %v",
					probe.reset, probe.cleanup, got, probe.delivered)
			}
		})
	}
}
