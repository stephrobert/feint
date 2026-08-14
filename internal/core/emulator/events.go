package emulator

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/stephrobert/feint/internal/trace"
)

// The call log: what went through, what was not read, and what does not exist.
//
// This is the feature that justifies the page on its own, and nothing here had
// it: the observer counted calls and kept neither their order, nor their time,
// nor their status. The real working loop of this project is to run a client and
// find out which routes it walks, in what order, and which one answers 404 and
// takes the whole plan down — the trap CLAUDE.md records under "décliner le
// catalogue casse le CLI", where four reads happen before anything is created.
// That was an hour of tcpdump.
//
// The warning line is the product. unread_request_fields is the only signal in
// this repository that names a causal defect — an argument the API accepted and
// then ignored — and it was already computed and already ignored, because you
// had to know it existed and think to look after the command had lied.
//
// Three properties, each deliberate:
//
//   - Bounded. 256 entries, oldest dropped. An emulator left running overnight
//     under a loop of applies must not grow without end, and a log nobody can
//     scroll to the end of is not more useful for being complete.
//   - One-way. text/event-stream over plain HTTP, which net/http does natively
//     and which no browser can write back to. There is no path from the page to
//     a command, by construction rather than by refusing verbs.
//   - Never about itself. The page polls this emulator every two seconds, so a
//     log that recorded /_feint/* would be a log of the page watching itself,
//     drowning the client traffic it exists to show.
//     TestTheLogIgnoresTheEmulatorsOwnEndpoints fails without that.

// ringSize bounds the log. Chosen because a terraform apply of the fixture in
// tools/conformance walks a few dozen routes: 256 holds several runs, and the
// page keeps the same number of rows, so what the reader scrolls is what the
// process remembers.
const ringSize = 256

// The record is trace.Exchange, not a shape of this package's own.
//
// `feint proxy` (X-2, #72) records the same fact from outside the process, and
// `feint replay` (X-3, #73) reads what either of them wrote. One shape means a
// trace pulled from a running emulator is replayable, and a transcript recorded
// against a real cloud is renderable by this page. See internal/trace for what
// the ring deliberately leaves empty.

// RuntimeEvent is what the machine runtime reported about something the
// emulator created. It shares the stream with the calls because an operator
// reading "the server is running" needs to see, on the same timeline, that the
// container behind it stopped.
type RuntimeEvent struct {
	Seq      int64     `json:"seq"`
	At       time.Time `json:"at"`
	Kind     string    `json:"kind"`
	Level    string    `json:"level,omitempty"`
	Action   string    `json:"action,omitempty"`
	Resource string    `json:"resource,omitempty"`
	Message  string    `json:"message,omitempty"`
}

// streamEvent is one entry, already encoded. Marshalling once at publish rather
// than once per subscriber keeps the cost off the request path, which is where
// this code runs.
type streamEvent struct {
	name string
	data []byte
}

// stream is the ring buffer and its subscribers.
type stream struct {
	mu   sync.Mutex
	seq  int64
	ring []streamEvent
	subs map[chan streamEvent]struct{}
	now  func() time.Time
}

func newStream() *stream {
	return &stream{
		ring: make([]streamEvent, 0, ringSize),
		subs: map[chan streamEvent]struct{}{},
		now:  func() time.Time { return time.Now().UTC() },
	}
}

// publish records an entry and offers it to every subscriber.
//
// A subscriber that cannot keep up is skipped, never waited for. This runs
// inside the handler of a request a real client is waiting on, and a browser
// tab that stopped reading must not be able to hold a terraform apply — which is
// the same lesson store.Snapshot learned when a reader that stopped consuming
// froze the whole emulator. The seq the subscriber sees jumps, and the page says
// so.
func (s *stream) publish(name string, payload func(seq int64, at time.Time) any) {
	s.mu.Lock()
	s.seq++
	body, err := json.Marshal(payload(s.seq, s.now()))
	if err != nil {
		// Nothing to report to and nothing worth failing a request over: the
		// payloads here are built from strings and numbers by this package.
		s.mu.Unlock()
		return
	}
	event := streamEvent{name: name, data: body}
	if len(s.ring) == ringSize {
		copy(s.ring, s.ring[1:])
		s.ring[ringSize-1] = event
	} else {
		s.ring = append(s.ring, event)
	}
	for ch := range s.subs {
		select {
		case ch <- event:
		default:
		}
	}
	s.mu.Unlock()
}

// subscribe registers a reader and hands it what the ring already holds.
//
// Both under one lock, so a call published between the snapshot and the
// registration is neither missed nor delivered twice.
func (s *stream) subscribe() (<-chan streamEvent, []streamEvent, func()) {
	ch := make(chan streamEvent, ringSize)
	s.mu.Lock()
	history := make([]streamEvent, len(s.ring))
	copy(history, s.ring)
	s.subs[ch] = struct{}{}
	s.mu.Unlock()

	return ch, history, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

// recent returns what the ring holds, for a reader that wants one answer rather
// than a stream.
func (s *stream) recent() []streamEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]streamEvent, len(s.ring))
	copy(out, s.ring)
	return out
}

// publishExchange records one answered request.
func (s *stream) publishExchange(x trace.Exchange) {
	s.publish("call", func(seq int64, at time.Time) any {
		x.Seq = seq
		x.At = at
		return x
	})
}

// PublishRuntimeEvent puts what the machine runtime reported on the same
// timeline as the calls.
//
// Exported because the watcher is consumed by the command that owns the
// process, not by the server: internal/cli reads the driver's stream and reports
// it to the log, and this is the second reader.
func (s *Server) PublishRuntimeEvent(kind, level, action, resource, message string) {
	s.stream.publish("runtime", func(seq int64, at time.Time) any {
		return RuntimeEvent{
			Seq: seq, At: at, Kind: kind, Level: level,
			Action: action, Resource: resource, Message: message,
		}
	})
}

// internalPath reports whether a path belongs to the emulator's own control
// plane rather than to a provider.
//
// The page polls /_feint/health, /_feint/conformance, /_feint/resources and
// /_feint/routes every two seconds. Logging those would fill the log with the
// page watching itself — several entries per second, drowning the client traffic
// the log exists to show, and growing without a client ever making a call.
// TestTheLogIgnoresTheEmulatorsOwnEndpoints fails without this.
func internalPath(path string) bool { return strings.HasPrefix(path, "/_feint/") }

// heartbeat keeps a stream that has nothing to say from being mistaken for a
// dead one by whatever sits between the browser and this process.
const heartbeat = 25 * time.Second

// handleEvents serves the log as text/event-stream.
//
// SSE rather than WebSocket, and the reason is the product's rather than a
// preference: it is HTTP and text, net/http does it with no dependency, and the
// channel is one-way — so no command can travel from the page to the emulator,
// by construction. The browser reconnects on its own, and the ring means a
// reconnection loses nothing that is still in it.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// serve sets WriteTimeout on the http.Server, and a stream is exactly the
	// response that timeout was not written for: it would cut this connection
	// after 60 seconds, every 60 seconds, for as long as the page stays open.
	// Clearing the deadline is the standard library's own answer to a long-lived
	// response, and it is scoped to this handler.
	// TestTheStreamOutlivesTheServersWriteTimeout fails without this.
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		// A ResponseWriter that cannot do this still streams; it will simply be
		// cut at the deadline and the browser will reconnect. Worth neither
		// failing the request nor staying silent about.
		s.log().Debug("the event stream keeps the server's write deadline", "error", err)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	// Named for the one proxy behaviour that breaks SSE silently: a buffering
	// reverse proxy holds every event until the response ends, which for a
	// stream is never.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	events, history, unsubscribe := s.stream.subscribe()
	defer unsubscribe()

	send := func(e streamEvent) bool {
		if _, err := w.Write(sseFrame(e)); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	for _, e := range history {
		if !send(e) {
			return
		}
	}

	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-events:
			if !send(e) {
				return
			}
		case <-ticker.C:
			// A comment frame: valid SSE, ignored by EventSource, and enough to
			// keep the connection alive through anything that times out idle
			// sockets.
			if _, err := w.Write([]byte(": keep-alive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// handleTrace answers what the ring holds, as one JSON array.
//
// The stream is right for a page and wrong for everything else that wants this
// data: an SSE consumer has to be running before the interesting thing happens,
// so a shell script, a conformance assertion or a CI job that wants to say "the
// apply walked these operations in this order" could not read it at all. Same
// bounded structure, one more handler, no new state — and it is what makes the
// difference between an interface feature and a mechanism the interface happens
// to display.
//
// It answers the exchanges only. The runtime events on the same stream are the
// machine runtime talking about itself, and a trace meant to be replayed by
// `feint replay` (X-3, #73) must carry requests and nothing else.
func (s *Server) handleTrace(w http.ResponseWriter, _ *http.Request) {
	held := s.stream.recent()
	out := make([]json.RawMessage, 0, len(held))
	for _, e := range held {
		if e.name != "call" {
			continue
		}
		out = append(out, json.RawMessage(e.data))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": TraceSchemaVersion,
		"count":          len(out),
		// Said on the wire rather than left for a reader to discover by
		// counting: a trace that silently held the last 256 of 4000 calls would
		// make a script assert on a window it did not know it was looking
		// through.
		"kept":      ringSize,
		"exchanges": out,
	})
}

// sseFrame renders one event.
//
// The data is JSON produced by encoding/json, which never emits a raw newline
// inside a string, so one data: line always holds the whole payload. That is a
// property of the encoder rather than an assumption about the values: a name a
// client chose with a newline in it comes out escaped.
func sseFrame(e streamEvent) []byte {
	frame := make([]byte, 0, len(e.data)+len(e.name)+16)
	frame = append(frame, "event: "...)
	frame = append(frame, e.name...)
	frame = append(frame, '\n')
	frame = append(frame, "data: "...)
	frame = append(frame, e.data...)
	frame = append(frame, '\n', '\n')
	return frame
}
