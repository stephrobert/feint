package objectstore

import (
	"context"
	"fmt"
	"maps"
	"math"
)

// BucketUsage describes one consistent published inventory, excluding retained
// orphan blobs and unpublished multipart parts. Metadata is detached.
type BucketUsage struct {
	Metadata map[string]string
	Size     int64
}

// Usage returns logical published bytes per bucket under one metadata read lock.
// Duplicate references count separately: this is usage, not physical disk space.
func (s *Store) Usage(ctx context.Context) (map[string]BucketUsage, error) {
	end, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer end()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]BucketUsage, len(s.state.Buckets))
	for name, bucket := range s.state.Buckets {
		usage := BucketUsage{Metadata: maps.Clone(bucket.Metadata)}
		for _, object := range bucket.Objects {
			if object.Size > math.MaxInt64-usage.Size {
				return nil, fmt.Errorf("%w: logical bucket usage exceeds representable bytes", ErrInvalid)
			}
			usage.Size += object.Size
		}
		out[name] = usage
	}
	return out, nil
}

// BucketMetadata returns a detached copy of a bucket's opaque UTF-8 attributes.
// A legacy bucket with no attributes returns an empty map.
func (s *Store) BucketMetadata(ctx context.Context, name string) (map[string]string, error) {
	end, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer end()
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, err := s.lookup(name)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(b.Metadata))
	maps.Copy(out, b.Metadata)
	return out, nil
}

// ListBucketMetadata returns detached attributes for one consistent inventory.
// Deletion cannot race a per-bucket lookup into a partially read result.
func (s *Store) ListBucketMetadata(ctx context.Context) (map[string]map[string]string, error) {
	end, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer end()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]map[string]string, len(s.state.Buckets))
	for name, b := range s.state.Buckets {
		metadata := make(map[string]string, len(b.Metadata))
		maps.Copy(metadata, b.Metadata)
		out[name] = metadata
	}
	return out, nil
}

// SetBucketMetadata durably replaces one opaque attribute, preserving the
// others. The whole bucket's raw UTF-8 metadata is bounded to 65,536 bytes.
// Metadata version 1 is read without changes and upgraded on the next write;
// version 1 readers cannot read the resulting version 2 index.
func (s *Store) SetBucketMetadata(ctx context.Context, name, key, value string) error {
	end, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer end()
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.lookup(name)
	if err != nil {
		return err
	}
	metadata := make(map[string]string, len(b.Metadata)+1)
	maps.Copy(metadata, b.Metadata)
	metadata[key] = value
	if err := validMetadata(Object{Metadata: metadata}); err != nil {
		return err
	}
	next := s.clone()
	next.Buckets[name].Metadata = metadata
	return s.commit(ctx, next)
}
