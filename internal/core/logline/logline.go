// Package logline spells a value a client controls as one printable line, so
// that the emulator's log records what happened and never what the value
// pretended.
//
// The values are the ones CLAUDE.md names as inputs from outside however
// internal they look: a machine or network name restored verbatim from a
// snapshot (Resource.Runtime), an image identifier a request carried, an
// address a pack was asked to route, the host a CONNECT named, and the error a
// driver built out of any of them. Written raw into a log record, a newline in
// one of them ends the record and starts another that reads as the emulator's
// own — CWE-117, which CodeQL reported fifteen times over binding.go, plan.go
// and forward.go, read live on 2026-09-06.
//
// Two neighbours do related work and neither is this one. cloudinit's
// Spec.checkInjection refuses a control character at the door, because its
// value is rendered into a YAML document where no spelling of it is safe. A
// log is different: the value being written is often the one the emulator
// just refused, and an operator has to read what was refused — so it is
// spelled, not dropped. internal/cli's TestALoggedClientValueCannotForgeALine
// measures that slog.TextHandler already quotes such a value at the sink;
// Sanitise puts the property on the value instead, so it holds whatever
// handler an operator plugs in, and so the analysis that reported the flow can
// measure it cut rather than be asked to believe a dismissal.
//
// It sits under internal/core because both the machine layer and the proxy
// need it and neither may import the other; it imports nothing but the
// standard library, so it can never close a cycle, and it names no provider.
package logline

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Sanitise returns s with every character that cannot stand in a printable
// line spelled the way a Go string literal spells it: a newline as the two
// characters `\n`, an escape as `\x1b`, a Unicode line separator as `\u2028`,
// a byte that is not UTF-8 as `\xff`. A value made of printable characters —
// every machine name, image identifier, address and host this emulator handles
// — comes back unchanged, and that half is measured too: a sanitiser that
// refuses everything passes every attack and breaks the product.
//
// The loop is the sanitiser: every C0 and C1 control, the escape that drives
// a terminal, DEL, the separators U+2028 and U+2029, the spaces that are not
// the ASCII one, and a byte that is not UTF-8. The two strings.ReplaceAll
// calls before it are redundant with it by design — the loop would spell a
// newline just as well — and they stay because they are the exact shape
// CodeQL's go/log-injection query recognises as a sanitiser (a ReplaceAll
// whose replaced string is "\n" or "\r"), so the flow is measured as cut by
// the instrument that reported it, on every pull request, instead of
// dismissed in a web page nobody re-reads. That run is what holds them and
// nothing local can: removing either changes nothing the loop does not repair,
// which tools/falsify/specs/a-log-line-cannot-be-forged.json found on its
// first attempt ("test still passed") and is why it declares no mutation on
// them.
//
// TestSanitiseSpellsEveryControlCharacter fails without the loop's spelling,
// and TestSanitiseLeavesAPrintableValueAlone without the other half.
func Sanitise(s string) string {
	s = strings.ReplaceAll(s, "\r", `\r`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	if utf8.ValidString(s) && strings.IndexFunc(s, unprintable) < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			// Not UTF-8 at all, so no rune stands for it: spell the byte.
			b.WriteString(spell(s[i : i+1]))
		case unprintable(r):
			b.WriteString(spell(s[i : i+size]))
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// unprintable is what cannot stand in a line as itself. unicode.IsPrint keeps
// letters, marks, numbers, punctuation, symbols and the ASCII space, which is
// every character a legitimate name carries; what it refuses is every control
// character, the other spaces (a tab, a no-break space) and the separators.
func unprintable(r rune) bool { return !unicode.IsPrint(r) }

// spell is Go's own spelling of one character, without the quotes strconv puts
// around it: `\t`, `\x1b`, `\u2028`, and `\xff` for a byte that is not UTF-8.
func spell(fragment string) string {
	q := strconv.Quote(fragment)
	return q[1 : len(q)-1]
}
