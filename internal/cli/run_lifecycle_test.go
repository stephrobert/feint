package cli

import (
	"bytes"
	"encoding/json"
	"errors"
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
	}

	var prose bytes.Buffer
	receipt.render(&prose)
	text := prose.String()
	for _, want := range []string{
		"passed", "owned", "http://127.0.0.1:41235", "scaleway", "terraform apply",
		"256", "window", "s3.fr-par.scw.cloud", "OSC_PROFILE", "clean",
		"no known escape observed",
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
func TestAFreeLoopbackAddrIsLoopbackAndFree(t *testing.T) {
	addr, err := freeLoopbackAddr()
	if err != nil {
		t.Fatalf("free address: %v", err)
	}
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("the lab would listen on %s, which is not loopback", addr)
	}
	second, err := freeLoopbackAddr()
	if err != nil {
		t.Fatalf("second free address: %v", err)
	}
	if addr == second {
		t.Errorf("two consecutive probes answered the same port %s, so one lab would refuse to start", addr)
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
				"--endpoint", server.URL, "--json", "--", "sh", "-c", probe.script,
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
