package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/upstream"
)

// The per-operation evidence record (#123): what is proven about an operation
// is a set of named, independent axes, published — never a level on a ladder,
// never a number. This file is the half that lives outside the emulator
// process: joining the observed shapes onto mounted operations (the core must
// not know providers or where recordings live), and turning a run's live
// /_feint/conformance view into the versioned artefact the documentation
// renders.
//
// The artefact is a snapshot of stated runs, never an accumulation. Merging a
// fresh run with a stale artefact would let a level survive the removal of
// its evidence, which is the one property this record must not have: the
// /falsify criterion is that deleting a conformance assertion demotes the
// operations it proved at the next regeneration. So --join only exists for
// the two legs of one `mise run evidence:update`, and the artefact carries
// the runtimes its runs were backed by, so a reader knows the conditions.

// evidenceFormat and evidenceVersion follow the snapshot doctrine (#133): a
// file that names what it is can be refused when it is something else.
const (
	evidenceFormat  = "feint-evidence"
	evidenceVersion = 1
)

// evidenceArtefact is coverage/evidence.json.
type evidenceArtefact struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	// Machines lists the runtime behind each contributing run, sorted. The
	// dataplane axis only means something next to this list.
	Machines []string `json:"machines"`
	// Operations maps every mounted operation to its evidence.
	Operations map[string]emulator.Evidence `json:"operations"`
}

func evidence(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("evidence", flag.ContinueOnError)
	endpoint := fs.String("endpoint", "http://"+DefaultAddr, "the running emulator to read /_feint/conformance from")
	shapesDir := fs.String("shapes", "shapes", "directory of observed real-cloud shapes; the shape axis is resolved from it (empty to leave the run's answer)")
	out := fs.String("out", filepath.Join("coverage", "evidence.json"), "where to write the artefact")
	join := fs.String("join", "", "an artefact from another leg of the same regeneration, merged in (fresh runs only; see the header comment)")
	if err := fs.Parse(args); err != nil {
		return exitError
	}

	var view emulator.ConformanceView
	if err := getJSON(*endpoint+"/_feint/conformance", statusTimeout, &view); err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	if len(view.Evidence) == 0 {
		fmt.Fprintf(stderr, "feint: %s published no evidence; is this emulator older than the record it feeds?\n", *endpoint)
		return exitError
	}

	art := &evidenceArtefact{
		Format:     evidenceFormat,
		Version:    evidenceVersion,
		Machines:   []string{view.Machines},
		Operations: view.Evidence,
	}

	// The shape axis is static — a property of the catalogue, not of the run —
	// so it is resolved here rather than trusted from however the server was
	// launched: a serve without --shapes answers "unknown", and an artefact
	// must do better when the catalogue sits in the tree.
	if *shapesDir != "" {
		covered, err := shapeCoveredOperations(*shapesDir)
		if err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
		if covered != nil {
			for op, ev := range art.Operations {
				ev.Shape = emulator.ShapeUnobserved
				if covered[op] {
					ev.Shape = emulator.ShapeObserved
				}
				art.Operations[op] = ev
			}
		}
	}

	if *join != "" {
		other, err := readEvidence(*join)
		if err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
		art = joinEvidence(art, other)
	}

	blob, err := json.MarshalIndent(art, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	if err := os.WriteFile(*out, append(blob, '\n'), 0o644); err != nil { //nolint:gosec // a versioned artefact is world-readable by design
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	fmt.Fprintf(stdout, "evidence for %d operation(s) written to %s (machines: %s)\n",
		len(art.Operations), *out, strings.Join(art.Machines, ", "))
	return exitOK
}

// readEvidence loads an artefact, refusing what it cannot account for — the
// same bar Restore holds a snapshot to.
func readEvidence(path string) (*evidenceArtefact, error) {
	blob, err := os.ReadFile(path) //nolint:gosec // operator-supplied path, by design
	if err != nil {
		return nil, err
	}
	var art evidenceArtefact
	if err := json.Unmarshal(blob, &art); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if art.Format != evidenceFormat {
		return nil, fmt.Errorf("%s is not an evidence artefact (format %q)", path, art.Format)
	}
	if art.Version != evidenceVersion {
		return nil, fmt.Errorf("%s carries version %d, this binary writes %d; regenerate rather than mix", path, art.Version, evidenceVersion)
	}
	return &art, nil
}

// joinEvidence merges the two legs of one regeneration. Booleans take the
// stronger answer; the contract verdict keeps a violation over anything, and
// a check over silence; shape keeps an observation. That ordering is safe
// only because both legs are fresh: neither can carry a proof whose evidence
// has since been deleted.
func joinEvidence(a, b *evidenceArtefact) *evidenceArtefact {
	machines := map[string]bool{}
	for _, m := range append(append([]string{}, a.Machines...), b.Machines...) {
		machines[m] = true
	}
	names := make([]string, 0, len(machines))
	for m := range machines {
		names = append(names, m)
	}
	sort.Strings(names)

	ops := make(map[string]emulator.Evidence, len(a.Operations))
	for op, ev := range a.Operations {
		ops[op] = ev
	}
	for op, other := range b.Operations {
		ev, seen := ops[op]
		if !seen {
			ops[op] = other
			continue
		}
		ev.Driven = ev.Driven || other.Driven
		ev.Probed = ev.Probed || other.Probed
		ev.Dataplane = ev.Dataplane || other.Dataplane
		ev.Behaviour = ev.Behaviour || other.Behaviour
		ev.Negative = ev.Negative || other.Negative
		ev.Contract = strongerContract(ev.Contract, other.Contract)
		ev.Shape = strongerShape(ev.Shape, other.Shape)
		ops[op] = ev
	}
	return &evidenceArtefact{Format: evidenceFormat, Version: evidenceVersion, Machines: names, Operations: ops}
}

func strongerContract(a, b string) string {
	rank := map[string]int{emulator.ContractViolating: 2, emulator.ContractClean: 1, emulator.ContractUnchecked: 0}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func strongerShape(a, b string) string {
	rank := map[string]int{emulator.ShapeObserved: 2, emulator.ShapeUnobserved: 1, emulator.ShapeUnknown: 0}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// shapeCoveredOperations maps the observed catalogues onto mounted
// operations: for every entry of the curated read list that the catalogue
// covers with at least one field — the same bar the shapes gate applies — the
// mounted route answering that call contributes its operation name.
//
// nil with no error means no catalogue exists at all, which callers report as
// "unknown" rather than demoting every operation to "unobserved".
func shapeCoveredOperations(dir string) (map[string]bool, error) {
	srv, _, err := newServer(nil)
	if err != nil {
		return nil, err
	}
	routes := srv.AllRoutes()

	covered := map[string]bool{}
	found := false
	for _, p := range srv.Packs() {
		cat, err := readCatalogue(filepath.Join(dir, p.Name()+".json"), p.Name())
		if err != nil {
			return nil, err
		}
		if cat == nil {
			continue
		}
		found = true
		provider := upstream.Provider(p.Name())
		for _, call := range upstream.Reads[provider] {
			key, method, path := callIdentity(provider, call)
			want, known := cat.Operations[key]
			if !known || len(want.Fields) == 0 {
				continue
			}
			op, err := operationForCall(routes, method, path)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			if op != "" {
				covered[op] = true
			}
		}
	}
	if !found {
		return nil, nil
	}
	return covered, nil
}

// operationForCall resolves the mounted route a concrete request would reach.
// Among the patterns that match, the one with the fewest wildcards wins — the
// same specificity rule the mux applies — and a tie is refused out loud
// rather than resolved by luck.
func operationForCall(routes []emulator.Route, method, path string) (string, error) {
	best, bestWild, ties := "", -1, 0
	for _, r := range routes {
		if r.Method != method {
			continue
		}
		wild, ok := matchPattern(r.Path, path)
		if !ok {
			continue
		}
		switch {
		case bestWild == -1 || wild < bestWild:
			best, bestWild, ties = r.Operation, wild, 1
		case wild == bestWild:
			ties++
		}
	}
	if ties > 1 {
		return "", fmt.Errorf("%s %s matches %d routes equally; the shape axis cannot guess", method, path, ties)
	}
	return best, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// matchPattern reports whether a concrete path satisfies a route pattern, and
// with how many wildcard segments. `{zone}`-style segments match any one
// segment; everything else matches itself.
func matchPattern(pattern, path string) (wildcards int, ok bool) {
	ps := strings.Split(strings.Trim(pattern, "/"), "/")
	cs := strings.Split(strings.Trim(path, "/"), "/")
	if len(ps) != len(cs) {
		return 0, false
	}
	for i, seg := range ps {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			wildcards++
			continue
		}
		if seg != cs[i] {
			return 0, false
		}
	}
	return wildcards, true
}
