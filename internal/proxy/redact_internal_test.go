package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/trace"
)

// The name-pattern rule, both halves.
//
// A redactor that answered true to everything would pass every leak test in this
// package and produce a transcript of nothing but REDACTED, which is why the
// spared column is as long as the caught one. The names in it are real: they are
// what these three APIs put on the wire beside their credentials.
func TestTheNamePatternRuleCoversTheCarriersAndSparesTheRest(t *testing.T) {
	caught := []string{
		// Named by the providers, not guessed.
		"X-Auth-Token",     // Scaleway
		"Authorization",    // Outscale SigV4, Exoscale EXO2-HMAC-SHA256
		"X-Amz-Signature",  // SigV4, presigned in the query
		"X-Amz-Credential", // SigV4
		"Proxy-Authorization",
		"Cookie",
		"Set-Cookie",
		// Body keys. Every one of these is a field one of the three APIs
		// actually carries.
		"password",
		"admin_password",
		"passwd",
		"secret_key",
		"access_key",
		"api_key",
		"SecretKey",
		"credentials",
		// Case is not a defence.
		"x-auth-token",
		"AUTHORIZATION",
	}
	spared := []string{
		"Content-Type", "Content-Length", "Accept-Encoding", "User-Agent", "Host",
		"Date", "Location", "X-Request-Id", "X-Total-Count",
		"name", "zone", "region", "project", "organization", "state",
		"commercial_type", "dynamic_ip_required", "image", "volumes",
		"VmId", "SubregionName", "ResponseContext",
	}

	for _, name := range caught {
		if !sensitive(name) {
			t.Errorf("%q is a credential carrier and the rule lets it through", name)
		}
	}
	for _, name := range spared {
		if sensitive(name) {
			t.Errorf("%q carries no credential and the rule redacts it, so the transcript loses a field it needs", name)
		}
	}
}

// A sensitive key hides whatever it holds, object or scalar.
//
// Descending into it to redact the leaves it happens to have today is the version
// that breaks the first time a provider nests one more level.
func TestASensitiveKeyHidesTheWholeSubtree(t *testing.T) {
	var document any
	if err := json.Unmarshal([]byte(`{
		"name": "conformance-1",
		"credentials": {"access": "AAA", "nested": {"deeper": "BBB"}},
		"items": [{"api_key": "CCC"}, {"zone": "fr-par-1"}]
	}`), &document); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	out, err := json.Marshal(redactValue(document))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, gone := range []string{"AAA", "BBB", "CCC"} {
		if bytes.Contains(out, []byte(gone)) {
			t.Errorf("%q survived: %s", gone, out)
		}
	}
	for _, kept := range []string{"conformance-1", "fr-par-1", "credentials", "api_key", "items"} {
		if !bytes.Contains(out, []byte(kept)) {
			t.Errorf("%q was lost: %s", kept, out)
		}
	}
}

// A query with nothing to redact comes back byte-identical.
//
// Re-encoding through url.Values would sort the parameters and normalise the
// escaping, so a transcript would stop showing what the client sent and a replay
// reissuing it would send something else. Nothing in this package tells the
// difference between "unchanged" and "re-encoded and happened to match", so the
// case that would differ is in the table.
func TestAQueryWithNothingToRedactIsUnchanged(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"empty", "", ""},
		{"ordering is preserved", "zone=fr-par-1&a=1", "zone=fr-par-1&a=1"},
		{"escaping is preserved", "name=a%2Bb&per_page=50", "name=a%2Bb&per_page=50"},
		{"a flag with no value", "dry_run&zone=fr-par-1", "dry_run&zone=fr-par-1"},
		{"a signature goes", "X-Amz-Signature=abc&zone=fr-par-1", "X-Amz-Signature=" + Placeholder + "&zone=fr-par-1"},
		{"an encoded name still matches", "X%2DAmz%2DSignature=abc", "X%2DAmz%2DSignature=" + Placeholder},
		{"only the value goes", "token=abc", "token=" + Placeholder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactQuery(tc.in); got != tc.want {
				t.Errorf("redactQuery(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The body decoder is fed whatever an upstream sends, which is untrusted input by
// the definition this repository uses: it crosses a process boundary and is
// written to a file another program will read.
//
// It asserts the two properties that matter for a recorder — it returns, and what
// it returns can be written down — rather than a value, because for most inputs
// there is no single right answer.
func FuzzABodyIsRecordableWhateverItHolds(f *testing.F) {
	f.Add([]byte(`{"name":"a"}`))
	f.Add([]byte(`[1,2,3]`))
	f.Add([]byte("plain text"))
	f.Add([]byte{0xff, 0xfe, 0x00})
	f.Add([]byte(`{"password":"x"}`))
	f.Add([]byte(""))

	f.Fuzz(func(t *testing.T, data []byte) {
		b := &body{max: DefaultMaxBody}
		if _, err := b.Write(data); err != nil {
			t.Fatalf("the capture refused bytes, which would fail a client's request: %v", err)
		}
		decoded := b.decoded(http.Header{"Content-Type": []string{"application/json"}})
		if _, err := json.Marshal(redactValue(decoded)); err != nil {
			t.Fatalf("a recorded body cannot be written to the transcript: %v", err)
		}
	})
}

// What recording one exchange costs, isolated from the round trip it rides on.
//
// The end-to-end benchmarks in bench_test.go measure the proxy against a direct
// call and therefore measure a second HTTP hop, which is inherent to being a
// proxy and swamps everything else. This measures the part this package chose:
// capturing the bodies, decoding them, and redacting.
func BenchmarkCaptureAndRedact(b *testing.B) {
	const answer = `{"servers":[{"id":"11111111-1111-1111-1111-111111111111","name":"conformance-1",` +
		`"state":"running","commercial_type":"DEV1-S","tags":["a","b"],"zone":"fr-par-1"}]}`
	const payload = `{"name":"conformance-1","commercial_type":"DEV1-S","password":"hunter2"}`

	req := httptest.NewRequest(http.MethodPost, "/instance/v1/zones/fr-par-1/servers?zone=fr-par-1", nil)
	req.Header.Set("X-Auth-Token", "11111111-1111-1111-1111-111111111111")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "scaleway-cli/2.30.0")
	resHeader := http.Header{
		"Content-Type":   []string{"application/json"},
		"X-Total-Count":  []string{"1"},
		"X-Request-Id":   []string{"33333333-3333-3333-3333-333333333333"},
		"Content-Length": []string{"512"},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		reqBody := &body{max: DefaultMaxBody}
		_, _ = reqBody.Write([]byte(payload))
		resBody := &body{max: DefaultMaxBody}
		_, _ = resBody.Write([]byte(answer))
		capture(seen{
			at:        time.Now().UTC(),
			elapsed:   time.Millisecond,
			req:       req,
			reqBody:   reqBody,
			status:    http.StatusOK,
			resHeader: resHeader,
			resBody:   resBody,
		})
	}
}

// A header nobody vouched for has its value dropped.
//
// The rule used to be a denylist of eight name substrings, and it was measured
// to fail on 2026-08-10: sent through this proxy, X-Auth-Token became REDACTED
// and X-Consumer carrying the same value was written in full. The three
// dialects served here pass the denylist only because their bearers happen to
// be called Authorization and X-Auth-Token — a coincidence, not a rule.
//
// That is the "bien formé n'est pas autorisé" family: matching a name answers
// "does this look like a credential", never "is this not one". A fourth
// provider arrives in exactly that shape — OVHcloud's own bearer is
// X-Ovh-Consumer, which matches none of the eight substrings.
func TestAnUnknownHeaderIsRedacted(t *testing.T) {
	const marker = "value-that-must-not-be-written"
	msg := &trace.Message{Headers: map[string]string{
		// Names no rule anticipated. Each was invented for this test, and that
		// is the point: the rule must not depend on having anticipated them.
		"X-Consumer":     marker,
		"X-Ovh-Consumer": marker,
		"X-Session":      marker,
		"Grumpf":         marker,
	}}
	redactMessage(msg)

	for name, value := range msg.Headers {
		if value != Placeholder {
			t.Errorf("%s was written as %q", name, value)
		}
	}

	// The names survive. A transcript exists to say which headers a client
	// sent; a record whose keys were erased along with its values answers none
	// of the questions it is read for.
	for _, name := range []string{"X-Consumer", "X-Ovh-Consumer", "X-Session", "Grumpf"} {
		if _, present := msg.Headers[name]; !present {
			t.Errorf("%s vanished; the name has to stay", name)
		}
	}
}

// The headers that carry no credential keep their value.
//
// Without this half the rule would be "redact everything", which passes every
// leak test and makes a transcript useless: Content-Type is how a reader knows
// what a body was, and User-Agent is how they know which client made the call.
func TestAVouchedHeaderKeepsItsValue(t *testing.T) {
	msg := &trace.Message{Headers: map[string]string{
		"Content-Type": "application/json",
		"User-Agent":   "scw-cli/2.56.3",
		"Accept":       "*/*",
		"Host":         "api.scaleway.com",
	}}
	redactMessage(msg)

	for name, want := range map[string]string{
		"Content-Type": "application/json",
		"User-Agent":   "scw-cli/2.56.3",
		"Accept":       "*/*",
		"Host":         "api.scaleway.com",
	} {
		if got := msg.Headers[name]; got != want {
			t.Errorf("%s was redacted to %q; a transcript needs it", name, got)
		}
	}
}

// A null under a credential-bearing key stays null.
//
// Redaction protects a value; there is nothing to protect in a null, the key's
// name is written in full either way, and replacing it *invents* a string where
// the cloud answered nothing. That invention is not cosmetic: it changed the
// recorded type, and `feint replay` read it back as a field the emulator failed
// to serve — nine of nine divergences on a real `terraform apply`, all of them
// `kms_key_id` (matched by "key") and `next_page_token` (matched by "token").
//
// The over-inclusive denylist stays over-inclusive. What this fixes is that it
// now costs an unreadable value rather than a false measurement.
func TestARedactedNullStaysNull(t *testing.T) {
	body := map[string]any{
		"kms_key_id":      nil,
		"next_page_token": nil,
		"secret_key":      "the-value-that-must-not-survive",
		"name":            "a-volume",
	}
	out, ok := redactValue(body).(map[string]any)
	if !ok {
		t.Fatalf("redactValue did not answer an object: %T", redactValue(body))
	}
	for _, key := range []string{"kms_key_id", "next_page_token"} {
		if out[key] != nil {
			t.Errorf("%s came back %#v, want nil: a null carries nothing to redact, and "+
				"writing over it invents a type the cloud never answered", key, out[key])
		}
		if _, present := out[key]; !present {
			t.Errorf("%s vanished from the body; the key always stays", key)
		}
	}
	if out["secret_key"] != Placeholder {
		t.Errorf("a non-null credential came back %#v, want %q: this is the half that must not move",
			out["secret_key"], Placeholder)
	}
	if out["name"] != "a-volume" {
		t.Errorf("an ordinary field was redacted: %#v", out["name"])
	}
}
