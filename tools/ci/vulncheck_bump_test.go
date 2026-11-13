package ci

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// tools/ci/vulncheck-bump.sh moves every Go pin in the repository to one new
// version, together.
//
// It exists because the pin is not one number: it is nineteen, in twelve
// files, and a nightly pull request that moves eighteen of them is a pull
// request born red — internal/cli's TestTheGoWorkflowPinsTheVersionMiseDoes
// fails on any mismatch. The script is the propose job's only write, so a half-moved tree
// here is a half-moved tree in the pull request. Every branch below is driven
// on a scratch tree, and the assertion is on the whole tree afterwards: every
// pin moved, and every line that is not a pin left alone — including the prose
// that quotes the old version, because the history in go.yml is a record of
// why the pin moved, not a pin.
func TestVulncheckBump(t *testing.T) {
	for _, tc := range []struct {
		name    string
		to      string
		wantErr bool
		wantOut string
	}{
		{
			// The case that happens: an advisory is fixed in 1.26.12, and the
			// bump moves mise.toml and every workflow pin to it, in one
			// command, with the count named in the output.
			name: "moves every pin together", to: "1.26.12",
			wantOut: "moved 4 pins to 1.26.12",
		},
		{
			// A bump to the version already pinned is a no-op wearing a
			// command's clothes. Refuse it: a pull request that changes
			// nothing is a pull request that wastes a reviewer's evening.
			name: "refuses the version already pinned", to: "1.26.11", wantErr: true,
		},
		{
			// A bump backwards is a downgrade, and a downgrade past a fixed
			// advisory is a regression. Refuse it the same way.
			name: "refuses a backwards move", to: "1.26.10", wantErr: true,
		},
		{
			// A version that is not a dotted triple cannot be compared
			// against the pin, and comparing "1.26" against "1.26.11" with
			// sort -V would answer something. Refuse it before it moves.
			name: "refuses a malformed version", to: "1.26", wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := bumpTree(t)
			code, out := runVulncheckBump(t, root, tc.to)
			if tc.wantErr {
				if code == 0 {
					t.Errorf("exit 0, want non-zero\n%s", out)
				}
				return
			}
			if code != 0 {
				t.Fatalf("exit %d, want 0\n%s", code, out)
			}
			if !strings.Contains(out, tc.wantOut) {
				t.Errorf("output does not contain %q\n%s", tc.wantOut, out)
			}
			// The assertion is on the pin pattern, not on the bare version:
			// the history prose in a.yml quotes the old version on purpose,
			// and a bump that rewrote it would rewrite the record of why
			// the pin moved. That is asserted separately below.
			for _, path := range []string{
				"mise.toml",
				".github/workflows/a.yml",
				".github/workflows/b.yml",
			} {
				pin := "go-version: '1.26.11'"
				wantPin := "go-version: '1.26.12'"
				if path == "mise.toml" {
					pin = `go = "1.26.11"`
					wantPin = `go = "1.26.12"`
				}
				data, err := os.ReadFile(filepath.Join(root, path))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(data), pin) {
					t.Errorf("%s still names the old pin\n%s", path, data)
				}
				if !strings.Contains(string(data), wantPin) {
					t.Errorf("%s does not name the new pin\n%s", path, data)
				}
			}
			// The prose is not a pin: the history line in a.yml quotes the
			// old version, and a bump that rewrote it would rewrite the
			// record of why the pin moved.
			data, _ := os.ReadFile(filepath.Join(root, ".github/workflows/a.yml"))
			if !strings.Contains(string(data), "# moved to 1.26.11 on 2026-11-11") {
				t.Errorf("a.yml's history prose was rewritten\n%s", data)
			}
		})
	}
}

// A tree with no workflow pin at all is its own case: it is what a renamed
// workflow directory produces, and "moved 0 pins" would be a pull request
// that changes nothing while claiming to have moved the pin.
func TestVulncheckBumpRefusesATreeWithNoWorkflowPin(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "mise.toml"), []byte("[tools]\ngo = \"1.26.11\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".github", "workflows", "a.yml"), []byte("name: nothing pins Go here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out := runVulncheckBump(t, root, "1.26.12")
	if code == 0 {
		t.Errorf("exit 0, want non-zero\n%s", out)
	}
	if !strings.Contains(out, "no workflow pins a Go version") {
		t.Errorf("output does not contain %q\n%s", "no workflow pins found", out)
	}
}

// bumpTree is a scratch repository root: mise.toml plus two workflows, one of
// them carrying a prose history line that quotes the old version.
func bumpTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "mise.toml"), []byte("[tools]\ngo = \"1.26.11\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	workflows := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(workflows, 0o755); err != nil {
		t.Fatal(err)
	}
	a := "name: a\njobs:\n  build:\n    steps:\n      - uses: actions/setup-go@v1\n        with:\n          go-version: '1.26.11'\n          # moved to 1.26.11 on 2026-11-11\n      - uses: actions/setup-go@v1\n        with:\n          go-version: '1.26.11'\n"
	if err := os.WriteFile(filepath.Join(workflows, "a.yml"), []byte(a), 0o644); err != nil {
		t.Fatal(err)
	}
	b := "name: b\njobs:\n  scan:\n    steps:\n      - uses: actions/setup-go@v1\n        with:\n          go-version: '1.26.11'\n"
	if err := os.WriteFile(filepath.Join(workflows, "b.yml"), []byte(b), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func runVulncheckBump(t *testing.T, root, to string) (int, string) {
	t.Helper()
	script, err := filepath.Abs("vulncheck-bump.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script, to)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return code, string(out)
}
