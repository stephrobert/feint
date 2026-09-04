package cli

import (
	"strings"
	"testing"
)

// `feint start` forwards the resolver to `feint serve` when one is named, and
// forwards nothing when none is, so the serve default (machine.DefaultResolver)
// stays the one place the default lives (#660).
func TestStartForwardsTheResolverItWasGiven(t *testing.T) {
	named := serveFlags{addr: "127.0.0.1:4599", vm: "incus-ovn", logLevel: "info", resolver: "192.0.2.53"}
	if got := strings.Join(named.args(), " "); !strings.Contains(got, "--resolver 192.0.2.53") {
		t.Errorf("start does not forward the resolver it was given: %s", got)
	}
	unnamed := serveFlags{addr: "127.0.0.1:4599", vm: "incus-ovn", logLevel: "info"}
	if got := strings.Join(unnamed.args(), " "); strings.Contains(got, "--resolver") {
		t.Errorf("start forwards a resolver nobody named, so serve's default is no longer the default: %s", got)
	}
}
