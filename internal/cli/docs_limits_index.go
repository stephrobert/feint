package cli

// The product coverage index at the top of docs/limits.md (#872): one table per
// provider answering "is my stack in scope" before the fifty-odd sections that
// follow. Kept in its own file, the way every other generated block here is
// (docs_axes.go, docs_prereq.go, docs_confidence.go), rather than in docs.go.

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/drift"
)

// renderLimitsIndex builds the product coverage table for the top of
// docs/limits.md (#872).
//
// A reader, not a measurement: every row comes from the committed artefacts
// in coverage/, so when the scan changes the table changes with it. The
// statuses are the counts' own verdicts — served when every operation the scan
// found is mounted, declined when the pack refused them all, partially served
// in between — and "not scanned" is the row a product the scan never looked
// at earns. Declined carries no product-level reason because there is none to
// carry: the reason lives per operation, in the pack's Declined() and in the
// artefact's entries, and inventing a product-level one would be exactly the
// format this repository refuses to invent.
//
// The rows are alphabetical within a provider, scanned rows first and
// not-scanned rows after them: a reader finds their product by name, and the
// loud remainder sits where it cannot be mistaken for coverage. Providers are
// alphabetical, which is the only order a reader can predict without knowing
// how many routes each one mounts.
func renderLimitsIndex(reports []drift.CoverageFile) string {
	ordered := append([]drift.CoverageFile(nil), reports...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Provider < ordered[j].Provider })

	var b strings.Builder
	b.WriteString(docsGenerated)
	for i, rep := range ordered {
		if i == 0 {
			b.WriteString("\n\n")
		} else {
			// One blank line between providers: the previous section already
			// ends on a newline, so a second one here would double it.
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "### %s\n\n", capitalise(rep.Provider))
		scanned := len(rep.Products)
		if len(rep.Unscanned) > 0 {
			fmt.Fprintf(&b, "The scan covers %d of the %d %s upstream declares. ", scanned, scanned+len(rep.Unscanned), plural(scanned+len(rep.Unscanned), "product", "products"))
			fmt.Fprintf(&b, "The other %d %s not scanned at all.\n\n",
				len(rep.Unscanned), plural(len(rep.Unscanned), "is", "are"))
		} else if len(rep.Groups) > scanned {
			// Exoscale files everything under one synthetic `exoscale` product
			// and Outscale under `osc`, while upstream names 14 and 51 tags.
			// "The scan covers all 1 product upstream declares" reads as a
			// complete answer to "is my stack in scope" and answers nothing.
			// Derived from the artefact, never from a provider name: the day a
			// scan splits by product, this sentence stops appearing on its own.
			fmt.Fprintf(&b, "This SDK exposes %d %s to the scan, and upstream files its operations "+
				"under %d named %s instead. The %s below %s the whole surface, not a per-product "+
				"answer: for that, read the group breakdown in `coverage/%s-coverage.json`.\n\n",
				scanned, plural(scanned, "product", "products"),
				len(rep.Groups), plural(len(rep.Groups), "group", "groups"),
				plural(scanned, "row", "rows"), plural(scanned, "is", "are"), rep.Provider)
		} else {
			fmt.Fprintf(&b, "The scan covers all %d %s upstream declares.\n\n", scanned, plural(scanned, "product", "products"))
		}
		b.WriteString("| Product | Status | Served | Declined | Untriaged | Upstream |\n")
		b.WriteString("|---|---|---:|---:|---:|---:|\n")

		products := append([]drift.ProductView(nil), rep.Products...)
		sort.Slice(products, func(i, j int) bool { return products[i].Product < products[j].Product })
		for _, p := range products {
			status := "partially served"
			switch {
			case p.Total > 0 && p.Implemented == p.Total:
				status = "served"
			case p.Total > 0 && p.Implemented == 0 && p.Declined == p.Total:
				status = "declined"
			}
			fmt.Fprintf(&b, "| `%s` | %s | %d | %d | %d | %d |\n",
				p.Product, status, p.Implemented, p.Declined, p.Unknown, p.Total)
		}

		unscanned := append([]string(nil), rep.Unscanned...)
		sort.Strings(unscanned)
		for _, name := range unscanned {
			fmt.Fprintf(&b, "| `%s` | **not scanned** | — | — | — | — |\n", name)
		}
		if len(unscanned) > 0 {
			// Loud on purpose, and the consequence stated where the row is:
			// a stack on an unscanned product does not hit the emulator, so a
			// reader who misses this row acts on a machine that is not there.
			b.WriteString("\n**Not scanned is the row that costs a reader their stack.** A client " +
				"that calls one of these products does not hit the emulator at all: it reaches " +
				"the real endpoint with whatever credentials the shell holds. The scan covers " +
				"only the products the emulator has started; a product enters this table the " +
				"day it enters the scan.\n")
		}
	}
	b.WriteString("\nNames in the first column are the SDK's package names, not the portal's " +
		"product names: `k8s` is Kapsule, `tem` Transactional Email, `s2s_vpn` Site-to-Site " +
		"VPN. `test` is the SDK's own fixture package, counted because it is measured rather " +
		"than filtered by hand.\n")
	b.WriteString("\nStatuses: **served** means every operation the scan found is mounted; " +
		"**partially served** means some are; **declined** means the pack decided not to serve " +
		"the product, and the reason for each operation is in the pack's `Declined()`; " +
		"**not scanned** means the scan never looked — the client leaves the machine.\n")
	return b.String()
}

// spliceLimitsIndex renders the product coverage index and reports whether
// the file would change. It returns false with no error when the file does
// not exist, or when it never claimed to carry this table — same terms as
// every other generated section in this file.
func spliceLimitsIndex(path string, reports []drift.CoverageFile) (bool, error) {
	if path == "" {
		return false, nil
	}
	current, err := os.ReadFile(path) //nolint:gosec // an operator-supplied path, read only
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !strings.Contains(string(current), limitsIndexStartMarker) {
		// A Markdown file that never claimed to carry this table.
		return false, nil
	}
	updated, err := spliceSection(string(current), limitsIndexStartMarker, limitsIndexEndMarker, renderLimitsIndex(reports))
	if err != nil {
		return false, err
	}
	return updated != string(current), nil
}

func writeSplicedLimitsIndex(path string, reports []drift.CoverageFile) error {
	current, err := os.ReadFile(path) //nolint:gosec // same path as above
	if err != nil {
		return err
	}
	updated, err := spliceSection(string(current), limitsIndexStartMarker, limitsIndexEndMarker, renderLimitsIndex(reports))
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(updated), 0o644) //nolint:gosec // documentation is world-readable by design
}
