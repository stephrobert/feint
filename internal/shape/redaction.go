package shape

import (
	"strconv"
	"strings"
)

// RedactionToken is the prefix of every synthetic string the corpus sanitiser
// hands out for a value that has no shape of its own.
//
// It lives here rather than in internal/corpus because two packages must
// recognise the same spelling from opposite ends of the same chain, and
// internal/corpus already imports this one: the sanitiser writes the token, and
// this package must refuse to learn a type or a route from it. Two spellings
// would answer differently the day one of them learned a case — the duplication
// [IsMintedIdentifier] already exists to avoid, and internal/corpus aliases this
// constant rather than restating it.
const RedactionToken = "redacted-"

// recorderToken is the prefix the proxy writes over every value it replaces.
//
// Restated here rather than imported, and the reason is the import graph:
// internal/proxy mounts an emulator, internal/core/emulator reads this package,
// so a dependency the other way is a cycle. The restatement is not trusted —
// TestEveryValueTheRecorderWritesIsOneShapeRefuses (internal/proxy, which can
// see both) feeds this function the recorder's own output, so the day the
// recorder changes its spelling the test goes red here rather than a fold
// silently learning "string" again.
const recorderToken = "REDACTED"

// IsRedacted reports whether a recorded value was replaced rather than
// observed — by the recorder (proxy's `REDACTED`, bare or suffixed) or by the
// corpus sanitiser (this package's [RedactionToken]).
//
// The distinction it protects is the one this package is made of. A shape is
// paths and types and nothing else, so a value whose type was erased by a
// redaction is not evidence about the API: the recorder writes a string over
// whatever it replaced, and folding that in teaches "string" for a bool.
//
// Measured on the committed corpora before this existed: folding them into the
// catalogues turned `osc/Client.ReadKeypairs.Keypairs` from `array` into
// `array|string` and `osc/Client.ReadLoadBalancers.LoadBalancers[].SecuredCookies`
// from `bool` into `bool|string`, on top of types a direct `feint shapes
// --record` run had already got right. Twenty-three (operation, field) pairs of
// those corpora carry a placeholder, seven of them over a non-string.
//
// TestARedactedValueTeachesNoType and TestFoldingACorpusDoesNotUnlearnAType
// fail without this.
func IsRedacted(v any) bool {
	s, isText := v.(string)
	if !isText {
		return false
	}
	if s == recorderToken {
		return true
	}
	// The recorder suffixes a keyed digest and the sanitiser a decimal counter;
	// both are numbered forms of the same erasure, and neither carries the type
	// it replaced. A value that merely starts with one of the words is not one
	// of ours.
	if rest, found := strings.CutPrefix(s, recorderToken+"-"); found {
		return isHex(rest) || isDigits(rest)
	}
	rest, found := strings.CutPrefix(s, RedactionToken)
	return found && isDigits(rest)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	_, err := strconv.Atoi(s)
	return err == nil
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// PathIsRedacted reports whether any segment of a request path was replaced
// rather than recorded.
//
// Such a path names no API. It cannot be a catalogue key — `GET
// /redacted-749/redacted-750/redacted-751/fr-par-3/redacted-752` is what the
// committed Scaleway corpus produced, and the number in it moves every time the
// corpus is re-sanitised, which is exactly the volatility this package's own
// header forbids storing — and it cannot be an [Operation.Path] either, because
// that field exists so a reader can reproduce the call.
//
// TestARedactedPathIsNeverAKey fails without this.
func PathIsRedacted(path string) bool {
	for _, seg := range strings.Split(path, "/") {
		if IsRedacted(seg) {
			return true
		}
	}
	return false
}
