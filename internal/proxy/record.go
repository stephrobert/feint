package proxy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/trace"
)

// Redacted is one exchange whose credential-bearing values have been replaced.
//
// The field is unexported and [capture] is the only function in this package
// that returns one, so a Redacted cannot exist without having been through
// [redactExchange]. That is the difference this type is for: redaction as a
// property of what the writer accepts, rather than a step at a call site
// somebody has to remember. A [Writer] takes a Redacted and nothing else, so
// there is no path to the file for a value the rules never saw — not one that is
// discouraged, one that does not compile.
//
// #72 asks for redaction "on the record before it reaches the writer", and a
// function called at the right place would satisfy the sentence. It would not
// satisfy the next writer, who adds a second path to the file: this repository's
// most expensive defect is the control that reads as done and is not.
//
// TestATranscriptCarriesNoCredential fails if [capture] stops redacting.
type Redacted struct{ x trace.Exchange }

// MarshalJSON writes the exchange itself: the wrapper is a compile-time
// constraint and must leave no trace on the wire, or every reader of a
// transcript would have to know about it.
func (r Redacted) MarshalJSON() ([]byte, error) { return json.Marshal(r.x) }

// Operation is the upstream operation the exchange addressed, empty when no pack
// claims that route. Exported for the caller that counts findings rather than
// exchanges; the value itself is never sensitive.
func (r Redacted) Operation() string { return r.x.Operation }

// seen is everything the proxy observed about one exchange, before redaction.
//
// It is not a trace.Exchange, on purpose: the only thing that produces one of
// those here is capture, and it redacts on the way.
type seen struct {
	at      time.Time
	elapsed time.Duration
	// req is the request as the client sent it, before the reverse proxy
	// rewrote a clone of it. What a replay reissues is this, not the outbound
	// form with its hop-by-hop headers removed.
	req     *http.Request
	reqBody *body
	// status, resHeader and resBody are what the client received, which on a
	// transport failure is the proxy's own 502 rather than anything a cloud said.
	// Recording what was received rather than what was sent upstream is what
	// makes a transcript match the client's own view of the run.
	status    int
	resHeader http.Header
	resBody   *body
	route     emulator.Mounted
	mounted   bool
}

// capture builds the record of one exchange, redacted.
//
// The redaction is the second-to-last statement rather than a caller's
// responsibility, and [Redacted] is what stops a future caller from having one.
func capture(s seen) Redacted {
	x := trace.Exchange{
		At:      s.at,
		Method:  s.req.Method,
		Path:    s.req.URL.Path,
		Query:   s.req.URL.RawQuery,
		Status:  s.status,
		Ms:      float64(s.elapsed.Microseconds()) / 1000,
		Mounted: s.mounted,
		Req: &trace.Message{
			Headers: headersOf(s.req.Header),
			Body:    s.reqBody.decoded(s.req.Header),
		},
		Res: &trace.Message{
			Headers: headersOf(s.resHeader),
			Body:    s.resBody.decoded(s.resHeader),
		},
	}
	if s.mounted {
		x.Operation = s.route.Route.Operation
		x.Provider = s.route.Provider
	}

	redactExchange(&x)
	return Redacted{x: x}
}

// headersOf flattens a header set into the map a transcript carries.
//
// trace.Message keeps one value per name and says why: a transcript is read by
// humans and reissued by a replay that sends one value per header. A repeated
// header is joined the way net/http itself would have sent it, so nothing is
// lost even though nothing in these three APIs repeats one.
func headersOf(h http.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for name, values := range h {
		out[name] = strings.Join(values, ", ")
	}
	return out
}

// body captures the first max bytes of a stream while the whole of it passes
// through, and counts what went past.
//
// Bounded rather than buffered whole: #72's proxy is meant to run for a night of
// applies, and a recorder that keeps every byte it forwards is a recorder that
// ends the night by being killed. What it does when it reaches the bound is say
// so — see [Omission] — because a body silently cut at a megabyte and written as
// if it were whole is worse than no body at all.
//
// The mutex is not decoration. The request body is read by the transport, which
// writes it from its own goroutine, so the capture can still be filling when the
// response arrives; -race says so without it.
type body struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	total int64
	max   int
}

// Write implements io.Writer so the body can be teed rather than read twice.
// It never fails: refusing bytes here would fail the request the client is
// waiting on, and a recorder must not be able to break what it observes.
func (b *body) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total += int64(len(p))
	if room := b.max - b.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.buf.Write(p)
	}
	return len(p), nil
}

// decoded turns what was captured into the value the transcript carries.
func (b *body) decoded(header http.Header) any {
	b.mu.Lock()
	data := b.buf.Bytes()
	total := b.total
	b.mu.Unlock()

	if total == 0 {
		return nil
	}
	contentType := header.Get("Content-Type")
	if total > int64(len(data)) {
		return &Omission{
			Reason: "body larger than the record cap; raise --max-body to keep it",
			Bytes:  total,
			Type:   contentType,
		}
	}

	// Decompressed for the record only, never on the wire: the client asked for
	// gzip and gets exactly the bytes the cloud sent. Without this the response
	// body of every scw run is a blob, because the CLI sends Accept-Encoding and
	// a reverse proxy has no business rewriting the exchange it is measuring.
	if strings.EqualFold(header.Get("Content-Encoding"), "gzip") {
		plain, err := gunzip(data)
		if err != nil {
			return &Omission{
				Reason: "gzip body could not be decompressed: " + err.Error(),
				Bytes:  total,
				Type:   contentType,
			}
		}
		data = plain
	}

	// UseNumber, so an identifier that is 19 digits long comes back out as it
	// went in. Through float64 it would not, and a transcript that alters an ID
	// is a transcript a replay cannot use.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var decoded any
	if err := dec.Decode(&decoded); err == nil {
		return decoded
	}

	// Not JSON. A string, as trace.Message documents — but only if it is text: a
	// binary body cast to a string is invalid UTF-8, which encoding/json rewrites
	// into replacement characters, so the transcript would carry neither the
	// bytes nor an admission that it lost them.
	if utf8.Valid(data) {
		return string(data)
	}
	return &Omission{Reason: "body is neither JSON nor text", Bytes: total, Type: contentType}
}

// Omission stands where a body is not, and says why.
//
// It marshals to an object carrying feint_omitted, which is the key a reader
// tests for: a transcript that dropped a body must not be indistinguishable from
// one whose body was empty. Same argument as /_feint/trace publishing how many
// entries it keeps rather than leaving a reader to discover the window by
// counting.
type Omission struct {
	Reason string `json:"feint_omitted"`
	Bytes  int64  `json:"bytes"`
	Type   string `json:"content_type,omitempty"`
}

// gunzip decompresses a recorded body.
//
// The reader is bounded by what was captured, which is already capped, so no
// decompression bomb can grow past the cap plus the ratio of one buffer.
func gunzip(data []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()
	// Bounded again on the way out: a gzip bomb of a megabyte expands to a
	// gigabyte, and the recorder must not be the thing that runs the station out
	// of memory. maxDecompressed is generous next to any control-plane response.
	plain, err := io.ReadAll(io.LimitReader(zr, maxDecompressed))
	if err != nil {
		return nil, err
	}
	return plain, nil
}

// maxDecompressed bounds what a compressed body may become once expanded.
const maxDecompressed = 8 << 20
