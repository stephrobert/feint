package proxy

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// A redaction must not merge two different values into one (#384).
//
// # What went wrong
//
// [Placeholder] was one constant, so every value the rules matched became the
// same string. That is right for a credential and wrong for everything else the
// name-pattern rule catches, and the difference is not cosmetic: a transcript
// exists to be replayed, and a replay reissues what it reads.
//
// Measured on 2026-08-21, recording a real Outscale account. The recording
// imports a keypair called `feint-corpus-key` and later deletes one called
// `feint-corpus-absent`, on purpose, to record a refusal. `KeypairName` matches
// `key`, so **both names reached the transcript as `REDACTED`** and the file now
// said the two calls addressed the same object. Replayed, the emulator deleted
// the real key on the exchange that was meant to 404 and had nothing left for
// the one that was meant to succeed: two status divergences and an absent
// `Errors` array, none of them a defect of the emulator.
//
// internal/corpus refuses exactly this shape one stage later —
// TestTwoValuesNeverShareAReplacement, "the transcript would then say that two
// objects of the account were one" — and could not see it, because the merge
// happened in the recorder, before the sanitiser ever met two values.
//
// # What replaces it
//
// One placeholder per distinct original: `REDACTED-<8 hex>`, where the hex is an
// HMAC of the value under a key drawn once per process and never written down.
// Three properties, and the third is why it is an HMAC rather than a hash:
//
//   - the same original gets the same placeholder throughout a recording, so a
//     create and the delete that names it still refer to each other;
//   - two originals get two placeholders, so the transcript stops claiming they
//     were one;
//   - **the value cannot be recovered from the placeholder, whatever its
//     entropy.** A plain hash of a short secret is a brute-force away from being
//     the secret; a keyed one is not, and the key exists only in this process's
//     memory. That is the property that keeps this a redaction rather than a
//     hint.
//
// The prefix stays `REDACTED` so every reader that recognises the family keeps
// working through [IsPlaceholder], and so a human scanning a transcript reads
// the same word. Nothing about the *set* of redacted names changes: `carriers`
// is untouched and so is the header allowlist.
//
// TestTwoDifferentValuesGetTwoDifferentPlaceholders fails without this, and
// TestOneValueGetsOnePlaceholderThroughoutARecording holds the other half.

// Placeholder is the prefix of every value the rules match, and the whole of
// the value where there is nothing to tell apart.
//
// The name beside it always stays. A transcript exists to be read — by a human
// looking for the call a client made before the one that failed, by a replay
// reissuing it, by `coverage --observed` ranking what to serve next — and a
// record whose keys have been erased along with its values answers none of
// those questions. So: names in full, values gone.
const Placeholder = "REDACTED"

// placeholderKey is drawn once per process and never leaves it. A recording is
// a single process's output, which is exactly the scope over which two values
// must be told apart; across two recordings the placeholders differ, and
// nothing needs them to match.
var placeholderKey = newPlaceholderKey()

func newPlaceholderKey() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		// crypto/rand does not fail on any platform this runs on, and a
		// recorder that carried on with a predictable key would be writing
		// down a reversible digest of a credential. Refusing to start is the
		// only honest option, and it is unreachable in practice.
		panic("proxy: no randomness for the redaction key: " + err.Error())
	}
	return key
}

// placeholderFor returns the placeholder that stands for one value.
//
// An empty value keeps the bare [Placeholder]: there is nothing to tell it
// apart from, and suffixing it would invent a distinction the recording does
// not carry.
func placeholderFor(value string) string {
	if value == "" {
		return Placeholder
	}
	mac := hmac.New(sha256.New, placeholderKey)
	mac.Write([]byte(value))
	return Placeholder + "-" + hex.EncodeToString(mac.Sum(nil)[:4])
}

// IsPlaceholder reports whether a recorded value is one the recorder replaced.
//
// Exported because three readers ask this question from outside: the replay,
// which must not compare a value whose type was erased rather than observed;
// the sanitiser, which must not publish one and must not treat it as an
// ordinary string either; and the cloud reissue, which refuses to send a
// request still carrying one.
//
// It accepts the bare prefix as well as the suffixed form, so a transcript
// recorded before #384 is still read correctly rather than silently graded as
// an ordinary string — the failure mode that would turn an old corpus into a
// pile of false divergences.
func IsPlaceholder(v any) bool {
	s, isText := v.(string)
	if !isText || !strings.HasPrefix(s, Placeholder) {
		return false
	}
	rest := s[len(Placeholder):]
	if rest == "" {
		return true
	}
	if rest[0] != '-' {
		return false
	}
	// Either the recorder's hex or the sanitiser's counter, and nothing else:
	// a value that merely starts with the word is not one of ours.
	for _, r := range rest[1:] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return len(rest) > 1
}

// textOf renders the value a placeholder stands for, so that two originals of
// any JSON type are told apart rather than only two strings.
//
// A key named "secret" holding an object is still a secret and is replaced
// whole (see [redactValue]); this is what gives that whole replacement its own
// identity. Rendering rather than hashing the Go value: two structurally equal
// documents are the same secret, and two different ones are two.
func textOf(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case nil:
		return ""
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			// Unreachable for a document that came out of encoding/json, and a
			// distinct placeholder is not worth a panic: the bare prefix is the
			// safe answer, since it reveals nothing and only costs the
			// distinction.
			return ""
		}
		return string(raw)
	}
}
