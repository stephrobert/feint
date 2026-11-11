package objectstore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCorruptPartCannotPublishAssembledObject(t *testing.T) {
	s, path := newStore(t)
	mustBucket(t, s, "bucket")
	old := mustPut(t, s, "bucket", "key", "old")
	id, err := s.CreateUpload(t.Context(), "bucket", "key", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	part, err := s.PutPart(t.Context(), "bucket", "key", id, 1, bytes.NewBufferString("abc"), 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "blobs", part.Digest), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteUpload(t.Context(), "bucket", "key", id, []PartRef{{1, part.Digest}}, 3); !errors.Is(err, ErrCorrupt) {
		t.Fatal("corrupt part completed successfully:", err)
	}
	if obj, err := s.HeadObject(t.Context(), "bucket", "key"); err != nil || obj.Digest != old.Digest {
		t.Fatal("corrupt assembly replaced old object:", obj, err)
	}
	if _, err := s.ReadUpload(t.Context(), "bucket", "key", id); err != nil {
		t.Fatal("corrupt assembly lost the resumable session:", err)
	}
}
