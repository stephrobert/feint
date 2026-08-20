// Package cli wires the feint commands.
//
// No CLI framework: a dozen subcommands do not justify a dependency, and the
// standard flag package keeps the binary free of supply-chain surface.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/drift"
	"github.com/stephrobert/feint/internal/providers/exoscale"
	"github.com/stephrobert/feint/internal/providers/outscale"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// Version is stamped at build time with -ldflags "-X ...cli.Version=v0.1.0".
// It stays "dev" for a `go build` from a checkout, which is correct there and
// wrong for everybody else: released() answers what to print.
var Version = "dev"

// released reports the version to print.
//
// A binary installed with `go install ...@latest` carries no ldflags, so it used
// to answer "dev" to every user who had not built it themselves — the one
// question a bug report always contains, answered uselessly. The module version
// the toolchain records in the binary is the honest answer there, and the
// standard library exposes it, so this needs no dependency.
//
// Order matters: a stamped version wins, because a release build knows its own
// tag and the build info of a binary built from a checkout says "(devel)".
func released() string {
	if Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return Version
}

// DefaultAddr is the single port every provider is served on. One endpoint for
// three clouds is the whole point of the multi-provider design.
//
// The host is 127.0.0.1, not empty, and that is a security decision rather than
// a preference. This emulator accepts every credential without checking one, and
// with --vm it starts containers on the host with the operator's privileges. A
// bare ":4599" listens on every interface, so a laptop on a café network was
// offering a container executor to it.
//
// SECURITY.md already told readers that serve binds loopback by default; it was
// describing the behaviour this constant should always have had rather than the
// one it did. The fix is the code, not the sentence. An operator who genuinely
// wants to expose it says so with --addr, which is a decision they can be held
// to, where a default is one nobody made.
const DefaultAddr = "127.0.0.1:4599"

// Exit codes, documented because CI depends on them:
//
//	0  success
//	1  usage or runtime error
//	2  drift detected (coverage --fail-on-unknown found undecided operations)
const (
	exitOK    = 0
	exitError = 1
	exitDrift = 2
)

// cliSurfaceVersion names the version of the CLI surface a CI is allowed to
// depend on: the verbs, the flags each verb's flag.FlagSet registers, and the
// exit codes above. It moves when any of those move — additions included,
// because the number means "the surface changed", not "the surface broke"; the
// CHANGELOG says which of the two it was.
//
// Version 5 is where the observation stopped being a parse of the rendered help
// and became a read of the FlagSets themselves (#334). What moved is the
// observation, not the binary: --intercept and --expose-to-network were already
// accepted by proxy, --shapes by serve, --check by version, and the six serve
// flags by start, while --version and -v were never flags of the version verb
// at all. The entries are keyed by flag set, so snapshot now appears as
// `snapshot save`, `snapshot load` and `snapshot list`, which is what says that
// --force belongs to save alone.
//
// Version 7 adds the pair 0.11 is built around, and both are additions: the
// verb `replay` with --endpoint, --format and --timeout (#73), and
// `coverage --observed` (#74). Nothing was removed and no exit code moved, so a
// pipeline keyed on version 6 keeps working — the number says the surface
// changed, and the CHANGELOG says which of the two it was.
//
// The surface itself is frozen in testdata/frozen/cli.json, compared by
// TestTheFrozenSurfacesStillMatchTheirFixture, and a fixture regenerated
// without bumping this constant fails TestASurfaceChangeDemandsItsVersionBump.
// The procedure for a deliberate change is in RELEASING.md ("Frozen surfaces").
const cliSurfaceVersion = 7

// Run executes one command and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		usage(stderr)
		return exitError
	}

	// The banner goes to stderr and only to a terminal, so it never reaches a
	// pipe, an eval or a CI log. Printed before dispatch so every subcommand
	// gets it, the way the sibling projects do.
	banner(stderr, released())

	switch args[1] {
	case "serve":
		return run(stderr, func() error { return serve(args[2:], stdout) })
	case "coverage":
		return coverage(args[2:], stdout, stderr)
	case "catalog":
		return run(stderr, func() error { return catalog(args[2:], stdout) })
	case "clean":
		return run(stderr, func() error { return clean(args[2:], stdout) })
	case "probe":
		return probeCommand(args[2:], stdout, stderr)
	case "proxy":
		return proxyCommand(args[2:], stdout, stderr)
	case "transcript":
		return transcriptCommand(args[2:], stdout, stderr)
	case "replay":
		return replayCommand(args[2:], stdout, stderr)
	case "shapes":
		return shapesCommand(args[2:], stdout, stderr)
	case "evidence":
		return evidence(args[2:], stdout, stderr)
	case "docs":
		return docs(args[2:], stdout, stderr)
	case "start":
		return start(args[2:], stdout, stderr)
	case "stop":
		return stop(args[2:], stdout, stderr)
	case "restart":
		return restart(args[2:], stdout, stderr)
	case "wait":
		return waitCommand(args[2:], stdout, stderr)
	case "status":
		return status(args[2:], stdout, stderr)
	case "ui":
		return uiCommand(args[2:], stdout, stderr)
	case "logs":
		return logs(args[2:], stdout, stderr)
	case "env":
		return envCommand(args[2:], stdout, stderr)
	case "doctor":
		return doctor(args[2:], stdout, stderr)
	case "images":
		return images(args[2:], stdout, stderr)
	case "snapshot":
		return snapshot(args[2:], stdout, stderr)
	case "version", "--version", "-v":
		// The flag forms are aliases, not a second way of doing it: a user who
		// types `feint --version` and gets "unknown command" concludes the
		// binary is broken, and they are not wrong to.
		//
		// `--check` asks GitHub whether a newer release exists. Asked for, never
		// volunteered: nothing here reaches the network unless a user typed the
		// flag, which is what keeps "no account, no telemetry, ever" true.
		return versionCheck(args[2:], stdout, stderr)
	case "-h", "--help", "help":
		usage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[1])
		usage(stderr)
		return exitError
	}
}

func run(stderr io.Writer, fn func() error) int {
	if err := fn(); err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	return exitOK
}

func usage(w io.Writer) {
	fmt.Fprint(w, `feint — a local emulator for European clouds (Scaleway, Outscale, Exoscale)

Usage:
  feint serve      [--addr 127.0.0.1:4599] [--state <file>] [--vm off|incus|incus-vm|incus-ovn|auto]
                    [--cleanup] [--contracts <dir>] [--coverage <dir>] [--shapes <dir>]
                    [--log-level info|debug] [--expose-to-network]
                    Serve the three emulated clouds on one port, in the
                    foreground. --expose-to-network is the only way off
                    loopback, and it disarms the anti-rebinding guard: this
                    emulator accepts every credential and, under --vm, starts
                    containers with your privileges. Read SECURITY.md first.

  feint start      [--addr :4599] [--state <file>] [--vm off|incus|incus-vm|incus-ovn|auto]
                    [--cleanup] [--contracts <dir>] [--log-level info|debug]
                    [--timeout 30s] [--detach] [--foreground]
                    Same, detached: records the instance, waits until it
                    answers, prints where the log is. Refuses to adopt an
                    instance already running on that address. The serve flags
                    repeated here are exactly the ones a restart replays: the
                    coverage, shapes and expose-to-network flags are serve's
                    alone, and this verb refuses them. A dashed name in a block
                    is a flag of that block's verb, which is what lets the help
                    be held against the binary.

  feint stop       [--addr :4599] [--timeout 15s]
                    SIGTERM, then SIGKILL if it has to, and say which. Never
                    signals a pid that stopped being feint.

  feint restart    [--addr :4599] [--timeout 30s]
                    Stop, then start again with the flags that were recorded.

  feint wait       [--addr :4599] [--timeout 30s]
                    Poll until the emulator answers. The CI verb: it replaces
                    the hand-rolled curl loop every script had.

  feint status     [--addr :4599] [--coverage <dir>] [--format text|json]
                    What is running, what it mounts, what a real client has
                    driven this run, and how much of the upstream surface is
                    served.

  feint ui         [--addr :4599] [--print]
                    Open the emulator's own page: served against driven against
                    probed, the gap with the upstream API, and a live log of the
                    calls. Read-only, and served on loopback only.

  feint logs       [--addr :4599] [-n 50] [-f]
                    The detached run's log.

  feint doctor     [--addr :4599] [--vm auto]
                    Diagnose the host: the port, the machine runtime and what it
                    can prove, the clients, and the ssh trap. A warning never
                    fails; only a broken thing does.

  feint images     [--vm auto] [--only ubuntu/24.04] [--check]
                    Build the machine images, which carry an ssh daemon so a
                    machine answers on port 22 without reaching a package
                    repository. No upstream image ships one. --check reports
                    what is missing and exits 2, building nothing.

  feint env <provider> [--shell bash|fish|powershell] [--endpoint <url>] [--unset]
                       [--client <family>]
                    The environment a real client of that provider needs.
                    Exports on stdout, everything else on stderr, so
                    eval "$(feint env scaleway)" is safe. --client selects a
                    family when a provider's clients disagree about a value:
                    outscale serves terraform (>= 1.7, the default) and
                    oapi-cli / terraform-1.1, which want the bare host.

  feint snapshot   save <name> [--addr :4599] [--force]
                   load <name> [--addr :4599]
                   list [--format text|json]
                   rm <name>
                    Name the state of a running emulator and come back to it.
                    Same bytes the serve state file holds, taken mid-run rather
                    than at exit. Loading replaces the store: a fixture must not
                    depend on what the session did before it.

  feint coverage   (--sdk <dir> | --contract <file>) [--provider scaleway|outscale|exoscale]
                    [--products <a,b,c>] [--format text|json|triage|list]
                    [--baseline <file> [--write-baseline]] [--fail-on-unknown]
                    [--artefact <file>] [--observed <recording.jsonl|dir>]
                    Compare the upstream API surface with what the packs serve.
                    --artefact compares the committed coverage artefact against
                    what the pack declares today — statuses and decline reasons —
                    and exits 2 on any skew, so an edited reason cannot outlive
                    itself in the versioned copy.
                    --observed reads proxy recordings and ranks what the packs
                    decline by how often a real client called it anyway — the
                    demand a written reason cannot carry. It needs --contract,
                    because a declined operation has no route to name it by, and
                    it renders instead of --format's own report so the committed
                    artefact cannot gain a key. An operation nobody called and
                    one nobody triaged are counted separately and never summed.
                    Scaleway and Outscale are read from an SDK checkout; Exoscale
                    publishes an OpenAPI document, so it is read with --contract.
                    Given both, the SDK lists the operations and the contract adds
                    the upstream's own grouping, which the SDK flattens away.

  feint probe      [--endpoint http://127.0.0.1:4599] [--contracts <dir>] [--provider <name>]
                    Drive every mounted route from its API description and check
                    the answers. Proves the protocol, never the behaviour.

  feint proxy      --upstream <url> --record <file.jsonl> [--addr 127.0.0.1:4600]
                    [--provider <name>] [--max-body <bytes>] [--queue <n>]
                    [--intercept <host,host>] [--forward <host,...>]
                    [--expose-to-network]
                    Sit between a real client and a real cloud and write down
                    every exchange, as JSON Lines, one object per call, with the
                    upstream operation named. Credentials are redacted before
                    anything is written. Point the client at --addr and drive it
                    as usual. Two ways reach a client you cannot redirect by
                    configuration. --intercept serves HTTPS with a locally-minted
                    certificate for those hostnames, so a client redirected here
                    by name trusts the proxy and lands on it. --forward records a
                    client whose endpoint is compiled in: it accepts CONNECT for
                    the hosts you name, terminates the TLS with a certificate
                    minted for the run, and needs nothing of the client but
                    HTTPS_PROXY and SSL_CERT_FILE. Loopback only, in every mode;
                    docs/proxy.md says what each costs, as does
                    --expose-to-network, which puts this port and the account
                    behind it on the network.

  feint transcript <recording.jsonl> [--shape OP [--against emu.jsonl]] [--format text|json]
                    Read a proxy recording and answer what to serve next. With no
                    flag, the operations a real client called that no pack serves,
                    most-called first. With --shape, the response shape one
                    operation actually returned. With --against, diff that shape
                    against the emulator's own answer: the fields it omits.

  feint replay     <recording.jsonl> [--endpoint http://127.0.0.1:4599]
                   [--format text|json] [--timeout 30s]
                    Reissue every recorded request at a running emulator and
                    compare the two answers, operation by operation. The status
                    is exact, the fields and their types are exact minus what a
                    pack declines, and values and ordering are compared only
                    where a pack declares them comparable. Identifiers are
                    rebound to the ones this emulator mints, so a recording
                    replays against a fresh instance. Nothing from the recording
                    is printed: a finding names a path, a type and a position.
                    Exit 2 on a divergence; an operation no route serves is
                    reported and never counted against the verdict.

  feint shapes     <recording.jsonl> --provider <name> [--dir shapes] [--dry-run]
                   [--record [--profile <name>]] [--check]
                    The field trees a real cloud returns, versioned. Paths and
                    JSON types only, never a value or an identifier, which is
                    what makes them committable where a transcript is not.
                    --record reads a real account directly, and reads only.
                    --check compares this emulator with what was recorded and
                    exits 2 on a field the cloud returns and it omits.

  feint evidence   [--endpoint http://127.0.0.1:4599] [--shapes shapes]
                   [--contracts contracts] [--suites tools/conformance]
                   [--out coverage/evidence.json] [--join <other.json>]
                   [--allow-narrowing]
                    Write the per-operation evidence record from a running
                    emulator's /_feint/conformance: which independent proofs
                    each operation has earned, side by side, never summed.
                    --contracts and --suites are digested into the record's
                    provenance. --join merges the other leg of the same
                    regeneration, and --allow-narrowing is what a run reaching
                    fewer runtimes than the record it replaces has to say out
                    loud before it may overwrite it.

  feint docs       [--file README.md] [--coverage <dir>] [--contracts <dir>] [--check]
                    [--limits <file>] [--routes <file>] [--confidence <file>]
                    [--install <file>] [--proved <file>] [--workflow <file>]
                    [--client-pins <file>] [--screenshots <file>]
                    [--ui-manifest <file>]
                    Regenerate the coverage tables in a Markdown file. Each of
                    the page flags names the document holding one generated
                    block and takes an empty value to skip it; --check writes
                    nothing and exits 2 when a block is out of date.
                    --ui-manifest only records the page's digest beside the
                    screenshots, and regenerates nothing.

  feint catalog    [--format json]
                    Print the emulated inventory a client reads before creating.

  feint clean      [--vm incus|incus-vm|incus-ovn]
                    Remove every machine, network and rule set the emulator
                    created. Labelled resources only; nothing else is touched.

  feint version    [--check]
                    Print the version. --check asks GitHub whether a newer
                    release exists; nothing here reaches the network unless
                    that flag is typed.

Typing feint with the version flag, in either spelling, is an alias for the
version verb rather than a flag of it.

The lifecycle verbs are Unix only: detaching uses setsid, which Windows has no
equivalent for, and the released binaries are linux and darwin.

Exit codes: 0 ok, 1 error, 2 drift detected (coverage, or docs --check).
A wait that times out is 1, not a fourth code.

Every one of these is also a mise task: run "mise tasks" to list them.
`)
}

// machineDriver resolves the --vm flag.
//
//	off       metadata only: servers change state, nothing runs (default)
//	incus     backed by an Incus system container
//	incus-vm  backed by a real KVM virtual machine, with its own kernel
//	incus-ovn containers on OVN networks: subnets isolated by construction
//	auto      the most capable runtime that actually answers, off otherwise
//
// The default is off on purpose: starting machines is a side effect on the
// operator's host, and it must be asked for.
//
// Incus is the only runtime, and that is a decision rather than an omission:
// emulating a cloud means emulating its network, which needs managed bridges
// carrying a chosen block, fixed addresses on a NIC, and enforceable ACLs.
// docs/limits.md records what the Docker driver could not do here before it was
// removed. incus is the fast path (a system container, seconds); incus-vm is the
// faithful one (a real kernel, tens of seconds), which is what a test touching
// sysctl, kernel modules or the boot path needs. incus-ovn is the one whose
// subnets are actually separate.
//
// auto tries OVN first, and that ordering is deliberate. It used to try the
// bridge alone, on the argument that the OVN wiring cannot be guessed from a
// daemon that answers — which was true of the daemon and false of the question.
// The consequence was backwards: an operator who had installed ovn-central,
// ovn-host and Open vSwitch got the one mode that cannot isolate two VPCs, and
// nothing told them. Isolation is the emulator's strongest claim; auto should
// not silently decline it.
//
// The guess is cheap to make safe: the OVN driver's own availability check
// creates nothing, so a probe that fails costs one call and falls through to the
// bridge. What is chosen is printed, because a runtime selected in silence is a
// runtime nobody can reason about.
func machineDriver(mode string, stdout io.Writer) (machine.Driver, error) {
	ctx := context.Background()

	// verify asks the host what it delivers, once, before anything is published.
	//
	// Before #181 a mode's capabilities were a function of the flag: NewIncusOVN
	// set OVN and `isolation: true` followed, on a host with no OVN wiring at
	// all. Measured on 2026-08-15: `--vm auto` chose incus-ovn on an ordinary
	// Incus 7.2 host and /_feint/health published isolation until the first
	// network creation failed and blamed the address block.
	//
	// The narrowing is announced rather than silent, because a capability that
	// quietly drops is how a suite starts skipping what it used to assert.
	verify := func(d machine.Driver) []string {
		v, ok := d.(interface {
			Verify(context.Context) (machine.Capabilities, []string)
		})
		if !ok {
			return nil
		}
		_, unmet := v.Verify(ctx)
		return unmet
	}

	requested := func(d machine.Driver) (machine.Driver, error) {
		if !d.Available(ctx) {
			return nil, fmt.Errorf("--vm %s requested but the Incus daemon does not answer", mode)
		}
		// Asked for by name, and the host cannot serve it: refuse at startup
		// naming the missing half, the same shape as the line above. Accepting
		// it would publish a capability the first create disproves, and blame
		// the client for it.
		if unmet := verify(d); len(unmet) > 0 {
			return nil, fmt.Errorf("--vm %s requested but this host cannot deliver it:\n  %s",
				mode, strings.Join(unmet, "\n  "))
		}
		return d, nil
	}

	switch mode {
	case "off", "none", "":
		return machine.Noop{}, nil
	case "incus":
		return requested(machine.NewIncus())
	case "incus-vm", "kvm":
		return requested(machine.NewIncusVM())
	case "incus-ovn", "ovn":
		return requested(machine.NewIncusOVN())
	case "auto":
		// Most capable first. Never incus-vm: a virtual machine costs tens of
		// seconds to boot where a container costs seconds, and that is a trade
		// an operator makes on purpose rather than one auto makes for them.
		for _, d := range []machine.Driver{machine.NewIncusOVN(), machine.NewIncus()} {
			if !d.Available(ctx) {
				continue
			}
			// The fall-through the comment above always claimed. It could never
			// trigger while Available was one `incus list` for all three modes:
			// a mode whose defining capability the host cannot deliver is passed
			// over, so the ordinary host that never installed OVN lands on the
			// bridge that works instead of on a promise that does not.
			declared := machine.CapabilitiesOf(d)
			unmet := verify(d)
			caps := machine.CapabilitiesOf(d)
			if declared.Isolation && !caps.Isolation {
				// Isolation is the only reason this mode is tried first, so a
				// host that cannot deliver it gets the next mode rather than
				// this one with its reason removed. Said out loud: a runtime
				// chosen differently from what the operator would expect is
				// exactly the kind of decision that must not be silent.
				fmt.Fprintf(stdout, "%s passed over: %s\n", d.Name(), strings.Join(unmet, "; "))
				continue
			}
			fmt.Fprintf(stdout, "machine runtime: %s (isolation: %v)\n", d.Name(), caps.Isolation)
			for _, why := range unmet {
				fmt.Fprintf(stdout, "  the host narrowed this: %s\n", why)
			}
			if !caps.Isolation {
				fmt.Fprintln(stdout,
					"  subnets of different VPCs will reach each other; install ovn-central and ovn-host for isolation")
			}
			return d, nil
		}
		fmt.Fprintln(stdout, "no machine runtime available, falling back to metadata-only machines")
		return machine.Noop{}, nil
	default:
		return nil, fmt.Errorf("unknown --vm mode %q (off, incus, incus-vm, incus-ovn, auto)", mode)
	}
}

// loadContracts reads every API description in a directory, keyed by the
// provider each one names.
//
// The provider comes from the file's own contents rather than its name, so a
// renamed artefact cannot silently check one provider's responses against
// another's schemas — which would report violations everywhere and be blamed on
// the emulator.
func loadContracts(dir string) (map[string]*contract.Doc, error) {
	entries, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("read contracts at %s: %w", dir, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no contract under %s: nothing would be checked", dir)
	}

	docs := make(map[string]*contract.Doc, len(entries))
	for _, path := range entries {
		doc, err := contract.Load(path)
		if err != nil {
			return nil, err
		}
		if doc.Provider == "" {
			return nil, fmt.Errorf("%s names no provider", path)
		}
		docs[doc.Provider] = doc
	}
	return docs, nil
}

// packsFor is which packs the emulator mounts. A variable rather than a literal
// so a test can hand over a pack of its own — the hardwired list was what made
// coverage()'s own gate untestable, and an audit named it three times before it
// was worth the seam. Production never assigns it.
//
// FEINT_OUTSCALE_REGION selects the Outscale region, because at Outscale a
// region is a property of the deployment — which endpoint a client points at —
// never of the API surface, and #269 had frozen it into a constant (#290). An
// environment variable rather than a flag because every entry point that
// mounts packs (serve, probe, proxy, shapes) must agree on it without each
// growing an option, and it is read here, in the composition root, so the
// packs stay constructor-configured and the core learns no provider name.
// Unset keeps eu-west-2, exactly the previous behaviour; a region Outscale
// does not publish refuses to serve rather than silently answering the
// default, which would be #268's lie moved to startup.
// TestAnUnknownOutscaleRegionRefusesToServe fails without the refusal.
//
// FEINT_EXOSCALE_ZONE is the same knob for the Exoscale zone (#278): at
// Exoscale a zone is a property of the endpoint a client points at
// (api-<zone>.exoscale.com), so the emulator's single endpoint serves one
// zone per process, chosen here. Unset keeps ch-dk-2, the official CLI's own
// default; a zone Exoscale does not publish refuses to serve, naming the
// accepted list. TestAnUnknownExoscaleZoneRefusesToServe fails without the
// refusal.
var packsFor = func(env *emulator.Env) ([]emulator.Pack, error) {
	osc := outscale.New(env)
	if region := os.Getenv("FEINT_OUTSCALE_REGION"); region != "" {
		var err error
		if osc, err = outscale.NewInRegion(env, region); err != nil {
			return nil, fmt.Errorf("FEINT_OUTSCALE_REGION: %w", err)
		}
	}
	exo := exoscale.New(env)
	if zone := os.Getenv("FEINT_EXOSCALE_ZONE"); zone != "" {
		var err error
		if exo, err = exoscale.NewInZone(env, zone); err != nil {
			return nil, fmt.Errorf("FEINT_EXOSCALE_ZONE: %w", err)
		}
	}
	return []emulator.Pack{scaleway.New(env), osc, exo}, nil
}

// newServer builds the emulator with every pack mounted. With contracts, every
// response is checked against the provider's own API description.
func newServer(contracts map[string]*contract.Doc) (*emulator.Server, *emulator.Env, error) {
	env := emulator.DefaultEnv()
	env.Contracts = contracts
	packs, err := packsFor(env)
	if err != nil {
		return nil, nil, err
	}
	srv, err := emulator.NewServer(env, packs...)
	if err != nil {
		return nil, nil, err
	}
	return srv, env, nil
}

// checkListenAddr refuses an address that would serve with the browser guard
// disarmed, unless the operator asked for exactly that.
//
// Off loopback, the anti-rebinding guard stops refusing anything: measured, a
// page on another origin gets 200 where it gets 403 on 127.0.0.1, and so does a
// forged Host. With --vm on, what is then reachable from the network is a
// container runtime — and store.Restore validates nothing, so a forged state is
// one request away from naming a machine on the host. The old behaviour was to
// print "feint dev listening on 0.0.0.0:4599" and say nothing else.
//
// It is a function rather than a branch inside serve because of what falsifying
// it showed: the first test drove `serve` itself, and with the refusal removed,
// serve did its job — it listened, and the test never returned. A test that
// hangs when its fix is deleted proves nothing and blocks every test behind it.
// The decision is therefore separated from the act, and only the decision is
// tested.
//
// TestServeRefusesANonLoopbackAddress fails without this.
func checkListenAddr(addr string, expose bool) error {
	if emulator.LoopbackListen(addr) || expose {
		return nil
	}
	return fmt.Errorf("refusing to listen on %s: off loopback the browser guard is disarmed, "+
		"so any page the operator visits can drive this emulator — and with --vm, start containers. "+
		"Pass --expose-to-network if that is what you want", addr)
}

// checkAddrUnclaimed refuses to serve on an address where an emulator already
// answers, naming the process instead of losing the fact in a bind error.
//
// The bind error alone was the failure mode #309 measured: a wrapper that
// spawns `serve` and then polls health takes the incumbent's answer as its
// child's, and the bind error dies with the child, unread in a log. Refusing
// here puts the incumbent's pid and start time where the operator is looking,
// and does so identically for `serve` in a terminal and for the detached child
// of `start`. Same shape as checkListenAddr, and for the reason that function
// documents: the decision is separated from the act, and only the decision is
// tested. TestServeRefusesAnAddressAnotherEmulatorClaims fails without it.
func checkAddrUnclaimed(addr string) error {
	id, ok := probeIdentity(addr, 500*time.Millisecond)
	if !ok {
		return nil
	}
	return fmt.Errorf("refusing to serve on %s: it is already served by %s. "+
		"Stop it first (feint stop --addr %s, or kill it), or pick another address",
		addr, describeForeign(id), addr)
}

func serve(args []string, stdout io.Writer) error {
	fs := newFlagSet("serve")
	addr := fs.String("addr", DefaultAddr, "listen address")
	state := fs.String("state", "", "load and persist the store to this JSON file")
	vm := fs.String("vm", "off", "back powered-on servers with real machines: off, incus, incus-vm, incus-ovn, auto")
	cleanup := fs.Bool("cleanup", false, "remove the machines and networks this run created before exiting")
	logLevel := fs.String("log-level", "info", "log verbosity: error, warn, info, debug")
	contracts := fs.String("contracts", "", "directory of API contracts; every response is checked against them and /_feint/conformance reports what failed")
	shapesDir := fs.String("shapes", "shapes", "directory of observed real-cloud shapes; the evidence record's shape axis reads it (empty to disable)")
	coverageDir := fs.String("coverage", "coverage", "directory holding the versioned coverage artefacts the page reads")
	expose := fs.Bool("expose-to-network", false, "listen off loopback, which disarms the browser guard: read what it costs before setting it")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if err := checkListenAddr(*addr, *expose); err != nil {
		return err
	}
	if err := checkAddrUnclaimed(*addr); err != nil {
		return err
	}

	level, err := parseLogLevel(*logLevel)
	if err != nil {
		return err
	}

	var docs map[string]*contract.Doc
	if *contracts != "" {
		docs, err = loadContracts(*contracts)
		if err != nil {
			return err
		}
	}

	srv, env, err := newServer(docs)
	if err != nil {
		return err
	}

	// The shape axis of the evidence record, resolved from the observed
	// catalogues when they are in reach. An installed binary has no shapes/
	// directory and the record then answers "unknown" — which is a different
	// fact from "unobserved", and the difference is the point.
	if *shapesDir != "" {
		covered, err := shapeCoveredOperations(*shapesDir)
		if err != nil {
			return err
		}
		if covered != nil {
			srv.SetShapeCovered(sortedKeys(covered))
		}
		// The corroboration half of the omission check (#88): which fields a
		// real cloud's recorded answers carried, per mounted operation. Without
		// it the check publishes every declared-but-absent field as unconfirmed
		// and fails on none — an installed binary without shapes/ loses the
		// verdict, never invents one.
		observed, err := observedFieldsByOperation(*shapesDir, srv)
		if err != nil {
			return err
		}
		if observed != nil {
			srv.SetObservedFields(observed)
		}
	}

	driver, err := machineDriver(*vm, stdout)
	if err != nil {
		return err
	}
	env.Machines = driver
	// Set after newServer, which cannot know the flag. At debug the runtime's
	// own lifecycle events come through, which is what makes a machine that
	// will not start explainable without leaving the emulator's log.
	env.Log = slog.New(slog.NewTextHandler(stdout, &slog.HandlerOptions{Level: level}))

	// What the runtime knows, the emulator says. Without this an operator debugs
	// a machine that will not start by reading the daemon's own log, which is
	// exactly the step nobody thinks of taking.
	// What a previous life left on the runtime, said before this one serves
	// beside it. The policy and its boundaries live in leftovers.go.
	reportLeftovers(driver, env.Log)

	watchCtx, stopWatching := context.WithCancel(context.Background())
	defer stopWatching()
	if watcher, ok := driver.(machine.Watcher); ok {
		if events, err := watcher.Watch(watchCtx); err == nil {
			go reportRuntimeEvents(events, env.Log, srv)
		} else {
			env.Log.Warn("could not watch the machine runtime", "error", err)
		}
	}

	if *state != "" {
		f, err := os.Open(*state) //nolint:gosec // operator-supplied path, by design
		switch {
		case err == nil:
			err = env.Store.Restore(f)
			_ = f.Close()
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "restored %d resources from %s\n", env.Store.Len(), *state)
		case !errors.Is(err, os.ErrNotExist):
			return fmt.Errorf("open state file: %w", err)
		}
	}

	httpSrv := &http.Server{
		Addr: *addr,
		// Wrapped here rather than in the server: this is where the listen
		// address is known, and the guard's whole job is to compare what the
		// caller asked for against what this process is bound to.
		Handler:           emulator.GuardRebinding(srv.Handler(), *addr),
		ReadHeaderTimeout: 10 * time.Second,
		// A client that opens a response and stops reading it holds a handler
		// goroutine, and before the store learned to encode outside its lock,
		// held the whole emulator with it. The lock is fixed; the timeout is the
		// second line, and it costs nothing here because every response this
		// emulator produces is small and local.
		WriteTimeout: 60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Mounted here, where the listen address is known, and only when that address
	// keeps the emulator on this machine. Off loopback the page does not exist:
	// the browser guard can no longer tell a local page from a hostile one, and a
	// dashboard that drives a container runtime is not something to leave
	// reachable by name. TestThePageIsNotServedOffLoopback in the emulator
	// package fails without that.
	page := srv.MountUI(emulator.UI{Addr: *addr, Version: released(), Upstream: upstreamGap(*coverageDir)})

	errs := make(chan error, 1)
	go func() {
		fmt.Fprintf(stdout, "feint %s listening on %s\n", Version, *addr)
		for _, p := range srv.Packs() {
			fmt.Fprintf(stdout, "  %-9s %d routes\n", p.Name(), len(p.Routes()))
		}
		fmt.Fprintf(stdout, "  machines  %s\n", env.Machines.Name())
		if page {
			fmt.Fprintf(stdout, "  page      http://%s/_feint/ui\n", *addr)
		}
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	// Swept before the state is written: what the runtime no longer holds must
	// not be described as running in a snapshot the next run restores.
	if *cleanup {
		if pruner, ok := driver.(machine.Pruner); ok {
			pruned, err := pruner.Prune(context.Background())
			fmt.Fprintf(stdout, "cleanup: removed %d machine(s), %d network(s), %d rule set(s)\n",
				pruned.Machines, pruned.Networks, pruned.Firewalls)
			if err != nil {
				fmt.Fprintf(stdout, "cleanup: %v\n", err)
			}
		}
	}

	if *state != "" {
		f, err := os.OpenFile(*state, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // operator-supplied path, by design
		if err != nil {
			return fmt.Errorf("write state file: %w", err)
		}
		if err := env.Store.Snapshot(f); err != nil {
			_ = f.Close()
			return err
		}
		// Checked, not deferred: the snapshot is the session, and announcing
		// "saved" over a truncated file loses it silently.
		if err := f.Close(); err != nil {
			return fmt.Errorf("write state file %s: %w", *state, err)
		}
		fmt.Fprintf(stdout, "saved %d resources to %s\n", env.Store.Len(), *state)
	}
	return nil
}

// coverage compares the upstream SDK surface with what the packs serve. This is
// the anti-drift gate: run it in CI after every SDK bump.
func coverage(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("coverage")
	sdk := fs.String("sdk", "", "path to a checkout of the provider Go SDK")
	contractPath := fs.String("contract", "", "read the upstream surface from a contract artefact instead of an SDK checkout")
	provider := fs.String("provider", scaleway.Name, "provider to report on")
	format := fs.String("format", "text", "output format: text, json, triage or list")
	products := fs.String("products", "", "comma-separated upstream products to restrict the report to (e.g. instance,rdb)")
	failOnUnknown := fs.Bool("fail-on-unknown", false, "exit 2 when an upstream operation is neither served nor declined")
	baseline := fs.String("baseline", "", "compare the unknown operations against this baseline file and exit 2 on new ones")
	writeBaseline := fs.Bool("write-baseline", false, "rewrite the baseline file from the current upstream surface")
	artefact := fs.String("artefact", "", "compare this committed coverage artefact against what the pack declares and exit 2 on any skew")
	observed := fs.String("observed", "", "a proxy recording, or a directory of them: rank what the packs decline by how often a real client called it")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if *sdk == "" && *contractPath == "" {
		fmt.Fprintln(stderr, "feint: one of --sdk (a provider SDK checkout) or --contract (a contract artefact) is required")
		return exitError
	}
	// One scanner per provider, because their SDKs share no shape: Scaleway
	// generates a package per product and version from its own IDL, Outscale
	// generates one client per service from OpenAPI. A map rather than a switch,
	// so the refusal below lists what is registered instead of repeating it: the
	// old message named two providers in its own words, which is the kind of
	// sentence that stays true until the third scanner lands and then lies.
	scanners := map[string]func(string) ([]drift.Operation, error){
		scaleway.Name: drift.ScanScalewaySDK,
		outscale.Name: drift.ScanOutscaleSDK,
	}
	var (
		upstream []drift.Operation
		err      error
		// Kept rather than discarded once the scan is done: --observed names the
		// operation a recorded call addressed, and only the provider's own
		// document can do that for an operation no route serves.
		doc *contract.Doc
	)
	switch {
	case *contractPath != "" && *sdk == "":
		// A provider that publishes an OpenAPI document needs no SDK reader: the
		// contract already lists its whole surface, and one artefact then feeds
		// both the drift gate and the response check.
		if doc, err = contract.Load(*contractPath); err == nil {
			upstream, err = drift.ScanContract(doc)
		}
	default:
		scan, known := scanners[*provider]
		if !known {
			names := make([]string, 0, len(scanners))
			for name := range scanners {
				names = append(names, name)
			}
			sort.Strings(names)
			fmt.Fprintf(stderr, "feint: no SDK scanner for provider %q yet; supported: %s\n",
				*provider, strings.Join(names, ", "))
			return exitError
		}
		upstream, err = scan(*sdk)
		// Given both, the SDK is the authority on which operations exist — its
		// method names are what routes declare — and the contract only adds
		// where the upstream's document files each one. Outscale is the case:
		// oapi-codegen drops the document's 50 tags when it flattens every
		// operation onto one Client, so without this join the whole surface
		// renders as a single `osc` row.
		if err == nil && *contractPath != "" {
			if doc, err = contract.Load(*contractPath); err == nil {
				drift.Regroup(upstream, doc)
			}
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	// The CI gate is only meaningful on the products the emulator has started:
	// asking it to account for all 1700 upstream operations would fail forever
	// and teach everyone to ignore it.
	productList := splitProducts(*products)
	if len(productList) > 0 {
		upstream = drift.OnlyProducts(upstream, productList)
		if len(upstream) == 0 {
			fmt.Fprintf(stderr, "feint: no upstream operation matches products %q\n", *products)
			return exitError
		}
	}

	srv, _, err := newServer(nil)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	var served []string
	declined := map[string]string{}
	for _, p := range srv.Packs() {
		if p.Name() != *provider {
			continue
		}
		for _, r := range p.Routes() {
			served = append(served, r.Operation)
		}
		// The whole guard, here rather than only in `go test`. The gate used to
		// fail on an empty reason and let "TODO" through, so a placeholder
		// reached the report and only CI's test job would ever have said so — a
		// control enforced in one of its two places is a control somebody
		// bypasses by running the other.
		if problems := declineProblems(p); len(problems) > 0 {
			for _, line := range problems {
				fmt.Fprintf(stderr, "feint: %s\n", line)
			}
			// exitError, not exitDrift: the exit codes are a contract the CI
			// depends on — 1 is an error, 2 is drift — and a pack declining
			// something without a usable reason is a defect in this repository,
			// not upstream movement. Returning 2 would have drift.yml open a
			// triage pull request for something no triage can fix.
			return exitError
		}
		for _, d := range p.Declined() {
			declined[d.Operation] = d.Reason
		}
	}

	// Restrict both sides to the same products, otherwise a route serving another
	// product looks like an orphan.
	if len(productList) > 0 {
		served = drift.OnlyProductNames(served, productList)
		kept := map[string]string{}
		for _, op := range drift.OnlyProductNames(emulator.DeclinedOperations(declinesOf(declined)), productList) {
			kept[op] = declined[op]
		}
		declined = kept
	}

	rep := drift.Compare(*provider, upstream, served, declined)

	if *baseline != "" {
		code, err := applyBaseline(*baseline, *writeBaseline, rep, *products, stdout)
		if err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
		if code != exitOK {
			return code
		}
	}

	// The committed artefact against the pack's current declarations. The
	// baseline above watches the upstream side; this watches the copy: #298
	// measured a decline reason rewritten in a pack that took four days to reach
	// coverage/scaleway-coverage.json, because the baseline compares operation
	// names and docs --check regenerates the README from the same stale artefact
	// — two gates agreeing with each other while both disagreed with the code.
	// TestTheCommittedArtefactCarriesWhatThePacksDeclare fails on the same skew;
	// this flag is what makes tools/drift/gate.sh check exit 2 on it.
	if *artefact != "" {
		skew, err := artefactSkew(*artefact, served, declined)
		if err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
		if len(skew) > 0 {
			fmt.Fprintf(stderr, "feint: %s disagrees with what the pack declares, %d operation(s):\n", *artefact, len(skew))
			for _, line := range skew {
				fmt.Fprintf(stderr, "  %s\n", line)
			}
			fmt.Fprintln(stderr, "feint: the artefact follows the code; regenerate it: mise run drift:update")
			return exitDrift
		}
	}

	// --observed asks a different question from the one --format selects, so it
	// renders instead of the report and never beside it. That is what makes
	// TestCoverageWithoutObservedRendersExactlyWhatItRenderedBefore trivially
	// true, and it
	// is deliberate: `feint coverage --format json` writes the committed
	// artefact, and a flag that could add a key to it would put the drift
	// mechanism at risk to improve it.
	if *observed != "" {
		return observedCoverage(rep, doc, *observed, *format, stdout, stderr)
	}

	switch *format {
	case "json":
		err = rep.WriteJSON(stdout)
	case "text":
		err = rep.WriteText(stdout)
	case "triage":
		// What the coverage report leaves open: of everything nobody decided
		// on, what is worth deciding first. Grouped by API and by subject,
		// because a subject is one decision and a flat list of names is not.
		err = rep.WriteTriage(stdout)
	case "list":
		// Every operation with its status, one per line. What triage groups,
		// this spells out: the exact names to paste into a Declined() block.
		err = rep.WriteList(stdout)
	default:
		fmt.Fprintf(stderr, "feint: unknown format %q\n", *format)
		return exitError
	}
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	// A refusal without a reason is a defect in the pack, and it now has its own
	// verdict: before this, an empty reason reclassified the operation as
	// untriaged and reported it as an orphan, so the gate failed while naming the
	// wrong thing.
	if len(rep.Unexplained) > 0 {
		fmt.Fprintf(stderr, "feint: %d declined operation(s) carry no reason: %s\n",
			len(rep.Unexplained), strings.Join(rep.Unexplained, ", "))
		fmt.Fprintln(stderr, "feint: a refusal without a reason is indistinguishable from an oversight")
		// exitError like its twin above, and for the same reason: this is a
		// defect in a pack, not upstream movement. Fixing one branch and leaving
		// the other is the pattern this repository has a paragraph about.
		return exitError
	}
	if len(rep.Orphans) > 0 {
		fmt.Fprintf(stderr, "feint: %d route(s) reference an operation that no longer exists upstream\n", len(rep.Orphans))
		return exitDrift
	}
	if *failOnUnknown && rep.Unknown > 0 {
		fmt.Fprintf(stderr, "feint: %d upstream operation(s) are neither served nor declined\n", rep.Unknown)
		return exitDrift
	}
	return exitOK
}

// catalog emits what the emulator serves. The documentation site builds its
// pages from this, so the docs cannot drift from the code.
func catalog(args []string, stdout io.Writer) error {
	fs := newFlagSet("catalog")
	format := fs.String("format", "json", "output format: json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *format != "json" {
		return fmt.Errorf("unknown format %q", *format)
	}

	srv, _, err := newServer(nil)
	if err != nil {
		return err
	}

	type routeView struct {
		Method    string `json:"method"`
		Path      string `json:"path"`
		Operation string `json:"operation"`
	}
	type declineView struct {
		Operation string `json:"operation"`
		Reason    string `json:"reason"`
	}

	type packView struct {
		Provider string        `json:"provider"`
		Routes   []routeView   `json:"routes"`
		Declined []declineView `json:"declined"`
	}

	packs := make([]packView, 0, len(srv.Packs()))
	for _, p := range srv.Packs() {
		routes := make([]routeView, 0, len(p.Routes()))
		for _, r := range p.Routes() {
			routes = append(routes, routeView{Method: r.Method, Path: r.Path, Operation: r.Operation})
		}
		// The reason travels with the operation here too: /_feint/routes is what
		// a reader hits when they want to know why something is refused, and a
		// list of bare names sent them back to the source.
		declined := make([]declineView, 0, len(p.Declined()))
		for _, d := range p.Declined() {
			declined = append(declined, declineView{Operation: d.Operation, Reason: d.Reason})
		}
		packs = append(packs, packView{Provider: p.Name(), Routes: routes, Declined: declined})
	}

	return writeJSON(stdout, map[string]any{
		"version":   Version,
		"providers": packs,
	})
}

// reportRuntimeEvents turns the runtime's stream into the emulator's own log.
//
// Levels are mapped rather than copied: a runtime warning about a resource the
// emulator created is worth an operator's attention, and a lifecycle change is
// not, which is why the latter is debug.
// The server is the second reader: what the runtime says about a machine the
// emulator created belongs on the same timeline as the calls that created it. An
// operator reading "the server is running" needs to see the container behind it
// stop, and until now that only reached the process log at debug level.
func reportRuntimeEvents(events <-chan machine.Event, log *slog.Logger, srv *emulator.Server) {
	// Consecutive duplicates are dropped: the daemon logs its teardown race
	// once per concurrent list, so a single stop raced by a watcher produces
	// tens of identical lines in a second, and the repetition carries nothing.
	var lastKey string
	for event := range events {
		key := event.Kind + "\x00" + event.Resource + "\x00" + event.Message
		if key == lastKey {
			continue
		}
		lastKey = key
		if srv != nil {
			srv.PublishRuntimeEvent(event.Kind, event.Level, event.Action, event.Resource, event.Message)
		}
		switch {
		case event.Kind == "lifecycle":
			log.Debug("runtime "+event.Action, "resource", event.Resource)
		case event.Level == "error" || event.Level == "panic" || event.Level == "fatal":
			log.Error("runtime reported an error", "resource", event.Resource, "message", event.Message)
		default:
			log.Warn("runtime reported a warning", "resource", event.Resource, "message", event.Message)
		}
	}
}

// parseLogLevel maps the flag onto a slog level. Rejecting an unknown value
// rather than defaulting: a typo that silently keeps the default is how someone
// concludes the emulator says nothing.
func parseLogLevel(name string) (slog.Level, error) {
	switch name {
	case "error":
		return slog.LevelError, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	default:
		return 0, fmt.Errorf("unknown log level %q (error, warn, info, debug)", name)
	}
}

// declinesOf turns the operation-to-reason map back into the slice the product
// filter takes. One conversion in one place: the filter works on names, the
// report needs reasons, and doing this at each call site is how one of them
// drops the reason.
func declinesOf(declined map[string]string) []emulator.Decline {
	out := make([]emulator.Decline, 0, len(declined))
	for op, reason := range declined {
		out = append(out, emulator.Decline{Operation: op, Reason: reason})
	}
	return out
}

// declineProblems is what makes a pack's refusals unusable, as sentences.
//
// Two tests, and the difference between them is the point.
// TestTheGateRefusesUnusableRefusals proves this function refuses a placeholder
// and a duplicate and accepts a clean pack. TestCoverageRefusesAPackWithUnusableRefusals
// proves coverage() still calls it, by mounting a broken pack through packsFor
// and asserting the exit code — deleting the call site now fails that test.
//
// It took four audits to get here. The first version of this paragraph declared
// the gap and misdiagnosed it (blaming an SDK checkout that `feint coverage` does
// not need); the second declared it accurately and left it. A declared intention
// with a plan is still not a control, so the seam was added and the plan carried
// out.
//
// The second gate below, on rep.Unexplained, stays unreachable defence in depth:
// an empty reason trips this function first, so that branch only fires if this
// one is deleted. That is deliberate and stated rather than mistaken for a live
// verdict.
//
// Extracted from coverage() so it can be tested. Inline it was unfalsifiable: an
// audit deleted the block that consumed it and the entire suite stayed green,
// which is the same defect as a comment describing a control — the control
// existed and nothing proved it ran.
func declineProblems(p emulator.Pack) []string {
	var out []string
	declined := p.Declined()
	if bad := emulator.UnexplainedDeclines(declined); len(bad) > 0 {
		out = append(out, fmt.Sprintf("%s declines %d operation(s) with no usable reason: %s",
			p.Name(), len(bad), strings.Join(bad, ", ")))
	}
	if dup := emulator.DuplicateDeclines(declined); len(dup) > 0 {
		out = append(out, fmt.Sprintf("%s declines %s more than once, so two documents disagree about the reason",
			p.Name(), strings.Join(dup, ", ")))
	}
	return out
}
