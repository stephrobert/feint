package s3

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/objectstore"
)

func mountedFixture(t *testing.T) (*Doors, *objectstore.Store) {
	t.Helper()
	_, store := fixture(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	control := http.NewServeMux()
	control.HandleFunc("GET /native", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("control")) })
	// A positive fallback witness makes a reserved-path leak observable even
	// when the real control plane would happen to answer the same HTTP 404.
	control.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("control-fallback")) })
	doors, err := Mount(control, store, []Profile{
		{Provider: "scaleway", Region: "fr-par", MissingBucket: ScalewayMissingBucket},
		{Provider: "scaleway", Region: "nl-ams", MissingBucket: ScalewayMissingBucket},
		{Provider: "exoscale", Region: "ch-gva-2", MissingBucket: ExoscaleMissingBucket},
	}, 512, log)
	if err != nil {
		t.Fatal(err)
	}
	return doors, store
}

func doorRequest(t *testing.T, client *http.Client, method, target, body string, status int) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(res.Body)
	closeErr := res.Body.Close()
	if err != nil || closeErr != nil || res.StatusCode != status || res.Header.Get("Location") != "" {
		t.Fatalf("%s %s: status=%d want=%d read=%v close=%v location=%q body=%q",
			method, target, res.StatusCode, status, err, closeErr, res.Header.Get("Location"), raw)
	}
	return raw
}

func TestMountedDoorsPreserveRawKeysAndIsolateScopes(t *testing.T) {
	doors, store := mountedFixture(t)
	ts := httptest.NewServer(emulator.GuardRebinding(doors, "127.0.0.1:4599"))
	defer ts.Close()
	client := ts.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	wireKey := "/a/../%2F%252F%00%3F%23"
	logicalKey := "/a/..//%2F\x00?#"
	for i, door := range doors.Snapshot().Doors {
		prefix := ts.URL + door.Path
		doorRequest(t, client, "PUT", prefix+"bucket", "", 200)
		payload := strings.Repeat(string(rune('a'+i)), i+1)
		doorRequest(t, client, "PUT", prefix+"bucket/"+wireKey, payload, 200)
		got := doorRequest(t, client, "GET", prefix+"bucket/"+wireKey, "", 200)
		if string(got) != payload {
			t.Fatalf("scope %s returned %q, want %q", door.Path, got, payload)
		}
		obj, err := store.HeadObject(t.Context(), door.Provider+"/"+door.Region+"/bucket", logicalKey)
		if err != nil || obj.Size != int64(len(payload)) {
			t.Fatalf("key was cleaned, decoded twice or stored in the wrong scope: %+v %v", obj, err)
		}
	}
	for i, door := range doors.Snapshot().Doors {
		got := doorRequest(t, client, "GET", ts.URL+door.Path+"bucket/"+wireKey, "", 200)
		if string(got) != strings.Repeat(string(rune('a'+i)), i+1) {
			t.Fatalf("later write contaminated %s: %q", door.Path, got)
		}
	}
	report := doors.Snapshot()
	if !report.Enabled || report.MaxBlobBytes != store.BlobLimit() || report.MaxObjectBytes != 512 || len(report.HTTPRequests) == 0 {
		t.Fatalf("incorrect live configuration/evidence: %+v", report)
	}
	report.Doors[0].Path = "/wrong"
	clear(report.HTTPRequests)
	if doors.Snapshot().Doors[0].Path == "/wrong" || len(doors.Snapshot().HTTPRequests) == 0 {
		t.Fatal("snapshot aliases live configuration/evidence")
	}
	if got := doorRequest(t, client, "GET", ts.URL+"/native", "", 200); string(got) != "control" {
		t.Fatal("mount displaced the control plane")
	}
}

func TestMountedDoorRefusesUnknownAndEncodedPrefixesWithoutRedirect(t *testing.T) {
	doors, _ := mountedFixture(t)
	ts := httptest.NewServer(doors)
	defer ts.Close()
	for _, path := range []string{
		"/_feint/s3/outscale/eu-west-2/bucket/key",
		"/_feint/s3/scaleway/not-declared/bucket/key",
		"/_feint/s3/scaleway/fr-par",
		"/_feint/s3/scaleway/../fr-par/bucket/key",
		"/_feint/s3%2Fscaleway/fr-par/bucket/key",
		"/_feint/s3/scaleway%2Ffr-par/bucket/key",
	} {
		doorRequest(t, ts.Client(), "GET", ts.URL+path, "", 404)
	}
	for _, door := range doors.Snapshot().Doors {
		body := doorRequest(t, ts.Client(), "GET", ts.URL+door.Path+"missing-bucket/key", "", 404)
		if !bytes.Contains(body, []byte("<BucketName>missing-bucket</BucketName>")) ||
			bytes.Contains(body, []byte(door.Provider+"/"+door.Region)) {
			t.Fatalf("private namespace leaked into missing-bucket dialect: %q", body)
		}
		doorRequest(t, ts.Client(), "GET", ts.URL+doors.Snapshot().Doors[0].Path+"bucket%2Fother/key", "", 400)
	}
}

func TestMountedBucketListingAcceptsOnlyItsCanonicalBareEndpoint(t *testing.T) {
	doors, _ := mountedFixture(t)
	prefix := DoorPath("scaleway", "fr-par")
	doors.handlers[prefix].cfg.BucketConfiguration = true
	ts := httptest.NewServer(doors)
	defer ts.Close()
	doorRequest(t, ts.Client(), "PUT", ts.URL+prefix+"bucket", "", 200)
	bare := strings.TrimSuffix(prefix, "/")
	for _, path := range []string{prefix, bare, prefix + "?x-id=ListBuckets", bare + "?x-id=ListBuckets"} {
		body := doorRequest(t, ts.Client(), "GET", ts.URL+path, "", 200)
		if !bytes.Contains(body, []byte("<Name>bucket</Name>")) {
			t.Fatal("canonical bucket root omitted actual inventory:", string(body))
		}
	}
	for _, path := range []string{
		strings.Replace(bare, "/scaleway/", "/scaleway%2F", 1),
		bare + "-foreign",
	} {
		doorRequest(t, ts.Client(), "GET", ts.URL+path, "", 404)
	}
	doorRequest(t, ts.Client(), "PUT", ts.URL+bare, "", 404)
}

type deadlineWriter struct {
	*httptest.ResponseRecorder
	deadline time.Time
}

func (w *deadlineWriter) SetReadDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func TestMountedPutRequiresAndInstallsBodyReadDeadline(t *testing.T) {
	doors, _ := mountedFixture(t)
	request := func() *http.Request {
		req := httptest.NewRequest("PUT", "http://localhost/_feint/s3/scaleway/fr-par/bucket", nil)
		req.RemoteAddr = "127.0.0.1:1234"
		return req
	}
	unsupported := httptest.NewRecorder()
	doors.ServeHTTP(unsupported, request())
	if unsupported.Code != 503 {
		t.Fatal("a PUT without deadline support was accepted:", unsupported.Code)
	}
	writer := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	before := time.Now()
	doors.ServeHTTP(writer, request())
	if writer.Code != 200 || writer.deadline.Before(before.Add(UploadReadTimeout)) ||
		writer.deadline.After(time.Now().Add(UploadReadTimeout)) {
		t.Fatalf("valid PUT did not install the bounded read deadline: %d %v", writer.Code, writer.deadline)
	}
}

func TestMountedS3CannotDisarmItsOriginGuard(t *testing.T) {
	doors, _ := mountedFixture(t)
	// Deliberately no outer guard: this also covers an exposed control plane.
	ts := httptest.NewServer(doors)
	defer ts.Close()
	req, err := http.NewRequestWithContext(t.Context(), "PUT", ts.URL+doors.Snapshot().Doors[0].Path+"bucket", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://hostile.example")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 403 {
		t.Fatal("exposing the control plane admitted a browser S3 mutation:", res.StatusCode)
	}
	doorRequest(t, ts.Client(), "PUT", req.URL.String(), "", 200)
}

func TestMountRequiresActualQuotaAndExplicitActivation(t *testing.T) {
	doors, store := mountedFixture(t)
	_ = doors
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	next := http.NotFoundHandler()
	profile := Profile{Provider: "scaleway", Region: "fr-par", MissingBucket: ScalewayMissingBucket}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	unlimited, err := objectstore.Open(t.Context(), filepath.Join(parent, "unlimited"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unlimited.Close(); err != nil {
			t.Error(err)
		}
	}()
	for _, tc := range []struct {
		store    *objectstore.Store
		profiles []Profile
		limit    int64
	}{
		{nil, []Profile{profile}, 512}, {store, nil, 512}, {unlimited, []Profile{profile}, 512},
		{store, []Profile{profile}, store.BlobLimit() + 1}, {store, []Profile{profile}, 0},
		{store, []Profile{profile, profile}, 512},
		{store, []Profile{{Provider: "scaleway", Region: "../region", MissingBucket: ScalewayMissingBucket}}, 512},
	} {
		if _, err := Mount(next, tc.store, tc.profiles, tc.limit, log); err == nil {
			t.Fatalf("invalid activation/configuration accepted: %+v", tc)
		}
	}
	disabled, err := Mount(next, nil, nil, 0, log)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Snapshot().Enabled || disabled.Snapshot().MaxBlobBytes != 0 {
		t.Fatal("disabled storage advertised an active budget")
	}
	if err := store.HeadBucket(t.Context(), "bucket"); !errors.Is(err, objectstore.ErrBucketNotFound) {
		t.Fatal("construction unexpectedly changed storage:", err)
	}
}

func TestAnInterruptedDoorDoesNotInventResponseEvidence(t *testing.T) {
	doors, _ := mountedFixture(t)
	prefix := doors.Snapshot().Doors[0].Path
	// Force an interrupted handler before it writes any response header.
	doors.handlers[prefix] = nil
	req := httptest.NewRequest("GET", "http://localhost"+prefix+"bucket/key", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the interruption control did not interrupt its handler")
			}
		}()
		doors.ServeHTTP(httptest.NewRecorder(), req)
	}()
	if len(doors.Snapshot().HTTPRequests) != 0 {
		t.Fatal("a handler without response headers acquired completed-response evidence")
	}
}
