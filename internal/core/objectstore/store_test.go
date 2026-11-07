package objectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(privateDir(t), "objects")
	s, err := Open(t.Context(), path)
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

func privateDir(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustBucket(t *testing.T, s *Store, name string) {
	t.Helper()
	if err := s.CreateBucket(t.Context(), name); err != nil {
		t.Fatal(err)
	}
}

func mustPut(t *testing.T, s *Store, name, key, data string) Object {
	t.Helper()
	obj, err := s.PutObject(t.Context(), name, key, strings.NewReader(data), PutOptions{Size: int64(len(data))})
	if err != nil {
		t.Fatal(err)
	}
	return obj
}

func readObject(t *testing.T, s *Store, name, key string) (Object, []byte) {
	t.Helper()
	obj, reader, err := s.GetObject(t.Context(), name, key)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(reader)
	if err := errors.Join(readErr, reader.Close()); err != nil {
		t.Fatal(err)
	}
	return obj, data
}

func TestBytesAndMetadataSurviveReopen(t *testing.T) {
	s, path := newStore(t)
	mustBucket(t, s, "b")
	data := bytes.Repeat([]byte{0, 1, 255, '\n'}, 100000)
	hash := sha256.Sum256(data)
	options := PutOptions{Size: int64(len(data)), ContentType: "application/octet-stream", Metadata: map[string]string{"purpose": "roundtrip"}}
	obj, err := s.PutObject(t.Context(), "b", "nested/key", bytes.NewReader(data), options)
	if err != nil {
		t.Fatal(err)
	}
	if obj.Digest != hex.EncodeToString(hash[:]) || obj.Size != int64(len(data)) {
		t.Fatalf("wrong stored digest/size: %+v", obj)
	}
	options.Metadata["purpose"] = "caller mutation"
	obj.Metadata["purpose"] = "result mutation"
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
	got, body := readObject(t, reopened, "b", "nested/key")
	if !bytes.Equal(body, data) || got.Metadata["purpose"] != "roundtrip" || got.ContentType != options.ContentType {
		t.Fatalf("bytes or metadata changed after reopen: %+v", got)
	}
	list, err := reopened.ListObjects(t.Context(), "b")
	if err != nil {
		t.Fatal(err)
	}
	list["nested/key"].Metadata["purpose"] = "inventory mutation"
	delete(list, "nested/key")
	head, err := reopened.HeadObject(t.Context(), "b", "nested/key")
	if err != nil || head.Metadata["purpose"] != "roundtrip" {
		t.Fatalf("returned inventory aliased the store: %+v, %v", head, err)
	}
}

func TestLogicalNamesNeverBecomeFilesystemPaths(t *testing.T) {
	s, path := newStore(t)
	outside := filepath.Join(filepath.Dir(path), "outside")
	if err := os.WriteFile(outside, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "../outside"
	mustBucket(t, s, name)
	for _, key := range []string{"../../outside", "/absolute/key", `C:\outside`, "nested//../key", "key\x00suffix", "cle-\u00e9"} {
		obj := mustPut(t, s, name, key, "real bytes")
		_, data := readObject(t, s, name, key)
		if string(data) != "real bytes" {
			t.Fatalf("logical key %q did not roundtrip", key)
		}
		if !validDigest(obj.Digest) {
			t.Fatal("physical blob name is not a digest")
		}
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "untouched" {
		t.Fatalf("logical names modified outside bytes: %q, %v", data, err)
	}
	files, err := os.ReadDir(filepath.Join(path, "blobs"))
	if err != nil || len(files) != 1 {
		t.Fatalf("identical bytes were not deduplicated: %v, %v", files, err)
	}
	if !validDigest(files[0].Name()) {
		t.Fatalf("unexpected physical filename %q", files[0].Name())
	}
}

func TestEmptyObjectsAndBucketLifecycle(t *testing.T) {
	s, _ := newStore(t)
	mustBucket(t, s, "z")
	mustBucket(t, s, "a")
	if err := s.CreateBucket(t.Context(), "a"); !errors.Is(err, ErrBucketExists) {
		t.Fatalf("duplicate create: %v", err)
	}
	names, err := s.ListBuckets(t.Context())
	if err != nil || !reflect.DeepEqual(names, []string{"a", "z"}) {
		t.Fatalf("bucket list: %v, %v", names, err)
	}
	mustPut(t, s, "a", "empty", "")
	obj, data := readObject(t, s, "a", "empty")
	if len(data) != 0 || obj.Size != 0 || obj.Digest != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("invalid empty object: %+v, %q", obj, data)
	}
	if err := s.DeleteBucket(t.Context(), "a"); !errors.Is(err, ErrBucketNotEmpty) {
		t.Fatalf("nonempty bucket delete: %v", err)
	}
	if err := s.DeleteObject(t.Context(), "a", "empty"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObject(t.Context(), "a", "empty"); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("missing object delete: %v", err)
	}
	if err := s.DeleteBucket(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.HeadBucket(t.Context(), "a"); !errors.Is(err, ErrBucketNotFound) {
		t.Fatalf("deleted bucket head: %v", err)
	}
}

type failedReader struct{}

func (failedReader) Read(p []byte) (int, error) {
	copy(p, "partial")
	return min(len(p), len("partial")), io.ErrUnexpectedEOF
}

func TestUnfinishedUploadsNeverReplaceCommittedBytes(t *testing.T) {
	s, path := newStore(t)
	mustBucket(t, s, "b")
	original := mustPut(t, s, "b", "k", "old")
	for _, tc := range []struct {
		name string
		body io.Reader
		size int64
		want error
	}{
		{"short", strings.NewReader("x"), 2, ErrInvalid},
		{"long", strings.NewReader("xxx"), 2, ErrInvalid},
		{"interrupted", failedReader{}, 100, io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.PutObject(t.Context(), "b", "k", tc.body, PutOptions{Size: tc.size})
			if !errors.Is(err, tc.want) {
				t.Fatalf("upload refusal: %v", err)
			}
			obj, body := readObject(t, s, "b", "k")
			if string(body) != "old" || obj.Digest != original.Digest {
				t.Fatalf("failed upload published bytes: %+v %q", obj, body)
			}
		})
	}
	entries, err := os.ReadDir(filepath.Join(path, "blobs"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("unfinished upload leaked temporary bytes: %v, %v", entries, err)
	}
}

type gatedReader struct {
	reader  io.Reader
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func gate(t *testing.T, data string) (*gatedReader, func()) {
	t.Helper()
	r := &gatedReader{reader: strings.NewReader(data), entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(r.release) }) }
	t.Cleanup(unblock)
	return r, unblock
}

func (r *gatedReader) Read(p []byte) (int, error) {
	r.once.Do(func() {
		close(r.entered)
		<-r.release
	})
	return r.reader.Read(p)
}

func waitFor(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("bounded operation did not finish")
		return errors.New("operation did not finish")
	}
}

func waitReady(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not reach its controlled read")
	}
}

func TestDeletedAndRecreatedBucketCannotBeResurrectedByUpload(t *testing.T) {
	s, _ := newStore(t)
	mustBucket(t, s, "b")
	reader, unblock := gate(t, "new")
	result := make(chan error, 1)
	go func() {
		_, err := s.PutObject(t.Context(), "b", "k", reader, PutOptions{Size: 3})
		result <- err
	}()
	waitReady(t, reader.entered)
	if err := s.DeleteBucket(t.Context(), "b"); err != nil {
		t.Fatal(err)
	}
	mustBucket(t, s, "b")
	unblock()
	if err := waitFor(t, result); !errors.Is(err, ErrBucketNotFound) {
		t.Fatalf("stale upload committed into recreated bucket: %v", err)
	}
	objects, err := s.ListObjects(t.Context(), "b")
	if err != nil || len(objects) != 0 {
		t.Fatalf("new bucket inherited stale upload: %v, %v", objects, err)
	}
	mustPut(t, s, "b", "k", "accepted")
}

func TestUnrelatedKeysProgressWhileAnUploadIsBlocked(t *testing.T) {
	s, _ := newStore(t)
	mustBucket(t, s, "b")
	reader, unblock := gate(t, "blocked")
	first := make(chan error, 1)
	go func() {
		_, err := s.PutObject(t.Context(), "b", "one", reader, PutOptions{Size: 7})
		first <- err
	}()
	waitReady(t, reader.entered)
	second := make(chan error, 1)
	go func() {
		_, err := s.PutObject(t.Context(), "b", "two", strings.NewReader("independent"), PutOptions{Size: 11})
		second <- err
	}()
	if err := waitFor(t, second); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := waitFor(t, first); err != nil {
		t.Fatal(err)
	}
}

func TestCancellationNeverPublishesPartialOrCompletedUpload(t *testing.T) {
	s, _ := newStore(t)
	mustBucket(t, s, "b")
	reader, unblock := gate(t, "new")
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := s.PutObject(ctx, "b", "k", reader, PutOptions{Size: 3})
		result <- err
	}()
	waitReady(t, reader.entered)
	cancel()
	unblock()
	if err := waitFor(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled upload: %v", err)
	}
	if _, _, err := s.GetObject(t.Context(), "b", "k"); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("canceled upload published metadata: %v", err)
	}
	mustPut(t, s, "b", "k", "accepted")
}

func TestReaderSeesCompleteOldBytesDuringReplacementAndAfterDelete(t *testing.T) {
	s, _ := newStore(t)
	mustBucket(t, s, "b")
	mustPut(t, s, "b", "k", "old")
	reader, unblock := gate(t, "replacement")
	result := make(chan error, 1)
	go func() {
		_, err := s.PutObject(t.Context(), "b", "k", reader, PutOptions{Size: 11})
		result <- err
	}()
	waitReady(t, reader.entered)
	_, old := readObject(t, s, "b", "k")
	if string(old) != "old" {
		t.Fatalf("uncommitted replacement leaked: %q", old)
	}
	unblock()
	if err := waitFor(t, result); err != nil {
		t.Fatal(err)
	}
	_, held, err := s.GetObject(t.Context(), "b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObject(t.Context(), "b", "k"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(held)
	if err := errors.Join(readErr, held.Close()); err != nil {
		t.Fatal(err)
	}
	if string(data) != "replacement" {
		t.Fatalf("held reader lost immutable bytes: %q", data)
	}
}

func TestReturnedReaderHonorsCancellationThroughCopy(t *testing.T) {
	s, _ := newStore(t)
	mustBucket(t, s, "b")
	mustPut(t, s, "b", "k", "good")
	ctx, cancel := context.WithCancel(t.Context())
	_, reader, err := s.GetObject(ctx, "b", "k")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	n, err := io.Copy(io.Discard, reader)
	if !errors.Is(err, context.Canceled) || n != 0 {
		t.Fatalf("io.Copy bypassed cancellation: %d bytes, %v", n, err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentPutAndDeleteNeverTearBytesOrLoseOtherKeys(t *testing.T) {
	s, _ := newStore(t)
	mustBucket(t, s, "b")
	var wg sync.WaitGroup
	results := make(chan error, 64)
	for i := range 32 {
		wg.Go(func() {
			data := strings.Repeat(fmt.Sprintf("%02d", i), 4096)
			_, err := s.PutObject(t.Context(), "b", "shared", strings.NewReader(data), PutOptions{Size: int64(len(data))})
			if err != nil {
				results <- err
				return
			}
			err = s.DeleteObject(t.Context(), "b", "shared")
			if err != nil && !errors.Is(err, ErrObjectNotFound) {
				results <- err
			}
			_, err = s.PutObject(t.Context(), "b", fmt.Sprintf("key-%02d", i), strings.NewReader(data), PutOptions{Size: int64(len(data))})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := range 32 {
		obj, data := readObject(t, s, "b", fmt.Sprintf("key-%02d", i))
		expected := strings.Repeat(fmt.Sprintf("%02d", i), 4096)
		hash := sha256.Sum256(data)
		if string(data) != expected || obj.Digest != hex.EncodeToString(hash[:]) {
			t.Fatal("concurrent operation tore bytes or lost metadata")
		}
	}
}

func TestMissingAndCorruptBlobsFailGetHeadAndReopen(t *testing.T) {
	for _, mode := range []string{"missing", "wrong-size", "wrong-digest", "symlink", "permissions"} {
		t.Run(mode, func(t *testing.T) {
			s, path := newStore(t)
			mustBucket(t, s, "b")
			obj := mustPut(t, s, "b", "k", "good")
			blob := filepath.Join(path, "blobs", obj.Digest)
			var err error
			switch mode {
			case "missing":
				err = os.Remove(blob)
			case "wrong-size":
				err = os.WriteFile(blob, []byte("shorter"), 0o600)
			case "wrong-digest":
				err = os.WriteFile(blob, []byte("evil"), 0o600)
			case "symlink":
				target := filepath.Join(filepath.Dir(path), "outside")
				if err := os.WriteFile(target, []byte("good"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(blob); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(target, blob)
			case "permissions":
				err = os.Chmod(blob, 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, reader, err := s.GetObject(t.Context(), "b", "k"); !errors.Is(err, ErrCorrupt) || reader != nil {
				t.Fatalf("GET served invalid bytes: reader=%v err=%v", reader, err)
			}
			if _, err := s.HeadObject(t.Context(), "b", "k"); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("HEAD reported corrupt blob held: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, err := Open(t.Context(), path); !errors.Is(err, ErrCorrupt) || reopened != nil {
				t.Fatalf("reopen admitted corrupt blob: store=%v err=%v", reopened, err)
			}
		})
	}
}

func TestRootOwnershipPermissionsAndSymlinksAreRefused(t *testing.T) {
	t.Run("unmarked", func(t *testing.T) {
		path := filepath.Join(privateDir(t), "operator")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(t.Context(), path); !errors.Is(err, ErrUnowned) {
			t.Fatalf("adopted unmarked directory: %v", err)
		}
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) != 0 {
			t.Fatalf("unmarked directory modified: %v, %v", entries, err)
		}
	})
	for _, mode := range []string{"root-symlink", "root-permissions", "index-symlink", "index-permissions", "blobs-symlink"} {
		t.Run(mode, func(t *testing.T) {
			s, path := newStore(t)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "root-symlink":
				alias := path + "-alias"
				if err := os.Symlink(filepath.Base(path), alias); err != nil {
					t.Fatal(err)
				}
				path = alias
			case "root-permissions":
				if err := os.Chmod(path, 0o755); err != nil {
					t.Fatal(err)
				}
			case "index-symlink":
				if err := os.Rename(filepath.Join(path, "index.json"), filepath.Join(path, "saved-index")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("saved-index", filepath.Join(path, "index.json")); err != nil {
					t.Fatal(err)
				}
			case "index-permissions":
				if err := os.Chmod(filepath.Join(path, "index.json"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "blobs-symlink":
				if err := os.Rename(filepath.Join(path, "blobs"), filepath.Join(path, "saved-blobs")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("saved-blobs", filepath.Join(path, "blobs")); err != nil {
					t.Fatal(err)
				}
			}
			reopened, err := Open(t.Context(), path)
			if err == nil || reopened != nil {
				t.Fatalf("unsafe store root admitted: %v, %v", reopened, err)
			}
		})
	}
}

func TestUnsafeParentIsRefusedBeforeCreation(t *testing.T) {
	for _, mode := range []os.FileMode{0o777, 0o770, 0o707} {
		t.Run(mode.String(), func(t *testing.T) {
			parent := t.TempDir()
			if err := os.Chmod(parent, mode); err != nil {
				t.Fatal(err)
			}
			operator := filepath.Join(parent, "operator")
			if err := os.Mkdir(operator, 0o700); err != nil {
				t.Fatal(err)
			}
			index := filepath.Join(operator, "index.json")
			if err := os.WriteFile(index, []byte("unrelated operator data"), 0o600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(parent, "new")
			store, err := Open(t.Context(), path)
			if store != nil {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			}
			if !errors.Is(err, ErrUnowned) || store != nil {
				t.Fatalf("unsafe parent admitted: %v, %v", store, err)
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unsafe parent was modified before refusal: %v", err)
			}
			data, err := os.ReadFile(index)
			if err != nil || string(data) != "unrelated operator data" {
				t.Fatalf("operator index modified: %q, %v", data, err)
			}
		})
	}
	for _, mode := range []os.FileMode{0o700, 0o755, os.ModeSticky | 0o777} {
		t.Run("accept-"+mode.String(), func(t *testing.T) {
			parent := t.TempDir()
			if err := os.Chmod(parent, mode); err != nil {
				t.Fatal(err)
			}
			s, err := Open(t.Context(), filepath.Join(parent, "new"))
			if err != nil {
				t.Fatalf("protected parent refused: %v", err)
			}
			mustBucket(t, s, "b")
			mustPut(t, s, "b", "k", "safe")
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type parentOwnerInfo struct {
	os.FileInfo
	uid uint32
}

func (info parentOwnerInfo) Sys() any {
	stat := *info.FileInfo.Sys().(*syscall.Stat_t)
	stat.Uid = info.uid
	return &stat
}

func TestStickyProtectionDoesNotTrustAForeignParentOwner(t *testing.T) {
	path := t.TempDir()
	if err := os.Chmod(path, os.ModeSticky|0o777); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	foreign := uint32(1)
	if int64(foreign) == int64(os.Geteuid()) {
		foreign = 2
	}
	if err := checkParent(parentOwnerInfo{FileInfo: info, uid: foreign}); !errors.Is(err, ErrUnowned) {
		t.Fatalf("sticky parent owner can still replace entries: %v", err)
	}
	if err := checkParent(parentOwnerInfo{FileInfo: info, uid: 0}); err != nil {
		t.Fatalf("root-owned sticky parent refused: %v", err)
	}
	if err := checkParent(info); err != nil {
		t.Fatalf("this user's sticky parent refused: %v", err)
	}
}

func TestRootIsExclusiveAcrossInstancesAndProcesses(t *testing.T) {
	const envName = "FEINT_OBJECTSTORE_TEST_LOCK_ROOT"
	const readAfterClose = "FEINT_OBJECTSTORE_TEST_READ_AFTER_CLOSE"
	if path := os.Getenv(envName); path != "" {
		if os.Getenv(readAfterClose) == "1" {
			s, err := Open(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			_, body := readObject(t, s, "b", "k")
			if string(body) != "durable bytes" {
				t.Fatalf("fresh process lost persisted bytes: %q", body)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			return
		}
		if other, err := Open(t.Context(), path); !errors.Is(err, ErrLocked) || other != nil {
			t.Fatalf("child acquired locked root: %v, %v", other, err)
		}
		return
	}
	s, path := newStore(t)
	mustBucket(t, s, "b")
	mustPut(t, s, "b", "k", "durable bytes")
	if other, err := Open(t.Context(), path); !errors.Is(err, ErrLocked) || other != nil {
		t.Fatalf("second instance acquired locked root: %v, %v", other, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRootIsExclusiveAcrossInstancesAndProcesses$")
	cmd.Env = append(os.Environ(), envName+"="+path, readAfterClose+"=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child root ownership check failed: %v\n%s", err, output)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	cmd = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRootIsExclusiveAcrossInstancesAndProcesses$")
	cmd.Env = append(os.Environ(), envName+"="+path, readAfterClose+"=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fresh process could not read persisted bytes: %v\n%s", err, output)
	}
	other, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("closed store did not release process lock: %v", err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidInputsAndClosedStoreReturnErrors(t *testing.T) {
	s, _ := newStore(t)
	mustBucket(t, s, "b")
	for _, name := range []string{"", "nul\x00name", strings.Repeat("n", 256), string([]byte{255})} {
		if err := s.CreateBucket(t.Context(), name); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid bucket name %q accepted: %v", name, err)
		}
	}
	for _, key := range []string{"", strings.Repeat("k", 1025), string([]byte{255})} {
		if _, err := s.PutObject(t.Context(), "b", key, strings.NewReader("x"), PutOptions{Size: 1}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid key accepted: %v", err)
		}
		if _, reader, err := s.GetObject(t.Context(), "b", key); !errors.Is(err, ErrInvalid) || reader != nil {
			t.Fatalf("invalid read key accepted: %v", err)
		}
	}
	for _, options := range []PutOptions{
		{Size: -1}, {Size: math.MaxInt64},
		{Size: 1, ContentType: strings.Repeat("c", 65537)},
		{Size: 1, Metadata: map[string]string{"m": strings.Repeat("v", 65536)}},
		{Size: 1, Metadata: map[string]string{"m": string([]byte{255})}},
	} {
		if _, err := s.PutObject(t.Context(), "b", "k", strings.NewReader("x"), options); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid options accepted: %v", err)
		}
	}
	if _, err := s.PutObject(t.Context(), "b", "k", nil, PutOptions{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil body accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.CreateBucket(ctx, "canceled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled mutation accepted: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListBuckets(t.Context()); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed store accepted access: %v", err)
	}
}

func TestPoisonedMetadataCannotOpenAnOutsideBlob(t *testing.T) {
	s, path := newStore(t)
	mustBucket(t, s, "b")
	mustPut(t, s, "b", "k", "good")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(path, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := decodeManifest(body)
	if err != nil {
		t.Fatal(err)
	}
	obj := state.Buckets["b"].Objects["k"]
	obj.Digest = "../" + strings.Repeat("a", 61)
	state.Buckets["b"].Objects["k"] = obj
	poisoned, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "index.json"), poisoned, 0o600); err != nil {
		t.Fatal(err)
	}
	if other, err := Open(t.Context(), path); !errors.Is(err, ErrCorrupt) || other != nil {
		t.Fatalf("poisoned persisted path was admitted: %v, %v", other, err)
	}
}

func TestManifestIsClosedVersionedAndValidated(t *testing.T) {
	valid := `{"format":"feint-objectstore","version":1,"buckets":{}}`
	if _, err := decodeManifest([]byte(valid)); err != nil {
		t.Fatalf("valid manifest refused: %v", err)
	}
	for _, body := range []string{
		`null`, `{}`, `{"format":"operator","version":1,"buckets":{}}`,
		`{"format":"feint-objectstore","version":3,"buckets":{}}`,
		`{"format":"feint-objectstore","version":1,"buckets":null}`,
		`{"format":"feint-objectstore","version":1,"buckets":{"b":null}}`,
		`{"format":"feint-objectstore","version":1,"buckets":{"b":{"generation":"","objects":{}}}}`,
		`{"format":"feint-objectstore","version":1,"buckets":{"b":{"generation":"test","objects":{"k":{"digest":"../../outside","size":4}}}}}`,
		`{"format":"feint-objectstore","version":1,"buckets":{"b":{"generation":"test","objects":{"k":{"digest":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855","size":-1}}}}}`,
		`{"format":"feint-objectstore","version":1,"buckets":{},"snapshot":{}}`,
		valid + `{}`, valid + `junk`,
	} {
		if _, err := decodeManifest([]byte(body)); err == nil {
			t.Fatalf("invalid manifest admitted: %s", body)
		}
	}
}

func TestDirectorySyncFailureIsNotSuccessAndRequiresReopen(t *testing.T) {
	s, path := newStore(t)
	failedDir, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := failedDir.Close(); err != nil {
		t.Fatal(err)
	}
	lockedDir := s.dir
	s.dir = failedDir
	defer func() { s.dir = lockedDir }()
	if err := s.CreateBucket(t.Context(), "uncertain"); err == nil {
		t.Fatal("directory sync failure reported success")
	}
	if _, err := s.ListBuckets(t.Context()); err == nil {
		t.Fatal("indeterminate instance continued serving")
	}
	if err := s.CreateBucket(t.Context(), "later"); err == nil {
		t.Fatal("indeterminate instance continued mutating")
	}
	s.dir = lockedDir
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
	if err := reopened.HeadBucket(t.Context(), "uncertain"); err != nil {
		t.Fatalf("reopen did not load published metadata: %v", err)
	}
	if err := reopened.HeadBucket(t.Context(), "later"); !errors.Is(err, ErrBucketNotFound) {
		t.Fatalf("refused later mutation was published: %v", err)
	}
}

func TestFailedOpenReleasesOwnershipLock(t *testing.T) {
	s, path := newStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(path, "index.json")
	original, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(index, []byte(`{"format":"feint-objectstore","version":3,"buckets":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if broken, err := Open(t.Context(), path); !errors.Is(err, ErrCorrupt) || broken != nil {
		t.Fatalf("invalid metadata accepted: %v, %v", broken, err)
	}
	if err := os.WriteFile(index, original, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("failed open retained a process lock: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func FuzzManifest(f *testing.F) {
	f.Add([]byte(`{"format":"feint-objectstore","version":1,"buckets":{}}`))
	f.Add([]byte(`{"format":"feint-objectstore","version":1,"buckets":{"b":null}}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		state, err := decodeManifest(body)
		if err != nil {
			return
		}
		encoded, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		roundtrip, err := decodeManifest(encoded)
		if err != nil || !reflect.DeepEqual(state, roundtrip) {
			t.Fatalf("accepted manifest cannot roundtrip: %v", err)
		}
	})
}
