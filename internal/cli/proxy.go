package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/proxy"
)

// DefaultProxyAddr is where `feint proxy` listens.
//
// Loopback for the same reason serve is, and one worse: every request through
// this port carries a real credential belonging to whoever started it. 4600 sits
// beside the emulator's 4599 so both can run at once, which is what the
// two-observer check needs.
const DefaultProxyAddr = "127.0.0.1:4600"

// proxyCommand records what a real client and a real cloud say to each other.
func proxyCommand(args []string, _ io.Writer, stderr io.Writer) int {
	fs := newFlagSet("proxy")
	upstream := fs.String("upstream", "", "the cloud (or emulator) to forward to, e.g. https://api.scaleway.com")
	addr := fs.String("addr", DefaultProxyAddr, "listen address")
	record := fs.String("record", "", "write the transcript here as JSON Lines; - for stdout")
	provider := fs.String("provider", "", "name operations from this pack alone (default: all of them)")
	maxBody := fs.Int("max-body", proxy.DefaultMaxBody, "record at most this many bytes of each body; the rest is declared, never silently cut")
	queue := fs.Int("queue", proxy.DefaultQueue, "how many exchanges may wait to be written before one is dropped")
	expose := fs.Bool("expose-to-network", false, "listen off loopback, which offers this proxy's transcript and this cloud account to the network")
	intercept := fs.String("intercept", "", "serve HTTPS with a locally-minted certificate for these comma-separated hostnames, so a client redirected to this proxy by name (a container's own /etc/hosts) trusts it and lands here; see docs/limits.md #76")
	forward := fs.String("forward", "", "be a forward proxy for these comma-separated hostnames: accept CONNECT, terminate the TLS with a locally-minted certificate and record it, so a client whose endpoint is compiled in is recorded through HTTPS_PROXY alone. An entry may name where its traffic then goes, host=target (api.scaleway.com=http://127.0.0.1:4599); written bare it goes to the real host. See docs/proxy.md")
	if err := fs.Parse(args); err != nil {
		return exitError
	}

	if err := checkProxyMode(*upstream, *forward, *intercept); err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	if *record == "" {
		fmt.Fprintln(stderr, "feint: --record is required: pass a path, or - for stdout")
		return exitError
	}
	if err := checkProxyAddr(*addr, *expose, *forward != ""); err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	// Parsed even in forward mode, where it is empty and unused: url.Parse of ""
	// is a valid empty URL, and the branch that skipped it would be one more
	// thing to keep in step for nothing.
	target, err := url.Parse(*upstream)
	if err != nil {
		fmt.Fprintf(stderr, "feint: --upstream %q: %v\n", *upstream, err)
		return exitError
	}

	table, err := proxyTable(*provider)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	out, closeOut, err := transcriptFile(*record)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	writer := proxy.NewWriter(out, *queue)
	logger := slog.New(slog.NewTextHandler(stderr, nil))

	// rec is what listens. The two modes differ in where a request goes and in
	// nothing else: both record through this writer, by the same capture, under
	// the same redaction. A forward proxy that had its own recording path would
	// be a second door to the transcript, which is what proxy.Redacted exists to
	// make impossible.
	var (
		rec       proxyRecorder
		forwarder *proxy.Forward
	)
	if *forward != "" {
		authority, caPath, removeCA, err := mintTemporaryAuthority()
		if err != nil {
			_ = writer.Close()
			_ = closeOut()
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
		// The CA outlives nothing: it is a temporary file removed when this
		// command returns, whatever the exit path, and it is never installed
		// anywhere a process could trust it by accident.
		defer removeCA()
		forwarder, err = proxy.NewForward(proxy.ForwardOptions{
			Hosts:     splitHosts(*forward),
			Writer:    writer,
			Table:     table,
			MaxBody:   *maxBody,
			Log:       logger,
			Authority: authority,
		})
		if err != nil {
			_ = writer.Close()
			_ = closeOut()
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
		rec = forwarder
		printForwardRecipe(stderr, forwarder.Destinations(), *addr, caPath)
	} else {
		p, err := proxy.New(proxy.Options{
			Upstream: target,
			Writer:   writer,
			Table:    table,
			MaxBody:  *maxBody,
			Log:      logger,
		})
		if err != nil {
			_ = writer.Close()
			_ = closeOut()
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
		rec = p
	}

	// Everything this command says goes to stderr, without exception: --record -
	// puts the transcript on stdout, and a status line landing in the middle of a
	// JSON Lines stream would break every reader of it.
	fmt.Fprintf(stderr, "feint proxy listening on %s\n", *addr)
	if *forward == "" {
		fmt.Fprintf(stderr, "  upstream  %s\n", target)
	}
	fmt.Fprintf(stderr, "  recording %s\n", *record)
	fmt.Fprintf(stderr, "  naming    %s\n", namingOf(*provider))
	if *intercept == "" && *forward == "" {
		fmt.Fprintf(stderr, "  point a client at http://%s and drive it as usual\n", *addr)
	}

	srv := &http.Server{
		Addr:    *addr,
		Handler: rec,
		// The only timeout set, and the omissions are deliberate: a real cloud
		// answering a large list is allowed to take its time, and a proxy that
		// cut the response would be blamed for the cloud's latency. This one
		// bounds the headers alone, which is what stops a stuck connection
		// holding a goroutine for ever.
		ReadHeaderTimeout: 30 * time.Second,
	}

	// serve is the listener the run blocks on. Plain HTTP by default; HTTPS with a
	// minted certificate when --intercept names the hosts a redirected client will
	// address. The certificate half of #76: everything after this is identical.
	serve := srv.ListenAndServe
	if *intercept != "" {
		cfg, cleanup, err := setUpInterception(*intercept, *addr, stderr)
		if err != nil {
			_ = writer.Close()
			_ = closeOut()
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
		defer cleanup()
		srv.TLSConfig = cfg
		serve = func() error { return srv.ListenAndServeTLS("", "") }
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errs := make(chan error, 1)
	go func() {
		if err := serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	var runErr error
	select {
	case runErr = <-errs:
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			runErr = fmt.Errorf("shutdown: %w", err)
		}
	}

	// Ordered: the listener stops, then the writer drains, then the file closes.
	// Closing the file first would truncate the transcript at whatever the writer
	// had not yet handed over, which is the one failure this whole design is
	// arranged to avoid.
	writeErr := writer.Close()
	closeErr := closeOut()

	written, dropped := writer.Stats()
	fmt.Fprintf(stderr, "\nrecorded %d exchange(s) to %s\n", written, *record)
	if forwarder != nil {
		fmt.Fprintf(stderr, "%d tunnel(s) terminated\n", forwarder.Tunnels())
		if refused, hosts := forwarder.Refused(); refused > 0 {
			// Named rather than counted alone: every refusal is a call the
			// client made and this transcript does not carry, and the operator
			// can only tell "the client tried something else" from "I forgot a
			// host" by reading which.
			fmt.Fprintf(stderr,
				"%d connection(s) were refused because --forward does not name their host: %s\n"+
					"  the client's own calls to them failed, and none of them is in this transcript.\n"+
					"  add the missing entries: --forward ...,%s\n",
				refused, strings.Join(sortedHosts(hosts), ", "), strings.Join(sortedHosts(hosts), ","))
		}
	}
	if unnamed := rec.Unnamed(); unnamed > 0 {
		// The product, printed rather than left in the file for someone to grep
		// for: an exchange no pack claims is a route a real client walks and this
		// emulator does not serve. That is the list #74 will rank.
		fmt.Fprintf(stderr, "%d of them walked a route no pack claims: grep '\"operation\":\"\"' or filter on .mounted == false\n",
			unnamed)
	}
	if handed, hosts := rec.HandedElsewhere(); handed > 0 {
		// The other half of the product, and the one that would otherwise be
		// invisible: an answer that gave the client a different address. If the
		// client followed it, everything after this point left no trace here,
		// and a transcript that stops early is indistinguishable from a session
		// that ended early. Measured on Exoscale: a session worth about ninety
		// exchanges recorded eight, and nothing said so.
		fmt.Fprintf(stderr,
			"%d response(s) handed the client an address that is not this proxy: %s\n"+
				"  anything the client sent there is absent from this transcript. See docs/proxy.md.\n",
			handed, strings.Join(sortedHosts(hosts), ", "))
	}
	if dropped > 0 {
		fmt.Fprintf(stderr, "%d exchange(s) were dropped because the transcript could not keep up; "+
			"the gaps in \"seq\" are where. Raise --queue.\n", dropped)
	}

	for _, err := range []error{runErr, writeErr, closeErr} {
		if err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
	}
	return exitOK
}

// proxyRecorder is what `feint proxy` mounts: a handler that also answers what
// its own transcript does not cover.
//
// The interface exists so the summary at the end of a run is written once. Both
// modes produce the same two findings — an exchange no pack names, an answer
// that handed the client an address — and a second copy of that reporting is a
// second place for one of them to be forgotten.
type proxyRecorder interface {
	http.Handler
	Unnamed() int64
	HandedElsewhere() (int64, map[string]int64)
}

// checkProxyMode refuses an invocation that names two doors, or none.
//
// The two doors do opposite things with the address a client asks for.
// --upstream sends every request to the one host the operator named, whatever
// the client asked; --forward sends each request to the host the client asked
// for, which is what a CONNECT names. An invocation carrying both has not said
// which, and picking one silently is how an operator ends up with a transcript
// of the wrong endpoint. --intercept is the third: it serves TLS on the
// listener itself, for a client redirected by name, and that is the same
// question answered a different way.
// TestProxyRefusesTwoDoorsAtOnce fails without this, and TestProxyRefusesAnUnusableInvocation
// drives the same refusals through the command.
func checkProxyMode(upstream, forward, intercept string) error {
	switch {
	case upstream != "" && forward != "":
		return fmt.Errorf("--upstream and --forward are two different proxies: --upstream sends every " +
			"request to one host you chose, --forward sends each one to the host the client asked for. " +
			"Pass one")
	case forward != "" && intercept != "":
		return fmt.Errorf("--forward and --intercept are two ways to reach the same interception: " +
			"--forward accepts CONNECT from a client honouring HTTPS_PROXY, --intercept serves TLS to " +
			"a client redirected to this proxy by name. Pass one")
	case upstream == "" && forward == "":
		return fmt.Errorf("--upstream is required: a proxy with nothing to forward to records nothing " +
			"(or --forward, to record whichever host the client asks for)")
	}
	return nil
}

// checkProxyAddr refuses to offer the proxy to the network unless asked, and
// refuses it outright in forward mode.
//
// The reason is not serve's. There is no browser guard here and nothing to
// rebind: what an open port on this proxy offers is a relay to a real cloud and,
// with it, a transcript of somebody else's traffic written into the operator's
// file. Loopback by default, and an operator who wants otherwise says so in a
// flag they can be held to.
//
// --forward does not get that flag. A forward proxy holds an authority whose
// certificates a client has been told to trust, so off loopback it is not a
// relay but a machine that decrypts and files whatever any reachable client
// sends it — including credentials belonging to someone who never started it.
// The reverse proxy at least only ever reaches the one upstream its operator
// named. TestAForwardProxyIsRefusedOffLoopback fails without this.
//
// A function rather than a branch inside proxyCommand, for the reason
// checkListenAddr is one: with the refusal removed, a test that drove the
// command would find it listening and never return. TestProxyRefusesANonLoopbackAddress
// fails without this.
func checkProxyAddr(addr string, expose, forward bool) error {
	if forward && expose {
		return fmt.Errorf("refusing --expose-to-network with --forward: this proxy mints certificates " +
			"a client has been told to trust, so off loopback it decrypts and records whatever anyone " +
			"who can reach the port sends through it. Record on loopback")
	}
	if emulator.LoopbackListen(addr) || expose {
		return nil
	}
	return fmt.Errorf("refusing to listen on %s: every request through this proxy carries a real "+
		"credential, and off loopback anyone who can reach the port can relay through it and "+
		"land in your transcript. Pass --expose-to-network if that is what you want", addr)
}

// setUpInterception mints the certificate a redirected client must trust, writes
// its CA where SSL_CERT_FILE can find it, and prints the two-line recipe that
// makes the redirect disposable.
//
// It writes nothing durable and installs nothing: the CA lands in a temporary
// file that cleanup removes, never in the system trust store, and the name
// redirect is left to the operator's own namespace — a container's /etc/hosts —
// never this machine's. That scoping is the safety argument of docs/limits.md's
// #76 section, and this function keeps to it by construction: it cannot touch the
// operator's /etc/hosts because it never writes one.
func setUpInterception(hosts, addr string, stderr io.Writer) (*tls.Config, func(), error) {
	names := splitHosts(hosts)
	if len(names) == 0 {
		return nil, nil, fmt.Errorf("--intercept was given no hostname")
	}
	ic, err := proxy.MintInterceptor(names...)
	if err != nil {
		return nil, nil, fmt.Errorf("mint the interception certificate: %w", err)
	}
	caPath, cleanup, err := publishCA(ic)
	if err != nil {
		return nil, nil, err
	}

	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		port = "4600"
	}
	fmt.Fprintf(stderr, "  intercepting HTTPS for %s\n", strings.Join(names, ", "))
	fmt.Fprintf(stderr, "  CA written to %s (a temporary file, removed on exit)\n", caPath)
	fmt.Fprintln(stderr, "  point a client at this proxy by name, in a namespace of its own, e.g.:")
	fmt.Fprintf(stderr, "    export SSL_CERT_FILE=%s\n", caPath)
	for _, n := range names {
		fmt.Fprintf(stderr, "    # resolve %s to this proxy (a container's own /etc/hosts, never yours):\n", n)
		fmt.Fprintf(stderr, "    #   podman run --add-host=%s:host-gateway ... , proxy reachable on :%s\n", n, port)
	}
	return ic.ServerTLSConfig(), cleanup, nil
}

// mintTemporaryAuthority builds the authority a forward proxy signs with, and
// publishes its CA where the client can be told to trust it.
//
// The names are not known here and cannot be: a forward proxy learns them one
// CONNECT at a time. What is known before the client starts is the CA, which is
// exactly what SSL_CERT_FILE needs to point at.
func mintTemporaryAuthority() (*proxy.Interceptor, string, func(), error) {
	authority, err := proxy.MintAuthority()
	if err != nil {
		return nil, "", nil, fmt.Errorf("mint the interception authority: %w", err)
	}
	caPath, cleanup, err := publishCA(authority)
	if err != nil {
		return nil, "", nil, err
	}
	return authority, caPath, cleanup, nil
}

// publishCA writes an interception CA to a temporary file, and returns how to
// remove it.
//
// A temporary file and never the system trust store, which is the whole safety
// argument: the certificates this run mints are trusted by the one process the
// operator points at this file, for as long as the command runs, and by nothing
// else afterwards. TestTheInterceptionCAIsTemporaryAndNeverInstalled fails if
// this writes anywhere durable.
func publishCA(ic *proxy.Interceptor) (string, func(), error) {
	caFile, err := os.CreateTemp("", "feint-intercept-ca-*.pem")
	if err != nil {
		return "", nil, fmt.Errorf("create the CA file: %w", err)
	}
	caPath := caFile.Name()
	_ = caFile.Close()
	if err := ic.WriteCA(caPath); err != nil {
		_ = os.Remove(caPath)
		return "", nil, err
	}
	return caPath, func() { _ = os.Remove(caPath) }, nil
}

// printForwardRecipe says what to export so a client with a compiled-in endpoint
// lands here.
//
// Two variables and nothing else — no /etc/hosts, no system trust store, no
// change in the client — which is what makes this door the cheap one. The
// warning is part of the recipe: what this records is decrypted, so it is said
// where the operator is about to run the command rather than only in the docs.
//
// destinations is one line per entry, each naming where that host's traffic is
// re-originated. Printed rather than summarised, because "the real cloud" and
// "the emulator" are the two answers an operator must never confuse, and #357
// made them differ per host.
func printForwardRecipe(stderr io.Writer, destinations []string, addr, caPath string) {
	fmt.Fprintln(stderr, "  forwarding for:")
	for _, d := range destinations {
		fmt.Fprintf(stderr, "    %s\n", d)
	}
	fmt.Fprintf(stderr, "  CA written to %s (a temporary file, removed on exit)\n", caPath)
	fmt.Fprintln(stderr, "  drive a client whose endpoint is compiled in with:")
	fmt.Fprintf(stderr, "    export HTTPS_PROXY=http://%s\n", addr)
	fmt.Fprintf(stderr, "    export SSL_CERT_FILE=%s\n", caPath)
	fmt.Fprintln(stderr, "  every exchange with those hosts is decrypted and written to the transcript;")
	fmt.Fprintln(stderr, "  a CONNECT to any other host is refused, and reported at exit.")
}

// splitHosts turns the comma list --intercept takes into trimmed, non-empty
// names. A trailing comma or a stray space must not become an empty hostname the
// mint then refuses.
func splitHosts(list string) []string {
	var out []string
	for _, part := range strings.Split(list, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// proxyTable builds the route table an exchange is named from.
//
// It is the emulator's own, built from the same packs it mounts, so the proxy
// cannot name an operation differently from the thing it is comparing against.
func proxyTable(provider string) (*emulator.Table, error) {
	env := emulator.DefaultEnv()
	all, err := packsFor(env)
	if err != nil {
		return nil, err
	}
	if provider == "" {
		return emulator.NewTable(all...)
	}
	for _, p := range all {
		if p.Name() == provider {
			return emulator.NewTable(p)
		}
	}
	names := make([]string, 0, len(all))
	for _, p := range all {
		names = append(names, p.Name())
	}
	return nil, fmt.Errorf("unknown provider %q (%v)", provider, names)
}

func namingOf(provider string) string {
	if provider == "" {
		return "every pack"
	}
	return provider + " only"
}

// transcriptFile opens where the transcript goes, and returns how to close it.
//
// 0600 because a transcript is a record of an operator's own session against
// their own account: redacted of credentials, and still a list of what they have
// and what they did with it.
func transcriptFile(path string) (io.Writer, func() error, error) {
	if path == "-" {
		return os.Stdout, func() error { return nil }, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // operator-supplied path, by design
	if err != nil {
		return nil, nil, fmt.Errorf("open the transcript %s: %w", path, err)
	}
	return f, f.Close, nil
}

// sortedHosts names the hosts a run was handed, most seen first, then
// alphabetically so two identical runs print identically.
func sortedHosts(hosts map[string]int64) []string {
	out := make([]string, 0, len(hosts))
	for host := range hosts {
		out = append(out, host)
	}
	sort.Slice(out, func(i, j int) bool {
		if hosts[out[i]] != hosts[out[j]] {
			return hosts[out[i]] > hosts[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}
