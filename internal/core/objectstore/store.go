// Package objectstore persists neutral bucket metadata and real object bytes.
//
// Blobs are immutable and addressed by SHA-256, never by a bucket or object key.
// Mutations publish metadata only after the blob is durable. One Store owns a
// private directory exclusively; Close releases it so Open can load it again.
// This package provides no S3 protocol, resource snapshots, checkpoints or GC.
package objectstore

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"slices"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/stephrobert/feint/internal/core/serialise"
)

// Errors distinguish neutral store outcomes from provider protocol errors.
var (
	ErrBucketExists   = errors.New("bucket already exists")
	ErrBucketNotFound = errors.New("bucket not found")
	ErrBucketNotEmpty = errors.New("bucket is not empty")
	ErrObjectNotFound = errors.New("object not found")
	ErrInvalid        = errors.New("invalid object store input")
	ErrCorrupt        = errors.New("object store data is corrupt")
	ErrUnowned        = errors.New("directory is not an owned object store")
	ErrLocked         = errors.New("object store is already open")
	ErrClosed         = errors.New("object store is closed")
)

// Object describes stored bytes. Digest is lowercase SHA-256, not an S3 ETag.
// Metadata is copied on intake and on every return.
type Object struct {
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	ContentType string            `json:"content_type"`
	Metadata    map[string]string `json:"metadata"`
	// Modified is the persisted publication time, zero for legacy references.
	Modified time.Time `json:"modified,omitzero"`
}

// PutOptions declares the exact decoded byte count and application metadata.
// Size must be nonnegative; callers must impose their own quota before upload.
type PutOptions struct {
	Size        int64
	ContentType string
	Metadata    map[string]string
}

type bucket struct {
	Generation string            `json:"generation"`
	Objects    map[string]Object `json:"objects"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Uploads    map[string]Upload `json:"uploads,omitempty"`
}

type manifest struct {
	Format  string             `json:"format"`
	Version int                `json:"version"`
	Buckets map[string]*bucket `json:"buckets"`
}

// Store is safe for concurrent use. Uploads to different keys run independently;
// writes and deletes to the same key are serialized. Metadata commits are short
// exclusive transactions. Close waits for in-flight operations, not for the
// independent readers returned by GetObject.
type Store struct {
	life   sync.RWMutex
	mu     sync.RWMutex
	root   *os.Root
	blobs  *os.Root
	dir    *os.File
	state  manifest
	closed bool
	fault  error
	domain string
	budget *blobBudget
}

// Close releases directory handles and the exclusive process lock.
// Returned object readers remain valid until the caller closes them.
func (s *Store) Close() error {
	s.life.Lock()
	defer s.life.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return errors.Join(s.blobs.Close(), s.root.Close(), s.dir.Close())
}

func (s *Store) begin(ctx context.Context) (func(), error) {
	s.life.RLock()
	if err := ctx.Err(); err != nil {
		s.life.RUnlock()
		return nil, err
	}
	if s.closed {
		s.life.RUnlock()
		return nil, ErrClosed
	}
	s.mu.RLock()
	err := s.fault
	s.mu.RUnlock()
	if err != nil {
		s.life.RUnlock()
		return nil, err
	}
	return s.life.RUnlock, nil
}

// CreateBucket durably creates a logical bucket. Its name never becomes a path.
func (s *Store) CreateBucket(ctx context.Context, name string) error {
	return s.CreateBucketWithMetadata(ctx, name, nil)
}

// CreateBucketWithMetadata atomically creates a bucket with detached, opaque
// UTF-8 attributes. Attributes share SetBucketMetadata's 65,536-byte bound.
func (s *Store) CreateBucketWithMetadata(ctx context.Context, name string, metadata map[string]string) error {
	if err := validName(name); err != nil {
		return err
	}
	if err := validMetadata(Object{Metadata: metadata}); err != nil {
		return err
	}
	end, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer end()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.state.Buckets[name]; ok {
		return ErrBucketExists
	}
	next := s.clone()
	next.Buckets[name] = &bucket{Generation: rand.Text(), Objects: make(map[string]Object), Metadata: maps.Clone(metadata)}
	return s.commit(ctx, next)
}

// HeadBucket reports whether a logical bucket exists.
func (s *Store) HeadBucket(ctx context.Context, name string) error {
	end, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer end()
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, err = s.lookup(name)
	return err
}

// ListBuckets returns logical names in lexical order.
func (s *Store) ListBuckets(ctx context.Context) ([]string, error) {
	end, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer end()
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.state.Buckets))
	for name := range s.state.Buckets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// DeleteBucket durably removes an empty bucket. It never removes blob files.
// An unfinished new upload cannot resurrect a deleted or recreated bucket.
func (s *Store) DeleteBucket(ctx context.Context, name string) error {
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
	if len(b.Objects) != 0 || len(b.Uploads) != 0 {
		return ErrBucketNotEmpty
	}
	next := s.clone()
	delete(next.Buckets, name)
	return s.commit(ctx, next)
}

// PutObject streams exactly options.Size bytes and publishes their metadata.
// Cancellation is checked between reads and before publication. A caller must
// arrange to unblock a reader that can block inside Read on cancellation.
func (s *Store) PutObject(ctx context.Context, name, key string, body io.Reader, options PutOptions) (Object, error) {
	if err := validName(name); err != nil {
		return Object{}, err
	}
	if err := validKey(key); err != nil {
		return Object{}, err
	}
	if body == nil || options.Size < 0 || options.Size == math.MaxInt64 {
		return Object{}, fmt.Errorf("%w: a reader and exact representable size are required", ErrInvalid)
	}
	obj := Object{Size: options.Size, ContentType: options.ContentType, Metadata: options.Metadata}
	if err := validMetadata(obj); err != nil {
		return Object{}, err
	}
	obj.Metadata = maps.Clone(obj.Metadata)
	end, err := s.begin(ctx)
	if err != nil {
		return Object{}, err
	}
	defer end()
	release := serialise.Lock(s.domain + "/" + name + "\x00" + key)
	defer release()
	s.mu.RLock()
	b, err := s.lookup(name)
	var generation string
	if err == nil {
		generation = b.Generation
	}
	s.mu.RUnlock()
	if err != nil {
		return Object{}, err
	}
	obj.Digest, err = s.writeBlob(ctx, body, obj.Size)
	if err != nil {
		return Object{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err = s.lookup(name)
	if err != nil {
		return Object{}, err
	}
	if b.Generation != generation {
		return Object{}, fmt.Errorf("%w: bucket was recreated during upload", ErrBucketNotFound)
	}
	obj.Modified = time.Now().UTC()
	next := s.clone()
	next.Buckets[name].Objects[key] = obj
	if err := s.commit(ctx, next); err != nil {
		return Object{}, err
	}
	return copyObject(obj), nil
}

// GetObject verifies the complete blob's size and digest before returning a
// seekable reader positioned at byte zero. It never returns missing or corrupt bytes
// as a successful empty object. The caller must close the reader.
func (s *Store) GetObject(ctx context.Context, name, key string) (Object, io.ReadSeekCloser, error) {
	if err := validKey(key); err != nil {
		return Object{}, nil, err
	}
	end, err := s.begin(ctx)
	if err != nil {
		return Object{}, nil, err
	}
	defer end()
	s.mu.RLock()
	b, err := s.lookup(name)
	var obj Object
	if err == nil {
		var ok bool
		obj, ok = b.Objects[key]
		if !ok {
			err = ErrObjectNotFound
		}
	}
	s.mu.RUnlock()
	if err != nil {
		return Object{}, nil, err
	}
	file, err := s.openBlob(ctx, obj)
	if err != nil {
		return Object{}, nil, err
	}
	return copyObject(obj), &blobReader{ctx: ctx, file: file}, nil
}

// HeadObject returns metadata only after verifying the corresponding blob.
func (s *Store) HeadObject(ctx context.Context, name, key string) (Object, error) {
	obj, reader, err := s.GetObject(ctx, name, key)
	if err != nil {
		return Object{}, err
	}
	if err := reader.Close(); err != nil {
		return Object{}, fmt.Errorf("close verified blob: %w", err)
	}
	return obj, nil
}

// ListObjects returns an independent metadata inventory, not a verification of
// every blob. GetObject or HeadObject performs byte verification.
func (s *Store) ListObjects(ctx context.Context, name string) (map[string]Object, error) {
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
	result := make(map[string]Object, len(b.Objects))
	for key, obj := range b.Objects {
		result[key] = copyObject(obj)
	}
	return result, nil
}

// DeleteObject removes a metadata reference, retaining its immutable blob.
// Protocol adapters may choose idempotent deletion; this layer reports absence.
func (s *Store) DeleteObject(ctx context.Context, name, key string) error {
	if err := validKey(key); err != nil {
		return err
	}
	end, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer end()
	release := serialise.Lock(s.domain + "/" + name + "\x00" + key)
	defer release()
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.lookup(name)
	if err != nil {
		return err
	}
	if _, ok := b.Objects[key]; !ok {
		return ErrObjectNotFound
	}
	next := s.clone()
	delete(next.Buckets[name].Objects, key)
	return s.commit(ctx, next)
}

// DeleteObjects removes references in one metadata transaction, retaining blobs.
// Missing keys are harmless. Validation or a failed commit never publishes a
// partially deleted inventory; indeterminate durability still faults the store.
func (s *Store) DeleteObjects(ctx context.Context, name string, keys []string) error {
	if len(keys) == 0 {
		return fmt.Errorf("%w: deletion requires at least one key", ErrInvalid)
	}
	keys = slices.Clone(keys)
	for _, key := range keys {
		if err := validKey(key); err != nil {
			return err
		}
	}
	end, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer end()
	slices.Sort(keys)
	keys = slices.Compact(keys)
	for _, key := range keys {
		release := serialise.Lock(s.domain + "/" + name + "\x00" + key)
		defer release()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.lookup(name); err != nil {
		return err
	}
	next := s.clone()
	for _, key := range keys {
		delete(next.Buckets[name].Objects, key)
	}
	return s.commit(ctx, next)
}

func (s *Store) lookup(name string) (*bucket, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	if s.fault != nil {
		return nil, s.fault
	}
	b, ok := s.state.Buckets[name]
	if !ok {
		return nil, ErrBucketNotFound
	}
	return b, nil
}

func (s *Store) clone() manifest {
	next := manifest{Format: s.state.Format, Version: storeVersion, Buckets: make(map[string]*bucket)}
	for name, b := range s.state.Buckets {
		next.Buckets[name] = &bucket{Generation: b.Generation, Objects: maps.Clone(b.Objects),
			Metadata: maps.Clone(b.Metadata), Uploads: maps.Clone(b.Uploads)}
	}
	return next
}

func copyObject(obj Object) Object {
	obj.Metadata = maps.Clone(obj.Metadata)
	return obj
}

func validName(name string) error {
	if name == "" || len(name) > 255 || !utf8.ValidString(name) {
		return fmt.Errorf("%w: bucket name must be 1-255 UTF-8 bytes", ErrInvalid)
	}
	for _, r := range name {
		if r == 0 {
			return fmt.Errorf("%w: bucket name contains NUL", ErrInvalid)
		}
	}
	return nil
}

func validKey(key string) error {
	if key == "" || len(key) > 1024 || !utf8.ValidString(key) {
		return fmt.Errorf("%w: object key must be 1-1024 UTF-8 bytes", ErrInvalid)
	}
	return nil
}

func validMetadata(obj Object) error {
	size := len(obj.ContentType)
	if !utf8.ValidString(obj.ContentType) {
		return fmt.Errorf("%w: content type is not UTF-8", ErrInvalid)
	}
	for key, value := range obj.Metadata {
		if !utf8.ValidString(key) || !utf8.ValidString(value) {
			return fmt.Errorf("%w: metadata is not UTF-8", ErrInvalid)
		}
		size += len(key) + len(value)
		if size > 64<<10 {
			return fmt.Errorf("%w: metadata exceeds 65536 bytes", ErrInvalid)
		}
	}
	if size > 64<<10 {
		return fmt.Errorf("%w: metadata exceeds 65536 bytes", ErrInvalid)
	}
	return nil
}
