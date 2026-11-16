package cli

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
)

// stationOn is the bare station of #974 on another release: Incus answering,
// OVN and Open vSwitch absent.
func stationOn(osRelease string) station {
	s := bareStation()
	s.distro = parseOSRelease(osRelease)
	return s
}

// Debian 12 ships OVN 23.03.1 and Open vSwitch 3.1.0, below the 24.03.0 and
// 3.3.0 Incus 7.4 and later require (#979). Doctor must not print the command
// that installs that pairing. Ubuntu 22.04 is in the same case (OVN 22.03.8,
// Open vSwitch 2.17.12). One case per clause of oldOVNOf(): each refused
// release matches one clause and no other, and the accepted ones prove the
// refusal is not "every Debian" or "every Ubuntu".
func TestDebian12GetsNoOVNCommand(t *testing.T) {
	const debian12, jammy = "OVN 23.03.1 and Open vSwitch 3.1.0", "OVN 22.03.8 and Open vSwitch 2.17.12"
	cases := []struct {
		name, osRelease string
		refused         string // what the refusal says the release ships; "" means served
	}{
		{"Debian 12 by its VERSION_ID", "ID=debian\nVERSION_ID=\"12\"\n", debian12},
		{"a derivative naming bookworm as its codename", "ID=raspbian\nID_LIKE=debian\nVERSION_ID=\"12\"\nVERSION_CODENAME=bookworm\n", debian12},
		{"a derivative naming bookworm as its Debian base", "ID=linuxmint\nID_LIKE=debian\nVERSION_ID=\"6\"\nVERSION_CODENAME=faye\nDEBIAN_CODENAME=bookworm\n", debian12},
		{"Ubuntu 22.04 by its VERSION_ID", "ID=ubuntu\nID_LIKE=debian\nVERSION_ID=\"22.04\"\n", jammy},
		{"a derivative naming jammy as its Ubuntu base", "ID=linuxmint\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=\"21.3\"\nVERSION_CODENAME=virginia\nUBUNTU_CODENAME=jammy\n", jammy},
		{"Debian 13 is served", "ID=debian\nVERSION_ID=\"13\"\nVERSION_CODENAME=trixie\n", ""},
		{"Ubuntu 24.04 is served", "ID=ubuntu\nID_LIKE=debian\nVERSION_ID=\"24.04\"\nVERSION_CODENAME=noble\nUBUNTU_CODENAME=noble\n", ""},
		// VERSION_ID 12 alone is not Debian 12: the ID must say debian.
		{"another family's release 12 is served", "ID=pop\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=\"12\"\nVERSION_CODENAME=kinetic\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			checks := setupChecks(stationOn(c.osRelease), "incus-ovn")
			ovn, wiring := byID(checks, stepOVN), byID(checks, stepWiring)
			if ovn.state != verdictWarn || !ovn.step {
				t.Fatalf("the OVN step is not reported as missing: %+v", ovn)
			}
			if c.refused == "" {
				if len(ovn.commands) == 0 || len(wiring.commands) == 0 {
					t.Errorf("a supported release lost its commands: ovn %q, wiring %q", ovn.commands, wiring.commands)
				}
				if strings.Contains(ovn.detail, "ships OVN") {
					t.Errorf("a supported release is told its OVN is too old: %s", ovn.detail)
				}
				return
			}
			if len(ovn.commands) != 0 || ovn.root {
				t.Errorf("doctor prints an OVN install the tested Incus refuses: %q", ovn.commands)
			}
			for _, want := range []string{"OVN " + machine.OVNMinimum + " and Open vSwitch " + machine.OVSMinimum, c.refused, "--vm incus works here"} {
				if !strings.Contains(ovn.detail, want) {
					t.Errorf("the refusal does not say %q: %s", want, ovn.detail)
				}
			}
			if !strings.Contains(ovn.title, "too old for Incus "+machine.IncusTested) {
				t.Errorf("the title does not name the tested Incus: %s", ovn.title)
			}
			if len(wiring.commands) != 0 {
				t.Errorf("doctor wires an OVN it told the reader not to install: %q", wiring.commands)
			}
		})
	}
}

// Under auto the OVN step stays optional on Debian 12, and still says why no
// command is printed.
func TestDebian12UnderAutoIsOptionalAndSaysWhy(t *testing.T) {
	ovn := byID(setupChecks(stationOn("ID=debian\nVERSION_ID=\"12\"\n"), "auto"), stepOVN)
	if !ovn.optional || len(ovn.commands) != 0 {
		t.Errorf("optional %v, commands %q: want an optional step with no command", ovn.optional, ovn.commands)
	}
	if !strings.Contains(ovn.detail, "Optional") || !strings.Contains(ovn.detail, "OVN 23.03.1") {
		t.Errorf("the detail lost one of its halves: %s", ovn.detail)
	}
}

// The doctor line of a mode names stateless rules (#979), and a bridge mode
// adds one plain line saying such a group is refused there.
func TestDoctorSaysWhatTheModeDoesWithStatelessRules(t *testing.T) {
	const refusal = "a security group with a stateless rule is refused"
	cases := []struct {
		name        string
		caps        machine.Capabilities
		wantCan     bool // "apply stateless rules" said after "can"
		wantRefusal bool
	}{
		{"incus-ovn", machine.Capabilities{Machines: true, Addresses: true, Firewall: true, Isolation: true, StatelessRules: true}, true, false},
		{"incus", machine.Capabilities{Machines: true, Addresses: true, Firewall: true}, false, true},
		// No firewall at all: there is no security group to refuse, so the
		// extra line would be noise.
		{"no firewall", machine.Capabilities{Machines: true, Addresses: true}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lines := capabilityLines(c.name, c.caps)
			head := lines[0].title
			can, cannot, _ := strings.Cut(head, " cannot ")
			if !strings.Contains(head, "stateless rules") {
				t.Fatalf("the mode line does not mention stateless rules: %s", head)
			}
			if got := strings.Contains(can, "apply stateless rules"); got != c.wantCan {
				t.Errorf("said as a capability: %v, want %v (%s)", got, c.wantCan, head)
			}
			if got := strings.Contains(cannot, "apply stateless rules"); got == c.wantCan {
				t.Errorf("said as a missing capability: %v, want %v (%s)", got, !c.wantCan, head)
			}
			gotRefusal := len(lines) == 2 && strings.Contains(lines[1].title, refusal) && strings.Contains(lines[1].title, "--vm incus-ovn")
			if gotRefusal != c.wantRefusal || len(lines) > 2 || (!c.wantRefusal && len(lines) != 1) {
				t.Errorf("refusal line: %v, want %v (%d lines: %+v)", gotRefusal, c.wantRefusal, len(lines), lines)
			}
		})
	}
}

// ovnStation is the ready station with the OVN and Open vSwitch versions given.
func ovnStation(osRelease string, ovn, ovs []int) station {
	s := readyStation()
	s.distro = parseOSRelease(osRelease)
	s.ovnVersion, s.ovsVersion = ovn, ovs
	return s
}

// An installed OVN is judged by its version on every distribution (#979). One
// case per question: each refused station is below on one side only, and the
// remedy is asserted by family.
func TestAnOVNOlderThanTheTestedIncusNeedsIsAStep(t *testing.T) {
	const noble = "ID=ubuntu\nID_LIKE=debian\nVERSION_ID=\"24.04\"\nVERSION_CODENAME=noble\n"
	cases := []struct {
		name, osRelease string
		ovn, ovs        []int
		want            string // "" means accepted
		commands        bool
	}{
		{"both at or above", noble, []int{24, 3, 8}, []int{3, 3, 9}, "", false},
		{"exactly the floor", noble, []int{24, 3, 0}, []int{3, 3, 0}, "", false},
		{"OVN below", noble, []int{23, 3, 1}, []int{3, 3, 9}, "OVN 23.03.1 and Open vSwitch 3.3.9 are installed", true},
		{"Open vSwitch below", noble, []int{24, 3, 8}, []int{3, 1, 0}, "Open vSwitch 3.1.0 are installed", true},
		{"Debian 12 has no newer package", "ID=debian\nVERSION_ID=\"12\"\n", []int{23, 3, 1}, []int{3, 1, 0}, "This distribution ships older ones", false},
		{"an unknown system gets no command", "ID=arch\n", []int{23, 3, 1}, []int{3, 1, 0}, "This distribution ships older ones", false},
		{"Fedora is told to upgrade, with no install command", "ID=fedora\n", []int{23, 3, 1}, []int{3, 3, 9}, "sudo dnf upgrade", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ovn := byID(setupChecks(ovnStation(c.osRelease, c.ovn, c.ovs), "incus-ovn"), stepOVN)
			if c.want == "" {
				if ovn.state != verdictOK || ovn.step {
					t.Errorf("a sufficient OVN was refused: %+v", ovn)
				}
				return
			}
			if ovn.state != verdictWarn || !ovn.step || ovn.optional {
				t.Fatalf("not a required step: %+v", ovn)
			}
			text := ovn.title + " " + ovn.detail
			for _, w := range []string{c.want, "needs OVN " + machine.OVNMinimum + " and Open vSwitch " + machine.OVSMinimum} {
				if !strings.Contains(text, w) {
					t.Errorf("does not say %q: %s", w, text)
				}
			}
			if got := len(ovn.commands) > 0; got != c.commands {
				t.Errorf("commands %q, want some: %v", ovn.commands, c.commands)
			}
		})
	}
}

// Under auto the step is optional; an unreadable version is said, never read
// as sufficient or as too old.
func TestAnOVNVersionUnderAutoAndUnreadable(t *testing.T) {
	const noble = "ID=ubuntu\nID_LIKE=debian\n"
	if c := byID(setupChecks(ovnStation(noble, []int{23, 3, 1}, []int{3, 3, 9}), "auto"), stepOVN); !c.step || !c.optional {
		t.Errorf("under auto, want an optional step: %+v", c)
	}
	c := byID(setupChecks(ovnStation(noble, nil, []int{3, 3, 9}), "incus-ovn"), stepOVN)
	if c.state != verdictWarn || c.step || !strings.Contains(c.title, "could not read their versions") {
		t.Errorf("an unreadable OVN version: %+v", c)
	}
}

// The probe reads each tool's own line: ovn-nbctl prints the Open vSwitch
// library on its second line, which must not be taken for OVN's version.
func TestTheProbeReadsOVNAndOVSVersions(t *testing.T) {
	probe := hostProbe{
		osRelease: func() (string, error) { return "ID=debian\n", nil },
		lookPath:  func(name string) (string, error) { return "/fake/" + name, nil },
		exists:    func(string) bool { return false },
		run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			switch name + " " + strings.Join(args, " ") {
			case "ovn-nbctl --version":
				return []byte("ovn-nbctl 24.03.8\nOpen vSwitch Library 3.3.9\nDB Schema 7.3.0\n"), nil
			case "ovs-vsctl --version":
				return []byte("ovs-vsctl (Open vSwitch) 3.1.0\nDB Schema 8.5.1\n"), nil
			}
			return nil, errors.New("unexpected")
		},
		images: func(context.Context) ([]string, error) { return nil, nil },
	}
	s := probeStation(context.Background(), probe)
	if !reflect.DeepEqual(s.ovnVersion, []int{24, 3, 8}) || !reflect.DeepEqual(s.ovsVersion, []int{3, 1, 0}) {
		t.Errorf("read OVN %v and Open vSwitch %v, want [24 3 8] and [3 1 0]", s.ovnVersion, s.ovsVersion)
	}
	if got := firstLineVersion("no version here\n3.3.9\n"); got != nil {
		t.Errorf("a first line without a version read as %v", got)
	}
}

// Doctor says when the installed Incus is not the release the runtime proof
// ran on (#979): a warning, never a step or a failure, since it runs.
func TestDoctorSaysWhenIncusIsNotTheTestedRelease(t *testing.T) {
	tested := incusRecommended
	cases := []struct {
		name    string
		version []int
		warn    bool
	}{
		{"the tested release", []int{tested[0], tested[1], 0}, false},
		{"the tested release, two parts", []int{tested[0], tested[1]}, false},
		{"another minor", []int{tested[0], tested[1] + 1, 0}, true},
		{"another major", []int{tested[0] + 1, tested[1], 0}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := readyStation()
			s.incusVersion = c.version
			got := byID(setupChecks(s, "incus"), stepIncus)
			if got.step {
				t.Fatalf("a supported Incus became a step: %+v", got)
			}
			said := strings.Contains(got.title, "feint is tested on "+machine.IncusTested)
			if c.warn != (got.state == verdictWarn) || c.warn != said {
				t.Errorf("warn %v, said %v, want %v: %+v", got.state == verdictWarn, said, c.warn, got)
			}
		})
	}
}

// 6.0.4 was the floor until #979; it is refused now, as a step, naming the
// nftables ordering fix 6.0.5 carries. 6.0.5 itself is accepted.
func TestDoctorRefusesIncus604AndSaysWhy(t *testing.T) {
	s := readyStation()
	s.incusVersion = []int{6, 0, 4}
	got := byID(setupChecks(s, "incus"), stepIncus)
	if !got.step || got.state != verdictWarn || !strings.Contains(got.title, "too old: 6.0.5 or later") {
		t.Fatalf("6.0.4 is not refused as a step: %+v", got)
	}
	if !strings.Contains(got.detail, "lxc/incus#2222") {
		t.Errorf("the refusal does not name the fix: %s", got.detail)
	}
	s.incusVersion = []int{6, 0, 5}
	if got := byID(setupChecks(s, "incus"), stepIncus); got.step {
		t.Errorf("6.0.5, the floor, was refused: %+v", got)
	}
}

// One case per system floor of Incus 7.0: each refused host is below one
// floor and meets the others; an unreadable version is said, an absent tool
// is not judged.
func TestDoctorWarnsBelowTheSystemMinimumsOfIncus7(t *testing.T) {
	ok := map[string]systemPart{"Linux": {[]int{7, 2, 15}}, "nftables": {[]int{1, 0, 9}}, "dnsmasq": {[]int{2, 91}}}
	with := func(label string, v []int) map[string]systemPart {
		m := map[string]systemPart{}
		for k, p := range ok {
			m[k] = p
		}
		m[label] = systemPart{v}
		return m
	}
	cases := []struct {
		name   string
		system map[string]systemPart
		state  verdict
		want   string // "" means no check at all
	}{
		{"all met", ok, verdictOK, "Linux 7.2.15, nftables 1.0.9 and dnsmasq 2.91 meet"},
		{"exactly the floors", map[string]systemPart{"Linux": {[]int{6, 12}}, "nftables": {[]int{1, 0, 0}}, "dnsmasq": {[]int{2, 90}}}, verdictOK, "meet"},
		{"an old kernel", with("Linux", []int{6, 8, 0}), verdictWarn, "Linux 6.8.0 (needs 6.12)"},
		{"an old nftables", with("nftables", []int{0, 9, 9}), verdictWarn, "nftables 0.9.9 (needs 1.0.0)"},
		{"an old dnsmasq", with("dnsmasq", []int{2, 89}), verdictWarn, "dnsmasq 2.89 (needs 2.90)"},
		{"an unreadable version", with("nftables", nil), verdictWarn, "could not read the version of nftables"},
		{"an absent tool is not judged", map[string]systemPart{"Linux": {[]int{7, 2, 15}}}, verdictOK, "Linux 7.2.15 meet"},
		{"nothing read", map[string]systemPart{}, verdictOK, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := readyStation()
			s.system = c.system
			got := byID(setupChecks(s, "incus"), "system")
			if c.want == "" {
				if got.id != "" {
					t.Errorf("a check with nothing read: %+v", got)
				}
				return
			}
			if got.state != c.state || got.step || !strings.Contains(got.title, c.want) {
				t.Errorf("state %v step %v title %q, want state %v and %q", got.state, got.step, got.title, c.state, c.want)
			}
		})
	}
}

// The probe reads the kernel from its release string and each tool from the
// first version on its first line, whatever the language; an absent tool is
// left out.
func TestTheProbeReadsTheSystemVersions(t *testing.T) {
	probe := hostProbe{
		osRelease: func() (string, error) { return "ID=debian\n", nil },
		lookPath: func(name string) (string, error) {
			if name == "nft" {
				return "/usr/sbin/nft", nil
			}
			if name == "dnsmasq" {
				return "/usr/sbin/dnsmasq", nil
			}
			return "", errors.New("absent")
		},
		exists: func(string) bool { return false },
		run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			switch name {
			case "nft":
				return []byte("nftables v1.0.9 (Old Doc Yak #3)\n"), nil
			case "dnsmasq":
				return []byte("Version de Dnsmasq 2.91  Copyright (c) 2000-2025 Simon Kelley\n"), nil
			}
			return nil, errors.New("unexpected")
		},
		images: func(context.Context) ([]string, error) { return nil, nil },
		kernel: func() (string, error) { return "6.8.0-45-generic\n", nil },
	}
	got := probeStation(context.Background(), probe).system
	want := map[string]systemPart{"Linux": {[]int{6, 8, 0}}, "nftables": {[]int{1, 0, 9}}, "dnsmasq": {[]int{2, 91}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read %v, want %v", got, want)
	}
	// dnsmasq absent from the host: left out, never read as unreadable.
	probe.lookPath = func(name string) (string, error) {
		if name == "nft" {
			return "/usr/sbin/nft", nil
		}
		return "", errors.New("absent")
	}
	if got := probeStation(context.Background(), probe).system; len(got) != 2 {
		t.Errorf("an absent dnsmasq was read: %v", got)
	}
}
