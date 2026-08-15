package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// What produced a record, and why that is a control rather than metadata (#171).
//
// The rule the evidence artefact lives under was stated twice, in prose, and
// held by nothing:
//
//	internal/cli/evidence.go: "the /falsify criterion is that deleting a
//	conformance assertion demotes the operations it proved"
//	mise.toml: "Removing an assertion from a suite and re-running this task must
//	demote the operations it proved — that is the falsification the record lives
//	under."
//
// `grep -rn demotes --include='*_test.go'` answered nothing. That criterion is
// what separates a record from a high-water mark: if a proof can outlive the
// assertion that produced it, deleting a conformance check raises nothing and
// lowers nothing, and every figure this project publishes rests on it.
//
// The join is where the risk is concrete. Two legs are merged by taking the
// stronger answer per axis, which is safe *only* because both are fresh — a
// convention the caller was trusted to respect, and nothing more.
//
// So provenance is not printed beside the record, it gates the join: an artefact
// whose inputs differ from this run's cannot contribute a level. Remove an
// assertion from a suite and the suite digest moves, which makes the previous
// artefact unjoinable, which is exactly the sentence above, enforced.
//
// TestAJoinRefusesAnArtefactFromOtherInputs fails without this.

// provenance is the digest of everything a regeneration reads.
//
// Digests of the inputs rather than a git SHA, deliberately: a digest is
// reproducible from a checkout, it still answers on a dirty tree where somebody
// is regenerating locally, and it answers the question that matters — did the
// inputs move? — instead of which commit happened to be checked out.
type provenance struct {
	// Contracts is the API descriptions every response is validated against.
	Contracts string `json:"contracts"`
	// Shapes is the recorded real-cloud answers the shape and field axes read.
	Shapes string `json:"shapes"`
	// Suites is the conformance scripts. This is the one that moves when an
	// assertion is added or removed, which is what makes the join gate mean
	// what the criterion says.
	Suites string `json:"suites"`
}

// provenanceOf digests the three input sets. A directory that does not exist
// digests to "absent" rather than failing: `feint evidence` is runnable from a
// tree without recordings, and refusing there would trade a missing input for a
// missing command.
func provenanceOf(contractsDir, shapesDir, suitesDir string) (provenance, error) {
	contracts, err := digestTree(contractsDir, ".json")
	if err != nil {
		return provenance{}, err
	}
	shapes, err := digestTree(shapesDir, ".json")
	if err != nil {
		return provenance{}, err
	}
	suites, err := digestTree(suitesDir, ".sh")
	if err != nil {
		return provenance{}, err
	}
	return provenance{Contracts: contracts, Shapes: shapes, Suites: suites}, nil
}

// digestTree hashes every file under root with the given suffix, path and
// content both, in sorted order.
//
// Sorted because two runs on the same tree must produce the same string:
// filesystem order is not a promise, and a digest that changes on its own would
// refuse every join and be disabled within the week. The path is hashed beside
// the content so that renaming a suite is a change, which it is.
func digestTree(root, suffix string) (string, error) {
	if root == "" {
		return "absent", nil
	}
	info, err := os.Stat(root)
	if os.IsNotExist(err) {
		return "absent", nil
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", root)
	}

	var paths []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, suffix) {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)

	sum := sha256.New()
	for _, path := range paths {
		body, err := os.ReadFile(path) //nolint:gosec // a directory the caller named
		if err != nil {
			return "", err
		}
		// The relative path, so the digest does not move with the checkout's
		// location: two people regenerating from different directories must
		// agree, or the gate refuses their joins for no reason.
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return "", err
		}
		sum.Write([]byte(rel))
		sum.Write([]byte{0})
		sum.Write(body)
		sum.Write([]byte{0})
	}
	return hex.EncodeToString(sum.Sum(nil))[:16], nil
}

// differsFrom answers the inputs that moved between two records, empty when
// they were produced from the same ones.
func (p provenance) differsFrom(other provenance) []string {
	var moved []string
	if p.Contracts != other.Contracts {
		moved = append(moved, "contracts")
	}
	if p.Shapes != other.Shapes {
		moved = append(moved, "shapes")
	}
	if p.Suites != other.Suites {
		moved = append(moved, "suites")
	}
	return moved
}
