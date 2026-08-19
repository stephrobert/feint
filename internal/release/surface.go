// Package release answers one question about a release: what changed in the
// surface it serves, and does the release say so.
//
// The question is not rhetorical. 0.9.0 mounted `instance/v2alpha1` private
// network interfaces and its CHANGELOG contains neither `v2alpha1` nor
// `private-network-interfaces`, so the one consumer it mattered to ran v0.8.0
// and v0.9.0 side by side against the same probe to discover a 501 had become a
// 200 (#326). That is a day of a stranger's work to recover a fact this
// repository already had in a versioned artefact.
//
// And the git history is not where the fix goes, which is worth stating because
// the first reading of this incident said it was. Measured with
// `git log --diff-filter=A`, the commit that brought the family in is 992bf42,
// "feat(scaleway): instance/v2alpha1 serves the interfaces v1 creates (#257)
// (#260)" — its title names the route exactly. The work was described where
// engineers look and undescribed where consumers look, so a gate reading commit
// subjects or pull-request titles would have found nothing to complain about
// here. This one reads the CHANGELOG, because the CHANGELOG is the release
// body.
//
// "Write better release notes" is a resolution, not a control, and this
// repository has a rule about the difference. So the diff is computed rather
// than remembered: coverage/<provider>-coverage.json carries a status per
// operation, it is committed, and `git show <tag>:coverage/...` recovers the
// previous release's copy. Comparing the two says exactly which operations
// changed hands, and a note that omits one of them is factually incomplete.
//
// Three of the five transitions are consumer-visible and must be named:
//
//   - Served — absent, now implemented. The 0.9.0 case.
//   - RefusalWithdrawn — declined, now implemented. The case that costs
//     silently: the same downstream report listed three refusals it had
//     designed around (`osc/Client.CreateLoadBalancer`, `ipam/v1/API.BookIP`,
//     the `sbs_volume` type), all three of which had become features without
//     anything going red for them. A refusal withdrawn is as much a fact about
//     a release as a route added, and it is the one nobody thinks to publish.
//   - Withdrawn — implemented, now declined or gone. The dangerous direction:
//     a consumer's pipeline breaks on it, and nothing else in this repository
//     would tell them it was deliberate.
//
// The two that are not: an operation appearing upstream already declined
// (nothing a consumer could reach changed), and a declined operation
// disappearing upstream (same). They are reported, never required — a note
// listing the 137 operations the lb and vpcgw families brought in as refusals
// would train everybody to skip the section that carries the other three.
package release

import (
	"fmt"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/drift"
)

// Class is what one status change means to somebody consuming the emulator.
type Class string

const (
	// Served is an operation the previous release did not carry at all and this
	// one serves.
	Served Class = "newly served"
	// RefusalWithdrawn is an operation the previous release declined and this
	// one serves. Its own class rather than a shade of Served, because the
	// consumer harmed by it is a different one: somebody who read the refusal,
	// believed it, and built around it.
	RefusalWithdrawn Class = "refusal withdrawn"
	// Withdrawn is an operation the previous release served and this one does
	// not, whether it is now declined or gone from the surface entirely.
	Withdrawn Class = "withdrawn"
	// NewlyDeclined is an operation that appeared upstream and was triaged into
	// a refusal. Nothing a consumer could reach has changed.
	NewlyDeclined Class = "declined on arrival"
	// Dropped is a declined operation that upstream stopped publishing.
	Dropped Class = "gone from upstream"
)

// MustBeNamed reports whether a release note that omits this class is
// incomplete.
//
// A method rather than a set the callers each rebuild: the three that must be
// named and the two that must not are the whole judgement of this package, and
// a second copy of it is a second answer.
func (c Class) MustBeNamed() bool {
	return c == Served || c == RefusalWithdrawn || c == Withdrawn
}

// absent is the status of an operation the artefact does not carry. It is not
// one of drift's statuses, and it is deliberately not spelled like one: it is
// the absence of an entry, which is a different fact from any verdict a pack
// could reach about an operation.
const absent = "absent"

// Change is one operation whose status moved between two releases.
type Change struct {
	Provider  string
	Operation string
	From      string // a drift.Status, or absent
	To        string // a drift.Status, or absent
	Class     Class
	// Reason is the decline reason the newer artefact carries, empty when the
	// operation is not declined there. Printed with a withdrawal so the reader
	// sees why in the same line as what.
	Reason string
}

// Compare diffs one provider's two coverage artefacts.
//
// Both sides are entry lists as the artefact carries them; the caller is what
// recovers the older one, because the recovery is a git operation and this
// package must stay testable without one.
func Compare(provider string, before, after []drift.Entry) []Change {
	old := index(before)
	current := index(after)

	var out []Change
	for op, entry := range current {
		from := absent
		if previous, ok := old[op]; ok {
			from = string(previous.Status)
		}
		to := string(entry.Status)
		if from == to {
			continue
		}
		class, visible := classify(from, to)
		if !visible {
			continue
		}
		out = append(out, Change{
			Provider:  provider,
			Operation: op,
			From:      from,
			To:        to,
			Class:     class,
			Reason:    entry.Reason,
		})
	}
	// The operations the newer artefact no longer carries. Read from the older
	// side, so they cannot be found by walking the newer one — which is how a
	// diff comes to report additions only, and the withdrawal direction is the
	// one this exists for.
	for op, entry := range old {
		if _, ok := current[op]; ok {
			continue
		}
		class, visible := classify(string(entry.Status), absent)
		if !visible {
			continue
		}
		out = append(out, Change{
			Provider:  provider,
			Operation: op,
			From:      string(entry.Status),
			To:        absent,
			Class:     class,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Operation < out[j].Operation
	})
	return out
}

// classify names a transition. The second return distinguishes a transition
// this package has an opinion about from one it does not: an operation moving
// to or from `unknown` is untriaged drift, which `drift:check` already refuses
// and which no release note can be asked to describe.
func classify(from, to string) (Class, bool) {
	implemented := string(drift.StatusImplemented)
	declined := string(drift.StatusDeclined)
	switch {
	case from == absent && to == implemented:
		return Served, true
	case from == declined && to == implemented:
		return RefusalWithdrawn, true
	case from == implemented && (to == declined || to == absent):
		return Withdrawn, true
	case from == absent && to == declined:
		return NewlyDeclined, true
	case from == declined && to == absent:
		return Dropped, true
	default:
		return "", false
	}
}

func index(entries []drift.Entry) map[string]drift.Entry {
	out := make(map[string]drift.Entry, len(entries))
	for _, e := range entries {
		out[e.Operation] = e
	}
	return out
}

// Family is the operation string without its final segment: `lb/v1` of
// `lb/v1/ZonedAPI.CreateLB`, `osc` of `osc/Client.CreateLoadBalancer`. It is
// the string a consumer greps for when their provider moves a resource from one
// API version to another, which is the incident #326 was filed for.
func Family(operation string) string {
	if i := strings.LastIndex(operation, "/"); i > 0 {
		return operation[:i]
	}
	return operation
}

// Method is the final segment: `ZonedAPI.CreateLB`, `Client.CreateLoadBalancer`,
// or Exoscale's whole operation id, which has no receiver.
func Method(operation string) string {
	if i := strings.LastIndex(operation, "/"); i >= 0 && i+1 < len(operation) {
		return operation[i+1:]
	}
	return operation
}

// Names reports whether text names the operation.
//
// Either spelling counts: the whole operation string, or its family and its
// method both present somewhere in the text. The second form is what a note
// actually reads like — "serves `lb/v1` zoned load balancers: `ZonedAPI.CreateLB`,
// `ZonedAPI.CreateBackend`, …" — and requiring both halves is what keeps it from
// being satisfied by a family mentioned in passing, which is the failure mode of
// a gate that matches on `instance/v1` alone.
//
// Matching is on token boundaries, so `CreateIP` does not answer for
// `CreateIPs` and `instance/v1` does not answer for `instance/v1alpha1`.
func Names(text, operation string) bool {
	if containsToken(text, operation) {
		return true
	}
	family, method := Family(operation), Method(operation)
	if family == operation || method == operation {
		// No separator: family and method are the same string, and requiring
		// it twice would be requiring it once. Already answered above.
		return false
	}
	return containsToken(text, family) && containsToken(text, method)
}

// containsToken reports whether text carries token with neither an identifier
// character nor a name separator glued to it.
//
// The two boundary sets differ, and each difference is a case:
//
//   - `/` may precede (`ZonedAPI.CreateLB` inside `lb/v1/ZonedAPI.CreateLB`)
//     and may follow (`lb/v1` inside `lb/v1/ZonedAPI.CreateLB`).
//   - `.` may follow, because a token at the end of a sentence is followed by
//     a full stop, and may not precede, so a longer receiver cannot answer for
//     a shorter one.
//   - `-` may neither precede nor follow, because Exoscale's operation ids are
//     hyphenated and a prefix of one is a different operation.
func containsToken(text, token string) bool {
	if token == "" {
		return false
	}
	for offset := 0; ; {
		i := strings.Index(text[offset:], token)
		if i < 0 {
			return false
		}
		start := offset + i
		end := start + len(token)
		before := byte(' ')
		if start > 0 {
			before = text[start-1]
		}
		after := byte(' ')
		if end < len(text) {
			after = text[end]
		}
		if !identByte(before) && before != '.' && before != '-' &&
			!identByte(after) && after != '-' {
			return true
		}
		offset = start + 1
	}
}

func identByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

// Exemption is one operation the release deliberately does not name, with the
// reason it does not.
//
// The same serve-or-decline-with-a-reason discipline the packs live under,
// applied to what the project says about itself: "not worth naming" is a
// decision somebody takes and signs, never a silence.
type Exemption struct {
	Operation string `json:"operation"`
	Reason    string `json:"reason"`
}

// Verdict is what an audit found. Every change that had to be named lands in
// exactly one of the first three fields.
type Verdict struct {
	Named   []Change
	Excused []Change
	Unnamed []Change
	// Stale are exemptions that excuse nothing in this window. An exemption
	// that matches nothing is a gate that quietly stopped covering what it
	// names, which is why tools/compat/accepted.json refuses one too.
	Stale []Exemption
	// Unreasoned are exemptions carrying no reason. An exemption without one is
	// a silence with a filename.
	Unreasoned []Exemption
	// Reported are the changes no note has to carry, kept so a run says what it
	// saw rather than only what it refused.
	Reported []Change
}

// OK reports whether the release may be cut.
func (v Verdict) OK() bool {
	return len(v.Unnamed) == 0 && len(v.Stale) == 0 && len(v.Unreasoned) == 0
}

// Audit judges a window: the changes it found, the changelog text that is
// allowed to name them, and the exemptions somebody signed.
func Audit(changes []Change, section string, exemptions []Exemption) Verdict {
	excused := make(map[string]Exemption, len(exemptions))
	var verdict Verdict
	for _, e := range exemptions {
		if strings.TrimSpace(e.Reason) == "" {
			verdict.Unreasoned = append(verdict.Unreasoned, e)
			continue
		}
		excused[e.Operation] = e
	}

	used := make(map[string]bool, len(excused))
	for _, change := range changes {
		if !change.Class.MustBeNamed() {
			verdict.Reported = append(verdict.Reported, change)
			continue
		}
		switch {
		case Names(section, change.Operation):
			verdict.Named = append(verdict.Named, change)
		case excused[change.Operation].Operation != "":
			used[change.Operation] = true
			verdict.Excused = append(verdict.Excused, change)
		default:
			verdict.Unnamed = append(verdict.Unnamed, change)
		}
	}

	for _, e := range exemptions {
		if strings.TrimSpace(e.Reason) == "" || used[e.Operation] {
			continue
		}
		verdict.Stale = append(verdict.Stale, e)
	}
	sort.Slice(verdict.Stale, func(i, j int) bool { return verdict.Stale[i].Operation < verdict.Stale[j].Operation })
	return verdict
}

// Section returns the part of a CHANGELOG that is allowed to name what changed
// since a version.
//
// Everything above `## [<version>]`, which is every section describing work
// done after that tag was cut: the Unreleased one while a train is running, and
// the version's own heading once somebody renames it to cut the release. The
// release body is that same text — .github/workflows/release.yml reads the
// section between two `## [` headings — so a gate satisfied here is a gate
// satisfied on what a reader downloads.
//
// A missing heading is an error rather than a whole-file scan. Scanning the
// whole file would make every operation ever mentioned count as named, which is
// a gate that passes by being pointed at the wrong thing — and this repository
// has already published one control that measured its own absence.
func Section(changelog, version string) (string, error) {
	heading := "## [" + version + "]"
	above, _, found := strings.Cut(changelog, heading)
	if !found {
		return "", fmt.Errorf("no %q section in the changelog: nothing marks where the window opens, "+
			"so there is no text this gate could honestly read", heading)
	}
	return above, nil
}
