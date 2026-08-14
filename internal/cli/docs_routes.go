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
// upstream group, each with the proofs it has earned, then what each pack
// declines on purpose. evidence may be nil — an installed binary has no
// artefact — and the page then says so instead of rendering a column of
// guesses.
//
// groups maps an operation to the group the upstream's own document files it
// under, read from the coverage artefacts so this page and the README tables
// can never disagree on where an operation belongs. An operation the artefacts
// do not know falls back to the first segment of its name — the product — which
// is exactly what every section was before groups existed. The map may be
// empty (an installed binary has no coverage/ either) and the page then keeps
// its product sections rather than failing.
func renderRoutes(evidence *evidenceArtefact, groups map[string]string) (string, error) {
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

	// The group an operation files under: the upstream's own grouping when the
	// coverage artefacts carry it, the product segment of its name otherwise.
	sectionOf := func(operation string) string {
		if g := groups[operation]; g != "" {
			return g
		}
		return productOf(operation)
	}

	for _, p := range srv.Packs() {
		fmt.Fprintf(&b, "\n## %s\n\n", strings.ToUpper(p.Name()[:1])+p.Name()[1:])

		bySection := make(map[string][]string)
		for _, r := range p.Routes() {
			row := fmt.Sprintf("| `%s` | `%s` | `%s` |", r.Method, r.Path, r.Operation)
			if evidence != nil {
				row += fmt.Sprintf(" %s |", evidenceTokens(evidence.Operations[r.Operation]))
			}
			bySection[sectionOf(r.Operation)] = append(bySection[sectionOf(r.Operation)], row)
		}
		sections := make([]string, 0, len(bySection))
		for section := range bySection {
			sections = append(sections, section)
		}
		sort.Strings(sections)

		header := "| Method | Path | Upstream operation |\n|---|---|---|\n"
		if evidence != nil {
			header = "| Method | Path | Upstream operation | Proven by |\n|---|---|---|---|\n"
		}
		for _, section := range sections {
			rows := bySection[section]
			sort.Strings(rows)
			fmt.Fprintf(&b, "### `%s`\n\n", section)
			b.WriteString(header)
			for _, row := range rows {
				b.WriteString(row)
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}

		declined := append([]emulator.Decline(nil), p.Declined()...)
		fmt.Fprintf(&b, "### Declined on purpose (%d)\n\n", len(declined))
		if len(declined) == 0 {
			b.WriteString("Nothing yet: everything upstream declares is either served or untriaged.\n")
			continue
		}
		b.WriteString("Operations this pack knowingly does not serve, and why. Declining is a\n")
		b.WriteString("decision the drift gate records, which is what separates it from having\n")
		b.WriteString("missed one — and the reason is what separates a decision from a list.\n")
		b.WriteString("One line is one decision: the group upstream files the operations under,\n")
		b.WriteString("how many it covers, and the reason they share. The per-operation verdicts\n")
		b.WriteString("are in `coverage/`, one artefact per provider.\n\n")
		for _, block := range declineBlocks(declined, sectionOf) {
			fmt.Fprintf(&b, "- `%s` — %s — %s\n", block.group, countNoun(block.n, "operation"), block.reason)
		}
	}

	return b.String(), nil
}

// declineBlock is one refusal decision as the page states it: a group, how many
// of its operations it covers, and the reason they share.
type declineBlock struct {
	group  string
	n      int
	reason string
}

// declineBlocks folds a pack's declined operations into decisions.
//
// The flat list this replaces was 253 bullets long for Exoscale — 146 of them
// the same sentence about managed databases repeated verbatim — which buried
// the eight other decisions it contained. The unit a reader weighs is the
// decision, and a decision here is a group plus the reason its operations
// share: 146 dbaas refusals collapse into one line saying 146, they do not
// vanish. A group refused for several distinct reasons renders one line per
// reason, because those are distinct decisions.
//
// Ordered by group, then by weight so a group's biggest refusal leads, reason
// text breaking ties so two runs render the same bytes.
func declineBlocks(declined []emulator.Decline, sectionOf func(string) string) []declineBlock {
	type key struct{ group, reason string }
	counts := make(map[key]int)
	for _, d := range declined {
		counts[key{sectionOf(d.Operation), d.Reason}]++
	}
	out := make([]declineBlock, 0, len(counts))
	for k, n := range counts {
		out = append(out, declineBlock{group: k.group, n: n, reason: k.reason})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].group != out[j].group {
			return out[i].group < out[j].group
		}
		if out[i].n != out[j].n {
			return out[i].n > out[j].n
		}
		return out[i].reason < out[j].reason
	})
	return out
}

// countNoun renders "1 operation" or "146 operations", because "operation(s)"
// on a page a human reads is the writer passing their work to the reader.
func countNoun(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
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
	b.WriteString("- `probe` — the contract-driven probe built a call from the operation's own\n")
	b.WriteString("  request schema and its success answer validated against the response\n")
	b.WriteString("  schema; where the contract declares a page-size parameter, one probed\n")
	b.WriteString("  call carried it and the answer stayed within the bound it asked. Its\n")
	b.WriteString("  refusals, when it met any, validated against the provider's declared\n")
	b.WriteString("  error shape. Protocol only: a well-shaped empty inventory would pass,\n")
	b.WriteString("  and cursors and filters are not exercised.\n")
	b.WriteString("- `probe-refusal` — everything the probe could validate here was a refusal,\n")
	b.WriteString("  read and validated against the provider's declared error shape. It does\n")
	b.WriteString("  not say the success shape was ever seen. Absent (neither token): the\n")
	b.WriteString("  probe validated nothing — not reached, or answers nothing upstream\n")
	b.WriteString("  gives a shape to.\n")
	b.WriteString("- `behaviour` — inside a span a suite declared to be a lifecycle assertion,\n")
	b.WriteString("  this operation touched a resource the emulator's own store saw created and\n")
	b.WriteString("  then destroyed. The suite declares the span; the emulator verifies the\n")
	b.WriteString("  lifecycle and resolves which operations took part — no operation is ever\n")
	b.WriteString("  named by hand. It does not say the operation's own effect was asserted,\n")
	b.WriteString("  only that it worked inside a real create-to-destroy sequence.\n")
	b.WriteString("- `negative` — inside a span where a suite declared it was demanding a\n")
	b.WriteString("  refusal, this operation really answered a 4xx to a real client, and the\n")
	b.WriteString("  emulator refuses to close such a span when no refusal happened. One\n")
	b.WriteString("  demanded refusal earns it; it does not say every refusal the operation\n")
	b.WriteString("  owes was reproduced.\n\n")
	b.WriteString("The two span-borne axes were absent until the suites could emit a signal at\n")
	b.WriteString("the assertion level (`POST /_feint/assert`): a hand-written value would have\n")
	b.WriteString("been a comment, not a control.\n\n")
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
	switch ev.Probed {
	case emulator.ProbeResponse:
		t = append(t, "`probe`")
	case emulator.ProbeRefusal:
		t = append(t, "`probe-refusal`")
	}
	if ev.Behaviour {
		t = append(t, "`behaviour`")
	}
	if ev.Negative {
		t = append(t, "`negative`")
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
func spliceRoutes(path string, evidence *evidenceArtefact, groups map[string]string) (bool, error) {
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
	rendered, err := renderRoutes(evidence, groups)
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
func writeSplicedRoutes(path string, evidence *evidenceArtefact, groups map[string]string) error {
	current, err := os.ReadFile(path) //nolint:gosec // operator-supplied path, by design
	if err != nil {
		return err
	}
	rendered, err := renderRoutes(evidence, groups)
	if err != nil {
		return err
	}
	updated, err := spliceSection(string(current), routesStartMarker, routesEndMarker, rendered)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return os.WriteFile(path, []byte(updated), 0o644) //nolint:gosec // a reference page is world-readable by design
}
