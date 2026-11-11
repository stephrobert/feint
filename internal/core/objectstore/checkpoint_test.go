package objectstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func mustSave(t *testing.T, s *Store, name string) {
	t.Helper()
	if err := s.SaveCheckpoint(t.Context(), name); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointRestoresFullManifestAndBytes(t *testing.T) {
	s, path := newStore(t)
	if err := s.CreateBucketWithMetadata(t.Context(), "b", map[string]string{"opaque": "value"}); err != nil {
		t.Fatal(err)
	}
	original := mustPut(t, s, "b", "old", "original\x00bytes")
	id, err := s.CreateUpload(t.Context(), "b", "assembled", PutOptions{
		ContentType: "text/plain", Metadata: map[string]string{"session": "retained"},
	})
	if err != nil {
		t.Fatal(err)
	}
	part, err := s.PutPart(t.Context(), "b", "assembled", id, 1, strings.NewReader("part"), 4)
	if err != nil {
		t.Fatal(err)
	}
	before := s.clone()
	mustSave(t, s, "Before_1")
	mustPut(t, s, "b", "old", "replacement")
	mustPut(t, s, "b", "extra", "extra")
	mustBucket(t, s, "extra-bucket")
	if _, err := s.CreateUpload(t.Context(), "b", "extra-session", PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteUpload(t.Context(), "b", "assembled", id, []PartRef{{1, part.Digest}}, 20); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	for range 2 {
		if err := s.ResetCheckpoint(t.Context(), "Before_1"); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, s.state) {
			t.Fatalf("reset did not restore exact full metadata: %+v", s.state)
		}
		got, body := readObject(t, s, "b", "old")
		if !reflect.DeepEqual(got, original) || string(body) != "original\x00bytes" {
			t.Fatalf("reset changed object: %+v %q", got, body)
		}
	}
	if _, err := s.CompleteUpload(t.Context(), "b", "assembled", id, []PartRef{{1, part.Digest}}, 20); err != nil {
		t.Fatal(err)
	}
	_, body := readObject(t, s, "b", "assembled")
	if string(body) != "part" {
		t.Fatalf("restored session lost its bytes: %q", body)
	}
}

func TestCheckpointCorruptBytesNeverPublish(t *testing.T) {
	for _, target := range []string{"object", "part"} {
		t.Run(target, func(t *testing.T) {
			s, path := newStore(t)
			mustBucket(t, s, "b")
			obj := mustPut(t, s, "b", "key", "good")
			id, err := s.CreateUpload(t.Context(), "b", "upload", PutOptions{})
			if err != nil {
				t.Fatal(err)
			}
			part, err := s.PutPart(t.Context(), "b", "upload", id, 1, strings.NewReader("part"), 4)
			if err != nil {
				t.Fatal(err)
			}
			mustSave(t, s, "old")
			if err := s.AbortUpload(t.Context(), "b", "upload", id); err != nil {
				t.Fatal(err)
			}
			mustPut(t, s, "b", "key", "live")
			live := s.clone()
			digest := obj.Digest
			if target == "part" {
				digest = part.Digest
			}
			if err := os.WriteFile(filepath.Join(path, "blobs", digest), []byte("evil"), 0o600); err != nil {
				t.Fatal(err)
			}
			indexBefore, err := os.ReadFile(filepath.Join(path, "index.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := s.ResetCheckpoint(t.Context(), "old"); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("corrupt %s checkpoint reset: %v", target, err)
			}
			indexAfter, err := os.ReadFile(filepath.Join(path, "index.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(s.state, live) || string(indexBefore) != string(indexAfter) {
				t.Fatal("failed reset published corrupt metadata")
			}
			if err := s.CollectGarbage(t.Context()); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("GC accepted corrupt checkpoint: %v", err)
			}
			good := "good"
			if target == "part" {
				good = "part"
			}
			if err := os.WriteFile(filepath.Join(path, "blobs", digest), []byte(good), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := s.ResetCheckpoint(t.Context(), "old"); err != nil {
				t.Fatalf("accepting half after repairing blob: %v", err)
			}
			if err := os.WriteFile(filepath.Join(path, "blobs", digest), []byte("evil"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := s.SaveCheckpoint(t.Context(), "corrupt"); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("saved corrupt live %s reference: %v", target, err)
			}
			if _, err := os.Stat(filepath.Join(path, "checkpoints", "corrupt.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed save published a checkpoint: %v", err)
			}
			if err := os.WriteFile(filepath.Join(path, "blobs", digest), []byte(good), 0o600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCheckpointDirectorySyncFailureIsReportedAndRetried(t *testing.T) {
	s, path := newStore(t)
	mustBucket(t, s, "b")
	if err := s.dir.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCheckpoint(t.Context(), "saved"); err == nil {
		t.Fatal("directory sync failure reported success")
	}
	if _, err := os.Stat(filepath.Join(path, "checkpoints", "saved.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkpoint published before parent directory was durable: %v", err)
	}
	// Closing the handle released the process lock. No second Store enters
	// during this injection; reacquire the lock before resuming normal operations.
	dir, err := s.root.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	s.dir = dir
	if err := syscall.Flock(int(dir.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	mustSave(t, s, "saved")
	if err := s.ResetCheckpoint(t.Context(), "saved"); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointNamesAndFormats(t *testing.T) {
	s, path := newStore(t)
	mustBucket(t, s, "b")
	for _, name := range []string{"", ".", "..", "../outside", "a/b", "a.json", "white space", "é", strings.Repeat("a", 65)} {
		for _, operation := range []func(context.Context, string) error{s.SaveCheckpoint, s.ResetCheckpoint, s.DeleteCheckpoint} {
			if err := operation(t.Context(), name); !errors.Is(err, ErrInvalid) {
				t.Fatalf("unsafe name %q: %v", name, err)
			}
		}
	}
	for _, operation := range []func(context.Context, string) error{s.ResetCheckpoint, s.DeleteCheckpoint} {
		if err := operation(t.Context(), "missing"); !errors.Is(err, ErrCheckpointNotFound) {
			t.Fatalf("missing checkpoint: %v", err)
		}
	}
	mustSave(t, s, strings.Repeat("a", 64))
	mustSave(t, s, "valid-Name_2")
	checkpoint := filepath.Join(path, "checkpoints", "valid-Name_2.json")
	good, err := os.ReadFile(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"format":"feint-objectstore","version":5,"buckets":{}}`,
		`{"format":"other","version":4,"buckets":{}}`,
		`{"format":"feint-objectstore","version":4,"buckets":null}`,
		strings.TrimSuffix(string(good), "}") + `,"unknown":true}`,
		string(good) + `{}`,
		`{"format":"feint-objectstore","version":4,"buckets":{"b":null}}`,
	} {
		if err := os.WriteFile(checkpoint, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := s.ResetCheckpoint(t.Context(), "valid-Name_2"); err == nil {
			t.Fatalf("accepted malformed checkpoint: %s", body)
		}
		if err := s.CollectGarbage(t.Context()); err == nil {
			t.Fatalf("GC accepted malformed checkpoint: %s", body)
		}
	}
	for version := 1; version <= 4; version++ {
		state := s.clone()
		state.Version = version
		body, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(checkpoint, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := s.ResetCheckpoint(t.Context(), "valid-Name_2"); err != nil {
			t.Fatalf("legacy v%d: %v", version, err)
		}
	}
	mustPut(t, s, "b", "saved", "bytes")
	mustSave(t, s, "valid-Name_2") // Replacement is supported.
	if err := s.ResetCheckpoint(t.Context(), "valid-Name_2"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCheckpoint(t.Context(), "valid-Name_2"); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetCheckpoint(t.Context(), "valid-Name_2"); !errors.Is(err, ErrCheckpointNotFound) {
		t.Fatalf("deleted checkpoint still exists: %v", err)
	}
}

func TestCheckpointPrivatePaths(t *testing.T) {
	for _, attack := range []string{"directory-link", "directory-permissions", "file-link", "file-permissions"} {
		t.Run(attack, func(t *testing.T) {
			s, path := newStore(t)
			mustSave(t, s, "valid")
			directory := filepath.Join(path, "checkpoints")
			file := filepath.Join(directory, "valid.json")
			switch attack {
			case "directory-link":
				moved := filepath.Join(path, "moved")
				if err := os.Rename(directory, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("moved", directory); err != nil {
					t.Fatal(err)
				}
			case "directory-permissions":
				if err := os.Chmod(directory, 0o755); err != nil {
					t.Fatal(err)
				}
			case "file-link":
				if err := os.Rename(file, filepath.Join(directory, "other.json")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("other.json", file); err != nil {
					t.Fatal(err)
				}
			case "file-permissions":
				if err := os.Chmod(file, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			for _, operation := range []func(context.Context, string) error{s.SaveCheckpoint, s.ResetCheckpoint, s.DeleteCheckpoint} {
				if err := operation(t.Context(), "valid"); err == nil {
					t.Fatalf("%s accepted unsafe checkpoint path", attack)
				}
			}
			if err := s.CollectGarbage(t.Context()); err == nil {
				t.Fatalf("%s: GC accepted unsafe checkpoint path", attack)
			}
		})
	}
}

func TestCheckpointCancellationClosedAndFault(t *testing.T) {
	s, _ := newStore(t)
	mustSave(t, s, "valid")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, operation := range []func(context.Context, string) error{s.SaveCheckpoint, s.ResetCheckpoint, s.DeleteCheckpoint} {
		if err := operation(ctx, "valid"); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled operation: %v", err)
		}
	}
	if err := s.CollectGarbage(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled collection: %v", err)
	}
	s.fault = errors.New("indeterminate commit")
	if err := s.ResetCheckpoint(t.Context(), "valid"); !errors.Is(err, s.fault) {
		t.Fatalf("fault ignored: %v", err)
	}
	s.fault = nil
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []func(context.Context, string) error{s.SaveCheckpoint, s.ResetCheckpoint, s.DeleteCheckpoint} {
		if err := operation(t.Context(), "valid"); !errors.Is(err, ErrClosed) {
			t.Fatalf("closed operation: %v", err)
		}
	}
	if err := s.CollectGarbage(t.Context()); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed collection: %v", err)
	}
}
