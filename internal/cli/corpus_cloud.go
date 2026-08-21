package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/corpus"
	"github.com/stephrobert/feint/internal/proxy"
	"github.com/stephrobert/feint/internal/replay"
	"github.com/stephrobert/feint/internal/shape"
	"github.com/stephrobert/feint/internal/trace"
)

// The other direction of one comparison (#359).
//
// `feint corpus --check` replays a committed corpus against a fresh emulator,
// and a divergence there means *this emulator is wrong*. `feint corpus
// --against-cloud` reissues the same file at the provider it was recorded from,
// and a divergence there means *the cloud has changed*. Same artefact, same
// comparator, opposite conclusion — and that is the whole design: internal/replay
// is called by both, so there is no second recorder and no second comparison to
// keep in step. Everything below is what a real account demands and an emulator
// does not.
//
// # What this catches that internal/drift cannot
//
// The surface scan reads the provider's own generated SDK and reports an
// operation that appeared or disappeared. It sees that a method exists; it sees
// nothing of what the method answers. A status that moves from 200 to 201, a
// field that appears, a field that goes, a list that reorders, a refusal that
// stops being one: the Go signature is identical before and after, the baseline
// stays green, and this emulator goes on serving what the cloud stopped
// answering. #270 measured three of those in one read of one private network.
//
// # Why it is not a gate
//
// It needs a credential, it creates real objects, it costs money and its verdict
// depends on whose account ran it. `mise run conformance` is outside the
// pre-commit hook for a weaker version of the same reason, and CLAUDE.md is
// explicit that a gate people skip by habit is worse than no gate. So this runs
// on a schedule and on demand, on a maintainer's account, and opens a pull
// request when something moved — the shape .github/workflows/drift.yml already
// has for the surface.
//
// # The honest limit, stated where the code is rather than only in prose
//
// A corpus records one account, in one region, at one time. A difference is the
// cloud changing for everybody, or one account's quota, or a region's rollout,
// and **nothing here can tell those apart**. The report says what was measured
// and names the recording's date; deciding which of the three it is stays a
// human's job. What the code *does* separate is the three outcomes it must never
// blur, and [cloudOutcome] is that separation.

// cloudOutcome is what a reported exchange came to when it was reissued at the
// provider: the three verdicts #359 says must never be blurred into one another.
//
// Three and not five, deliberately. An exchange the provider answered exactly as
// recorded, and one no route of this emulator serves, are *counts* rather than
// outcomes: nobody acts on either, and giving them a name in this list would put
// them in the same sentence as a finding somebody has to read. What the
// arithmetic must never do is sum the three below, which is how "I could not
// measure" starts reading as "nothing is wrong".
type cloudOutcome string

const (
	// cloudMoved: the provider answers differently now. Exit 2 — the finding
	// this whole direction exists for.
	cloudMoved cloudOutcome = "the cloud answers differently"
	// cloudInstrument: the recording cannot be reissued as it was recorded, or
	// what came back is explained by a value the recorder invented. **Never a
	// cloud change**, and it is the first thing to suspect rather than the last:
	// #73 found the proxy's own redaction manufacturing nine false divergences,
	// and #354 four more, three of which hid an entire lifecycle.
	cloudInstrument cloudOutcome = "the recording could not be reissued as recorded"
	// cloudUnmeasured: the call was not made, or came back with an answer that
	// is about the caller rather than about the resource — an authentication
	// refusal, a rate limit, a gateway error, a guard that would not let the
	// request out. An error, never a pass. Exit 1.
	cloudUnmeasured cloudOutcome = "the call could not be made"
)

// cloudFinding is one line of the report, and it is written to be actionable
// without opening a transcript: the operation, the path it went to, what kind of
// change it is, and how old the recording that says so is.
type cloudFinding struct {
	Seq       int          `json:"seq"`
	Operation string       `json:"operation"`
	Method    string       `json:"method"`
	Path      string       `json:"path"`
	Outcome   cloudOutcome `json:"outcome"`
	// Detail is [describeFinding]'s sentence, the vocabulary `feint replay`
	// already uses, or the guard's own refusal.
	Detail string `json:"detail"`
	// Instrument names the values the request still carried that the sanitiser
	// invented, when there were any. It is the caution the three verdicts turn
	// on: a refusal landing on a request holding a minted address is a defect of
	// the recording until somebody proves otherwise.
	Instrument []string `json:"instrument_values,omitempty"`
	// RecordedAt is the day the corpus was recorded, so a reader can weigh the
	// finding without going to look the date up.
	RecordedAt string `json:"recorded_at"`
	Cloud      string `json:"cloud"`
}

// cloudReport is the whole run, and the counts are never summed.
type cloudReport struct {
	File       string         `json:"file"`
	RecordedAt string         `json:"recorded_at"`
	AgeDays    int            `json:"age_days"`
	Client     string         `json:"client"`
	Cloud      string         `json:"cloud"`
	Endpoint   string         `json:"endpoint"`
	Exchanges  int            `json:"exchanges"`
	Compared   int            `json:"compared"`
	Moved      int            `json:"cloud_changed"`
	Instrument int            `json:"recording_defective"`
	Unmeasured int            `json:"not_measured"`
	Unserved   int            `json:"unserved"`
	Created    int            `json:"created"`
	Destroyed  int            `json:"destroyed_and_proved"`
	Findings   []cloudFinding `json:"findings"`
}

// cloudReplayRequest is what one run needs. A struct rather than eight
// parameters, because every one of them is a decision somebody has to be able to
// read back off a command line.
type cloudReplayRequest struct {
	dir, accepted, file string
	endpoint            string
	// credentials maps a header name to the environment variable holding its
	// value. The *name* travels on the command line and the value never does:
	// argv is world-readable on this platform, and a secret key in it is a
	// secret key in `ps`.
	credentials map[string]string
	// bind seeds [replay.Options.Bind]: which project, which organisation.
	bind    map[string]string
	format  string
	timeout time.Duration
	dryRun  bool
	// markStale writes the finding back into the acceptance file, which is how
	// the two directions of one comparison talk to each other: `corpus --check`
	// then warns with a *measured* date instead of a chosen horizon. See
	// [corpusRecording.CloudMovedAt].
	markStale bool
}

// replayCorpusAtCloud is the command.
//
// The return value is named because the ledger runs in a defer and has to be
// able to move it: an object this run created and could not destroy is a failure
// of the run, whatever the comparison found, and a report that said "the cloud
// has not moved" while leaving a resource behind would be the worst possible
// combination of true and useless.
func replayCorpusAtCloud(req cloudReplayRequest, now time.Time, stdout, stderr io.Writer) (code int) {
	if req.file == "" {
		fmt.Fprintln(stderr, "feint: --against-cloud needs --file naming one committed corpus: a directory of them would be a directory of real calls nobody named")
		return exitError
	}
	if code := checkCloudEndpoint(req.endpoint, len(req.credentials) > 0, stderr); code != exitOK {
		return code
	}

	acc, err := readCorpusAcceptance(req.accepted)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	rel := filepath.ToSlash(req.file)
	if trimmed, cut := strings.CutPrefix(rel, filepath.ToSlash(req.dir)+"/"); cut {
		rel = trimmed
	}
	// The recording's own entry, and it is mandatory. A replay that cannot say
	// how old the recording is cannot report a change against it: "the cloud
	// moved" and "this file describes a cloud of eight months ago" are the same
	// observation read at two different ages.
	// TestAnUndatedCorpusIsNotReplayedAtTheCloud fails without this.
	var rec corpusRecording
	for _, r := range acc.Recorded {
		if r.File == rel {
			rec = r
		}
	}
	if rec.File == "" {
		fmt.Fprintf(stderr, "feint: %s has no entry in %s saying when it was recorded, so a difference here could not be dated\n", rel, req.accepted)
		return exitError
	}
	at, err := time.Parse(time.DateOnly, rec.At)
	if err != nil {
		fmt.Fprintf(stderr, "feint: the recording entry for %s carries no readable date (%q, want YYYY-MM-DD)\n", rel, rec.At)
		return exitError
	}

	exs, err := loadTranscript(filepath.Join(req.dir, filepath.FromSlash(rel)))
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	if len(exs) == 0 {
		fmt.Fprintf(stderr, "feint: %s holds no exchange, so there was nothing to reissue\n", rel)
		return exitError
	}

	rep := cloudReport{
		File: rel, RecordedAt: rec.At, AgeDays: int(now.Sub(at).Hours() / 24),
		Client: rec.Client, Cloud: rec.Cloud, Endpoint: req.endpoint, Exchanges: len(exs),
	}

	// Split before anything is sent: an exchange the sanitiser blanked cannot be
	// addressed at the provider, and sending it would spend a real call on a
	// path that exists nowhere and then grade the 404 as a change of the cloud.
	sendable, refusedByShape := splitReplayable(exs)
	for _, u := range refusedByShape {
		rep.Findings = append(rep.Findings, cloudFinding{
			Seq: u.seq, Method: u.method, Path: shape.AnonymisePath(u.path),
			Outcome: cloudInstrument, Detail: u.why, RecordedAt: rec.At, Cloud: rec.Cloud,
		})
		rep.Instrument++
	}
	if len(sendable) == 0 {
		fmt.Fprintf(stderr, "feint: %s carries no exchange this replay can reissue, so it compared nothing against the cloud\n", rel)
		return exitError
	}
	// Every object this run creates carries a name a human scanning the console
	// recognises at a glance. The recorded name is the sanitiser's invention
	// either way (`redacted-2`), so nothing of the measurement is lost — and the
	// rule that says so is #352's, not a preference.
	// TestEveryObjectThisRunCreatesIsNamedForItAndIsDestroyed fails without this.
	renameForThisRun(sendable)

	env := emulator.DefaultEnv()
	packs, err := packsFor(env)
	if err != nil {
		fmt.Fprintf(stderr, "feint: build the emulator: %v\n", err)
		return exitError
	}
	table, err := emulator.NewTable(packs...)
	if err != nil {
		fmt.Fprintf(stderr, "feint: build the route table: %v\n", err)
		return exitError
	}

	headers, err := credentialHeaders(req.credentials)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	guard := newAccountGuard(req.dryRun)
	client := &http.Client{
		Timeout:   req.timeout,
		Transport: &credentialTransport{base: http.DefaultTransport, headers: headers},
	}
	// Armed before the first request, which is `trap … EXIT` written in Go: a
	// panic, a transport error, a context that dies — every one of them still
	// runs this, and an aborted run is exactly when an orphan is made.
	defer func() {
		destroyed, leftovers := guard.destroy(client, req.endpoint)
		rep.Created, rep.Destroyed = len(guard.created), destroyed
		reportLedger(guard, destroyed, leftovers, stdout, stderr)
		// TestADestructionIsProvedByAReadAndNotByTheDeletesOwnAnswer fails
		// without this.
		if len(leftovers) > 0 {
			code = exitError
		}
	}()

	declined, invariants, declarations := replayDeclarations(packs, stderr)
	if declarations != exitOK {
		return declarations
	}

	run, err := replay.Run(context.Background(), sendable, replay.Options{
		Endpoint:   strings.TrimSuffix(req.endpoint, "/"),
		Client:     client,
		Table:      table,
		Declined:   excusedAtTheCloud(declined),
		Invariants: invariants,
		Bind:       req.bind,
		Guard:      guard,
	})
	if err != nil {
		// A transport failure aborts the run, and it is verdict three: the call
		// could not be made. The ledger above still empties the account.
		fmt.Fprintf(stderr, "feint: %s: %v\n", rel, err)
		return exitError
	}

	gradeCloudRun(&rep, run, sendable, guard, rec)
	if req.format == "json" {
		if err := writeJSON(stdout, rep); err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
	} else {
		writeCloudReport(rep, acc.WarnAfterDays, stdout)
	}

	if req.markStale && rep.Moved > 0 {
		if err := markCorpusStale(req.accepted, rel, now, rep.Moved); err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
		fmt.Fprintf(stdout, "\n%s now records in %s that the cloud has moved under it, so `corpus --check` says so on every run\n", rel, req.accepted)
	}

	switch {
	case rep.Unmeasured > 0:
		fmt.Fprintf(stderr, "feint: %d call(s) could not be made, so this run measured less than it says: fix that before reading the rest\n", rep.Unmeasured)
		return exitError
	case rep.Moved > 0:
		fmt.Fprintf(stderr, "feint: %d finding(s) say the cloud answers differently from the recording of %s\n", rep.Moved, rec.At)
		return exitDrift
	}
	return exitOK
}

// markCorpusStale writes the measurement back into the acceptance file.
//
// Read back, edited and rewritten rather than regenerated, because the file is
// prose as much as data: its "comment" array carries the argument for every
// exemption in it, and a marshaller that reordered or reflowed that would make
// the diff of a scheduled job unreadable — which is the same as making it
// unreviewed.
//
// TestMarkingACorpusStaleWritesTheMeasurementAndNothingElse fails without this.
func markCorpusStale(path, file string, now time.Time, moved int) error {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied path, just read by readCorpusAcceptance
	if err != nil {
		return fmt.Errorf("read the acceptance file %s: %w", path, err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("read the acceptance file %s: %w", path, err)
	}
	var recorded []map[string]any
	if err := json.Unmarshal(doc["recorded"], &recorded); err != nil {
		return fmt.Errorf("read the recordings of %s: %w", path, err)
	}
	marked := false
	for _, entry := range recorded {
		if entry["file"] != file {
			continue
		}
		entry["cloud_moved_at"] = now.Format(time.DateOnly)
		entry["cloud_moved"] = moved
		marked = true
	}
	if !marked {
		return fmt.Errorf("%s has no recording entry in %s to mark", file, path)
	}
	updated, err := json.MarshalIndent(recorded, "  ", "  ")
	if err != nil {
		return err
	}
	doc["recorded"] = updated
	// The key order of the document, spelled out: a map has none, and a file
	// whose keys shuffled on every write would produce a diff nobody reads.
	var out []byte
	out = append(out, "{\n"...)
	for i, key := range []string{"comment", "warn_after_days", "recorded", "accepted"} {
		value, present := doc[key]
		if !present {
			continue
		}
		if i > 0 && len(out) > 2 {
			out = append(out, ",\n"...)
		}
		out = append(out, "  \""...)
		out = append(out, key...)
		out = append(out, "\": "...)
		out = append(out, value...)
	}
	out = append(out, "\n}\n"...)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("write the acceptance file %s: %w", path, err)
	}
	// Read back through the gate's own reader, so a write that produced
	// something this repository cannot parse fails here rather than on the next
	// pull request.
	if _, err := readCorpusAcceptance(path); err != nil {
		return fmt.Errorf("the acceptance file this run wrote cannot be read back: %w", err)
	}
	return nil
}

// excusedAtTheCloud is what a pack's declined fields are worth against the
// provider: nothing.
//
// A decline excuses a field *this emulator* does not serve, in the pack's own
// words, and that is right for `corpus --check`. Carrying the same list here
// would excuse the provider for not answering it — hiding the exact drift this
// direction exists to catch, since a field that leaves a provider's answers is
// invisible to every SDK scan.
//
// Written as a function rather than as an omitted struct field on purpose: an
// absence cannot be neutralised, so it cannot be falsified, and a decision
// nobody can make fail is a comment.
// TestAPacksDeclineDoesNotExcuseTheCloud fails without this.
func excusedAtTheCloud([]emulator.FieldDecline) []emulator.FieldDecline { return nil }

// checkCloudEndpoint refuses an endpoint a credential must not travel to.
//
// Plain HTTP off loopback is refused outright: this command exists to be pointed
// at a provider, its client carries an account's secret key in a header, and a
// typo in a scheme would put that key on the wire in clear. Loopback stays
// allowed because that is how the falsification runs — an emulator standing in
// for the cloud, on a port nobody else can reach.
// TestACredentialNeverTravelsInClear fails without this.
func checkCloudEndpoint(endpoint string, credentialled bool, stderr io.Writer) int {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		fmt.Fprintf(stderr, "feint: --endpoint %q is not an http(s) URL naming the cloud to reissue at\n", endpoint)
		return exitError
	}
	if u.Scheme == "https" {
		return exitOK
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); (ip != nil && ip.IsLoopback()) || host == "localhost" {
		return exitOK
	}
	carrying := "no credential"
	if credentialled {
		carrying = "an account's credential"
	}
	fmt.Fprintf(stderr, "feint: --endpoint %q is plain HTTP off loopback, and this run carries %s: refused rather than sent in clear\n", endpoint, carrying)
	return exitError
}

// credentialTransport puts back the headers the recorder took out.
//
// The recorder wrote "REDACTED" over every credential-shaped header, which is
// the right thing for a committed artefact and leaves a replay with no way to
// authenticate. So the operator names header and environment variable, and the
// value is read here, held in memory, and written to nothing: not to the report,
// not to a log line, not to the process arguments.
type credentialTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *credentialTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	for name, value := range t.headers {
		clone.Header.Set(name, value)
	}
	return t.base.RoundTrip(clone)
}

// credentialHeaders reads each named environment variable, and fails on one that
// is empty rather than sending an unauthenticated request that would come back
// 401 and read like a cloud that changed.
func credentialHeaders(spec map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(spec))
	names := make([]string, 0, len(spec))
	for header := range spec {
		names = append(names, header)
	}
	sort.Strings(names)
	for _, header := range names {
		envName := spec[header]
		value := os.Getenv(envName)
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("--credential %s=%s: $%s is empty, and an unauthenticated replay answers 401 everywhere, which reads exactly like a cloud that changed", header, envName, envName)
		}
		out[header] = value
	}
	return out, nil
}

// unreplayable is one exchange that cannot be reissued, and why.
type unreplayable struct {
	seq          int
	method, path string
	why          string
}

// splitReplayable separates what can honestly be sent from what cannot.
//
// Two rules, and the difference between them is the difference between verdict
// one and verdict two:
//
//   - a path or a query value the sanitiser **blanked** cannot be addressed at
//     all. `GET /redacted-1/redacted-2` names no route of any cloud, and
//     `?status=redacted-6` is an enumerated value the provider refuses. Sending
//     either spends a real call to collect a 400 the recording caused.
//   - a body field the sanitiser blanked is ordinary synthetic text — a name, a
//     tag — and is sent. Refusing those would refuse every create in the corpus.
//   - the proxy's own "REDACTED" anywhere in the request is fatal wherever it
//     sits: the value was destroyed rather than replaced, so the request that
//     went out would carry a string no client ever sent. That is #73's family,
//     nine false divergences from one substitution.
//
// TestABlankedPathIsNeverSentToTheCloud fails without this.
func splitReplayable(exs []trace.Exchange) ([]trace.Exchange, []unreplayable) {
	var send []trace.Exchange
	var refused []unreplayable
	for i := range exs {
		x := exs[i]
		seq := i + 1
		switch {
		case blankedSegment(x.Path):
			refused = append(refused, unreplayable{seq, x.Method, x.Path,
				"the sanitiser blanked this path, so it addresses no operation of any cloud (corpus/README.md, \"What it still cannot see\")"})
		case blankedQuery(x.Query):
			refused = append(refused, unreplayable{seq, x.Method, x.Path,
				"the sanitiser blanked a query value, and a provider's enumerated parameter refuses an invented one"})
		case redactedRequest(x):
			refused = append(refused, unreplayable{seq, x.Method, x.Path,
				"the recorder wrote REDACTED over a value of this request, so reissuing it would send a string the client never sent (#73)"})
		default:
			send = append(send, x)
		}
	}
	return send, refused
}

// blankedSegment reports whether any path segment is one the sanitiser invented.
func blankedSegment(path string) bool {
	for _, seg := range strings.Split(path, "/") {
		if strings.HasPrefix(seg, corpus.Token) {
			return true
		}
	}
	return false
}

// blankedQuery reports whether any query value is one the sanitiser invented.
func blankedQuery(raw string) bool {
	for _, pair := range strings.Split(raw, "&") {
		if _, value, ok := strings.Cut(pair, "="); ok && strings.HasPrefix(value, corpus.Token) {
			return true
		}
	}
	return false
}

// redactedRequest reports whether the proxy's placeholder survives anywhere the
// request would carry it out. Headers are excluded: every recorded credential
// header is "REDACTED" by construction, [replay.Run] reissues none of them, and
// --credential is what puts the real one back.
func redactedRequest(x trace.Exchange) bool {
	if strings.Contains(x.Path, proxy.Placeholder) || strings.Contains(x.Query, proxy.Placeholder) {
		return true
	}
	if x.Req == nil {
		return false
	}
	found := false
	walkStrings(x.Req.Body, func(s string) {
		if s == proxy.Placeholder {
			found = true
		}
	})
	return found
}

// thisRunPrefix is the name every object this run creates carries, so that an
// object left behind by an aborted run is identifiable in a console by eye.
const thisRunPrefix = "feint-corpus-"

// renameForThisRun rewrites the top-level `name` of every request that changes
// state, in place.
//
// Top level only: a nested `name` belongs to a subnet or a rule the parent
// object owns, and renaming those would change what the request means. The
// value is not compared — a replay grades presence, type, and the values a pack
// declares invariant, and no pack declares one on these operations — so what
// this changes is what a human sees in the console and nothing of the verdict.
func renameForThisRun(exs []trace.Exchange) {
	for i := range exs {
		x := &exs[i]
		if x.Method == http.MethodGet || x.Method == http.MethodHead || x.Req == nil {
			continue
		}
		body, isObject := x.Req.Body.(map[string]any)
		if !isObject {
			continue
		}
		if _, named := body["name"]; !named {
			continue
		}
		renamed := make(map[string]any, len(body))
		for k, v := range body {
			renamed[k] = v
		}
		renamed["name"] = fmt.Sprintf("%s%d", thisRunPrefix, i+1)
		copied := *x.Req
		copied.Body = renamed
		x.Req = &copied
	}
}

// walkStrings visits every string in a decoded JSON value.
func walkStrings(v any, fn func(string)) {
	switch t := v.(type) {
	case map[string]any:
		for _, nested := range t {
			walkStrings(nested, fn)
		}
	case []any:
		for _, nested := range t {
			walkStrings(nested, fn)
		}
	case string:
		fn(t)
	}
}

// gradeCloudRun turns the replay's own results into the three verdicts, and it
// is the only place in this file where they are decided.
func gradeCloudRun(rep *cloudReport, run replay.Report, sent []trace.Exchange, guard *accountGuard, rec corpusRecording) {
	for _, r := range run.Results {
		recorded := 0
		if i := r.Seq - 1; i >= 0 && i < len(sent) {
			recorded = sent[i].Status
		}
		line := cloudFinding{
			Seq: r.Seq, Operation: r.Operation, Method: r.Method, Path: r.Path,
			Instrument: guard.synthetic[r.Seq], RecordedAt: rec.At, Cloud: rec.Cloud,
		}
		switch {
		case r.Verdict == replay.Unserved:
			rep.Unserved++
			continue
		case r.Verdict == replay.Refused:
			line.Outcome, line.Detail = cloudUnmeasured, r.Refused
			rep.Unmeasured++
		case r.Verdict == replay.Skipped:
			line.Outcome = cloudInstrument
			line.Detail = "the recording carries no answer for this request, so there was nothing to compare"
			rep.Instrument++
		case notAboutTheResource(recorded, r.Status):
			// An authentication refusal, a rate limit, a gateway error. The
			// request reached something and came back with an answer about the
			// caller, and calling that "the cloud changed" is the exact
			// mistake #359 asks this to never make.
			line.Outcome = cloudUnmeasured
			line.Detail = fmt.Sprintf("the recording carries %d and the endpoint answered %d, which is about this caller rather than about the resource", recorded, r.Status)
			rep.Unmeasured++
		case r.Verdict == replay.Matched:
			rep.Compared++
			continue
		default:
			rep.Compared++
			for _, f := range r.Findings {
				if f.Kind == replay.KindRedacted {
					// The recorder erased the type, so there is nothing to
					// compare and nothing to conclude. Counted, never blurred
					// into a change of the cloud.
					continue
				}
				one := line
				one.Detail = describeFinding(f)
				if len(one.Instrument) > 0 {
					one.Outcome = cloudInstrument
					rep.Instrument++
				} else {
					one.Outcome = cloudMoved
					rep.Moved++
				}
				rep.Findings = append(rep.Findings, one)
			}
			continue
		}
		rep.Findings = append(rep.Findings, line)
	}
	sort.SliceStable(rep.Findings, func(i, j int) bool { return rep.Findings[i].Seq < rep.Findings[j].Seq })
}

// notAboutTheResource reports whether an answer describes the caller rather than
// the thing addressed. A recorded refusal that the provider still refuses is a
// comparison that held, so the recorded side has to have succeeded.
func notAboutTheResource(recorded, got int) bool {
	if recorded >= 400 || got == 0 {
		return false
	}
	switch got {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
		return true
	}
	return got >= 500
}

// writeCloudReport renders the run. Every line names the operation, the path,
// the kind of change and the date of the recording, which is #359's own
// definition of actionable.
func writeCloudReport(rep cloudReport, warnAfterDays int, w io.Writer) {
	fmt.Fprintf(w, "\n%s recorded %s (%d day(s) ago) with %s against %s\n",
		rep.File, rep.RecordedAt, rep.AgeDays, rep.Client, rep.Cloud)
	fmt.Fprintf(w, "reissued at %s\n\n", rep.Endpoint)

	for _, f := range rep.Findings {
		fmt.Fprintf(w, "  seq %-4d %-44s %s %s\n", f.Seq, f.Operation, f.Method, f.Path)
		fmt.Fprintf(w, "           %s: %s\n", f.Outcome, f.Detail)
		if len(f.Instrument) > 0 {
			fmt.Fprintf(w, "           this request still carried %d value(s) the sanitiser invented (%s), so suspect the recording first\n",
				len(f.Instrument), strings.Join(f.Instrument, ", "))
		}
	}
	if len(rep.Findings) > 0 {
		fmt.Fprintln(w)
	}

	fmt.Fprintf(w, "%d exchange(s) in the file, %d compared against %s\n", rep.Exchanges, rep.Compared, rep.Cloud)
	fmt.Fprintf(w, "%d finding(s) say the cloud answers differently now, which is what this run exists to find\n", rep.Moved)
	fmt.Fprintf(w, "%d finding(s) are the recording rather than the cloud, and are never counted as a change\n", rep.Instrument)
	fmt.Fprintf(w, "%d call(s) could not be made, which is an error and never a pass\n", rep.Unmeasured)
	fmt.Fprintf(w, "%d exchange(s) no route of this emulator serves, which is #74's queue\n", rep.Unserved)
	if rep.AgeDays > warnAfterDays && warnAfterDays > 0 {
		fmt.Fprintf(w, "\nwarning: this recording is %d days old, past the %d-day horizon of corpus/accepted.json\n", rep.AgeDays, warnAfterDays)
	}
	fmt.Fprintln(w, "\nwhat this cannot tell you: a difference here is the cloud changing for everybody, or one")
	fmt.Fprintln(w, "account's quota, or a region's rollout. It reports what it measured; which of the three it")
	fmt.Fprintln(w, "is stays a human's call.")
}

// reportLedger says what the run created and proves what it destroyed.
func reportLedger(g *accountGuard, destroyed int, leftovers []string, stdout, stderr io.Writer) {
	if len(g.created) == 0 {
		fmt.Fprintln(stdout, "\nthis run created nothing at the endpoint")
		return
	}
	fmt.Fprintf(stdout, "\n%d object(s) created, %d destroyed with the destruction proved by a read answering 404\n",
		len(g.created), destroyed)
	for _, left := range leftovers {
		// The one place an identifier is printed, and it is printed because the
		// alternative is an operator who cannot find what to delete. A leftover
		// is a failure of this run, not an artefact of it.
		fmt.Fprintf(stderr, "feint: NOT DESTROYED: %s — delete it by hand and say so in the report\n", left)
	}
}

// accountGuard is "bien formé n'est pas autorisé" for a replay that spends
// money.
//
// A recorded request is well formed by construction; that says nothing about
// whether the object it addresses is ours. The recording's identifiers are
// rebound to whatever the endpoint answered, and a rebinding that pairs the
// recorded object with a pre-existing one of the account turns a recorded DELETE
// into the deletion of somebody's property. `mustOwn` in the Incus driver asks
// exactly this question of a label the emulator itself posted; this asks it of
// the identifiers this run's own creates minted.
//
// Two rules, and they are different questions:
//
//   - **may this be created at all** — a create is billable or it is not, and
//     the list of what is free is a decision written down rather than a guess
//     made per run.
//   - **is this object ours** — every identifier in the path of a request that
//     changes state has to be one this run created.
type accountGuard struct {
	// created is the ledger, in creation order, so [accountGuard.destroy] can
	// walk it backwards: a private network is created after the VPC it sits in
	// and has to go first.
	created []createdObject
	// owned is every identifier a create of this run answered.
	owned map[string]bool
	// synthetic records, per exchange, the values the sanitiser invented that a
	// request still carried after rebinding. It is what tells a defect of the
	// instrument from a change of the cloud when a request is refused.
	synthetic map[int][]string
	dryRun    bool
}

// createdObject is one thing this run made and therefore has to destroy.
type createdObject struct {
	// collection is the path the create was POSTed to, and id what the answer
	// named. The two concatenated are the object's own URL, which is what REST
	// means and what every operation on this run's free list obeys.
	collection, id string
	operation      string
}

func (o createdObject) url() string { return o.collection + "/" + o.id }

func newAccountGuard(dryRun bool) *accountGuard {
	return &accountGuard{owned: map[string]bool{}, synthetic: map[int][]string{}, dryRun: dryRun}
}

// freeToCreate names every operation this replay may create with, and why each
// one costs nothing.
//
// A closed list rather than a rule, because "free" is not a property any request
// carries: it is a fact about a provider's price list on a given day, and the
// only honest form for it is a decision a human wrote down and a reviewer can
// see in a diff. An operation absent from it is refused with its own name, so
// the report says which measurement is out of reach without spending, which
// #359 asks for by name.
//
// TestABillableCreateIsRefusedRatherThanSent fails without this.
var freeToCreate = map[string]string{
	"vpc/v2/API.CreateVPC":            "a Scaleway VPC costs nothing; the account is billed for what runs inside one",
	"vpc/v2/API.CreatePrivateNetwork": "a Scaleway private network costs nothing, and #352 recorded the same lifecycle under the same rule",
	"iam/v1alpha1/API.CreateSSHKey":   "an IAM SSH key is a credential record, not a resource, and is free",
}

// Before is the refusal. It runs on the request as it will actually go out.
func (g *accountGuard) Before(a replay.Attempt) string {
	g.noteSynthetic(a)
	if a.Method == http.MethodGet || a.Method == http.MethodHead {
		return ""
	}
	if g.dryRun {
		return "--dry-run: nothing that changes an account is sent"
	}
	// Every identifier this request addresses has to be one this run made. It
	// covers the destructive methods and the create that posts into somebody
	// else's sub-collection with one rule, because they are one question.
	// TestADeleteOfSomethingThisRunDidNotCreateIsRefused fails without this.
	minted := 0
	for _, seg := range strings.Split(a.Path, "/") {
		id, _, _ := strings.Cut(seg, ":")
		if !shape.IsMintedIdentifier(id) {
			continue
		}
		minted++
		if !g.owned[id] {
			return fmt.Sprintf("%s addresses an object this run did not create, and a replay does not delete or reconfigure an account's own property", a.Operation)
		}
	}
	switch a.Method {
	case http.MethodDelete, http.MethodPatch:
		if minted == 0 {
			return fmt.Sprintf("%s changes state and names no object this run created, so nothing proves what it would have addressed", a.Operation)
		}
		return ""
	case http.MethodPost, http.MethodPut:
		if _, free := freeToCreate[a.Operation]; !free {
			return fmt.Sprintf("%s is not on the free-to-create list, so this replay will not bill the account to measure it", a.Operation)
		}
		return ""
	}
	return fmt.Sprintf("%s uses %s, which this replay has no rule for", a.Operation, a.Method)
}

// After records what the answer created.
func (g *accountGuard) After(a replay.Attempt, status int, body any) {
	if a.Method != http.MethodPost && a.Method != http.MethodPut {
		return
	}
	if status < 200 || status >= 300 {
		return
	}
	for _, id := range mintedIDs(body) {
		g.owned[id] = true
	}
	if id, named := objectID(body); named {
		g.created = append(g.created, createdObject{collection: a.Path, id: id, operation: a.Operation})
	}
}

// noteSynthetic records the values the sanitiser invented that survived into the
// request. A rebound identifier is not one of them: it is whatever the endpoint
// answered a moment ago.
//
// # The path is where this earns its place
//
// A corpus is a causal sequence: the create mints the identifier the read then
// addresses. When the create does not happen — refused as billable, refused as
// somebody else's, or simply not sent under --dry-run — nothing rebinds it, and
// the read goes out carrying the *sanitiser's* identifier. It answers 404, and
// every field of the recorded body then reads as absent.
//
// Measured on 2026-08-21, dry-running corpus/scaleway/terraform.jsonl against
// the real Scaleway account: 145 findings, not one of them the cloud. Grading
// those as "the provider changed" would be this tool committing the very defect
// it exists to detect — #73's family, one layer further out.
// TestARequestStillCarryingASyntheticIdentifierIsNotACloudChange fails without
// this.
func (g *accountGuard) noteSynthetic(a replay.Attempt) {
	seen := map[string]bool{}
	var out []string
	note := func(s string) {
		if s == "" || seen[s] || !corpus.Minted(s) {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	walkStrings(a.Body, note)
	for _, seg := range strings.Split(a.Path, "/") {
		id, _, _ := strings.Cut(seg, ":")
		note(id)
	}
	for _, pair := range strings.Split(a.Query, "&") {
		if _, value, ok := strings.Cut(pair, "="); ok {
			note(value)
		}
	}
	if len(out) > 0 {
		sort.Strings(out)
		g.synthetic[a.Seq] = out
	}
}

// mintedIDs is every identifier an answer names under a key called exactly "id".
//
// "id" and nothing else, and that narrowness is the control: a create answers
// project_id, organization_id and vpc_id beside its own id, and a guard that
// collected those would consider the account's project something this run
// created — which is the door a `DELETE /projects/{id}` would walk through.
// TestOwnershipDoesNotFollowAParentIdentifier fails without this.
func mintedIDs(body any) []string {
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, nested := range t {
				if s, isString := nested.(string); k == "id" && isString && shape.IsMintedIdentifier(s) {
					out = append(out, s)
					continue
				}
				walk(nested)
			}
		case []any:
			for _, nested := range t {
				walk(nested)
			}
		}
	}
	walk(body)
	return out
}

// objectID names the object a create answered, for the ledger.
//
// The top-level "id", or the "id" of the single object a one-key answer wraps —
// Scaleway's vpc/v2 answers the first shape and its instance API the second. An
// answer of neither shape leaves nothing in the ledger, and [accountGuard.destroy]
// says so rather than pretending the run created nothing.
func objectID(body any) (string, bool) {
	top, isObject := body.(map[string]any)
	if !isObject {
		return "", false
	}
	if s, isString := top["id"].(string); isString && shape.IsMintedIdentifier(s) {
		return s, true
	}
	if len(top) != 1 {
		return "", false
	}
	for _, nested := range top {
		if inner, wrapped := nested.(map[string]any); wrapped {
			if s, isString := inner["id"].(string); isString && shape.IsMintedIdentifier(s) {
				return s, true
			}
		}
	}
	return "", false
}

// destroy empties the account and proves it with a read.
//
// Never the exit code of the DELETE, which is #352's rule and the reason it is
// one: a delete that answered 204 on a resource that then takes a minute to go
// is a delete that proved nothing. The proof is a GET that answers 404. Walked
// backwards, so a private network goes before the VPC that holds it.
//
// Returns how many were proved gone, and the URL of everything that was not.
func (g *accountGuard) destroy(client *http.Client, endpoint string) (int, []string) {
	proved := 0
	var leftovers []string
	for i := len(g.created) - 1; i >= 0; i-- {
		o := g.created[i]
		url := strings.TrimSuffix(endpoint, "/") + o.url()
		if req, err := http.NewRequest(http.MethodDelete, url, nil); err == nil {
			if res, err := client.Do(req); err == nil {
				_ = res.Body.Close()
			}
		}
		gone := false
		if res, err := client.Get(url); err == nil {
			_ = res.Body.Close()
			gone = res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone
		}
		if gone {
			proved++
			continue
		}
		leftovers = append(leftovers, o.operation+" "+o.url())
	}
	return proved, leftovers
}

// parseKeyValues reads a repeatable `key=value` flag into a map, refusing a
// spelling that would silently mean nothing.
func parseKeyValues(flagName string, raw []string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(raw))
	for _, entry := range raw {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("--%s %q: expected key=value with both halves", flagName, entry)
		}
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return out, nil
}

// repeatable is a flag that may be given more than once, since the standard
// flag package has no such thing and a comma-separated string would forbid a
// comma in a value.
type repeatable []string

func (r *repeatable) String() string { return strings.Join(*r, ",") }

func (r *repeatable) Set(v string) error {
	*r = append(*r, v)
	return nil
}

// compile-time proof that the guard is one.
var _ replay.Guard = (*accountGuard)(nil)
