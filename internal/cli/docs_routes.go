package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// The reference a reader needs before installing anything: is my resource
// emulated?
//
// Until this existed, answering it meant running the binary and reading
// /_feint/routes, or reading the packs. Both are fine for somebody who already
// decided to try feint, and useless to somebody deciding whether to.
//
// It is generated for the reason every number in the README is: the counts in
// this repository have been wrong before, by a factor of four, and nobody lied.
// The routes come from the packs mounted in this process, and the declined
// operations from Declined(), so the page cannot disagree with what the binary
// serves — it is written by the binary that serves them.

const (
	routesStartMarker = "<!-- routes:start -->"
	routesEndMarker   = "<!-- routes:end -->"
)

// renderRoutes builds the reference body: every mounted route by provider and
// product, then what each pack declines on purpose.
func renderRoutes() (string, error) {
	srv, _, err := newServer(nil)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(docsGenerated)
	b.WriteString("\n\n")

	total := 0
	for _, p := range srv.Packs() {
		total += len(p.Routes())
	}
	fmt.Fprintf(&b, "%d routes across %d packs. Every route names the upstream operation it\n", total, len(srv.Packs()))
	b.WriteString("stands for, in the provider's own spelling: that name is what the drift scan\n")
	b.WriteString("matches against the upstream SDK, so a route that renames it stops being\n")
	b.WriteString("counted.\n")

	for _, p := range srv.Packs() {
		fmt.Fprintf(&b, "\n## %s\n\n", strings.ToUpper(p.Name()[:1])+p.Name()[1:])

		byProduct := make(map[string][]string)
		for _, r := range p.Routes() {
			row := fmt.Sprintf("| `%s` | `%s` | `%s` |", r.Method, r.Path, r.Operation)
			byProduct[productOf(r.Operation)] = append(byProduct[productOf(r.Operation)], row)
		}
		products := make([]string, 0, len(byProduct))
		for product := range byProduct {
			products = append(products, product)
		}
		sort.Strings(products)

		for _, product := range products {
			rows := byProduct[product]
			sort.Strings(rows)
			fmt.Fprintf(&b, "### `%s`\n\n", product)
			b.WriteString("| Method | Path | Upstream operation |\n|---|---|---|\n")
			for _, row := range rows {
				b.WriteString(row)
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}

		declined := append([]emulator.Decline(nil), p.Declined()...)
		sort.Slice(declined, func(i, j int) bool { return declined[i].Operation < declined[j].Operation })
		fmt.Fprintf(&b, "### Declined on purpose (%d)\n\n", len(declined))
		if len(declined) == 0 {
			b.WriteString("Nothing yet: everything upstream declares is either served or untriaged.\n")
			continue
		}
		b.WriteString("Operations this pack knowingly does not serve, and why. Declining is a\n")
		b.WriteString("decision the drift gate records, which is what separates it from having\n")
		b.WriteString("missed one — and the reason is what separates a decision from a list.\n\n")
		for _, d := range declined {
			fmt.Fprintf(&b, "- `%s` — %s\n", d.Operation, d.Reason)
		}
	}

	return b.String(), nil
}

// spliceRoutes renders the reference into the file's markers and reports whether
// it changed. Absent file, or a file without the markers: nothing to do, no
// complaint — the same terms as the contract table, so `feint docs` still works
// for somebody who installed the binary and has no docs/ directory.
func spliceRoutes(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	current, err := os.ReadFile(path) //nolint:gosec // operator-supplied path, by design
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !strings.Contains(string(current), routesStartMarker) {
		return false, nil
	}
	rendered, err := renderRoutes()
	if err != nil {
		return false, err
	}
	updated, err := spliceSection(string(current), routesStartMarker, routesEndMarker, rendered)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return updated != string(current), nil
}

// writeSplicedRoutes writes what spliceRoutes reported as changed.
func writeSplicedRoutes(path string) error {
	current, err := os.ReadFile(path) //nolint:gosec // operator-supplied path, by design
	if err != nil {
		return err
	}
	rendered, err := renderRoutes()
	if err != nil {
		return err
	}
	updated, err := spliceSection(string(current), routesStartMarker, routesEndMarker, rendered)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return os.WriteFile(path, []byte(updated), 0o644) //nolint:gosec // a reference page is world-readable by design
}
