package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// The fixture table credits a suite that names no provider (#26, #356).
//
// Every conformance suite before this one lived under a provider directory, so
// the reader that answers "does CI apply this fixture" was written as
// provider/suite and, worse, filtered through clientOf — the map that decides
// which *client* a suite proves. tools/conformance/faults.sh drives all four
// clients against an emulator of its own, so it belongs to no provider and to
// no single client, and under the old reading its Terraform fixture was
// published as "Applied in CI: no" while the `fields` leg applied it on every
// run.
//
// The subject is asserted before the question is asked: a repository where the
// workflow does not run this suite, or where the fixture no longer holds a
// required_providers block, would otherwise satisfy this test by having nothing
// to credit.
func TestTheFixtureTableCreditsATopLevelSuite(t *testing.T) {
	root := repoRoot(t)
	workflow := filepath.Join(root, ".github", "workflows", "conformance.yml")
	conformance := filepath.Join(root, conformanceRoot)
	// Keyed off the same base the readers are handed, so the comparison is
	// between two answers about one tree rather than between two spellings.
	fixture := filepath.ToSlash(filepath.Join(conformance, "faults"))

	// The subject exists: a top-level suite, run by the workflow, holding a
	// Terraform fixture.
	if _, err := os.Stat(filepath.Join(conformance, "faults.sh")); err != nil {
		t.Fatalf("tools/conformance/faults.sh is the subject of this test and is missing: %v", err)
	}
	pins, err := providerPins(conformance, func(string) bool { return false })
	if err != nil {
		t.Fatalf("read the provider pins: %v", err)
	}
	held := false
	for _, pin := range pins {
		if pin.Dir == fixture {
			held = true
		}
	}
	if !held {
		t.Fatalf("%s declares no required_providers block, so there is nothing for the table to credit", fixture)
	}

	applied, err := fixturesAppliedInCI(conformance, workflow)
	if err != nil {
		t.Fatalf("read which fixtures CI applies: %v", err)
	}
	if !applied[fixture] {
		t.Errorf("%s is applied by tools/conformance/faults.sh, which the workflow runs, and the "+
			"generated table would publish it as never applied in CI", fixture)
	}

	// And the other direction, without which the reader could be crediting
	// everything: a directory no suite locates stays uncredited.
	if applied[filepath.ToSlash(filepath.Join(conformance, "shared"))] {
		t.Error("tools/conformance/shared holds no Terraform fixture and was credited anyway: " +
			"the reader is matching something other than the locate idiom")
	}
}
