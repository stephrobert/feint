package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/s3"
)

// The fault receipt read from an emulator of another build (#928).
//
// The rest of the fault tests drive the emulator this tree serves, never a
// stub, and the reason is written at the top of fault_test.go. These cannot:
// an emulator whose receipt is schema 2 or 3 is not something this tree can
// build, so its answer is served by hand. The bodies are kept to the lines a
// schema 2 or 3 emulator really publishes, under the invariant its own
// schema.go note states: a finished line is `status != 0, abandoned: false` or
// `status: 0, abandoned: true`, and `status: 0, abandoned: false` is a line in
// flight.
//
// One case per question, each passing every other one:
//
//   - a schema 3 line in flight renders in flight          (derivation)
//   - schema 3 finished lines stay finished                (derivation's bound)
//   - a schema 2 receipt is exact, so it carries no note   (#928 cycle 3)
//   - no schema_version: the note, above the lines         (the note)
//   - no schema_version and no line: no note               (only with lines)
//   - schema 1 carrying lines: named                       (below the receipt)
//   - schema 2 exactly, the current one, a newer one: silent
//   - `fault add` prints the note when its answer has lines
//   - the run receipt: note without a schema, none at 2 or at the current one
//   - the run receipt against schema 1: no "none fired", armed by it or not

// faultsNote is the prefix every note starts with.
const faultsNote = "note: this emulator's fault receipt"

// Receipt lines as a schema 2 or 3 emulator publishes them.
const (
	firedInFlight  = `{"operation":"` + faultOpListServers + `","rank":1,"status":0,"abandoned":false}`
	firedAnswered  = `{"operation":"` + faultOpListServers + `","rank":2,"status":503,"abandoned":false}`
	firedAbandoned = `{"operation":"` + faultOpListServers + `","rank":3,"status":0,"abandoned":true}`
)

// faultsBody is a document with one armed rule and the given fired lines.
// version < 0 omits schema_version entirely.
func faultsBody(version int, fired ...string) string {
	schema := ""
	if version >= 0 {
		schema = fmt.Sprintf(`"schema_version":%d,`, version)
	}
	return `{` + schema +
		`"faults":[{"operation":"` + faultOpListServers + `","delay":"1m0s","hits":1,"calls":1}],` +
		`"fired":[` + strings.Join(fired, ",") + `],"fired_dropped":0}`
}

// faultStub serves one fixed /_feint/faults document to GET and to PUT, and
// answers the --addr to reach it on.
func faultStub(t *testing.T, body string) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_feint/faults" || (r.Method != http.MethodGet && r.Method != http.MethodPut) {
			http.NotFound(w, r)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return strings.TrimPrefix(ts.URL, "http://")
}

// listAgainst runs `fault list` and holds what every case asserts: exit 0,
// nothing on stderr, the armed rule still printed.
func listAgainst(t *testing.T, addr string) string {
	t.Helper()
	code, out, errOut := faultRun("fault", "list", "--addr", addr)
	if code != exitOK {
		t.Fatalf("list exited %d, want 0 (an older emulator is served, not refused): %s", code, errOut)
	}
	if errOut != "" {
		t.Errorf("list wrote to stderr; the note belongs with the listing it qualifies:\n%s", errOut)
	}
	if !strings.Contains(out, "  "+faultOpListServers+": delay 1m0s") {
		t.Fatalf("the armed rule is not listed; the case cannot be judged:\n%s", out)
	}
	return out
}

// A schema 3 line in flight says so, though schema 3 has no in_flight key.
//
// Without deriveInFlight this fails with `call 1: answered 0`, a status for a
// request nobody has answered, which is the line #925 removed.
//
// Run at every schema the derivation covers, one subtest each: the schema 2
// subtest is what holds deriveInFlight's lower bound, and since schema 2 is
// exact (no note) that bound is the only thing keeping its lines honest.
// Measured by the coordinator: with the bound at faultsSchemaTransport and the
// test at schema 3 alone, the whole of internal/cli stayed green.
func TestAnOlderReceiptStillSaysWhatIsInFlight(t *testing.T) {
	for version := faultsSchemaInFlightDerivable; version < faultsSchemaInFlight; version++ {
		t.Run(fmt.Sprintf("schema %d", version), func(t *testing.T) {
			out := listAgainst(t, faultStub(t, faultsBody(version, firedInFlight)))
			if !strings.Contains(out, faultOpListServers+" call 1: still in flight, no answer yet") {
				t.Errorf("a schema %d line with status 0 and abandoned false is not rendered in flight:\n%s",
					version, out)
			}
		})
	}
}

// The derivation marks one pair and no other. A finished line keeps its
// status, and a line the client left keeps saying so, at every schema the
// derivation covers.
func TestAnOlderReceiptKeepsItsFinishedLinesFinished(t *testing.T) {
	for version := faultsSchemaInFlightDerivable; version < faultsSchemaInFlight; version++ {
		t.Run(fmt.Sprintf("schema %d", version), func(t *testing.T) {
			out := listAgainst(t, faultStub(t, faultsBody(version, firedAnswered, firedAbandoned)))
			for _, want := range []string{
				faultOpListServers + " call 2: answered 503",
				faultOpListServers + " call 3: the client left before the answer",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("a finished schema %d line is not rendered as finished; want %q:\n%s",
						version, want, out)
				}
			}
			if strings.Contains(out, "in flight") {
				t.Errorf("a finished line is taken for one in flight:\n%s", out)
			}
		})
	}
}

// A schema 2 receipt is exact, and carries no note.
//
// The first version of #928 flagged it, claiming a dropped connection, a held
// connection and a client that gave up all read alike there. They cannot: the
// transport failures arrived with schema 3 (51612e6), and at schema 2
// (b89fd75) faults.go still said they "would need a hook". So `status: 0,
// abandoned: true` at schema 2 is a client that left, and the line says so.
// Fails if faultsSchemaTransport goes back into faultsSchemaRendered.
func TestASchema2ReceiptCarriesNoNote(t *testing.T) {
	if faultsSchemaReceipt >= faultsSchemaTransport {
		t.Fatalf("the case must be a schema with a receipt and without transport; receipt %d, transport %d",
			faultsSchemaReceipt, faultsSchemaTransport)
	}
	out := listAgainst(t, faultStub(t, faultsBody(faultsSchemaReceipt, firedAbandoned)))
	if strings.Contains(out, faultsNote) {
		t.Errorf("a schema %d receipt, which is exact, carries a note:\n%s", faultsSchemaReceipt, out)
	}
	if !strings.Contains(out, faultOpListServers+" call 3: the client left before the answer") {
		t.Errorf("the schema %d line is not rendered:\n%s", faultsSchemaReceipt, out)
	}
}

// An empty receipt has nothing to be inexact about, even one that does not
// name its schema.
func TestAnEmptyReceiptCarriesNoNote(t *testing.T) {
	out := listAgainst(t, faultStub(t, faultsBody(-1)))
	if strings.Contains(out, faultsNote) {
		t.Errorf("a receipt with no fired line carries a note about its lines:\n%s", out)
	}
}

// A document with no schema_version is named as such, under the heading and
// above the lines: not as version 0, and not as the current one, which is how
// a Go struct reads an absent key.
//
// Without the print in reportFaults this fails with "not named": nothing was
// derived from the document, so a request still in progress can read
// `answered 0`, and nothing on the page says why.
func TestAReceiptThatNamesNoSchemaSaysSo(t *testing.T) {
	out := listAgainst(t, faultStub(t, faultsBody(-1, firedAbandoned)))
	want := "fired (1):\n  note: this emulator's fault receipt does not name its schema_version"
	if !strings.Contains(out, want) {
		t.Errorf("a receipt carrying no schema_version is not named as such under its heading:\n%s", out)
	}
	if strings.Count(out, faultsNote) != 1 {
		t.Errorf("want exactly one note, got %d:\n%s", strings.Count(out, faultsNote), out)
	}
	if strings.Contains(out, "schema_version 0") {
		t.Errorf("a missing schema is printed as if the emulator had said 0:\n%s", out)
	}
	// A negative version is no schema either: it reads like the missing key,
	// never as "says schema_version -1".
	if faultsSchemaNote(-1) != faultsSchemaNote(0) {
		t.Errorf("a negative schema_version is not read like a missing one: %q", faultsSchemaNote(-1))
	}
}

// A schema below the receipt that still carries lines is named. No emulator of
// this repository publishes one, since schema 1 has no receipt; the case is
// the branch that keeps such a document from passing as exact.
func TestALineUnderASchemaWithoutAReceiptIsNamed(t *testing.T) {
	out := listAgainst(t, faultStub(t, faultsBody(faultsSchemaReceipt-1, firedAbandoned)))
	want := fmt.Sprintf("  note: this emulator's fault receipt says schema_version %d, which has no per-call receipt",
		faultsSchemaReceipt-1)
	if !strings.Contains(out, want) {
		t.Errorf("fired lines under schema %d are not named; want %q in:\n%s", faultsSchemaReceipt-1, want, out)
	}
}

// The first schema the rendering is exact for carries no note: the boundary,
// separate from the current schema so that it still holds the day
// FaultsSchemaVersion moves on.
func TestTheFirstExactSchemaCarriesNoNote(t *testing.T) {
	out := listAgainst(t, faultStub(t, faultsBody(faultsSchemaRendered, firedAbandoned)))
	if strings.Contains(out, faultsNote) {
		t.Errorf("schema %d carries every field this CLI renders, and is flagged:\n%s", faultsSchemaRendered, out)
	}
}

// The schema this tree serves carries no note. The accepting half: a check
// that flagged every receipt would pass every case that expects a note.
func TestTheCurrentSchemaCarriesNoNote(t *testing.T) {
	if faultsSchemaRendered > emulator.FaultsSchemaVersion {
		t.Fatalf("the CLI needs faults schema %d to render exactly, and the emulator it ships with serves %d",
			faultsSchemaRendered, emulator.FaultsSchemaVersion)
	}
	out := listAgainst(t, faultStub(t, faultsBody(emulator.FaultsSchemaVersion, firedAbandoned)))
	if strings.Contains(out, faultsNote) {
		t.Errorf("the schema this tree serves (%d) is flagged:\n%s", emulator.FaultsSchemaVersion, out)
	}
}

// A newer schema carries no note: versioning is additive, so it still carries
// every field this CLI reads.
func TestANewerSchemaCarriesNoNote(t *testing.T) {
	newer := emulator.FaultsSchemaVersion + 1
	out := listAgainst(t, faultStub(t, faultsBody(newer, firedAbandoned)))
	if strings.Contains(out, faultsNote) {
		t.Errorf("schema %d, newer than any this CLI knows, is flagged:\n%s", newer, out)
	}
}

// `fault add` prints the same listing, so the same note when its answer has
// lines. A real emulator's PUT clears the receipt, so this is the rare case,
// served by hand.
func TestAddCarriesTheNoteWhenItsAnswerHasLines(t *testing.T) {
	code, out, errOut := faultRun("fault", "add", faultOpGetServer, "--status", "503",
		"--addr", faultStub(t, faultsBody(-1, firedAbandoned)))
	if code != exitOK {
		t.Fatalf("add exited %d, want 0: %s", code, errOut)
	}
	if !strings.Contains(out, "  "+faultsSchemaNote(0)+"\n") {
		t.Errorf("add's listing of a receipt that names no schema carries no note:\n%s", out)
	}
}

// labStub is an attached lab: the S3 report reportServer serves on every
// path, except /_feint/faults, which answers body. The run reads the receipt
// from there once its child has exited.
func labStub(t *testing.T, body string) string {
	t.Helper()
	report := s3.Report{SchemaVersion: s3.ReportVersion, Enabled: true,
		MaxBlobBytes: 1 << 20, MaxObjectBytes: 1 << 10,
		Doors: []s3.Door{door("scaleway", "fr-par")}, HTTPRequests: map[string]uint64{}}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/_feint/faults" {
			_, _ = w.Write([]byte(body))
			return
		}
		if err := json.NewEncoder(w).Encode(report); err != nil {
			t.Errorf("encode report: %v", err)
		}
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

// runReceiptOf attaches `feint run` to a lab and answers the prose receipt,
// holding that the child's code is the run's whatever the receipt says.
func runReceiptOf(t *testing.T, endpoint string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runCommand([]string{"--endpoint", endpoint, "--", "sh", "-c", "exit 3"}, &stdout, &stderr)
	if code != 3 {
		t.Fatalf("the run answered %d where the child answered 3 (the receipt must not move it)\nstderr:\n%s",
			code, stderr.String())
	}
	return stderr.String()
}

// runAgainst is runReceiptOf for a lab whose receipt has one fired line, and
// holds that the line is still printed.
func runAgainst(t *testing.T, endpoint string) string {
	t.Helper()
	receipt := runReceiptOf(t, endpoint)
	if !strings.Contains(receipt, "faults         1 fired") ||
		!strings.Contains(receipt, faultOpListServers+" call 3: the client left before the answer") {
		t.Fatalf("the receipt no longer prints the fired line; the case cannot be judged:\n%s", receipt)
	}
	return receipt
}

// `feint run` renders the same lines, so it owes the same note, from the same
// function, right above them in its faults section.
//
// Without the print in renderFaultReceipt this fails with "says nothing": the
// receipt is written once, after the child exits, and nobody can go back.
func TestARunReceiptSaysWhenTheLabNamesNoSchema(t *testing.T) {
	receipt := runAgainst(t, labStub(t, faultsBody(-1, firedAbandoned)))

	want := "    " + faultsSchemaNote(0) + "\n    " + faultOpListServers + " call 3:"
	if !strings.Contains(receipt, want) {
		t.Errorf("the run receipt says nothing about a lab that names no schema; want the note above the line:\n%s",
			receipt)
	}
	if strings.Count(receipt, faultsNote) != 1 {
		t.Errorf("want exactly one note, got %d:\n%s", strings.Count(receipt, faultsNote), receipt)
	}
}

// A schema 2 lab gives an exact receipt, so the run prints no note. It is also
// the case that sees the schema lost between the endpoint and the receipt: the
// run would then name the lab as one that does not name its schema.
func TestARunReceiptAgainstASchema2LabCarriesNoNote(t *testing.T) {
	receipt := runAgainst(t, labStub(t, faultsBody(faultsSchemaReceipt, firedAbandoned)))
	if strings.Contains(receipt, faultsNote) {
		t.Errorf("a schema %d lab, whose receipt is exact, is flagged:\n%s", faultsSchemaReceipt, receipt)
	}
}

// The accepting half for the run: a lab at the schema this tree serves gives
// an exact receipt, and it carries no note.
func TestARunReceiptAgainstACurrentLabCarriesNoNote(t *testing.T) {
	receipt := runAgainst(t, labStub(t, faultsBody(emulator.FaultsSchemaVersion, firedAbandoned)))
	if strings.Contains(receipt, faultsNote) {
		t.Errorf("a lab at schema %d is flagged:\n%s", emulator.FaultsSchemaVersion, receipt)
	}
}

// schema1Body is what every release up to v0.16.0 answers: one armed rule
// with its hit count, and no `fired` key at all, because schema 1 has no
// receipt. version < 0 omits schema_version too.
func schema1Body(version int) string {
	schema := ""
	if version >= 0 {
		schema = fmt.Sprintf(`"schema_version":%d,`, version)
	}
	return `{` + schema + `"faults":[{"operation":"` + faultOpListServers +
		`","status":0,"delay":"1m0s","times":0,"truncate_bytes":null,"hits":0,"spent":false}]}`
}

// noneFired is the verdict only a lab that publishes a receipt can earn.
const noneFired = "none fired: no call reached the operations named"

// Against a lab with no receipt, an empty `fired` is not "none fired".
//
// Without the branch in renderFaultReceipt this fails with the run claiming
// that no call reached the operations named, against every released emulator,
// from a field those emulators do not publish.
func TestARunReceiptAgainstALabWithoutAReceiptClaimsNothing(t *testing.T) {
	receipt := runReceiptOf(t, labStub(t, schema1Body(faultsSchemaReceipt-1)))
	if strings.Contains(receipt, noneFired) {
		t.Errorf("a schema %d lab, which publishes no receipt, is reported as one where nothing fired:\n%s",
			faultsSchemaReceipt-1, receipt)
	}
	want := fmt.Sprintf("this lab publishes no per-call receipt (schema_version %d), only a count per rule",
		faultsSchemaReceipt-1)
	if !strings.Contains(receipt, want) {
		t.Errorf("the receipt does not say what the lab does not publish; want %q in:\n%s", want, receipt)
	}
}

// A document with no schema_version promises no receipt either, and is named
// in its own words rather than as `schema_version 0`.
func TestARunReceiptThatNamesNoSchemaClaimsNothing(t *testing.T) {
	receipt := runReceiptOf(t, labStub(t, schema1Body(-1)))
	if strings.Contains(receipt, noneFired) {
		t.Errorf("a lab whose document names no schema is reported as one where nothing fired:\n%s", receipt)
	}
	if !strings.Contains(receipt, "publishes no per-call receipt (it does not name its schema_version)") {
		t.Errorf("the receipt does not say the lab names no schema:\n%s", receipt)
	}
}

// From faultsSchemaReceipt on, an empty receipt is evidence, and "none fired"
// stays: the verdict is refused where it is unfounded, not everywhere.
func TestARunReceiptWithAnEmptyReceiptStillSaysNoneFired(t *testing.T) {
	for _, version := range []int{faultsSchemaReceipt, emulator.FaultsSchemaVersion} {
		t.Run(fmt.Sprintf("schema %d", version), func(t *testing.T) {
			receipt := runReceiptOf(t, labStub(t, faultsBody(version)))
			if !strings.Contains(receipt, "faults         the lab had a campaign armed, and "+noneFired) {
				t.Errorf("a schema %d lab with an empty receipt no longer says none fired:\n%s", version, receipt)
			}
			if strings.Contains(receipt, "per-call receipt") {
				t.Errorf("a schema %d lab is said to publish no receipt:\n%s", version, receipt)
			}
		})
	}
}

// `fault list` against a lab with no receipt claims nothing about firings: it
// prints the armed rule with its hit count, which schema 1 does publish, and
// no receipt heading and no note, since there is no line to qualify.
func TestListAgainstALabWithoutAReceiptClaimsNothing(t *testing.T) {
	out := listAgainst(t, faultStub(t, schema1Body(faultsSchemaReceipt-1)))
	if strings.Contains(out, "fired") || strings.Contains(out, faultsNote) {
		t.Errorf("a lab that publishes no receipt is read as having one:\n%s", out)
	}
	if !strings.Contains(out, "0 hit(s) of 0 call(s)") {
		t.Errorf("the rule's own counter is not printed:\n%s", out)
	}
}

// The same refusal when the campaign is this run's own: `feint run --fault`
// against a released emulator arms its rules and then cannot read whether they
// fired, and must say so in the "armed" wording rather than claim none did.
func TestARunWhoseOwnCampaignMeetsALabWithoutAReceiptClaimsNothing(t *testing.T) {
	campaign := filepath.Join(t.TempDir(), "campaign.json")
	if err := os.WriteFile(campaign, []byte(`{"faults":[{"operation":"`+faultOpListServers+
		`","status":503}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// The lab echoes the rule it was sent, as v0.16.0 does for a status rule
	// (measured 2026-11-16): a stub echoing another rule would be refused at
	// arming since #968, and this case would never reach the receipt.
	armed := `{"schema_version":1,"faults":[{"operation":"` + faultOpListServers +
		`","status":503,"delay":"","times":0,"truncate_bytes":null,"hits":2,"spent":false}]}`
	var stdout, stderr bytes.Buffer
	code := runCommand([]string{"--endpoint", labStub(t, armed),
		"--fault", campaign, "--", "sh", "-c", "exit 3"}, &stdout, &stderr)
	if code != 3 {
		t.Fatalf("the run answered %d where the child answered 3\nstderr:\n%s", code, stderr.String())
	}
	receipt := stderr.String()
	want := fmt.Sprintf("faults         armed, but this lab publishes no per-call receipt (schema_version %d)",
		faultsSchemaReceipt-1)
	if !strings.Contains(receipt, want) || strings.Contains(receipt, noneFired) {
		t.Errorf("a campaign this run armed on a lab without a receipt is not reported as unreadable; want %q in:\n%s",
			want, receipt)
	}
}
