package cli

import (
	"fmt"
	"os"
	"strings"
)

// The per-provider axis table in docs/routes.md, generated rather than typed.
//
// Why generated, and it is the argument docs.go already makes twice. The
// contract policy table in docs/limits.md was three hand-written paragraphs, and
// its numbers went wrong twice: once by going stale, once by being recomputed
// from the extractor's own --assume-closed flag, which measures this project's
// assumption rather than what the provider declares. The README's route counts
// said 12 / 3 / 5 when the packs mounted 55 / 20 / 14, and nobody had lied.
//
// A conformance table is the same risk with a worse failure: a stale one reads
// as a measurement. So it is written by the binary, from coverage/evidence.json,
// and `feint docs --check` — which prepush and the pre-commit hook run — refuses
// the commit the moment the page and the record disagree.
//
// Why docs/routes.md rather than the README or docs/limits.md. Three reasons,
// and the first is decisive:
//
//   - the long legend that defines every axis is already inside this page's
//     other generated block (evidenceLegend, docs_routes.go), including the
//     sentence about injected faults. A table anywhere else would either
//     duplicate that legend — a second copy, which is how one of them goes
//     stale — or omit it and print percentages nobody can read.
//   - the table is the column total of the rows this page already prints, from
//     the artefact `feint docs` has already loaded. Two readers of one file are
//     two chances to disagree about it (docs_confidence.go says so).
//   - docs/confidence.md closes by saying it "deliberately carries no score",
//     and it is right to: it is the coarse human view. Putting a percentage
//     table there would contradict the page in its own words.
//
// The one-line meaning of each axis comes from evidenceAxisList, so the table
// and the command cannot describe an axis differently.

const (
	axesStartMarker = "<!-- axes:start -->"
	axesEndMarker   = "<!-- axes:end -->"
)

// renderAxes builds the block. evidence may be nil — an installed binary has no
// artefact — and the page then says so rather than printing a table of zeroes,
// which reads as "nothing is proven" instead of "nothing was measured".
func renderAxes(evidence *evidenceArtefact) (string, error) {
	var b strings.Builder
	b.WriteString(docsGenerated)
	b.WriteString("\n\n")

	if evidence == nil || len(evidence.Operations) == 0 {
		b.WriteString("No evidence record was found, so this page cannot say how much of each\n")
		b.WriteString("pack's surface a recorded run has proven. `mise run evidence:update` writes\n")
		b.WriteString("`coverage/evidence.json`, and every figure below comes from it.\n")
		return b.String(), nil
	}

	owners, providers, err := operationOwners()
	if err != nil {
		return "", err
	}
	table, err := tallyEvidence("coverage/evidence.json", evidence, owners, providers, "", "")
	if err != nil {
		return "", err
	}

	machines := "none declared"
	if len(evidence.Machines) > 0 {
		machines = strings.Join(evidence.Machines, ", ")
	}
	fmt.Fprintf(&b, "%d operations served across %d packs, counted from the record of the last\n",
		table.Total.Served, len(table.Providers))
	fmt.Fprintf(&b, "recorded conformance run (machines: %s). Reproduce it yourself, offline,\n", machines)
	b.WriteString("from the committed artefact:\n\n")
	b.WriteString("```bash\n")
	b.WriteString("feint coverage --evidence coverage/evidence.json\n")
	b.WriteString("feint coverage --evidence coverage/evidence.json --axis negative --provider exoscale\n")
	b.WriteString("feint coverage --evidence coverage/evidence.json --format json\n")
	b.WriteString("```\n\n")
	b.WriteString("The first prints the table below. The second names the operations at zero on\n")
	b.WriteString("one axis, which is what turns a score into a work queue — a percentage says\n")
	b.WriteString("how much is left, a list says what. The third publishes the same numbers for\n")
	b.WriteString("a workflow. None of them opens a socket.\n\n")

	// The table. Percentage and count in one cell: the percentage is what a
	// reader compares across clouds, the count is what they can act on, and a
	// population of 93 moves by more than a whole point per operation.
	b.WriteString("| Cloud | Served |")
	for _, a := range table.Total.Axes {
		fmt.Fprintf(&b, " `%s` |", a.Axis)
	}
	b.WriteString("\n|---|---|")
	for range table.Total.Axes {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, row := range append(append([]providerTally(nil), table.Providers...), table.Total) {
		name := strings.ToUpper(row.Provider[:1]) + row.Provider[1:]
		if row.Provider == "all" {
			name = "**All three**"
		}
		fmt.Fprintf(&b, "| %s | %d |", name, row.Served)
		for _, a := range row.Axes {
			fmt.Fprintf(&b, " %d %% (%d) |", a.Percent, a.Earned)
		}
		b.WriteString("\n")
	}

	b.WriteString("\nWhat each axis says, one line each. They are independent and are never added\n")
	b.WriteString("into one number: none of them implies another, and an operation can be driven\n")
	b.WriteString("by a real client and never contract-checked, or probed and never driven.\n\n")
	b.WriteString("| Axis | What earns it |\n|---|---|\n")
	for _, a := range evidenceAxisList() {
		fmt.Fprintf(&b, "| `%s` | %s |\n", a.Name, a.Meaning)
	}

	// The property that makes the negative column mean anything, stated in the
	// generated block so it cannot be left behind by a change of doctrine. A
	// reader who does not have it could conclude that arming faults improves the
	// score, which is the one thing this repository decided to make impossible.
	// The two ends of the negative column, read from the table rather than
	// typed. A generated block that quotes its own figures in prose is the
	// staleness this whole file exists to prevent, one paragraph lower down.
	low, high := spread(table.Providers, "negative")
	b.WriteString("\n**An injected fault earns none of them.** The emulator can be made to refuse on\n")
	b.WriteString("purpose (`PUT /_feint/faults`), and an answer produced that way moves no counter\n")
	b.WriteString("here: the operation stays un-driven and un-proven, and a `negative` span cannot\n")
	fmt.Fprintf(&b, "be closed on it. So the %d %% and the %d %% at the two ends of that column are\n", low, high)
	b.WriteString("refusals real clients really got, and no amount of arming faults can raise them.\n\n")
	b.WriteString("A percentage here is of the operations this emulator *serves*, never of the\n")
	b.WriteString("provider's whole API — that comparison is the coverage table in the\n")
	b.WriteString("[README](../README.md#coverage), which counts the upstream surface. And one\n")
	b.WriteString("axis is not reproducible to the operation: `behaviour` attributes a store touch\n")
	b.WriteString("only while a single client request is in flight, so a parallel client loses\n")
	b.WriteString("attribution rather than being guessed about, and the count moves by about one\n")
	b.WriteString("between runs (#398).\n")
	return b.String(), nil
}

// spliceAxes renders the block and reports whether the file would change.
// Absent file, or a file without the markers: nothing to do and no complaint,
// the same terms every other optional block takes, so `feint docs` still works
// for somebody who installed the binary and has no docs/ directory.
func spliceAxes(path string, evidence *evidenceArtefact) (bool, error) {
	current, rendered, err := readAndRenderAxes(path, evidence)
	if err != nil || rendered == "" {
		return false, err
	}
	updated, err := spliceSection(current, axesStartMarker, axesEndMarker, rendered)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return updated != current, nil
}

// writeSplicedAxes writes what spliceAxes reported as changed.
func writeSplicedAxes(path string, evidence *evidenceArtefact) error {
	current, rendered, err := readAndRenderAxes(path, evidence)
	if err != nil || rendered == "" {
		return err
	}
	updated, err := spliceSection(current, axesStartMarker, axesEndMarker, rendered)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return os.WriteFile(path, []byte(updated), 0o644) //nolint:gosec // a reference page is world-readable by design
}

// readAndRenderAxes is the half spliceAxes and writeSplicedAxes share. Written
// twice, the two would answer differently the day one of them learns a new
// reason to skip — which is the defect docs.go's own header describes for the
// numbers this block prints.
func readAndRenderAxes(path string, evidence *evidenceArtefact) (current, rendered string, err error) {
	if path == "" {
		return "", "", nil
	}
	body, err := os.ReadFile(path) //nolint:gosec // operator-supplied path, by design
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", nil
		}
		return "", "", err
	}
	if !strings.Contains(string(body), axesStartMarker) {
		return "", "", nil
	}
	rendered, err = renderAxes(evidence)
	if err != nil {
		return "", "", err
	}
	return string(body), rendered, nil
}

// spread answers the lowest and highest percentage any provider reaches on one
// axis. Two numbers a sentence can quote without a hand typing them, which is
// the difference between a figure that follows the record and one that used to
// be true.
func spread(rows []providerTally, axis string) (low, high int) {
	low, high = -1, -1
	for _, row := range rows {
		for _, a := range row.Axes {
			if a.Axis != axis {
				continue
			}
			if low < 0 || a.Percent < low {
				low = a.Percent
			}
			if a.Percent > high {
				high = a.Percent
			}
		}
	}
	if low < 0 {
		low = 0
	}
	if high < 0 {
		high = 0
	}
	return low, high
}
