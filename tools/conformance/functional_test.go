package conformance

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// What a stack must prove (#503), held against planted defects.
//
// Every verdict in functionallib.sh names a way a stack can be green and dead:
// a service that does not listen, a published address that reaches nothing, a
// port a rule opens that refuses, a port no rule opens that answers, a
// neighbour unreachable, a foreign network reachable, a balancer that hands
// connections to nobody or to a backend it withheld. Each test below plants
// exactly one of those and requires the matching function to go red NAMING what
// it did not obtain — a red without a name teaches nothing, and a verdict that
// cannot go red on its own defect is a comment (CLAUDE.md, « un commentaire
// n'est pas un contrôle »).
//
// The accepting half is tested too, and not as ceremony: a guard that refuses
// everything passes every planted defect and breaks the product.
//
// tools/falsify/specs/stack-functional-proof.json replays these with each guard
// neutralised.
//
// Everything runs through functionallib.sh with planted files and stub probes,
// no host and no emulator: the functions read files and injected commands
// precisely so that this file can hold them.

// ---- the readers prove they can find --------------------------------------

func TestTheListenReaderReadsProcNetTcp(t *testing.T) {
	code, output := runFunctional(t, nil, `fnl_listen_reader_control`)
	if code != 0 {
		t.Fatalf("the listen reader cannot read a planted /proc/net/tcp:\n%s", output)
	}
	// The near miss is the half that matters: an established connection on a
	// high port is not a listener, and a reader that counted it would report a
	// machine as serving the port it dialled from.
	_, output = runFunctional(t, nil,
		`printf '%s\n' '   0: 0100007F:8AE1 0100007F:1F90 01 0 0 0 0' | fnl_listening_ports | sed 's/^/PORT /'`)
	if strings.Contains(output, "PORT ") {
		t.Errorf("an established connection was reported as a listener:\n%s", output)
	}
}

func TestTheNameReaderResolvesBothNamingShapes(t *testing.T) {
	code, output := runFunctional(t, nil, `fnl_name_reader_control`)
	if code != 0 {
		t.Fatalf("the name reader cannot resolve a machine, or matched a prefix:\n%s", output)
	}
}

func TestTheDeliveryReaderReadsNoProseAsAnAddress(t *testing.T) {
	code, output := runFunctional(t, nil, `fnl_delivery_reader_control`)
	if code != 0 {
		t.Fatalf("the delivery reader misreads a balancer record:\n%s", output)
	}
}

// ---- every machine started: #484 as a control ------------------------------

const twoRunning = `[{"name":"m-1","status":"Running"},{"name":"m-2","status":"Running"}]`

func TestAStackWhoseMachineDidNotStartFailsAlthoughApplyReturnedZero(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"machines":  "platform-web-0\ts-1\trunning\tm-1\nplatform-web-1\ts-2\tstopped\tm-2\n",
		"instances": twoRunning,
	}, `fnl_every_machine_started scaleway "$DIR/machines" "$DIR/instances" 2 running`)
	if code == 0 {
		t.Fatalf("a stack with a machine left stopped passed, which is the green apply #503 was opened about:\n%s", output)
	}
	for _, want := range []string{"FAIL:", "scaleway", "platform-web-1", "s-2", "stopped", "#484"} {
		if !strings.Contains(output, want) {
			t.Errorf("the failure does not carry %q; a red that names nothing cannot be acted on:\n%s", want, output)
		}
	}
}

func TestARunningMachineWithNoRuntimeMachineFails(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"machines":  "platform-app\ti-9\trunning\t\n",
		"instances": `[]`,
	}, `fnl_every_machine_started outscale "$DIR/machines" "$DIR/instances" 1 running`)
	if code == 0 {
		t.Fatalf("a running machine that recorded no runtime machine passed:\n%s", output)
	}
	if !strings.Contains(output, "recorded no runtime machine") || !strings.Contains(output, "platform-app") {
		t.Errorf("the failure does not say which machine has nothing behind it:\n%s", output)
	}
}

func TestAMachineTheHostHoldsStoppedFails(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"machines":  "platform-web-0\ts-1\trunning\tm-1\n",
		"instances": `[{"name":"m-1","status":"Stopped"}]`,
	}, `fnl_every_machine_started scaleway "$DIR/machines" "$DIR/instances" 1 running`)
	if code == 0 {
		t.Fatalf("the API says running, the host says Stopped, and the verdict passed:\n%s", output)
	}
	if !strings.Contains(output, "Stopped") {
		t.Errorf("the failure does not name the state the host answered:\n%s", output)
	}
}

func TestAMachineReaderBelowTheDeclaredFloorAccusesItself(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"machines":  "platform-web-0\ts-1\trunning\tm-1\n",
		"instances": twoRunning,
	}, `fnl_every_machine_started scaleway "$DIR/machines" "$DIR/instances" 6 running`)
	if code == 0 {
		t.Fatalf("the reader found one machine where the stack promises six and the verdict passed:\n%s", output)
	}
	if !strings.Contains(output, "the reader is the suspect") {
		t.Errorf("the failure blames the cloud rather than the instrument:\n%s", output)
	}
}

func TestAHealthyStackPassesTheStartedVerdict(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"machines":  "platform-web-0\ts-1\trunning\tm-1\nplatform-web-1\ts-2\trunning\tm-2\n",
		"instances": twoRunning,
	}, `fnl_every_machine_started scaleway "$DIR/machines" "$DIR/instances" 2 running`)
	if code != 0 {
		t.Fatalf("two started machines with two Running containers were refused:\n%s", output)
	}
	if !strings.Contains(output, "2 machine(s)") {
		t.Errorf("the pass does not say what it compared; a verdict that compared nothing reads the same:\n%s", output)
	}
}

// ---- service ---------------------------------------------------------------

func TestAServiceThatDoesNotListenFailsNamingMachineAndPort(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"listen": "22\n53\n",
	}, `fnl_service_listens scaleway "platform-web-0 (platform-web.service)" m-1 443 "$DIR/listen"`)
	if code == 0 {
		t.Fatalf("a machine listening on neither the declared port nor anything like it passed:\n%s", output)
	}
	for _, want := range []string{"FAIL:", "platform-web-0", "m-1", "443", "22 53"} {
		if !strings.Contains(output, want) {
			t.Errorf("the failure does not carry %q:\n%s", want, output)
		}
	}
}

func TestAServiceListeningPasses(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"listen": "22\n443\n",
	}, `fnl_service_listens scaleway platform-web-0 m-1 443 "$DIR/listen"`)
	if code != 0 {
		t.Fatalf("a machine listening on the declared port was refused:\n%s", output)
	}
}

func TestAPublishedAddressThatAnswersNothingFails(t *testing.T) {
	code, output := runFunctional(t, nil,
		`fnl_service_answers scaleway platform-web-0 m-1 203.0.113.3 443 ""`)
	if code == 0 {
		t.Fatalf("a published address that answered nothing passed, on a service proved alive inside:\n%s", output)
	}
	for _, want := range []string{"FAIL:", "203.0.113.3:443", "the service is alive"} {
		if !strings.Contains(output, want) {
			t.Errorf("the failure does not carry %q:\n%s", want, output)
		}
	}
}

func TestAPublishedAddressThatReachesAnotherMachineFails(t *testing.T) {
	code, output := runFunctional(t, nil,
		`fnl_service_answers scaleway platform-web-0 m-1 203.0.113.3 443 m-2`)
	if code == 0 {
		t.Fatalf("the address of one machine answered from another and the verdict passed; that is #484's duplicate address, unseen:\n%s", output)
	}
	if !strings.Contains(output, "m-2") || !strings.Contains(output, "m-1") {
		t.Errorf("the failure names neither the machine expected nor the one that answered:\n%s", output)
	}
}

// ---- firewall: the pair, and its refusal to judge without a listener --------

const probeStub = `stub_probe() { # machine address port
  case "$3" in
    8080) return ${OPEN_CODE:-0} ;;
    9090) return ${CLOSED_CODE:-1} ;;
  esac
  return 1
}
`

func TestTheFirewallPairRefusesToJudgeAPortNothingListensOn(t *testing.T) {
	// The closed half is the one that matters: a refusal on a port nothing
	// serves is a dead service, and reading it as a firewall verdict is the
	// exact mistake #219 shipped.
	code, output := runFunctional(t, map[string]string{
		"listen": "22\n8080\n",
	}, probeStub+`fnl_firewall_pair scaleway platform-web-0 m-web-0 platform-app 10.30.2.4 8080 9090 "$DIR/listen" stub_probe`)
	if code == 0 {
		t.Fatalf("the pair drew a firewall verdict from a port nothing was listening on:\n%s", output)
	}
	for _, want := range []string{"9090", "could not be told from a dead service"} {
		if !strings.Contains(output, want) {
			t.Errorf("the failure does not carry %q:\n%s", want, output)
		}
	}
}

func TestAnOpenPortThatRefusesFailsNamingIt(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"listen": "8080\n9090\n",
	}, `OPEN_CODE=1
`+probeStub+`fnl_firewall_pair scaleway platform-web-0 m-web-0 platform-app 10.30.2.4 8080 9090 "$DIR/listen" stub_probe`)
	if code == 0 {
		t.Fatalf("a port the group opens refused the connection and the verdict passed — a firewall check with no positive half:\n%s", output)
	}
	for _, want := range []string{"FAIL:", "platform-app", "10.30.2.4:8080", "does not reach"} {
		if !strings.Contains(output, want) {
			t.Errorf("the failure does not carry %q:\n%s", want, output)
		}
	}
}

func TestAClosedPortThatAnswersFailsNamingIt(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"listen": "8080\n9090\n",
	}, `CLOSED_CODE=0
`+probeStub+`fnl_firewall_pair scaleway platform-web-0 m-web-0 platform-app 10.30.2.4 8080 9090 "$DIR/listen" stub_probe`)
	if code == 0 {
		t.Fatalf("a port no rule opens answered and the verdict passed, which is #475 waved through:\n%s", output)
	}
	for _, want := range []string{"FAIL:", "10.30.2.4:9090", "not enforced"} {
		if !strings.Contains(output, want) {
			t.Errorf("the failure does not carry %q:\n%s", want, output)
		}
	}
}

func TestAProbeThatCouldNotBeMadeIsNotARefusal(t *testing.T) {
	// Three outcomes, never two. A probe that could not run at all is "nobody
	// could look", and reading it as "refused" turns a broken console into a
	// firewall doing its job.
	code, output := runFunctional(t, map[string]string{
		"listen": "8080\n9090\n",
	}, `CLOSED_CODE=2
`+probeStub+`fnl_firewall_pair scaleway platform-web-0 m-web-0 platform-app 10.30.2.4 8080 9090 "$DIR/listen" stub_probe`)
	if code == 0 {
		t.Fatalf("a probe that could not be made at all was read as a refusal, and the firewall passed on it:\n%s", output)
	}
	if !strings.Contains(output, "cannot look") {
		t.Errorf("the failure does not say that nobody could look:\n%s", output)
	}
}

func TestAHealthyFirewallPairPasses(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"listen": "8080\n9090\n",
	}, probeStub+`fnl_firewall_pair scaleway platform-web-0 m-web-0 platform-app 10.30.2.4 8080 9090 "$DIR/listen" stub_probe`)
	if code != 0 {
		t.Fatalf("an open port that answers and a closed port that refuses were refused:\n%s", output)
	}
	if !strings.Contains(output, "both listening inside") {
		t.Errorf("the pass does not say the listen proof was taken:\n%s", output)
	}
}

// ---- network ---------------------------------------------------------------

func TestANeighbourThatCannotBeReachedFails(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"listen": "443\n",
	}, `stub_probe() { return 1; }
fnl_reaches scaleway platform-web-0 m-web-0 platform-web-1 10.30.1.11 443 "$DIR/listen" stub_probe`)
	if code == 0 {
		t.Fatalf("two machines of one network could not reach each other and the verdict passed:\n%s", output)
	}
	for _, want := range []string{"platform-web-1", "10.30.1.11:443", "carries nothing between two of its own machines"} {
		if !strings.Contains(output, want) {
			t.Errorf("the failure does not carry %q:\n%s", want, output)
		}
	}
}

func TestReachesRefusesToJudgeADeadListener(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"listen": "22\n",
	}, `stub_probe() { return 0; }
fnl_reaches scaleway platform-web-0 m-web-0 platform-web-1 10.30.1.11 443 "$DIR/listen" stub_probe`)
	if code == 0 {
		t.Fatalf("reachability was reported on a port nothing listens on:\n%s", output)
	}
	if !strings.Contains(output, "measuring the listener, not the network") {
		t.Errorf("the failure does not say what it would have measured:\n%s", output)
	}
}

func TestAnIsolationBreachFailsNamingTheAddress(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"listen": "22\n",
	}, `stub_probe() { return 0; }
fnl_isolated scaleway platform-web-0 m-web-0 platform-bastion 10.40.1.2 22 "$DIR/listen" stub_probe`)
	if code == 0 {
		t.Fatalf("a machine of another VPC was reachable and the verdict passed, on the claim the product is sold on:\n%s", output)
	}
	for _, want := range []string{"10.40.1.2:22", "nothing peers"} {
		if !strings.Contains(output, want) {
			t.Errorf("the failure does not carry %q:\n%s", want, output)
		}
	}
}

func TestIsolationRefusesToJudgeADeadMachine(t *testing.T) {
	// #219 in one line: a machine that never booted refuses exactly as an
	// isolated one does, and this suite read the first as a pass.
	code, output := runFunctional(t, map[string]string{
		"listen": "53\n",
	}, `stub_probe() { return 1; }
fnl_isolated scaleway platform-web-0 m-web-0 platform-bastion 10.40.1.2 22 "$DIR/listen" stub_probe`)
	if code == 0 {
		t.Fatalf("isolation was reported from a machine that was not listening at all:\n%s", output)
	}
	if !strings.Contains(output, "dead machine rather than isolation") {
		t.Errorf("the failure does not name what it would have measured:\n%s", output)
	}
}

func TestAHealthyIsolationPasses(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"listen": "22\n",
	}, `stub_probe() { return 1; }
fnl_isolated scaleway platform-web-0 m-web-0 platform-bastion 10.40.1.2 22 "$DIR/listen" stub_probe`)
	if code != 0 {
		t.Fatalf("a listening machine of another VPC that could not be reached was refused:\n%s", output)
	}
}

// ---- balancer --------------------------------------------------------------

const twoBackends = "10.50.1.4\tm-web-a\n10.50.3.4\tm-web-b\n"

func TestABalancerAnsweringFromAWithheldBackendFails(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"hits":     "m-web-a\nm-web-b\n",
		"machines": twoBackends,
	}, `fnl_balancer_delivers outscale platform-front "$DIR/hits" 10.50.1.4 "10.50.3.4 (outside the block (#457))" "$DIR/machines"`)
	if code == 0 {
		t.Fatalf("a backend the runtime recorded as withheld served a connection and the verdict passed:\n%s", output)
	}
	if !strings.Contains(output, "m-web-b") || !strings.Contains(output, "withheld") {
		t.Errorf("the failure does not name the backend that answered:\n%s", output)
	}
}

func TestABalancerAnsweringFromAnUnknownMachineFails(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"hits":     "m-web-a\nsomething-else\n",
		"machines": twoBackends,
	}, `fnl_balancer_delivers outscale platform-front "$DIR/hits" 10.50.1.4 "" "$DIR/machines"`)
	if code == 0 {
		t.Fatalf("a machine that is no backend at all served the balancer's address and the verdict passed:\n%s", output)
	}
	if !strings.Contains(output, "something-else") {
		t.Errorf("the failure does not name what answered:\n%s", output)
	}
}

func TestABalancerThatDropsProbesFails(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"hits":     "m-web-a\n\nm-web-a\n",
		"machines": twoBackends,
	}, `fnl_balancer_delivers outscale platform-front "$DIR/hits" 10.50.1.4 "" "$DIR/machines"`)
	if code == 0 {
		t.Fatalf("a balancer that dropped a third of its probes passed as distributing:\n%s", output)
	}
	if !strings.Contains(output, "2 of 3") {
		t.Errorf("the failure does not say how many probes went unanswered:\n%s", output)
	}
}

func TestADistributedBackendThatNeverAnswersFails(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"hits":     "m-web-a\nm-web-a\nm-web-a\nm-web-a\nm-web-a\nm-web-a\n",
		"machines": twoBackends,
	}, `fnl_balancer_delivers outscale platform-front "$DIR/hits" "10.50.1.4,10.50.3.4" "" "$DIR/machines"`)
	if code == 0 {
		t.Fatalf("a balancer sending every connection to one of two delivered backends passed as distributing, which is #483 one size smaller:\n%s", output)
	}
	if !strings.Contains(output, "m-web-b") || !strings.Contains(output, "#483") {
		t.Errorf("the failure does not name the backend that received nothing:\n%s", output)
	}
}

func TestABalancerWithNoDeliveredBackendFails(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"hits":     "\n\n",
		"machines": twoBackends,
	}, `fnl_balancer_delivers outscale platform-front "$DIR/hits" "" "10.50.3.4 (outside)" "$DIR/machines"`)
	if code == 0 {
		t.Fatalf("a balancer registered with a listener and a backend, delivering to nobody, passed — that is #483 itself:\n%s", output)
	}
	if !strings.Contains(output, "#483") {
		t.Errorf("the failure does not name the defect it reproduces:\n%s", output)
	}
}

func TestADeliveredBackendNoMachineCarriesFails(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"hits":     "m-web-a\n",
		"machines": twoBackends,
	}, `fnl_balancer_delivers outscale platform-front "$DIR/hits" 10.99.99.99 "" "$DIR/machines"`)
	if code == 0 {
		t.Fatalf("the pack recorded a delivered address no machine carries and the verdict passed:\n%s", output)
	}
	if !strings.Contains(output, "10.99.99.99") {
		t.Errorf("the failure does not name the address the record and the host disagree on:\n%s", output)
	}
}

func TestAHealthyBalancerPassesAndSaysWhatItDidNotExercise(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"hits":     "m-web-a\nm-web-a\nm-web-a\n",
		"machines": twoBackends,
	}, `fnl_balancer_delivers outscale platform-front "$DIR/hits" 10.50.1.4 "10.50.3.4 (outside the block (#457))" "$DIR/machines"`)
	if code != 0 {
		t.Fatalf("a balancer answering every probe from its only delivered backend was refused:\n%s", output)
	}
	// The bound is named rather than passed over: one backend is not a spread,
	// and a run that says "distributes" without saying over how many is the
	// half-truth this repository exists to avoid.
	if !strings.Contains(output, "SKIP") || !strings.Contains(output, "#457") {
		t.Errorf("the pass does not name the spread it could not exercise:\n%s", output)
	}
}

func TestAWithdrawnBackendStillAnsweringFails(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"hits": "m-web-a\nm-web-b\n",
	}, `fnl_backend_withdrawn outscale platform-front m-web-a "$DIR/hits" 1`)
	if code == 0 {
		t.Fatalf("a backend the API unregistered went on receiving connections and the verdict passed:\n%s", output)
	}
	if !strings.Contains(output, "m-web-a") || !strings.Contains(output, "did not follow the control plane") {
		t.Errorf("the failure does not name the backend that kept receiving:\n%s", output)
	}
}

func TestAWithdrawalThatSilencedEveryRemainingBackendFails(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"hits": "\n\n",
	}, `fnl_backend_withdrawn outscale platform-front m-web-a "$DIR/hits" 1`)
	if code == 0 {
		t.Fatalf("unregistering one backend took the whole balancer down and the verdict passed:\n%s", output)
	}
	if !strings.Contains(output, "took the whole balancer with it") {
		t.Errorf("the failure does not say the withdrawal was too wide:\n%s", output)
	}
}

func TestAWithdrawalWithNothingLeftMustSilenceTheBalancer(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"hits": "m-web-c\n",
	}, `fnl_backend_withdrawn outscale platform-front m-web-a "$DIR/hits" 0`)
	if code == 0 {
		t.Fatalf("a balancer answered with no backend registered and the verdict passed:\n%s", output)
	}
	if !strings.Contains(output, "the control plane does not describe") {
		t.Errorf("the failure does not say what is wrong with an answer here:\n%s", output)
	}
}

func TestAHealthyWithdrawalPasses(t *testing.T) {
	code, output := runFunctional(t, map[string]string{
		"hits": "\n\n",
	}, `fnl_backend_withdrawn outscale platform-front m-web-a "$DIR/hits" 0`)
	if code != 0 {
		t.Fatalf("a balancer that went quiet once its last backend was unregistered was refused:\n%s", output)
	}
}

// ---- the declarations, and the gate's own wiring ----------------------------

// stackProof is what examples/stacks/<name>/proof.json must carry. Only the
// fields this test judges; the harness reads the rest.
type stackProof struct {
	Provider    string          `json:"provider"`
	MachineKind string          `json:"machine_kind"`
	Expect      int             `json:"expect_machines"`
	Running     string          `json:"running_state"`
	Service     json.RawMessage `json:"service"`
	Firewall    json.RawMessage `json:"firewall"`
	Network     json.RawMessage `json:"network"`
	Balancer    json.RawMessage `json:"balancer"`
}

// TestEveryStackTheGateNamesDeclaresWhatItMustProve is the structural half of
// the rule the gate enforces at run time: a family that is simply absent is a
// finding, and a family excused must carry a reason.
//
// It reads the gate's own default list rather than a list written here, so
// adding a stack to functional.sh without declaring what it must prove fails in
// milliseconds instead of at the first `feint up`.
func TestEveryStackTheGateNamesDeclaresWhatItMustProve(t *testing.T) {
	gate, err := os.ReadFile("functional.sh")
	if err != nil {
		t.Fatalf("read functional.sh: %v", err)
	}
	line := ""
	for _, candidate := range strings.Split(string(gate), "\n") {
		if strings.HasPrefix(strings.TrimSpace(candidate), "DEFAULT_STACKS=(") {
			line = strings.TrimSpace(candidate)
		}
	}
	if line == "" {
		t.Fatal("functional.sh names no default stack list; this test cannot find its population, and a test that judges nothing is worse than none")
	}
	inner := line[strings.Index(line, "(")+1 : strings.LastIndex(line, ")")]
	stacks := strings.Fields(inner)
	if len(stacks) == 0 {
		t.Fatal("the gate's default stack list is empty")
	}

	for _, stack := range stacks {
		path := filepath.Join("..", "..", "examples", "stacks", stack, "proof.json")
		raw, err := os.ReadFile(path) //nolint:gosec // a path built from the gate's own list
		if err != nil {
			t.Errorf("%s: the gate applies this stack and %s does not exist; it would be measured against nothing", stack, path)
			continue
		}
		var proof stackProof
		if err := json.Unmarshal(raw, &proof); err != nil {
			t.Errorf("%s: proof.json is not valid JSON: %v", stack, err)
			continue
		}
		if proof.Provider == "" || proof.MachineKind == "" || proof.Running == "" || proof.Expect <= 0 {
			t.Errorf("%s: proof.json must name provider, machine_kind, running_state and a non-zero expect_machines; without the floor a reader that found nothing reads as a cloud that holds nothing", stack)
		}
		for name, family := range map[string]json.RawMessage{
			"service": proof.Service, "firewall": proof.Firewall,
			"network": proof.Network, "balancer": proof.Balancer,
		} {
			if len(family) == 0 {
				t.Errorf("%s: proof.json declares nothing about its %s; a stack that says nothing about what its machines must do is the silence #503 was opened about", stack, name)
				continue
			}
			assertReasoned(t, stack, name, family)
		}
	}
}

// assertReasoned refuses an excuse with no reason, at every depth a family can
// carry one. `{"skip": ""}` would read as a decision and excuse everything.
func assertReasoned(t *testing.T, stack, name string, family json.RawMessage) {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(family, &doc); err != nil {
		return // a family that is not an object carries no skip to judge
	}
	if raw, ok := doc["skip"]; ok {
		var reason string
		if err := json.Unmarshal(raw, &reason); err != nil || strings.TrimSpace(reason) == "" {
			t.Errorf("%s: the %s family is excused with no reason; « non trié » and « hors périmètre » are not the same thing", stack, name)
		}
		return
	}
	for key, nested := range doc {
		if strings.HasPrefix(key, "_") {
			continue
		}
		if len(nested) > 0 && nested[0] == '{' {
			assertReasoned(t, stack, name+"."+key, nested)
		}
	}
}

// TestTheGateRunsItsReaderControlsBeforeAnyVerdict holds the wiring the
// mutations cannot reach from the library alone: a reader that stopped being
// controlled files false absences again, which is how a false #475 was very
// nearly published.
func TestTheGateRunsItsReaderControlsBeforeAnyVerdict(t *testing.T) {
	gate := readGate(t)
	for _, control := range []string{
		"fnl_listen_reader_control",
		"fnl_name_reader_control",
		"fnl_delivery_reader_control",
	} {
		called := false
		for _, line := range strings.Split(gate, "\n") {
			if strings.TrimSpace(line) == control {
				called = true
				break
			}
		}
		if !called {
			t.Errorf("functional.sh never calls %s on a line of its own; a broken reader would file false absences again", control)
		}
	}
}

// TestTheRestartGoesThroughPowerAndNeverThroughReboot is #547 written as a
// control on this gate rather than on the emulator.
//
// A reboot through the API leaves the container's pid and uptime untouched
// (measured 2026-08-27), so a restart assertion sent through it cannot fail —
// and a control that cannot fail is a demonstration. The day #547 is fixed this
// test is what has to be revisited on purpose, rather than the gate silently
// going back to measuring nothing.
func TestTheRestartGoesThroughPowerAndNeverThroughReboot(t *testing.T) {
	gate := readGate(t)
	for _, forbidden := range []string{"action=reboot", "RebootVms"} {
		if strings.Contains(gate, forbidden) {
			t.Errorf("functional.sh carries %q; #547 measured a reboot leaving the container's pid and uptime untouched, so the service would be asserted to survive a restart that never happened", forbidden)
		}
	}
	for _, want := range []string{`action=poweroff`, `action=poweron`, `action=StopVms`, `action=StartVms`} {
		if !strings.Contains(gate, want) {
			t.Errorf("functional.sh does not carry %q; the restart must be a real stop and start on every provider it drives", want)
		}
	}
}

func readGate(t *testing.T) string {
	t.Helper()
	gate, err := os.ReadFile("functional.sh")
	if err != nil {
		t.Fatalf("read functional.sh: %v", err)
	}
	return string(gate)
}

// ---- harness ----------------------------------------------------------------

// runFunctional sources the real functionallib.sh and runs the given script
// under `set -uo pipefail` with the same fail/ok/skip the gate defines, planted
// files reachable as $DIR/<name>.
func runFunctional(t *testing.T, files map[string]string, script string) (int, string) {
	t.Helper()
	requireTool(t, "bash")
	requireTool(t, "jq")

	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil { //nolint:gosec // planted test input
			t.Fatalf("write %s: %v", name, err)
		}
	}
	lib, err := filepath.Abs("functionallib.sh")
	if err != nil {
		t.Fatalf("locate functionallib.sh: %v", err)
	}
	if _, err := os.Stat(lib); err != nil {
		t.Fatalf("functionallib.sh is not where this test looks (%s): %v", lib, err)
	}
	return runShell(t, dir, lib, script)
}

// runShell sources one shell library and runs a script against it, with the
// same fail/ok/skip the gates define and planted files reachable as $DIR/<name>.
//
// Shared with witness_test.go rather than written twice: the two suites hold
// two libraries the same way, and the block that ran them used to be one
// function per suite. A block written twice is a block fixed in one of them,
// which is this repository's own rule for the packs and applies to their tests
// for the same reason.
func runShell(t *testing.T, dir, lib, script string) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", "-c", //nolint:gosec // fixed library, test-controlled script
		`set -uo pipefail
fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "  ok: $*"; }
skip() { echo "  SKIP: $*" >&2; }
DIR="$1"
. "$2"
`+script,
		"bash", dir, lib)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("run the shell script: %v\n%s", err, out)
		}
		code = exit.ExitCode()
	}
	return code, string(out)
}
