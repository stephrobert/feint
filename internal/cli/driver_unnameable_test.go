package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// bypassProbe is one package under testdata/bypass and the verdict the
// compiler owes it.
type bypassProbe struct {
	// dir is the directory name under testdata/bypass.
	dir string
	// wants is the fragment the build error must carry. Empty means the probe
	// must build: that is the positive control.
	wants string
	// why says which sentence this probe is, so a failure names the gesture
	// somebody just made compilable again rather than a directory.
	why string
}

// bypassProbes is the closed list, and it is asserted against the directory
// listing in both directions below.
var bypassProbes = []bypassProbe{
	{"admitted", "", "the positive control: what a pack may legitimately name"},
	{"driver", "undefined: machine.Driver",
		"the runtime itself — #514's acceptance criterion (a), verbatim"},
	{"router", "undefined: machine.Router",
		"the address half, reached past Reconciler.Route and the emulated-block guard"},
	{"firewaller", "undefined: machine.Firewaller",
		"the rule-set half, reached past GroupSync — the layer #475 was born in"},
	{"peerer", "undefined: machine.Peerer",
		"the peering half, the second writer the audit measured severing a live peering"},
	{"isolator", "undefined: machine.Isolator",
		"the isolation half, the other side of the same fork"},
	{"balancer", "undefined: machine.Balancer",
		"the balancing half, reached by a bare assertion until Binding gained the verbs"},
	{"envdriver", "emulator.Env{}.Machines undefined",
		"the field that put a driver in every pack's hand before #511"},
	{"bindingdriver", "b.Driver undefined",
		"`p.binding().Driver.EnsureNetwork(…)`, the sentence surface.go cites by name"},
}

// A provider pack cannot name the runtime, and the compiler is what says so
// (#514).
//
// # What this measures that no other test can
//
// #511 unexported emulator.Env's driver field and machine.Binding's, so a pack
// could no longer *obtain* a driver. It could still *name* the type. Measured
// on 154c204, in this repository, before the change this test arrived with:
//
//	package scaleway
//	import "github.com/stephrobert/feint/internal/core/machine"
//	var _ machine.Driver
//
// dropped into internal/providers/scaleway/ compiled, and
// `go build ./internal/providers/scaleway/` exited 0. So the boundary was held
// by TestNoPackReachesPastTheDeclaredDriverSurface alone — a convention plus an
// AST scan, which is what #514 §2.1 says is not enough, because a scan is a
// list somebody can widen and a build error is not.
//
// # Why it is a subprocess and not an assertion
//
// A test cannot assert that an expression does not compile from inside a
// package that compiles. The sentence has to live in a package of its own,
// outside every ./... pattern, and be handed to the toolchain. That is what
// testdata/bypass is: nine packages, eight of which must fail to build, one of
// which must succeed.
//
// # Why the positive control is not optional
//
// A probe that fails to build for the wrong reason — a mistyped import, a
// module boundary the internal rule refuses, no toolchain on PATH — reads
// exactly like the door being shut, and this repository has paid seven times
// in one day for instruments that reported success because they looked
// nowhere. So testdata/bypass/admitted names only what machine.PackSurface
// admits, imports the same two packages, and must build. It runs first, and
// its failure is fatal: nothing below is worth reading after it.
//
// # What this does NOT hold, written down rather than implied
//
// machine.Noop, machine.Incus and machine.Recorder stay exported, because the
// emulator's default, the only runtime and the shared contract recorder are
// all needed by name outside this package. So `machine.Noop{}.Remove(ctx, n)`
// in a pack still compiles, and is caught by
// TestNoPackReachesPastTheDeclaredDriverSurface instead — mustStayOutside
// names all three. The two tests are not alternatives: the compiler holds the
// vocabulary, the scan holds what is left.
func TestThePacksCannotNameTheDriver(t *testing.T) {
	root := repoRoot(t)
	base := filepath.Join(root, "internal", "cli", "testdata", "bypass")

	// The population, both ways. A probe directory nobody registered would be
	// built by nothing; a registered probe whose directory is gone would be a
	// verdict about an empty package, which every compiler grants.
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("read %s: the probes are the whole measurement, and a listing that fails "+
			"leaves this test asserting nothing: %v", base, err)
	}
	onDisk := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			onDisk[e.Name()] = true
		}
	}
	registered := map[string]bool{}
	for _, p := range bypassProbes {
		registered[p.dir] = true
		if !onDisk[p.dir] {
			t.Fatalf("testdata/bypass/%s is registered and does not exist: a probe with no source "+
				"is a verdict about an empty package, which compiles", p.dir)
		}
	}
	var unregistered []string
	for dir := range onDisk {
		if !registered[dir] {
			unregistered = append(unregistered, dir)
		}
	}
	sort.Strings(unregistered)
	if len(unregistered) > 0 {
		t.Fatalf("testdata/bypass holds %v, which this test builds nothing for: a probe nobody "+
			"runs is a sentence nobody checks", unregistered)
	}

	if _, err := exec.LookPath("go"); err != nil {
		// Never a skip. A skip here reports "the door is shut" for a run that
		// asked nothing, which is the failure mode this whole file exists to
		// refuse.
		t.Fatalf("no go toolchain on PATH, so nothing was compiled and nothing was proved: %v", err)
	}

	// The control first, and fatally: every verdict below is about the
	// toolchain answering the question that was asked.
	if out, err := buildProbe(t, root, "admitted"); err != nil {
		t.Fatalf("testdata/bypass/admitted must compile and did not, so every refusal below is "+
			"unreadable — a probe can fail for a reason that has nothing to do with the driver:\n%s",
			out)
	}

	for _, probe := range bypassProbes {
		if probe.wants == "" {
			continue
		}
		out, err := buildProbe(t, root, probe.dir)
		if err == nil {
			t.Errorf("testdata/bypass/%s compiles: %s. A pack can name the runtime again, and the "+
				"boundary is back to a convention an AST scan enforces — which is the state "+
				"#514 §2.1 measured and refused", probe.dir, probe.why)
			continue
		}
		if !strings.Contains(out, probe.wants) {
			t.Errorf("testdata/bypass/%s failed to build, but not on %q — so this probe is "+
				"measuring something else and its refusal proves nothing about %s:\n%s",
				probe.dir, probe.wants, probe.why, out)
		}
	}
}

// buildProbe compiles one probe package and returns everything the toolchain
// said. A non-nil error means the build failed, which is what most of these
// probes are for.
func buildProbe(t *testing.T, root, dir string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build",
		"./internal/cli/testdata/bypass/"+dir+"/")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		// A timeout is not a refusal. Reported as its own failure so a slow or
		// wedged toolchain never reads as the compiler rejecting the sentence.
		t.Fatalf("building testdata/bypass/%s did not finish: %v\n%s", dir, ctx.Err(), out)
	}
	return string(out), err
}
