package proxy

// The other door into the same recorder (#336).
//
// [Proxy] records a client that can be pointed at it: SCW_API_URL, an endpoint
// in a configuration file, a `--s3-endpoint`. A client whose endpoint is
// compiled in reads none of those. Pépin's collectors are that client — their
// base URLs are `https://api.scaleway.com`, `https://api-{zone}.exoscale.com/v2`
// and `https://api.{region}.{host}/api/v1`, and making them configurable was
// refused on its own delivery audit: every collection request carries a live
// secret key, and an endpoint anybody can redirect is a way to send a tenant's
// credentials to a host of their choosing. The redirect belongs outside the tool
// being measured, which is here.
//
// One property makes it cheap: a Go client that installs no Transport inherits
// http.DefaultTransport, and http.DefaultTransport honours HTTPS_PROXY. So it
// already emits
//
//	CONNECT api.scaleway.com:443 HTTP/1.1
//
// with nothing changed in its code. This file accepts that, terminates the TLS
// with the run's own authority (see [MintAuthority]), and hands each decrypted
// request to the same [Proxy] that records everything else — same capture, same
// [Redacted], same writer. TestASecretHeaderIsStillRedactedThroughCONNECT is
// what holds that "same": a recorder with its own certificate authority is a
// credential-harvesting tool the moment a second path to the writer forgets to
// redact.
//
// What it is not: a general-purpose MITM. It intercepts the hosts the operator
// named and refuses every other CONNECT out loud, because the alternative — a
// tunnel relayed blind — writes a transcript that silently misses exchanges,
// which is the exact defect handoff.go exists to report.

import (
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// tunnelHandshakeTimeout bounds the TLS handshake of one accepted tunnel.
//
// A connection that opened a tunnel and then says nothing holds a goroutine and
// a file descriptor for as long as it likes; ten seconds is far more than a
// local handshake needs and far less than a night.
const tunnelHandshakeTimeout = 10 * time.Second

// ForwardOptions configures a [Forward].
type ForwardOptions struct {
	// Hosts are the names this proxy may intercept, exactly as they appear in a
	// CONNECT line. A single-level wildcard is accepted (`*.exoscale.com`),
	// which is what a per-zone endpoint needs. Required: a forward proxy that
	// intercepts whatever it is handed decrypts more of the operator's traffic
	// than the measurement asked for.
	Hosts []string
	// Writer receives every exchange. Required, for [New]'s reason.
	Writer *Writer
	// Table names the operation an exchange addressed. Optional.
	Table *emulator.Table
	// MaxBody caps the recorded part of one body. Zero takes DefaultMaxBody.
	MaxBody int
	// Log is where a refused tunnel and a failed handshake are reported. Never
	// nil after NewForward.
	Log *slog.Logger
	// Authority mints the leaf each tunnel presents. Optional: nil mints a fresh
	// one. A caller that has to publish the CA before the proxy starts — the
	// command does, since the client needs SSL_CERT_FILE in its environment —
	// mints it first and passes it here.
	Authority *Interceptor
	// Transport carries the re-originated request. See [Options.Transport].
	Transport http.RoundTripper
}

// Forward records a client that was never told where to connect.
//
// It accepts `CONNECT host:port`, terminates the TLS itself, and re-originates
// to the host the client asked for. Everything it decrypts goes through the
// recorder it wraps, so a transcript from this door is the same shape, redacted
// by the same rules, as one from [Proxy].
//
// What it does not cover, said here rather than left to be discovered: a tunnel
// is a hijacked connection, and http.Server.Shutdown neither closes nor waits
// for one. An exchange still in flight when the operator interrupts the command
// can therefore reach the writer after it closed, where it is counted as a drop.
// The honest end of a recording session is the client finishing first.
type Forward struct {
	rec       *Proxy
	authority *Interceptor
	hosts     []string
	log       *slog.Logger

	tunnels atomic.Int64
	// refused counts and names the CONNECTs this proxy would not intercept. It
	// is reported rather than silently dropped: a client that could not reach a
	// host is about to fail, and the operator needs to read why here rather than
	// guess from the client's own error.
	refused refusals
}

// NewForward validates the options and builds the forward proxy.
func NewForward(o ForwardOptions) (*Forward, error) {
	hosts, err := hostPatterns(o.Hosts)
	if err != nil {
		return nil, err
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	authority := o.Authority
	if authority == nil {
		if authority, err = MintAuthority(); err != nil {
			return nil, err
		}
	}
	rec, err := newProxy(Options{
		Writer:    o.Writer,
		Table:     o.Table,
		MaxBody:   o.MaxBody,
		Log:       o.Log,
		Transport: o.Transport,
	}, true)
	if err != nil {
		return nil, err
	}
	return &Forward{rec: rec, authority: authority, hosts: hosts, log: o.Log}, nil
}

// hostPatterns trims the names a forward proxy may intercept, and refuses a set
// that would intercept nothing or everything.
//
// `*` alone is refused rather than read as "all": the flag would then be one
// character away from decrypting every TLS connection the client makes, which is
// the difference between a recorder and a wiretap.
// TestAForwardProxyRefusesAnUnusableHostSet fails without this.
func hostPatterns(hosts []string) ([]string, error) {
	var out []string
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if h == "*" || strings.HasPrefix(h, "*.*") {
			return nil, fmt.Errorf("--forward %q would intercept every host a client reaches: name the hosts to record", h)
		}
		out = append(out, h)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no host to forward for: a forward proxy that intercepts nothing records nothing")
	}
	return out, nil
}

// ServeHTTP answers the three shapes a forward proxy is addressed in.
func (f *Forward) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		f.tunnel(w, r)
		return
	}
	if r.URL.IsAbs() {
		// The plain-HTTP form, which HTTP_PROXY produces: the request already
		// names its destination and there is nothing to decrypt. Recorded by the
		// same path as everything else, and refused for a host nobody named for
		// the same reason a tunnel is.
		if !f.intercepts(r.URL.Hostname()) {
			f.refuse(w, r.URL.Hostname())
			return
		}
		f.rec.ServeHTTP(w, r)
		return
	}
	// Origin-form on the forward port: a client that was pointed here as if this
	// were the cloud itself. Answered rather than forwarded blindly, because the
	// alternative is a 502 that reads like the cloud being down.
	http.Error(w, "feint proxy: this is a forward proxy (--forward); set HTTPS_PROXY to it, "+
		"or use --upstream to point a client at it directly\n", http.StatusBadRequest)
}

// tunnel terminates one CONNECT and records what it carries.
func (f *Forward) tunnel(w http.ResponseWriter, r *http.Request) {
	host, port := splitTarget(r.Host)
	if !f.intercepts(host) {
		f.refuse(w, host)
		return
	}
	cfg, err := f.authority.TLSFor(host)
	if err != nil {
		f.log.Error("no certificate for the tunnel", "host", host, "error", err)
		http.Error(w, "feint proxy: could not mint a certificate for this host\n", http.StatusInternalServerError)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		// Unreachable through net/http's own server, which always hijacks; a
		// wrapper that does not is a configuration mistake and says so rather
		// than hanging.
		http.Error(w, "feint proxy: this server cannot open a tunnel\n", http.StatusInternalServerError)
		return
	}
	conn, buffered, err := hijacker.Hijack()
	if err != nil {
		f.log.Error("hijack the tunnel", "host", host, "error", err)
		return
	}
	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		f.log.Error("acknowledge the tunnel", "host", host, "error", err)
		_ = conn.Close()
		return
	}
	f.tunnels.Add(1)

	// Whatever the client pipelined behind its CONNECT is already in the
	// buffer, and a client that sends its ClientHello without waiting for the
	// 200 is within its rights. Dropping those bytes would fail the handshake
	// with an error naming the certificate, which is the wrong cause.
	f.serve(&prefixed{Conn: conn, first: buffered}, cfg, &url.URL{
		Scheme: "https",
		Host:   net.JoinHostPort(host, port),
	})
}

// serve terminates the TLS of one tunnel and records the requests inside it.
func (f *Forward) serve(conn net.Conn, cfg *tls.Config, dest *url.URL) {
	tlsConn := tls.Server(conn, cfg)
	if err := tlsConn.SetDeadline(time.Now().Add(tunnelHandshakeTimeout)); err != nil {
		f.log.Error("bound the handshake", "host", dest.Host, "error", err)
		_ = tlsConn.Close()
		return
	}
	if err := tlsConn.Handshake(); err != nil {
		// The ordinary cause is a client that does not trust the run's CA, and
		// it is worth a line: from the client's side it reads as the cloud
		// presenting a bad certificate, with nothing pointing here.
		f.log.Error("the client did not complete the tunnel handshake",
			"host", dest.Host, "error", err,
			"hint", "point the client's SSL_CERT_FILE at this run's CA")
		_ = tlsConn.Close()
		return
	}
	if err := tlsConn.SetDeadline(time.Time{}); err != nil {
		f.log.Error("release the handshake deadline", "host", dest.Host, "error", err)
		_ = tlsConn.Close()
		return
	}

	listener := &oneConnListener{conn: tlsConn, done: make(chan struct{})}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Promoted to the absolute form the recorder forwards on. The Host
			// header the client sent is left alone: it is part of the exchange
			// being measured, and a proxy that rewrote it would be recording its
			// own edit. What the request is *sent* to stays the address the
			// CONNECT named and this proxy admitted.
			r.URL.Scheme = dest.Scheme
			r.URL.Host = dest.Host
			f.rec.ServeHTTP(w, r)
		}),
		// The tunnel's own bound, for the reason the command sets one on the
		// listener: a connection that opens a request and stops holds a
		// goroutine, and no cloud needs thirty seconds to send its headers.
		ReadHeaderTimeout: 30 * time.Second,
	}
	_ = srv.Serve(listener)
}

// intercepts reports whether this proxy may terminate a tunnel to host.
//
// Case-insensitive and trailing-dot-insensitive, because a client is free to
// send either; a single-level wildcard matches one label, exactly as a
// certificate's does, so `*.exoscale.com` covers `api-ch-gva-2.exoscale.com` and
// never `a.b.exoscale.com`.
func (f *Forward) intercepts(host string) bool {
	want := strings.ToLower(strings.TrimSuffix(host, "."))
	if want == "" {
		return false
	}
	for _, pattern := range f.hosts {
		p := strings.ToLower(pattern)
		if p == want {
			return true
		}
		suffix, isWildcard := strings.CutPrefix(p, "*")
		if !isWildcard || !strings.HasPrefix(suffix, ".") {
			continue
		}
		label, found := strings.CutSuffix(want, suffix)
		if found && label != "" && !strings.Contains(label, ".") {
			return true
		}
	}
	return false
}

// refuse answers a CONNECT for a host nobody named.
//
// Loud, and never a blind relay. A tunnel passed through unopened would let the
// client finish its session while the transcript quietly missed every exchange
// in it — the failure handoff.go was written to report, reintroduced at another
// door. TestAHostNobodyNamedIsNotIntercepted fails without this.
func (f *Forward) refuse(w http.ResponseWriter, host string) {
	f.refused.note(host)
	f.log.Warn("refused to intercept a host --forward does not name",
		"host", host, "forwarding", strings.Join(f.hosts, ", "))
	http.Error(w, "feint proxy: this proxy intercepts only the hosts --forward names\n", http.StatusForbidden)
}

// Unnamed is how many exchanges walked a route no pack claims.
func (f *Forward) Unnamed() int64 { return f.rec.Unnamed() }

// HandedElsewhere is how many responses gave the client an address other than
// the host it was addressing.
//
// It reads better from here than from the reverse proxy: through a tunnel the
// client addresses the cloud's own name, so a republished endpoint is compared
// against what the client believes it is talking to.
func (f *Forward) HandedElsewhere() (int64, map[string]int64) { return f.rec.HandedElsewhere() }

// Tunnels is how many CONNECTs this proxy terminated.
func (f *Forward) Tunnels() int64 { return f.tunnels.Load() }

// Refused is how many CONNECTs it turned down, and for which hosts. Every one of
// them is a call the client made and this transcript does not carry.
func (f *Forward) Refused() (int64, map[string]int64) { return f.refused.seen() }

// splitTarget reads the authority a CONNECT line names.
//
// A CONNECT without a port is malformed, but a client sending one means 443 and
// answering it with a refusal would blame the wrong side.
func splitTarget(target string) (host, port string) {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return strings.Trim(target, "[]"), "443"
	}
	return host, port
}

// refusals counts what was not intercepted, and names the hosts.
type refusals struct {
	mu    sync.Mutex
	count int64
	hosts map[string]int64
}

func (r *refusals) note(host string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.count++
	if r.hosts == nil {
		r.hosts = map[string]int64{}
	}
	r.hosts[host]++
}

func (r *refusals) seen() (int64, map[string]int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]int64, len(r.hosts))
	for host, n := range r.hosts {
		out[host] = n
	}
	return r.count, out
}

// prefixed is a hijacked connection that still owes its buffered bytes.
//
// net/http hands back whatever it had already read past the CONNECT line, and
// on a client that does not wait for the 200 those bytes are the first record of
// the TLS handshake. Reading the socket directly would lose them.
// The reader is the one net/http returned rather than a MultiReader assembled
// here: it is a bufio.Reader over this same connection, so it answers what was
// buffered first and the socket afterwards, in order, with nothing to get wrong.
type prefixed struct {
	net.Conn
	first io.Reader
}

func (p *prefixed) Read(b []byte) (int, error) { return p.first.Read(b) }

// oneConnListener hands an already-accepted connection to an http.Server, and
// reports the listener closed once that connection is.
//
// The alternative — reading requests off the tunnel by hand with
// http.ReadRequest — reimplements keep-alive, chunked bodies, 100-continue and
// the half-dozen other things net/http already knows, on the one code path where
// being wrong means a transcript that quietly disagrees with the wire.
type oneConnListener struct {
	conn net.Conn
	once sync.Once
	done chan struct{}
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	var handed net.Conn
	l.once.Do(func() { handed = &closes{Conn: l.conn, done: l.done} })
	if handed != nil {
		return handed, nil
	}
	// Blocks until the served connection is closed, at which point Serve returns
	// and the tunnel's goroutine ends. Returning an error straight away would
	// have the server tear down a connection it is still serving.
	<-l.done
	return nil, net.ErrClosed
}

func (l *oneConnListener) Close() error   { return nil }
func (l *oneConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

// closes signals the listener when the served connection ends.
type closes struct {
	net.Conn
	once sync.Once
	done chan struct{}
}

func (c *closes) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.Conn.Close()
}
