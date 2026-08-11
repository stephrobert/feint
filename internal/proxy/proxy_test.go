package proxy_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/exoscale"
	"github.com/stephrobert/feint/internal/providers/outscale"
	"github.com/stephrobert/feint/internal/providers/scaleway"
	"github.com/stephrobert/feint/internal/proxy"
	"github.com/stephrobert/feint/internal/trace"
)

// transcript starts a proxy in front of upstream, writing to a file in t's
// temporary directory, and returns the proxy's URL and a function that shuts
// everything down and reads the file back.
//
// The file is real rather than a bytes.Buffer, because "the bytes on disk" is
// what #72 asks about and because the writer's contract — one line, one write,
// nothing buffered by us — is only observable through something that can be read
// while it is being written.
func transcript(t *testing.T, upstream string, table *emulator.Table) (string, func() []byte) {
	t.Helper()

	target, err := url.Parse(upstream)
	if err != nil {
		t.Fatalf("parse the upstream URL: %v", err)
	}
	path := filepath.Join(t.TempDir(), "run.jsonl")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("open the transcript: %v", err)
	}
	writer := proxy.NewWriter(file, 0)

	p, err := proxy.New(proxy.Options{Upstream: target, Writer: writer, Table: table})
	if err != nil {
		t.Fatalf("build the proxy: %v", err)
	}
	front := httptest.NewServer(p)

	return front.URL, func() []byte {
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
		return raw
	}
}

// read parses a transcript into the shape both writers of this repository use.
//
// Decoding into trace.Exchange rather than into a map is deliberate: it is the
// same type the emulator's ring publishes, so a field this package started
// writing under another name would fail here rather than in whatever reads a
// transcript in six months.
func read(t *testing.T, raw []byte) []trace.Exchange {
	t.Helper()
	var out []trace.Exchange
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var x trace.Exchange
		if err := json.Unmarshal(line, &x); err != nil {
			t.Fatalf("a transcript line does not decode as a trace.Exchange: %v\n%s", err, line)
		}
		out = append(out, x)
	}
	return out
}

// The leak test #72 names first, and the one /falsify is pointed at.
//
// Six markers, one per carrier the proxy can see: two request headers named by
// the providers themselves, a query parameter (SigV4 presigns there), a body key
// nested one level down, a response header and a response body key. The upstream
// asserts it received every one of them unchanged, because a redactor that broke
// the request would pass this test while breaking the product.
//
// Delete the redactExchange call in capture and this fails — that is the
// condition for the redaction counting as a control rather than an intention.
//
// The limit it does not test, stated so nobody reads more into it than it says:
// redaction is by name. A credential in the value of a field named "message"
// survives, and nothing keyed on names can find it.
func TestATranscriptCarriesNoCredential(t *testing.T) {
	const (
		tokenMarker     = "MARKER-scaleway-x-auth-token"
		authMarker      = "MARKER-exoscale-authorization"
		queryMarker     = "MARKER-sigv4-query-signature"
		bodyMarker      = "MARKER-request-body-password"
		cookieMarker    = "MARKER-response-set-cookie"
		responseMarker  = "MARKER-response-body-secret-key"
		harmlessRequest = "conformance-1"
		harmlessAnswer  = "fr-par-1"
	)

	var received *http.Request
	var receivedBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Clone(r.Context())
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Set-Cookie", "session="+cookieMarker)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"secret_key":"`+responseMarker+`","zone":"`+harmlessAnswer+`"}`)
	}))
	defer upstream.Close()

	front, finish := transcript(t, upstream.URL, nil)

	body := `{"name":"` + harmlessRequest + `","user":{"password":"` + bodyMarker + `"}}`
	req, err := http.NewRequest(http.MethodPost,
		front+"/instance/v1/zones/fr-par-1/servers?X-Amz-Signature="+queryMarker+"&zone=fr-par-1",
		strings.NewReader(body))
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	req.Header.Set("X-Auth-Token", tokenMarker)
	req.Header.Set("Authorization", "EXO2-HMAC-SHA256 credential="+authMarker)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("the proxy did not answer: %v", err)
	}
	answer, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()

	raw := finish()

	// The accepting half, first: a guard that refuses everything passes every
	// leak test and breaks the product. The cloud must have received exactly what
	// the client sent, and the client exactly what the cloud answered.
	if received == nil {
		t.Fatal("the upstream was never reached, so nothing was measured")
	}
	if got := received.Header.Get("X-Auth-Token"); got != tokenMarker {
		t.Errorf("the upstream received X-Auth-Token %q, the client sent %q", got, tokenMarker)
	}
	if got := received.Header.Get("Authorization"); !strings.Contains(got, authMarker) {
		t.Errorf("the upstream received Authorization %q, which does not carry what the client sent", got)
	}
	if got := received.URL.Query().Get("X-Amz-Signature"); got != queryMarker {
		t.Errorf("the upstream received the signature %q, the client sent %q", got, queryMarker)
	}
	if !bytes.Contains(receivedBody, []byte(bodyMarker)) {
		t.Errorf("the upstream received a body without the password the client sent: %s", receivedBody)
	}
	if !bytes.Contains(answer, []byte(responseMarker)) {
		t.Errorf("the client received an answer without what the upstream sent: %s", answer)
	}

	// The refusing half.
	for _, marker := range []string{
		tokenMarker, authMarker, queryMarker, bodyMarker, cookieMarker, responseMarker,
	} {
		if bytes.Contains(raw, []byte(marker)) {
			t.Errorf("the transcript on disk carries %q", marker)
		}
	}

	// And the half that makes the transcript worth keeping: names stay, and so
	// does everything that is not a credential. A redactor that emptied the
	// record would pass the six assertions above.
	for _, keep := range []string{
		"X-Auth-Token", "Authorization", "X-Amz-Signature", "password", "secret_key", "Set-Cookie",
		harmlessRequest, harmlessAnswer, "instance/v1", proxy.Placeholder,
	} {
		if !bytes.Contains(raw, []byte(keep)) {
			t.Errorf("the transcript lost %q, so it is no longer readable", keep)
		}
	}
}

// The three carriers #72 names, by name, so that editing the pattern list cannot
// quietly stop covering them.
//
// The patterns are a substring rule and these three are covered by it today. A
// later edit narrowing "auth" to "authorization" would still pass every generic
// test and stop redacting X-Auth-Token, which is Scaleway's only credential.
func TestTheNamedCredentialCarriersAreRedacted(t *testing.T) {
	const marker = "MARKER-credential"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	// Named at their source rather than retyped: scaleway/pack.go documents
	// X-Auth-Token, exoscale/pack.go documents EXO2-HMAC-SHA256 in Authorization,
	// and outscale/pack.go documents SigV4 in the same header.
	for _, header := range []string{"X-Auth-Token", "Authorization"} {
		t.Run(header, func(t *testing.T) {
			front, finish := transcript(t, upstream.URL, nil)
			req, err := http.NewRequest(http.MethodGet, front+"/anything", nil)
			if err != nil {
				t.Fatalf("build the request: %v", err)
			}
			req.Header.Set(header, marker)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("the proxy did not answer: %v", err)
			}
			_ = res.Body.Close()

			raw := finish()
			if bytes.Contains(raw, []byte(marker)) {
				t.Errorf("%s reached the transcript", header)
			}
			if !bytes.Contains(raw, []byte(header)) {
				t.Errorf("%s was removed from the transcript, name included", header)
			}
		})
	}
}

// The second test #72 asks for: two independent observers of one run must agree
// on which operations it walked.
//
// One is the proxy, watching from outside with nothing but the route table and
// the request line. The other is the emulator's own counter, which knows because
// it dispatched them. A divergence means one of them is lying, and either answer
// being wrong is worth knowing: the proxy's naming is what X-4 (#74) will rank a
// backlog from, and /_feint/conformance is what the score is computed from.
//
// The run is driven by an ordinary http.Client rather than by scw, and that is a
// deliberate difference from the issue's wording. A Go test that shells out to a
// client which may not be installed either skips — measuring nothing, which
// CLAUDE.md forbids — or fails on a workstation for a reason that says nothing
// about this code. The same comparison over the real CLI is
// tools/conformance/proxy.sh, run by `mise run conformance:proxy`, which is
// outside the CI path because recording is a human's job.
func TestTwoObserversOfOneRunAgree(t *testing.T) {
	env := emulator.DefaultEnv()
	packs := []emulator.Pack{scaleway.New(env), outscale.New(env), exoscale.New(env)}
	srv, err := emulator.NewServer(env, packs...)
	if err != nil {
		t.Fatalf("mount the packs: %v", err)
	}
	emu := httptest.NewServer(srv.Handler())
	defer emu.Close()

	table, err := emulator.NewTable(packs...)
	if err != nil {
		t.Fatalf("build the table: %v", err)
	}
	front, finish := transcript(t, emu.URL, table)

	// A walk across all three dialects, so the agreement is not an artefact of
	// one pack's URL shape.
	const zone = "/instance/v1/zones/fr-par-1"
	walk := []struct {
		method, path, body string
	}{
		{http.MethodGet, zone + "/servers", ""},
		{http.MethodGet, zone + "/ips", ""},
		{http.MethodPost, zone + "/ips", `{"project":"11111111-1111-1111-1111-111111111111"}`},
		{http.MethodGet, zone + "/security_groups", ""},
		{http.MethodGet, "/v2/zone", ""},
		{http.MethodGet, "/v2/instance-type", ""},
		{http.MethodPost, "/api/v1/ReadVms", `{}`},
		{http.MethodPost, "/api/v1/ReadRegions", `{}`},
		// Repeated on purpose: the emulator counts calls and the proxy records
		// exchanges, so the two agree on the set of operations and not on the
		// number of lines. Comparing sets is what the issue asks for and this is
		// the case that tells them apart.
		{http.MethodGet, zone + "/servers", ""},
		// A route no pack claims. It must appear in the transcript with no
		// operation — that is the finding #72 is built to produce — and it must
		// not appear on either side of the comparison.
		{http.MethodGet, "/no/such/api", ""},
	}
	for _, call := range walk {
		var payload io.Reader
		if call.body != "" {
			payload = strings.NewReader(call.body)
		}
		req, err := http.NewRequest(call.method, front+call.path, payload)
		if err != nil {
			t.Fatalf("build %s %s: %v", call.method, call.path, err)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", call.method, call.path, err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
	}

	raw := finish()

	// The emulator is asked directly, never through the proxy: a poll of
	// /_feint/conformance travelling through the recorder would add an exchange
	// to one observer that the other cannot count.
	var view emulator.ConformanceView
	res, err := http.Get(emu.URL + "/_feint/conformance")
	if err != nil {
		t.Fatalf("read /_feint/conformance: %v", err)
	}
	if err := json.NewDecoder(res.Body).Decode(&view); err != nil {
		t.Fatalf("decode /_feint/conformance: %v", err)
	}
	_ = res.Body.Close()

	fromProxy := map[string]bool{}
	unnamed := 0
	for _, x := range read(t, raw) {
		if x.Operation == "" {
			unnamed++
			if x.Mounted {
				t.Errorf("%s %s: no operation but mounted, which no reader can interpret", x.Method, x.Path)
			}
			continue
		}
		fromProxy[x.Operation] = true
	}
	fromEmulator := map[string]bool{}
	for op := range view.Calls {
		fromEmulator[op] = true
	}

	if len(fromProxy) == 0 {
		t.Fatal("the proxy named no operation, so this test compares two empty sets")
	}
	if diff := disagree(fromProxy, fromEmulator); diff != "" {
		t.Errorf("the two observers of one run disagree:\n%s", diff)
	}
	if unnamed != 1 {
		t.Errorf("expected exactly one exchange on a route no pack claims, got %d", unnamed)
	}
}

// disagree names what one observer saw and the other did not.
func disagree(fromProxy, fromEmulator map[string]bool) string {
	var only []string
	for op := range fromProxy {
		if !fromEmulator[op] {
			only = append(only, "  only the proxy saw "+op)
		}
	}
	for op := range fromEmulator {
		if !fromProxy[op] {
			only = append(only, "  only the emulator saw "+op)
		}
	}
	sort.Strings(only)
	return strings.Join(only, "\n")
}

// A transport failure is recorded as what the client received, and the reason
// stays out of the body.
//
// Both halves matter. The transcript must show the 502, or a run that died
// halfway looks like a run that stopped making calls; and the 502's body must not
// carry the transport error, because that text holds the request URL and a query
// string can carry a signature. A body is redacted by JSON key name, so a
// credential in a plain-text error message would go through untouched.
func TestAFailedUpstreamLeaksNothingIntoTheTranscript(t *testing.T) {
	const marker = "MARKER-presigned-signature"

	// An address nothing listens on: the connection is refused, which is the
	// ordinary failure when a cloud is unreachable or a name does not resolve.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	front, finish := transcript(t, deadURL, nil)
	res, err := http.Get(front + "/v2/instance?X-Amz-Signature=" + marker)
	if err != nil {
		t.Fatalf("the proxy did not answer: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()

	if res.StatusCode != http.StatusBadGateway {
		t.Errorf("an unreachable upstream answered %d, expected 502", res.StatusCode)
	}
	if bytes.Contains(body, []byte(marker)) {
		t.Errorf("the 502 body carries the query the request was signed with: %s", body)
	}

	raw := finish()
	exchanges := read(t, raw)
	if len(exchanges) != 1 {
		t.Fatalf("expected the failed exchange to be recorded once, got %d lines", len(exchanges))
	}
	if exchanges[0].Status != http.StatusBadGateway {
		t.Errorf("the transcript records status %d for a failed upstream", exchanges[0].Status)
	}
	if bytes.Contains(raw, []byte(marker)) {
		t.Errorf("the transcript carries the signature the request was presigned with")
	}
}

// A body over the cap is reported, never cut and passed off as whole.
//
// The forwarding half is asserted with it: the cap is a bound on what is
// recorded, and a proxy that also truncated what it forwards would corrupt every
// large request a client makes.
func TestABodyOverTheCapIsDeclaredRatherThanTruncated(t *testing.T) {
	const limit = 512
	large := strings.Repeat("x", limit*4)

	var receivedLen int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedLen = len(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"echo":"`+large+`"}`)
	}))
	defer upstream.Close()

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse the upstream URL: %v", err)
	}
	var file bytes.Buffer
	writer := proxy.NewWriter(&file, 0)
	p, err := proxy.New(proxy.Options{Upstream: target, Writer: writer, MaxBody: limit})
	if err != nil {
		t.Fatalf("build the proxy: %v", err)
	}
	front := httptest.NewServer(p)

	res, err := http.Post(front.URL+"/big", "application/json", strings.NewReader(`{"data":"`+large+`"}`))
	if err != nil {
		t.Fatalf("the proxy did not answer: %v", err)
	}
	answer, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	front.Close()
	if err := writer.Close(); err != nil {
		t.Fatalf("close the writer: %v", err)
	}

	if receivedLen <= limit {
		t.Errorf("the upstream received %d bytes of a %d-byte body: the cap truncated the wire",
			receivedLen, len(large))
	}
	if len(answer) <= limit {
		t.Errorf("the client received %d bytes of a %d-byte answer: the cap truncated the wire",
			len(answer), len(large))
	}

	exchanges := read(t, file.Bytes())
	if len(exchanges) != 1 {
		t.Fatalf("expected one exchange, got %d", len(exchanges))
	}
	for side, message := range map[string]*trace.Message{
		"request": exchanges[0].Req, "response": exchanges[0].Res,
	} {
		omission, ok := message.Body.(map[string]any)
		if !ok {
			t.Fatalf("the %s body was recorded as %T, expected an omission marker", side, message.Body)
		}
		if _, said := omission["feint_omitted"]; !said {
			t.Errorf("the %s body was cut without saying so: %v", side, omission)
		}
		if omission["bytes"] == nil {
			t.Errorf("the %s omission does not say how much was dropped: %v", side, omission)
		}
	}
	if strings.Contains(file.String(), strings.Repeat("x", limit)) {
		t.Error("the transcript holds a body it declared omitted")
	}
}

// A gzip answer is readable in the transcript and gzipped on the wire.
//
// scw, terraform and every SDK send Accept-Encoding: gzip, so without this the
// response body of a real run is an unreadable blob — and rewriting the request
// to stop the compression would mean the proxy no longer measures the exchange
// it is there to record.
func TestAGzipAnswerIsDecodedForTheRecordAndNotOnTheWire(t *testing.T) {
	const secret = "value-only-in-the-compressed-body"
	compressed := gzipped(t, `{"marker":"`+secret+`"}`)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(compressed)
	}))
	defer upstream.Close()

	front, finish := transcript(t, upstream.URL, nil)
	req, err := http.NewRequest(http.MethodGet, front+"/v2/zone", nil)
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	// Set explicitly, so net/http's transparent decompression stays out of it:
	// that is exactly what a real client does.
	req.Header.Set("Accept-Encoding", "gzip")
	res, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatalf("the proxy did not answer: %v", err)
	}
	onTheWire, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()

	if !bytes.Equal(onTheWire, compressed) {
		t.Error("the client received something other than the bytes the upstream sent")
	}
	raw := finish()
	if !bytes.Contains(raw, []byte(secret)) {
		t.Errorf("the transcript did not decode the gzip body:\n%s", raw)
	}
}

func gzipped(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := io.WriteString(zw, s); err != nil {
		t.Fatalf("compress: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("compress: %v", err)
	}
	return buf.Bytes()
}

// New refuses what cannot be a transcript.
func TestNewRefusesAnUnusableConfiguration(t *testing.T) {
	writer := proxy.NewWriter(io.Discard, 0)
	defer func() { _ = writer.Close() }()
	good, err := url.Parse("https://api.scaleway.com")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	for name, o := range map[string]proxy.Options{
		"no upstream":  {Writer: writer},
		"no writer":    {Upstream: good},
		"no host":      {Upstream: &url.URL{Scheme: "https"}, Writer: writer},
		"wrong scheme": {Upstream: &url.URL{Scheme: "ftp", Host: "example.com"}, Writer: writer},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := proxy.New(o); err == nil {
				t.Error("accepted a configuration that cannot record anything")
			}
		})
	}

	// The accepting half.
	if _, err := proxy.New(proxy.Options{Upstream: good, Writer: writer}); err != nil {
		t.Errorf("refused a usable configuration: %v", err)
	}
}

// The probe header is not this package's business, but its absence is: a
// transcript of a real client must not be mistaken for synthetic traffic. This
// pins the one header the emulator treats specially, so that a proxy which
// started adding headers of its own would be caught here.
func TestTheProxyAddsNoHeaderOfItsOwn(t *testing.T) {
	var got http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	front, finish := transcript(t, upstream.URL, nil)
	res, err := http.Get(front + "/v2/zone")
	if err != nil {
		t.Fatalf("the proxy did not answer: %v", err)
	}
	_ = res.Body.Close()
	finish()

	for _, unwanted := range []string{
		emulator.ProbeHeader,
		"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto",
	} {
		if v := got.Get(unwanted); v != "" {
			t.Errorf("the proxy added %s: %q, so the cloud does not see what the client sent", unwanted, v)
		}
	}
}

// A transcript is written as it goes, not at the end.
//
// JSON Lines is chosen for exactly this — "a crash keeps what it already wrote"
// — and a writer that buffered would keep the same format while losing the
// property. The check is that the line is on disk before anything is closed.
func TestTheTranscriptIsOnDiskBeforeTheRunEnds(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	path := filepath.Join(t.TempDir(), "run.jsonl")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = file.Close() }()
	writer := proxy.NewWriter(file, 0)
	defer func() { _ = writer.Close() }()

	p, err := proxy.New(proxy.Options{Upstream: target, Writer: writer})
	if err != nil {
		t.Fatalf("build the proxy: %v", err)
	}
	front := httptest.NewServer(p)
	defer front.Close()

	res, err := http.Get(front.URL + "/v2/zone")
	if err != nil {
		t.Fatalf("the proxy did not answer: %v", err)
	}
	_ = res.Body.Close()

	// The write is asynchronous by design, so this waits for it rather than
	// assuming it has happened; what it must never need is a Close.
	//
	// Two facts are waited for, not one: the line on disk, and the writer
	// counting it. They never become true atomically — run() writes, then locks
	// and increments — so a poll landing between the syscall and the count sees
	// the line with written still 0. Asserted instantaneously, that window
	// failed this test about one run in five on a loaded station ("the file
	// holds a line the writer does not count: 0"); a 20 ms pause inserted at
	// that exact point, standing in for a scheduler preemption, reproduced it
	// on every run. Waiting for both keeps what the test pins — nothing here
	// ever calls Close before returning — without asserting an ordering the
	// writer does not promise.
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read the transcript: %v", err)
		}
		onDisk := bytes.HasSuffix(raw, []byte("\n"))
		written, _ := writer.Stats()
		if onDisk && written == 1 {
			return
		}
		if time.Now().After(deadline) {
			if onDisk {
				t.Fatalf("the file holds a line the writer never counted: %d", written)
			}
			t.Fatal("nothing reached the transcript while the proxy was still running")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
