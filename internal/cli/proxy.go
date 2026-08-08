package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
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
	fs := flag.NewFlagSet("proxy", flag.ContinueOnError)
	upstream := fs.String("upstream", "", "the cloud (or emulator) to forward to, e.g. https://api.scaleway.com")
	addr := fs.String("addr", DefaultProxyAddr, "listen address")
	record := fs.String("record", "", "write the transcript here as JSON Lines; - for stdout")
	provider := fs.String("provider", "", "name operations from this pack alone (default: all of them)")
	maxBody := fs.Int("max-body", proxy.DefaultMaxBody, "record at most this many bytes of each body; the rest is declared, never silently cut")
	queue := fs.Int("queue", proxy.DefaultQueue, "how many exchanges may wait to be written before one is dropped")
	expose := fs.Bool("expose-to-network", false, "listen off loopback, which offers this proxy's transcript and this cloud account to the network")
	if err := fs.Parse(args); err != nil {
		return exitError
	}

	if *upstream == "" {
		fmt.Fprintln(stderr, "feint: --upstream is required: a proxy with nothing to forward to records nothing")
		return exitError
	}
	if *record == "" {
		fmt.Fprintln(stderr, "feint: --record is required: pass a path, or - for stdout")
		return exitError
	}
	if err := checkProxyAddr(*addr, *expose); err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
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
	p, err := proxy.New(proxy.Options{
		Upstream: target,
		Writer:   writer,
		Table:    table,
		MaxBody:  *maxBody,
		Log:      slog.New(slog.NewTextHandler(stderr, nil)),
	})
	if err != nil {
		_ = writer.Close()
		_ = closeOut()
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	// Everything this command says goes to stderr, without exception: --record -
	// puts the transcript on stdout, and a status line landing in the middle of a
	// JSON Lines stream would break every reader of it.
	fmt.Fprintf(stderr, "feint proxy listening on %s\n", *addr)
	fmt.Fprintf(stderr, "  upstream  %s\n", target)
	fmt.Fprintf(stderr, "  recording %s\n", *record)
	fmt.Fprintf(stderr, "  naming    %s\n", namingOf(*provider))
	fmt.Fprintf(stderr, "  point a client at http://%s and drive it as usual\n", *addr)

	srv := &http.Server{
		Addr:    *addr,
		Handler: p,
		// The only timeout set, and the omissions are deliberate: a real cloud
		// answering a large list is allowed to take its time, and a proxy that
		// cut the response would be blamed for the cloud's latency. This one
		// bounds the headers alone, which is what stops a stuck connection
		// holding a goroutine for ever.
		ReadHeaderTimeout: 30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errs := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
	if unnamed := p.Unnamed(); unnamed > 0 {
		// The product, printed rather than left in the file for someone to grep
		// for: an exchange no pack claims is a route a real client walks and this
		// emulator does not serve. That is the list #74 will rank.
		fmt.Fprintf(stderr, "%d of them walked a route no pack claims: grep '\"operation\":\"\"' or filter on .mounted == false\n",
			unnamed)
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

// checkProxyAddr refuses to offer the proxy to the network unless asked.
//
// The reason is not serve's. There is no browser guard here and nothing to
// rebind: what an open port on this proxy offers is a relay to a real cloud and,
// with it, a transcript of somebody else's traffic written into the operator's
// file. Loopback by default, and an operator who wants otherwise says so in a
// flag they can be held to.
//
// A function rather than a branch inside proxyCommand, for the reason
// checkListenAddr is one: with the refusal removed, a test that drove the
// command would find it listening and never return. TestProxyRefusesANonLoopbackAddress
// fails without this.
func checkProxyAddr(addr string, expose bool) error {
	if emulator.LoopbackListen(addr) || expose {
		return nil
	}
	return fmt.Errorf("refusing to listen on %s: every request through this proxy carries a real "+
		"credential, and off loopback anyone who can reach the port can relay through it and "+
		"land in your transcript. Pass --expose-to-network if that is what you want", addr)
}

// proxyTable builds the route table an exchange is named from.
//
// It is the emulator's own, built from the same packs it mounts, so the proxy
// cannot name an operation differently from the thing it is comparing against.
func proxyTable(provider string) (*emulator.Table, error) {
	env := emulator.DefaultEnv()
	all := packsFor(env)
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
