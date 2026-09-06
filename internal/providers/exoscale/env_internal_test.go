package exoscale

import (
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// The note `feint env exoscale` prints names the floor the refusal enforces,
// read from the same constant (#701).
//
// The note outlived #644 by a day: the refusal had become a version floor and
// the note still told a Terraform user not to point the provider here, on
// stderr, as the first thing that user reads. A note and a guard that each
// carry their own copy of a version are two things that drift apart, so the
// note reads terraformProviderFloor and this holds that it does. Nothing is
// said about the wording beyond the two facts a user acts on: the version to
// pin, and that an older provider is refused rather than half served.
//
// The refusal itself is held next door by TestAProviderBelowTheFloorIsRefused.
func TestTheEnvNoteNamesTheFloor(t *testing.T) {
	note := New(emulator.DefaultEnv()).Env("http://127.0.0.1:4599").Note
	for _, want := range []string{terraformProviderFloor, "pin", "refuses"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note never says %q: %q", want, note)
		}
	}
}
