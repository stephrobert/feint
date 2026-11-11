package s3

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/objectstore"
)

func decodeListing(t *testing.T, s *Server, query string) listResult {
	t.Helper()
	w := request(t, s, "GET", "/bucket?"+query, nil, nil)
	var result listResult
	if w.Code != 200 || xml.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatalf("listing failed: status=%d body=%q", w.Code, w.Body)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("<ETag>")) {
		t.Fatal("listing invented an ETag")
	}
	return result
}

func TestListingPagesContainActualSortedObjectsAndPrefixes(t *testing.T) {
	s, store := fixture(t)
	request(t, s, "PUT", "/bucket", nil, nil)
	for _, key := range []string{"z", "dir/two", "a", "dir/one"} {
		if w := request(t, s, "PUT", "/bucket/"+key, []byte(key), nil); w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
	}
	first := decodeListing(t, s, "list-type=2&delimiter=/&max-keys=1")
	if first.KeyCount != 1 || len(first.Objects) != 1 || first.Objects[0].Key != "a" ||
		first.Objects[0].Size != 1 || !first.Truncated || first.Next == "" {
		t.Fatal("first page did not reflect actual objects:", first)
	}
	modified, err := time.Parse(time.RFC3339Nano, first.Objects[0].Modified)
	obj, readErr := store.HeadObject(t.Context(), "bucket", "a")
	if err != nil || readErr != nil || !modified.Equal(obj.Modified) {
		t.Fatal("listing invented or lost its persisted modification time:", err, readErr)
	}
	second := decodeListing(t, s, "list-type=2&delimiter=/&max-keys=1&continuation-token="+url.QueryEscape(first.Next))
	if second.KeyCount != 1 || len(second.Objects) != 0 || len(second.Prefixes) != 1 ||
		second.Prefixes[0].Prefix != "dir/" || !second.Truncated || second.Next == "" || second.Next == first.Next {
		t.Fatal("prefix grouping consumed the wrong page or cursor:", second)
	}
	third := decodeListing(t, s, "list-type=2&delimiter=/&max-keys=1&continuation-token="+url.QueryEscape(second.Next))
	if third.KeyCount != 1 || len(third.Objects) != 1 || third.Objects[0].Key != "z" || third.Truncated || third.Next != "" {
		t.Fatal("final page repeated or fabricated an entry:", third)
	}
	filtered := decodeListing(t, s, "list-type=2&prefix=dir/&start-after=dir/one")
	if len(filtered.Objects) != 1 || filtered.Objects[0].Key != "dir/two" {
		t.Fatal("prefix and start-after were not applied literally:", filtered)
	}
	zero := decodeListing(t, s, "list-type=2&max-keys=0")
	if zero.KeyCount != 0 || zero.Truncated || zero.Next != "" || len(zero.Objects) != 0 || len(zero.Prefixes) != 0 {
		t.Fatal("zero limit fabricated a page or a non-progressing cursor:", zero)
	}
}

func TestListingRejectsInvalidQueriesAndCrossScopeCursors(t *testing.T) {
	s, _ := fixture(t)
	request(t, s, "PUT", "/bucket", nil, nil)
	request(t, s, "PUT", "/bucket/a", nil, nil)
	request(t, s, "PUT", "/bucket/b", nil, nil)
	first := decodeListing(t, s, "list-type=2&max-keys=1")
	for _, query := range []string{
		"list-type=2&max-keys=-1", "list-type=2&max-keys=1001", "list-type=2&max-keys=no",
		"list-type=2&prefix=a&prefix=b", "list-type=2&fetch-owner=true",
		"list-type=2&encoding-type=unknown",
		"list-type=2&continuation-token=invalid",
		"list-type=2&prefix=a&continuation-token=" + url.QueryEscape(first.Next),
		"list-type=2&delimiter=/&continuation-token=" + url.QueryEscape(first.Next),
		"list-type=2&start-after=a&continuation-token=" + url.QueryEscape(first.Next),
		"list-type=2&prefix=" + url.QueryEscape(strings.Repeat("a", 1025)),
		"list-type=2&prefix=" + url.QueryEscape(string([]byte{255})),
		"list-type=2&continuation-token=" + strings.Repeat("a", 4097),
	} {
		if w := request(t, s, "GET", "/bucket?"+query, nil, nil); w.Code != 400 {
			t.Fatal("invalid listing silently fell back to another query:", query, w.Code, w.Body)
		}
	}
	raw, err := base64.RawURLEncoding.DecodeString(first.Next)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{string(raw[:len(raw)-1]) + `,"unknown":true}`, string(raw) + ` {}`,
		string(bytes.Replace(raw, []byte(`"after":"a"`), []byte("\"after\":\"\xff\""), 1))} {
		token := base64.RawURLEncoding.EncodeToString([]byte(body))
		if w := request(t, s, "GET", "/bucket?list-type=2&continuation-token="+token, nil, nil); w.Code != 400 {
			t.Fatal("non-closed cursor admitted:", w.Code, w.Body)
		}
	}
	var cursor listCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		t.Fatal(err)
	}
	for _, after := range []string{"", strings.Repeat("a", 1025)} {
		cursor.After = after
		body, err := json.Marshal(cursor)
		if err != nil {
			t.Fatal(err)
		}
		token := base64.RawURLEncoding.EncodeToString(body)
		if w := request(t, s, "GET", "/bucket?list-type=2&continuation-token="+token, nil, nil); w.Code != 400 {
			t.Fatal("unbounded or empty cursor boundary admitted:", w.Code)
		}
	}
	if w := request(t, s, "GET", "/other?list-type=2&continuation-token="+url.QueryEscape(first.Next), nil, nil); w.Code != 400 {
		t.Fatal("cursor crossed a bucket boundary:", w.Code)
	}
	s.cfg.Namespace = "other-region"
	if w := request(t, s, "GET", "/bucket?list-type=2&continuation-token="+url.QueryEscape(first.Next), nil, nil); w.Code != 400 {
		t.Fatal("cursor crossed a deployment namespace:", w.Code)
	}
}

func TestListingRefusesUndatedLegacyObjectsWithoutBreakingTheirBytes(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "objects")
	store, err := objectstore.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket(t.Context(), "bucket"); err != nil {
		t.Fatal(err)
	}
	obj, err := store.PutObject(t.Context(), "bucket", "key", strings.NewReader("bytes"), objectstore.PutOptions{Size: 5})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(path, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	date, err := json.Marshal(obj.Modified)
	if err != nil {
		t.Fatal(err)
	}
	field := append([]byte(`,"modified":`), date...)
	if bytes.Count(raw, field) != 1 {
		t.Fatal("the actual producer did not persist the expected modification field")
	}
	if err := os.WriteFile(filepath.Join(path, "index.json"), bytes.Replace(raw, field, nil, 1), 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := objectstore.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	})
	s, err := New(reopened, Config{Region: "fr-par", MaxObjectBytes: 1 << 20,
		MissingBucket: ExoscaleMissingBucket, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if w := request(t, s, "GET", "/bucket?list-type=2", nil, nil); w.Code != 501 ||
		!strings.Contains(w.Body.String(), "no persisted modification date") {
		t.Fatal("legacy listing fabricated a modification date:", w.Code, w.Body)
	}
	if w := request(t, s, "GET", "/bucket/key", nil, nil); w.Code != 200 ||
		w.Body.String() != "bytes" || w.Header().Get("Last-Modified") != "" {
		t.Fatal("legacy byte reads were refused or assigned an invented date:", w.Code, w.Body)
	}
	if w := request(t, s, "PUT", "/bucket/key", []byte("new"), nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	result := decodeListing(t, s, "list-type=2")
	if len(result.Objects) != 1 || result.Objects[0].Size != 3 || result.Objects[0].Modified == "" {
		t.Fatal("overwriting a legacy object did not restore truthful listing:", result)
	}
}

func TestListingEmptyContinuationTokenStartsAtActualInventory(t *testing.T) {
	s, _ := fixture(t)
	request(t, s, "PUT", "/bucket", nil, nil)
	request(t, s, "PUT", "/bucket/key", []byte("content"), nil)
	result := decodeListing(t, s, "list-type=2&continuation-token=")
	if len(result.Objects) != 1 || result.Objects[0].Key != "key" || result.Objects[0].Size != 7 || result.Truncated {
		t.Fatal("empty optional token did not yield the actual first page:", result)
	}
}

func TestListingPreservesEncodedKeysAndRefusesLossyXML(t *testing.T) {
	s, _ := fixture(t)
	request(t, s, "PUT", "/bucket", nil, nil)
	for _, key := range []string{"folder//a/../%2F +?#", "control\x00key"} {
		if w := request(t, s, "PUT", "/bucket/"+url.PathEscape(key), []byte("bytes"), nil); w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
	}
	if w := request(t, s, "GET", "/bucket?list-type=2", nil, nil); w.Code != 501 {
		t.Fatal("XML silently replaced a logical control character:", w.Code, w.Body)
	}
	result := decodeListing(t, s, "list-type=2&encoding-type=url")
	if len(result.Objects) != 2 || result.Encoding != "url" {
		t.Fatal("encoded listing lost actual keys:", result)
	}
	for i, expected := range []string{"control\x00key", "folder//a/../%2F +?#"} {
		key, err := url.PathUnescape(result.Objects[i].Key)
		if err != nil || key != expected {
			t.Fatal("key was cleaned or decoded twice:", key, expected, err)
		}
	}
}
