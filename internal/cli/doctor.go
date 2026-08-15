package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/machine"
)

// Diagnostics earn their place only by encoding something that cost somebody an
// hour. Every check below did, and docs/limits.md records each one with the story
// attached.
//
// The rule that shapes the output: a warning must never fail a CI job. doctor
// exits 1 on a fail and 0 on any number of warnings, because most of what it
// finds is "this will not work the way you expect" rather than "this is broken",
// and a diagnostic that fails builds is a diagnostic people stop running.

type verdict int

const (
	verdictOK verdict = iota
	verdictWarn
	verdictFail
)

type check struct {
	title  string
	state  verdict
	detail string
	// fix is what to do about it. A check that reports a problem without saying
	// what to do is a check that sends the reader to the source.
	fix string
}

func doctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	addr := fs.String("addr", DefaultAddr, "the address serve would listen on")
	vm := fs.String("vm", "auto", "the machine runtime to diagnose: off, incus, incus-vm, incus-ovn, auto")
	if err := fs.Parse(args); err != nil {
		return exitError
	}

	ctx := context.Background()
	checks := []check{
		checkPort(*addr),
		checkContracts(),
	}
	checks = append(checks, checkRuntime(ctx, *vm)...)
	checks = append(checks, checkClients()...)
	checks = append(checks, checkSSHConfig())

	failed := 0
	for _, c := range checks {
		mark, stream := "ok  ", stdout
		switch c.state {
		case verdictWarn:
			mark = "warn"
		case verdictFail:
			mark, stream = "FAIL", stderr
			failed++
		case verdictOK:
		}
		fmt.Fprintf(stream, "  %s  %s\n", mark, c.title)
		if c.detail != "" {
			fmt.Fprintf(stream, "        %s\n", c.detail)
		}
		if c.fix != "" && c.state != verdictOK {
			fmt.Fprintf(stream, "        → %s\n", c.fix)
		}
	}

	if failed > 0 {
		fmt.Fprintf(stderr, "\n%d check(s) failed\n", failed)
		return exitError
	}
	return exitOK
}

// checkPort answers the first question anybody asks when serve will not start,
// and names the process holding the port rather than leaving it to lsof.
func checkPort(addr string) check {
	listener, err := net.Listen("tcp", addr)
	if err == nil {
		_ = listener.Close()
		return check{title: "the listen address " + addr + " is free", state: verdictOK}
	}
	c := check{
		title:  "the listen address " + addr + " is taken",
		state:  verdictFail,
		detail: err.Error(),
		fix:    "stop what holds it, or serve on another address with --addr",
	}
	// If it is this emulator, say so: "already running" is a different problem
	// from "something else took the port".
	if healthy(addr, time.Second) {
		c.state = verdictWarn
		c.title = "an emulator already answers on " + addr
		c.detail = ""
		c.fix = "feint stop --addr " + addr + ", or use it as it is"
	}
	return c
}

// checkContracts reports whether the response check would actually run. Serving
// without contracts is legitimate and quiet, which is exactly why a reader who
// believes their responses are validated deserves to be told they are not.
func checkContracts() check {
	entries, err := os.ReadDir("contracts")
	if err != nil {
		return check{
			title:  "no contracts/ directory here",
			state:  verdictWarn,
			detail: "responses will not be checked against the providers' API descriptions",
			fix:    "run from the repository, or pass --contracts <dir>",
		}
	}
	count := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			count++
		}
	}
	if count == 0 {
		return check{
			title: "contracts/ holds no API description",
			state: verdictWarn,
			fix:   "mise run contracts:update",
		}
	}
	return check{title: fmt.Sprintf("%d API description(s) in contracts/", count), state: verdictOK}
}

// checkRuntime diagnoses the machine runtime, and the three things about it that
// have each cost an afternoon.
func checkRuntime(ctx context.Context, mode string) []check {
	if mode == "off" || mode == "none" {
		return []check{{
			title:  "no machine runtime requested",
			state:  verdictOK,
			detail: "servers change state and nothing runs, which is the default and what CI uses",
		}}
	}

	driver, err := machineDriver(mode, io.Discard)
	if err != nil {
		return []check{{title: "the --vm mode is not one this binary knows", state: verdictFail, detail: err.Error()}}
	}
	if _, isNoop := driver.(machine.Noop); isNoop {
		return []check{{
			title:  "no machine runtime is available",
			state:  verdictWarn,
			detail: "the Incus daemon does not answer",
			fix:    "install Incus, or accept metadata-only machines",
		}}
	}

	caps := machine.CapabilitiesOf(driver)
	out := []check{{
		title:  "machine runtime: " + driver.Name(),
		state:  verdictOK,
		detail: fmt.Sprintf("addresses %v, firewall %v, isolation %v, own kernel %v", caps.Addresses, caps.Firewall, caps.Isolation, caps.OwnKernel),
	}}

	// The one that surprises people: auto never picks a VM, and until recently
	// it never picked OVN either — so an operator with everything installed got
	// the one mode that cannot isolate two VPCs.
	if mode == "auto" && !caps.Isolation {
		out = append(out, check{
			title:  "auto chose a mode that cannot isolate two VPCs",
			state:  verdictWarn,
			detail: "subnets of different VPCs will reach each other; the conformance suite reports it as a skip",
			fix:    "install ovn-central, ovn-host and Open vSwitch, or ask for --vm incus-ovn explicitly",
		})
	}
	if mode == "auto" {
		out = append(out, check{
			title:  "auto never starts a virtual machine",
			state:  verdictOK,
			detail: "a VM costs tens of seconds where a container costs seconds; ask for --vm incus-vm to get one",
		})
	}
	out = append(out, checkIncusVersion(ctx))
	out = append(out, checkImages(ctx, driver))
	return out
}

// checkImages reports the machine images that are not on the host (#203).
//
// It diagnoses and never builds, deliberately. A build launches a container,
// installs a package and publishes an image: minutes, and a side effect on the
// operator's station. That is the same arbitration `--vm off` already makes,
// and the reason `mise run serve` does not start machines unless asked. So this
// names what is missing and the exact command that makes it, and the operator
// decides.
//
// An image set that is complete says so as an ok rather than staying silent: a
// diagnostic that only speaks when something is wrong leaves the reader unable
// to tell "checked and fine" from "never looked".
//
// TestDoctorNamesTheMissingImagesAndHowToBuildThem fails without this.
func checkImages(ctx context.Context, driver machine.Driver) check {
	inventory, err := machine.ImageInventory(ctx, driver)
	if err != nil {
		return check{
			title:  "could not read the machine images",
			state:  verdictWarn,
			detail: err.Error(),
			fix:    "check that the Incus daemon answers, then run `feint images`",
		}
	}
	missing := machine.MissingImages(inventory)
	if len(missing) == 0 {
		return check{
			title:  fmt.Sprintf("%d machine image(s) present", len(inventory)),
			state:  verdictOK,
			detail: "each carries an ssh daemon, so a machine answers on port 22 without reaching a package repository",
		}
	}
	return check{
		title: fmt.Sprintf("%d machine image(s) missing", len(missing)),
		state: verdictWarn,
		detail: machine.ImageNames(missing) + " — no upstream image ships an ssh daemon, so " +
			"a machine built from one installs it at first boot and needs outbound internet to do it",
		fix: "feint images  (builds them locally; minutes, and it starts a container)",
	}
}

// incusMinimum is the version below which NIC ACLs are refused, and the failure
// looks like a bug in this emulator rather than a version floor: a security
// group is created, attached, and enforces nothing.
//
// Read from the runtime rather than declared here since #181: `serve` checks the
// same floor before publishing `firewall: true`, and two copies is how the two
// drift — the diagnostic saying one thing and the capability another, which is
// exactly the shape of defect that issue was filed for.
var incusMinimum = machine.IncusMinimum

// incusRecommended is the series every measurement in docs/limits.md was taken
// on, and the one docs/install.md installs. It is not a floor: a 6.0.4 host runs
// everything this emulator asks for.
//
// Declared beside the floor because the documentation reads both from here. The
// prerequisites table used to restate them by hand, which is how a page ends up
// recommending a version nobody measured.
var incusRecommended = [2]int{7, 2}

// versionText renders a version tuple the way the documentation and the
// diagnostics both cite it. Written once: the two strings below used to spell
// "6.0.4" out beside incusMinimum, so a change to the floor would have moved the
// check without moving what it tells the operator to install.
//
// It forwards to the runtime's copy for the same reason the floor does: the
// startup line that narrows a capability cites the same version, in the same
// spelling.
func versionText(parts []int) string { return machine.VersionText(parts) }

func checkIncusVersion(ctx context.Context) check {
	cmd := exec.CommandContext(ctx, "incus", "version") //nolint:gosec // a fixed binary name
	out, err := cmd.Output()
	if err != nil {
		return check{title: "the incus client is not on PATH", state: verdictWarn,
			fix: "install Incus " + versionText(incusMinimum[:]) + " or later"}
	}
	// `incus version` prints "Client version: X" and "Server version: Y"; the
	// server is the one that enforces.
	//
	// The patch component is optional: 7.2 is a real answer and an earlier
	// version of this check demanded three components, so it reported "could not
	// read the Incus version" on a perfectly good installation. A diagnostic
	// that cannot read the common case is worse than no diagnostic.
	version := regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`).FindStringSubmatch(string(out))
	if len(version) < 3 {
		return check{title: "could not read the Incus version", state: verdictWarn, detail: firstLine(string(out))}
	}
	var got [3]int
	for i := range got {
		got[i], _ = strconv.Atoi(version[i+1])
	}
	for i := range got {
		if got[i] != incusMinimum[i] {
			if got[i] > incusMinimum[i] {
				break
			}
			return check{
				title:  fmt.Sprintf("Incus %s is below %s", versionText(got[:]), versionText(incusMinimum[:])),
				state:  verdictWarn,
				detail: "NIC ACLs are refused below that version, so a security group attaches and enforces nothing",
				fix:    "upgrade Incus; Ubuntu ships 6.0.0, the Zabbly stable channel ships later",
			}
		}
	}
	return check{title: fmt.Sprintf("Incus %d.%d.%d, new enough for NIC ACLs", got[0], got[1], got[2]), state: verdictOK}
}

// checkClients reports which conformance clients are installed, with versions.
// Absent ones are a warning: the suites fail loudly on their own, and a
// diagnostic that fails because Terraform is missing would fail on every machine
// that only cares about Scaleway.
func checkClients() []check {
	type client struct{ binary, args, why string }
	clients := []client{
		{"scw", "version", "the Scaleway CLI suite"},
		{"oapi-cli", "--version", "the Outscale CLI suite"},
		{"exo", "version", "the Exoscale CLI suite"},
		{"terraform", "version", "the Terraform suite"},
		{"tofu", "version", "the OpenTofu suite"},
		{"jq", "--version", "every suite"},
	}
	out := make([]check, 0, len(clients))
	for _, c := range clients {
		path, err := exec.LookPath(c.binary)
		if err != nil {
			out = append(out, check{
				title: c.binary + " is not installed",
				state: verdictWarn,
				fix:   "needed by " + c.why + "; see tools/conformance/README.md",
			})
			continue
		}
		version := firstLine(runQuiet(c.binary, c.args))
		out = append(out, check{title: c.binary + " " + version, state: verdictOK, detail: path})
	}
	return out
}

// checkSSHConfig is the check that most earns its place. A ProxyJump declared on
// a broad Host pattern in the operator's ~/.ssh/config also captures the
// runtime's private range; ssh then composes with a bastion that does not route
// to 10.x, and the connection dies on "timed out during banner exchange". The
// symptom is that of an absent ssh daemon, while sshd is running, listening and
// holding the right key. It cost an hour once.
func checkSSHConfig() check {
	home, err := os.UserHomeDir()
	if err != nil {
		return check{title: "could not read ~/.ssh/config", state: verdictOK}
	}
	file, err := os.Open(filepath.Join(home, ".ssh", "config")) //nolint:gosec // the operator's own file
	if err != nil {
		return check{title: "no ~/.ssh/config to get in the way", state: verdictOK}
	}
	defer func() { _ = file.Close() }()

	var host string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch strings.ToLower(fields[0]) {
		case "host":
			host = strings.Join(fields[1:], " ")
		case "proxyjump":
			// A pattern that would match a private address the runtime hands
			// out. "*" is the obvious one; "10.*" the deliberate one.
			for _, pattern := range strings.Fields(host) {
				if pattern == "*" || strings.HasPrefix(pattern, "10.") {
					return check{
						title:  "a ProxyJump applies to " + pattern + " in ~/.ssh/config",
						state:  verdictWarn,
						detail: "it captures the runtime's private range, and ssh dies on 'timed out during banner exchange' while sshd is running and listening",
						fix:    "narrow the Host pattern, or use ssh -F /dev/null as tools/conformance/scaleway/ssh.sh does",
					}
				}
			}
		}
	}
	return check{title: "no ProxyJump in ~/.ssh/config captures the runtime's range", state: verdictOK}
}

func runQuiet(binary string, args ...string) string {
	cmd := exec.Command(binary, args...) //nolint:gosec // binary and args are this file's own
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
