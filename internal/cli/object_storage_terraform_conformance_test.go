//go:build conformance

package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/conformancetest"
	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/objectstore"
	"github.com/stephrobert/feint/internal/providers/scaleway"
	"github.com/stephrobert/feint/internal/proxy"
	"github.com/stephrobert/feint/internal/s3"
)

// This witness refuses to run on the host network. The pinned provider ignores
// its S3 endpoint; the scoped TLS proxy captures its real cloud authorities.
func TestScalewayTerraformStorageInIsolatedNetwork(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Fatal("drop privileges after creating the disposable network namespace")
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	if len(interfaces) != 1 || interfaces[0].Name != "lo" || interfaces[0].Flags&net.FlagUp == 0 {
		t.Fatal("explicit proof requires a fresh network namespace with only loopback up")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp4", "192.0.2.1:443")
	if err == nil {
		connection.Close()
		t.Fatal("namespace escape control connected outside loopback")
	}
	pluginDir := os.Getenv("FEINT_TF_PLUGIN_DIR")
	if !filepath.IsAbs(pluginDir) {
		t.Fatal("FEINT_TF_PLUGIN_DIR must name the predownloaded, pinned provider directory")
	}
	terraform, err := exec.LookPath("terraform")
	if err != nil {
		t.Fatal("the explicit Terraform witness requires Terraform:", err)
	}
	home := privateStorageDir(t)
	payload := bytes.Repeat([]byte("feint Terraform storage bytes\n"), 4096)
	if err := os.WriteFile(filepath.Join(home, "payload"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "terraform.rc"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	objects, err := objectstore.OpenWithLimit(t.Context(), filepath.Join(home, "bytes"), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	}()
	env := emulator.DefaultEnv()
	profile := scaleway.New(env).ObjectStorage("fr-par")
	mount := func() *httptest.Server {
		t.Helper()
		handler, err := s3.Mount(http.NotFoundHandler(), objects, []s3.Profile{profile},
			1<<20, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		return httptest.NewServer(handler)
	}
	server := mount()
	defer func() { server.Close() }()
	endpoint := server.URL + strings.TrimSuffix(s3.DoorPath("scaleway", "fr-par"), "/")
	authority, err := proxy.MintAuthority()
	if err != nil {
		t.Fatal(err)
	}
	ca := filepath.Join(home, "ca.pem")
	if err := authority.WriteCA(ca); err != nil {
		t.Fatal(err)
	}
	writer := proxy.NewWriter(io.Discard, 64)
	defer func() {
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
	}()
	startProxy := func() (*httptest.Server, *s3.LocalTransport) {
		t.Helper()
		transport, err := s3.NewLocalTransport(server.URL, []s3.Profile{profile})
		if err != nil {
			t.Fatal(err)
		}
		forward, err := proxy.NewForward(proxy.ForwardOptions{
			Hosts:  []string{profile.CloudHost, "feint-terraform-storage." + profile.CloudHost},
			Writer: writer, Authority: authority, Transport: transport,
			Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		if err != nil {
			t.Fatal(err)
		}
		return httptest.NewServer(forward), transport
	}
	egress, transport := startProxy()
	defer func() { egress.Close(); transport.CloseIdleConnections() }()
	config := `
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
  name = "feint-terraform-storage"
  region = "fr-par"
}
resource "scaleway_object" "payload" {
  bucket = scaleway_object_bucket.storage.name
  key = "flatcar_production_scaleway_image.qcow2"
  file = "${path.module}/payload"
  hash = filemd5("${path.module}/payload")
  content_type = "application/octet-stream"
}
`
	if err := os.WriteFile(filepath.Join(home, "main.tf"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	fake := conformancetest.FakeCredentials(t)
	run := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, terraform, args...)
		cmd.Dir = home
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1",
			"TF_CLI_CONFIG_FILE=" + filepath.Join(home, "terraform.rc"),
			"SCW_S3_ENDPOINT=" + endpoint,
			"SCW_API_URL=" + server.URL, "SCW_INSECURE=false", "SSL_CERT_FILE=" + ca,
			"HTTP_PROXY=" + egress.URL, "HTTPS_PROXY=" + egress.URL, "NO_PROXY=127.0.0.1,localhost"}
		for key, value := range fake {
			if key != "SCW_API_URL" && key != "SCW_INSECURE" {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated terraform %v: %v\n%s", args, err, out)
		}
		return out
	}
	run("init", "-input=false", "-no-color", "-plugin-dir="+pluginDir)
	run("apply", "-auto-approve", "-input=false", "-no-color")
	run("plan", "-input=false", "-no-color", "-detailed-exitcode")
	checkBytes := func() {
		t.Helper()
		response, err := http.Get(endpoint + "/feint-terraform-storage/flatcar_production_scaleway_image.qcow2") //nolint:noctx // loopback test server with the test's lifetime
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		actual, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != 200 || !bytes.Equal(actual, payload) {
			t.Fatal("Terraform did not publish identical bytes:", response.StatusCode, err)
		}
	}
	checkBytes()
	egress.Close()
	transport.CloseIdleConnections()
	server.Close()
	if err := objects.Close(); err != nil {
		t.Fatal(err)
	}
	objects, err = objectstore.OpenWithLimit(t.Context(), filepath.Join(home, "bytes"), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	server = mount()
	egress, transport = startProxy()
	endpoint = server.URL + strings.TrimSuffix(s3.DoorPath("scaleway", "fr-par"), "/")
	checkBytes()
	run("plan", "-input=false", "-no-color", "-detailed-exitcode")
	run("destroy", "-auto-approve", "-input=false", "-no-color")
	inventory, err := objects.Usage(t.Context())
	if err != nil || len(inventory) != 0 {
		t.Fatal("destroy left actual object references:", inventory, err)
	}
	if err := objects.CollectGarbage(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	written, dropped := writer.Stats()
	if written == 0 || dropped != 0 {
		t.Fatal("interception witness was empty or incomplete:", written, dropped)
	}
	t.Log(fmt.Sprintf("Scaleway 2.82.0: apply, two empty plans, %d identical bytes across reopen, destroy and owned GC; cloud-host TLS interception, loopback-only namespace", len(payload)))
}
