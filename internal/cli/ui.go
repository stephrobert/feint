package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// The page, from the side that owns the listen address and the artefacts.
//
// Two things live here rather than in the core, and for the same reason both
// times: the core must not learn a provider's name (rule 5). Reading
// coverage/*-coverage.json means knowing a shape whose every row names a
// provider, so the loader stays in this package — where `feint docs` already
// keeps it — and the numbers cross into the core as data. And `feint ui` is a
// command, which the core has no business having.

// upstreamGap reads the versioned coverage artefacts for the page.
//
// It is handed to the server as a function, not as a value, so a
// `mise run drift:update` in another terminal shows up on the page's next slow
// refresh rather than at the next restart.
//
// A missing directory is not an error: a binary installed with `go install` has
// no coverage/ beside it, and the page says the gap is unknown — which is the
// honest answer, and a different one from zero.
func upstreamGap(dir string) func() emulator.UpstreamView {
	return func() emulator.UpstreamView {
		// Refresh is left to the core, which owns the sentence the page prints.
		// Written here too, it would be the same fact in two files, and the one
		// that went stale would be the one nobody was reading.
		view := emulator.UpstreamView{
			Source:     dir,
			Products:   []emulator.UpstreamProduct{},
			Operations: []emulator.UpstreamOperation{},
		}
		reports, err := loadCoverage(dir)
		if err != nil || len(reports) == 0 {
			return view
		}
		for _, rep := range reports {
			for _, p := range rep.Products {
				view.Products = append(view.Products, emulator.UpstreamProduct{
					Provider:  rep.Provider,
					Product:   p.Product,
					Served:    p.Implemented,
					Declined:  p.Declined,
					Untriaged: p.Unknown,
					Total:     p.Total,
				})
			}
			// Copied across, never recomputed: the verdict and its reason are
			// what drift.Compare decided and what the artefact recorded. The
			// provider is added because the artefact keeps it once at the top
			// and the page needs it on every row to group without knowing a
			// single provider name.
			for _, e := range rep.Entries {
				view.Operations = append(view.Operations, emulator.UpstreamOperation{
					Operation: e.Operation,
					Provider:  rep.Provider,
					Product:   e.Product,
					Version:   e.Version,
					Status:    string(e.Status),
					Reason:    e.Reason,
				})
			}
		}
		view.Available = len(view.Products) > 0
		view.WrittenAt = newestArtefact(dir)
		return view
	}
}

// newestArtefact returns when the coverage artefacts were last written, RFC 3339,
// or "" when that cannot be read.
//
// This is the file's timestamp, and the page says so rather than calling it a
// scan date. Recording the scan date inside the artefact would be the better
// answer and it is the one thing this must not do: .github/workflows/drift.yml
// decides whether the upstream surface moved with `git diff --quiet --
// coverage/`, so a field that changes on every run would open a drift pull
// request every Monday whether or not anything upstream had moved — and the
// mechanism this whole project rests on would become the noise everybody
// filters out.
func newestArtefact(dir string) string {
	paths, err := filepath.Glob(filepath.Join(dir, "*-coverage.json"))
	if err != nil {
		return ""
	}
	var newest time.Time
	for _, path := range paths {
		info, statErr := os.Stat(path)
		if statErr != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	if newest.IsZero() {
		return ""
	}
	return newest.UTC().Format(time.RFC3339)
}

// ui opens the page in a browser.
//
// It refuses before opening rather than after: a browser tab on a dead address
// shows the browser's own error page, and an operator reads that as "the page is
// broken" instead of "nothing is running there". So the emulator is asked for
// its health first, and the address is checked against the same predicate that
// decided whether the page was mounted at all — one function, so the command and
// the server can never disagree about what loopback means.
func uiCommand(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("ui")
	addr := fs.String("addr", DefaultAddr, "the address the emulator is serving on")
	print := fs.Bool("print", false, "print the URL instead of opening a browser")
	if err := fs.Parse(args); err != nil {
		return exitError
	}

	if !emulator.LoopbackListen(*addr) {
		fmt.Fprintf(stderr, "feint: no page is served on %s: it is not a loopback address, "+
			"so the page is not mounted there at all\n", *addr)
		return exitError
	}
	if _, err := fetchHealth(*addr); err != nil {
		fmt.Fprintf(stderr, "feint: nothing answers on %s; start it with `feint start`\n", *addr)
		return exitError
	}

	url := "http://" + *addr + "/_feint/ui"
	if *print {
		fmt.Fprintln(stdout, url)
		return exitOK
	}
	if err := openBrowser(url); err != nil {
		// Not a failure worth an exit code: the operator has the URL, which is
		// all the command was for. A headless host has no browser and that is
		// normal, not broken.
		fmt.Fprintf(stderr, "feint: could not open a browser (%v)\n", err)
	}
	fmt.Fprintln(stdout, url)
	return exitOK
}

// openBrowser hands a URL to the desktop.
//
// The URL is built from a validated loopback address and a constant path, and it
// is passed as one argument to a fixed binary: nothing here reaches a shell, so
// there is no quoting to get wrong. The lifecycle verbs are Unix only for the
// same reason as the rest of this package — the released binaries are linux and
// darwin.
func openBrowser(url string) error {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	path, err := exec.LookPath(opener)
	if err != nil {
		return fmt.Errorf("%s is not installed", opener)
	}
	return exec.Command(path, url).Start() //nolint:gosec // a fixed opener, one argument, no shell
}
