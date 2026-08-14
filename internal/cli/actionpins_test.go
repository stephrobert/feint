package cli_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// One action, one pin, across every workflow.
//
// A stale pin does not look stale. `step-security/harden-runner@6c439dc8… #
// v2.12.2` sat in one job of one workflow while the other twenty-three
// invocations were on v2.20.0, and the old one carried three published
// advisories. Plumber caught it on `main` — after the merge, which is the wrong
// end of the pipeline for something a comparison of two lines answers.
//
// The mistake worth naming is how it got there: the SHA was written from memory
// instead of copied from the pins the repository already holds. Nothing else in
// the file would tell you, because a pinned SHA is opaque by design — the
// trailing `# v2.12.2` is a comment, and a comment is not a control.
//
// So this compares them. It does not know which version is current, and it must
// not: that is the advisory scanner's job and it does it better. What this
// answers is narrower and cheaper — the repository must not disagree with
// itself about which build of an action it trusts.
func TestEveryWorkflowPinsAnActionToOneSHA(t *testing.T) {
	root := moduleRoot(t)
	dir := filepath.Join(root, ".github", "workflows")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the workflow directory: %v", err)
	}

	// `uses: owner/repo@<sha>` and the version comment that follows, which is
	// how every workflow here pins.
	pin := regexp.MustCompile(`uses:\s+([A-Za-z0-9._/-]+)@([a-f0-9]{40})(?:\s+#\s*(\S+))?`)

	type site struct{ workflow, sha, version string }
	seen := map[string][]site{}
	total := 0

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, entry.Name())) //nolint:gosec // paths from the walk
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		for _, m := range pin.FindAllStringSubmatch(string(body), -1) {
			action, sha, version := m[1], m[2], m[3]
			seen[action] = append(seen[action], site{entry.Name(), sha, version})
			total++
		}
	}

	if total == 0 {
		t.Fatal("no pinned action was found in .github/workflows: the pattern no longer " +
			"matches how this repository pins, and this test would pass while measuring nothing")
	}

	for action, sites := range seen {
		shas := map[string][]string{}
		for _, s := range sites {
			label := s.workflow
			if s.version != "" {
				label += " (" + s.version + ")"
			}
			shas[s.sha] = append(shas[s.sha], label)
		}
		if len(shas) < 2 {
			continue
		}
		// Sorted so two runs of the same disagreement read the same, and so the
		// message names every side rather than an arbitrary one.
		keys := make([]string, 0, len(shas))
		for sha := range shas {
			keys = append(keys, sha)
		}
		sort.Strings(keys)

		var detail strings.Builder
		for _, sha := range keys {
			where := shas[sha]
			sort.Strings(where)
			detail.WriteString("\n  " + sha[:12] + "… in " + strings.Join(where, ", "))
		}
		t.Errorf("%s is pinned to %d different SHAs, so one of them is the one nobody "+
			"updated and it is invisible without this comparison:%s",
			action, len(shas), detail.String())
	}
}

// Every workflow asks for the Go version mise pins, patch included.
//
// Two pull requests fixed the same failure on one morning, in two ways, and the
// merge is where they had to be reconciled. go1.26.6 fixed five standard-library
// vulnerabilities; a `go-version: '1.26'` minor pin let setup-go take whatever
// 1.26.x the runner image had cached, so govulncheck went red on every pull
// request — including ones that touched no Go file — and a rerun installed the
// same stale patch again.
//
// `check-latest: true` was the other fix, and it is not the one kept: it makes
// CI resolve a version the mise pin does not name, so the toolchain in CI and
// the one a contributor runs locally drift apart in silence. An exact pin is
// reproducible and moves when somebody decides it moves. This is what stops the
// two from disagreeing again — including in the six workflows that were still on
// the minor when the reconciliation happened, none of which the failure had
// reached yet.
func TestTheGoWorkflowPinsTheVersionMiseDoes(t *testing.T) {
	root := moduleRoot(t)

	config, err := os.ReadFile(filepath.Join(root, "mise.toml"))
	if err != nil {
		t.Fatalf("read mise.toml: %v", err)
	}
	wanted := regexp.MustCompile(`(?m)^go\s*=\s*"([0-9.]+)"`).FindSubmatch(config)
	if wanted == nil {
		t.Fatal("mise.toml pins no Go version, so this test cannot compare anything")
	}
	pin := string(wanted[1])
	if strings.Count(pin, ".") < 2 {
		t.Fatalf("mise pins Go %q, which names no patch: the comparison below would "+
			"accept the drift it exists to catch", pin)
	}

	entries, err := os.ReadDir(filepath.Join(root, ".github", "workflows"))
	if err != nil {
		t.Fatalf("read the workflow directory: %v", err)
	}
	asked := regexp.MustCompile(`go-version:\s*'([^']+)'`)
	found := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", entry.Name())) //nolint:gosec // paths from the walk
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			// Comment lines are prose, and prose quotes versions: the paragraph
			// explaining why the minor pin failed contains the literal string it
			// warns about, and matching it made this test fail on its own
			// explanation. Found by running it.
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			m := asked.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			found++
			if m[1] != pin {
				t.Errorf("%s asks for Go %q and mise pins %q: CI and a contributor's "+
					"laptop would build with different toolchains, and only one of them "+
					"is the one govulncheck judged", entry.Name(), m[1], pin)
			}
		}
	}
	if found == 0 {
		t.Fatal("no workflow asks for a Go version: the pattern no longer matches how " +
			"they are written, and this test would pass while measuring nothing")
	}
}
