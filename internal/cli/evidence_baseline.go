package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// `feint evidence baseline` and `feint evidence verify` (#488).
//
// # What they answer, and what they deliberately do not
//
// A downstream project pins the LEVEL OF PROOF it depends on, and its own CI
// fails when this emulator stops delivering it. Not "feint changed version" —
// "feint stopped proving what this project was relying on". #325 and #326 exist
// because a consumer discovered a change from the outside, after the fact.
//
// This is NOT `drift:check`, and the boundary has to hold or one of the two
// questions is lost:
//
//   - `coverage/*-baseline.json` + `drift:check` watch the UPSTREAM SDK surface:
//     has the cloud moved?
//   - these two watch THIS EMULATOR's own level of evidence, from the point of
//     view of somebody who is not in this repository: has what I trusted moved?
//
// A design that folds one into the other answers one question twice.
//
// # Why a baseline that only grows would be wrong
//
// Claims are MEANT to be withdrawn here. #475, #481 and #483 are each "this was
// claimed and should not have been", and a ratchet nobody can lower would have
// stopped all three. So `verify` distinguishes a regression from a deliberate
// withdrawal, and the second is accepted with a reason — the model
// `corpus/accepted.json` already uses.
//
// # Why JSON and not the YAML the issue sketched
//
// A deviation, stated rather than slipped in. The artefact this pins is JSON,
// the accepted-withdrawal file it is modelled on is JSON, and this repository
// carries no YAML dependency: `internal/environment` hand-rolls a reader bound
// to its own schema, so YAML here would be a second hand-rolled parser to get
// wrong. The shape of the file is the issue's; the encoding is not.

// axisValues reads an Evidence by axis name.
//
// Written out rather than reflected over, for the reason pinnedAxes is written
// out: adding an axis to the record is then a decision about this file too,
// visible in a diff, instead of something a baseline silently starts pinning.
func axisValues(e emulator.Evidence) map[string]any {
	return map[string]any{
		"driven":    e.Driven,
		"probed":    e.Probed,
		"contract":  e.Contract,
		"dataplane": e.Dataplane,
		"shape":     e.Shape,
		"behaviour": e.Behaviour,
		"negative":  e.Negative,
	}
}

// evidenceBaseline is what a consumer pins.
//
// Provenance is recorded rather than assumed, and that is the second of the
// three ways this goes wrong: several axes are verdicts about ONE RUN. A
// baseline captured from a leg that drove one client would pin `unobserved` as
// if it were the truth. So the population is carried in the file, and a capture
// that knows itself partial is refused below.
type evidenceBaseline struct {
	Format string `json:"format"`
	// Version of this file's own schema, not of feint.
	Version int `json:"version"`
	// Machines are the runtimes the artefact was earned under, copied from it.
	Machines []string `json:"machines"`
	// GeneratedFrom is the artefact's own provenance digest, carried so a
	// reader can tell two baselines apart when the suites moved under them.
	GeneratedFrom provenance `json:"generated_from"`
	// Operations maps an upstream operation to the axes pinned on it. An axis
	// absent from the map is an axis this consumer does not rely on.
	Operations map[string]map[string]any `json:"operations"`
}

// evidenceAccepted is a withdrawal somebody wrote down, with its reason.
type evidenceAccepted struct {
	Accepted []struct {
		Operation string `json:"operation"`
		Axis      string `json:"axis"`
		Reason    string `json:"reason"`
	} `json:"accepted"`
}

const (
	evidenceBaselineFormat  = "feint-evidence-baseline"
	evidenceBaselineVersion = 1
)

// pinnedAxes are the axes a baseline may carry, in the order the record prints
// them. Named here rather than derived by reflection so that adding an axis to
// the artefact is a decision about this file too.
var pinnedAxes = []string{"driven", "probed", "contract", "dataplane", "shape", "behaviour", "negative"}

// provesSomething answers whether a verdict is a level of proof at all.
//
// `false`, `none`, `unchecked`, `unobserved` and `unknown` are the record's ways
// of saying nobody looked. A consumer cannot rely on them, and pinning them
// would turn every later improvement into a regression — which is exactly what
// the first version of this file did: run against v0.11.0 it reported
// `osc/Client.AcceptNetPeering behaviour: false → true` as a fall.
//
// So they are dropped at capture. The baseline then carries only what somebody
// depends on, which is the shape the issue sketched: its example lists proven
// axes and nothing else.
//
// TestAnAxisThatProvesNothingIsNotPinned fails without this.
func provesSomething(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch v {
		case "", emulator.ProbeNone, emulator.ContractUnchecked,
			emulator.ShapeUnobserved, emulator.ShapeUnknown:
			return false
		}
		return true
	default:
		return false
	}
}

// satisfies answers whether a current verdict still delivers what was pinned.
//
// A relation rather than an ordering, and the difference is the whole design.
// Ranking the verdicts would mean inventing where `violating` sits against
// `unchecked`, which nothing in this repository states. What a consumer pins is
// a LEVEL it relies on, so the only question is whether the level is still
// delivered:
//
//   - a pinned boolean is `true` by construction — `false` proves nothing and
//     is never pinned (see provesSomething) — and is delivered by `true` alone;
//   - a pinned probe verdict is delivered by EITHER verdict, because `response`
//     and `refusal` are both "a real client got an answer here" and neither is
//     above the other — #156 took this axis from 181 arrivals down to 83
//     verdicts and that was a fix;
//   - every other pinned string is delivered by itself alone. `clean` stays
//     `clean`; `observed` stays `observed`.
func satisfies(axis string, pinned, current any) bool {
	if axis == "probed" {
		p, okP := pinned.(string)
		c, okC := current.(string)
		if !okP || !okC {
			return false
		}
		verdict := func(v string) bool {
			return v == emulator.ProbeResponse || v == emulator.ProbeRefusal
		}
		if verdict(p) {
			return verdict(c)
		}
		return p == c
	}
	return fmt.Sprintf("%v", pinned) == fmt.Sprintf("%v", current)
}

// evidenceCapture writes the baseline.
func evidenceCapture(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("evidence baseline")
	from := fs.String("evidence", filepath.Join("coverage", "evidence.json"),
		"the artefact to pin, as `feint evidence` wrote it")
	out := fs.String("out", "", "where to write the baseline (default: standard output)")
	axes := fs.String("axes", strings.Join(pinnedAxes, ","),
		"which axes to pin, comma-separated; pin only what you actually rely on")
	if err := fs.Parse(args); err != nil {
		return exitError
	}

	art, err := readEvidence(*from)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	// The refusal the issue asks for by name. An artefact earned with no machine
	// runtime pins `dataplane: false` on every operation that would have earned
	// it, and a consumer would then be told nothing regressed on the day it
	// really did. Refused at capture rather than written and explained later —
	// a baseline taken from a partial run is worse than none.
	//
	// TestABaselineIsRefusedWhenTheArtefactReachedNoRuntime fails without this.
	if !reachesARuntime(art.Machines) {
		fmt.Fprintf(stderr, "feint: %s was earned under %s alone, so every dataplane verdict in "+
			"it is a fact about a run that started no machine.\n"+
			"Pinning it would tell a consumer nothing regressed on the day it did. "+
			"Regenerate with `mise run evidence:update` on a host that can start machines.\n",
			*from, strings.Join(art.Machines, ", "))
		return exitError
	}

	wanted := map[string]bool{}
	for _, axis := range strings.Split(*axes, ",") {
		axis = strings.TrimSpace(axis)
		if axis == "" {
			continue
		}
		if !contains(pinnedAxes, axis) {
			fmt.Fprintf(stderr, "feint: %q is not an axis of this record; the axes are %s\n",
				axis, strings.Join(pinnedAxes, ", "))
			return exitError
		}
		wanted[axis] = true
	}
	if len(wanted) == 0 {
		fmt.Fprintln(stderr, "feint: --axes named none, so this baseline would pin nothing")
		return exitError
	}

	baseline := evidenceBaseline{
		Format:        evidenceBaselineFormat,
		Version:       evidenceBaselineVersion,
		Machines:      art.Machines,
		GeneratedFrom: art.GeneratedFrom,
		Operations:    map[string]map[string]any{},
	}
	for operation, verdicts := range art.Operations {
		values := axisValues(verdicts)
		pinned := map[string]any{}
		for _, axis := range pinnedAxes {
			if wanted[axis] && provesSomething(values[axis]) {
				pinned[axis] = values[axis]
			}
		}
		if len(pinned) > 0 {
			baseline.Operations[operation] = pinned
		}
	}

	encoded, err := json.MarshalIndent(baseline, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	encoded = append(encoded, '\n')
	if *out == "" {
		_, _ = stdout.Write(encoded)
		return exitOK
	}
	if err := os.WriteFile(*out, encoded, 0o644); err != nil { //nolint:gosec // a baseline a consumer commits
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	fmt.Fprintf(stdout, "pinned %d operation(s) on %d axis(es) to %s (machines: %s)\n",
		len(baseline.Operations), len(wanted), *out, strings.Join(baseline.Machines, ", "))
	return exitOK
}

// evidenceVerify compares the artefact against the baseline.
func evidenceVerify(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("evidence verify")
	baselinePath := fs.String("baseline", ".feint-evidence.json", "the baseline to hold this run against")
	from := fs.String("evidence", filepath.Join("coverage", "evidence.json"), "the artefact to check")
	acceptedPath := fs.String("accepted", "",
		"withdrawals somebody wrote down with a reason; unset, every fall is a regression")
	if err := fs.Parse(args); err != nil {
		return exitError
	}

	raw, err := os.ReadFile(*baselinePath)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	var baseline evidenceBaseline
	if err := json.Unmarshal(raw, &baseline); err != nil {
		fmt.Fprintf(stderr, "feint: reading %s: %v\n", *baselinePath, err)
		return exitError
	}
	if baseline.Format != evidenceBaselineFormat {
		fmt.Fprintf(stderr, "feint: %s is not a feint evidence baseline (format %q)\n",
			*baselinePath, baseline.Format)
		return exitError
	}

	art, err := readEvidence(*from)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	accepted := map[string]string{}
	if *acceptedPath != "" {
		acceptedRaw, err := os.ReadFile(*acceptedPath)
		if err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
		var doc evidenceAccepted
		if err := json.Unmarshal(acceptedRaw, &doc); err != nil {
			fmt.Fprintf(stderr, "feint: reading %s: %v\n", *acceptedPath, err)
			return exitError
		}
		for _, entry := range doc.Accepted {
			// An entry with no reason is not an acceptance, it is a silencer.
			// Refused rather than honoured: the whole value of this file is that
			// a withdrawal carries why.
			if strings.TrimSpace(entry.Reason) == "" {
				fmt.Fprintf(stderr, "feint: %s accepts %s/%s with no reason, and a withdrawal "+
					"without one is a claim nobody has to defend\n",
					*acceptedPath, entry.Operation, entry.Axis)
				return exitError
			}
			accepted[entry.Operation+"\x00"+entry.Axis] = entry.Reason
		}
	}

	type fall struct{ operation, axis, was, now, reason string }
	var regressions, withdrawals []fall

	operations := make([]string, 0, len(baseline.Operations))
	for operation := range baseline.Operations {
		operations = append(operations, operation)
	}
	sort.Strings(operations)

	for _, operation := range operations {
		pinned := baseline.Operations[operation]
		current, present := art.Operations[operation]
		for _, axis := range pinnedAxes {
			want, relied := pinned[axis]
			if !relied {
				continue
			}
			// An operation that is gone entirely is the strongest fall there is,
			// and it must not read as "no verdict to compare".
			now := "absent"
			if present {
				got := axisValues(current)[axis]
				if satisfies(axis, want, got) {
					continue
				}
				now = fmt.Sprintf("%v", got)
			}
			f := fall{operation, axis, fmt.Sprintf("%v", want), now, accepted[operation+"\x00"+axis]}
			if f.reason != "" {
				withdrawals = append(withdrawals, f)
				continue
			}
			regressions = append(regressions, f)
		}
	}

	for _, w := range withdrawals {
		fmt.Fprintf(stdout, "  withdrawn: %s %s %s → %s — %s\n", w.operation, w.axis, w.was, w.now, w.reason)
	}
	if len(regressions) == 0 {
		fmt.Fprintf(stdout, "every level %s pins is still delivered (%d operation(s), %d withdrawal(s) accepted)\n",
			*baselinePath, len(baseline.Operations), len(withdrawals))
		return exitOK
	}
	for _, r := range regressions {
		fmt.Fprintf(stderr, "REGRESSED: %s %s: %s → %s\n", r.operation, r.axis, r.was, r.now)
	}
	fmt.Fprintf(stderr, "\n%d level(s) this baseline pins are no longer delivered.\n"+
		"If a claim was withdrawn on purpose, write it down with its reason and pass --accepted:\n"+
		"  {\"accepted\": [{\"operation\": %q, \"axis\": %q, \"reason\": \"…\"}]}\n",
		len(regressions), regressions[0].operation, regressions[0].axis)
	return exitDrift
}

// reachesARuntime answers whether the record was earned with a machine runtime.
// "none" is a runtime name here, and it is the one that proves nothing about a
// dataplane.
func reachesARuntime(machines []string) bool {
	for _, m := range machines {
		if m != "" && m != "none" {
			return true
		}
	}
	return false
}
