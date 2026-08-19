package outscale_test

import (
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/outscale"
)

// The doorway measurements behind every assertion here are in #286, each cell
// produced by running the real client against this emulator on 2026-08-19:
// provider 1.8.0 needs the /api/v1 path inside OSC_ENDPOINT_API and dies on a
// 404 without it; provider 1.1.3 and oapi-cli append the path themselves and
// die on `invalid port ":4599%2Fapi%2Fv1"` with it.

// The default environment serves the current Terraform provider line, because
// that is what docs/adoption.md tells a stranger to run first. Printing the
// bare host here is not a style choice: it is the measured wall of #286.
func TestEnvServesTheCurrentTerraformProviderLine(t *testing.T) {
	pack := outscale.New(emulator.DefaultEnv())
	env := pack.Env("http://127.0.0.1:4599")
	got := env.Vars["OSC_ENDPOINT_API"]
	if got != "http://127.0.0.1:4599/api/v1" {
		t.Fatalf("the default OSC_ENDPOINT_API is %q; provider >= 1.7 needs the /api/v1 path in the value", got)
	}
	if !strings.Contains(env.Note, "oapi-cli") || !strings.Contains(env.Note, "--client") {
		t.Fatalf("the note does not teach the other client families their flag: %q", env.Note)
	}
}

// The families that append /api/v1 themselves get the bare host, and a family
// nobody measured gets a refusal rather than a guess.
func TestEnvForServesEachMeasuredClientFamily(t *testing.T) {
	pack := outscale.New(emulator.DefaultEnv())
	for _, client := range []string{"oapi-cli", "terraform-1.1"} {
		env, ok := pack.EnvFor("http://127.0.0.1:4599", client)
		if !ok {
			t.Fatalf("EnvFor refused the measured family %q", client)
		}
		if got := env.Vars["OSC_ENDPOINT_API"]; got != "http://127.0.0.1:4599" {
			t.Errorf("EnvFor(%q) printed %q; this family appends /api/v1 itself and wants the bare host", client, got)
		}
	}
	if _, ok := pack.EnvFor("http://127.0.0.1:4599", "osc-cli"); ok {
		t.Fatal("an unmeasured client family was served a guessed environment")
	}
	for _, known := range pack.EnvClients() {
		if _, ok := pack.EnvFor("http://127.0.0.1:4599", known); !ok {
			t.Errorf("EnvClients names %q but EnvFor refuses it", known)
		}
	}
}

// The escape that reaches the real cloud: with OSC_PROFILE set, provider 1.1.3
// reads ~/.osc/config.json and never reads OSC_ENDPOINT_API — reproduced in
// #286 by watching the plan leave for api.<region>.outscale.com while the
// emulator received nothing. The warning must exist before the apply does.
func TestEnvHazardsNameTheProfileEscape(t *testing.T) {
	pack := outscale.New(emulator.DefaultEnv())

	clean := func(string) (string, bool) { return "", false }
	if warnings := pack.EnvHazards(clean); len(warnings) != 0 {
		t.Fatalf("a clean shell was warned about: %v", warnings)
	}

	profile := func(name string) (string, bool) {
		if name == "OSC_PROFILE" {
			return "default", true
		}
		return "", false
	}
	warnings := pack.EnvHazards(profile)
	if len(warnings) == 0 {
		t.Fatal("OSC_PROFILE is set and nothing warned; that shell's terraform run reaches the real cloud")
	}
	if !strings.Contains(warnings[0], "OSC_PROFILE") || !strings.Contains(warnings[0], "OSC_ENDPOINT_API") {
		t.Fatalf("the warning names neither the trigger nor what it disables: %q", warnings[0])
	}
}

// The legacy credential names do not redirect anything by themselves — four
// measured combinations against 1.1.3 all reached the emulator while
// OSC_ENDPOINT_API was set — but they are real-cloud credentials one lost
// export away from being used, and the warning says exactly that.
func TestEnvHazardsNameLegacyCredentials(t *testing.T) {
	pack := outscale.New(emulator.DefaultEnv())
	legacy := func(name string) (string, bool) {
		if name == "OUTSCALE_ACCESSKEYID" || name == "OUTSCALE_SECRETKEYID" {
			return "x", true
		}
		return "", false
	}
	warnings := pack.EnvHazards(legacy)
	if len(warnings) != 1 {
		t.Fatalf("expected one warning for the legacy credential pair, got %v", warnings)
	}
	if !strings.Contains(warnings[0], "OUTSCALE_ACCESSKEYID") {
		t.Fatalf("the warning does not name the variables it is about: %q", warnings[0])
	}
}
