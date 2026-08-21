package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/replay"
)

// The default locations, so the gate is one word to type and the paths are not
// spread across a mise task, a workflow and a hook.
const (
	defaultCorpusDir      = "corpus"
	defaultCorpusAccepted = "corpus/accepted.json"
)

// corpusCommand replays every committed corpus against a fresh emulator and
// says whether this emulator still answers what a real cloud answered.
//
//	feint corpus --check [--dir corpus] [--accepted corpus/accepted.json]
//
// # Why this is a gate where `conformance` cannot be one
//
// The conformance suite proves a real client *accepts* what this emulator
// answers. It cannot prove the answer is the one the cloud would have given,
// because the cloud is not there. corpus/ is the only artefact in this
// repository that says what a real cloud actually returned (#351, #352), and
// replaying it is the only control that compares the two.
//
// Every input is a committed file: the corpus, this binary, the acceptance
// list. No credential, no network, no `scw`, no Terraform. That is exactly what
// keeps it out of the trap CLAUDE.md records twice — `conformance` is
// deliberately outside the pre-commit hook because it needs four binaries
// installed and would fail on an absent one rather than on the code, teaching
// `--no-verify`, which disarms every other hook at once. This one needs none of
// them, so it belongs where compat:check and release:surface live: on every
// pull request, in seconds.
//
// It is also not the class of control that cost #88 two red pull requests. That
// rule — a verdict about "this run" must be tried on the poorest run that will
// trigger it — applies to a gate whose population is whatever a CI leg happened
// to drive. This one's population is a directory of versioned files, identical
// on every runner and in every leg. Two tests hold that claim rather than a
// sentence: TestTheGateReadsOnlyTheFilesItIsPointedAt, which requires the same
// verdict from a copy of the directory elsewhere, and
// TestTheGatesVerdictDoesNotDependOnTheEnvironment, which covers the one seam
// through which it could stop being true — packsFor reads FEINT_OUTSCALE_REGION
// and FEINT_EXOSCALE_ZONE, and those decide which routes are mounted and
// therefore which exchanges count as unserved.
//
// # The three verdicts it must never blur
//
//	a divergence          the emulator answers differently from the cloud   exit 2
//	an unserved operation a route nobody mounted — #74's queue, never counted
//	a corpus not read     empty, unreadable, or replayed against nothing    exit 1
//
// The third is the one that gets forgotten, and this repository has shipped its
// shape before: the network conformance suite returned SKIP in CI from the day
// it was written, and five more controls in tools/ui/check-page.py were found
// measuring nothing in August 2026. **A corpus that replays nothing is red.**
//
// # The second direction, and why it is the same command
//
//	feint corpus --against-cloud --file scaleway/terraform.jsonl \
//	  --endpoint https://api.scaleway.com \
//	  --credential X-Auth-Token=SCW_SECRET_KEY \
//	  --bind project_id=<project> --bind organization_id=<organisation>
//
// reissues one committed corpus at the provider it was recorded from, and a
// divergence there means *the cloud has changed* (#359). One verb rather than
// two, because it is one artefact, one comparator and one vocabulary — only the
// endpoint and the conclusion differ. What the second direction adds is
// everything a real account demands and an emulator does not, and all of it
// lives in corpus_cloud.go: a guard that refuses to touch what this run did not
// create, a closed list of what may be created because it is free, a ledger
// destroyed at exit with the destruction proved by a read, and three outcomes
// that are never blurred into one another.
//
// It is deliberately **not** a gate: credentials, real objects, real money, a
// verdict that depends on whose account ran it. It runs on a schedule and on
// demand, like the Monday drift pull request.
func corpusCommand(args []string, stdout, stderr io.Writer) int {
	flags := newFlagSet("corpus")
	check := flags.Bool("check", false, "replay every committed corpus and fail on a divergence the acceptance list does not carry")
	dir := flags.String("dir", defaultCorpusDir, "directory of committed corpora, one subdirectory per provider")
	accepted := flags.String("accepted", defaultCorpusAccepted, "the divergences this repository records rather than fixes, each with its reason")
	againstCloud := flags.Bool("against-cloud", false, "reissue one committed corpus at the cloud it was recorded from, and report what that cloud answers differently now")
	file := flags.String("file", "", "the one corpus to reissue, relative to --dir (required with --against-cloud)")
	endpoint := flags.String("endpoint", "", "the cloud to reissue at, e.g. https://api.scaleway.com (required with --against-cloud)")
	format := flags.String("format", "text", "output format for --against-cloud: text or json")
	timeout := flags.Duration("timeout", 60*time.Second, "how long one reissued request may take")
	dryRun := flags.Bool("dry-run", false, "with --against-cloud, send the reads and refuse everything that would change the account")
	markStale := flags.Bool("mark-stale", false, "with --against-cloud, write back into the acceptance file that the cloud has moved under this corpus")
	var credentials, bind repeatable
	flags.Var(&credentials, "credential", "put a redacted header back, as Header=ENV_VAR; the value never travels in argv (repeatable)")
	flags.Var(&bind, "bind", "send this value wherever the recording carries an identifier at that field, as field=value (repeatable)")
	flags.Usage = func() {
		fmt.Fprint(stderr, "usage: feint corpus --check [--dir corpus] [--accepted corpus/accepted.json]\n")
		fmt.Fprint(stderr, "       feint corpus --against-cloud --file <corpus> --endpoint <url> [--credential H=ENV] [--bind field=value]\n")
		fmt.Fprint(stderr, "                    [--dir corpus] [--accepted corpus/accepted.json] [--format text|json] [--timeout 60s] [--dry-run] [--mark-stale]\n")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return exitError
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "feint: unexpected argument %q; corpus takes flags only\n", flags.Arg(0))
		return exitError
	}
	// Refused rather than silently ordered, because the two directions reach
	// two different endpoints: one of them creates objects in somebody's
	// account, and "which one ran" is not a question a reader of the output
	// should have to answer from the ordering of a switch.
	if *check && *againstCloud {
		fmt.Fprintln(stderr, "feint: --check and --against-cloud are the two directions of one comparison; run them one at a time")
		return exitError
	}
	switch {
	case *check:
		return checkCorpus(*dir, *accepted, time.Now(), stdout, stderr)
	case *againstCloud:
		creds, err := parseKeyValues("credential", credentials)
		if err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
		seeds, err := parseKeyValues("bind", bind)
		if err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
		return replayCorpusAtCloud(cloudReplayRequest{
			dir: *dir, accepted: *accepted, file: *file, endpoint: *endpoint,
			credentials: creds, bind: seeds, format: *format, timeout: *timeout,
			dryRun: *dryRun, markStale: *markStale,
		}, time.Now(), stdout, stderr)
	}
	fmt.Fprintln(stderr, "feint: corpus has two modes: --check against a fresh emulator, --against-cloud against the provider (see --help)")
	return exitError
}

// corpusAcceptance is the committed judgement on a corpus: when each file was
// recorded, and which divergences this repository records rather than fixes.
//
// One file rather than two, because the two questions are asked of the same
// subject and separating them is how one of them stops being maintained.
type corpusAcceptance struct {
	// Comment is prose for whoever opens the file. Read and ignored here.
	Comment []string `json:"comment"`
	// WarnAfterDays is how old a recording may be before every run says so. A
	// warning, never a failure: see [agedCorpora].
	WarnAfterDays int `json:"warn_after_days"`
	// Recorded is one entry per corpus file, and it is mandatory: a file with no
	// entry fails the gate rather than being replayed anonymously, because the
	// age of a recording is the one thing the file itself cannot carry — the
	// sanitiser normalises every timestamp in it.
	Recorded []corpusRecording `json:"recorded"`
	// Accepted is the exemption list, on the model of tools/compat/accepted.json:
	// recorded and still printed, never hidden, and an entry that excuses
	// nothing fails the gate.
	Accepted []corpusException `json:"accepted"`
}

// corpusRecording is what a corpus file cannot say about itself.
type corpusRecording struct {
	File string `json:"file"`
	// At is the day the recording was made against the real cloud, YYYY-MM-DD.
	At string `json:"at"`
	// Client and Cloud are for the reader: which client drove it, and which
	// cloud answered. A re-recording that changes either is a different
	// measurement, and the entry is where that is legible.
	Client string `json:"client"`
	Cloud  string `json:"cloud"`
	// CloudMovedAt and CloudMoved are what `corpus --against-cloud --mark-stale`
	// writes back, and they are how the two directions of one comparison talk to
	// each other (#359).
	//
	// #353 asked how a corpus ages and could only answer with a chosen horizon —
	// 180 days, "about two releases" — because this gate holds one side of the
	// comparison and cannot tell a cloud that moved from an emulator that broke.
	// The other direction can, so when it finds the provider answering
	// differently it writes the day and the count here, and the warning below
	// stops being a guess about age and becomes a measurement.
	//
	// Written only when something moved. A clean run writes nothing, which costs
	// the ability to say "last verified on", and buys a scheduled job that opens
	// a pull request when there is something to decide and stays silent when
	// there is not.
	CloudMovedAt string `json:"cloud_moved_at,omitempty"`
	CloudMoved   int    `json:"cloud_moved,omitempty"`
}

// corpusException is one divergence this repository knows about and has decided
// not to fail on yet.
//
// Keyed the way a finding is named — file, operation, kind, field path — rather
// than by operation alone, so an exemption for a missing tag cannot also excuse
// a status that starts differing on the same operation tomorrow. Path takes the
// same "*" segment as emulator.FieldDecline, and matches through the same
// implementation, because the gate that excuses a field and the replay that
// compares one must not disagree about which field they are naming.
type corpusException struct {
	File      string `json:"file"`
	Operation string `json:"operation"`
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	Reason    string `json:"reason"`
	// Issue names what retires this entry. Required: an exemption with a reason
	// and no way out is a decision to live with a defect forever, written as if
	// it were temporary.
	Issue string `json:"issue"`
}

// key is what this entry is addressed to, for a message that names it.
func (e corpusException) key() string {
	return e.File + " " + e.Operation + " " + e.Kind + " " + e.Path
}

// matches reports whether this entry excuses that finding of that file.
func (e corpusException) matches(file, operation string, f replay.Finding) bool {
	if e.File != file || e.Kind != string(f.Kind) {
		return false
	}
	d := emulator.FieldDecline{Operation: e.Operation, Path: e.Path}
	return d.Matches(operation, f.Path)
}

// checkCorpus is the gate. now is a parameter so the ageing warning is testable
// without waiting for a calendar.
func checkCorpus(dir, acceptedPath string, now time.Time, stdout, stderr io.Writer) int {
	acc, err := readCorpusAcceptance(acceptedPath)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	if bad := unusableCorpusExceptions(acc.Accepted); len(bad) > 0 {
		// The same guard a pack's own declines face (emulator.carriesNoDecision,
		// through UnexplainedFieldDeclines): an exemption whose reason carries no
		// decision is a comment, and CLAUDE.md's most expensive defect is a
		// comment standing in for a control.
		// TestAnExemptionWithoutAUsableReasonFailsTheGate fails without this.
		for _, b := range bad {
			fmt.Fprintf(stderr, "feint: the accepted divergence %s %s\n", b.key, b.why)
		}
		return exitError
	}

	files, err := corpusFiles(dir)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	// Asserted, not assumed. A gate that reads an empty directory and reports
	// success is the SKIP this repository has already shipped once.
	// TestACorpusThatComparesNothingIsRed fails without this.
	if len(files) == 0 {
		fmt.Fprintf(stderr, "feint: %s holds no .jsonl corpus, so this compared nothing against a real cloud\n", dir)
		return exitError
	}
	if code := checkCorpusRecordings(files, acc.Recorded, dir, stderr); code != exitOK {
		return code
	}

	env := emulator.DefaultEnv()
	packs, err := packsFor(env)
	if err != nil {
		fmt.Fprintf(stderr, "feint: build the emulator: %v\n", err)
		return exitError
	}
	declined, invariants, code := replayDeclarations(packs, stderr)
	if code != exitOK {
		return code
	}

	used := make([]int, len(acc.Accepted))
	compared, divergent, unserved, excused := 0, 0, 0, 0
	values, orders := 0, 0
	for _, f := range files {
		rep, err := replayCorpusFile(dir, f, env, packs, declined, invariants)
		if err != nil {
			// Unreadable, or the emulator refused to answer. An error, never a
			// pass: "I could not measure" and "nothing is wrong" are different
			// answers, and only one of them may be green.
			fmt.Fprintf(stderr, "feint: %s: %v\n", f, err)
			return exitError
		}
		// A file that produced no comparison at all — every exchange unserved,
		// or the transcript empty. Named per file rather than in a total, so a
		// second corpus cannot cover for the first having stopped replaying.
		// TestACorpusThatComparesNothingIsRed fails without this.
		if rep.Matched+rep.Divergent == 0 {
			fmt.Fprintf(stderr, "feint: %s replayed against nothing: %d exchange(s), none of them compared\n",
				f, len(rep.Results))
			return exitError
		}
		compared += rep.Matched + rep.Divergent
		unserved += rep.Unserved
		values += rep.Values
		orders += rep.Orders
		divergent += reportCorpusFile(f, rep, acc.Accepted, used, &excused, stdout, stderr)
	}

	// Coverage, printed whatever the verdict, for the reason `feint shapes
	// --check` prints its own: a run that says "no divergence" without saying
	// how much it compared reads as "nothing is wrong" when it may mean
	// "nothing was checked".
	fmt.Fprintf(stdout, "\n%d corpus file(s), %d exchange(s) compared against a real cloud's answer\n", len(files), compared)
	fmt.Fprintf(stdout, "%d divergent finding(s) nothing accepts, which is what this gate fails on\n", divergent)
	fmt.Fprintf(stdout, "%d exchange(s) no route serves, which is #74's work queue and not a failure\n", unserved)
	fmt.Fprintf(stdout, "%d divergent finding(s) knowingly accepted, each printed above with its reason\n", excused)
	fmt.Fprintf(stdout, "%d declared value comparison(s) and %d declared order comparison(s) actually ran\n", values, orders)

	// The two warnings are emitted here, before any early return, and that
	// placement is the whole point of them.
	//
	// Both read `acc` alone: neither depends on a single comparison this run
	// made, and neither moves an exit code — that is written into their own
	// doc comments as their contract. So the only thing a later placement can
	// do is *withhold* them, and it did: they sat below the unexercised-
	// invariant guard (#343) and below the stale-exemption guard, so a run
	// that went red for either reason printed no word of the corpus that a
	// provider has been measured to have moved under. A repository whose gate
	// is already red is the one that most needs to know its recordings are
	// stale, because "re-record it" may be the very fix.
	//
	// This is the shape CLAUDE.md's "un commentaire n'est pas un contrôle" is
	// about, one turn further in: the comment on warnMovedCorpora says "on
	// every run", and there was no test that made it true.
	// TestTheMovedWarningSurvivesARunThatIsRedForAnotherReason fails without
	// this placement, and TestACorpusTheCloudHasMovedUnderWarnsAndDoesNotFail
	// reaches its own subject only because of it.
	warnAgedCorpora(acc, now, stdout)
	warnMovedCorpora(acc, stdout)

	// The subject of a declared comparison, asserted rather than hoped for.
	//
	// Presence and type are compared everywhere; a value and an order are
	// compared only where a pack declares an invariant, and such a declaration
	// is exactly a place this repository has already been wrong — the order of
	// Server.public_ips is #320, a defect that cost a pull request. The two
	// counts above therefore have to be able to be zero for a reason, and
	// "nothing in the corpus reaches the operation" is not one: it reads as a
	// clean pass on a check that never happened, which is the defect shape
	// CLAUDE.md's "un commentaire n'est pas un contrôle" is about and the one
	// TestACorpusThatComparesNothingIsRed already refuses one level up.
	//
	// This is what the corpus was silently failing at for the whole of its
	// first life: every Scaleway invariant lives on CreateServer, GetServer and
	// UpdateServer, a server is billed, and the two free recordings therefore
	// ran values_checked=0 and orders_checked=0 while the gate printed "no
	// divergence" (#343).
	//
	// The condition is the packs' own declaration and not a constant, so a
	// repository whose packs declare no invariant of a kind is not asked to
	// exercise one: a control that fires where there is nothing to control is
	// how a gate gets disabled.
	// TestACorpusThatRunsNoDeclaredComparisonIsRed fails without this.
	if bad := unexercisedInvariantKinds(invariants, values, orders); len(bad) > 0 {
		for _, b := range bad {
			fmt.Fprintf(stderr, "feint: the packs declare %d %s invariant(s) and this corpus ran none of them: "+
				"no recording reaches the operations they name, so the comparison they describe did not happen\n",
				b.declared, b.kind)
		}
		return exitError
	}

	// An exemption that excused nothing. The rule tools/compat/accepted.json
	// states and this one inherits: a stale exemption is a gate that quietly
	// stopped covering what it names, and the day the defect is fixed is the day
	// the entry has to go.
	// TestAnExemptionThatExcusesNothingFailsTheGate fails without this.
	stale := 0
	for i, e := range acc.Accepted {
		if used[i] == 0 {
			fmt.Fprintf(stderr, "feint: the accepted divergence %s excused nothing this run: either it is fixed and the entry goes, or the corpus no longer carries it\n", e.key())
			stale++
		}
	}
	if stale > 0 {
		return exitError
	}

	if divergent > 0 {
		fmt.Fprintf(stderr, "feint: %d divergence(s) between this emulator and the recorded cloud, none of them accepted in %s\n", divergent, acceptedPath)
		return exitDrift
	}
	return exitOK
}

// unexercisedInvariantKind is one kind of declared comparison that no exchange
// of the corpus ran, with how many the packs declare so the message can name
// what went unmeasured rather than only that something did.
type unexercisedInvariantKind struct {
	kind     emulator.InvariantKind
	declared int
}

// unexercisedInvariantKinds reports the kinds the packs declare and this run
// never evaluated.
//
// Per kind rather than as one total, and the falsification is the same one that
// split replay.Report's own two counters: with a single number, breaking the
// ordering declaration left the value declarations still counting, the total
// stayed above zero, and the test meant to prove the order check ran stayed
// green (tools/falsify/specs/replay-compares.json, run of 2026-08-20).
func unexercisedInvariantKinds(invariants []emulator.Invariant, values, orders int) []unexercisedInvariantKind {
	declared := map[emulator.InvariantKind]int{}
	for _, i := range invariants {
		declared[i.Kind]++
	}
	ran := map[emulator.InvariantKind]int{
		emulator.InvariantValue: values,
		emulator.InvariantOrder: orders,
	}
	var out []unexercisedInvariantKind
	for _, kind := range []emulator.InvariantKind{emulator.InvariantValue, emulator.InvariantOrder} {
		if declared[kind] > 0 && ran[kind] == 0 {
			out = append(out, unexercisedInvariantKind{kind: kind, declared: declared[kind]})
		}
	}
	return out
}

// checkCorpusRecordings holds the corpus and its manifest to each other, in both
// directions.
//
// Both directions, because each one alone rots: a file with no entry replays
// with nobody knowing how old it is, and an entry with no file is a claim about
// a recording that left. TestTheCorpusAndItsRecordingDatesAreHeldToEachOther
// fails without this, in one subtest per direction.
func checkCorpusRecordings(files []string, recorded []corpusRecording, dir string, stderr io.Writer) int {
	known := map[string]bool{}
	for _, r := range recorded {
		known[r.File] = true
	}
	bad := 0
	for _, f := range files {
		if !known[f] {
			fmt.Fprintf(stderr, "feint: %s is committed and no entry says when it was recorded: add it to the acceptance file's \"recorded\" list\n", f)
			bad++
		}
	}
	present := map[string]bool{}
	for _, f := range files {
		present[f] = true
	}
	for _, r := range recorded {
		if !present[r.File] {
			fmt.Fprintf(stderr, "feint: the recording entry for %s names no file under %s\n", r.File, dir)
			bad++
		}
		if _, err := time.Parse(time.DateOnly, r.At); err != nil {
			fmt.Fprintf(stderr, "feint: the recording entry for %s carries no readable date (%q, want YYYY-MM-DD)\n", r.File, r.At)
			bad++
		}
	}
	if bad > 0 {
		return exitError
	}
	return exitOK
}

// reportCorpusFile prints one file's result and returns how many divergent
// findings nothing accepted.
//
// The unit is the finding rather than the exchange, deliberately: an exchange
// with one accepted absence and one unexpected status must not read as
// accepted because the exemption matched half of it.
func reportCorpusFile(file string, rep replay.Report, accepted []corpusException, used []int, excused *int, stdout, stderr io.Writer) int {
	unaccepted := 0
	// Collapsed per exemption rather than per finding: the catalogue exemption
	// covers 127 commercial types, and printing them one by one buries the
	// findings a reader has to act on. The count stays, so what the gate
	// subtracts is still a number and not a shrug.
	hits := map[int]int{}
	for _, r := range rep.Results {
		if r.Verdict != replay.Divergent {
			continue
		}
		for _, f := range r.Findings {
			if f.Kind == replay.KindExcused || f.Kind == replay.KindRedacted {
				continue
			}
			if i, ok := acceptedBy(accepted, file, r.Operation, f); ok {
				used[i]++
				hits[i]++
				*excused++
				continue
			}
			if unaccepted == 0 {
				fmt.Fprintf(stderr, "%s: this emulator answers differently from the recorded cloud\n", file)
			}
			fmt.Fprintf(stderr, "  %-44s %s\n", r.Operation, describeFinding(f))
			unaccepted++
		}
	}
	keys := make([]int, 0, len(hits))
	for i := range hits {
		keys = append(keys, i)
	}
	sort.Ints(keys)
	for _, i := range keys {
		fmt.Fprintf(stdout, "accepted %s: %d finding(s) — %s (%s)\n",
			accepted[i].key(), hits[i], accepted[i].Reason, accepted[i].Issue)
	}
	return unaccepted
}

// acceptedBy returns the index of the entry that excuses this finding.
func acceptedBy(accepted []corpusException, file, operation string, f replay.Finding) (int, bool) {
	for i, e := range accepted {
		if e.matches(file, operation, f) {
			return i, true
		}
	}
	return 0, false
}

// replayCorpusFile replays one corpus against an emulator of its own.
//
// Of its own, not a shared one, and corpus/README.md says why: a corpus is a
// causal sequence that creates what it later reads, so a second file replayed
// into the same store meets objects its own recording never created, and an
// unfiltered list then answers more than the cloud did.
func replayCorpusFile(dir, file string, env *emulator.Env, packs []emulator.Pack, declined []emulator.FieldDecline, invariants []emulator.Invariant) (replay.Report, error) {
	exs, err := loadTranscript(filepath.Join(dir, filepath.FromSlash(file)))
	if err != nil {
		return replay.Report{}, err
	}
	// A transcript that parsed to nothing. Refused here rather than replayed,
	// so the caller never has to tell "read and empty" from "not read".
	// TestACorpusThatComparesNothingIsRed fails without this.
	if len(exs) == 0 {
		return replay.Report{}, fmt.Errorf("holds no exchange, so there was nothing to compare")
	}
	srv, err := emulator.NewServer(env, packs...)
	if err != nil {
		return replay.Report{}, fmt.Errorf("build the emulator: %w", err)
	}
	table, err := emulator.NewTable(packs...)
	if err != nil {
		return replay.Report{}, fmt.Errorf("build the route table: %w", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	return replay.Run(context.Background(), exs, replay.Options{
		Endpoint:   ts.URL,
		Client:     &http.Client{Timeout: 30 * time.Second},
		Table:      table,
		Declined:   declined,
		Invariants: invariants,
	})
}

// corpusFiles lists every committed corpus under dir, as slash-separated paths
// relative to it, sorted so two runs report the same order.
func corpusFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no corpus directory at %s: the committed recordings are what this gate compares against", dir)
		}
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func readCorpusAcceptance(path string) (corpusAcceptance, error) {
	var acc corpusAcceptance
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied path, read-only
	if err != nil {
		return acc, fmt.Errorf("read the acceptance file %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &acc); err != nil {
		return acc, fmt.Errorf("read the acceptance file %s: %w", path, err)
	}
	return acc, nil
}

// unusableCorpusExceptions names the entries that excuse without arguing.
//
// The reason faces emulator's own guard rather than a second one written here:
// one implementation, so an exemption in this file and a decline in a pack are
// held to the same sentence. The issue is required on top, because a reason
// says why the divergence is tolerable today and only an issue says when it
// stops being.
func unusableCorpusExceptions(accepted []corpusException) []unusableException {
	var out []unusableException
	for _, e := range accepted {
		switch {
		case len(emulator.UnexplainedFieldDeclines([]emulator.FieldDecline{{Reason: e.Reason}})) > 0:
			out = append(out, unusableException{e.key(), "carries no usable reason"})
		case strings.TrimSpace(e.Issue) == "":
			out = append(out, unusableException{e.key(), "names no issue that retires it"})
		case strings.TrimSpace(e.Kind) == "":
			out = append(out, unusableException{e.key(), "names no finding kind, so it would excuse every kind at that path"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// unusableException is one refused entry and the sentence that says why. The
// three causes are printed apart rather than as one sentence listing them,
// because a message that names every possible cause names none of them.
type unusableException struct{ key, why string }

// warnAgedCorpora says which recordings have outlived the horizon, and changes
// no exit code.
//
// # How a corpus ages, and why the answer is a warning
//
// A cloud changes. A corpus recorded today describes the cloud of today, and a
// gate that fails because the *cloud* moved is a gate that gets disabled — with
// the whole of its coverage. Three options were on the table (#353): an expiry
// that fails, a periodic re-record like upstream:sync, and an acceptance file.
// This gate carries the second and third, and the expiry only warns, for a
// reason that is a limit of the measurement rather than a preference:
//
// **This gate cannot tell "the cloud moved" from "the emulator broke."** It has
// one side of the comparison. A red run says the two disagree, and nothing in
// this process knows which of them changed. Failing on age would be this gate
// asserting the very thing it cannot measure. The half that can is #359, which
// replays the corpus against the *cloud*; until it exists, the honest form is a
// warning that names the file, its age and the procedure to re-record it.
//
// So the warning is loud, it is on every run, and its date lives in a committed
// file — moving it is a diff somebody signs, not a flag somebody passes.
// TestAnAgedCorpusWarnsAndDoesNotFailTheGate fails without this, in both
// directions: the warning must appear, and the exit code must not move.
func warnAgedCorpora(acc corpusAcceptance, now time.Time, stdout io.Writer) {
	for _, name := range agedCorpora(acc.Recorded, now, acc.WarnAfterDays) {
		fmt.Fprintf(stdout, "warning: %s, and a cloud changes: re-record it (corpus/README.md, \"Recording another one\"), or wait for #359 to say whether it is the cloud that moved\n", name)
	}
}

// warnMovedCorpora says which recordings the *cloud* has been measured to have
// moved under, and changes no exit code either.
//
// The same argument as [warnAgedCorpora], one step less speculative: there, the
// gate warns because a recording is old and a cloud might have moved; here it
// warns because somebody pointed `corpus --against-cloud` at the provider and it
// did. Still a warning, because the finding names a file to re-record rather
// than a defect of this emulator, and a gate that fails on somebody else's
// change is a gate that gets disabled.
// TestACorpusTheCloudHasMovedUnderWarnsAndDoesNotFail fails without this.
func warnMovedCorpora(acc corpusAcceptance, stdout io.Writer) {
	for _, r := range acc.Recorded {
		if r.CloudMovedAt == "" || r.CloudMoved == 0 {
			continue
		}
		fmt.Fprintf(stdout, "warning: %s was reissued at %s on %s and %d answer(s) had moved: re-record it (corpus/README.md, \"Recording another one\"), because this file now describes a cloud that no longer answers that way\n",
			r.File, r.Cloud, r.CloudMovedAt, r.CloudMoved)
	}
}

// agedCorpora names the recordings older than the horizon, with their age.
// A horizon of zero disables the warning and is refused by the caller's own
// fixture rather than here, so that a repository which genuinely wants no
// horizon writes the zero rather than inheriting it.
func agedCorpora(recorded []corpusRecording, now time.Time, days int) []string {
	if days <= 0 {
		return nil
	}
	var out []string
	for _, r := range recorded {
		at, err := time.Parse(time.DateOnly, r.At)
		if err != nil {
			// Already refused by checkCorpusRecordings, which runs first and
			// exits. Nothing to report twice.
			continue
		}
		age := int(now.Sub(at).Hours() / 24)
		if age > days {
			out = append(out, fmt.Sprintf("%s was recorded %d days ago against %s, past the %d-day horizon",
				r.File, age, r.Cloud, days))
		}
	}
	sort.Strings(out)
	return out
}
