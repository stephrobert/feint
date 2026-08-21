// Package corpus turns a recording of a real cloud into an artefact this
// repository may commit.
//
// `feint replay` (#73) compares this emulator's answer with a recorded one, and
// it has only ever been proved on the identity case: a recording made against
// the emulator, replayed against the emulator. It has never met a real cloud's
// answer, and it could not, because there was no corpus of real recordings and
// no way to commit one (#351). A transcript is the inventory of somebody's
// account — docs/proxy.md says so field by field — and `shapes/*.json`, the one
// committable thing derived from a recording, throws away exactly what a replay
// grades beyond the field tree: the status, the order, and the sequence itself.
//
// # What is kept and what goes
//
//	kept                                    dropped
//	method, status, order, sequence         every identifier and address
//	the literal segments of the path        every value the cloud minted
//	query parameter names                   every name, tag and free text
//	body field names and JSON types         the recording's own timings
//	numbers, booleans, nulls                every header value but the HTTP ones
//	what a pack vouches for as vocabulary   everything else
//
// # Why a sanitised transcript is still replayable
//
// Because the replay already rebinds. It learns, from each answer, which
// identifier this emulator minted in place of the recorded one, and substitutes
// it into every later request. A transcript whose identifiers are synthetic is
// therefore replayed exactly like one whose identifiers are real: the replay
// binds "00000000-0000-4000-8000-000000000003" to whatever this emulator
// answers, the same way it binds a UUID a cloud handed out.
//
// That is what makes the substitution shape-preserving rather than blanket. A
// UUID becomes a UUID, an address an address, a CIDR a CIDR of the same prefix
// length, an OpenSSH public key a valid OpenSSH public key. A value replaced by
// a bare "REDACTED" would break the request that carries it and retype the
// field that holds it — the defect #73 measured on the proxy's own redaction,
// where nine null fields became strings and read back as nine divergences.
//
// # Default deny
//
// Partial sanitisation is worse than none, because it reads as safe: Pépin's
// delivery audit opened on a real instance UUID left in a fixture whose IP
// address had been scrubbed, and the scrubbing is what made the file look
// reviewed. So the rule here is not a list of what to remove — a redaction by
// name answers "does this look like a secret" and never "is this not one" — but
// a list of what may stay:
//
//   - a segment the provider's own document states is a literal of that path;
//   - a value a pack vouches for as its API's closed vocabulary
//     (emulator.Vocabulary: Scaleway's zones and regions, which it answers 400
//     for);
//   - a short run of digits, a boolean word, an empty string, and the proxy's
//     own "REDACTED";
//   - an HTTP header value whose name is HTTP's own vocabulary.
//
// Everything else is replaced, including the segments of a path the document
// does not describe: the corpus then carries the exchange, its status and its
// field tree, and cannot name where it went. That is a loss, it is counted in
// [Report.Unnamed], and the way to recover it is to extend the provider's
// contract rather than to guess which segments looked harmless.
//
// # The scan does not trust any of the above
//
// [Scan] reads the artefact that is about to be committed and refuses every
// value outside the alphabet a sanitised transcript may contain, and [Audit]
// cross-references the output against the source recording so that no value of
// the account survives anywhere in it. Neither reads the rules that produced
// the file: they read the file.
package corpus

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math/bits"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/core/sshkey"
	"github.com/stephrobert/feint/internal/proxy"
	"github.com/stephrobert/feint/internal/shape"
	"github.com/stephrobert/feint/internal/trace"
	"github.com/stephrobert/feint/internal/transcript"
)

// Options is what a sanitisation needs.
type Options struct {
	// Doc is the provider's own API description, and it is required.
	//
	// It is the only thing that can say which segments of a path are literals
	// of the API and which are the account's: "/vpc/v2/regions/{region}/
	// private-networks/{private_network_id}" names one of each. Deriving that
	// from the mounted routes instead would cover the served operations only,
	// and a recording's most interesting lines are the ones nothing serves yet.
	Doc *contract.Doc
	// Vocabulary is what the packs vouch for, gathered by the caller because it
	// is the caller that holds the packs (emulator.VocabularyOf).
	Vocabulary []string
}

// Report is what a sanitisation did, in numbers a reader can check.
type Report struct {
	Exchanges int `json:"exchanges"`
	// Replaced is how many distinct values were given a synthetic one.
	Replaced int `json:"replaced"`
	// Kept is how many values were left verbatim, all reasons together.
	Kept int `json:"kept"`
	// Unnamed lists the exchanges whose path the provider's document does not
	// describe, as "METHOD <sanitised path>" — never the path that was read.
	// They are carried, not dropped: a total that shrinks in silence is the
	// failure `feint shapes --check` has a paragraph about.
	Unnamed []string `json:"unnamed,omitempty"`
}

// Sanitise rewrites a recording so that it may be committed.
//
// The order of the exchanges is the transcript's causality and is preserved
// exactly; so is the order of every list inside a body, because that order is
// what an emulator.InvariantOrder declaration grades.
func Sanitise(exs []trace.Exchange, opt Options) ([]trace.Exchange, Report, error) {
	if opt.Doc == nil {
		return nil, Report{}, fmt.Errorf("sanitising needs the provider's contract: naming the literal segments of a path is the document's job, and a path it does not describe is one this tool must blank rather than guess at")
	}
	m := newMint(opt)
	m.learnAddressOrder(exs)
	m.planBlocks(exs)
	out := make([]trace.Exchange, len(exs))
	var rep Report
	for i := range exs {
		x := exs[i]
		path, named := m.path(x.Method, x.Path, opt.Doc)
		if !named {
			rep.Unnamed = append(rep.Unnamed, x.Method+" "+path)
		}
		x.Path = path
		x.Query = m.query(x.Query)
		x.Host = m.value(x.Host)
		x.Req = m.message(x.Req)
		x.Res = m.message(x.Res)
		// The clock and the stopwatch go. Neither is compared by a replay, both
		// move on their own, and internal/shape already states the rule for a
		// committed artefact: nothing volatile, or `git diff --quiet` turns the
		// signal into noise. The sequence number stays, because it is a position
		// and a reader looks a finding up by it.
		x.At = corpusEpoch.Add(time.Duration(i) * time.Second)
		x.Ms = 0
		out[i] = x
		rep.Exchanges++
	}
	if len(m.collided) > 0 {
		return nil, Report{}, fmt.Errorf("the substitution handed out %d replacement(s) twice, so two values of the recording would read as one; this is a defect of a shape rule, not of the recording", len(m.collided))
	}
	rep.Replaced = len(m.to)
	rep.Kept = m.kept
	sort.Strings(rep.Unnamed)
	rep.Unnamed = dedupe(rep.Unnamed)
	return out, rep, nil
}

// corpusEpoch is the instant every sanitised transcript starts at. A fixed date
// rather than the recording's own: two runs of the same session then produce the
// same bytes, and the file says nothing about when its author was working.
var corpusEpoch = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// mint hands out one synthetic value per distinct original, and remembers the
// pairing so the whole transcript stays consistent with itself: the identifier a
// create answered is the identifier the next read addresses, which is the only
// reason a sanitised transcript replays at all.
type mint struct {
	to      map[string]string
	issued  map[string]bool
	allowed map[string]bool
	kept    int
	// collided names every replacement handed out twice. Never empty in
	// silence: [Sanitise] refuses the whole run rather than write a transcript
	// in which two objects of the account have become one.
	collided []string
	// counters are per class, so a reader of the artefact can see at a glance
	// that the fourth UUID is the fourth UUID.
	n map[string]int
	// rank is the position of each address of the recording among the
	// addresses of its own family, once they are sorted. See
	// [mint.learnAddressOrder].
	rank map[string]int
	// block is the replacement chosen for each CIDR of the recording, decided
	// in one pass before anything is written so that a block inside another
	// block stays inside it. See [mint.planBlocks].
	block map[string]string
}

func newMint(opt Options) *mint {
	return &mint{to: map[string]string{}, issued: map[string]bool{}, allowed: allowedValues(opt),
		n: map[string]int{}, rank: map[string]int{}, block: map[string]string{}}
}

// planBlocks chooses the replacement for every CIDR of the recording at once,
// so that a block the recording nests inside another stays nested.
//
// # Why containment is part of the shape
//
// The shape of a value is whatever the emulator validates about it, and for a
// CIDR that includes which other CIDRs contain it. Measured on 2026-08-21,
// replaying a real Outscale account: the Net was `10.111.0.0/16` and its Subnet
// `10.111.1.0/24`, the two were minted from one counter and came out as two
// disjoint blocks, and the emulator answered `400 IpRange 198.18.12.0/24 is
// outside the Net range 198.18.11.0/24` where the cloud answered 200. Every
// create, read, update and delete downstream of that subnet — the machine, its
// volume, its NIC, its public IP, the route table link, the NAT service, the
// load balancer — then answered 400 or 404 for that one reason: about a hundred
// findings, not one of them a defect of the emulator.
//
// It is the third of its family, and the family is now the argument for doing
// this as a pre-pass rather than a special case: a netmask that stopped being a
// netmask, an address range that ran backwards, and now a subnet outside its
// net. Each was a relation between values that the per-value walk could not
// see.
//
// # How
//
// Shortest prefix first, so a parent is always placed before its children, and
// each child is then placed at the same offset inside its parent's replacement
// as it held inside the original. The prefix length is preserved as before, so
// a client that computes with it gets the same answer. Ties are broken by
// address, so two runs of the same recording produce the same bytes.
//
// # What it publishes, stated plainly
//
// That one block of the account contained another, and at what offset — never
// an octet of either. It is the same trade [mint.learnAddressOrder] makes with
// the relative order of addresses, and for the same reason.
//
// TestASubnetStaysInsideItsNet fails without this.
func (m *mint) planBlocks(exs []trace.Exchange) {
	var all []netip.Prefix
	seen := map[string]bool{}
	for i := range exs {
		for _, v := range located(&exs[i]) {
			if v.value == "" || seen[v.value] || m.keepable(v.value) {
				continue
			}
			seen[v.value] = true
			p, err := netip.ParsePrefix(v.value)
			if err != nil {
				continue
			}
			all = append(all, p)
		}
	}
	// Shortest prefix first: a container is decided before anything it
	// contains. Then by address, so the order is total and the run repeatable.
	sort.Slice(all, func(i, j int) bool {
		if all[i].Bits() != all[j].Bits() {
			return all[i].Bits() < all[j].Bits()
		}
		return all[i].Addr().Less(all[j].Addr())
	})
	placed := make([]netip.Prefix, 0, len(all)) // originals, in the order decided
	for _, p := range all {
		if to, ok := m.insideAPlacedBlock(p, placed); ok {
			m.block[p.String()] = to
			placed = append(placed, p)
			continue
		}
		m.block[p.String()] = m.freshBlock(p)
		placed = append(placed, p)
	}
}

// insideAPlacedBlock places p inside the replacement of the most specific
// already-placed block that contains it, at the offset it held in the original.
//
// The parent's own replacement has to be inside 198.18.0.0/15, and that is the
// same guarantee [offsetV4] states: a synthetic address lives in that space and
// nowhere else. The case that made it necessary is a prefix of length zero —
// "10.0.0.0/0", a /0 whose address half is the account's, which
// [mint.keepable] does not keep. It contains every block of the recording, its
// replacement masks down to 0.0.0.0/0, and every child derived from it walked
// out of the space: TestTheDefaultRouteSurvivesSanitisation reported
// "172.16.32.0/22" becoming "162.16.32.0/22", refused by the alphabet.
//
// Confining the *child* as well would be redundant rather than defensive, and
// it is deliberately not done: once the parent's replacement is inside the
// space and aligned on its own prefix length, a child at an offset smaller than
// the parent's size is inside the parent and therefore inside the space. A
// second check there could not fail — the falsification run of 2026-08-21
// removed it and every test stayed green, which is the definition of a comment
// standing in for a control.
func (m *mint) insideAPlacedBlock(p netip.Prefix, placed []netip.Prefix) (string, bool) {
	if !p.Addr().Is4() {
		// IPv6 nesting is not derived: the offset arithmetic below is 32-bit,
		// and a v6 recording with nested blocks has not been measured. Refused
		// rather than guessed at, which leaves today's behaviour for v6.
		return "", false
	}
	best := -1
	for i, q := range placed {
		if q.Bits() >= p.Bits() || !q.Addr().Is4() || !q.Contains(p.Addr()) {
			continue
		}
		if best == -1 || q.Bits() > placed[best].Bits() {
			best = i
		}
	}
	if best == -1 {
		return "", false
	}
	parent := placed[best]
	to, err := netip.ParsePrefix(m.block[parent.String()])
	if err != nil || !to.Addr().Is4() || to.Bits() < syntheticV4.Bits() || !syntheticV4.Contains(to.Addr()) {
		return "", false
	}
	offset := binary.BigEndian.Uint32(as4(p.Addr())) - binary.BigEndian.Uint32(as4(parent.Addr()))
	var out [4]byte
	binary.BigEndian.PutUint32(out[:], binary.BigEndian.Uint32(as4(to.Addr()))+offset)
	return netip.PrefixFrom(netip.AddrFrom4(out), p.Bits()).Masked().String(), true
}

// as4 is the four bytes of an IPv4 address, for the arithmetic above.
func as4(a netip.Addr) []byte {
	b := a.As4()
	return b[:]
}

// freshBlock allocates a block of the same prefix length that nothing contains.
//
// A block shorter than the synthetic space itself cannot be *placed* inside it,
// and masking the replacement to that length walks straight out: "10.0.0.0/8"
// came back as "198.0.0.0/8", whose address half is somebody's, and
// TestNoCommittedCorpusCarriesAnIdentifier (internal/cli) is the tooth that
// saw it — on a
// refusal corpus, where "the block size has to be between 16 and 28" is the
// whole point of the exchange. The size is therefore the half that must
// survive; the address half becomes the space's own, left unmasked, so every
// octet published is inside 198.18.0.0/15 and the prefix length the API
// validates is the one the recording carried.
// TestABlockShorterThanTheSyntheticSpaceStaysInsideIt fails without this.
func (m *mint) freshBlock(p netip.Prefix) string {
	bits := p.Bits()
	if p.Addr().Is4() && bits < syntheticV4.Bits() {
		return netip.PrefixFrom(offsetV4(uint32(m.next("cidr4"))), bits).String() //nolint:gosec // a counter over one recording
	}
	if p.Addr().Is4() {
		shift := 32 - bits
		if shift < 0 || shift > 31 {
			shift = 0
		}
		block := uint32(m.next("cidr4")) << uint(shift) //nolint:gosec // a counter over one recording
		return netip.PrefixFrom(offsetV4(block), bits).Masked().String()
	}
	return netip.PrefixFrom(blockV6(uint64(m.next("cidr6")), bits), bits).Masked().String() //nolint:gosec // a counter over one recording
}

// learnAddressOrder gives every address of the recording a rank among the
// addresses of its own family, so that [mint.ip] hands out synthetic addresses
// that sort the way the originals sorted.
//
// # Why ordering is part of the shape
//
// The shape of a value is whatever the emulator validates about it, and for an
// address that includes how it compares with the other addresses of the same
// request. Measured on 2026-08-21, replaying a real `exo compute
// private-network create`: `start-ip` and `end-ip` were minted in the order the
// walk met them — alphabetically, so `end-ip` first — the artefact carried a
// range running backwards, and the emulator answered `400 end-ip is below
// start-ip` where the cloud answered 200. The get, the update, the delete and
// the operation polls behind it then answered 404, around twenty findings, not
// one of them a defect of the emulator.
//
// A pre-pass rather than a rule about field names: "start"/"end", "first"/
// "last", "from"/"to" and whatever a fourth provider calls them are one problem,
// and sorting solves it without naming any of them.
//
// # What it publishes, stated plainly
//
// The relative order of the account's addresses, and nothing else — no octet of
// any of them. It is the same trade [mint.cidr] already makes by keeping the
// prefix length, and for the same reason: a value a client computes with, whose
// shape is what makes the transcript replayable at all.
//
// The counters start above the ranks so an address met later — one the walk
// below did not reach — is still given an address of its own rather than one
// already handed out.
//
// TestAnAddressRangeStillRunsForwards fails without this.
func (m *mint) learnAddressOrder(exs []trace.Exchange) {
	type ranked struct {
		raw  string
		addr netip.Addr
	}
	var v4, v6 []ranked
	seen := map[string]bool{}
	for i := range exs {
		for _, v := range located(&exs[i]) {
			if v.value == "" || seen[v.value] {
				continue
			}
			seen[v.value] = true
			if m.keepable(v.value) || isCIDR(v.value) || netmaskBits(v.value) != 0 {
				continue
			}
			addr, err := netip.ParseAddr(v.value)
			if err != nil {
				continue
			}
			if addr.Is4() {
				v4 = append(v4, ranked{v.value, addr})
			} else {
				v6 = append(v6, ranked{v.value, addr})
			}
		}
	}
	for class, list := range map[string][]ranked{"ipv4": v4, "ipv6": v6} {
		sort.Slice(list, func(i, j int) bool { return list[i].addr.Less(list[j].addr) })
		for i, r := range list {
			m.rank[r.raw] = i + 1
		}
		m.n[class] = len(list)
	}
}

// rankOf is the learned rank of an address, or the next free position for one
// this recording's walk did not reach.
func (m *mint) rankOf(s, class string) int {
	if r, learned := m.rank[s]; learned {
		return r
	}
	return m.next(class)
}

// allowedValues is every value a sanitised transcript may keep because it is
// the provider's own word rather than the account's.
//
// Two sources, and both are documents this repository holds: what the packs
// vouch for (emulator.Vocabulary — the zones and regions a pack answers 400
// for) and every value the provider's own API description enumerates. The
// second was added after a measurement, not from a design: the first real
// Scaleway corpus replayed four list operations as 400 because
// "order=created_at_desc" had become a synthetic string, and a 400 the
// sanitiser manufactured is exactly the false divergence #73 warns about.
//
// TestAnEnumeratedValueSurvivesSanitisation fails without the second source.
func allowedValues(opt Options) map[string]bool {
	out := map[string]bool{}
	for _, v := range opt.Vocabulary {
		out[v] = true
	}
	if opt.Doc != nil {
		for _, v := range opt.Doc.EnumValues() {
			out[v] = true
		}
	}
	return out
}

func (m *mint) next(class string) int {
	m.n[class]++
	return m.n[class]
}

// value is the whole of the rule: keep what is vouched for, replace the rest
// with something of the same shape, and give the same original the same
// replacement every time.
func (m *mint) value(s string) string {
	if m.keepable(s) {
		m.kept++
		return s
	}
	if to, bound := m.to[s]; bound {
		return to
	}
	to := m.synthesise(s)
	if m.issued[to] {
		// Two originals handed the same replacement. The transcript would then
		// say that two objects of the account were one, which is a measurement
		// invented by the sanitiser — and the shape it would invent is exactly
		// the one #270 found by hand ("two networks share a /48"). A counter
		// that repeats is a bug in a class above; it is refused here rather
		// than published, and [Sanitise] turns it into an error.
		//
		// TestTwoValuesNeverShareAReplacement fails without this.
		m.collided = append(m.collided, to)
	}
	m.issued[to] = true
	m.to[s] = to
	return to
}

// keepable reports whether a value may be written down as it was read.
//
// Six reasons, and no seventh: the empty string, the proxy's own placeholder
// for a credential it dropped, a value a pack vouches for, a boolean word, a
// short run of digits, and the prefix that selects every address.
//
// The digits deserve their bound. A page number, a size and a port carry
// nothing of an account, and replacing them would have the emulator refuse the
// request that carries them — but Outscale mints a twelve-digit account
// identifier as a *string*, so "all digits" without a length would be a rule
// that publishes account numbers. Six is longer than any paging value and
// shorter than every identifier of that family.
//
// TestALongRunOfDigitsIsNotKept fails without the bound.
//
// The sixth deserves its own paragraph, because it is the one reason here that
// is not a judgement about what a value looks like. "0.0.0.0/0" selects every
// address there is: it names no network of the account, and there is exactly
// one of it per family, so no replacement exists that is both of the same shape
// and a different value. [mint.cidr] duly hands back the string it was given —
// masking a zero-length prefix yields the same prefix — and [Audit] then finds
// that string in the recording *and* in the artefact and refuses the whole run.
// Every real security-group rule that opens a port to the internet carries it,
// so without this the most ordinary exchange a cloud answers is unrecordable,
// and the only way past would be to trim the corpus until it passes.
//
// Measured on 2026-08-21, recording `exo compute security-group rule add
// --network 0.0.0.0/0` against a real Exoscale account: eleven positions
// refused, every one of them this value.
//
// It is narrow on purpose. Only the canonical unspecified address qualifies, so
// "10.0.0.0/0" — a prefix whose address half still carries something of the
// account — is replaced like any other.
//
// TestTheDefaultRouteSurvivesSanitisation fails without this.
func (m *mint) keepable(s string) bool {
	switch {
	case s == "", s == proxy.Placeholder:
		return true
	case proxy.IsPlaceholder(s):
		// A suffixed placeholder is NOT kept: it goes through [mint.value] like
		// everything else, so the committed artefact carries the counter this
		// package hands out rather than the recorder's keyed digest. See
		// [mint.synthesise], and #384 for why a placeholder has a suffix at all.
		return false
	case m.allowed[s]:
		return true
	case s == "true", s == "false":
		return true
	case isShortDigits(s):
		return true
	case isDefaultRoute(s):
		return true
	}
	return false
}

// isMintedPlaceholder recognises the spelling THIS package hands out — the bare
// placeholder, or one suffixed with a decimal counter.
//
// Deliberately narrower than [proxy.IsPlaceholder], which also admits the
// recorder's keyed digest (#384). The two ask different questions: the replay
// asks "did the recorder replace this", and must recognise everything it wrote;
// the audit asks "may this value stand in a committed artefact", and a digest
// from the recording standing there would be a value of the recording that
// survived — which is exactly what the audit exists to refuse.
//
// TestARecordersPlaceholderIsRenumberedRatherThanKept fails without the
// narrowing.
func isMintedPlaceholder(s string) bool {
	if s == proxy.Placeholder {
		return true
	}
	rest, found := strings.CutPrefix(s, proxy.Placeholder+"-")
	return found && isDigits(rest)
}

// isDigits reports whether s is a non-empty run of decimal digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isDefaultRoute recognises "0.0.0.0/0" and "::/0", and nothing else.
func isDefaultRoute(s string) bool {
	p, err := netip.ParsePrefix(s)
	return err == nil && p.Bits() == 0 && p.Addr().IsUnspecified()
}

const maxKeptDigits = 6

func isShortDigits(s string) bool {
	if s == "" || len(s) > maxKeptDigits {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// synthesise invents a value of the same shape as the one it replaces.
//
// The shape is what the emulator validates and what the replay rebinds, so a
// value that loses it is a value that manufactures a divergence: a synthetic
// string where a UUID stood answers 404 on the read that follows, and a
// synthetic string where an OpenSSH key stood is refused by sshkey.Parse before
// any comparison happens.
func (m *mint) synthesise(s string) string {
	switch {
	case proxy.IsPlaceholder(s):
		// The recorder's placeholder carries a keyed digest of the value it
		// replaced, so that two originals are two placeholders (#384). Nothing
		// is recoverable from it, and it is still replaced here: what a
		// committed artefact should carry is this package's own counter, so a
		// reader can see at a glance that the fourth redaction is the fourth,
		// and so the alphabet has one spelling to admit rather than two.
		return fmt.Sprintf("%s-%d", proxy.Placeholder, m.next("redacted"))
	case shape.IsUUID(s):
		return fmt.Sprintf("00000000-0000-4000-8000-%012d", m.next("uuid"))
	case isPrefix(s):
		prefix := s[:strings.LastIndex(s, "-")+1]
		return fmt.Sprintf("%s%08x", prefix, m.next("prefixed"))
	case isCIDR(s):
		return m.cidr(s)
	case netmaskBits(s) != 0:
		return maskOf(33 - netmaskBits(s))
	case isIP(s):
		return m.ip(s)
	case sshkey.Valid(s):
		return syntheticKey(m.next("sshkey"))
	case isTimestamp(s):
		return corpusEpoch.Add(time.Duration(m.next("time")) * time.Second).Format(time.RFC3339)
	default:
		return fmt.Sprintf("%s%d", Token, m.next("string"))
	}
}

// Token is the prefix of every synthetic string that has no shape of its own.
// Exported because [Scan] recognises it and because a reader of a committed
// corpus should be able to tell an invented value from a surviving one at a
// glance.
const Token = "redacted-"

// isPrefix recognises Outscale's "i-<hex>" family without recognising a UUID,
// which shape.IsMintedIdentifier answers for as well.
func isPrefix(s string) bool {
	return !shape.IsUUID(s) && !isIP(s) && shape.IsMintedIdentifier(s)
}

func isIP(s string) bool { _, err := netip.ParseAddr(s); return err == nil }

// netmaskBits is the prefix length of a dotted IPv4 netmask, or 0 for anything
// that is not one.
//
// A netmask is an address by syntax and a length by meaning, and [mint.ip]
// treated it as the first: "255.255.255.0" became an address of the synthetic
// space, which is not a mask at all. Measured on 2026-08-21, replaying a real
// `exo compute private-network create`: the emulator answered `400 netmask is
// not a usable IPv4 netmask` where the cloud answered 200, and the read, the
// update, the delete and the three operation polls that followed then answered
// 404 for that one reason — around twenty findings, none of them a defect of
// the emulator. It is the same family as the `public_key` substitution that
// produced five of #352's eight exemptions: a value that loses its shape is a
// value that manufactures a divergence.
//
// The replacement is [maskOf](33-n), which is a bijection of 1..32 onto itself
// with no fixed point, so the account's own mask never survives and the value
// written down is always a mask. A length of 0 is not one of them: "0.0.0.0" is
// an ordinary address in every field but this one, and it goes through
// [mint.ip] as before.
//
// TestANetmaskIsReplacedByANetmask fails without this.
func netmaskBits(s string) int {
	addr, err := netip.ParseAddr(s)
	if err != nil || !addr.Is4() {
		return 0
	}
	four := addr.As4()
	v := binary.BigEndian.Uint32(four[:])
	n := bits.OnesCount32(v)
	if n == 0 || v != ^uint32(0)<<(32-n) {
		return 0
	}
	return n
}

// maskOf is the dotted IPv4 netmask of that prefix length.
func maskOf(n int) string {
	if n < 1 || n > 32 {
		return "255.255.255.255"
	}
	var out [4]byte
	binary.BigEndian.PutUint32(out[:], ^uint32(0)<<(32-n))
	return netip.AddrFrom4(out).String()
}

func isCIDR(s string) bool { _, err := netip.ParsePrefix(s); return err == nil }

func isTimestamp(s string) bool {
	_, err := time.Parse(time.RFC3339, s)
	return err == nil
}

// The two spaces every synthetic address comes from.
//
// 198.18.0.0/15 rather than one of the documentation /24s of RFC 5737, and the
// reason is a measurement: a Scaleway private network is a /22, and a /22 does
// not fit in a /24, so a corpus built on the documentation blocks could not
// carry the one CIDR this project's own conformance suites create. RFC 2544
// reserves 198.18.0.0/15 for benchmarking, it is not routable on the public
// internet, and it is large enough for every prefix a cloud hands out.
//
// IPv6 takes RFC 3849's documentation prefix, which is a /32 and needs no such
// argument.
var (
	syntheticV4 = netip.MustParsePrefix("198.18.0.0/15")
	syntheticV6 = netip.MustParsePrefix("2001:db8::/32")
)

// ip hands out an address from the synthetic space of the right family.
func (m *mint) ip(s string) string {
	addr, err := netip.ParseAddr(s)
	if err != nil || addr.Is4() {
		return offsetV4(uint32(m.rankOf(s, "ipv4"))).String() //nolint:gosec // a rank over one recording
	}
	return offsetV6(uint64(m.rankOf(s, "ipv6"))).String() //nolint:gosec // a rank over one recording
}

// cidr hands out a block of the same prefix length, aligned so the address it
// starts at is the network address — a client that computes with it gets the
// same answer it would from the real one.
//
// The counter is placed at the bit position the prefix length leaves free, and
// that arithmetic is the whole of the function. Getting it wrong is not a
// cosmetic bug: the first version shifted an IPv6 block inside the low 64 bits,
// where a /64 mask erases it, so every /64 of a recording became
// 2001:db8::/64 — two different networks written as one. That is the exact
// finding #270 made against a real account ("two networks of one project share
// a /48"), manufactured by the instrument.
//
// TestTwoValuesNeverShareAReplacement fails without this, and [mint.value]
// refuses a collision whatever its cause.
// The plan [mint.planBlocks] decided is what this answers; the allocation below
// is the fallback for a block the pre-pass did not meet — a value assembled
// after the walk, which no measured recording has produced.
func (m *mint) cidr(s string) string {
	if to, planned := m.block[s]; planned {
		return to
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return Token + fmt.Sprint(m.next("string"))
	}
	return m.freshBlock(p)
}

// blockV6 is the nth block of the given prefix length inside the synthetic IPv6
// space: the counter sits just above the bits the mask erases, so two blocks of
// the same length are two blocks.
func blockV6(n uint64, bits int) netip.Addr {
	shift := 128 - bits
	if shift < 0 || shift > 127 {
		shift = 0
	}
	var high, low uint64
	switch {
	case shift >= 64:
		high = n << uint(shift-64)
	default:
		high = n >> uint(64-shift)
		low = n << uint(shift)
	}
	base := syntheticV6.Addr().As16()
	var out [16]byte
	binary.BigEndian.PutUint64(out[:8], binary.BigEndian.Uint64(base[:8])|high)
	binary.BigEndian.PutUint64(out[8:], binary.BigEndian.Uint64(base[8:])|low)
	return netip.AddrFrom16(out)
}

// offsetV4 is the nth address of the synthetic IPv4 space, and never an address
// outside it.
//
// The confinement is a control rather than tidiness. [Scan] admits an IPv4
// address only inside 198.18.0.0/15, so a counter that walks past the end of
// that block does not produce a slightly odd address: it produces a value the
// alphabet refuses, and the sanitisation fails on the artefact instead of on
// the arithmetic that made it. Wrapping turns that into a repeated replacement,
// which [mint.value] already refuses by name — "the substitution handed out a
// replacement twice" says what happened, where "not a value a sanitised
// transcript may carry" says only where it landed.
//
// Measured on 2026-08-21, recording Outscale's `ReadPublicIpRanges`, which
// publishes the provider's whole public address space: 90 blocks, three of them
// /20s, and the counter that reached them shifted twelve bits and landed in
// 198.20.0.0 — outside the space, refused by the alphabet, nothing written.
//
// Confining rather than counting per prefix length, and that is a measurement
// too: the arithmetic is modular once it is confined, so the *offset* a shared
// counter reaches makes no difference at all. Only how many blocks of one
// length a recording carries does, and 198.18.0.0/15 holds 512 /24s and 32
// /20s whichever counter walks it.
//
// TestASpaceWithNoRoomLeftIsRefusedRatherThanOverrun fails without this.
func offsetV4(n uint32) netip.Addr {
	base := syntheticV4.Addr().As4()
	n &= ^uint32(0) >> uint(syntheticV4.Bits()) //nolint:gosec // a prefix length of a v4 space, 0..32
	v := binary.BigEndian.Uint32(base[:]) + n
	var out [4]byte
	binary.BigEndian.PutUint32(out[:], v)
	return netip.AddrFrom4(out)
}

// offsetV6 is the nth address of the synthetic IPv6 space, counted in the low
// 64 bits so a /48 and a /64 both land inside 2001:db8::/32.
func offsetV6(n uint64) netip.Addr {
	base := syntheticV6.Addr().As16()
	high := binary.BigEndian.Uint64(base[:8]) + (n >> 16)
	low := n << 48
	var out [16]byte
	binary.BigEndian.PutUint64(out[:8], high)
	binary.BigEndian.PutUint64(out[8:], low)
	return netip.AddrFrom16(out)
}

// syntheticKey renders a valid OpenSSH ed25519 public key whose material is the
// counter and nothing else.
//
// Rendered through internal/core/sshkey rather than by pasting a base64 line,
// for the reason that package exists: the format was written twice in this
// repository and the copies drifted. What the emulator parses is what this
// writes.
func syntheticKey(n int) string {
	blob := make([]byte, 0, 4+len("ssh-ed25519")+4+32)
	blob = appendString(blob, []byte("ssh-ed25519"))
	material := make([]byte, 32)
	binary.BigEndian.PutUint64(material[24:], uint64(n)) //nolint:gosec // a counter over one recording
	blob = appendString(blob, material)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " " + Token + fmt.Sprint(n)
}

func appendString(dst, s []byte) []byte {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(s))) //nolint:gosec // a fixed-size field
	dst = append(dst, length[:]...)
	return append(dst, s...)
}

// path replaces every segment of a request path the provider's document does
// not state is a literal, and reports whether the document described the path
// at all.
//
// A path it does not describe has *every* segment replaced. That is deliberate
// and it is the default-deny rule applied where it costs something: a bucket
// name, a DNS zone and a project slug are all "just a word" in a path, and a
// tool that kept the words it did not recognise would publish exactly those.
func (m *mint) path(method, p string, doc *contract.Doc) (string, bool) {
	name, described := doc.OperationAt(method, p)
	if !described {
		return m.blankPath(p), false
	}
	template := doc.PathPrefix + doc.Operations[name].Path
	want := strings.Split(strings.TrimSuffix(template, "/"), "/")
	got := strings.Split(strings.TrimSuffix(p, "/"), "/")
	if len(want) != len(got) {
		// The document matched by another road (a trailing slash, a spelling
		// this alignment does not reproduce). Blanking is the safe answer.
		return m.blankPath(p), false
	}
	for i := range got {
		got[i] = m.segment(want[i], got[i])
	}
	out := strings.Join(got, "/")
	if strings.HasSuffix(p, "/") && !strings.HasSuffix(out, "/") {
		out += "/"
	}
	return out, true
}

// segment sanitises one path segment against its template.
func (m *mint) segment(want, got string) string {
	if !strings.Contains(want, "{") {
		// A literal of the API: the document wrote it, not the account.
		m.kept++
		return got
	}
	// A dialect that puts the verb in the identifier's own segment
	// ("{id}:start") keeps the verb, which is the document's word, and replaces
	// the identifier, which is the account's.
	if _, action, found := strings.Cut(want, ":"); found {
		id, _, hasAction := strings.Cut(got, ":")
		if hasAction {
			return m.value(id) + ":" + action
		}
	}
	return m.value(got)
}

// blankPath replaces every segment, keeping the shape of the path and its
// separators so a reader can still see how deep it went.
func (m *mint) blankPath(p string) string {
	segments := strings.Split(p, "/")
	for i, seg := range segments {
		if seg == "" {
			continue
		}
		segments[i] = m.value(seg)
	}
	return strings.Join(segments, "/")
}

// query keeps the parameter names and replaces the values, leaving the string
// byte-identical when nothing had to change — the reason the proxy's own
// redaction does the same: re-encoding through url.Values sorts and re-escapes,
// and the request a replay reissues would then not be the one recorded.
func (m *mint) query(raw string) string {
	if raw == "" {
		return raw
	}
	pairs := strings.Split(raw, "&")
	changed := false
	for i, pair := range pairs {
		name, value, hasValue := strings.Cut(pair, "=")
		if !hasValue {
			continue
		}
		to := m.value(value)
		if to != value {
			pairs[i] = name + "=" + to
			changed = true
		}
	}
	if !changed {
		return raw
	}
	return strings.Join(pairs, "&")
}

// message sanitises one side of an exchange.
func (m *mint) message(msg *trace.Message) *trace.Message {
	if msg == nil {
		return nil
	}
	out := &trace.Message{Body: m.body(msg.Body)}
	if msg.Headers != nil {
		out.Headers = make(map[string]string, len(msg.Headers))
		names := make([]string, 0, len(msg.Headers))
		for name := range msg.Headers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			out.Headers[name] = m.header(name, msg.Headers[name])
		}
	}
	return out
}

// httpVocabulary are the header names whose value is HTTP's own words rather
// than anything of an account: a media type, an encoding, a length.
//
// An allowlist, and a short one. Date and X-Request-Id are deliberately absent:
// one moves on its own and the other is minted by the cloud per call, and both
// were on the proxy's harmless list because the question there was "does this
// carry a credential", which is not the question here.
var httpVocabulary = map[string]bool{
	"accept":          true,
	"accept-encoding": true,
	"cache-control":   true,
	"connection":      true,
	"content-length":  true,
	"content-type":    true,
	"expect":          true,
}

// header sanitises one header value.
//
// The User-Agent is the one that needs an argument. It is the only request
// header the proxy writes down in full, `feint coverage --observed` reads it to
// say which client wanted an operation, and it carries whatever the build put
// in it — a version, a commit, sometimes a path. So what survives is the
// substring that named the family and nothing else: a value out of
// internal/transcript's own closed table, which keeps the attribution working
// on the sanitised file and publishes none of the rest.
//
// TestTheClientOfASanitisedExchangeIsStillNamed fails without this.
func (m *mint) header(name, value string) string {
	lower := strings.ToLower(name)
	if httpVocabulary[lower] {
		m.kept++
		return value
	}
	if lower == "user-agent" {
		if needle := transcript.Needle(value); needle != "" {
			m.kept++
			return needle
		}
	}
	return m.value(value)
}

// body walks a decoded JSON document, keeping every key and every number and
// replacing every string.
//
// Keys are the API's words and stay; numbers, booleans and nulls stay, and that
// is a decision with a stated limit. In these three dialects an identifier is a
// string — a UUID, an address, an "i-<hex>", and even Outscale's account number
// is a string of digits — while the numbers are sizes, counts, ports and prefix
// lengths, which the emulator validates and which a replay must therefore
// receive unaltered. A provider that minted a numeric identifier would defeat
// this, and that is the assumption to revisit rather than a property proved.
func (m *mint) body(v any) any {
	switch value := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		keys := make([]string, 0, len(value))
		for k := range value {
			keys = append(keys, k)
		}
		// Sorted, so the counters are handed out in the same order on two runs
		// over the same recording and the committed file has the same bytes.
		sort.Strings(keys)
		for _, k := range keys {
			out[k] = m.body(value[k])
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = m.body(item)
		}
		return out
	case string:
		return m.value(value)
	default:
		return v
	}
}

func dedupe(xs []string) []string {
	if len(xs) == 0 {
		return nil
	}
	out := xs[:1]
	for _, x := range xs[1:] {
		if x != out[len(out)-1] {
			out = append(out, x)
		}
	}
	return out
}
