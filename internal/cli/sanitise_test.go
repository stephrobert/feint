package cli

import (
	"bytes"
	"net/netip"
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
	vocabulary := vocabularyOfPacks(t)
	for _, file := range files {
		exs := mustLoad(t, file)
		if len(exs) == 0 {
			t.Errorf("%s carries no exchange: a corpus file that measures nothing", file)
		}
		opt := corpus.Options{Doc: contractOfCorpus(t, file), Vocabulary: vocabulary}
		for _, leak := range corpus.Scan(exs, opt) {
			t.Errorf("%s: %s", filepath.Base(file), leak)
		}
	}
}

// contractOfCorpus loads the document of the provider whose directory the file
// sits in.
//
// It used to be Scaleway's for every file, which was true while Scaleway was
// the only corpus and became a false verdict the day a second one landed: the
// alphabet a sanitised transcript may carry includes the values the provider's
// own description enumerates, and Exoscale's zone names and instance families
// are in Exoscale's document alone. Read against Scaleway's, the first
// committed Exoscale corpus reported hundreds of leaks that were nothing of the
// sort — the scan measuring its own mis-wiring rather than the file.
//
// TestTheCommittedCorpusCarriesOnlyWhatASanitisedTranscriptMay fails without
// this, on corpus/exoscale/exo-cli.jsonl.
func contractOfCorpus(t *testing.T, file string) *contract.Doc {
	t.Helper()
	provider := filepath.Base(filepath.Dir(file))
	doc, err := contract.Load(filepath.Join("..", "..", "contracts", provider+".json"))
	if err != nil {
		t.Fatalf("%s sits in a directory named for no contract this repository holds: %v", file, err)
	}
	return doc
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
				// Two dotted quads that are not addresses of anybody's account,
				// and the reason is arithmetic rather than judgement: both come
				// from closed sets too small to hold one. "0.0.0.0" is the
				// address half of the default route, the one prefix the
				// sanitiser cannot replace because masking a zero-length prefix
				// yields the same prefix — and every security-group rule that
				// opens a port to the internet carries it. A contiguous netmask
				// is one of thirty-two values, and the sanitiser maps that set
				// onto itself so that a create carrying one is not answered 400.
				// Both are checked here by their own arithmetic rather than by
				// calling internal/corpus, because this test disbelieves those
				// rules on purpose.
				case found == "0.0.0.0", isContiguousNetmask(found):
					continue
				}
				t.Errorf("%s carries %s: %q — it belongs to whoever recorded it",
					filepath.Base(file), shape.name, found)
			}
		}
	}
}

// isContiguousNetmask reports whether a dotted quad is a run of ones followed
// by a run of zeros, with at least one of each: "255.255.255.0" yes,
// "255.0.255.0" no, "0.0.0.0" and "255.255.255.255" no.
//
// Written out of netip rather than borrowed from internal/corpus, because this
// test's whole value is that it knows nothing about the rules that produced the
// file it reads.
func isContiguousNetmask(s string) bool {
	addr, err := netip.ParseAddr(s)
	if err != nil || !addr.Is4() {
		return false
	}
	four := addr.As4()
	v := uint32(four[0])<<24 | uint32(four[1])<<16 | uint32(four[2])<<8 | uint32(four[3])
	if v == 0 || v == ^uint32(0) {
		return false
	}
	// A mask is exactly a value whose complement plus one is a power of two.
	inverted := ^v
	return inverted&(inverted+1) == 0
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

// An Outscale recording keeps the subregion, the flow of a rule, the kind of a
// load balancer and the protocol of a listener.
//
// The Outscale pack vouched for nothing at all until #354, and the corpus could
// not see it because the two committed Outscale recordings were catalogue reads
// carrying none of those values. The first recording of a real account did:
// SubregionName became "redacted-5", knownSubregion refused it — the #269
// invariant — and CreateSubnet answered 400 where the cloud answered 200,
// taking the machine, the volume, the NIC, the public IP, the route table link,
// the NAT service and the load balancer with it.
//
// One test over the four, because they are one decision: a pack keeps the
// closed lists it validates a request against, and each of these is refused by
// name with a 400 that reads exactly like a defect of the emulator.
func TestASanitisedOutscaleTranscriptKeepsWhatThePackValidates(t *testing.T) {
	env := emulator.DefaultEnv()
	packs, err := packsFor(env)
	if err != nil {
		t.Fatal(err)
	}
	var vocabulary []string
	for _, p := range packs {
		if p.Name() == "outscale" {
			vocabulary = emulator.VocabularyOf(p)
		}
	}
	// Asserted, not assumed: a pack that vouches for nothing makes every check
	// below vacuous, and that was exactly the state this test was written for.
	if len(vocabulary) == 0 {
		t.Fatal("the Outscale pack vouches for nothing, so every value of a recording is replaced " +
			"and every create that names a subregion replays 400")
	}
	has := func(want string) bool {
		for _, v := range vocabulary {
			if v == want {
				return true
			}
		}
		return false
	}
	for _, want := range []string{
		"cloudgouv-eu-west-1", "cloudgouv-eu-west-1a", "eu-west-2", "eu-west-2a",
		"Inbound", "Outbound", "internal", "internet-facing", "TCP", "HTTPS",
	} {
		if !has(want) {
			t.Errorf("the Outscale pack does not vouch for %q, which it refuses a request for by name: "+
				"a recording carrying it replays 400 and reads like a defect of the emulator", want)
		}
	}
	// And the guard the declaration itself faces: nothing here may be shaped
	// like something a cloud minted.
	if unsafe := emulator.UnsafeVocabulary(vocabulary); len(unsafe) > 0 {
		t.Errorf("the Outscale vocabulary carries values a sanitiser must never keep: %v", unsafe)
	}
}
