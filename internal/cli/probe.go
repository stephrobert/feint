package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/probe"
)

// probeCommand drives every mounted route from the provider's own API
// description and checks the answers against it.
//
// It complements the suites under tools/conformance/ and replaces none of them.
// Those drive the real clients and prove behaviour; this proves the protocol,
// for every route, without a case written per operation — which is what makes
// the hundred and fifty operations still to serve affordable.
//
// What it reports never moves the conformance score. A probed route stays on the
// list of routes nobody has proven until a real client drives it.
func probeCommand(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("probe")
	endpoint := fs.String("endpoint", "http://"+DefaultAddr, "the running emulator")
	contracts := fs.String("contracts", "contracts", "directory of API contracts")
	provider := fs.String("provider", "", "probe only this provider")
	if err := fs.Parse(args); err != nil {
		return exitError
	}

	docs, err := loadContracts(*contracts)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	// A synthetic run creates machines when a runtime is configured, and a probe
	// walking every Create* would start a container per route on the operator's
	// host. Refused rather than throttled: the probe is a protocol check, and
	// booting machines is not part of what it proves.
	if driver, err := runtimeOf(*endpoint); err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	} else if driver != "none" {
		fmt.Fprintf(stderr, "feint: the emulator runs with --vm %s; probing would start "+
			"a machine per route. Restart it with --vm off to probe.\n", driver)
		return exitError
	}

	routes, err := mountedRoutes(*endpoint)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	failed := false
	for name, doc := range docs {
		if *provider != "" && *provider != name {
			continue
		}
		runner := &probe.Runner{Doc: doc, Base: *endpoint}
		report, err := runner.Run(context.Background(), routesOf(routes, doc))
		if err != nil {
			fmt.Fprintf(stderr, "feint: probe %s: %v\n", name, err)
			failed = true
			continue
		}
		fmt.Fprintln(stdout, report)
		// A run that attempted everything and proved nothing is a failure even
		// with no violation to point at: it means every call was refused, and a
		// probe that goes green on that measures nothing.
		if report.Barren() {
			fmt.Fprintf(stderr,
				"feint: probe %s attempted %d operation(s) and proved none; every call was refused\n",
				name, len(report.Results))
			failed = true
		}
		if len(report.Failures()) > 0 {
			failed = true
		}
	}
	if failed {
		return exitError
	}
	return exitOK
}

// runtimeOf asks the emulator which machine driver it runs with.
func runtimeOf(endpoint string) (string, error) {
	var health struct {
		Machines string `json:"machines"`
	}
	if err := getJSON(endpoint+"/_feint/health", 10*time.Second, &health); err != nil {
		return "", fmt.Errorf("the emulator does not answer at %s: %w", endpoint, err)
	}
	return health.Machines, nil
}

// mountedRoutes reads the route table from the running emulator rather than
// rebuilding the packs, so the probe measures what is actually served.
func mountedRoutes(endpoint string) ([]contract.MountedRoute, error) {
	var routes []struct {
		Method    string `json:"method"`
		Path      string `json:"path"`
		Operation string `json:"operation"`
	}
	if err := getJSON(endpoint+"/_feint/routes", 10*time.Second, &routes); err != nil {
		return nil, fmt.Errorf("read the route table: %w", err)
	}
	out := make([]contract.MountedRoute, 0, len(routes))
	for _, r := range routes {
		out = append(out, contract.MountedRoute{Method: r.Method, Path: r.Path, Operation: r.Operation})
	}
	return out, nil
}

// routesOf keeps the routes one contract describes — name, method and path,
// not name alone: Outscale's document resolves any bare operationId, and a
// Scaleway route matched by name used to be probed against the Outscale path
// (see contract.Doc.Owns).
func routesOf(routes []contract.MountedRoute, doc *contract.Doc) []contract.MountedRoute {
	out := make([]contract.MountedRoute, 0, len(routes))
	for _, r := range routes {
		if doc.Owns(r) {
			out = append(out, r)
		}
	}
	return out
}

// getJSON reads a JSON document, with the caller choosing how long to wait.
//
// The timeout is a parameter rather than a constant because the two callers want
// opposite things: the probe reads a route table from an emulator it knows is up
// and can afford ten seconds, while status asks an address that may well be
// dead and must answer at once rather than hang for a third of a minute.
func getJSON(url string, timeout time.Duration, into any) error {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", url, res.StatusCode)
	}
	return json.NewDecoder(res.Body).Decode(into)
}
