package machine

import (
	"context"
	"strings"
	"testing"
)

// hostSaying builds an Incus driver whose CLI answers are dictated, so a test
// can stand on a host it does not have. The runner is the same seam the
// argument-level tests use; here it stands in for the host's answers rather
// than recording the driver's questions.
func hostSaying(ovnNB, version string) *Incus {
	d := NewIncusOVN()
	d.runner = func(_ context.Context, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "config get "+ovnNorthbound):
			return []byte(ovnNB + "\n"), nil
		case joined == "query /1.0":
			return []byte(`{"environment": {"server_version": "` + version + `"}}`), nil
		}
		return nil, nil
	}
	return d
}

// TestVerifyNarrowsWhatTheHostCannotDeliver is the refusing half, and the defect
// #181 was filed for.
//
// `NewIncusOVN` set OVN and `isolation: true` followed, whatever the host could
// do. There was one `Available` for all three Incus modes and it ran
// `incus list`, so the fall-through `--vm auto` was documented to rely on could
// never trigger. Measured on 2026-08-15 on an Incus 7.2 host with no OVN wiring:
// auto chose incus-ovn and /_feint/health published isolation until the first
// network creation failed, blaming the address block.
func TestVerifyNarrowsWhatTheHostCannotDeliver(t *testing.T) {
	// A host with Incus and no OVN wiring at all: the ordinary one.
	d := hostSaying("", "7.2")
	caps, unmet := d.Verify(context.Background())

	if caps.Isolation {
		t.Error("isolation survived a host with no northbound connection: this is " +
			"the capability a suite keying on it would assert and the host cannot deliver")
	}
	if len(unmet) != 1 || !strings.Contains(unmet[0], ovnNorthbound) {
		t.Errorf("the narrowing must name what was checked and what answered, got %v", unmet)
	}
	// And what the host does deliver is kept: a probe that refused everything
	// would pass this test's first half and break the product.
	if !caps.Machines || !caps.Addresses || !caps.Firewall {
		t.Errorf("verification removed capabilities the host answers for: %+v", caps)
	}
	// Capabilities publishes the verified set from now on, which is the point:
	// the flag's promise is not what /_feint/health carries.
	if d.Capabilities().Isolation {
		t.Error("Capabilities still publishes the flag's promise after Verify narrowed it")
	}
}

// The accepting half, on the same seam: a host that delivers keeps everything.
func TestVerifyKeepsWhatTheHostDelivers(t *testing.T) {
	d := hostSaying("tcp:10.0.0.1:6641", "7.2")
	caps, unmet := d.Verify(context.Background())

	if len(unmet) != 0 {
		t.Errorf("a wired host narrows nothing, got %v", unmet)
	}
	if !caps.Isolation {
		t.Error("isolation was removed on a host whose northbound is wired: the probe " +
			"refuses what it should accept, which breaks the mode it exists to protect")
	}
}

// The firewall floor is the daemon's, and it is the same constant the
// diagnostics cite. 6.0.0 is not a hypothetical: it is what Ubuntu 24.04 ships
// and will not move past, and on it `security.acls` on a NIC is refused, so a
// security group attaches and enforces nothing.
func TestVerifyRefusesTheFirewallBelowTheFloor(t *testing.T) {
	d := hostSaying("tcp:10.0.0.1:6641", "6.0.0")
	caps, unmet := d.Verify(context.Background())

	if caps.Firewall {
		t.Error("firewall survived a daemon below the floor: a group is created, " +
			"attached, and enforces nothing, while health says it is delivered")
	}
	if len(unmet) != 1 || !strings.Contains(unmet[0], VersionText(IncusMinimum[:])) {
		t.Errorf("the narrowing must cite the floor it applied, got %v", unmet)
	}
	// The floor itself, on the boundary rather than near it.
	if exact := hostSaying("tcp:10.0.0.1:6641", VersionText(IncusMinimum[:])); !mustVerify(t, exact).Firewall {
		t.Error("the floor version itself was refused: 6.0.4 runs everything this emulator asks for")
	}
}

// An unreadable version keeps the capability rather than losing it.
//
// A daemon that answers `incus list` and whose version this cannot parse is far
// more likely to be a format change than an old release. Refusing there would
// turn a diagnostic gap into a lost capability, and the operator would have no
// way to tell the two apart.
func TestAnUnreadableVersionDoesNotCostTheCapability(t *testing.T) {
	d := hostSaying("tcp:10.0.0.1:6641", "not a version")
	caps, unmet := d.Verify(context.Background())

	if !caps.Firewall {
		t.Error("an unparseable version cost the firewall capability")
	}
	if len(unmet) != 0 {
		t.Errorf("nothing was disproven, so nothing must be narrowed, got %v", unmet)
	}
}

func mustVerify(t *testing.T, d *Incus) Capabilities {
	t.Helper()
	caps, _ := d.Verify(context.Background())
	return caps
}
