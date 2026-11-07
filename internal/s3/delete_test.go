package s3

import (
	"crypto/md5" //nolint:gosec // Exercise the official client's legacy transport checksum.
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"hash/crc32"
	"net/http/httptest"
	"strings"
	"testing"
)

func deleteMD5(body []byte) string {
	sum := md5.Sum(body) //nolint:gosec // Transport checksum only.
	return base64.StdEncoding.EncodeToString(sum[:])
}

func TestDeleteObjectsReturnsActualLiteralKeysAndQuietState(t *testing.T) {
	for _, mode := range []string{"false", "true"} {
		t.Run(mode, func(t *testing.T) {
			quiet := mode == "true"
			s, store := fixture(t)
			if err := store.CreateBucket(t.Context(), "bucket"); err != nil {
				t.Fatal(err)
			}
			if w := request(t, s, "PUT", "/bucket/a%2F..%2F%252F%3F%26%23", []byte("bytes"), nil); w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			body := []byte(`<Delete xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Object><Key>a/../%2F?&amp;#</Key></Object><Object><Key>missing</Key></Object><Quiet>` +
				mode + `</Quiet></Delete>`)
			w := request(t, s, "POST", "/bucket?delete=&x-id=DeleteObjects", body, map[string]string{"Content-MD5": deleteMD5(body)})
			var result deleteResult
			if w.Code != 200 || xml.Unmarshal(w.Body.Bytes(), &result) != nil {
				t.Fatal("invalid DeleteResult:", w.Code, w.Body.String())
			}
			if (!quiet && (len(result.Deleted) != 2 || result.Deleted[0].Key != "a/../%2F?&#" || result.Deleted[1].Key != "missing")) ||
				(quiet && len(result.Deleted) != 0) {
				t.Fatal("literal keys or Quiet lost:", w.Body.String())
			}
			inventory, err := store.ListObjects(t.Context(), "bucket")
			if err != nil || len(inventory) != 0 {
				t.Fatal("successful response did not delete actual objects:", inventory, err)
			}
		})
	}
}

func TestDeleteObjectsAcceptsExactly1000Keys(t *testing.T) {
	body := `<Delete>` + strings.Repeat(`<Object><Key>key</Key></Object>`, 1000) + `</Delete>`
	request, err := parseDelete([]byte(body))
	if err != nil || len(request.keys) != 1000 {
		t.Fatal("the documented key bound was not accepted:", len(request.keys), err)
	}
}

func TestDeleteObjectsClosedRefusalShapes(t *testing.T) {
	for _, body := range []string{
		`<Delete/>`,
		`<Delete><Object><Key>` + strings.Repeat("x", 1025) + `</Key></Object></Delete>`,
	} {
		if _, err := parseDelete([]byte(body)); err == nil {
			t.Fatal("the wire parser accepted an empty or oversized request")
		}
	}
}

func TestDeleteObjectsRefusalsCannotDeleteValidatedPrefix(t *testing.T) {
	valid := `<Delete><Object><Key>keep</Key></Object></Delete>`
	for _, tc := range []struct{ name, body, query string }{
		{"version", `<Delete><Object><Key>keep</Key></Object><Object><Key>other</Key><VersionId>version</VersionId></Object></Delete>`, "?delete"},
		{"unknown", `<Delete><Object><Key>keep</Key></Object><Unknown/></Delete>`, "?delete"},
		{"duplicate-key", `<Delete><Object><Key>keep</Key><Key>other</Key></Object></Delete>`, "?delete"},
		{"empty-key", `<Delete><Object><Key>keep</Key></Object><Object><Key/></Object></Delete>`, "?delete"},
		{"oversize-key", `<Delete><Object><Key>keep</Key></Object><Object><Key>` + strings.Repeat("x", 1025) + `</Key></Object></Delete>`, "?delete"},
		{"over-1000", `<Delete>` + strings.Repeat(`<Object><Key>keep</Key></Object>`, 1001) + `</Delete>`, "?delete"},
		{"quiet-value", `<Delete><Object><Key>keep</Key></Object><Quiet>maybe</Quiet></Delete>`, "?delete"},
		{"quiet-duplicate", `<Delete><Object><Key>keep</Key></Object><Quiet>true</Quiet><Quiet>false</Quiet></Delete>`, "?delete"},
		{"namespace", `<Delete xmlns="urn:other"><Object><Key>keep</Key></Object></Delete>`, "?delete"},
		{"unbound-prefix", `<other:Delete xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Object><Key>keep</Key></Object></other:Delete>`, "?delete"},
		{"attribute", `<Delete extra="ignored"><Object><Key>keep</Key></Object></Delete>`, "?delete"},
		{"nested", `<Delete><Object><Key>keep<Other/></Key></Object></Delete>`, "?delete"},
		{"directive", `<!DOCTYPE Delete><Delete><Object><Key>keep</Key></Object></Delete>`, "?delete"},
		{"trailing", valid + `<Delete/>`, "?delete"},
		{"empty-request", `<Delete/>`, "?delete"},
		{"query-value", valid, "?delete=true"},
		{"query-duplicate", valid, "?delete=&delete="},
		{"unknown-query", valid, "?delete=&unknown="},
		{"wrong-operation", valid, "?delete=&x-id=PutObject"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, store := fixture(t)
			if w := request(t, s, "PUT", "/bucket", nil, nil); w.Code != 200 {
				t.Fatal(w.Code)
			}
			if w := request(t, s, "PUT", "/bucket/keep", []byte("retain"), nil); w.Code != 200 {
				t.Fatal(w.Code)
			}
			body := []byte(tc.body)
			w := request(t, s, "POST", "/bucket"+tc.query, body, map[string]string{"Content-MD5": deleteMD5(body)})
			inventory, err := store.ListObjects(t.Context(), "bucket")
			if w.Code < 400 || err != nil || len(inventory) != 1 {
				t.Fatal("refused batch deleted a validated prefix:", w.Code, inventory, err)
			}
		})
	}
}

func TestDeleteObjectsChecksumsAndSizeBeforeDeletion(t *testing.T) {
	body := []byte(`<Delete><Object><Key>keep</Key></Object></Delete>`)
	var crc [4]byte
	binary.BigEndian.PutUint32(crc[:], crc32.ChecksumIEEE(body))
	for _, tc := range []struct {
		name, header, value string
		accept              bool
		length              int64
	}{
		{"md5", "Content-MD5", deleteMD5(body), true, int64(len(body))},
		{"crc32", "X-Amz-Checksum-Crc32", base64.StdEncoding.EncodeToString(crc[:]), true, int64(len(body))},
		{"wrong-md5", "Content-MD5", deleteMD5([]byte("wrong")), false, int64(len(body))},
		{"wrong-crc32", "X-Amz-Checksum-Crc32", "AAAAAA==", false, int64(len(body))},
		{"missing", "", "", false, int64(len(body))},
		{"short-body", "Content-MD5", deleteMD5(body), false, int64(len(body) + 1)},
		{"long-body", "Content-MD5", deleteMD5(body), false, int64(len(body) - 1)},
		{"over-bound", "Content-MD5", deleteMD5(body), false, maxDeleteXMLBytes + 1},
		{"duplicate-md5", "Content-MD5", deleteMD5(body), false, int64(len(body))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, store := fixture(t)
			request(t, s, "PUT", "/bucket", nil, nil)
			request(t, s, "PUT", "/bucket/keep", []byte("retain"), nil)
			r := httptest.NewRequest("POST", "http://127.0.0.1:4599/bucket?delete", strings.NewReader(string(body)))
			r.RemoteAddr = "127.0.0.1:1234"
			r.ContentLength = tc.length
			if tc.header != "" {
				r.Header.Set(tc.header, tc.value)
			}
			if tc.name == "duplicate-md5" {
				r.Header.Add(tc.header, tc.value)
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			inventory, err := store.ListObjects(t.Context(), "bucket")
			want := 1
			if tc.accept {
				want = 0
			}
			if (w.Code == 200) != tc.accept || err != nil || len(inventory) != want {
				t.Fatal("checksum/size validation did not precede deletion:", w.Code, inventory, err)
			}
		})
	}
}

func FuzzDeleteObjectsClosedShape(f *testing.F) {
	for _, body := range []string{`<Delete><Object><Key>key</Key></Object></Delete>`, `<Delete/>`, `<!DOCTYPE Delete>`, `<Delete><Object><Key>&amp;</Key></Object></Delete>`} {
		f.Add(body)
	}
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > maxDeleteXMLBytes {
			return
		}
		request, err := parseDelete([]byte(body))
		if err == nil && (len(request.keys) == 0 || len(request.keys) > 1000) {
			t.Fatal("accepted unbounded or empty deletion")
		}
	})
}
