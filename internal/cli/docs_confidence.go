package cli

import (
	"fmt"
	"os"
	"strings"
)

// The two figures docs/confidence.md carries, generated rather than typed.
//
// The page is prose by design — it answers "what can I validate here" in a
// user's words — and prose with numbers in it is how a document starts
// disagreeing with the repository. The README's own status figures were wrong by
// a factor of four once, and nobody had lied.
//
// So the counts ride the same machinery as every other block: written by the
// binary that mounts the routes, checked by `feint docs --check`. What stays
// hand-written is the table, because a verdict is a judgement and this file has
// none to offer — the control on the table is
// TestEveryConfidenceRowCarriesItsProof, which demands that each row resolve to
// a proof or a limit.

const (
	confidenceStartMarker = "<!-- confidence:start -->"
	confidenceEndMarker   = "<!-- confidence:end -->"
)

// renderConfidence writes the counts. It takes the artefact rather than reading
// it again: `feint docs` has already loaded it for the routes page, and two
// readers of one file are two chances to disagree about it.
func renderConfidence(evidence *evidenceArtefact) string {
	var b strings.Builder
	b.WriteString(docsGenerated)
	b.WriteString("\n\n")

	if evidence == nil || len(evidence.Operations) == 0 {
		// An installed binary has no artefact. Saying so beats printing zero,
		// which reads as "nothing is proven" rather than "nothing was measured".
		b.WriteString("No evidence record was found, so this page cannot say how much of the\n")
		b.WriteString("mounted surface a real client has driven. `mise run evidence:update` writes\n")
		b.WriteString("it, and every figure here comes from it.\n")
		return b.String()
	}

	driven := 0
	for _, ev := range evidence.Operations {
		if ev.Driven {
			driven++
		}
	}
	mounted := len(evidence.Operations)

	fmt.Fprintf(&b, "**%d operations mounted, %d driven by a real client** in the recorded run.",
		mounted, driven)
	if undriven := mounted - driven; undriven > 0 {
		fmt.Fprintf(&b, " The\n%d that are not each state why at their route, and [routes.md](routes.md)\nprints the reason under the pack that owns it.\n", undriven)
	} else {
		b.WriteString(" Every\none of them is driven, so there is no reason left to state.\n")
	}
	return b.String()
}

func spliceConfidence(path string, evidence *evidenceArtefact) (bool, error) {
	if path == "" {
		return false, nil
	}
	current, err := os.ReadFile(path) //nolint:gosec // a path this repository owns
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !strings.Contains(string(current), confidenceStartMarker) {
		return false, nil
	}
	updated, err := spliceSection(string(current), confidenceStartMarker, confidenceEndMarker,
		renderConfidence(evidence))
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return updated != string(current), nil
}

func writeSplicedConfidence(path string, evidence *evidenceArtefact) error {
	current, err := os.ReadFile(path) //nolint:gosec // a path this repository owns
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !strings.Contains(string(current), confidenceStartMarker) {
		return nil
	}
	updated, err := spliceSection(string(current), confidenceStartMarker, confidenceEndMarker,
		renderConfidence(evidence))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return os.WriteFile(path, []byte(updated), 0o644) //nolint:gosec // documentation is world-readable by design
}
