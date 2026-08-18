package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/stephrobert/feint/internal/drift"
)

// applyBaseline is the automated drift gate.
//
// With --write-baseline it records the current upstream surface; otherwise it
// compares and returns exitDrift as soon as an operation appeared upstream that
// nobody triaged. The weekly workflow runs the first form and opens a pull
// request, so an upstream addition always lands as a reviewable diff rather than
// as silence.
func applyBaseline(path string, write bool, rep drift.Report, products string, stdout io.Writer) (int, error) {
	list := splitProducts(products)

	if write {
		base := drift.NewBaseline(rep, list)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644) //nolint:gosec // a committed, non-secret baseline
		if err != nil {
			return exitError, fmt.Errorf("write baseline: %w", err)
		}
		if err := base.Write(f); err != nil {
			_ = f.Close()
			return exitError, err
		}
		// Checked, not deferred: a failing Close on a written file means the
		// baseline is truncated, and reporting success would commit a file the
		// drift gate then compares against forever.
		if err := f.Close(); err != nil {
			return exitError, fmt.Errorf("write baseline %s: %w", path, err)
		}
		fmt.Fprintf(stdout, "baseline written to %s (%d unknown operations)\n", path, len(base.Unknown))
		return exitOK, nil
	}

	f, err := os.Open(path) //nolint:gosec // a committed, non-secret baseline
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return exitError, fmt.Errorf("baseline %s does not exist; create it with --write-baseline", path)
		}
		return exitError, fmt.Errorf("open baseline: %w", err)
	}
	defer func() { _ = f.Close() }()

	base, err := drift.LoadBaseline(f)
	if err != nil {
		return exitError, err
	}

	diff := base.Compare(rep)
	if err := diff.WriteText(stdout); err != nil {
		return exitError, err
	}
	if len(diff.Added) > 0 {
		return exitDrift, nil
	}
	return exitOK, nil
}

// artefactSkew loads the committed coverage artefact and reports every verdict
// in it that the pack no longer states. The comparison itself lives in
// internal/drift (ArtefactSkew), where the test that reads the real repository
// artefacts shares it — one comparison, two callers, so the gate and the test
// cannot drift apart the way the two halves of the old drift gate did.
func artefactSkew(path string, served []string, declined map[string]string) ([]string, error) {
	f, err := os.Open(path) //nolint:gosec // a committed, non-secret artefact
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("coverage artefact %s does not exist; write it with drift:update", path)
		}
		return nil, fmt.Errorf("open coverage artefact: %w", err)
	}
	defer func() { _ = f.Close() }()

	committed, err := drift.LoadCoverage(f)
	if err != nil {
		return nil, fmt.Errorf("read coverage artefact %s: %w", path, err)
	}
	return drift.ArtefactSkew(committed, served, declined), nil
}

func splitProducts(products string) []string {
	if products == "" {
		return nil
	}
	parts := strings.Split(products, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
