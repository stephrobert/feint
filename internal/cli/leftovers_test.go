package cli

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
)

// The startup notice is the observable half of the crash policy (#135): after
// a kill -9, labelled machines survive on the runtime, and the next start must
// name them without resurrecting them. These tests hold the notice through a
// fake Surveyor, so no real runtime is needed; the Incus half of the property
// (only labelled names, only read commands) lives with the driver, in
// TestSurveyFindsOnlyTheEmulatorsWorkAndTouchesNothing.

// surveyingDriver is a metadata-only driver that also answers a survey, which
// is exactly the shape reportLeftovers keys on.
type surveyingDriver struct {
	machine.Noop
	left machine.Leftovers
	err  error
}

func (d surveyingDriver) Survey(context.Context) (machine.Leftovers, error) {
	return d.left, d.err
}

func noticed(t *testing.T, driver machine.Runtime) string {
	t.Helper()
	var buf bytes.Buffer
	reportLeftovers(driver, slog.New(slog.NewTextHandler(&buf, nil)))
	return buf.String()
}

func TestStartupNamesTheLeftoversItDidNotAdopt(t *testing.T) {
	out := noticed(t, machine.Use(surveyingDriver{left: machine.Leftovers{
		Machines: []string{"feint-scw-79e3ef40", "feint-osc-i-21519d57"},
		Networks: []string{"fnt-default"},
	}}))

	// The line must name the machines: "2 machines exist" sends the operator
	// to the runtime to find out which, and the whole point is that the
	// emulator already knows.
	for _, name := range []string{"feint-scw-79e3ef40", "feint-osc-i-21519d57"} {
		if !strings.Contains(out, name) {
			t.Errorf("the notice does not name %s:\n%s", name, out)
		}
	}
	// And it must say what to do, and what was not done: `feint clean` is the
	// remedy, adoption is the trap.
	if !strings.Contains(out, "feint clean") {
		t.Errorf("the notice does not name the remedy:\n%s", out)
	}
	if !strings.Contains(out, "adopted") {
		t.Errorf("the notice does not state that nothing was adopted:\n%s", out)
	}
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("leftover machines are a warning, not chatter:\n%s", out)
	}
}

func TestStartupStaysSilentWhenTheRuntimeIsClean(t *testing.T) {
	if out := noticed(t, machine.Use(surveyingDriver{})); out != "" {
		t.Errorf("a clean runtime produced a notice:\n%s", out)
	}
}

func TestStartupStaysSilentOverPlumbingAlone(t *testing.T) {
	// Networks without machines are the healthy steady state: an emulated
	// bridge is reused under its name on the next boot, and the OVN uplink is
	// kept across runs by design. A notice that fired on every restart would
	// be read by nobody, which is worse than none.
	out := noticed(t, machine.Use(surveyingDriver{left: machine.Leftovers{
		Networks:  []string{"feint-uplink", "fnt-default"},
		Firewalls: []string{"scw-bbbb"},
	}}))
	if out != "" {
		t.Errorf("plumbing without machines produced a notice:\n%s", out)
	}
}

func TestStartupSaysWhenItCouldNotLook(t *testing.T) {
	// Silence on error would be the exact defect the notice removes: the
	// operator must at least learn that nobody looked.
	out := noticed(t, machine.Use(surveyingDriver{err: errors.New("incus: connection refused")}))
	if !strings.Contains(out, "could not look") || !strings.Contains(out, "connection refused") {
		t.Errorf("a failed survey must be said, got:\n%s", out)
	}
}

func TestStartupAsksNothingOfADriverThatCannotAnswer(t *testing.T) {
	// Noop backs --vm off: no runtime, nothing to survey, and the notice must
	// not manufacture one.
	if out := noticed(t, machine.Use(machine.Noop{})); out != "" {
		t.Errorf("a driver without Survey produced a notice:\n%s", out)
	}
}
