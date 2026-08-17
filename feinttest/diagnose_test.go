package feinttest

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A start that fails says what the container did, rather than that it is gone.
//
// This is the control for the `--rm` flag Start no longer passes. On 17 August
// 2026 the ARM leg of the test matrix printed, in place of a reason:
//
//	feinttest: the emulator never answered on http://127.0.0.1:33437: context deadline exceeded
//	    Error response from daemon: No such container: feint-test-7821-33437
//
// The container had died, `--rm` had removed it, and the log that would have
// said why went with it — a diagnosis destroyed by the flag meant to keep the
// machine tidy. Put `--rm` back on the run in feinttest.go and this test fails
// on both assertions below: the runtime answers "No such container" to inspect
// and to logs alike.
//
// `alpine` is the fixture because it exits immediately and listens on nothing,
// which is exactly the shape of the failure being diagnosed.
func TestAFailedStartSaysWhatTheContainerDid(t *testing.T) {
	requireOptIn(t)
	runtime, err := containerRuntime()
	if err != nil {
		t.Skipf("feinttest: %v", err)
	}

	cloud, err := launch(runtime, config{image: "alpine:3.21", timeout: 15 * time.Second})
	if cloud != nil {
		t.Cleanup(cloud.stop)
	}
	if err == nil {
		t.Fatal("a container that listens on nothing was reported as a working emulator")
	}

	msg := err.Error()
	if strings.Contains(msg, "No such container") {
		t.Errorf("the failure names a container the runtime has already destroyed:\n%s", msg)
	}
	if !strings.Contains(msg, "exited") {
		t.Errorf("the failure does not say the container exited, which is the fact a reader needs:\n%s", msg)
	}
}

// requireOptIn keeps the container tests out of a plain `go test ./...`.
//
// They pull a published image from a registry, and `mise run check` is
// deterministic and offline by contract — a suite that reaches ghcr.io fails on
// a train, which teaches contributors to stop trusting it. The CI sets
// FEINT_TESTCONTAINER on every leg of the matrix, so nothing is measured less
// than before; what changed is that the network is asked for on purpose.
func requireOptIn(t *testing.T) {
	t.Helper()
	if os.Getenv("FEINT_TESTCONTAINER") == "" {
		t.Skip("feinttest: set FEINT_TESTCONTAINER=1 to run the container tests (they pull a published image)")
	}
}
