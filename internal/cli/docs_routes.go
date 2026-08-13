package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// The reference a reader needs before installing anything: is my resource
// emulated?
//
// Until this existed, answering it meant running the binary and reading
// /_feint/routes, or reading the packs. Both are fine for somebody who already
// decided to try feint, and useless to somebody deciding whether to.
//
// It is generated for the reason every number in the README is: the counts in
// this repository have been wrong before, by a factor of four, and nobody lied.
// The routes come from the packs mounted in this process, and the declined
// operations from Declined(), so the page cannot disagree with what the binary
// serves — it is written by the binary that serves them.

const (
	routesStartMarker = "<!-- routes:start -->"
	routesEndMarker   = "<!-- routes:end -->"
)

// renderRoutes builds the reference body: every mounted route by provider and
// product, each with the proofs it has earned, then what each pack declines
// on purpose. evidence may be nil — an installed binary has no artefact — and
// the page then says so instead of rendering a column of guesses.
func renderRoutes(evidence *evidenceArtefact) (string, error) {
	srv, _, err := newServer(nil)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(docsGenerated)
	b.WriteString("\n\n")

	total := 0
	for _, p := range srv.Packs() {
		total += len(p.Routes())
	}
	fmt.Fprintf(&b, "%d routes across %d packs. Every route names the upstream operation it\n", total, len(srv.Packs()))
	b.WriteString("stands for, in the provider's own spelling: that name is what the drift scan\n")
	b.WriteString("matches against the upstream SDK, so a route that renames it stops being\n")
	b.WriteString("counted.\n")
	b.WriteString(evidenceLegend(evidence))

	for _, p := range srv.Packs() {
		fmt.Fprintf(&b, "\n## %s\n\n", strings.ToUpper(p.Name()[:1])+p.Name()[1:])

		byProduct := make(map[string][]string)
		for _, r := range p.Routes() {
			row := fmt.Sprintf("| `%s` | `%s` | `%s` |", r.Method, r.Path, r.Operation)
			if evidence != nil {
				row += fmt.Sprintf(" %s |", evidenceTokens(evidence.Operations[r.Operation]))
			}
			byProduct[productOf(r.Operation)] = append(byProduct[productOf(r.Operation)], row)
		}
		products := make([]string, 0, len(byProduct))
		for product := range byProduct {
			products = append(products, product)
		}
		sort.Strings(products)

		header := "| Method | Path | Upstream operation |\n|---|---|---|\n"
		if evidence != nil {
			header = "| Method | Path | Upstream operation | Proven by |\n|---|---|---|---|\n"
		}
		for _, product := range products {
			rows := byProduct[product]
			sort.Strings(rows)
			fmt.Fprintf(&b, "### `%s`\n\n", product)
			b.WriteString(header)
			for _, row := range rows {
				b.WriteString(row)
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}

		declined := append([]emulator.Decline(nil), p.Declined()...)
		sort.Slice(declined, func(i, j int) bool { return declined[i].Operation < declined[j].Operation })
		fmt.Fprintf(&b, "### Declined on purpose (%d)\n\n", len(declined))
		if len(declined) == 0 {
			b.WriteString("Nothing yet: everything upstream declares is either served or untriaged.\n")
			continue
		}
		b.WriteString("Operations this pack knowingly does not serve, and why. Declining is a\n")
		b.WriteString("decision the drift gate records, which is what separates it from having\n")
		b.WriteString("missed one — and the reason is what separates a decision from a list.\n\n")
		for _, d := range declined {
			fmt.Fprintf(&b, "- `%s` — %s\n", d.Operation, d.Reason)
		}
	}

	return b.String(), nil
}

// evidenceLegend states what each proof token asserts, and what is absent on
// purpose. It lives inside the generated block so a change of doctrine here
// cannot leave stale prose behind — the same reason no figure is frozen in
// hand-written text (#127).
func evidenceLegend(evidence *evidenceArtefact) string {
	if evidence == nil {
		return "\nNo evidence artefact was found, so the rows carry no proof column: what a\n" +
			"conformance run proved is recorded by `mise run evidence:update` into\n" +
			"`coverage/evidence.json`, and this page only prints what that record holds.\n"
	}
	var b strings.Builder
	b.WriteString("\nEach route lists the proofs it has earned — independent axes, side by side,\n")
	b.WriteString("never added into one number. None implies another: a route can be driven by\n")
	b.WriteString("a real client and never contract-checked, probed and never driven. The order\n")
	b.WriteString("of tokens is fixed for diff stability; it is not a ranking.\n\n")
	b.WriteString("- `client` — a real client drove it in the recorded conformance run. It\n")
	b.WriteString("  proves what the suite asserted, nothing more: #116 and #83 were driven the\n")
	b.WriteString("  whole time they were wrong.\n")
	b.WriteString("- `contract` — at least one answer was validated against the provider's own\n")
	b.WriteString("  API description and none violated it. `contract-violated` — one did.\n")
	b.WriteString("  Absent: no answer of this operation was ever validated, which is a\n")
	b.WriteString("  different fact from \"no violation\".\n")
	b.WriteString("- `shape` — an observed real-cloud answer covers it, and the shapes gate\n")
	b.WriteString("  holds the emulator to that record, field by field.\n")
	fmt.Fprintf(&b, "- `runtime` — it was driven in a run whose machines were real (machines: %s).\n",
		strings.Join(evidence.Machines, ", "))
	b.WriteString("  It says the control plane ran with side effects on; it does not say a\n")
	b.WriteString("  machine-level assertion named this operation.\n")
	b.WriteString("- `probe` — the contract-driven probe reached it. Protocol only: a\n")
	b.WriteString("  well-shaped empty object would pass.\n\n")
	b.WriteString("Two proofs the suites deliver and this record does not list: *behaviour* (a\n")
	b.WriteString("lifecycle assertion names the operation) and *negative* (an error case\n")
	b.WriteString("reproduced on purpose). They have no machine-checkable source today — the\n")
	b.WriteString("suites emit no assertion-level signal — and a hand-written value would be a\n")
	b.WriteString("comment, not a control. They appear when the suites can name what each\n")
	b.WriteString("assertion proved, not before.\n\n")
	b.WriteString("The record is `coverage/evidence.json`, regenerated by `mise run\n")
	b.WriteString("evidence:update`: a proof disappears from these rows when its assertion\n")
	b.WriteString("disappears from the suite.\n")
	return b.String()
}

// evidenceTokens renders one operation's axes. A fixed order, never a count:
// a reader must be able to see which proofs exist and must never be handed
// "3 of 5".
func evidenceTokens(ev emulator.Evidence) string {
	var t []string
	if ev.Driven {
		t = append(t, "`client`")
	}
	switch ev.Contract {
	case emulator.ContractClean:
		t = append(t, "`contract`")
	case emulator.ContractViolating:
		t = append(t, "**`contract-violated`**")
	}
	if ev.Shape == emulator.ShapeObserved {
		t = append(t, "`shape`")
	}
	if ev.Dataplane {
		t = append(t, "`runtime`")
	}
	if ev.Probed {
		t = append(t, "`probe`")
	}
	if len(t) == 0 {
		return "—"
	}
	return strings.Join(t, " ")
}

// loadEvidenceArtefact reads coverage/evidence.json for the reference page.
// Absent is not an error — an installed binary has no artefact — but a file
// that exists and cannot be accounted for is, the same bar readEvidence
// applies everywhere else.
func loadEvidenceArtefact(path string) (*evidenceArtefact, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	return readEvidence(path)
}

// spliceRoutes renders the reference into the file's markers and reports whether
// it changed. Absent file, or a file without the markers: nothing to do, no
// complaint — the same terms as the contract table, so `feint docs` still works
// for somebody who installed the binary and has no docs/ directory.
func spliceRoutes(path string, evidence *evidenceArtefact) (bool, error) {
	if path == "" {
		return false, nil
	}
	current, err := os.ReadFile(path) //nolint:gosec // operator-supplied path, by design
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !strings.Contains(string(current), routesStartMarker) {
		return false, nil
	}
	rendered, err := renderRoutes(evidence)
	if err != nil {
		return false, err
	}
	updated, err := spliceSection(string(current), routesStartMarker, routesEndMarker, rendered)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return updated != string(current), nil
}

// writeSplicedRoutes writes what spliceRoutes reported as changed.
func writeSplicedRoutes(path string, evidence *evidenceArtefact) error {
	current, err := os.ReadFile(path) //nolint:gosec // operator-supplied path, by design
	if err != nil {
		return err
	}
	rendered, err := renderRoutes(evidence)
	if err != nil {
		return err
	}
	updated, err := spliceSection(string(current), routesStartMarker, routesEndMarker, rendered)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return os.WriteFile(path, []byte(updated), 0o644) //nolint:gosec // a reference page is world-readable by design
}
