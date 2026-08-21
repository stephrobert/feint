package proxy_test

// Where a terminated host's traffic actually goes (#357).
//
// --forward could send it to one place only: the host the client asked for,
// which is the real cloud. So the two useful things could not be combined —
// record a client that cannot be redirected, and have it land on the emulator —
// and getting there took a user + mount + network namespace, a replaced
// /etc/hosts inside it, a listener on port 443 in that private stack and a
// second proxy stage. These tests hold the combination that replaces all of it,
// and they hold it without leaving loopback.

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/proxy"
)

// landed is what a mapped entry's target received.
//
// It is asserted rather than assumed, because half of what #357 promises lives
// here: the requested host survives into the Host header the target reads, and
// only the socket moved.
type landed struct {
	host  string
	token string
	body  string
	query string
}

// emulatorStandIn starts a plain-HTTP listener on loopback — the shape `feint
// serve` has — and returns it with the channel it reports on.
func emulatorStandIn(t *testing.T) (*httptest.Server, <-chan landed) {
	t.Helper()
	seen := make(chan landed, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen <- landed{host: r.Host, token: r.Header.Get("X-Auth-Token"), body: string(raw), query: r.URL.RawQuery}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"servers":[{"id":"11111111-1111-1111-1111-111111111111"}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

// loopbackOnly is the transport a mapped entry is re-originated over.
//
// It refuses any address off loopback, which is this test's own guard rail: a
// mapping that stopped applying would otherwise resolve a real cloud name on the
// operator's network, and the assertions below would still pass.
func loopbackOnly(t *testing.T) *http.Transport {
	t.Helper()
	return &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
				return nil, fmt.Errorf("this test reaches loopback and nothing else, refusing %s", addr)
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}
}

// TestAMappedTunnelRecordsTheHostTheClientAsked is #357's whole point, and its
// two halves have to hold together.
//
// A client whose endpoint is a constant reaches an emulator on loopback — no
// namespace, no /etc/hosts edit, no privileged port, two environment variables —
// and what the transcript names is still the host that client asked for.
// Recording the target instead would be --upstream in disguise: a transcript of
// a session against 127.0.0.1, which loses the one fact a recording exists to
// carry.
//
// The redaction is asserted at the same door, because it is the first of #336's
// four requirements and this is a path that did not exist when it was written:
// the markers reach the target verbatim and the transcript holds none of them.
func TestAMappedTunnelRecordsTheHostTheClientAsked(t *testing.T) {
	emu, seen := emulatorStandIn(t)

	addr, authority, finish := recording(t, loopbackOnly(t), quietLog(), compiledInHost+"="+emu.URL)
	client := through(t, addr, authority)

	req, err := http.NewRequest(http.MethodPost,
		compiledInEndpoint+"/instance/v1/servers?x-amz-signature="+queryMarker,
		strings.NewReader(`{"name":"web","secret_key":"`+bodyMarker+`"}`))
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	req.Header.Set("X-Auth-Token", tokenMarker)
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("the mapped call failed: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"servers"`)) {
		t.Fatalf("the emulator's answer did not come back: %d %s", res.StatusCode, body)
	}

	// What the target received: the request the client made, at the socket the
	// operator chose, addressed to that socket by its own name.
	//
	// The authority is the one thing a mapped entry does move, and it has to:
	// feint's own rebinding guard answers 403 to a Host it does not recognise,
	// so a run that forwarded `api.scaleway.com` verbatim to the emulator would
	// record a transcript of refusals and satisfy "reaches the emulator" on a
	// technicality. Measured on 2026-08-21 before this line existed: `terraform
	// apply` of a scaleway_object_bucket landed on the emulator and every
	// request came back 403 from that guard.
	emuHost := strings.TrimPrefix(emu.URL, "http://")
	select {
	case got := <-seen:
		if got.host != emuHost {
			t.Errorf("the target was addressed as %q, want its own authority %q", got.host, emuHost)
		}
		if got.token != tokenMarker {
			t.Errorf("the target received the token %q, want it verbatim: the recorder alters "+
				"nothing on the wire", got.token)
		}
		if !strings.Contains(got.body, bodyMarker) || !strings.Contains(got.query, queryMarker) {
			t.Errorf("the target received body %q query %q, want both markers verbatim", got.body, got.query)
		}
	default:
		t.Fatal("the target received nothing: the mapped entry did not land")
	}

	recorded := finish()
	if len(recorded) != 1 {
		t.Fatalf("recorded %d exchange(s), want one", len(recorded))
	}
	x := recorded[0]
	if x.Host != compiledInHost {
		t.Errorf("the transcript names host %q, want %q: only the socket moves, never the record",
			x.Host, compiledInHost)
	}
	if x.Path != "/instance/v1/servers" || x.Status != http.StatusOK {
		t.Errorf("recorded %s %s -> %d, want POST /instance/v1/servers -> 200", x.Method, x.Path, x.Status)
	}
	if value := x.Req.Headers["X-Auth-Token"]; !proxy.IsPlaceholder(value) {
		t.Errorf("the credential reads %q in the transcript, want %q: the redaction has to survive "+
			"this door too", value, proxy.Placeholder)
	}
	for _, marker := range []string{tokenMarker, bodyMarker, queryMarker} {
		if strings.Contains(fmt.Sprintf("%+v", x), marker) {
			t.Errorf("the marker %q survived into the transcript: %+v", marker, x)
		}
	}
}

// TestABareEntryStillReachesTheRealHostBesideAMappedOne is #357's compatibility
// half, and it is asserted in one run rather than in two.
//
// A host written without a target keeps going to the host the client asked for.
// Two separate runs would prove that a proxy built with no mapping behaves as
// before, which nobody doubted; one run carrying both proves the thing that
// could actually break — that a mapping applies to its own entry and to no
// other.
func TestABareEntryStillReachesTheRealHostBesideAMappedOne(t *testing.T) {
	c := stubCloud(t, compiledInHost, func(w http.ResponseWriter, r *http.Request) {
		// The bare entry's own promise, asserted here because this is the run
		// where a mapped entry sits beside it: the real host is still addressed
		// by the name the client used, so a SigV4 signature over Host still
		// verifies. Only a mapped entry moves the authority.
		if r.Host != compiledInHost {
			w.WriteHeader(http.StatusTeapot)
			_, _ = fmt.Fprintf(w, "the real host was addressed as %q, want %q", r.Host, compiledInHost)
			return
		}
		_, _ = io.WriteString(w, `{"from":"the real host"}`)
	})
	emu, seen := emulatorStandIn(t)

	// One transport for both destinations: the stand-in cloud under its name,
	// loopback for the mapped target, nothing else.
	both := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr == net.JoinHostPort(compiledInHost, "443") {
				return (&net.Dialer{}).DialContext(ctx, network, c.server.Listener.Addr().String())
			}
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			}
			return nil, fmt.Errorf("this test reaches %s and loopback, refusing %s", compiledInHost, addr)
		},
		TLSClientConfig: &tls.Config{RootCAs: c.transport.TLSClientConfig.RootCAs, MinVersion: tls.VersionTLS12},
	}

	addr, authority, finish := recording(t, both, quietLog(),
		compiledInHost, "sos.scaleway.test="+emu.URL)
	client := through(t, addr, authority)

	bare, err := client.Get(compiledInEndpoint + "/instance/v1/servers")
	if err != nil {
		t.Fatalf("the bare entry did not reach the host the client asked for: %v", err)
	}
	bareBody, _ := io.ReadAll(bare.Body)
	_ = bare.Body.Close()
	if !bytes.Contains(bareBody, []byte("the real host")) {
		t.Errorf("the bare entry answered %s, want the stand-in cloud's own body", bareBody)
	}

	mapped, err := client.Get("https://sos.scaleway.test/bucket")
	if err != nil {
		t.Fatalf("the mapped entry did not reach its target: %v", err)
	}
	_, _ = io.Copy(io.Discard, mapped.Body)
	_ = mapped.Body.Close()

	select {
	case got := <-seen:
		if want := strings.TrimPrefix(emu.URL, "http://"); got.host != want {
			t.Errorf("the target was addressed as %q, want its own authority %q", got.host, want)
		}
	default:
		t.Fatal("the mapped entry did not land on its target")
	}

	recorded := finish()
	if len(recorded) != 2 {
		t.Fatalf("recorded %d exchange(s), want the two the client made", len(recorded))
	}
	hosts := map[string]bool{}
	for _, x := range recorded {
		hosts[x.Host] = true
	}
	if !hosts[compiledInHost] || !hosts["sos.scaleway.test"] {
		t.Errorf("the transcript names %v, want both hosts the client asked for", hosts)
	}
}

// TestAMappedRouteDoesNotWidenTheAllowlist is the fourth of #336's requirements,
// re-asserted at the door #357 opened.
//
// Naming a target must not turn the allowlist into a wildcard: a host nobody
// wrote down is still refused, and still absent from the transcript. Without
// this, "only the hosts you name are intercepted" would be a property of the
// version before the mapping existed.
func TestAMappedRouteDoesNotWidenTheAllowlist(t *testing.T) {
	emu, _ := emulatorStandIn(t)

	addr, authority, finish := recording(t, loopbackOnly(t), quietLog(), compiledInHost+"="+emu.URL)
	client := through(t, addr, authority)

	res, err := client.Get(compiledInEndpoint + "/instance/v1/servers")
	if err != nil {
		t.Fatalf("the mapped host was not reachable: %v", err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()

	for _, elsewhere := range []string{
		"https://telemetry.example.test/collect",
		"https://a.b.scaleway.test/collect",
	} {
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

	recorded := finish()
	if len(recorded) != 1 || recorded[0].Host != compiledInHost {
		t.Fatalf("recorded %d exchange(s), want only the named host's: %+v", len(recorded), recorded)
	}
}

// TestARefusalNamesTheEntryThatIsMissing is #357's last acceptance criterion.
//
// An API family can live on a different host than the main one — Outscale's
// managed-Kubernetes API does — and the proxy rightly refuses a host it was not
// told about. What it must not do is leave the operator reading that as a
// network fault: the answer names the host and writes out the entry to add, in
// the form the flag takes.
//
// Driven in the absolute form, because that is where the body reaches the
// caller: through a CONNECT the client's transport reports the status line
// alone, and the same text also goes to the log and to the exit summary.
func TestARefusalNamesTheEntryThatIsMissing(t *testing.T) {
	writer := proxy.NewWriter(io.Discard, 0)
	defer func() { _ = writer.Close() }()

	f, err := proxy.NewForward(proxy.ForwardOptions{
		Hosts:  []string{"api.scaleway.test=http://127.0.0.1:4599"},
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

	res, err := client.Get("http://api.eu-west-2.outscale.test/api/v1/ReadVms")
	if err != nil {
		t.Fatalf("the refusal did not come back as an answer: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("a host --forward does not name answered %d, want 403", res.StatusCode)
	}
	for _, want := range []string{"api.eu-west-2.outscale.test", "--forward"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the refusal does not name %q, so a missing entry reads as a connection "+
				"failure: %s", want, body)
		}
	}
}

// TestAForwardTargetMustNameASocket refuses a mapping that would rewrite more
// than the socket.
//
// A target carrying a path would silently prepend it to every request the client
// makes, and the transcript — which records the client's own request line —
// would not show it. A recorder whose own rewrite is invisible in its output is
// the one thing this package must never be. User info is refused for its own
// reason: it is a credential written on a command line.
func TestAForwardTargetMustNameASocket(t *testing.T) {
	writer := proxy.NewWriter(io.Discard, 0)
	defer func() { _ = writer.Close() }()

	for name, entry := range map[string]string{
		"no target":     "api.scaleway.test=",
		"blank target":  "api.scaleway.test=   ",
		"no scheme":     "api.scaleway.test=127.0.0.1:4599",
		"wrong scheme":  "api.scaleway.test=tcp://127.0.0.1:4599",
		"no host":       "api.scaleway.test=http://",
		"carries path":  "api.scaleway.test=http://127.0.0.1:4599/v2",
		"carries query": "api.scaleway.test=http://127.0.0.1:4599?zone=fr-par",
		"carries user":  "api.scaleway.test=http://someone@127.0.0.1:4599",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := proxy.NewForward(proxy.ForwardOptions{Hosts: []string{entry}, Writer: writer}); err == nil {
				t.Errorf("--forward %q was accepted", entry)
			}
		})
	}

	// The accepting half, so a guard that refused every target would fail here:
	// a bare socket, the trailing slash a shell completion adds, a TLS target,
	// and the wildcard a per-zone endpoint needs.
	for name, entry := range map[string]string{
		"plain":          "api.scaleway.test=http://127.0.0.1:4599",
		"trailing slash": "api.scaleway.test=http://127.0.0.1:4599/",
		"tls target":     "api.scaleway.test=https://127.0.0.1:4599",
		"wildcard":       "*.exoscale.test=http://127.0.0.1:4599",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := proxy.NewForward(proxy.ForwardOptions{Hosts: []string{entry}, Writer: writer}); err != nil {
				t.Errorf("--forward %q was refused: %v", entry, err)
			}
		})
	}
}
