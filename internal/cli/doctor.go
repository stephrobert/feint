package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
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
	// fix is what to do about it, in words. A check that reports a problem
	// without saying what to do is a check that sends the reader to the source.
	fix string

	// The fields below came with #974: grouping, ordering and commands.
	id    string
	group string
	// step marks a setup step: numbered, counted in the summary, done in order.
	step bool
	// optional marks a step the mode runs without (OVN under --vm auto): still
	// numbered, counted apart, never marked as a failure.
	optional bool
	// foundAt and answered are where the report found a client and the first
	// line it answered, so --fix names what it replaces without running it again.
	foundAt, answered string
	// needs names the steps that must be done first; the reader sees "(do step
	// N first)" when one of them is missing too.
	needs []string
	after string // a free-form ordering note, for checks that are not steps
	// commands is the remedy as lines to paste, in order. root says they need
	// sudo, so `--fix` prints them and never runs them.
	commands []string
	root     bool
	// verbose is a caveat about what the check looked at: printed under -v
	// only, because the title already says exactly what was checked.
	verbose string
	// fixKey ties the check to an action `--fix` knows how to take.
	fixKey string
	paths  []string // the exact paths a cleanup step removes
}

const (
	groupEmulator = "The emulator"
	groupMachines = "Machines"
	groupClients  = "Conformance clients (optional: only the test suites use them)"
	groupTools    = "Repository tools (optional: serve needs neither)"
	groupHost     = "This machine"
	groupStack    = "This stack"
)

// validModes are the --vm values this binary knows. An unknown one is refused
// before any probe, so a typo never reads as a host that lacks something.
var validModes = map[string]bool{"off": true, "none": true, "incus": true, "incus-vm": true, "incus-ovn": true, "auto": true}

func grouped(group string, checks ...check) []check {
	for i := range checks {
		if checks[i].group == "" {
			checks[i].group = group
		}
	}
	return checks
}

func doctor(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("doctor")
	addr := fs.String("addr", DefaultAddr, "the address serve would listen on")
	vm := fs.String("vm", "auto", "the machine runtime to diagnose: off, incus, incus-vm, incus-ovn, auto")
	withS3 := fs.Bool("s3", false, "require and diagnose live S3 activation on this address")
	stackDir := fs.String("stack", "", "also read the Terraform or OpenTofu configuration in this directory and report what it appears to need")
	stackEngine := fs.String("engine", "terraform", "which engine's loading rules to read the stack with: terraform or opentofu. They disagree about which files a module is")
	stackJSON := fs.Bool("stack-json", false, "print the stack report as JSON instead of prose")
	verbose := fs.Bool("v", false, "also print what each check did not look at")
	fix := fs.Bool("fix", false, "apply the remedies that need no root, after printing the plan; root steps are printed, never run")
	yes := fs.Bool("yes", false, "with --fix: run the plan without asking")
	withClients := fs.Bool("clients", false, "with --fix: also install the pinned conformance clients into ~/.local/bin")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if !validModes[*vm] {
		fmt.Fprintf(stderr, "feint doctor: --vm %q is not a mode this binary knows: off, incus, incus-vm, incus-ovn, auto\n", *vm)
		return exitError
	}

	ctx := context.Background()
	host := realHostProbe()
	collect := func() []check {
		return collectChecks(ctx, host, realToolProbe(), *addr, *vm, *withS3)
	}
	checks := collect()
	playbook, checkout := playbookFound()
	view := doctorView{mode: *vm, distro: probeDistro(host), verbose: *verbose,
		color: colorWanted(stdout), playbook: playbook, checkout: checkout}

	if *fix {
		deps := realFixDeps(collect)
		// Both ends: the plan goes to stdout, so a question asked while it is
		// piped away would be a question about a plan nobody saw.
		out, isFile := stdout.(*os.File)
		tty := isTerminal(os.Stdin) && isFile && isTerminal(out)
		return runFix(ctx, stdout, os.Stdin, tty, *yes, view,
			buildFixPlan(checks, deps, *withClients), deps)
	}

	// The stack preflight prints its own report (and its JSON shape, which
	// this command does not change) before the grouped checks.
	checks = append(checks, grouped(groupStack, checkStackPreflight(*stackDir, *stackEngine, *stackJSON, stdout, stderr)...)...)
	failed := renderDoctor(stdout, checks, view)
	if failed > 0 {
		fmt.Fprintf(stderr, "\n%d check(s) failed\n", failed)
		return exitError
	}
	return exitOK
}

// collectChecks runs every check, in the order the groups are printed. It is
// the one list both the report and `--fix`'s re-check read, so a step `--fix`
// verifies is the step the report showed.
func collectChecks(ctx context.Context, host hostProbe, tools toolProbe, addr, mode string, withS3 bool) []check {
	checks := grouped(groupEmulator, checkPort(addr), checkContracts())
	if withS3 {
		checks = append(checks, grouped(groupEmulator, checkObjectStorage(addr))...)
	}
	checks = append(checks, grouped(groupMachines, checkRuntime(ctx, mode, host)...)...)
	checks = append(checks, grouped(groupClients, clientChecks(tools)...)...)
	checks = append(checks, grouped(groupTools, toolChecks(tools)...)...)
	checks = append(checks, grouped(groupHost, checkLeftoverRunAuthorities(), checkLeftoverDHCP())...)
	checks = append(checks, grouped(groupHost, checkEnvHazards()...)...)
	checks = append(checks, grouped(groupHost, checkSSHConfig())...)
	checks = append(checks, grouped(groupStack, checkStackHazards()...)...)
	return checks
}

func checkObjectStorage(addr string) check {
	c := check{title: "Live S3 storage is not on at this address.", state: verdictFail,
		fix:      "Start the emulator with a private directory for the objects, then point the clients at it:",
		commands: []string{"feint start --objectstore <private-directory>", "feint env <provider> --s3"}}
	endpoint, err := storageListenEndpoint(addr)
	if err != nil {
		c.detail = err.Error()
		return c
	}
	report, err := fetchS3(endpoint)
	if err != nil {
		c.detail = err.Error()
	} else if !report.Enabled {
		c.detail = "S3 is explicitly disabled"
	} else {
		c.state = verdictOK
		c.title = "Live S3 storage is on."
		c.detail = fmt.Sprintf("%d declared door(s); %d blob bytes maximum; %d bytes per simple PUT.",
			len(report.Doors), report.MaxBlobBytes, report.MaxObjectBytes)
		for _, door := range report.Doors {
			if door.Host != "" {
				c.detail += " Host " + door.Host + " needs the local non-forwarding HTTP proxy."
			}
		}
		c.verbose = "Ancillary client lifecycles are not certified."
	}
	return c
}

// checkPort answers the first question anybody asks when serve will not start,
// and names the process holding the port rather than leaving it to lsof.
func checkPort(addr string) check {
	listener, err := net.Listen("tcp", addr)
	if err == nil {
		_ = listener.Close()
		return check{title: "Port " + addr + " is free.", state: verdictOK}
	}
	c := check{
		title:  "Port " + addr + " is taken by another program.",
		state:  verdictFail,
		detail: err.Error() + ". Stop what holds it, or pick another address with --addr.",
	}
	// If it is this emulator, say so: "already running" is a different problem
	// from "something else took the port".
	if healthy(addr, time.Second) {
		c.state = verdictWarn
		c.title = "An emulator already answers on " + addr + "."
		c.detail = "Use it as it is, or stop it first:"
		c.commands = []string{"feint stop --addr " + addr}
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
			title:   "No contracts/ directory here: responses will not be checked against the providers' API descriptions.",
			state:   verdictWarn,
			verbose: "Run from a clone of the repository, or start with --contracts <dir>, to have them checked.",
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
			title:    "contracts/ holds no API description, so responses are not checked.",
			state:    verdictWarn,
			commands: []string{"mise run contracts:update"},
		}
	}
	return check{title: fmt.Sprintf("%d API descriptions in contracts/: responses are checked against them.", count), state: verdictOK}
}

// checkRuntime diagnoses the machine runtime: first the setup steps in the
// order a reader must do them (setupChecks, #974), then what the mode can
// prove once the driver is built.
//
// Before #974 this returned early when the Incus daemon did not answer, so the
// version and the images were never reported on the station that most needed
// them, and a driver refused for a missing OVN read "the --vm mode is not one
// this binary knows" — false for incus-ovn, a mode this binary knows well.
// TestDoctorListsEveryMissingStepInOnePass fails without the steps. An unknown
// mode is now refused before any probe (TestAnUnknownModeIsRefusedBeforeAnyProbe);
// the wording of a refused driver has no unit test, because machineDriver
// builds the real Incus driver and offers no seam to make it refuse.
func checkRuntime(ctx context.Context, mode string, host hostProbe) []check {
	if mode == "off" || mode == "none" {
		return []check{{
			title:   "Not checked: --vm off runs no machines. Servers change state and nothing runs, which is what CI uses.",
			state:   verdictOK,
			verbose: "Run feint doctor --vm incus-ovn (or incus, incus-vm, auto) to check what machines need.",
		}}
	}
	out := setupChecks(probeStation(ctx, host), mode)
	missing := false
	for _, c := range out {
		missing = missing || (c.step && c.state != verdictOK)
	}

	rt, err := machineDriver(mode, io.Discard)
	if err != nil {
		// First in the group: after the steps, "the steps above fix it"
		// read as a promise about every step, which nobody had checked.
		c := check{title: "--vm " + mode + " cannot start on this machine.", state: verdictFail, detail: err.Error() + "."}
		if missing {
			c.detail += " The numbered steps below are what doctor found missing."
		}
		return append([]check{c}, out...)
	}
	if !rt.Runs() {
		if !missing {
			out = append(out, check{title: "No machine runtime answers.", state: verdictWarn,
				detail: "Servers will change state without running anything until Incus answers."})
		}
		return out
	}

	caps := rt.Capabilities()
	can, cannot := []string{}, []string{}
	for _, item := range []struct {
		ok   bool
		text string
	}{{caps.Addresses, "give addresses"}, {caps.Firewall, "apply firewalls"}, {caps.Isolation, "isolate VPCs"}, {caps.OwnKernel, "give each machine its own kernel"}} {
		if item.ok {
			can = append(can, item.text)
		} else {
			cannot = append(cannot, item.text)
		}
	}
	line := check{title: "This mode (" + rt.Name() + ")", state: verdictOK}
	if len(can) > 0 {
		line.title += " can " + joinAnd(can)
		if len(cannot) > 0 {
			line.title += ", and"
		}
	}
	if len(cannot) > 0 {
		line.title += " cannot " + joinOr(cannot)
	}
	line.title += "."
	out = append(out, line)

	// The one that surprises people: auto never picks a VM, and until recently
	// it never picked OVN either — so an operator with everything installed got
	// the one mode that cannot isolate two VPCs.
	if mode == "auto" && !caps.Isolation {
		out = append(out, check{
			title:  "auto chose a mode that cannot keep two VPCs apart.",
			state:  verdictWarn,
			detail: "Subnets of different VPCs will reach each other, and the conformance suite reports it as a skip. The OVN steps above make auto choose incus-ovn.",
		})
	}
	return out
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func joinAnd(items []string) string { return joinWith(items, " and ") }
func joinOr(items []string) string  { return joinWith(items, " or ") }

func joinWith(items []string, last string) string {
	if len(items) <= 1 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + last + items[len(items)-1]
}

// missingImageNames asks a driver which machine images are not on the host.
// A driver that cannot list answers an error, never "nothing is missing":
// TestDoctorDoesNotReadSilenceAsACompleteImageSet fails otherwise.
func missingImageNames(ctx context.Context, rt machine.Runtime) ([]string, error) {
	inventory, err := rt.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, spec := range machine.MissingImages(inventory) {
		names = append(names, spec.Name)
	}
	return names, nil
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

// checkLeftoverDHCP reports the DHCP services a run left holding an
// address block (#316, #342): dnsmasq processes attributable to this emulator
// whose network is gone — the interface with it, or the network object alone,
// the interface having survived. The condition is invisible to `ip addr` and
// `incus network list`, and knowing that `ss -lnp` is the third place to look
// cost three ten-minute runs the first time; this check is that knowledge,
// one line, before the run starts. It reports and never signals — ending the
// process is `feint clean`'s job, and touching the host is not what a
// diagnostic is for.
//
// A fail, not a warning, deliberately: unlike the hazards around it, this
// state makes the next machines-on run die at the bind with certainty, and a
// green doctor followed by that death is precisely the misplaced reassurance
// #342 measured. The check once answered `ok: no DHCP service outlives its
// interface` while pid 612421 held 10.50.2.1 — a true sentence about a
// narrower question than its reader was asking, because the interface had
// survived along with its service and only the network had died. The green
// line now claims exactly what was measured, no more.
//
// Independent of --vm on purpose: the leftover is a host fact, and it blocks a
// future machines-on run whatever mode this invocation asked about.
//
// TestDoctorNamesTheDHCPServiceThatOutlivedItsInterface and
// TestDoctorFailsOnTheDHCPServiceWhoseNetworkIsGone fail without this.
func checkLeftoverDHCP() check {
	leftovers, err := findLeftoverDHCP()
	if err != nil {
		return check{
			title:  "Could not look for leftover DHCP servers.",
			state:  verdictWarn,
			detail: err.Error(),
		}
	}
	if len(leftovers) == 0 {
		return check{
			title:   "No DHCP server of an earlier run outlives its network.",
			state:   verdictOK,
			verbose: "Checked: dnsmasq processes on interfaces this emulator names. A server of another owner shows only when a create fails on its block, and the create error names it.",
		}
	}
	held := make([]string, 0, len(leftovers))
	for _, leftover := range leftovers {
		held = append(held, leftover.String())
	}
	return check{
		title:    fmt.Sprintf("%d DHCP server(s) left by an earlier run still hold an address block feint will need.", len(leftovers)),
		state:    verdictFail,
		detail:   strings.Join(held, "; ") + ". The next run on that block dies on 'Address already in use'. The first command says which are yours without touching anything; the second ends yours; the third ends those of another user.",
		commands: []string{"feint clean --check", "feint clean", "sudo feint clean --vm <mode>"},
	}
}

// checkEnvHazards asks each mounted pack whether the operator's shell carries
// a value that would send that provider's clients to the real cloud no matter
// what `feint env` prints — OSC_PROFILE is the measured case (#286): with it
// set, the Outscale Terraform provider 1.1.x ignores OSC_ENDPOINT_API and a
// run that looks local reaches api.<region>.outscale.com. The variable names
// are provider knowledge and live in each pack; doctor only relays.
//
// Warnings, never failures: the shell may be configured that way on purpose,
// and a diagnostic that fails builds is a diagnostic people stop running.
func checkEnvHazards() []check {
	srv, _, err := newServer(nil)
	if err != nil {
		// Some other check owns reporting a server that cannot be built.
		return nil
	}
	var out []check
	for _, p := range srv.Packs() {
		hazards, ok := p.(packEnvHazards)
		if !ok {
			continue
		}
		warnings := hazards.EnvHazards(os.LookupEnv)
		if len(warnings) == 0 {
			// "nothing in this shell reroutes" claimed the shell; what was
			// measured is the pack's list of known variables (#342).
			out = append(out, check{
				title:   "No variable in this shell sends " + titleCase(p.Name()) + " clients to the real cloud.",
				state:   verdictOK,
				verbose: "This checks the variables measured to do it, not every way a shell can redirect a client.",
			})
			continue
		}
		for _, warning := range warnings {
			out = append(out, check{
				title: "This shell can send " + titleCase(p.Name()) + " clients to the real cloud.",
				state: verdictWarn,
				fix:   warning,
			})
		}
	}
	return out
}

// checkStackHazards reads the Terraform configuration around the operator —
// doctor is documented to run from the stack directory — and asks each pack
// whether its text names something measured to reach the real cloud no matter
// where the exports point. scaleway_object_* is the measured case (#262,
// #280): the provider hardcodes s3.<region>.scw.cloud for Object Storage, so a
// run that looks local sends those calls out with whatever credentials it
// holds, and the emulator cannot see a request that never arrives. The
// signatures are provider knowledge and live in each pack; doctor only walks
// the files and relays.
//
// Silence rules, in order: no Terraform files here means no rows at all — a
// doctor run from a home directory must not gain noise — and a stack carrying
// no signature gets one row saying what was checked, worded as the measured
// list it is rather than as a guarantee nobody can give.
// TestDoctorNamesTheObjectStorageEscape fails without the warning, and
// TestDoctorStaysSilentOffAStack without the silence.
func checkStackHazards() []check {
	dir, err := os.Getwd()
	if err != nil {
		return nil
	}
	config, files := stackConfig(dir)
	if files == 0 {
		return nil
	}
	srv, _, err := newServer(nil)
	if err != nil {
		// Some other check owns reporting a server that cannot be built.
		return nil
	}
	var out []check
	for _, p := range srv.Packs() {
		hazards, ok := p.(packStackHazards)
		if !ok {
			continue
		}
		for _, warning := range hazards.StackHazards(config) {
			out = append(out, check{
				title: "This Terraform configuration can reach the real " + p.Name() + " cloud.",
				state: verdictWarn,
				fix:   warning,
			})
		}
	}
	if len(out) == 0 {
		out = append(out, check{
			title:   fmt.Sprintf("None of the %d Terraform file(s) here uses a resource on the measured list of those that reach the real cloud.", files),
			state:   verdictOK,
			verbose: "This checks the measured list (docs/limits.md), not every path a client could compose.",
		})
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
		// "Could not look" was reported as an ok for years; a green mark on an
		// unasked question is the #342 family, so it warns now.
		return check{title: "Could not look for a ProxyJump in ~/.ssh/config.", state: verdictWarn, detail: err.Error()}
	}
	file, err := os.Open(filepath.Join(home, ".ssh", "config")) //nolint:gosec // the operator's own file
	if err != nil {
		if os.IsNotExist(err) {
			return check{title: "No ~/.ssh/config to get in the way.", state: verdictOK}
		}
		return check{title: "Could not look for a ProxyJump in ~/.ssh/config.", state: verdictWarn, detail: err.Error()}
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
						title:  "A ProxyJump applies to Host " + pattern + " in ~/.ssh/config.",
						state:  verdictWarn,
						detail: "It also catches the machines' private addresses, so ssh to them dies on 'timed out during banner exchange' while sshd is running. Narrow the Host pattern, or connect with ssh -F /dev/null as tools/conformance/scaleway/ssh.sh does.",
					}
				}
			}
		}
	}
	// The claim matches the measurement: two patterns are checked, not every
	// pattern ssh can compose ("10*" or "?0.*" would slip past) — #342.
	return check{
		title:   "No ProxyJump on Host * or 10.* in ~/.ssh/config.",
		state:   verdictOK,
		verbose: "This checks the two measured patterns, not every pattern ssh can compose.",
	}
}

// runQuiet runs a version probe, bounded: a binary on PATH that never exits
// must not hang doctor (the rule of #963). CHECKPOINT_DISABLE keeps terraform
// from calling HashiCorp's update service while it prints its version.
func runQuiet(binary string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...) //nolint:gosec // binary and args are this file's own
	cmd.Env = append(os.Environ(), "CHECKPOINT_DISABLE=1")
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

// checkLeftoverRunAuthorities reports the interception authorities `feint run`
// left on disk, which is the leftover detection #849 asks this command for.
//
// The verdict distinguishes a leftover from a run in flight, and that is the
// whole design. A healthy `feint run` owns such a directory for as long as its
// child lives, so reporting "a directory exists" would warn on every correct
// use and decide nothing — the kind of diagnostic this file's own header says
// people stop running. So each directory records its owning PID, and this reads
// it back: a live owner is reported as a run in progress, a dead one as a
// leftover to remove.
//
// A leftover is a warn and not a fail, deliberately: the only thing it holds is
// a public certificate inside an owner-only directory, so it leaks nothing and
// blocks nothing. What it costs is disk and puzzlement, which is a warning's
// job. The contrast with checkLeftoverDHCP is the point of both: that one fails
// because the next run DIES on the address block it holds.
//
// TestDoctorTellsALeftoverAuthorityFromARunInFlight fails without this.
//
// What enters the report is filtered at intake, because the report turns names
// read from a world-writable directory into commands a reader pastes and into
// paths --fix removes. Any user can create /tmp/feint-run-x;touch${IFS}PWNED;#
// or a name carrying an escape sequence. Only a name of the exact shape
// os.MkdirTemp("", runDirPrefix) produces, on a real directory (not a link),
// owned by this user with mode 0700, is considered (runDirIsOurs).
// TestDoctorIgnoresARunDirectoryItDidNotMake fails without that filter, and the
// printed paths are shell-quoted as a second line of defence (shellQuote).
func checkLeftoverRunAuthorities() check {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return check{title: "Could not look for leftover `feint run` directories.",
			state: verdictWarn, detail: err.Error()}
	}
	var stale, unknown, live []string
	for _, entry := range entries {
		path := filepath.Join(os.TempDir(), entry.Name())
		if runDirIsOurs(path, os.Getuid()) != nil {
			continue
		}
		switch alive, known := runOwnerState(path); {
		case alive:
			live = append(live, path)
		case !known:
			unknown = append(unknown, path)
		default:
			stale = append(stale, path)
		}
	}
	where := filepath.Join(os.TempDir(), runDirPrefix) + "<digits>"
	switch {
	case len(stale) == 0 && len(unknown) == 0 && len(live) == 0:
		return check{title: "No leftover `feint run` directory in " + os.TempDir() + ".", state: verdictOK,
			verbose: "Checked: " + where + ", yours, mode 0700. Directories of other users are not reported: they are not yours to remove."}
	case len(stale) == 0 && len(unknown) == 0:
		return check{title: fmt.Sprintf("%d `feint run` command(s) running; their directories go when they end.", len(live)),
			state: verdictOK, verbose: strings.Join(live, ", ")}
	case len(stale) > 0:
		// The exact paths, never the glob: a run that starts between this
		// report and the paste would match the glob and lose its directory.
		// TestDoctorTellsALeftoverAuthorityFromARunInFlight fails without it.
		// The notes about the other directories come before the sentence
		// that leads into the command: after it, they sat between "safe:" and
		// what it introduces. TestTheLeftoverNotesComeBeforeTheCommandSentence
		// fails when they follow it.
		detail := "They hold only a public certificate, so they leak nothing."
		if len(live) > 0 {
			detail += " " + moreOf(len(live), "belongs to a run still going, and is left alone.", "belong to a run still going, and are left alone.")
		}
		if len(unknown) > 0 {
			detail += " " + moreOf(len(unknown), "has no owner record, so doctor cannot tell whether its run has ended; it is left alone: ",
				"have no owner record, so doctor cannot tell whether their run has ended; they are left alone: ") + strings.Join(unknown, ", ") + "."
		}
		detail += " The process that made each one has exited, so removing them is safe:"
		return check{title: fmt.Sprintf("%d directories left in %s by `feint run` commands that have ended.", len(stale), os.TempDir()),
			state:    verdictWarn,
			detail:   detail,
			commands: []string{"rm -rf " + shellQuoteAll(stale)},
			fixKey:   "leftovers", paths: stale}
	default:
		return check{title: fmt.Sprintf("%d `feint run` directories in %s have no owner record.", len(unknown), os.TempDir()),
			state:    verdictWarn,
			detail:   "An older feint wrote them, so doctor cannot tell whether their run has ended. If the first command lists no `feint run`, the second removes them:",
			commands: []string{"pgrep -af 'feint run'", "rm -rf " + shellQuoteAll(unknown)}}
	}
}

// moreOf is "1 more <one>" or "N more <many>": the notes about the other
// leftovers agree in number. TestTheLeftoverNotesComeBeforeTheCommandSentence
// fails on "1 more belong".
func moreOf(n int, one, many string) string {
	if n == 1 {
		return "1 more " + one
	}
	return fmt.Sprintf("%d more %s", n, many)
}

// runDirName is the exact shape os.MkdirTemp("", runDirPrefix) gives a
// directory: the prefix, then the decimal digits of a random number.
var runDirName = regexp.MustCompile(`^` + regexp.QuoteMeta(runDirPrefix) + `[0-9]+$`)

// runDirIsOurs answers whether path is a directory `feint run` could have made
// for the user uid: the name MkdirTemp gives, a directory and not a link, owned
// by uid, mode 0700. Anything else is refused with the reason, whatever it
// holds. It is asked at intake by the report and again by --fix just before it
// removes anything, since /tmp can change between the two.
func runDirIsOurs(path string, uid int) error {
	if !runDirName.MatchString(filepath.Base(path)) {
		return fmt.Errorf("%q is not a name feint run gives its directories", filepath.Base(path))
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%s has mode %o, feint run makes 700", path, info.Mode().Perm())
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != uid {
		return fmt.Errorf("%s does not belong to you", path)
	}
	return nil
}

// shellQuote makes a value one word for a POSIX shell. A value made only of
// characters no shell treats specially is left as it is, so the common path
// reads plainly; anything else goes in single quotes.
func shellQuote(value string) string {
	if value != "" && strings.Trim(value, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./-") == "" {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func shellQuoteAll(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = shellQuote(v)
	}
	return strings.Join(quoted, " ")
}

// runOwnerState answers whether the process that created this directory is
// still running, and whether the directory said who that was. An absent or
// malformed owner file is "unknown", not "dead": before #974 it counted as
// dead, and the remedy then offered to delete the directory of a run an older
// version had started — on 2026-11-15 one was in flight.
func runOwnerState(dir string) (alive, known bool) {
	raw, err := os.ReadFile(filepath.Join(dir, runOwnerFile)) //nolint:gosec // a path this command composed from TempDir and its own prefix
	if err != nil {
		return false, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return false, false
	}
	return runOwnerAlive(dir), true
}

// runOwnerAlive answers whether the process that created this directory is
// still running. An unreadable or malformed owner file counts as DEAD, which is
// the useful direction: the alternative would hide a leftover written by an
// older version behind a missing file, and a leftover reported wrongly as live
// is a leftover nobody removes.
func runOwnerAlive(dir string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, runOwnerFile)) //nolint:gosec // a path this command composed from TempDir and its own prefix
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 asks the kernel the question without delivering anything, and is
	// the portable way to do it. A process this user may not signal answers
	// EPERM rather than ESRCH, and that is still alive.
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, os.ErrPermission)
}
