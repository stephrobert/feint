package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
)

// The serve half of #521: the driver knows how to release the uplink, and a
// graceful exit that never asks leaves the one network no client's delete can
// remove — which is what two green conformance runs left, and what the next
// run's doorstep refused. These tests hold the wiring through a fake releaser,
// the way leftovers_test.go holds the startup notice; the driver's own
// behaviour lives with the driver, in incus_uplink_release_test.go.

// releasingDriver is a metadata-only driver that records whether the exit
// asked it to release the uplink.
type releasingDriver struct {
	machine.Noop
	asked    bool
	released bool
	err      error
}

func (d *releasingDriver) ReleaseUplink(context.Context) (bool, error) {
	d.asked = true
	return d.released, d.err
}

// TestAGracefulExitReleasesTheUplink: the call happens without --cleanup, and
// a release that happened is said out loud rather than logged nowhere.
func TestAGracefulExitReleasesTheUplink(t *testing.T) {
	var buf bytes.Buffer
	driver := &releasingDriver{released: true}

	shutdownSweep(machine.Use(driver), false, &buf)

	if !driver.asked {
		t.Fatal("the exit never asked the driver to release the uplink; the next run's doorstep refuses what stays (#521)")
	}
	if !strings.Contains(buf.String(), "released the uplink") {
		t.Errorf("a release that happened was not said:\n%s", buf.String())
	}
}

// TestAnExitSaysNothingWhenTheUplinkIsNotItsToRelease: an uplink kept — held
// by another run, or still in use — is the doorstep's story to tell, and a
// line here on every runtime-free exit would be noise nobody reads.
func TestAnExitSaysNothingWhenTheUplinkIsNotItsToRelease(t *testing.T) {
	var buf bytes.Buffer
	driver := &releasingDriver{released: false}

	shutdownSweep(machine.Use(driver), false, &buf)

	if !driver.asked {
		t.Fatal("the exit never asked the driver")
	}
	if buf.Len() != 0 {
		t.Errorf("an exit with nothing released still wrote:\n%s", buf.String())
	}
}

// TestAnExitWithoutAReleaserStaysAnExit: --vm off and every driver without an
// uplink must shut down exactly as before.
func TestAnExitWithoutAReleaserStaysAnExit(t *testing.T) {
	var buf bytes.Buffer

	shutdownSweep(machine.Use(machine.Noop{}), false, &buf)

	if buf.Len() != 0 {
		t.Errorf("a driver with no uplink produced output:\n%s", buf.String())
	}
}
