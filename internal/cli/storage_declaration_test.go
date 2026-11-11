package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/environment"
	"github.com/stephrobert/feint/internal/s3"
)

// TestEveryDeclaredStorageEndpointNamesADeclaredDoor is cited by the
// `storage.doors` field of the environment schema, and it holds the join
// between two things one file says separately.
//
// A stack that drives object storage says it twice by necessity: `storage.doors`
// arms the prefix the emulator serves, and an `iac.vars` value points the engine
// at it. Nothing makes the two agree — the second is a string with a path in it
// — so a door renamed in one place and not the other produces a 404 from a
// prefix nobody mounted, which reads as a broken emulator rather than as a
// declaration out of step. That is the defect shape `--projects` already paid
// for across three starters.
//
// The reverse is checked too, and it is the one that bites silently: doors
// declared with no `storage.directory` are dropped by startArgs without a word,
// so the file would ask for a prefix and never get it.
func TestEveryDeclaredStorageEndpointNamesADeclaredDoor(t *testing.T) {
	root := repoRoot(t)
	declarations := 0
	for _, source := range []string{"examples/stacks", "examples/quickstart"} {
		base := filepath.Join(root, source)
		dirs, err := stackDirs(base)
		if err != nil {
			// A root this repository does not have is not a failure: the
			// subject of this test is whatever stacks exist.
			continue
		}
		for _, dir := range dirs {
			path := filepath.Join(base, dir, "feint.yaml")
			if _, err := os.Stat(path); err != nil {
				continue
			}
			decl, err := environment.Load(path)
			if err != nil {
				t.Errorf("%s: %v", path, err)
				continue
			}
			declarations++
			checkStorageDeclaration(t, path, decl)
		}
	}
	// The reader proves it can find before it judges: a glob that matched
	// nothing would pass this file however wrong the stacks became.
	if declarations == 0 {
		t.Fatal("no feint.yaml was read, so this test measured nothing")
	}
}

func checkStorageDeclaration(t *testing.T, path string, decl *environment.File) {
	t.Helper()
	declared := make(map[string]bool, len(decl.Storage.Doors))
	for _, door := range decl.Storage.Doors {
		declared[door] = true
	}
	if len(decl.Storage.Doors) > 0 && decl.Storage.Directory == "" {
		t.Errorf("%s declares storage.doors %v and no storage.directory: `up` drops the doors "+
			"without a word, so the file asks for a prefix it will never get",
			path, decl.Storage.Doors)
	}
	for name, value := range decl.IaC.Vars {
		index := strings.Index(value, s3.ReportPath+"/")
		if index < 0 {
			continue
		}
		rest := strings.TrimPrefix(value[index:], s3.ReportPath+"/")
		parts := strings.SplitN(strings.TrimSuffix(rest, "/"), "/", 3)
		if len(parts) < 2 {
			t.Errorf("%s: iac.vars.%s points inside %s and names no provider/region door: %q",
				path, name, s3.ReportPath, value)
			continue
		}
		door := parts[0] + "/" + parts[1]
		if !declared[door] {
			t.Errorf("%s: iac.vars.%s points at the %s door and storage.doors declares %v: "+
				"the engine would ask for a prefix the emulator never mounted, and a 404 there "+
				"reads as a broken emulator rather than as a declaration out of step",
				path, name, door, decl.Storage.Doors)
		}
	}
}

// TestStartArgsArmsTheByteStoreOnlyWhenDeclared holds both directions of the
// one thing `up` gained, and the silent half is the second.
//
// A declaration that asks for storage must get it, or the stack's bucket is
// `count = 0` and the leg passes while proving nothing. And a declaration that
// does NOT ask must not get it: writing bytes is a side effect on the
// operator's disk, which is why `storage.directory` is absent by default
// exactly as `runtime.mode` is. Arming it for every stack would make `feint up`
// create a byte store for a reader who never mentioned one.
func TestStartArgsArmsTheByteStoreOnlyWhenDeclared(t *testing.T) {
	const declared = `version: 1
cloud:
  provider: exoscale
emulator:
  addr: 127.0.0.1:4599
storage:
  directory: /tmp/feint-test-objects
  doors:
    - exoscale/ch-dk-2
iac:
  engine: terraform
  directory: .
`
	const silent = `version: 1
cloud:
  provider: exoscale
emulator:
  addr: 127.0.0.1:4599
iac:
  engine: terraform
  directory: .
`
	for _, c := range []struct {
		name  string
		src   string
		armed bool
	}{
		{name: "declared", src: declared, armed: true},
		{name: "silent", src: silent, armed: false},
	} {
		t.Run(c.name, func(t *testing.T) {
			decl, err := environment.Parse(c.src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			args := strings.Join(startArgs(decl), " ")
			switch {
			case c.armed:
				if !strings.Contains(args, "--objectstore") {
					t.Fatalf("a declaration asking for storage got none: %s", args)
				}
				if !strings.Contains(args, "--s3-doors exoscale/ch-dk-2") {
					t.Fatalf("the declared door did not reach the command line: %s", args)
				}
			default:
				if strings.Contains(args, "--objectstore") || strings.Contains(args, "--s3-doors") {
					t.Fatalf("a declaration mentioning no storage was given one anyway, which writes "+
						"bytes on a disk nobody offered: %s", args)
				}
			}
		})
	}
}
