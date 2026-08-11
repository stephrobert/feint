package proxy_test

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/proxy"
)

// recorder builds a proxy in front of an upstream and hands back both the
// address a client uses and the proxy itself, which the shared helper does not
// expose — these tests assert on a counter rather than on the file.
func recorder(t *testing.T, upstream string) (client string, p *proxy.Proxy) {
	t.Helper()

	target, err := url.Parse(upstream)
	if err != nil {
		t.Fatalf("parse the upstream URL: %v", err)
	}
	file, err := os.OpenFile(filepath.Join(t.TempDir(), "run.jsonl"),
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("open the transcript: %v", err)
	}
	writer := proxy.NewWriter(file, 0)

	p, err = proxy.New(proxy.Options{Upstream: target, Writer: writer})
	if err != nil {
		t.Fatalf("build the proxy: %v", err)
	}
	front := httptest.NewServer(p)
	t.Cleanup(func() {
		front.Close()
		_ = writer.Close()
		_ = file.Close()
	})
	return front.URL, p
}

func get(t *testing.T, addr string) {
	t.Helper()
	res, err := http.Get(addr + "/v2/zone") //nolint:noctx // test client
	if err != nil {
		t.Fatalf("GET through the proxy: %v", err)
	}
	_ = res.Body.Close()
}

// TestAnAnswerThatSendsTheClientElsewhereIsCounted is the defect #92 named.
//
// Exoscale publishes an `api-endpoint` per zone in `GET /v2/zone`, and `exo`
// follows it for everything after. Against a real cloud that address is the
// real cloud, so the proxy sees the first exchange and none of the rest: a
// session worth about ninety exchanges recorded eight, and the transcript said
// nothing about the other eighty-two. It looked complete, which is the one
// thing a recorder must never look when it is not.
//
// The proxy does not know the field name, and must not: three provider names in
// a tool that has none would leave a fourth API silent again. It knows the
// shape — an absolute URL naming a host that is not the one the client is
// talking to.
func TestAnAnswerThatSendsTheClientElsewhereIsCounted(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"zones":[{"name":"ch-dk-2","api-endpoint":"https://api-ch-dk-2.exoscale.com/v2"}]}`)
	}))
	defer upstream.Close()

	client, p := recorder(t, upstream.URL)
	get(t, client)

	handed, hosts := p.HandedElsewhere()
	if handed != 1 {
		t.Fatalf("HandedElsewhere counted %d, want 1", handed)
	}
	if hosts["api-ch-dk-2.exoscale.com"] != 1 {
		t.Errorf("the host the client was sent to is not named: %v", hosts)
	}
}

// TestAnAnswerNamingThisProxyIsNotAHandoff is the half that keeps the signal
// worth reading. An emulator serving this same route publishes an api-endpoint
// pointing at itself — internal/providers/exoscale/catalog.go does exactly that,
// and says it is the whole reason the route exists. Counting that as a handoff
// would raise the alarm on every correct recording, and an alarm that fires on
// the normal case gets ignored, which costs more than the silence it replaced.
func TestAnAnswerNamingThisProxyIsNotAHandoff(t *testing.T) {
	var upstream *httptest.Server
	proxied := make(chan string, 1)

	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The emulator answers with the address the client used, which through a
		// proxy is the proxy's own. r.Host here is the upstream's, so the test
		// feeds back what the client asked for.
		self := <-proxied
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"zones":[{"name":"ch-dk-2","api-endpoint":"http://%s/v2"}]}`, self)
	}))
	defer upstream.Close()

	client, p := recorder(t, upstream.URL)
	proxied <- strings.TrimPrefix(client, "http://")
	get(t, client)

	if handed, hosts := p.HandedElsewhere(); handed != 0 {
		t.Fatalf("an answer naming this proxy was reported as a handoff: %d %v", handed, hosts)
	}
}

// TestAGzippedHandoffIsStillSeen holds the blind spot shut. `scw` and `exo`
// both send Accept-Encoding, so on a real session almost every body arrives
// compressed: a scan over the raw bytes would find no URL in any of them and
// report nothing found, which reads exactly like nothing being there.
func TestAGzippedHandoffIsStillSeen(t *testing.T) {
	var packed bytes.Buffer
	zw := gzip.NewWriter(&packed)
	if _, err := zw.Write([]byte(
		`{"zones":[{"api-endpoint":"https://api-de-fra-1.exoscale.com/v2"}]}`)); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close the gzip writer: %v", err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(packed.Bytes())
	}))
	defer upstream.Close()

	client, p := recorder(t, upstream.URL)
	get(t, client)

	handed, hosts := p.HandedElsewhere()
	if handed != 1 {
		t.Fatalf("a gzipped handoff was not seen: %d %v", handed, hosts)
	}
	if hosts["api-de-fra-1.exoscale.com"] != 1 {
		t.Errorf("the host is not named: %v", hosts)
	}
}

// TestAnAnswerWithNoAddressInItIsNotAHandoff: the ordinary case, which is most
// of them. A body that carries no absolute URL cannot send anybody anywhere.
func TestAnAnswerWithNoAddressInItIsNotAHandoff(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"server":{"id":"a-b-c","state":"running","private_ip":"10.0.0.4"}}`)
	}))
	defer upstream.Close()

	client, p := recorder(t, upstream.URL)
	get(t, client)

	if handed, hosts := p.HandedElsewhere(); handed != 0 {
		t.Fatalf("an ordinary answer was reported as a handoff: %d %v", handed, hosts)
	}
}
