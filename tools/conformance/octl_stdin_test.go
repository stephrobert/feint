package conformance

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every octl invocation in this repository closes its standard input.
//
// octl reads ALL of stdin whenever stdin is not a terminal and decodes it as
// the request body (osc-sdk-go runner/stdin.go). A call that does not redirect
// therefore inherits whatever stdin the suite was started with, and when that
// is a pipe nobody closes, octl waits for a body that never comes.
//
// Measured 2026-11-13: `octl --version` with an open pipe on stdin blocks
// until it is killed; with `</dev/null` it answers in 109 ms. It cost 25
// minutes of a `fields` leg whose log stopped after the scw suite and printed
// nothing more.
//
// The rule was already written, at point 5 of the header of
// tools/conformance/outscale/octl.sh: "Every call here is </dev/null, in the
// wrapper, so the body can only come from flags." It held for every call that
// went through the `osc` wrapper and for neither of the two version probes
// that did not. A rule stated in a comment and obeyed by most callers is the
// shape this repository names first, so here it is as a control.
//
// Interactive runs never see it, because a terminal is a terminal. That is
// what makes it worth a test rather than a habit: the failure only appears
// where nobody is watching.
func TestEveryOctlCallClosesItsStandardInput(t *testing.T) {
	// An invocation is octl in COMMAND POSITION: at the start of a line, or
	// after $( | && || ; or {. Anything else is prose — `command -v octl`
	// tests for the binary without running it, and the first version of this
	// matcher accused two `fail "octl --version printed no version…"` messages
	// of blocking on stdin. A guard that reports a string literal is a guard
	// somebody switches off.
	invocation := regexp.MustCompile(`(^|\$\(|\||&&|;|\{)\s*octl\s+-`)
	root := filepath.Join("..", "..", "tools")

	scanned, calls := 0, 0
	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".sh") {
			return err
		}
		raw, readErr := os.ReadFile(path) // #nosec G304 -- a committed suite path this test walked
		if readErr != nil {
			return readErr
		}
		scanned++
		for n, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") || !invocation.MatchString(line) {
				continue
			}
			calls++
			if !strings.Contains(line, "</dev/null") {
				offenders = append(offenders, filepath.ToSlash(path)+":"+itoa(n+1)+"  "+trimmed)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	// The population, named rather than assumed. A walk that found no suite
	// would pass while measuring nothing, which is the failure this repository
	// puts above all others.
	if scanned == 0 || calls == 0 {
		t.Fatalf("scanned %d shell file(s) and found %d octl invocation(s): this test measured nothing",
			scanned, calls)
	}

	if len(offenders) != 0 {
		t.Fatalf("%d octl invocation(s) of %d do not close standard input:\n  %s\n\n"+
			"octl reads all of stdin whenever stdin is not a terminal and decodes it as the request "+
			"body, so such a call blocks forever when the suite's own stdin is a pipe nobody closes. "+
			"Add </dev/null, or route the call through the osc() wrapper, which already has it.",
			len(offenders), calls, strings.Join(offenders, "\n  "))
	}
}

// itoa avoids pulling strconv in for one call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
