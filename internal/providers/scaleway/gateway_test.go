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
	// What it answers instead is this pack's own refusal, and that is worth
	// asserting rather than merely "not 200": declaring `/metadata` in
	// productPrefixes is what buys it. Before that line, a POST here fell
	// through to net/http and came back as plain-text "404 page not found" —
	// which the SDK drops, leaving a caller with a bare status and nothing to
	// branch on. That is #74's finding, and this route is inside it.
	//
	// Measured both ways on 2026-09-15: without the prefix, `404 page not
	// found`; with it, `501 {"type":"not_emulated", …}`.
	status, body := do(t, ts, "POST", "/metadata", "")
	if status == http.StatusOK {
		t.Error("POST /metadata answered 200; the gateway serves a read")
	}
	if body["type"] != "not_emulated" {
		t.Errorf("POST /metadata answered %v, not this pack's refusal. A path inside "+
			"a declared prefix must answer in Scaleway's dialect, or the SDK drops "+
			"the body and the caller gets a bare status", body)
	}
}
