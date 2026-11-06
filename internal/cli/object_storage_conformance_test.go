//go:build conformance

package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/conformancetest"
	"github.com/stephrobert/feint/internal/core/objectstore"
	"github.com/stephrobert/feint/internal/s3"
)

func TestMountedS3OfficialAWSCLIBytes(t *testing.T) {
	var guardHits atomic.Int64
	guard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		guardHits.Add(1)
		http.Error(w, "external requests refused, never forwarded", http.StatusBadGateway)
	}))
	defer guard.Close()
	if err := conformancetest.ProbeProxyGuard(t.Context(), guard.URL); err != nil || guardHits.Load() != 1 {
		t.Fatalf("local proxy control did not record its refusal: %v hits=%d", err, guardHits.Load())
	}
	aws := conformancetest.AWS(t, guard.URL, "")
	binary := filepath.Join(t.TempDir(), "feint")
	buildCtx, cancelBuild := context.WithTimeout(t.Context(), 2*time.Minute)
	build := exec.CommandContext(buildCtx, "go", "build", "-o", binary, "../../cmd/feint")
	if out, err := build.CombinedOutput(); err != nil {
		cancelBuild()
		t.Fatalf("build the actual binary: %v\n%s", err, out)
	}
	cancelBuild()
	lease, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lease.Addr().String()
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + addr
	work := privateStorageDir(t)
	root := filepath.Join(work, "bytes")
	run := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env, cmd.Dir = aws.Env, work
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("feint %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	// Cleanup uses the recorded process identity, not a process-name kill.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "stop", "--addr", addr)
		cmd.Env = aws.Env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("stop the owned S3 witness: %v\n%s", err, out)
		}
	})
	run("start", "--addr", addr, "--vm", "off", "--objectstore", "bytes",
		"--objectstore-budget-mib", "3", "--s3-object-limit-mib", "1")
	var first statusReport
	if err := json.Unmarshal(run("status", "--addr", addr, "--format", "json"), &first); err != nil ||
		!first.Running || first.PID <= 0 || first.ObjectStorage == nil ||
		first.ObjectStorage.MaxBlobBytes != 3<<20 || first.ObjectStorage.MaxObjectBytes != 1<<20 {
		t.Fatalf("real process/configuration did not become ready: %+v %v", first, err)
	}
	if len(first.ObjectStorage.Doors) != 2 {
		t.Fatal("actual serve did not mount both declared doors")
	}
	for _, provider := range []string{"scaleway", "exoscale"} {
		out := run("env", provider, "--s3", "--endpoint", endpoint)
		found := false
		for _, door := range first.ObjectStorage.Doors {
			if door.Provider == provider && bytes.Contains(out, []byte(endpoint+door.Path)) {
				found = true
			}
		}
		if !found {
			t.Fatalf("actual env lost the configured %s S3 endpoint: %q", provider, out)
		}
	}
	payload := bytes.Repeat([]byte{0, 255, 'f', 'e', 'i', 'n', 't', '\r', '\n'}, 8192)
	key := "folder//a/../percent%2F?plus+#"
	bucket := "feint-mounted-bytes"
	input, output := filepath.Join(aws.Home, "input"), filepath.Join(aws.Home, "output")
	if err := os.WriteFile(input, payload, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	call := func(door s3.Door, args ...string) []byte {
		t.Helper()
		return aws.Run(t, append([]string{"--endpoint-url", endpoint + door.Path, "--region", door.Region,
			"--no-cli-pager", "s3api"}, args...)...)
	}
	get := func(door s3.Door, expected []byte) {
		t.Helper()
		result := call(door, "get-object", "--bucket", bucket, "--key", key, "--checksum-mode", "ENABLED", output)
		got, err := os.ReadFile(output)
		var meta struct{ ChecksumSHA256 string }
		hash := sha256.Sum256(expected)
		if err != nil || !bytes.Equal(got, expected) || json.Unmarshal(result, &meta) != nil ||
			meta.ChecksumSHA256 != base64.StdEncoding.EncodeToString(hash[:]) {
			t.Fatalf("official client did not return identical verified bytes: read=%v result=%q", err, result)
		}
	}
	for i, door := range first.ObjectStorage.Doors {
		call(door, "create-bucket", "--bucket", bucket)
		if i == 1 {
			if err := os.WriteFile(input, []byte("separate-sos-scope"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		call(door, "put-object", "--bucket", bucket, "--key", key, "--body", input,
			"--content-type", "application/octet-stream", "--checksum-algorithm", "CRC32")
	}
	get(first.ObjectStorage.Doors[0], payload)
	get(first.ObjectStorage.Doors[1], []byte("separate-sos-scope"))
	// Restart the initially relative-root invocation from a different cwd.
	work = privateStorageDir(t)
	run("restart", "--addr", addr)
	var restarted statusReport
	if err := json.Unmarshal(run("status", "--addr", addr, "--format", "json"), &restarted); err != nil ||
		!restarted.Running || restarted.PID == first.PID || restarted.ObjectStorage == nil ||
		restarted.ObjectStorage.MaxBlobBytes != 3<<20 || restarted.ObjectStorage.MaxObjectBytes != 1<<20 {
		t.Fatalf("restart lost the process or persisted configuration: %+v %v", restarted, err)
	}
	get(first.ObjectStorage.Doors[0], payload)
	get(first.ObjectStorage.Doors[1], []byte("separate-sos-scope"))
	// Inspect only our private store while the owner is stopped: GET of the
	// same cleaned key twice would otherwise masquerade as key preservation.
	run("stop", "--addr", addr)
	store, err := objectstore.OpenWithLimit(t.Context(), root, 3<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, door := range first.ObjectStorage.Doors {
		if _, err := store.HeadObject(t.Context(), door.Provider+"/"+door.Region+"/"+bucket, key); err != nil {
			_ = store.Close()
			t.Fatal("the real client key was cleaned or decoded twice:", err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	run("start", "--addr", addr, "--vm", "off", "--objectstore", root,
		"--objectstore-budget-mib", "3", "--s3-object-limit-mib", "1")
	for _, door := range first.ObjectStorage.Doors {
		call(door, "head-object", "--bucket", bucket, "--key", key)
		call(door, "delete-object", "--bucket", bucket, "--key", key)
		call(door, "delete-bucket", "--bucket", bucket)
	}
	if guardHits.Load() != 1 {
		t.Fatalf("official client reached the refusing egress proxy: delta=%d", guardHits.Load()-1)
	}
	t.Logf("actual feint pid %d -> %d; two same-port namespaces; preserved key; bytes=%d sha256=%s; stop/reopen/restart; proxy delta=0 (not a network namespace)",
		first.PID, restarted.PID, len(payload), hex.EncodeToString(sum[:]))
}
