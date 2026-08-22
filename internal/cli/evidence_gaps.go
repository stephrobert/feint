package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// The work a zero names, and the whole reason this file exists beside
// evidence_axes.go.
//
// #402 answers "where do we stand": seven axes, per provider, from the record.
// A score does not say what to do next, and steering needs the second half —
// which is #408. Reading a list of 158 operation names in artefact order is
// data, not a queue.
//
// **A zero does not mean one thing.** An operation missing `shape` because no
// client has ever driven it is a conformance-suite job; the same zero on an
// operation a client drives every run is a recording session against a real
// account; and an axis whose record says the operation *violated* its own
// contract is a defect in the pack. Three zeros, three different people. A
// queue that merges them sends all three to read the same list.
//
// The categories are derived from the record itself — never from a name, a
// guess, or a hand-kept list — so a category cannot drift from what was
// measured.
type gapKind int

const (
	// gapViolating: the record holds a verdict saying the operation broke the
	// axis rather than never reaching it. It is served, it is reached, and it
	// is wrong: nothing else in this queue is a defect.
	gapViolating gapKind = iota
	// gapUnrecorded: the operation is driven by a real client, and the axis is
	// still missing. For `shape` that is exactly "no recording of the real
	// cloud's answer exists" — one recording session away from earned, which
	// is why it outranks the two below.
	gapUnrecorded
	// gapUndriven: no real client reaches this operation at all. Most axes
	// cannot be earned without that, so this is the upstream job: a conformance
	// suite, not a pack change.
	gapUndriven
	// gapUnproven: driven, not violating, and still not earned, for a reason
	// the record does not spell. Named rather than folded into another kind,
	// because a bucket that absorbs the unexplained is how a queue starts
	// lying.
	gapUnproven
)

// gapKindNames are what the output prints, and the keys --format json uses.
// Short, lowercase, stable: they are a vocabulary consumers will match on.
var gapKindNames = map[gapKind]string{
	gapViolating:  "violating",
	gapUnrecorded: "unrecorded",
	gapUndriven:   "undriven",
	gapUnproven:   "unproven",
}

// gapKindWork is the sentence the text output prints once per group. It names
// the job, not the state, because the reader of this queue is deciding what to
// pick up.
var gapKindWork = map[gapKind]string{
	gapViolating:  "a defect: the operation is served and reached, and the record says it broke this axis",
	gapUnrecorded: "a recording: a real client already drives it, and no answer of the real cloud has been kept",
	gapUndriven:   "a conformance suite: no real client reaches this operation, so most axes cannot be earned",
	gapUnproven:   "unexplained: driven, not violating, still not earned — the record does not say why",
}

// classifyGap decides which of the four a zero is, from the record alone.
//
// The order of the tests is the classification: a violation outranks everything
// because it is the only defect here, and "undriven" is asked before the
// catch-all so that the unexplained bucket stays as small as the record allows.
//
// TestAGapIsClassifiedFromTheRecordRatherThanTheName fails without this.
func classifyGap(ev emulator.Evidence, axis evidenceAxis) gapKind {
	if v := axis.verdict(ev); v == "violating" {
		return gapViolating
	}
	if !ev.Driven {
		return gapUndriven
	}
	if axis.Name == "shape" {
		return gapUnrecorded
	}
	return gapUnproven
}

// gapEntry is one operation of the queue, with the work it names.
type gapEntry struct {
	Operation string `json:"operation"`
	Axis      string `json:"axis"`
	Kind      string `json:"kind"`
	// Verdict carries the record's own word for a non-boolean axis, so a
	// consumer never has to re-derive it from Kind.
	Verdict string `json:"verdict,omitempty"`
}

// gapGroup is one (provider, axis) pair's queue.
type gapGroup struct {
	Provider string     `json:"provider"`
	Axis     string     `json:"axis"`
	Missing  int        `json:"missing"`
	Entries  []gapEntry `json:"entries"`
}

// gapReport is what --gaps publishes.
type gapReport struct {
	Record string     `json:"record"`
	Groups []gapGroup `json:"groups"`
	// Kinds is the vocabulary, published beside the data so a consumer reading
	// only the JSON knows what a kind means without reading this file.
	Kinds map[string]string `json:"kinds"`
}

// buildGaps walks every provider and every axis and collects what is at zero.
//
// It deliberately re-reads the same record and the same ownership map the score
// uses. No second source of truth: this computes nothing a gate does not
// already compute, it only says what the zeros are for.
func buildGaps(record string, art *evidenceArtefact, owners map[string]string,
	providers []string, onlyProvider, onlyAxis string) (*gapReport, error) {
	axes := evidenceAxisList()
	if onlyAxis != "" {
		found := false
		for _, a := range axes {
			if a.Name == onlyAxis {
				axes, found = []evidenceAxis{a}, true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("no axis named %q; the record holds %s",
				onlyAxis, strings.Join(evidenceAxisNames(), ", "))
		}
	}

	byProvider := map[string][]string{}
	for op := range art.Operations {
		owner, ok := owners[op]
		if !ok {
			// An operation no pack claims cannot be attributed to a cloud, and
			// #402's own guard already refuses the record in that state. Skipped
			// rather than bucketed under a provider it does not belong to.
			continue
		}
		byProvider[owner] = append(byProvider[owner], op)
	}

	report := &gapReport{Record: record, Kinds: map[string]string{}}
	for k, name := range gapKindNames {
		report.Kinds[name] = gapKindWork[k]
	}

	for _, p := range providers {
		if onlyProvider != "" && p != onlyProvider {
			continue
		}
		ops := byProvider[p]
		sort.Strings(ops)
		for _, a := range axes {
			group := gapGroup{Provider: p, Axis: a.Name}
			for _, op := range ops {
				ev := art.Operations[op]
				if a.earned(ev) {
					continue
				}
				kind := classifyGap(ev, a)
				group.Entries = append(group.Entries, gapEntry{
					Operation: op,
					Axis:      a.Name,
					Kind:      gapKindNames[kind],
					Verdict:   a.verdict(ev),
				})
			}
			if len(group.Entries) == 0 {
				continue
			}
			group.Missing = len(group.Entries)
			// Sorted by the work it names, then by operation. Not a score:
			// the order is the fixed one declared by gapKind, printed beside
			// every group, so a reader can disagree with it knowingly.
			sort.SliceStable(group.Entries, func(i, j int) bool {
				ki, kj := kindRank(group.Entries[i].Kind), kindRank(group.Entries[j].Kind)
				if ki != kj {
					return ki < kj
				}
				return group.Entries[i].Operation < group.Entries[j].Operation
			})
			report.Groups = append(report.Groups, group)
		}
	}
	return report, nil
}

// kindRank orders the four. Violations first because they are the only defect;
// then the recording, which is one session from earned; then the suite, which
// is upstream of most axes; then what nothing explains.
func kindRank(name string) int {
	for k, n := range gapKindNames {
		if n == name {
			return int(k)
		}
	}
	return len(gapKindNames)
}

// writeGapsText renders the queue for a terminal.
func writeGapsText(w io.Writer, r *gapReport) error {
	if len(r.Groups) == 0 {
		// Said out loud rather than printed as an empty page: "nothing is
		// missing" and "nothing was examined" must never look alike, which is
		// the rule corpus --check already states for a replay of nothing.
		_, err := fmt.Fprintf(w, "no gap on any axis of any provider, from %s\n", r.Record)
		return err
	}
	if _, err := fmt.Fprintf(w, "what is missing, from %s\n", r.Record); err != nil {
		return err
	}
	for _, g := range r.Groups {
		if _, err := fmt.Fprintf(w, "\n%s / %s — %d operation(s) at zero\n",
			g.Provider, g.Axis, g.Missing); err != nil {
			return err
		}
		lastKind := ""
		for _, e := range g.Entries {
			if e.Kind != lastKind {
				if _, err := fmt.Fprintf(w, "  %s — %s\n", e.Kind, r.Kinds[e.Kind]); err != nil {
					return err
				}
				lastKind = e.Kind
			}
			line := "      " + e.Operation
			if e.Verdict != "" {
				line += "  (" + e.Verdict + ")"
			}
			if _, err := fmt.Fprintln(w, line); err != nil {
				return err
			}
		}
	}
	return nil
}

// evidenceGapsView is what `feint coverage --evidence <record> --gaps` runs.
//
// Offline, from the committed record, like the score beside it. It shares
// readEvidence and operationOwners with #402 rather than re-deriving either:
// the day the record's shape changes, both halves break together and loudly,
// which is the property `internal/cli/docs.go` records having learned the hard
// way ("one shape, one owner").
func evidenceGapsView(record, provider, axis, format string, stdout, stderr io.Writer) int {
	art, err := readEvidence(record)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	if art == nil || len(art.Operations) == 0 {
		fmt.Fprintf(stderr, "feint: %s holds no operation, so there is nothing to queue; "+
			"`mise run evidence:update` writes it\n", record)
		return exitError
	}
	owners, providers, err := operationOwners()
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	report, err := buildGaps(record, art, owners, providers, provider, axis)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	switch format {
	case "json":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		err = enc.Encode(report)
	case "text":
		err = writeGapsText(stdout, report)
	default:
		fmt.Fprintf(stderr, "feint: --gaps renders text or json, not %q\n", format)
		return exitError
	}
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	return exitOK
}
