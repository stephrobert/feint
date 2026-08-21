package proxy_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/proxy"
	"github.com/stephrobert/feint/internal/trace"
)

// compiledInEndpoint stands for Pépin's own `https://api.scaleway.com`: an
// endpoint a collector carries in its source and no environment can move.
//
// A reserved TLD (RFC 6761) rather than the real name, on purpose. The stand-in
// cloud below is reached by a transport that maps this one address and refuses
// every other, so a mapping that stopped applying fails here instead of reaching
// a real, billed account — which is the difference between a test that is
// offline and a test that is offline as long as nothing breaks.
const compiledInEndpoint = "https://api.scaleway.test"

const compiledInHost = "api.scaleway.test"

// The markers, one per carrier the recorder can see through a tunnel.
//
// consumerMarker is the one that matters most. `X-Consumer` matches none of the
// eight name patterns in carriers, and a denylist wrote it out in full while
// redacting an X-Auth-Token holding the very same value (measured 2026-08-10,
// see redact.go). It is here so that "the redaction survives interception" is
// answered by the rule that actually holds — the allowlist — and not by a name
// that happens to look like a secret.
const (
	tokenMarker    = "MARKER-connect-x-auth-token"
	consumerMarker = "MARKER-connect-x-consumer"
	bodyMarker     = "MARKER-connect-body-secret"
	queryMarker    = "MARKER-connect-query-signature"
)

// cloud is a local HTTPS server standing in for a real one, plus the transport
// that reaches it.
//
// The handler is where a test asserts what the cloud received: a recorder that
// broke the request on its way out would otherwise pass a redaction check by
// destroying the very exchange it is supposed to be measuring.
type cloud struct {
	server    *httptest.Server
	transport *http.Transport
}

// quietLog keeps a refused tunnel out of the test output. The refusal itself is
// asserted through Refused(), which is the counter an operator reads.
func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// stubCloud starts an HTTPS server answering as host, and returns the transport
// that reaches it under that name.
//
// The transport refuses any address but the one it maps. That refusal is the
// test's own guard rail: without it, a proxy that re-originated to the wrong
// place would quietly resolve a real hostname on the operator's network.
func stubCloud(t *testing.T, host string, handler http.HandlerFunc) *cloud {
	t.Helper()

	cert, err := proxy.MintInterceptor(host)
	if err != nil {
		t.Fatalf("mint the stand-in cloud's certificate: %v", err)
	}
	c := &cloud{}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = cert.ServerTLSConfig()
	server.StartTLS()
	t.Cleanup(server.Close)

	c.server = server
	c.transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr != net.JoinHostPort(host, "443") {
				return nil, fmt.Errorf("this test reaches %s and nothing else, refusing %s", host, addr)
			}
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
		TLSClientConfig: &tls.Config{RootCAs: cert.CAPool(), MinVersion: tls.VersionTLS12},
	}
	return c
}

// forwarding starts a forward proxy in front of a stand-in cloud, and returns
// its address, its authority and a function that shuts it down and reads the
// transcript back.
func forwarding(t *testing.T, c *cloud, hosts ...string) (addr string, authority *proxy.Interceptor, finish func() []trace.Exchange) {
	t.Helper()
	return recording(t, c.transport, nil, hosts...)
}

// recording is forwarding over any transport, which is what a mapped entry
// (#357) needs: its traffic goes to a socket on loopback rather than to the
// stand-in cloud, so the transport that reaches the one cannot reach the other.
func recording(t *testing.T, tr http.RoundTripper, log *slog.Logger, hosts ...string) (addr string, authority *proxy.Interceptor, finish func() []trace.Exchange) {
	t.Helper()

	authority, err := proxy.MintAuthority()
	if err != nil {
		t.Fatalf("mint the authority: %v", err)
	}
	path := filepath.Join(t.TempDir(), "run.jsonl")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("open the transcript: %v", err)
	}
	writer := proxy.NewWriter(file, 0)

	f, err := proxy.NewForward(proxy.ForwardOptions{
		Hosts:     hosts,
		Writer:    writer,
		Authority: authority,
		Transport: tr,
		Log:       log,
	})
	if err != nil {
		t.Fatalf("build the forward proxy: %v", err)
	}
	front := httptest.NewServer(f)

	return front.Listener.Addr().String(), authority, func() []trace.Exchange {
		front.Close()
		if err := writer.Close(); err != nil {
			t.Fatalf("close the writer: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("close the transcript: %v", err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read the transcript: %v", err)
		}
		return read(t, raw)
	}
}

// through builds a client that reaches the cloud only by way of the proxy, and
// trusts the proxy's authority the way SSL_CERT_FILE makes a separate process
// trust it.
func through(t *testing.T, proxyAddr string, authority *proxy.Interceptor) *http.Client {
	t.Helper()
	target, err := url.Parse("http://" + proxyAddr)
	if err != nil {
		t.Fatalf("parse the proxy address: %v", err)
	}
	return &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(target),
		TLSClientConfig: &tls.Config{RootCAs: authority.CAPool(), MinVersion: tls.VersionTLS12},
	}}
}

// TestASecretHeaderIsStillRedactedThroughCONNECT is the requirement #336 puts
// first, and the one a falsification is pointed at.
//
// An intercepting proxy holding its own certificate authority is a
// credential-harvesting tool by construction: it decrypts, and it writes down.
// The only thing between that and a file full of live tenant keys is that the
// CONNECT path records through the same [proxy.Redacted] as everything else.
// This drives a real TLS session through a real tunnel and asserts both ends of
// it — the cloud received every marker verbatim, the transcript carries none of
// them, and the header names are all still there to read.
//
// Remove the redactExchange call in capture and this goes red. That is the
// condition for "the redaction survives interception" counting as a control
// rather than as a sentence in a design note.
func TestASecretHeaderIsStillRedactedThroughCONNECT(t *testing.T) {
	c := stubCloud(t, compiledInHost, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// The Host the client sent reaches the cloud unaltered, which is more
		// than tidiness: a SigV4 signature covers this header, and a reverse
		// proxy re-originating under its own name is exactly why a signed client
		// cannot be recorded that way (see docs/proxy.md). Through a tunnel the
		// client addresses the cloud's own name and this must still be it.
		if r.Host != compiledInHost {
			w.WriteHeader(http.StatusTeapot)
			_, _ = fmt.Fprintf(w, "the cloud was addressed as %q, want %q", r.Host, compiledInHost)
			return
		}
		// The upstream asserts what it received, so a redactor that mangled the
		// request on its way out cannot pass by destroying the exchange.
		for name, want := range map[string]string{
			"X-Auth-Token": tokenMarker,
			"X-Consumer":   consumerMarker,
		} {
			if got := r.Header.Get(name); got != want {
				w.WriteHeader(http.StatusTeapot)
				_, _ = fmt.Fprintf(w, "the cloud received %s = %q, want %q", name, got, want)
				return
			}
		}
		if !bytes.Contains(body, []byte(bodyMarker)) {
			w.WriteHeader(http.StatusTeapot)
			_, _ = io.WriteString(w, "the cloud did not receive the body marker")
			return
		}
		if got := r.URL.Query().Get("x-amz-signature"); got != queryMarker {
			w.WriteHeader(http.StatusTeapot)
			_, _ = fmt.Fprintf(w, "the cloud received the query signature %q", got)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Session-Token", tokenMarker)
		_, _ = io.WriteString(w, `{"servers":[{"id":"11111111-1111-1111-1111-111111111111","secret":"`+bodyMarker+`"}]}`)
	})

	addr, authority, finish := forwarding(t, c, compiledInHost)
	client := through(t, addr, authority)

	body := `{"name":"web","volumes":{"0":{"secret_key":"` + bodyMarker + `"}}}`
	req, err := http.NewRequest(http.MethodPost,
		compiledInEndpoint+"/instance/v1/zones/fr-par-1/servers?x-amz-signature="+queryMarker,
		strings.NewReader(body))
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Token", tokenMarker)
	// The header a name check answers "no" about and a tenant's account answers
	// "yes" about.
	req.Header.Set("X-Consumer", consumerMarker)

	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("the tunnelled request failed: %v", err)
	}
	answer, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the stand-in cloud refused the exchange (%d): %s", res.StatusCode, answer)
	}
	if !bytes.Contains(answer, []byte(bodyMarker)) {
		t.Fatalf("the client did not receive the cloud's answer unaltered: %s", answer)
	}

	recorded := finish()
	if len(recorded) != 1 {
		t.Fatalf("the tunnel recorded %d exchange(s), want 1", len(recorded))
	}
	x := recorded[0]

	// The subject is asserted present before anything is asserted about it: a
	// transcript that recorded nothing carries no marker either, and would pass
	// a leak check that only searched.
	if x.Host != compiledInHost {
		t.Errorf("the exchange names host %q, want %q: a tunnelled transcript that cannot say which "+
			"cloud answered is unreadable the moment two of them are in one file", x.Host, compiledInHost)
	}
	if x.Path != "/instance/v1/zones/fr-par-1/servers" || x.Status != http.StatusOK {
		t.Errorf("the exchange is %s %s -> %d, want the tunnelled POST", x.Method, x.Path, x.Status)
	}
	for _, name := range []string{"X-Auth-Token", "X-Consumer"} {
		value, carried := x.Req.Headers[name]
		if !carried {
			t.Errorf("%s is missing from the transcript: the name always stays, only the value goes", name)
			continue
		}
		if !proxy.IsPlaceholder(value) {
			t.Errorf("%s reads %q, want %q", name, value, proxy.Placeholder)
		}
	}
	if value := x.Res.Headers["X-Session-Token"]; !proxy.IsPlaceholder(value) {
		t.Errorf("the response header X-Session-Token reads %q, want %q", value, proxy.Placeholder)
	}

	line, err := json.Marshal(x)
	if err != nil {
		t.Fatalf("re-encode the exchange: %v", err)
	}
	for _, marker := range []string{tokenMarker, consumerMarker, bodyMarker, queryMarker} {
		if bytes.Contains(line, []byte(marker)) {
			t.Errorf("the transcript carries %s in clear:\n%s", marker, line)
		}
	}
}

// TestAClientWithACompiledInEndpointIsRecordedEndToEnd is #336's acceptance
// criterion, run as the issue words it: a Go client with no configurable
// endpoint, recorded end to end, against a local HTTPS server, with no cloud
// account anywhere near it.
//
// The client is a separate process, and that is the point rather than
// thoroughness. In this one everything is arranged — the transport, the trust
// pool, the proxy — and none of that is available in a tool one is trying to
// measure. The child is given two environment variables and nothing else:
// HTTPS_PROXY and SSL_CERT_FILE. It calls a constant. It installs no Transport,
// so it inherits http.DefaultTransport and therefore ProxyFromEnvironment, which
// is the exact property #336 measured on Pépin's collectors.
func TestAClientWithACompiledInEndpointIsRecordedEndToEnd(t *testing.T) {
	c := stubCloud(t, compiledInHost, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Auth-Token"); got != tokenMarker {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"servers":[{"id":"11111111-1111-1111-1111-111111111111","name":"web"}]}`)
	})

	addr, authority, finish := forwarding(t, c, compiledInHost)

	caPath := filepath.Join(t.TempDir(), "feint-ca.pem")
	if err := authority.WriteCA(caPath); err != nil {
		t.Fatalf("publish the CA: %v", err)
	}

	child := exec.Command(os.Args[0], "-test.run=^TestCompiledInClientHelper$", "-test.v")
	child.Env = append(os.Environ(),
		"FEINT_COMPILED_IN_CLIENT=1",
		"HTTPS_PROXY=http://"+addr,
		"SSL_CERT_FILE="+caPath,
		// Emptied rather than inherited: a NO_PROXY on the operator's station
		// would take the child around the proxy and this test would measure the
		// station's environment instead of the proxy.
		"NO_PROXY=",
		"no_proxy=",
	)
	out, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("the client process failed: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("CHILD-OK")) {
		t.Fatalf("the client process did not report a successful call:\n%s", out)
	}

	recorded := finish()
	if len(recorded) != 1 {
		t.Fatalf("recorded %d exchange(s), want the one the child made:\n%s", len(recorded), out)
	}
	x := recorded[0]
	if x.Host != compiledInHost || x.Path != "/instance/v1/servers" || x.Status != http.StatusOK {
		t.Errorf("recorded %s %s%s -> %d, want GET %s/instance/v1/servers -> 200",
			x.Method, x.Host, x.Path, x.Status, compiledInHost)
	}
	if value := x.Req.Headers["X-Auth-Token"]; !proxy.IsPlaceholder(value) {
		t.Errorf("the child's credential reads %q in the transcript, want %q", value, proxy.Placeholder)
	}
	servers, ok := x.Res.Body.(map[string]any)
	if !ok || servers["servers"] == nil {
		t.Errorf("the recorded body is not the cloud's answer: %#v", x.Res.Body)
	}
}

// TestCompiledInClientHelper is the child process of the test above, and is a
// no-op anywhere else.
//
// It is deliberately as dumb as a collector: one constant, one header, one call
// through http.DefaultClient. Nothing here knows a proxy exists.
func TestCompiledInClientHelper(t *testing.T) {
	if os.Getenv("FEINT_COMPILED_IN_CLIENT") == "" {
		t.Skip("the child half of TestAClientWithACompiledInEndpointIsRecordedEndToEnd")
	}
	req, err := http.NewRequest(http.MethodGet, compiledInEndpoint+"/instance/v1/servers", nil)
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	req.Header.Set("X-Auth-Token", tokenMarker)

	// On every platform but macOS this is http.DefaultClient untouched, which is
	// the property #336 is about: two environment variables and not one line of
	// the traced tool.
	//
	// macOS is the exception, and it is a property of Go rather than of this
	// proxy: crypto/x509 reads the system keychain there, and the code path that
	// consults SSL_CERT_FILE carries a build constraint excluding darwin. The
	// child therefore cannot trust this run's authority from the environment,
	// and the first CI run on macos-15 said so in as many words —
	// "x509: certificate signed by unknown authority", while the proxy logged
	// "the client did not complete the tunnel handshake".
	//
	// Loading the CA explicitly here keeps macOS proving what this test exists
	// to prove — that the tunnel records a client whose endpoint is a constant —
	// while docs/proxy.md and docs/limits.md carry the part it can no longer
	// prove there: that nothing about the client had to change.
	client := http.DefaultClient
	if runtime.GOOS == "darwin" {
		pem, err := os.ReadFile(os.Getenv("SSL_CERT_FILE"))
		if err != nil {
			t.Fatalf("read the authority macOS will not read for us: %v", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			t.Fatal("the authority did not parse")
		}
		client = &http.Client{Transport: &http.Transport{
			Proxy:           http.ProxyFromEnvironment,
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		}}
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("the call failed: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the cloud answered %d: %s", res.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"servers"`)) {
		t.Fatalf("the answer is not the cloud's: %s", body)
	}
	t.Log("CHILD-OK")
}

// TestAHostNobodyNamedIsNotIntercepted holds the bound on what this door
// decrypts.
//
// A forward proxy that terminated every CONNECT would decrypt whatever else the
// measured process does — a package index, an identity provider, a telemetry
// endpoint — and file it beside the measurement. So the hosts are named, and a
// tunnel to any other is refused rather than relayed blind: a blind relay would
// let the client finish its session while the transcript silently missed every
// exchange in it, which is the defect handoff.go exists to report.
//
// Both halves are asserted in one run, against one proxy: the named host is
// recorded, the unnamed one is refused, counted and absent.
func TestAHostNobodyNamedIsNotIntercepted(t *testing.T) {
	c := stubCloud(t, compiledInHost, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true}`)
	})

	authority, err := proxy.MintAuthority()
	if err != nil {
		t.Fatalf("mint the authority: %v", err)
	}
	path := filepath.Join(t.TempDir(), "run.jsonl")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("open the transcript: %v", err)
	}
	writer := proxy.NewWriter(file, 0)
	// A wildcard, because that is what a per-zone endpoint needs, and because it
	// is where "named" is easiest to get wrong: it covers one label and never
	// two, exactly as a certificate's does.
	f, err := proxy.NewForward(proxy.ForwardOptions{
		Hosts:     []string{"*.scaleway.test"},
		Writer:    writer,
		Authority: authority,
		Transport: c.transport,
		Log:       quietLog(),
	})
	if err != nil {
		t.Fatalf("build the forward proxy: %v", err)
	}
	front := httptest.NewServer(f)
	client := through(t, front.Listener.Addr().String(), authority)

	// The named host: recorded.
	res, err := client.Get(compiledInEndpoint + "/instance/v1/servers")
	if err != nil {
		t.Fatalf("the named host was not reachable: %v", err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()

	// The ones nobody named: refused, and the client is told so rather than left
	// with a tunnel that goes nowhere. The second is the wildcard's own bound —
	// `*.scaleway.test` must not swallow a deeper name.
	for _, elsewhere := range []string{
		"https://telemetry.example.test/collect",
		"https://a.b.scaleway.test/collect",
	} {
		// The transport reports the refused CONNECT by its status text, which is
		// what the operator will read in the client's own error.
		answered, err := client.Get(elsewhere)
		if err == nil {
			_ = answered.Body.Close()
			t.Errorf("a CONNECT to %s, which --forward does not name, was accepted", elsewhere)
			continue
		}
		if !strings.Contains(err.Error(), "Forbidden") {
			t.Errorf("the refusal of %s did not reach the client as a refusal: %v", elsewhere, err)
		}
	}

	front.Close()
	if err := writer.Close(); err != nil {
		t.Fatalf("close the writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close the transcript: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the transcript: %v", err)
	}

	recorded := read(t, raw)
	if len(recorded) != 1 || recorded[0].Host != compiledInHost {
		t.Fatalf("recorded %d exchange(s), want only the named host's: %s", len(recorded), raw)
	}
	if tunnels := f.Tunnels(); tunnels != 1 {
		t.Errorf("terminated %d tunnel(s), want 1", tunnels)
	}
	refused, hosts := f.Refused()
	if refused != 2 || hosts["telemetry.example.test"] != 1 || hosts["a.b.scaleway.test"] != 1 {
		t.Errorf("refused %d connection(s) %v, want one each to telemetry.example.test and "+
			"a.b.scaleway.test: a refusal nobody counts is a call the operator cannot know is missing",
			refused, hosts)
	}
}

// TestAPlainHTTPRequestThroughTheForwardProxyIsRecorded covers the other form a
// forward proxy is addressed in.
//
// HTTP_PROXY produces an absolute-form request rather than a CONNECT, and a
// proxy that answered it with a 400 would look broken to a client that has
// nothing wrong with it. The allowlist applies here too: the destination is
// still a host the operator did or did not name.
func TestAPlainHTTPRequestThroughTheForwardProxyIsRecorded(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"plain":true}`)
	}))
	defer upstream.Close()
	host := upstream.Listener.Addr().String()

	path := filepath.Join(t.TempDir(), "run.jsonl")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("open the transcript: %v", err)
	}
	writer := proxy.NewWriter(file, 0)
	f, err := proxy.NewForward(proxy.ForwardOptions{
		Hosts:  []string{"127.0.0.1"},
		Writer: writer,
		Log:    quietLog(),
	})
	if err != nil {
		t.Fatalf("build the forward proxy: %v", err)
	}
	front := httptest.NewServer(f)
	defer front.Close()

	target, err := url.Parse(front.URL)
	if err != nil {
		t.Fatalf("parse the proxy URL: %v", err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(target)}}

	res, err := client.Get("http://" + host + "/v2/instance")
	if err != nil {
		t.Fatalf("the plain request failed: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if !bytes.Contains(body, []byte(`"plain"`)) {
		t.Fatalf("the upstream's answer did not come back: %s", body)
	}

	// And a host nobody named is refused in this form too. Here the refusal is
	// the answer itself rather than a failed tunnel, so it is read as a status.
	refusal, err := client.Get("http://elsewhere.example.test/v2/instance")
	if err != nil {
		t.Fatalf("the refused request did not come back as an answer: %v", err)
	}
	_, _ = io.Copy(io.Discard, refusal.Body)
	_ = refusal.Body.Close()
	if refusal.StatusCode != http.StatusForbidden {
		t.Errorf("a plain request to a host --forward does not name answered %d, want 403", refusal.StatusCode)
	}

	front.Close()
	if err := writer.Close(); err != nil {
		t.Fatalf("close the writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close the transcript: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the transcript: %v", err)
	}
	recorded := read(t, raw)
	if len(recorded) != 1 || recorded[0].Path != "/v2/instance" {
		t.Fatalf("recorded %d exchange(s), want the plain one alone: %s", len(recorded), raw)
	}
}

// TestTheForwardProxyMintsUnderOneEphemeralAuthority pins the third security
// requirement: one authority for the run, generated on the fly, trusted through
// the file the client was given and nothing else.
//
// The property that has teeth is "one". A leaf minted under a second authority
// would be a certificate nothing has been told to trust, and every call after it
// would fail with an error naming the wrong cause — so the test asks the
// authority for two hosts it has never seen and verifies both against the single
// pool published before either existed.
func TestTheForwardProxyMintsUnderOneEphemeralAuthority(t *testing.T) {
	authority, err := proxy.MintAuthority()
	if err != nil {
		t.Fatalf("mint the authority: %v", err)
	}
	published := authority.CAPool()

	for _, host := range []string{compiledInHost, "api-ch-gva-2.exoscale.test", "127.0.0.1"} {
		cfg, err := authority.TLSFor(host)
		if err != nil {
			t.Fatalf("mint a leaf for %s: %v", host, err)
		}
		if len(cfg.Certificates) != 1 {
			t.Fatalf("the configuration for %s presents %d certificate(s)", host, len(cfg.Certificates))
		}
		leaf, err := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
		if err != nil {
			t.Fatalf("parse the leaf for %s: %v", host, err)
		}
		if leaf.IsCA {
			t.Errorf("the leaf for %s is itself a CA: a client trusting it would trust anything it signs", host)
		}
		if err := leaf.VerifyHostname(host); err != nil {
			t.Errorf("the leaf minted for %s does not cover it: %v", host, err)
		}
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: published, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
			t.Errorf("the leaf for %s does not chain to the CA the client was given: %v", host, err)
		}
	}

	// The same host twice is the same leaf: a session is hundreds of requests
	// over a handful of tunnels, and a key generation per connection would sit on
	// the critical path of every one of them.
	first, err := authority.TLSFor(compiledInHost)
	if err != nil {
		t.Fatalf("mint again: %v", err)
	}
	second, err := authority.TLSFor(compiledInHost)
	if err != nil {
		t.Fatalf("mint again: %v", err)
	}
	if !bytes.Equal(first.Certificates[0].Certificate[0], second.Certificates[0].Certificate[0]) {
		t.Error("two tunnels to one host were served two different certificates")
	}

	// And nothing was written anywhere: the CA exists in memory until a caller
	// asks for it by path.
	if _, err := authority.TLSFor(""); err == nil {
		t.Error("a certificate covering no host was minted")
	}
}

// TestAForwardProxyRefusesAnUnusableHostSet holds the two ends of the allowlist.
//
// Empty intercepts nothing and records nothing; `*` intercepts everything, which
// is the difference between a recorder and a wiretap and is one character away
// on the command line.
//
// The mapped forms are here for the reason #357 could have broken this without
// anybody noticing: `*=http://127.0.0.1:4599` is the same wiretap as `*`, and a
// guard still reading the whole entry rather than the name it cuts out would
// accept it, because the string no longer equals "*". The wiretap would then be
// one that files everything it decrypts into a transcript on the operator's own
// disk.
func TestAForwardProxyRefusesAnUnusableHostSet(t *testing.T) {
	writer := proxy.NewWriter(io.Discard, 0)
	defer func() { _ = writer.Close() }()

	for name, hosts := range map[string][]string{
		"none":                       nil,
		"blank":                      {"  ", ""},
		"everything":                 {"*"},
		"everything twice":           {"api.scaleway.test", "*.*"},
		"everything, mapped":         {"*=http://127.0.0.1:4599"},
		"everything, mapped, spaced": {" * = http://127.0.0.1:4599 "},
		"everything twice, mapped":   {"api.scaleway.test", "*.*=http://127.0.0.1:4599"},
		"a target and no host":       {"=http://127.0.0.1:4599"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := proxy.NewForward(proxy.ForwardOptions{Hosts: hosts, Writer: writer}); err == nil {
				t.Errorf("--forward %v was accepted", hosts)
			}
		})
	}

	// The accepting half: a name, and the single-level wildcard a per-zone
	// endpoint needs.
	if _, err := proxy.NewForward(proxy.ForwardOptions{
		Hosts:  []string{"api.scaleway.test", "*.exoscale.test"},
		Writer: writer,
	}); err != nil {
		t.Errorf("a usable host set was refused: %v", err)
	}
}
