package objectstore

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func limitedStore(t *testing.T, limit int64) (*Store, string) {
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
	return s, path
}

func TestBudgetRetainsDeletedBytesAndAllowsExactThreshold(t *testing.T) {
	s, path := limitedStore(t, 8)
	mustBucket(t, s, "bucket")
	mustPut(t, s, "bucket", "first", "abcd")
	if err := s.DeleteObject(t.Context(), "bucket", "first"); err != nil {
		t.Fatal(err)
	}
	// A duplicate fits in the remaining temporary space, then releases it.
	mustPut(t, s, "bucket", "duplicate", "abcd")
	mustPut(t, s, "bucket", "second", "efgh")
	if _, err := s.PutObject(t.Context(), "bucket", "second", strings.NewReader("x"), PutOptions{Size: 1}); !errors.Is(err, ErrQuota) {
		t.Fatal("over-budget overwrite was not refused", err)
	}
	_, data := readObject(t, s, "bucket", "second")
	if string(data) != "efgh" {
		t.Fatal("quota refusal changed committed bytes")
	}
	mustPut(t, s, "bucket", "empty", "")
	if err := s.DeleteObject(t.Context(), "bucket", "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject(t.Context(), "bucket", "new", strings.NewReader("x"), PutOptions{Size: 1}); !errors.Is(err, ErrQuota) {
		t.Fatal("deleting metadata released retained blob bytes", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWithLimit(t.Context(), path, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := reopened.PutObject(t.Context(), "bucket", "new", strings.NewReader("x"), PutOptions{Size: 1}); !errors.Is(err, ErrQuota) {
		t.Fatal("reopen forgot retained bytes", err)
	}
}

type gatedBudgetReader struct {
	entered chan struct{}
	release chan struct{}
	body    io.Reader
	started bool
}

func (r *gatedBudgetReader) Read(p []byte) (int, error) {
	if !r.started {
		r.started = true
		close(r.entered)
		<-r.release
	}
	return r.body.Read(p)
}

type countingBudgetReader struct{ calls int }

func (r *countingBudgetReader) Read([]byte) (int, error) {
	r.calls++
	return 0, io.EOF
}

func TestBudgetReservesConcurrentUploadsBeforeReading(t *testing.T) {
	s, _ := limitedStore(t, 6)
	mustBucket(t, s, "bucket")
	reader := &gatedBudgetReader{entered: make(chan struct{}), release: make(chan struct{}), body: strings.NewReader("abcd")}
	done := make(chan error, 1)
	go func() {
		_, err := s.PutObject(context.Background(), "bucket", "first", reader, PutOptions{Size: 4})
		done <- err
	}()
	select {
	case <-reader.entered:
	case err := <-done:
		t.Fatal("first upload did not reach its accepting case", err)
	}
	probe := &countingBudgetReader{}
	_, err := s.PutObject(t.Context(), "bucket", "second", probe, PutOptions{Size: 3})
	// Always release the first upload before asserting so a failing mutation
	// cannot leave Close waiting on a test-owned blocked reader.
	close(reader.release)
	firstErr := <-done
	if firstErr != nil || !errors.Is(err, ErrQuota) || probe.calls != 0 {
		t.Fatal("reservation did not protect the aggregate limit before input reads", firstErr, err, probe.calls)
	}
	if _, err := s.HeadObject(t.Context(), "bucket", "second"); !errors.Is(err, ErrObjectNotFound) {
		t.Fatal("refused upload published metadata", err)
	}
	mustPut(t, s, "bucket", "fits", "ef")
}

func TestFailedUploadReleasesBudgetAndNeverWritesProbeByte(t *testing.T) {
	s, path := limitedStore(t, 4)
	mustBucket(t, s, "bucket")
	for _, data := range []string{"x", "abcde"} {
		if _, err := s.PutObject(t.Context(), "bucket", "bad", strings.NewReader(data), PutOptions{Size: 4}); err == nil {
			t.Fatal("invalid upload succeeded")
		}
		entries, err := os.ReadDir(filepath.Join(path, "blobs"))
		if err != nil || len(entries) != 0 {
			t.Fatal("failed upload retained temporary bytes", entries, err)
		}
	}
	// Observe the temporary file at the EOF probe, not only after cleanup.
	reader := &probeSizeReader{t: t, path: filepath.Join(path, "blobs"), body: strings.NewReader("abcd")}
	if _, err := s.PutObject(t.Context(), "bucket", "bad", reader, PutOptions{Size: 4}); err == nil {
		t.Fatal("extra probe byte was accepted")
	}
	if reader.observe == nil {
		t.Fatal("EOF probe was not exercised")
	}
	info, statErr := reader.observe.Stat()
	closeErr := reader.observe.Close()
	if err := errors.Join(statErr, closeErr); err != nil || info.Size() != 4 {
		t.Fatal("temporary file wrote beyond its declared reservation", info, err)
	}
	mustPut(t, s, "bucket", "good", "abcd")
}

type probeSizeReader struct {
	t       *testing.T
	path    string
	body    *strings.Reader
	observe *os.File
}

func (r *probeSizeReader) Read(p []byte) (int, error) {
	if r.body.Len() == 0 {
		entries, err := os.ReadDir(r.path)
		if err != nil || len(entries) != 1 {
			r.t.Fatal(entries, err)
		}
		r.observe, err = os.Open(filepath.Join(r.path, entries[0].Name()))
		if err != nil {
			r.t.Fatal(err)
		}
		return copy(p, []byte("extra")), nil
	}
	return r.body.Read(p)
}

func TestBudgetRecountsCrashFilesAndHardLinks(t *testing.T) {
	s, path := newStore(t)
	mustBucket(t, s, "bucket")
	obj := mustPut(t, s, "bucket", "key", "abcd")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	blobs := filepath.Join(path, "blobs")
	if err := os.Link(filepath.Join(blobs, obj.Digest), filepath.Join(blobs, ".pending-alias")); err != nil {
		t.Fatal(err)
	}
	bounded, err := OpenWithLimit(t.Context(), path, 4)
	if err != nil {
		t.Fatal("one inode was counted twice", err)
	}
	mustPut(t, bounded, "bucket", "empty", "")
	if err := bounded.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blobs, ".pending-crash"), []byte("xy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if over, err := OpenWithLimit(t.Context(), path, 5); !errors.Is(err, ErrQuota) {
		if over != nil {
			_ = over.Close()
		}
		t.Fatal("crash leftovers did not count", err)
	}
	bounded, err = OpenWithLimit(t.Context(), path, 6)
	if err != nil {
		t.Fatal("larger explicit limit did not admit existing data", err)
	}
	if err := bounded.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(blobs, ".pending-crash")); err != nil {
		t.Fatal("opening over budget removed existing data", err)
	}
}

func TestBudgetRejectsUnsafeInventoryAndInvalidLimit(t *testing.T) {
	s, path := newStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int64{0, -1} {
		if got, err := OpenWithLimit(t.Context(), path, limit); !errors.Is(err, ErrInvalid) || got != nil {
			t.Fatal(got, err)
		}
	}
	if err := os.Symlink("../index.json", filepath.Join(path, "blobs", ".pending-link")); err != nil {
		t.Fatal(err)
	}
	if got, err := OpenWithLimit(t.Context(), path, 1024); !errors.Is(err, ErrCorrupt) || got != nil {
		t.Fatal("unsafe inventory was trusted", got, err)
	}
	// A refused limited open must release ownership for the original API.
	unlimited, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := unlimited.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBudgetChargesBlobWithoutMetadataPublication(t *testing.T) {
	s, _ := limitedStore(t, 6)
	mustBucket(t, s, "bucket")
	reader := &gatedBudgetReader{entered: make(chan struct{}), release: make(chan struct{}), body: strings.NewReader("abcd")}
	done := make(chan error, 1)
	go func() {
		_, err := s.PutObject(context.Background(), "bucket", "first", reader, PutOptions{Size: 4})
		done <- err
	}()
	select {
	case <-reader.entered:
	case err := <-done:
		t.Fatal("upload did not reach its accepting case", err)
	}
	deleteErr := s.DeleteBucket(t.Context(), "bucket")
	close(reader.release)
	err := <-done
	if deleteErr != nil || !errors.Is(err, ErrBucketNotFound) {
		t.Fatal(deleteErr, err)
	}
	mustBucket(t, s, "bucket")
	if _, err := s.PutObject(t.Context(), "bucket", "new", strings.NewReader("xyz"), PutOptions{Size: 3}); !errors.Is(err, ErrQuota) {
		t.Fatal("unreferenced published bytes were not charged", err)
	}
	mustPut(t, s, "bucket", "fits", "xy")
}

type movedTempReader struct {
	body *strings.Reader
	path string
}

func (r *movedTempReader) Read(p []byte) (int, error) {
	if r.body.Len() == 0 {
		entries, err := os.ReadDir(r.path)
		if err != nil || len(entries) != 1 {
			return 0, errors.Join(err, errors.New("expected one temporary blob"))
		}
		// Inject the filesystem failure without permissions depending on UID:
		// the open inode stays in blobs, but publish and cleanup lose its name.
		if err := os.Rename(filepath.Join(r.path, entries[0].Name()), filepath.Join(r.path, ".pending-moved")); err != nil {
			return 0, err
		}
	}
	return r.body.Read(p)
}

func TestBudgetChargesFailedTemporaryCleanup(t *testing.T) {
	s, path := limitedStore(t, 6)
	mustBucket(t, s, "bucket")
	reader := &movedTempReader{body: strings.NewReader("abcd"), path: filepath.Join(path, "blobs")}
	if _, err := s.PutObject(t.Context(), "bucket", "bad", reader, PutOptions{Size: 4}); err == nil {
		t.Fatal("filesystem failure became success")
	}
	if _, err := s.PutObject(t.Context(), "bucket", "new", strings.NewReader("xyz"), PutOptions{Size: 3}); !errors.Is(err, ErrQuota) {
		t.Fatal("failed cleanup released bytes still on disk", err)
	}
	mustPut(t, s, "bucket", "fits", "xy")
}
