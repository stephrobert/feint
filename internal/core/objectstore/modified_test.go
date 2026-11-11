package objectstore

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestObjectModificationDatePersistsAndLegacyDatesAreNotInvented(t *testing.T) {
	s, path := newStore(t)
	if err := s.CreateBucket(t.Context(), "bucket"); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	obj := mustPut(t, s, "bucket", "key", "bytes")
	if obj.Modified.IsZero() || obj.Modified.Before(before) || obj.Modified.After(time.Now()) {
		t.Fatal("new object lacks its actual commit time:", obj.Modified)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.HeadObject(t.Context(), "bucket", "key")
	if err != nil || !got.Modified.Equal(obj.Modified) {
		t.Fatal("restart changed the object's modification date:", got, err)
	}
	legacy := reopened.clone()
	legacy.Version = 2
	incompatible, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeManifest(incompatible); !errors.Is(err, ErrCorrupt) {
		t.Fatal("version 2 admitted a version 3 object date:", err)
	}
	yearZero := reopened.clone()
	obj.Modified = time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC)
	yearZero.Buckets["bucket"].Objects["key"] = obj
	invalidDate, err := json.Marshal(yearZero)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeManifest(invalidDate); !errors.Is(err, ErrCorrupt) {
		t.Fatal("restored modification date was not validated:", err)
	}
	got.Modified = time.Time{}
	legacy.Buckets["bucket"].Objects["key"] = got
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "index.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	older, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := older.Close(); err != nil {
			t.Error(err)
		}
	}()
	legacyObject, err := older.HeadObject(t.Context(), "bucket", "key")
	if err != nil || !legacyObject.Modified.IsZero() {
		t.Fatal("legacy object was refused or assigned a fabricated date:", legacyObject, err)
	}
	unchanged, err := os.ReadFile(filepath.Join(path, "index.json"))
	if err != nil || string(unchanged) != string(raw) {
		t.Fatal("reading version 2 rewrote the index:", err)
	}
}

func TestVerifiedObjectReaderSeeksAndCancelsWithoutExposingFileMethods(t *testing.T) {
	s, _ := newStore(t)
	if err := s.CreateBucket(t.Context(), "bucket"); err != nil {
		t.Fatal(err)
	}
	mustPut(t, s, "bucket", "key", "abcdef")
	ctx, cancel := context.WithCancel(t.Context())
	_, body, err := s.GetObject(ctx, "bucket", "key")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := body.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := body.Seek(3, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(body)
	if err != nil || string(raw) != "def" {
		t.Fatal(string(raw), err)
	}
	cancel()
	if _, err := body.Seek(0, io.SeekStart); !errors.Is(err, context.Canceled) {
		t.Fatal("seek ignored cancellation:", err)
	}
}
