package proxy_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/proxy"
)

// stalled is a disk that has stopped answering, and a record of what reached it
// once it starts again.
type stalled struct {
	release chan struct{}

	mu      sync.Mutex
	written bytes.Buffer
}

func newStalled() *stalled { return &stalled{release: make(chan struct{})} }

func (s *stalled) Write(p []byte) (int, error) {
	<-s.release
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.written.Write(p)
}

func (s *stalled) resume() { close(s.release) }

func (s *stalled) bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.written.Bytes()...)
}

// A full queue drops, and the transcript says which records are missing.
//
// The alternative — blocking until there is room — is what turns a slow disk
// into a slow cloud API for the client on the other side, and #72 asks for a
// recorder that does not double the latency of what it records. Dropping is
// only honest if it is visible, and it is: a dropped record still consumes its
// sequence number, so the gap in Seq is the admission. trace.Exchange.Seq
// documents exactly that use.
//
// It is written with a timeout rather than a bare loop because the failure mode
// of the property is a hang: if Record blocked, this test would never return and
// would take every test behind it with it.
func TestTheWriterDropsRatherThanBlocks(t *testing.T) {
	const records = 200
	disk := newStalled()
	w := proxy.NewWriter(disk, 2)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range records {
			w.Record(proxy.SampleRecord())
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		disk.resume()
		t.Fatal("Record blocked on a stalled disk, so a slow transcript would hold every client request")
	}

	// The disk comes back, and the run goes on. That ordering is the test rather
	// than an incidental detail: a gap lives between two lines, so it only exists
	// once something is written after the drops. The first version of this test
	// asserted the gap without writing anything afterwards, and correctly failed
	// — the file ended at 3 and nothing in it said 197 records were missing. That
	// case is real and is documented on Writer rather than papered over here.
	disk.resume()
	waitForDrain(t, w, records)

	// Offered one at a time, each waited for: the queue is two deep, so offering
	// five at once would drop two of them and make the last sequence number a
	// race rather than a fact.
	const afterwards = 5
	for i := range int64(afterwards) {
		w.Record(proxy.SampleRecord())
		waitForDrain(t, w, records+i+1)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	written, dropped := w.Stats()
	if written+dropped != records+afterwards {
		t.Errorf("%d records in, %d written and %d dropped: %d are unaccounted for",
			records+afterwards, written, dropped, records+afterwards-written-dropped)
	}
	if dropped == 0 {
		t.Fatal("a queue of two absorbed 200 records against a stalled disk, so nothing was measured")
	}

	lines := read(t, disk.bytes())
	if len(lines) == 0 {
		t.Fatal("nothing reached the disk once it resumed")
	}
	var previous, biggestJump int64
	for _, x := range lines {
		if x.Seq <= previous {
			t.Errorf("sequence numbers are not ascending: %d after %d", x.Seq, previous)
		}
		if jump := x.Seq - previous; jump > biggestJump {
			biggestJump = jump
		}
		previous = x.Seq
	}
	if biggestJump < 2 {
		t.Errorf("the transcript is numbered without a gap, so %d dropped records left no trace in it",
			dropped)
	}
	if previous != int64(records+afterwards) {
		t.Errorf("the last record is numbered %d, not %d: a sequence number is reused or skipped at the end",
			previous, records+afterwards)
	}
}

// waitForDrain blocks until the writer has accounted for every record offered so
// far. Bounded, and a timeout fails rather than hanging the suite behind it.
func waitForDrain(t *testing.T, w *proxy.Writer, offered int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		written, dropped := w.Stats()
		if written+dropped >= offered {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the writer accounted for %d of %d records after the disk resumed", written+dropped, offered)
		}
		time.Sleep(time.Millisecond)
	}
}

// The client is answered whatever the disk is doing.
//
// The unit test above proves Record does not block; this proves the request path
// is where that matters. With Record on the wrong side of the write, every one of
// these calls would wait for a disk that never answers.
func TestASlowDiskDoesNotHoldTheClient(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	disk := newStalled()
	defer disk.resume()
	w := proxy.NewWriter(disk, 1)
	p, err := proxy.New(proxy.Options{Upstream: target, Writer: w})
	if err != nil {
		t.Fatalf("build the proxy: %v", err)
	}
	front := httptest.NewServer(p)
	defer front.Close()

	answered := make(chan error, 1)
	go func() {
		for range 20 {
			res, err := http.Get(front.URL + "/v2/zone")
			if err != nil {
				answered <- err
				return
			}
			_, _ = io.Copy(io.Discard, res.Body)
			_ = res.Body.Close()
		}
		answered <- nil
	}()

	select {
	case err := <-answered:
		if err != nil {
			t.Fatalf("the proxy did not answer: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the client waited on the transcript: a stalled disk stopped the proxy answering")
	}
}

// Close is safe to call twice, because the command that owns the process calls
// it on the signal path and again on the way out.
func TestClosingTheWriterTwiceIsSafe(t *testing.T) {
	w := proxy.NewWriter(io.Discard, 0)
	if err := w.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	// And a record arriving after the close is counted rather than panicking on
	// a closed channel: an in-flight request can outlive the shutdown that
	// closed the writer.
	w.Record(proxy.SampleRecord())
	if _, dropped := w.Stats(); dropped != 1 {
		t.Errorf("a record offered after Close was neither written nor counted: dropped=%d", dropped)
	}
}

// A write error is reported once, by Close, rather than per record.
func TestAFailedWriteIsReportedOnce(t *testing.T) {
	w := proxy.NewWriter(brokenDisk{}, 0)
	for range 5 {
		w.Record(proxy.SampleRecord())
	}
	if err := w.Close(); err == nil {
		t.Fatal("a transcript that could not be written reported success")
	}
	if written, _ := w.Stats(); written != 0 {
		t.Errorf("the writer counted %d records it could not write", written)
	}
}

type brokenDisk struct{}

func (brokenDisk) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
