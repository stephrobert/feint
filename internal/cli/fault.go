package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// `feint fault`: the verb over the fault injector the emulator already serves
// (#26, #356, #851).
//
//	feint fault add instance/v1/API.ListServers --status 503 --nth 3
//	feint fault list
//	feint fault clear
//
// The endpoint was reachable with curl alone, which is the same as saying it
// was reachable by whoever had already read internal/core/emulator/faults.go.
// A campaign is written by hand in a shell script, and a shell script that
// composes JSON by hand is the thing this repository refuses elsewhere.
//
// ## Why `add` reads before it writes
//
// PUT /_feint/faults REPLACES the whole armed set — deliberately, so that a
// suite arming its rules gets the same emulator whatever ran before it. There
// is no append on the wire. So `add` does GET, appends one rule to what came
// back, and PUTs the whole set again. Anything else would silently disarm every
// rule a previous `add` set, which is the surprise this paragraph exists to
// stop a future reader from rediscovering.
//
// TestAddKeepsWhatWasAlreadyArmed fails with the read dropped.
//
// Two consequences a caller should know, rather than find out:
//
//   - the receipt is cleared by any write, because the emulator clears it with
//     the set (faultSet.replace). So `add` ends a campaign's receipt. `list`
//     is what reads one.
//   - two `add` calls racing each other can lose a rule, the way any
//     read-modify-write over HTTP can. One operator at one terminal is the
//     population here; a suite that needs atomicity writes the whole document
//     with one PUT.
//
// ## Why the refusals come back whole
//
// Every bound of a rule is checked by the emulator when the rule is written —
// an unmounted operation, a status the pack renders in no dialect, `nth` with
// `times` — and each refusal arrives as a sentence saying which and why. This
// verb prints that sentence and exits 1. It does not re-implement the checks:
// a second copy of a bound is a copy that disagrees with the first the week
// after. TestAddRendersTheApisRefusal fails if the body is printed raw.

// faultTimeout bounds a call to the admin plane. The endpoint touches no
// runtime and no disk, so a slow answer is a dead emulator rather than a busy
// one.
const faultTimeout = 30 * time.Second

// faultRule is one rule, in both directions.
//
// The same struct serves the PUT and the GET on purpose: the emulator
// documents that round-tripping its own answer back into a write must not be
// an error, and one struct is how that stays true. The counters carry
// omitempty so a rule this verb composes sends only what a caller asked for.
type faultRule struct {
	Operation string `json:"operation"`
	Status    int    `json:"status,omitempty"`
	Delay     string `json:"delay,omitempty"`
	Times     int    `json:"times,omitempty"`
	Nth       int    `json:"nth,omitempty"`
	Truncate  *int   `json:"truncate_bytes,omitempty"`
	Transport string `json:"transport,omitempty"`

	// Read back from the emulator, never meant by a caller. Hits is what the
	// rule answered, Calls every call it saw; under --nth the two differ by
	// construction.
	Hits  int  `json:"hits,omitempty"`
	Calls int  `json:"calls,omitempty"`
	Spent bool `json:"spent,omitempty"`
}

// firedFault is one line of the receipt: which operation was answered, at which
// rank among the calls its rule saw, with what status.
type firedFault struct {
	Operation string `json:"operation"`
	Rank      int    `json:"rank"`
	Status    int    `json:"status"`
	// Abandoned says the client left before the answer reached it, which a
	// delayed rule makes ordinary. Printed as words, because `answered 0` is
	// not a status anybody can read.
	Abandoned bool `json:"abandoned"`
	// Transport is "close" or "hang" when the failure happened below HTTP,
	// and empty otherwise. It is what tells `status 0, abandoned` caused by a
	// dropped connection from the same line caused by a client that gave up
	// during a delay.
	Transport string `json:"transport,omitempty"`
	// InFlight says the firing has not finished, so Status and Abandoned still
	// hold their zero values (FaultsSchemaVersion 4, #925). Transport does
	// not: it is named at the hijack, which is what the hang branch of
	// describeFiredLine below relies on.
	//
	// It is read here and not only on the wire because this CLI is the
	// receipt's human reader: without it, `feint fault list` fell through to
	// the status branch and printed `answered 0` for a request that had not
	// been answered at all. A contract review measured that against a live
	// emulator while the HTTP surface was already saying `in_flight: true`.
	InFlight bool `json:"in_flight,omitempty"`
}

// describeFiredLine is the one sentence a receipt line is worth, shared by
// `feint fault list` and by a run's receipt so the two cannot drift apart.
//
// In flight comes FIRST and before the transport switch, because a held
// connection carries `transport: "hang"` for as long as it is held: deciding
// on the transport alone would describe a connection this emulator is still
// holding in the past tense.
func describeFiredLine(line firedFault) string {
	if line.InFlight {
		if line.Transport == emulator.TransportHang {
			return "the connection is held open right now, no answer yet"
		}
		return "still in flight, no answer yet"
	}
	switch line.Transport {
	case emulator.TransportClose:
		return "the connection was dropped, no answer sent"
	case emulator.TransportHang:
		return "the connection was held open, no answer sent"
	}
	if line.Abandoned {
		return "the client left before the answer"
	}
	return fmt.Sprintf("answered %d", line.Status)
}

// faultsDocument is what /_feint/faults answers to all three methods.
type faultsDocument struct {
	SchemaVersion int          `json:"schema_version"`
	Faults        []faultRule  `json:"faults"`
	Fired         []firedFault `json:"fired"`
	// FiredDropped is how many firings the emulator's cap refused. Printed
	// when it is not zero, so a reader of 256 lines is not left believing the
	// receipt is the whole story.
	FiredDropped int `json:"fired_dropped"`
}

func faultCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "feint: fault needs a subcommand: list, add or clear")
		return exitError
	}
	switch args[0] {
	case "list", "ls":
		return faultList(args[1:], stdout, stderr)
	case "add":
		return faultAdd(args[1:], stdout, stderr)
	case "clear":
		return faultClear(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "feint: unknown fault subcommand %q; expected list, add or clear\n", args[0])
		return exitError
	}
}

// faultList prints what is armed and what has fired.
func faultList(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("fault list")
	addr := fs.String("addr", DefaultAddr, "listen address of the emulator to read")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "feint fault list: unexpected argument %q\n", fs.Arg(0))
		return exitError
	}
	document, err := callFaults(http.MethodGet, "http://"+*addr+"/_feint/faults", nil)
	if err != nil {
		fmt.Fprintf(stderr, "feint fault list: %v\n", err)
		return exitError
	}
	reportFaults(stdout, document)
	return exitOK
}

// Three consequences a caller must know, and the limits page states them:
// the receipt is cleared, two `add` at once can lose a rule, and **every
// armed rule's call counter goes back to zero**, so a `--nth` campaign in
// flight restarts. TestArmingAgainRestartsEveryCampaign, in
// package internal/core/emulator, holds the third.
// faultAdd arms one more rule, keeping what is already armed.
func faultAdd(args []string, stdout, stderr io.Writer) int {
	// The operation comes first and the flags behind it, the way `feint
	// checkpoint <name> --addr …` already reads: Go's flag package stops at
	// the first non-flag argument, so an operation parsed by it would swallow
	// every flag behind it into the positionals.
	//
	// The set is built BEFORE the operation is taken off, because `feint fault
	// add -h` must print the flags, and the frozen surface observes the set
	// this builds.
	fs := newFlagSet("fault add")
	addr := fs.String("addr", DefaultAddr, "listen address of the emulator to arm")
	status := fs.Int("status", 0, "answer this HTTP status instead of calling the handler")
	delay := fs.Duration("delay", 0, "wait this long before answering (\"750ms\", \"4s\")")
	nth := fs.Int("nth", 0, "answer the Nth call to this operation and no other, counting from one")
	times := fs.Int("times", 0, "answer this many calls, then stop firing; 0 is every call")
	truncate := fs.Int("truncate", 0, "cut the handler's own answer to this many bytes")
	dropConn := fs.Bool("close", false, "drop the connection instead of answering, with a reset")
	hangConn := fs.Bool("hang", false, "hold the connection open and never answer")
	seed := fs.Int("seed", 0, "refused: see the message it prints, and use --nth instead")
	if len(args) != 0 && strings.HasPrefix(args[0], "-") {
		// -h, --help, or a mistyped flag: the flag set is the only thing that
		// can print the usage, so let it answer.
		if err := fs.Parse(args); err != nil {
			return exitError
		}
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, "feint fault add: name one operation: feint fault add <operation> [flags]")
		fmt.Fprintln(stderr, "  GET /_feint/routes lists the operations this emulator serves.")
		return exitError
	}
	operation := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return exitError
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "feint fault add: unexpected argument %q; one operation, then flags\n", fs.Arg(0))
		return exitError
	}

	if wasSet(fs, "seed") {
		reportNoSeed(stderr, *seed)
		return exitError
	}
	if *dropConn && *hangConn {
		fmt.Fprintln(stderr, "feint fault add: --close and --hang are two different failures: one drops the")
		fmt.Fprintln(stderr, "  connection, the other holds it open and never answers. Arm one.")
		return exitError
	}

	rule := faultRule{Operation: operation, Status: *status, Times: *times, Nth: *nth}
	switch {
	case *dropConn:
		rule.Transport = emulator.TransportClose
	case *hangConn:
		rule.Transport = emulator.TransportHang
	}
	if *delay != 0 {
		rule.Delay = delay.String()
	}
	// Only when it was typed: zero is a legal truncation — an empty body — so
	// the default cannot stand in for "no truncation asked for".
	if wasSet(fs, "truncate") {
		cut := *truncate
		rule.Truncate = &cut
	}

	endpoint := "http://" + *addr + "/_feint/faults"
	// Read first. PUT replaces the whole set, so what is already armed has to
	// be sent back with the new rule or it is disarmed.
	armed, err := callFaults(http.MethodGet, endpoint, nil)
	if err != nil {
		fmt.Fprintf(stderr, "feint fault add: %v\n", err)
		return exitError
	}
	document, err := callFaults(http.MethodPut, endpoint, append(armed.Faults, rule))
	if err != nil {
		fmt.Fprintf(stderr, "feint fault add: %v\n", err)
		return exitError
	}
	fmt.Fprintf(stdout, "armed %s (%d rule(s) now armed)\n", operation, len(document.Faults))
	reportFaults(stdout, document)
	return exitOK
}

// faultClear disarms everything, back to the state every emulator starts in.
func faultClear(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("fault clear")
	addr := fs.String("addr", DefaultAddr, "listen address of the emulator to disarm")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "feint fault clear: unexpected argument %q\n", fs.Arg(0))
		return exitError
	}
	if _, err := callFaults(http.MethodDelete, "http://"+*addr+"/_feint/faults", nil); err != nil {
		fmt.Fprintf(stderr, "feint fault clear: %v\n", err)
		return exitError
	}
	fmt.Fprintln(stdout, "no fault is armed")
	return exitOK
}

// reportFaults prints the armed set and the receipt.
//
// "no fault is armed" is printed rather than nothing at all: an empty answer
// and an emulator that did not understand the question look identical, and the
// one question an operator meeting a 500 asks is whether anything was staged.
func reportFaults(stdout io.Writer, document faultsDocument) {
	if len(document.Faults) == 0 {
		fmt.Fprintln(stdout, "no fault is armed")
	}
	rules := append([]faultRule(nil), document.Faults...)
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Operation < rules[j].Operation })
	for _, rule := range rules {
		fmt.Fprintf(stdout, "  %s: %s\n", rule.Operation, describeFault(rule))
	}
	if len(document.Fired) == 0 {
		return
	}
	if document.FiredDropped > 0 {
		fmt.Fprintf(stdout, "fired (%d shown, %d more the receipt could not hold):\n",
			len(document.Fired), document.FiredDropped)
	} else {
		fmt.Fprintf(stdout, "fired (%d):\n", len(document.Fired))
	}
	for _, line := range document.Fired {
		fmt.Fprintf(stdout, "  %s call %d: %s\n", line.Operation, line.Rank, describeFiredLine(line))
	}
}

// describeFault says what one rule does and what it has done, in one line.
// reportNoSeed refuses a seeded campaign, and says what was measured rather
// than that it is unsupported.
//
// #851 asked for campaigns whose decisions replay from a seed, and bound the
// ask itself: the outcome must be a pure function of stable inputs, and "if no
// stable request identity exists, the CLI must say so rather than pretend
// reproducibility". None exists here, and that is a measurement, not an
// opinion: across the 1088 exchanges committed under corpus/, the complete set
// of request header names the official clients send is nine — accept,
// accept-encoding, authorization, connection, content-length, content-type,
// user-agent, x-amz-date, x-auth-token — and not one of them is minted by the
// client per call. The measurement is not a note: it is recomputed over the
// committed recordings by TestTheCorpusCarriesNoClientMintedRequestIdentity,
// which fails the day a recording carries a tenth name, so this refusal
// cannot outlive its own premise.
//
// The only input left is arrival order, which is exactly what the issue
// forbids a seed from resting on: two concurrent clients reorder it between
// runs, and a seed that replays only under one sequential client is a seed
// that lies about what it guarantees.
//
// So the flag exists and refuses. A flag that is simply absent teaches nothing
// — the next person writes a shell loop around arrival order and believes it
// replays — and a sentence in a source comment is not something a caller ever
// reads. TestSeedIsRefusedWithItsMeasurement fails if this becomes silent.
func reportNoSeed(stderr io.Writer, seed int) {
	fmt.Fprintf(stderr, "feint fault add: --seed %d is refused, and this is measured rather than "+
		"unimplemented.\n", seed)
	fmt.Fprintln(stderr, "  A seeded campaign must decide from something stable about the request. Across the")
	fmt.Fprintln(stderr, "  1088 exchanges recorded under corpus/, the official clients send nine distinct")
	fmt.Fprintln(stderr, "  request header names, and none is minted per call: there is no request identity")
	fmt.Fprintln(stderr, "  to seed from.")
	fmt.Fprintln(stderr, "  What is left is arrival order, which two concurrent clients reorder between runs.")
	fmt.Fprintln(stderr, "  A seed resting on it would replay under one sequential client and nowhere else.")
	fmt.Fprintln(stderr, "  Use --nth to name a call by its rank, and read the receipt for what actually fired.")
}

func describeFault(rule faultRule) string {
	var parts []string
	if rule.Status != 0 {
		parts = append(parts, fmt.Sprintf("status %d", rule.Status))
	}
	if rule.Truncate != nil {
		parts = append(parts, fmt.Sprintf("truncated to %d byte(s)", *rule.Truncate))
	}
	if rule.Delay != "" {
		parts = append(parts, "delay "+rule.Delay)
	}
	switch rule.Transport {
	case emulator.TransportClose:
		parts = append(parts, "connection dropped")
	case emulator.TransportHang:
		parts = append(parts, "never answers")
	}
	switch {
	case rule.Nth > 0:
		parts = append(parts, fmt.Sprintf("on call %d only", rule.Nth))
	case rule.Times > 0:
		parts = append(parts, fmt.Sprintf("%d time(s)", rule.Times))
	default:
		parts = append(parts, "every call")
	}
	// Both counters, because under --nth they differ by construction: calls is
	// every call the rule saw, hits the subset it answered.
	parts = append(parts, fmt.Sprintf("%d hit(s) of %d call(s)", rule.Hits, rule.Calls))
	if rule.Spent {
		parts = append(parts, "spent")
	}
	return strings.Join(parts, ", ")
}

// callFaults drives /_feint/faults and renders a refusal as the sentence the
// emulator wrote.
//
// The emulator answers every refusal as {"error": "<sentence>"}, and those
// sentences are the whole value of the endpoint's validation: which bound was
// crossed, and what to use instead. Printing the JSON document would hand the
// operator the envelope and keep the letter.
func callFaults(method, url string, rules []faultRule) (faultsDocument, error) {
	var body io.Reader
	if rules != nil {
		encoded, err := json.Marshal(map[string]any{"faults": rules})
		if err != nil {
			return faultsDocument{}, err
		}
		body = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(context.Background(), faultTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return faultsDocument{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return faultsDocument{}, fmt.Errorf("the emulator did not answer: %w", err)
	}
	defer func() { _ = res.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(res.Body, maxStateBody))
	if err != nil {
		return faultsDocument{}, fmt.Errorf("the emulator's answer could not be read: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		var refusal struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &refusal) == nil && refusal.Error != "" {
			return faultsDocument{}, fmt.Errorf("%s", refusal.Error)
		}
		return faultsDocument{}, fmt.Errorf("the emulator answered %d", res.StatusCode)
	}
	var document faultsDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return faultsDocument{}, fmt.Errorf("the emulator's answer is not a fault document: %w", err)
	}
	return document, nil
}
