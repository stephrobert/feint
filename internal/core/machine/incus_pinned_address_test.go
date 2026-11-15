package machine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Attachment.Address was the only address in Spec that reached a command
// without being parsed (#944). It comes from the same restored
// Resource.Runtime as the network names #909 and #932 are about.
//
// At `ipv4.address=<it>` a leading dash cannot open a flag, but the value
// round-trips: restorePinnedAddress reads it back off the device, and both it
// and Attach build `<it>/<len>` for `exec … -- ip address add`, an argument
// of its own. What the guest's `ip` would do with a crafted one is not
// measured here and not relied on: the check refuses anything that is not an
// IPv4 address before a command is sent. The public half of the same Spec was
// parsed (routedDevice, routedNetworkConfig); this half relied on the Incus
// daemon refusing the pin first.
//
// Driven through the injectable runner. No Incus daemon is involved and no
// machine is started.

// pinTaints are the crafted pins, each on an owned, well-formed network and an
// owned machine, so the address is the only question any of them fails.
//
// The last row is the one that isolates the IPv4 half: it is an address
// netip.ParseAddr accepts, so a guard that only asked "does it parse" lets it
// through, and its zone is text a client chose.
var pinTaints = []string{
	"-foo",            // starts like an option, and is no address
	"1.2.3.4 --force", // an address followed by a flag
	" ",               // present, and empty of anything
	"fe80::1%--force", // parses, and is not IPv4
}

// carriesPin reports whether any emitted argv element carries the pin in a
// position where it would reach a command: as the device key's value, or as
// the start of the guest's `ip address add` argument.
func carriesPin(f *fakeRuntime, pin string) bool {
	for _, call := range f.calls {
		for _, arg := range call {
			if arg == "ipv4.address="+pin || strings.HasPrefix(arg, pin+"/") || arg == pin {
				return true
			}
		}
	}
	return false
}

// TestAPinnedAddressThatIsNotAnAddressReachesNoCommand drives each crafted pin
// through both doors that plug a NIC with an address: Start (on the primary
// interface and on an extra one) and Attach. The refusal is at each door's
// entry, so the assertion is that nothing at all was sent, and in particular
// that no argv carries the pin.
func TestAPinnedAddressThatIsNotAnAddressReachesNoCommand(t *testing.T) {
	owned := NetworkName(NetworkPrefix, "11112222-3333-4444-5555-666677778888")
	second := NetworkName(NetworkPrefix, "99990000-aaaa-bbbb-cccc-ddddeeeeffff")

	for _, pin := range pinTaints {
		t.Run("Start primary "+pin, func(t *testing.T) {
			f := &fakeRuntime{}
			startScript(f)
			d := newFakeDriver(f)
			_, err := d.Start(context.Background(), Spec{Name: "srv", Image: "alpine:3.21",
				Attachments: []Attachment{{Network: owned, Address: pin}}})
			if err == nil {
				t.Fatalf("Start accepted the pin %q", pin)
			}
			if carriesPin(f, pin) || len(f.calls) != 0 {
				t.Fatalf("the pin %q was refused, so nothing should have been sent: %v", pin, f.commands())
			}
		})
		t.Run("Start extra "+pin, func(t *testing.T) {
			f := &fakeRuntime{}
			startScript(f)
			d := newFakeDriver(f)
			_, err := d.Start(context.Background(), Spec{Name: "srv", Image: "alpine:3.21",
				Attachments: []Attachment{{Network: owned}, {Network: second, Address: pin}}})
			if err == nil {
				t.Fatalf("Start accepted the pin %q on an extra NIC", pin)
			}
			if carriesPin(f, pin) || len(f.calls) != 0 {
				t.Fatalf("the pin %q was refused, so nothing should have been sent: %v", pin, f.commands())
			}
		})
		t.Run("Attach "+pin, func(t *testing.T) {
			f := attachRuntime("feint-scw-mine")
			d := newFakeDriver(f)
			err := d.Attach(context.Background(), "feint-scw-mine",
				Attachment{Network: owned, Address: pin, PrefixLen: 24})
			if err == nil {
				t.Fatalf("Attach accepted the pin %q", pin)
			}
			if carriesPin(f, pin) || len(f.calls) != 0 {
				t.Fatalf("the pin %q was refused, so nothing should have been sent: %v", pin, f.commands())
			}
		})
	}
}

// TestAPinnedAddressStillReachesTheDeviceAndTheGuest is the accepting half: a
// guard that refused every pin would pass the test above and leave every
// private NIC to DHCP, which on OVN never answers some images (see
// incus_firstboot_test.go).
//
// The legitimate pin reaches both places it is meant to: the device key
// `ipv4.address=`, on Start's primary interface, on an extra NIC and on a hot
// Attach; and the guest's `ip address add`, on Attach and on an OVN first boot,
// where it is read back off the device.
func TestAPinnedAddressStillReachesTheDeviceAndTheGuest(t *testing.T) {
	owned := NetworkName(NetworkPrefix, "11112222-3333-4444-5555-666677778888")
	second := NetworkName(NetworkPrefix, "99990000-aaaa-bbbb-cccc-ddddeeeeffff")

	t.Run("Start, on the primary interface and an extra one", func(t *testing.T) {
		f := &fakeRuntime{}
		startScript(f)
		d := newFakeDriver(f)
		d.routePoll = time.Millisecond
		d.routeBudget = 5 * time.Millisecond
		if _, err := d.Start(context.Background(), Spec{Name: "srv", Image: "alpine:3.21",
			Attachments: []Attachment{{Network: owned, Address: "10.0.0.4"}, {Network: second, Address: "10.0.1.7"}}}); err != nil {
			t.Fatalf("Start refused two legitimate pins: %v", err)
		}
		if len(f.matching("config device set srv eth0 ipv4.address=10.0.0.4")) != 1 {
			t.Fatalf("the primary pin did not reach its device: %v", f.commands())
		}
		if len(f.matching("config device add srv eth1 nic network="+second+" ipv4.address=10.0.1.7")) != 1 {
			t.Fatalf("the extra NIC's pin did not reach its device: %v", f.commands())
		}
	})

	t.Run("Start, an OVN first boot reads it back into the guest", func(t *testing.T) {
		f := &fakeRuntime{}
		firstBootScript(f, firstBootPinned)
		d := ovnDriver(f)
		if _, err := d.Start(context.Background(), Spec{Name: "srv", Image: "alpine:3.21",
			Attachments: []Attachment{{Network: "fnt-368798629f8", Address: "10.181.0.2"}}}); err != nil {
			t.Fatalf("Start refused a legitimate pin: %v", err)
		}
		if len(f.matching("ipv4.address=10.181.0.2")) == 0 {
			t.Fatalf("the pin did not reach its device: %v", f.commands())
		}
		if len(f.matching("exec srv -- ip address add 10.181.0.2/24 dev eth0")) == 0 {
			t.Fatalf("the pin did not reach the guest: %v", f.commands())
		}
	})

	t.Run("Attach, on the device and in the guest", func(t *testing.T) {
		f := attachRuntime("feint-scw-mine")
		d := newFakeDriver(f)
		if err := d.Attach(context.Background(), "feint-scw-mine",
			Attachment{Network: owned, Address: "10.0.0.4", PrefixLen: 24}); err != nil {
			t.Fatalf("Attach refused a legitimate pin: %v", err)
		}
		if got := f.matching("config device add feint-scw-mine eth"); len(got) != 1 ||
			!strings.HasSuffix(got[0], "network="+owned+" ipv4.address=10.0.0.4") {
			t.Fatalf("the pin did not reach the device: %v", f.commands())
		}
		if len(f.matching("exec feint-scw-mine -- ip address add 10.0.0.4/24 dev")) != 1 {
			t.Fatalf("the pin did not reach the guest: %v", f.commands())
		}
	})
}

// TestASecondaryAddressThatIsNotAnAddressReachesNoCommand is the same question
// asked of Attachment.Secondary, the addresses Outscale calls secondary
// private IPs. They reach the guest's `ip address add <it>/<len>` through
// reconcileSecondary on the hot Attach, and they are restored with the rest of
// the state without passing through the API that may or may not have parsed
// them, so the driver asks.
//
// The primary pin on every row is legitimate, so the secondary is the only
// value any row puts in question. Start does not emit secondaries, and is
// driven anyway: it asks the same predicate at its entry, so a boot whose plan
// carries one it cannot trust sends nothing.
func TestASecondaryAddressThatIsNotAnAddressReachesNoCommand(t *testing.T) {
	owned := NetworkName(NetworkPrefix, "11112222-3333-4444-5555-666677778888")
	for _, pin := range pinTaints {
		att := Attachment{Network: owned, Address: "10.0.0.4", PrefixLen: 24,
			Secondary: []string{"10.0.0.9", pin}}
		t.Run("Attach "+pin, func(t *testing.T) {
			f := attachRuntime("feint-scw-mine")
			d := newFakeDriver(f)
			if err := d.Attach(context.Background(), "feint-scw-mine", att); err == nil {
				t.Fatalf("Attach accepted the secondary address %q", pin)
			}
			if carriesPin(f, pin) || len(f.calls) != 0 {
				t.Fatalf("the secondary %q was refused, so nothing should have been sent: %v", pin, f.commands())
			}
		})
		t.Run("Start "+pin, func(t *testing.T) {
			f := &fakeRuntime{}
			startScript(f)
			d := newFakeDriver(f)
			if _, err := d.Start(context.Background(), Spec{Name: "srv", Image: "alpine:3.21",
				Attachments: []Attachment{att}}); err == nil {
				t.Fatalf("Start accepted a plan carrying the secondary address %q", pin)
			}
			if carriesPin(f, pin) || len(f.calls) != 0 {
				t.Fatalf("the secondary %q was refused, so nothing should have been sent: %v", pin, f.commands())
			}
		})
	}
}

// TestASecondaryAddressStillReachesTheGuest is the accepting half: a
// legitimate secondary is still configured inside the guest beside the
// primary.
func TestASecondaryAddressStillReachesTheGuest(t *testing.T) {
	owned := NetworkName(NetworkPrefix, "11112222-3333-4444-5555-666677778888")
	f := attachRuntime("feint-scw-mine")
	d := newFakeDriver(f)
	if err := d.Attach(context.Background(), "feint-scw-mine", Attachment{
		Network: owned, Address: "10.0.0.4", PrefixLen: 24, Secondary: []string{"10.0.0.9"},
	}); err != nil {
		t.Fatalf("Attach refused a legitimate secondary address: %v", err)
	}
	if len(f.matching("exec feint-scw-mine -- ip address add 10.0.0.9/24 dev")) != 1 {
		t.Fatalf("the secondary address did not reach the guest: %v", f.commands())
	}
}

// pinnedDevices is the device view of a restarted machine whose private NIC
// pins the given value, built by a marshaller so the value cannot break the
// fixture's own JSON.
func pinnedDevices(t *testing.T, pin string) string {
	t.Helper()
	nic := map[string]string{"type": "nic", "network": "fnt-368798629f8", "ipv4.address": pin}
	view := map[string]map[string]map[string]string{
		"devices":          {"eth1": nic},
		"expanded_devices": {"eth1": nic},
	}
	out, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestAPinnedAddressReadBackOffTheDeviceIsAskedAgain is the round trip of
// #944. restorePinnedAddress reads the pin back off the device on a restart
// and hands `<it>/<len>` to the guest's `ip address add`. A device's config is
// state the host holds: a pin written before the entry parse existed, or by
// anything other than this driver, comes back from there, so it is asked the
// same questions on the way out as on the way in.
//
// The spec carries no attachment, so the entry guard of Start has nothing to
// refuse: the only value in question is the one read back. Both modes, because
// the restart restores the pin under a bridge as under OVN.
func TestAPinnedAddressReadBackOffTheDeviceIsAskedAgain(t *testing.T) {
	for _, pin := range pinTaints {
		for _, ovn := range []bool{false, true} {
			f := &fakeRuntime{}
			restartedInstanceCarrying(f, pinnedDevices(t, pin), "")
			d := newFakeDriver(f)
			d.OVN = ovn
			d.routePoll = time.Millisecond
			d.routeBudget = 5 * time.Millisecond

			_, _ = d.Start(context.Background(), Spec{Name: "srv", Image: "ubuntu:22.04"})
			if carriesPin(f, pin) {
				t.Fatalf("ovn=%v: the pin %q read back off the device reached a command: %v", ovn, pin, f.matching("ip address"))
			}
		}
	}
}

// TestAnAddressTheGuestReportsIsParsedBeforeItIsRemoved holds the other end of
// the reconcile: what the guest says it carries. That output is written by the
// guest, whose root is whoever was given the machine, and reconcileSecondary
// hands every address it finds and no longer wants to `ip address del
// <it>/<len>`, a bare argv element.
//
// The guest reports one stale legitimate address beside each crafted line:
// the stale one must still be removed (the accepting half), and the crafted
// one must reach no command.
func TestAnAddressTheGuestReportsIsParsedBeforeItIsRemoved(t *testing.T) {
	owned := NetworkName(NetworkPrefix, "11112222-3333-4444-5555-666677778888")
	for _, crafted := range []string{"-foo", "fe80::1%--force"} {
		t.Run(crafted, func(t *testing.T) {
			f := attachRuntime("feint-scw-mine")
			answer := f.hook
			f.hook = func(call int, args []string) ([]byte, error, bool) {
				if strings.Contains(strings.Join(args, " "), "-o addr show dev") {
					return []byte("3: eth0    inet 10.0.0.8/24 scope global eth0\n" +
						"3: eth0    inet " + crafted + "/24 scope global eth0\n"), nil, true
				}
				return answer(call, args)
			}
			d := newFakeDriver(f)
			if err := d.Attach(context.Background(), "feint-scw-mine",
				Attachment{Network: owned, Address: "10.0.0.4", PrefixLen: 24}); err != nil {
				t.Fatalf("attach: %v", err)
			}
			if carriesPin(f, crafted) {
				t.Fatalf("an address the guest reported, %q, reached a command: %v", crafted, f.matching("ip address"))
			}
			if len(f.matching("ip address del 10.0.0.8/24 dev")) != 1 {
				t.Fatalf("the stale address the guest carries was not removed: %v", f.commands())
			}
		})
	}
}
