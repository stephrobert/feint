package machine

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
)

// Every command line below except the synthetic zsh one was measured on the
// station #316 was filed from, on 2026-08-19, by reading /proc/*/cmdline while
// libvirt, two Incus projects and a feint run were all alive. The population a
// sweep must refuse is not hypothetical; it is what this host runs every day.

// incusDNSMasq renders the argv Incus launches for a managed bridge, the shape
// measured on pid 4026429 of that survey.
func incusDNSMasq(iface, gateway string) []string {
	return []string{
		"dnsmasq", "--keep-in-foreground", "--strict-order", "--bind-interfaces",
		"--except-interface=lo", "--pid-file=", "--no-ping",
		"--interface=" + iface,
		"--dhcp-rapid-commit", "--no-negcache", "--quiet-dhcp",
		"--listen-address=" + gateway,
		"--dhcp-no-override", "--dhcp-authoritative",
		"--dhcp-leasefile=/var/lib/incus/networks/" + iface + "/dnsmasq.leases",
		"-s", "incus", "--interface-name", "_gateway.incus," + iface,
		"-u", "incus", "-g", "incus",
	}
}

// libvirtDNSMasq is libvirt's shape: the configuration lives in a file, and
// nothing on argv names an interface.
var libvirtDNSMasq = []string{
	"/usr/sbin/dnsmasq", "--conf-file=/var/lib/libvirt/dnsmasq/default.conf",
	"--leasefile-ro", "--dhcp-script=/usr/lib/libvirt/libvirt_leaseshelper",
}

// runtimeKnows is the conservative default for the network question: the
// runtime was not asked, or answered that it still manages the name. Under it
// only a vanished interface can make a leftover, which is #316's original
// classification.
func runtimeKnows(string) bool { return false }

// runtimeForgot answers that no project of the runtime has a network under
// the name: the missing half of #342.
func runtimeForgot(string) bool { return true }

func TestLeftoverDHCPFindsTheServiceThatOutlivedItsInterface(t *testing.T) {
	// The exact condition measured in #316: the interface is gone, the
	// process is not, and only the process knows which block it still holds.
	procs := []HostProcess{
		{PID: 4071323, Argv: incusDNSMasq("fnt-99109f524b2", "10.50.2.1")},
	}
	everythingGone := func(string) bool { return true }

	leftovers := leftoverDHCP(procs, everythingGone, runtimeKnows)
	if len(leftovers) != 1 {
		t.Fatalf("expected one leftover, got %v", leftovers)
	}
	got := leftovers[0]
	if got.PID != 4071323 || got.Interface != "fnt-99109f524b2" {
		t.Errorf("wrong attribution: %+v", got)
	}
	// The report must name the address, because the address is what the next
	// run fails on and what `ss -lnp` was the only tool to show.
	if len(got.Addresses) != 1 || got.Addresses[0] != "10.50.2.1" {
		t.Errorf("the leftover does not carry the address it holds: %+v", got)
	}
	if !strings.Contains(got.String(), "10.50.2.1") || !strings.Contains(got.String(), "fnt-99109f524b2") {
		t.Errorf("the report names neither the address nor the interface: %s", got)
	}
}

// TestLeftoverDHCPRefusesAProcessItCannotAttribute is the guard #316 exists
// for, and the target of the falsification spec dhcp-leftover-ownership.json:
// without the ownedNetwork question in classifyDHCP, the foreign services
// below are claimed and a sweep would signal them.
func TestLeftoverDHCPRefusesAProcessItCannotAttribute(t *testing.T) {
	procs := []HostProcess{
		// libvirt's dnsmasq: nothing on argv says whose it is.
		{PID: 2308, Argv: libvirtDNSMasq},
		// Incus's own default bridge, and a neighbouring project's network:
		// both dnsmasq by name and --interface by shape, neither ours. gone()
		// answers true for them below, which is exactly the recycled-host
		// case where prefix is the only thing left to tell them apart.
		{PID: 3187, Argv: incusDNSMasq("incusbr0", "10.76.154.1")},
		{PID: 3181, Argv: incusDNSMasq("hp-test-net", "10.191.0.1")},
		// The operator's lab, the one the issue names as never to be touched.
		{PID: 2214, Argv: incusDNSMasq("virbr40", "10.10.40.1")},
		// Not a dnsmasq at all, whatever its arguments say.
		{PID: 4032559, Argv: []string{"/usr/bin/zsh", "-c", "echo --interface=fnt-decoy0000"}},
		// A dnsmasq that only mentions an fnt- name in a flag this parser does
		// not read: the description of an interface is not the interface.
		{PID: 4032560, Argv: []string{"dnsmasq", "--interface-name", "_gateway.incus,fnt-decoy0000"}},
	}
	everythingGone := func(string) bool { return true }

	if leftovers := leftoverDHCP(procs, everythingGone, runtimeForgot); len(leftovers) != 0 {
		t.Fatalf("a foreign process was attributed to the emulator: %v", leftovers)
	}
}

// A live service is not a leftover, whoever owns it: while the regeneration
// that motivated #316 was running, this station carried two dnsmasq on live
// fnt- interfaces, and a sweep that claimed those would kill the run it was
// meant to protect.
func TestLeftoverDHCPLeavesALiveServiceAlone(t *testing.T) {
	procs := []HostProcess{
		{PID: 4026429, Argv: incusDNSMasq("fnt-ae6e2020dcf", "10.182.0.1")},
		{PID: 4031763, Argv: incusDNSMasq("fnt-default", "10.209.84.1")},
	}
	alive := func(string) bool { return false }

	if leftovers := leftoverDHCP(procs, alive, runtimeKnows); len(leftovers) != 0 {
		t.Fatalf("a live service was reported as a leftover: %v", leftovers)
	}
}

// A service straddling interfaces is attributable only when every one of them
// is ours and gone; one live or foreign interface in the set makes it
// somebody's working arrangement.
func TestLeftoverDHCPRefusesAMixedInterfaceSet(t *testing.T) {
	argv := append(incusDNSMasq("fnt-99109f524b2", "10.50.2.1"), "--interface=incusbr0")
	procs := []HostProcess{{PID: 99, Argv: argv}}

	if leftovers := leftoverDHCP(procs, func(string) bool { return true }, runtimeKnows); len(leftovers) != 0 {
		t.Fatalf("a mixed interface set was attributed to the emulator: %v", leftovers)
	}
}

// TestTerminateLeftoverRefusesAPidThatIsNoLongerTheLeftover holds the re-check
// at the moment of the signal: pids are reused, and the window between scan
// and signal is exactly where a recycled pid would turn the sweep into a shot
// at an innocent process. The assertion is on the signals, not on the error:
// what matters is that no signal reaches a process that is not the leftover.
func TestTerminateLeftoverRefusesAPidThatIsNoLongerTheLeftover(t *testing.T) {
	leftover := DHCPLeftover{PID: 4071323, Interface: "fnt-99109f524b2", Addresses: []string{"10.50.2.1"}}

	cases := map[string]struct {
		argv []string
		err  error
		gone bool
	}{
		"the pid was recycled by a foreign process": {argv: []string{"postgres"}},
		"the pid was recycled by somebody's dnsmasq": {
			argv: incusDNSMasq("virbr40", "10.10.40.1"),
		},
		"the interface came back to life": {
			argv: incusDNSMasq("fnt-99109f524b2", "10.50.2.1"), gone: false,
		},
		"the pid now serves another of our networks": {
			argv: incusDNSMasq("fnt-ae6e2020dcf", "10.182.0.1"), gone: true,
		},
	}
	for name, tc := range cases {
		var signalled []int
		err := terminateLeftover(leftover,
			func(int) ([]string, error) { return tc.argv, tc.err },
			func(string) bool { return tc.gone },
			runtimeKnows,
			func(pid int) error { signalled = append(signalled, pid); return nil })
		if len(signalled) != 0 {
			t.Errorf("%s: a signal was sent anyway, to %v", name, signalled)
		}
		if err == nil {
			t.Errorf("%s: the refusal was silent", name)
		}
	}
}

// A process already gone is the goal state, not a failure: the sweep must
// report it as done rather than send the operator chasing a pid that no
// longer exists.
func TestTerminateLeftoverTreatsAGoneProcessAsDone(t *testing.T) {
	var signalled []int
	err := terminateLeftover(DHCPLeftover{PID: 1, Interface: "fnt-x"},
		func(int) ([]string, error) { return nil, errors.New("no such process") },
		func(string) bool { return true },
		runtimeKnows,
		func(pid int) error { signalled = append(signalled, pid); return nil })
	if err != nil || len(signalled) != 0 {
		t.Fatalf("a vanished process was not treated as done: err=%v signals=%v", err, signalled)
	}
}

// And the accepting half: a guard that refused every termination would pass
// every test above and leave the leftover holding its block forever.
func TestTerminateLeftoverEndsTheLeftoverItWasGiven(t *testing.T) {
	leftover := DHCPLeftover{PID: 4071323, Interface: "fnt-99109f524b2"}
	var signalled []int
	err := terminateLeftover(leftover,
		func(int) ([]string, error) { return incusDNSMasq("fnt-99109f524b2", "10.50.2.1"), nil },
		func(string) bool { return true },
		runtimeKnows,
		func(pid int) error { signalled = append(signalled, pid); return nil })
	if err != nil {
		t.Fatalf("the leftover was refused: %v", err)
	}
	if len(signalled) != 1 || signalled[0] != 4071323 {
		t.Fatalf("the signal did not reach the leftover: %v", signalled)
	}
}

// The minute-eight failure, as the operator reads it: the create fails on a
// clean-looking host, and the error must name the process that holds the
// block, because ss -lnp being the third place to look is what #316 cost.
func TestANetworkCreateFailureNamesTheLeftoverHoldingTheBlock(t *testing.T) {
	f := &fakeRuntime{fail: map[string]error{
		"query /1.0/networks/fnt-99109f524b2": errors.New("Network not found"),
		"network create": errors.New(`The DNS and DHCP service exited prematurely: exit status 2 ` +
			`("dnsmasq: failed to create listening socket for 10.50.2.1: Address already in use")`),
	}}
	d := newFakeDriver(f)
	d.leftoverScan = func() []DHCPLeftover {
		return []DHCPLeftover{{PID: 4071323, Interface: "fnt-a1b2c3d4e5f", Addresses: []string{"10.50.2.1"}}}
	}

	err := d.EnsureNetwork(t.Context(), NetworkSpec{
		Name: "fnt-99109f524b2", CIDR: "10.50.2.0/24", Gateway: "10.50.2.1",
	})
	if err == nil {
		t.Fatal("the failed create reported success")
	}
	for _, fact := range []string{"4071323", "fnt-a1b2c3d4e5f", "10.50.2.1", "feint clean"} {
		if !strings.Contains(err.Error(), fact) {
			t.Errorf("the error does not carry %q:\n%v", fact, err)
		}
	}
}

// And the quiet half: a leftover holding some other block is not the cause of
// this failure, and naming it would send the operator to kill a process that
// has nothing to do with their error.
func TestANetworkCreateFailureStaysQuietAboutAnUnrelatedLeftover(t *testing.T) {
	f := &fakeRuntime{fail: map[string]error{
		"query /1.0/networks/fnt-99109f524b2": errors.New("Network not found"),
		"network create":                      errors.New("Address already in use"),
	}}
	d := newFakeDriver(f)
	d.leftoverScan = func() []DHCPLeftover {
		return []DHCPLeftover{{PID: 7777, Interface: "fnt-a1b2c3d4e5f", Addresses: []string{"10.99.0.1"}}}
	}

	err := d.EnsureNetwork(t.Context(), NetworkSpec{
		Name: "fnt-99109f524b2", CIDR: "10.50.2.0/24", Gateway: "10.50.2.1",
	})
	if err == nil {
		t.Fatal("the failed create reported success")
	}
	if strings.Contains(err.Error(), "7777") || strings.Contains(err.Error(), "fnt-a1b2c3d4e5f") {
		t.Fatalf("the error names a process that does not hold this block:\n%v", err)
	}
}

// TestLeftoverDHCPFindsTheServiceWhoseNetworkIsGone is #342's exact state,
// measured on 2026-08-18: pid 612421 held 10.50.2.1 on a bridge that had
// survived alongside its dnsmasq, both outliving the network object — and the
// interface-only question answered "nothing outlives its interface" while the
// next conformance run died on the bind. The falsification spec
// dhcp-leftover-ownership.json proves this test fails when the network half
// of the question is removed.
func TestLeftoverDHCPFindsTheServiceWhoseNetworkIsGone(t *testing.T) {
	procs := []HostProcess{
		{PID: 612421, Argv: incusDNSMasq("fnt-99109f524b2", "10.50.2.1")},
	}
	interfaceStillThere := func(string) bool { return false }

	leftovers := leftoverDHCP(procs, interfaceStillThere, runtimeForgot)
	if len(leftovers) != 1 {
		t.Fatalf("a service whose network is gone was not classified: %v", leftovers)
	}
	got := leftovers[0]
	if got.PID != 612421 || got.Interface != "fnt-99109f524b2" || !got.InterfaceAlive {
		t.Errorf("wrong classification: %+v", got)
	}
	// The report carries the two facts needed to act: the block (through its
	// held address) and the pid.
	for _, fact := range []string{"612421", "10.50.2.1", "fnt-99109f524b2"} {
		if !strings.Contains(got.String(), fact) {
			t.Errorf("the report does not carry %q: %s", fact, got)
		}
	}
}

// A live interface with a live network is nobody's leftover — and so is a
// dnsmasq that merely borrowed an fnt- name without running off the runtime's
// state directory. The live-interface case demands the strongest attribution
// precisely because the wrong claim signals a working service.
func TestLeftoverDHCPDemandsTheRuntimesOwnPathWhenTheInterfaceSurvives(t *testing.T) {
	handRolled := []string{
		"dnsmasq", "--interface=fnt-99109f524b2", "--listen-address=10.50.2.1",
		"--conf-file=/home/operator/lab/dnsmasq.conf",
	}
	procs := []HostProcess{{PID: 4242, Argv: handRolled}}
	interfaceStillThere := func(string) bool { return false }

	if leftovers := leftoverDHCP(procs, interfaceStillThere, runtimeForgot); len(leftovers) != 0 {
		t.Fatalf("a service that does not run off the runtime's state directory was claimed: %v", leftovers)
	}
}

// DHCPHolders answers the other half of #342's question — "of ours or
// otherwise" — at the one moment the wanted block is known: a create just
// failed on it. Attribution is deliberately absent from the question, which is
// why the answer must only ever name, never touch.
func TestDHCPHoldersNamesAnyServiceInsideTheBlock(t *testing.T) {
	procs := []HostProcess{
		// The operator's own Incus bridge: never a leftover, but it does hold
		// its block, and a create that collides with it deserves the fact.
		{PID: 3187, Argv: incusDNSMasq("incusbr0", "10.76.154.1")},
		// libvirt: no listen address on argv, so nothing places it in a block.
		{PID: 2308, Argv: libvirtDNSMasq},
		// Not a dnsmasq: not a DHCP service, whatever it listens on.
		{PID: 77, Argv: []string{"nginx", "--listen-address=10.76.154.9"}},
	}

	holders := dhcpHolders(procs, netip.MustParsePrefix("10.76.154.0/24"))
	if len(holders) != 1 || holders[0].PID != 3187 || holders[0].Address != "10.76.154.1" {
		t.Fatalf("the holder of the block was not the one named: %v", holders)
	}
	for _, fact := range []string{"3187", "10.76.154.1", "dnsmasq"} {
		if !strings.Contains(holders[0].String(), fact) {
			t.Errorf("the report does not carry %q: %s", fact, holders[0])
		}
	}

	if holders := dhcpHolders(procs, netip.MustParsePrefix("10.50.0.0/16")); len(holders) != 0 {
		t.Fatalf("a service outside the block was named: %v", holders)
	}
}
