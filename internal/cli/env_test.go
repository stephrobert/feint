package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What is pinned here is the accident, not the feature.
//
// `feint env scaleway --endpoint http://…` printed nothing, because Go's flag
// package stops at the first non-flag argument and the provider was read after
// parsing. The command exited 0. A test script then ran `eval "$(…)"` on that
// empty output, which succeeded, and the scw CLI that followed fell back to the
// operator's stored credentials — creating a DEV1-S server and a flexible IP on
// a real, paying account.
//
// Every test below exists so that no single link of that chain can form again.

// The provider must be readable when flags follow it, which is the ordinary way
// anybody types this command.
func TestEnvReadsTheProviderBeforeItsFlags(t *testing.T) {
	code, printed, errOut := run("env", "scaleway", "--endpoint", "http://127.0.0.1:4599")
	if code != 0 {
		t.Fatalf("exited %d: %s", code, errOut)
	}
	if !strings.Contains(printed, "export SCW_API_URL='http://127.0.0.1:4599'") {
		t.Fatalf("the endpoint flag was not applied:\n%s", printed)
	}
}

// Success with empty output is the shape of the accident: an eval of nothing
// leaves the shell pointed wherever it already was, which is a real cloud for
// anybody who has ever logged in.
func TestEnvNeverSucceedsWithoutPrintingAnything(t *testing.T) {
	for _, args := range [][]string{
		{"env"},
		{"env", "--endpoint", "http://127.0.0.1:4599"},
		{"env", "nowhere"},
	} {
		code, printed, _ := run(args...)
		if code == 0 && strings.TrimSpace(printed) == "" {
			t.Fatalf("%v exited 0 and printed nothing on stdout", args)
		}
	}
}

// Only exports reach stdout. One line of prose there and the caller's eval tries
// to execute it.
func TestEnvPutsNothingButExportsOnStdout(t *testing.T) {
	for _, provider := range []string{"scaleway", "outscale", "exoscale"} {
		code, printed, errOut := run("env", provider)
		if code != 0 {
			t.Fatalf("env %s exited %d: %s", provider, code, errOut)
		}
		for _, line := range strings.Split(strings.TrimSpace(printed), "\n") {
			if !strings.HasPrefix(line, "export ") {
				t.Errorf("env %s put a non-export line on stdout: %q", provider, line)
			}
		}
	}
}

// The Exoscale note is the reason the Note field exists, and what it warns about
// has changed twice as the clients did. It said the exo CLI ignores these
// variables, until a contributor showed it reads them (#51); it now says the
// Terraform provider honours EXOSCALE_API_ENDPOINT for its v3 client and not
// its v2 one, so an apply splits between this emulator and the real cloud.
//
// What this test holds is the property that does not change with a client: a
// warning that costs money if unread reaches stderr, and never stdout, where
// eval would execute it.
func TestEnvSendsItsNoteToStderr(t *testing.T) {
	code, printed, errOut := run("env", "exoscale")
	if code != 0 {
		t.Fatalf("exited %d: %s", code, errOut)
	}
	if !strings.Contains(errOut, "terraform") {
		t.Fatalf("the exoscale caveat is not on stderr: %q", errOut)
	}
	if !strings.Contains(errOut, "billable") {
		t.Fatalf("the caveat does not say what it costs: %q", errOut)
	}
	if strings.Contains(printed, "terraform") {
		t.Fatalf("the caveat leaked onto stdout, where eval would execute it:\n%s", printed)
	}
}

// A shell gets its own syntax or a clear refusal, never another shell's.
func TestEnvRendersEachShellInItsOwnSyntax(t *testing.T) {
	cases := map[string]string{
		"bash":       "export SCW_API_URL='",
		"fish":       "set -gx SCW_API_URL '",
		"powershell": `$env:SCW_API_URL = "`,
	}
	for shell, want := range cases {
		code, printed, errOut := run("env", "scaleway", "--shell", shell)
		if code != 0 {
			t.Fatalf("env --shell %s exited %d: %s", shell, code, errOut)
		}
		if !strings.Contains(printed, want) {
			t.Errorf("--shell %s did not render %q:\n%s", shell, want, printed)
		}
	}
	if code, _, errOut := run("env", "scaleway", "--shell", "csh"); code == 0 {
		t.Errorf("an unknown shell was accepted: %s", errOut)
	}
}

// --unset is the way back to the real cloud in the same shell. Without it, a
// developer who pointed a terminal at the emulator has to open a new one.
func TestEnvUnsetRemovesWhatItSet(t *testing.T) {
	_, exported, _ := run("env", "scaleway")
	code, removed, errOut := run("env", "scaleway", "--unset")
	if code != 0 {
		t.Fatalf("exited %d: %s", code, errOut)
	}
	for _, line := range strings.Split(strings.TrimSpace(exported), "\n") {
		key := strings.TrimPrefix(line, "export ")
		key = key[:strings.Index(key, "=")]
		if !strings.Contains(removed, "unset "+key) {
			t.Errorf("%s is exported but never unset", key)
		}
	}
}

// An unknown provider must name the ones that exist. "no such provider" alone
// sends the reader to the source.
func TestEnvNamesTheProvidersItServes(t *testing.T) {
	code, _, errOut := run("env", "nowhere")
	if code != 1 {
		t.Fatalf("exited %d, want 1", code)
	}
	for _, provider := range []string{"scaleway", "outscale", "exoscale"} {
		if !strings.Contains(errOut, provider) {
			t.Errorf("the error does not mention %s: %q", provider, errOut)
		}
	}
}

// The Outscale doorway (#286). One variable, two measured shapes: the default
// serves the current Terraform provider line (the /api/v1 path in the value,
// or provider 1.8.0 dies on a 404 at the root), and --client selects the
// families that append the path themselves. What must never happen is the
// third option: one value printed for everybody with nothing saying whom it
// fails.
func TestEnvOutscaleServesTheClientFamilyItWasAskedFor(t *testing.T) {
	code, printed, errOut := run("env", "outscale", "--endpoint", "http://127.0.0.1:4599")
	if code != 0 {
		t.Fatalf("exited %d: %s", code, errOut)
	}
	if !strings.Contains(printed, "export OSC_ENDPOINT_API='http://127.0.0.1:4599/api/v1'") {
		t.Fatalf("the default does not carry the /api/v1 path the current provider reads:\n%s", printed)
	}

	// octl, the client the conformance suite drives since #460, takes the path
	// in the value — so this is the same shape as the default and NOT the same
	// as the archived CLI two blocks down. Asserted through the CLI rather than
	// only on the pack, because what a reader types is `feint env outscale
	// --client octl` and a flag that silently served another family's shape
	// would be the exact wall #286 removed.
	code, printed, errOut = run("env", "outscale", "--client", "octl", "--endpoint", "http://127.0.0.1:4599")
	if code != 0 {
		t.Fatalf("--client octl exited %d: %s", code, errOut)
	}
	if !strings.Contains(printed, "export OSC_ENDPOINT_API='http://127.0.0.1:4599/api/v1'") {
		t.Fatalf("--client octl does not print the /api/v1 shape octl reads:\n%s", printed)
	}

	code, printed, errOut = run("env", "outscale", "--client", "oapi-cli", "--endpoint", "http://127.0.0.1:4599")
	if code != 0 {
		t.Fatalf("--client oapi-cli exited %d: %s", code, errOut)
	}
	if !strings.Contains(printed, "export OSC_ENDPOINT_API='http://127.0.0.1:4599'\n") {
		t.Fatalf("--client oapi-cli does not print the bare host it appends /api/v1 to:\n%s", printed)
	}

	// An unknown family is refused by name, with the known ones listed —
	// printing a guessed shape is exactly the wall this flag removes.
	code, printed, errOut = run("env", "outscale", "--client", "osc-cli")
	if code == 0 {
		t.Fatalf("an unknown client family was served:\n%s", printed)
	}
	for _, family := range []string{"oapi-cli", "octl", "terraform"} {
		if !strings.Contains(errOut, family) {
			t.Fatalf("the refusal does not name %s, a family that exists: %q", family, errOut)
		}
	}

	// A pack whose clients all read the same environment refuses the flag
	// rather than teaching that it selects something.
	if code, _, _ := run("env", "scaleway", "--client", "terraform"); code == 0 {
		t.Fatal("--client was accepted by a pack that serves every client the same environment")
	}
}

// The escape warning fires where the user can still act: on the same stderr
// the eval cannot swallow, before any terraform run. With OSC_PROFILE set the
// Outscale 1.1.x provider ignores OSC_ENDPOINT_API and reaches the real cloud
// (measured, #286) — and a warning that leaked to stdout would be executed by
// the eval instead of read.
func TestEnvHazardWarningsReachStderrNeverStdout(t *testing.T) {
	t.Setenv("OSC_PROFILE", "default")

	code, printed, errOut := run("env", "outscale")
	if code != 0 {
		t.Fatalf("exited %d: %s", code, errOut)
	}
	if !strings.Contains(errOut, "warning:") || !strings.Contains(errOut, "OSC_PROFILE") {
		t.Fatalf("OSC_PROFILE is set and stderr carries no warning about it: %q", errOut)
	}
	for _, line := range strings.Split(strings.TrimSpace(printed), "\n") {
		if !strings.HasPrefix(line, "export ") {
			t.Fatalf("a warning leaked onto stdout, where eval would execute it: %q", line)
		}
	}

	// --unset is the deliberate way back to the real cloud; warning on the way
	// out would teach people to ignore the warning on the way in.
	_, _, errOut = run("env", "outscale", "--unset")
	if strings.Contains(errOut, "OSC_PROFILE") {
		t.Fatalf("--unset still warns about the shell it is restoring: %q", errOut)
	}
}

// The stack's own text can defeat every export env prints: a
// scaleway_object_* resource reaches the real s3.<region>.scw.cloud no matter
// what SCW_API_URL says (measured, #262/#280). env is eval'd from the stack
// directory, so it is the last voice before the apply that escapes — on
// stderr, where the eval cannot swallow it, and silent when the directory
// carries no Terraform at all.
func TestEnvNamesTheEscapeInTheStackNextToIt(t *testing.T) {
	dir := t.TempDir()
	stack := `
resource "scaleway_instance_ip" "local" {}
resource "scaleway_object_bucket" "escapes" { name = "x" }
`
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(stack), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	code, printed, errOut := run("env", "scaleway")
	if code != 0 {
		t.Fatalf("exited %d: %s", code, errOut)
	}
	if !strings.Contains(errOut, "warning:") || !strings.Contains(errOut, "scaleway_object") {
		t.Fatalf("the stack next to this shell escapes and stderr says nothing: %q", errOut)
	}
	for _, line := range strings.Split(strings.TrimSpace(printed), "\n") {
		if !strings.HasPrefix(line, "export ") {
			t.Fatalf("a warning leaked onto stdout, where eval would execute it: %q", line)
		}
	}

	// A directory with no Terraform files stays silent: env runs from plenty
	// of places that are not stacks, and noise there teaches people to
	// ignore the warning where it is true.
	t.Chdir(t.TempDir())
	_, _, errOut = run("env", "scaleway")
	if strings.Contains(errOut, "warning:") {
		t.Fatalf("an empty directory drew a warning: %q", errOut)
	}
}
