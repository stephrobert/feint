package conformance

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The login assertions the three ssh suites share (#501).
//
// runtime-proof.yml was red on the "Scaleway ssh suite" step for five
// consecutive scheduled nights (2026-08-22 to 2026-08-26) and the step's log
// ended on nothing: no FAIL line, no emulator ERROR, exit 1. The block died in
// a single unguarded command substitution whose remote command ended in
// `grep -c`, which exits 1 when the count is zero — and the count WAS zero,
// legitimately, because it counted the key's comment and the real cloud drops
// the comment (sshkeys.go, held by internal/providers/scaleway's
// TestASSHKeyIsPublishedWithoutItsComment, merged the day the red nights
// started).
//
// These tests drive assert_login itself, against a stub ssh, under the same
// `set -euo pipefail` the suites run under. What they hold is the repair's two
// halves: the assertion bears on the key MATERIAL, never the comment, and every
// way the login can disappoint produces a FAIL line that names it.
// tools/falsify/specs/ssh-login-names-what-it-lacks.json replays them with each
// guard neutralised.

// The accepting half first: a machine whose authorized_keys holds the key the
// way the real cloud publishes it — material without comment — passes. This is
// the exact input the old block died on.
func TestASuccessfulLoginReportsTheKeyByItsMaterial(t *testing.T) {
	code, output := runLogin(t, loginStub{
		user:       "root",
		authorized: "ssh-ed25519 " + stubMaterial + "\n",
	}, "root")
	if code != 0 {
		t.Errorf("a login whose key lost its comment failed (exit %d); the comment is not a property any provider promises:\n%s", code, output)
	}
	if !strings.Contains(output, "registered key present") {
		t.Errorf("the success does not say the key was found:\n%s", output)
	}
}

// The core of #501: deprived of its key in authorized_keys, the suite fails
// NAMING authorized_keys — not by exiting 1 without a word.
func TestALoginMissingItsKeyFailsNamingAuthorizedKeys(t *testing.T) {
	code, output := runLogin(t, loginStub{
		user:       "root",
		authorized: "ssh-ed25519 AAAAsomebodyElsesKey other-host\n",
	}, "root")
	if code == 0 {
		t.Errorf("the login passed while authorized_keys does not hold the registered key:\n%s", output)
	}
	if !strings.Contains(output, "FAIL:") || !strings.Contains(output, "authorized_keys") {
		t.Errorf("the failure does not name authorized_keys; a suite that exits 1 without a word is not fixed, it is displaced:\n%s", output)
	}
}

// A default account that moved is named, with the account it found.
func TestALoginAsTheWrongUserFailsNamingTheAccount(t *testing.T) {
	code, output := runLogin(t, loginStub{
		user:       "alpine",
		authorized: "ssh-ed25519 " + stubMaterial + "\n",
	}, "root")
	if code == 0 {
		t.Errorf("the login passed as 'alpine' where the provider provisions root:\n%s", output)
	}
	if !strings.Contains(output, "FAIL:") || !strings.Contains(output, "'alpine'") {
		t.Errorf("the failure does not name the account it got:\n%s", output)
	}
}

// An unreadable file is not an absent key: the two get different sentences,
// because "the shape is unknown" must never be reported as "not found".
func TestAnUnreadableAuthorizedKeysFailsSayingSo(t *testing.T) {
	code, output := runLogin(t, loginStub{
		user:       "root",
		catFails:   true,
		authorized: "unused",
	}, "root")
	if code == 0 {
		t.Errorf("the login passed while authorized_keys cannot be read:\n%s", output)
	}
	if !strings.Contains(output, "FAIL:") || !strings.Contains(output, "not readable") {
		t.Errorf("the failure does not say the file was unreadable, so an io error reads as a missing key:\n%s", output)
	}
}

// The call sites, on the same terms as TestEverySuiteThatLogsInAsksForItsImages:
// a shared block nobody calls repairs nothing, and the three suites carried the
// same defect precisely because the block was written three times.
func TestEverySshSuiteAssertsItsLoginThroughTheSharedBlock(t *testing.T) {
	suites := []string{
		filepath.Join("scaleway", "ssh.sh"),
		filepath.Join("outscale", "ssh.sh"),
		filepath.Join("exoscale", "ssh.sh"),
	}
	for _, suite := range suites {
		body, err := os.ReadFile(suite) //nolint:gosec // a fixed path in this directory
		if err != nil {
			t.Fatalf("read %s: %v", suite, err)
		}
		called := false
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), `assert_login "`) {
				called = true
				break
			}
		}
		if !called {
			t.Errorf("%s never calls assert_login on a line of its own: its login step is not held by the shared assertions", suite)
		}
		if strings.Contains(string(body), "grep -c ") {
			t.Errorf("%s still lets a grep -c near its verdict; grep -c exits 1 on a count of zero, which is the silent death of #501", suite)
		}
	}
}

const stubMaterial = "AAAAC3NzaC1lZDI1NTE5AAAAIFeintConformanceStubMaterial"

type loginStub struct {
	user       string
	authorized string
	catFails   bool
}

// runLogin sources the real sshlogin.sh and calls assert_login under
// `set -euo pipefail` — the exact regime the suites run under, and the one
// that made the unguarded block die silently — with a stub ssh first in PATH
// standing for the machine.
func runLogin(t *testing.T, stub loginStub, wantUser string) (int, string) {
	t.Helper()
	requireTool(t, "bash")

	dir := t.TempDir()

	// The machine, as far as assert_login can see one.
	stubSSH := `#!/usr/bin/env bash
cmd="${!#}"
case "$cmd" in
  hostname) echo stub-machine ;;
  'id -un') echo "$STUB_USER" ;;
  'cat ~/.ssh/authorized_keys')
    if [ -n "${STUB_CAT_FAILS:-}" ]; then
      echo "cat: can't open '/root/.ssh/authorized_keys'" >&2
      exit 1
    fi
    printf '%s' "$STUB_AUTHORIZED" ;;
  *) echo "stub ssh: unexpected remote command: $cmd" >&2; exit 99 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(stubSSH), 0o755); err != nil { //nolint:gosec // an executable stub
		t.Fatalf("write the ssh stub: %v", err)
	}

	// The key pair the suite registered: the public file carries a comment,
	// the way ssh-keygen -C writes it, and the machine's authorized_keys may
	// or may not — that difference is the whole subject.
	identity := filepath.Join(dir, "id")
	pubkey := identity + ".pub"
	if err := os.WriteFile(identity, []byte("stub private key\n"), 0o600); err != nil {
		t.Fatalf("write the identity stub: %v", err)
	}
	if err := os.WriteFile(pubkey, []byte("ssh-ed25519 "+stubMaterial+" feint-conformance\n"), 0o644); err != nil { //nolint:gosec // a public key
		t.Fatalf("write the public key stub: %v", err)
	}

	script, err := filepath.Abs("sshlogin.sh")
	if err != nil {
		t.Fatalf("locate sshlogin.sh: %v", err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("sshlogin.sh is not where this test looks (%s): %v", script, err)
	}

	cmd := exec.Command("bash", "-c", //nolint:gosec // fixed script, test-controlled arguments
		`set -euo pipefail
fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "  ok: $*"; }
. "$1"
assert_login "$2" "$3" "$4" "$5"`,
		"bash", script, "root@203.0.113.9", identity, wantUser, pubkey)
	cmd.Env = append(os.Environ(),
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"STUB_USER="+stub.user,
		"STUB_AUTHORIZED="+stub.authorized,
	)
	if stub.catFails {
		cmd.Env = append(cmd.Env, "STUB_CAT_FAILS=1")
	}
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("run assert_login: %v\n%s", err, out)
		}
		code = exit.ExitCode()
	}
	return code, string(out)
}
