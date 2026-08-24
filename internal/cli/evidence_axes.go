package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// The evidence record, counted per provider — the answer to "which cloud is
// weakest, and on which axis" as a command rather than as a script somebody
// writes again each time.
//
// Why it is here and not in a throwaway script (#402). The question was answered
// once with one, and that script was wrong twice before it was right: it first
// looked for a key named "operation" inside each entry, when the operation name
// is the map *key*, so every one of the 370 rows fell into the same bucket and it
// printed "scaleway: 370 served, 93 % driven" — a table with the right shape,
// the right column headers, plausible numbers, and no relation to the record. A
// reader had no way to doubt it. That is this repository's own recurring defect
// (CLAUDE.md, "un commentaire n'est pas un contrôle") applied to the number the
// project is steered by, so the reader of the record is code, with a test.
//
// Two decisions make it hard to repeat that mistake:
//
//  1. **A provider is never inferred from an operation's name.** `osc/…` looks
//     like Outscale and `instance/v1/…` like Scaleway, and a prefix table would
//     be right today and silently wrong the day a fourth pack lands or a pack
//     renames a product. The owner of an operation is the pack that mounts a
//     route declaring it, asked in process — the same authority docs/routes.md
//     is written by.
//  2. **A record naming an operation no pack mounts is refused**, never counted
//     under some default. Silently dropping it, or filing it under the first
//     provider, is exactly how the throwaway script produced its plausible
//     table. `TestEveryMountedOperationHasAnEvidenceRow` already holds the other
//     direction.
//
// The counts are cross-checked against a genuinely independent source in
// evidence_axes_test.go: coverage/<provider>-coverage.json, written by the drift
// scan, lists each provider's implemented operations. Two paths from the packs to
// a per-provider set, and the test fails when they disagree.

// evidenceAxis is one of the seven independent axes of the record.
//
// Three of them are not booleans, which is the first thing a hand-written reader
// of this artefact gets wrong: `probed` is a verdict ("response", "refusal",
// "none"), `contract` is a verdict ("clean", "violating", "unchecked") and
// `shape` is a state ("observed", "unobserved", "unknown"). Counting `probed` by
// truthiness would count "none" as earned. So each axis carries its own
// predicate rather than a field name, and the one-line meaning the documentation
// prints lives beside it — one place, so the table and the prose cannot drift.
type evidenceAxis struct {
	// Name is the key the record uses and the value --axis takes.
	Name string
	// Meaning is the single line docs/routes.md prints for this axis. The long
	// form is evidenceLegend, in docs_routes.go; this is the row of the summary
	// table, and a reader who skips the legend must still not misread a score.
	Meaning string
	// earned answers whether one operation has earned this axis.
	earned func(emulator.Evidence) bool
	// verdict is what the record actually holds here, for an axis that is not a
	// boolean, and "" for one that is. A work queue that prints it separates an
	// operation nothing checked ("unchecked") from one that violated its own
	// contract ("violating"), which are the same "not earned" and very
	// different work.
	verdict func(emulator.Evidence) string
}

// evidenceAxisList is the seven axes in the order emulator.Evidence declares
// them. The order is fixed for diff stability and is not a ranking — the record
// publishes independent axes and never adds them into a score, which is the
// whole design of #123.
func evidenceAxisList() []evidenceAxis {
	noVerdict := func(emulator.Evidence) string { return "" }
	return []evidenceAxis{
		{
			Name:    "driven",
			Meaning: "a real client reached it in the recorded run; it proves what the suite asserted, nothing more",
			earned:  func(e emulator.Evidence) bool { return e.Driven },
			verdict: noVerdict,
		},
		{
			Name:    "probed",
			Meaning: "the contract-driven probe validated an answer here against the operation's own schema, a success (`response`) or a refusal (`refusal`)",
			// Named positively, and that is the point. Written as `!= ProbeNone`
			// it counted a record carrying no verdict at all — an empty string,
			// a key encoding/json never found — as a success, which is the very
			// defect #406 measured in the throwaway script that read a verdict
			// as a boolean. The other two verdict axes were already positive.
			// TestAnAxisWithNoVerdictIsNotEarned fails without it.
			earned: func(e emulator.Evidence) bool {
				return e.Probed == emulator.ProbeResponse || e.Probed == emulator.ProbeRefusal
			},
			verdict: func(e emulator.Evidence) string { return e.Probed },
		},
		{
			Name:    "contract",
			Meaning: "at least one answer was validated against the provider's own API description and none violated it (`unchecked`: none was ever validated, which is not the same as no violation)",
			earned:  func(e emulator.Evidence) bool { return e.Contract == emulator.ContractClean },
			verdict: func(e emulator.Evidence) string { return e.Contract },
		},
		{
			Name:    "dataplane",
			Meaning: "it was driven in a run whose machines were real, so the control plane ran with its side effects on",
			earned:  func(e emulator.Evidence) bool { return e.Dataplane },
			verdict: noVerdict,
		},
		{
			Name:    "shape",
			Meaning: "a recorded real-cloud answer covers it, from `shapes/` — which `mise run shapes:fold` also fills from the committed corpora; it says the answer has been held against a real one, not that the offline gate re-issues the call",
			earned:  func(e emulator.Evidence) bool { return e.Shape == emulator.ShapeObserved },
			verdict: func(e emulator.Evidence) string { return e.Shape },
		},
		{
			Name:    "behaviour",
			Meaning: "it took part in a create-to-destroy sequence the emulator's own store observed, inside a span a suite declared",
			earned:  func(e emulator.Evidence) bool { return e.Behaviour },
			verdict: noVerdict,
		},
		{
			Name:    "negative",
			Meaning: "it really answered 4xx to a real client inside a span where a suite demanded a refusal; **an injected fault never earns it**, so arming faults cannot move this number",
			earned:  func(e emulator.Evidence) bool { return e.Negative },
			verdict: noVerdict,
		},
	}
}

// evidenceAxisNames lists the axis names, for an error message that cannot go
// stale by naming them in its own words.
func evidenceAxisNames() []string {
	axes := evidenceAxisList()
	names := make([]string, 0, len(axes))
	for _, a := range axes {
		names = append(names, a.Name)
	}
	return names
}

// axisTally is one axis counted over one population.
type axisTally struct {
	Axis string `json:"axis"`
	// Earned is how many operations of the population have earned the axis.
	Earned int `json:"earned"`
	// Missing is the rest — the size of the work queue --axis prints.
	Missing int `json:"missing"`
	// Percent is Earned out of the population, rounded to nearest.
	Percent int `json:"percent"`
}

// queueEntry is one operation at zero on the named axis.
type queueEntry struct {
	Operation string `json:"operation"`
	// Verdict is what the record holds for a non-boolean axis, absent for a
	// boolean one: "unchecked" and "violating" are both not-earned and are not
	// the same work.
	Verdict string `json:"verdict,omitempty"`
}

// providerTally is one provider's row of the table.
type providerTally struct {
	Provider string      `json:"provider"`
	Served   int         `json:"served"`
	Axes     []axisTally `json:"axes"`
	// Queue is filled only when an axis was named, and holds that axis's
	// operations at zero, sorted.
	Queue []queueEntry `json:"queue,omitempty"`
}

// evidenceTable is the whole answer, in the shape --format json publishes.
type evidenceTable struct {
	// Record is the path the numbers were read from, so a pasted output says
	// what it measured.
	Record string `json:"record"`
	// Machines is the record's own list of the runtimes its runs were backed
	// by. The dataplane axis only means something next to it.
	Machines []string `json:"machines"`
	// Axis is the axis --axis named, empty otherwise.
	Axis string `json:"axis,omitempty"`
	// Providers is one row per pack, sorted by name.
	Providers []providerTally `json:"providers"`
	// Total is every provider together, under the name "all". It is a row of
	// the same table rather than a separate shape, because a reader compares it
	// with the rows above it.
	Total providerTally `json:"total"`
}

// percentOf renders n out of total as a whole percentage, rounded to nearest
// rather than truncated.
//
// Not a detail: 166 of 173 is 95.95 %, which truncates to 95 and rounds to 96,
// and a reader comparing this table with the one in #402 would have found three
// of its twelve cells "wrong" for that reason alone. Integer arithmetic, so the
// value cannot depend on a float's last bit.
func percentOf(n, total int) int {
	if total == 0 {
		return 0
	}
	return (200*n + total) / (2 * total)
}

// operationOwners answers which pack mounts each operation, and in what order
// the packs are mounted.
//
// From the packs in process, never from the operation's name. A name-prefix
// table would be a provider list written in this file — the kind of line
// CLAUDE.md's rule 5 refuses in the core and that is no better here, because it
// is right until a pack renames a product and then wrong without saying so.
//
// Two routes may declare one operation (the packs mount 372 routes for 370
// operations), which is why the map is keyed by operation and the count of
// served operations is not the count of routes.
func operationOwners() (map[string]string, []string, error) {
	srv, _, err := newServer(nil)
	if err != nil {
		return nil, nil, err
	}
	owners := make(map[string]string)
	providers := make([]string, 0, len(srv.Packs()))
	for _, p := range srv.Packs() {
		providers = append(providers, p.Name())
		for _, r := range p.Routes() {
			if r.Operation == "" {
				continue
			}
			if prev, taken := owners[r.Operation]; taken && prev != p.Name() {
				// NewServer already refuses two packs claiming one route, but
				// nothing refuses two packs claiming one operation, and this
				// reader would then answer differently depending on mount
				// order. Refusing beats picking one.
				return nil, nil, fmt.Errorf("operation %s is claimed by both %s and %s, "+
					"so it cannot be counted under either", r.Operation, prev, p.Name())
			}
			owners[r.Operation] = p.Name()
		}
	}
	sort.Strings(providers)
	return owners, providers, nil
}

// tallyEvidence counts the record per provider.
//
// axis may be "" (no queue) or one of evidenceAxisList's names; provider may be
// "" (every pack) or one pack's name. An operation the packs do not claim is an
// error rather than a row filed somewhere plausible.
func tallyEvidence(record string, art *evidenceArtefact, owners map[string]string, providers []string,
	provider, axis string) (*evidenceTable, error) {
	axes := evidenceAxisList()
	var named *evidenceAxis
	if axis != "" {
		for i := range axes {
			if axes[i].Name == axis {
				named = &axes[i]
				break
			}
		}
		if named == nil {
			return nil, fmt.Errorf("no axis named %q in the record; it publishes %s",
				axis, strings.Join(evidenceAxisNames(), ", "))
		}
	}
	if provider != "" {
		known := false
		for _, p := range providers {
			if p == provider {
				known = true
				break
			}
		}
		if !known {
			return nil, fmt.Errorf("no pack named %q is mounted; this binary mounts %s",
				provider, strings.Join(providers, ", "))
		}
	}

	// Split first, so an operation nobody claims is named before anything is
	// counted. Counting it under a default is the failure this command exists
	// to stop being possible.
	byProvider := make(map[string][]string, len(providers))
	var unclaimed []string
	for name := range art.Operations {
		owner, known := owners[name]
		if !known {
			unclaimed = append(unclaimed, name)
			continue
		}
		byProvider[owner] = append(byProvider[owner], name)
	}
	if len(unclaimed) > 0 {
		sort.Strings(unclaimed)
		return nil, fmt.Errorf("%s names %d operation(s) no mounted pack serves, so they belong to no "+
			"provider and this table would be missing them: %s\nthe record follows the code; "+
			"regenerate it: mise run evidence:update",
			record, len(unclaimed), strings.Join(unclaimed, ", "))
	}

	table := &evidenceTable{Record: record, Machines: art.Machines, Axis: axis}
	var everything []string
	for _, p := range providers {
		everything = append(everything, byProvider[p]...)
		if provider != "" && p != provider {
			continue
		}
		table.Providers = append(table.Providers, tallyOne(p, byProvider[p], art, axes, named))
	}
	// "all" is every pack even when --provider narrowed the rows above, because
	// the question this view answers is comparative: a row with nothing to be
	// weaker than is not an answer to "which cloud is weakest". It carries no
	// queue — a work queue belongs to the pack that would clear it.
	table.Total = tallyOne("all", everything, art, axes, nil)
	return table, nil
}

// tallyOne counts one population. named, when set, fills the queue.
func tallyOne(name string, operations []string, art *evidenceArtefact,
	axes []evidenceAxis, named *evidenceAxis) providerTally {
	row := providerTally{Provider: name, Served: len(operations)}
	for _, a := range axes {
		earned := 0
		for _, op := range operations {
			if a.earned(art.Operations[op]) {
				earned++
			}
		}
		row.Axes = append(row.Axes, axisTally{
			Axis:    a.Name,
			Earned:  earned,
			Missing: len(operations) - earned,
			Percent: percentOf(earned, len(operations)),
		})
	}
	if named != nil {
		for _, op := range operations {
			ev := art.Operations[op]
			if named.earned(ev) {
				continue
			}
			row.Queue = append(row.Queue, queueEntry{Operation: op, Verdict: named.verdict(ev)})
		}
		sort.Slice(row.Queue, func(i, j int) bool { return row.Queue[i].Operation < row.Queue[j].Operation })
	}
	return row
}

// writeEvidenceText renders the table for a terminal, and the queue under it
// when an axis was named.
func writeEvidenceText(w io.Writer, t *evidenceTable) error {
	machines := "none declared"
	if len(t.Machines) > 0 {
		machines = strings.Join(t.Machines, ", ")
	}
	if _, err := fmt.Fprintf(w, "%d operations served, from %s (machines: %s)\n\n",
		t.Total.Served, t.Record, machines); err != nil {
		return err
	}
	// Percentages of populations this size move by a whole point for one
	// operation, and the axes are never summed into a score: both are
	// properties of the record rather than of this renderer, and a reader who
	// pastes this table elsewhere must carry them with it.

	if _, err := fmt.Fprintf(w, "%-10s %6s", "provider", "served"); err != nil {
		return err
	}
	for _, a := range t.Total.Axes {
		if _, err := fmt.Fprintf(w, " %9s", a.Axis); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}

	rows := append(append([]providerTally(nil), t.Providers...), t.Total)
	for _, row := range rows {
		if _, err := fmt.Fprintf(w, "%-10s %6d", row.Provider, row.Served); err != nil {
			return err
		}
		for _, a := range row.Axes {
			if _, err := fmt.Fprintf(w, " %4d %3d%%", a.Earned, a.Percent); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}

	if t.Axis == "" {
		// The score alone is not a queue, and saying so is what stops this
		// output being read as a verdict. Name the flag that turns it into one.
		_, err := fmt.Fprintf(w, "\nNaming an axis lists the operations at zero on it: --axis %s\n",
			strings.Join(evidenceAxisNames(), "|"))
		return err
	}

	for _, row := range t.Providers {
		if _, err := fmt.Fprintf(w, "\n%s: %s at zero on %s, of %d served\n",
			row.Provider, countNoun(len(row.Queue), "operation"), t.Axis, row.Served); err != nil {
			return err
		}
		for _, q := range row.Queue {
			line := "  " + q.Operation
			if q.Verdict != "" {
				line += "  (" + q.Verdict + ")"
			}
			if _, err := fmt.Fprintln(w, line); err != nil {
				return err
			}
		}
	}
	return nil
}

// evidenceAxesView is what `feint coverage --evidence` runs. Offline: it reads
// the committed artefact and mounts the packs in process, and talks to nothing.
func evidenceAxesView(record, provider, axis, format string, stdout, stderr io.Writer) int {
	art, err := readEvidence(record)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	if art == nil || len(art.Operations) == 0 {
		// Zero rows would render a table of zeroes, which reads as "nothing is
		// proven" rather than "nothing was measured" — the distinction
		// renderConfidence already refuses to blur.
		fmt.Fprintf(stderr, "feint: %s holds no operation, so there is nothing to count; "+
			"`mise run evidence:update` writes it\n", record)
		return exitError
	}
	owners, providers, err := operationOwners()
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	table, err := tallyEvidence(record, art, owners, providers, provider, axis)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	switch format {
	case "json":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		err = enc.Encode(table)
	case "text":
		err = writeEvidenceText(stdout, table)
	default:
		fmt.Fprintf(stderr, "feint: --evidence renders text or json, not %q\n", format)
		return exitError
	}
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	return exitOK
}

// undrivenReasons answers, per operation, what the pack declares at the route
// about why no official client reaches it — Route.Undriven, empty for every
// operation a client drives.
//
// From the packs in process, for the same reason operationOwners is: the
// alternative is a list in this file, and a list disagrees with the packs at
// the first edit and keeps being the thing people read.
//
// Two routes may mount one operation (372 routes for 370 operations). A reason
// on either is the operation's reason, and the longer one wins so that the
// answer cannot depend on mount order — a silent tie-break by iteration order
// is how a report becomes irreproducible.
// unearnableReasons answers, per operation and per axis, what the pack declares
// at the route about an axis that can never be earned there — Route.Unearnable,
// empty for every operation whose axes are all still in play.
//
// Separate from undrivenReasons because it answers a different question, and
// keying it by axis is what keeps the exemption's subject matched to its key: a
// declaration is written against one axis, and carrying it on the others would
// excuse zeros nobody examined.
//
// From the packs in process, for the same reason the two neighbours are.
func unearnableReasons() (map[string]map[string]string, error) {
	srv, _, err := newServer(nil)
	if err != nil {
		return nil, err
	}
	reasons := make(map[string]map[string]string)
	for _, r := range srv.AllRoutes() {
		if r.Operation == "" {
			continue
		}
		for _, u := range r.Unearnable {
			byAxis := reasons[r.Operation]
			if byAxis == nil {
				byAxis = map[string]string{}
				reasons[r.Operation] = byAxis
			}
			// Two routes may mount one operation; the longer reason wins, so
			// the answer cannot depend on mount order.
			if prev, seen := byAxis[u.Axis]; seen && len(prev) >= len(u.Reason) {
				continue
			}
			byAxis[u.Axis] = u.Reason
		}
	}
	return reasons, nil
}

func undrivenReasons() (map[string]string, error) {
	srv, _, err := newServer(nil)
	if err != nil {
		return nil, err
	}
	reasons := make(map[string]string)
	for _, r := range srv.AllRoutes() {
		if r.Operation == "" || r.Undriven == "" {
			continue
		}
		if prev, seen := reasons[r.Operation]; seen && len(prev) >= len(r.Undriven) {
			continue
		}
		reasons[r.Operation] = r.Undriven
	}
	return reasons, nil
}
