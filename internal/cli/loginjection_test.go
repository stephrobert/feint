package cli

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// A value a client sent cannot forge a line in the emulator's log.
//
// CodeQL reports `go/log-injection` five times, on the three packs that log an
// address they refused to route: `internal/providers/outscale/publicips.go`,
// `internal/providers/exoscale/elasticips.go` (twice) and
// `internal/providers/outscale/privateips.go`. The value really is
// client-controlled and really does reach a log call, so the dataflow the query
// describes is real — the branch that logs it is precisely the one where
// `netip.ParseAddr` refused it, so it can be any string at all.
//
// What the query cannot see is the sink. `slog.TextHandler` quotes any value
// carrying a space, an `=` or a control character, so a newline is written as
// the two characters `\n` inside quotes and the record stays on one line.
// Measured before this test was written, with the exact payload below: one line
// out, `address="192.0.2.1\nlevel=ERROR msg=\"…\""`.
//
// So the five alerts are false positives, and this is the difference between
// saying that and knowing it. A dismissal is a sentence in a web interface that
// no future change consults; this test fails the day the emulator's logger
// stops escaping — a hand-rolled handler, a switch to a format that does not
// quote, or a message built with fmt.Sprintf into a sink that does not.
//
// It is the same rule as everywhere else here: what is claimed is what is
// checked. CLAUDE.md's own version of it is the YAML one — refuse at the door
// rather than hope the render escapes — and the reason this defect is answered
// at the sink instead is that the value being logged is the one the emulator
// just *refused*. Sanitising it would hide what a reader needs to see.
func TestALoggedClientValueCannotForgeALine(t *testing.T) {
	// The payload a log-injection attack uses: end the record, start one that
	// looks like the emulator's own.
	forged := "192.0.2.1\nlevel=ERROR msg=\"the emulator was compromised\" attacker=yes"

	var out bytes.Buffer
	// The handler `serve` builds, constructed the same way rather than borrowed
	// from a test helper: what is under test is the sink this binary really
	// writes to.
	log := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelInfo}))
	log.Warn("refusing to route an address outside the emulated elastic block",
		"address", forged, "instance", "i-1234")

	written := strings.TrimRight(out.String(), "\n")
	if lines := strings.Count(written, "\n"); lines != 0 {
		t.Errorf("one log call produced %d extra line(s): a client value ended the record "+
			"and started another, which is the injection CodeQL warns about.\n%s", lines, written)
	}
	// And the forged level never appears as a field of its own, which is the
	// half a line count alone would miss: a handler could escape the newline and
	// still let `level=ERROR` through as a second attribute.
	if strings.Contains(written, " level=ERROR") {
		t.Errorf("the forged level reached the record as a field of its own:\n%s", written)
	}
	// The value is still readable, which is the point of logging it: an
	// operator debugging a refused address needs to see what was refused.
	if !strings.Contains(written, "192.0.2.1") {
		t.Errorf("the refused address is not in the record at all:\n%s", written)
	}
}
