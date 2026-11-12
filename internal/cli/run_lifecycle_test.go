package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/s3"
)

// The lab half of `feint run` (#848), in the parts a unit test can hold.
//
// What it cannot hold is the lifecycle itself: `start` re-execs
// os.Executable(), which under `go test` is the test binary, so a unit test of
// the spawn would run the test suite again with `serve` arguments. That half
// is proved against the built binary in run_lab_conformance_test.go, and this
// file deliberately does not pretend to cover it.

// TestARunStripsEveryVariableAPackMeasuredAsAReroute is cited by
// hazardVariables and by childEnvironment.apply.
//
// It asserts the discovery and the removal separately, because they fail
// differently: a discovery that finds nothing strips nothing and still looks
// green from the child's side, which is the silent half.
func TestARunStripsEveryVariableAPackMeasuredAsAReroute(t *testing.T) {
	found := hazardVariables()

	// The three the Outscale pack measured (#342): OSC_PROFILE makes the
	// provider read ~/.osc/config.json and ignore the endpoint export, and the
	// two legacy keys are what every client falls back to.
	for _, want := range []string{"OSC_PROFILE", "OUTSCALE_ACCESSKEYID", "OUTSCALE_SECRETKEYID"} {
		if !slices.Contains(found, want) {
			t.Errorf("the packs measure %s as a reroute and this run would hand it to the child: %v",
				want, found)
		}
	}

	childEnv := childEnvironment{strip: found, set: []string{"SCW_API_URL=http://127.0.0.1:4599"}}
	got := childEnv.apply([]string{
		"OSC_PROFILE=production",
		"PATH=/usr/bin",
		"SCW_API_URL=https://api.scaleway.com",
	})

	for _, pair := range got {
		if strings.HasPrefix(pair, "OSC_PROFILE=") {
			t.Errorf("a measured reroute reached the child: %v", got)
		}
		if pair == "SCW_API_URL=https://api.scaleway.com" {
			t.Errorf("the operator's own endpoint beat this command's: %v", got)
		}
	}
	if !slices.Contains(got, "PATH=/usr/bin") {
		t.Errorf("an ordinary variable was dropped: %v", got)
	}
	if !slices.Contains(got, "SCW_API_URL=http://127.0.0.1:4599") {
		t.Errorf("this command's own export is missing: %v", got)
	}
	// Exactly once, not shadowed: a duplicated name is a coin toss for a child
	// that reads it with C getenv.
	if n := slices.IndexFunc(got, func(s string) bool { return strings.HasPrefix(s, "SCW_API_URL=") }); n < 0 {
		t.Fatal("no SCW_API_URL at all")
	}
	count := 0
	for _, pair := range got {
		if strings.HasPrefix(pair, "SCW_API_URL=") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("SCW_API_URL appears %d times: %v", count, got)
	}
}

// TestARunExportsTheSameEnvironmentAsFeintEnv is cited by providerExports.
//
// Asserted against the packs rather than against a copy of their values,
// because a copy is what drifted the last time this command kept a second
// list of what the packs publish (acceptedFakes, #849).
func TestARunExportsTheSameEnvironmentAsFeintEnv(t *testing.T) {
	const endpoint = "http://127.0.0.1:4599"
	packs, err := packsFor(emulator.DefaultEnv())
	if err != nil {
		t.Fatalf("packs: %v", err)
	}
	if len(packs) == 0 {
		t.Fatal("no pack at all; this test would pass on an empty build")
	}

	// One provider: exactly that pack's vars, and no other pack's.
	for _, pack := range packs {
		exports, served, err := providerExports(endpoint, pack.Name())
		if err != nil {
			t.Fatalf("%s: %v", pack.Name(), err)
		}
		if len(served) != 1 || served[0] != pack.Name() {
			t.Fatalf("--provider %s served %v", pack.Name(), served)
		}
		want := pack.Env(endpoint).Vars
		if len(exports) != len(want) {
			t.Errorf("%s publishes %d variable(s) and the child would get %d: %v",
				pack.Name(), len(want), len(exports), exports)
		}
		for name, value := range want {
			if !slices.Contains(exports, name+"="+value) {
				t.Errorf("%s publishes %s and the child would not see it", pack.Name(), name)
			}
		}
	}

	// No provider: every pack's, which is what a run driving two clouds needs
	// and what `feint env` cannot do, since it writes one shell's syntax.
	all, served, err := providerExports(endpoint, "")
	if err != nil {
		t.Fatalf("every pack: %v", err)
	}
	if len(served) != len(packs) {
		t.Errorf("an empty --provider served %v of %d pack(s)", served, len(packs))
	}
	if len(all) == 0 {
		t.Error("an empty --provider exported nothing")
	}

	// An unknown provider is refused by name, and the refusal says what this
	// build does serve: a typo answered with silence would run the child
	// against an emulator it has no variables for.
	_, _, err = providerExports(endpoint, "aws")
	if err == nil {
		t.Fatal("an unknown provider was accepted")
	}
	for _, pack := range packs {
		if !strings.Contains(err.Error(), pack.Name()) {
			t.Errorf("the refusal does not name %s: %v", pack.Name(), err)
		}
	}
}

// TestTheRunReceiptSaysTheSameThingInBothShapes is cited by runReceipt.
//
// #848 asks for a machine-readable answer "without parsing prose", so the two
// renderings are written from one struct. This holds that neither drops a
// fact the other carries — above all the truncation flag, which is the one
// that turns a count into a window.
func TestTheRunReceiptSaysTheSameThingInBothShapes(t *testing.T) {
	receipt := runReceipt{
		SchemaVersion: runReceiptSchemaVersion, Lab: "owned",
		Endpoint:  "http://127.0.0.1:41235",
		Providers: []string{"exoscale", "scaleway"},
		Intercepted: []string{
			"s3.fr-par.scw.cloud", "sos-ch-gva-2.exo.io",
		},
		Command: []string{"terraform", "apply"}, Calls: 256, CallsTruncated: true,
		Stripped: []string{"OSC_PROFILE"}, Exit: 0, Cleanup: "clean",
		Session: "feint-lab-4021", Runtime: "incus-ovn",
		Capabilities: []string{"firewall", "isolation"},
		RealCloud:    "no known escape observed",
	}

	var prose bytes.Buffer
	receipt.render(&prose)
	text := prose.String()
	for _, want := range []string{
		"passed", "owned", "http://127.0.0.1:41235", "scaleway", "terraform apply",
		"256", "window", "s3.fr-par.scw.cloud", "OSC_PROFILE", "clean",
		"no known escape observed", "incus-ovn", "isolation",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the prose receipt never says %q:\n%s", want, text)
		}
	}

	var encoded bytes.Buffer
	if err := receipt.renderJSON(&encoded); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var back runReceipt
	if err := json.Unmarshal(encoded.Bytes(), &back); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if back.SchemaVersion != runReceiptSchemaVersion {
		t.Errorf("the JSON receipt carries schema %d", back.SchemaVersion)
	}
	if !back.CallsTruncated {
		t.Error("the JSON receipt lost the truncation flag, which is the fact that makes the count a window")
	}
	// The claim a reader cares about most must not be able to differ between
	// the two shapes, which is why it is a field and not a sentence render()
	// prints: the first version printed it as a literal and the JSON carried
	// nothing at all.
	if back.RealCloud != receipt.RealCloud {
		t.Errorf("the JSON receipt says %q about the real cloud where the prose says %q",
			back.RealCloud, receipt.RealCloud)
	}
	if back.Runtime != receipt.Runtime || len(back.Capabilities) != len(receipt.Capabilities) {
		t.Errorf("the JSON receipt lost the runtime the emulator declared: %+v", back)
	}
	if back.Session != receipt.Session {
		t.Errorf("the JSON receipt lost the lab's identity: %q", back.Session)
	}
	if back.Calls != receipt.Calls || back.Exit != receipt.Exit || back.Cleanup != receipt.Cleanup {
		t.Errorf("the JSON receipt disagrees with the struct it came from: %+v", back)
	}

	// A failing run says so in the first line, where a reader looks.
	receipt.Exit = 2
	var failed bytes.Buffer
	receipt.render(&failed)
	if !strings.Contains(failed.String(), "failed (2)") {
		t.Errorf("a failing run reads as a passing one:\n%s", failed.String())
	}
}

// TestAFreeLoopbackAddrIsLoopbackAndFree is cited by freeLoopbackAddr: the
// address must be bindable, and it must be on loopback, because an owned lab
// reachable from the network would be a cloud emulator listening to strangers.
//
// # WHAT THIS TEST USED TO ASSERT, AND WHY IT HAD TO GO
//
// It called the probe twice and required the two answers to differ, with the
// message "so one lab would refuse to start". That is a property of the
// kernel's ephemeral port allocator, not of this function, and the kernel does
// not promise it: freeLoopbackAddr closes its listener before answering, so the
// same port is free again and may be handed straight back.
//
// Measured on 2026-11-12, two thousand consecutive pairs on one station: one
// pair came back identical, 0.05%. It went red on main the same day. A test
// that fails one run in two thousand is not flaky by bad luck, it is asserting
// something that was never true, and the subject says so itself — the
// function's own comment begins "Racy by nature". The test contradicted its
// own subject's documentation and passed for months because the odds were long.
//
// What is asserted instead is what the function actually promises: loopback,
// and bindable when it answers. The race past that moment is real, documented
// where the function is, and loud when it is lost — `start` refuses a port it
// cannot bind. A test cannot close a window the design leaves open, and
// pretending otherwise is how a green run stops meaning anything.
func TestAFreeLoopbackAddrIsLoopbackAndFree(t *testing.T) {
	addr, err := freeLoopbackAddr()
	if err != nil {
		t.Fatalf("free address: %v", err)
	}
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("the lab would listen on %s, which is not loopback", addr)
	}

	// Bindable, which is the whole promise: an address the lab cannot listen
	// on is worse than no address, because the failure arrives after the start.
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("the probe answered %s and nothing can listen on it: %v", addr, err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close the test listener: %v", err)
	}
}

// TestAnAttachedLabIsNamedAsSuchInTheReceipt is cited by labKind. The word is
// not decoration: it is what tells a reader whether the emulator they can
// still reach is one this command forgot to stop.
func TestAnAttachedLabIsNamedAsSuchInTheReceipt(t *testing.T) {
	if got := labKind(nil); got != "attached" {
		t.Errorf("an emulator this command did not start reads as %q", got)
	}
	if got := labKind(&ownedLab{}); got != "owned" {
		t.Errorf("an emulator this command started reads as %q", got)
	}
}

// TestALabIsKeptOnlyWhenAFailedRunAskedForIt is cited by keepsTheLab.
//
// The expensive direction is keeping on success: a lab left up after every
// green run binds a port and holds a directory, and nothing in the output
// says so. So the table asserts the three "no" answers as hard as the one
// "yes".
func TestALabIsKeptOnlyWhenAFailedRunAskedForIt(t *testing.T) {
	for _, probe := range []struct {
		name   string
		code   int
		err    error
		asked  bool
		expect bool
	}{
		{"a green run, nobody asked", exitOK, nil, false, false},
		{"a green run, asked anyway", exitOK, nil, true, false},
		{"a failed child, nobody asked", 3, nil, false, false},
		{"a failed child, asked", 3, nil, true, true},
		{"the run itself failed, asked", exitOK, errors.New("the proxy died"), true, true},
		{"the run itself failed, nobody asked", exitOK, errors.New("the proxy died"), false, false},
	} {
		t.Run(probe.name, func(t *testing.T) {
			if got := keepsTheLab(probe.code, probe.err, probe.asked); got != probe.expect {
				t.Errorf("keepsTheLab(%d, %v, %v) = %v", probe.code, probe.err, probe.asked, got)
			}
		})
	}
}

// TestAnAttachedRunReturnsTheChildsCodeAndSaysItIsNotItsLab drives the whole
// command, which an owned lab cannot be driven here: `start` re-execs
// os.Executable(), the test binary. With --endpoint there is no spawn, so the
// rest of the command — the receipt, the exit code, the refusal to claim a
// cleanup it did not do — is observable in process.
//
// The exit code is the assertion that matters most. A wrapper that answered
// its own success would turn a failing test suite green, and the only wrong
// byte would be one nobody prints.
func TestAnAttachedRunReturnsTheChildsCodeAndSaysItIsNotItsLab(t *testing.T) {
	server := reportServer(t, door("scaleway", "fr-par"))

	for _, probe := range []struct {
		name, script string
		expect       int
	}{
		{"a child that succeeds", "exit 0", exitOK},
		{"a child that fails", "exit 3", 3},
	} {
		t.Run(probe.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runCommand([]string{
				"--endpoint", server.URL, "--json", "-", "--", "sh", "-c", probe.script,
			}, &stdout, &stderr)
			if code != probe.expect {
				t.Fatalf("the command answered %d where the child answered %d\nstderr: %s",
					code, probe.expect, stderr.String())
			}
			var receipt runReceipt
			if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
				t.Fatalf("stdout is not the receipt alone: %v\n%s", err, stdout.String())
			}
			if receipt.Exit != probe.expect {
				t.Errorf("the receipt reports exit %d", receipt.Exit)
			}
			// It did not start this emulator, so it must not report a cleanup
			// it never performed: a reader seeing "clean" would believe the
			// endpoint is gone.
			if receipt.Lab != "attached" || receipt.Cleanup != "not owned" {
				t.Errorf("lab=%q cleanup=%q for an emulator this command did not start",
					receipt.Lab, receipt.Cleanup)
			}
			if receipt.Endpoint != server.URL {
				t.Errorf("the receipt names %q", receipt.Endpoint)
			}
		})
	}
}

// TestARunRefusesAnEscapeNoDoorOfItsOwnWouldCatch is cited by stackEscapes.
//
// Three populations, and the middle one is why this is not simply "refuse on
// any hazard": the hazards the packs report are the ones this command exists
// to neutralise, so refusing them all would refuse exactly the stacks #849
// was built for. The door is what decides.
func TestARunRefusesAnEscapeNoDoorOfItsOwnWouldCatch(t *testing.T) {
	stack := t.TempDir()
	// The measured Scaleway shape (#262): the provider hardcodes
	// s3.<region>.scw.cloud for this resource whatever the exports say.
	if err := os.WriteFile(filepath.Join(stack, "main.tf"),
		[]byte("resource \"scaleway_object_bucket\" \"assets\" {\n  name = \"x\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(stack)

	t.Run("no door for that provider is an escape", func(t *testing.T) {
		escapes := stackEscapes(nil)
		if len(escapes) == 0 {
			t.Fatal("a stack that hardcodes s3.<region>.scw.cloud was let through with no door at all")
		}
		if !strings.Contains(strings.Join(escapes, "\n"), "scaleway") {
			t.Errorf("the refusal does not name the pack that reported it: %v", escapes)
		}
	})

	t.Run("a door for that provider covers it", func(t *testing.T) {
		escapes := stackEscapes([]s3.Profile{{Provider: "scaleway", Region: "fr-par"}})
		if len(escapes) != 0 {
			t.Errorf("a run that intercepts scaleway refused the stack it was built for: %v", escapes)
		}
	})

	t.Run("a door for another provider does not", func(t *testing.T) {
		escapes := stackEscapes([]s3.Profile{{Provider: "exoscale", Region: "ch-gva-2"}})
		if len(escapes) == 0 {
			t.Error("an exoscale door was taken as covering a scaleway escape")
		}
	})

	t.Run("the command refuses before it spawns anything", func(t *testing.T) {
		// The whole command, in a stack whose escape this run has no door
		// for: the child must never start. A refusal computed and not acted
		// on is the defect this repository names most often.
		server := reportServer(t, door("exoscale", "ch-gva-2"))
		witness := filepath.Join(t.TempDir(), "the-child-ran")
		var stdout, stderr bytes.Buffer
		code := runCommand([]string{
			"--endpoint", server.URL, "--", "sh", "-c", "touch " + witness,
		}, &stdout, &stderr)
		if code != exitError {
			t.Fatalf("the run was allowed (%d)\nstderr: %s", code, stderr.String())
		}
		if _, err := os.Stat(witness); err == nil {
			t.Error("the child ran before the escape was refused")
		}
		if !strings.Contains(stderr.String(), "does not intercept") {
			t.Errorf("the refusal does not say what it is: %s", stderr.String())
		}
	})

	t.Run("off a stack there is nothing to analyse", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if escapes := stackEscapes(nil); len(escapes) != 0 {
			t.Errorf("a directory with no Terraform file was interrogated anyway: %v", escapes)
		}
	})
}

// TestTheJSONReceiptGoesWhereItWasAskedFor is cited by writeReceipt.
//
// The path is the point. The first version wrote the JSON to stdout, and
// replaying a surveyed stack made `terraform apply` print its plan into the
// same stream: the receipt was unparseable exactly when the run was real. So
// the default stream belongs to the command being run, and the receipt goes
// where the caller says.
func TestTheJSONReceiptGoesWhereItWasAskedFor(t *testing.T) {
	receipt := runReceipt{
		SchemaVersion: runReceiptSchemaVersion, Lab: "owned", Exit: 0,
		Cleanup: "clean", RealCloud: "no known escape observed",
	}

	path := filepath.Join(t.TempDir(), "receipt.json")
	var stdout bytes.Buffer
	if err := writeReceipt(receipt, path, &stdout); err != nil {
		t.Fatalf("write to a path: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("a receipt asked for on disk also went to stdout: %q", stdout.String())
	}
	body, err := os.ReadFile(path) //nolint:gosec // a path this test made
	if err != nil {
		t.Fatalf("read it back: %v", err)
	}
	var back runReceipt
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatalf("the file is not the receipt: %v\n%s", err, body)
	}
	if back.RealCloud != receipt.RealCloud {
		t.Errorf("the file lost a field: %+v", back)
	}
	// Owner-only, like everything else this run writes: a receipt names the
	// lab and the command, and there is no reason for another account to read
	// it.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&^os.FileMode(0o600) != 0 {
		t.Errorf("the receipt is %#o, wider than the 0600 it is written with", mode)
	}

	// A single dash is the caller saying their command is quiet.
	var toStdout bytes.Buffer
	if err := writeReceipt(receipt, "-", &toStdout); err != nil {
		t.Fatalf("write to stdout: %v", err)
	}
	if !strings.Contains(toStdout.String(), "no known escape observed") {
		t.Errorf("a dash wrote nothing to stdout: %q", toStdout.String())
	}

	// A path that cannot be opened is an error, not a silent skip: a suite
	// reading the receipt afterwards would find nothing and blame the run.
	if err := writeReceipt(receipt, filepath.Join(t.TempDir(), "no-such-dir", "r.json"), &stdout); err == nil {
		t.Error("an unwritable path was accepted")
	}
}

// TestTheReceiptReportsTheRuntimeTheEmulatorDeclares is cited by labHealth.
//
// From /_feint/health and not from the --runtime flag, which is the
// repository's rule twice over: a capability is DECLARED by the driver and an
// undeclared one counts as absent, and a receipt must report what the process
// can prove rather than what the caller asked for. A run started with
// `--runtime auto` on a host where OVN is missing would otherwise claim
// isolation it does not have.
func TestTheReceiptReportsTheRuntimeTheEmulatorDeclares(t *testing.T) {
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"machines":"incus-ovn","capabilities":` +
			`{"isolation":true,"firewall":true,"address":false},` +
			`"enforced":{"firewall":["scaleway"],"balancing":[]}}`))
	}))
	defer health.Close()

	runtime, capabilities, proof := labHealth(health.URL)
	if runtime != "incus-ovn" {
		t.Errorf("the receipt would report runtime %q", runtime)
	}
	// Only the declared ones, and sorted so a receipt does not shuffle between
	// two runs of the same lab.
	want := []string{"firewall", "isolation"}
	if len(capabilities) != len(want) {
		t.Fatalf("capabilities %v", capabilities)
	}
	for i, name := range want {
		if capabilities[i] != name {
			t.Errorf("capabilities %v, want %v", capabilities, want)
		}
	}
	// The proof level is the INTERSECTION of the two halves, and both
	// exclusions are asserted because each was wrong once. `balancing` here
	// is enforced by nobody, so it is not proof. And an axis some pack hands
	// over while the driver declares nothing is not proof either: the first
	// version of labHealth reported "balancing, firewall" beside
	// "runtime: none" on a real replay, because the packs ask when nothing
	// answers.
	if len(proof) != 1 || proof[0] != "firewall" {
		t.Errorf("runtime proof %v; want firewall alone", proof)
	}
	asked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"machines":"none","capabilities":null,` +
			`"enforced":{"firewall":["scaleway"],"balancing":["outscale"]}}`))
	}))
	defer asked.Close()
	if name, held, proof := labHealth(asked.URL); len(held) != 0 || len(proof) != 0 {
		t.Errorf("a runtime that declares nothing reported %q %v proof=%v", name, held, proof)
	}

	// A driver that said nothing, and a lab that cannot be reached, both
	// answer empty rather than a guess: an empty row is visibly empty.
	silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"machines":"off","capabilities":null}`))
	}))
	defer silent.Close()
	if name, held, proof := labHealth(silent.URL); name != "off" || len(held) != 0 || len(proof) != 0 {
		t.Errorf("a silent driver answered %q %v %v", name, held, proof)
	}
	if name, held, proof := labHealth("http://127.0.0.1:1"); name != "" || held != nil || proof != nil {
		t.Errorf("an unreachable lab answered %q %v %v", name, held, proof)
	}
}

// TestARuntimeNeedsALabThisCommandOwns is cited by nothing in the source and
// is named here because the refusal it holds has no other witness: --runtime
// configures the emulator at startup, and an emulator somebody else started is
// already configured. Accepting the flag there would silently ignore it, which
// is the shape of a flag that does nothing.
func TestARuntimeNeedsALabThisCommandOwns(t *testing.T) {
	server := reportServer(t, door("scaleway", "fr-par"))
	var stdout, stderr bytes.Buffer
	code := runCommand([]string{
		"--endpoint", server.URL, "--runtime", "incus-ovn", "--", "true",
	}, &stdout, &stderr)
	if code != exitError {
		t.Fatalf("--runtime with --endpoint was accepted (%d)", code)
	}
	for _, want := range []string{"--runtime incus-ovn", "--endpoint", "owns"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the refusal does not mention %q: %s", want, stderr.String())
		}
	}
	// And off is not refused, because off is the default and asks nothing of
	// the host: a guard that refused the default would refuse every run.
	stdout.Reset()
	stderr.Reset()
	if code := runCommand([]string{
		"--endpoint", server.URL, "--runtime", "off", "--", "true",
	}, &stdout, &stderr); code != exitOK {
		t.Errorf("--runtime off was refused (%d): %s", code, stderr.String())
	}
}

// TestAnOwnedLabSweepsItsRuntimeObjects is cited by labArgs.
//
// Stopping the emulator is not cleaning up after it: the machines and networks
// it created outlive the process, and #848's sixth behaviour is that they do
// not. So the lab is armed with --cleanup whenever a runtime is in play, and
// the test asserts BOTH directions, because a flag passed under `off` would be
// a sweep of nothing recorded in every command line forever.
func TestAnOwnedLabSweepsItsRuntimeObjects(t *testing.T) {
	for _, probe := range []struct {
		mode  string
		sweep bool
	}{
		{"off", false},
		{"incus", true},
		{"incus-vm", true},
		{"incus-ovn", true},
		{"auto", true},
	} {
		t.Run(probe.mode, func(t *testing.T) {
			args := labArgs("127.0.0.1:4599", t.TempDir(), probe.mode)
			if !slices.Contains(args, "--vm") || !slices.Contains(args, probe.mode) {
				t.Fatalf("the lab is not told its runtime: %v", args)
			}
			if got := slices.Contains(args, "--cleanup"); got != probe.sweep {
				t.Errorf("--cleanup present=%v under %q: %v", got, probe.mode, args)
			}
			// The byte store is always armed, because the doors are what the
			// interception rewrites onto and a lab with none intercepts
			// nothing.
			if !slices.Contains(args, "--objectstore") {
				t.Errorf("the lab mounts no byte store: %v", args)
			}
		})
	}
}
