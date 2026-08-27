package scaleway

import "testing"

// tagValues must hand back a COPY, and the []any branch is the only one where
// that can fail.
//
// A live create stores []string, so building a []any from it copies by
// construction and an end-to-end test through the HTTP surface cannot tell a
// copy from an alias there — it would pass with either. The []any branch is
// what a restored snapshot produces: `feint snapshot load` and PUT /_feint/state
// decode the stored tags as JSON, so Attrs["tags"] comes back []any, and a view
// that returned it directly would put the store's own slice inside a response
// map that outlives the call.
//
// This is the control for the claim serverIPView makes in its comment, and it
// is an internal test because the branch it exercises is not reachable from
// outside without a snapshot round-trip: an assertion whose subject cannot be
// selected is an assertion about something else.
func TestTagValuesCopiesTheStoredSlice(t *testing.T) {
	stored := []any{"feint-corpus"}
	out := tagValues(stored)

	if len(out) != 1 || out[0] != "feint-corpus" {
		t.Fatalf("tagValues answered %#v, want the tags it was given", out)
	}
	out[0] = "poisoned"
	if stored[0] != "feint-corpus" {
		t.Errorf("mutating the answer reached the stored slice: it now reads %#v", stored)
	}

	// The []string branch, for what it is worth: same values, and never the
	// same backing array whatever happens.
	if got := tagValues([]string{"a", "b"}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("tagValues([]string) answered %#v", got)
	}

	// Anything else is an empty list, never nil: a client ranging over null
	// crashes, and the SDK declares Tags as a slice.
	for _, absent := range []any{nil, "not a list", 7} {
		if got := tagValues(absent); got == nil || len(got) != 0 {
			t.Errorf("tagValues(%#v) answered %#v, want an empty list", absent, got)
		}
	}
}
