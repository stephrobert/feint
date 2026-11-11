//go:build conformance

package conformancetest

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// SCW reuses the witness's private HOME and refusing proxy, never a host
// credential profile. S3 requests target only the explicit local endpoint.
func SCW(t *testing.T, base Client, endpoint string) Client {
	t.Helper()
	binary, err := exec.LookPath("scw")
	if err != nil {
		t.Fatal("install Scaleway CLI 2.62.0 for this explicit bucket proof:", err)
	}
	client := Client{Path: binary, Home: base.Home, Env: append([]string{}, base.Env...)}
	api, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal("invalid witness endpoint:", err)
	}
	api.Path, api.RawPath = "", ""
	cache := filepath.Join(base.Home, "scw-cache")
	if err := os.Mkdir(cache, 0700); err != nil { //nolint:gosec // owned witness directory, not a file
		t.Fatal(err)
	}
	// core/checks.go at the pinned revision skips unrelated GitHub/API-key
	// checks when this private marker is fresh. The refusing proxy stays on.
	if err := os.WriteFile(filepath.Join(cache, "last-cli-checks"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	client.Env = append(client.Env, "PATH="+filepath.Dir(binary)+":/usr/bin:/bin",
		"SCW_S3_ENDPOINT="+endpoint, "SCW_API_URL="+api.String(), "SCW_INSECURE=true", "SCW_CACHE_DIR="+cache)
	for key, value := range FakeCredentials(t) {
		if key != "SCW_API_URL" {
			client.Env = append(client.Env, key+"="+value)
		}
	}
	version := string(client.Run(t, "version"))
	fields := strings.Fields(version)
	if len(fields) < 2 || fields[0] != "Version" || fields[1] != "2.62.0" {
		t.Fatal("proof requires the measured Scaleway CLI 2.62.0, got:", version)
	}
	t.Log("Scaleway CLI 2.62.0; isolated public fake credentials")
	return client
}
