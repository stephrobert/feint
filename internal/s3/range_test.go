package s3

import (
	"bytes"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestSingleByteRangesReturnOnlyTheirVerifiedBytes(t *testing.T) {
	s, store := fixture(t)
	if err := store.CreateBucket(t.Context(), "bucket"); err != nil {
		t.Fatal(err)
	}
	payload := []byte{0, 255, 13, 10, 1, 128}
	if w := request(t, s, "PUT", "/bucket/key", payload, nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	for _, tc := range []struct {
		value, contentRange string
		start, end          int
	}{
		{"bytes=1-3", "bytes 1-3/6", 1, 4},
		{"bytes=4-", "bytes 4-5/6", 4, 6},
		{"bytes=-2", "bytes 4-5/6", 4, 6},
		{"bytes=0-99", "bytes 0-5/6", 0, 6},
		{"bytes=-99", "bytes 0-5/6", 0, 6},
	} {
		w := request(t, s, "GET", "/bucket/key", nil, map[string]string{"Range": tc.value, "X-Amz-Checksum-Mode": "ENABLED"})
		if w.Code != 206 || !bytes.Equal(w.Body.Bytes(), payload[tc.start:tc.end]) ||
			w.Header().Get("Content-Range") != tc.contentRange || w.Header().Get("X-Amz-Checksum-Sha256") != "" ||
			w.Header().Get("Content-Length") != strconv.Itoa(tc.end-tc.start) || w.Header().Get("Last-Modified") == "" {
			t.Fatalf("range %s: status=%d body=%v headers=%v", tc.value, w.Code, w.Body.Bytes(), w.Header())
		}
	}
	if w := request(t, s, "GET", "/bucket/key", nil, nil); w.Code != 200 ||
		!bytes.Equal(w.Body.Bytes(), payload) || w.Header().Get("X-Amz-Checksum-Sha256") == "" {
		t.Fatal("range support weakened the full-object checksum proof:", w.Code, w.Header())
	}
}

func TestRangeRefusalsAreExplicitAndDoNotMasqueradeAsFullReads(t *testing.T) {
	s, store := fixture(t)
	if err := store.CreateBucket(t.Context(), "bucket"); err != nil {
		t.Fatal(err)
	}
	request(t, s, "PUT", "/bucket/key", []byte("123456"), nil)
	request(t, s, "PUT", "/bucket/empty", nil, nil)
	for _, tc := range []struct {
		key, value string
		status     int
	}{
		{"key", "bytes=6-", 416},
		{"empty", "bytes=0-1", 416},
		{"key", "bytes=-0", 416},
		{"key", "bytes=3-2", 400},
		{"key", "bytes=+1-2", 400},
		{"key", "bytes=1", 400},
		{"key", "bytes=999999999999999999999-", 400},
		{"key", "items=0-1", 501},
		{"key", "bytes=0-1,3-4", 501},
	} {
		w := request(t, s, "GET", "/bucket/"+tc.key, nil, map[string]string{"Range": tc.value})
		if w.Code != tc.status || w.Header().Get("X-Amz-Checksum-Sha256") != "" {
			t.Fatalf("range refusal %s %s: status=%d want=%d headers=%v", tc.key, tc.value, w.Code, tc.status, w.Header())
		}
		if tc.status == 416 {
			size := "6"
			if tc.key == "empty" {
				size = "0"
			}
			if w.Header().Get("Content-Range") != "bytes */"+size {
				t.Fatal("unsatisfiable range did not state its actual size")
			}
		}
	}
	for _, method := range []string{"HEAD", "PUT", "DELETE"} {
		if w := request(t, s, method, "/bucket/key", nil, map[string]string{"Range": "bytes=0-1"}); w.Code != 501 {
			t.Fatal("Range leaked into another operation:", method, w.Code)
		}
	}
	r := httptest.NewRequest("GET", "http://localhost/bucket/key", nil)
	r.RemoteAddr = "127.0.0.1:1"
	r.Header["Range"] = []string{"bytes=0-1", "bytes=2-3"}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("ambiguous Range headers were accepted:", w.Code)
	}
}

func FuzzByteRangeIsBounded(f *testing.F) {
	for _, value := range []string{"bytes=0-1", "bytes=-3", "bytes=8-", "bytes=0-9999999999999999999", "items=0-1", "bytes=2-1"} {
		f.Add(value, int64(9))
	}
	f.Fuzz(func(t *testing.T, value string, size int64) {
		if size < 0 {
			return
		}
		start, length, err := byteRange(value, size)
		if err == nil && (start < 0 || length <= 0 || start >= size || length > size-start) {
			t.Fatalf("unbounded range: %q size=%d start=%d length=%d", value, size, start, length)
		}
	})
}
