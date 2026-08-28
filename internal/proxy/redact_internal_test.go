package proxy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/sshkey"
	"github.com/stephrobert/feint/internal/shape"
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
		{"a signature goes", "X-Amz-Signature=abc&zone=fr-par-1", "X-Amz-Signature=" + placeholderFor("abc") + "&zone=fr-par-1"},
		{"an encoded name still matches", "X%2DAmz%2DSignature=abc", "X%2DAmz%2DSignature=" + placeholderFor("abc")},
		{"only the value goes", "token=abc", "token=" + placeholderFor("abc")},
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
		if !IsPlaceholder(value) {
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
	if !IsPlaceholder(out["secret_key"]) {
		t.Errorf("a non-null credential came back %#v, want %q: this is the half that must not move",
			out["secret_key"], Placeholder)
	}
	if out["name"] != "a-volume" {
		t.Errorf("an ordinary field was redacted: %#v", out["name"])
	}
}

// A list of scalars under a credential-bearing name keeps its brackets.
//
// The null case above, one type over, and measured the same way. The committed
// corpus holds
// `ReadKeypairs {"Filters":{"KeypairNames":"REDACTED-17"}}`: oapi-cli sent an
// array, `KeypairNames` matches "key", and the whole array was written down as
// one string. A replay then reissues a shape no client ever sent — the argument
// [setHeaders] already makes for the headers it refuses to copy.
//
// Nothing could see it until 2026-08-28 (#566), because the Outscale pack read
// an undecodable filter as an absent one and answered 200 with the whole
// inventory. Two silent defects cancelling out is why this needs a test rather
// than a comment: neither instrument could report the other.
//
// Both directions are asserted. A list of scalars keeps its length and its type
// with one placeholder per element, so two distinct originals stay two; a list
// of objects still goes wholesale, which is
// TestASensitiveContainerIsStillReplacedWholesale's rule and must not have
// widened.
func TestARedactedListOfScalarsStaysAList(t *testing.T) {
	body := map[string]any{
		"KeypairNames": []any{"one-name", "another-name"},
		"api_keys":     []any{"secret-a", "secret-a", "secret-b"},
		"ssh_keys":     []any{map[string]any{"public_key": publicKeyLine(t)}},
		"VmIds":        []any{"i-1", "i-2"},
	}
	out, ok := redactValue(body).(map[string]any)
	if !ok {
		t.Fatalf("redactValue did not answer an object")
	}

	names, isList := out["KeypairNames"].([]any)
	if !isList {
		t.Fatalf("KeypairNames came back %#v, want a list: flattening it changes the "+
			"recorded type, and a replay then reissues a shape the client never sent",
			out["KeypairNames"])
	}
	if len(names) != 2 {
		t.Fatalf("KeypairNames came back with %d element(s), want 2", len(names))
	}
	for i, item := range names {
		if !IsPlaceholder(item) {
			t.Errorf("KeypairNames[%d] came back %#v, want a placeholder: the brackets are "+
				"kept, the values are not", i, item)
		}
	}
	if names[0] == names[1] {
		t.Errorf("two distinct names came back as one placeholder (%v): a transcript would "+
			"then claim the client asked for the same keypair twice", names[0])
	}

	keys, isList := out["api_keys"].([]any)
	if !isList || len(keys) != 3 {
		t.Fatalf("api_keys came back %#v, want a list of three", out["api_keys"])
	}
	if keys[0] != keys[1] {
		t.Errorf("two equal secrets came back as two placeholders (%v, %v): the placeholder "+
			"stands for a value, so equal values share one", keys[0], keys[1])
	}
	if keys[1] == keys[2] {
		t.Errorf("two different secrets came back as one placeholder (%v)", keys[1])
	}

	// The rule that must not have widened: a list of objects is still one
	// string, because descending into it would publish every leaf the denylist
	// does not name.
	if !IsPlaceholder(out["ssh_keys"]) {
		t.Errorf("a list of objects came back %#v, want %q", out["ssh_keys"], Placeholder)
	}
	// And an ordinary list is untouched, or the whole corpus becomes unreadable.
	if ids, _ := out["VmIds"].([]any); len(ids) != 2 || ids[0] != "i-1" || ids[1] != "i-2" {
		t.Errorf("an ordinary list was redacted: %#v", out["VmIds"])
	}
}

// An OpenSSH public key under a name the denylist matches is written down.
//
// `public_key` matches "key", and the substitution that follows is not a
// cosmetic loss: [sshkey.Parse] refuses "REDACTED", so the create the corpus
// recorded answered 400 where the cloud answered 200, and the read and the
// delete of that key went with it. Five of the eight divergences #352 recorded
// were that one value, and none of them was a defect of the emulator (#355).
//
// The exemption is keyed on the value's own format, never on the field's name:
// a key line names its algorithm out of a closed set and carries base64 and
// nothing else, which is a positive identification rather than a guess about
// what a name suggests.
func TestAPublicKeyUnderACredentialNameIsWrittenDown(t *testing.T) {
	key := publicKeyLine(t)
	body := map[string]any{
		"name":       "feint-corpus-key",
		"public_key": key,
		"ssh_key":    key,
		// Under an ordinary container, because a *container* named for a
		// credential is still replaced wholesale and that rule does not move:
		// see TestASensitiveContainerIsStillReplacedWholesale.
		"item": map[string]any{"public_key": key},
	}
	out, ok := redactValue(body).(map[string]any)
	if !ok {
		t.Fatalf("redactValue did not answer an object")
	}
	for _, at := range []string{"public_key", "ssh_key"} {
		if out[at] != key {
			t.Errorf("%s came back %#v, want the key verbatim: a public key is the one thing "+
				"called \"key\" that exists to be published, and replacing it makes the "+
				"transcript unreplayable", at, out[at])
		}
	}
	nested, isObject := out["item"].(map[string]any)
	if !isObject {
		t.Fatalf("an ordinary object holding a public key was replaced wholesale: %#v", out["item"])
	}
	if nested["public_key"] != key {
		t.Errorf("the nested public key came back %#v, want it verbatim", nested["public_key"])
	}
}

// THE SECOND DIRECTION, at the container. A list or an object under a
// credential-bearing name is still replaced whole, whatever its elements look
// like.
//
// This is the rule the exemption above must not have widened, and the cost is
// stated rather than hidden: `ssh_keys` matches "key", so the answer of
// ListSSHKeys reaches a transcript as one string and its shape is not graded.
// That is a loss of coverage, and it is a smaller loss than descending into
// every object somebody named "credentials" and keeping the leaves whose own
// names happen to look harmless.
func TestASensitiveContainerIsStillReplacedWholesale(t *testing.T) {
	key := publicKeyLine(t)
	body := map[string]any{
		"ssh_keys":    []any{map[string]any{"public_key": key}},
		"credentials": map[string]any{"public_key": key, "id": "not-a-secret-but-not-vouched-for"},
	}
	out, ok := redactValue(body).(map[string]any)
	if !ok {
		t.Fatalf("redactValue did not answer an object")
	}
	for _, at := range []string{"ssh_keys", "credentials"} {
		if !IsPlaceholder(out[at]) {
			t.Errorf("%s came back %#v, want %q: a container named for a credential is "+
				"replaced whole, and one publishable leaf inside it does not lift that",
				at, out[at], Placeholder)
		}
	}
}

// THE SECOND DIRECTION. Everything else under a credential-bearing name still
// goes, and the value's format is what keeps the exemption from being a hole.
//
// The five values below are what a loosening would let through: a real secret,
// a bearer token, an OpenSSH *private* key in the armoured form OpenSSH
// actually writes, the same armour flattened onto one line so that only a
// format reader can tell it from a public key, and an object.
func TestASecretUnderACredentialNameIsStillRedacted(t *testing.T) {
	armoured := privateKeyArmour(t)
	body := map[string]any{
		"secret_key":  "not-a-real-credential-but-shaped-like-one",
		"auth_token":  "ey.a.token",
		"private_key": armoured,
		"ssh_key":     strings.ReplaceAll(armoured, "\n", " "),
		"api_key":     map[string]any{"secret": "still-a-secret"},
	}
	out, ok := redactValue(body).(map[string]any)
	if !ok {
		t.Fatalf("redactValue did not answer an object")
	}
	for _, at := range []string{"secret_key", "auth_token", "private_key", "ssh_key", "api_key"} {
		if !IsPlaceholder(out[at]) {
			t.Errorf("%s came back %#v, want %q: the exemption is for a value whose format "+
				"proves it is published, and none of these has that format", at, out[at], Placeholder)
		}
	}
}

// THE SECOND DIRECTION, at the two other doors. A header keeps its allowlist and
// a query parameter keeps the denylist, whatever the value looks like.
//
// A public key travels in neither, and the query is where SigV4 puts a
// signature: an exemption that reached them would answer "does this value look
// harmless" at exactly the places where the answer has to be "nobody vouched
// for this name".
func TestAPublicKeyOutsideABodyIsStillRedacted(t *testing.T) {
	key := publicKeyLine(t)
	x := trace.Exchange{
		Query: "public_key=" + key,
		Req:   &trace.Message{Headers: map[string]string{"X-Ssh-Key": key, "X-Consumer": key}},
	}
	redactExchange(&x)
	if x.Query != "public_key="+placeholderFor(key) {
		t.Errorf("the query came back %q, want the parameter redacted: the query is where a "+
			"presigned signature lives, and it is not a place a public key travels", x.Query)
	}
	if strings.Contains(x.Query, key) {
		t.Errorf("the key itself is in the query: %q", x.Query)
	}
	for name, value := range x.Req.Headers {
		if !IsPlaceholder(value) {
			t.Errorf("header %s came back %q; headers are an allowlist and no key format lifts it", name, value)
		}
	}
}

// publicKeyLine renders a valid OpenSSH public key rather than pasting one.
//
// Rendered through internal/core/sshkey for the reason that package exists: the
// format was written twice here and the copies drifted, so a fixture pasted by
// hand would be a third copy — and this one has to be exactly what the reader
// under test accepts, or the test would pass on a string neither side calls a
// key.
func publicKeyLine(t *testing.T) string {
	t.Helper()
	blob := make([]byte, 0, 4+len("ssh-ed25519")+4+32)
	blob = append(blob, 0, 0, 0, byte(len("ssh-ed25519")))
	blob = append(blob, "ssh-ed25519"...)
	blob = append(blob, 0, 0, 0, 32)
	blob = append(blob, make([]byte, 32)...)
	line := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " feint-corpus"
	if !sshkey.Valid(line) {
		t.Fatalf("the fixture is not a key the emulator's own reader accepts, so this test would prove nothing")
	}
	return line
}

// privateKeyArmour renders the armour OpenSSH puts around a private key, with
// the label assembled rather than written out: a secret scanner reads a
// repository line by line and cannot tell a fixture from the real thing, which
// is exactly the property that makes the armour worth testing against.
func privateKeyArmour(t *testing.T) string {
	t.Helper()
	label := "OPENSSH PRIVATE" + " KEY"
	armoured := "-----BEGIN " + label + "-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmU=\n-----END " + label + "-----"
	if sshkey.Valid(armoured) {
		t.Fatalf("the key reader accepts an armoured private key, which would make the exemption a hole")
	}
	return armoured
}

// Every value this recorder writes is one internal/shape refuses to learn from.
//
// internal/shape restates the `REDACTED` prefix rather than importing it: this
// package mounts an emulator and internal/core/emulator reads internal/shape,
// so the import can only go one way. A restatement is a second spelling, and
// this repository has paid twice for two spellings of one fact — so the
// restatement is not trusted, it is fed this recorder's own output.
//
// Without it, a recorder that changed its placeholder would leave the fold
// learning "string" for every bool and array it replaced, silently, and the
// catalogue would publish a polymorphism no provider has.
func TestEveryValueTheRecorderWritesIsOneShapeRefuses(t *testing.T) {
	for _, value := range []string{
		Placeholder,
		placeholderFor(""),
		placeholderFor("a-secret"),
		placeholderFor("feint-corpus-key"),
	} {
		if !shape.IsRedacted(value) {
			t.Errorf("shape.IsRedacted(%q) = false: the recorder writes it and the catalogue would learn its type", value)
		}
	}
	// The witness: a value the recorder never writes must not be refused, or a
	// predicate that answered true to everything would pass this test and erase
	// the catalogue.
	for _, value := range []string{"REDACTEDLY", "REDACTED-zz", "running", ""} {
		if shape.IsRedacted(value) {
			t.Errorf("shape.IsRedacted(%q) = true, and the recorder never writes it", value)
		}
	}
}
