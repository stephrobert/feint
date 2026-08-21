package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/replay"
	"github.com/stephrobert/feint/internal/trace"
)

// replayCommand reissues a recording at a running emulator and reports what
// diverged.
//
//	feint replay recording.jsonl [--endpoint http://127.0.0.1:4599]
//
// Exit 2 on a divergence, not 1, and the choice is rule 9's rather than a
// preference: 1 means this tool failed — the file is unreadable, the emulator
// does not answer — and 2 means the measurement and the code disagree, which
// is what `feint shapes --check` and the drift baseline already spend that code
// on. An unserved operation is neither: it is #74's work queue, printed and
// never counted against the verdict, because the day it fails the build is the
// day somebody stops recording.
func replayCommand(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("replay")
	endpoint := fs.String("endpoint", "http://"+DefaultAddr, "the running emulator to replay against")
	format := fs.String("format", "text", "output format: text or json")
	timeout := fs.Duration("timeout", 30*time.Second, "how long one replayed request may take")
	refusalsOnly := fs.Bool("refusals-only", false,
		"refuse to send anything unless every exchange of the recording is a 4xx; "+
			"for replaying beside other suites, where a mutating recording would disturb them")
	fs.Usage = func() {
		fmt.Fprint(stderr, "usage: feint replay <recording.jsonl> [--endpoint http://127.0.0.1:4599] [--refusals-only] [--format text|json] [--timeout 30s]\n")
		fs.PrintDefaults()
	}
	// The file comes first, the way `transcript` and `shapes` take theirs: Go's
	// flag package stops at the first positional, so it is peeled off the front
	// and the rest parsed.
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		_ = fs.Parse(args)
		fmt.Fprintln(stderr, "feint: replay needs a recording file first (see --help)")
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
	// Read before anything is sent, which is the whole of the guarantee: a
	// caller that hands this flag the wrong file learns so from a refusal, not
	// from the servers it created in somebody else's emulator.
	if *refusalsOnly {
		if why := notRefusalsOnly(exs); why != "" {
			fmt.Fprintf(stderr, "feint: %s: %s\n", file, why)
			return exitError
		}
	}

	env := emulator.DefaultEnv()
	packs, err := packsFor(env)
	if err != nil {
		fmt.Fprintf(stderr, "feint: build the emulator: %v\n", err)
		return exitError
	}
	// The same table `feint proxy` names an exchange with, rather than a second
	// route list written here: a copy would answer "no pack claims this" for a
	// route a pack had merely moved, and "unserved" is a finding this repository
	// acts on.
	table, err := emulator.NewTable(packs...)
	if err != nil {
		fmt.Fprintf(stderr, "feint: build the route table: %v\n", err)
		return exitError
	}

	declined, invariants, code := replayDeclarations(packs, stderr)
	if code != exitOK {
		return code
	}

	rep, err := replay.Run(context.Background(), exs, replay.Options{
		Endpoint:   strings.TrimSuffix(*endpoint, "/"),
		Client:     &http.Client{Timeout: *timeout},
		Table:      table,
		Declined:   declined,
		Invariants: invariants,
	})
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	switch *format {
	case "json":
		if err := writeJSON(stdout, rep); err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
	case "text":
		writeReplayText(stdout, rep)
	default:
		fmt.Fprintf(stderr, "feint: unknown format %q\n", *format)
		return exitError
	}

	if rep.Divergent > 0 {
		return exitDrift
	}
	return exitOK
}

// notRefusalsOnly names why a recording is not a corpus of refusals, or answers
// "" when it is one.
//
// A file whose every exchange is a 4xx mutates nothing, and that is what lets
// tools/conformance/refusals.sh replay it beside the other suites of a run
// instead of against an emulator of its own (#390). The property belongs to the
// file, so it is read off the file here — before the first request goes out —
// rather than promised by the caller that picked the file.
//
// An empty recording is not "refusals only": it is empty, and answering yes to
// it would let a replay of nothing read as a replay that proved something. That
// is the SKIP `feint corpus --check` already has a paragraph about.
//
// TestRefusalsOnlyRefusesARecordingThatWouldMutate fails without this.
func notRefusalsOnly(exs []trace.Exchange) string {
	if len(exs) == 0 {
		return "the recording holds no exchange, so replaying it would prove nothing"
	}
	for i := range exs {
		if exs[i].Status < 400 || exs[i].Status >= 500 {
			return fmt.Sprintf("exchange %d answered %d, so this recording is not refusals only "+
				"and replaying it here could change what the other suites see",
				i+1, exs[i].Status)
		}
	}
	return ""
}

// replayDeclarations gathers what the packs declare comparable, holding each
// declaration to the guard its neighbours face.
//
// A pack that declares an invariant with no usable reason, or the same field
// twice, or a kind nothing implements, fails here rather than being quietly
// dropped — the difference between a declaration and a comment.
// TestAnInvariantWithoutAUsableReasonIsRefused (internal/core/emulator) holds
// the guard; TestThePacksDeclarationsPassTheirOwnGuard holds this wiring, and
// the second is the half that is usually missing.
func replayDeclarations(packs []emulator.Pack, stderr io.Writer) ([]emulator.FieldDecline, []emulator.Invariant, int) {
	var declined []emulator.FieldDecline
	var invariants []emulator.Invariant
	unusable := 0
	for _, p := range packs {
		declined = append(declined, emulator.FieldDeclinesOf(p)...)
		inv := emulator.InvariantsOf(p)
		for _, bad := range emulator.UnexplainedInvariants(inv) {
			fmt.Fprintf(stderr, "feint: %s declares the replay invariant %s with no usable reason or no known kind\n", p.Name(), bad)
			unusable++
		}
		for _, dup := range emulator.DuplicateInvariants(inv) {
			fmt.Fprintf(stderr, "feint: %s declares the replay invariant %s more than once\n", p.Name(), dup)
			unusable++
		}
		invariants = append(invariants, inv...)
	}
	if unusable > 0 {
		return nil, nil, exitError
	}
	return declined, invariants, exitOK
}

// writeReplayText renders the run operation by operation, then the three counts
// side by side and never summed.
func writeReplayText(w io.Writer, rep replay.Report) {
	for _, r := range rep.Results {
		name := r.Operation
		if name == "" {
			name = r.Method + " " + r.Path
		}
		switch r.Verdict {
		case replay.Matched:
			fmt.Fprintf(w, "%-48s PASS\n", name)
		case replay.Unserved:
			fmt.Fprintf(w, "%-48s NOT SERVED\n", name)
			continue
		case replay.Skipped:
			fmt.Fprintf(w, "%-48s NO ANSWER RECORDED\n", name)
			continue
		default:
			fmt.Fprintf(w, "%-48s DIFF\n", name)
		}
		for _, f := range r.Findings {
			fmt.Fprintln(w, "  "+describeFinding(f))
		}
	}

	fmt.Fprintf(w, "\n%d matched, %d divergent, %d not served, %d without a recorded answer\n",
		rep.Matched, rep.Divergent, rep.Unserved, rep.Skipped)
	// Printed whatever the verdict, for the reason `feint shapes --check`
	// prints its own coverage: a run that says "no divergence" without saying
	// how much it compared reads as "nothing is wrong" when it may mean
	// "nothing was checked".
	fmt.Fprintf(w, "%d field(s) knowingly not served, each printed above with its reason\n", rep.Excused)
	fmt.Fprintf(w, "%d field(s) the recorder redacted, whose type could not be compared\n", rep.Redacted)
	fmt.Fprintf(w, "%d recorded identifier(s) rebound to the one this emulator minted\n", rep.Rebound)
	if rep.Ambiguous > 0 {
		// Printed only when there are any, unlike the counts above: those three
		// answer "how much was compared" and have to be legible at zero, while
		// this one answers "did anything need arbitrating" and a zero would add
		// a line to every run to say nothing happened.
		fmt.Fprintf(w, "%d recorded value(s) two fields bound differently, resolved by field name\n", rep.Ambiguous)
	}
	if rep.Unserved > 0 {
		fmt.Fprintln(w, "not served is a work item, not a failure: rank it with `feint coverage --observed <recording>`")
	}
}

// describeFinding says what differs without printing a value from either side.
// A recording is the inventory of a real account (docs/proxy.md), and a tool
// that reads one has no business republishing it.
func describeFinding(f replay.Finding) string {
	switch f.Kind {
	case replay.KindStatus:
		return fmt.Sprintf("status:   expected %s, got %s", f.Want, f.Got)
	case replay.KindAbsent:
		return fmt.Sprintf("absent:   %s (%s upstream)", f.Path, f.Want)
	case replay.KindType:
		return fmt.Sprintf("type:     %s is %s upstream, %s here", f.Path, f.Want, f.Got)
	case replay.KindValue:
		return fmt.Sprintf("value:    %s does not carry back what the request named (%s, %s)", f.Path, f.Want, f.Got)
	case replay.KindOrder:
		return fmt.Sprintf("order:    %s is ordered %s upstream, %s here", f.Path, f.Want, f.Got)
	case replay.KindExcused:
		return fmt.Sprintf("declined: %s: %s", f.Path, f.Reason)
	case replay.KindRedacted:
		return fmt.Sprintf("redacted: %s carries no recorded type to compare (%s here)", f.Path, f.Want)
	default:
		return string(f.Kind) + ": " + f.Path
	}
}
