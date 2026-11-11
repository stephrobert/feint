package objectstore

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
)

type checkpointBlockingReader struct {
	entered chan struct{}
	release chan struct{}
	reader  io.Reader
}

func (r *checkpointBlockingReader) Read(p []byte) (int, error) {
	if r.entered != nil {
		close(r.entered)
		r.entered = nil
		<-r.release
	}
	return r.reader.Read(p)
}

// The second cancellation check is in assembly's contextReader, after its
// reservation and temporary file exist, not in the short metadata transaction.
type checkpointAssemblyContext struct {
	context.Context
	calls   int
	entered chan struct{}
	release chan struct{}
}

func (c *checkpointAssemblyContext) Err() error {
	c.calls++
	if c.calls == 2 {
		close(c.entered)
		<-c.release
	}
	return c.Context.Err()
}

func TestCheckpointExclusiveGate(t *testing.T) {
	s, _ := newStore(t)
	end, err := s.beginExclusive(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	shared := s.life.TryRLock()
	if shared {
		s.life.RUnlock()
	}
	end()
	if shared {
		t.Fatal("checkpoint gate admitted an active operation")
	}
	// Accepting half: normal operations resume immediately after release.
	mustBucket(t, s, "b")
}

func TestCheckpointResetAndCollectionWaitForActiveMutations(t *testing.T) {
	for _, mutation := range []string{"put", "part", "completion"} {
		t.Run(mutation, func(t *testing.T) {
			s, _ := limitedCheckpointStore(t, 64)
			mustPut(t, s, "b", "key", "old")
			id, err := s.CreateUpload(t.Context(), "b", "key", PutOptions{})
			if err != nil {
				t.Fatal(err)
			}
			part, err := s.PutPart(t.Context(), "b", "key", id, 1, strings.NewReader("part"), 4)
			if err != nil {
				t.Fatal(err)
			}
			before := s.clone()
			mustSave(t, s, "saved")
			entered, release := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			go func() {
				body := &checkpointBlockingReader{entered: entered, release: release, reader: strings.NewReader("new")}
				var err error
				switch mutation {
				case "put":
					_, err = s.PutObject(t.Context(), "b", "key", body, PutOptions{Size: 3})
				case "part":
					_, err = s.PutPart(t.Context(), "b", "key", id, 1, body, 3)
				case "completion":
					ctx := &checkpointAssemblyContext{Context: t.Context(), entered: entered, release: release}
					_, err = s.CompleteUpload(ctx, "b", "key", id, []PartRef{{1, part.Digest}}, 10)
				}
				done <- err
			}()
			<-entered
			// Lock assertions, not scheduling delays, prove both sides of the
			// lifecycle boundary while each operation has bytes reserved.
			exclusive := s.life.TryLock()
			if exclusive {
				s.life.Unlock()
			}
			metadataFree := s.mu.TryLock()
			if metadataFree {
				s.mu.Unlock()
			}
			s.budget.mu.Lock()
			held := s.budget.held
			s.budget.mu.Unlock()
			resetDone, gcDone := make(chan error, 1), make(chan error, 1)
			go func() { resetDone <- s.ResetCheckpoint(t.Context(), "saved") }()
			go func() { gcDone <- s.CollectGarbage(t.Context()) }()
			close(release)
			for _, result := range []<-chan error{done, resetDone, gcDone} {
				if err := <-result; err != nil {
					t.Fatal(err)
				}
			}
			if exclusive || !metadataFree || held == 0 {
				t.Fatalf("slow %s did not hold exclusive-operation seam outside metadata lock: exclusive=%v free=%v held=%d",
					mutation, exclusive, metadataFree, held)
			}
			if !reflect.DeepEqual(s.state, before) {
				t.Fatalf("%s resurrected/overwrote reset state", mutation)
			}
			_, data := readObject(t, s, "b", "key")
			if string(data) != "old" {
				t.Fatalf("reset lost old bytes after %s: %q", mutation, data)
			}
			if err := s.ResetCheckpoint(t.Context(), "saved"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
