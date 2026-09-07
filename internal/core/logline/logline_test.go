package logline_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/stephrobert/feint/internal/core/logline"
)

// The refusing half: every character that could end a record or drive a
// terminal comes out spelled, and the result is a printable line.
func TestSanitiseSpellsEveryControlCharacter(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"a newline", "a\nb", `a\nb`},
		{"a carriage return", "a\rb", `a\rb`},
		{"the CRLF pair", "a\r\nb", `a\r\nb`},
		{"a terminal escape", "a\x1b[2Kb", `a\x1b[2Kb`},
		{"a NUL", "a\x00b", `a\x00b`},
		{"a backspace", "a\bb", `a\bb`},
		{"DEL", "a\x7fb", `a\x7fb`},
		{"a tab", "a\tb", `a\tb`},
		{"a C1 control", "a\u0085b", `a\u0085b`},
		{"the line separator", "a\u2028b", `a\u2028b`},
		{"the paragraph separator", "a\u2029b", `a\u2029b`},
		{"a byte that is not UTF-8", "a\xffb", `a\xffb`},
		{"a newline behind a byte that is not UTF-8", "a\xff\nb", `a\xff\nb`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := logline.Sanitise(c.in)
			if got != c.want {
				t.Errorf("Sanitise(%q) = %q, want %q", c.in, got, c.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("Sanitise(%q) = %q is not UTF-8", c.in, got)
			}
			if i := strings.IndexFunc(got, func(r rune) bool { return !unicode.IsPrint(r) }); i >= 0 {
				t.Errorf("Sanitise(%q) = %q still carries an unprintable character at %d", c.in, got, i)
			}
		})
	}

	// The payload an injection uses — end the record, start one that reads as
	// the emulator's own — through a writer that quotes nothing. The property
	// is on the value, so the handler does not have to help.
	t.Run("a forged record through a writer that does not quote", func(t *testing.T) {
		forged := "203.0.113.9\nlevel=ERROR msg=\"the emulator was compromised\" attacker=yes"
		var out bytes.Buffer
		fmt.Fprintf(&out, "level=WARN msg=refused address=%s\n", logline.Sanitise(forged))
		written := out.String()
		if n := strings.Count(written, "\n"); n != 1 {
			t.Errorf("one record produced %d line(s): the value ended it and started another\n%s", n, written)
		}
		if strings.Contains(written, "\nlevel=ERROR") {
			t.Errorf("the forged record stands on a line of its own:\n%s", written)
		}
		if !strings.Contains(written, "203.0.113.9") {
			t.Errorf("the refused address is not in the record at all:\n%s", written)
		}
	})
}

// The accepting half, and the one a sanitiser is most often written without:
// what this emulator actually logs — machine names, hosts, image identifiers,
// addresses, an error the runtime returned — comes back byte for byte.
func TestSanitiseLeavesAPrintableValueAlone(t *testing.T) {
	for _, name := range []string{
		"",
		"feint-acme-8f3a2b1c-77d2-4a6e-9b0e-1c2d3e4f5a6b",
		"fnt-ovn-9b0e1c2d",
		"api-eu-west-1.example.net:443",
		"ami-47899c77",
		"ubuntu_24.04",
		"Ubuntu 24.04 LTS (noble)",
		"203.0.113.9",
		"fe80::1%eth0",
		"la machine d'Éliane, n° 3",
		"incus launch: Error: Failed getting remote image info",
		`a literal backslash \n stays the two characters it is`,
	} {
		if got := logline.Sanitise(name); got != name {
			t.Errorf("Sanitise(%q) = %q: a printable value must come back unchanged", name, got)
		}
	}
}
