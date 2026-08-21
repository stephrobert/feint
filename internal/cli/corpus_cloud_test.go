package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/proxy"
)

// The two directions of one comparison meet here (#359).
//
// Everything below runs offline: the "cloud" is a second emulator on loopback,
// which is exactly what makes the falsification possible at all — a control
// whose proof needed an account would be a control nobody replays. What the
// stand-in cannot prove is what a *provider* answers, and no test can: that is
// what the run against the real Scaleway account is for, and its result belongs
// in the pull request rather than in an assertion.

// A mutated corpus makes the run report the change and exit non-zero; the same
// corpus unmutated reports nothing and exits 0. #359's own falsification, both
// halves, because a control that fires on everything is not a control.
func TestAMutatedCorpusIsReportedAsACloudChangeAndAnUnchangedOneIsNot(t *testing.T) {
	dir, accepted, _ := vpcCorpusFixture(t)

	cloud, _ := recordingCloud(t)
	out, errs, code := runCloudReplay(t, dir, accepted, cloud.URL, nil)
	if code != exitOK {
		t.Fatalf("an unmutated corpus exited %d, want 0.\nstdout:\n%s\nstderr:\n%s", code, out, errs)
	}
	if !strings.Contains(out, "0 finding(s) say the cloud answers differently now") {
		t.Fatalf("an unmutated corpus reported a change:\n%s", out)
	}
	// The subject, asserted rather than hoped for: a run that compared nothing
	// would also report no change, and this whole direction exists because
	// those two read identically.
	if strings.Contains(out, "exchange(s) in the file, 0 compared") {
		t.Fatalf("the run compared nothing, so its silence measures nothing:\n%s", out)
	}

	operation := mutateStatus(t, filepath.Join(dir, "self/self.jsonl"), 200, 418)
	second, _ := recordingCloud(t)
	out, errs, code = runCloudReplay(t, dir, accepted, second.URL, nil)
	if code != exitDrift {
		t.Fatalf("a mutated corpus exited %d, want %d.\nstdout:\n%s\nstderr:\n%s", code, exitDrift, out, errs)
	}
	if !strings.Contains(out, operation) || !strings.Contains(out, "the cloud answers differently") {
		t.Fatalf("the run does not name %s as answering differently:\n%s", operation, out)
	}
	// Actionable without a transcript: the operation, the path, the kind of
	// change, and the date the recording was made.
	if !strings.Contains(out, "expected 418") || !strings.Contains(out, "recorded 2026-08-21") {
		t.Fatalf("the finding does not carry the kind of change and the recording's date:\n%s", out)
	}
}

// A recorded DELETE addressing an object this run did not create is refused, and
// the object is still there afterwards.
//
// This is "bien formé n'est pas autorisé" on the one path where getting it wrong
// destroys somebody's property: the request is well formed, its identifier is a
// real one of the account, and none of that makes it ours. The second assertion
// is the one that matters — without the guard the VPC is gone, and a test that
// only read the exit code would not notice.
func TestADeleteOfSomethingThisRunDidNotCreateIsRefused(t *testing.T) {
	cloud, _ := recordingCloud(t)
	existing := createVPC(t, cloud.URL, "somebody-elses-vpc")

	path := "/vpc/v2/regions/fr-par/vpcs/" + existing
	dir, accepted := cloudCorpusFixture(t, exchangeLine(t, 1, "DELETE", path, "", 204, nil, nil))

	out, errs, code := runCloudReplay(t, dir, accepted, cloud.URL, nil)
	if code != exitError {
		t.Fatalf("a delete of a foreign object exited %d, want %d.\nstdout:\n%s\nstderr:\n%s", code, exitError, out, errs)
	}
	if !strings.Contains(out, "did not create") {
		t.Fatalf("the run does not say why the delete was refused:\n%s", out)
	}
	if !strings.Contains(out, "the call could not be made") {
		t.Fatalf("a refusal is not reported as verdict three:\n%s", out)
	}
	if status := statusOf(t, cloud.URL+path); status != http.StatusOK {
		t.Fatalf("the VPC this run did not create answers %d after the run: the guard let the delete through", status)
	}
}

// A create of something that is billed is refused before it is sent, and the
// report names the operation so a reader learns which measurement is out of
// reach without spending.
//
// The account rules of #352 are not negotiable and are not a comment: free
// resources only. What proves it is the second assertion — no server exists at
// the endpoint afterwards.
func TestABillableCreateIsRefusedRatherThanSent(t *testing.T) {
	cloud, seen := recordingCloud(t)
	body := map[string]any{"name": "expensive", "commercial_type": "DEV1-S", "image": "ubuntu_jammy"}
	dir, accepted := cloudCorpusFixture(t, exchangeLine(t, 1, "POST",
		"/instance/v1/zones/fr-par-1/servers", "", 201, body, map[string]any{"server": map[string]any{"id": "x"}}))

	out, errs, code := runCloudReplay(t, dir, accepted, cloud.URL, nil)
	if code != exitError {
		t.Fatalf("a billable create exited %d, want %d.\nstdout:\n%s\nstderr:\n%s", code, exitError, out, errs)
	}
	if !strings.Contains(out, "instance/v1/API.CreateServer") || !strings.Contains(out, "free-to-create") {
		t.Fatalf("the run does not name the operation it refused to bill for:\n%s", out)
	}
	for _, r := range seen() {
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/servers") {
			t.Fatalf("the create reached the endpoint: %s %s", r.Method, r.Path)
		}
	}
}

// A path the sanitiser blanked is never sent, and is reported as a defect of the
// recording rather than as a cloud that changed.
//
// `GET /redacted-1/redacted-2` addresses no operation of any cloud. Sending it
// would spend a real call to collect a 404 the instrument caused — which is the
// family #73 measured as nine false divergences.
func TestABlankedPathIsNeverSentToTheCloud(t *testing.T) {
	cloud, seen := recordingCloud(t)
	dir, accepted := cloudCorpusFixture(t,
		exchangeLine(t, 1, "GET", "/redacted-1/redacted-2/redacted-3", "", 200, nil, map[string]any{"x": "y"}),
		exchangeLine(t, 2, "GET", "/vpc/v2/regions/fr-par/vpcs", "order_by=created_at_asc&page=1", 200,
			nil, map[string]any{"vpcs": []any{}, "total_count": 0}))

	out, errs, code := runCloudReplay(t, dir, accepted, cloud.URL, nil)
	if code != exitOK {
		t.Fatalf("the run exited %d, want 0.\nstdout:\n%s\nstderr:\n%s", code, out, errs)
	}
	for _, r := range seen() {
		if strings.Contains(r.Path, "redacted-") {
			t.Fatalf("a blanked path reached the endpoint: %s", r.Path)
		}
	}
	if !strings.Contains(out, "the recording could not be reissued as recorded") {
		t.Fatalf("the blanked path is not reported as a defect of the recording:\n%s", out)
	}
	if !strings.Contains(out, "1 finding(s) are the recording rather than the cloud") {
		t.Fatalf("the blanked path is counted somewhere other than as the recording's own defect:\n%s", out)
	}
}

// An answer that is about the caller — 401, 403, 429, a gateway error — is
// verdict three and never verdict one. A run that read a 401 as "the cloud
// changed" would open a pull request every time a token expired.
func TestAnAnswerAboutTheCallerIsNotACloudChange(t *testing.T) {
	for name, status := range map[string]int{"unauthorised": 401, "rate limited": 429, "gateway": 502} {
		t.Run(name, func(t *testing.T) {
			cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"message":"no"}`))
			}))
			t.Cleanup(cloud.Close)
			dir, accepted := cloudCorpusFixture(t, exchangeLine(t, 1, "GET",
				"/vpc/v2/regions/fr-par/vpcs", "page=1", 200, nil, map[string]any{"vpcs": []any{}, "total_count": 0}))

			out, errs, code := runCloudReplay(t, dir, accepted, cloud.URL, nil)
			if code != exitError {
				t.Fatalf("a %d exited %d, want %d.\nstdout:\n%s\nstderr:\n%s", status, code, exitError, out, errs)
			}
			if strings.Contains(out, "1 finding(s) say the cloud answers differently") {
				t.Fatalf("a %d was graded as a change of the cloud:\n%s", status, out)
			}
			if !strings.Contains(out, "about this caller rather than about the resource") {
				t.Fatalf("a %d is not reported as a call that could not be made:\n%s", status, out)
			}
		})
	}
}

// A field a pack declines is still a finding at the cloud.
//
// The declines excuse what *this emulator* does not serve. Excusing them here
// would hide the day the provider stops answering the field, which is the drift
// this direction exists to catch, and it is one line away: passing `declined` to
// the run would do it.
func TestAPacksDeclineDoesNotExcuseTheCloud(t *testing.T) {
	recorded := map[string]any{"servers": map[string]any{
		"DEV1-S": map[string]any{"per_volume_constraint": map[string]any{
			"l_ssd": map[string]any{"min_size": 1, "max_size": 2}}}}}
	line := exchangeLine(t, 1, "GET", "/instance/v1/zones/fr-par-1/products/servers", "page=1", 200, nil, recorded)

	// Against the emulator, the pack's own decline excuses it and the gate is
	// green. Both halves are asserted, because "it is reported at the cloud" is
	// only interesting beside "it is excused at the emulator".
	gateDir, gateAccepted := cloudCorpusFixture(t, line)
	// The subject is asserted, not the exit code. A one-exchange catalogue
	// fixture reaches no operation carrying an invariant, so #343's guard
	// makes the whole run exitError for a reason that has nothing to do with
	// this decline; reading the code alone would have this test measure that
	// guard instead of the excuse it is about.
	gate, _, _ := runCorpusGate(t, gateDir, gateAccepted)
	if !strings.Contains(gate, "\n0 divergent finding(s) nothing accepts") {
		t.Fatalf("the declined field is not excused at the emulator, so this fixture no longer isolates the decline:\n%s", gate)
	}

	cloud, _ := recordingCloud(t)
	out, _, code := runCloudReplay(t, gateDir, gateAccepted, cloud.URL, nil)
	if code != exitDrift {
		t.Fatalf("a declined field was excused at the cloud (exit %d):\n%s", code, out)
	}
	if !strings.Contains(out, "l_ssd") {
		t.Fatalf("the run does not name the field the cloud carries and this emulator omits:\n%s", out)
	}
}

// Everything this run creates carries a name a human scanning a console
// recognises, and every object it created is destroyed with the destruction
// proved by a read.
func TestEveryObjectThisRunCreatesIsNamedForItAndIsDestroyed(t *testing.T) {
	dir, accepted, _ := vpcCorpusFixture(t)
	cloud, seen := recordingCloud(t)

	out, errs, code := runCloudReplay(t, dir, accepted, cloud.URL, nil)
	if code != exitOK {
		t.Fatalf("the lifecycle exited %d.\nstdout:\n%s\nstderr:\n%s", code, out, errs)
	}

	creates := 0
	for _, r := range seen() {
		if r.Method != http.MethodPost {
			continue
		}
		creates++
		var body map[string]any
		if err := json.Unmarshal(r.Body, &body); err != nil {
			t.Fatalf("decode a create this run sent: %v", err)
		}
		name, _ := body["name"].(string)
		if !strings.HasPrefix(name, thisRunPrefix) {
			t.Errorf("this run created an object named %q, which nobody scanning a console can attribute to it", name)
		}
	}
	if creates == 0 {
		t.Fatal("the fixture created nothing, so it proves nothing about what a run leaves behind")
	}
	if !strings.Contains(out, "destroyed with the destruction proved by a read answering 404") {
		t.Fatalf("the run does not prove what it destroyed:\n%s", out)
	}
	if strings.Contains(errs, "NOT DESTROYED") {
		t.Fatalf("the run left something behind:\n%s", errs)
	}
	// The account is as it was found. The two lists compared, not the exit code
	// of a delete: #352's rule, and the reason it is one.
	if left := vpcCount(t, cloud.URL); left != 1 {
		t.Fatalf("%d VPC(s) at the endpoint after the run, want the 1 the emulator mints by default", left)
	}
}

// A destruction is proved by a read, never by the delete's own answer — and an
// object this run could not destroy makes the run fail.
//
// #352's rule, and the reason it is one: a delete that answers 204 on a resource
// that is still there proves nothing. The stand-in cloud below answers every
// DELETE with 204 and deletes nothing, which is the exact shape a provider's
// asynchronous delete has, and the run has to notice.
func TestADestructionIsProvedByAReadAndNotByTheDeletesOwnAnswer(t *testing.T) {
	honest, _ := recordingCloud(t)
	// A corpus that creates and never deletes: the ledger is the only thing
	// that can empty the account.
	create := exchangeLine(t, 1, "POST", "/vpc/v2/regions/fr-par/vpcs", "",
		200, map[string]any{"name": "left-behind"}, map[string]any{"id": "11111111-1111-4111-8111-111111111111"})
	dir, accepted := cloudCorpusFixture(t, create)

	if _, _, code := runCloudReplay(t, dir, accepted, honest.URL, nil); code != exitOK {
		t.Fatalf("a create the recording never deletes exited %d, want 0", code)
	}
	if left := vpcCount(t, honest.URL); left != 1 {
		t.Fatalf("%d VPC(s) after the run: the ledger did not empty the account", left)
	}

	// The same run against an endpoint that swallows deletes.
	swallowing, inner := swallowingCloud(t)
	dir, accepted = cloudCorpusFixture(t, create)
	out, errs, code := runCloudReplay(t, dir, accepted, swallowing.URL, nil)
	if code == exitOK {
		t.Fatalf("a run that left an object behind exited 0.\nstdout:\n%s\nstderr:\n%s", out, errs)
	}
	if !strings.Contains(errs, "NOT DESTROYED") {
		t.Fatalf("the run does not name what it left behind:\n%s", errs)
	}
	if left := vpcCount(t, inner); left != 2 {
		t.Fatalf("this test no longer reproduces a swallowed delete: %d VPC(s) at the endpoint", left)
	}
}

// A value the recorder wrote REDACTED over is never sent, and is reported as a
// defect of the instrument.
//
// #73's family, exactly: reissuing such a request sends a string the client
// never sent, the create answers 400, and every read and delete that follows
// answers 404 for that one reason — nine false divergences from one
// substitution.
func TestARedactedRequestIsNeverSentToTheCloud(t *testing.T) {
	cloud, seen := recordingCloud(t)
	dir, accepted := cloudCorpusFixture(t,
		exchangeLine(t, 1, "POST", "/iam/v1alpha1/ssh-keys", "", 200,
			map[string]any{"name": "k", "public_key": "REDACTED"}, map[string]any{"id": "x"}),
		exchangeLine(t, 2, "GET", "/vpc/v2/regions/fr-par/vpcs", "page=1", 200,
			nil, map[string]any{"vpcs": []any{}, "total_count": 0}))

	out, errs, code := runCloudReplay(t, dir, accepted, cloud.URL, nil)
	if code != exitOK {
		t.Fatalf("the run exited %d, want 0.\nstdout:\n%s\nstderr:\n%s", code, out, errs)
	}
	for _, r := range seen() {
		if bytes.Contains(r.Body, []byte("REDACTED")) {
			t.Fatalf("a request carrying the recorder's placeholder reached the endpoint: %s %s", r.Method, r.Path)
		}
	}
	if !strings.Contains(out, "the recording could not be reissued as recorded") {
		t.Fatalf("the redacted request is not reported as a defect of the recording:\n%s", out)
	}
	if strings.Contains(out, "1 finding(s) say the cloud answers differently") {
		t.Fatalf("the recorder's own substitution was graded as a change of the cloud:\n%s", out)
	}
}

// A read whose path still carries the sanitiser's own identifier is not a cloud
// that changed.
//
// A corpus is a causal sequence, and when the create it opens on does not happen
// — refused as billable, refused as somebody else's, or not sent at all under
// --dry-run — nothing rebinds the identifier the read addresses. The read then
// answers 404 and every recorded field reads as absent. Measured on 2026-08-21,
// dry-running corpus/scaleway/terraform.jsonl at the real Scaleway account: 145
// findings, not one of them the provider's.
func TestARequestStillCarryingASyntheticIdentifierIsNotACloudChange(t *testing.T) {
	cloud, _ := recordingCloud(t)
	synthetic := "00000000-0000-4000-8000-000000000002"
	dir, accepted := cloudCorpusFixture(t, exchangeLine(t, 1, "GET",
		"/vpc/v2/regions/fr-par/vpcs/"+synthetic, "", 200,
		nil, map[string]any{"id": synthetic, "name": "n", "tags": []any{}}))

	out, errs, code := runCloudReplay(t, dir, accepted, cloud.URL, nil)
	if code == exitDrift {
		t.Fatalf("a read of an identifier the sanitiser invented was graded as a change of the cloud.\nstdout:\n%s\nstderr:\n%s", out, errs)
	}
	if !strings.Contains(out, "the sanitiser invented") {
		t.Fatalf("the report does not say the recording is the likelier cause:\n%s", out)
	}
	if !strings.Contains(out, "0 finding(s) say the cloud answers differently now") {
		t.Fatalf("the finding was counted against the cloud:\n%s", out)
	}
}

// The two directions talk to each other: a run that finds the cloud has moved
// writes the measurement into the acceptance file, and `corpus --check` then
// warns with a measured date instead of a chosen horizon — which is #353's own
// open question answered by measurement.
//
// Three assertions: the measurement lands, the file still reads back through the
// gate's own reader, and the gate warns without failing. The last is the one
// that matters — a gate that failed because somebody else's cloud moved is a
// gate that gets disabled, taking all of its coverage with it.
func TestACorpusTheCloudHasMovedUnderWarnsAndDoesNotFail(t *testing.T) {
	dir, accepted, _ := vpcCorpusFixture(t)
	operation := mutateStatus(t, filepath.Join(dir, "self/self.jsonl"), 200, 418)
	cloud, _ := recordingCloud(t)

	req := cloudReplayRequest{dir: dir, accepted: accepted, file: "self/self.jsonl",
		endpoint: cloud.URL, format: "text", timeout: 30 * time.Second, markStale: true}
	var out, errb bytes.Buffer
	if code := replayCorpusAtCloud(req, corpusNow, &out, &errb); code != exitDrift {
		t.Fatalf("the run exited %d, want %d.\nstdout:\n%s\nstderr:\n%s", code, exitDrift, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "records in") {
		t.Fatalf("the run does not say it wrote the measurement back:\n%s", out.String())
	}

	acc, err := readCorpusAcceptance(accepted)
	if err != nil {
		t.Fatalf("the acceptance file this run wrote cannot be read back: %v", err)
	}
	if len(acc.Recorded) != 1 || acc.Recorded[0].CloudMovedAt != "2026-08-21" || acc.Recorded[0].CloudMoved == 0 {
		t.Fatalf("the measurement did not land: %+v", acc.Recorded)
	}
	if acc.WarnAfterDays != 180 {
		t.Fatalf("writing the measurement lost the rest of the file: warn_after_days is %d", acc.WarnAfterDays)
	}

	// And the emulator gate says so, on every run, without failing.
	gate, _, code := runCorpusGate(t, dir, accepted)
	if code != exitDrift {
		// The mutated status makes the emulator gate red too, which is right and
		// is not what this test is about: what it asserts is the warning.
		t.Logf("the emulator gate exited %d on the same mutated corpus", code)
	}
	if !strings.Contains(gate, "had moved: re-record it") {
		t.Fatalf("the emulator gate does not warn that the cloud has moved under this corpus:\n%s", gate)
	}
	_ = operation
}

// Marking a corpus stale writes the measurement and nothing else.
//
// Run against the committed acceptance file, not a fixture, because that file is
// prose as much as data: its "comment" array carries the argument for every
// exemption in it. A writer that reflowed or reordered it would make the diff of
// a scheduled job unreadable, which is the same as making it unreviewed.
func TestMarkingACorpusStaleWritesTheMeasurementAndNothingElse(t *testing.T) {
	original, err := os.ReadFile("../../corpus/accepted.json")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "accepted.json")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := markCorpusStale(path, "scaleway/terraform.jsonl", corpusNow, 3); err != nil {
		t.Fatalf("mark the committed acceptance file: %v", err)
	}

	before, after := map[string]any{}, map[string]any{}
	if err := json.Unmarshal(original, &before); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path) //nolint:gosec // a file this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(written, &after); err != nil {
		t.Fatalf("the acceptance file this run wrote cannot be parsed: %v", err)
	}
	for _, key := range []string{"comment", "warn_after_days", "accepted"} {
		if fmt.Sprint(before[key]) != fmt.Sprint(after[key]) {
			t.Fatalf("marking one recording rewrote %q", key)
		}
	}
	marked := 0
	for _, entry := range after["recorded"].([]any) {
		row := entry.(map[string]any)
		if row["cloud_moved_at"] == nil {
			continue
		}
		marked++
		if row["file"] != "scaleway/terraform.jsonl" || row["cloud_moved_at"] != "2026-08-21" {
			t.Fatalf("the wrong recording was marked: %v", row)
		}
	}
	if marked != 1 {
		t.Fatalf("%d recording(s) marked, want exactly the one named", marked)
	}
	if err := markCorpusStale(path, "no/such.jsonl", corpusNow, 1); err == nil {
		t.Fatal("marking a corpus with no recording entry was accepted, so a typo would write nothing and say nothing")
	}
}

// Ownership never follows a parent's identifier.
//
// A create answers project_id, organization_id and vpc_id beside its own id. A
// guard that collected those would consider the account's project something this
// run created, and `DELETE /projects/{id}` would walk straight through the
// ownership check.
func TestOwnershipDoesNotFollowAParentIdentifier(t *testing.T) {
	body := map[string]any{
		"id":              "11111111-1111-4111-8111-111111111111",
		"project_id":      "22222222-2222-4222-8222-222222222222",
		"organization_id": "33333333-3333-4333-8333-333333333333",
		"vpc_id":          "44444444-4444-4444-8444-444444444444",
		"subnets":         []any{map[string]any{"id": "55555555-5555-4555-8555-555555555555"}},
	}
	got := map[string]bool{}
	for _, id := range mintedIDs(body) {
		got[id] = true
	}
	if !got["11111111-1111-4111-8111-111111111111"] || !got["55555555-5555-4555-8555-555555555555"] {
		t.Fatalf("the object this run created is not owned by it: %v", got)
	}
	for _, parent := range []string{
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
		"44444444-4444-4444-8444-444444444444",
	} {
		if got[parent] {
			t.Fatalf("%s is a parent's identifier and this run claims to own it", parent)
		}
	}
}

// A credential never travels in clear, and never in argv.
func TestACredentialNeverTravelsInClear(t *testing.T) {
	var errs bytes.Buffer
	if code := checkCloudEndpoint("http://api.example.com", true, &errs); code != exitError {
		t.Fatalf("plain HTTP off loopback was accepted (exit %d)", code)
	}
	if !strings.Contains(errs.String(), "in clear") {
		t.Fatalf("the refusal does not say why:\n%s", errs.String())
	}
	if code := checkCloudEndpoint("https://api.scaleway.com", true, io.Discard); code != exitOK {
		t.Fatal("an https endpoint was refused")
	}
	if code := checkCloudEndpoint("http://127.0.0.1:4800", true, io.Discard); code != exitOK {
		t.Fatal("a loopback endpoint was refused, and it is what every falsification runs against")
	}

	// The value comes from the environment, never from the command line: argv
	// is world-readable, and a secret key in it is a secret key in `ps`.
	t.Setenv("FEINT_TEST_TOKEN", "s3cret")
	headers, err := credentialHeaders(map[string]string{"X-Auth-Token": "FEINT_TEST_TOKEN"})
	if err != nil || headers["X-Auth-Token"] != "s3cret" {
		t.Fatalf("the credential was not read from its environment variable: %v %v", headers, err)
	}
	if _, err := credentialHeaders(map[string]string{"X-Auth-Token": "FEINT_TEST_ABSENT"}); err == nil {
		t.Fatal("an empty credential was accepted, and an unauthenticated replay answers 401 everywhere — which reads exactly like a cloud that changed")
	}
}

// A credential the operator named is actually sent, on every request. Asserted
// rather than assumed: a transport that dropped it would make every run answer
// 401 and every 401 is verdict three, so the failure would look like a cloud
// problem forever.
func TestTheNamedCredentialReachesEveryRequest(t *testing.T) {
	t.Setenv("FEINT_TEST_TOKEN", "s3cret")
	cloud, seen := recordingCloud(t)
	dir, accepted := cloudCorpusFixture(t, exchangeLine(t, 1, "GET",
		"/vpc/v2/regions/fr-par/vpcs", "page=1", 200, nil, map[string]any{"vpcs": []any{}, "total_count": 0}))

	_, _, code := runCloudReplay(t, dir, accepted, cloud.URL, map[string]string{"X-Auth-Token": "FEINT_TEST_TOKEN"})
	if code != exitOK {
		t.Fatalf("the run exited %d", code)
	}
	requests := seen()
	if len(requests) == 0 {
		t.Fatal("nothing reached the endpoint, so this proves nothing about its headers")
	}
	for _, r := range requests {
		if r.Token != "s3cret" {
			t.Fatalf("%s %s carried %q as its credential", r.Method, r.Path, r.Token)
		}
	}
}

// A corpus nobody dated is not reissued at a cloud. A difference against an
// undated recording cannot be read: "the cloud moved" and "this file describes
// the cloud of eight months ago" are the same observation at two ages.
func TestAnUndatedCorpusIsNotReplayedAtTheCloud(t *testing.T) {
	cloud, seen := recordingCloud(t)
	dir, _ := cloudCorpusFixture(t, exchangeLine(t, 1, "GET",
		"/vpc/v2/regions/fr-par/vpcs", "page=1", 200, nil, map[string]any{"vpcs": []any{}}))
	undated := writeAccepted(t, corpusAcceptance{WarnAfterDays: 180})

	out, errs, code := runCloudReplay(t, dir, undated, cloud.URL, nil)
	if code != exitError {
		t.Fatalf("an undated corpus exited %d, want %d.\nstdout:\n%s\nstderr:\n%s", code, exitError, out, errs)
	}
	if !strings.Contains(errs, "no entry") {
		t.Fatalf("the refusal does not say the recording carries no date:\n%s", errs)
	}
	if len(seen()) != 0 {
		t.Fatal("an undated corpus reached the endpoint anyway")
	}
}

// --dry-run reads and refuses everything that would change the account, so an
// operator can see what a run would do before it does it.
func TestADryRunSendsNoChange(t *testing.T) {
	dir, accepted, _ := vpcCorpusFixture(t)
	cloud, seen := recordingCloud(t)

	req := cloudReplayRequest{dir: dir, accepted: accepted, file: "self/self.jsonl",
		endpoint: cloud.URL, format: "text", timeout: 30 * time.Second, dryRun: true}
	var out, errb bytes.Buffer
	_ = replayCorpusAtCloud(req, corpusNow, &out, &errb)
	for _, r := range seen() {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			t.Fatalf("--dry-run sent %s %s", r.Method, r.Path)
		}
	}
	if !strings.Contains(out.String(), "--dry-run") {
		t.Fatalf("the run does not say why it changed nothing:\n%s", out.String())
	}
}

// ---------------------------------------------------------------------------
// harness

func runCloudReplay(t *testing.T, dir, accepted, endpoint string, credentials map[string]string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := replayCorpusAtCloud(cloudReplayRequest{
		dir: dir, accepted: accepted, file: "self/self.jsonl", endpoint: endpoint,
		credentials: credentials, format: "text", timeout: 30 * time.Second,
	}, corpusNow, &out, &errb)
	return out.String(), errb.String(), code
}

// capturedRequest is one call that reached the stand-in cloud.
type capturedRequest struct {
	Method, Path, Token string
	Body                []byte
}

// recordingCloud is an emulator that also says what was asked of it. The stand-in
// for a provider, and the only way a test can assert what a run *did not* send —
// which is what every guard here is about.
func recordingCloud(t *testing.T) (*httptest.Server, func() []capturedRequest) {
	t.Helper()
	srv, _, err := newServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	inner := srv.Handler()

	var mu sync.Mutex
	var seen []capturedRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		_ = r.Body.Close()
		mu.Lock()
		seen = append(seen, capturedRequest{Method: r.Method, Path: r.URL.Path,
			Token: r.Header.Get("X-Auth-Token"), Body: body})
		mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, func() []capturedRequest {
		mu.Lock()
		defer mu.Unlock()
		out := make([]capturedRequest, len(seen))
		copy(out, seen)
		return out
	}
}

// swallowingCloud answers 204 to every DELETE and forwards nothing, which is
// what a provider's own asynchronous delete looks like from outside. Returns the
// front door and the endpoint behind it, so a test can read what is really
// there.
func swallowingCloud(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	inner, _ := recordingCloud(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		proxied, err := http.NewRequestWithContext(r.Context(), r.Method, inner.URL+r.URL.RequestURI(), r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		proxied.Header = r.Header.Clone()
		res, err := http.DefaultClient.Do(proxied)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = res.Body.Close() }()
		for name, values := range res.Header {
			for _, v := range values {
				w.Header().Add(name, v)
			}
		}
		w.WriteHeader(res.StatusCode)
		_, _ = io.Copy(w, res.Body)
	}))
	t.Cleanup(ts.Close)
	return ts, inner.URL
}

// vpcCorpusFixture records a free lifecycle — a VPC and a private network,
// created, read, updated, deleted, and each deletion proved by a read answering
// 404 — through the proxy, exactly as corpus/README.md's procedure does against
// a real account. Only operations on the free-to-create list, so the fixture
// obeys the rule it is here to prove.
func vpcCorpusFixture(t *testing.T) (dir, accepted, recording string) {
	t.Helper()
	source := freshEmulator(t)

	recording = filepath.Join(t.TempDir(), "lifecycle.jsonl")
	file, err := os.OpenFile(recording, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	writer := proxy.NewWriter(file, 0)
	upstream, err := url.Parse(source.URL)
	if err != nil {
		t.Fatal(err)
	}
	table, err := emulator.NewTable(mustPacks(t)...)
	if err != nil {
		t.Fatal(err)
	}
	px, err := proxy.New(proxy.Options{Upstream: upstream, Writer: writer, Table: table})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(px)
	defer front.Close()

	region := "/vpc/v2/regions/fr-par"
	vpc := topID(t, callJSON(t, front.URL, "POST", region+"/vpcs", `{"name":"fixture-vpc","tags":["a"]}`))
	callJSON(t, front.URL, "GET", region+"/vpcs/"+vpc, "")
	pn := topID(t, callJSON(t, front.URL, "POST", region+"/private-networks",
		`{"name":"fixture-pn","vpc_id":"`+vpc+`"}`))
	callJSON(t, front.URL, "GET", region+"/private-networks/"+pn, "")
	callJSON(t, front.URL, "DELETE", region+"/private-networks/"+pn, "")
	// The read that proves the deletion, which is #352's rule and therefore
	// belongs in the fixture: the proof of a destruction is a 404, never the
	// exit code of the delete. callJSON refuses a 4xx, so this one goes raw.
	callAnything(t, front.URL, "GET", region+"/private-networks/"+pn)
	callJSON(t, front.URL, "DELETE", region+"/vpcs/"+vpc, "")
	callAnything(t, front.URL, "GET", region+"/vpcs/"+vpc)

	if err := writer.Close(); err != nil {
		t.Fatalf("close the transcript writer: %v", err)
	}
	raw, err := os.ReadFile(recording) //nolint:gosec // a file this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	dir, accepted = cloudCorpusFixture(t, strings.Split(strings.TrimRight(string(raw), "\n"), "\n")...)
	return dir, accepted, recording
}

// cloudCorpusFixture lays lines out as a corpus with its dated acceptance file.
func cloudCorpusFixture(t *testing.T, lines ...string) (dir, accepted string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "corpus")
	if err := os.MkdirAll(filepath.Join(dir, "self"), 0o750); err != nil {
		t.Fatal(err)
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "self", "self.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	accepted = writeAccepted(t, corpusAcceptance{
		WarnAfterDays: 180,
		Recorded: []corpusRecording{
			{File: "self/self.jsonl", At: "2026-08-21", Client: "feint", Cloud: "a stand-in cloud"},
		},
	})
	return dir, accepted
}

// exchangeLine writes one transcript line by hand, for the shapes no recording
// of this emulator can produce.
func exchangeLine(t *testing.T, seq int, method, path, query string, status int, req, res any) string {
	t.Helper()
	line := map[string]any{
		"seq": seq, "t": "2020-01-01T00:00:00Z", "method": method, "path": path,
		"status": status, "ms": 0, "mounted": true,
		"res": map[string]any{"headers": map[string]any{"Content-Type": "application/json"}, "body": res},
	}
	if query != "" {
		line["query"] = query
	}
	if req != nil {
		line["req"] = map[string]any{"headers": map[string]any{"Content-Type": "application/json"}, "body": req}
	}
	raw, err := json.Marshal(line)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func createVPC(t *testing.T, endpoint, name string) string {
	t.Helper()
	return topID(t, callJSON(t, endpoint, "POST", "/vpc/v2/regions/fr-par/vpcs", `{"name":"`+name+`"}`))
}

// topID reads the identifier a Scaleway vpc/v2 create answers at the top level,
// where instance/v1 wraps its object in a key and [idOf] reads that shape.
func topID(t *testing.T, body map[string]any) string {
	t.Helper()
	id, ok := body["id"].(string)
	if !ok || id == "" {
		t.Fatalf("the answer carries no identifier: %v", body)
	}
	return id
}

// callAnything issues a request and reads its answer whatever the status is.
func callAnything(t *testing.T, base, method, path string) {
	t.Helper()
	req, err := http.NewRequest(method, base+path, nil) //nolint:noctx // a loopback test server
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
}

func statusOf(t *testing.T, target string) int {
	t.Helper()
	res, err := http.Get(target) //nolint:gosec,noctx // a loopback test server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	return res.StatusCode
}

func vpcCount(t *testing.T, endpoint string) int {
	t.Helper()
	res, err := http.Get(endpoint + "/vpc/v2/regions/fr-par/vpcs?page=1") //nolint:gosec,noctx // a loopback test server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var body struct {
		VPCs []json.RawMessage `json:"vpcs"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return len(body.VPCs)
}

// The moved-cloud warning survives a run that is red for another reason.
//
// warnMovedCorpora's own comment says the warning is on every run, and until
// this test existed that sentence was false: the call sat below the
// unexercised-invariant guard (#343) and below the stale-exemption guard, so a
// corpus that tripped either printed nothing about the provider having moved
// under it. That is the worst moment to withhold it — "re-record this file" is
// a candidate fix for the very redness being reported, and a maintainer who
// does not see it goes looking for a defect in the emulator instead.
//
// The fixture is deliberately the poorest run that reaches the print: a VPC
// corpus reaches no operation carrying an invariant, so #343's guard returns
// exitError before the warning's old position. Asserting the exit code as well
// as the warning is what keeps this honest — if a later change made the run
// green, the test would still pass while measuring nothing.
func TestTheMovedWarningSurvivesARunThatIsRedForAnotherReason(t *testing.T) {
	dir, accepted, _ := vpcCorpusFixture(t)

	acc, err := readCorpusAcceptance(accepted)
	if err != nil {
		t.Fatal(err)
	}
	if len(acc.Recorded) != 1 {
		t.Fatalf("the fixture no longer carries exactly one recording: %+v", acc.Recorded)
	}
	acc.Recorded[0].CloudMovedAt = "2026-08-21"
	acc.Recorded[0].CloudMoved = 3
	body, err := json.MarshalIndent(acc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(accepted, body, 0o600); err != nil {
		t.Fatal(err)
	}

	out, errs, code := runCorpusGate(t, dir, accepted)
	if code != exitError {
		t.Fatalf("this fixture no longer goes red for another reason, so it cannot prove the warning survives one (exit %d).\nstdout:\n%s\nstderr:\n%s",
			code, out, errs)
	}
	if !strings.Contains(errs, "invariant(s) and this corpus ran none of them") {
		t.Fatalf("the run is red for some other reason than the one this test assumes:\n%s", errs)
	}
	if !strings.Contains(out, "had moved: re-record it") {
		t.Fatalf("a run red for another reason withheld the moved-cloud warning:\n%s", out)
	}
}
