// Package shape stores what a real cloud was observed to return, so that a
// change in the answer is as visible as a change in the surface.
//
// internal/drift reads the upstream SDK and reports what a provider *offers*.
// Nothing reported what it *returns*, and that is where this emulator was
// measurably wrong: recording a real Outscale account found ReadVms omitting
// twenty fields the cloud sends on every machine, each of which had passed the
// contract gate. That gate can only see a field the emulator invents, or a
// `required` field it omits — and the providers declare almost none: 27% of
// Outscale schemas carry a required field, 34% for Exoscale, 11% for Scaleway.
// On the rest, an omission is undetectable.
//
// What is stored here is the field tree and nothing else: paths and JSON types,
// no values, no identifiers. That is what makes it committable where a
// transcript is not — a transcript describes somebody's account, a shape
// describes an API.
//
// Two properties are load-bearing and are tested rather than assumed:
//
//   - **Shapes merge, they never overwrite.** A recording made against a sparse
//     account does not exercise every field, so a later, poorer run must not
//     erase what a richer one saw. Merge is commutative: applying two recordings
//     in either order gives the same catalogue.
//   - **Nothing volatile is stored.** No timestamp, no counter that moves on its
//     own. The drift workflow decides something changed with `git diff --quiet`,
//     so a field that moves every run would make the signal noise — the same
//     reason coverage/ carries no scan date.
package shape

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/trace"
	"github.com/stephrobert/feint/internal/transcript"
)

// Catalogue is every operation observed for one provider.
type Catalogue struct {
	Provider string `json:"provider"`
	// Operations is keyed by the upstream operation name when the recording
	// could name it, and by "METHOD path" when it could not — an operation no
	// pack claims is exactly what this is for, so it must not be dropped.
	Operations map[string]*Operation `json:"operations"`
}

// Operation is what one operation was observed to answer.
type Operation struct {
	// Method and Path are how it was reached, kept so a reader can reproduce
	// the call without guessing the route from the name.
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
	// Fields is the union of every response seen, sorted by path.
	//
	// An empty slice is not the same as a missing operation: it means the
	// operation answered and carried nothing — a list with no elements. A
	// reader that conflated the two would report a defect where there is only
	// an account with nothing to show.
	Fields []transcript.Field `json:"fields"`
	// Statuses are the HTTP statuses seen, sorted. A 4xx alongside a 200 says
	// the shape was only partly observed, which a reader should know before
	// treating it as complete.
	Statuses []int `json:"statuses,omitempty"`
}

// New returns an empty catalogue for a provider.
func New(provider string) *Catalogue {
	return &Catalogue{Provider: provider, Operations: map[string]*Operation{}}
}

// Merge folds a recording into the catalogue and reports what it changed.
//
// Every exchange with a response contributes, including one that answered
// nothing: an operation observed empty is recorded as observed, so a later
// reader can tell "this account had none" from "nobody ever called it".
func (c *Catalogue) Merge(exs []trace.Exchange) Changes {
	var ch Changes
	if c.Operations == nil {
		c.Operations = map[string]*Operation{}
	}
	for i := range exs {
		x := &exs[i]
		if x.Res == nil {
			continue
		}
		// The path is anonymised before it is used for anything: it becomes the
		// operation key when the recording could not name the operation, and it
		// is stored on the operation either way.
		path := AnonymisePath(x.Path)
		key := keyFor(x, path)
		op, known := c.Operations[key]
		if !known {
			// Fields starts as an empty slice rather than nil so the file
			// always carries `[]`. A null there would make "answered and
			// carried nothing" and "refused, shape unobserved" look alike,
			// and the difference between those two is the whole reason
			// Statuses is recorded. Measured on a real Exoscale reading where
			// one 404 wrote a null and the distinction vanished.
			op = &Operation{Method: x.Method, Path: path, Fields: []transcript.Field{}}
			c.Operations[key] = op
			ch.Added = append(ch.Added, key)
		}
		op.absorb(x, key, &ch)
	}
	ch.sort()
	return ch
}

// absorb folds one exchange into an operation.
//
// The status is always recorded; the body only when the call succeeded. An
// error body is a different schema — the same rule internal/contract states for
// its own check — and folding one in would put `Errors[].Code` into the shape of
// ReadVolumes, which is not what ReadVolumes returns. Measured on the first real
// run of tools/shapes/record.sh, which is how this rule got written.
//
// The operation still exists in the catalogue with its status, because "called
// and refused" is worth knowing: it says the shape is unobserved rather than
// empty, and a reader must not take an empty field list for a complete answer.
//
// TestAnErrorBodyIsNotTheOperationsShape fails without this.
func (o *Operation) absorb(x *trace.Exchange, key string, ch *Changes) {
	if !containsInt(o.Statuses, x.Status) {
		o.Statuses = append(o.Statuses, x.Status)
		sort.Ints(o.Statuses)
	}
	if x.Status < 200 || x.Status >= 300 {
		return
	}

	seen := map[string]string{}
	for _, f := range o.Fields {
		seen[f.Path] = f.Type
	}
	fresh := map[string]string{}
	for _, f := range transcript.FieldsOf(x.Res.Body) {
		fresh[f.Path] = f.Type
	}

	for path, typ := range fresh {
		old, known := seen[path]
		switch {
		case !known:
			seen[path] = typ
			ch.Fields = append(ch.Fields, FieldChange{Operation: key, Path: path, To: typ})
		case old == typ:
			// Nothing to say.
		default:
			merged := mergeType(old, typ)
			if merged != old {
				seen[path] = merged
				ch.Fields = append(ch.Fields, FieldChange{Operation: key, Path: path, From: old, To: merged})
			}
		}
	}

	o.Fields = o.Fields[:0]
	for path, typ := range seen {
		o.Fields = append(o.Fields, transcript.Field{Path: path, Type: typ})
	}
	sort.Slice(o.Fields, func(i, j int) bool { return o.Fields[i].Path < o.Fields[j].Path })
}

// mergeType reconciles two types seen for one field.
//
// A JSON null carries no type information: a field that was null once and a
// string another time is a string that is sometimes absent, not a conflict. So
// null yields to anything concrete, in either direction — which is what makes
// Merge commutative, and TestMergingIsCommutative fails without it.
//
// A genuine disagreement between two concrete types is kept, joined and sorted,
// rather than silently resolved. It means the API is polymorphic there or that
// one recording is wrong, and both are worth a human's eye.
func mergeType(a, b string) string {
	if a == b {
		return a
	}
	if a == "null" {
		return b
	}
	if b == "null" {
		return a
	}
	parts := map[string]bool{}
	for _, s := range append(strings.Split(a, "|"), strings.Split(b, "|")...) {
		parts[s] = true
	}
	out := make([]string, 0, len(parts))
	for s := range parts {
		out = append(out, s)
	}
	sort.Strings(out)
	return strings.Join(out, "|")
}

// keyFor names an operation. The upstream name when the recording carries one,
// the route otherwise — an operation nothing claims is precisely what a shape
// catalogue should hold, so it is never dropped for lack of a name. The path it
// is handed is the anonymised one, never the recorded one.
func keyFor(x *trace.Exchange, path string) string {
	if x.Operation != "" {
		return x.Operation
	}
	return x.Method + " " + path
}

// AnonymisePath replaces every identifier segment of a request path with
// "{id}".
//
// The catalogue is committed and a recording is not, and what makes that
// difference safe is the package's own claim: paths and types, "no values, no
// identifiers". A path stopped honouring it the moment a read list carried a
// call that addresses one resource — `GET
// /vpc/v2/regions/fr-par/private-networks/3f2a91c4-…` names an object of
// somebody's account, and it would be written twice, into Operation.Path and
// into the operation key. Nothing caught it because until #270 no read list
// held such a call.
//
// The control sits at this boundary rather than in the recorder, because the
// recorder is not the only writer: `feint shapes <recording.jsonl>` folds a
// `feint proxy` transcript, which carries whatever a real client asked for. The
// place that must hold is where a recording becomes a committed artefact.
//
// An identifier is a UUID, which is the form these providers put in a path:
// Scaleway and Exoscale address resources that way, Outscale addresses
// everything by POST /api/v1/<Action> and puts none there at all. A provider
// that adopted another spelling would slip through, which is why
// TestNoCommittedCatalogueCarriesAnIdentifier reads the files on disk instead of
// trusting this function.
//
// TestARecordedIdentifierNeverReachesTheCatalogue fails without this.
func AnonymisePath(path string) string {
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		if IsUUID(seg) {
			segments[i] = Placeholder
		}
	}
	return strings.Join(segments, "/")
}

// Placeholder is what an identifier is written as, in a catalogue path and in
// the read list that produced it. The two must be the same string or the shapes
// gate would look an operation up under a name the recorder never wrote.
const Placeholder = "{id}"

// IsUUID reports whether a string is a UUID, in the canonical 8-4-4-4-12
// hexadecimal spelling. The version and variant nibbles are not checked: what
// is being recognised is an identifier, not a conforming one, and a cloud that
// hands out an off-version UUID still hands out an identifier.
//
// Exported because internal/replay asks the same question of a recorded value
// before rebinding it to the one this emulator answered, and two spellings of
// "is this an identifier" would answer differently the day one of them learned
// a case — the duplication this repository has paid for twice.
func IsUUID(seg string) bool {
	if len(seg) != 36 {
		return false
	}
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		hex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !hex {
			return false
		}
	}
	return true
}

// Changes is what a merge did, so a caller can report it instead of asking the
// reader to diff two files by eye.
type Changes struct {
	Added  []string      `json:"added_operations,omitempty"`
	Fields []FieldChange `json:"fields,omitempty"`
}

// FieldChange is one field that appeared or changed type.
type FieldChange struct {
	Operation string `json:"operation"`
	Path      string `json:"path"`
	// From is empty when the field is new.
	From string `json:"from,omitempty"`
	To   string `json:"to"`
}

// Empty reports whether the merge learned nothing.
func (c Changes) Empty() bool { return len(c.Added) == 0 && len(c.Fields) == 0 }

func (c *Changes) sort() {
	sort.Strings(c.Added)
	sort.Slice(c.Fields, func(i, j int) bool {
		if c.Fields[i].Operation != c.Fields[j].Operation {
			return c.Fields[i].Operation < c.Fields[j].Operation
		}
		return c.Fields[i].Path < c.Fields[j].Path
	})
}

// Save writes the catalogue deterministically.
//
// Sorted keys and a trailing newline, because this file is committed and read
// by `git diff`: two runs over the same recordings must produce the same bytes,
// or every refresh reports a change that is only map ordering.
//
// TestSavingTwiceProducesTheSameBytes fails without this.
func (c *Catalogue) Save(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(c)
}

// Load reads a catalogue. A missing file is the caller's business; an
// unreadable one is an error, because silently starting from empty would let a
// corrupt file erase everything the next Save writes.
func Load(r io.Reader) (*Catalogue, error) {
	var c Catalogue
	if err := json.NewDecoder(r).Decode(&c); err != nil {
		return nil, fmt.Errorf("read the shape catalogue: %w", err)
	}
	if c.Operations == nil {
		c.Operations = map[string]*Operation{}
	}
	return &c, nil
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
