package emulator_test

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/objectstore"
	"github.com/stephrobert/feint/internal/core/resource"
)

// TestAFailedControlPlaneHalfLeavesTheEarlierBytesWhole is cited by
// handleCheckpointSave, and it is the audit's finding about my own comment.
//
// The first version saved the bytes first and the control plane second while
// promising "both halves or neither". A failure in the second step then left
// an earlier checkpoint of that name holding its OLD store beside the NEW
// object references, and a later reset restored one against the other.
//
// ## Why this test looks the way it does
//
// A first attempt made the failure by closing the byte store, and it measured
// NOTHING: with the bytes failing first, both orders refuse at the same step
// and the earlier checkpoint survives either way. The mutation that reverses
// the order stayed green, which is the harness catching the author.
//
// The scenario that separates the two orders is the one the audit actually
// described: the byte half SUCCEEDS and the control-plane half fails. So the
// store is made unmarshallable — a resource carrying a value encoding/json
// cannot write — and the assertion is on the BYTES a later reset brings back.
//
// Under the shipped order: the snapshot fails first, the bytes are never
// touched, and the reset returns the original content. Under the reversed
// order: the new bytes are published, the snapshot then fails, and the reset
// returns content that was never checkpointed.
func TestAFailedControlPlaneHalfLeavesTheEarlierBytesWhole(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := objectstore.OpenWithLimit(t.Context(), filepath.Join(root, "objects"), 2<<20)
	if err != nil {
		t.Fatalf("open the byte store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	server, held := labServerWithBytes(t, store, stubPack{name: "acme"})

	const bucket, key = "acme/images", "disk.img"
	const original, later = "the bytes the checkpoint holds", "bytes nobody checkpointed"
	if err := store.CreateBucket(t.Context(), bucket); err != nil {
		t.Fatalf("create the bucket: %v", err)
	}
	write := func(body string) {
		t.Helper()
		if _, err := store.PutObject(t.Context(), bucket, key,
			strings.NewReader(body), objectstore.PutOptions{Size: int64(len(body))}); err != nil {
			t.Fatalf("put: %v", err)
		}
	}

	write(original)
	if code, body := checkpointPost(t, server.URL+"/_feint/checkpoint/baseline"); code != http.StatusOK {
		t.Fatalf("the first checkpoint: %d %v", code, body)
	}

	// The lab moves on, and the control-plane half is then made to fail while
	// the byte half would succeed.
	write(later)
	unmarshallable := resource.New("broken", "instance/server",
		resource.Tenant{Provider: "acme", Project: "p", Zone: "z"}, "running",
		time.Unix(1700000000, 0).UTC())
	unmarshallable.Attrs = map[string]any{"cannot": make(chan int)}
	held.Put(unmarshallable)

	code, body := checkpointPost(t, server.URL+"/_feint/checkpoint/baseline")
	if code == http.StatusOK {
		t.Fatalf("a checkpoint whose control-plane half could not be taken reported success: %v", body)
	}
	if message, _ := body["error"].(string); !strings.Contains(message, "control plane") {
		t.Errorf("the refusal does not name the half that failed: %v", body["error"])
	}

	// The earlier checkpoint's BYTES are intact. This is the assertion the
	// reversed order fails: it would have published `later` under the name
	// `baseline` before refusing.
	if err := store.ResetCheckpoint(t.Context(), "baseline"); err != nil {
		t.Fatalf("reset the byte checkpoint: %v", err)
	}
	_, reader, err := store.GetObject(t.Context(), bucket, key)
	if err != nil {
		t.Fatalf("read the object back: %v", err)
	}
	defer func() { _ = reader.Close() }()
	back, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(back) != original {
		t.Errorf("the checkpoint's bytes are %q, not the %q it held: a half-taken checkpoint "+
			"published bytes the control plane never saw", back, original)
	}
}
