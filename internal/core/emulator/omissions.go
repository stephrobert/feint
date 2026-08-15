package emulator

import (
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/transcript"
)

// The omission half of the contract check (#88).
//
// Validate catches what a response invents; nothing caught what it forgot,
// because an absent field only violates a schema when the provider declared it
// required, and the providers barely do — 9% of Scaleway's schemas, 27% of
// Outscale's. The gap was not theoretical: ReadVms served twenty fields short
// of the real cloud, ReadImages three, one of which segfaulted the Terraform
// provider (#86), and each was found only because a recording of a real
// account happened to cover that operation.
//
// Two upstream sources speak here, and the verdict depends on both, because
// each alone was measured wrong in its own direction:
//
//   - The provider's API description declares every field a response may
//     carry (contract.ResponseFields). Alone it over-declares: held against
//     this emulator's first instrumented conformance run, 83 of the 106
//     declared-but-absent fields a recording could arbitrate were absent from
//     the real cloud's answer too — pagination tokens that only appear when a
//     further page exists, client tokens echoed only when sent. A gate red on
//     all of that is a gate somebody turns off.
//   - The recorded shapes (shapes/) prove what a real account's answers
//     actually carried. Alone, offline against an empty store, they could not
//     see a single element field: every list is empty there, so
//     `feint shapes --check` skips the elements — which is why twenty fields
//     of ReadVms passed it.
//
// So: a field both declared by the document and observed on the real cloud,
// missing from the populated answers a conformance run produced, fails the
// gate (Missing). A field only the document declares is published as
// Unconfirmed — visible, actionable by recording that operation, never a
// failure, because no source independent of this emulator says it should have
// been there. The traffic that makes either comparison meaningful is the
// conformance run's: real clients create resources, so the answers carry the
// populated objects the offline check never had.
//
// The rules that keep the comparison honest, each measured on the first run:
//
//   - the union of every answer counts, so a field present in any response of
//     the run is served, whatever a sparser answer omitted;
//   - a field below an absent or null container is not reported: the omission
//     is the container's, and the real clouds answer null for an unset object;
//   - an element path ("servers[]") is never reported: an empty list is a
//     state of the store, not a defect of the view;
//   - a pack may argue an absence is a decision (DeclinedFields), which moves
//     it to Excused, printed with its reason; a decline whose field the run
//     demonstrably served is published stale, so the excused list cannot rot
//     into fiction.

// FieldGaps is the verdict of the omission check, published on
// /_feint/conformance for the gate (tools/conformance/score.sh) to read.
type FieldGaps struct {
	// Compared names the operations the check could hold to the document: a
	// response schema on one side, at least one decoded 2xx answer on the
	// other. Everything absent from this list is exactly that — unchecked, not
	// clean — the same distinction the contract axis draws.
	Compared []string `json:"compared"`
	// Missing maps an operation to the fields no answer of this run carried
	// although both sources vouch for them — declared by the provider's
	// document, observed on the real cloud — each as "path: declared type".
	// This is the map the gate fails on.
	Missing map[string][]string `json:"missing"`
	// Unconfirmed maps an operation to the declared-but-absent fields no
	// recording arbitrates: the document says they may exist, nothing proves
	// the real cloud serves them, and the measured base rate says four of
	// five such fields are absent from the real answer too. Published so the
	// blind spot has a name — each entry is exactly one recording away from
	// becoming either a Missing or nothing — and never failed on.
	Unconfirmed map[string][]string `json:"unconfirmed"`
	// Excused maps an operation to the declared-but-absent fields a pack
	// declines on purpose, each with its reason. Printed, never failed on:
	// what the gate subtracts must stay visible.
	Excused map[string][]string `json:"excused"`
	// StaleDeclines lists field declines that argue for an omission the
	// emulator does not have: the field was served in this very run. Each one
	// is a decision that outlived its subject, and the gate fails on them.
	StaleDeclines []string `json:"stale_declines"`
}

// recordServed folds one decoded 2xx answer into the union of what this
// operation was seen to serve. The union, not the last answer: a list serves
// its element fields only while the store holds elements, and a later empty
// answer must not erase what a populated one proved.
//
// The type is kept, not just the presence, because the container rule below
// needs it: a field served as null is present, and nothing below it is
// observable. upgradeType keeps the most container-like type seen, so one
// populated answer among many null ones is what counts.
func (o *observer) recordServed(operation string, decoded any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	seen := o.served[operation]
	if seen == nil {
		seen = map[string]string{}
		o.served[operation] = seen
	}
	for _, f := range transcript.FieldsOf(decoded) {
		seen[f.Path] = upgradeType(seen[f.Path], f.Type)
	}
}

// upgradeType reconciles two observations of one path. A container wins over a
// scalar and anything wins over null, because what the union feeds is the
// question "could this path's children ever have been observed": one answer
// where the container was populated says yes, however many nulls surround it.
func upgradeType(old, new string) string {
	switch {
	case old == "" || old == "null":
		return new
	case new == "object" || new == "array":
		return new
	default:
		return old
	}
}

// servedCopy hands the accumulated unions out from under the lock.
func (o *observer) servedCopy() map[string]map[string]string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make(map[string]map[string]string, len(o.served))
	for op, fields := range o.served {
		c := make(map[string]string, len(fields))
		for f, t := range fields {
			c[f] = t
		}
		out[op] = c
	}
	return out
}

// fieldGaps compares every operation's served union with what its contract
// declares, and files each absence as missing, excused, or — for a decline
// arguing against a field the run served — stale.
func (s *Server) fieldGaps(served map[string]map[string]string) FieldGaps {
	gaps := FieldGaps{
		Compared:      []string{},
		Missing:       map[string][]string{},
		Unconfirmed:   map[string][]string{},
		Excused:       map[string][]string{},
		StaleDeclines: []string{},
	}
	for _, p := range s.packs {
		doc := s.observer.contracts[p.Name()]
		if doc == nil {
			continue
		}
		declines := FieldDeclinesOf(p)
		for _, r := range p.Routes() {
			seen, answered := served[r.Operation]
			if !answered {
				continue
			}
			_, name, known := doc.OperationFor(r.Operation)
			if !known {
				continue
			}
			expected := doc.ResponseFields(name)
			if len(expected) == 0 {
				continue
			}
			gaps.Compared = append(gaps.Compared, r.Operation)
			compareFields(&gaps, r.Operation, expected, seen, s.observedFields[r.Operation], declines)
		}
	}
	sort.Strings(gaps.Compared)
	sort.Strings(gaps.StaleDeclines)
	return gaps
}

// compareFields holds one operation's served union to its declared fields,
// with the recorded real answer as the arbiter of which absences fail.
func compareFields(gaps *FieldGaps, operation string, expected map[string]string, seen map[string]string, observed map[string]bool, declines []FieldDecline) {
	for _, path := range contract.SortedFieldPaths(expected) {
		if _, present := seen[path]; present {
			continue
		}
		// An element that never appeared is an empty list, which is the
		// store's state, not the view's defect — the shapes gate's rule,
		// TestAnEmptyListIsNotAnOmission here.
		if strings.HasSuffix(path, "[]") {
			continue
		}
		// A field below a container that was never served populated: absent,
		// the omission is the container's, already reported at its own level;
		// served null, nothing below it is observable, and the real clouds do
		// answer null for an unset object — shapes/scaleway.json records
		// `default_bootscript: null` from a real account, and the first run of
		// this check accused all twelve of its children before this rule
		// (TestANullContainerDoesNotAccuseItsChildren,
		// TestAMissingContainerIsOneOmissionNotMany).
		if !containersPopulated(seen, ancestorsOf(path)) {
			continue
		}
		if i := matchingFieldDecline(declines, operation, path); i >= 0 {
			gaps.Excused[operation] = append(gaps.Excused[operation],
				path+": "+declines[i].Reason)
			continue
		}
		// Only an absence both sources vouch for fails; the document alone
		// files it as unconfirmed. The 4-to-1 base rate behind that split is
		// in the package comment, and TestADeclaredFieldNobodyObservedDoesNotFail
		// holds the line.
		if observed[path] {
			gaps.Missing[operation] = append(gaps.Missing[operation],
				path+": "+expected[path])
			continue
		}
		gaps.Unconfirmed[operation] = append(gaps.Unconfirmed[operation],
			path+": "+expected[path])
	}
	// A decline is provably stale when the run served the very field it argues
	// the emulator does not have. Only provably: an operation this run never
	// compared says nothing, and failing on it would make the gate flap with
	// the traffic (TestAProvablyStaleFieldDeclineIsPublished).
	for _, d := range declines {
		if d.Operation != operation {
			continue
		}
		for path := range seen {
			if d.Matches(operation, path) {
				gaps.StaleDeclines = append(gaps.StaleDeclines,
					d.Operation+" "+d.Path+": the emulator serves this field")
				break
			}
		}
	}
}

// ancestorsOf lists every container a field sits in, outermost first:
// "servers[].image.id" -> ["servers", "servers[]", "servers[].image"].
func ancestorsOf(path string) []string {
	segments := strings.Split(path, ".")
	var out []string
	prefix := ""
	for _, seg := range segments[:len(segments)-1] {
		if prefix != "" {
			prefix += "."
		}
		if strings.HasSuffix(seg, "[]") {
			out = append(out, prefix+strings.TrimSuffix(seg, "[]"))
		}
		prefix += seg
		out = append(out, prefix)
	}
	return out
}

// containersPopulated reports whether every ancestor was served as a real
// container — an object or an array — at least once. A null or scalar there
// means the branch below it was never observable.
func containersPopulated(seen map[string]string, paths []string) bool {
	for _, p := range paths {
		if t := seen[p]; t != "object" && t != "array" {
			return false
		}
	}
	return true
}

func matchingFieldDecline(declines []FieldDecline, operation, path string) int {
	for i, d := range declines {
		if d.Matches(operation, path) {
			return i
		}
	}
	return -1
}
