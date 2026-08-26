package cli

import (
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/environment"
)

// The property that stood between #525's five escaped requests and a real
// account, held by nothing until that incident: the pack's deliberately public
// credentials outrank whatever the caller's shell exports, because
// engineEnvironment appends them after os.Environ() and exec.Cmd lets the last
// duplicate win.
//
// TestTheEngineGetsThePacksOwnEnvironmentAndTheDeclaredVariables next door
// checks the variables are *present*; presence survives the two halves being
// swapped, and swapped is exactly the state in which an operator's real
// credentials would sign the requests that leave. This test measures the
// winner, not the membership.
//
// TestThePacksOwnCredentialsOutrankTheCallersShell fails when
// engineEnvironment appends the pack's variables before os.Environ().
func TestThePacksOwnCredentialsOutrankTheCallersShell(t *testing.T) {
	for _, tc := range []struct {
		provider string
		planted  map[string]string
	}{
		{"scaleway", map[string]string{
			"SCW_ACCESS_KEY": "SCWREALREALREALREALR",
			"SCW_SECRET_KEY": "deadbeef-real-cred-from-the-callers-shell",
		}},
		{"outscale", map[string]string{
			"OSC_ACCESS_KEY": "REALREALREALREALREAL",
			"OSC_SECRET_KEY": "realrealrealrealrealrealrealrealrealreal",
		}},
		{"exoscale", map[string]string{
			"EXOSCALE_API_KEY":    "EXOrealrealrealrealreal",
			"EXOSCALE_API_SECRET": "real-secret-from-the-callers-shell",
		}},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			for name, value := range tc.planted {
				t.Setenv(name, value)
			}
			decl, err := environment.Parse("version: 1\ncloud:\n  provider: " + tc.provider +
				"\nemulator:\n  addr: 127.0.0.1:4613\niac:\n  engine: terraform\n")
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			env, err := engineEnvironment(decl)
			if err != nil {
				t.Fatalf("engineEnvironment: %v", err)
			}
			// What exec.Cmd hands the process: the last duplicate wins.
			effective := map[string]string{}
			for _, entry := range env {
				name, value, ok := strings.Cut(entry, "=")
				if ok {
					effective[name] = value
				}
			}
			for name, real := range tc.planted {
				got, present := effective[name]
				if !present {
					t.Fatalf("%s is absent: the pack no longer exports it, so the shell's own "+
						"value would reach the engine unopposed", name)
				}
				if got == real {
					t.Errorf("%s: the caller's shell wins, and a run like #525's would sign "+
						"with real credentials", name)
				}
			}
		})
	}
}
