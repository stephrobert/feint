package conformance

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A verdict about a segment must not be drawn from a silence nobody questioned.
//
// `shared/verdicts.sh` states the rule in its own preamble and has since #219:
// a machine that never booted, or whose listener never started, produces exactly
// the same observation as a network that separates too much. The answer is a
// positive control — ask the target, on its own loopback or its own address,
// before concluding anything about what reaches it.
//
// The rule was written once and applied unevenly, which is the failure this
// repository pays for most often. Measured on 2026-08-27, while an
// `evidence:update` pass was regenerating the record:
//
//	FAIL: 10.186.0.20 is unreachable inside one private network;
//	      the segment is broken, not isolated
//
// The whole pass died there. The base of that tree had been measured green on
// the *same* assertion twenty minutes earlier, so the verdict was accusing the
// product's dataplane on the strength of a connection that did not open — with
// nothing having established that the responder existed. `scaleway/network.sh`
// had carried the control on its own guard since #219; `exoscale/network.sh`
// carried it for the far network only, and `outscale/network.sh` carried it for
// the Vm of the *other* Net and not for the Vm of the same one. Two suites, one
// lesson, learned in the third.
//
// So the count is held here rather than remembered. It is deliberately coarse —
// it cannot tell which probe a control guards — and coarse is the right trade:
// a suite that gains a reachability probe without gaining a control reddens on
// the commit that adds it, and the author is pointed at the helper that already
// exists.
//
// What this guard cannot see, found by trying to falsify it and worth stating
// rather than discovering later:
//
//   - **it counts identifiers, so anything that keeps the identifier keeps the
//     count.** A control call commented out with `:` in front of it, or one
//     whose arguments name the wrong machine, satisfies this test. The
//     falsification therefore comes from the other bound — a probe added
//     without a control, which is the regression that actually happened twice
//     (tools/falsify/specs/a-verdict-drawn-from-silence.json, three mutations).
//   - **the `probes == 0` branch below is not falsifiable under this harness**,
//     which requires every name in a mutation's `find` to survive into its
//     `replace`: removing a suite's last probe necessarily removes its name.
//     That branch is asserted and unproven, and it is written down here rather
//     than counted as covered.

var (
	reachProbe = regexp.MustCompile(`(?m)^\s*[a-z_]*reach\(\) \{`)
	control    = regexp.MustCompile(`assert_listening_within|assert_answers_itself_within`)
)

// unguardedProbes names a suite that draws a reachability verdict with fewer
// controls than probes, and says why that is acceptable. Empty, and it should
// stay that way: the two helpers cost one call.
var unguardedProbes = map[string]string{}

func runtimeSuites(t *testing.T) map[string]string {
	t.Helper()
	dir := filepath.Join("..", "..", "tools", "conformance")
	if _, err := os.Stat(dir); err != nil {
		dir = "." // running from tools/conformance itself
	}
	out := map[string]string{}
	for _, provider := range []string{"scaleway", "outscale", "exoscale"} {
		path := filepath.Join(dir, provider, "network.sh")
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		out[provider+"/network.sh"] = string(body)
	}
	if len(out) == 0 {
		t.Fatal("no runtime suite read; this test would pass over an empty world")
	}
	return out
}

func TestEveryReachabilityProbeIsBackedByAPositiveControl(t *testing.T) {
	for name, body := range runtimeSuites(t) {
		probes := len(reachProbe.FindAllString(body, -1))
		controls := len(control.FindAllString(body, -1))
		if probes == 0 {
			t.Errorf("%s defines no reachability probe; either it stopped measuring "+
				"the dataplane or this test stopped finding what it measures", name)
			continue
		}
		if controls >= probes {
			continue
		}
		if why, excused := unguardedProbes[name]; excused {
			if strings.TrimSpace(why) == "" {
				t.Errorf("%s is excused with an empty reason, which excuses nothing", name)
			}
			continue
		}
		t.Errorf("%s has %d reachability probe(s) and %d positive control(s): a verdict "+
			"there can be drawn from a silence nobody questioned.\n"+
			"Call assert_listening_within (a port) or assert_answers_itself_within (an "+
			"address) on the target before concluding, the way scaleway/network.sh has "+
			"since #219. A dead listener and a broken segment are the same observation, "+
			"and only one of them is the product's fault.", name, probes, controls)
	}
}

// The control this repository actually offers, named once so a lookalike cannot
// satisfy the count above. If these helpers are renamed, the regexp above stops
// matching and every suite reddens — which is the correct failure: the count is
// meaningless the moment it stops counting the real thing.
func TestThePositiveControlsTheSuitesCountOnStillExist(t *testing.T) {
	dir := filepath.Join("..", "..", "tools", "conformance")
	if _, err := os.Stat(dir); err != nil {
		dir = "."
	}
	body, err := os.ReadFile(filepath.Join(dir, "shared", "verdicts.sh"))
	if err != nil {
		t.Fatalf("reading shared/verdicts.sh: %v", err)
	}
	for _, fn := range []string{"assert_listening_within", "assert_answers_itself_within"} {
		if !strings.Contains(string(body), fn+"() {") {
			t.Errorf("shared/verdicts.sh no longer defines %s, so the count in "+
				"TestEveryReachabilityProbeIsBackedByAPositiveControl counts nothing", fn)
		}
	}
}
