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
	// gapUnvalidated: no answer of this operation was ever held to the
	// provider's own API description — the record says `probed: none` or
	// `contract: unchecked`. The contract-driven probe is what earns those two
	// axes and it needs no client at all, so the cause is on the probe's side:
	// its plan, its seeding pool, or the extraction that built the document.
	//
	// It says that and no more. Which of the three it is, the record does not
	// carry, and this queue does not guess — the whole defect it was split out
	// of (#445) was a line claiming a cause the record could not verify.
	//
	// It outranks the recording below because it is the one entry of this queue
	// that needs neither a cloud account nor a client binary: everything it
	// asks for is in this repository. #429 is the measurement — a single fix to
	// the extraction, which had recorded "the document declares an empty answer"
	// and "no schema" the same way, retired 31 Scaleway zeros on `contract` and
	// 29 on `probed` at once, and every one of the thirty-one operations turned
	// out to have been correct all along.
	gapUnvalidated
	// gapUnrecorded: no recorded real-cloud answer covers this operation. It is
	// the whole story of a `shape` zero, and one recording session from earned.
	//
	// It says nothing about clients, and that is deliberate since #445: the
	// recorder reads the cloud directly rather than through an official client
	// (tools/shapes/record.sh), so a sentence about what `exo` or `scw` cannot
	// compose does not explain a missing recording. The committed record agrees
	// — exoscale/v2.get-operation carries an observed shape and no client has
	// ever driven it.
	gapUnrecorded
	// gapUndriven: no real client reaches this operation, no route says why, and
	// the axis is one only a client can earn (axisEarner). So this is the
	// upstream job: a conformance suite, not a pack change.
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
	// It is the largest bucket, and 98 of its 113 zeros are the `negative` axis
	// (measured 2026-08-24, after #445 moved the probe's and the recorder's
	// zeros to the kinds that name them). A tempting reading — measured and rejected on 2026-08-24 — is that
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
	// It exists because the kinds above could not tell "nobody has written
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
	gapViolating:   "violating",
	gapUnvalidated: "unvalidated",
	gapUnrecorded:  "unrecorded",
	gapUndriven:    "undriven",
	gapUnproven:    "unproven",
	gapDeclared:    "declared",
}

// gapKindWork is the sentence the text output prints once per group. It names
// the job, not the state, because the reader of this queue is deciding what to
// pick up.
var gapKindWork = map[gapKind]string{
	gapViolating: "a defect: the operation is served and reached, and the record says it broke this axis",
	gapUnvalidated: "the probe: this axis is earned by an answer held to the provider's own API description, " +
		"which the contract-driven probe produces with no client at all, and the record says none was ever held here",
	gapUnrecorded: "a recording: no answer of the real cloud has been kept for this operation",
	gapUndriven:   "a conformance suite: no real client reaches this operation, and only a client can earn this axis",
	gapUnproven:   "unexplained: driven, not violating, still not earned — the record does not say why",
	gapDeclared:   "not work: no path exists to close this zero, and the route says which — the reason is printed with each line",
}

// classifyGap decides which of the six a zero is, from the record and from what
// the pack declares at the route — never from the operation's name.
//
// It returns the reason it classified on, so that a `declared` line cannot
// print a sentence the classifier did not use. That is not tidiness: the defect
// this function carried until #445 was exactly a printed reason and a branch
// that had nothing to do with each other, and a second copy of the branch
// conditions in the caller is how the two drifted apart in the first place.
//
// The order of the tests is the classification: a violation outranks everything
// because it is the only defect here, and the declarations are asked before the
// catch-all so that the unexplained bucket stays as small as the record allows.
//
// `undriven` is Route.Undriven for this operation, empty when the route
// declares none. Two conditions gate it, and each answers a different question:
//
//   - the record must say no client drove the operation. A reason on a driven
//     operation is a stale excuse, and TestEveryUndrivenOperationSaysWhy is the
//     control that refuses one — this function must not quietly honour what
//     that test exists to reject.
//   - the axis must be one a client alone can earn (axisEarner). "No official
//     client reaches this operation" is a fact about client traffic, and it
//     explains a zero only where client traffic is what earns the axis. Applied
//     to `probed` — which the probe earns with no client whatsoever — it filed
//     doable work as "not work" and printed a sentence about `exo` beside it
//     (#445). The committed record shows the same reason sitting on operations
//     that earned `probed` anyway.
//
// TestAGapIsClassifiedFromTheRecordRatherThanTheName,
// TestADeclaredReasonSplitsTheUndrivenQueue and
// TestAClientShapedReasonNeverExplainsAProbeSideZero fail without this.
func classifyGap(ev emulator.Evidence, axis evidenceAxis, undriven, unearnable string) (gapKind, string) {
	if v := axis.verdict(ev); v == "violating" {
		return gapViolating, ""
	}
	if axis.earner == earnedByAClient && !ev.Driven {
		if undriven != "" {
			return gapDeclared, undriven
		}
		return gapUndriven, ""
	}
	// The route says this axis can never be earned here. Asked before the
	// catch-alls for the same reason the client reason is: the unexplained
	// bucket must stay as small as the declarations allow, and a zero no work
	// can close is not work.
	//
	// It is asked AFTER the branch above on purpose: an operation nothing drives
	// is Undriven's business on the axes a client earns, and letting an axis
	// declaration answer for it would hide a missing suite behind a true
	// statement about a different thing. Unlike Route.Undriven it is keyed by
	// axis, so its subject already matches what it excuses, and it is honoured
	// on every axis a route names.
	if unearnable != "" {
		return gapDeclared, unearnable
	}
	switch axis.earner {
	case earnedByValidation:
		return gapUnvalidated, ""
	case earnedByARecording:
		return gapUnrecorded, ""
	}
	return gapUnproven, ""
}

// gapEntry is one operation of the queue, with the work it names.
type gapEntry struct {
	Operation string `json:"operation"`
	Axis      string `json:"axis"`
	Kind      string `json:"kind"`
	// Verdict carries the record's own word for a non-boolean axis, so a
	// consumer never has to re-derive it from Kind.
	Verdict string `json:"verdict,omitempty"`
	// Reason is the declaration the classifier retired this zero on —
	// Route.Undriven or the Route.Unearnable entry for this axis — carried on
	// the "declared" lines only. Published rather than left for the reader to go
	// and find in a pack file: a decision a report names but does not state is a
	// decision nobody re-examines.
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
				kind, why := classifyGap(ev, a, reasons[op], unearnable[op][a.Name])
				group.Entries = append(group.Entries, gapEntry{
					Operation: op,
					Axis:      a.Name,
					Kind:      gapKindNames[kind],
					Verdict:   a.verdict(ev),
					// The reason the classifier used, never one this loop went
					// and fetched again: the two readings drifting apart is the
					// defect #445 measured, and a single return value is what
					// makes them one reading.
					Reason: why,
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

// kindRank orders the six. Violations first because they are the only defect;
// then the probe, which asks for nothing outside this repository; then the
// recording, which needs a real account; then the suite, which needs a client;
// then what nothing explains, and last what nothing can close.
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
