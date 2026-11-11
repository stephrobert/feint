package objectstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestBulkDeletePersistsReferencesAndRetainsBytes(t *testing.T) {
	s, path := newStore(t)
	mustBucket(t, s, "bucket")
	object := mustPut(t, s, "bucket", "a", "retained bytes")
	mustPut(t, s, "bucket", "b", "other bytes")
	mustPut(t, s, "bucket", "keep", "untouched")
	keys := []string{"b", "a", "a", "missing"}
	if err := s.DeleteObjects(t.Context(), "bucket", keys); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(keys, []string{"b", "a", "a", "missing"}) {
		t.Fatal("caller-owned key order changed:", keys)
	}
	if _, err := os.Stat(filepath.Join(path, "blobs", object.Digest)); err != nil {
		t.Fatal("bulk deletion reclaimed immutable bytes:", err)
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
	inventory, err := reopened.ListObjects(t.Context(), "bucket")
	if err != nil || len(inventory) != 1 || inventory["keep"].Size != 9 {
		t.Fatal("persisted bulk deletion lost other references:", inventory, err)
	}
}

func TestBulkDeleteRefusalsPreserveEveryReference(t *testing.T) {
	for _, name := range []string{"invalid-key", "empty", "canceled", "commit-failure"} {
		t.Run(name, func(t *testing.T) {
			s, path := newStore(t)
			mustBucket(t, s, "bucket")
			mustPut(t, s, "bucket", "a", "a")
			mustPut(t, s, "bucket", "b", "b")
			ctx := t.Context()
			keys := []string{"a", "b"}
			switch name {
			case "invalid-key":
				keys[1] = ""
			case "empty":
				keys = nil
			case "canceled":
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			case "commit-failure":
				index := filepath.Join(path, "index.json")
				saved := filepath.Join(path, "saved-index")
				if err := os.Rename(index, saved); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(index, 0700); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Remove(index); err != nil {
						t.Error(err)
					}
					if err := os.Rename(saved, index); err != nil {
						t.Error(err)
					}
				})
			}
			if err := s.DeleteObjects(ctx, "bucket", keys); err == nil {
				t.Fatal("invalid or failed batch returned success")
			}
			inventory, err := s.ListObjects(t.Context(), "bucket")
			if err != nil || len(inventory) != 2 {
				t.Fatal("failed batch partially deleted references:", inventory, err)
			}
		})
	}
	s, _ := newStore(t)
	if err := s.DeleteObjects(t.Context(), "missing", []string{"key"}); !errors.Is(err, ErrBucketNotFound) {
		t.Fatal("missing bucket did not preserve its typed error:", err)
	}
}
