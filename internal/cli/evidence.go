package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// The per-operation evidence record (#123): what is proven about an operation
// is a set of named, independent axes, published — never a level on a ladder,
// never a number. This file is the half that lives outside the emulator
// process: joining the observed shapes onto mounted operations (the core must
// not know providers or where recordings live), and turning a run's live
// /_feint/conformance view into the versioned artefact the documentation
// renders.
//
// The artefact is a snapshot of stated runs, never an accumulation. Merging a
// fresh run with a stale artefact would let a level survive the removal of
// its evidence, which is the one property this record must not have: the
// /falsify criterion is that deleting a conformance assertion demotes the
// operations it proved at the next regeneration. So --join only exists for
// the two legs of one `mise run evidence:update`, and the artefact carries
// the runtimes its runs were backed by, so a reader knows the conditions.

// evidenceFormat and evidenceVersion follow the snapshot doctrine (#133): a
// file that names what it is can be refused when it is something else.
//
// Version 2: the probed axis became a verdict ("response", "refusal", "none")
// where version 1 published an arrival as a boolean — the overclaim #156
// measured. A version-1 artefact is refused rather than reinterpreted, because
// its probed: true is precisely the value this version exists not to carry.
const (
	evidenceFormat  = "feint-evidence"
	evidenceVersion = 3
)

// evidenceArtefact is coverage/evidence.json.
type evidenceArtefact struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	// Machines lists the runtime behind each contributing run, sorted. The
	// dataplane axis only means something next to this list.
	Machines []string `json:"machines"`
	// GeneratedFrom digests what this record was produced from (#171). It is
	// not decoration: --join refuses an artefact whose inputs differ, so a
	// suite that loses an assertion makes the previous record unjoinable and
	// the level it carried cannot survive. See provenance.go.
	GeneratedFrom provenance `json:"generated_from"`
	// Operations maps every mounted operation to its evidence.
	Operations map[string]emulator.Evidence `json:"operations"`
}

// runtimesLost reads the record already at path and answers the runtimes it was
// earned under that this one does not reach.
//
// A file that is not there loses nothing: the first write of an artefact is not
// a narrowing. A file that *is* there and cannot be read is a different answer
// and now says so, because the two were the same for as long as this guard
// existed and that is how a guard stops working in silence: bumping
// evidenceVersion makes the committed record unreadable to the new binary, so
// "unreadable means nothing lost" would disarm the check on the one
// regeneration where it matters most. Found while bumping it, for #171.
//
// "none" is not a runtime. A record earned with machines off and rewritten with
// machines off narrows nothing, and one that gains a runtime is a widening.
func runtimesLost(path string, next *evidenceArtefact) ([]string, error) {
	existing, err := readEvidence(path)
	switch {
	case os.IsNotExist(err):
		return nil, nil
	case err != nil:
		return nil, err
	case existing == nil:
		return nil, nil
	}
	reached := map[string]bool{}
	for _, m := range next.Machines {
		reached[m] = true
	}
	var lost []string
	for _, m := range existing.Machines {
		if m == "none" || reached[m] {
			continue
		}
		lost = append(lost, m)
	}
	sort.Strings(lost)
	return lost, nil
}

// axesLost answers, per axis, the operations the record already at path had
// earned and this one does not.
//
// It is a report and not a refusal, and the distinction is the one runtimesLost
// already draws for a different reason: an axis may legitimately shrink when a
// claim is corrected — #156 took `probed` from 181 arrivals down to 83 verdicts
// — and a suite that loses an assertion *must* demote what it proved, which is
// the falsification this record lives under. Refusing a narrowing here would
// break that.
//
// What it must not do is happen in silence. #398 measured why: two identical
// conformance runs marked 311 operations each on `behaviour` and disagreed on
// six of them, so a regeneration could demote an operation whose assertion was
// still in the suite and still passing, and nothing said a word. The attribution
// is deterministic now, so a loss printed here names real work — an assertion
// that went, or a suite that did not run — rather than a scheduling accident.
//
// A record that cannot be read loses nothing it can name: the caller has already
// been told, by runtimesLost, that the comparison could not be made.
// TestARegenerationNamesTheOperationsItDemotes fails without this.
func axesLost(path string, next *evidenceArtefact) map[string][]string {
	existing, err := readEvidence(path)
	if err != nil || existing == nil {
		return nil
	}
	lost := map[string][]string{}
	for _, axis := range evidenceAxisList() {
		for op, was := range existing.Operations {
			if !axis.earned(was) {
				continue
			}
			now, still := next.Operations[op]
			if still && axis.earned(now) {
				continue
			}
			lost[axis.Name] = append(lost[axis.Name], op)
		}
	}
	for _, ops := range lost {
		sort.Strings(ops)
	}
	return lost
}

// reportAxesLost prints what a write demotes, axis by axis, or nothing at all
// when it demotes nothing.
func reportAxesLost(w io.Writer, path string, next *evidenceArtefact) {
	lost := axesLost(path, next)
	if len(lost) == 0 {
		return
	}
	for _, axis := range evidenceAxisList() {
		ops := lost[axis.Name]
		if len(ops) == 0 {
			continue
		}
		fmt.Fprintf(w, "feint: this run no longer reaches %d operation(s) that %s had earned on `%s`:\n",
			len(ops), path, axis.Name)
		for _, op := range ops {
			fmt.Fprintf(w, "feint:   %s\n", op)
		}
	}
	fmt.Fprintf(w, "feint: a record may legitimately shrink when a claim is corrected or an "+
		"assertion is removed, and this is not a failure — it is the demotion said out loud (#398).\n")
}

func evidence(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("evidence")
	endpoint := fs.String("endpoint", "http://"+DefaultAddr, "the running emulator to read /_feint/conformance from")
	shapesDir := fs.String("shapes", "shapes", "directory of observed real-cloud shapes; the shape axis is resolved from it (empty to leave the run's answer)")
	out := fs.String("out", filepath.Join("coverage", "evidence.json"), "where to write the artefact")
	contractsDir := fs.String("contracts", "contracts", "directory of API descriptions, digested into the record's provenance")
	suitesDir := fs.String("suites", filepath.Join("tools", "conformance"), "directory of conformance suites, digested into the record's provenance")
	join := fs.String("join", "", "an artefact from another leg of the same regeneration, merged in (fresh runs only; see the header comment)")
	allowNarrowing := fs.Bool("allow-narrowing", false,
		"write even when this run reaches fewer runtimes than the record it replaces; "+
			"for the single-leg caller of evidence:update, never for a committed artefact")
	reshape := fs.Bool("reshape", false,
		"recompute only the shape axis of an existing record from --shapes, offline, without a run")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if *reshape {
		return reshapeEvidence(*out, *shapesDir, *contractsDir, *suitesDir, stdout, stderr)
	}

	var view emulator.ConformanceView
	if err := getJSON(*endpoint+"/_feint/conformance", statusTimeout, &view); err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	if len(view.Evidence) == 0 {
		fmt.Fprintf(stderr, "feint: %s published no evidence; is this emulator older than the record it feeds?\n", *endpoint)
		return exitError
	}

	from, err := provenanceOf(*contractsDir, *shapesDir, *suitesDir)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	art := &evidenceArtefact{
		Format:        evidenceFormat,
		Version:       evidenceVersion,
		Machines:      []string{view.Machines},
		GeneratedFrom: from,
		Operations:    view.Evidence,
	}

	// The shape axis is static — a property of the catalogue, not of the run —
	// so it is resolved here rather than trusted from however the server was
	// launched: a serve without --shapes answers "unknown", and an artefact
	// must do better when the catalogue sits in the tree.
	if *shapesDir != "" {
		covered, err := shapeCoveredOperations(*shapesDir)
		if err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
		if covered != nil {
			for op, ev := range art.Operations {
				ev.Shape = emulator.ShapeUnobserved
				if covered[op] {
					ev.Shape = emulator.ShapeObserved
				}
				art.Operations[op] = ev
			}
		}
	}

	if *join != "" {
		other, err := readEvidence(*join)
		if err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
		// The gate the criterion always named and nothing enforced. Joining
		// takes the stronger answer per axis, which is safe only while both
		// legs read the same inputs; an artefact produced from others would let
		// a level survive the evidence that earned it.
		if moved := from.differsFrom(other.GeneratedFrom); len(moved) > 0 {
			fmt.Fprintf(stderr,
				"feint: %s was produced from different %s, so joining it would carry a "+
					"level this run did not earn; regenerate both legs together\n",
				*join, strings.Join(moved, " and "))
			return exitError
		}
		art = joinEvidence(art, other)
	}

	// Refuse to overwrite a record with one that proves less.
	//
	// Twice in one day an artefact was regenerated from a machines-off run alone
	// and silently dropped the dataplane axis for 169 operations. Nothing was
	// red: the file declared `machines: ["none"]`, so it was honest — and
	// docs/routes.md then printed the absence exactly like an operation nothing
	// has proven. A record that quietly narrows is the same defect as a record
	// that goes stale, and the second one already has a control.
	//
	// The comparison is on the runtimes the record was earned under, not on the
	// axes: an axis can legitimately shrink when a claim is corrected — #156 took
	// probed from 181 arrivals down to 83 verdicts, and that is a fix, not a
	// loss. Losing a *runtime* is never a fix; it means a leg did not run.
	//
	// --allow-narrowing exists for the one caller that legitimately writes a
	// single leg: `evidence:update` writes its machines-off leg to a temporary
	// file before joining the runtime one.
	// TestEvidenceRefusesToNarrowTheRuntimesItWasEarnedUnder fails without this.
	if !*allowNarrowing {
		lost, err := runtimesLost(*out, art)
		if err != nil {
			// The comparison could not be made, so this write is unchecked. Said
			// out loud rather than treated as "nothing lost", which is what it
			// used to be: a record that cannot be compared is not a record that
			// proves nothing was dropped.
			fmt.Fprintf(stderr, "feint: %s cannot be compared with this run (%v), so "+
				"nothing checks whether this write narrows the record.\n"+
				"Pass --allow-narrowing if replacing it wholesale is what you meant.\n",
				*out, err)
			return exitError
		}
		if len(lost) > 0 {
			fmt.Fprintf(stderr, "feint: %s was earned under %s and this run only reaches %s, "+
				"so writing it would drop every proof taken under %s.\n"+
				"Run `mise run evidence:update` on a host that can start machines, or pass "+
				"--allow-narrowing when a single leg is what you meant.\n",
				*out, strings.Join(lost, ", "), strings.Join(art.Machines, ", "),
				strings.Join(lost, ", "))
			return exitError
		}
	}

	// Said before the write, so it is still true of the file being replaced.
	reportAxesLost(stderr, *out, art)

	if err := writeEvidence(*out, art); err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	fmt.Fprintf(stdout, "evidence for %d operation(s) written to %s (machines: %s)\n",
		len(art.Operations), *out, strings.Join(art.Machines, ", "))
	return exitOK
}

// writeEvidence renders an artefact where every writer of one puts it: indented,
// key-sorted by encoding/json, and newline-terminated, because the file is
// committed and read by `git diff`.
func writeEvidence(path string, art *evidenceArtefact) error {
	blob, err := json.MarshalIndent(art, "", "  ")
	if err != nil {
		return err
	}
	//nolint:gosec // a versioned artefact is world-readable by design
	return os.WriteFile(path, append(blob, '\n'), 0o644)
}

// readEvidence loads an artefact, refusing what it cannot account for — the
// same bar Restore holds a snapshot to.
func readEvidence(path string) (*evidenceArtefact, error) {
	blob, err := os.ReadFile(path) //nolint:gosec // operator-supplied path, by design
	if err != nil {
		return nil, err
	}
	var art evidenceArtefact
	if err := json.Unmarshal(blob, &art); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if art.Format != evidenceFormat {
		return nil, fmt.Errorf("%s is not an evidence artefact (format %q)", path, art.Format)
	}
	if art.Version != evidenceVersion {
		return nil, fmt.Errorf("%s carries version %d, this binary writes %d; regenerate rather than mix", path, art.Version, evidenceVersion)
	}
	// The three axes that are verdicts rather than booleans, held to their own
	// vocabulary. The comment above this function claims it refuses what it
	// cannot account for and it did not: a missing key decodes to the empty
	// string, every reader downstream then had to decide what an empty verdict
	// means, and #406 is the record of what happens when one of them decides
	// wrong. Refused here, at the boundary, so no consumer has to.
	// TestAnAxisWithNoVerdictIsNotEarned fails without it.
	for op, ev := range art.Operations {
		if bad := unknownVerdict(ev); bad != "" {
			return nil, fmt.Errorf("%s: %s carries %s, which is not one of the values this record uses; regenerate it", path, op, bad)
		}
	}
	return &art, nil
}

// unknownVerdict names the first field of one row whose value is outside its
// declared vocabulary, or "" when the row accounts for itself.
func unknownVerdict(ev emulator.Evidence) string {
	for _, field := range []struct {
		name  string
		value string
		valid []string
	}{
		{"probed", ev.Probed, []string{emulator.ProbeResponse, emulator.ProbeRefusal, emulator.ProbeNone}},
		{"contract", ev.Contract, []string{emulator.ContractClean, emulator.ContractViolating, emulator.ContractUnchecked}},
		{"shape", ev.Shape, []string{emulator.ShapeObserved, emulator.ShapeUnobserved, emulator.ShapeUnknown}},
	} {
		if !slices.Contains(field.valid, field.value) {
			return fmt.Sprintf("%s: %q", field.name, field.value)
		}
	}
	return ""
}

// joinEvidence merges the two legs of one regeneration. Booleans take the
// stronger answer; the contract verdict keeps a violation over anything, and
// a check over silence; shape keeps an observation. That ordering is safe
// only because both legs are fresh: neither can carry a proof whose evidence
// has since been deleted.
func joinEvidence(a, b *evidenceArtefact) *evidenceArtefact {
	machines := map[string]bool{}
	for _, m := range append(append([]string{}, a.Machines...), b.Machines...) {
		machines[m] = true
	}
	names := make([]string, 0, len(machines))
	for m := range machines {
		names = append(names, m)
	}
	sort.Strings(names)

	ops := make(map[string]emulator.Evidence, len(a.Operations))
	for op, ev := range a.Operations {
		ops[op] = ev
	}
	for op, other := range b.Operations {
		ev, seen := ops[op]
		if !seen {
			ops[op] = other
			continue
		}
		ev.Driven = ev.Driven || other.Driven
		ev.Probed = strongerProbe(ev.Probed, other.Probed)
		ev.Dataplane = ev.Dataplane || other.Dataplane
		ev.Behaviour = ev.Behaviour || other.Behaviour
		ev.Negative = ev.Negative || other.Negative
		ev.Contract = strongerContract(ev.Contract, other.Contract)
		ev.Shape = strongerShape(ev.Shape, other.Shape)
		ops[op] = ev
	}
	// The provenance of the run doing the joining, carried rather than rebuilt.
	// Dropping it was the first version of this function under #171, and the
	// consequence was worse than a missing field: three empty digests compare
	// equal to three empty digests, so the gate that refuses a stale leg would
	// have accepted everything while looking like a control. Found by reading
	// the regenerated artefact instead of trusting the tests, which passed.
	return &evidenceArtefact{
		Format:        evidenceFormat,
		Version:       evidenceVersion,
		Machines:      names,
		GeneratedFrom: a.GeneratedFrom,
		Operations:    ops,
	}
}

// strongerProbe keeps the fuller validation: a validated success over a
// validated refusal, either over nothing. Safe on the same terms as the other
// two — both legs are fresh runs.
func strongerProbe(a, b string) string {
	rank := map[string]int{emulator.ProbeResponse: 2, emulator.ProbeRefusal: 1, emulator.ProbeNone: 0}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func strongerContract(a, b string) string {
	rank := map[string]int{emulator.ContractViolating: 2, emulator.ContractClean: 1, emulator.ContractUnchecked: 0}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func strongerShape(a, b string) string {
	rank := map[string]int{emulator.ShapeObserved: 2, emulator.ShapeUnobserved: 1, emulator.ShapeUnknown: 0}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// reshapeEvidence recomputes the shape axis of a record already on disk, from
// the catalogues on disk, and writes nothing else.
//
// It exists because the shape axis is the one axis that is *not* a property of
// a run: [evidence] already discards whatever the server answered and re-derives
// it from --shapes. So a catalogue that grows — `mise run shapes:fold` folding
// the committed corpora is the case #407 built it for — moves a published figure
// that no conformance run is needed to establish. Without this, refreshing it
// would mean two full conformance legs, one of them on a host that can start
// machines, to publish a number computed offline in milliseconds.
//
// Two refusals keep it from becoming a way to write a number by hand, which is
// the defect #406 closed elsewhere and which a narrow rewrite invites:
//
//   - the contracts and the suites must digest to what the record was produced
//     from. If either moved, the record is stale for a reason a reshape cannot
//     fix, and refreshing one axis would put a fresh figure on a stale record.
//   - every other axis is carried over untouched, and the shape column is
//     recomputed wholesale rather than raised: an operation the catalogue no
//     longer covers is demoted, so a fold that removed evidence lowers the
//     figure instead of leaving a high-water mark.
//
// TestAReshapeRefusesARecordWhoseOtherInputsMoved and
// TestAReshapeDemotesAnOperationTheCatalogueNoLongerCovers fail without these.
func reshapeEvidence(path, shapesDir, contractsDir, suitesDir string, stdout, stderr io.Writer) int {
	if shapesDir == "" {
		fmt.Fprintln(stderr, "feint: --reshape reads the catalogues; it cannot run with --shapes empty")
		return exitError
	}
	art, err := readEvidence(path)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	from, err := provenanceOf(contractsDir, shapesDir, suitesDir)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	var moved []string
	if from.Contracts != art.GeneratedFrom.Contracts {
		moved = append(moved, "contracts")
	}
	if from.Suites != art.GeneratedFrom.Suites {
		moved = append(moved, "suites")
	}
	if len(moved) > 0 {
		fmt.Fprintf(stderr,
			"feint: %s was produced from other %s, so its other axes are stale and a fresh shape "+
				"column would sit on them; regenerate it with `mise run evidence:update`\n",
			path, strings.Join(moved, " and "))
		return exitError
	}

	covered, err := shapeCoveredOperations(shapesDir)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	if covered == nil {
		fmt.Fprintf(stderr, "feint: no catalogue in %s, so there is nothing to resolve the shape axis from\n", shapesDir)
		return exitError
	}

	was, now := 0, 0
	for op, ev := range art.Operations {
		if ev.Shape == emulator.ShapeObserved {
			was++
		}
		ev.Shape = emulator.ShapeUnobserved
		if covered[op] {
			ev.Shape = emulator.ShapeObserved
			now++
		}
		art.Operations[op] = ev
	}
	art.GeneratedFrom.Shapes = from.Shapes

	reportAxesLost(stderr, path, art)
	if err := writeEvidence(path, art); err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	fmt.Fprintf(stdout, "%s: shape %d -> %d of %d operation(s), from %s; no other axis touched\n",
		path, was, now, len(art.Operations), shapesDir)
	return exitOK
}

// shapeCoveredOperations names the mounted operations whose answer has been
// held against one recorded from the real cloud: every catalogue entry
// carrying at least one field, resolved onto the operation that serves the
// same call.
//
// It walks the whole catalogue, and that is the correction #407 measured. It
// used to walk [upstream.Reads] instead — thirty-odd curated calls per
// provider — which capped the axis at the size of a hand-written list however
// much had been recorded. The cap was not theoretical: the axis read 52 of 370
// and its ceiling was **also 52**, per provider to the unit (exoscale 14,
// outscale 23, scaleway 15). Saturated at its own ceiling, it published 14 %
// and named 292 operations "no answer of the real cloud has been kept" —
// including every operation the committed corpora had already recorded, which
// the axis never asked about. A control whose numerator cannot move is not a
// measurement.
//
// nil with no error means no catalogue exists at all, which callers report as
// "unknown" rather than demoting every operation to "unobserved".
//
// TestAShapeRecordedOutsideTheReadListStillCounts fails without this.
func shapeCoveredOperations(dir string) (map[string]bool, error) {
	srv, _, err := newServer(nil)
	if err != nil {
		return nil, err
	}
	observed, err := observedFieldsByOperation(dir, srv)
	if err != nil || observed == nil {
		return nil, err
	}
	covered := make(map[string]bool, len(observed))
	for op := range observed {
		covered[op] = true
	}
	return covered, nil
}

// observedFieldsByOperation maps every recorded real-cloud answer onto the
// mounted operation that serves the same call: operation name to the field
// paths the real cloud was seen to return. This is the corroboration half of
// the omission check (emulator.SetObservedFields) — computed here for the same
// reason shapeCoveredOperations is: the core knows neither providers nor where
// recordings live.
//
// Unlike shapeCoveredOperations it walks the whole catalogue rather than the
// curated read list: a recording keyed by an operation name (Outscale's
// osc/Client.ReadVms) corroborates that operation directly, and one keyed by
// "METHOD path" resolves through the mounted routes. An entry nothing mounts is
// skipped — an operation no pack claims is the drift gate's business, not this
// check's.
//
// nil with no error means no catalogue exists at all, and the check then fails
// on nothing rather than treating "no recording" as "nothing to prove".
func observedFieldsByOperation(dir string, srv *emulator.Server) (map[string][]string, error) {
	routes := srv.AllRoutes()
	mounted := map[string]bool{}
	for _, r := range routes {
		mounted[r.Operation] = true
	}

	observed := map[string][]string{}
	found := false
	for _, p := range srv.Packs() {
		cat, err := readCatalogue(filepath.Join(dir, p.Name()+".json"), p.Name())
		if err != nil {
			return nil, err
		}
		if cat == nil {
			continue
		}
		found = true
		for key, rec := range cat.Operations {
			if len(rec.Fields) == 0 {
				continue
			}
			op := ""
			switch {
			case mounted[key]:
				op = key
			case rec.Method != "" && rec.Path != "":
				op, err = operationForCall(routes, rec.Method, rec.Path)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", key, err)
				}
			}
			if op == "" {
				continue
			}
			paths := make([]string, 0, len(rec.Fields))
			for _, f := range rec.Fields {
				paths = append(paths, f.Path)
			}
			observed[op] = append(observed[op], paths...)
		}
	}
	if !found {
		return nil, nil
	}
	return observed, nil
}

// operationForCall resolves the mounted route a concrete request would reach.
// Among the patterns that match, the one with the fewest wildcards wins — the
// same specificity rule the mux applies — and a tie is refused out loud
// rather than resolved by luck.
func operationForCall(routes []emulator.Route, method, path string) (string, error) {
	best, bestWild, ties := "", -1, 0
	for _, r := range routes {
		if r.Method != method {
			continue
		}
		wild, ok := matchPattern(r.Path, path)
		if !ok {
			continue
		}
		switch {
		case bestWild == -1 || wild < bestWild:
			best, bestWild, ties = r.Operation, wild, 1
		case wild == bestWild:
			ties++
		}
	}
	if ties > 1 {
		return "", fmt.Errorf("%s %s matches %d routes equally; the shape axis cannot guess", method, path, ties)
	}
	return best, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// matchPattern reports whether a concrete path satisfies a route pattern, and
// with how many wildcard segments. `{zone}`-style segments match any one
// segment; everything else matches itself.
func matchPattern(pattern, path string) (wildcards int, ok bool) {
	ps := strings.Split(strings.Trim(pattern, "/"), "/")
	cs := strings.Split(strings.Trim(path, "/"), "/")
	if len(ps) != len(cs) {
		return 0, false
	}
	for i, seg := range ps {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			wildcards++
			continue
		}
		if seg != cs[i] {
			return 0, false
		}
	}
	return wildcards, true
}
