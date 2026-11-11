package objectstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// CollectGarbage explicitly deletes only unreferenced owned blob files.
// References in the live manifest and every durable checkpoint are protected,
// including upload parts. Active operations/reservations finish before inventory.
// Every entry is validated before any deletion; unsafe names, permissions,
// symlinks or corrupt checkpoints fail closed. Cancellation or I/O failure during
// deletion may leave a partially collected inventory; quota accounting is updated
// for successful unlinks, and retry is safe. Open returned readers keep their
// descriptors valid; their unlinked bytes are outside the retained-file budget.
func (s *Store) CollectGarbage(ctx context.Context) (err error) {
	end, err := s.beginExclusive(ctx)
	if err != nil {
		return err
	}
	defer end()
	refs := make(map[string]bool)
	if err := s.protectManifest(ctx, s.state, refs); err != nil {
		return err
	}
	if err := s.protectCheckpoints(ctx, refs); err != nil {
		return err
	}
	entries, err := s.blobInventory(ctx)
	if err != nil {
		return err
	}
	changed := false
	defer func() {
		if s.budget != nil {
			seen := make(map[string]bool)
			var used int64
			for _, entry := range entries {
				if !entry.removed && !seen[entry.identity] {
					seen[entry.identity] = true
					used += entry.size
				}
			}
			s.budget.mu.Lock()
			s.budget.used = used
			s.budget.mu.Unlock()
		}
		if changed {
			err = errors.Join(err, syncDirectory(s.blobs, "garbage collected"))
		}
	}()
	for i := range entries {
		entry := &entries[i]
		// TestCheckpointProtectsGarbageAndReleasesBudget holds this retention guard.
		if refs[entry.name] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.blobs.Remove(entry.name); err != nil {
			return fmt.Errorf("collect blob %s: %w", entry.name, err)
		}
		entry.removed = true
		changed = true
	}
	return nil
}

func (s *Store) protectManifest(ctx context.Context, state manifest, refs map[string]bool) error {
	if err := s.verifyManifest(ctx, state); err != nil {
		return err
	}
	for _, b := range state.Buckets {
		for _, obj := range b.Objects {
			refs[obj.Digest] = true
		}
		for _, upload := range b.Uploads {
			for _, part := range upload.Parts {
				refs[part.Digest] = true
			}
		}
	}
	return nil
}

func (s *Store) protectCheckpoints(ctx context.Context, refs map[string]bool) (err error) {
	root, err := s.checkpointRoot(false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if validPendingName(name) {
			file, err := openPrivate(root, name)
			if err != nil {
				return errors.Join(ErrCorrupt, err)
			}
			if err := file.Close(); err != nil {
				return err
			}
			continue // Unpublished crash debris is not a durable checkpoint.
		}
		if !strings.HasSuffix(name, ".json") || !validCheckpointName(strings.TrimSuffix(name, ".json")) {
			return fmt.Errorf("%w: unsafe persisted checkpoint filename", ErrCorrupt)
		}
		state, err := readNamedManifest(ctx, root, name)
		if err != nil {
			return err
		}
		if err := s.protectManifest(ctx, state, refs); err != nil {
			return err
		}
	}
	return ctx.Err()
}

type retainedBlob struct {
	name     string
	identity string
	size     int64
	removed  bool
}

func (s *Store) blobInventory(ctx context.Context) (_ []retainedBlob, err error) {
	dir, err := s.blobs.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	inventory := make([]retainedBlob, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := entry.Name()
		// TestGarbageCollectionRejectsUnsafeInventory includes an accepting half.
		if !validDigest(name) && !validPendingName(name) {
			return nil, fmt.Errorf("%w: unsafe persisted blob filename", ErrCorrupt)
		}
		file, err := openPrivate(s.blobs, name)
		if err != nil {
			return nil, errors.Join(ErrCorrupt, err)
		}
		info, statErr := file.Stat()
		if err := errors.Join(statErr, file.Close()); err != nil {
			return nil, err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || info.Size() < 0 {
			return nil, fmt.Errorf("%w: invalid blob file identity or size", ErrCorrupt)
		}
		inventory = append(inventory, retainedBlob{
			name: name, identity: fmt.Sprintf("%v:%v", stat.Dev, stat.Ino), size: info.Size(),
		})
	}
	return inventory, nil
}

func validPendingName(name string) bool {
	const prefix = ".pending-"
	if !strings.HasPrefix(name, prefix) || len(name) != len(prefix)+26 {
		return false
	}
	for _, c := range name[len(prefix):] {
		if (c < 'A' || c > 'Z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}
