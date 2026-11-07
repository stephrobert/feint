package objectstore

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func limitedCheckpointStore(t *testing.T, limit int64) (*Store, string) {
	t.Helper()
	path := filepath.Join(privateDir(t), "objects")
	s, err := OpenWithLimit(t.Context(), path, limit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	mustBucket(t, s, "b")
	return s, path
}

func TestCheckpointProtectsGarbageAndReleasesBudget(t *testing.T) {
	s, path := limitedCheckpointStore(t, 8)
	obj := mustPut(t, s, "b", "key", "same")
	mustPut(t, s, "b", "duplicate", "same")
	id, err := s.CreateUpload(t.Context(), "b", "upload", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	part, err := s.PutPart(t.Context(), "b", "upload", id, 1, strings.NewReader("part"), 4)
	if err != nil {
		t.Fatal(err)
	}
	mustSave(t, s, "one")
	mustSave(t, s, "two")
	for _, key := range []string{"key", "duplicate"} {
		if err := s.DeleteObject(t.Context(), "b", key); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AbortUpload(t.Context(), "b", "upload", id); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCheckpoint(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	if err := s.CollectGarbage(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, digest := range []string{obj.Digest, part.Digest} {
		if _, err := os.Stat(filepath.Join(path, "blobs", digest)); err != nil {
			t.Fatalf("checkpoint-protected bytes lost: %v", err)
		}
	}
	if _, err := s.PutObject(t.Context(), "b", "new", strings.NewReader("x"), PutOptions{Size: 1}); !errors.Is(err, ErrQuota) {
		t.Fatalf("checkpoint-retained bytes no longer count: %v", err)
	}
	if err := s.ResetCheckpoint(t.Context(), "two"); err != nil {
		t.Fatal(err)
	}
	_, body := readObject(t, s, "b", "key")
	if string(body) != "same" {
		t.Fatalf("checkpoint was not preserved: %q", body)
	}
	for _, key := range []string{"key", "duplicate"} {
		if err := s.DeleteObject(t.Context(), "b", key); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AbortUpload(t.Context(), "b", "upload", id); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCheckpoint(t.Context(), "two"); err != nil {
		t.Fatal(err)
	}
	// Deleting a checkpoint/aborting still does not automatically collect.
	if s.budget.used != 8 {
		t.Fatalf("automatic collection occurred: %d", s.budget.used)
	}
	for range 2 {
		if err := s.CollectGarbage(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if s.budget.used != 0 || s.budget.held != 0 {
		t.Fatalf("collection leaked budget: %+v", s.budget)
	}
	mustPut(t, s, "b", "new", "12345678")
}

func TestGarbageCollectionCountsHardlinksOnce(t *testing.T) {
	s, path := limitedCheckpointStore(t, 4)
	obj := mustPut(t, s, "b", "key", "data")
	alias := strings.Repeat("f", 64)
	if err := os.Link(filepath.Join(path, "blobs", obj.Digest), filepath.Join(path, "blobs", alias)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenWithLimit(t.Context(), path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := s.CollectGarbage(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, "blobs", alias)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan hardlink retained: %v", err)
	}
	if s.budget.used != 4 {
		t.Fatalf("deleting one hardlink released still-retained bytes: %d", s.budget.used)
	}
	if _, err := s.PutObject(t.Context(), "b", "new", strings.NewReader("x"), PutOptions{Size: 1}); !errors.Is(err, ErrQuota) {
		t.Fatalf("retained hardlink budget ignored: %v", err)
	}
	if err := s.DeleteObject(t.Context(), "b", "key"); err != nil {
		t.Fatal(err)
	}
	if err := s.CollectGarbage(t.Context()); err != nil {
		t.Fatal(err)
	}
	mustPut(t, s, "b", "new", "more")
}

func TestGarbageCollectionRejectsUnsafeInventory(t *testing.T) {
	for _, attack := range []string{"foreign-name", "symlink", "permissions", "checkpoint-name", "checkpoint-symlink"} {
		t.Run(attack, func(t *testing.T) {
			s, path := newStore(t)
			mustBucket(t, s, "b")
			orphan := mustPut(t, s, "b", "orphan", "orphan")
			if err := s.DeleteObject(t.Context(), "b", "orphan"); err != nil {
				t.Fatal(err)
			}
			mustSave(t, s, "safe")
			name := strings.Repeat("f", 64)
			dir := filepath.Join(path, "blobs")
			switch attack {
			case "foreign-name":
				name = "operator-file"
				if err := os.WriteFile(filepath.Join(dir, name), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(orphan.Digest, filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.WriteFile(filepath.Join(dir, name), []byte("keep"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "checkpoint-name":
				dir = filepath.Join(path, "checkpoints")
				name = "operator-file"
				body, err := os.ReadFile(filepath.Join(dir, "safe.json"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
					t.Fatal(err)
				}
			case "checkpoint-symlink":
				dir = filepath.Join(path, "checkpoints")
				name = "unsafe.json"
				if err := os.Symlink("safe.json", filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.CollectGarbage(t.Context()); err == nil {
				t.Fatalf("unsafe %s inventory was collected", attack)
			}
			if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
				t.Fatalf("unsafe entry was deleted: %v", err)
			}
			if _, err := os.Stat(filepath.Join(path, "blobs", orphan.Digest)); err != nil {
				t.Fatalf("partial deletion before validation finished: %v", err)
			}
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
			// Accept known crash debris; reject arbitrary names, not all inventory.
			pending := filepath.Join(path, "blobs", ".pending-"+rand.Text())
			if err := os.WriteFile(pending, []byte("debris"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := s.CollectGarbage(t.Context()); err != nil {
				t.Fatalf("accepting half: %v", err)
			}
			for _, name := range []string{pending, filepath.Join(path, "blobs", orphan.Digest)} {
				if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("owned garbage was not deleted: %v", err)
				}
			}
		})
	}
}

func TestGarbageCollectionProtectsLiveUploads(t *testing.T) {
	s, _ := newStore(t)
	mustBucket(t, s, "b")
	id, err := s.CreateUpload(t.Context(), "b", "key", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	part, err := s.PutPart(t.Context(), "b", "key", id, 1, strings.NewReader("part"), 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CollectGarbage(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteUpload(t.Context(), "b", "key", id, []PartRef{{1, part.Digest}}, 10); err != nil {
		t.Fatalf("GC removed live part: %v", err)
	}
}
