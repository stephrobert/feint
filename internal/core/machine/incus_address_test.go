package machine

import (
	"errors"
	"testing"
)

// The route lookup is over an exact comma-separated entry: a substring match
// would see 203.0.113.2/32 inside 203.0.113.22/32 and undo the wrong address.
func TestRouteListContains(t *testing.T) {
	tests := []struct {
		routes string
		route  string
		want   bool
	}{
		{"203.0.113.2/32", "203.0.113.2/32", true},
		{"10.0.0.0/24, 203.0.113.2/32", "203.0.113.2/32", true},
		{"203.0.113.22/32", "203.0.113.2/32", false},
		{"", "203.0.113.2/32", false},
	}
	for _, tt := range tests {
		if got := routeListContains(tt.routes, tt.route); got != tt.want {
			t.Errorf("routeListContains(%q, %q) = %v, want %v", tt.routes, tt.route, got, tt.want)
		}
	}
}

// TestARepeatedRouteIsNotAFailure holds the idempotent half of RouteAddress.
//
// Giving an address that is already on the interface must succeed: the contract
// says a second call is a no-op, and the poweron path replays the routing of an
// address attached before the machine existed.
//
// Two wordings, because two implementations of `ip` are in play and the code
// knew one. iproute2 says "File exists"; busybox, which is what an Alpine image
// ships, says "Address already assigned" — measured on images:alpine/3.21/cloud,
// not recalled. Matching only the first turned a re-route of an Alpine machine
// into a hard failure on the path that must not fail.
func TestARepeatedRouteIsNotAFailure(t *testing.T) {
	tolerated := map[string]string{
		"iproute2": "RTNETLINK answers: File exists",
		"busybox":  "Error: ipv4: Address already assigned.",
		// Case is not the contract: the comparison lowercases, and a runtime
		// that shouts would otherwise reopen the same hole.
		"shouting": "ERROR: IPV4: ADDRESS ALREADY ASSIGNED.",
	}
	for who, said := range tolerated {
		if !addressAlreadyThere(errors.New(said)) {
			t.Errorf("%s: %q should read as already there", who, said)
		}
	}

	// And a real failure must stay one. A permission error or a missing device
	// silently swallowed is a machine the API says is addressed and is not.
	for who, said := range map[string]string{
		"missing device": "Cannot find device \"eth9\"",
		"not permitted":  "RTNETLINK answers: Operation not permitted",
		"no such file":   "exec: \"ip\": executable file not found in $PATH",
	} {
		if addressAlreadyThere(errors.New(said)) {
			t.Errorf("%s: %q was swallowed as already there", who, said)
		}
	}
}
