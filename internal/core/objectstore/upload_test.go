package objectstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestUploadPartsResumeAndCompleteWithoutAliasing(t *testing.T) {
	s, path := newStore(t)
	mustBucket(t, s, "bucket")
	metadata := map[string]string{"purpose": "resume"}
	id, err := s.CreateUpload(t.Context(), "bucket", "key", PutOptions{ContentType: "application/octet-stream", Metadata: metadata})
	if err != nil {
		t.Fatal(err)
	}
	metadata["purpose"] = "caller changed"
	a, err := s.PutPart(t.Context(), "bucket", "key", id, 1, bytes.NewBufferString("abc"), 3)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.ReadUpload(t.Context(), "bucket", "key", id)
	if err != nil || before.Metadata["purpose"] != "resume" {
		t.Fatal(before, err)
	}
	changed, err := s.ListUploads(t.Context(), "bucket")
	if err != nil {
		t.Fatal(err)
	}
	delete(changed[id].Parts, 1)
	changed[id].Metadata["purpose"] = "aliased"
	current, err := s.ReadUpload(t.Context(), "bucket", "key", id)
	if err != nil || current.Metadata["purpose"] != "resume" || len(current.Parts) != 1 {
		t.Fatal("returned inventory aliases the live session:", current, err)
	}
	if _, err := s.HeadObject(t.Context(), "bucket", "key"); !errors.Is(err, ErrObjectNotFound) {
		t.Fatal("uncompleted session published an object:", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	after, err := s.ReadUpload(t.Context(), "bucket", "key", id)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("restart changed durable upload state:", before, after, err)
	}
	b, err := s.PutPart(t.Context(), "bucket", "key", id, 2, bytes.NewBufferString("defg"), 4)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := s.CompleteUpload(t.Context(), "bucket", "key", id, []PartRef{{1, a.Digest}, {2, b.Digest}}, 7)
	if err != nil || obj.Size != 7 || obj.Metadata["purpose"] != "resume" {
		t.Fatal(obj, err)
	}
	_, body, err := s.GetObject(t.Context(), "bucket", "key")
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(body)
	if err := errors.Join(readErr, body.Close()); err != nil || string(got) != "abcdefg" {
		t.Fatal("assembled wrong bytes:", string(got), err)
	}
	if _, err := s.ReadUpload(t.Context(), "bucket", "key", id); !errors.Is(err, ErrUploadNotFound) {
		t.Fatal("completed session survived:", err)
	}
}

func TestUploadCompletionRefusalsPreserveSessionAndOldObject(t *testing.T) {
	for _, name := range []string{"scope", "digest", "order", "missing", "limit", "canceled", "commit"} {
		t.Run(name, func(t *testing.T) {
			s, path := newStore(t)
			mustBucket(t, s, "bucket")
			old := mustPut(t, s, "bucket", "key", "old")
			id, err := s.CreateUpload(t.Context(), "bucket", "key", PutOptions{})
			if err != nil {
				t.Fatal(err)
			}
			a, err := s.PutPart(t.Context(), "bucket", "key", id, 1, bytes.NewBufferString("abc"), 3)
			if err != nil {
				t.Fatal(err)
			}
			refs, key, limit, ctx := []PartRef{{1, a.Digest}}, "key", int64(3), t.Context()
			switch name {
			case "scope":
				key = "other"
			case "digest":
				refs[0].Digest = old.Digest
			case "order":
				refs = append(refs, refs[0])
				limit = 6
			case "missing":
				refs[0].Number = 2
			case "limit":
				limit = 2
			case "canceled":
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			case "commit":
				index, saved := filepath.Join(path, "index.json"), filepath.Join(path, "saved-index")
				if err := os.Rename(index, saved); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(index, 0700); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Remove(index); err != nil {
						t.Error(err)
					}
					if err := os.Rename(saved, index); err != nil {
						t.Error(err)
					}
				})
			}
			if _, err := s.CompleteUpload(ctx, "bucket", key, id, refs, limit); err == nil {
				t.Fatal("invalid completion accepted")
			}
			if obj, err := s.HeadObject(t.Context(), "bucket", "key"); err != nil || obj.Digest != old.Digest {
				t.Fatal("failed completion replaced old object:", obj, err)
			}
			if upload, err := s.ReadUpload(t.Context(), "bucket", "key", id); err != nil || len(upload.Parts) != 1 {
				t.Fatal("failed completion lost parts:", upload, err)
			}
		})
	}
}

func TestUploadQuotaAndAbortKeepRetainedBytes(t *testing.T) {
	path := filepath.Join(privateDir(t), "objects")
	s, err := OpenWithLimit(t.Context(), path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	mustBucket(t, s, "bucket")
	id, err := s.CreateUpload(t.Context(), "bucket", "key", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	part, err := s.PutPart(t.Context(), "bucket", "key", id, 1, bytes.NewBufferString("abc"), 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteUpload(t.Context(), "bucket", "key", id, []PartRef{{1, part.Digest}}, 3); !errors.Is(err, ErrQuota) {
		t.Fatal("assembly bypassed shared reservation:", err)
	}
	if err := s.DeleteBucket(t.Context(), "bucket"); !errors.Is(err, ErrBucketNotEmpty) {
		t.Fatal("bucket deletion ignored active sessions:", err)
	}
	if err := s.AbortUpload(t.Context(), "bucket", "key", id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, "blobs", part.Digest)); err != nil {
		t.Fatal("abort reclaimed immutable bytes:", err)
	}
	if _, err := s.PutObject(t.Context(), "bucket", "other", bytes.NewBufferString("xy"), PutOptions{Size: 2}); !errors.Is(err, ErrQuota) {
		t.Fatal("abort released retained byte charge:", err)
	}
	if err := s.DeleteBucket(t.Context(), "bucket"); err != nil {
		t.Fatal("aborted session still blocked deletion:", err)
	}
}

func TestRestoredUploadMetadataIsClosedAndValidated(t *testing.T) {
	s, _ := newStore(t)
	mustBucket(t, s, "bucket")
	id, err := s.CreateUpload(t.Context(), "bucket", "key", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	part, err := s.PutPart(t.Context(), "bucket", "key", id, 1, bytes.NewBufferString("abc"), 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"legacy", "identifier", "date", "nil-parts", "digest", "number", "size", "part-date"} {
		t.Run(name, func(t *testing.T) {
			state := s.clone()
			upload := copyUpload(state.Buckets["bucket"].Uploads[id])
			targetID, badPart := id, part
			switch name {
			case "legacy":
				state.Version = 3
			case "identifier":
				delete(state.Buckets["bucket"].Uploads, id)
				targetID = "../foreign"
			case "date":
				upload.Created = time.Time{}
			case "nil-parts":
				upload.Parts = nil
			case "digest":
				badPart.Digest = "../../foreign"
				upload.Parts[1] = badPart
			case "number":
				upload.Parts[0] = badPart
			case "size":
				badPart.Size = -1
				upload.Parts[1] = badPart
			case "part-date":
				badPart.Modified = time.Time{}
				upload.Parts[1] = badPart
			}
			state.Buckets["bucket"].Uploads[targetID] = upload
			raw, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeManifest(raw); !errors.Is(err, ErrCorrupt) {
				t.Fatal("unsafe upload metadata admitted:", name, err)
			}
		})
	}
}

func TestFailedPartChecksumCannotReplaceDurablePart(t *testing.T) {
	s, _ := newStore(t)
	mustBucket(t, s, "bucket")
	id, err := s.CreateUpload(t.Context(), "bucket", "key", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.PutPart(t.Context(), "bucket", "key", id, 1, bytes.NewBufferString("abc"), 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutPart(context.Background(), "bucket", "key", id, 1, bytes.NewBufferString("x"), 3); err == nil {
		t.Fatal("truncated replacement accepted")
	}
	if upload, err := s.ReadUpload(t.Context(), "bucket", "key", id); err != nil || upload.Parts[1].Digest != old.Digest {
		t.Fatal("failed part replaced durable metadata:", upload, err)
	}
}
