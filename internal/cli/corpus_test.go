package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The date every fixture is judged against, so a test's verdict never depends
// on the day it runs — the failure mode a support-window test in this package
// already carries a paragraph about.
var corpusNow = time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

// The committed corpus passes its own gate, and says the same thing twice.
//
// Both halves matter and the second is the one #353 asks for. A gate whose
// verdict flaps is a gate that gets disarmed the first time it seems to lie,
// and this one flapped before the bindings were scoped by field name: six
// replays of corpus/scaleway/scw-cli.jsonl against six fresh emulators graded
// vpc/v2/API.ListPrivateNetworks divergent three times and matched three times
// (corpus/README.md). Comparing two whole runs byte for byte covers the counts,
// the order of the accepted list and every finding printed.
func TestTheCommittedCorpusPassesItsOwnGateAndSaysTheSameThingTwice(t *testing.T) {
	first, firstErr, code := runCorpusGate(t, "../../corpus", "../../corpus/accepted.json")
	if code != exitOK {
		t.Fatalf("the committed corpus exited %d, want 0.\nstdout:\n%s\nstderr:\n%s", code, first, firstErr)
	}
	second, secondErr, again := runCorpusGate(t, "../../corpus", "../../corpus/accepted.json")
	if again != code || second != first || secondErr != firstErr {
		t.Fatalf("two runs of the same corpus disagree.\nfirst (exit %d):\n%s%s\nsecond (exit %d):\n%s%s",
			code, first, firstErr, again, second, secondErr)
	}
	// The subject is asserted rather than assumed: a run that compared nothing
	// would also print no divergence, and this whole gate exists because that
	// reads identically to a run that found nothing wrong.
	if !strings.Contains(first, "exchange(s) compared against a real cloud's answer") ||
		strings.Contains(first, "\n0 exchange(s) compared") {
		t.Fatalf("the gate does not say how many exchanges it compared, or compared none:\n%s", first)
	}
}

// The verdict is a function of the two paths it is given and of nothing else in
// the tree. That is what makes this gate exempt from the rule that cost #88 two
// red pull requests: its population is not "whatever this run happened to
// drive", it is a directory of versioned files, identical on every runner and
// in every CI leg.
//
// Proved by copying that directory somewhere else and requiring the same
// output, rather than by asserting it in a comment.
func TestTheGateReadsOnlyTheFilesItIsPointedAt(t *testing.T) {
	committed, _, code := runCorpusGate(t, "../../corpus", "../../corpus/accepted.json")
	if code != exitOK {
		t.Fatalf("the committed corpus exited %d, want 0", code)
	}
	elsewhere := filepath.Join(t.TempDir(), "corpus")
	copyTree(t, "../../corpus", elsewhere)
	copied, _, copiedCode := runCorpusGate(t, elsewhere, filepath.Join(elsewhere, "accepted.json"))
	if copiedCode != code || copied != committed {
		t.Fatalf("the same corpus read from %s gives a different verdict (%d vs %d):\n%s\nvs\n%s",
			elsewhere, copiedCode, code, copied, committed)
	}
}

// The two environment variables that can change which routes are mounted, and
// therefore which exchanges count as unserved: FEINT_OUTSCALE_REGION and
// FEINT_EXOSCALE_ZONE, read by packsFor.
//
// They are the one seam through which this gate's population is not purely a
// set of committed files, and the claim in corpusCommand's doc comment is only
// true while they make no difference. Today they make none, because the corpus
// is Scaleway's alone — but the day somebody records Outscale (#354) this test
// goes red, and whoever does gets to decide rather than discover. Measured
// rather than reasoned about: mounting from packsFor is deliberate, since a
// hardcoded pack list is what made a fourth pack invisible to `shapes --check`
// instead of reported.
func TestTheGatesVerdictDoesNotDependOnTheEnvironment(t *testing.T) {
	plain, plainErr, code := runCorpusGate(t, "../../corpus", "../../corpus/accepted.json")
	if code != exitOK {
		t.Fatalf("the committed corpus exited %d, want 0", code)
	}
	t.Setenv("FEINT_OUTSCALE_REGION", "eu-west-2")
	t.Setenv("FEINT_EXOSCALE_ZONE", "de-fra-1")
	altered, alteredErr, alteredCode := runCorpusGate(t, "../../corpus", "../../corpus/accepted.json")
	if alteredCode != code || altered != plain || alteredErr != plainErr {
		t.Fatalf("an environment variable moved the verdict (%d vs %d): the population is no longer "+
			"the committed files alone, and this gate's claim to be exempt from the partial-run rule "+
			"has to be re-argued.\nplain:\n%s%s\naltered:\n%s%s",
			alteredCode, code, plain, plainErr, altered, alteredErr)
	}
}

// #353's falsification, and the one it names first: mutate one recorded status
// and the gate goes red naming it.
//
// The fixture is a recording of this emulator by this emulator, so the clean
// run needs no exemption at all — the only difference between green and red is
// the mutation.
func TestAMutatedRecordedStatusTurnsTheGateRedAndNamesIt(t *testing.T) {
	dir, accepted := corpusFixture(t, recordAgainstAFreshEmulator(t))
	out, errs, code := runCorpusGate(t, dir, accepted)
	if code != exitOK {
		t.Fatalf("a recording of this emulator failed its own gate (%d).\nstdout:\n%s\nstderr:\n%s", code, out, errs)
	}

	operation := mutateStatus(t, filepath.Join(dir, "self/self.jsonl"), 201, 418)
	out, errs, code = runCorpusGate(t, dir, accepted)
	if code != exitDrift {
		t.Fatalf("a mutated recorded status exited %d, want %d (drift).\nstdout:\n%s\nstderr:\n%s",
			code, exitDrift, out, errs)
	}
	if !strings.Contains(errs, operation) {
		t.Fatalf("the gate does not name the operation whose status was mutated (%s):\n%s", operation, errs)
	}
	if !strings.Contains(errs, "expected 418") {
		t.Fatalf("the gate does not name the status it expected:\n%s", errs)
	}
}

// A corpus that replays nothing is red. The rule this repository refuses to
// break a second time: the network conformance suite returned SKIP in CI from
// the day it was written, and five controls in tools/ui/check-page.py were
// found measuring nothing in August 2026.
//
// Three shapes of "nothing", each asserted, because each one arrives by its own
// route: a file that parses to no exchange, a directory holding no file, and a
// file whose every exchange addresses a route no pack mounts.
func TestACorpusThatComparesNothingIsRed(t *testing.T) {
	t.Run("an empty file", func(t *testing.T) {
		dir, accepted := corpusFixture(t, writeLines(t, ""))
		out, errs, code := runCorpusGate(t, dir, accepted)
		if code != exitError {
			t.Fatalf("an empty corpus exited %d, want %d (error).\nstdout:\n%s\nstderr:\n%s", code, exitError, out, errs)
		}
		if !strings.Contains(errs, "nothing to compare") {
			t.Fatalf("the gate does not say the corpus held nothing:\n%s", errs)
		}
	})

	t.Run("no file at all", func(t *testing.T) {
		dir := t.TempDir()
		accepted := writeAccepted(t, corpusAcceptance{WarnAfterDays: 180})
		out, errs, code := runCorpusGate(t, dir, accepted)
		if code != exitError {
			t.Fatalf("an empty corpus directory exited %d, want %d (error).\nstdout:\n%s\nstderr:\n%s", code, exitError, out, errs)
		}
		if !strings.Contains(errs, "compared nothing against a real cloud") {
			t.Fatalf("the gate does not say it compared nothing:\n%s", errs)
		}
	})

	t.Run("every exchange unserved", func(t *testing.T) {
		// A product no pack mounts. Unserved is a work item and never a
		// divergence — but a whole corpus of them compared nothing, and that is
		// an error, not a pass.
		line := `{"seq":1,"method":"GET","path":"/nowhere/v1/things","status":200,` +
			`"req":{"headers":{}},"res":{"headers":{},"body":{"things":[]}}}`
		dir, accepted := corpusFixture(t, writeLines(t, line))
		out, errs, code := runCorpusGate(t, dir, accepted)
		if code != exitError {
			t.Fatalf("a corpus of unserved operations exited %d, want %d (error).\nstdout:\n%s\nstderr:\n%s",
				code, exitError, out, errs)
		}
		if !strings.Contains(errs, "replayed against nothing") {
			t.Fatalf("the gate does not say the file compared nothing:\n%s", errs)
		}
	})
}

// A corpus file nobody dated, and a date naming no file. Both directions,
// because each alone rots: an undated file replays with nobody able to say how
// old the cloud it describes is, and a date with no file is a claim about a
// recording that left.
func TestTheCorpusAndItsRecordingDatesAreHeldToEachOther(t *testing.T) {
	t.Run("a file with no recording entry", func(t *testing.T) {
		dir, _ := corpusFixture(t, recordAgainstAFreshEmulator(t))
		bare := writeAccepted(t, corpusAcceptance{WarnAfterDays: 180})
		_, errs, code := runCorpusGate(t, dir, bare)
		if code != exitError {
			t.Fatalf("an undated corpus exited %d, want %d (error):\n%s", code, exitError, errs)
		}
		if !strings.Contains(errs, "no entry says when it was recorded") {
			t.Fatalf("the gate does not name what is missing:\n%s", errs)
		}
	})

	t.Run("a recording entry naming no file", func(t *testing.T) {
		dir, _ := corpusFixture(t, recordAgainstAFreshEmulator(t))
		accepted := writeAccepted(t, corpusAcceptance{
			WarnAfterDays: 180,
			Recorded: []corpusRecording{
				{File: "self/self.jsonl", At: "2026-08-21", Client: "feint", Cloud: "this emulator"},
				{File: "self/gone.jsonl", At: "2026-08-21", Client: "feint", Cloud: "this emulator"},
			},
		})
		_, errs, code := runCorpusGate(t, dir, accepted)
		if code != exitError {
			t.Fatalf("a date naming no file exited %d, want %d (error):\n%s", code, exitError, errs)
		}
		if !strings.Contains(errs, "names no file") {
			t.Fatalf("the gate does not name the stale entry:\n%s", errs)
		}
	})
}

// An exemption that excuses nothing fails the gate — the rule
// tools/compat/accepted.json states and this file inherits. It is what makes
// the list self-retiring, and it did retire the list: the day #355 served the
// default VPC's tags this gate went red on an entry that had stopped excusing
// anything, and it stayed red until all eight were deleted. Nobody had to
// remember, which is the whole of the mechanism.
func TestAnExemptionThatExcusesNothingFailsTheGate(t *testing.T) {
	dir, _ := corpusFixture(t, recordAgainstAFreshEmulator(t))
	accepted := writeAccepted(t, corpusAcceptance{
		WarnAfterDays: 180,
		Recorded:      []corpusRecording{{File: "self/self.jsonl", At: "2026-08-21", Client: "feint", Cloud: "this emulator"}},
		Accepted: []corpusException{{
			File: "self/self.jsonl", Operation: "instance/v1/API.CreateServer", Kind: "absent", Path: "server.nothing",
			Reason: "a field this recording does not carry and this emulator does not omit, so the entry covers no finding at all",
			Issue:  "#353",
		}},
	})
	_, errs, code := runCorpusGate(t, dir, accepted)
	if code != exitError {
		t.Fatalf("a stale exemption exited %d, want %d (error):\n%s", code, exitError, errs)
	}
	if !strings.Contains(errs, "excused nothing this run") {
		t.Fatalf("the gate does not say the exemption is stale:\n%s", errs)
	}
}

// An exemption whose reason carries no decision, and one that names no issue.
// The same guard a pack's own declines face, through the same implementation:
// an exemption is a decision, and a decision that argues nothing is a comment.
func TestAnExemptionWithoutAUsableReasonFailsTheGate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		entry  corpusException
		expect string
	}{
		{
			name: "a placeholder reason",
			entry: corpusException{
				File: "self/self.jsonl", Operation: "instance/v1/API.CreateServer", Kind: "status", Path: "",
				Reason: "TODO", Issue: "#353",
			},
			expect: "no usable reason",
		},
		{
			name: "no issue to retire it",
			entry: corpusException{
				File: "self/self.jsonl", Operation: "instance/v1/API.CreateServer", Kind: "status", Path: "",
				Reason: "a status this emulator answers differently for a reason somebody wrote down at length",
				Issue:  "",
			},
			expect: "names no issue",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := corpusFixture(t, recordAgainstAFreshEmulator(t))
			accepted := writeAccepted(t, corpusAcceptance{
				WarnAfterDays: 180,
				Recorded:      []corpusRecording{{File: "self/self.jsonl", At: "2026-08-21", Client: "feint", Cloud: "this emulator"}},
				Accepted:      []corpusException{tc.entry},
			})
			_, errs, code := runCorpusGate(t, dir, accepted)
			if code != exitError {
				t.Fatalf("an unusable exemption exited %d, want %d (error):\n%s", code, exitError, errs)
			}
			if !strings.Contains(errs, tc.expect) {
				t.Fatalf("the gate does not say %q:\n%s", tc.expect, errs)
			}
		})
	}
}

// How a corpus ages: it warns, and it never fails.
//
// Both directions are asserted and the second carries the decision. A gate that
// failed on age would be asserting what it cannot measure — it holds one side of
// the comparison and cannot tell a cloud that moved from an emulator that broke
// (#359 is the half that can) — and a gate that fails because the cloud changed
// is a gate somebody disables, taking all of its coverage with it. A warning
// that could quietly stop appearing would be the other failure, so the first
// half is asserted too.
func TestAnAgedCorpusWarnsAndDoesNotFailTheGate(t *testing.T) {
	dir, _ := corpusFixture(t, recordAgainstAFreshEmulator(t))
	accepted := writeAccepted(t, corpusAcceptance{
		WarnAfterDays: 180,
		Recorded:      []corpusRecording{{File: "self/self.jsonl", At: "2025-01-01", Client: "feint", Cloud: "this emulator"}},
	})
	out, errs, code := runCorpusGate(t, dir, accepted)
	if code != exitOK {
		t.Fatalf("an aged corpus exited %d, want 0: age must never fail this gate.\nstdout:\n%s\nstderr:\n%s",
			code, out, errs)
	}
	if !strings.Contains(out, "warning:") || !strings.Contains(out, "past the 180-day horizon") {
		t.Fatalf("an aged corpus produced no warning:\n%s", out)
	}
	if !strings.Contains(out, "self/self.jsonl") {
		t.Fatalf("the warning does not name the file that aged:\n%s", out)
	}

	// And a fresh one says nothing, so the warning is a measurement and not a
	// line printed on every run.
	fresh := writeAccepted(t, corpusAcceptance{
		WarnAfterDays: 180,
		Recorded:      []corpusRecording{{File: "self/self.jsonl", At: "2026-08-01", Client: "feint", Cloud: "this emulator"}},
	})
	out, _, code = runCorpusGate(t, dir, fresh)
	if code != exitOK || strings.Contains(out, "warning:") {
		t.Fatalf("a recording of three weeks ago warned (exit %d):\n%s", code, out)
	}
}

// An unserved operation is reported and never counted against the verdict — the
// second of the three verdicts this gate must not blur. The day it fails the
// build is the day somebody stops recording, which would make #74's queue
// unfeedable.
func TestAnUnservedOperationDoesNotFailTheCorpusGate(t *testing.T) {
	recording := recordAgainstAFreshEmulator(t)
	raw, err := os.ReadFile(recording) //nolint:gosec // a file this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	unserved := `{"seq":99,"method":"GET","path":"/nowhere/v1/things","status":200,` +
		`"req":{"headers":{}},"res":{"headers":{},"body":{"things":[]}}}` + "\n"
	dir, accepted := corpusFixture(t, writeLines(t, string(raw)+unserved))
	out, errs, code := runCorpusGate(t, dir, accepted)
	if code != exitOK {
		t.Fatalf("an unserved operation failed the gate (%d).\nstdout:\n%s\nstderr:\n%s", code, out, errs)
	}
	if !strings.Contains(out, "1 exchange(s) no route serves") {
		t.Fatalf("the gate does not report the unserved operation:\n%s", out)
	}
}

// --- helpers ---------------------------------------------------------------

func runCorpusGate(t *testing.T, dir, accepted string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := checkCorpus(dir, accepted, corpusNow, &out, &errb)
	return out.String(), errb.String(), code
}

// corpusFixture lays a recording out as a corpus directory with its acceptance
// file, and returns both paths.
func corpusFixture(t *testing.T, recording string) (dir, accepted string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "corpus")
	if err := os.MkdirAll(filepath.Join(dir, "self"), 0o750); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(recording) //nolint:gosec // a file this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "self", "self.jsonl"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	accepted = writeAccepted(t, corpusAcceptance{
		WarnAfterDays: 180,
		Recorded: []corpusRecording{
			{File: "self/self.jsonl", At: "2026-08-21", Client: "feint", Cloud: "this emulator"},
		},
	})
	return dir, accepted
}

func writeAccepted(t *testing.T, acc corpusAcceptance) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "accepted.json")
	raw, err := json.MarshalIndent(acc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeLines(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "written.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// mutateStatus changes the first recorded status equal to from, and returns the
// operation the mutated exchange names — so the assertion is about the exchange
// that moved rather than about a string the test hardcoded.
func mutateStatus(t *testing.T, path string, from, to int) string {
	t.Helper()
	file, err := os.Open(path) //nolint:gosec // a file this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	var out bytes.Buffer
	operation := ""
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		var x map[string]any
		if operation == "" && json.Unmarshal(line, &x) == nil {
			if status, ok := x["status"].(float64); ok && int(status) == from {
				x["status"] = to
				operation, _ = x["operation"].(string)
				raw, err := json.Marshal(x)
				if err != nil {
					t.Fatal(err)
				}
				line = raw
			}
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if operation == "" {
		t.Fatalf("the recording carries no exchange answering %d, so the fixture no longer exercises one", from)
	}
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return operation
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o750); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		from, to := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		if e.IsDir() {
			copyTree(t, from, to)
			continue
		}
		raw, err := os.ReadFile(from) //nolint:gosec // a path this test built
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
