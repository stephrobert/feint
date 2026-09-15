package scaleway_test

import (
	"net/http"
	"testing"
)

// The gateway answers the metadata the SDK asks for, with the values a real
// account answered.
//
// The SDK that Terraform provider 2.83.0 embeds calls this after every read of a
// product carrying an SRN, and builds a `srn://<service>.<domain>/…` from the
// domain. Measured 2026-09-14: one apply plus destroy sent 148 of these and this
// emulator answered 404 to every one (#776). Nothing failed, because the SDK
// discards the error — which is luck, not a decision.
//
// The three values are the recording's, not a guess:
//
//	GET https://api.scaleway.com/metadata -> 200
//	{"platform": "external", "partition": "scw", "domain": "scw.eu"}
//
// corpus/scaleway/scw-gateway.jsonl carries the exchange, and `corpus:check`
// replays it on every pull request.
func TestTheGatewayAnswersTheMetadataTheSdkAsksFor(t *testing.T) {
	ts := newTestServer(t)

	status, body := do(t, ts, "GET", "/metadata", "")
	if status != http.StatusOK {
		t.Fatalf("GET /metadata: expected 200, got %d (%v)", status, body)
	}

	// Written out rather than read from the handler: comparing the answer
	// against the map it is built from would pass whatever both became. These
	// are the strings the real gateway sent.
	for field, want := range map[string]string{
		"platform":  "external",
		"partition": "scw",
		"domain":    "scw.eu",
	} {
		if got, _ := body[field].(string); got != want {
			t.Errorf("%s = %q, the recorded gateway answers %q", field, got, want)
		}
	}
	if len(body) != 3 {
		t.Errorf("the answer carries %d field(s): %v. The recording carries three, "+
			"and a fourth would be one this emulator invented", len(body), body)
	}
}

// The route is not zoned, not projected, and not authenticated differently from
// anything else.
//
// The real gateway answers it to any caller that reaches it — that is what the
// recording shows — so inventing a refusal here would be a behaviour nobody
// measured. This holds the shape of the route rather than its body: a second
// call answers the same thing, and no path variable makes it vary.
func TestTheGatewayMetadataDoesNotVary(t *testing.T) {
	ts := newTestServer(t)

	_, first := do(t, ts, "GET", "/metadata", "")
	_, second := do(t, ts, "GET", "/metadata", "")
	for field := range first {
		if first[field] != second[field] {
			t.Errorf("%s changed between two reads: %v then %v",
				field, first[field], second[field])
		}
	}

	// And it is a GET: the SDK issues no other verb at this path, so anything
	// else must not be served here by accident.
	//
	// Read with a bare client rather than the shared helper, which decodes JSON
	// and fails on this answer — because an unmounted path answers net/http's
	// plain-text "404 page not found". The real gateway answers a Scaleway error
	// document there (`{"type":"404","message":…}`, measured 2026-09-14). That
	// divergence is real and belongs to every unmounted path rather than to this
	// route, so it is named in #776 and not fixed here; this assertion is about
	// the verb, and it is written not to depend on a body it is not judging.
	res, err := ts.Client().Post(ts.URL+"/metadata", "application/json", nil) //nolint:noctx // test client
	if err != nil {
		t.Fatalf("POST /metadata: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusOK {
		t.Error("POST /metadata answered 200; the gateway serves a read")
	}
}
