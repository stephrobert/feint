package proxy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/trace"
)

// Two different values under credential-shaped names come back as two different
// placeholders (#384).
//
// **Measured before it was written.** Recording a real Outscale account on
// 2026-08-21: the run imports a keypair called `feint-corpus-key` and later
// deletes one called `feint-corpus-absent`, on purpose, to record a refusal.
// `KeypairName` matches `key`, so both reached the transcript as `REDACTED` and
// the file said the two calls addressed the same object. Replayed, the emulator
// deleted the real key on the exchange meant to answer 404 and had nothing left
// for the one meant to answer 200: two status divergences and an absent
// `Errors`, none of them a defect of the emulator.
//
// internal/corpus refuses exactly this shape one stage later
// (TestTwoValuesNeverShareAReplacement) and could not see it, because the merge
// happened here, before the sanitiser ever met two values.
func TestTwoDifferentValuesGetTwoDifferentPlaceholders(t *testing.T) {
	x := trace.Exchange{
		Req: &trace.Message{Body: map[string]any{
			"KeypairName": "feint-corpus-key",
			"OtherKey":    "feint-corpus-absent",
		}},
	}
	redactExchange(&x)
	body, _ := x.Req.Body.(map[string]any)

	first, _ := body["KeypairName"].(string)
	second, _ := body["OtherKey"].(string)
	if first == second {
		t.Fatalf("two different names came back as one placeholder %q: the transcript now says "+
			"two objects of the account were the same, and a replay reissues that claim", first)
	}
	for name, got := range map[string]string{"KeypairName": first, "OtherKey": second} {
		if !IsPlaceholder(got) {
			t.Errorf("%s came back %q, which is not a placeholder at all", name, got)
		}
	}
	// The half that must not move: the values themselves are still gone.
	raw, _ := json.Marshal(x)
	for _, secret := range []string{"feint-corpus-key", "feint-corpus-absent"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("%q is in the transcript: distinguishing two values must not mean writing either down", secret)
		}
	}
}

// The other half, and the one that makes a transcript refer to itself: the same
// value gets the same placeholder wherever it appears.
//
// Without it a create and the delete that names it would carry two different
// placeholders, and a replay would delete something the recording never made.
func TestOneValueGetsOnePlaceholderThroughoutARecording(t *testing.T) {
	const name = "feint-corpus-key"
	create := trace.Exchange{Req: &trace.Message{Body: map[string]any{"KeypairName": name}}}
	remove := trace.Exchange{Req: &trace.Message{Body: map[string]any{"KeypairName": name}}}
	redactExchange(&create)
	redactExchange(&remove)

	a, _ := create.Req.Body.(map[string]any)
	b, _ := remove.Req.Body.(map[string]any)
	if a["KeypairName"] != b["KeypairName"] {
		t.Fatalf("one value got two placeholders, %v and %v: the delete no longer names what the "+
			"create made, and a replayed corpus stops referring to itself", a["KeypairName"], b["KeypairName"])
	}
}

// A placeholder reveals nothing about the value it stands for, whatever that
// value's entropy.
//
// This is the property that keeps the suffix a redaction rather than a hint. A
// plain digest of a short secret is a brute force away from being the secret;
// the suffix is an HMAC under a key drawn once per process and never written
// down, so the same value recorded by two processes reads differently and
// neither reading is invertible.
func TestAPlaceholderIsNotADigestAnybodyCanInvert(t *testing.T) {
	const secret = "hunter2"
	mine := placeholderFor(secret)

	// A second key stands in for a second process. Same value, different
	// placeholder: nothing about the value decides the suffix on its own.
	keep := placeholderKey
	t.Cleanup(func() { placeholderKey = keep })
	placeholderKey = newPlaceholderKey()
	theirs := placeholderFor(secret)

	if mine == theirs {
		t.Fatalf("the same value hashed to %q under two different keys, so the suffix is a plain "+
			"digest and a short secret can be recovered by trying candidates", mine)
	}
	if strings.Contains(mine, secret) || strings.Contains(theirs, secret) {
		t.Errorf("the placeholder carries the value: %q", mine)
	}
}

// IsPlaceholder recognises what this package writes, and refuses what it does
// not — including a value that merely starts with the word.
//
// The bare prefix is accepted because a transcript recorded before #384 carries
// it, and reading one of those as an ordinary string would turn an old corpus
// into a pile of false divergences.
func TestIsPlaceholderRecognisesTheFamilyAndNothingElse(t *testing.T) {
	for _, yes := range []string{Placeholder, Placeholder + "-1", Placeholder + "-a625a944", placeholderFor("x")} {
		if !IsPlaceholder(yes) {
			t.Errorf("%q is one of ours and was not recognised", yes)
		}
	}
	for _, no := range []any{
		"REDACTEDish", "REDACTED-", "REDACTED-zz", "REDACTED_1", "redacted-1", "", 42, nil,
	} {
		if IsPlaceholder(no) {
			t.Errorf("%v was taken for a placeholder; a value the recorder never wrote must not be "+
				"skipped by the replay or kept by the audit", no)
		}
	}
}
