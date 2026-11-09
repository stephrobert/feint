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
	endpoint := fs.String("endpoint", defaultEndpoint(), "the running emulator, whose mounted S3 doors decide what is intercepted. Loopback HTTP(S) only")
	addr := fs.String("addr", "127.0.0.1:0", "the proxy's listen address; the default takes a free loopback port and prints it")
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

	profiles, err := runProfiles(*endpoint)
	if err != nil {
		fmt.Fprintf(stderr, "feint run: %v\n", err)
		return exitError
	}

	code, err := runChild(*endpoint, *addr, profiles, command, *unsafe, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "feint run: %v\n", err)
		return exitError
	}
	return code
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

// knownFakes are the deliberately public placeholders this repository ships,
// and the only credential values `feint run` passes on without --unsafe.
//
// A list and not a shape test, because every shape test gets this wrong in the
// expensive direction: "twenty identical characters" would admit a real key
// that happens to repeat, and "looks random" would refuse the fakes. The cost
// of the list is that a fake it does not know is refused, which is the harmless
// direction — the operator sees exactly which variable and can use a known one.
var knownFakes = []string{
	"AAAAAAAAAAAAAAAAAAAA",
	"BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
	"SCWXXXXXXXXXXXXXXXXX",
	"11111111-1111-1111-1111-111111111111",
	"EXOxxxxxxxxxxxxxxxxxxxxxx",
	"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
}

// realCredentials names the credential variables carrying something other than
// a known fake. Empty is the safe answer: no credentials set at all is a run
// the emulator serves happily, since it checks no signature.
func realCredentials(environ []string) []string {
	var offenders []string
	for _, pair := range environ {
		name, value, ok := strings.Cut(pair, "=")
		if !ok || value == "" || !slices.Contains(credentialVariables, name) {
			continue
		}
		if !slices.Contains(knownFakes, value) {
			// The NAME only. Printing the value would put a real credential in
			// a terminal, a CI log and a bug report, which is the accident this
			// refusal exists to prevent.
			offenders = append(offenders, name)
		}
	}
	slices.Sort(offenders)
	return offenders
}

// runChild mounts the forward, mints the CA, runs the command and removes both.
func runChild(endpoint, addr string, profiles []s3.Profile, command []string, unsafe bool,
	stdout, stderr io.Writer,
) (int, error) {
	// The transport is what makes a door reachable: a client asks for
	// GET / on the cloud authority, and the door lives under a path prefix.
	// LocalTransport rewrites that, refusing any authority not declared here.
	transport, err := s3.NewLocalTransport(endpoint, profiles)
	if err != nil {
		return 0, err
	}
	hosts := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		hosts = append(hosts, profile.CloudHost)
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
	dir, err := os.MkdirTemp("", "feint-run-")
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
	fmt.Fprintf(stdout, "feint run: proxy on %s, authority at %s\n", proxyURL, caPath)
	for _, profile := range profiles {
		fmt.Fprintf(stdout, "  https://%s -> %s%s (TLS terminated with the run's own authority)\n",
			profile.CloudHost, endpoint, s3.DoorPath(profile.Provider, profile.Region))
	}
	fmt.Fprintln(stdout, "  every other host is refused, never tunnelled")
	if unsafe {
		fmt.Fprintln(stdout, "  --unsafe-real-credentials: the child's credentials were NOT checked")
	}

	child := exec.Command(command[0], command[1:]...) //nolint:gosec // the operator's own command, explicit argv, no shell
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, stdout, stderr
	child.Env = append(os.Environ(),
		"HTTP_PROXY="+proxyURL, "HTTPS_PROXY="+proxyURL,
		"http_proxy="+proxyURL, "https_proxy="+proxyURL,
		// The emulator itself must not go through the proxy, or a request to it
		// would be intercepted by the thing forwarding to it.
		"NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost",
		"SSL_CERT_FILE="+caPath,
		// Measured on the Terraform witness: the provider honours HTTPS_PROXY
		// and SSL_CERT_FILE, which is why this command needs no network
		// namespace and no privilege at all.
		"AWS_CA_BUNDLE="+caPath,
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
