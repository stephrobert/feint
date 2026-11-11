//go:build conformance

package conformancetest

import (
	"os/exec"
	"testing"
)

// OCTL builds a private octl environment pointed at one local OOS door.
//
// endpoint must carry the door's canonical path INCLUDING its trailing slash.
// Measured on the wire 2026-11-09, with a listener that printed the request
// line and nothing else:
//
//	OSC_ENDPOINT_OOS=http://127.0.0.1:P/_feint/s3/outscale/eu-west-2
//	  -> GET /_feint/s3/outscale/eu-west-2?x-id=ListBuckets
//	OSC_ENDPOINT_OOS=http://127.0.0.1:P/_feint/s3/outscale/eu-west-2/
//	  -> GET /_feint/s3/outscale/eu-west-2/?x-id=ListBuckets
//
// So octl preserves the endpoint's path, unlike exo which drops it, and the
// only thing between the first form and a served request was one character.
// The emulator requires the canonical prefix, trailing slash included, and is
// right to: a prefix it accepted loosely would dispatch two different doors to
// the same handler.
//
// --no-upgrade on every call, for the reason octl.sh states: without it octl
// asks GitHub for a newer release, which is a network call this harness must
// not make and a version this harness did not pin.
func OCTL(t *testing.T, endpoint string) Client {
	t.Helper()
	binary, err := exec.LookPath("octl")
	if err != nil {
		t.Skip("octl is not installed; the OOS door is unproven in this run")
	}
	home := t.TempDir()
	env := []string{
		"HOME=" + home,
		"PATH=/usr/bin:/bin",
		// The repository's deliberately public placeholders. The emulator never
		// validates the signature; the client still refuses to sign without
		// well-formed credentials.
		"OSC_ACCESS_KEY=AAAAAAAAAAAAAAAAAAAA",
		"OSC_SECRET_KEY=BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		"OSC_REGION=eu-west-2",
		"OSC_ENDPOINT_OOS=" + endpoint,
		// Nothing inherits a proxy here: an OOS call that left this machine
		// would be the one failure this harness exists to make impossible.
		"NO_PROXY=*",
		"no_proxy=*",
	}
	return Client{Path: binary, Home: home, Env: env}
}
