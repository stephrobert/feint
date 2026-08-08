package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/exoscale"
	"github.com/stephrobert/feint/internal/providers/outscale"
	"github.com/stephrobert/feint/internal/providers/scaleway"
	"github.com/stephrobert/feint/internal/proxy"
)

// A response of the size these APIs actually answer: a server list of a handful
// of entries is a few kilobytes, and the shape matters because the recorder
// decodes it.
const benchAnswer = `{"servers":[{"id":"11111111-1111-1111-1111-111111111111",` +
	`"name":"conformance-1","state":"running","commercial_type":"DEV1-S",` +
	`"public_ips":[{"id":"22222222-2222-2222-2222-222222222222","address":"51.15.0.1"}],` +
	`"tags":["a","b"],"zone":"fr-par-1"}]}`

func benchUpstream() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, benchAnswer)
	}))
}

// The baseline: a client talking to the upstream with nothing in between.
//
// It exists so the number beside it means something. "The proxy adds 0.2 ms" is
// only a claim about the proxy if the same request without it was measured on the
// same machine in the same run.
func BenchmarkDirectRoundTrip(b *testing.B) {
	upstream := benchUpstream()
	defer upstream.Close()
	benchRoundTrip(b, upstream.URL)
}

// The same request through the proxy, recording to /dev/null.
//
// io.Discard rather than a file, on purpose: this measures what the proxy adds to
// the request path, and the whole design is that the disk is not on it. A file
// would measure the disk.
func BenchmarkProxyRoundTrip(b *testing.B) {
	upstream := benchUpstream()
	defer upstream.Close()

	env := emulator.DefaultEnv()
	table, err := emulator.NewTable(scaleway.New(env), outscale.New(env), exoscale.New(env))
	if err != nil {
		b.Fatalf("build the table: %v", err)
	}
	target, err := url.Parse(upstream.URL)
	if err != nil {
		b.Fatalf("parse: %v", err)
	}
	writer := proxy.NewWriter(io.Discard, 0)
	defer func() { _ = writer.Close() }()

	p, err := proxy.New(proxy.Options{Upstream: target, Writer: writer, Table: table})
	if err != nil {
		b.Fatalf("build the proxy: %v", err)
	}
	front := httptest.NewServer(p)
	defer front.Close()

	benchRoundTrip(b, front.URL)
}

// The same again with no route table, so the cost of naming the operation is
// separable from the cost of proxying and recording.
func BenchmarkProxyRoundTripWithoutNaming(b *testing.B) {
	upstream := benchUpstream()
	defer upstream.Close()

	target, err := url.Parse(upstream.URL)
	if err != nil {
		b.Fatalf("parse: %v", err)
	}
	writer := proxy.NewWriter(io.Discard, 0)
	defer func() { _ = writer.Close() }()

	p, err := proxy.New(proxy.Options{Upstream: target, Writer: writer})
	if err != nil {
		b.Fatalf("build the proxy: %v", err)
	}
	front := httptest.NewServer(p)
	defer front.Close()

	benchRoundTrip(b, front.URL)
}

func benchRoundTrip(b *testing.B, base string) {
	b.Helper()
	const path = "/instance/v1/zones/fr-par-1/servers"
	const payload = `{"name":"conformance-1","commercial_type":"DEV1-S","project":"11111111-1111-1111-1111-111111111111"}`
	client := &http.Client{}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		req, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(payload))
		if err != nil {
			b.Fatalf("build the request: %v", err)
		}
		req.Header.Set("X-Auth-Token", "11111111-1111-1111-1111-111111111111")
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			b.Fatalf("request: %v", err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
	}
}
