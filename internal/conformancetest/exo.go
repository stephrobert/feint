//go:build conformance

package conformancetest

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// EXO uses only the witness HOME, refusing proxy and the pack's public fake
// environment. No account configuration from the host is read.
func EXO(t *testing.T, base Client, vars map[string]string) Client {
	t.Helper()
	binary, err := exec.LookPath("exo")
	if err != nil {
		t.Fatal("install Exoscale CLI 1.95.1 for this explicit byte proof:", err)
	}
	client := Client{Path: binary, Home: base.Home, Env: append([]string{}, base.Env...)}
	client.Env = append(client.Env, "PATH="+filepath.Dir(binary)+":/usr/bin:/bin")
	for key, value := range vars {
		client.Env = append(client.Env, key+"="+value)
	}
	version := string(client.Run(t, "version"))
	if !strings.HasPrefix(version, "exo 1.95.1 7aaf76d0 ") {
		t.Fatal("proof requires the measured Exoscale CLI 1.95.1, got:", version)
	}
	t.Log("Exoscale CLI 1.95.1; isolated pack-owned fake credentials")
	return client
}
