package proxy

import (
	"net/url"
	"strings"

	"github.com/stephrobert/feint/internal/core/sshkey"
	"github.com/stephrobert/feint/internal/trace"
)

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
//
// One value is bought back, and by its own format rather than by its name: an
// OpenSSH public key line, which is the one thing here called "key" that exists
// to be published. See [publishable] — the list below is untouched, and so is
// the header allowlist.
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

// harmlessHeaders are the header names whose value is written down.
//
// Headers are an allowlist where bodies are a denylist, and the asymmetry is
// the whole point. A body is the measurement — the shape a cloud answered is
// what a transcript exists to carry — so redacting it wholesale would destroy
// the tool. A header is not: what matters about it is *which* headers a client
// sent, and that is preserved because the name always stays.
//
// The denylist was measured to fail. `carriers` matches eight substrings, and
// the three dialects served here pass only because their bearers happen to be
// called Authorization and X-Auth-Token. Reproduced on 2026-08-10 against this
// proxy: X-Auth-Token became REDACTED and a header named X-Consumer carrying
// the same value was written in full. That is the "bien formé n'est pas
// autorisé" family — a name check answers "does this look like a credential",
// never "is this not one" — and it is exactly the shape a fourth provider
// arrives in: OVHcloud's own bearer is X-Ovh-Consumer, which matches none of
// the eight.
//
// So the question is inverted. A name nobody has vouched for has its value
// dropped, and adding a dialect means adding to this list rather than hoping
// its bearer resembles the last one. The cost is a transcript slightly less
// readable; the cost of the other order is a credential on disk.
//
// The model is not new here: internal/upstream records no request header at
// all, which is stronger still. This is the same reasoning applied where the
// names have to survive.
//
// TestAnUnknownHeaderIsRedacted fails without this.
var harmlessHeaders = map[string]bool{
	"accept":            true,
	"accept-encoding":   true,
	"accept-language":   true,
	"cache-control":     true,
	"connection":        true,
	"content-length":    true,
	"content-type":      true,
	"date":              true,
	"expect":            true,
	"host":              true,
	"if-match":          true,
	"if-none-match":     true,
	"user-agent":        true,
	"x-forwarded-for":   true,
	"x-forwarded-proto": true,
	"x-request-id":      true,
	"x-feint-probe":     true,
}

// harmless reports whether a header's value may be written down.
func harmless(name string) bool { return harmlessHeaders[strings.ToLower(name)] }

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
	for name, value := range m.Headers {
		if !harmless(name) {
			// The whole value, scheme included. Keeping "EXO2-HMAC-SHA256" and
			// dropping the rest would be more readable and would mean splitting a
			// credential and writing part of it down, which is how the interesting
			// half ends up on the wrong side of the split.
			m.Headers[name] = placeholderFor(value)
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
			if sensitive(k) && nested != nil && !publishable(nested) {
				// Whatever the type, except a null. A key named "secret" holding
				// an object is still a secret, and replacing it wholesale beats
				// descending into it to redact the leaves it happens to have
				// today.
				//
				// A null is the exception because it holds nothing to reveal,
				// and writing "REDACTED" over it *invents* a value: the field's
				// name is kept in full anyway, so nothing is protected, and the
				// recorded type changes from null to string. Measured on
				// 2026-08-20: replaying a real `terraform apply` reported nine
				// divergences, and all nine were this — `kms_key_id` (matched by
				// "key") and `next_page_token` (matched by "token"), both null
				// on the wire, both "REDACTED" on disk, both read back by
				// `feint replay` as a string the emulator failed to serve.
				//
				// The denylist is over-inclusive on purpose and that stays; what
				// changes is that over-inclusion now costs an unreadable value
				// instead of a false measurement.
				//
				// TestARedactedNullStaysNull fails without this.
				//
				// One placeholder per distinct value, not one for all of them:
				// a name-pattern rule catches names as well as secrets, and two
				// names written as one string make a transcript claim two
				// objects were the same. See [placeholderFor] and #384.
				//
				// A list of scalars keeps its brackets, for the null case's
				// reason one type over: flattening it to a string changes the
				// recorded type, and a replay then reissues a shape the client
				// never sent. Measured on 2026-08-28 (#566): the corpus holds
				// `ReadKeypairs {"Filters":{"KeypairNames":"REDACTED-17"}}`,
				// where oapi-cli sent an array — KeypairNames matches "key",
				// which redact.go's own comment names as the price paid
				// knowingly. Nothing had ever been able to see it, because the
				// Outscale pack read an undecodable filter as an absent one and
				// answered 200 with the whole inventory. The type gate that
				// closed #566 turned that silence into a 400, which is how this
				// surfaced at all.
				//
				// Scalars only: an array of objects still goes wholesale,
				// because descending into it would publish every leaf the
				// denylist does not name, which is the opposite of what this
				// function is for.
				// TestARedactedListOfScalarsStaysAList fails without this.
				if items, ok := scalarList(nested); ok {
					value[k] = items
					continue
				}
				value[k] = placeholderFor(textOf(nested))
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

// scalarList replaces every element of a list of scalars with its own
// placeholder, and reports whether the value was such a list.
//
// The brackets are the point. A recording is reissued, and a shape the client
// never sent is a measurement of nothing — the argument [setHeaders] already
// makes for the headers it refuses to copy. One placeholder per element rather
// than one for the list, for [placeholderFor]'s reason: two originals must stay
// two.
//
// A publishable element keeps its value, because the same allowlist that buys
// back a public key at the top level buys it back inside a list.
func scalarList(v any) ([]any, bool) {
	items, isList := v.([]any)
	if !isList {
		return nil, false
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		switch item.(type) {
		case map[string]any, []any:
			return nil, false
		case nil:
			// Kept, for the same reason a null field is: it holds nothing to
			// reveal, and writing over it invents a value.
			out = append(out, item)
		default:
			if publishable(item) {
				out = append(out, item)
				continue
			}
			out = append(out, placeholderFor(textOf(item)))
		}
	}
	return out, true
}

// publishable reports whether a value is one whose own format proves it is
// meant to be published, so that the name-pattern rule above must not eat it.
//
// # Why this is not the denylist loosening
//
// `harmlessHeaders` is an allowlist because a *name* check answers "does this
// look like a credential" and never "is this not one" — measured on 2026-08-10,
// where `X-Auth-Token` became REDACTED while an `X-Consumer` carrying the same
// value was written in full. Nothing here weakens that: this is not a name that
// somebody vouched for, it is a *value* that identifies itself. An OpenSSH
// public key line names its own algorithm out of a closed set of eight and
// carries base64 material and nothing else; [sshkey.Parse] is the same reader
// the packs authenticate with, so what is written down is exactly what the
// emulator would accept. A credential does not arrive in that shape, and a
// private key does not either: OpenSSH writes those as a multi-line PEM block,
// which this refuses on its control characters before it looks at anything.
//
// It is also confined to bodies. Headers keep their allowlist untouched and a
// query parameter keeps the denylist, because a public key travels in neither
// and the query is where SigV4 puts a signature.
//
// # What it costs and what it buys
//
// The asymmetry `carriers` states — a false positive costs one unreadable
// value, a false negative costs a credential — held only while an unreadable
// value was a cosmetic loss. It stopped being one when a transcript became an
// artefact this repository replays: `public_key` matches "key", reached the
// corpus as "REDACTED", and `sshkey.Parse` then refused it, so the create
// answered 400 where the cloud answered 200 and took the read and the delete of
// that key with it. Five of the eight divergences #352 recorded were that one
// substitution, and none of them was a defect of the emulator (#355). This is
// the same family as the null #73 measured: over-inclusion is fine, inventing a
// measurement is not.
//
// TestAPublicKeyUnderACredentialNameIsWrittenDown fails without this, and
// TestASecretUnderACredentialNameIsStillRedacted fails without the shape check
// that keeps it narrow.
func publishable(v any) bool {
	s, isText := v.(string)
	return isText && sshkey.Valid(s)
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
			_, value, _ := strings.Cut(pair, "=")
			out.WriteString(name)
			out.WriteByte('=')
			out.WriteString(placeholderFor(value))
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
