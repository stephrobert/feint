package conformance

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Every network suite this repository carries is replayed by the workflow that
// exists to replay them (#574).
//
// It was not, and that absence is a third of why an Exoscale defect lived from
// a344f8d to 2026-08-27. runtime-proof.yml ran the Scaleway and Outscale
// network suites and not the Exoscale one; no leg, bridge or OVN, replayed it;
// local work happens under OVN, where the defect was masked by rule ordering
// (#491); and the only routine that replayed it under the bridge was
// `evidence:update`'s second leg, which nobody had run since. A suite no
// matrix carries has a verdict as old as the last person who thought to run it
// by hand.
//
// The denominator is derived, never listed: what has to run is every
// `tools/conformance/*/network.sh` on disk, so a fourth pack's suite is
// demanded the day it exists rather than the day somebody remembers. Both
// consumers are checked, because they answer different questions — the
// workflow is what CI proves, `leg.sh runtime` is what an operator reproduces,
// and leg.sh's own comment claims to carry the workflow's order.
func TestEveryNetworkSuiteIsReplayedByTheRuntimeProof(t *testing.T) {
	root := repoRoot(t)

	suites := networkSuites(t, root)
	// The reader proves it can find before it judges: three packs ship a
	// network suite today, and a glob that matched nothing would make every
	// assertion below vacuously true.
	if len(suites) < 3 {
		t.Fatalf("found %d network suite(s) under tools/conformance (%v): the reader is the "+
			"suspect, not the workflow", len(suites), suites)
	}
	if !contains(suites, "tools/conformance/exoscale/network.sh") {
		t.Fatalf("the reader did not find the Exoscale network suite, which is the one #574 was "+
			"about; it found %v", suites)
	}

	for _, consumer := range []struct {
		path string
		what string
	}{
		{
			path: filepath.Join(".github", "workflows", "runtime-proof.yml"),
			what: "the workflow that proves the dataplane on real machines, on both its legs",
		},
		{
			path: filepath.Join("tools", "conformance", "leg.sh"),
			what: "`mise run conformance:leg -- runtime`, which is how an operator reproduces that workflow",
		},
	} {
		body := readFile(t, filepath.Join(root, consumer.path))
		for _, suite := range suites {
			if !strings.Contains(body, suite) {
				t.Errorf("%s never runs %s — %s. A suite nothing replays reports the verdict of "+
					"whenever somebody last ran it by hand (#574)", consumer.path, suite, consumer.what)
			}
		}
	}
}

// networkSuites is every provider network suite on disk, as the repository-
// relative paths the workflow and leg.sh name them by.
func networkSuites(t *testing.T, root string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, "tools", "conformance", "*", "network.sh"))
	if err != nil {
		t.Fatalf("list the network suites: %v", err)
	}
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		rel, err := filepath.Rel(root, match)
		if err != nil {
			t.Fatalf("relativise %s: %v", match, err)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	sort.Strings(out)
	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // a fixed path in this repository
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func contains(haystack []string, needle string) bool {
	for _, entry := range haystack {
		if entry == needle {
			return true
		}
	}
	return false
}
