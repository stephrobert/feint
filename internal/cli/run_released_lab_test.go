package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stephrobert/feint/internal/s3"
)

// `feint run --fault` against a released emulator (#968).
//
// Measured on 2026-11-16 against the published v0.16.0 binary
// (feint-linux-amd64, sha256 19bb414b…, checked against the release's
// checksums.txt), which serves /_feint/faults at schema 1:
//
//   - a status rule is accepted, echoed with its status, and its `hits` count;
//   - a rule with `"nth": 2` is accepted with a 200 and echoed WITHOUT nth,
//     and the child met the 503 on calls 1, 2 and 3;
//   - a transport rule is refused with a 400 ("asks for nothing").
//
// The stubs below answer exactly those bodies. One case per question.

// releasedLab is an attached lab answering /_feint/faults as v0.16.0 does: PUT
// echoes `echo`, GET answers `state`, and every DELETE is counted.
type releasedLab struct {
	url     string
	mu      sync.Mutex
	deletes int
	puts    int
	// Set before the run: the status a DELETE or a GET answers (0 means
	// 200), and the session checkpoints taken and removed.
	deleteStatus, getStatus            int
	checkpointPosts, checkpointDeletes int
}

func newReleasedLab(t *testing.T, echo, state string) *releasedLab {
	t.Helper()
	lab := &releasedLab{}
	report := s3.Report{SchemaVersion: s3.ReportVersion, Enabled: true,
		MaxBlobBytes: 1 << 20, MaxObjectBytes: 1 << 10,
		Doors: []s3.Door{door("scaleway", "fr-par")}, HTTPRequests: map[string]uint64{}}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/_feint/checkpoint/") {
			lab.mu.Lock()
			if r.Method == http.MethodDelete {
				lab.checkpointDeletes++
			} else {
				lab.checkpointPosts++
			}
			lab.mu.Unlock()
			_, _ = w.Write([]byte(`{"schema_version":1,"name":"x","resources":0,"bytes":false}`))
			return
		}
		// A real emulator answers the inventory and, with a door mounted, an S3
		// bucket listing in XML. The fake answered its report body on every
		// other path, which was invisible while a failed observation was
		// swallowed (#977); a checkpoint read that cannot be taken now refuses,
		// so the fixture has to answer the reads it is asked.
		if r.URL.Path == "/_feint/resources" {
			_, _ = w.Write([]byte(`{"resources":[]}`))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/_feint/s3/") {
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>` +
				`<ListAllMyBucketsResult><Buckets></Buckets></ListAllMyBucketsResult>`))
			return
		}
		if r.URL.Path != "/_feint/faults" {
			_ = json.NewEncoder(w).Encode(report)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		switch r.Method {
		case http.MethodPut:
			lab.mu.Lock()
			lab.puts++
			lab.mu.Unlock()
			_, _ = w.Write([]byte(echo))
		case http.MethodDelete:
			lab.mu.Lock()
			lab.deletes++
			status := lab.deleteStatus
			lab.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"the lab could not clear its rules"}`))
				return
			}
			_, _ = w.Write([]byte(`{"schema_version":1,"faults":[]}`))
		default:
			lab.mu.Lock()
			status := lab.getStatus
			lab.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"the fault state is unavailable"}`))
				return
			}
			_, _ = w.Write([]byte(state))
		}
	}))
	t.Cleanup(ts.Close)
	lab.url = ts.URL
	return lab
}

func (l *releasedLab) deleted() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.deletes
}

// v0160Rule is a rule as v0.16.0 echoes it: no nth, no transport.
func v0160Rule(status, hits int) string {
	return `{"schema_version":1,"faults":[{"operation":"` + faultOpListServers + `","status":` +
		itoa(status) + `,"delay":"","times":0,"truncate_bytes":null,"hits":` + itoa(hits) + `,"spent":false}]}`
}

func writeCampaign(t *testing.T, rules string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "campaign.json")
	if err := os.WriteFile(path, []byte(`{"faults":[`+rules+`]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestARunRefusesACampaignTheLabArmedDifferently is cited by armFaultCampaign.
//
// The v0.16.0 answer to an nth rule: 200, the rule armed without nth. The run
// must stop before the child, say which knob was lost, and clear what the lab
// armed.
func TestARunRefusesACampaignTheLabArmedDifferently(t *testing.T) {
	lab := newReleasedLab(t, v0160Rule(503, 0), v0160Rule(503, 0))
	campaign := writeCampaign(t, `{"operation":"`+faultOpListServers+`","status":503,"nth":2}`)
	ran := filepath.Join(t.TempDir(), "ran")

	var stdout, stderr bytes.Buffer
	code := runCommand([]string{"--endpoint", lab.url, "--fault", campaign, "--",
		"sh", "-c", `: > "$1"`, "sh", ran}, &stdout, &stderr)
	if code != exitError {
		t.Fatalf("a campaign armed without its nth answered %d, want %d\nstderr:\n%s", code, exitError, stderr.String())
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("the child ran against a campaign the lab armed differently")
	}
	if !strings.Contains(stderr.String(), "without nth") ||
		!strings.Contains(stderr.String(), "schema_version 1") {
		t.Errorf("the refusal does not name the lost knob and the lab's schema:\n%s", stderr.String())
	}
	if lab.deleted() != 1 {
		t.Errorf("the lab was cleared %d time(s) after the refusal, want 1: what it armed would meet the next client",
			lab.deleted())
	}
}

// The accepting half against the same released lab: a status rule comes back
// as it was sent, so the run arms it, runs the child, and does not clear it
// before the receipt is read.
func TestARunArmsAStatusRuleOnAReleasedLab(t *testing.T) {
	lab := newReleasedLab(t, v0160Rule(503, 0), v0160Rule(503, 2))
	campaign := writeCampaign(t, `{"operation":"`+faultOpListServers+`","status":503}`)
	var stdout, stderr bytes.Buffer
	code := runCommand([]string{"--endpoint", lab.url, "--fault", campaign, "--", "sh", "-c", "exit 3"},
		&stdout, &stderr)
	if code != 3 {
		t.Fatalf("a campaign the lab armed as sent answered %d, want the child's 3\nstderr:\n%s", code, stderr.String())
	}
}

// TestARunReceiptAgainstASchema1LabCountsEachRule is cited by renderFaultReceipt
// and runReceipt.FaultRules: schema 1 publishes `hits` per rule, and that is
// the answer the receipt gives, in prose and in JSON.
func TestARunReceiptAgainstASchema1LabCountsEachRule(t *testing.T) {
	lab := newReleasedLab(t, v0160Rule(503, 0), v0160Rule(503, 2))
	campaign := writeCampaign(t, `{"operation":"`+faultOpListServers+`","status":503}`)
	var stdout, stderr bytes.Buffer
	code := runCommand([]string{"--endpoint", lab.url, "--fault", campaign, "--json", "-", "--",
		"sh", "-c", "exit 3"}, &stdout, &stderr)
	if code != 3 {
		t.Fatalf("the run answered %d, want the child's 3\nstderr:\n%s", code, stderr.String())
	}
	if want := "    " + faultOpListServers + ": 2 hit(s)\n"; !strings.Contains(stderr.String(), want) {
		t.Errorf("the prose receipt does not count the rule; want %q in:\n%s", want, stderr.String())
	}
	var receipt struct {
		FaultRules []ruleHits `json:"fault_rules"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
		t.Fatalf("stdout is not the receipt alone: %v\n%s", err, stdout.String())
	}
	if len(receipt.FaultRules) != 1 || receipt.FaultRules[0] != (ruleHits{Operation: faultOpListServers, Hits: 2}) {
		t.Errorf("fault_rules = %+v, want the one rule with 2 hits", receipt.FaultRules)
	}
}

// TestARunArmsEveryKnobOnTheEmulatorItShipsWith is the accepting half of the
// comparison against the real emulator: every knob a campaign can set comes
// back as it was sent, so the comparison refuses nothing this tree serves.
func TestARunArmsEveryKnobOnTheEmulatorItShipsWith(t *testing.T) {
	srv, env, err := newServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	flags := &objectStorageFlags{root: filepath.Join(privateStorageDir(t), "bytes"),
		budgetMiB: 3, objectMiB: 1, doors: defaultS3Doors}
	doors, store, err := mountObjectStorage(t.Context(), srv, flags, env.Log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ts := httptest.NewServer(doors)
	t.Cleanup(ts.Close)

	campaign := writeCampaign(t, strings.Join([]string{
		`{"operation":"instance/v1/API.GetServer","status":503,"nth":2}`,
		`{"operation":"instance/v1/API.ListIPs","transport":"close"}`,
		`{"operation":"instance/v1/API.ListSecurityGroups","status":502,"delay":"1m","times":2}`,
		`{"operation":"instance/v1/API.ListServers","truncate_bytes":10}`,
	}, ","))
	var stdout, stderr bytes.Buffer
	code := runCommand([]string{"--endpoint", ts.URL, "--fault", campaign, "--", "sh", "-c", "exit 3"},
		&stdout, &stderr)
	if code != 3 {
		t.Fatalf("a campaign the emulator of this tree armed as sent answered %d, want the child's 3\nstderr:\n%s",
			code, stderr.String())
	}
}

func (l *releasedLab) armed() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.puts
}

// TestARunSaysTransportNeedsSchema3BeforeArming is cited by
// refuseTransportBelowItsSchema: against a schema 1 lab a transport rule is
// refused by the CLI, in its own words, and nothing is PUT.
func TestARunSaysTransportNeedsSchema3BeforeArming(t *testing.T) {
	lab := newReleasedLab(t, v0160Rule(503, 0), `{"schema_version":1,"faults":[]}`)
	campaign := writeCampaign(t, `{"operation":"`+faultOpListServers+`","transport":"close"}`)
	var stdout, stderr bytes.Buffer
	code := runCommand([]string{"--endpoint", lab.url, "--fault", campaign, "--", "sh", "-c", "exit 3"},
		&stdout, &stderr)
	if code != exitError {
		t.Fatalf("a transport rule against a schema 1 lab answered %d, want %d\nstderr:\n%s", code, exitError, stderr.String())
	}
	if want := `asks for transport "close", which needs /_feint/faults schema_version 3 or later, ` +
		`and this lab serves schema_version 1`; !strings.Contains(stderr.String(), want) {
		t.Errorf("the refusal does not say what transport needs; want %q in:\n%s", want, stderr.String())
	}
	if lab.armed() != 0 {
		t.Errorf("the campaign was PUT %d time(s) before the refusal, want 0", lab.armed())
	}
}

// The bound: a schema 3 lab has transport, so the same rule is armed there.
func TestARunArmsTransportOnASchema3Lab(t *testing.T) {
	echo := `{"schema_version":3,"faults":[{"operation":"` + faultOpListServers +
		`","status":0,"delay":"","times":0,"nth":0,"transport":"close","hits":0,"calls":0}],"fired":[]}`
	lab := newReleasedLab(t, echo, echo)
	campaign := writeCampaign(t, `{"operation":"`+faultOpListServers+`","transport":"close"}`)
	var stdout, stderr bytes.Buffer
	code := runCommand([]string{"--endpoint", lab.url, "--fault", campaign, "--", "sh", "-c", "exit 3"},
		&stdout, &stderr)
	if code != 3 || lab.armed() != 1 {
		t.Fatalf("a transport rule against a schema 3 lab answered %d after %d PUT(s), want the child's 3 after 1\nstderr:\n%s",
			code, lab.armed(), stderr.String())
	}
}

// TestEveryKnobTheLabDidNotKeepIsNamed is cited by knobsNotKept: one case per
// knob, each changing that knob alone in the rule the lab echoes, so a branch
// that stopped comparing is the only case that goes green-wrong.
func TestEveryKnobTheLabDidNotKeepIsNamed(t *testing.T) {
	ten, eleven := 10, 11
	sent := faultRule{Operation: faultOpListServers, Status: 503, Delay: "1m", Times: 2, Nth: 3,
		Truncate: &ten, Transport: "close"}
	for i, probe := range []struct {
		knob   string
		change func(*faultRule)
	}{
		{"its operation", func(r *faultRule) { r.Operation = "instance/v1/API.GetServer" }},
		{"status", func(r *faultRule) { r.Status = 502 }},
		{"delay", func(r *faultRule) { r.Delay = "" }},
		{"times", func(r *faultRule) { r.Times = 0 }},
		{"nth", func(r *faultRule) { r.Nth = 0 }},
		{"truncate_bytes", func(r *faultRule) { r.Truncate = nil }},
		{"truncate_bytes", func(r *faultRule) { r.Truncate = &eleven }},
		{"transport", func(r *faultRule) { r.Transport = "" }},
	} {
		echoed := sent
		probe.change(&echoed)
		t.Run(fmt.Sprintf("%d %s", i, probe.knob), func(t *testing.T) {
			if got := knobsNotKept(sent, echoed); !slices.Equal(got, []string{probe.knob}) {
				t.Errorf("knobsNotKept = %q, want exactly [%q]", got, probe.knob)
			}
		})
	}
	// The accepting half: every knob kept, nothing named; and a knob the
	// campaign left at zero is not compared, which is how v0.16.0 echoes the
	// knobs it does not have.
	if got := knobsNotKept(sent, sent); len(got) != 0 {
		t.Errorf("a rule echoed as sent is said to have lost %q", got)
	}
	bare := faultRule{Operation: faultOpListServers, Status: 503}
	if got := knobsNotKept(bare, faultRule{Operation: faultOpListServers, Status: 503, Delay: "", Hits: 4}); len(got) != 0 {
		t.Errorf("a rule that set only a status is said to have lost %q", got)
	}
}

// runFault runs `feint run --fault` with the campaign against a lab, child
// `exit 3`, extra flags first, and answers the code and stderr.
func runFault(t *testing.T, lab *releasedLab, rules string, extra ...string) (int, string, string) {
	t.Helper()
	args := append([]string{"--endpoint", lab.url, "--fault", writeCampaign(t, rules)}, extra...)
	args = append(args, "--", "sh", "-c", "exit 3")
	var stdout, stderr bytes.Buffer
	code := runCommand(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

const statusRule = `{"operation":"` + faultOpListServers + `","status":503}`

// A 200 that echoes another number of rules has armed them: refused, and
// cleared. Cited by armFaultCampaign.
func TestARunClearsACampaignArmedWithAnotherRuleCount(t *testing.T) {
	lab := newReleasedLab(t, v0160Rule(503, 0), v0160Rule(503, 0))
	code, _, stderr := runFault(t, lab, statusRule+`,{"operation":"instance/v1/API.GetServer","status":503}`)
	if code != exitError || !strings.Contains(stderr, "2 rule(s) were sent and 1 are armed") {
		t.Fatalf("answered %d; want a refusal naming the counts:\n%s", code, stderr)
	}
	if lab.deleted() != 1 {
		t.Errorf("cleared %d time(s), want 1", lab.deleted())
	}
}

// A 200 whose body is not a fault document (a proxy page) has been applied by
// the lab all the same: refused, and cleared.
func TestARunClearsACampaignWhoseAnswerDoesNotDecode(t *testing.T) {
	lab := newReleasedLab(t, `<html>proxy</html>`, v0160Rule(503, 0))
	code, _, stderr := runFault(t, lab, statusRule)
	if code != exitError || !strings.Contains(stderr, "is not a fault document") {
		t.Fatalf("answered %d; want a refusal naming the undecodable answer:\n%s", code, stderr)
	}
	if lab.deleted() != 1 {
		t.Errorf("cleared %d time(s), want 1", lab.deleted())
	}
}

// A clear that fails is said, and never reported as done. Cited by clearArmed.
func TestARefusalWhoseClearFailsSaysSo(t *testing.T) {
	lab := newReleasedLab(t, v0160Rule(503, 0), v0160Rule(503, 0))
	lab.deleteStatus = http.StatusInternalServerError
	code, _, stderr := runFault(t, lab, `{"operation":"`+faultOpListServers+`","status":503,"nth":2}`)
	if code != exitError || !strings.Contains(stderr, "could NOT be cleared") ||
		strings.Contains(stderr, "and cleared what it had armed") {
		t.Errorf("answered %d; a failed clear must read as failed:\n%s", code, stderr)
	}
}

// The transport check reads the whole campaign, not its first rule.
func TestTheTransportCheckNamesTheRuleThatAsks(t *testing.T) {
	lab := newReleasedLab(t, v0160Rule(503, 0), `{"schema_version":1,"faults":[]}`)
	code, _, stderr := runFault(t, lab, statusRule+`,{"operation":"instance/v1/API.ListServers","transport":"close"}`)
	if code != exitError || !strings.Contains(stderr, "rule 2 (instance/v1/API.ListServers) asks for transport") {
		t.Errorf("answered %d; want rule 2 named:\n%s", code, stderr)
	}
	if lab.armed() != 0 {
		t.Errorf("PUT %d time(s) before the refusal, want 0", lab.armed())
	}
}

// A lab that does not name its schema is said to, never as schema_version 0.
func TestTheTransportCheckSaysWhenTheLabNamesNoSchema(t *testing.T) {
	lab := newReleasedLab(t, v0160Rule(503, 0), `{"faults":[]}`)
	code, _, stderr := runFault(t, lab, `{"operation":"`+faultOpListServers+`","transport":"close"}`)
	if code != exitError || !strings.Contains(stderr, "and this lab does not name its schema_version") ||
		strings.Contains(stderr, "schema_version 0") {
		t.Errorf("answered %d; want the lab named as one without a schema:\n%s", code, stderr)
	}
}

// A rule the lab reports spent (a times:2 rule used up, as v0.16.0 answers)
// says so, in prose and in JSON.
func TestARunReceiptSaysARuleIsSpent(t *testing.T) {
	spent := `{"schema_version":1,"faults":[{"operation":"` + faultOpListServers +
		`","status":503,"delay":"","times":2,"truncate_bytes":null,"hits":2,"spent":true}]}`
	lab := newReleasedLab(t, spent, spent)
	code, stdout, stderr := runFault(t, lab, `{"operation":"`+faultOpListServers+`","status":503,"times":2}`, "--json", "-")
	if code != 3 {
		t.Fatalf("answered %d, want the child's 3:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, faultOpListServers+": 2 hit(s), spent\n") {
		t.Errorf("the prose does not say the rule is spent:\n%s", stderr)
	}
	var receipt struct {
		FaultRules []ruleHits `json:"fault_rules"`
	}
	if err := json.Unmarshal([]byte(stdout), &receipt); err != nil {
		t.Fatalf("stdout is not the receipt: %v\n%s", err, stdout)
	}
	if want := (ruleHits{Operation: faultOpListServers, Hits: 2, Spent: true}); len(receipt.FaultRules) != 1 ||
		receipt.FaultRules[0] != want {
		t.Errorf("fault_rules = %+v, want [%+v]", receipt.FaultRules, want)
	}
}

// A state that could not be read publishes no schema: faults_read says why.
func TestAnUnreadFaultStatePublishesNoSchema(t *testing.T) {
	lab := newReleasedLab(t, v0160Rule(503, 0), v0160Rule(503, 0))
	lab.getStatus = http.StatusInternalServerError
	var stdout, stderr bytes.Buffer
	code := runCommand([]string{"--endpoint", lab.url, "--json", "-", "--", "sh", "-c", "exit 3"}, &stdout, &stderr)
	if code != 3 {
		t.Fatalf("answered %d, want the child's 3:\n%s", code, stderr.String())
	}
	var receipt map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
		t.Fatalf("stdout is not the receipt: %v\n%s", err, stdout.String())
	}
	if receipt["faults_read"] != false {
		t.Fatalf("faults_read = %v; the lab answered 500, so this case cannot be judged", receipt["faults_read"])
	}
	if got, present := receipt["faults_schema_version"]; present {
		t.Errorf("an unread state published faults_schema_version %v", got)
	}
}

// A rule armed with a knob the campaign did not ask for is refused too: a
// default 503 on a transport-only rule does not do what the campaign says.
func TestARunRefusesAKnobTheCampaignDidNotAskFor(t *testing.T) {
	echo := `{"schema_version":3,"faults":[{"operation":"` + faultOpListServers +
		`","status":503,"delay":"","times":0,"nth":0,"transport":"close","hits":0,"calls":0}],"fired":[]}`
	lab := newReleasedLab(t, echo, echo)
	code, _, stderr := runFault(t, lab, `{"operation":"`+faultOpListServers+`","transport":"close"}`)
	if code != exitError || !strings.Contains(stderr, "without status") {
		t.Errorf("answered %d; want a refusal naming status:\n%s", code, stderr)
	}
	// A schema 3 lab is not a release up to v0.16.0, so that line stays out.
	if strings.Contains(stderr, "Every release up to v0.16.0") {
		t.Errorf("a schema 3 lab is told about schema 1:\n%s", stderr)
	}
}

// A lab that armed another operation at that rank is named for what it did.
func TestARefusalNamesTheOperationTheLabArmedInstead(t *testing.T) {
	echo := `{"schema_version":1,"faults":[{"operation":"instance/v1/API.GetServer","status":503,` +
		`"delay":"","times":0,"truncate_bytes":null,"hits":0,"spent":false}]}`
	lab := newReleasedLab(t, echo, echo)
	code, _, stderr := runFault(t, lab, statusRule)
	if want := "answered rule 1 (" + faultOpListServers + ") as instance/v1/API.GetServer"; code != exitError ||
		!strings.Contains(stderr, want) {
		t.Errorf("answered %d; want %q in:\n%s", code, want, stderr)
	}
}

// A refused campaign leaves no session checkpoint, on the lab or on disk.
// Cited where run.go drops it.
func TestARefusedCampaignDropsTheSessionCheckpoint(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	lab := newReleasedLab(t, v0160Rule(503, 0), v0160Rule(503, 0))
	code, _, stderr := runFault(t, lab, `{"operation":"`+faultOpListServers+`","status":503,"nth":2}`, "--reset-after")
	if code != exitError {
		t.Fatalf("answered %d, want a refusal:\n%s", code, stderr)
	}
	lab.mu.Lock()
	posts, deletes := lab.checkpointPosts, lab.checkpointDeletes
	lab.mu.Unlock()
	if posts != 1 || deletes != 1 {
		t.Errorf("checkpoint taken %d time(s) and removed %d, want 1 and 1", posts, deletes)
	}
	path, err := checkpointPath("127.0.0.1:0", "feint-run-session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the recorded session checkpoint %s outlived the refused run (stat: %v)", path, err)
	}
}
