// Package compat classifies what a consumer's expression does across two
// releases of the emulator's own surfaces.
//
// #132 froze the shapes of /_feint/health, /_feint/routes, /_feint/conformance
// and /_feint/trace, gave each a schema_version, and made a shape change fail CI
// unless the version moves with it. That is the producer's half and it works.
//
// Nothing exercised the consumer's half (#170): no test took an expression a
// user could legitimately have written against an older release and ran it
// against the current one. This package is that measurement, and it exists
// because the interesting failure is not an error — it is an answer.
package compat

import "fmt"

// Verdict is where one expression lands. Every expression lands in exactly one.
type Verdict string

const (
	// Compatible: the same answer, so nothing is needed.
	Compatible Verdict = "compatible"
	// ExplicitlyBroken: the answer changed, and the surface's schema_version
	// changed with it, so a consumer keying on the version notices before it
	// misreads anything. This is what the versioning policy is for.
	ExplicitlyBroken Verdict = "explicitly broken"
	// SilentlyWrong: the answer changed, the expression still runs, still
	// answers, and the answer is false — with nothing in the payload that lets
	// the consumer know. A single one of these fails a release.
	SilentlyWrong Verdict = "silently wrong"
)

// Expression is one thing a consumer wrote, and what it produced on each side.
//
// Before and After are the evaluated results as text, because that is what a
// consumer sees: a number, a list, a null. Comparing text rather than a decoded
// value is deliberate — 0 and "0" are the same answer to a shell pipeline and a
// different one to a JSON decoder, and the consumer here is a shell pipeline.
type Expression struct {
	// Name is how the finding is reported.
	Name string
	// Surface is the payload it reads, e.g. "conformance". The classifier looks
	// up that surface's schema_version on both sides.
	Surface string
	// Source is the expression itself, printed in the finding so a reader can
	// run it.
	Source string
	// Means is what the consumer believed it counted. Data rather than a
	// comment, for the reason Decline.Reason is: whoever reads a finding is not
	// holding this file open.
	Means string
	// Before and After are what it evaluated to on each release.
	Before string
	After  string
}

// Versions carries a surface's schema_version on each side. A surface that
// carried none is Absent, which is not the same as zero and is the case the
// 0.8 boundary is made of: schema_version did not exist before #132, so a
// consumer of that release had nothing to key on at all.
type Versions struct {
	Before      string
	After       string
	BeforeKnown bool
	AfterKnown  bool
}

// Changed reports whether a consumer could tell the two payloads apart from the
// version alone.
//
// An absent version on the older side counts as *no signal*, not as a change.
// The reasoning matters, because the opposite reading would let every release
// before the policy claim the policy's protection retroactively: a consumer
// writing against a payload with no schema_version cannot have checked one, so
// nothing it could have written would have warned it.
func (v Versions) Changed() bool {
	if !v.BeforeKnown {
		return false
	}
	if !v.AfterKnown {
		// The signal disappeared, which a consumer that checked it does notice.
		return true
	}
	return v.Before != v.After
}

// Finding is one classified expression.
type Finding struct {
	Expression
	Verdict Verdict
	Why     string
}

// Classify places one expression in exactly one bucket.
func Classify(expr Expression, versions Versions) Finding {
	switch {
	case expr.Before == expr.After:
		return Finding{
			Expression: expr,
			Verdict:    Compatible,
			Why:        fmt.Sprintf("the same answer on both sides (%s)", expr.Before),
		}
	case versions.Changed():
		return Finding{
			Expression: expr,
			Verdict:    ExplicitlyBroken,
			Why: fmt.Sprintf("%s answered %s and now answers %s, and %s's schema_version moved "+
				"from %s to %s, so a consumer keying on it notices before it misreads anything",
				expr.Name, expr.Before, expr.After, expr.Surface, versions.Before, versions.After),
		}
	default:
		signal := "the version did not move"
		if !versions.BeforeKnown {
			signal = "that release carried no schema_version at all, so nothing it could have " +
				"checked would have warned it"
		}
		return Finding{
			Expression: expr,
			Verdict:    SilentlyWrong,
			Why: fmt.Sprintf("%s answered %s and now answers %s — it still runs, still answers, "+
				"and the answer is false; %s", expr.Name, expr.Before, expr.After, signal),
		}
	}
}

// ClassifyAll runs Classify over a set, keyed by surface.
func ClassifyAll(exprs []Expression, versions map[string]Versions) []Finding {
	out := make([]Finding, 0, len(exprs))
	for _, expr := range exprs {
		out = append(out, Classify(expr, versions[expr.Surface]))
	}
	return out
}

// Accepted is a silently-wrong finding the project has recorded rather than
// fixed, with the reason. The 0.8 boundary is made of these: schema_version did
// not exist then, so no consumer of that release could have been protected by a
// policy that shipped after it.
//
// Recorded rather than hidden, which is what #170 asked for. An accepted finding
// still prints; it simply does not fail the gate.
type Accepted struct {
	Name   string
	Reason string
}

// Gate returns the findings that must fail a release: every silently-wrong one
// that is not accepted.
//
// The accepted list is matched by expression name, and an accepted entry that
// matches nothing is itself reported — a stale exemption is how a gate quietly
// stops covering what it names.
func Gate(findings []Finding, accepted []Accepted) (fail []Finding, stale []string) {
	excused := make(map[string]string, len(accepted))
	for _, entry := range accepted {
		excused[entry.Name] = entry.Reason
	}
	matched := map[string]bool{}

	for _, finding := range findings {
		if finding.Verdict != SilentlyWrong {
			continue
		}
		if _, ok := excused[finding.Name]; ok {
			matched[finding.Name] = true
			continue
		}
		fail = append(fail, finding)
	}
	for _, entry := range accepted {
		if !matched[entry.Name] {
			stale = append(stale, entry.Name)
		}
	}
	return fail, stale
}
