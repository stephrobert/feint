package drift

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Status is the verdict on one upstream operation.
type Status string

const (
	// StatusImplemented means a mounted route serves it.
	StatusImplemented Status = "implemented"
	// StatusDeclined means the pack decided not to serve it.
	StatusDeclined Status = "declined"
	// StatusUnknown means nobody decided. This is the state that fails CI: it
	// is how an upstream addition announces itself.
	StatusUnknown Status = "unknown"
)

// Entry is one line of the report.
type Entry struct {
	Operation string `json:"operation"`
	Product   string `json:"product"`
	// Reason is why a declined operation is declined, empty for every other
	// status. It is carried here rather than looked up by the caller because
	// each renderer would otherwise have to join two lists and one of them
	// would forget.
	Reason string `json:"reason,omitempty"`
	// Version is the product's API version, empty when the SDK carries none
	// (Outscale's flat namespace). It is kept per entry because triage verdicts
	// differ per version: a stable v1 addition is work, an alpha rewrite is a
	// Declined() block, and a product total that mixes them hides the decision.
	Version string `json:"version,omitempty"`
	Status  Status `json:"status"`
}

// Report is the coverage of one provider.
type Report struct {
	Provider    string `json:"provider"`
	Total       int    `json:"total"`
	Implemented int    `json:"implemented"`
	Declined    int    `json:"declined"`
	// Unexplained lists declined operations whose reason is missing. It is a
	// defect in the pack rather than drift, and it is reported rather than
	// silently classified: the gate reads it and fails, so "declined with no
	// reason" can never be mistaken for "untriaged".
	// No json tag: WriteJSON builds its own map and does not carry this, and a
	// tag advertising a field the wire never has is the same lie one layer down.
	Unexplained []string `json:"-"`
	Unknown     int      `json:"unknown"`
	Entries     []Entry  `json:"entries"`
	// Orphans are routes whose Operation matches nothing upstream: either a
	// typo, or an operation upstream just removed. Both need a human.
	Orphans []string `json:"orphans"`
}

// Compare joins the upstream surface with what the pack claims.
// Compare joins the upstream surface against what a pack serves and what it
// declines. declined maps an operation to the reason it is refused, so a report
// can say why rather than only how many.
func Compare(provider string, upstream []Operation, served []string, declined map[string]string) Report {
	servedSet := toSet(served)
	declinedSet := declined

	rep := Report{Provider: provider, Total: len(upstream)}
	matched := make(map[string]bool, len(servedSet))

	for _, op := range upstream {
		e := Entry{Operation: op.Name, Product: op.Product, Version: op.Version, Status: StatusUnknown}
		switch {
		case servedSet[op.Name]:
			e.Status = StatusImplemented
			matched[op.Name] = true
			rep.Implemented++
		case declinedIs(declinedSet, op.Name):
			// Membership, not the reason string. Deciding on `!= ""` made an
			// empty reason reclassify the operation as untriaged *and* report it
			// as an orphan — an upstream-matching operation announced as having
			// no upstream match, on the one signal this report exists to keep
			// reliable. An audit reproduced both.
			e.Status = StatusDeclined
			e.Reason = declinedSet[op.Name]
			if strings.TrimSpace(e.Reason) == "" {
				// Named rather than tolerated: the caller's own guard should
				// have caught this, and a report that stays silent here is how
				// the guard's absence goes unnoticed.
				rep.Unexplained = append(rep.Unexplained, op.Name)
			}
			matched[op.Name] = true
			rep.Declined++
		default:
			rep.Unknown++
		}
		rep.Entries = append(rep.Entries, e)
	}

	for name := range servedSet {
		if !matched[name] {
			rep.Orphans = append(rep.Orphans, name)
		}
	}
	for name := range declinedSet {
		if !matched[name] {
			rep.Orphans = append(rep.Orphans, name)
		}
	}
	sort.Strings(rep.Orphans)

	return rep
}

// OnlyProducts filters an upstream surface down to the given products. Names are
// trimmed so a comma-separated flag value works as typed.
func OnlyProducts(ops []Operation, products []string) []Operation {
	wanted := make(map[string]bool, len(products))
	for _, p := range products {
		if p = strings.TrimSpace(p); p != "" {
			wanted[p] = true
		}
	}
	if len(wanted) == 0 {
		return ops
	}
	out := make([]Operation, 0, len(ops))
	for _, op := range ops {
		if wanted[op.Product] {
			out = append(out, op)
		}
	}
	return out
}

// OnlyProductNames filters a list of operation names down to the given products.
// Operation names start with "<product>/<version>/", so restricting a report to a
// product must restrict both sides: otherwise a route serving another product is
// reported as an orphan, which is a false alarm on the one signal that must stay
// trustworthy.
func OnlyProductNames(names []string, products []string) []string {
	wanted := make(map[string]bool, len(products))
	for _, p := range products {
		if p = strings.TrimSpace(p); p != "" {
			wanted[p] = true
		}
	}
	if len(wanted) == 0 {
		return names
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		product, _, found := strings.Cut(name, "/")
		if found && wanted[product] {
			out = append(out, name)
		}
	}
	return out
}

func toSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, v := range values {
		if v != "" {
			set[v] = true
		}
	}
	return set
}

// ByProduct groups the entries per upstream product, for the report and the
// generated documentation.
func (r Report) ByProduct() map[string]Report {
	out := make(map[string]Report)
	for _, e := range r.Entries {
		sub := out[e.Product]
		sub.Provider = r.Provider
		sub.Total++
		switch e.Status {
		case StatusImplemented:
			sub.Implemented++
		case StatusDeclined:
			sub.Declined++
		case StatusUnknown:
			sub.Unknown++
		}
		sub.Entries = append(sub.Entries, e)
		out[e.Product] = sub
	}
	return out
}

// VersionCount is the coverage of one API version of one product.
type VersionCount struct {
	Version     string `json:"version"`
	Total       int    `json:"total"`
	Implemented int    `json:"implemented"`
	Declined    int    `json:"declined"`
	Unknown     int    `json:"unknown"`
}

// Versions returns per-version counts for the report's entries, sorted by
// version name. Entries without a version are not counted: a flat namespace
// has nothing to break down.
//
// This exists because a product total that mixes versions hides the one fact
// triage runs on. Measured on this repository: "instance 137 operations, 73
// declined" reads as a product three-quarters refused, when 59 of those are the
// v2alpha1 rewrite declined as a block and v1 is mostly served. Asserted by
// TestVersionsSplitsAProductAcrossItsAPIVersions, which fails if Compare drops
// the version again.
func (r Report) Versions() []VersionCount {
	counts := make(map[string]*VersionCount)
	for _, e := range r.Entries {
		if e.Version == "" {
			continue
		}
		vc, ok := counts[e.Version]
		if !ok {
			vc = &VersionCount{Version: e.Version}
			counts[e.Version] = vc
		}
		vc.Total++
		switch e.Status {
		case StatusImplemented:
			vc.Implemented++
		case StatusDeclined:
			vc.Declined++
		case StatusUnknown:
			vc.Unknown++
		}
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]VersionCount, 0, len(names))
	for _, name := range names {
		out = append(out, *counts[name])
	}
	return out
}

// WriteText renders a human summary: the products that are actually started,
// then the totals. Listing 1700 untouched operations would bury the signal.
func (r Report) WriteText(w io.Writer) error {
	products := r.ByProduct()
	names := make([]string, 0, len(products))
	for name, sub := range products {
		if sub.Implemented > 0 || sub.Declined > 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	if _, err := fmt.Fprintf(w, "provider %s: %d operations upstream\n", r.Provider, r.Total); err != nil {
		return err
	}
	for _, name := range names {
		sub := products[name]
		if _, err := fmt.Fprintf(w, "  %-24s %3d implemented  %3d declined  %3d unknown  (of %d)\n",
			name, sub.Implemented, sub.Declined, sub.Unknown, sub.Total); err != nil {
			return err
		}
		// The version rows appear only when there is a split to show: repeating
		// a single-version product's numbers one line below them says nothing.
		versions := sub.Versions()
		if len(versions) < 2 {
			continue
		}
		for _, v := range versions {
			if _, err := fmt.Fprintf(w, "    %-22s %3d implemented  %3d declined  %3d unknown  (of %d)\n",
				v.Version, v.Implemented, v.Declined, v.Unknown, v.Total); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintf(w, "  %-24s %3d implemented  %3d declined  %3d unknown\n",
		"TOTAL", r.Implemented, r.Declined, r.Unknown)
	if err != nil {
		return err
	}
	if len(r.Orphans) > 0 {
		if _, err := fmt.Fprintf(w, "  orphan routes (no upstream match): %s\n", strings.Join(r.Orphans, ", ")); err != nil {
			return err
		}
	}
	return nil
}

// WriteList renders one operation per line, prefixed by its status.
//
// The triage view answers "what should I decide first" and truncates its lines
// to stay readable; this answers "what exactly is in there", which is the shape
// somebody needs when they are about to write a Declined() block and must not
// mistype a single name. Sorted, so two runs diff cleanly.
//
// It exists because the alternative is grepping the SDK by hand with a rule
// that approximates the scanner's, and an approximation is precisely what makes
// a route claim an operation nobody serves.
func (r Report) WriteList(w io.Writer) error {
	entries := make([]Entry, len(r.Entries))
	copy(entries, r.Entries)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Operation < entries[j].Operation })

	for _, e := range entries {
		line := fmt.Sprintf("%-12s %s", e.Status, e.Operation)
		if e.Reason != "" {
			line += " — " + e.Reason
		}
		if _, err := fmt.Fprintf(w, "%s\n", line); err != nil {
			return err
		}
	}
	return nil
}

// WriteJSON renders the machine-readable form consumed by the documentation
// site. Entries are omitted: the site only needs the counts per product, and a
// 1700-line array would bloat every page build.
func (r Report) WriteJSON(w io.Writer) error {
	type productView struct {
		Product     string `json:"product"`
		Total       int    `json:"total"`
		Implemented int    `json:"implemented"`
		Declined    int    `json:"declined"`
		Unknown     int    `json:"unknown"`
		// Versions breaks the product down per API version, absent when the
		// SDK carries none. Additive: the docs site reads the fields above and
		// ignores this one.
		Versions []VersionCount `json:"versions,omitempty"`
	}
	products := r.ByProduct()
	names := make([]string, 0, len(products))
	for name := range products {
		names = append(names, name)
	}
	sort.Strings(names)

	views := make([]productView, 0, len(names))
	for _, name := range names {
		sub := products[name]
		views = append(views, productView{
			Product:     name,
			Total:       sub.Total,
			Implemented: sub.Implemented,
			Declined:    sub.Declined,
			Unknown:     sub.Unknown,
			Versions:    sub.Versions(),
		})
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{
		"provider":    r.Provider,
		"total":       r.Total,
		"implemented": r.Implemented,
		"declined":    r.Declined,
		"unknown":     r.Unknown,
		"orphans":     r.Orphans,
		"products":    views,
	})
}

// declinedIs reports whether the pack declined this operation, whatever its
// reason says. Separated from the reason lookup so that no future edit can make
// classification depend on the text again.
func declinedIs(declined map[string]string, name string) bool {
	_, ok := declined[name]
	return ok
}
