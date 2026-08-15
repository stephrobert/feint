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
	seen := map[string]bool{}
	var prefixes []string
	for _, r := range routes {
		prefix, ok := prefixAnswering(path, r.Path)
		if !ok || seen[prefix] {
			continue
		}
		seen[prefix] = true
		prefixes = append(prefixes, prefix)
	}
	if len(prefixes) == 0 {
		return "", nil
	}
	// Sorted so two runs say the same thing, and joined rather than picked:
	// when two prefixes would both answer, saying so is more honest than
	// choosing one and sounding certain.
	sort.Strings(prefixes)
	return "no route matches this path, but it is served under " +
		strings.Join(prefixes, " and ") +
		": the endpoint is probably missing that prefix. " +
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
// Nothing of the client's reaches this body. The sentence names the prefix,
// which is read out of this process's own mounted route table; the request path
// is not repeated, because the caller already knows what it sent and repeating
// it was the only reason this function ever needed an allow-list defending it.
//
// That was the second lesson of #179 and it cost two rounds of CodeQL to learn:
// when a tool reports untrusted data reaching an output, the question to ask
// first is not "is my filter good enough" but "do I need to put it there at
// all". A filter has to be argued for; a value that never came from the client
// does not.
func writeMispointed(w http.ResponseWriter, hint string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(hint + "\n"))
}
