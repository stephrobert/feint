package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The recording below is built from an observed shape with invented values, the
// way docs/proxy.md says a fixture is built. Two Exoscale reads: one the pack
// serves, one it declines with a reason. The identifier is a made-up UUID and
// the agent is the one `exo` was measured sending.
const exoRecording = `{"method":"GET","path":"/v2/zone","operation":"exoscale/v2.list-zones","status":200,"mounted":true,` +
	`"req":{"headers":{"User-Agent":"exocli/1.95.1/0000000 egoscale/v3.1.36 (go1.26.4; linux/amd64)"}},` +
	`"res":{"body":{"zones":[{"name":"ch-gva-2"}]}}}
{"method":"GET","path":"/v2/dns-domain","status":404,"mounted":false,` +
	`"req":{"headers":{"User-Agent":"exocli/1.95.1/0000000 egoscale/v3.1.36 (go1.26.4; linux/amd64)"}},` +
	`"res":{"body":{"message":"not found"}}}
{"method":"GET","path":"/v2/dns-domain","status":404,"mounted":false,` +
	`"req":{"headers":{"User-Agent":"exocli/1.95.1/0000000 egoscale/v3.1.36 (go1.26.4; linux/amd64)"}},` +
	`"res":{"body":{"message":"not found"}}}
`

func runCoverage(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := coverage(args, &out, &errb)
	return out.String(), errb.String(), code
}

func exoFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "run.jsonl")
	if err := os.WriteFile(path, []byte(exoRecording), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The core of #74, end to end and against the committed contract: a recorded
// call to an operation *no route serves* is named and ranked.
//
// That naming is the whole difficulty. `feint proxy` records an empty operation
// for a declined call, because it names an exchange from the mounted routes —
// so nothing in this repository could put "list-dns-domains" on a `GET
// /v2/dns-domain` until the contract was consulted for it.
func TestADeclinedOperationACallReachedIsNamedAndRanked(t *testing.T) {
	out, errs, code := runCoverage(t,
		"--provider", "exoscale", "--contract", "../../contracts/exoscale.json",
		"--observed", exoFixture(t))
	if code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", code, errs)
	}
	if !strings.Contains(out, "list-dns-domains") {
		t.Fatalf("the declined operation the recording called is not named:\n%s", out)
	}
	if !strings.Contains(out, "      2  exo ") {
		t.Fatalf("the ranking does not carry two calls attributed to exo:\n%s", out)
	}
	if !strings.Contains(out, "declined operation(s) in all") {
		t.Fatalf("the populations are not stated:\n%s", out)
	}
}

// The client column comes from a closed vocabulary, never from the agent.
// A recording is somebody's inventory (docs/proxy.md) and a User-Agent carries
// whatever the build put in it — here, a path.
func TestTheObservedReportNamesNoRawUserAgent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	poisoned := strings.ReplaceAll(exoRecording,
		"exocli/1.95.1/0000000 egoscale/v3.1.36 (go1.26.4; linux/amd64)",
		"exocli/1.95.1 (/home/someone/infra/prod-secrets) build-runner-07")
	if err := os.WriteFile(path, []byte(poisoned), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, code := runCoverage(t,
		"--provider", "exoscale", "--contract", "../../contracts/exoscale.json", "--observed", path)
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	for _, leak := range []string{"/home/someone", "prod-secrets", "build-runner-07", "1.95.1"} {
		if strings.Contains(out, leak) {
			t.Errorf("the report republishes %q out of a User-Agent", leak)
		}
	}
	if !strings.Contains(out, "exo") {
		t.Errorf("the client family is missing entirely, which is not the fix:\n%s", out)
	}
}

// Nothing in a recording reaches the output but a count and a family. The
// identifiers a recording carries are the account's, and this holds the
// rendering to that rather than the intention.
func TestTheObservedReportRepublishesNoIdentifier(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	withIDs := strings.ReplaceAll(exoRecording, "/v2/dns-domain",
		"/v2/dns-domain/44444444-4444-4444-4444-444444444444")
	if err := os.WriteFile(path, []byte(withIDs), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, _ := runCoverage(t,
		"--provider", "exoscale", "--contract", "../../contracts/exoscale.json", "--observed", path)
	if strings.Contains(out, "44444444-4444-4444-4444-444444444444") {
		t.Errorf("the report republishes an identifier from the recording:\n%s", out)
	}
}

// #74's third acceptance criterion, and the one that protects the mechanism
// this repository rests on: `feint coverage` without --observed writes exactly
// what it wrote before, so tools/drift/gate.sh and the generated README tables
// are untouched.
//
// Byte-identical, compared against a run of the same command, because the
// alternative — reading the committed artefact — would pass even if the flag
// had changed the renderer, as long as nobody had regenerated.
func TestCoverageWithoutObservedRendersExactlyWhatItRenderedBefore(t *testing.T) {
	plain, _, code := runCoverage(t, "--provider", "exoscale", "--contract", "../../contracts/exoscale.json", "--format", "json")
	if code != exitOK {
		t.Fatalf("the plain run exited %d", code)
	}
	committed, err := os.ReadFile("../../coverage/exoscale-coverage.json")
	if err != nil {
		t.Fatal(err)
	}
	if plain != string(committed) {
		t.Fatalf("`feint coverage --format json` no longer reproduces the committed artefact; " +
			"the --observed flag must render instead of the report, never beside it")
	}
}

// The rule that keeps the committed artefact safe: --observed renders *instead
// of* the report the --format selects, never beside it.
//
// Asserted on the JSON, because that is where it would do damage: `feint
// coverage --format json` is what writes coverage/<provider>-coverage.json, and
// a flag that appended a second document to it would corrupt the artefact the
// whole drift mechanism rests on. The sibling test proves the plain run is
// unchanged; this one proves the observed run does not carry the report along.
func TestObservedRendersInsteadOfTheReportAndNotBesideIt(t *testing.T) {
	out, errs, code := runCoverage(t,
		"--provider", "exoscale", "--contract", "../../contracts/exoscale.json",
		"--observed", exoFixture(t), "--format", "json")
	if code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", code, errs)
	}
	var observed map[string]any
	if err := json.Unmarshal([]byte(out), &observed); err != nil {
		t.Fatalf("the observed run did not write one JSON document, which is what "+
			"appending the report to it would look like: %v\n%s", err, out)
	}
	if _, has := observed["declined_and_called"]; !has {
		t.Fatalf("the observed document is missing its own ranking:\n%s", out)
	}
	if _, has := observed["entries"]; has {
		t.Fatalf("the observed document carries the coverage report's entries: " +
			"--observed must render instead of the report, never beside it")
	}
}

// --observed without a contract is refused, and the refusal says why rather
// than falling back to something that would rank only what a route already
// names — which is the population this view exists to look past.
func TestObservedWithoutAContractIsRefusedWithItsReason(t *testing.T) {
	_, errs, code := runCoverage(t,
		"--provider", "scaleway", "--sdk", "../drift/testdata/fake-scaleway-sdk", "--observed", exoFixture(t))
	if code != exitError {
		t.Fatalf("exit %d, want %d", code, exitError)
	}
	if !strings.Contains(errs, "--observed needs --contract") {
		t.Fatalf("the refusal does not name what is missing:\n%s", errs)
	}
}

// A directory with no recording in it is an error, not an empty ranking: a
// report drawn from nothing must not read as a report drawn from a clean run.
// This is the same reasoning `feint shapes --check` applies when it prints how
// much it compared.
func TestADirectoryWithNoRecordingIsAnErrorNotAnEmptyRanking(t *testing.T) {
	_, errs, code := runCoverage(t,
		"--provider", "exoscale", "--contract", "../../contracts/exoscale.json",
		"--observed", t.TempDir())
	if code != exitError {
		t.Fatalf("exit %d, want %d", code, exitError)
	}
	if !strings.Contains(errs, "no .jsonl recording") {
		t.Fatalf("the refusal does not say what it found:\n%s", errs)
	}
}
