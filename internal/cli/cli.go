// Package cli wires the feint commands.
//
// No CLI framework: a dozen subcommands do not justify a dependency, and the
// standard flag package keeps the binary free of supply-chain surface.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
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
                    [--cleanup] [--contracts <dir>] [--coverage <dir>] [--log-level info|debug]
                    Serve the three emulated clouds on one port, in the
                    foreground.

  feint start      [every serve flag] [--timeout 30s] [--detach] [--foreground]
                    Same, detached: records the instance, waits until it
                    answers, prints where the log is. Refuses to adopt an
                    instance already running on that address.

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

  feint env <provider> [--shell bash|fish|powershell] [--endpoint <url>] [--unset]
                    The environment a real client of that provider needs.
                    Exports on stdout, everything else on stderr, so
                    eval "$(feint env scaleway)" is safe.

  feint snapshot   save <name> [--addr :4599] [--force]
                   load <name> [--addr :4599]
                   list [--format text|json]
                   rm <name>
                    Name the state of a running emulator and come back to it.
                    Same bytes as serve --state, taken mid-run rather than at
                    exit. Loading replaces the store: a fixture must not depend
                    on what the session did before it.

  feint coverage   (--sdk <dir> | --contract <file>) [--provider scaleway|outscale|exoscale]
                    [--products <a,b,c>] [--format text|json|triage|list]
                    [--baseline <file> [--write-baseline]] [--fail-on-unknown]
                    Compare the upstream API surface with what the packs serve.
                    Scaleway and Outscale are read from an SDK checkout; Exoscale
                    publishes an OpenAPI document, so it is read with --contract.

  feint probe      [--endpoint http://127.0.0.1:4599] [--contracts <dir>] [--provider <name>]
                    Drive every mounted route from its API description and check
                    the answers. Proves the protocol, never the behaviour.

  feint proxy      --upstream <url> --record <file.jsonl> [--addr 127.0.0.1:4600]
                    [--provider <name>] [--max-body <bytes>] [--queue <n>]
                    Sit between a real client and a real cloud and write down
                    every exchange, as JSON Lines, one object per call, with the
                    upstream operation named. Credentials are redacted before
                    anything is written. Point the client at --addr and drive it
                    as usual.

  feint transcript <recording.jsonl> [--shape OP [--against emu.jsonl]] [--format text|json]
                    Read a proxy recording and answer what to serve next. With no
                    flag, the operations a real client called that no pack serves,
                    most-called first. With --shape, the response shape one
                    operation actually returned. With --against, diff that shape
                    against the emulator's own answer: the fields it omits.

  feint docs       [--file README.md] [--coverage <dir>] [--check]
                    Regenerate the coverage tables in a Markdown file.

  feint catalog    [--format json]
                    Print the emulated inventory a client reads before creating.

  feint clean      [--vm incus|incus-vm|incus-ovn]
                    Remove every machine, network and rule set the emulator
                    created. Labelled resources only; nothing else is touched.

  feint version    Print the version. --version and -v do the same.

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

	requested := func(d machine.Driver) (machine.Driver, error) {
		if !d.Available(ctx) {
			return nil, fmt.Errorf("--vm %s requested but the Incus daemon does not answer", mode)
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
			caps := machine.CapabilitiesOf(d)
			fmt.Fprintf(stdout, "machine runtime: %s (isolation: %v)\n", d.Name(), caps.Isolation)
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
var packsFor = func(env *emulator.Env) []emulator.Pack {
	return []emulator.Pack{scaleway.New(env), outscale.New(env), exoscale.New(env)}
}

// newServer builds the emulator with every pack mounted. With contracts, every
// response is checked against the provider's own API description.
func newServer(contracts map[string]*contract.Doc) (*emulator.Server, *emulator.Env, error) {
	env := emulator.DefaultEnv()
	env.Contracts = contracts
	srv, err := emulator.NewServer(env, packsFor(env)...)
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

func serve(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", DefaultAddr, "listen address")
	state := fs.String("state", "", "load and persist the store to this JSON file")
	vm := fs.String("vm", "off", "back powered-on servers with real machines: off, incus, incus-vm, incus-ovn, auto")
	cleanup := fs.Bool("cleanup", false, "remove the machines and networks this run created before exiting")
	logLevel := fs.String("log-level", "info", "log verbosity: error, warn, info, debug")
	contracts := fs.String("contracts", "", "directory of API contracts; every response is checked against them and /_feint/conformance reports what failed")
	coverageDir := fs.String("coverage", "coverage", "directory holding the versioned coverage artefacts the page reads")
	expose := fs.Bool("expose-to-network", false, "listen off loopback, which disarms the browser guard: read what it costs before setting it")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if err := checkListenAddr(*addr, *expose); err != nil {
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
	fs := flag.NewFlagSet("coverage", flag.ContinueOnError)
	sdk := fs.String("sdk", "", "path to a checkout of the provider Go SDK")
	contractPath := fs.String("contract", "", "read the upstream surface from a contract artefact instead of an SDK checkout")
	provider := fs.String("provider", scaleway.Name, "provider to report on")
	format := fs.String("format", "text", "output format: text, json, triage or list")
	products := fs.String("products", "", "comma-separated upstream products to restrict the report to (e.g. instance,rdb)")
	failOnUnknown := fs.Bool("fail-on-unknown", false, "exit 2 when an upstream operation is neither served nor declined")
	baseline := fs.String("baseline", "", "compare the unknown operations against this baseline file and exit 2 on new ones")
	writeBaseline := fs.Bool("write-baseline", false, "rewrite the baseline file from the current upstream surface")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if *sdk == "" && *contractPath == "" {
		fmt.Fprintln(stderr, "feint: one of --sdk (a provider SDK checkout) or --contract (a contract artefact) is required")
		return exitError
	}
	// One scanner per provider, because their SDKs share no shape: Scaleway
	// generates a package per product and version from its own IDL, Outscale
	// generates one client per service from OpenAPI.
	var (
		upstream []drift.Operation
		err      error
	)
	switch {
	case *contractPath != "":
		// A provider that publishes an OpenAPI document needs no SDK reader: the
		// contract already lists its whole surface, and one artefact then feeds
		// both the drift gate and the response check.
		var doc *contract.Doc
		if doc, err = contract.Load(*contractPath); err == nil {
			upstream, err = drift.ScanContract(doc)
		}
	case *provider == scaleway.Name:
		upstream, err = drift.ScanScalewaySDK(*sdk)
	case *provider == outscale.Name:
		upstream, err = drift.ScanOutscaleSDK(*sdk)
	default:
		fmt.Fprintf(stderr, "feint: no SDK scanner for provider %q yet; %q and %q are supported\n",
			*provider, scaleway.Name, outscale.Name)
		return exitError
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
	fs := flag.NewFlagSet("catalog", flag.ContinueOnError)
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
