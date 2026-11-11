package cli

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/proxy"
	"github.com/stephrobert/feint/internal/s3"
)

// `feint run` lands a client whose cloud endpoint is compiled in on the local
// emulator, for one child process and nothing else (#849).
//
// ## Why a command and not documentation
//
// The measured wall: a client that discards the endpoint it was given leaves
// for the real cloud, which is #280 and the reason #850 cannot be closed by
// serving more routes. Telling an operator to export four variables and mint a
// CA by hand is not a fix; it is the same wall with a paragraph in front of it.
//
// ## Why it takes no hostname, and that is the safety property
//
// An earlier draft of this command took `--forward host=target`, and that was
// the wrong shape for one reason: the operator typing the hostname is the
// operator able to mistype it, and a mistyped cloud host is not intercepted —
// it is *resolved*, so the run leaves for a third party carrying whatever
// credentials it holds. The failure is silent, because a request that reaches
// the real cloud looks exactly like a request that worked.
//
// So nothing here is typed. The emulator is asked which doors it actually
// mounted (GET /_feint/s3, read and validated by fetchS3), and the cloud
// authority for each one comes from the pack in this binary. Both halves of the
// mapping are therefore measured rather than supplied, and the set of possible
// targets is the set of doors that exist: there is no spelling of this command
// that sends traffic off loopback.
//
// The split is also the one [s3.Profile] requires: an instance fact (which
// doors are mounted) may come from the report, a wire dialect may not.
//
// ## What makes it safe, and it is all one idea
//
// Everything is scoped to the child. The proxy listens on loopback, the CA
// lives in a directory this process creates and removes, and the four variables
// are set on the child's environment — never on the operator's shell, never in
// /etc/hosts, never in a system trust store. When `feint run` returns, the only
// trace left is the exit code it passes through.
//
// The interception is also an allow-list, and that is proxy.NewForward's rule
// rather than this file's: a host must be named to be intercepted, and an
// unnamed one is refused rather than tunnelled. #849 states it as "never
// convert an unrecognised production hostname into a local route", and the
// forward was already written that way because a proxy that intercepts
// whatever it is handed decrypts more of the operator's traffic than the
// measurement asked for.
//
// ## Why it refuses real credentials by default
//
// A client pointed at the emulator still signs its requests, and the emulator
// accepts any signature without checking it. So a run carrying REAL keys proves
// nothing the fake ones would not, and risks everything they would not.
//
// The rule is fail-closed: a credential-shaped variable is refused unless its
// value is one of the known fakes, and --unsafe-real-credentials is the only
// way past. The alternative — guessing which values look real — fails in the
// one direction that costs something.
func runCommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newFlagSet("run")
	endpoint := fs.String("endpoint", "", "attach to an emulator already running here, instead of starting one. Empty means this command owns the lab and stops it. Loopback HTTP(S) only")
	provider := fs.String("provider", "", "resolve only this provider's client environment into the child; empty resolves every pack's")
	addr := fs.String("addr", "127.0.0.1:0", "the proxy's listen address; the default takes a free loopback port and prints it")
	runtimeMode := fs.String("runtime", "off", "the machine runtime the lab starts with: off, incus, incus-vm, incus-ovn, auto. Anything but off starts containers on this host, so it is asked for")
	keep := fs.Bool("keep-on-failure", false, "when the child fails, leave the lab running and print how to reach and remove it")
	receiptPath := fs.String("json", "", "write the receipt as JSON to this path; `-` means stdout, which is only safe when the command itself is quiet")
	unsafe := fs.Bool("unsafe-real-credentials", false, "allow the child to carry credentials that are not known fakes. The emulator never checks a signature, so this buys nothing and risks sending real keys to a third party")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	command := fs.Args()
	if len(command) == 0 {
		fmt.Fprintln(stderr, "feint run: name a command to run, after --")
		return exitError
	}

	if offenders := realCredentials(os.Environ()); len(offenders) != 0 && !*unsafe {
		fmt.Fprintf(stderr, "feint run: refusing to run with credentials that are not known fakes: %s\n",
			strings.Join(offenders, ", "))
		fmt.Fprintln(stderr, "  The emulator accepts any signature without checking it, so real keys buy")
		fmt.Fprintln(stderr, "  nothing here. Use the repository's public fakes, or pass")
		fmt.Fprintln(stderr, "  --unsafe-real-credentials if you have a reason this refusal does not cover.")
		return exitError
	}

	// The runtime, asked of the host before anything starts, which is `up`'s
	// rule and for its reason: a refusal must fire with nothing running. The
	// same seam answers, so the refusal says what `up`'s says.
	if *endpoint != "" && *runtimeMode != "off" {
		fmt.Fprintf(stderr, "feint run: --runtime %s needs a lab this command owns; --endpoint names one it does not\n", *runtimeMode)
		fmt.Fprintln(stderr, "  Drop --endpoint to own the lab, or start that emulator with --vm yourself.")
		return exitError
	}
	if *endpoint == "" {
		if _, err := resolveRuntime(*runtimeMode, stderr); err != nil {
			fmt.Fprintf(stderr, "feint run: %v\n", err)
			fmt.Fprintf(stderr, "  `feint doctor --vm %s` reports the whole diagnosis, and --runtime off needs none of it.\n", *runtimeMode)
			return exitError
		}
	}

	// The lab. Owned when nothing was named, which is the default because
	// #848's whole point is that Feint infrastructure leaves the user's
	// mental model; attached when an endpoint is named, because a suite that
	// already brought an emulator up with its own flags must not get a second.
	target, owned := *endpoint, (*ownedLab)(nil)
	if target == "" {
		lab, err := startOwnedLab(*runtimeMode, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "feint run: %v\n", err)
			return exitError
		}
		owned, target = lab, lab.endpoint
	}

	// Everything that can be refused is refused before the child is spawned,
	// which is `up`'s rule and for its reason: a run that dies four minutes in
	// on a missing door has spent those minutes teaching nothing. The teardown
	// is wired first so that a refusal below cannot leak the lab.
	fail := func(err error) int {
		fmt.Fprintf(stderr, "feint run: %v\n", err)
		if owned != nil {
			if closeErr := owned.close(stderr); closeErr != nil {
				fmt.Fprintf(stderr, "feint run: %v\n", closeErr)
			}
		}
		return exitError
	}

	profiles, err := runProfiles(target)
	if err != nil {
		return fail(err)
	}
	exports, providers, err := providerExports(target, *provider)
	if err != nil {
		return fail(err)
	}

	// The escape refusal #848 asks for, before a child is spawned and before
	// a `terraform apply` can reach anything. It fires on a hazard this run
	// has no door for, never on one it intercepts — the distinction is in
	// stackEscapes and it is the whole difference between a useful refusal and
	// a command that refuses the stacks it was built for.
	if escapes := stackEscapes(profiles); len(escapes) != 0 {
		fmt.Fprintln(stderr, "feint run: refusing to start; this configuration can reach a real cloud this run does not intercept")
		for _, escape := range escapes {
			fmt.Fprintf(stderr, "  %s\n", escape)
		}
		fmt.Fprintln(stderr, "  Mount that provider's door (--objectstore and --s3-doors on the emulator), or run elsewhere.")
		if owned != nil {
			if closeErr := owned.close(stderr); closeErr != nil {
				fmt.Fprintf(stderr, "feint run: %v\n", closeErr)
			}
		}
		return exitError
	}

	// What the child may see: the measured reroutes removed, this command's
	// own exports added. The strip is what makes the receipt's "no known
	// escape observed" more than a hope — a variable a pack measured as a way
	// to reach the real cloud cannot reach the child at all.
	stripped := hazardVariables()
	childEnv := childEnvironment{strip: stripped, set: exports}

	// Counted before and after, so an ATTACHED lab's earlier traffic is not
	// reported as this run's. The owned case starts at zero and the delta is
	// the same number; the attached case is where a total would lie.
	before, _ := callsSoFar(target)

	code, runErr := runChild(target, *addr, profiles, command, *unsafe, childEnv, stdout, stderr)

	// Read before the teardown, or there is nothing left to read.
	after, truncated := callsSoFar(target)
	calls := after - before
	if calls < 0 || truncated {
		// A ring that wrapped makes the delta meaningless, and so does a
		// restart. Report what was last seen and say it is a window.
		calls, truncated = after, true
	}
	runtimeName, capabilities := labHealth(target)

	cleanup := "not owned"
	if owned != nil {
		switch {
		case keepsTheLab(code, runErr, *keep):
			owned.keep(stderr)
			cleanup = "kept"
		default:
			if closeErr := owned.close(stderr); closeErr != nil {
				fmt.Fprintf(stderr, "feint run: %v\n", closeErr)
				cleanup = "failed"
			} else {
				cleanup = "clean"
			}
		}
	}

	if runErr != nil {
		fmt.Fprintf(stderr, "feint run: %v\n", runErr)
		return exitError
	}

	intercepted := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		intercepted = append(intercepted, profile.CloudHost)
	}
	receipt := runReceipt{
		SchemaVersion: runReceiptSchemaVersion, Lab: labKind(owned),
		Session: labSession(owned), Endpoint: target,
		Runtime: runtimeName, Capabilities: capabilities,
		Providers: providers, Intercepted: intercepted, Command: command,
		Calls: calls, CallsTruncated: truncated, Stripped: stripped,
		Exit: code, Cleanup: cleanup,
		RealCloud: "no known escape observed",
	}
	// stdout belongs to the CHILD, and to nothing else. Measured rather than
	// reasoned: the first version wrote the JSON receipt there, and replaying
	// a surveyed stack made `terraform apply` print its plan into the same
	// stream, so the receipt was unparseable exactly when the run was real.
	// The prose goes to stderr beside the rest of this command's prose, and
	// the JSON goes to a path the caller names.
	receipt.render(stderr)
	if *receiptPath != "" {
		if err := writeReceipt(receipt, *receiptPath, stdout); err != nil {
			fmt.Fprintf(stderr, "feint run: %v\n", err)
			return exitError
		}
	}
	// The child's code, not this command's: a wrapper that swallowed a
	// failing test would be worse than no wrapper.
	return code
}

// keepsTheLab answers whether a finished run leaves its emulator up.
//
// A function rather than a condition inline, because it is the decision this
// command is most likely to get wrong in the expensive direction: keeping on
// success would leave a port and a directory behind after every green run,
// and nothing in the output would say so. Both halves are asserted, and the
// mutation that drops either is in
// tools/falsify/specs/a-run-owns-its-lab.json.
//
// TestALabIsKeptOnlyWhenAFailedRunAskedForIt fails without this.
func keepsTheLab(code int, runErr error, asked bool) bool {
	if !asked {
		return false
	}
	return code != exitOK || runErr != nil
}

// labSession names an owned lab, and deliberately nothing for an attached
// one: its identity belongs to whoever started it, and inventing a second name
// for it would put two names on one emulator.
func labSession(owned *ownedLab) string {
	if owned == nil {
		return ""
	}
	return owned.session
}

// labKind names who owns the emulator, for a receipt a reader can act on.
func labKind(owned *ownedLab) string {
	if owned == nil {
		return "attached"
	}
	return "owned"
}

// runProfiles asks the emulator which doors it mounted and pairs each with the
// cloud authority its own pack declares.
//
// The refusal on a door whose pack declares no CloudHost is deliberate and is
// not a gap to paper over: a provider with no measured authority has nothing
// this command could intercept, and inventing one would send a client to a name
// nobody recorded. TestRunRefusesADoorWithNoMeasuredAuthority holds it.
func runProfiles(endpoint string) ([]s3.Profile, error) {
	report, err := fetchS3(endpoint)
	if err != nil {
		return nil, err
	}
	if !report.Enabled || len(report.Doors) == 0 {
		return nil, errors.New("the emulator serves no S3 door, so there is nothing to intercept; " +
			"start it with --objectstore and --s3-doors")
	}
	declaration := make([]string, 0, len(report.Doors))
	for _, door := range report.Doors {
		declaration = append(declaration, door.Provider+"/"+door.Region)
	}
	packs, err := packsFor(emulator.DefaultEnv())
	if err != nil {
		return nil, err
	}
	profiles, err := storageProfiles(packs, strings.Join(declaration, ","))
	if err != nil {
		return nil, err
	}
	for _, profile := range profiles {
		if profile.CloudHost == "" {
			return nil, fmt.Errorf("the %s/%s door declares no measured cloud authority, so this command "+
				"cannot say what to intercept for it", profile.Provider, profile.Region)
		}
	}
	return profiles, nil
}

// runDirPrefix and runOwnerFile are shared with `feint doctor`, which is the
// only reason they are constants: a diagnostic that hunts for leftovers by
// re-spelling the producer's naming scheme is a diagnostic that goes blind the
// day the scheme changes, and nothing fails to say so.
const (
	runDirPrefix = "feint-run-"
	runOwnerFile = "owner.pid"
)

// credentialVariables are the names whose values this command refuses to pass
// on unless they are known fakes. Names, not patterns: a pattern like *_KEY
// would catch SSH_AUTH_SOCK's neighbours and refuse runs for no reason, and a
// name this list forgets is a name the operator can still protect by using the
// public fakes.
var credentialVariables = []string{
	"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
	"SCW_ACCESS_KEY", "SCW_SECRET_KEY",
	"OSC_ACCESS_KEY", "OSC_SECRET_KEY",
	"EXOSCALE_API_KEY", "EXOSCALE_API_SECRET",
}

// knownFakes are deliberately public placeholders that no pack publishes, kept
// as a hand-written list for the clients that carry their own.
//
// A list and not a shape test, because every shape test gets this wrong in the
// expensive direction: "twenty identical characters" would admit a real key
// that happens to repeat, and "looks random" would refuse the fakes. The cost
// of the list is that a fake it does not know is refused, which is the harmless
// direction — the operator sees exactly which variable and can use a known one.
//
// It is NOT the whole accepted set: see acceptedFakes, and the defect that
// made the difference matter.
var knownFakes = []string{
	"AAAAAAAAAAAAAAAAAAAA",
	"BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
	"SCWXXXXXXXXXXXXXXXXX",
	"11111111-1111-1111-1111-111111111111",
	"EXOxxxxxxxxxxxxxxxxxxxxxx",
	"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
}

// acceptedFakes is every credential value this binary itself tells an operator
// to export, plus the hand-written knownFakes no pack publishes.
//
// Derived rather than listed, and the reason is measured. The two lists were
// written separately and drifted: `feint env exoscale` published a 23-character
// EXOSCALE_API_KEY and a 40-character secret, while knownFakes held a
// 25-character key and neither secret. So the sequence both commands document —
// eval `feint env`, then `feint run` — was refused by its own binary, naming
// four variables the operator had never chosen. Found by replaying
// PhilippeChepy/platform from examples/stacks/surveyed.md, not by a test, which
// is why the fix removes the second list rather than correcting it.
//
// TestEveryPublishedFakeIsAcceptedByRun fails without this function.
func acceptedFakes() []string {
	accepted := slices.Clone(knownFakes)
	packs, err := packsFor(emulator.DefaultEnv())
	if err != nil {
		// Fail closed: fewer accepted values means more refusals, and a
		// refusal names the variable and offers --unsafe-real-credentials.
		return accepted
	}
	for _, pack := range packs {
		for name, value := range pack.Env(defaultEndpoint()).Vars {
			if value != "" && slices.Contains(credentialVariables, name) {
				accepted = append(accepted, value)
			}
		}
	}
	return accepted
}

// realCredentials names the credential variables carrying something other than
// a known fake. Empty is the safe answer: no credentials set at all is a run
// the emulator serves happily, since it checks no signature.
func realCredentials(environ []string) []string {
	accepted := acceptedFakes()
	var offenders []string
	for _, pair := range environ {
		name, value, ok := strings.Cut(pair, "=")
		if !ok || value == "" || !slices.Contains(credentialVariables, name) {
			continue
		}
		if !slices.Contains(accepted, value) {
			// The NAME only. Printing the value would put a real credential in
			// a terminal, a CI log and a bug report, which is the accident this
			// refusal exists to prevent.
			offenders = append(offenders, name)
		}
	}
	slices.Sort(offenders)
	return offenders
}

// withoutCABundle drops AWS_CA_BUNDLE from an inherited environment.
//
// Removing rather than emptying, because an empty value is not an absent one:
// a client that reads the variable would be asked to load a bundle from "",
// which is a different failure with the same cause.
//
// This is the one place the command edits what the operator already had, and
// the reason is that inheriting it cannot help under any value. Pointing at the
// station's own bundle makes a client reject this run's authority, since that
// bundle does not contain it; pointing anywhere else is no better. Measured on
// the published Scaleway provider 2.82.0, the failure is not even a TLS error
// but "unable to add custom RootCAs HTTPClient, has no WithTransportOptions,
// *http.Client", which sends the reader looking at the wrong layer entirely.
// SSL_CERT_FILE, which crypto/x509 reads, covers every Go client without it.
func withoutCABundle(environ []string) []string {
	kept := make([]string, 0, len(environ))
	for _, pair := range environ {
		if name, _, ok := strings.Cut(pair, "="); ok && name == "AWS_CA_BUNDLE" {
			continue
		}
		kept = append(kept, pair)
	}
	return kept
}

// runChild mounts the forward, mints the CA, runs the command and removes both.
func runChild(endpoint, addr string, profiles []s3.Profile, command []string, unsafe bool,
	childEnv childEnvironment, stdout, stderr io.Writer,
) (int, error) {
	// The transport is what makes a door reachable: a client asks for
	// GET / on the cloud authority, and the door lives under a path prefix.
	// LocalTransport rewrites that, refusing any authority not declared here.
	transport, err := s3.NewLocalTransport(endpoint, profiles)
	if err != nil {
		return 0, err
	}
	// Two authorities per door, and the second one is not a convenience: S3
	// clients address a bucket as <bucket>.<authority>, which is the form
	// Terraform uses, so a door declared only by its bare authority is a door
	// no real client reaches. A path-style check does NOT catch this, and did
	// not — the first proof of this command used curl against the bare host and
	// passed while every bucket request would have been refused.
	//
	// The single-level wildcard is the forward's own feature (forward.go:69) and
	// it is as wide as a certificate's: one label, never two. What keeps it
	// honest is that the wildcard only decides what is INTERCEPTED; where each
	// request then goes is LocalTransport's decision, and it re-derives the
	// bucket from the authority and refuses a name that is not a valid bucket
	// (local_transport.go, "invalid virtual-host bucket"). So widening the
	// allow-list here cannot widen what is served.
	// TestRunInterceptsBucketsAddressedAsSubdomains holds it.
	hosts := make([]string, 0, 2*len(profiles))
	for _, profile := range profiles {
		hosts = append(hosts, profile.CloudHost, "*."+profile.CloudHost)
	}

	authority, err := proxy.MintAuthority()
	if err != nil {
		return 0, fmt.Errorf("mint the local authority: %w", err)
	}

	// Owner-only on the directory, and nothing durable: #849 asks for both, and
	// the directory is where the whole property lives.
	//
	// The permission is VERIFIED rather than set, and the first run of that
	// check earned its keep: an earlier draft asserted 0600 on the CA file too
	// and the check reported 0644, because proxy.WriteCA writes 0644 on purpose
	// (intercept.go:294, "a public CA, no secret; must be readable by the
	// client process"). The requirement was mine, not the system's. What
	// actually makes the run private is the 0700 directory, and the only real
	// secret — the authority's private key — is never written to disk at all.
	// A Chmod would have written that wrong assumption down as a value nobody
	// reads back, which is the defect this repository measures most often.
	// TestRunKeepsItsAuthorityOwnerOnly drives it through the child, which is
	// where the permissions actually matter.
	dir, err := os.MkdirTemp("", runDirPrefix)
	if err != nil {
		return 0, fmt.Errorf("create the run's private directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(stderr, "feint run: could not remove %s: %v\n", dir, err)
		}
	}()
	caPath := filepath.Join(dir, "ca.pem")
	if err := authority.WriteCA(caPath); err != nil {
		return 0, fmt.Errorf("write the run's authority: %w", err)
	}
	// The owning PID, so `feint doctor` can tell a LEFTOVER from a run in
	// flight. Without it the diagnostic #849 asks for could only ever say "a
	// directory exists", which is true of every healthy run and therefore
	// decides nothing — a warning nobody can act on is the kind of diagnostic
	// people stop reading, and doctor.go says so in its own header.
	if err := os.WriteFile(filepath.Join(dir, runOwnerFile),
		[]byte(fmt.Sprintln(os.Getpid())), 0o600); err != nil {
		return 0, fmt.Errorf("record the run's owner: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return 0, fmt.Errorf("inspect %s: %w", dir, err)
	}
	if mode := info.Mode().Perm(); mode&^os.FileMode(0o700) != 0 {
		return 0, fmt.Errorf("%s is %#o, wider than the 0700 this run requires", dir, mode)
	}

	writer := proxy.NewWriter(io.Discard, proxy.DefaultQueue)
	defer func() { _ = writer.Close() }()
	forward, err := proxy.NewForward(proxy.ForwardOptions{
		Hosts: hosts, Writer: writer, Authority: authority, Transport: transport,
	})
	if err != nil {
		return 0, fmt.Errorf("build the forward proxy: %w", err)
	}
	// Forward is an http.Handler, so this owns the listener. Loopback is
	// checked rather than assumed: a proxy off loopback would offer this
	// interception, and the child's traffic, to the network.
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return 0, fmt.Errorf("listen on %s: %w", addr, err)
	}
	defer func() { _ = listener.Close() }()
	if host, _, splitErr := net.SplitHostPort(listener.Addr().String()); splitErr != nil ||
		!net.ParseIP(host).IsLoopback() {
		return 0, fmt.Errorf("the proxy must listen on loopback, not %s", listener.Addr())
	}
	server := &http.Server{Handler: forward, ReadHeaderTimeout: 30 * time.Second}
	defer func() { _ = server.Close() }()
	go func() { _ = server.Serve(listener) }()

	proxyURL := "http://" + listener.Addr().String()

	// The receipt, before the child runs rather than after. #849 asks that
	// every rewrite be inspectable; printed first, it is also inspectable by
	// somebody who interrupts the run.
	// The interception receipt is prose, so it goes to stderr: with --json,
	// stdout carries the run receipt and nothing else, and the child's own
	// stdout is already there. Before #848 this was on stdout, where it mixed
	// with whatever the child printed; nothing reads it as data.
	fmt.Fprintf(stderr, "feint run: proxy on %s, authority at %s\n", proxyURL, caPath)
	for _, profile := range profiles {
		fmt.Fprintf(stderr, "  https://%s and https://<bucket>.%s -> %s%s (TLS terminated with the run's own authority)\n",
			profile.CloudHost, profile.CloudHost, endpoint, s3.DoorPath(profile.Provider, profile.Region))
	}
	fmt.Fprintln(stderr, "  every other host is refused, never tunnelled")
	if unsafe {
		fmt.Fprintln(stderr, "  --unsafe-real-credentials: the child's credentials were NOT checked")
	}

	child := exec.Command(command[0], command[1:]...) //nolint:gosec // the operator's own command, explicit argv, no shell
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, stdout, stderr
	child.Env = append(childEnv.apply(withoutCABundle(os.Environ())),
		"HTTP_PROXY="+proxyURL, "HTTPS_PROXY="+proxyURL,
		"http_proxy="+proxyURL, "https_proxy="+proxyURL,
		// The emulator itself must not go through the proxy, or a request to it
		// would be intercepted by the thing forwarding to it.
		"NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost",
		// SSL_CERT_FILE and nothing else, and the "nothing else" is measured.
		//
		// It is read by crypto/x509 itself, so every Go client honours it
		// without knowing this command exists — which covers the three official
		// CLIs and the Terraform providers alike. That is what the existing
		// witness measured, and it sets exactly these four variables.
		//
		// An earlier draft also set AWS_CA_BUNDLE, on the reasoning that an S3
		// client would want it. The Scaleway provider 2.82.0 then failed its
		// apply with "unable to add custom RootCAs HTTPClient, has no
		// WithTransportOptions, *http.Client": it hands the SDK a prebuilt
		// *http.Client, and a CA bundle the SDK is asked to install on it has
		// nowhere to go. The variable bought nothing — a Go client already
		// trusts SSL_CERT_FILE — and cost the one client this feature exists
		// for. TestRunGivesTheChildOnlyTheFourMeasuredVariables holds the list.
		"SSL_CERT_FILE="+caPath,
	)

	// A signal reaches the child and the cleanup still runs. Without this,
	// Ctrl-C would leave the authority on disk, which is the leftover #849
	// names. TestRunRemovesItsAuthorityAfterASignal holds it.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	if err := child.Start(); err != nil {
		return 0, fmt.Errorf("start %s: %w", command[0], err)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	for {
		select {
		case sig := <-signals:
			if p := child.Process; p != nil {
				_ = p.Signal(sig)
			}
		case waitErr := <-done:
			var exit *exec.ExitError
			if errors.As(waitErr, &exit) {
				return exit.ExitCode(), nil
			}
			if waitErr != nil {
				return 0, fmt.Errorf("%s: %w", command[0], waitErr)
			}
			return 0, nil
		}
	}
}
