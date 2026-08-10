package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/stephrobert/feint/internal/shape"
)

// shapesCommand folds a proxy recording into the versioned shape catalogue.
//
//	feint shapes <recording.jsonl> --provider outscale
//	feint shapes <recording.jsonl> --provider outscale --dir shapes --dry-run
//
// The recording says what a real cloud answered; the catalogue keeps the field
// tree of it — paths and JSON types, no values, no identifiers. That difference
// is the whole point: a transcript describes somebody's account and cannot be
// committed, a catalogue describes an API and must be.
//
// What it prints is what the merge learned, so a refresh reports itself instead
// of leaving a reader to diff two JSON files by eye. `git diff` on the
// catalogue remains the authoritative record — this is the summary above it.
//
// Read-only towards the network: it opens a file and writes a file.
func shapesCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("shapes", flag.ContinueOnError)
	provider := fs.String("provider", "", "which provider this recording is of: scaleway, outscale, exoscale")
	dir := fs.String("dir", "shapes", "directory holding the versioned catalogues")
	dryRun := fs.Bool("dry-run", false, "report what would be learned without writing")
	fs.Usage = func() {
		fmt.Fprint(stderr, "usage: feint shapes <recording.jsonl> --provider <name> [--dir shapes] [--dry-run]\n")
		fs.PrintDefaults()
	}
	// The file first, as `feint transcript` takes it, because both read the same
	// artefact and a verb that reversed the order for no reason would be one
	// more thing to remember.
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		_ = fs.Parse(args)
		fmt.Fprintln(stderr, "feint: shapes needs a recording file first (see --help)")
		return exitError
	}
	file := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return exitError
	}
	if *provider == "" {
		fmt.Fprintln(stderr, "feint: --provider is required: a catalogue is per provider, and guessing it from a path would be wrong the first time a file is renamed")
		return exitError
	}

	exs, err := loadTranscript(file)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	path := filepath.Join(*dir, *provider+".json")
	cat, err := openCatalogue(path, *provider)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	changes := cat.Merge(exs)

	if changes.Empty() {
		fmt.Fprintf(stdout, "%s: nothing new; %d operation(s) already described\n", *provider, len(cat.Operations))
	} else {
		if len(changes.Added) > 0 {
			fmt.Fprintf(stdout, "%s: %d operation(s) observed for the first time\n", *provider, len(changes.Added))
			for _, op := range changes.Added {
				fmt.Fprintf(stdout, "  + %s\n", op)
			}
		}
		if len(changes.Fields) > 0 {
			fmt.Fprintf(stdout, "%s: %d field(s) learned or changed\n", *provider, len(changes.Fields))
			for _, f := range changes.Fields {
				if f.From == "" {
					fmt.Fprintf(stdout, "  + %s  %s: %s\n", f.Operation, f.Path, f.To)
					continue
				}
				// A type that moved is the interesting line: either the provider
				// changed, or two recordings disagree, and both want a human.
				fmt.Fprintf(stdout, "  ~ %s  %s: %s -> %s\n", f.Operation, f.Path, f.From, f.To)
			}
		}
	}

	if *dryRun {
		fmt.Fprintln(stdout, "(--dry-run: nothing written)")
		return exitOK
	}

	if err := writeCatalogue(path, cat); err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	fmt.Fprintf(stdout, "wrote %s\n", path)
	return exitOK
}

// openCatalogue reads the existing catalogue, or starts one.
//
// A missing file is a first run and is not an error. An unreadable one is:
// starting from empty there would let a corrupt or truncated file silently
// erase every shape earlier recordings learned, and the next commit would look
// like the provider had removed them all.
func openCatalogue(path, provider string) (*shape.Catalogue, error) {
	f, err := os.Open(path) //nolint:gosec // a path built from a flag the operator typed
	if os.IsNotExist(err) {
		return shape.New(provider), nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	cat, err := shape.Load(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if cat.Provider != provider {
		return nil, fmt.Errorf("%s describes %q, not %q", path, cat.Provider, provider)
	}
	return cat, nil
}

func writeCatalogue(path string, cat *shape.Catalogue) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	f, err := os.Create(path) //nolint:gosec // a path built from a flag the operator typed
	if err != nil {
		return err
	}
	if err := cat.Save(f); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
