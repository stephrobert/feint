package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/corpus"
	"github.com/stephrobert/feint/internal/trace"
)

// The second direction of #351, and the one a sanitiser fails silently:
// **a sanitised transcript still replays**.
//
// The first direction — nothing of the account survives — is easy to satisfy by
// destroying the file, and a sanitiser that destroys the file has produced a
// museum piece. So this is the same identity case
// TestATranscriptOfThisEmulatorReplaysAgainstAFreshOneWithoutDivergence runs,
// with one step inserted: the recording is sanitised before it is replayed, and
// the run must still come back with no divergence and with identifiers rebound.
//
// It bites on every part of the design at once. Blank the zone (remove
// PublicVocabulary) and the emulator answers 400 "unknown zone" to every call;
// replace a UUID with a bare token and every read answers 404; lose the
// consistency of the substitution and the create's own identifier no longer
// matches the read that follows.
func TestASanitisedTranscriptStillReplays(t *testing.T) {
	sanitised := sanitisedRecording(t)

	target := freshEmulator(t)
	out, errs, code := runReplay(t, sanitised, "--endpoint", target.URL)
	if code != exitOK {
		t.Fatalf("a sanitised transcript exited %d, want 0.\nstdout:\n%s\nstderr:\n%s", code, out, errs)
	}
	if strings.Contains(out, "DIFF") {
		t.Fatalf("a sanitised transcript of this emulator diverged against this emulator:\n%s", out)
	}
	if strings.Contains(out, "NOT SERVED") {
		t.Fatalf("sanitising moved a request off the route that served it:\n%s", out)
	}
	if !strings.Contains(out, "instance/v1/API.CreateServer") {
		t.Fatalf("the sanitised recording no longer exercises the create it is built around:\n%s", out)
	}
	if strings.Contains(out, "0 recorded identifier(s) rebound") {
		t.Fatalf("nothing was rebound: the synthetic identifiers are not being bound to the ones "+
			"this emulator mints, so this proves nothing about a fresh instance:\n%s", out)
	}
}

// The first direction, at the level that matters: when a value of the recording
// survives, **nothing is written**.
//
// A tool that reports a leak after writing the file has already published it,
// so the property under test is the absence of the file, not the wording of the
// message. The leak is provoked the one way that needs no broken sanitiser: the
// recording is given a value equal to one this package mints, so the artefact
// and the source share a string, and [corpus.Audit] refuses on the collision
// rather than publishing a file in which the two cannot be told apart.
func TestASanitisationThatKeptAValueWritesNothing(t *testing.T) {
	recording := recordAgainstAFreshEmulator(t)
	mutate(t, recording, `"replay-fixture"`, `"`+corpus.Token+`1"`)

	out := filepath.Join(t.TempDir(), "corpus.jsonl")
	var stdout, stderr bytes.Buffer
	code := transcriptCommand([]string{recording, "--sanitise", out,
		"--contract", filepath.Join("..", "..", "contracts", "scaleway.json")}, &stdout, &stderr)
	if code == exitOK {
		t.Fatalf("a value shared with the recording was published:\n%s", stdout.String())
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("the artefact was written despite the leak, so the leak was published before it was reported")
	}
	if !strings.Contains(stderr.String(), "nothing was written") {
		t.Errorf("the refusal does not say the file was not written:\n%s", stderr.String())
	}
}

// Every value of the committed corpus is one a sanitised transcript may carry.
//
// The alphabet check, run over the artefacts on disk rather than over the
// function that produced them: a corpus committed by hand, or one produced by
// an older version of the sanitiser, faces the same rule.
func TestTheCommittedCorpusCarriesOnlyWhatASanitisedTranscriptMay(t *testing.T) {
	files := committedCorpus(t)
	opt := corpus.Options{Doc: mustContract(t), Vocabulary: vocabularyOfPacks(t)}
	for _, file := range files {
		exs := mustLoad(t, file)
		if len(exs) == 0 {
			t.Errorf("%s carries no exchange: a corpus file that measures nothing", file)
		}
		for _, leak := range corpus.Scan(exs, opt) {
			t.Errorf("%s: %s", filepath.Base(file), leak)
		}
	}
}

// identifierShapes are the spellings that must not appear in a committed
// corpus, whatever the sanitiser believes.
//
// This is the tooth that disbelieves [corpus.Scan] the way
// TestNoCommittedCatalogueCarriesAnIdentifier disbelieves AnonymisePath: it
// reads the bytes a commit would publish and knows nothing about the rules that
// produced them. A UUID outside the synthetic namespace, an address outside the
// two synthetic spaces, an email, a PEM block: each is refused by shape, and a
// provider that invented a fourth identifier spelling would satisfy the scan
// and fail here — which is the order things should fail in.
var identifierShapes = []struct {
	name string
	re   *regexp.Regexp
}{
	{"a UUID that is not one this repository minted",
		regexp.MustCompile(`(?i)\b(?:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\b`)},
	{"an IPv4 address outside 198.18.0.0/15",
		regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)},
	{"an email address", regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.-]+`)},
	{"a PEM block", regexp.MustCompile(`-----BEGIN [A-Z ]+-----`)},
}

func TestNoCommittedCorpusCarriesAnIdentifier(t *testing.T) {
	synthetic := regexp.MustCompile(`^00000000-0000-4000-8000-\d{12}$`)
	inSyntheticV4 := regexp.MustCompile(`^198\.(1[89])\.\d{1,3}\.\d{1,3}$`)

	files := committedCorpus(t)
	for _, file := range files {
		body, err := os.ReadFile(file) //nolint:gosec // a committed artefact of this repository
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		text := string(body)
		for _, shape := range identifierShapes {
			for _, found := range shape.re.FindAllString(text, -1) {
				switch {
				case synthetic.MatchString(found), inSyntheticV4.MatchString(found):
					continue
				}
				t.Errorf("%s carries %s: %q — it belongs to whoever recorded it",
					filepath.Base(file), shape.name, found)
			}
		}
	}
}

// committedCorpus lists the corpus files, and fails when there are none: a scan
// that found no file would pass while measuring nothing, which is the failure
// mode this repository has already paid for elsewhere.
func committedCorpus(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "corpus", "*", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no corpus found under corpus/: the scan is broken, not the files")
	}
	return files
}

// The wiring half of emulator.UnsafeVocabulary: the guard exists, and it is
// pointed at the packs actually mounted. TestAVocabularyEntryThatLooksMintedIsRefused
// holds the guard itself.
func TestThePacksVocabularyPassesItsOwnGuard(t *testing.T) {
	var errs bytes.Buffer
	values, code := packVocabulary(mustPacks(t), &errs)
	if code != exitOK {
		t.Fatalf("a mounted pack vouches for something it may not:\n%s", errs.String())
	}
	if len(values) == 0 {
		t.Fatal("no pack vouches for anything, so this test measures nothing: " +
			"a sanitised Scaleway transcript would carry no zone and replay 400 on every call")
	}
}

func sanitisedRecording(t *testing.T) string {
	t.Helper()
	recording := recordAgainstAFreshEmulator(t)
	out := filepath.Join(t.TempDir(), "corpus.jsonl")
	var stdout, stderr bytes.Buffer
	code := transcriptCommand([]string{recording, "--sanitise", out,
		"--contract", filepath.Join("..", "..", "contracts", "scaleway.json")}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("sanitising exited %d:\n%s\n%s", code, stdout.String(), stderr.String())
	}
	return out
}

func mustContract(t *testing.T) *contract.Doc {
	t.Helper()
	doc, err := contract.Load(filepath.Join("..", "..", "contracts", "scaleway.json"))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func vocabularyOfPacks(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, p := range mustPacks(t) {
		out = append(out, emulator.VocabularyOf(p)...)
	}
	return out
}

func mustLoad(t *testing.T, path string) []trace.Exchange {
	t.Helper()
	exs, err := loadTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	return exs
}
