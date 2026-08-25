package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/environment"
)

// What `feint up` refuses, driven through the dispatcher, so that what is
// measured is the binary's behaviour rather than a helper's.
//
// The refusal that has to arrive *before anything starts* lives in
// up_wait_test.go instead, in the package: proving it needs to see that no
// emulator was spawned, and the way to see that is the instance directory.

// A declaration this binary cannot read is refused at load, and the message
// says where the field reference is. A refusal with no way forward gets worked
// around by copying the emulator.
func TestUpRefusesAMistypedDeclarationNamingTheField(t *testing.T) {
	dir := t.TempDir()
	// The mistake a reader actually makes: the CLI flag is --log-level and the
	// field is log_level. Refused by name, with the list of what the block takes.
	write(t, dir, environment.DefaultFile, "version: 1\nemulator:\n  log-level: debug\n")
	t.Chdir(dir)

	code, _, errOut := run("up")
	if code != 1 {
		t.Fatalf("exited %d, want 1", code)
	}
	if !strings.Contains(errOut, "emulator.log-level") {
		t.Errorf("the refusal never names the field: %q", errOut)
	}
}

// The three stacks this repository ships each carry a declaration, and each one
// has to load. A stack whose feint.yaml stopped parsing would be found by a
// reader rather than by a gate, which is the class of defect the repository has
// paid for most often.
func TestEveryExampleStackCarriesADeclarationThatLoads(t *testing.T) {
	root := filepath.Join("..", "..", "examples", "stacks")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}
	found := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name(), environment.DefaultFile)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s ships no %s, so a reader has to reconstitute the environment by hand",
				entry.Name(), environment.DefaultFile)
			continue
		}
		decl, err := environment.Load(path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if decl.Cloud.Provider != entry.Name() {
			t.Errorf("%s declares provider %q, and it sits in %s", path, decl.Cloud.Provider, entry.Name())
		}
		if len(decl.Unread()) != 0 {
			t.Errorf("%s declares %v, which no verb reads", path, decl.Unread())
		}
		found++
	}
	// The witness: a scan that found no stack would pass every assertion above.
	if found < 3 {
		t.Fatalf("only %d stacks carry a declaration; the scan is broken, not the stacks", found)
	}
}

// The vocabulary the schema documents is the vocabulary the binary takes. The
// list lives in internal/environment because a schema that discovers its own
// words at run time cannot document them, and this is what stops the two from
// drifting apart.
func TestEveryDeclaredRuntimeModeIsAModeTheBinaryTakes(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	for _, mode := range environment.RuntimeModes {
		if mode != "off" && mode != "auto" {
			// Every other mode needs a runtime this station may not have, and
			// asserting on it would measure the station. `off` and `auto` are
			// the two that answer everywhere.
			continue
		}
		write(t, dir, environment.DefaultFile, "version: 1\nemulator:\n  addr: 127.0.0.1:1\nruntime:\n  mode: "+mode+"\n")
		// --no-iac and an address nothing can bind would still start; what is
		// read here is only whether the mode itself was refused.
		_, _, errOut := run("up", "--no-iac", "--timeout", "1s")
		if strings.Contains(errOut, "unknown --vm mode") {
			t.Errorf("the schema documents the mode %q and the binary answers %q", mode, errOut)
		}
	}
	// The witness, without which the loop above would pass while measuring
	// nothing: a mode the schema does not carry is refused, and by the schema.
	write(t, dir, environment.DefaultFile, "version: 1\nruntime:\n  mode: podman\n")
	if code, _, errOut := run("up"); code == 0 {
		t.Errorf("`mode: podman` was accepted: %q", errOut)
	}
}

// The reference page is rendered from the schema, so a field added to the
// schema and a page that never learned about it is a gate failure rather than a
// reader's discovery.
func TestTheEnvironmentReferenceIsGeneratedFromTheSchema(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "..", "docs", "environment.md"))
	if err != nil {
		t.Fatalf("read the reference: %v", err)
	}
	body := string(page)
	if len(environment.Fields()) < 10 {
		t.Fatalf("only %d fields in the schema: the table is not being read", len(environment.Fields()))
	}
	for _, fd := range environment.Fields() {
		if !strings.Contains(body, "`"+fd.Path+"`") {
			t.Errorf("the reference never names `%s`; run `feint docs`", fd.Path)
		}
	}
	for _, fd := range environment.NotCarried() {
		if !strings.Contains(body, "`"+fd.Path+"`") {
			t.Errorf("the reference never names `%s`, so a reader who writes it learns nothing", fd.Path)
		}
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
