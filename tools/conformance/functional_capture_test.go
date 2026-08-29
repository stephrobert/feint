package conformance

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every verdict that needs a listener waits for it — asserted structurally,
// because the defect was never one call site.
//
// #600 taught the SERVICE path to wait, and the scheduled night of 2026-08-29
// failed further along on a different one:
//
//	FAIL: scaleway: platform-bastion does not listen on 22 inside the machine
//	      (listening: 53 ); 'unreachable' would be measuring a dead machine
//	      rather than isolation
//
// `platform-bastion` is the TARGET of the isolation verdict, and its listeners
// came from `listen_of`, which read once and cached. Five of the six verdicts in
// functional.sh drew their conclusion from that cache.
//
// A per-call-site test would have passed on the five that were already wrong, so
// what is asserted here is the property: no verdict in functional.sh reads a
// capture that nothing waited for. This is the same shape as
// TestEveryRouteDeclaresAnOperation in internal/core/emulator — the rule is
// checked over the whole file, not remembered by whoever adds the seventh
// verdict.

// verdictsTakingACapture are the functionallib.sh verdicts that are HANDED a
// listen file. Each decides whether a port is open inside a machine from a
// capture it did not take, so each needs one something waited for.
//
// `fnl_service_came_up` is deliberately absent: it fills its own capture, by
// polling for the port with `wait_until` (#600). Demanding a `listen_for` before
// it would accuse the one verdict in this file that already waits — which the
// first version of this test did, and which is why the list says what it
// selects on rather than naming every verdict.
var verdictsTakingACapture = []string{
	"fnl_firewall_pair",
	"fnl_reaches",
	"fnl_isolated",
}

func TestNoVerdictReadsAListenCaptureNobodyWaitedFor(t *testing.T) {
	source, err := os.ReadFile("functional.sh")
	if err != nil {
		t.Fatalf("read functional.sh: %v", err)
	}
	lines := strings.Split(string(source), "\n")

	// The caching capture is gone, and must not come back beside its
	// replacement for the next caller to reach for. Named rather than inferred:
	// a helper that returns an existing file without reading the machine is the
	// defect, whatever it is called.
	cachingRead := regexp.MustCompile(`\[ -f "\$LISTEN" \] && return 0`)
	for i, line := range lines {
		if cachingRead.MatchString(line) {
			t.Errorf("functional.sh:%d reintroduces a capture that returns a cached file: %s",
				i+1, strings.TrimSpace(line))
		}
	}

	// Every verdict call site is preceded by a wait for the port it judges.
	// Four lines of slack: the call sites put the capture immediately above,
	// and a comment between them is ordinary.
	const slack = 4
	seen := map[string]int{}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		for _, verdict := range verdictsTakingACapture {
			if !strings.HasPrefix(trimmed, verdict+" ") {
				continue
			}
			seen[verdict]++
			waited := false
			for back := i - 1; back >= 0 && back >= i-slack; back-- {
				if strings.Contains(lines[back], "listen_for ") {
					waited = true
					break
				}
			}
			if !waited {
				t.Errorf("functional.sh:%d draws %s from a capture no listen_for filled — "+
					"the 2026-08-29 night, one verdict over", i+1, verdict)
			}
		}
	}

	// The reader proves it can find before it judges: a regexp that matched
	// nothing would pass this file however wrong it became.
	for _, verdict := range verdictsTakingACapture {
		if seen[verdict] == 0 {
			t.Errorf("no call site of %s was found, so this test measured nothing about it", verdict)
		}
	}
}
