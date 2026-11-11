//go:build conformance

// Package conformancetest contains the isolated, pinned official-client harness.
package conformancetest

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Client is a pinned official client in an isolated, public-fake environment.
type Client struct {
	Path string
	Home string
	Env  []string
}

// AWS builds a private environment from the repository's public fake keys.
func AWS(t *testing.T, proxy, ca string) Client {
	t.Helper()
	binary, err := exec.LookPath("aws")
	if err != nil {
		t.Fatal("install AWS CLI 2.32.17 for this explicit real-client proof:", err)
	}
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil { //nolint:gosec // a private directory, not a file
		t.Fatal(err)
	}
	fake := FakeCredentials(t)
	cli := Client{Path: binary, Home: home, Env: []string{
		"HOME=" + home, "PATH=" + filepath.Dir(binary) + ":/usr/bin:/bin",
		"XDG_STATE_HOME=" + home, "XDG_RUNTIME_DIR=" + home,
		"AWS_ACCESS_KEY_ID=" + fake["SCW_ACCESS_KEY"], "AWS_SECRET_ACCESS_KEY=" + fake["SCW_SECRET_KEY"],
		"AWS_DEFAULT_REGION=fr-par", "AWS_EC2_METADATA_DISABLED=true",
		"AWS_CONFIG_FILE=" + filepath.Join(home, "config"), "AWS_SHARED_CREDENTIALS_FILE=" + filepath.Join(home, "credentials"),
		"AWS_PAGER=", "AWS_CLI_AUTO_PROMPT=off", "AWS_MAX_ATTEMPTS=1",
		"HTTP_PROXY=" + proxy, "HTTPS_PROXY=" + proxy, "ALL_PROXY=" + proxy,
		"NO_PROXY=127.0.0.1,localhost,::1", "LANG=C.UTF-8",
	}}
	if ca != "" {
		cli.Env = append(cli.Env, "AWS_CA_BUNDLE="+ca)
	}
	version := string(cli.Run(t, "--version"))
	if !strings.HasPrefix(version, "aws-cli/2.32.17 ") {
		t.Fatal("proof requires the measured AWS CLI 2.32.17, got:", version)
	}
	t.Log(strings.TrimSpace(version))
	return cli
}

// FakeCredentials reads the single public-fake credential fixture.
func FakeCredentials(t *testing.T) map[string]string {
	t.Helper()
	creds, err := os.ReadFile("../../tools/conformance/scaleway/fake-credentials.env")
	if err != nil {
		t.Fatal(err)
	}
	fake := make(map[string]string)
	for _, line := range strings.Split(string(creds), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok && strings.HasPrefix(key, "SCW_") {
			fake[key] = value
		}
	}
	if fake["SCW_ACCESS_KEY"] == "" || fake["SCW_SECRET_KEY"] == "" {
		t.Fatal("canonical public fake credentials are absent")
	}
	return fake
}

// Run executes the pinned client with a bounded lifetime and no shell.
func (c Client) Run(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Path, args...) //nolint:gosec // pinned official client, explicit arguments, no shell
	cmd.Env = c.Env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", filepath.Base(c.Path), strings.Join(args, " "), err, out)
	}
	return out
}

// Attempt runs the client and returns its output and its error instead of
// failing the test, for the cases where the REFUSAL is the subject.
//
// Run cannot serve those: it calls t.Fatalf on a non-zero exit, so a test that
// wants to assert "the client was told no" would have to pass for the wrong
// reason — either by not running the case, or by reading a success as proof.
// A refusal a suite cannot express is a refusal the emulator can stop sending
// without anything noticing.
func (c Client) Attempt(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Path, args...) //nolint:gosec // pinned official client, explicit arguments, no shell
	cmd.Env = c.Env
	out, err := cmd.CombinedOutput()
	return out, err
}

// ProbeProxyGuard sends a local CONNECT refusal control, never a cloud request.
func ProbeProxyGuard(ctx context.Context, endpoint string) error {
	proxy, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://s3.fr-par.scw.cloud/never-forward", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(r)
	if response != nil {
		if closeErr := response.Body.Close(); closeErr != nil {
			return closeErr
		}
	}
	if err == nil {
		return fmt.Errorf("guard unexpectedly permitted CONNECT")
	}
	return nil
}
