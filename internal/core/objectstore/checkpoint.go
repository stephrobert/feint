package objectstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// ErrCheckpointNotFound means the requested durable name does not exist.
var ErrCheckpointNotFound = errors.New("object store checkpoint not found")

// beginExclusive shares Close's lifecycle boundary. TestCheckpointExclusiveGate
// holds both halves: streaming operations retain readers; resets take a writer.
// Waiting for the lock, like Close, cannot interrupt a blocked caller's Read.
func (s *Store) beginExclusive(ctx context.Context) (func(), error) {
	s.life.Lock()
	end := s.life.Unlock
	if err := ctx.Err(); err != nil {
		end()
		return nil, err
	}
	if s.closed {
		end()
		return nil, ErrClosed
	}
	s.mu.RLock()
	err := s.fault
	s.mu.RUnlock()
	if err != nil {
		end()
		return nil, err
	}
	return end, nil
}

func validCheckpointName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, c := range name {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') &&
			(c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// checkpointRoot is called only under life's writer lock. Legacy stores acquire
// the optional private directory lazily, without changing manifest versions.
func (s *Store) checkpointRoot(create bool) (*os.Root, error) {
	info, err := s.root.Lstat("checkpoints")
	if errors.Is(err, os.ErrNotExist) && create {
		if err := s.root.Mkdir("checkpoints", 0o700); err != nil {
			return nil, fmt.Errorf("create checkpoint directory: %w", err)
		}
		info, err = s.root.Lstat("checkpoints")
	}
	if err != nil {
		return nil, err
	}
	if err := private(info, true); err != nil {
		return nil, fmt.Errorf("checkpoint directory: %w", errors.Join(ErrCorrupt, err))
	}
	root, err := s.root.OpenRoot("checkpoints")
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err == nil {
		err = private(opened, true)
	}
	if err == nil && !os.SameFile(info, opened) {
		err = fmt.Errorf("%w: checkpoint directory changed while opening", ErrCorrupt)
	}
	// TestCheckpointDirectorySyncFailureIsReportedAndRetried holds the parent
	// sync, including retry after an earlier mkdir succeeded but its sync failed.
	if err == nil && create {
		err = s.dir.Sync()
		if err != nil {
			err = fmt.Errorf("sync checkpoint directory creation: %w", err)
		}
	}
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	return root, nil
}

func (s *Store) verifyManifest(ctx context.Context, state manifest) error {
	for _, b := range state.Buckets {
		for _, obj := range b.Objects {
			if err := s.verifyReference(ctx, obj); err != nil {
				return err
			}
		}
		for _, upload := range b.Uploads {
			for _, part := range upload.Parts {
				if err := s.verifyReference(ctx, part); err != nil {
					return err
				}
			}
		}
	}
	return ctx.Err()
}

func (s *Store) verifyReference(ctx context.Context, obj Object) error {
	file, err := s.openBlob(ctx, obj)
	if err != nil {
		return err
	}
	return file.Close()
}

// SaveCheckpoint atomically saves the full manifest, including unfinished upload
// sessions, after verifying all referenced bytes. Saving an existing name replaces
// it. Names are 1–64 ASCII letters, digits, underscores or hyphens. Blobs remain
// in this store's root: checkpoints are not independent exports or backup copies.
func (s *Store) SaveCheckpoint(ctx context.Context, name string) (err error) {
	if !validCheckpointName(name) {
		return fmt.Errorf("%w: unsafe checkpoint name", ErrInvalid)
	}
	end, err := s.beginExclusive(ctx)
	if err != nil {
		return err
	}
	defer end()
	if err := s.verifyManifest(ctx, s.state); err != nil {
		return err
	}
	root, err := s.checkpointRoot(true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	return s.writeCheckpoint(ctx, root, name+".json")
}

func (s *Store) writeCheckpoint(ctx context.Context, root *os.Root, name string) (err error) {
	// Never silently replace a symlink, special file or foreign-format checkpoint.
	if _, err := readNamedManifest(ctx, root, name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	body, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	if len(body) > maxManifestBytes {
		return fmt.Errorf("%w: checkpoint metadata is too large", ErrInvalid)
	}
	file, temp, err := tempFile(root)
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			err = errors.Join(err, root.Remove(temp))
		}
	}()
	_, writeErr := file.Write(body)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return fmt.Errorf("write checkpoint: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := root.Rename(temp, name); err != nil {
		return fmt.Errorf("publish checkpoint: %w", err)
	}
	published = true
	return syncDirectory(root, "checkpoint published")
}

// ResetCheckpoint verifies every referenced blob before atomically replacing the
// live manifest. Extra buckets, objects and sessions disappear; bytes are retained
// until CollectGarbage. Repeating a reset is idempotent. It waits for in-flight
// operations, so none can publish pre-reset work after the reset returns.
func (s *Store) ResetCheckpoint(ctx context.Context, name string) (err error) {
	if !validCheckpointName(name) {
		return fmt.Errorf("%w: unsafe checkpoint name", ErrInvalid)
	}
	end, err := s.beginExclusive(ctx)
	if err != nil {
		return err
	}
	defer end()
	root, err := s.checkpointRoot(false)
	if errors.Is(err, os.ErrNotExist) {
		return ErrCheckpointNotFound
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	next, err := readNamedManifest(ctx, root, name+".json")
	if errors.Is(err, os.ErrNotExist) {
		return ErrCheckpointNotFound
	}
	if err != nil {
		return err
	}
	// TestCheckpointCorruptBytesNeverPublish covers objects and upload parts.
	if err := s.verifyManifest(ctx, next); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commit(ctx, next)
}

// DeleteCheckpoint durably removes a validated named manifest, not its blobs.
// Use CollectGarbage explicitly to reclaim newly unreferenced retained bytes.
func (s *Store) DeleteCheckpoint(ctx context.Context, name string) (err error) {
	if !validCheckpointName(name) {
		return fmt.Errorf("%w: unsafe checkpoint name", ErrInvalid)
	}
	end, err := s.beginExclusive(ctx)
	if err != nil {
		return err
	}
	defer end()
	root, err := s.checkpointRoot(false)
	if errors.Is(err, os.ErrNotExist) {
		return ErrCheckpointNotFound
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	if _, err := readNamedManifest(ctx, root, name+".json"); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrCheckpointNotFound
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := root.Remove(name + ".json"); err != nil {
		return err
	}
	return syncDirectory(root, "checkpoint deleted")
}

func syncDirectory(root *os.Root, operation string) error {
	dir, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("%s; open directory for sync failed: %w", operation, err)
	}
	if err := errors.Join(dir.Sync(), dir.Close()); err != nil {
		return fmt.Errorf("%s but directory sync failed; durability is indeterminate: %w", operation, err)
	}
	return nil
}
