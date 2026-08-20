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
	// A fail, not a warning: this state kills the next machines-on run with
	// certainty, and a doctor that stays green over it is the reassurance
	// #342 measured the cost of.
	if c.state != verdictFail {
		t.Fatalf("the leftover did not fail the diagnosis: %+v", c)
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

// TestDoctorFailsOnTheDHCPServiceWhoseNetworkIsGone is #342's acceptance: the
// state that produced it — a bridge and its dnsmasq both surviving the
// network — goes red, and the line carries the block and the pid, the two
// facts needed to act.
func TestDoctorFailsOnTheDHCPServiceWhoseNetworkIsGone(t *testing.T) {
	survivor := machine.DHCPLeftover{
		PID:            612421,
		Interface:      "fnt-99109f524b2",
		Addresses:      []string{"10.50.2.1"},
		InterfaceAlive: true,
	}
	swapLeftoverSeams(t,
		func() ([]machine.DHCPLeftover, error) { return []machine.DHCPLeftover{survivor}, nil },
		func(machine.DHCPLeftover) error { t.Fatal("a diagnostic signalled a process"); return nil })

	c := checkLeftoverDHCP()
	if c.state != verdictFail {
		t.Fatalf("the survivor did not fail the diagnosis: %+v", c)
	}
	for _, fact := range []string{"612421", "10.50.2.1"} {
		if !strings.Contains(c.detail, fact) {
			t.Errorf("the detail does not carry %q: %s", fact, c.detail)
		}
	}
}

// And the green line claims exactly what was measured: the emulator's own
// services against their networks — not "no DHCP service outlives its
// interface", the true-but-narrower sentence #342 caught reassuring the
// operator past a held block.
func TestDoctorGreenLineClaimsTheNetworkQuestion(t *testing.T) {
	swapLeftoverSeams(t,
		func() ([]machine.DHCPLeftover, error) { return nil, nil },
		func(machine.DHCPLeftover) error { t.Fatal("a diagnostic signalled a process"); return nil })

	c := checkLeftoverDHCP()
	if c.state != verdictOK {
		t.Fatalf("a healthy host did not get an ok: %+v", c)
	}
	if !strings.Contains(c.title, "network") {
		t.Fatalf("the green line still speaks of interfaces, not networks: %s", c.title)
	}
	if strings.Contains(c.title, "outlives its interface") {
		t.Fatalf("the green line still carries the narrower claim: %s", c.title)
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

// TestCleanSaysWhatItWillNotTouchWhenTheBridgeSurvived holds #342's third
// half: the sweep ends the service it attributed, and names the bridge it
// refuses to delete — the surviving interface carries no label, so nothing
// proves the emulator created it, and a bridge nobody here created is not
// ours to remove.
func TestCleanSaysWhatItWillNotTouchWhenTheBridgeSurvived(t *testing.T) {
	survivor := machine.DHCPLeftover{
		PID:            612421,
		Interface:      "fnt-99109f524b2",
		Addresses:      []string{"10.50.2.1"},
		InterfaceAlive: true,
	}
	var ended []machine.DHCPLeftover
	swapLeftoverSeams(t,
		func() ([]machine.DHCPLeftover, error) { return []machine.DHCPLeftover{survivor}, nil },
		func(l machine.DHCPLeftover) error { ended = append(ended, l); return nil })

	var out bytes.Buffer
	if err := sweepLeftoverDHCP(&out); err != nil {
		t.Fatalf("a swept leftover still failed the sweep: %v", err)
	}
	if len(ended) != 1 || ended[0].PID != survivor.PID {
		t.Fatalf("the service was not ended: %v", ended)
	}
	report := out.String()
	if !strings.Contains(report, "left untouched") || !strings.Contains(report, "fnt-99109f524b2") {
		t.Fatalf("the sweep does not say what it will not touch:\n%s", report)
	}
	if !strings.Contains(report, "ip link delete fnt-99109f524b2") {
		t.Fatalf("the report does not carry the exact command for the operator:\n%s", report)
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
