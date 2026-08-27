// Package evidence holds the tests of the shell `mise run evidence:update`
// leans on. The task itself takes twenty minutes and needs Incus, real
// clients and a station nobody else is using; deciding which runtime its
// second leg answers under takes none of that, and it is the part that lied.
package evidence

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The evidence record's second leg says which runtime it ran under, and cannot
// be handed one nobody asked for (#574).
//
// What the wrong output looked like matters more than the fix: nothing was
// printed at all. `FEINT_VM=incus-ovn mise run evidence:update` ran the bridge,
// answered green, and the operator read the verdict as OVN's. Two false
// attributions came out of that in one session — an Exoscale reachability
// defect was blamed first on the population of the run, then on the branch it
// ran from — and each disproof cost a 1300-second pass.
//
// Four outcomes, and the third and fourth are the ones a two-way resolver
// would have collapsed: honour the caller, honour the task's own knob, refuse
// a disagreement rather than arbitrate it, refuse a mode that would silently
// narrow the record.
func TestTheRuntimeLegResolvesAndAnnouncesItsMode(t *testing.T) {
	cases := []struct {
		name    string
		env     []string
		mode    string
		refused bool
		says    string
	}{
		{
			name: "an exported FEINT_VM wins, which is the whole defect",
			env:  []string{"FEINT_VM=incus-ovn"},
			mode: "incus-ovn",
			says: "FEINT_VM, exported by the caller",
		},
		{
			name: "the task's own knob still steers the leg without touching leg 1",
			env:  []string{"FEINT_EVIDENCE_VM=incus-vm"},
			mode: "incus-vm",
			says: "FEINT_EVIDENCE_VM",
		},
		{
			name: "nothing exported: the default, said out loud rather than assumed",
			env:  nil,
			mode: "incus",
			says: "this task's default",
		},
		{
			name: "both set to the same runtime is not a disagreement",
			env:  []string{"FEINT_VM=incus-ovn", "FEINT_EVIDENCE_VM=incus-ovn"},
			mode: "incus-ovn",
			says: "FEINT_VM, exported by the caller",
		},
		{
			name:    "two different runtimes are refused by name, never arbitrated",
			env:     []string{"FEINT_VM=incus-ovn", "FEINT_EVIDENCE_VM=incus"},
			refused: true,
			says:    "name two different runtimes",
		},
		{
			name:    "machines off is refused: it is leg 1 run twice, and it would narrow the record",
			env:     []string{"FEINT_VM=off"},
			refused: true,
			says:    "leg 1 run a second time",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runMode(t, tc.env)
			if tc.refused {
				if code == 0 {
					t.Fatalf("the resolver accepted what it must refuse (mode %q)", strings.TrimSpace(stdout))
				}
				if strings.TrimSpace(stdout) != "" {
					t.Errorf("a refusal still printed a mode on stdout (%q), which the task would run",
						strings.TrimSpace(stdout))
				}
			} else {
				if code != 0 {
					t.Fatalf("the resolver refused a mode it must honour: %s", stderr)
				}
				if got := strings.TrimSpace(stdout); got != tc.mode {
					t.Fatalf("the leg would run under %q, want %q", got, tc.mode)
				}
			}
			// The announcement is the half that was missing entirely, so it is
			// asserted for every outcome: a resolver that picks right and says
			// nothing leaves the reader exactly where #574 found them.
			if !strings.Contains(stderr, tc.says) {
				t.Errorf("the resolver never said %q; it said:\n%s", tc.says, stderr)
			}
			if !tc.refused && !strings.Contains(stderr, "--vm "+tc.mode) {
				t.Errorf("the resolver never named the mode it runs; it said:\n%s", stderr)
			}
		})
	}
}

// The resolver is only worth its file if the task actually asks it. A
// behaviour test on a script nothing calls is the instrument this repository
// keeps finding: green, honest about itself, and measuring nothing.
//
// Two halves, because the wiring can rot in two directions: the call must be
// there, and the literal it replaced must not come back.
func TestTheEvidenceTaskAsksForItsModeRatherThanPinningIt(t *testing.T) {
	body := evidenceTask(t)
	if !strings.Contains(body, "tools/evidence/mode.sh") {
		t.Error("the evidence task no longer asks tools/evidence/mode.sh which runtime its second leg " +
			"runs under: whatever it picks instead, it picks silently (#574)")
	}
	if strings.Contains(body, "FEINT_EVIDENCE_VM") {
		t.Error("the evidence task arbitrates FEINT_EVIDENCE_VM itself again instead of delegating " +
			"the whole question to the resolver: that is the line that ignored an exported " +
			"FEINT_VM and manufactured two false attributions (#574)")
	}
	// Leg 1 is the one mode that is genuinely fixed, and it says so where a
	// reader meets it rather than only in a comment above the task.
	if !strings.Contains(body, "--vm off always") {
		t.Error("the evidence task no longer says that its first leg is fixed at --vm off: an " +
			"unexplained override is the same defect one leg to the left")
	}
}

// evidenceTask is what `mise run evidence:update` executes, read from
// mise.toml: the run block alone, never the prose above it. The comment there
// quotes the line this change removed, and a reader that swallowed it would
// report the defect present forever.
//
// The reader proves it can find before it judges: a slice that came back empty
// would let both assertions above pass on a task that no longer exists.
func evidenceTask(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "mise.toml")) //nolint:gosec // a fixed path in this repository
	if err != nil {
		t.Fatalf("read mise.toml: %v", err)
	}
	_, after, found := strings.Cut(string(raw), `[tasks."evidence:update"]`)
	if !found {
		t.Fatal("mise.toml declares no evidence:update task: this test would pass on its absence")
	}
	_, script, found := strings.Cut(after, "run = \"\"\"")
	if !found {
		t.Fatal("the evidence:update task carries no run block: this test would pass on its absence")
	}
	body, _, found := strings.Cut(script, "\"\"\"")
	if !found {
		t.Fatal("the evidence:update run block is never closed: the reader is the suspect")
	}
	if !strings.Contains(body, "mise run conformance") {
		t.Fatalf("the evidence:update task read from mise.toml drives no conformance run, so the "+
			"reader is the suspect, not the task:\n%s", body)
	}
	return body
}

// runMode executes the resolver with exactly the environment a case names.
//
// The parent environment is deliberately not inherited for the two variables
// under test: a station that already exports FEINT_VM would otherwise decide
// the verdict, which is the shape of the defect itself.
func runMode(t *testing.T, env []string) (code int, stdout, stderr string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Fatalf("bash is not on this host, and the resolver is shell because the task is shell: %v", err)
	}
	script, err := filepath.Abs("mode.sh")
	if err != nil {
		t.Fatalf("locate mode.sh: %v", err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("mode.sh is not where this test looks (%s): %v", script, err)
	}

	cmd := exec.Command("bash", script) //nolint:gosec // a fixed script in this repository
	cmd.Env = append(cleanEnvironment(), env...)
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("run the resolver: %v\n%s", err, errBuf.String())
		}
		code = exit.ExitCode()
	}
	return code, out.String(), errBuf.String()
}

// cleanEnvironment is the parent environment without the two variables the
// resolver reads, so a case says the whole truth about its own inputs.
func cleanEnvironment() []string {
	out := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "FEINT_VM=") || strings.HasPrefix(entry, "FEINT_EVIDENCE_VM=") {
			continue
		}
		out = append(out, entry)
	}
	return out
}
