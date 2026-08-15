package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Capabilities answered by the host, not promised by the flag (#181).
//
// `Capabilities()` used to be a pure function of the driver's own fields:
// `NewIncusOVN` set `OVN = true` and `Isolation` followed, whatever the host
// could actually do. There was one `Available` for all three Incus modes and it
// ran `incus list`, so on any host whose daemon answers, the OVN driver reported
// available. `internal/cli/cli.go` carried a comment vouching for a fall-through
// that could therefore never trigger:
//
//	The guess is cheap to make safe: the OVN driver's own availability check
//	creates nothing, so a probe that fails costs one call and falls through to
//	the bridge.
//
// That probe did not exist. Measured on 2026-08-15 on a host running Incus 7.2
// with no OVN wiring at all: `--vm auto` selected `incus-ovn` and
// `/_feint/health` published `isolation: true`, until the first network
// creation failed and blamed the address block.
//
// A false capability is strictly worse than none here, because this project
// tells every consumer to key on `capabilities.isolation` rather than on a mode
// name. A suite obeying that rule asserts what the host cannot deliver.
//
// TestVerifyNarrowsWhatTheHostCannotDeliver fails without this.

// IncusMinimum is the daemon version below which NIC ACLs are refused, and the
// failure looks like a bug in this emulator rather than a version floor: a
// security group is created, attached, and enforces nothing.
//
// It lives here rather than in the diagnostics because two callers need it and
// a second copy is how the two drift — the same failure shape #171 was filed
// for, expressed in versions. `feint doctor` reads it from here.
var IncusMinimum = [3]int{6, 0, 4}

// ovnNorthbound is the server setting an OVN network cannot be created without.
// Reading it creates nothing and leaves nothing, which `auto` requires: it runs
// at every startup, on hosts that are perfectly healthy.
const ovnNorthbound = "network.ovn.northbound_connection"

// serverVersion matches what `incus query /1.0` reports. The patch component is
// optional because 7.2 is a real answer — an earlier version of the equivalent
// check in the diagnostics demanded three components and reported "could not
// read the version" on a perfectly good installation.
var serverVersion = regexp.MustCompile(`^(\d+)\.(\d+)(?:\.(\d+))?`)

// Verify asks the host what it can actually deliver and narrows the declared
// capabilities to it. It returns the verified set and, for each capability the
// flag promised and the host cannot deliver, one sentence naming what was
// checked and what answered.
//
// What it does not do, stated because the boundary matters: a wired northbound
// connection is necessary for an OVN network and not sufficient. A host whose
// uplink is misconfigured still reports the northbound and still fails at the
// first create — docs/install.md documents that case and this probe does not
// close it. What it does close is the ordinary host, the one that never
// installed OVN, which is the host most likely to meet `auto`.
//
// Both calls are reads. Neither creates a network, touches the uplink, or
// leaves a trace.
func (d *Incus) Verify(ctx context.Context) (Capabilities, []string) {
	declared := d.Capabilities()
	verified := declared
	var unmet []string

	if declared.Isolation {
		if wired, why := d.ovnWired(ctx); !wired {
			verified.Isolation = false
			unmet = append(unmet, "isolation: "+why)
		}
	}

	if declared.Firewall {
		if ok, why := d.firewallAge(ctx); !ok {
			verified.Firewall = false
			unmet = append(unmet, "firewall: "+why)
		}
	}

	d.verified = &verified
	return verified, unmet
}

// ovnWired reports whether the daemon has a northbound connection configured.
func (d *Incus) ovnWired(ctx context.Context) (bool, string) {
	out, err := d.run(ctx, "config", "get", ovnNorthbound)
	if err != nil {
		return false, "the daemon did not answer for " + ovnNorthbound
	}
	if strings.TrimSpace(string(out)) == "" {
		return false, ovnNorthbound + " is unset, so no OVN network can be created " +
			"(install ovn-central and ovn-host, and wire the northbound)"
	}
	return true, ""
}

// firewallAge reports whether the daemon is new enough to accept NIC ACLs.
//
// Unreadable is not refused: a daemon that answers `incus list` and whose
// version this cannot parse is far more likely to be a format change than an
// old release, and refusing there would turn a diagnostic gap into a lost
// capability. The version that is read and too old is refused, loudly.
func (d *Incus) firewallAge(ctx context.Context) (bool, string) {
	out, err := d.run(ctx, "query", "/1.0")
	if err != nil {
		return true, ""
	}
	var payload struct {
		Environment struct {
			ServerVersion string `json:"server_version"`
		} `json:"environment"`
	}
	if json.Unmarshal(out, &payload) != nil {
		return true, ""
	}
	got, ok := parseVersion(payload.Environment.ServerVersion)
	if !ok {
		return true, ""
	}
	if olderThan(got, IncusMinimum) {
		return false, fmt.Sprintf(
			"the daemon is %s and NIC ACLs are refused below %s, so a security group "+
				"attaches and enforces nothing",
			payload.Environment.ServerVersion, VersionText(IncusMinimum[:]))
	}
	return true, ""
}

// parseVersion reads "7.2" or "6.0.4" into a comparable tuple.
func parseVersion(s string) ([3]int, bool) {
	var out [3]int
	m := serverVersion.FindStringSubmatch(strings.TrimSpace(s))
	if len(m) < 3 {
		return out, false
	}
	for i := range out {
		if m[i+1] != "" {
			out[i], _ = strconv.Atoi(m[i+1])
		}
	}
	return out, true
}

func olderThan(got, floor [3]int) bool {
	for i := range got {
		if got[i] != floor[i] {
			return got[i] < floor[i]
		}
	}
	return false
}

// VersionText renders a version tuple the way the documentation and the
// diagnostics both cite it, so a change to the floor moves every sentence that
// tells an operator what to install.
func VersionText(parts []int) string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = strconv.Itoa(p)
	}
	return strings.Join(out, ".")
}
