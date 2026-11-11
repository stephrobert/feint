package ci

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// maxFalseVerdict is the rate this repository is willing to call acceptable for
// a verdict drawn from a fixed sample of coin flips.
//
// 1e-6 is a chosen horizon rather than a measured one, and the choice is the
// usual one: small enough that nobody meets it over the life of the suite,
// large enough that the sample stays cheap. What matters more than the number
// is that it is written down, because the defect it replaces was a number
// nobody had computed.
const maxFalseVerdict = 1e-6

var (
	probeCall      = regexp.MustCompile(`probe\s+"?\$?\{?([A-Za-z_][A-Za-z0-9_]*|[0-9]+)\}?"?\s*\)`)
	probeConstant  = regexp.MustCompile(`(?m)^DISTRIBUTION_PROBES=([0-9]+)`)
	perMachineCase = regexp.MustCompile(`\*"\$vm_[ab]"\*`)
)

// TestABalancerVerdictDrawsFromASampleLargeEnoughToMeanIt is cited by
// tools/conformance/outscale/balancer.sh.
//
// ## The defect (#559)
//
// The suite asked six connections whether a balancer reaches both machines. Six
// is a coin flipped six times: a balancer that works sends every connection to
// one side with probability 2 × 0.5⁶ = **3.1 %**, so the gate reddened about
// one run in thirty-two on a subject that was behaving. A gate that reds on a
// working system teaches re-running it, which this repository has written down
// as worse than having no gate at all — and this one sits on the dataplane
// suite, whose subject is the product's own differentiator.
//
// The same arithmetic runs the other way at the unlink site: six probes that
// happen to avoid a machine the unlink never reached is 0.5⁶ = 1.6 %, a false
// GREEN over a real defect. That site's comment already named it "#559's defect
// with a different number".
//
// ## What this asserts, and why it reads the script
//
// The subject *is* the sample size, so the control reads it — but it checks a
// property rather than a value: every `probe` call whose verdict names a
// specific machine must draw from the declared constant, and that constant must
// put the false-verdict rate under maxFalseVerdict. Raising or lowering the
// number stays free; going back to a literal, or to a number that cannot
// support the claim, does not.
//
// A liveness probe ("did every connection get an answer") is deliberately out
// of scope: it has no false negative more probes would remove, and demanding
// the big sample there would buy wall-clock and nothing else.
func TestABalancerVerdictDrawsFromASampleLargeEnoughToMeanIt(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "conformance", "outscale", "balancer.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path) // #nosec G304 -- a fixed path inside the repository
	if err != nil {
		t.Fatal(err)
	}
	script := string(body)

	declared := probeConstant.FindStringSubmatch(script)
	if declared == nil {
		t.Fatal("balancer.sh declares no DISTRIBUTION_PROBES: the sample a per-machine " +
			"verdict is drawn from must be named once, with its arithmetic, not spelled at each site")
	}
	n, err := strconv.Atoi(declared[1])
	if err != nil {
		t.Fatal(err)
	}
	// Both tails: every connection landing on one side, either side.
	rate := 2 * math.Pow(0.5, float64(n))
	if rate > maxFalseVerdict {
		t.Errorf("DISTRIBUTION_PROBES=%d puts a working balancer's false verdict at %.3g, "+
			"over the %.0e this repository accepts: a gate that reds on a healthy subject "+
			"teaches the reflex of re-running it", n, rate, maxFalseVerdict)
	}

	// Every per-machine verdict draws from that constant. Walk the lines, and
	// for each `probe <something>` look ahead at the case arms it feeds.
	lines := strings.Split(script, "\n")
	checked := 0
	for i, line := range lines {
		call := probeCall.FindStringSubmatch(line)
		if call == nil {
			continue
		}
		window := strings.Join(lines[i:min(i+10, len(lines))], "\n")
		if !perMachineCase.MatchString(window) {
			continue // a liveness probe: out of scope, see above
		}
		checked++
		if call[1] != "DISTRIBUTION_PROBES" {
			t.Errorf("line %d draws a per-machine verdict from %q instead of DISTRIBUTION_PROBES: "+
				"a sample written at the site is one that stops agreeing with the arithmetic "+
				"beside the constant", i+1, call[1])
		}
	}
	// A denominator, because a loop over nothing passes silently — and would
	// have passed before this suite had any per-machine verdict at all.
	if checked < 2 {
		t.Fatalf("only %d per-machine verdict(s) found; this suite draws two, "+
			"the distribution and the unlink", checked)
	}
}
