package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/trace"
)

// The two halves of #851's seed question: the measurement, and the refusal it
// pays for.
//
// #851 asked for campaigns whose decisions replay from a seed, and bound the
// ask in the same breath: the outcome must be a pure function of stable
// inputs, and if no stable request identity exists the CLI must say so rather
// than pretend reproducibility. Whether one exists is a fact about what the
// official clients send, so it is measured here rather than argued.

// theNineHeaderNames is what the committed corpus carries, measured
// 2026-11-13 over its 1088 recorded exchanges.
//
// Written out rather than counted, so that a future recording carrying a tenth
// name fails this test instead of passing it. That failure is the point: a
// client-minted request identity appearing upstream would make a seeded
// campaign possible, and the refusal `feint fault add --seed` prints would
// have to be revisited rather than quietly outlived.
var theNineHeaderNames = []string{
	"accept",
	"accept-encoding",
	"authorization",
	"connection",
	"content-length",
	"content-type",
	"user-agent",
	"x-amz-date",
	"x-auth-token",
}

// TestTheCorpusCarriesNoClientMintedRequestIdentity measures the premise of
// the refusal.
//
// Falsified by adding a tenth name to theNineHeaderNames: the test then fails
// with "the corpus carries no x-request-id", which is the right failure, since
// the list is meant to be what was recorded and not what someone hoped for.
func TestTheCorpusCarriesNoClientMintedRequestIdentity(t *testing.T) {
	root := filepath.Join("..", "..", "corpus")
	var files []string
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".jsonl") {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk the corpus: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no corpus recording was found, so this test measured nothing: " +
			"a verdict drawn from an empty population is the failure this repository names first")
	}

	seen := map[string]bool{}
	exchanges := 0
	for _, file := range files {
		raw, err := os.ReadFile(file) // #nosec G304 -- a committed corpus path this test walked
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var exchange trace.Exchange
			if err := json.Unmarshal([]byte(line), &exchange); err != nil {
				t.Fatalf("decode a line of %s: %v", file, err)
			}
			if exchange.Req == nil {
				continue
			}
			exchanges++
			for name := range exchange.Req.Headers {
				seen[strings.ToLower(name)] = true
			}
		}
	}
	if exchanges == 0 {
		t.Fatal("the corpus files held no request headers, so this test measured nothing")
	}

	got := make([]string, 0, len(seen))
	for name := range seen {
		got = append(got, name)
	}
	sort.Strings(got)
	want := append([]string(nil), theNineHeaderNames...)
	sort.Strings(want)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("the request header names the official clients send have changed over %d exchanges.\n"+
			"measured: %s\nrecorded: %s\n"+
			"If one of the new names is minted by the client per call, a seeded campaign becomes "+
			"possible and the refusal in reportNoSeed must be revisited rather than left standing.",
			exchanges, strings.Join(got, ", "), strings.Join(want, ", "))
	}
}

// TestSeedIsRefusedWithItsMeasurement holds the refusal itself, and holds that
// it carries its reason.
//
// A flag that refused with "unsupported" would teach the next person nothing,
// and they would write a shell loop around arrival order believing it replays.
// So the assertion is not only the exit code: it is that the sentence names
// what was measured and points at --nth.
//
// Falsified by replacing the reportNoSeed call in faultAdd with a plain
// return: the test then fails with "--seed was accepted, exit 0".
func TestSeedIsRefusedWithItsMeasurement(t *testing.T) {
	addr := faultLab(t)

	code, _, errOut := faultRun("fault", "add", faultOpListServers, "--addr", addr,
		"--status", "503", "--seed", "426")
	if code == 0 {
		t.Fatalf("--seed was accepted, exit 0; a seed this emulator cannot honour must be refused\n%s",
			errOut)
	}
	for _, want := range []string{"1088", "nine", "arrival order", "--nth"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("the refusal does not mention %q, so it does not say why it refuses:\n%s",
				want, errOut)
		}
	}

	// And the refusal arms nothing: a rule half-written by a refused command
	// would fire later, against an operator who believes nothing is armed.
	listCode, out, listErr := faultRun("fault", "list", "--addr", addr)
	if listCode != 0 {
		t.Fatalf("list exited %d: %s", listCode, listErr)
	}
	if !strings.Contains(out, "no fault is armed") {
		t.Fatalf("the refused --seed left a rule armed:\n%s", out)
	}
}
