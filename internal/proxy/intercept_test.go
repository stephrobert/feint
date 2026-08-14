package proxy_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/proxy"
)

// The host a real Exoscale account hands the client in `GET /v2/zone`, and the
// one that walked the client away from the plain proxy in #92. It is not the
// proxy; that is the whole point.
const republishedHost = "api-ch-gva-2.exoscale.com"

// configuredHost is the name the operator points the client's endpoint at: the
// proxy, reached by a name the leaf covers, standing in for whatever the exo
// config file holds. The first call of the session lands here — recorded in both
// runs — before the cloud republishes republishedHost.
const configuredHost = "proxy.feint.test"

// TestInterceptionRecordsThePostHandoffExchanges is the #92 proof.
//
// A session is driven twice against the same recording proxy. It differs in one
// thing only: whether the name the cloud republishes in its first answer
// resolves back to the proxy (interception on) or away to the cloud
// (interception off) — the in-process stand-in for a container's own /etc/hosts.
// Off, the proxy records the one exchange before the handoff and the client
// takes everything after somewhere the transcript cannot see, which is the
// eight-of-ninety #92 measured. On, every exchange of the session lands on the
// proxy and the transcript is complete.
//
// Remove the interception (mint no cert, do not redirect) and the recorded count
// collapses back to one: that is the condition for interception counting as the
// fix rather than a claim.
func TestInterceptionRecordsThePostHandoffExchanges(t *testing.T) {
	// followUps is how many calls the client makes after it has read the
	// republished endpoint. The real session was about ninety; a dozen is enough
	// to tell "the one before the handoff" from "all of them" without pretending
	// to reproduce a real account's exact traffic.
	const followUps = 12

	// interceptor covers the configured proxy name and the republished name.
	// Minted once, shared by both runs so the only variable is the redirect.
	interceptor, err := proxy.MintInterceptor(configuredHost, republishedHost)
	if err != nil {
		t.Fatalf("mint the interceptor: %v", err)
	}

	// The cloud the proxy forwards to. Its zone answer republishes republishedHost,
	// exactly as a real account does; every other route is a stub the session can
	// walk. Plain HTTP: the proxy speaks TLS to the client and HTTP to its upstream,
	// so nothing here needs an upstream trust root.
	var upstreamHits int
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		if r.URL.Path == "/v2/zone" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"zones": []map[string]any{{
					"name":         "ch-gva-2",
					"api-endpoint": "https://" + republishedHost + "/v2",
				}},
			})
			return
		}
		_, _ = io.WriteString(w, "{}")
	}))
	defer cloud.Close()

	// awayHits stands in for the real cloud the client reaches when nothing
	// redirects the republished name: where the eighty-two lost exchanges of #92
	// actually went. The proxy never sees these. It serves a certificate valid for
	// the republished name (minted with the same tool), which the client trusts
	// the way it would trust the real cloud through the system store.
	awayInterceptor, err := proxy.MintInterceptor(republishedHost)
	if err != nil {
		t.Fatalf("mint the away cloud's certificate: %v", err)
	}
	var awayHits int
	away := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		awayHits++
		_, _ = io.WriteString(w, "{}")
	}))
	away.TLS = awayInterceptor.ServerTLSConfig()
	away.StartTLS()
	defer away.Close()

	run := func(t *testing.T, intercept bool) (recorded int, handedHosts map[string]int64) {
		t.Helper()
		upstreamHits, awayHits = 0, 0

		target, err := url.Parse(cloud.URL)
		if err != nil {
			t.Fatalf("parse the upstream: %v", err)
		}
		path := filepath.Join(t.TempDir(), "run.jsonl")
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			t.Fatalf("open the transcript: %v", err)
		}
		writer := proxy.NewWriter(file, 0)
		p, err := proxy.New(proxy.Options{Upstream: target, Writer: writer})
		if err != nil {
			t.Fatalf("build the proxy: %v", err)
		}

		// The proxy listens over TLS with the minted leaf. This is where the
		// client's configured endpoint points, and where interception sends the
		// republished name too.
		front := httptest.NewUnstartedServer(p)
		front.TLS = interceptor.ServerTLSConfig()
		front.StartTLS()
		proxyAddr := front.Listener.Addr().String()

		// The client trusts the interceptor CA (the SSL_CERT_FILE half) so it will
		// speak to the proxy, and — off interception — the away cloud's CA too,
		// which stands for the real cloud a client already trusts through the
		// system store.
		roots := interceptor.CAPool()
		if !intercept {
			roots = roots.Clone()
			if !roots.AppendCertsFromPEM(awayInterceptor.CAPEM()) {
				t.Fatal("the away cloud's CA does not parse")
			}
		}
		// The dialer is the /etc/hosts stand-in. The configured proxy name always
		// resolves to the proxy; the republished name resolves to the proxy on
		// interception, to the away cloud off it.
		redirect := map[string]string{configuredHost + ":443": proxyAddr}
		if intercept {
			redirect[republishedHost+":443"] = proxyAddr
		} else {
			redirect[republishedHost+":443"] = away.Listener.Addr().String()
		}
		client := &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if to, ok := redirect[addr]; ok {
					addr = to
				}
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
			TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		}}

		// The session: the client is configured to talk to the proxy (by a covered
		// name), reads the zone, then follows the republished api-endpoint for the
		// rest — the exact shape that lost the client in #92.
		endpoint := driveSession(t, client, "https://"+configuredHost, followUps)
		if endpoint != "https://"+republishedHost+"/v2" {
			t.Fatalf("the cloud did not republish its address: got %q", endpoint)
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
		_, hosts := p.HandedElsewhere()
		return len(read(t, raw)), hosts
	}

	t.Run("without interception the transcript stops at the handoff", func(t *testing.T) {
		recorded, hosts := run(t, false)
		if recorded != 1 {
			t.Errorf("without interception the proxy should record only the pre-handoff exchange, got %d", recorded)
		}
		if awayHits != followUps {
			t.Errorf("the client should have taken all %d follow-ups away from the proxy, the away cloud saw %d", followUps, awayHits)
		}
		if hosts[republishedHost] == 0 {
			t.Errorf("the proxy should have reported the handoff to %s, saw %v", republishedHost, hosts)
		}
	})

	t.Run("with interception the whole session is recorded", func(t *testing.T) {
		recorded, _ := run(t, true)
		if want := 1 + followUps; recorded != want {
			t.Errorf("with interception the proxy should record the whole session (%d), got %d", want, recorded)
		}
		if awayHits != 0 {
			t.Errorf("with interception nothing should reach the away cloud, it saw %d", awayHits)
		}
	})
}

// driveSession runs the client the way #92's session ran: one call to the
// configured endpoint, read the republished api-endpoint, then that many calls
// against it. It returns the republished endpoint so the caller can assert the
// cloud handed one out at all.
func driveSession(t *testing.T, client *http.Client, endpoint string, followUps int) string {
	t.Helper()

	res, err := client.Get(endpoint + "/v2/zone")
	if err != nil {
		t.Fatalf("GET /v2/zone: %v", err)
	}
	var body struct {
		Zones []struct {
			APIEndpoint string `json:"api-endpoint"`
		} `json:"zones"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode /v2/zone: %v", err)
	}
	_ = res.Body.Close()
	if len(body.Zones) == 0 {
		t.Fatal("no zone in the answer")
	}
	api := body.Zones[0].APIEndpoint

	for i := 0; i < followUps; i++ {
		res, err := client.Get(api + "/instance")
		if err != nil {
			t.Fatalf("follow-up %d to %s: %v", i, api, err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
	}
	return api
}

// TestMintInterceptorRefusesNoHost pins the guard in MintInterceptor: a
// certificate covering nothing redirects nothing, so minting one is refused.
// Remove the guard and this fails — the mint returns a usable-looking
// interceptor for an empty name set.
func TestMintInterceptorRefusesNoHost(t *testing.T) {
	if _, err := proxy.MintInterceptor(); err == nil {
		t.Error("minting an interceptor for no host should be refused")
	}
	if _, err := proxy.MintInterceptor("a.example", ""); err == nil {
		t.Error("minting an interceptor with an empty host name should be refused")
	}
}

// TestAMintedLeafIsShortLivedAndNotACA pins the safety scoping: the leaf a client
// is asked to trust cannot itself sign further certificates, and it expires
// within the day. Mark it IsCA, or stretch its validity past leafValidity, and
// this fails — which is the condition for the safety argument in docs/limits.md
// resting on a checked property rather than a comment.
func TestAMintedLeafIsShortLivedAndNotACA(t *testing.T) {
	interceptor, err := proxy.MintInterceptor(republishedHost)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	cfg := interceptor.ServerTLSConfig()
	if len(cfg.Certificates) == 0 {
		t.Fatal("the interceptor presents no certificate")
	}
	leaf, err := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatalf("parse the leaf: %v", err)
	}
	if leaf.IsCA {
		t.Error("the leaf a client trusts must not itself be a CA")
	}
	// leafValidity is a day; NotBefore is backdated a minute for clock skew, so
	// the span is a shade over a day. Anything approaching a year is the
	// regression this guards against.
	if d := leaf.NotAfter.Sub(leaf.NotBefore); d > 25*time.Hour {
		t.Errorf("the leaf lives too long (%s): a forgotten cert must not become a durable anchor", d)
	}
	if len(leaf.DNSNames) != 1 || !strings.EqualFold(leaf.DNSNames[0], republishedHost) {
		t.Errorf("the leaf should cover exactly the intercepted host, covers %v", leaf.DNSNames)
	}
}
