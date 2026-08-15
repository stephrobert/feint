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
		{"the exact trap the README documents", "/instance", "served under /v2/"},
		{"a wildcard in the tail still points", "/instance/i-123", "served under /v2/"},
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
			got, _ := missingPrefixHint(tc.path, routes)
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
	got, _ := missingPrefixHint("/instance", routes)
	if !strings.Contains(got, "/v2/") || !strings.Contains(got, "/other/") {
		t.Errorf("both candidates must appear, got %q", got)
	}
}

// The log carries nothing the client chose.
//
// CodeQL flagged the hint as a tainted value reaching a log record, and it was
// right to: the sentence embeds the request path. The first answer narrowed the
// line to one argument and leaned on plainPath's allow-list, which is a real
// control and still an argument about what a filter keeps out.
//
// This is the stronger form. The prefixes come out of this process's own mounted
// route table, so the value logged never came from the client at all. The test
// holds exactly that: whatever the client sends, the second return carries only
// strings that appear in the route table.
func TestTheLogCarriesNothingTheClientChose(t *testing.T) {
	routes := []Route{
		{Method: "GET", Path: "/v2/instance"},
		{Method: "GET", Path: "/v2/instance/{id}"},
	}
	for _, path := range []string{"/instance", "/instance/i-123", "/instance/anything-at-all"} {
		_, prefixes := missingPrefixHint(path, routes)
		if len(prefixes) == 0 {
			t.Fatalf("no prefix for %q, so this test measures nothing", path)
		}
		for _, p := range prefixes {
			if p != "/v2/" {
				t.Errorf("the logged value must come from the route table and nowhere "+
					"else; got %q for request %q", p, path)
			}
		}
	}
}

// The answer never repeats what the client sent.
//
// This is the guard the allow-list used to stand in for, and it is stronger
// because it needs no argument: whatever a client puts in the path, none of it
// comes back. CodeQL traced the old echo through the response wrappers and
// reported two high-severity findings on files this change never touched, which
// is how a data flow announces that it is the source.
func TestTheAnswerNeverRepeatsWhatTheClientSent(t *testing.T) {
	routes := []Route{{Method: "GET", Path: "/v2/instance/{id}"}}

	for _, sent := range []string{
		"i-123",
		"<script>alert(1)</script>",
		"\"quoted\"",
		"a&b",
		strings.Repeat("x", 400),
	} {
		hint, _ := missingPrefixHint("/instance/"+sent, routes)
		if hint == "" {
			continue // refusing to hint is its own valid answer
		}
		if strings.Contains(hint, sent) {
			t.Errorf("the answer repeated a value the client chose.\n sent: %.40q\n hint: %s", sent, hint)
		}
	}
}
