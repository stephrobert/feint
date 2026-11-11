package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/objectstore"
)

func fixture(t *testing.T) (*Server, *objectstore.Store) {
	t.Helper()
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := objectstore.OpenWithLimit(t.Context(), filepath.Join(parent, "objects"), 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	server, err := New(store, Config{Region: "fr-par", MaxObjectBytes: 1 << 20,
		MissingBucket: ExoscaleMissingBucket, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return server, store
}

func request(t *testing.T, s *Server, method, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://127.0.0.1:4599"+path, bytes.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1234"
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestRoundTripPreservesLogicalKeysAndBytes(t *testing.T) {
	s, store := fixture(t)
	if w := request(t, s, "PUT", "/bucket", nil, nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	payload := []byte{0, 1, 255, 13, 10, 128}
	for _, path := range []string{"/bucket/a/../b//c", "/bucket//root", "/bucket/%252F", "/bucket/a%2Fb", "/bucket/empty"} {
		body := payload
		if path == "/bucket/empty" {
			body = nil
		}
		w := request(t, s, "PUT", path, body, map[string]string{"Content-Type": "application/octet-stream", "X-Amz-Meta-Purpose": "byte-proof"})
		if w.Code != 200 {
			t.Fatal(path, w.Code, w.Body.String())
		}
		w = request(t, s, "GET", path, nil, nil)
		sum := sha256.Sum256(body)
		if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), body) || w.Header().Get("X-Amz-Checksum-Sha256") != base64.StdEncoding.EncodeToString(sum[:]) {
			t.Fatalf("GET %q: status=%d body=%q headers=%v", path, w.Code, w.Body.Bytes(), w.Header())
		}
		if w.Header().Get("ETag") != "" || w.Header().Get("X-Amz-Meta-Purpose") != "byte-proof" {
			t.Fatal("invented ETag or lost metadata")
		}
	}
	objects, err := store.ListObjects(t.Context(), "bucket")
	if err != nil || len(objects) != 5 {
		t.Fatal(objects, err)
	}
	for _, key := range []string{"a/../b//c", "/root", "%2F", "a/b", "empty"} {
		if _, ok := objects[key]; !ok {
			t.Fatal("key was cleaned or decoded twice", key)
		}
	}
	if w := request(t, s, "HEAD", "/bucket/a%2Fb", nil, nil); w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "6" {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
	for range 2 {
		if w := request(t, s, "DELETE", "/bucket/a%2Fb", nil, nil); w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestUploadChecksumsAndFramingBeforePublication(t *testing.T) {
	// The 53-byte CRC32 trailer request is the actual phase-0 Terraform
	// Scaleway wire payload, not a generated fixture matching the decoder.
	framed := []byte("c\r\nfeint-proof\n\r\n0\r\nx-amz-checksum-crc32:2ZZ3Xg==\r\n\r\n")
	base := map[string]string{
		"Content-Encoding": "aws-chunked", "X-Amz-Decoded-Content-Length": "12",
		"X-Amz-Trailer": "x-amz-checksum-crc32", "X-Amz-Content-Sha256": "STREAMING-UNSIGNED-PAYLOAD-TRAILER",
		"X-Amz-Sdk-Checksum-Algorithm": "CRC32",
	}
	tests := []struct {
		name string
		body []byte
		edit func(map[string]string)
		ok   bool
	}{
		{"recorded", framed, nil, true},
		{"checksum-mismatch", bytes.ReplaceAll(framed, []byte("2ZZ3Xg=="), []byte("AAAAAA==")), nil, false},
		{"short-decoded", framed, func(h map[string]string) { h["X-Amz-Decoded-Content-Length"] = "13" }, false},
		{"long-decoded", framed, func(h map[string]string) { h["X-Amz-Decoded-Content-Length"] = "11" }, false},
		{"missing-trailer", []byte("c\r\nfeint-proof\n\r\n0\r\n\r\n"), nil, false},
		{"extra-bytes", append(bytes.Clone(framed), []byte("junk\r\n\r\n")...), nil, false},
		{"truncated", framed[:len(framed)-2], nil, false},
		{"bare-lf-trailer", bytes.Replace(framed, []byte("2ZZ3Xg==\r\n\r\n"), []byte("2ZZ3Xg==\n\n"), 1), nil, false},
		{"undeclared-trailer", framed, func(h map[string]string) { delete(h, "X-Amz-Trailer"); delete(h, "X-Amz-Sdk-Checksum-Algorithm") }, false},
		{"unsupported-algorithm", framed, func(h map[string]string) { h["X-Amz-Sdk-Checksum-Algorithm"] = "CRC64NVME" }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, store := fixture(t)
			if err := store.CreateBucket(t.Context(), "bucket"); err != nil {
				t.Fatal(err)
			}
			headers := make(map[string]string)
			for k, v := range base {
				headers[k] = v
			}
			if tc.edit != nil {
				tc.edit(headers)
			}
			w := request(t, s, "PUT", "/bucket/key", tc.body, headers)
			obj, err := store.HeadObject(t.Context(), "bucket", "key")
			if tc.ok {
				if w.Code != 200 || err != nil || obj.Size != 12 || obj.Digest != "e56788086092c82c6af2ab449d9d26f920080381a55070fb1378fc7fedd04872" {
					t.Fatal(w.Code, w.Body.String(), obj, err)
				}
			} else if w.Code < 400 || !errors.Is(err, objectstore.ErrObjectNotFound) {
				t.Fatal("failed upload published metadata", w.Code, obj, err)
			}
		})
	}
}

func TestPlainUploadChecksumsAndBound(t *testing.T) {
	s, store := fixture(t)
	if err := store.CreateBucket(t.Context(), "bucket"); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("feint-proof\n"))
	for _, tc := range []struct {
		name, header, value string
		body                []byte
		ok                  bool
	}{
		{"crc-good", "X-Amz-Checksum-Crc32", "2ZZ3Xg==", []byte("feint-proof\n"), true},
		{"crc-bad", "X-Amz-Checksum-Crc32", "AAAAAA==", []byte("feint-proof\n"), false},
		{"sha-good", "X-Amz-Content-Sha256", hex.EncodeToString(sum[:]), []byte("feint-proof\n"), true},
		{"sha-bad", "X-Amz-Content-Sha256", strings.Repeat("0", 64), []byte("feint-proof\n"), false},
		{"oversized", "", "", bytes.Repeat([]byte("x"), (1<<20)+1), false},
	} {
		w := request(t, s, "PUT", "/bucket/"+tc.name, tc.body, map[string]string{tc.header: tc.value})
		_, err := store.HeadObject(t.Context(), "bucket", tc.name)
		if tc.ok {
			if w.Code != 200 || err != nil {
				t.Fatal(tc.name, w.Code, w.Body.String(), err)
			}
		} else if w.Code < 400 || !errors.Is(err, objectstore.ErrObjectNotFound) {
			t.Fatal(tc.name, "invalid upload published", w.Code, err)
		}
	}
}

func TestRefusalsDoNotMasqueradeAsCloudSupport(t *testing.T) {
	s, store := fixture(t)
	if err := store.CreateBucket(t.Context(), "bucket"); err != nil {
		t.Fatal(err)
	}
	if w := request(t, s, "PUT", "/bucket/key", []byte("x"), nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, tc := range []struct {
		method, path string
		headers      map[string]string
	}{
		{"GET", "/bucket?list-type=1", nil},
		{"GET", "/bucket/missing", nil},
		{"PUT", "/bucket", nil},
		{"PUT", "/bucket/key?uploads", nil},
		{"PUT", "/bucket/key?future-feature=", nil},
		{"PUT", "/bucket/key", map[string]string{"X-Amz-Acl": "public-read"}},
		{"GET", "/bucket/key", map[string]string{"Range": "items=0-1"}},
		{"PUT", "/bucket/key", map[string]string{"If-None-Match": "*"}},
	} {
		w := request(t, s, tc.method, tc.path, nil, tc.headers)
		if w.Code != 501 || !strings.HasPrefix(w.Body.String(), "feint: ") {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
	if w := request(t, s, "PUT", "/bucket/key", []byte("x"), nil); w.Code != 200 {
		t.Fatal("accepting half", w.Code, w.Body.String())
	}
	if w := request(t, s, "DELETE", "/bucket", nil, nil); w.Code != 501 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(t, s, "GET", "/bucket", nil, nil); w.Code != 501 {
		t.Fatal("bucket listing became a successful empty stub", w.Code, w.Body.String())
	}
	objects, err := store.ListObjects(t.Context(), "bucket")
	if err != nil || len(objects) != 1 {
		t.Fatal("refused request mutated storage", objects, err)
	}
}

func TestLoopbackAndPathStyleBoundary(t *testing.T) {
	s, _ := fixture(t)
	for _, tc := range []struct {
		peer, host string
		ok         bool
	}{
		{"127.0.0.1:1234", "127.0.0.1:4599", true},
		{"[::1]:1234", "[::1]:4599", true},
		{"127.0.0.1:1234", "localhost:4599", true},
		{"192.0.2.1:1234", "127.0.0.1:4599", false},
		{"", "127.0.0.1:4599", false},
		{"127.0.0.1:1234", "bucket.s3.fr-par.scw.cloud", false},
	} {
		r := httptest.NewRequest("GET", "http://127.0.0.1/missing", nil)
		r.RemoteAddr, r.Host = tc.peer, tc.host
		r.Header.Set("X-Forwarded-For", "127.0.0.1")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if tc.ok && w.Code != 404 || !tc.ok && w.Code != 403 && w.Code != 501 {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
}

func TestPersistedMetadataIsRevalidatedForHTTP(t *testing.T) {
	s, store := fixture(t)
	if err := store.CreateBucket(t.Context(), "bucket"); err != nil {
		t.Fatal(err)
	}
	for _, metadata := range []map[string]string{{"purpose": "valid"}, {"purpose": "x\r\nInjected: yes"}, {"bad key": "x"}, {"purpose": "x", "PURPOSE": "y"}} {
		_, err := store.PutObject(t.Context(), "bucket", "key", strings.NewReader("x"),
			objectstore.PutOptions{Size: 1, ContentType: "application/octet-stream", Metadata: metadata})
		if err != nil {
			t.Fatal(err)
		}

		w := request(t, s, "GET", "/bucket/key", nil, nil)
		if metadata["purpose"] == "valid" {
			if w.Code != 200 || w.Body.String() != "x" {
				t.Fatal(w.Code, w.Body.String())
			}
		} else if w.Code != 500 || w.Header().Get("X-Amz-Meta-Purpose") != "" {
			t.Fatal("untrusted persisted headers served", w.Code, w.Header())
		}
	}
}

func TestUploadHeaderValidationAndConstructor(t *testing.T) {
	s, store := fixture(t)
	if err := store.CreateBucket(t.Context(), "bucket"); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("x"))
	for _, headers := range []map[string]string{
		{"X-Amz-Meta-Purpose": "valid", "Content-Type": "application/octet-stream"},
		{"X-Amz-Meta-Purpose": "x\ninjected"},
		{"X-Amz-Meta-Bad Key": "x"},
		{"Content-Type": "x\r\ninjected"},
		{"X-Amz-Decoded-Content-Length": "12"},
		{"X-Amz-Checksum-Sha256": "not-base64"},
		{"X-Amz-Sdk-Checksum-Algorithm": "CRC32"},
		{"X-Amz-Content-Sha256": hex.EncodeToString(sum[:]), "X-Amz-Checksum-Sha256": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))},
	} {
		w := request(t, s, "PUT", "/bucket/key", []byte("x"), headers)
		valid := headers["X-Amz-Meta-Purpose"] == "valid"
		if valid && w.Code != 200 || !valid && w.Code < 400 {
			t.Fatal(headers, w.Code, w.Body.String())
		}
		if _, err := store.HeadObject(t.Context(), "bucket", "key"); err != nil {
			t.Fatal("invalid overwrite destroyed the previously committed object", err)
		}
	}
	for _, change := range []func(*Config){
		func(c *Config) { c.MaxObjectBytes = 0 },
		func(c *Config) { c.MaxObjectBytes = 1<<63 - 1 },
		func(c *Config) { c.Region = "x\r\nInjected" },
		func(c *Config) { c.MissingBucket = 0 },
		// A shape past the last measured one. The allow-list in New must
		// refuse it by NAME: a range check would admit the day somebody
		// adds a constant and forgets the envelope, and the provider would
		// ship with whatever the default branch happens to render.
		func(c *Config) { c.MissingBucket = OutscaleMissingBucket + 1 },
		func(c *Config) { c.Log = nil },
	} {
		cfg := s.cfg
		change(&cfg)
		if _, err := New(store, cfg); err == nil {
			t.Fatal("invalid constructor succeeded", cfg)
		}
	}
	if _, err := New(nil, s.cfg); err == nil {
		t.Fatal("nil store accepted")
	}
}

func TestMissingBucketFollowsMeasuredErrorDialects(t *testing.T) {
	s, _ := fixture(t)
	for _, shape := range []MissingBucketShape{
		ScalewayMissingBucket, ExoscaleMissingBucket, OutscaleMissingBucket,
	} {
		file := "exoscale-no-such-bucket.xml"
		switch shape {
		case ScalewayMissingBucket:
			file = "scaleway-no-such-bucket.xml"
		case OutscaleMissingBucket:
			file = "outscale-no-such-bucket.xml"
		}
		// Real anonymous HTTPS GET responses recorded 2026-11-06T20:33:53Z
		// at s3.fr-par.scw.cloud and sos-ch-gva-2.exo.io, and 2026-11-09 at
		// oos.eu-west-2.outscale.com. Response identifiers are redacted and a
		// trailing newline added; fields remain recorded.
		//
		// The Outscale one is why Resource and BucketName are pointers: it
		// carries <Resource></Resource> present-and-empty and NO BucketName,
		// which a string with `omitempty` cannot tell from Exoscale omitting
		// the element. This loop is what proves it, because it re-marshals the
		// recording and compares it to what the server emits.
		recording, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		var observed missingBucketError
		if err := xml.Unmarshal(recording, &observed); err != nil {
			t.Fatal("cannot decode real producer response", err)
		}
		// The recording must decode into the boundary type, and it must carry
		// the field its own dialect carries. Outscale's answer has no
		// BucketName at all, so demanding one of every dialect would have made
		// this check reject a faithful recording.
		switch {
		case observed.Code != "NoSuchBucket":
			t.Fatal("recording did not decode into the boundary type", observed)
		case shape == OutscaleMissingBucket:
			if observed.BucketName != nil {
				t.Fatal("the Outscale recording grew a BucketName", observed)
			}
			if observed.Resource == nil || *observed.Resource != "" {
				t.Fatal("the Outscale recording lost its present-and-empty Resource", observed)
			}
		case observed.BucketName == nil || *observed.BucketName == "":
			t.Fatal("recording did not decode into the boundary type", observed)
		}
		s.cfg.MissingBucket = shape
		w := request(t, s, "GET", "/missing", nil, nil)
		body := w.Body.String()
		var emitted missingBucketError
		if err := xml.Unmarshal(w.Body.Bytes(), &emitted); err != nil {
			t.Fatal(err)
		}
		observed.BucketName = emitted.BucketName
		switch shape {
		case ScalewayMissingBucket:
			resource := "/missing"
			observed.RequestID, observed.HostID, observed.Resource = emitted.RequestID, emitted.HostID, &resource
			if emitted.RequestID != emitted.HostID {
				t.Fatal("recorded Scaleway identifiers were equal", emitted)
			}
		case OutscaleMissingBucket:
			// Only the identifier is substituted. Resource is NOT, because
			// present-and-empty is the measured shape and copying the emitted
			// value would let the server define what it is compared against —
			// which is the one way this comparison could stop measuring.
			observed.RequestID = emitted.RequestID
			if emitted.Resource == nil || *emitted.Resource != "" {
				t.Fatal("the Outscale envelope lost its present-and-empty Resource", emitted)
			}
			if emitted.BucketName != nil {
				t.Fatal("the Outscale envelope grew a BucketName", emitted)
			}
		}
		want, err := xml.Marshal(observed)
		if err != nil {
			t.Fatal(err)
		}
		// Both dialects whose recording carries the declaration carry it here.
		if shape == ExoscaleMissingBucket || shape == OutscaleMissingBucket {
			want = append([]byte(strings.TrimSuffix(xml.Header, "\n")), want...)
		}
		if body != string(want) {
			t.Fatal("response differs from the actual producer envelope", body, string(want))
		}
		if w.Code != 404 || w.Header().Get("Content-Type") != "application/xml" ||
			!strings.Contains(body, "<Code>NoSuchBucket</Code>") {
			t.Fatal(w.Code, body, w.Header())
		}
		// The bucket name is asserted per dialect rather than of all three, and
		// the Outscale branch asserts its ABSENCE, which is stricter than the
		// blanket Contains this replaced: that one demanded
		// <BucketName>missing</BucketName> of every envelope, so it would have
		// rejected a faithful Outscale answer and, worse, would have passed a
		// wrong one that added the element.
		if shape == OutscaleMissingBucket {
			if strings.Contains(body, "<BucketName>") {
				t.Fatal("the Outscale envelope names a bucket its producer does not", body)
			}
			if !strings.Contains(body, "<Resource></Resource>") {
				t.Fatal("the Outscale envelope lost the empty Resource element", body)
			}
			// Measured: the producer answered the same value in both headers.
			if id2 := w.Header().Get("X-Amz-Id-2"); id2 == "" || id2 != w.Header().Get("X-Amz-Request-Id") {
				t.Fatal("Outscale answers one identifier in both headers", w.Header())
			}
			if w.Header().Get("X-Amzn-Request-Id") != "" {
				t.Fatal("the Outscale envelope grew Exoscale's extra identifier header", w.Header())
			}
		} else if !strings.Contains(body, "<BucketName>missing</BucketName>") {
			t.Fatal(w.Code, body, w.Header())
		}
		// One branch per dialect, each asserting what its own producer carries
		// AND what it does not. The last two used to share an `else` whose
		// shape was Exoscale's: no declaration prefix was allowed to be absent,
		// and <RequestId> had to be absent. Outscale carries both the prefix
		// and a RequestId, so that `else` rejected a faithful answer — and an
		// `else` that covered two dialects quietly claims to cover every one
		// that comes later.
		switch shape {
		case ScalewayMissingBucket:
			if strings.HasPrefix(body, "<?xml") || !strings.Contains(body, "<RequestId>") ||
				!strings.Contains(body, "<Resource>/missing</Resource>") ||
				!strings.Contains(body, "<Message>The specified bucket does not exist</Message>") {
				t.Fatal(body)
			}
		case ExoscaleMissingBucket:
			if !strings.HasPrefix(body, "<?xml") || strings.Contains(body, "<RequestId>") ||
				strings.Contains(body, "<Resource>") ||
				!strings.Contains(body, "<Message>The specified bucket does not exist.</Message>") {
				t.Fatal(body)
			}
		case OutscaleMissingBucket:
			if !strings.HasPrefix(body, "<?xml") || !strings.Contains(body, "<RequestId>") ||
				strings.Contains(body, "<HostId>") ||
				!strings.Contains(body, "<Message>The specified bucket does not exist.</Message>") {
				t.Fatal(body)
			}
		}
	}
}

func FuzzUploadFraming(f *testing.F) {
	f.Add([]byte("1\r\nx\r\n0\r\n\r\n"))
	f.Add([]byte("0\r\n\r\n"))
	f.Add([]byte("not a chunk"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4096 {
			return
		}
		r := httptest.NewRequest("PUT", "http://127.0.0.1/bucket/key", bytes.NewReader(data))
		r.Header.Set("Content-Encoding", "aws-chunked")
		r.Header.Set("X-Amz-Decoded-Content-Length", "1")
		u, _, err := newUpload(httptest.NewRecorder(), r, 4096)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, u)
	})
}

func TestCanceledUploadDoesNotPublish(t *testing.T) {
	s, store := fixture(t)
	if err := store.CreateBucket(t.Context(), "bucket"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r := httptest.NewRequest("PUT", "http://127.0.0.1/bucket/key", strings.NewReader("x")).WithContext(ctx)
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	_, err := store.HeadObject(t.Context(), "bucket", "key")
	if w.Code < 400 || !errors.Is(err, objectstore.ErrObjectNotFound) {
		t.Fatal(w.Code, err)
	}
}

func TestAmbiguousUploadHeadersDoNotPublish(t *testing.T) {
	s, store := fixture(t)
	if err := store.CreateBucket(t.Context(), "bucket"); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("x"))
	for _, duplicate := range []bool{false, true} {
		r := httptest.NewRequest("PUT", "http://127.0.0.1/bucket/key", strings.NewReader("x"))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("X-Amz-Content-Sha256", hex.EncodeToString(sum[:]))
		if duplicate {
			r.Header.Add("X-Amz-Content-Sha256", strings.Repeat("0", 64))
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if !duplicate && w.Code != 200 || duplicate && w.Code != 400 {
			t.Fatal("ambiguous headers were silently ignored", duplicate, w.Code, w.Body.String())
		}
	}

}

func TestQuotaRefusalIsExplicitAndPreservesExistingObject(t *testing.T) {
	s, _ := fixture(t)
	if w := request(t, s, "PUT", "/bucket", nil, nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	payload := bytes.Repeat([]byte("x"), 1<<20)
	if w := request(t, s, "PUT", "/bucket/key", payload, nil); w.Code != 200 {
		t.Fatal("exact quota threshold must work", w.Code, w.Body.String())
	}
	if w := request(t, s, "PUT", "/bucket/second", bytes.Repeat([]byte("y"), 1<<20), nil); w.Code != 200 {
		t.Fatal("remaining quota must work", w.Code, w.Body.String())
	}
	w := request(t, s, "PUT", "/bucket/key", []byte("z"), nil)
	if w.Code != 507 || !strings.HasPrefix(w.Body.String(), "feint: object storage blob byte budget exceeded") {
		t.Fatal("quota became an invented cloud response or success", w.Code, w.Body.String())
	}
	w = request(t, s, "GET", "/bucket/key", nil, nil)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), payload) {
		t.Fatal("quota refusal damaged existing bytes", w.Code)
	}
}
