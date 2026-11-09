package conformance

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Three entry points start a conformance emulator, and they must declare the
// same project catalogue.
//
// Measured on PR #605: `tools/conformance/leg.sh` and the `conformance` mise
// task were taught `--projects` (#572) and `.github/workflows/conformance.yml`
// was not. Five matrix jobs went red at 44 s on one line:
//
//	FAIL: the declared project did not list under its own name: []
//
// That is the trap CLAUDE.md states in as many words. `mise run conformance`
// drives every suite against ONE emulator and is green by construction for an
// assertion about what that emulator holds; `conformance.yml` splits the clients
// into a matrix with an emulator per leg. A local pass could not see it, and the
// suite that reads the catalogue is exactly the kind that tells the two
// populations apart.
//
// A per-file check would have passed on the two that were right. What is
// asserted here is the property: whatever the catalogue is, all three name it.

// projectStarters are the three files that start an emulator a conformance suite
// then drives WITH FLAGS. A fourth entry point adds itself here rather than
// being discovered by a red matrix.
//
// The `image` job of conformance.yml is deliberately absent, and adding it would
// be the mistake: it runs the shipped container with no override on purpose —
// "a proof that quietly passes different flags proves a different image". That
// leg is served by the suite asking the emulator what catalogue it holds before
// asserting on it (tools/conformance/scaleway/scw-cli.sh), which is the same
// rule the network suites follow for `capabilities.isolation`: branch on a
// declaration, never on a name you expect.
var projectStarters = []string{
	"leg.sh",
	"../../mise.toml",
	"../../.github/workflows/conformance.yml",
}

var declaresProjects = regexp.MustCompile(`--projects\s+([A-Za-z0-9_.,-]+)`)

func TestEveryConformanceEmulatorDeclaresTheSameProjects(t *testing.T) {
	catalogues := map[string]string{}
	for _, file := range projectStarters {
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		// Comment lines are skipped, and that is not a nicety: the comment this
		// flag carries in leg.sh begins with `--projects`, so a whole-file match
		// read the prose and reported the catalogue as the word after it. Found
		// by running this test, which is the only reason it is written down.
		var match []string
		for _, line := range strings.Split(string(source), "\n") {
			if trimmed := strings.TrimSpace(line); trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			if m := declaresProjects.FindStringSubmatch(line); m != nil {
				match = m
				break
			}
		}
		if match == nil {
			t.Errorf("%s starts a conformance emulator and names no --projects: the suites that "+
				"read the catalogue will report it empty, which is PR #605's five red jobs", file)
			continue
		}
		catalogues[file] = match[1]
	}

	// They must agree. A catalogue that differs between legs is worse than one
	// that is missing: the suite passes on the leg that matches and fails on the
	// others, which reads as a flake rather than as a declaration nobody kept.
	var first, firstFile string
	for _, file := range projectStarters {
		got, ok := catalogues[file]
		if !ok {
			continue
		}
		if first == "" {
			first, firstFile = got, file
			continue
		}
		if got != first {
			t.Errorf("%s declares %q and %s declares %q: one emulator per leg, two catalogues",
				file, got, firstFile, first)
		}
	}

	// The reader proves it can find before it judges. A regexp that matched
	// nothing would pass this file however wrong it became.
	if len(catalogues) == 0 {
		t.Fatal("no --projects declaration was found anywhere, so this test measured nothing")
	}

	// And the catalogue must hold the name the suites actually ask for, which is
	// the one that is NOT the pack's default: asserting on `default` alone would
	// pass against an emulator that declares nothing at all.
	if !strings.Contains(first, "platform-prod") {
		t.Errorf("the declared catalogue %q does not carry the non-default name the suites "+
			"resolve (tools/conformance/scaleway/scw-cli.sh, terraform/main.tf)", first)
	}
}

// Both spellings, because Go's flag package takes either and a reader that
// only knows one would report a starter as unarmed the day somebody writes the
// other. Found by asking what would make this detector wrong rather than by
// seeing it fail, which is the cheaper of the two ways.
var armsObjectStore = regexp.MustCompile(`--objectstore[=\s]\S+`)

// TestEveryConformanceEmulatorArmsTheSameObjectStore is the same property as
// the test above, for the flag that repeated its defect.
//
// Measured on this branch: `mise.toml` was taught `--objectstore` and the other
// two starters were not. That is PR #605 to the letter, including the reason a
// local pass could not see it — `mise run conformance` drives every suite
// against ONE emulator, which was the one that had the flag. Four CI jobs went
// red: probe and image on
//
//	FAIL exoscale/v2.list-sos-buckets-usage: answered 503
//
// and fields and exo-cli on the client suite that calls the same operation.
//
// The generalisation worth keeping is that a per-flag test is the wrong shape:
// the test above was written for `--projects` and could not notice a second
// flag drifting. So this one asserts the property for the flag that bit, and
// the next one to be added should extend the TABLE below rather than copy the
// function a third time.
//
// The `image` job stays deliberately absent for the reason projectStarters
// gives: it runs the shipped container with no override, so a flag cannot reach
// it. That leg is served by the probe branching on the emulator's own
// declaration rather than on an expectation — the rule this file already states
// as "branch on a declaration, never on a name you expect".
func TestEveryConformanceEmulatorArmsTheSameObjectStore(t *testing.T) {
	for _, flag := range []struct {
		name    string
		pattern *regexp.Regexp
		why     string
	}{
		{name: "--objectstore", pattern: armsObjectStore,
			why: "exoscale/v2.list-sos-buckets-usage answers 503 without it while its schema " +
				"declares a 200 body, so the probe reports a route answering the wrong shape"},
	} {
		found := 0
		for _, file := range projectStarters {
			source, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("read %s: %v", file, err)
			}
			// Comments skipped, for the reason the test above records: the
			// prose explaining a flag contains the flag.
			var declared bool
			for _, line := range strings.Split(string(source), "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" || strings.HasPrefix(trimmed, "#") {
					continue
				}
				if flag.pattern.MatchString(line) {
					declared = true
					break
				}
			}
			if !declared {
				t.Errorf("%s starts a conformance emulator and does not arm %s: %s",
					file, flag.name, flag.why)
				continue
			}
			found++
		}
		// The reader proves it can find before it judges, exactly as above: a
		// pattern matching nothing would pass this file however wrong it got.
		if found == 0 {
			t.Fatalf("%s was found in none of the starters, so this test measured nothing", flag.name)
		}
	}
}
