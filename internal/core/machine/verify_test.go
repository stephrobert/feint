package machine

import (
	"context"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"
)

// socketInfo is a FileInfo whose only interesting answer is that it is a socket.
type socketInfo struct{}

func (socketInfo) Name() string       { return "ovnnb_db.sock" }
func (socketInfo) Size() int64        { return 0 }
func (socketInfo) Mode() os.FileMode  { return os.ModeSocket | 0o750 }
func (socketInfo) ModTime() time.Time { return time.Time{} }
func (socketInfo) IsDir() bool        { return false }
func (socketInfo) Sys() any           { return nil }

// hostSaying builds an Incus driver whose CLI answers are dictated, so a test
// can stand on a host it does not have. The runner is the same seam the
// argument-level tests use; here it stands in for the host's answers rather
// than recording the driver's questions.
func hostSaying(ovnNB, version string) *Incus {
	// The northbound socket exists unless a test says otherwise. That is the
	// ordinary host: ovn-central running, nothing configured, because the
	// setting is already at its default.
	return hostSayingWithSocket(ovnNB, version, true)
}

// hostSayingWithSocket adds the half hostSaying assumes: whether the northbound
// socket the connection string names is actually there. Existence is what the
// driver checks and it is checked here, rather than a connection, because the
// socket is root-owned and this process is not incusd.
func hostSayingWithSocket(ovnNB, version string, socketExists bool) *Incus {
	d := NewIncusOVN()
	d.statPath = func(string) (os.FileInfo, error) {
		if !socketExists {
			return nil, fs.ErrNotExist
		}
		return socketInfo{}, nil
	}
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
	// A host with Incus and no OVN at all: nothing configured, and no northbound
	// socket either. Both halves matter, and the second is the whole correction —
	// an unset key alone is the *default* applying, and this check read it as
	// absence until it refused a station where OVN was running.
	d := hostSayingWithSocket("", "7.2", false)
	caps, unmet := d.Verify(context.Background())

	if caps.Isolation {
		t.Error("isolation survived a host with no northbound connection: this is " +
			"the capability a suite keying on it would assert and the host cannot deliver")
	}
	// Two lines now, one per OVN-borne capability, and each must name what was
	// checked: a narrowing that says "isolation: unavailable" sends the reader
	// nowhere.
	if len(unmet) != 2 {
		t.Errorf("a host with no OVN must narrow both OVN capabilities, got %v", unmet)
	}
	for _, line := range unmet {
		if !strings.Contains(line, ovnNorthbound) {
			t.Errorf("the narrowing must name what was checked and what answered, got %q", line)
		}
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

// An unset northbound is the default applying, never an absence.
//
// This is the test that was missing, and its absence is why the guard shipped
// wrong: `incus config get` answers empty for a key at its documented default,
// and the default is `unix:/run/ovn/ovnnb_db.sock`. So the ordinary healthy
// host — ovn-central running, nothing configured, because nothing needs to be —
// answered empty and had its isolation taken away.
//
// Measured on this project's station on 2026-08-15: openvswitch-switch,
// ovn-central and ovn-host all active, `ovn-nbctl show` answering on the
// socket, and `feint doctor --vm incus-ovn` refusing the mode with
// "network.ovn.northbound_connection is unset, so no OVN network can be
// created". Setting the key by hand changed nothing, because Incus does not
// store a value equal to the default — which is the observation that found the
// bug.
//
// Both halves, because a probe that accepted everything would pass the first.
func TestAnUnsetNorthboundIsTheDefaultAndNotAnAbsence(t *testing.T) {
	// Nothing configured, socket there: the ordinary OVN host.
	caps, unmet := mustVerifyBoth(t, hostSayingWithSocket("", "7.2", true))
	if !caps.Isolation {
		t.Errorf("a host with the northbound at its default lost isolation: %v", unmet)
	}

	// Nothing configured, socket absent: OVN is genuinely not there.
	caps, unmet = mustVerifyBoth(t, hostSayingWithSocket("", "7.2", false))
	if caps.Isolation {
		t.Error("isolation survived a host with no northbound socket at all")
	}
	if len(unmet) != 2 {
		t.Errorf("a host with no northbound socket must narrow both OVN capabilities, got %v", unmet)
	}
	for _, line := range unmet {
		if !strings.Contains(line, ovnDefaultNorthbound) {
			t.Errorf("the refusal must name the socket it looked for, got %q", line)
		}
	}

	// A remote northbound this process cannot reach: unknown is not a refusal,
	// the same policy an unreadable version gets. incusd connects to it, not us.
	if caps, _ := mustVerifyBoth(t, hostSayingWithSocket("tcp:10.0.0.1:6641", "7.2", false)); !caps.Isolation {
		t.Error("a tcp northbound was refused on a filesystem check that cannot apply to it")
	}
}

func mustVerifyBoth(t *testing.T, d *Incus) (Capabilities, []string) {
	t.Helper()
	return d.Verify(context.Background())
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

// Balancing is narrowed with isolation, and by the same probe.
//
// Both are OVN's, and both are false on a host with no northbound connection.
// The reason to assert it rather than trust the shape: a capability added later
// gets its declaration and forgets its verification, and the result is exactly
// what #181 measured for isolation — /_feint/health publishing a claim the host
// cannot deliver, to a suite this project tells to key on capabilities rather
// than on a mode name.
//
// The accepting half is here too, because a probe that removed balancing from
// every host would satisfy the first half and take the feature away.
func TestVerifyNarrowsBalancingWithIsolation(t *testing.T) {
	// No OVN at all: nothing configured, no socket.
	caps, unmet := mustVerifyBoth(t, hostSayingWithSocket("", "7.2", false))
	if caps.Balancing {
		t.Error("balancing survived a host with no northbound connection")
	}
	if caps.Isolation {
		t.Error("isolation survived a host with no northbound connection")
	}
	named := false
	for _, line := range unmet {
		if strings.HasPrefix(line, "balancing:") {
			named = true
		}
	}
	if !named {
		t.Errorf("the narrowing never mentions balancing, so nothing tells the operator it went: %v", unmet)
	}

	// A wired host keeps it.
	if caps, unmet := mustVerifyBoth(t, hostSaying("tcp:10.0.0.1:6641", "7.2")); !caps.Balancing {
		t.Errorf("a wired host lost balancing: %v", unmet)
	}
}

func mustVerify(t *testing.T, d *Incus) Capabilities {
	t.Helper()
	caps, _ := d.Verify(context.Background())
	return caps
}
