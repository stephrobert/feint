package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"syscall"
)

// BlobLimit returns the configured cumulative blob-byte limit, or zero for an
// unlimited store. The limit is immutable for the lifetime of an open instance.
func (s *Store) BlobLimit() int64 {
	if s.budget == nil {
		return 0
	}
	return s.budget.limit
}

// ErrQuota distinguishes a byte-budget refusal from corrupt or invalid data.
var ErrQuota = errors.New("object store blob byte budget exceeded")

type blobBudget struct {
	mu    sync.Mutex
	limit int64
	used  int64
	held  int64
}

// OpenWithLimit opens the same private format as Open with a positive cumulative
// blob-byte budget. Retained and orphan files count; hard links count once.
// Every upload reserves its full declared size before writing, even a duplicate.
// The limit is a runtime policy, not persisted metadata or a filesystem quota:
// metadata and filesystem overhead are excluded. Existing over-limit stores are
// refused without deleting data. Open retains its unlimited library behavior.
func OpenWithLimit(ctx context.Context, path string, maxBlobBytes int64) (*Store, error) {
	if maxBlobBytes <= 0 {
		return nil, fmt.Errorf("%w: blob byte budget must be positive", ErrInvalid)
	}
	s, err := Open(ctx, path)
	if err != nil {
		return nil, err
	}
	used, err := s.retainedBytes(ctx, maxBlobBytes)
	if err != nil {
		return nil, errors.Join(err, s.Close())
	}
	s.budget = &blobBudget{limit: maxBlobBytes, used: used}
	return s, nil
}

func (s *Store) retainedBytes(ctx context.Context, limit int64) (used int64, err error) {
	dir, err := s.blobs.Open(".")
	if err != nil {
		return 0, fmt.Errorf("open blobs for budget inventory: %w", err)
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	seen := make(map[string]bool)
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		entries, readErr := dir.ReadDir(128)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			file, err := openPrivate(s.blobs, entry.Name())
			if err != nil {
				return 0, fmt.Errorf("inspect retained blob: %w", errors.Join(ErrCorrupt, err))
			}
			info, statErr := file.Stat()
			closeErr := file.Close()
			if err := errors.Join(statErr, closeErr); err != nil {
				return 0, fmt.Errorf("stat retained blob: %w", err)
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				return 0, fmt.Errorf("%w: retained blob has no file identity", ErrCorrupt)
			}
			identity := fmt.Sprintf("%v:%v", stat.Dev, stat.Ino)
			if seen[identity] {
				continue
			}
			seen[identity] = true
			if info.Size() < 0 || info.Size() > limit-used {
				return 0, ErrQuota
			}
			used += info.Size()
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return 0, fmt.Errorf("read blob budget inventory: %w", readErr)
			}
			return used, nil
		}
	}
}

func (s *Store) reserveBlob(size int64) error {
	if s.budget == nil {
		return nil
	}
	b := s.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	if size > b.limit-b.used-b.held {
		return ErrQuota
	}
	b.held += size
	return nil
}

func (s *Store) settleBlob(reserved, retained int64) {
	if s.budget == nil {
		return
	}
	b := s.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	b.held -= reserved
	b.used += retained
}
