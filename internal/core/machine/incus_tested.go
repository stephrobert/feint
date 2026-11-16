package machine

import (
	"regexp"
	"strconv"
)

// IncusTested is the Incus release the runtime proof runs on, spelled the way
// the Zabbly stable channel spells the upstream part of its package version
// (`1:7.6-ubuntu24.04-202611072126` is upstream 7.6).
//
// It is the one place that version is written (#979). Four things read it and
// none restates it:
//
//   - `feint doctor` and the prerequisites table of README.md and
//     docs/install.md, through `incusRecommended` (internal/cli/doctor.go), so
//     `feint docs --check` exits 2 when the pages name another version;
//   - .github/workflows/runtime-proof.yml, which installs
//     `incus=1:<IncusTested>-*` and nothing else; TestTheRuntimeProofInstallsTheTestedIncus
//     (tools/ci/incus_pin_test.go) fails when the workflow pins anything but
//     this value, or installs Incus without a version;
//   - tools/install/ansible/roles/feint_host/vars/debian.yml, which installs
//     `incus=1:{{ feint_host_incus_version }}-*`; TestTheInstallRoleInstallsTheTestedIncus
//     (tools/ci/incus_pin_test.go) holds that variable to this value;
//   - .github/workflows/incus-release.yml, which compares the workflow's pin
//     with what the Zabbly stable channel serves each night and keeps one issue
//     open while they differ (tools/ci/incus-release.sh).
//
// Before #979 the workflow installed whatever Zabbly published last, while the
// code still named 7.2 and the station ran 7.6: a nightly verdict that no
// longer said what it was measured on.
//
// It is not a floor: IncusMinimum is. A host between the two runs everything
// this emulator asks for; IncusTested is what was proven, not what is required.
// docs/runtime-incus.md records what changed upstream between the versions this
// constant has held.
const IncusTested = "7.6"

// OVNMinimum and OVSMinimum are the oldest OVN and Open vSwitch the OVN driver
// of IncusTested accepts. Incus 7.4 raised them to these two values (Incus 7.4
// release announcement, discuss.linuxcontainers.org; docs/runtime-incus.md,
// I14), so they hold for every IncusTested from 7.4 on: moving IncusTested
// means re-reading them. `feint doctor` compares what `ovn-nbctl --version`
// and `ovs-vsctl --version` print with them, both readable without root
// (measured on the maintainer's station, 2026-11-16: OVN 24.03.8, Open vSwitch
// 3.3.9). TestAnOVNOlderThanTheTestedIncusNeedsIsAStep (internal/cli) fails
// when doctor stops comparing (#979).
const (
	OVNMinimum = "24.03.0"
	OVSMinimum = "3.3.0"
)

// The system minimums Incus 7.0 raised (Incus 7.0 LTS announcement,
// discuss.linuxcontainers.org topic 26641, "Breaking changes"): Linux 6.12,
// nftables 1.0.0, dnsmasq 2.90 (and Open vSwitch 2.15.0, OVN 23.03.0, which
// 7.4 raised again to OVSMinimum and OVNMinimum). They hold for every
// IncusTested from 7.0 on. `feint doctor` reads the kernel from
// /proc/sys/kernel/osrelease and the two tools from `--version`, none of
// which needs root, and warns below them: what breaks there was not measured.
// internal/cli's TestDoctorWarnsBelowTheSystemMinimumsOfIncus7 fails without it (#979).
const (
	KernelMinimum   = "6.12"
	NftablesMinimum = "1.0.0"
	DnsmasqMinimum  = "2.90"
)

// testedVersion accepts a dotted release with two or three parts and nothing
// around it. Stricter than serverVersion on purpose: that one reads what a
// daemon answers and tolerates a suffix, this one reads a constant the
// repository writes, and "7.6 " or "v7.6" there would install nothing.
var testedVersion = regexp.MustCompile(`^(\d+)\.(\d+)(?:\.(\d+))?$`)

// IncusTestedSeries is IncusTested as a major and a minor, the form the
// diagnostics and the prerequisites table cite. A malformed constant yields
// the zero series, which TestIncusTestedIsARelease refuses: a page reading
// "0.0 recommended" is the visible failure, never a guessed one.
func IncusTestedSeries() [2]int {
	return seriesOf(IncusTested)
}

func seriesOf(v string) [2]int {
	m := testedVersion.FindStringSubmatch(v)
	if m == nil {
		return [2]int{}
	}
	major, err1 := strconv.Atoi(m[1])
	minor, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil {
		return [2]int{}
	}
	return [2]int{major, minor}
}
