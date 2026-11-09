package objectstore

import (
	"bytes"
	"testing"
)

func TestUsageCountsPublishedLogicalBytesNotRetainedParts(t *testing.T) {
	store, _ := newStore(t)
	mustBucket(t, store, "bucket")
	mustPut(t, store, "bucket", "first", "abc")
	mustPut(t, store, "bucket", "duplicate", "abc")
	if err := store.SetBucketMetadata(t.Context(), "bucket", "opaque", "stable"); err != nil {
		t.Fatal(err)
	}
	id, err := store.CreateUpload(t.Context(), "bucket", "future", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutPart(t.Context(), "bucket", "future", id, 1, bytes.NewBufferString("unpublished"), 11); err != nil {
		t.Fatal(err)
	}
	usage, err := store.Usage(t.Context())
	if err != nil || usage["bucket"].Size != 6 || usage["bucket"].Metadata["opaque"] != "stable" {
		t.Fatal("logical published usage lost or included retained blobs:", usage, err)
	}
	usage["bucket"].Metadata["opaque"] = "caller changed"
	if err := store.DeleteObject(t.Context(), "bucket", "first"); err != nil {
		t.Fatal(err)
	}
	after, err := store.Usage(t.Context())
	if err != nil || after["bucket"].Size != 3 || after["bucket"].Metadata["opaque"] != "stable" {
		t.Fatal("usage aliases metadata or counts deleted references:", after, err)
	}
}
