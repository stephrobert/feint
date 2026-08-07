package drift

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// The versioned coverage artefact, written and read through one type.
//
// It used to be written from a map literal here and read by a struct declared in
// internal/cli, which is the copy pattern that already cost this repository a
// command: the conformance view was declared twice, the two disagreed on one key,
// every decode failed into a zero value and `feint status` reported 0 for the
// number it exists to report. The reader's own comment argued that a separate
// declaration would "fail loudly if the writer changes shape". It did not fail
// loudly. Nothing in encoding/json can: an unknown field is dropped and a missing
// one is a zero.
//
// So the file format is a type, both ends use it, and a field that moves is a
// compile error somewhere rather than a silent zero everywhere.

// CoverageFile is coverage/<provider>-coverage.json.
type CoverageFile struct {
	Provider    string        `json:"provider"`
	Total       int           `json:"total"`
	Implemented int           `json:"implemented"`
	Declined    int           `json:"declined"`
	Unknown     int           `json:"unknown"`
	Orphans     []string      `json:"orphans"`
	Products    []ProductView `json:"products"`
	// Entries is the per-operation verdict, sorted by operation name.
	//
	// It carries the reason a declined operation is declined, and that is the
	// point of including it. `Declined()` holds a written argument for each of
	// the operations a pack refuses — 111 of them on one product alone — and
	// until now the only way to read one was `feint coverage --format list`,
	// which needs an SDK checkout and a scan. Shipping the verdicts with the
	// counts turns a bare number into the decisions it is made of.
	//
	// It is also what stops the page recomputing the comparison. The join
	// between the upstream surface, what the packs serve and what they decline
	// is Compare's, and a second implementation of it in a browser would be a
	// second answer nobody could reconcile with `feint coverage`.
	//
	// What must never join it is a timestamp. The weekly workflow decides that
	// the upstream surface moved by running `git diff --quiet -- coverage/`, so a
	// field that changes on every run would open a drift pull request every
	// Monday whether or not anything upstream had moved, and the mechanism this
	// project rests on would become the noise everybody filters out.
	Entries []Entry `json:"entries"`
}

// ProductView is one upstream product's counts inside a CoverageFile.
type ProductView struct {
	Product     string `json:"product"`
	Total       int    `json:"total"`
	Implemented int    `json:"implemented"`
	Declined    int    `json:"declined"`
	Unknown     int    `json:"unknown"`
	// Versions breaks the product down per API version, absent when the SDK
	// carries none. Additive: the documentation site reads the fields above and
	// ignores this one.
	Versions []VersionCount `json:"versions,omitempty"`
}

// File renders the report as the artefact.
func (r Report) File() CoverageFile {
	products := r.ByProduct()
	names := make([]string, 0, len(products))
	for name := range products {
		names = append(names, name)
	}
	sort.Strings(names)

	views := make([]ProductView, 0, len(names))
	for _, name := range names {
		sub := products[name]
		views = append(views, ProductView{
			Product:     name,
			Total:       sub.Total,
			Implemented: sub.Implemented,
			Declined:    sub.Declined,
			Unknown:     sub.Unknown,
			Versions:    sub.Versions(),
		})
	}

	// Sorted here rather than left in scan order, because the artefact is
	// committed: two runs over an unchanged SDK must produce the same bytes, or
	// the drift gate reports a change every time somebody refreshes it.
	entries := make([]Entry, len(r.Entries))
	copy(entries, r.Entries)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Operation < entries[j].Operation })

	return CoverageFile{
		Provider:    r.Provider,
		Total:       r.Total,
		Implemented: r.Implemented,
		Declined:    r.Declined,
		Unknown:     r.Unknown,
		Orphans:     r.Orphans,
		Products:    views,
		Entries:     entries,
	}
}

// WriteJSON renders the machine-readable form the documentation site, the docs
// command and the emulator's own page all read.
func (r Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r.File())
}

// LoadCoverage reads one artefact.
//
// An artefact with no provider name is refused rather than returned empty: the
// glob that finds these files matches on a suffix, and a JSON file that happens
// to be called *-coverage.json is not one. Failing here is what stops the
// caller rendering a table of zeroes.
func LoadCoverage(r io.Reader) (CoverageFile, error) {
	var out CoverageFile
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		return CoverageFile{}, err
	}
	if out.Provider == "" {
		return CoverageFile{}, fmt.Errorf("no provider name; the artefact is not a coverage report")
	}
	return out, nil
}
