package objectstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"syscall"
)

// Open creates a new private store at path, or reopens a marked, valid store.
// The parent must exist and prevent replacement by other UIDs. Existing
// unmarked directories are never adopted. Existing roots, metadata and blobs
// must be private and owned by this user.
// A root symlink is refused. A directory lock excludes other Store instances
// and processes on the supported Linux and macOS local filesystems.
func Open(ctx context.Context, path string) (result *Store, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, fmt.Errorf("%w: storage root must be explicit", ErrInvalid)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve storage root: %w", err)
	}
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("open storage parent: %w", err)
	}
	var s *Store
	defer func() {
		err = errors.Join(err, parent.Close())
		if err != nil && s != nil {
			if s.blobs != nil {
				err = errors.Join(err, s.blobs.Close())
			}
			err = errors.Join(err, s.root.Close())
			if s.dir != nil {
				err = errors.Join(err, s.dir.Close())
			}
			result = nil
		}
	}()
	parentInfo, err := parent.Stat(".")
	if err != nil {
		return nil, fmt.Errorf("inspect storage parent: %w", err)
	}
	if err := checkParent(parentInfo); err != nil {
		return nil, err
	}
	name := filepath.Base(path)
	created := false
	if err := parent.Mkdir(name, 0o700); err == nil {
		created = true
	} else if !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("create storage root: %w", err)
	}
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("inspect storage root: %w", err)
	}
	if err := private(info, true); err != nil {
		return nil, fmt.Errorf("storage root: %w", errors.Join(ErrUnowned, err))
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, fmt.Errorf("open storage root: %w", err)
	}
	s = &Store{root: root, domain: rand.Text()}
	s.dir, err = root.OpenFile(".", os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open storage lock directory: %w", err)
	}
	opened, err := s.dir.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened storage root: %w", err)
	}
	if !os.SameFile(info, opened) {
		return nil, fmt.Errorf("%w: storage root changed while opening", ErrUnowned)
	}
	if err := private(opened, true); err != nil {
		return nil, fmt.Errorf("opened storage root: %w", errors.Join(ErrUnowned, err))
	}
	if err := syscall.Flock(int(s.dir.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("lock storage root: %w", err)
	}
	if created {
		if err := root.Mkdir("blobs", 0o700); err != nil {
			return nil, fmt.Errorf("create blob directory: %w", err)
		}
		s.state = manifest{Format: storeFormat, Version: 1, Buckets: make(map[string]*bucket)}
		if err := s.commit(ctx, s.state); err != nil {
			return nil, err
		}
		parentDir, err := parent.Open(".")
		if err != nil {
			return nil, fmt.Errorf("open storage parent for sync: %w", err)
		}
		if err := errors.Join(parentDir.Sync(), parentDir.Close()); err != nil {
			return nil, fmt.Errorf("sync new storage root: %w", err)
		}
	} else {
		s.state, err = readManifest(ctx, root)
		if err != nil {
			return nil, err
		}
	}
	info, err = root.Lstat("blobs")
	if err != nil {
		return nil, fmt.Errorf("inspect blob directory: %w", errors.Join(ErrCorrupt, err))
	}
	if err := private(info, true); err != nil {
		return nil, fmt.Errorf("blob directory: %w", errors.Join(ErrCorrupt, err))
	}
	s.blobs, err = root.OpenRoot("blobs")
	if err != nil {
		return nil, fmt.Errorf("open blob directory: %w", err)
	}
	opened, err = s.blobs.Stat(".")
	if err != nil {
		return nil, fmt.Errorf("inspect opened blob directory: %w", err)
	}
	if !os.SameFile(info, opened) {
		return nil, fmt.Errorf("%w: blob directory changed while opening", ErrCorrupt)
	}
	if err := private(opened, true); err != nil {
		return nil, fmt.Errorf("opened blob directory: %w", errors.Join(ErrCorrupt, err))
	}
	for _, b := range s.state.Buckets {
		for _, obj := range b.Objects {
			file, err := s.openBlob(ctx, obj)
			if err != nil {
				return nil, err
			}
			if err := file.Close(); err != nil {
				return nil, fmt.Errorf("close startup blob verification: %w", err)
			}
		}
	}
	return s, nil
}

// TestUnsafeParentIsRefusedBeforeCreation holds the Mkdir-to-open substitution
// boundary: a trusted parent must prevent another UID from replacing entries.
func checkParent(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Uid != 0 && int64(stat.Uid) != int64(os.Geteuid())) {
		return fmt.Errorf("%w: storage parent must belong to this user or root", ErrUnowned)
	}
	if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
		return fmt.Errorf("%w: writable storage parent requires sticky protection", ErrUnowned)
	}
	return nil
}

func private(info os.FileInfo, directory bool) error {
	if (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return errors.New("expected a real directory or regular file, not a symlink or special file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("group or other permissions are not private")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(stat.Uid) != int64(os.Geteuid()) {
		return errors.New("filesystem entry is not owned by the current user")
	}
	return nil
}

func openPrivate(root *os.Root, name string) (*os.File, error) {
	// Root resolves internal symlinks before opening: O_NOFOLLOW alone does
	// not hold TestRootOwnershipPermissionsAndSymlinksAreRefused.
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if err := private(info, false); err != nil {
		return nil, err
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err == nil {
		err = private(opened, false)
	}
	if err == nil && !os.SameFile(info, opened) {
		err = errors.New("filesystem entry changed while opening")
	}
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func tempFile(root *os.Root) (*os.File, string, error) {
	name := ".pending-" + rand.Text()
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, "", fmt.Errorf("create private temporary file: %w", err)
	}
	return file, name, nil
}

func (s *Store) writeBlob(ctx context.Context, body io.Reader, size int64) (_ string, err error) {
	file, temp, err := tempFile(s.blobs)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, s.blobs.Remove(temp)) }()
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(&contextReader{ctx: ctx, reader: body}, size+1))
	if copyErr == nil && n != size {
		copyErr = fmt.Errorf("%w: expected %d bytes, received %d", ErrInvalid, size, n)
	}
	if copyErr == nil {
		copyErr = ctx.Err()
	}
	if copyErr == nil {
		copyErr = file.Sync()
	}
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return "", fmt.Errorf("write blob: %w", err)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if err := s.blobs.Link(temp, digest); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("publish blob: %w", err)
		}
		existing, err := s.openBlob(ctx, Object{Digest: digest, Size: size})
		if err != nil {
			return "", err
		}
		if err := existing.Close(); err != nil {
			return "", fmt.Errorf("close existing blob: %w", err)
		}
	}
	dir, err := s.blobs.Open(".")
	if err != nil {
		return "", fmt.Errorf("open blob directory for sync: %w", err)
	}
	if err := errors.Join(dir.Sync(), dir.Close()); err != nil {
		return "", fmt.Errorf("sync published blob: %w", err)
	}
	return digest, nil
}

func (s *Store) openBlob(ctx context.Context, obj Object) (_ *os.File, err error) {
	if !validDigest(obj.Digest) || obj.Size < 0 || obj.Size == math.MaxInt64 {
		return nil, fmt.Errorf("%w: invalid blob reference", ErrCorrupt)
	}
	file, err := openPrivate(s.blobs, obj.Digest)
	if err != nil {
		return nil, fmt.Errorf("open blob %s: %w", obj.Digest, errors.Join(ErrCorrupt, err))
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, file.Close())
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat blob: %w", err)
	}
	if info.Size() != obj.Size {
		return nil, fmt.Errorf("%w: blob %s has size %d, expected %d", ErrCorrupt, obj.Digest, info.Size(), obj.Size)
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(&contextReader{ctx: ctx, reader: file}, obj.Size+1))
	if err != nil {
		return nil, fmt.Errorf("verify blob: %w", err)
	}
	if n != obj.Size || hex.EncodeToString(hash.Sum(nil)) != obj.Digest {
		return nil, fmt.Errorf("%w: blob %s digest does not match bytes", ErrCorrupt, obj.Digest)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind verified blob: %w", err)
	}
	return file, nil
}

func validDigest(digest string) bool {
	if len(digest) != sha256.Size*2 {
		return false
	}
	for _, c := range digest {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// TestReturnedReaderHonorsCancellationThroughCopy forbids embedding File:
// its promoted WriteTo would bypass the context-aware Read.
type blobReader struct {
	ctx  context.Context
	file *os.File
}

func (r *blobReader) Read(p []byte) (int, error) {
	return (&contextReader{ctx: r.ctx, reader: r.file}).Read(p)
}

func (r *blobReader) Close() error {
	return r.file.Close()
}
