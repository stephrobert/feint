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
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/serialise"
)

// ErrUploadNotFound includes a session belonging to another logical key.
var ErrUploadNotFound = errors.New("upload session not found")

// Upload holds durable, unpublished parts. IDs are the enclosing inventory keys.
// Abandoning or completing a session removes references, never immutable blobs.
type Upload struct {
	Key         string            `json:"key"`
	Created     time.Time         `json:"created"`
	ContentType string            `json:"content_type"`
	Metadata    map[string]string `json:"metadata"`
	Parts       map[int]Object    `json:"parts"`
}

// PartRef selects a part and the exact content digest the caller observed.
type PartRef struct {
	Number int
	Digest string
}

// CreateUpload durably opens a session without publishing an object.
func (s *Store) CreateUpload(ctx context.Context, name, key string, options PutOptions) (string, error) {
	if err := validKey(key); err != nil {
		return "", err
	}
	if options.Size != 0 {
		return "", fmt.Errorf("%w: an upload session has no initial bytes", ErrInvalid)
	}
	if err := validMetadata(Object{ContentType: options.ContentType, Metadata: options.Metadata}); err != nil {
		return "", err
	}
	end, err := s.begin(ctx)
	if err != nil {
		return "", err
	}
	defer end()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.lookup(name); err != nil {
		return "", err
	}
	id := rand.Text()
	next := s.clone()
	b := next.Buckets[name]
	if b.Uploads == nil {
		b.Uploads = make(map[string]Upload)
	}
	b.Uploads[id] = Upload{Key: key, Created: time.Now().UTC(), ContentType: options.ContentType,
		Metadata: maps.Clone(options.Metadata), Parts: make(map[int]Object)}
	if err := s.commit(ctx, next); err != nil {
		return "", err
	}
	return id, nil
}

// ListUploads returns independent session and part metadata.
func (s *Store) ListUploads(ctx context.Context, name string) (map[string]Upload, error) {
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
	result := make(map[string]Upload, len(b.Uploads))
	for id, upload := range b.Uploads {
		result[id] = copyUpload(upload)
	}
	return result, nil
}

// ReadUpload requires both the session ID and its original logical key.
func (s *Store) ReadUpload(ctx context.Context, name, key, id string) (Upload, error) {
	end, err := s.begin(ctx)
	if err != nil {
		return Upload{}, err
	}
	defer end()
	s.mu.RLock()
	defer s.mu.RUnlock()
	upload, err := s.upload(name, key, id)
	return copyUpload(upload), err
}

// PutPart streams and verifies bytes before conditionally publishing a part.
// One session serializes part replacement, completion and abandonment.
func (s *Store) PutPart(ctx context.Context, name, key, id string, number int, body io.Reader, size int64) (Object, error) {
	if number < 1 || number > 10000 || body == nil || size < 0 || size == math.MaxInt64 {
		return Object{}, fmt.Errorf("%w: invalid upload part", ErrInvalid)
	}
	end, err := s.begin(ctx)
	if err != nil {
		return Object{}, err
	}
	defer end()
	release := serialise.Lock(s.uploadLock(name, id))
	defer release()
	s.mu.RLock()
	_, err = s.upload(name, key, id)
	s.mu.RUnlock()
	if err != nil {
		return Object{}, err
	}
	obj := Object{Size: size}
	obj.Digest, err = s.writeBlob(ctx, body, size)
	if err != nil {
		return Object{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	upload, err := s.upload(name, key, id)
	if err != nil {
		return Object{}, err
	}
	upload = copyUpload(upload)
	obj.Modified = time.Now().UTC()
	upload.Parts[number] = obj
	next := s.clone()
	next.Buckets[name].Uploads[id] = upload
	if err := s.commit(ctx, next); err != nil {
		return Object{}, err
	}
	return obj, nil
}

// AbortUpload removes a session but retains the bytes and their quota charge.
func (s *Store) AbortUpload(ctx context.Context, name, key, id string) error {
	end, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer end()
	release := serialise.Lock(s.uploadLock(name, id))
	defer release()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.upload(name, key, id); err != nil {
		return err
	}
	next := s.clone()
	delete(next.Buckets[name].Uploads, id)
	return s.commit(ctx, next)
}

// CompleteUpload assembles verified selected parts within maxBytes and commits
// the object and session removal together. A failed assembly leaves the session.
func (s *Store) CompleteUpload(ctx context.Context, name, key, id string, refs []PartRef, maxBytes int64) (_ Object, err error) {
	if len(refs) == 0 || len(refs) > 10000 || maxBytes <= 0 || maxBytes == math.MaxInt64 {
		return Object{}, fmt.Errorf("%w: invalid assembly bounds", ErrInvalid)
	}
	refs = slices.Clone(refs)
	end, err := s.begin(ctx)
	if err != nil {
		return Object{}, err
	}
	defer end()
	release := serialise.Lock(s.uploadLock(name, id))
	defer release()
	releaseKey := serialise.Lock(s.domain + "/" + name + "\x00" + key)
	defer releaseKey()
	s.mu.RLock()
	upload, err := s.upload(name, key, id)
	s.mu.RUnlock()
	if err != nil {
		return Object{}, err
	}
	var parts []Object
	var size int64
	previous := 0
	for _, ref := range refs {
		part, ok := upload.Parts[ref.Number]
		if ref.Number <= previous || !ok || ref.Digest != part.Digest {
			return Object{}, fmt.Errorf("%w: part order or observed digest does not match", ErrInvalid)
		}
		if part.Size > maxBytes-size {
			return Object{}, fmt.Errorf("%w: assembled object exceeds its byte limit", ErrInvalid)
		}
		size += part.Size
		previous = ref.Number
		parts = append(parts, part)
	}
	body := &partSequence{store: s, ctx: ctx, parts: parts}
	defer func() { err = errors.Join(err, body.Close()) }()
	obj := Object{Size: size, ContentType: upload.ContentType, Metadata: maps.Clone(upload.Metadata)}
	obj.Digest, err = s.writeBlob(ctx, body, size)
	if err != nil {
		return Object{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.upload(name, key, id); err != nil {
		return Object{}, err
	}
	obj.Modified = time.Now().UTC()
	next := s.clone()
	next.Buckets[name].Objects[key] = obj
	delete(next.Buckets[name].Uploads, id)
	if err := s.commit(ctx, next); err != nil {
		return Object{}, err
	}
	return copyObject(obj), nil
}

func (s *Store) uploadLock(name, id string) string {
	return "upload\x00" + s.domain + "/" + name + "\x00" + id
}

// Called only under mu; the returned maps remain immutable until cloned.
func (s *Store) upload(name, key, id string) (Upload, error) {
	b, err := s.lookup(name)
	if err != nil {
		return Upload{}, err
	}
	upload, ok := b.Uploads[id]
	if !ok || upload.Key != key {
		return Upload{}, ErrUploadNotFound
	}
	return upload, nil
}

func copyUpload(upload Upload) Upload {
	upload.Metadata = maps.Clone(upload.Metadata)
	upload.Parts = maps.Clone(upload.Parts)
	for number, part := range upload.Parts {
		upload.Parts[number] = copyObject(part)
	}
	return upload
}

func validUpload(id string, upload Upload) error {
	if id == "" || len(id) > 128 || strings.IndexFunc(id, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') &&
			(r < '0' || r > '9') && r != '-' && r != '_'
	}) >= 0 || upload.Created.IsZero() || upload.Created.Year() < 1 || upload.Created.Year() > 9999 ||
		upload.Parts == nil || len(upload.Parts) > 10000 {
		return fmt.Errorf("%w: invalid persisted upload session", ErrInvalid)
	}
	if err := validKey(upload.Key); err != nil {
		return err
	}
	if err := validMetadata(Object{ContentType: upload.ContentType, Metadata: upload.Metadata}); err != nil {
		return err
	}
	for number, part := range upload.Parts {
		if number < 1 || number > 10000 || !validDigest(part.Digest) || part.Size < 0 || part.Size == math.MaxInt64 ||
			part.Modified.IsZero() || part.Modified.Year() < 1 || part.Modified.Year() > 9999 {
			return fmt.Errorf("%w: invalid persisted part reference", ErrInvalid)
		}
		if err := validMetadata(part); err != nil {
			return err
		}
	}
	return nil
}

// Only one verified part file is open at a time, even for a large part list.
type partSequence struct {
	store *Store
	ctx   context.Context
	parts []Object
	file  *os.File
}

func (r *partSequence) Read(p []byte) (int, error) {
	for {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
		if r.file == nil {
			if len(r.parts) == 0 {
				return 0, io.EOF
			}
			var err error
			r.file, err = r.store.openBlob(r.ctx, r.parts[0])
			if err != nil {
				return 0, err
			}
			r.parts = r.parts[1:]
		}
		n, err := r.file.Read(p)
		if errors.Is(err, io.EOF) {
			if err := r.Close(); err != nil {
				return n, err
			}
			if n == 0 {
				continue
			}
			return n, nil
		}
		return n, err
	}
}

func (r *partSequence) Close() error {
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}
