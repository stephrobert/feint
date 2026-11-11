//go:build conformance

package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/conformancetest"
	"github.com/stephrobert/feint/internal/s3"
)

// This is the mirror of TestScalewayTerraformStorageInIsolatedNetwork, and the
// two are only worth having together.
//
// That witness REFUSES to run outside a fresh network namespace: it checks that
// only loopback is up, dials an external address to prove it cannot escape, and
// needs `sudo unshare`. It answers "the provider's traffic can be made to reach
// a local door, and nothing leaked while we proved it".
//
// This one answers the question #849 actually asks, which is the opposite one:
// can an operator get there on the HOST network, with no privilege, no
// namespace and no change to the machine? So it refuses to run INSIDE a
// namespace — a pass obtained in one would prove nothing about the command,
// since the namespace itself would be doing the containment.
//
// The proof is not "apply succeeded". It is that the bytes are in the LOCAL
// byte store at the expected sha256: a request that went anywhere else cannot
// produce that, and the real cloud would refuse these deliberately public fake
// credentials long before it stored anything.
func TestScalewayTerraformStorageThroughFeintRunOnTheHostNetwork(t *testing.T) {
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	if len(interfaces) <= 1 {
		t.Fatal("this witness must run on the HOST network; in a namespace the containment would be the namespace's, not the command's")
	}
	pluginDir := os.Getenv("FEINT_TF_PLUGIN_DIR")
	if !filepath.IsAbs(pluginDir) {
		t.Fatal("FEINT_TF_PLUGIN_DIR must name the predownloaded, pinned provider directory")
	}
	terraform, err := exec.LookPath("terraform")
	if err != nil {
		t.Fatal("this witness requires Terraform:", err)
	}

	store, objects := doorServer(t, door("scaleway", "fr-par"))
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	profiles, err := runProfiles(objects.URL)
	if err != nil {
		t.Fatalf("the emulator's doors could not be discovered: %v", err)
	}

	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("feint run terraform witness bytes\n"), 4096)
	digest := sha256.Sum256(payload)
	want := hex.EncodeToString(digest[:])
	if err := os.WriteFile(filepath.Join(home, "payload"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "terraform.rc"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	const bucket, key = "feint-run-witness", "payload.qcow2"
	config := fmt.Sprintf(`
terraform {
  required_providers {
    scaleway = {
      source = "scaleway/scaleway"
      version = "= 2.82.0"
    }
  }
}
provider "scaleway" {}
resource "scaleway_object_bucket" "storage" {
  name = %q
  region = "fr-par"
}
resource "scaleway_object" "payload" {
  bucket = scaleway_object_bucket.storage.name
  key = %q
  file = "${path.module}/payload"
  hash = filemd5("${path.module}/payload")
  content_type = "application/octet-stream"
}
`, bucket, key)
	if err := os.WriteFile(filepath.Join(home, "main.tf"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	// The command refuses credentials that are not known fakes, so these are
	// the repository's public ones and the refusal is part of what is proved.
	fake := conformancetest.FakeCredentials(t)
	for name, value := range fake {
		t.Setenv(name, value)
	}
	t.Setenv("SCW_API_URL", objects.URL)
	t.Setenv("TF_IN_AUTOMATION", "1")
	t.Setenv("TF_INPUT", "0")
	t.Setenv("TF_DATA_DIR", filepath.Join(home, ".terraform"))
	t.Setenv("TF_CLI_CONFIG_FILE", filepath.Join(home, "terraform.rc"))
	// Deliberately NOT TF_PLUGIN_CACHE_DIR: pointing the cache at the same
	// directory as -plugin-dir makes the first run unpack into the mirror and
	// every later run fail with "cannot install existing provider directory to
	// itself". The witness was green once and red the second time for exactly
	// that reason, which is a witness that proves nothing on a machine that has
	// already run it.

	// init runs OUTSIDE the command: resolving a provider is not what is being
	// measured, and asking the run to intercept the registry would be a second
	// claim this witness does not make.
	initCtx, cancelInit := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancelInit()
	initialise := exec.CommandContext(initCtx, terraform, "init", "-no-color",
		"-plugin-dir="+pluginDir)
	initialise.Dir = home
	startedInit := time.Now()
	if out, err := initialise.CombinedOutput(); err != nil {
		t.Fatalf("terraform init: %v\n%s", err, out)
	}
	t.Logf("terraform init in %s", time.Since(startedInit).Round(time.Millisecond))

	// through runs the client through the command under test, from the module
	// directory, and returns the exit code the child reported.
	through := func(args ...string) (int, string) {
		t.Helper()
		previous, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(home); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Chdir(previous); err != nil {
				t.Error(err)
			}
		}()
		var stdout, stderr bytes.Buffer
		started := time.Now()
		code, err := runChild(objects.URL, "127.0.0.1:0", profiles,
			append([]string{terraform}, args...), false, childEnvironment{}, &stdout, &stderr)
		// Timed and logged, because a witness that passes implausibly fast is a
		// witness nobody should believe: 0.94s for init+apply+plan+destroy was
		// the first result here, and reading the durations is what settles it.
		t.Logf("feint run %v: exit %d in %s", args, code, time.Since(started).Round(time.Millisecond))
		if err != nil {
			t.Fatalf("feint run %v: %v\n%s", args, err, stderr.String())
		}
		return code, stdout.String() + stderr.String()
	}

	code, out := through("apply", "-auto-approve", "-no-color")
	if code != 0 || !strings.Contains(out, "Apply complete!") {
		t.Fatalf("apply did not complete through feint run: code %d\n%s", code, out)
	}
	// The receipt is part of the contract: #849 asks that every rewrite be
	// inspectable, and this is where an operator reads it.
	if !strings.Contains(out, "<bucket>.s3.fr-par.scw.cloud") {
		t.Fatalf("the receipt does not name the virtual-host rewrite:\n%s", out)
	}

	// THE proof: the bytes are in the local store, read back through the door.
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		objects.URL+s3.DoorPath("scaleway", "fr-par")+bucket+"/"+key, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("read the object back from the local door: %v", err)
	}
	stored, err := io.ReadAll(io.LimitReader(response.Body, int64(len(payload))+1))
	if err := response.Body.Close(); err != nil {
		t.Error(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("the local door answered %d for the object Terraform wrote: %s", response.StatusCode, stored)
	}
	got := sha256.Sum256(stored)
	if hex.EncodeToString(got[:]) != want {
		t.Fatalf("the local store holds %d bytes digesting %s; Terraform wrote %d bytes digesting %s",
			len(stored), hex.EncodeToString(got[:]), len(payload), want)
	}

	// An empty second plan, which is what proves create-then-read round-trips
	// rather than merely answering.
	if code, out := through("plan", "-no-color", "-detailed-exitcode"); code != 0 {
		t.Fatalf("the second plan reported changes (exit %d):\n%s", code, out)
	}

	if code, out := through("destroy", "-auto-approve", "-no-color"); code != 0 ||
		!strings.Contains(out, "Destroy complete!") {
		t.Fatalf("destroy did not complete through feint run: code %d\n%s", code, out)
	}
}
