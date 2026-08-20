package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/stephrobert/feint/internal/trace"
	"github.com/stephrobert/feint/internal/transcript"
)

// transcriptCommand reads a proxy recording and answers the three questions a
// developer asks before serving one more operation.
//
// The proxy writes the measurement; this reads it. Without it the file is
// answered with jq and the knowledge of where an operation's name and shape sit
// in the record — which is exactly the kind of thing `feint status` and `feint
// coverage` exist to spare a reader. Three modes, one per question:
//
//	feint transcript real.jsonl                 what to serve next (the default)
//	feint transcript real.jsonl --shape ReadNics   what the response must look like
//	feint transcript real.jsonl --shape ReadVms --against emu.jsonl   what we get wrong
//
// Read-only and offline: it opens files and talks to nothing.
func transcriptCommand(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("transcript")
	shape := fs.String("shape", "", "print the response shape of this operation (e.g. ReadNics) instead of the work queue")
	against := fs.String("against", "", "a second transcript, recorded against the emulator: with --shape, diff the two")
	format := fs.String("format", "text", "output format: text or json")
	fs.Usage = func() {
		fmt.Fprint(stderr, "usage: feint transcript <recording.jsonl> [--shape OP [--against emu.jsonl]] [--format text|json]\n")
		fs.PrintDefaults()
	}
	// The file comes first, before the flags, which reads the way the usage line
	// does and the way a developer types it. Go's flag package stops at the first
	// positional, so the file is taken off the front and the rest is parsed —
	// rather than forcing flags ahead of the file, which every other verb here
	// avoids by having no positional at all.
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		// No file, or a lone flag such as --help: let Parse produce the usage.
		_ = fs.Parse(args)
		fmt.Fprintln(stderr, "feint: transcript needs a recording file first (see --help)")
		return exitError
	}
	file := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return exitError
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "feint: unexpected extra argument %q; the recording file goes first\n", fs.Arg(0))
		return exitError
	}

	exs, err := loadTranscript(file)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	switch {
	case *shape != "" && *against != "":
		return transcriptDiff(exs, *shape, *against, *format, stdout, stderr)
	case *shape != "":
		return transcriptShape(exs, *shape, *format, stdout, stderr)
	default:
		if *against != "" {
			fmt.Fprintln(stderr, "feint: --against needs --shape: the diff is per operation")
			return exitError
		}
		return transcriptQueue(exs, *format, stdout, stderr)
	}
}

func loadTranscript(path string) ([]trace.Exchange, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied path, read-only
	if err != nil {
		return nil, fmt.Errorf("open the recording %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return transcript.Load(f)
}

func transcriptQueue(exs []trace.Exchange, format string, stdout, stderr io.Writer) int {
	unserved := transcript.Unserved(exs)
	served := transcript.Served(exs)
	if format == "json" {
		if err := writeJSON(stdout, map[string]any{"unserved": unserved, "served": served}); err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
		return exitOK
	}
	if len(unserved) == 0 {
		fmt.Fprintln(stdout, "every operation the client called is served by a pack.")
	} else {
		fmt.Fprintf(stdout, "operations a real client called that no pack serves (%d), most-called first:\n\n", len(unserved))
		fmt.Fprintln(stdout, "  calls  resp.bytes  statuses      operation")
		for _, op := range unserved {
			fmt.Fprintf(stdout, "  %5d  %10d  %-12s  %s\n",
				op.Calls, op.MaxBytes, statusList(op.Statuses), op.Display())
		}
		fmt.Fprintln(stdout, "\nrun `feint transcript <file> --shape <operation>` to see the shape one returned.")
		fmt.Fprintln(stdout, "a 400 among the statuses means the call needs a parameter the sweep did not send;")
		fmt.Fprintln(stdout, "the operation exists, its populated shape is just not in this recording.")
	}
	if len(served) > 0 {
		fmt.Fprintf(stdout, "\nalready served, and exercised here (%d) — compare with --shape OP --against emu.jsonl:\n", len(served))
		for _, op := range served {
			fmt.Fprintf(stdout, "  %5d  %10d  %-12s  %s\n",
				op.Calls, op.MaxBytes, statusList(op.Statuses), op.Display())
		}
	}
	return exitOK
}

func transcriptShape(exs []trace.Exchange, selector, format string, stdout, stderr io.Writer) int {
	fields, ok := transcript.Shape(exs, selector)
	if !ok {
		fmt.Fprintf(stderr, "feint: no exchange in this recording addresses %q\n", selector)
		return exitError
	}
	if format == "json" {
		if err := writeJSON(stdout, fields); err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
		return exitOK
	}
	fmt.Fprintf(stdout, "response shape of %s, as the real cloud returned it:\n\n", selector)
	for _, f := range fields {
		fmt.Fprintf(stdout, "  %-10s  %s\n", f.Type, f.Path)
	}
	return exitOK
}

func transcriptDiff(exs []trace.Exchange, selector, againstPath, format string, stdout, stderr io.Writer) int {
	emu, err := loadTranscript(againstPath)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	diffs, ok := transcript.Diff(exs, emu, selector)
	if !ok {
		fmt.Fprintf(stderr, "feint: the first recording has no exchange addressing %q\n", selector)
		return exitError
	}
	if format == "json" {
		if err := writeJSON(stdout, diffs); err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
		return exitOK
	}
	if len(diffs) == 0 {
		fmt.Fprintf(stdout, "%s: every field the real cloud returned, the emulator returns too, with the same type.\n", selector)
		return exitOK
	}
	fmt.Fprintf(stdout, "%s: fields the real cloud returns that the emulator gets wrong (%d):\n\n", selector, len(diffs))
	fmt.Fprintln(stdout, "  real        emulator    field")
	for _, d := range diffs {
		emuCol := d.Emu
		if emuCol == "" {
			emuCol = "(absent)"
		}
		fmt.Fprintf(stdout, "  %-10s  %-10s  %s\n", d.Real, emuCol, d.Path)
	}
	return exitOK
}

func statusList(statuses []int) string {
	parts := make([]string, len(statuses))
	for i, s := range statuses {
		parts[i] = fmt.Sprintf("%d", s)
	}
	return strings.Join(parts, ",")
}
