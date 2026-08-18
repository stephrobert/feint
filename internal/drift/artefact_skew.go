package drift

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// The committed artefact repeats what the packs declare, and #298 measured what
// happens when nothing compares the copy with its source: a decline reason
// rewritten by #260 took four days to reach coverage/scaleway-coverage.json,
// during which the versioned verdict for 24 operations was a sentence the code
// had already corrected. Both gates passed over it — the baseline compares
// operation names, and docs --check regenerates the README from the same stale
// artefact, so the two agreed with each other while both disagreed with the
// code.
//
// ArtefactSkew is the missing comparison. It takes the committed artefact and
// the pack's declarations — the operations its routes serve, the operations it
// declines and why — and reports every entry whose verdict the pack no longer
// states: a reason that changed, a status that flipped, a decline the artefact
// never recorded. The caller treats any line as drift (exit 2), because the
// artefact follows the code and never the other way round.
//
// Both sides are scoped to the products the artefact itself carries, so a pack
// declaration outside the scan's --products list is not reported as missing: the
// artefact is the record of a scoped scan, and this function judges the copy
// against its source on the surface the copy claims to cover. What it leaves
// alone is the upstream side — entries appearing or disappearing because the SDK
// moved is the baseline's verdict, not this one's.
//
// TestTheCommittedArtefactCarriesWhatThePacksDeclare (internal/cli) fails
// without this comparison, and tools/falsify/specs/artefact-reasons.json proves
// it bites: re-editing the pack sentence #260 rewrote turns the test red.
func ArtefactSkew(committed CoverageFile, served []string, declined map[string]string) []string {
	products := make([]string, 0, len(committed.Products))
	for _, p := range committed.Products {
		products = append(products, p.Product)
	}

	servedSet := toSet(OnlyProductNames(served, products))
	scoped := make(map[string]string, len(declined))
	for op, reason := range declined {
		if product, _, found := strings.Cut(op, "/"); len(products) == 0 || (found && slices.Contains(products, product)) {
			scoped[op] = reason
		}
	}

	var skew []string
	listed := make(map[string]bool, len(committed.Entries))
	for _, e := range committed.Entries {
		listed[e.Operation] = true
		reason, isDeclined := scoped[e.Operation]
		// Same precedence as Compare: a served operation is implemented even if
		// a stale decline also names it. Diverging here would report a skew
		// that regenerating the artefact cannot remove.
		switch {
		case servedSet[e.Operation]:
			if e.Status != StatusImplemented {
				skew = append(skew, fmt.Sprintf("%s: the artefact says %q, the pack serves it", e.Operation, e.Status))
			}
		case isDeclined:
			if e.Status != StatusDeclined {
				skew = append(skew, fmt.Sprintf("%s: the artefact says %q, the pack declines it", e.Operation, e.Status))
			} else if e.Reason != reason {
				skew = append(skew, fmt.Sprintf("%s: the artefact carries a reason the pack no longer states (artefact: %q; pack: %q)", e.Operation, e.Reason, reason))
			}
		default:
			if e.Status != StatusUnknown {
				skew = append(skew, fmt.Sprintf("%s: the artefact says %q, the pack neither serves nor declines it", e.Operation, e.Status))
			}
		}
	}

	// A decline the artefact never recorded is the same defect before its first
	// regeneration: the pack made a decision and the committed record still
	// calls the operation untriaged — or worse, does not mention it. Served
	// operations get no such check, because a route absent from the entries is
	// either an orphan (Compare's verdict) or outside the scanned surface.
	for op := range scoped {
		if !listed[op] {
			skew = append(skew, fmt.Sprintf("%s: the pack declines it and the artefact has no entry", op))
		}
	}

	sort.Strings(skew)
	return skew
}
