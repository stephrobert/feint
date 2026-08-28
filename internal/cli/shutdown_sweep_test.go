package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
)

// The serve half of #521: the driver knows how to release the host plumbing,
// and a graceful exit that never asks leaves the objects no client's delete can
// remove — which is what two green conformance runs left, and what the next
// run's doorstep refused. The same defect came back on 2026-08-28 with three
// more objects (the default machine network and two default-group rule sets),
// which is why the exit now names what it gave back instead of announcing one.
// These tests hold the wiring through a fake releaser, the way leftovers_test.go
// holds the startup notice; the driver's own behaviour lives with the driver, in
// incus_release_test.go and incus_uplink_release_test.go.

// releasingDriver is a metadata-only driver that records whether the exit
// asked it to release its plumbing.
type releasingDriver struct {
	machine.Noop
	asked    bool
	released []string
	err      error
}

func (d *releasingDriver) ReleasePlumbing(context.Context) ([]string, error) {
	d.asked = true
	return d.released, d.err
}

// TestAGracefulExitReleasesTheUplink: the call happens without --cleanup, and
// a release that happened is said out loud rather than logged nowhere.
func TestAGracefulExitReleasesTheUplink(t *testing.T) {
	var buf bytes.Buffer
	driver := &releasingDriver{released: []string{"feint-uplink"}}

	shutdownSweep(machine.Use(driver), false, &buf)

	if !driver.asked {
		t.Fatal("the exit never asked the driver to release its plumbing; the next run's doorstep refuses what stays (#521)")
	}
	if !strings.Contains(buf.String(), "feint-uplink") {
		t.Errorf("a release that happened was not said:\n%s", buf.String())
	}
}

// TestAGracefulExitNamesEveryPieceOfPlumbingItGaveBack is the half the leg of
// 2026-08-28 asked for: four objects were left on that host and the exit's
// line spoke of one. A count would not have told the operator which of them
// went, and the whole value of this line is that the next doorstep's silence
// is explained by it.
func TestAGracefulExitNamesEveryPieceOfPlumbingItGaveBack(t *testing.T) {
	var buf bytes.Buffer
	driver := &releasingDriver{released: []string{"scw-45e6b24f03f", "fnt-default", "feint-uplink"}}

	shutdownSweep(machine.Use(driver), false, &buf)

	for _, name := range driver.released {
		if !strings.Contains(buf.String(), name) {
			t.Errorf("the exit released %s and did not name it:\n%s", name, buf.String())
		}
	}
}

// TestAnExitSaysNothingWhenTheUplinkIsNotItsToRelease: plumbing kept — held by
// another run, or still in use — is the doorstep's story to tell, and a line
// here on every runtime-free exit would be noise nobody reads.
func TestAnExitSaysNothingWhenTheUplinkIsNotItsToRelease(t *testing.T) {
	var buf bytes.Buffer
	driver := &releasingDriver{}

	shutdownSweep(machine.Use(driver), false, &buf)

	if !driver.asked {
		t.Fatal("the exit never asked the driver")
	}
	if buf.Len() != 0 {
		t.Errorf("an exit with nothing released still wrote:\n%s", buf.String())
	}
}

// TestAnExitThatCouldNotReleaseSaysSo: a release that failed is what the next
// doorstep will refuse on, and the operator must hear it from the run that
// caused it rather than from the run that meets it.
func TestAnExitThatCouldNotReleaseSaysSo(t *testing.T) {
	var buf bytes.Buffer
	driver := &releasingDriver{err: errors.New("rule set scw-45e6b24f03f: in use by something")}

	shutdownSweep(machine.Use(driver), false, &buf)

	if !strings.Contains(buf.String(), "scw-45e6b24f03f") {
		t.Errorf("a failed release was swallowed:\n%s", buf.String())
	}
}

// TestAnExitWithoutAReleaserStaysAnExit: --vm off and every driver without
// plumbing must shut down exactly as before.
func TestAnExitWithoutAReleaserStaysAnExit(t *testing.T) {
	var buf bytes.Buffer

	shutdownSweep(machine.Use(machine.Noop{}), false, &buf)

	if buf.Len() != 0 {
		t.Errorf("a driver with no plumbing produced output:\n%s", buf.String())
	}
}
