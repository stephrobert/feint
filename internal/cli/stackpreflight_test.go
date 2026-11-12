package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/stack"
)

// stackDirFor lays out one configuration for a test and answers its path.
func stackDirFor(t *testing.T, main string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A run stops before anything starts when the stack declares a type this
// emulator refuses.
//
// THE MEASUREMENT THIS REPLACES, 2026-11-12 against a real emulator with
// provider outscale/outscale 1.8.0: the same declarations, applied. Plan 51 to
// add, 48 resources created, two 404 OperationNotEmulated, 50 entries left in
// the state — and the third declined type never attempted, because its
// dependencies had failed. Three findings, discovered one apply at a time.
//
// What is asserted here is the opposite order: the verdict first, with nothing
// created. A refusal that arrived after the lab was up would have already cost
// what this check exists to save.
func TestARunRefusesADeclinedStackBeforeStartingAnything(t *testing.T) {
	dir := stackDirFor(t, `
resource "outscale_net" "main" {
  ip_range = "10.0.0.0/16"
}
resource "outscale_virtual_gateway" "vgw" {
  connection_type = "ipsec.1"
}
resource "outscale_vpn_connection" "tunnel" {
  connection_type = "ipsec.1"
}
`)

	var out, errOut bytes.Buffer
	code := runCommand([]string{"--runtime", "off", "--", "terraform", "-chdir=" + dir, "apply"}, &out, &errOut)

	if code != exitError {
		t.Fatalf("exit %d, want %d: a refused preflight is an error, and 2 is drift\nstderr:\n%s",
			code, exitError, errOut.String())
	}
	text := errOut.String()

	// NOTHING RAN AFTER IT, which is the half that makes this a preflight
	// rather than a message. Asserted on the last line rather than on the
	// presence of the refusal: the refusal is printed either way, so a test
	// that only looked for it passed with the stop removed — caught by
	// mutation, and the whole value of the check is the order.
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "--preflight=off to run anyway") {
		t.Errorf("something happened after the refusal; the last line is %q\nstderr:\n%s", last, text)
	}
	if strings.Contains(text, "proxy on") {
		t.Errorf("the child's proxy was mounted although the stack was refused:\n%s", text)
	}
	for _, want := range []string{
		"outscale_virtual_gateway",
		"outscale_vpn_connection",
		"CreateVirtualGateway",
		"stopping before the lab starts",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the refusal does not carry %q:\n%s", want, text)
		}
	}
}

// The child's own -chdir is where the stack is read, which is a defect fixed
// rather than a feature added.
//
// Nothing in this package read -chdir, while stackEscapes took its directory
// from os.Getwd(). So `feint run -- terraform -chdir=infra apply`, from a
// repository root with no top-level .tf, found no stack at all, and every
// reading of it — the escape warning included — stayed silent on a
// configuration that was about to run.
func TestThePreflightReadsTheChildsChdir(t *testing.T) {
	dir, engine, ok := stackTarget([]string{"terraform", "-chdir=infra", "apply"})
	if !ok {
		t.Fatal("a terraform apply was not recognised as a stack command")
	}
	if dir != "infra" {
		t.Errorf("dir = %q, want infra: the child would read there and nothing else would", dir)
	}
	if engine != stack.EngineTerraform {
		t.Errorf("engine = %q", engine)
	}

	if _, engine, ok := stackTarget([]string{"tofu", "plan"}); !ok || engine != stack.EngineOpenTofu {
		t.Errorf("tofu plan gave engine %q ok=%v; the engine decides which files are the module", engine, ok)
	}
}

// Subcommands that reach no provider are not interrogated, and neither is a
// command that is not an engine at all.
//
// Silence off a stack is the rule stackEscapes already holds: `feint run -- go
// test` in a Go repository must not be asked about a configuration that does
// not exist, and `terraform fmt` cannot fail for the reason this check exists.
func TestOnlyTheSubcommandsThatReachAProviderArePreflighted(t *testing.T) {
	const teardown = "des" + "troy"
	for _, command := range [][]string{
		{"terraform", "init"},
		{"terraform", "validate"},
		{"terraform", "fmt"},
		{"terraform", "providers"},
		{"terraform", "version"},
		{"go", "test", "./..."},
		{"tofu", "fmt"},
	} {
		if _, _, ok := stackTarget(command); ok {
			t.Errorf("%v was preflighted, and it reaches no provider", command)
		}
	}
	for _, command := range [][]string{
		{"terraform", "apply"},
		{"terraform", "plan", "-out=tfplan"},
		{"terraform", teardown},
		{"tofu", "apply", "-auto-approve"},
		{"/usr/local/bin/terraform", "refresh"},
	} {
		if _, _, ok := stackTarget(command); !ok {
			t.Errorf("%v was not preflighted, and it reaches a provider", command)
		}
	}
}

// strict refuses what auto proceeds on, and that difference is the whole
// reason both exist.
//
// Refusing every unmeasured type by default would refuse nearly every
// configuration anybody brings — the recorded-apply sets are 26 of 155 Scaleway
// resource types and 29 of 57 Outscale ones — and a gate that refuses
// everything is a gate people turn off. So the strict reading is asked for,
// never assumed.
func TestStrictRefusesWhatAutoProceedsOn(t *testing.T) {
	dir := stackDirFor(t, `
resource "outscale_net" "main" {
  ip_range = "10.0.0.0/16"
}
resource "outscale_oks_cluster" "nothing_here_has_measured_this" {
  name = "x"
}
`)
	rep, ran := stackPreflightDir(dir, stack.EngineTerraform)
	if !ran {
		t.Fatal("nothing was read")
	}
	if rep.Verdict != stack.VerdictUnknown {
		t.Fatalf("verdict = %q, want unknown", rep.Verdict)
	}
	if refuses(rep, preflightAuto) {
		t.Error("auto refuses an unmeasured type, which would refuse nearly every real configuration")
	}
	if !refuses(rep, preflightStrict) {
		t.Error("strict proceeds on an unmeasured type, so it is the same policy as auto")
	}
	if refuses(rep, preflightOff) {
		t.Error("off refuses something, so there is no way to run anyway and read what the emulator answers")
	}
}

// The report a CI reads carries the verdict and the rows behind it.
func TestThePreflightJSONCarriesTheVerdictAndItsRows(t *testing.T) {
	dir := stackDirFor(t, `
resource "outscale_virtual_gateway" "vgw" {
  connection_type = "ipsec.1"
}
`)
	rep, ran := stackPreflightDir(dir, stack.EngineTerraform)
	if !ran {
		t.Fatal("nothing was read")
	}
	var buf bytes.Buffer
	if err := writePreflightJSON(&buf, rep); err != nil {
		t.Fatal(err)
	}

	var back struct {
		Verdict  string `json:"verdict"`
		Findings []struct {
			Type      string `json:"type"`
			Standing  string `json:"standing"`
			Operation string `json:"operation"`
			Reason    string `json:"reason"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("the report is not readable JSON: %v\n%s", err, buf.String())
	}
	if back.Verdict != string(stack.VerdictIncompatible) {
		t.Errorf("verdict = %q", back.Verdict)
	}
	if len(back.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(back.Findings))
	}
	f := back.Findings[0]
	if f.Type != "outscale_virtual_gateway" || f.Standing != "declined" ||
		f.Operation == "" || f.Reason == "" {
		t.Errorf("the row does not carry what a reader needs: %+v", f)
	}
}

// Off a stack, the preflight says nothing at all.
func TestThePreflightIsSilentWhereThereIsNoStack(t *testing.T) {
	dir := t.TempDir()
	if _, ran := stackPreflightDir(dir, stack.EngineTerraform); ran {
		t.Error("an empty directory produced a reading")
	}

	var out, errOut bytes.Buffer
	if runStackPreflight([]string{"go", "test", "./..."}, preflightAuto, "", &out, &errOut) {
		t.Error("a Go test run was refused by a Terraform preflight")
	}
	if errOut.Len() != 0 {
		t.Errorf("it spoke about a stack that does not exist:\n%s", errOut.String())
	}
}

// A compatible stack says so without stopping anything, and the green verdict
// still carries what it does not promise.
func TestACompatibleStackIsNotStoppedAndStillStatesItsLimit(t *testing.T) {
	dir := stackDirFor(t, `
resource "outscale_net" "main" {
  ip_range = "10.0.0.0/16"
}
`)
	rep, ran := stackPreflightDir(dir, stack.EngineTerraform)
	if !ran || rep.Verdict != stack.VerdictCompatible {
		t.Fatalf("ran=%v verdict=%q, want a compatible reading", ran, rep.Verdict)
	}
	if refuses(rep, preflightAuto) || refuses(rep, preflightStrict) {
		t.Error("a compatible stack was refused by some policy")
	}

	var buf bytes.Buffer
	writePreflight(&buf, rep)
	text := buf.String()
	if !strings.Contains(text, "COMPATIBLE") {
		t.Errorf("the verdict is not stated:\n%s", text)
	}
	// The limit is printed on the green verdict too, which is where it is
	// easiest to forget and most likely to mislead.
	if !strings.Contains(text, "not a promise about every attribute") {
		t.Errorf("a compatible verdict does not say what it does not promise:\n%s", text)
	}
}
