package cli

import (
	"strings"
	"testing"
)

// serve refuses an address that disarms the browser guard.
//
// Measured on this repository, before the fix:
//
//	                                   127.0.0.1   0.0.0.0
//	cross-origin page (Origin: evil)      403        200
//	forged Host: evil.example             403        200
//
// and the only thing printed was "feint dev listening on 0.0.0.0:4603". The
// guard in internal/core/emulator turns itself off when the listen address is
// not loopback — correctly, since it can no longer tell what is local — and
// nothing told the operator the protection had gone with it. With --vm on, what
// is exposed is a container runtime, and store.Restore validates nothing.
//
// This drives the decision, not the server. The first version ran serve, and
// falsifying it hung the suite: with the refusal removed, serve listened, which
// is what it is for. A test that cannot finish without the fix it checks is not
// a test.
func TestServeRefusesANonLoopbackAddress(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:4599", ":4599", "[::]:4599", "192.168.1.10:4599"} {
		err := checkListenAddr(addr, false)
		if err == nil {
			t.Errorf("%s was accepted", addr)
			continue
		}
		// The refusal says what the exposure costs and how to accept it. Without
		// both, an operator reads it as a bug and works around it.
		for _, want := range []string{"browser guard", "--expose-to-network"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal for %s does not mention %q: %v", addr, want, err)
			}
		}
	}

	// The accepting half, which is what stops this from being a guard that
	// refuses everything and passes every attack test while breaking the
	// product: loopback needs no flag, and the flag lifts the refusal for
	// someone who means it.
	for _, addr := range []string{"127.0.0.1:4599", "localhost:4599", "[::1]:4599"} {
		if err := checkListenAddr(addr, false); err != nil {
			t.Errorf("the loopback address %s was refused: %v", addr, err)
		}
	}
	if err := checkListenAddr("0.0.0.0:4599", true); err != nil {
		t.Errorf("--expose-to-network did not lift the refusal: %v", err)
	}
}
