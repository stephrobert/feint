package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The scan exists because of a measured escape (#262, #280): a stack pointed
// here through SCW_API_URL sent its CreateBucket to the real
// s3.fr-par.scw.cloud while every other resource applied locally. The emulator
// cannot see a request that never arrives, so the warning has to come from the
// stack's own text, before the run — and it has to stay quiet everywhere else,
// or people learn to ignore it.

func writeStack(t *testing.T, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
}

// A stack carrying the measured escape gets a warning row naming the pack and
// the resource family; the row is a warning, never a failure, because doctor
// must not fail builds.
func TestDoctorNamesTheObjectStorageEscape(t *testing.T) {
	writeStack(t, map[string]string{
		"main.tf": `
resource "scaleway_instance_ip" "local" {}
resource "scaleway_object_bucket" "escapes" { name = "x" }
`,
	})
	checks := checkStackHazards()
	var warned bool
	for _, c := range checks {
		if c.state == verdictFail {
			t.Fatalf("a stack hazard failed the run instead of warning: %+v", c)
		}
		if c.state == verdictWarn && strings.Contains(c.title, "scaleway") && strings.Contains(c.fix, "scaleway_object") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("the measured Object Storage escape went unnamed: %+v", checks)
	}
}

// No Terraform files here means no rows at all: a doctor run from a home
// directory must not gain a line about stacks that do not exist.
func TestDoctorStaysSilentOffAStack(t *testing.T) {
	writeStack(t, map[string]string{"notes.txt": "not terraform"})
	if checks := checkStackHazards(); len(checks) != 0 {
		t.Fatalf("a directory with no Terraform files produced rows: %+v", checks)
	}
}

// A clean stack gets exactly one row, and that row states its own scope — the
// measured list — rather than promising that nothing can escape.
func TestACleanStackGetsOneHonestRow(t *testing.T) {
	writeStack(t, map[string]string{
		"main.tf": `resource "scaleway_instance_ip" "local" {}`,
	})
	checks := checkStackHazards()
	if len(checks) != 1 || checks[0].state != verdictOK {
		t.Fatalf("a clean stack should get one ok row, got: %+v", checks)
	}
	if !strings.Contains(checks[0].detail, "measured list") {
		t.Fatalf("the ok row does not state its scope, which licenses the belief it cannot back: %+v", checks[0])
	}
}

// Dead text must not trigger: a commented-out resource is the ordinary way
// somebody parks the S3 half of a stack to run the rest here, and warning on
// it teaches them the warning is noise. All three HCL comment forms count,
// and a "#" inside a quoted string is data, not a comment.
func TestAStackHazardInACommentStaysSilent(t *testing.T) {
	writeStack(t, map[string]string{
		"main.tf": `
# resource "scaleway_object_bucket" "parked" {}
// resource "scaleway_object_bucket" "parked" {}
/*
resource "scaleway_object_bucket" "parked" { name = "x" }
*/
resource "scaleway_instance_ip" "local" {
  tags = ["#not-a-comment"]
}
`,
	})
	checks := checkStackHazards()
	for _, c := range checks {
		if c.state == verdictWarn {
			t.Fatalf("a commented-out resource triggered a warning: %+v", c)
		}
	}
	if len(checks) != 1 {
		t.Fatalf("expected the one honest ok row, got: %+v", checks)
	}
}

// A directory that merely contains projects is not a stack: Terraform only
// runs where root module files sit. Without the top-level gate, a doctor run
// from a workspace holding vendored provider sources and old stack copies
// warned about text nobody was about to apply — measured on this repository's
// own scratchpad — while a stack whose escaping resource lives in a module
// below a rooted configuration must still be seen.
func TestADirectoryOfProjectsIsNotAStack(t *testing.T) {
	writeStack(t, map[string]string{
		"vendored-provider/examples/main.tf": `resource "scaleway_object_bucket" "someone_elses" {}`,
		"notes.md":                           "no terraform at this level",
	})
	if checks := checkStackHazards(); len(checks) != 0 {
		t.Fatalf("a directory of projects was scanned as a stack: %+v", checks)
	}
}

// The other half of the gate: once the top level is a root module, its
// modules are part of the same apply and must be scanned.
func TestARootedStacksModulesAreScanned(t *testing.T) {
	writeStack(t, map[string]string{
		"main.tf":                 `module "storage" { source = "./modules/storage" }`,
		"modules/storage/main.tf": `resource "scaleway_object_bucket" "escapes" { name = "x" }`,
	})
	checks := checkStackHazards()
	var warned bool
	for _, c := range checks {
		if c.state == verdictWarn && strings.Contains(c.fix, "scaleway_object") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("an escape in a module of a rooted stack went unnamed: %+v", checks)
	}
}

// What lives under .terraform is not the operator's own text: provider
// source trees and remote module copies mention real hosts constantly, and
// scanning them would make every initialised stack warn.
func TestTheScanSkipsDotDirectories(t *testing.T) {
	writeStack(t, map[string]string{
		"main.tf":                      `resource "scaleway_instance_ip" "local" {}`,
		".terraform/modules/x/main.tf": `resource "scaleway_object_bucket" "vendored" {}`,
	})
	checks := checkStackHazards()
	for _, c := range checks {
		if c.state == verdictWarn {
			t.Fatalf(".terraform content triggered a warning: %+v", c)
		}
	}
}

// The stripper itself, on the shapes that carried the measured escapes: the
// stack text survives with its strings intact and without its comments.
func TestStripHCLCommentsKeepsStringsAndDropsComments(t *testing.T) {
	in := `a = "https://s3.fr-par.scw.cloud" # trailing
b = "quoted # not a comment" // gone
/* block
gone too */ c = 1`
	out := stripHCLComments(in)
	for _, kept := range []string{`"https://s3.fr-par.scw.cloud"`, `"quoted # not a comment"`, "c = 1"} {
		if !strings.Contains(out, kept) {
			t.Errorf("stripped too much: %q missing from %q", kept, out)
		}
	}
	for _, dropped := range []string{"trailing", "gone", "block"} {
		if strings.Contains(out, dropped) {
			t.Errorf("comment text survived: %q in %q", dropped, out)
		}
	}
}
