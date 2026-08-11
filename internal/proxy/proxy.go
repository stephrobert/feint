// Package proxy records what a real client and a real cloud say to each other.
//
// Everything this repository knows about how an official client addresses an
// endpoint was found by putting a proxy between the client and the emulator by
// hand, reading the log, and throwing the proxy away. tools/conformance/README.md
// says so three times: that oapi-cli appends /api/v1 itself, that exo takes its
// endpoint only from its own configuration file and needs the /v2 suffix, and
// that `exo compute instance create` issues four reads before it posts anything
// — "a unit test would never have found them". Three findings, three throwaway
// proxies, nothing committed. This package is that tool, built.
//
// It is a reverse proxy, not a forward one: the client is pointed at it
// (SCW_API_URL, an endpoint in a configuration file) and it forwards to one
// upstream. A client whose endpoint is hardcoded needs DNS and TLS interception,
// which is #76 and not here.
//
// What it deliberately does not do:
//
//   - Store, forward or manage a credential. The request passes through with
//     whatever the client put in it, and what is written down is redacted before
//     the writer sees it — see [Redacted], which makes that a property of the
//     type rather than a step in a call site.
//   - Alter the exchange it measures. No X-Forwarded-* is added, no header is
//     rewritten beyond what net/http/httputil removes as hop-by-hop, and a gzip
//     response reaches the client gzipped. The recording decompresses its own
//     copy; the wire is untouched.
//   - Run against a real cloud in CI. Never: no account, no credentials in a
//     runner. A transcript is recorded by a human on their own station.
package proxy

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// DefaultMaxBody caps how much of one body is recorded.
//
// One megabyte is far above any control-plane payload of these three APIs (a
// server create is a few hundred bytes, the largest list a few tens of kilobytes)
// and far below what a night of applies could cost if bodies were kept whole.
// What happens at the cap is [Omission], not a silent cut.
const DefaultMaxBody = 1 << 20

// Options configures a [Proxy].
type Options struct {
	// Upstream is where every request goes. Required.
	Upstream *url.URL
	// Writer receives every exchange. Required: a proxy that records nothing is
	// a proxy, and this package is not for that.
	Writer *Writer
	// Table names the operation an exchange addressed. Optional — without one
	// every exchange is recorded with no operation, which is a legitimate
	// transcript of a provider feint has no pack for, and a useless one against
	// a provider it does.
	Table *emulator.Table
	// MaxBody caps the recorded part of one body. Zero takes DefaultMaxBody.
	MaxBody int
	// Log is where a failed upstream call is reported. Never nil after New.
	Log *slog.Logger
}

// Proxy forwards requests to one upstream and records what went past.
type Proxy struct {
	rp      *httputil.ReverseProxy
	writer  *Writer
	table   *emulator.Table
	maxBody int
	log     *slog.Logger
	now     func() time.Time
	// unnamed counts exchanges on routes no pack claims. It is the finding this
	// tool exists to produce rather than a diagnostic: every one of them is a
	// route a real client walks and this emulator does not serve, which is
	// exactly the list #74 ranks and the gap tools/conformance/README.md
	// describes finding three times by hand.
	unnamed atomic.Int64
	// handed counts responses that gave the client an address other than this
	// proxy. See handoff.go: it is the difference between a recording that
	// stops and a recording that stops and says so.
	handed handoff
}

// New validates the options and builds the proxy.
func New(o Options) (*Proxy, error) {
	if o.Upstream == nil {
		return nil, fmt.Errorf("no upstream to forward to")
	}
	if o.Upstream.Scheme != "http" && o.Upstream.Scheme != "https" {
		return nil, fmt.Errorf("upstream %q: scheme must be http or https", o.Upstream)
	}
	if o.Upstream.Host == "" {
		return nil, fmt.Errorf("upstream %q names no host", o.Upstream)
	}
	if o.Writer == nil {
		return nil, fmt.Errorf("no writer: a proxy that records nothing is a plain reverse proxy")
	}
	if o.MaxBody <= 0 {
		o.MaxBody = DefaultMaxBody
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}

	p := &Proxy{
		writer:  o.Writer,
		table:   o.Table,
		maxBody: o.MaxBody,
		log:     o.Log,
		now:     func() time.Time { return time.Now().UTC() },
	}
	upstream := *o.Upstream
	p.rp = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			// SetURL and nothing else. SetXForwarded is what a reverse proxy in
			// front of an application wants and the opposite of what this one
			// does: it would add three headers the client never sent to a request
			// a real cloud is about to answer, and the transcript exists to show
			// what the client sends.
			pr.SetURL(&upstream)
		},
		ErrorHandler: p.upstreamFailed,
	}
	return p, nil
}

// upstreamFailed answers a request the upstream did not.
//
// The detail goes to the log and never into the response, and that is a leak
// control rather than terseness: a transport error carries the request URL,
// SigV4 puts its signature in the query when a request is presigned, and this
// body is recorded verbatim as what the client received. A body is redacted by
// JSON key name, so a credential pasted into a plain-text error message would go
// straight through. TestAFailedUpstreamLeaksNothingIntoTheTranscript fails if
// this writes err.
func (p *Proxy) upstreamFailed(w http.ResponseWriter, _ *http.Request, err error) {
	p.log.Error("the upstream did not answer", "error", err)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	_, _ = io.WriteString(w, "feint proxy: the upstream did not answer; see the proxy's log\n")
}

// ServeHTTP forwards one request and records the exchange.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Resolved before the body is touched: Lookup reads the method and the URL,
	// and doing it first keeps it independent of anything the transfer does.
	var (
		route   emulator.Mounted
		mounted bool
	)
	if p.table != nil {
		route, mounted = p.table.Lookup(r)
	}
	if !mounted {
		p.unnamed.Add(1)
	}

	reqBody := &body{max: p.maxBody}
	if r.Body != nil {
		// Teed rather than read whole and replayed: a body larger than the cap
		// still reaches the upstream, it just stops being recorded. Reading it
		// into memory first would make the cap a limit on what a client may send
		// through, which is not the recorder's business.
		r.Body = struct {
			io.Reader
			io.Closer
		}{Reader: io.TeeReader(r.Body, reqBody), Closer: r.Body}
	}

	tap := &responseTap{ResponseWriter: w, status: http.StatusOK, body: &body{max: p.maxBody}}

	started := time.Now()
	p.rp.ServeHTTP(tap, r)
	elapsed := time.Since(started)

	// Read from the recorded copy, not from the wire: the client already has the
	// bytes and nothing here alters them. r.Host is the authority the client
	// addressed, which is what "elsewhere" is measured against.
	p.handed.note(r.Host, tap.body.plain(w.Header()))

	p.writer.Record(capture(seen{
		at:        p.now(),
		elapsed:   elapsed,
		req:       r,
		reqBody:   reqBody,
		status:    tap.status,
		resHeader: w.Header(),
		resBody:   tap.body,
		route:     route,
		mounted:   mounted,
	}))
}

// Unnamed is how many exchanges walked a route no pack claims.
//
// Zero with no table, because nothing was asked: a proxy naming nothing must not
// report every exchange as a finding.
func (p *Proxy) Unnamed() int64 {
	if p.table == nil {
		return 0
	}
	return p.unnamed.Load()
}

// HandedElsewhere is how many responses gave the client an address that is not
// this proxy, and which hosts they named.
//
// It is not an error and not always a problem — an API may name a bucket
// endpoint in a body nobody follows. It matters because the client *may* follow
// it, and if it does, everything after leaves no trace here. A transcript that
// stops early looks exactly like a session that ended early, and only this
// number tells them apart.
func (p *Proxy) HandedElsewhere() (int64, map[string]int64) { return p.handed.seen() }

// responseTap copies what is written to the client, and passes it through.
//
// Recording on the way out rather than in ModifyResponse, and the difference is
// the failure path: ModifyResponse is not called when the upstream cannot be
// reached, so the 502 the client actually received would be missing from the
// transcript — or worse, present with the status of the last exchange that
// worked. What the client received is one thing, and this is where it is.
type responseTap struct {
	http.ResponseWriter
	status int
	body   *body
}

func (t *responseTap) WriteHeader(status int) {
	t.status = status
	t.ResponseWriter.WriteHeader(status)
}

func (t *responseTap) Write(p []byte) (int, error) {
	_, _ = t.body.Write(p)
	return t.ResponseWriter.Write(p)
}

// Unwrap is what http.ResponseController follows, and httputil.ReverseProxy uses
// one to flush a streaming response and to hijack an upgraded connection.
// Without it a wrapped ResponseWriter silently loses both: a server-sent-event
// stream through this proxy would deliver nothing until it ended.
func (t *responseTap) Unwrap() http.ResponseWriter { return t.ResponseWriter }
