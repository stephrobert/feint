package conformance

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// `mise run conformance:leg -- <leg>` exists to reproduce a CI leg locally, and
// nothing held the two lists together (#459).
//
// The script's own header says the names are the matrix entries of
// .github/workflows/conformance.yml, and it refuses anything else by design —
// "a leg that silently ran something else would measure the wrong thing and
// report success". The consequence nobody guarded: when the matrix renames a
// leg, the refusal turns on the leg that now exists. #460 renamed `oapi-cli` to
// `octl` in the matrix and somebody remembered to follow here; nothing would
// have said so if they had not.
//
// One-directional on purpose, like TestEveryRequiredConformanceCheckIsAMatrixLeg
// beside it: leg.sh carries `runtime`, which is not a conformance-matrix leg at
// all — it reproduces the network steps of runtime-proof.yml, which need real
// machines. Requiring the reverse would fail on that, and it is deliberate.
func TestEveryMatrixLegCanBeReproducedLocally(t *testing.T) {
	root := repoRoot(t)

	legs := matrixLegs(t, filepath.Join(root, ".github", "workflows", "conformance.yml"))
	if len(legs) == 0 {
		t.Fatal("no matrix leg was read from conformance.yml, so this test compared nothing")
	}
	// The witness the rule about absence demands: the leg #460 renamed must be
	// found, or the reader is matching nothing and this test passes by looking
	// nowhere.
	if !legs["octl"] {
		t.Fatalf("the matrix does not name `octl`; the reader found %v", keys(legs))
	}

	accepted := legsAcceptedByTheScript(t, filepath.Join(root, "tools", "conformance", "leg.sh"))
	if len(accepted) == 0 {
		t.Fatal("no leg name was read from leg.sh, so this test compared nothing")
	}

	for leg := range legs {
		if !accepted[leg] {
			t.Errorf("the conformance matrix has the leg %q and `mise run conformance:leg` refuses "+
				"that name: the one command written to reproduce a CI leg locally cannot "+
				"reproduce this one. leg.sh accepts %v", leg, keys(accepted))
		}
	}
}

// The `runtime` leg refuses to run with no machine runtime, and it refuses
// BEFORE it starts anything (#459).
//
// Its four suites each begin by asking the emulator whether a runtime is
// configured and skipping themselves when none is. That skip is right inside
// `mise run conformance`, which must stay runnable in CI with no runtime at
// all; it is wrong for a leg asked for by name, because the leg would print
// four skips and exit 0 — a run that measured nothing reporting success, which
// is the verdict this repository refuses everywhere else.
//
// The assertion is on both halves: the exit code is 2, and the emulator was
// never started. Without the second, a refusal that fired after
// `./feint start` would leave a process behind on every invocation.
func TestTheRuntimeLegRefusesToMeasureNothing(t *testing.T) {
	requireTool(t, "bash")
	root := repoRoot(t)

	cmd := exec.Command("bash", "tools/conformance/leg.sh", "runtime") //nolint:gosec // a fixed path
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "FEINT_VM=off")
	out, err := cmd.CombinedOutput()

	code := 0
	var exitErr *exec.ExitError
	if err != nil {
		if !errors.As(err, &exitErr) {
			t.Fatalf("run leg.sh runtime: %v\n%s", err, out)
		}
		code = exitErr.ExitCode()
	}
	if code != 2 {
		t.Errorf("`conformance:leg -- runtime` with no runtime exited %d; a leg whose four suites "+
			"would each skip themselves must refuse rather than report success on nothing:\n%s",
			code, out)
	}
	if !strings.Contains(string(out), "no machine runtime configured") {
		t.Errorf("the refusal does not say what is missing:\n%s", out)
	}
	// The refusal has to come before anything is started, or every invocation
	// leaves an emulator behind. `feint start` prints its address on success and
	// the doorstep prints its verdict; neither may appear.
	for _, forbidden := range []string{"feint listening on", "leftovers:"} {
		if strings.Contains(string(out), forbidden) {
			t.Errorf("the refusal came after %q, so the leg had already started work before "+
				"refusing:\n%s", forbidden, out)
		}
	}
}

// legsAcceptedByTheScript reads the `case` label that validates the leg name.
//
// A line rather than a shell parse, for the reason matrixLegs gives: this module
// carries no dependencies, and a change to the line's shape breaks loudly here
// instead of quietly at merge time.
func legsAcceptedByTheScript(t *testing.T, path string) map[string]bool {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading leg.sh: %v", err)
	}
	// The validating case is the one whose body is empty — `... ) ;;` — which is
	// how the script says "this name is allowed" before doing anything with it.
	line := regexp.MustCompile(`(?m)^\s*([a-z0-9|-]+)\)\s*;;\s*$`)
	m := line.FindSubmatch(body)
	if m == nil {
		t.Fatal("no leg-validating `case` label in leg.sh: the reader this test depends on found " +
			"nothing, which would make it pass by looking nowhere")
	}
	out := map[string]bool{}
	for _, name := range strings.Split(string(m[1]), "|") {
		if name = strings.TrimSpace(name); name != "" {
			out[name] = true
		}
	}
	return out
}
