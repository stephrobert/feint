package emulator

import "strings"

// The trap CLAUDE.md opens with, given a control.
//
// `scw instance server create` reads the server types, the default image, the
// resolved image and then allocates an IP, before it creates anything. A 404 on
// any one of them fails the command with an error that names none of this. An
// emulator owns no inventory, and must serve one anyway — small, fixed, and
// present.
//
// Each pack solves that with its own table, and nothing checked it across packs.
// Declined() has such a check; the catalogues had none, so a fourth pack could
// decline its inventory, pass every unit test, and discover the consequence when
// a real client failed on its first create (#218).
//
// What a pack declares here is not documentation: the cross-pack test drives
// every entry against a fresh emulator and requires a non-empty answer, because
// a catalogue that answers `[]` fails a client exactly as a 404 does.

// CatalogueEntry is one inventory route a client reads before it can create.
type CatalogueEntry struct {
	// Method and Path are the route as mounted, so the guard can drive it.
	Method string
	Path   string
	// Reads says what a client reads it for, in a client's terms rather than the
	// pack's — "the server types `scw instance server create` sizes from". Data
	// rather than a comment, for the reason Decline.Reason is: whoever reads a
	// failure here is not holding the pack open.
	Reads string
	// Collection names the JSON key holding the list, empty when the body is a
	// bare array. The guard reads it to tell a served catalogue from an empty
	// one, which is the whole point of driving the route.
	Collection string
}

// Catalogued is the optional half of a Pack that serves an inventory.
//
// Optional in the type system, required in practice by the cross-pack guard: a
// pack serving machines that declares nothing here is reported by name. Failing
// loudly on a pack that genuinely has no inventory is the right direction — the
// author writes one line saying so, and the next pack cannot forget silently.
type Catalogued interface {
	Catalogue() []CatalogueEntry
}

// UnexplainedCatalogue returns the entries that say nothing about what they are
// for, the same discipline UnexplainedDeclines applies to a refusal.
func UnexplainedCatalogue(entries []CatalogueEntry) []string {
	var found []string
	for _, entry := range entries {
		if strings.TrimSpace(entry.Reads) == "" {
			found = append(found, entry.Method+" "+entry.Path)
		}
	}
	return found
}

// CatalogueRoutesNotMounted returns the declared entries that no route serves.
//
// This is the half that catches a decline: a pack can move an inventory route
// into Declined() and leave the declaration behind, and the result is exactly
// the trap — a catalogue that reads as served and answers 404.
func CatalogueRoutesNotMounted(entries []CatalogueEntry, routes []Route) []string {
	mounted := make(map[string]bool, len(routes))
	for _, route := range routes {
		mounted[route.Method+" "+route.Path] = true
	}
	var found []string
	for _, entry := range entries {
		if !mounted[entry.Method+" "+entry.Path] {
			found = append(found, entry.Method+" "+entry.Path)
		}
	}
	return found
}
