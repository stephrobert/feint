package stack

import "sort"

// Standing is what is known about one declared type.
//
// Four values, not two, and each boundary was paid for:
//
//   - Served and Declined are the two answers a pack actually has. Declined
//     carries the pack's own reason, read from its Declined() list rather than
//     copied here, so a reason rewritten in the pack reaches this report
//     without a second edit. #298 measured what the copy costs: a decline
//     reason rewritten in a pack took four days to reach the committed
//     artefact, while two gates agreed with each other and both disagreed with
//     the code.
//   - Unknown covers both absences: a type no pack has a mapping for, and an
//     operation a mapping names that its own pack has not decided about. An
//     absence of knowledge, which is the one thing a preflight must never
//     print as a green verdict. The reason says which absence it is.
//
// A fourth standing for "the product is outside the surface the drift scan
// covers" was written and removed before this shipped. It is a real category —
// the Scaleway scan covers 11 of the 56 products in that SDK — but no table row
// can reach it today, because TestEveryStackOperationIsOneThatPackDecided
// requires every row to name an operation its pack serves or declines. A state
// no input can produce is dead code with a comment attached, which is what this
// repository refuses. It comes back with the rows that need it, and with a
// guard that tells an unscanned product from a typo.
type Standing string

const (
	StandingServed   Standing = "served"
	StandingDeclined Standing = "declined"
	StandingUnknown  Standing = "unknown"
)

// Verdict is the whole configuration's answer.
type Verdict string

const (
	// VerdictCompatible means every declared type maps to an operation this
	// emulator serves. It says nothing about attributes, nested blocks, or
	// anything past the control plane.
	VerdictCompatible Verdict = "compatible"
	// VerdictIncompatible means at least one declared type maps to an
	// operation a pack refuses, with its reason. This is the only verdict that
	// justifies refusing before a client runs.
	VerdictIncompatible Verdict = "incompatible"
	// VerdictUnknown means the evidence does not reach a verdict: a type with
	// no mapping, an operation outside the scanned surface, a module that
	// could not be read, a file that could not be tokenized.
	VerdictUnknown Verdict = "unknown"
)

// Finding is what is known about one declared type.
type Finding struct {
	Name      string   `json:"type"`
	Mode      Mode     `json:"mode"`
	Standing  Standing `json:"standing"`
	Provider  string   `json:"provider,omitempty"`
	Operation string   `json:"operation,omitempty"`
	Reason    string   `json:"reason,omitempty"`

	// Counted carries forward that the instance count is plan-time. It does
	// not weaken the standing: the type is required whatever the count
	// evaluates to.
	Counted bool `json:"instance_count_is_plan_time,omitempty"`
}

// Report is the preflight's whole answer for one configuration.
type Report struct {
	Dir      string        `json:"dir"`
	Files    []string      `json:"files"`
	Engine   string        `json:"engine,omitempty"`
	Verdict  Verdict       `json:"verdict"`
	Findings []Finding     `json:"findings"`
	Unread   []FileProblem `json:"unreadable,omitempty"`

	// Unresolved names the module calls whose contents were not read, because a
	// module nobody could open is a hole in the inventory and not an empty one.
	Unresolved []string `json:"unresolved_modules,omitempty"`
}

// Decide folds the findings into one verdict.
//
// The order is deliberate. A refusal outranks an absence of knowledge: a
// configuration holding one type a pack refuses is incompatible whatever else
// is unknown about it, because that one type is enough to make the run fail.
// An absence of knowledge outranks compatibility, which is the rule a preflight
// exists to hold — a green verdict drawn from nothing measured is the failure
// this whole repository is organised against.
func (r *Report) Decide() {
	sort.SliceStable(r.Findings, func(i, j int) bool {
		if r.Findings[i].Mode != r.Findings[j].Mode {
			return r.Findings[i].Mode < r.Findings[j].Mode
		}
		return r.Findings[i].Name < r.Findings[j].Name
	})

	incompatible, unknown := false, false
	for _, f := range r.Findings {
		switch f.Standing {
		case StandingDeclined:
			incompatible = true
		case StandingUnknown:
			unknown = true
		case StandingServed:
		}
	}
	if len(r.Unread) > 0 || len(r.Unresolved) > 0 {
		unknown = true
	}

	switch {
	case incompatible:
		r.Verdict = VerdictIncompatible
	case unknown:
		r.Verdict = VerdictUnknown
	default:
		r.Verdict = VerdictCompatible
	}
}

// Refused is every finding a pack refuses, which is what a refusal message
// prints. Nothing else belongs in that message: a reader told "incompatible"
// needs the rows that make it so, not the whole inventory.
func (r Report) Refused() []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Standing == StandingDeclined {
			out = append(out, f)
		}
	}
	return out
}
