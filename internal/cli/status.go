package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
)

// What is running, and how much of the real API it is standing in for.
//
// The comparable emulators report "running" and a list of service names. This
// one already computes, in process, the route table and the conformance
// counters, and it ships a versioned coverage figure measured against the
// upstream SDK scan. So status answers the question those tools cannot: not just
// whether it is up, but how much of the cloud it is actually emulating, and how
// much of that a real client has driven this run.
//
// Every number here is read from the running process or from the committed
// artefacts. None is written down in this file, which is the same rule the
// README's generated tables follow.

// statusTimeout is short on purpose: status is often asked about an address
// where nothing runs, and a user waiting ten seconds to be told "not running"
// stops using the command.
const statusTimeout = 2 * time.Second

type healthResponse struct {
	Status    string   `json:"status"`
	Providers []string `json:"providers"`
	Resources int      `json:"resources"`
	Machines  string   `json:"machines"`
	// Capabilities is machine.Capabilities, imported rather than redeclared, and
	// a pointer because the wire distinguishes a driver that declared nothing
	// from one that declared everything false. This package has already paid for
	// hand-copying a shape the server publishes: the conformance view, with a key
	// nothing emitted, which made this very command print 0 for the number it
	// exists to report.
	Capabilities *machine.Capabilities `json:"capabilities"`
}

type routeResponse struct {
	Method    string `json:"method"`
	Path      string `json:"path"`
	Operation string `json:"operation"`
}

// The conformance view is the server's own type, imported rather than
// redeclared. The copy that used to live here named a key the server never
// emitted, so every decode failed and this command reported zero for the number
// it exists to report.

// statusReport is what status prints, and what --format json emits verbatim.
type statusReport struct {
	Running   bool             `json:"running"`
	Addr      string           `json:"addr"`
	PID       int              `json:"pid,omitempty"`
	Started   string           `json:"started_at,omitempty"`
	Stale     string           `json:"stale,omitempty"`
	Log       string           `json:"log,omitempty"`
	Machines  string           `json:"machines,omitempty"`
	Resources int              `json:"resources,omitempty"`
	Products  []productStatus  `json:"products,omitempty"`
	Providers []providerStatus `json:"providers,omitempty"`
	Contracts []string         `json:"contracts,omitempty"`
}

type providerStatus struct {
	Provider string `json:"provider"`
	Routes   int    `json:"routes"`
	Proven   int    `json:"proven_by_a_client"`
}

type productStatus struct {
	Product     string `json:"product"`
	Implemented int    `json:"implemented"`
	Declined    int    `json:"declined"`
	Unknown     int    `json:"unknown"`
	Total       int    `json:"total"`
}

func status(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	addr := fs.String("addr", DefaultAddr, "listen address to report on")
	coverageDir := fs.String("coverage", "coverage", "directory holding the versioned coverage artefacts")
	format := fs.String("format", "text", "output format: text or json")
	if err := fs.Parse(args); err != nil {
		return exitError
	}

	report := statusReport{Addr: *addr}

	// A recorded instance is optional. An emulator started in the foreground, in
	// a container or by another tool is still worth reporting on, so the record
	// enriches the answer rather than gating it.
	inst, err := loadInstance(*addr)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	if inst != nil {
		report.PID, report.Started, report.Log = inst.PID, inst.Started, inst.Log
		switch inst.alive() {
		case Dead:
			report.Stale = "recorded pid " + itoa(inst.PID) + " is gone; run `feint start` to replace the record"
		case Foreign:
			report.Stale = "recorded pid " + itoa(inst.PID) + " belongs to another process; the record is stale"
		case Alive:
		}
	}

	health, err := fetchHealth(*addr)
	if err == nil {
		report.Running = true
		report.Machines = health.Machines
		report.Resources = health.Resources
		report.Providers = provenPerProvider(*addr, health.Providers)
		if conf, confErr := fetchConformance(*addr); confErr == nil {
			report.Contracts = conf.Contracts
		}
	}
	report.Products = coverageStatus(*coverageDir)

	if *format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(report); encErr != nil {
			fmt.Fprintf(stderr, "feint: %v\n", encErr)
			return exitError
		}
		return exitOK
	}

	writeStatusText(stdout, report, health)
	// Always 0, including when nothing is running. Not running is a fact, not a
	// failure: a pipeline that tolerates a stopped emulator would break on an
	// exit 1, and a script that needs one to be up has `feint wait`, which exists
	// for exactly that and exits 1 on a timeout. `--format json` carries
	// `running` for anything in between.
	return exitOK
}

func writeStatusText(w io.Writer, r statusReport, health healthResponse) {
	if !r.Running {
		fmt.Fprintf(w, "not running on %s\n", r.Addr)
		if r.Stale != "" {
			fmt.Fprintf(w, "  stale: %s\n", r.Stale)
		}
		return
	}

	fmt.Fprintf(w, "running on %s", r.Addr)
	if r.PID > 0 {
		fmt.Fprintf(w, " (pid %d, since %s)", r.PID, r.Started)
	}
	fmt.Fprintln(w)
	if r.Stale != "" {
		fmt.Fprintf(w, "  warning: %s\n", r.Stale)
	}
	fmt.Fprintf(w, "  resources  %d\n", r.Resources)
	fmt.Fprintf(w, "  machines   %s", r.Machines)
	if health.Machines != "none" {
		// The capability that separates the runtime modes, said here rather than
		// left in a document nobody opens at this moment. A driver that declared
		// nothing gets "not declared", never "false": absent and refused are not
		// the same claim, and only one of them is ours to make.
		if health.Capabilities == nil {
			fmt.Fprint(w, " (isolation: not declared)")
		} else {
			fmt.Fprintf(w, " (isolation: %v)", health.Capabilities.Isolation)
		}
	}
	fmt.Fprintln(w)
	if len(r.Contracts) > 0 {
		fmt.Fprintf(w, "  contracts  %s\n", strings.Join(r.Contracts, ", "))
	}

	if len(r.Providers) > 0 {
		fmt.Fprintln(w, "\n  provider    routes  driven by a client")
		for _, p := range r.Providers {
			fmt.Fprintf(w, "  %-10s  %6d  %d\n", p.Provider, p.Routes, p.Proven)
		}
	}
	if len(r.Products) > 0 {
		fmt.Fprintln(w, "\n  upstream product   served  declined  untriaged   total")
		for _, p := range r.Products {
			fmt.Fprintf(w, "  %-16s  %7d  %8d  %9d  %6d\n",
				p.Product, p.Implemented, p.Declined, p.Unknown, p.Total)
		}
	}
}

func fetchHealth(addr string) (healthResponse, error) {
	var out healthResponse
	err := getJSON(healthURL(addr), statusTimeout, &out)
	return out, err
}

func fetchConformance(addr string) (emulator.ConformanceView, error) {
	var out emulator.ConformanceView
	err := getJSON(strings.Replace(healthURL(addr), "/health", "/conformance", 1), statusTimeout, &out)
	return out, err
}

// provenPerProvider counts what each pack mounts and what a real client has
// driven this run. Both come from the running process, so neither can disagree
// with what it is actually serving.
func provenPerProvider(addr string, providers []string) []providerStatus {
	var routes []routeResponse
	if err := getJSON(strings.Replace(healthURL(addr), "/health", "/routes", 1), statusTimeout, &routes); err != nil {
		return nil
	}
	conf, err := fetchConformance(addr)
	if err != nil {
		conf = emulator.ConformanceView{}
	}

	counts := map[string]int{}
	for _, r := range routes {
		counts[productOf(r.Operation)]++
	}
	// The route table is keyed by upstream product, the conformance report by
	// provider. Mapping one onto the other needs the pack's own list, which the
	// health endpoint gives.
	byProvider := map[string]*providerStatus{}
	for _, name := range providers {
		byProvider[name] = &providerStatus{Provider: name}
	}
	for product, n := range counts {
		if p, ok := byProvider[providerOfProduct(product, providers)]; ok {
			p.Routes += n
		}
	}
	// Counted from the calls a real client made, keyed by operation, which is
	// what the server publishes. The old code read a per-provider map that never
	// existed on the wire.
	for operation, calls := range conf.Calls {
		if calls == 0 {
			continue
		}
		if p, ok := byProvider[providerOfProduct(productOf(operation), providers)]; ok {
			p.Proven++
		}
	}

	out := make([]providerStatus, 0, len(byProvider))
	for _, p := range byProvider {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Routes > out[j].Routes })
	return out
}

// providerOfProduct maps an upstream product onto the pack serving it. A product
// whose name is a provider's own maps directly; anything else is attributed by
// the coverage artefacts, and failing that to the first provider — which only
// affects a display, never a gate.
func providerOfProduct(product string, providers []string) string {
	for _, name := range providers {
		if product == name {
			return name
		}
	}
	for _, name := range providers {
		if productsOfProvider[name][product] {
			return name
		}
	}
	if len(providers) > 0 {
		return providers[0]
	}
	return product
}

// productsOfProvider is the one place a product is tied to a pack, and it is
// display-only. It exists because the route table names products and the
// conformance report names providers, and nothing in the wire format joins them.
//
// It is not a rule-5 violation of internal/core — this is the CLI, not the core
// — but it is the kind of table that goes stale, so status degrades to an
// approximate attribution rather than failing when a product is missing from it.
var productsOfProvider = map[string]map[string]bool{
	"scaleway": {"instance": true, "vpc": true, "ipam": true, "iam": true, "marketplace": true},
	"outscale": {"osc": true, "oks": true},
	"exoscale": {"exoscale": true},
}

// coverageStatus reads the committed coverage artefacts. Absent artefacts are
// not an error: a user who installed the binary has no coverage/ directory, and
// status must still work for them.
func coverageStatus(dir string) []productStatus {
	reports, err := loadCoverage(dir)
	if err != nil {
		return nil
	}
	var out []productStatus
	for _, rep := range reports {
		for _, p := range rep.Products {
			out = append(out, productStatus{
				Product:     p.Product,
				Implemented: p.Implemented,
				Declined:    p.Declined,
				Unknown:     p.Unknown,
				Total:       p.Total,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Product < out[j].Product })
	return out
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
