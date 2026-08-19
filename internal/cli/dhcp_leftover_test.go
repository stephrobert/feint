package cli

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
)

// The sweep and the diagnostic for #316: a dnsmasq that outlived its fnt-
// interface holds the block the next run needs, `ip addr` and the runtime's
// listings both show a clean host, and only `ss -lnp` disagrees. These tests
// hold the two consumers through the seams in clean.go; the attribution
// itself — who may be named, who must be left alone — lives with the driver,
// in internal/core/machine's TestLeftoverDHCPRefusesAProcessItCannotAttribute.

// aLeftover is the shape measured on 2026-08-18 and 2026-08-19: the gateway
// of the block the machines-on leg failed to take back.
var aLeftover = machine.DHCPLeftover{
	PID:       4071323,
	Interface: "fnt-99109f524b2",
	Addresses: []string{"10.50.2.1"},
}

func swapLeftoverSeams(t *testing.T, find func() ([]machine.DHCPLeftover, error), end func(machine.DHCPLeftover) error) {
	t.Helper()
	savedFind, savedEnd := findLeftoverDHCP, endLeftoverDHCP
	findLeftoverDHCP, endLeftoverDHCP = find, end
	t.Cleanup(func() { findLeftoverDHCP, endLeftoverDHCP = savedFind, savedEnd })
}

func TestDoctorNamesTheDHCPServiceThatOutlivedItsInterface(t *testing.T) {
	swapLeftoverSeams(t,
		func() ([]machine.DHCPLeftover, error) { return []machine.DHCPLeftover{aLeftover}, nil },
		func(machine.DHCPLeftover) error { t.Fatal("a diagnostic signalled a process"); return nil })

	c := checkLeftoverDHCP()
	if c.state != verdictWarn {
		t.Fatalf("the leftover did not warn: %+v", c)
	}
	// The line must carry what ss -lnp was the only tool to show: the pid,
	// the address it holds, and the interface that no longer exists.
	for _, fact := range []string{"4071323", "10.50.2.1", "fnt-99109f524b2"} {
		if !strings.Contains(c.detail, fact) {
			t.Errorf("the detail does not carry %q: %s", fact, c.detail)
		}
	}
	if !strings.Contains(c.fix, "feint clean") {
		t.Errorf("the fix does not name the remedy: %s", c.fix)
	}
}

// A healthy host earns an ok, not silence: "checked and fine" and "never
// looked" must not read the same, per doctor's own rule.
func TestDoctorSaysNothingOutlivesItsInterfaceOnAHealthyHost(t *testing.T) {
	swapLeftoverSeams(t,
		func() ([]machine.DHCPLeftover, error) { return nil, nil },
		func(machine.DHCPLeftover) error { t.Fatal("a diagnostic signalled a process"); return nil })

	c := checkLeftoverDHCP()
	if c.state != verdictOK {
		t.Fatalf("a healthy host did not get an ok: %+v", c)
	}
}

func TestCleanEndsTheLeftoverDHCPService(t *testing.T) {
	var ended []machine.DHCPLeftover
	swapLeftoverSeams(t,
		func() ([]machine.DHCPLeftover, error) { return []machine.DHCPLeftover{aLeftover}, nil },
		func(l machine.DHCPLeftover) error { ended = append(ended, l); return nil })

	var out bytes.Buffer
	if err := sweepLeftoverDHCP(&out); err != nil {
		t.Fatalf("a swept leftover still failed the sweep: %v", err)
	}
	if len(ended) != 1 || ended[0].PID != aLeftover.PID {
		t.Fatalf("the leftover was not ended: %v", ended)
	}
	if !strings.Contains(out.String(), "10.50.2.1") {
		t.Errorf("the report does not say what was freed:\n%s", out.String())
	}
}

// The common refusal is permission, not foreignness: the runtime's dnsmasq
// belongs to the incus user (uid 987 on the station #316 was measured on),
// and an unprivileged sweep cannot signal it. Then the sweep must say the
// exact command and exit nonzero — an exit 0 would say "clean" about a host
// whose next run dies at the bind.
func TestCleanReportsTheCommandWhenTheLeftoverBelongsToAnotherUser(t *testing.T) {
	swapLeftoverSeams(t,
		func() ([]machine.DHCPLeftover, error) { return []machine.DHCPLeftover{aLeftover}, nil },
		func(machine.DHCPLeftover) error { return fmt.Errorf("signal: %w", os.ErrPermission) })

	var out bytes.Buffer
	err := sweepLeftoverDHCP(&out)
	if err == nil {
		t.Fatal("a block still held was reported as swept")
	}
	if !strings.Contains(out.String(), "sudo kill 4071323") {
		t.Errorf("the report does not carry the exact command:\n%s", out.String())
	}
}

// And the silence: a host with nothing left behind gets no line at all from
// the sweep, so the two sentences network.sh asserts on stay the whole story.
func TestCleanSaysNothingAboutDHCPOnAHealthyHost(t *testing.T) {
	swapLeftoverSeams(t,
		func() ([]machine.DHCPLeftover, error) { return nil, nil },
		func(machine.DHCPLeftover) error { t.Fatal("a healthy host was signalled"); return nil })

	var out bytes.Buffer
	if err := sweepLeftoverDHCP(&out); err != nil {
		t.Fatalf("a healthy host failed the sweep: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("a healthy host produced noise:\n%s", out.String())
	}
}
