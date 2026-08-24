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
	// gapUndriven: no real client reaches this operation, and no route says
	// why. Most axes cannot be earned without a client, so this is the upstream
	// job: a conformance suite, not a pack change.
	//
	// It reads zero on a green repository, and that is the design rather than a
	// dead branch: TestEveryUndrivenOperationSaysWhy already refuses an undriven
	// operation with no reason, so this kind is the window between mounting a
	// route and explaining it — an incoming-work signal, not a backlog. The
	// backlog it used to hold is now `declared` below, where each line carries
	// the reason that retired it.
	gapUndriven
	// gapUnproven: driven, not violating, and still not earned, for a reason
	// the record does not spell. Named rather than folded into another kind,
	// because a bucket that absorbs the unexplained is how a queue starts
	// lying.
	//
	// It is the largest bucket, and 172 of its 264 zeros are the `negative`
	// axis. A tempting reading — measured and rejected on 2026-08-24 — is that
	// those are recordings like `shape`, and should be reclassified as such.
	// They are not. `shape` is earned by a recorded real-cloud answer and by
	// nothing else, so "no recording exists" is its whole story; `negative` is
	// earned by an operation really answering 4xx to a real client inside a span
	// where a suite demanded a refusal, which a suite can produce without any
	// recording at all. Calling those 172 "a recording" would have sent a reader
	// to a cloud account for work a conformance case can do offline. Left in the
	// honest bucket rather than moved to a wrong one.
	gapUnproven
	// gapDeclared: the route says why this zero cannot be closed, either
	// because no client reaches the operation (Route.Undriven) or because the
	// axis itself is out of reach there (Route.Unearnable). This is the one
	// entry of the queue that is not work.
	//
	// The second source was added on 2026-08-24, when thirteen Outscale
	// operations at zero on `behaviour` turned out to be a ceiling rather than
	// a backlog: the axis marks an operation whose store touches fall on a
	// resource created and destroyed inside the span, and those thirteen either
	// touch no store or touch only kinds this emulator keeps on purpose. Twelve
	// of the thirteen resisted an attempt to earn them before they were
	// declared, which is the order this kind of entry has to be written in.
	//
	// It exists because the four kinds above could not tell "nobody has written
	// the suite yet" from "no client path exists to write one with", and the
	// queue told both to go and write a suite. Measured on 2026-08-24: all
	// twenty-five operations the record left undriven already carried a reason
	// at their route — `scw ipam ip` has no attach subcommand, `scw vpc` has no
	// subnet subcommand — so every one of those 141 zeros was asking for a
	// suite that cannot be written. That is the same defect #407 removed from
	// the `shape` axis: a queue entry no amount of work can retire.
	//
	// It is last in the order for the same reason it is kept rather than
	// filtered: a reader picking work must not meet it first, and a decision
	// that disappears from the report is a decision nobody can review.
	gapDeclared
)

// gapKindNames are what the output prints, and the keys --format json uses.
// Short, lowercase, stable: they are a vocabulary consumers will match on.
var gapKindNames = map[gapKind]string{
	gapViolating:  "violating",
	gapUnrecorded: "unrecorded",
	gapUndriven:   "undriven",
	gapUnproven:   "unproven",
	gapDeclared:   "declared",
}

// gapKindWork is the sentence the text output prints once per group. It names
// the job, not the state, because the reader of this queue is deciding what to
// pick up.
var gapKindWork = map[gapKind]string{
	gapViolating:  "a defect: the operation is served and reached, and the record says it broke this axis",
	gapUnrecorded: "a recording: a real client already drives it, and no answer of the real cloud has been kept",
	gapUndriven:   "a conformance suite: no real client reaches this operation, so most axes cannot be earned",
	gapUnproven:   "unexplained: driven, not violating, still not earned — the record does not say why",
	gapDeclared:   "not work: no path exists to close this zero, and the route says which — the reason is printed with each line",
}

// classifyGap decides which of the five a zero is, from the record and from
// what the pack declares at the route — never from the operation's name.
//
// The order of the tests is the classification: a violation outranks everything
// because it is the only defect here, and "undriven" is asked before the
// catch-all so that the unexplained bucket stays as small as the record allows.
//
// `reason` is Route.Undriven for this operation, empty when the route declares
// none. It is only ever consulted for an operation the record says no client
// drove: a reason on a driven operation is a stale excuse, and
// TestEveryUndrivenOperationSaysWhy is the control that refuses one — this
// function must not quietly honour what that test exists to reject.
//
// TestAGapIsClassifiedFromTheRecordRatherThanTheName and
// TestADeclaredReasonSplitsTheUndrivenQueue fail without this.
func classifyGap(ev emulator.Evidence, axis evidenceAxis, undriven, unearnable string) gapKind {
	if v := axis.verdict(ev); v == "violating" {
		return gapViolating
	}
	if !ev.Driven {
		if undriven != "" {
			return gapDeclared
		}
		return gapUndriven
	}
	// Driven, and the route says this axis can never be earned here. Asked
	// before the catch-all for the same reason "undriven" is: the unexplained
	// bucket must stay as small as the declarations allow, and a zero no work
	// can close is not work.
	//
	// It is asked AFTER the driven test on purpose: an operation nothing drives
	// is Undriven's business, and letting an axis declaration answer for it
	// would hide a missing suite behind a true statement about a different
	// thing.
	if unearnable != "" {
		return gapDeclared
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
	// Reason is Route.Undriven, carried on the "declared" lines only. Published
	// rather than left for the reader to go and find in a pack file: a decision
	// a report names but does not state is a decision nobody re-examines.
	Reason string `json:"reason,omitempty"`
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
	reasons map[string]string, unearnable map[string]map[string]string,
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
				unearned := unearnable[op][a.Name]
				kind := classifyGap(ev, a, reasons[op], unearned)
				entry := gapEntry{
					Operation: op,
					Axis:      a.Name,
					Kind:      gapKindNames[kind],
					Verdict:   a.verdict(ev),
				}
				if kind == gapDeclared {
					entry.Reason = reasons[op]
					if unearned != "" && ev.Driven {
						entry.Reason = unearned
					}
				}
				group.Entries = append(group.Entries, entry)
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
			if e.Reason != "" {
				line += "\n          " + e.Reason
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
	unearnable, err := unearnableReasons()
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	reasons, err := undrivenReasons()
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	report, err := buildGaps(record, art, owners, reasons, unearnable, providers, provider, axis)
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
