package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The dataplane witness verdicts (#486), held against planted defects.
//
// The gate reproduces #475, #483 and #484 as verdicts and #481's other half as
// a skip, so each test here plants exactly the defect one verdict names and
// requires the matching function to go red NAMING the provider and the object
// it did not obtain — a red without a name teaches nothing, and a verdict that
// cannot go red on its own defect is a comment (CLAUDE.md, « un commentaire
// n'est pas un contrôle »). tools/falsify/specs/dataplane-witness.json replays
// these with each guard neutralised.
//
// Everything runs through witnesslib.sh with planted inputs and stub
// transports, no host and no emulator: the functions read files and injected
// commands precisely so that this file can hold them.

// ---- verdict 3: running means a machine exists (#484) -----------------------

func TestARunningResourceWithNoMachineBehindItFailsNamingBoth(t *testing.T) {
	code, output := runWitness(t, map[string]string{
		"claims":    "i-123\tfeint-osc-i-123\n",
		"instances": `[{"name":"feint-osc-i-999","status":"Running"}]`,
	}, `witness_running_has_machines outscale "$DIR/claims" "$DIR/instances"`)
	if code == 0 {
		t.Fatalf("a resource the API calls running has no machine on the runtime and the verdict passed:\n%s", output)
	}
	// The absent machine's own sentence, not the state-mismatch fallback: the
	// next guard down also goes red on this input, with a diagnosis that sends
	// the reader to compare states instead of noticing nothing exists.
	for _, want := range []string{"FAIL:", "outscale", "i-123", "no machine feint-osc-i-123 exists", "#484"} {
		if !strings.Contains(output, want) {
			t.Errorf("the failure does not carry %q; a red that names nothing cannot be acted on:\n%s", want, output)
		}
	}
}

func TestARunningResourceWhoseMachineIsStoppedFails(t *testing.T) {
	code, output := runWitness(t, map[string]string{
		"claims":    "i-123\tfeint-osc-i-123\n",
		"instances": `[{"name":"feint-osc-i-123","status":"Stopped"}]`,
	}, `witness_running_has_machines outscale "$DIR/claims" "$DIR/instances"`)
	if code == 0 {
		t.Fatalf("the API says running, the host says Stopped, and the verdict passed:\n%s", output)
	}
	if !strings.Contains(output, "Stopped") {
		t.Errorf("the failure does not name the state the host answered:\n%s", output)
	}
}

func TestARunningResourceWithNoRecordedMachineFails(t *testing.T) {
	code, output := runWitness(t, map[string]string{
		"claims":    "i-9\t\n",
		"instances": `[]`,
	}, `witness_running_has_machines outscale "$DIR/claims" "$DIR/instances"`)
	if code == 0 {
		t.Fatalf("a running resource with no machine recorded passed:\n%s", output)
	}
	if !strings.Contains(output, "recorded no machine") {
		t.Errorf("the failure does not say the machine was never recorded:\n%s", output)
	}
}

// The accepting half: a guard that refuses everything passes every planted
// defect and breaks the product.
func TestAHealthyRunPassesTheRunningVerdict(t *testing.T) {
	code, output := runWitness(t, map[string]string{
		"claims":    "i-1\tfeint-osc-i-1\ni-2\tfeint-osc-i-2\n",
		"instances": `[{"name":"feint-osc-i-1","status":"Running"},{"name":"feint-osc-i-2","status":"Running"}]`,
	}, `witness_running_has_machines outscale "$DIR/claims" "$DIR/instances"`)
	if code != 0 {
		t.Fatalf("two running resources with two Running machines were refused:\n%s", output)
	}
	if !strings.Contains(output, "ok:") || !strings.Contains(output, "2 running resource(s)") {
		t.Errorf("the pass does not say what it compared; a verdict that compared nothing reads the same:\n%s", output)
	}
}

// ---- verdict 1: a claimed firewall leaves rule sets (#475) ------------------

const nakedInstance = `{"expanded_devices":{"eth0":{"type":"nic","network":"fnt-abc"}}}`
const guardedInstance = `{"expanded_devices":{"eth0":{"type":"nic","network":"fnt-abc","security.acls":"osc-abc123"}}}`

func TestAClaimedFirewallWithNoRuleSetFailsNamingProviderAndMachine(t *testing.T) {
	code, output := runWitness(t, map[string]string{
		"claims": "i-42\tfeint-osc-i-42\n",
	}, `stub_instance() { printf '%s' `+shellQuote(nakedInstance)+`; }
stub_acl() { echo unreachable >&2; exit 9; }
witness_firewalled_machines outscale "$DIR/claims" stub_instance stub_acl`)
	if code == 0 {
		t.Fatalf("a pack claiming enforced.firewall left a machine with zero rule sets and the verdict passed — that is #475 shipping again:\n%s", output)
	}
	// The zero-rule-sets sentence itself, not the not-ours fallback one guard
	// down, which also fires on an empty set with a diagnosis that blames the
	// wrong thing.
	for _, want := range []string{"FAIL:", "outscale", "feint-osc-i-42", "i-42", "no rule set on any NIC", "#475"} {
		if !strings.Contains(output, want) {
			t.Errorf("the failure does not carry %q:\n%s", want, output)
		}
	}
}

func TestAForeignRuleSetDoesNotAnswerForThePack(t *testing.T) {
	code, output := runWitness(t, map[string]string{
		"claims": "i-42\tfeint-osc-i-42\n",
	}, `stub_instance() { printf '%s' '{"expanded_devices":{"eth0":{"type":"nic","security.acls":"acltest"}}}'; }
stub_acl() { printf '%s' '{"description":"an operator rule set"}'; }
witness_firewalled_machines outscale "$DIR/claims" stub_instance stub_acl`)
	if code == 0 {
		t.Fatalf("an operator's hand-made ACL answered for the pack's handoff:\n%s", output)
	}
	if !strings.Contains(output, "none the emulator wrote") {
		t.Errorf("the failure does not say the rule sets found are not the emulator's:\n%s", output)
	}
}

func TestAFirewalledMachinePasses(t *testing.T) {
	code, output := runWitness(t, map[string]string{
		"claims": "i-42\tfeint-osc-i-42\n",
	}, `stub_instance() { printf '%s' `+shellQuote(guardedInstance)+`; }
stub_acl() { printf '%s' '{"description":"feint security group"}'; }
witness_firewalled_machines outscale "$DIR/claims" stub_instance stub_acl`)
	if code != 0 {
		t.Fatalf("a machine carrying the emulator's rule set was refused:\n%s", output)
	}
	if !strings.Contains(output, "ok:") {
		t.Errorf("the pass says nothing:\n%s", output)
	}
}

// Three outcomes, never two: a transport error is "nobody could look", which
// must never be read as "no witness" — that misreading is how a live account
// was once reported empty for forty minutes.
func TestAFirewallTransportErrorIsNotAnAbsence(t *testing.T) {
	code, output := runWitness(t, map[string]string{
		"claims": "i-42\tfeint-osc-i-42\n",
	}, `stub_instance() { echo "connection refused" >&2; return 1; }
stub_acl() { printf '%s' '{"description":"feint security group"}'; }
witness_firewalled_machines outscale "$DIR/claims" stub_instance stub_acl`)
	if code == 0 {
		t.Fatalf("a transport error passed as if the witness had been seen:\n%s", output)
	}
	if !strings.Contains(output, "cannot look") {
		t.Errorf("the failure reads as an absence rather than as an instrument failure:\n%s", output)
	}
	if strings.Contains(output, "#475") {
		t.Errorf("a transport error was blamed on the pack; that would be a false #475:\n%s", output)
	}
}

// ---- verdict 2: a claimed balancer distributes (#483) -----------------------

func TestABalancerTheRuntimeDoesNotHoldFails(t *testing.T) {
	code, output := runWitness(t, map[string]string{
		"claims":    "platform-front\n",
		"balancers": "",
	}, `witness_balancers_delivered outscale "$DIR/claims" "$DIR/balancers"`)
	if code == 0 {
		t.Fatalf("the API lists a placed, listening balancer, the runtime holds nothing, and the verdict passed — #483 shipping again:\n%s", output)
	}
	for _, want := range []string{"FAIL:", "outscale", "platform-front", "#483"} {
		if !strings.Contains(output, want) {
			t.Errorf("the failure does not carry %q:\n%s", want, output)
		}
	}
}

func TestABalancerHeldEmptyFails(t *testing.T) {
	code, output := runWitness(t, map[string]string{
		"claims":    "platform-front\n",
		"balancers": "platform-front\t0\t0\n",
	}, `witness_balancers_delivered outscale "$DIR/claims" "$DIR/balancers"`)
	if code == 0 {
		t.Fatalf("a balancer registered and holding no backend and no port passed:\n%s", output)
	}
	if !strings.Contains(output, "distributing nothing") {
		t.Errorf("the failure does not say the balancer distributes nothing:\n%s", output)
	}
}

func TestADeliveredBalancerPasses(t *testing.T) {
	code, output := runWitness(t, map[string]string{
		"claims":    "platform-front\n",
		"balancers": "platform-front\t1\t1\n",
	}, `witness_balancers_delivered outscale "$DIR/claims" "$DIR/balancers"`)
	if code != 0 {
		t.Fatalf("a delivered balancer was refused:\n%s", output)
	}
}

// ---- verdict 4: an unclaimed dataplane is not demanded (#481) ---------------

func TestAnUnclaimedCapabilityReadsAsAbsent(t *testing.T) {
	cases := []struct {
		name, health string
		claimed      bool
	}{
		{"the pack claims it", `{"enforced":{"balancing":["outscale"]}}`, true},
		{"another pack claims it", `{"enforced":{"balancing":["outscale"]}}`, false},
		{"nobody claims it", `{"enforced":{"balancing":[]}}`, false},
		{"a build older than the key", `{}`, false},
	}
	for _, tc := range cases {
		provider := "outscale"
		if tc.name == "another pack claims it" {
			provider = "scaleway"
		}
		code, output := runWitness(t, map[string]string{"health": tc.health},
			`witness_enforced `+provider+` balancing <"$DIR/health"`)
		if tc.claimed && code != 0 {
			t.Errorf("%s: the claim was not read (exit %d):\n%s", tc.name, code, output)
		}
		if !tc.claimed && code == 0 {
			t.Errorf("%s: an undeclared capability was read as claimed — the gate would demand what nobody promised (#481):\n%s", tc.name, output)
		}
	}
}

// ---- the readers prove they can find ---------------------------------------

// Every absence verdict above is void if its reader cannot find, so the
// controls are held here too: on a healthy library all three pass, and the
// falsification replay breaks a reader and requires them to go red.
func TestEveryReaderControlFindsItsPlantedWitness(t *testing.T) {
	code, output := runWitness(t, nil,
		`witness_machine_reader_control
witness_acl_reader_control
witness_balancer_reader_control`)
	if code != 0 {
		t.Fatalf("a reader control failed on the healthy library; every verdict it feeds is void:\n%s", output)
	}
	for _, want := range []string{"machine reader", "rule-set reader", "balancer reader"} {
		if !strings.Contains(output, want) {
			t.Errorf("the controls do not say %q passed:\n%s", want, output)
		}
	}
}

// The defect the machine control plants: a substring match lets another run's
// machine answer for this one (#219's lesson, on this gate's reader).
func TestTheMachineReaderRefusesASubstringMatch(t *testing.T) {
	code, output := runWitness(t, map[string]string{
		"claims":    "i-1\tfeint-osc-i-1\n",
		"instances": `[{"name":"feint-osc-i-11","status":"Running"}]`,
	}, `witness_running_has_machines outscale "$DIR/claims" "$DIR/instances"`)
	if code == 0 {
		t.Fatalf("feint-osc-i-11 answered for feint-osc-i-1; a substring verdict is another machine answering:\n%s", output)
	}
}

// ---- the gate wires the verdicts, and says when it cannot look --------------

func TestTheWitnessGateWiresEveryVerdictAndNamesItsSkips(t *testing.T) {
	body, err := os.ReadFile("witness.sh")
	if err != nil {
		t.Fatalf("read witness.sh: %v", err)
	}
	gate := string(body)
	for _, want := range []string{
		// The three verdicts and the claim check that guards each demand.
		"witness_running_has_machines",
		"witness_firewalled_machines",
		"witness_balancers_delivered",
		"witness_enforced",
		// A host nobody can look at is said out loud, never passed in silence.
		"NOTHING WAS MEASURED",
		// The unclaimed half skips by name instead of demanding (#481).
		"never promised is not demanded",
	} {
		if !strings.Contains(gate, want) {
			t.Errorf("witness.sh does not carry %q; the gate no longer wires what this suite holds", want)
		}
	}
	// The controls run before anything is judged, each as a call on a line of
	// its own — `strings.Contains` alone would still match a call somebody
	// neutralised behind `true ||`, which is exactly the mutation the replay
	// plants.
	for _, control := range []string{
		"witness_machine_reader_control",
		"witness_acl_reader_control",
		"witness_balancer_reader_control",
	} {
		called := false
		for _, line := range strings.Split(gate, "\n") {
			if strings.TrimSpace(line) == control {
				called = true
				break
			}
		}
		if !called {
			t.Errorf("witness.sh never calls %s on a line of its own; a broken reader would file false absences again", control)
		}
	}
}

// ---- harness ----------------------------------------------------------------

// runWitness sources the real witnesslib.sh and runs the given script under
// `set -uo pipefail` with the same fail/ok/skip the gate defines, planted
// files reachable as $DIR/<name>.
func runWitness(t *testing.T, files map[string]string, script string) (int, string) {
	t.Helper()
	requireTool(t, "bash")
	requireTool(t, "jq")

	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil { //nolint:gosec // planted test input
			t.Fatalf("write %s: %v", name, err)
		}
	}

	lib, err := filepath.Abs("witnesslib.sh")
	if err != nil {
		t.Fatalf("locate witnesslib.sh: %v", err)
	}
	if _, err := os.Stat(lib); err != nil {
		t.Fatalf("witnesslib.sh is not where this test looks (%s): %v", lib, err)
	}
	// runShell lives in functional_test.go: the same block ran both suites'
	// libraries, and a block written twice is a block fixed in one of them.
	return runShell(t, dir, lib, script)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
