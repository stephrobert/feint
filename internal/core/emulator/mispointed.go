package emulator

import (
	"net/http"
	"sort"
	"strings"
)

// A path nobody claimed, and the one question the answer never addressed (#179).
//
// First contact is the single moment a user cannot yet tell a broken emulator
// from a broken pointing, and the answer was `net/http`'s one-line page:
//
//	$ curl -s http://127.0.0.1:4599/instance
//	404 page not found
//
// Nothing in that says which side is wrong. The person holding it has not yet
// found `/_feint/trace`, and the knowledge that `exo` needs `/v2` on its
// endpoint lives in the README — which is exactly nowhere at the moment the
// mistake fires.
//
// What this adds is derived, never declared: the mounted route table already
// knows every prefix this process serves, so a path that becomes a mounted
// route once some prefix is put back in front of it is a pointing mistake, and
// the emulator can say which prefix is missing. No provider is named here and
// none can be — that is rule 5, and it is also why this works for a fourth pack
// nobody has written yet.
//
// Still 404. The request stays refused; only the refusal starts telling the
// truth.
//
// TestAnUnclaimedPathNamesThePrefixItIsMissing fails without this.

// missingPrefixHint answers the sentence to add to a 404 for a path that would
// have matched had it carried a prefix this server mounts, and the prefixes it
// named. Both are empty when the path is not that shape.
//
// The prefixes are returned separately because they are the half that came from
// this process rather than from the client: the sentence embeds the request
// path, the prefixes are read out of the mounted route table. The log takes the
// second and never the first, so no client-derived value reaches a log record
// at all — a structural guarantee rather than an argument about what an
// allow-list keeps out.
//
// Matching is on the mounted patterns rather than on their wildcards: a request
// for "/instance" is compared with "/v2/instance", and a request for
// "/instance/i-1" with "/v2/instance/{id}" through the same segment count and
// literal comparison the router itself would use. Anything cleverer would be a
// second router, and two routers disagreeing is worse than no hint.
func missingPrefixHint(path string, routes []Route) (string, []string) {
	if path == "" || path == "/" {
		return "", nil
	}
	// The answer echoes the path back, so the path is validated before it is
	// echoed rather than escaped afterwards — the order this repository states
	// for producing any text from client input, and the reason cloudinit is the
	// only place a structured format still goes through a template.
	//
	// A path outside this set matches no route of any provider here, so refusing
	// to hint costs nothing: the plain 404 stands, which is the right answer for
	// a request that was never going to match anyway.
	if !plainPath(path) {
		return "", nil
	}
	seen := map[string]bool{}
	var found, prefixes []string
	for _, r := range routes {
		prefix, ok := prefixAnswering(path, r.Path)
		if !ok || seen[prefix] {
			continue
		}
		seen[prefix] = true
		found = append(found, strings.TrimSuffix(prefix, "/")+path)
		prefixes = append(prefixes, prefix)
	}
	if len(found) == 0 {
		return "", nil
	}
	// Sorted so two runs say the same thing, and joined rather than picked:
	// when two prefixes would both answer, saying so is more honest than
	// choosing one and sounding certain.
	sort.Strings(found)
	sort.Strings(prefixes)
	return "no route matches " + path + ", but " + strings.Join(found, " and ") +
		" is served: the endpoint is probably missing that prefix. " +
		"`feint env <provider>` prints the endpoint a client expects, and " +
		"/_feint/routes lists what is mounted", prefixes
}

// prefixAnswering reports the prefix that turns path into pattern, when one
// does. It requires the remainder to match the pattern segment for segment,
// with a wildcard matching any single segment, so "/instance" is answered by
// "/v2/instance" and never by "/v2/instances".
func prefixAnswering(path, pattern string) (string, bool) {
	patternParts := strings.Split(strings.Trim(pattern, "/"), "/")
	pathParts := strings.Split(strings.Trim(path, "/"), "/")
	if len(pathParts) >= len(patternParts) {
		return "", false // no room for a prefix, or a longer path than the route
	}
	offset := len(patternParts) - len(pathParts)
	literals := 0
	for i, want := range patternParts[offset:] {
		if strings.HasPrefix(want, "{") {
			continue // a wildcard eats any one segment
		}
		if want != pathParts[i] {
			return "", false
		}
		literals++
	}
	// At least one segment the user typed must match a literal one the route
	// declares. Without this, "/instance" matched every route ending in "{id}"
	// — the wildcard swallowing the very word that was supposed to identify the
	// mistake — and the answer listed a dozen unrelated paths. Found by running
	// it against the real route table, which is the only place that flood shows.
	if literals == 0 {
		return "", false
	}
	return "/" + strings.Join(patternParts[:offset], "/") + "/", true
}

// writeMispointed answers 404 with the hint, in plain text.
//
// Plain text and not a provider's error envelope, deliberately: nothing claimed
// this URL space, so there is no dialect to speak. Inventing one would be the
// invented format this project never ships.
//
// The hint carries the request path, so three things stand between it and a
// reflected-content defect, and they are listed because the linter cannot see
// past the first:
//
//  1. the path is validated at intake by plainPath, an allow-list of the bytes
//     a provider's URL space uses — no angle bracket, quote or ampersand can
//     reach here, and TestAPathOutsideTheAllowListEarnsNoHint holds it;
//  2. the response is text/plain, which is not a document a browser executes;
//  3. X-Content-Type-Options: nosniff, so it is not re-typed into one.
//
// Removing any of the three reopens the question, which is why the suppression
// names all three rather than the linter's rule number alone.
func writeMispointed(w http.ResponseWriter, hint string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(hint + "\n")) //nolint:gosec // G705: allow-listed at intake, text/plain, nosniff — see above
}

// plainPath reports whether every byte of the path is one a provider's own URL
// space uses. Deliberately narrow: this is an allow-list, so a character nobody
// thought of is refused rather than accepted, and the failure mode of being too
// strict is one plain 404 instead of one helpful one.
//
// Length is capped for the same reason the character set is narrow: a hint is a
// sentence a human reads, and a two-kilobyte path echoed into it is not.
func plainPath(path string) bool {
	if len(path) > 200 {
		return false
	}
	for i := 0; i < len(path); i++ {
		c := path[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '/', c == '-', c == '_', c == '.', c == '~', c == ':':
		default:
			return false
		}
	}
	return true
}
