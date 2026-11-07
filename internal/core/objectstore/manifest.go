package objectstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"unicode/utf8"
)

const (
	storeFormat      = "feint-objectstore"
	storeVersion     = 4
	maxManifestBytes = 64 << 20
)

func readManifest(ctx context.Context, root *os.Root) (_ manifest, err error) {
	return readNamedManifest(ctx, root, "index.json")
}

func readNamedManifest(ctx context.Context, root *os.Root, name string) (_ manifest, err error) {
	file, err := openPrivate(root, name)
	if err != nil {
		return manifest{}, fmt.Errorf("read storage ownership metadata: %w", errors.Join(ErrUnowned, err))
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	body, err := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: file}, maxManifestBytes+1))
	if err != nil {
		return manifest{}, fmt.Errorf("read storage metadata: %w", err)
	}
	if len(body) > maxManifestBytes {
		return manifest{}, fmt.Errorf("%w: metadata exceeds %d bytes", ErrCorrupt, maxManifestBytes)
	}
	return decodeManifest(body)
}

func decodeManifest(body []byte) (manifest, error) {
	var state manifest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return manifest{}, fmt.Errorf("decode storage metadata: %w", errors.Join(ErrCorrupt, err))
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return manifest{}, fmt.Errorf("%w: trailing storage metadata", ErrCorrupt)
	}
	if state.Format != storeFormat {
		return manifest{}, ErrUnowned
	}
	if state.Version < 1 || state.Version > storeVersion || state.Buckets == nil {
		return manifest{}, fmt.Errorf("%w: unsupported version or missing buckets", ErrCorrupt)
	}
	for name, b := range state.Buckets {
		if err := validName(name); err != nil {
			return manifest{}, errors.Join(ErrCorrupt, err)
		}
		if b == nil || b.Objects == nil || b.Generation == "" || len(b.Generation) > 128 || !utf8.ValidString(b.Generation) {
			return manifest{}, fmt.Errorf("%w: invalid bucket metadata", ErrCorrupt)
		}
		if state.Version == 1 && b.Metadata != nil {
			return manifest{}, fmt.Errorf("%w: bucket attributes require metadata version 2", ErrCorrupt)
		}
		if err := validMetadata(Object{Metadata: b.Metadata}); err != nil {
			return manifest{}, errors.Join(ErrCorrupt, err)
		}
		if state.Version < 4 && len(b.Uploads) != 0 {
			return manifest{}, fmt.Errorf("%w: upload sessions require metadata version 4", ErrCorrupt)
		}
		for id, upload := range b.Uploads {
			if err := validUpload(id, upload); err != nil {
				return manifest{}, errors.Join(ErrCorrupt, err)
			}
		}
		for key, obj := range b.Objects {
			if state.Version < 3 && !obj.Modified.IsZero() {
				return manifest{}, fmt.Errorf("%w: object modification dates require metadata version 3", ErrCorrupt)
			}
			if !obj.Modified.IsZero() && (obj.Modified.Year() < 1 || obj.Modified.Year() > 9999) {
				return manifest{}, fmt.Errorf("%w: invalid object modification date", ErrCorrupt)
			}
			if err := validKey(key); err != nil {
				return manifest{}, errors.Join(ErrCorrupt, err)
			}
			if !validDigest(obj.Digest) || obj.Size < 0 || obj.Size == math.MaxInt64 {
				return manifest{}, fmt.Errorf("%w: invalid object reference", ErrCorrupt)
			}
			if err := validMetadata(obj); err != nil {
				return manifest{}, errors.Join(ErrCorrupt, err)
			}
		}
	}
	return state, nil
}

// commit runs under mu. Upload I/O is outside it; the only serialized I/O is
// the small metadata transaction. After a rename, a directory-sync failure is
// indeterminate durability, not success: stop serving mutations until reopen.
func (s *Store) commit(ctx context.Context, next manifest) (err error) {
	if s.fault != nil {
		return s.fault
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	body, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("encode storage metadata: %w", err)
	}
	if len(body) > maxManifestBytes {
		return fmt.Errorf("%w: metadata exceeds %d bytes", ErrInvalid, maxManifestBytes)
	}
	file, temp, err := tempFile(s.root)
	if err != nil {
		return err
	}
	renamed := false
	defer func() {
		if !renamed {
			err = errors.Join(err, s.root.Remove(temp))
		}
	}()
	_, writeErr := file.Write(body)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return fmt.Errorf("write storage metadata: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.root.Rename(temp, "index.json"); err != nil {
		return fmt.Errorf("publish storage metadata: %w", err)
	}
	renamed = true
	s.state = next
	if err := s.dir.Sync(); err != nil {
		s.fault = fmt.Errorf("metadata published but directory sync failed; close and reopen required: %w", err)
		return s.fault
	}
	return nil
}
