package objectstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBucketMetadataPersistsAndDoesNotAlias(t *testing.T) {
	s, path := newStore(t)
	initial := map[string]string{"initial": "creation"}
	if err := s.CreateBucketWithMetadata(t.Context(), "bucket", initial); err != nil {
		t.Fatal(err)
	}
	initial["initial"] = "outside"
	if err := s.SetBucketMetadata(t.Context(), "bucket", "first", "one"); err != nil {
		t.Fatal(err)
	}
	got, err := s.BucketMetadata(t.Context(), "bucket")
	if err != nil || got["first"] != "one" {
		t.Fatal(got, err)
	}
	got["first"] = "outside"
	listed, err := s.ListBucketMetadata(t.Context())
	if err != nil || len(listed) != 1 || listed["bucket"]["first"] != "one" {
		t.Fatal(listed, err)
	}
	listed["bucket"]["first"] = "outside-list"
	mustPut(t, s, "bucket", "key", "bytes")
	if err := s.SetBucketMetadata(t.Context(), "bucket", "second", "two"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	got, err = reopened.BucketMetadata(t.Context(), "bucket")
	if err != nil || got["initial"] != "creation" || got["first"] != "one" || got["second"] != "two" || len(got) != 3 {
		t.Fatal("attributes lost, aliased, or not persisted:", got, err)
	}
	if _, err := reopened.HeadObject(t.Context(), "bucket", "key"); err != nil {
		t.Fatal("attribute update lost an object:", err)
	}
}

func TestBucketMetadataBoundsAndConcurrentFields(t *testing.T) {
	s, _ := newStore(t)
	mustBucket(t, s, "bucket")
	if err := s.SetBucketMetadata(t.Context(), "bucket", "", strings.Repeat("x", 65536)); err != nil {
		t.Fatal("exact metadata bound rejected:", err)
	}
	if err := s.SetBucketMetadata(t.Context(), "bucket", "more", "x"); !errors.Is(err, ErrInvalid) {
		t.Fatal("cumulative metadata bound not enforced:", err)
	}
	if err := s.SetBucketMetadata(t.Context(), "bucket", "", ""); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"\xff", strings.Repeat("x", 65537)} {
		if err := s.SetBucketMetadata(t.Context(), "bucket", "bad", invalid); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid metadata admitted:", err)
		}
		if err := s.CreateBucketWithMetadata(t.Context(), "invalid", map[string]string{"bad": invalid}); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid initial metadata admitted:", err)
		}
	}
	var wg sync.WaitGroup
	for _, key := range []string{"first", "second"} {
		wg.Go(func() {
			if err := s.SetBucketMetadata(t.Context(), "bucket", key, key); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	got, err := s.BucketMetadata(t.Context(), "bucket")
	if err != nil || got["first"] != "first" || got["second"] != "second" || len(got) != 3 {
		t.Fatal("concurrent field update lost metadata or a failed write persisted:", got, err)
	}
	if err := s.SetBucketMetadata(t.Context(), "missing", "key", "value"); !errors.Is(err, ErrBucketNotFound) {
		t.Fatal("missing bucket accepted:", err)
	}
}

func TestLegacyBucketMetadataUpgradeAndValidation(t *testing.T) {
	s, path := newStore(t)
	mustBucket(t, s, "bucket")
	mustPut(t, s, "bucket", "key", "legacy")
	legacy := s.clone()
	legacy.Version = 1
	for key, obj := range legacy.Buckets["bucket"].Objects {
		obj.Modified = time.Time{}
		legacy.Buckets["bucket"].Objects[key] = obj
	}
	body, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(path, "index.json")
	if err := os.WriteFile(index, body, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	got, err := reopened.BucketMetadata(t.Context(), "bucket")
	if err != nil || len(got) != 0 {
		t.Fatal("legacy bucket refused:", got, err)
	}
	unchanged, err := os.ReadFile(index)
	if err != nil || string(unchanged) != string(body) {
		t.Fatal("read silently rewrote legacy metadata:", err)
	}
	if err := reopened.SetBucketMetadata(t.Context(), "bucket", "key", "value"); err != nil {
		t.Fatal(err)
	}
	if reopened.state.Version != storeVersion {
		t.Fatal("write did not upgrade metadata version")
	}
	if _, err := reopened.HeadObject(t.Context(), "bucket", "key"); err != nil {
		t.Fatal("upgrade lost legacy bytes:", err)
	}
	for _, invalid := range []string{
		`{"format":"feint-objectstore","version":1,"buckets":{"b":{"generation":"g","objects":{},"metadata":{"key":"value"}}}}`,
		`{"format":"feint-objectstore","version":2,"buckets":{"b":{"generation":"g","objects":{},"metadata":{"key":"` + strings.Repeat("x", 65537) + `"}}}}`,
	} {
		if _, err := decodeManifest([]byte(invalid)); !errors.Is(err, ErrCorrupt) {
			t.Fatal("restored metadata validation did not reject incompatible/oversized attributes:", err)
		}
	}
}
