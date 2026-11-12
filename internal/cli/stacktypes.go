package cli

import (
	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/stack"
)

// packStackTypes is the optional half of a pack that can say which upstream
// operation the creation of a Terraform type calls.
//
// The division of labour is the one packStackHazards already draws, and rule 5
// is why: internal/stack reads files and knows nothing about any provider, this
// file joins, and which operation a type calls is provider knowledge that lives
// in the pack. A fourth pack adds a table; nothing here changes.
//
// The table gives operation names only. Whether an operation is served, refused
// or outside the scanned surface is answered here, from the pack's own Routes()
// and Declined() — never from a second copy of those verdicts, because #298
// measured what the copy costs: a decline reason rewritten in a pack took four
// days to reach the committed artefact while two gates agreed with each other
// and both disagreed with the code.
type packStackTypes interface {
	StackOperations() map[stack.TypeKey]string
}

// typeAnswer is what one pack knows about one Terraform type.
type typeAnswer struct {
	provider  string
	operation string
	reason    string
	standing  stack.Standing
}

// stackAnswers folds every pack's table into one lookup, resolving each
// operation against what that pack serves and refuses today.
//
// Three standings come out of it, and the third is the one that keeps the join
// honest: an operation a table names that its own pack has not decided about is
// undecided, never served. A wrong row must not become a promise.
func stackAnswers(packs []emulator.Pack) map[stack.TypeKey]typeAnswer {
	out := map[stack.TypeKey]typeAnswer{}
	for _, p := range packs {
		table, ok := p.(packStackTypes)
		if !ok {
			continue
		}
		served := map[string]bool{}
		for _, r := range p.Routes() {
			served[r.Operation] = true
		}
		declined := map[string]string{}
		for _, d := range p.Declined() {
			declined[d.Operation] = d.Reason
		}
		for key, op := range table.StackOperations() {
			a := typeAnswer{provider: p.Name(), operation: op}
			switch {
			case declined[op] != "":
				a.standing = stack.StandingDeclined
				a.reason = declined[op]
			case served[op]:
				a.standing = stack.StandingServed
			default:
				// A row naming an operation this pack neither serves nor
				// declines. TestEveryStackOperationIsOneThatPackDecided makes
				// that a failing build rather than a runtime surprise, so this
				// branch is reachable only from a pack a test builds — and it
				// answers unknown, never served. A row that is wrong must not
				// turn into a promise.
				a.standing = stack.StandingUnknown
				a.reason = "this pack neither serves nor declines " + op + ", so nothing here has decided it"
			}
			out[key] = a
		}
	}
	return out
}

// stackReport is the preflight's answer for one module.
//
// Every declared type gets a row, including the ones nothing knows about: a
// report that listed only what it recognised would read as a clean bill of
// health for a configuration it had barely looked at.
func stackReport(packs []emulator.Pack, mod stack.Module) stack.Report {
	answers := stackAnswers(packs)

	rep := stack.Report{Dir: mod.Dir, Files: mod.Files, Unread: mod.Unreadable}

	// One row per (mode, type), not per declaration: ten servers are one
	// question about one type, and a report that repeated it ten times would
	// bury the one row that matters.
	type key struct {
		mode stack.Mode
		name string
	}
	seen := map[key]bool{}
	counted := map[key]bool{}
	for _, d := range mod.Declarations {
		if d.Type == "" {
			continue
		}
		k := key{d.Mode, d.Type}
		if d.Counted {
			counted[k] = true
		}
		if seen[k] {
			continue
		}
		seen[k] = true

		f := stack.Finding{Name: d.Type, Mode: d.Mode, Standing: stack.StandingUnknown}
		if a, ok := answers[stack.TypeKey{Mode: d.Mode, Type: d.Type}]; ok {
			f.Standing, f.Provider, f.Operation, f.Reason = a.standing, a.provider, a.operation, a.reason
		}
		rep.Findings = append(rep.Findings, f)
	}
	for i := range rep.Findings {
		if counted[key{rep.Findings[i].Mode, rep.Findings[i].Name}] {
			rep.Findings[i].Counted = true
		}
	}

	// EVERY module call is a hole in this inventory, and every one is named.
	//
	// This report covers one module: the one whose files were read. A module's
	// own declarations are not in it, so a verdict drawn over them would be a
	// verdict about files nobody opened. The first version of this function
	// named only the modules it could not have followed — an expression source,
	// a registry address — and therefore answered `compatible` for the
	// repository's own Outscale example, which calls ./modules/net twice. A
	// green verdict over two unread files is the failure this whole repository
	// is organised against, and it was written here before it was caught.
	//
	// The reason is spelled per kind, because they are not the same limit: a
	// local path could be followed and is not yet, a registry source would need
	// an install, and an expression source cannot be read at all without
	// evaluating it. Terraform 1.16 evaluates locals and `const` variables at
	// init and OpenTofu 1.11 evaluates input variables; this reader evaluates
	// neither.
	for _, m := range mod.ModuleCalls {
		switch {
		case m.ReadFrom != "":
			// Read, so its declarations are in the inventory above and there is
			// nothing to report.
		case m.Unread != "":
			rep.Unresolved = append(rep.Unresolved, m.Name+": "+m.Unread)
		default:
			// Neither read nor explained, which is what a module call looks
			// like when the caller used stack.Read and followed nothing. The
			// default is the honest one: a call nobody resolved is a hole,
			// and the first version of this function silently skipped these
			// and answered `compatible` over them.
			rep.Unresolved = append(rep.Unresolved, m.Name+": its contents were not read")
		}
	}

	rep.Decide()
	return rep
}
