package emulator

import (
	"strings"
	"testing"
)

// The hint is derived from the mounted routes, and it must stay silent as
// readily as it speaks (#179).
//
// First contact is the one moment a user cannot tell a broken emulator from a
// broken pointing, and the answer was net/http's one-line page. What this adds
// says which prefix is missing — and says nothing at all when the path is not
// that shape, because a hint that always fires is noise a reader learns to skip.
func TestAnUnclaimedPathNamesThePrefixItIsMissing(t *testing.T) {
	routes := []Route{
		{Method: "GET", Path: "/v2/instance"},
		{Method: "GET", Path: "/v2/instance/{id}"},
		{Method: "GET", Path: "/block/v1/zones/{zone}/volumes/{id}"},
		{Method: "POST", Path: "/api/v1/CreateVms"},
	}

	for _, tc := range []struct {
		name string
		path string
		want string // a substring the hint must carry, or "" for silence
	}{
		{"the exact trap the README documents", "/instance", "/v2/instance is served"},
		{"a wildcard in the tail still points", "/instance/i-123", "/v2/instance/i-123 is served"},
		{
			// The defect this test exists to keep out. Before the literal-segment
			// rule, "/instance" matched every route ending in "{id}" — the
			// wildcard swallowing the very word meant to identify the mistake —
			// and the answer listed a dozen unrelated paths, which is worse than
			// the one-line page it replaced.
			"a path that only a wildcard would swallow", "/anything", "",
		},
		{"a path that is nothing at all", "/completely-unknown", ""},
		{"the root", "/", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := missingPrefixHint(tc.path, routes)
			switch {
			case tc.want == "" && got != "":
				t.Errorf("a path that names no served route must get no hint, got %q", got)
			case tc.want != "" && got == "":
				t.Errorf("no hint for %q, which is served under a prefix", tc.path)
			case tc.want != "" && !strings.Contains(got, tc.want):
				t.Errorf("the hint must name the served path.\n got: %s\nwant substring: %s", got, tc.want)
			}
		})
	}
}

// Two prefixes that would both answer are both named, rather than one being
// picked and sounding certain.
func TestAmbiguityIsStatedRatherThanResolved(t *testing.T) {
	routes := []Route{
		{Method: "GET", Path: "/v2/instance"},
		{Method: "GET", Path: "/other/instance"},
	}
	got := missingPrefixHint("/instance", routes)
	if !strings.Contains(got, "/v2/instance") || !strings.Contains(got, "/other/instance") {
		t.Errorf("both candidates must appear, got %q", got)
	}
}

// The allow-list that lets the hint echo a path at all.
//
// writeMispointed suppresses a taint warning, and a suppression is only worth
// what the control behind it is worth. This is that control: a path carrying
// anything outside the bytes a provider's URL space uses earns no hint, so the
// plain 404 stands and nothing of the client's is reflected.
//
// The accepting half is asserted too. A validator that refused everything would
// pass every case below and silently remove the feature.
func TestAPathOutsideTheAllowListEarnsNoHint(t *testing.T) {
	routes := []Route{{Method: "GET", Path: "/v2/instance/{id}"}}

	for _, bad := range []string{
		"/instance/<script>alert(1)</script>",
		"/instance/\"quoted\"",
		"/instance/a&b",
		"/instance/" + strings.Repeat("x", 300),
		"/instance/nul\x00byte",
		"/instance/new\nline",
		// The two that matter for a log record rather than for a body:
		// slog's text handler separates keys with "=" and values with a
		// space, so neither may reach it. The allow-list is what stops them.
		"/instance/key=value",
		"/instance/two words",
	} {
		if got := missingPrefixHint(bad, routes); got != "" {
			t.Errorf("a path outside the allow-list must earn no hint.\n path: %q\n hint: %q", bad, got)
		}
	}

	// And the shapes a real client sends are still answered.
	for _, good := range []string{"/instance/i-123", "/instance/a.b-c_d~e", "/instance/i:1"} {
		if got := missingPrefixHint(good, routes); got == "" {
			t.Errorf("an ordinary identifier was refused by the allow-list: %q", good)
		}
	}
}
