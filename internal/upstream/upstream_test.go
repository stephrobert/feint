package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The credentials in these tests are invented and go nowhere: every call is
// served by an httptest server in the same process. Nothing here reaches a real
// cloud, which is what lets them run in CI where no account exists.
func fake() credentials {
	return credentials{key: "EXOakey", secret: "s3cret", region: "eu-west-2"}
}

// A call that is not a read is refused, in each provider's own dialect.
//
// The guard sits where the request is built rather than in each caller, so a
// caller written later cannot forget it. Creating on a real account is
// deliberate work, not a capability this package hands out.
func TestOnlyReadsAreMade(t *testing.T) {
	refused := map[Provider][]string{
		Outscale: {"CreateVms", "DeleteNet", "LinkVolume", "UpdateVm"},
		Scaleway: {"CreateServer", "instance/v1/servers"}, // no leading slash: not a path
		Exoscale: {"POST /v2/instance", "create-instance"},
	}
	for provider, calls := range refused {
		c := &Client{Provider: provider}
		for _, call := range calls {
			if c.isRead(call) {
				t.Errorf("%s: %q was taken for a read", provider, call)
			}
		}
	}

	// And the accepting half, without which a guard that refused everything
	// would pass the test above and make the package useless.
	accepted := map[Provider][]string{
		Outscale: {"ReadVms", "ReadNets"},
		Scaleway: {"/instance/v1/zones/fr-par-1/servers"},
		Exoscale: {"/v2/instance"},
	}
	for provider, calls := range accepted {
		c := &Client{Provider: provider}
		for _, call := range calls {
			if !c.isRead(call) {
				t.Errorf("%s: %q was refused and is a read", provider, call)
			}
		}
	}
}

// Read reports the refusal rather than performing it, so a caller cannot bypass
// the guard by ignoring an error it did not expect.
func TestReadRefusesAWriteWithoutCallingAnything(t *testing.T) {
	var reached atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := &Client{Provider: Outscale, Endpoint: srv.URL, Pace: -1,
		sign: func(*http.Request, []byte) error { return nil }, http: srv.Client()}
	if _, err := c.Read(context.Background(), "CreateVms"); err == nil {
		t.Fatal("a create was accepted")
	}
	if reached.Load() {
		t.Error("the refused call still reached the server")
	}
}

// A throttled answer is retried; a real refusal is not.
//
// The distinction matters because Outscale reports both with 400 and code 4120
// — the same code for a genuinely bad parameter — so the retry is bounded and a
// non-throttle answer must come straight back. Without the bound, a real
// parameter error would be tried four times and reported unchanged, four times
// slower.
func TestAThrottleIsRetriedAndAVerdictIsNot(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n < 3 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Errors": []any{map[string]any{"Code": "4120", "Type": "InvalidParameterValue"}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Vms": []any{}})
	}))
	defer srv.Close()

	c := &Client{Provider: Outscale, Endpoint: srv.URL, Pace: time.Millisecond, Retries: 3,
		sign: func(*http.Request, []byte) error { return nil }, http: srv.Client()}

	ex, err := c.Read(context.Background(), "ReadVms")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if ex.Status != http.StatusOK {
		t.Errorf("gave up on a throttle: status %d after %d call(s)", ex.Status, calls.Load())
	}
	if calls.Load() != 3 {
		t.Errorf("made %d call(s), want 3", calls.Load())
	}

	// A 404 is a verdict: one call, returned as-is.
	calls.Store(0)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer other.Close()
	c.Endpoint = other.URL
	c.http = other.Client()
	if ex, err = c.Read(context.Background(), "ReadNets"); err != nil {
		t.Fatalf("read: %v", err)
	}
	if ex.Status != http.StatusNotFound || calls.Load() != 1 {
		t.Errorf("a verdict was retried: status %d after %d call(s)", ex.Status, calls.Load())
	}
}

// No request header ever reaches the recorded exchange.
//
// Stronger than redacting one: the request is not captured at all, so a
// transcript written from this package cannot carry an Authorization line even
// if somebody later forgets what redaction was for.
func TestTheRecordCarriesNoRequestHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"Vms": []any{}})
	}))
	defer srv.Close()

	c := &Client{Provider: Outscale, Endpoint: srv.URL, Pace: -1, http: srv.Client(),
		sign: signOutscale(fake())}
	ex, err := c.Read(context.Background(), "ReadVms")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if ex.Req != nil {
		t.Errorf("the request was recorded: %+v", ex.Req)
	}
	if ex.Res == nil {
		t.Error("the response was not recorded, so this test measures nothing")
	}
}

// The Outscale signature covers the Host header.
//
// That is why a client pointed at a proxy cannot reach the real cloud through
// it, and it is a property worth pinning: if a later change dropped the host
// from the canonical request, signatures would still be produced, calls would
// still be made, and only the real cloud would notice — from a station that
// cannot run this test.
func TestOutscaleSignatureCoversTheHost(t *testing.T) {
	sign := signOutscale(fake())
	sig := func(host string) string {
		req, err := http.NewRequest(http.MethodPost, "https://"+host+"/api/v1/ReadVms", strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if err := sign(req, []byte("{}")); err != nil {
			t.Fatalf("sign: %v", err)
		}
		return req.Header.Get("Authorization")
	}
	if sig("api.eu-west-2.outscale.com") == sig("127.0.0.1:4700") {
		t.Error("two different hosts produced the same signature: the host is not signed")
	}
	if !strings.Contains(sig("api.eu-west-2.outscale.com"), "SignedHeaders=content-type;host;x-osc-date") {
		t.Error("host is missing from SignedHeaders")
	}
}

// The Exoscale signature has the order egoscale's own signRequest uses.
//
// Written from v2/api/security.go of the SDK the `exo` CLI is built on. The
// parts are joined by newlines: "METHOD escaped-path", body, the concatenated
// values of single-valued query parameters sorted by name, an empty line for
// signed headers, and the expiry. A different order signs cleanly and is
// refused by the cloud, which is a failure no local test would otherwise catch.
func TestExoscaleSignatureMatchesTheSDKsOrder(t *testing.T) {
	sign := signExoscale(fake())
	req, err := http.NewRequest(http.MethodGet, "https://api-ch-gva-2.exoscale.com/v2/instance?zone=ch-gva-2&b=1", nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := sign(req, nil); err != nil {
		t.Fatalf("sign: %v", err)
	}
	auth := req.Header.Get("Authorization")

	for _, want := range []string{
		"EXO2-HMAC-SHA256 credential=EXOakey",
		"signed-query-args=b;zone", // sorted by name, not by appearance
		"expires=",
		"signature=",
	} {
		if !strings.Contains(auth, want) {
			t.Errorf("Authorization lacks %q: %s", want, auth)
		}
	}

	// A request with no query carries no signed-query-args pragma at all —
	// the SDK omits it rather than sending an empty one.
	plain, err := http.NewRequest(http.MethodGet, "https://api-ch-gva-2.exoscale.com/v2/zone", nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := sign(plain, nil); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if strings.Contains(plain.Header.Get("Authorization"), "signed-query-args") {
		t.Error("an empty signed-query-args was sent")
	}
}

// Scaleway signs nothing: the secret is the credential. Pinned because it is
// surprising next to the other two, and because a future reader may "fix" it.
func TestScalewaySendsTheTokenAndSignsNothing(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://api.scaleway.com/instance/v1/zones/fr-par-1/servers", nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := signScaleway(fake())(req, nil); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if req.Header.Get("X-Auth-Token") != "s3cret" {
		t.Errorf("token header is %q", req.Header.Get("X-Auth-Token"))
	}
	if req.Header.Get("Authorization") != "" {
		t.Error("an Authorization header was added where the provider expects none")
	}
}

// Credentials are read whichever quoting the operator's file uses.
//
// The exoscale CLI writes single quotes. Reading only double-quoted values made
// this package report "no secret" against a file that has one — the most
// misleading error it could produce, because it accuses the configuration of
// being incomplete when it is the reader that is narrow. Measured on a real
// station, where every account in exoscale.toml uses single quotes.
func TestTomlValueReadsEveryQuotingTheFileMayUse(t *testing.T) {
	for _, form := range []struct{ name, text string }{
		{"double quotes", "secret = \"s3cret\"\n"},
		{"single quotes", "secret = 's3cret'\n"},
		{"unquoted", "secret = s3cret\n"},
		{"indented", "  secret   =   's3cret'  \n"},
	} {
		if got := tomlValue(form.text, "secret"); got != "s3cret" {
			t.Errorf("%s: read %q", form.name, got)
		}
	}
	// And a key that is genuinely absent still reads as absent, or the caller
	// would take an empty credential for a valid one.
	if got := tomlValue("key = 'abc'\n", "secret"); got != "" {
		t.Errorf("a missing key read as %q", got)
	}
}

// The Outscale call identity — key prefix, method, path — is one exported
// function with two consumers: the recorder here and the shapes gate in
// internal/cli. The values are pinned because artefacts on disk depend on
// them: shapes/outscale.json keys its operations "osc/Client.<Action>", and a
// silent change would orphan every recorded catalogue while both consumers
// kept agreeing with each other.
func TestTheOutscaleCallIdentityIsPinned(t *testing.T) {
	key, method, path := OutscaleCall("ReadVms")
	if key != "osc/Client.ReadVms" {
		t.Errorf("key = %q, want osc/Client.ReadVms", key)
	}
	if method != http.MethodPost {
		t.Errorf("method = %q, want POST", method)
	}
	if path != "/api/v1/ReadVms" {
		t.Errorf("path = %q, want /api/v1/ReadVms", path)
	}
}
