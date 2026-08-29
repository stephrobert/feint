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
// then drives. A fourth entry point adds itself here rather than being
// discovered by a red matrix.
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
