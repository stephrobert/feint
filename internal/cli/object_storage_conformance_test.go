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
	"net/url"
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
	guard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		guardHits.Add(1)
		t.Logf("refusing proxy observed method=%s host=%s (never forwarded)", r.Method, r.Host)
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
	t.Cleanup(func() {
		if t.Failed() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "logs", "--addr", addr)
			cmd.Env = aws.Env
			out, err := cmd.CombinedOutput()
			t.Logf("owned witness logs (read error=%v): %s", err, out)
		}
	})
	run("start", "--addr", addr, "--vm", "off", "--objectstore", "bytes",
		"--objectstore-budget-mib", "20", "--s3-object-limit-mib", "8", "--s3-host-doors")
	var first statusReport
	if err := json.Unmarshal(run("status", "--addr", addr, "--format", "json"), &first); err != nil ||
		!first.Running || first.PID <= 0 || first.ObjectStorage == nil ||
		first.ObjectStorage.MaxBlobBytes != 20<<20 || first.ObjectStorage.MaxObjectBytes != 8<<20 {
		t.Fatalf("real process/configuration did not become ready: %+v %v", first, err)
	}
	if len(first.ObjectStorage.Doors) != 2 {
		t.Fatal("actual serve did not mount both declared doors")
	}
	localProxy, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	proxyClient := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(localProxy)}, Timeout: 5 * time.Second}
	defer proxyClient.CloseIdleConnections()
	control, err := http.NewRequestWithContext(t.Context(), "GET", "http://must-not-forward.invalid/_feint/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	refused, err := proxyClient.Do(control)
	if err != nil {
		t.Fatal("actual emulator proxy control failed:", err)
	}
	if err := refused.Body.Close(); err != nil || refused.StatusCode != http.StatusForbidden {
		t.Fatal("actual emulator did not refuse an undeclared proxy authority:", refused.StatusCode, err)
	}
	for _, provider := range []string{"scaleway", "exoscale"} {
		out := run("env", provider, "--s3", "--endpoint", endpoint)
		found := false
		for _, door := range first.ObjectStorage.Doors {
			target := "http://" + door.Host
			if provider == "scaleway" {
				target = endpoint + strings.TrimSuffix(door.Path, "/")
			}
			if door.Provider == provider && door.Host == s3.DoorHost(door.Provider, door.Region) &&
				bytes.Contains(out, []byte(target)) && bytes.Contains(out, []byte(endpoint)) {
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
	scw := conformancetest.SCW(t, aws, endpoint+strings.TrimSuffix(s3.DoorPath("scaleway", "fr-par"), "/"))
	bucketCall := func(verb string, name bool) []byte {
		t.Helper()
		args := []string{"-o", "json", "object", "bucket", verb}
		if name {
			args = append(args, bucket)
		}
		return scw.Run(t, append(args, "region=fr-par")...)
	}
	checkVersioning := func() {
		t.Helper()
		var configuration struct{ Status string }
		out := call(s3.Door{Provider: "scaleway", Region: "fr-par", Path: s3.DoorPath("scaleway", "fr-par")},
			"get-bucket-versioning", "--bucket", bucket)
		if err := json.Unmarshal(out, &configuration); err != nil || configuration.Status != "Suspended" {
			t.Fatalf("actual persisted versioning status was lost or invented: %v %q", err, out)
		}
	}
	getKey := func(door s3.Door, objectKey string, expected []byte) {
		t.Helper()
		result := call(door, "get-object", "--bucket", bucket, "--key", objectKey, "--checksum-mode", "ENABLED", output)
		got, err := os.ReadFile(output)
		var meta struct{ ChecksumSHA256 string }
		hash := sha256.Sum256(expected)
		if err != nil || !bytes.Equal(got, expected) || json.Unmarshal(result, &meta) != nil ||
			meta.ChecksumSHA256 != base64.StdEncoding.EncodeToString(hash[:]) {
			t.Fatalf("official client did not return identical verified bytes: read=%v result=%q", err, result)
		}
	}
	get := func(door s3.Door, expected []byte) { getKey(door, key, expected) }
	for i, door := range first.ObjectStorage.Doors {
		if door.Provider == "scaleway" {
			bucketCall("create", true)
		} else {
			call(door, "create-bucket", "--bucket", bucket)
		}
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
	// Use each client's exact exported environment, without evaluating shell text.
	var exoVars map[string]string
	hostSCW := scw
	hostSCW.Env = append([]string{}, scw.Env...)
	server, _, err := newServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range server.Packs() {
		if p.Name() == "scaleway" {
			pack, ok := p.(packEnv)
			if !ok {
				t.Fatal("Scaleway pack environment missing")
			}
			vars := pack.Env(endpoint).Vars
			if err := storageEnvironment(pack, endpoint, "fr-par", false, vars); err != nil {
				t.Fatal(err)
			}
			for key, value := range vars {
				hostSCW.Env = append(hostSCW.Env, key+"="+value)
			}
		}
		if p.Name() == "exoscale" {
			pack, ok := p.(packEnv)
			if !ok {
				t.Fatal("Exoscale pack environment missing")
			}
			exoVars = pack.Env(endpoint).Vars
			if err := storageEnvironment(pack, endpoint, "ch-gva-2", false, exoVars); err != nil {
				t.Fatal(err)
			}
		}
	}
	if exoVars == nil {
		t.Fatal("Exoscale pack missing")
	}
	hostSCW.Run(t, "-o", "json", "object", "bucket", "create", "feint-host-scw", "region=fr-par")
	hostSCW.Run(t, "-o", "json", "object", "bucket", "get", "feint-host-scw", "region=fr-par")
	if out := hostSCW.Run(t, "-o", "json", "object", "bucket", "list", "region=fr-par"); !bytes.Contains(out, []byte("feint-host-scw")) {
		t.Fatal("exported SCW inventory omitted its actual created bucket:", string(out))
	}
	hostSCW.Run(t, "-o", "json", "object", "bucket", "delete", "feint-host-scw", "region=fr-par")
	exo := conformancetest.EXO(t, aws, exoVars)
	if err := os.WriteFile(input, payload, 0600); err != nil {
		t.Fatal(err)
	}
	exoKey := "host-byte-proof"
	exo.Run(t, "storage", "upload", input, "sos://"+bucket+"/"+exoKey)
	getKey(first.ObjectStorage.Doors[1], exoKey, payload)
	largePayload := bytes.Repeat(payload, 86)
	largeInput := filepath.Join(aws.Home, "large-input")
	if err := os.WriteFile(largeInput, largePayload, 0600); err != nil {
		t.Fatal(err)
	}
	largeKey := "large-byte-proof"
	// Seed a simple PUT, not an unsupported multipart upload. The Exo downloader
	// must then read more than its 5 MiB part size through multiple byte ranges.
	call(first.ObjectStorage.Doors[1], "put-object", "--bucket", bucket, "--key", largeKey,
		"--body", largeInput, "--checksum-algorithm", "CRC32")
	download := func(objectKey string, expected []byte) {
		t.Helper()
		destination := filepath.Join(exo.Home, "download-"+objectKey)
		out := exo.Run(t, "storage", "download", "--force", "sos://"+bucket+"/"+objectKey, destination)
		got, err := os.ReadFile(destination)
		if err != nil || !bytes.Equal(got, expected) || sha256.Sum256(got) != sha256.Sum256(expected) {
			t.Fatalf("Exo's exit code is not byte proof: read=%v bytes=%d want=%d output=%q", err, len(got), len(expected), out)
		}
	}
	download(exoKey, payload)
	download(largeKey, largePayload)
	var page struct {
		IsTruncated           bool
		NextContinuationToken string
		KeyCount              int
		Contents              []struct {
			Key, LastModified string
			Size              int64
		}
	}
	firstPage := call(first.ObjectStorage.Doors[1], "list-objects-v2", "--bucket", bucket,
		"--max-keys", "1", "--no-paginate")
	if err := json.Unmarshal(firstPage, &page); err != nil || !page.IsTruncated ||
		page.KeyCount != 1 || len(page.Contents) != 1 || page.Contents[0].Key != key || page.NextContinuationToken == "" {
		t.Fatalf("real AWS decoder did not receive the literal paginated key: %v %q", err, firstPage)
	}
	secondPage := call(first.ObjectStorage.Doors[1], "list-objects-v2", "--bucket", bucket,
		"--max-keys", "1", "--no-paginate", "--continuation-token", page.NextContinuationToken)
	if err := json.Unmarshal(secondPage, &page); err != nil || page.KeyCount != 1 ||
		len(page.Contents) != 1 || page.Contents[0].Key != exoKey || page.Contents[0].Size != int64(len(payload)) {
		t.Fatalf("real AWS pagination repeated or invented an entry: %v %q", err, secondPage)
	}
	beforeObjects := exo.Run(t, "--output-format", "json", "storage", "list", "sos://"+bucket+"/")
	var listed []struct {
		Name         string `json:"name"`
		Size         int64  `json:"size"`
		LastModified string `json:"last_modified"`
	}
	if err := json.Unmarshal(beforeObjects, &listed); err != nil {
		t.Fatal("decode real Exo listing:", err, string(beforeObjects))
	}
	foundLarge := false
	for _, obj := range listed {
		if obj.Name == largeKey && obj.Size == int64(len(largePayload)) && obj.LastModified != "" {
			foundLarge = true
		}
	}
	if !foundLarge {
		t.Fatal("actual Exo listing omitted size/date of the uploaded object:", string(beforeObjects))
	}
	// A same-name object in the other provider is a positive isolation control.
	if err := os.WriteFile(input, []byte("other-provider-host-key"), 0600); err != nil {
		t.Fatal(err)
	}
	call(first.ObjectStorage.Doors[0], "put-object", "--bucket", bucket, "--key", exoKey, "--body", input,
		"--checksum-algorithm", "CRC32")
	getKey(first.ObjectStorage.Doors[0], exoKey, []byte("other-provider-host-key"))
	beforeBucket := bucketCall("get", true)
	checkVersioning()
	beforeList := bucketCall("list", false)
	if !bytes.Contains(beforeList, []byte(bucket)) {
		t.Fatal("Scaleway CLI list omitted its created bucket:", string(beforeList))
	}
	// Restart the initially relative-root invocation from a different cwd.
	work = privateStorageDir(t)
	run("restart", "--addr", addr)
	var restarted statusReport
	if err := json.Unmarshal(run("status", "--addr", addr, "--format", "json"), &restarted); err != nil ||
		!restarted.Running || restarted.PID == first.PID || restarted.ObjectStorage == nil ||
		restarted.ObjectStorage.MaxBlobBytes != 20<<20 || restarted.ObjectStorage.MaxObjectBytes != 8<<20 {
		t.Fatalf("restart lost the process or persisted configuration: %+v %v", restarted, err)
	}
	get(first.ObjectStorage.Doors[0], payload)
	get(first.ObjectStorage.Doors[1], []byte("separate-sos-scope"))
	getKey(first.ObjectStorage.Doors[1], exoKey, payload)
	getKey(first.ObjectStorage.Doors[0], exoKey, []byte("other-provider-host-key"))
	if restarted.ObjectStorage.Doors[1].Host != first.ObjectStorage.Doors[1].Host ||
		restarted.ObjectStorage.HTTPRequests["http://"+first.ObjectStorage.Doors[1].Host+" 200"] != 0 {
		t.Fatal("restart lost host configuration or invented persisted HTTP evidence")
	}
	if after := exo.Run(t, "--output-format", "json", "storage", "list", "sos://"+bucket+"/"); !bytes.Equal(after, beforeObjects) {
		t.Fatalf("actual Exo object inventory changed across restart: before=%q after=%q", beforeObjects, after)
	}
	exo.Run(t, "storage", "upload", input, "sos://"+bucket+"/after-restart")
	getKey(first.ObjectStorage.Doors[1], "after-restart", []byte("other-provider-host-key"))
	var live statusReport
	if err := json.Unmarshal(run("status", "--addr", addr, "--format", "json"), &live); err != nil ||
		live.ObjectStorage == nil || live.ObjectStorage.HTTPRequests["http://"+first.ObjectStorage.Doors[1].Host+" 200"] != 4 {
		t.Fatalf("actual Exo HEAD/PUT after restart did not traverse its declared host: %+v %v", live.ObjectStorage, err)
	}
	download(exoKey, payload)
	download(largeKey, largePayload)
	if err := json.Unmarshal(run("status", "--addr", addr, "--format", "json"), &live); err != nil ||
		live.ObjectStorage.HTTPRequests["http://"+first.ObjectStorage.Doors[1].Host+" 206"] < 3 {
		t.Fatal("actual Exo small/multi-part downloads did not produce partial HTTP responses:", live.ObjectStorage, err)
	}
	run("doctor", "--addr", addr, "--s3", "--vm", "off")
	if after := bucketCall("get", true); !bytes.Equal(after, beforeBucket) {
		t.Fatalf("Scaleway bucket configuration changed across restart: before=%q after=%q", beforeBucket, after)
	}
	if after := hostSCW.Run(t, "-o", "json", "object", "bucket", "get", bucket, "region=fr-par"); !bytes.Equal(after, beforeBucket) {
		t.Fatalf("exported SCW read diverged after restart: before=%q after=%q", beforeBucket, after)
	}
	checkVersioning()
	if after := bucketCall("list", false); !bytes.Equal(after, beforeList) {
		t.Fatalf("Scaleway bucket identity/creation time changed across restart: before=%q after=%q", beforeList, after)
	}
	// Inspect only our private store while the owner is stopped: GET of the
	// same cleaned key twice would otherwise masquerade as key preservation.
	run("stop", "--addr", addr)
	store, err := objectstore.OpenWithLimit(t.Context(), root, 20<<20)
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
		"--objectstore-budget-mib", "20", "--s3-object-limit-mib", "8", "--s3-host-doors")
	checkInventory := func(want int) {
		t.Helper()
		var inventory struct {
			Name     string
			KeyCount *int
			Contents []json.RawMessage
		}
		out := call(first.ObjectStorage.Doors[1], "list-objects-v2", "--bucket", bucket, "--no-paginate")
		if err := json.Unmarshal(out, &inventory); err != nil || inventory.Name != bucket ||
			inventory.KeyCount == nil || *inventory.KeyCount != want || len(inventory.Contents) != want {
			t.Fatalf("independent inventory did not witness %d actual objects: %v %q", want, err, out)
		}
	}
	checkInventory(4)
	deleted := exo.Run(t, "storage", "delete", "--force", "--recursive", "--verbose", "sos://"+bucket+"/")
	for _, objectKey := range []string{key, exoKey, largeKey, "after-restart"} {
		if !bytes.Contains(deleted, []byte(objectKey+"\n")) {
			t.Fatalf("Exo did not report the actual deleted key %q: %q", objectKey, deleted)
		}
	}
	var absentDeletion struct {
		Deleted []struct{ Key string }
		Errors  []json.RawMessage
	}
	absent := call(first.ObjectStorage.Doors[1], "delete-objects", "--bucket", bucket,
		"--delete", `{"Objects":[{"Key":"absent-witness"}]}`, "--checksum-algorithm", "CRC32")
	if err := json.Unmarshal(absent, &absentDeletion); err != nil || len(absentDeletion.Errors) != 0 ||
		len(absentDeletion.Deleted) != 1 || absentDeletion.Deleted[0].Key != "absent-witness" {
		t.Fatalf("actual AWS decoder lost idempotent bulk deletion: %v %q", err, absent)
	}
	checkEmpty := func() {
		t.Helper()
		checkInventory(0)
		getKey(first.ObjectStorage.Doors[0], exoKey, []byte("other-provider-host-key"))
	}
	checkEmpty()
	call(first.ObjectStorage.Doors[1], "put-object", "--bucket", bucket, "--key", "quiet-witness",
		"--body", input, "--checksum-algorithm", "CRC32")
	checkInventory(1)
	quiet := call(first.ObjectStorage.Doors[1], "delete-objects", "--bucket", bucket,
		"--delete", `{"Objects":[{"Key":"quiet-witness"}],"Quiet":true}`, "--checksum-algorithm", "CRC32")
	if len(bytes.TrimSpace(quiet)) != 0 {
		var response map[string]json.RawMessage
		if err := json.Unmarshal(quiet, &response); err != nil || len(response) != 0 {
			t.Fatalf("actual AWS Quiet result advertised successful keys or errors: %v %q", err, quiet)
		}
	}
	checkEmpty()
	run("restart", "--addr", addr)
	checkEmpty()
	for _, door := range first.ObjectStorage.Doors {
		if door.Provider == "exoscale" {
			call(door, "delete-bucket", "--bucket", bucket)
			continue
		}
		call(door, "head-object", "--bucket", bucket, "--key", key)
		call(door, "delete-object", "--bucket", bucket, "--key", key)
		call(door, "delete-object", "--bucket", bucket, "--key", exoKey)
		bucketCall("delete", true)
	}
	if guardHits.Load() != 1 {
		t.Fatalf("official client reached the refusing egress proxy: delta=%d", guardHits.Load()-1)
	}
	t.Logf("Exo downloads before/after actual restart: small=%d bytes; multi-range=%d bytes sha256=%x",
		len(payload), len(largePayload), sha256.Sum256(largePayload))
	t.Log("Exo bulk-deleted four actual keys; independent AWS inventory empty across restart; other provider bytes retained")
	t.Logf("actual feint pid %d -> %d; two same-port namespaces; preserved key; bytes=%d sha256=%s; stop/reopen/restart; proxy delta=0 (not a network namespace)",
		first.PID, restarted.PID, len(payload), hex.EncodeToString(sum[:]))
}
