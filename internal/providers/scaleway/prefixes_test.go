package scaleway_test

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// The prefix list is what decides whether a request that matched no route gets
// this pack's error or net/http's plain text. A product added without its prefix
// would answer with the latter for every operation it does not yet serve, which
// is most of them, and nothing else would notice.
func TestEveryRouteFallsUnderADeclaredPrefix(t *testing.T) {
	pack := scaleway.New(emulator.DefaultEnv())
	unrouted, ok := any(pack).(emulator.Unrouted)
	if !ok {
		t.Fatal("the pack no longer declares its URL space")
	}
	prefixes := unrouted.Prefixes()

	for _, route := range pack.Routes() {
		covered := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(route.Path, prefix) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("route %s %s falls under no declared prefix: an unserved operation "+
				"of that product would answer in text/plain", route.Method, route.Path)
		}
	}
}

// The mirror defect is over-claiming, and it is not "a prefix with no route".
//
// That is what this test asked for until 2026-08-11, and the premise was wrong:
// it read a prefix serving nothing as a product this pack has no business
// answering for. But `/lb/v1/` is Scaleway's URL space whether this pack serves
// it or not, and a request landing there is unambiguously a Scaleway client's.
// Answering it in Scaleway's dialect is correct; answering it in net/http's
// text/plain is the defect @vde-dis reported on #74. The old assertion was
// holding that defect in place, which is why it is replaced rather than
// deleted.
//
// What over-claiming actually means is reaching into another pack's space, and
// that is not checked here. It was, for an hour, against a hand-written list of
// the other two roots — which is the same defect one level up: a list a fourth
// pack would be absent from, green while measuring nothing of it.
//
// It lives in the core instead, where the resolution happens and where whatever
// is mounted gets checked by existing: `NewServer` refuses two packs whose
// spaces overlap, the way `NewTable` already refuses two packs claiming one
// route. See TestTwoPacksMayNotClaimOverlappingSpaces in
// internal/core/emulator.
//
// What is left for this pack to assert is the half only it can know: that its
// declared space really is Scaleway's.
func TestEveryDeclaredPrefixLooksLikeAScalewayProduct(t *testing.T) {
	pack := scaleway.New(emulator.DefaultEnv())
	unrouted, _ := any(pack).(emulator.Unrouted)

	// `/<product>/v<N>[alpha|beta]<M>/`, which is how Scaleway mounts every
	// product. A prefix shaped otherwise is either a typo or somebody else's
	// space, and both answer requests in this pack's dialect.
	shape := regexp.MustCompile(`^/[a-z0-9-]+/v[0-9]+(alpha[0-9]*|beta[0-9]*)?/$`)

	for _, prefix := range unrouted.Prefixes() {
		if !shape.MatchString(prefix) {
			t.Errorf("prefix %q is not shaped like a Scaleway product root", prefix)
		}
	}
}

// TestAnUnservedProductAnswersInScalewaysDialect is #74's finding, driven.
//
// A product with no routes at all is invisible to
// TestEveryRouteFallsUnderADeclaredPrefix, which walks the routes this pack
// mounts. So the guard that claimed to stop "a whole product answering
// net/http's plain text" could only ever see the served half, and the unserved
// half answered `404 page not found` in text/plain — which the SDK drops,
// leaving a caller with "404 Not Found" and nothing else.
func TestAnUnservedProductAnswersInScalewaysDialect(t *testing.T) {
	ts := newTestServer(t)

	// Both measured by @vde-dis under a real OpenTofu apply: a load balancer
	// address and a public gateway address, neither served, both Scaleway.
	//
	// /block/v1 used to sit here as a third case and was removed by SW-3, which
	// serves it. That is the healthy direction for this list: a product leaves it
	// by being implemented.
	//
	// block/v1alpha1 briefly replaced it here and lasted one conformance run —
	// `scw` 2.56.3 calls the alpha for every block command, so it is served too.
	// Managed Kubernetes took the slot instead: an entire product with no route
	// under it and none planned, which is the shape this test exists for.
	for _, path := range []string{
		"/lb/v1/zones/fr-par-1/ips",
		"/vpc-gw/v2/zones/fr-par-1/ips",
		"/k8s/v1/regions/fr-par/clusters",
	} {
		// do() decodes the body as JSON and fails otherwise, which is the half
		// that matters: the SDK reads the content type first and drops a body
		// that is not application/json.
		status, body := do(t, ts, "GET", path, "")
		if status != http.StatusNotImplemented {
			t.Errorf("%s answered %d, want 501", path, status)
		}
		if body["type"] != "not_emulated" {
			t.Errorf("%s answered type %v, want not_emulated", path, body["type"])
		}
	}
}

func TestAnUnservedOperationIsReadableByTheSDK(t *testing.T) {
	ts := newTestServer(t)

	// The dashboard exists upstream and is declined (its counters span
	// products this pack does not serve), so this is the answer a real caller
	// meets today. Placement groups played this role until #285 served them.
	status, body := do(t, ts, "GET", "/instance/v1/zones/fr-par-1/dashboard", "")

	if status != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501: the operation exists upstream, it is not served here", status)
	}
	// Reaching this point at all is half the assertion: do() decodes the body as
	// JSON and fails the test otherwise, which is exactly what the SDK does.

	// Not "not_found": that type maps onto ResourceNotFoundError in the SDK, so a
	// caller branching on errors.As would be told a resource is missing when an
	// operation is merely unserved.
	if body["type"] != "not_emulated" {
		t.Errorf("type = %v, want not_emulated", body["type"])
	}
	if msg, _ := body["message"].(string); !strings.Contains(msg, "dashboard") {
		t.Errorf("message = %q, want it to name the path", msg)
	}
}
