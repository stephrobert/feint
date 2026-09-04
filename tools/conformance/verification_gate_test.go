package conformance

import (
	"path/filepath"
	"strings"
	"testing"
)

// The verification gate (#670), driven on planted payloads the way the
// functional library is: a gate that has never seen anything is
// indistinguishable from a gate that looks nowhere, so each refusal is
// planted before it is trusted.

func runVerificationGate(t *testing.T, script string) (int, string) {
	t.Helper()
	requireTool(t, "bash")
	requireTool(t, "jq")
	lib, err := filepath.Abs("guard.sh")
	if err != nil {
		t.Fatalf("locate guard.sh: %v", err)
	}
	return runShell(t, t.TempDir(), lib, script)
}

const healthyHealth = `{"machines":"incus-ovn","verification":{"held":12,"broken":0,"unreadable":1,"repaired":0}}`

const oneBrokenHealth = `{"machines":"incus-ovn","verification":{"held":12,"broken":1,"unreadable":0,"repaired":0}}`

// The planted state: one resource whose Runtime carries the claim, beside one
// that held, so the gate has to pick the right one.
const brokenState = `{"format":"feint-snapshot","version":3,"resources":[
 {"ID":"web-0","Kind":"server","Runtime":{"machine":"feint-scw-web-0","verified":"held"}},
 {"ID":"web-1","Kind":"server","Runtime":{"machine":"feint-scw-web-1","verified":"broken: door(203.0.113.2) want via 169.254.0.1 dev eth0, got via 10.77.0.1 dev eth0"}}
]}`

func TestTheVerificationGateRefusesABrokenClaim(t *testing.T) {
	code, out := runVerificationGate(t, `guard_verification_from '`+oneBrokenHealth+`' '`+brokenState+`'`)
	if code == 0 {
		t.Fatalf("a broken claim passed the gate:\n%s", out)
	}
	if !strings.Contains(out, "FAIL: 1 claim(s) broken") {
		t.Errorf("the refusal does not say what it refused:\n%s", out)
	}
}

// TestTheVerificationGateNamesTheClaim: the counter says how many, the state
// says which — and the refusal prints the claim, with what was wanted and
// what was found, rather than the number alone.
func TestTheVerificationGateNamesTheClaim(t *testing.T) {
	_, out := runVerificationGate(t, `guard_verification_from '`+oneBrokenHealth+`' '`+brokenState+`'`)
	if !strings.Contains(out, "server web-1: broken: door(203.0.113.2) want via 169.254.0.1 dev eth0, got via 10.77.0.1 dev eth0") {
		t.Fatalf("the refusal does not name the claim:\n%s", out)
	}
	if strings.Contains(out, "web-0") {
		t.Errorf("the refusal names a resource that held:\n%s", out)
	}
}

// A pass that could read less than it read is a pass that measured its own
// blindness, which is a different fact from success.
func TestTheVerificationGateRefusesAPassThatCouldNotRead(t *testing.T) {
	code, out := runVerificationGate(t, `guard_verification_from '{"machines":"incus-ovn","verification":{"held":2,"broken":0,"unreadable":9,"repaired":0}}' '{"resources":[]}'`)
	if code == 0 || !strings.Contains(out, "9 claim(s) could not be read against 2 held") {
		t.Fatalf("a pass that could not read passed the gate (exit %d):\n%s", code, out)
	}
}

// The reader proves it can find before it judges: a payload with no counters
// is an emulator that never asked, and zero would pass it.
func TestTheVerificationGateRefusesAPayloadWithNoCounters(t *testing.T) {
	code, out := runVerificationGate(t, `guard_verification_from '{"machines":"incus-ovn"}' '{"resources":[]}'`)
	if code == 0 || !strings.Contains(out, "carries no verification counters") {
		t.Fatalf("an emulator that never read its machines back passed the gate (exit %d):\n%s", code, out)
	}
}

// The accepting halves: a healthy pass prints its counters and passes, and a
// pass with no runtime is told so rather than judged.
func TestTheVerificationGatePassesAHealthyPassAndSkipsARuntimelessOne(t *testing.T) {
	code, out := runVerificationGate(t, `guard_verification_from '`+healthyHealth+`' '{"resources":[]}'`)
	if code != 0 || !strings.Contains(out, "verification: held=12 broken=0 unreadable=1 repaired=0") {
		t.Fatalf("a healthy pass did not pass (exit %d):\n%s", code, out)
	}
	code, out = runVerificationGate(t, `guard_verification_from '{"machines":"none"}' ''`)
	if code != 0 || !strings.Contains(out, "not judged") {
		t.Fatalf("a runtime-less pass was judged (exit %d):\n%s", code, out)
	}
}
