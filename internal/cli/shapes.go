package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/stephrobert/feint/internal/shape"
	"github.com/stephrobert/feint/internal/trace"
	"github.com/stephrobert/feint/internal/upstream"
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
	record := fs.Bool("record", false, "read the real cloud directly instead of a recording file")
	check := fs.Bool("check", false, "compare this emulator with the recorded shapes; exit 2 on a field it omits")
	profile := fs.String("profile", "", "credential profile to use with --record (default: the provider's own default)")
	fs.Usage = func() {
		fmt.Fprint(stderr, "usage: feint shapes <recording.jsonl> --provider <name> [--dir shapes] [--dry-run]\n")
		fs.PrintDefaults()
	}
	// The file first, as `feint transcript` takes it, because both read the same
	// artefact and a verb that reversed the order for no reason would be one
	// more thing to remember. With --record there is no file: the cloud is the
	// source, so the flags stand alone.
	file := ""
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		file, rest = args[0], args[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return exitError
	}
	if *check {
		var wanted []string
		if *provider != "" {
			wanted = []string{*provider}
		}
		return checkShapes(*dir, wanted, stdout, stderr)
	}
	if file == "" && !*record {
		fmt.Fprintln(stderr, "feint: shapes needs a recording file first, or --record to read the cloud itself (see --help)")
		return exitError
	}
	if file != "" && *record {
		fmt.Fprintln(stderr, "feint: --record reads the cloud; it takes no recording file")
		return exitError
	}
	if *provider == "" {
		fmt.Fprintln(stderr, "feint: --provider is required: a catalogue is per provider, and guessing it from a path would be wrong the first time a file is renamed")
		return exitError
	}

	var exs []trace.Exchange
	var err error
	if *record {
		exs, err = recordUpstream(upstream.Provider(*provider), *profile, stdout)
	} else {
		exs, err = loadTranscript(file)
	}
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

// recordUpstream reads a real cloud through internal/upstream.
//
// It exists so that recording is one implementation rather than one per script.
// Seven copies of the Outscale signature accumulated in throwaway files in a
// single evening before this package existed, and the difference between two of
// them cost an hour: one signed the request path, one signed "/", and only the
// cloud could tell them apart.
//
// It also sidesteps what defeats `feint proxy` for two of the three providers
// (#92): nothing here is configured to talk to an intermediary, so no client
// can be handed a different address or refuse a signature over the wrong host.
// A proxy records what a client does; this records what a cloud answers, and
// for learning a shape the second is what is wanted.
//
// Every call is announced as it is made. A batch that goes quiet for a minute
// is indistinguishable from one that has hung.
func recordUpstream(p upstream.Provider, profile string, stdout io.Writer) ([]trace.Exchange, error) {
	calls, known := upstream.Reads[p]
	if !known {
		return nil, fmt.Errorf("no read list for provider %q", p)
	}
	client, err := upstream.New(p, profile)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(stdout, "reading %s at %s, %d call(s)\n", p, client.Endpoint, len(calls))
	return readEach(context.Background(), p, calls, client.Read, stdout)
}

// readEach walks a read list, filling in the templated entries from what the
// run itself has already read.
//
// Separate from recordUpstream because recordUpstream cannot be run without a
// real account: it loads credentials at its first line. The two properties that
// matter here — a templated entry refuses to run before its collection, and an
// account with nothing to read says so instead of asking for an identifier it
// does not have — are properties of this loop, and this is where a test can
// hold them.
func readEach(ctx context.Context, p upstream.Provider, calls []string, read func(context.Context, string) (trace.Exchange, error), stdout io.Writer) ([]trace.Exchange, error) {
	out := make([]trace.Exchange, 0, len(calls))
	// What each collection answered, so a templated entry can be filled in from
	// the run's own reading rather than from an identifier written down
	// somewhere. See the note beside upstream.TemplateOf.
	answers := map[string]any{}
	answered, unresolved := 0, 0
	for _, call := range calls {
		target := call
		if collection, templated := upstream.TemplateOf(call); templated {
			body, read := answers[collection]
			if !read {
				// A list ordered so an element read comes before its
				// collection is a mistake in this file, not a fact about the
				// account, and it is refused rather than skipped: skipped, it
				// would read as "this account has none".
				return nil, fmt.Errorf("%s reads one element of %s, which no earlier entry of Reads[%s] asks for", call, collection, p)
			}
			id, found := upstream.FirstID(body)
			if !found {
				fmt.Fprintf(stdout, "  %-46s no element to read: %s answered none\n", call, collection)
				unresolved++
				continue
			}
			target = collection + "/" + id
		}
		fmt.Fprintf(stdout, "  %-46s ", call)
		ex, err := read(ctx, target)
		if err != nil {
			fmt.Fprintf(stdout, "error: %v\n", err)
			continue
		}
		if ex.Status >= 200 && ex.Status < 300 {
			answered++
			fmt.Fprintln(stdout, "ok")
			if ex.Res != nil {
				answers[call] = ex.Res.Body
			}
		} else {
			fmt.Fprintf(stdout, "HTTP %d\n", ex.Status)
		}
		out = append(out, ex)
	}
	// Said out loud, because a catalogue built from a partial reading must not
	// read as a complete one: a gate built on it would then report a missing
	// field where nothing was ever observed.
	fmt.Fprintf(stdout, "%d of %d answered\n", answered, len(calls))
	if unresolved > 0 {
		// Counted separately from a refusal: nothing was asked at all, because
		// the account holds no such resource. Silence here would let a run made
		// on an empty account read like a run that observed an empty shape.
		fmt.Fprintf(stdout, "%d not asked: no resource of that kind on this account to read one of\n", unresolved)
	}
	return out, nil
}
