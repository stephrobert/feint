package corpus

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/core/sshkey"
	"github.com/stephrobert/feint/internal/trace"
	"github.com/stephrobert/feint/internal/transcript"
)

// A sanitiser is a rule, and a rule is a claim about a file. These two read the
// file.
//
// [Scan] refuses every value outside the alphabet a sanitised transcript may
// contain — an allowlist over the output, not a search for shapes that look
// dangerous, because a search answers "does this look like a secret" and never
// "is this not one". [Audit] answers the other question, which no rule about
// the output can: did anything of the *source* survive? It is the step
// docs/proxy.md's own procedure ends on — "search the result for the account's
// own identifiers one last time" — and the step the audit's fixture skipped.

// Leak is one value that must not be published, named by where it sits.
type Leak struct {
	// Line is the exchange's position in the transcript, from 1.
	Line int
	// Where is the position inside that exchange: "path", "query[zone]",
	// "res.body.server.public_ip.address".
	Where string
	// Why says which rule refused it.
	Why string
	// Value is the offending value. It is filled by [Scan], which reads a file
	// somebody is about to commit — naming what to delete is the point — and
	// left empty by [Audit], which holds the source recording and has no
	// business copying a tenant's value into a report or a CI log.
	Value string
}

func (l Leak) String() string {
	at := fmt.Sprintf("line %d, %s: %s", l.Line, l.Where, l.Why)
	if l.Value != "" {
		return at + ": " + strconv.Quote(l.Value)
	}
	return at
}

// Scan reports every value in a transcript that is not one a sanitised
// transcript may carry.
//
// The alphabet is closed: a synthetic value this package minted, a literal the
// provider's own document states, a word a pack vouches for, a boolean, a short
// run of digits, an HTTP header's own vocabulary, the proxy's "REDACTED", or
// nothing at all. Anything else is reported — including values that look
// perfectly innocent, because "looks innocent" is the judgement this scan
// exists to refuse.
func Scan(exs []trace.Exchange, opt Options) []Leak {
	a := newAlphabet(opt)
	var out []Leak
	for i := range exs {
		for _, v := range located(&exs[i]) {
			if why := a.refuse(v); why != "" {
				out = append(out, Leak{Line: i + 1, Where: v.where, Why: why, Value: v.value})
			}
		}
	}
	return out
}

// Audit cross-references a sanitised transcript against the recording it was
// made from, and reports every value of the source that survived into it.
//
// This is the control that does not depend on the sanitiser being right about
// what a value *is*. A rule that failed to recognise an identifier, a shape a
// fourth provider invents, a name in a field nobody thought about: all of them
// come back here, because the question asked is only "was this string in the
// recording".
//
// What may legitimately be in both is exactly what the sanitiser is allowed to
// keep, and that list is short and stated in one place.
func Audit(source, sanitised []trace.Exchange, opt Options) []Leak {
	a := newAlphabet(opt)
	from := map[string]bool{}
	for i := range source {
		for _, v := range located(&source[i]) {
			from[v.value] = true
		}
	}
	var out []Leak
	for i := range sanitised {
		for _, v := range located(&sanitised[i]) {
			if !from[v.value] || a.mayKeep(v) {
				continue
			}
			out = append(out, Leak{Line: i + 1, Where: v.where,
				Why: "this value was read from the recording and survived into the artefact"})
		}
	}
	return out
}

// alphabet is what a sanitised transcript may contain.
type alphabet struct {
	vocabulary map[string]bool
	literals   map[string]bool
	needles    map[string]bool
}

func newAlphabet(opt Options) *alphabet {
	a := &alphabet{
		vocabulary: map[string]bool{},
		literals:   map[string]bool{},
		needles:    map[string]bool{},
	}
	for v := range allowedValues(opt) {
		a.vocabulary[v] = true
	}
	for _, seg := range literalSegments(opt.Doc) {
		a.literals[seg] = true
	}
	for _, n := range transcript.Needles() {
		a.needles[n] = true
	}
	return a
}

// mayKeep reports whether this position is allowed to hold the value it was
// read with.
//
// It is the list [mint.keepable] applies, read back off the file rather than
// off the rules that wrote it, plus the two positions only a transcript has
// (a literal path segment, an HTTP header's own vocabulary). The default route
// is here for the reason [mint.keepable] states: there is one of it per family
// and it names nothing of the account, so it is the one value the sanitiser
// cannot replace and [Audit] must not refuse.
func (a *alphabet) mayKeep(v value) bool {
	switch {
	case v.value == "", isMintedPlaceholder(v.value):
		return true
	case a.vocabulary[v.value]:
		return true
	case v.value == "true", v.value == "false":
		return true
	case isShortDigits(v.value):
		return true
	case isDefaultRoute(v.value):
		return true
	case v.kind == kindPath && a.literals[v.value]:
		return true
	case v.kind == kindHeader && httpVocabulary[strings.ToLower(v.name)]:
		return true
	case v.kind == kindHeader && strings.EqualFold(v.name, "user-agent") && a.needles[v.value]:
		return true
	}
	return false
}

// refuse names the rule a value breaks, or "" when it breaks none.
func (a *alphabet) refuse(v value) string {
	if a.mayKeep(v) {
		return ""
	}
	if synthetic(v.value) {
		return ""
	}
	return "not a value a sanitised transcript may carry"
}

// Minted reports whether a value is one this package invented.
//
// Exported for the direction that reads a committed corpus back and reissues it
// at the provider it was recorded from (#359). There, a value the sanitiser
// minted is a value the *cloud* never said, so a refusal landing on a request
// that still carries one is a defect of the instrument before it is anything
// about the cloud — the class #73 measured as nine false divergences and #354
// as four more. The question belongs here, where the answer is regenerated from
// the rules that wrote the value, and not in a caller re-spelling the alphabet.
func Minted(s string) bool { return synthetic(s) }

// synthetic reports whether a value is one this package mints.
//
// Each arm recognises the exact form [mint] writes, rather than a family it
// belongs to: "any UUID" would accept the account's, and "any address" would
// accept the tenant's network.
func synthetic(s string) bool {
	switch {
	case strings.HasPrefix(s, Token):
		_, err := strconv.Atoi(strings.TrimPrefix(s, Token))
		return err == nil
	case strings.HasPrefix(s, syntheticUUIDPrefix):
		_, err := strconv.Atoi(strings.TrimPrefix(s, syntheticUUIDPrefix))
		return err == nil
	case isCIDR(s):
		p, _ := netip.ParsePrefix(s)
		return syntheticV4.Overlaps(p) || syntheticV6.Overlaps(p)
	case netmaskBits(s) != 0:
		// The one arm that recognises a family rather than the exact form
		// [mint] writes, and the reason it may: [maskOf] maps 1..32 onto
		// itself, so "the mask this package would have minted" is every mask
		// there is and regenerating it proves nothing. That is admissible here
		// and nowhere else, because the family is a closed set of thirty-two
		// values, none of which can carry an identifier, an address or a name.
		// [Audit] is still the tooth: a mask of the recording that survived is
		// refused there, since [alphabet.mayKeep] does not list this.
		return true
	case isIP(s):
		addr, _ := netip.ParseAddr(s)
		return syntheticV4.Contains(addr) || syntheticV6.Contains(addr)
	case isTimestamp(s):
		t, _ := time.Parse(time.RFC3339, s)
		return !t.Before(corpusEpoch) && t.Sub(corpusEpoch) < time.Hour*24
	case sshkey.Valid(s):
		return isSyntheticKey(s)
	case isPrefix(s):
		// The "i-<hex>" family, minted as a counter in eight hexadecimal
		// characters, so the first two are always zero. A real one starts "00"
		// once in 256, and that residual is why this is not the only tooth:
		// TestNoCommittedCorpusCarriesAnIdentifier (internal/cli) reads
		// the bytes.
		hex := s[strings.LastIndex(s, "-")+1:]
		return len(hex) == 8 && strings.HasPrefix(hex, "00")
	}
	return false
}

const syntheticUUIDPrefix = "00000000-0000-4000-8000-"

// isSyntheticKey reproduces the key this package would have minted for the
// counter written in its comment, and compares. Regenerating rather than
// pattern-matching: the comparison is then exact, and a key whose material was
// somebody's is refused however plausible its comment looks.
func isSyntheticKey(s string) bool {
	parsed, err := sshkey.Parse(s)
	if err != nil || !strings.HasPrefix(parsed.Comment, Token) {
		return false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(parsed.Comment, Token))
	if err != nil {
		return false
	}
	return syntheticKey(n) == strings.TrimSpace(s)
}

// literalSegments is every path segment the provider's document writes itself.
//
// A flat set rather than a per-path alignment, deliberately: this is the scan,
// and reproducing the sanitiser's own reasoning here would make the two agree
// about a mistake. The set is wider than the alignment — it accepts "servers"
// in any path — and it is still closed, because it comes from the document and
// not from the recording.
func literalSegments(doc *contract.Doc) []string {
	if doc == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, op := range doc.Operations {
		for _, seg := range strings.Split(doc.PathPrefix+op.Path, "/") {
			if seg == "" || strings.Contains(seg, "{") {
				continue
			}
			seen[seg] = true
		}
	}
	out := make([]string, 0, len(seen))
	for seg := range seen {
		out = append(out, seg)
	}
	sort.Strings(out)
	return out
}

// kind says what a value is, because the rules differ by position: a path
// segment may be a literal of the API, a header value may be HTTP's own word,
// and a body string may be neither.
type kind int

const (
	kindPath kind = iota
	kindQuery
	kindHeader
	kindBody
	kindHost
)

// value is one string of a transcript and where it sits.
type value struct {
	kind  kind
	where string
	name  string
	value string
}

// located walks one exchange and returns every string a reader could publish.
//
// One walk for both the scan and the audit, so the two cannot disagree about
// which positions exist — a field visited by one and not the other is a field
// nothing checks, and it would be invisible.
func located(x *trace.Exchange) []value {
	var out []value
	if x.Host != "" {
		out = append(out, value{kind: kindHost, where: "host", value: x.Host})
	}
	for _, seg := range strings.Split(x.Path, "/") {
		if seg == "" {
			continue
		}
		// A dialect that writes the verb in the identifier's segment is split
		// here too, so the identifier faces the rules and the verb faces them
		// as a literal of the document.
		if id, action, found := strings.Cut(seg, ":"); found {
			out = append(out,
				value{kind: kindPath, where: "path", value: id},
				value{kind: kindPath, where: "path", value: action})
			continue
		}
		out = append(out, value{kind: kindPath, where: "path", value: seg})
	}
	if x.Query != "" {
		for _, pair := range strings.Split(x.Query, "&") {
			name, v, hasValue := strings.Cut(pair, "=")
			if !hasValue {
				continue
			}
			out = append(out, value{kind: kindQuery, where: "query[" + name + "]", name: name, value: v})
		}
	}
	out = append(out, fromMessage("req", x.Req)...)
	out = append(out, fromMessage("res", x.Res)...)
	return out
}

func fromMessage(side string, m *trace.Message) []value {
	if m == nil {
		return nil
	}
	var out []value
	names := make([]string, 0, len(m.Headers))
	for name := range m.Headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out = append(out, value{kind: kindHeader, where: side + ".headers[" + name + "]",
			name: name, value: m.Headers[name]})
	}
	return append(out, fromBody(side+".body", m.Body)...)
}

func fromBody(path string, v any) []value {
	switch body := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(body))
		for k := range body {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var out []value
		for _, k := range keys {
			out = append(out, fromBody(path+"."+k, body[k])...)
		}
		return out
	case []any:
		var out []value
		for _, item := range body {
			out = append(out, fromBody(path+"[]", item)...)
		}
		return out
	case string:
		return []value{{kind: kindBody, where: path, value: body}}
	default:
		return nil
	}
}
