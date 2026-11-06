//go:build conformance

package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/conformancetest"
)

func TestOfficialAWSCLIBytes(t *testing.T) {
	s, store := fixture(t)
	var guardHits atomic.Int64
	guard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		guardHits.Add(1)
		http.Error(w, "feint conformance: external requests are refused, never forwarded", http.StatusBadGateway)
	}))
	defer guard.Close()
	// A positive control proves the proxy actually records and refuses CONNECT.
	if err := conformancetest.ProbeProxyGuard(t.Context(), guard.URL); err != nil {
		t.Fatal(err)
	}
	if guardHits.Load() != 1 {
		t.Fatal("guard did not observe its positive control")
	}
	before := guardHits.Load()
	type exchange struct {
		Method, Path, Encoding, DecodedLength, ChecksumAlgorithm string
		SigV4                                                    bool
		Status                                                   int
	}
	var mu sync.Mutex
	var exchanges []exchange
	var corruptChecksum atomic.Bool
	endpoint := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record := exchange{Method: r.Method, Path: r.URL.RequestURI(),
			Encoding: r.Header.Get("Content-Encoding"), DecodedLength: r.Header.Get("X-Amz-Decoded-Content-Length"),
			ChecksumAlgorithm: r.Header.Get("X-Amz-Sdk-Checksum-Algorithm"),
			SigV4:             strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ")}
		response := &statusWriter{ResponseWriter: w, corrupt: corruptChecksum.Load()}
		s.ServeHTTP(response, r)
		record.Status = response.status
		mu.Lock()
		exchanges = append(exchanges, record)
		mu.Unlock()
	}))
	endpoint.Config.ReadHeaderTimeout = 5 * time.Second
	endpoint.Config.ReadTimeout = 30 * time.Second
	endpoint.Config.WriteTimeout = 30 * time.Second
	endpoint.StartTLS()
	defer endpoint.Close()
	home := t.TempDir()
	ca := filepath.Join(home, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: endpoint.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	cli := conformancetest.AWS(t, guard.URL, ca)
	call := func(args ...string) []byte {
		t.Helper()
		return cli.Run(t, append([]string{"--endpoint-url", endpoint.URL, "--no-cli-pager", "s3api"}, args...)...)
	}
	payload := bytes.Repeat([]byte{0, 255, 'f', 'e', 'i', 'n', 't', '\r', '\n'}, 8192)
	input, output := filepath.Join(home, "input"), filepath.Join(home, "output")
	if err := os.WriteFile(input, payload, 0600); err != nil {
		t.Fatal(err)
	}
	call("create-bucket", "--bucket", "feint-byte-proof")
	put := call("put-object", "--bucket", "feint-byte-proof", "--key", "proof/data",
		"--body", input, "--content-type", "application/octet-stream", "--metadata", "purpose=byte-proof",
		"--checksum-algorithm", "CRC32")
	get := call("get-object", "--bucket", "feint-byte-proof", "--key", "proof/data", "--checksum-mode", "ENABLED", output)
	got, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("official client did not retrieve identical bytes", err)
	}
	sum := sha256.Sum256(payload)
	for _, out := range [][]byte{put, get} {
		var result map[string]json.RawMessage
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatal("client result is not JSON:", err, string(out))
		}
		var checksum string
		if err := json.Unmarshal(result["ChecksumSHA256"], &checksum); err != nil || checksum != base64.StdEncoding.EncodeToString(sum[:]) {
			t.Fatal("client did not return the real SHA-256 checksum", err, string(out))
		}
		if _, exists := result["ETag"]; exists {
			t.Fatal("gateway must not fabricate an unmeasured ETag")
		}
	}
	// The accepting half alone cannot prove the client validates this header.
	// Alter only the response checksum, retaining the same successful bytes.
	corruptChecksum.Store(true)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	cmd := exec.CommandContext(ctx, cli.Path, "--endpoint-url", endpoint.URL, "--no-cli-pager", "s3api",
		"get-object", "--bucket", "feint-byte-proof", "--key", "proof/data", "--checksum-mode", "ENABLED", filepath.Join(home, "corrupt-output"))
	cmd.Env = cli.Env
	control, controlErr := cmd.CombinedOutput()
	cancel()
	corruptChecksum.Store(false)
	if controlErr == nil || !strings.Contains(strings.ToLower(string(control)), "checksum") {
		t.Fatal("official client did not reject a wrong response checksum", controlErr, string(control))
	}
	t.Log("negative control: identical response bytes with a wrong SHA-256 checksum were rejected by the official client")
	call("delete-object", "--bucket", "feint-byte-proof", "--key", "proof/data")
	call("delete-bucket", "--bucket", "feint-byte-proof")
	names, err := store.ListBuckets(t.Context())
	if err != nil || len(names) != 0 || guardHits.Load() != before {
		t.Fatal("cleanup or no-external-proxy-request proof failed", names, err, guardHits.Load()-before)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(exchanges) != 6 {
		t.Fatal("expected five lifecycle exchanges plus one checksum refusal control", exchanges)
	}
	for i, code := range []int{200, 200, 200, 200, 204, 204} {
		if exchanges[i].Status != code || !exchanges[i].SigV4 {
			t.Fatal("unexpected status or unsigned client request", exchanges)
		}
	}
	if exchanges[1].Encoding != "aws-chunked" || exchanges[1].DecodedLength != "73728" {
		t.Fatal("HTTPS proof did not exercise the client's real streaming framing", exchanges[1])
	}
	wire, err := json.Marshal(exchanges)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("exchanges=%s", wire)
	t.Logf("bytes=%d sha256=%s; ETag absent and not required by this client; proxy guard delta=0 (not a network namespace)", len(got), hex.EncodeToString(sum[:]))
}

type statusWriter struct {
	http.ResponseWriter
	status  int
	corrupt bool
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	if w.corrupt {
		w.Header().Set("X-Amz-Checksum-Sha256", base64.StdEncoding.EncodeToString(make([]byte, sha256.Size)))
	}
	w.ResponseWriter.WriteHeader(status)
}
