package proxy

import (
	"net/url"
	"strings"

	"github.com/stephrobert/feint/internal/trace"
)

// Placeholder is what replaces a value the rules match.
//
// The name beside it always stays. A transcript exists to be read — by a human
// looking for the call a client made before the one that failed, by X-3 (#73)
// replaying it, by X-4 (#74) ranking what to serve next — and a record whose
// keys have been erased along with its values answers none of those questions.
// So: names in full, values gone.
const Placeholder = "REDACTED"

// carriers are the header and JSON key names whose value never reaches the
// writer.
//
// Three of them are named by the providers themselves rather than guessed:
// X-Auth-Token is Scaleway's (internal/providers/scaleway/pack.go), Authorization
// carries Outscale's SigV4 and Exoscale's EXO2-HMAC-SHA256
// (internal/providers/exoscale/pack.go). Each is matched by a substring below,
// and TestTheNamedCredentialCarriersAreRedacted checks all three by name so that
// a later edit to this list cannot quietly stop covering them.
//
// The rest is a name-pattern rule, because the exhaustive list does not exist:
// these are three clouds' worth of headers plus whatever a body carries, and a
// create sending "password" is the ordinary case rather than the exotic one.
//
// "passw" rather than "password" so passwd is covered too. Deliberately absent:
// anything matching a value rather than a name — a rule that looked for
// base64-shaped strings would redact half of a legitimate response and still
// miss a short token.
//
// The list is over-inclusive on purpose, and the asymmetry is the reason: a
// false positive costs one unreadable value in a transcript, a false negative
// costs a credential. KeypairName matches "key" and is redacted; that is the
// price, paid knowingly.
var carriers = []string{
	"auth",
	"token",
	"key",
	"secret",
	"signature",
	"cookie",
	// #72 names password as the case the body rule exists for and then leaves it
	// out of its own list of patterns. Added here rather than followed to the
	// letter: a rule that misses the example given for it is a rule with a typo.
	"passw",
	"credential",
}

// sensitive reports whether a header, query parameter or JSON key names
// something whose value must not be written down.
func sensitive(name string) bool {
	lower := strings.ToLower(name)
	for _, c := range carriers {
		if strings.Contains(lower, c) {
			return true
		}
	}
	return false
}

// redactExchange replaces every credential-bearing value in an exchange.
//
// This is the call [capture] makes, and the only one: delete it and
// TestATranscriptCarriesNoCredential fails, which is the condition for the
// redaction counting as a control rather than an intention. The type around it
// does the other half — see [Redacted] — by making sure no second path to the
// writer exists for this call to be forgotten on.
func redactExchange(x *trace.Exchange) {
	x.Query = redactQuery(x.Query)
	redactMessage(x.Req)
	redactMessage(x.Res)
}

func redactMessage(m *trace.Message) {
	if m == nil {
		return
	}
	for name := range m.Headers {
		if sensitive(name) {
			// The whole value, scheme included. Keeping "EXO2-HMAC-SHA256" and
			// dropping the rest would be more readable and would mean splitting a
			// credential and writing part of it down, which is how the interesting
			// half ends up on the wrong side of the split.
			m.Headers[name] = Placeholder
		}
	}
	m.Body = redactValue(m.Body)
}

// redactValue walks a decoded JSON document and replaces the values whose key
// names a credential.
//
// Recursion is bounded by encoding/json itself, which refuses a document nested
// more than 10000 deep, so a body crafted to exhaust the stack does not decode
// in the first place and never reaches here.
func redactValue(v any) any {
	switch value := v.(type) {
	case map[string]any:
		for k, nested := range value {
			if sensitive(k) {
				// Whatever the type. A key named "secret" holding an object is
				// still a secret, and replacing it wholesale beats descending into
				// it to redact the leaves it happens to have today.
				value[k] = Placeholder
				continue
			}
			value[k] = redactValue(nested)
		}
		return value
	case []any:
		for i, item := range value {
			value[i] = redactValue(item)
		}
		return value
	default:
		return v
	}
}

// redactQuery replaces the value of every query parameter whose name carries a
// credential, and leaves the string byte-identical when none does.
//
// Byte-identical matters: re-encoding through url.Values sorts the parameters
// and normalises the escaping, so a transcript would stop showing what the
// client actually sent — and a replay reissuing it would send something else.
// SigV4 puts its signature in the query when a request is presigned, which is
// why this is not just the headers.
func redactQuery(raw string) string {
	if raw == "" {
		return raw
	}
	var out strings.Builder
	changed := false
	for i, pair := range strings.Split(raw, "&") {
		if i > 0 {
			out.WriteByte('&')
		}
		name, _, hasValue := strings.Cut(pair, "=")
		// Decoded to be tested, never to be written back: a parameter named
		// "X%2DAuth" is the same name as "X-Auth" to the server reading it.
		decoded, err := url.QueryUnescape(name)
		if err != nil {
			decoded = name
		}
		if hasValue && sensitive(decoded) {
			out.WriteString(name)
			out.WriteByte('=')
			out.WriteString(Placeholder)
			changed = true
			continue
		}
		out.WriteString(pair)
	}
	if !changed {
		return raw
	}
	return out.String()
}
