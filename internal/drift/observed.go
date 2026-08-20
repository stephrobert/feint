package drift

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Every refusal in this repository carries a reason, which is the discipline.
// None of them carries a *demand*, which is the gap this file closes.
//
// `Declined()` holds a written argument for each of the 315 Scaleway, 172
// Outscale and 281 Exoscale operations the packs refuse. The arguments are
// sound and they are all equally loud: nothing says that eighteen recorded
// calls landed on one of them and none has ever landed on another. That is the
// difference between "a user's plan died on this today" and "a list we can
// defend", and until a recording is read, the repository cannot tell them
// apart.
//
// Two facts, kept apart on purpose:
//
//   - **nobody called it** — an operation no recorded client addressed. It says
//     nothing about whether the decision on it is right;
//   - **nobody triaged it** — an operation upstream that is neither served nor
//     declined. That is what fails the gate, and a call count neither excuses
//     nor creates it.
//
// Blurring them would be the defect this exists to correct, so the report
// counts them separately and never adds them.

// Observation is what a set of recordings said about one operation.
type Observation struct {
	// Operation is the report's own spelling ("instance/v1/API.ListServers"),
	// so a reader can paste it into a Declined() block or a Route.
	Operation string `json:"operation"`
	Status    Status `json:"status"`
	// Calls is how many recorded requests addressed it. The ranking key.
	Calls int `json:"calls"`
	// Clients names the families that produced them, from a closed vocabulary
	// (transcript.ClientOf): never a raw User-Agent.
	Clients string `json:"clients,omitempty"`
	// Reason is the pack's argument for declining, carried so the ranking can
	// be read against the decision it questions rather than beside it.
	Reason string `json:"reason,omitempty"`
}

// Observed is a coverage report crossed with what recordings observed.
type Observed struct {
	Provider string `json:"provider"`
	// Declined and Unknown are the two rankings, each sorted by calls
	// descending then by name. Only operations with at least one call appear:
	// an operation nobody called is a count below, not a row here, because a
	// list that includes them is the alphabet again.
	Declined []Observation `json:"declined_and_called"`
	Unknown  []Observation `json:"untriaged_and_called"`
	// Implemented is the count of served operations the recordings exercised,
	// which is what says whether the recording reached this provider at all.
	Implemented int `json:"implemented_and_called"`
	// The three populations of the report, so "nobody called it" and "nobody
	// triaged it" are legible as the different facts they are.
	DeclinedTotal    int `json:"declined_total"`
	DeclinedSilent   int `json:"declined_never_called"`
	UnknownTotal     int `json:"untriaged_total"`
	UnknownSilent    int `json:"untriaged_never_called"`
	ImplementedTotal int `json:"implemented_total"`
	// Unattributed is how many recorded calls this provider's document
	// describes no operation for. Two populations land here and both are
	// ordinary: another provider's traffic, because one forward-proxy recording
	// holds several clouds, and a product outside the committed contract —
	// Scaleway publishes one document per product and contracts/scaleway.json
	// carries the ten the emulator has started, so a call to rdb or k8s cannot
	// be named. Counted rather than dropped, because a ranking that silently
	// ignored calls would read as a ranking of everything.
	Unattributed int `json:"calls_naming_no_operation"`
}

// Calls is what a caller measured in the recordings: how many requests reached
// each operation, and which client families made them.
//
// It is a parameter rather than something this package computes, because
// naming the operation a recorded call addressed needs the provider's contract
// document, and internal/drift must not learn a provider's dialect to do it.
type Calls struct {
	Count   map[string]int
	Clients map[string]string
}

// Observe crosses a report with the calls a set of recordings made.
func (r Report) Observe(calls Calls, unattributed int) Observed {
	out := Observed{Provider: r.Provider, Unattributed: unattributed}
	for _, e := range r.Entries {
		n := calls.Count[e.Operation]
		switch e.Status {
		case StatusImplemented:
			out.ImplementedTotal++
			if n > 0 {
				out.Implemented++
			}
		case StatusDeclined:
			out.DeclinedTotal++
			if n == 0 {
				out.DeclinedSilent++
				continue
			}
			out.Declined = append(out.Declined, Observation{
				Operation: e.Operation, Status: e.Status,
				Calls: n, Clients: calls.Clients[e.Operation], Reason: e.Reason,
			})
		case StatusUnknown:
			out.UnknownTotal++
			if n == 0 {
				out.UnknownSilent++
				continue
			}
			out.Unknown = append(out.Unknown, Observation{
				Operation: e.Operation, Status: e.Status,
				Calls: n, Clients: calls.Clients[e.Operation],
			})
		}
	}
	rankObservations(out.Declined)
	rankObservations(out.Unknown)
	return out
}

// rankObservations sorts by calls descending, then by name.
//
// The tie-break on the name is not decoration: this output is read into an
// issue and compared between two runs, so two runs over one recording have to
// produce the same order. TestAnOperationNobodyCalledNeverOutranksOneSomebodyDid
// holds the first half.
func rankObservations(obs []Observation) {
	sort.Slice(obs, func(i, j int) bool {
		if obs[i].Calls != obs[j].Calls {
			return obs[i].Calls > obs[j].Calls
		}
		return obs[i].Operation < obs[j].Operation
	})
}

// WriteObserved renders the ranking, then the populations it was drawn from.
//
// The populations matter as much as the rows. A ranking on its own reads as
// "this is the backlog"; with the counts beside it, it reads as what it is —
// the part of the backlog a client has been seen to want.
func (o Observed) WriteObserved(w io.Writer) error {
	if len(o.Declined) == 0 && len(o.Unknown) == 0 {
		if _, err := fmt.Fprintf(w,
			"provider %s: no recorded call reached an operation this pack declines or has left untriaged.\n",
			o.Provider); err != nil {
			return err
		}
		return o.writeTotals(w)
	}

	if len(o.Declined) > 0 {
		if _, err := fmt.Fprintf(w,
			"provider %s: %d declined operation(s) a recorded client called anyway, most-called first\n\n",
			o.Provider, len(o.Declined)); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, "  calls  client       operation"); err != nil {
			return err
		}
		for _, obs := range o.Declined {
			if _, err := fmt.Fprintf(w, "  %5d  %-11s  %s\n", obs.Calls, obs.Clients, obs.Operation); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "         declined: %s\n", wrapReason(obs.Reason)); err != nil {
				return err
			}
		}
	}

	if len(o.Unknown) > 0 {
		if _, err := fmt.Fprintf(w,
			"\nprovider %s: %d untriaged operation(s) a recorded client called, most-called first\n\n",
			o.Provider, len(o.Unknown)); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, "  calls  client       operation"); err != nil {
			return err
		}
		for _, obs := range o.Unknown {
			if _, err := fmt.Fprintf(w, "  %5d  %-11s  %s\n", obs.Calls, obs.Clients, obs.Operation); err != nil {
				return err
			}
		}
	}
	return o.writeTotals(w)
}

// writeTotals states the two facts that must never be confused, in words that
// cannot be read as each other.
func (o Observed) writeTotals(w io.Writer) error {
	_, err := fmt.Fprintf(w, `
%d declined operation(s) in all; %d of them no recorded client called.
%d upstream operation(s) nobody has triaged; %d of them no recorded client called.
%d implemented operation(s); %d of them this recording exercised.
%d recorded call(s) this provider's document describes no operation for: another
provider's traffic in the same file, or a product outside the committed contract.
`,
		o.DeclinedTotal, o.DeclinedSilent,
		o.UnknownTotal, o.UnknownSilent,
		o.ImplementedTotal, o.Implemented,
		o.Unattributed)
	return err
}

// wrapReason keeps a decline's argument on one line of a width a terminal can
// hold. The reason is the pack's own prose and is never truncated silently: an
// argument cut mid-clause reads as a different argument.
func wrapReason(reason string) string {
	const width = 96
	if len(reason) <= width {
		return reason
	}
	cut := strings.LastIndex(reason[:width], " ")
	if cut < 0 {
		cut = width
	}
	return reason[:cut] + " […]"
}
