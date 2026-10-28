package cli_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// A tool version written in two files drifts, and the failure is silent in the
// worst direction: the workstation and the runner install different things, and
// the artefacts the runner regenerates are produced by a tool nobody on the
// project is using.
//
// `.github/workflows/drift.yml` installed uv unpinned until 2026-08-13 — the
// version of the day, every week, in the very workflow that rewrites committed
// contracts and opens a pull request with them. Scorecard's Pinned-Dependencies
// flagged it and was right. Pinning it introduced the second copy this test
// exists to keep honest.
//
// mise.toml is the source: it is what a contributor's shell reads, and the
// workflow follows it rather than the other way round.
//
// golangci-lint and ruff were pinned twice and guarded by nothing until #749:
// their mise.toml comments said "mirrors the Go workflow" and "mirrors
// .pre-commit-config.yaml" and that was the whole mechanism, in a repository
// whose own rule is that a comment is not a control. They agreed on the day it
// was found, which is exactly when a missing guard is cheapest to add and
// easiest to argue is unnecessary.
//
// Each of the four entries below reds this test when its two copies disagree,
// and when either copy can no longer be found.
func TestTheWorkflowsPinTheSameToolsAsMise(t *testing.T) {
	root := moduleRoot(t)

	// The file that owns each version, and how to read it there.
	owners := map[string]struct {
		file    string
		pattern *regexp.Regexp
	}{
		// mise.toml: `uv = "0.11.26"` with a trailing comment.
		"uv": {"mise.toml", regexp.MustCompile(`(?m)^uv\s*=\s*"([^"]+)"`)},
		// The hook owns commitizen's: it is what refuses a message as it is
		// written, and tools/release/preflight.sh already reads the version
		// from here rather than keeping a third copy.
		"commitizen": {".pre-commit-config.yaml",
			regexp.MustCompile(`commitizen-tools/commitizen\s*\n\s*rev:\s*v?([0-9][^\s]*)`)},
		// mise.toml: `golangci-lint = "2.12.2"`. Anchored, because the file also
		// holds `trufflehog` and an unanchored name would match inside it.
		"golangci-lint": {"mise.toml",
			regexp.MustCompile(`(?m)^golangci-lint\s*=\s*"([^"]+)"`)},
		// mise.toml: `ruff = "0.14.9"`.
		"ruff": {"mise.toml", regexp.MustCompile(`(?m)^ruff\s*=\s*"([^"]+)"`)},
	}
	// Where each version is repeated, and the pattern that reads it back.
	repeats := map[string][]struct {
		file    string
		pattern *regexp.Regexp
	}{
		"uv": {{
			file:    "tools/ci/uv-requirements.txt",
			pattern: regexp.MustCompile(`(?m)^uv==([^\s\\]+)`),
		}},
		"commitizen": {{
			file:    "tools/ci/commitizen-requirements.txt",
			pattern: regexp.MustCompile(`(?m)^commitizen==([^\s\\]+)`),
		}},
		// A linter whose CI version differs from the workstation's reports
		// findings a contributor cannot reproduce, or misses ones they cannot
		// see. go.yml installs it by version rather than by hash, so the version
		// is the only thing that says which linter ran.
		"golangci-lint": {{
			file:    ".github/workflows/go.yml",
			pattern: regexp.MustCompile(`golangci-lint/v2/cmd/golangci-lint@v([^\s]+)`),
		}},
		// The hook is what formats Python on the workstation; mise is what the
		// contract extraction runs under. Two formatters disagreeing rewrite each
		// other's output, and the diff lands in committed contracts.
		"ruff": {{
			file:    ".pre-commit-config.yaml",
			pattern: regexp.MustCompile(`astral-sh/ruff-pre-commit\s*\n\s*rev:\s*v?([0-9][^\s]*)`),
		}},
	}

	for tool, owner := range owners {
		source, err := os.ReadFile(filepath.Join(root, owner.file))
		if err != nil {
			t.Errorf("read %s: %v", owner.file, err)
			continue
		}
		want := owner.pattern.FindSubmatch(source)
		if want == nil {
			t.Errorf("%s no longer declares %s: this test cannot compare what it "+
				"cannot find, and would otherwise pass while measuring nothing",
				owner.file, tool)
			continue
		}
		for _, repeat := range repeats[tool] {
			raw, err := os.ReadFile(filepath.Join(root, repeat.file))
			if err != nil {
				t.Errorf("read %s: %v", repeat.file, err)
				continue
			}
			got := repeat.pattern.FindSubmatch(raw)
			if got == nil {
				t.Errorf("%s no longer pins %s: an unpinned install there takes "+
					"whatever is newest, in a workflow that rewrites committed artefacts",
					repeat.file, tool)
				continue
			}
			if string(got[1]) != string(want[1]) {
				t.Errorf("%s pins %s at %s, %s says %s: a workstation and the runner "+
					"would install different tools",
					repeat.file, tool, got[1], owner.file, want[1])
			}
		}
	}
}

// .golangci.yml's `version: "2"` is the configuration schema's version, not the
// tool's, which is why mise.toml's comment claiming it "mirrors .golangci.yml"
// was never about the same number. The link is real all the same and it is a
// major: a v2 schema is read by golangci-lint v2 and rejected by any other
// major, so a mise pin that crossed one would make every lint run fail on the
// configuration rather than on the code.
//
// This is deliberately a major-only comparison. Pinning the schema to the
// tool's patch level would red the repository on every upstream patch release,
// which is the gate nobody keeps.
//
// TestTheLinterConfigSchemaMatchesTheLinterMajor fails without the pin it reads:
// flipping either side's major in isolation reds it.
func TestTheLinterConfigSchemaMatchesTheLinterMajor(t *testing.T) {
	root := moduleRoot(t)

	read := func(file string, pattern *regexp.Regexp, what string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		found := pattern.FindSubmatch(raw)
		if found == nil {
			t.Fatalf("%s no longer declares %s: this test cannot compare what it "+
				"cannot find, and would otherwise pass while measuring nothing",
				file, what)
		}
		return string(found[1])
	}

	toolMajor := read("mise.toml",
		regexp.MustCompile(`(?m)^golangci-lint\s*=\s*"(\d+)[^"]*"`),
		"golangci-lint's version")
	schemaMajor := read(".golangci.yml",
		regexp.MustCompile(`(?m)^version:\s*"?(\d+)"?`),
		"a configuration version")

	if toolMajor != schemaMajor {
		t.Errorf(".golangci.yml declares schema version %s and mise.toml pins "+
			"golangci-lint %s.x: the linter would refuse the configuration "+
			"before it read a line of Go", schemaMajor, toolMajor)
	}
}

// moduleRoot walks up to the module root rather than hardcoding "../..": a
// package that moves would otherwise make this test read the wrong tree, or no
// tree at all, and pass either way.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's directory")
		}
		dir = parent
	}
}
