package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
)

// The campaign half of `feint run` (#851).
//
// #851 asks that `feint run` be able to arm a campaign for the child process
// and print the resulting fault receipt. Both halves matter, and the second is
// the one that is easy to drop: a run that injects failures and reports only
// the child's exit code tells a reader that something went wrong without
// telling them that *we* were the ones who broke it. The receipt is what
// separates "the client has a bug" from "the client met the 503 we armed on
// call three and did not retry".
//
// ## Why a file rather than flags
//
// `feint fault add` composes one rule from flags, which is right at a terminal.
// A campaign is several rules that belong together and that a suite replays
// next month, so it belongs in a file the suite commits beside its own
// fixtures. It is also the only shape that replays exactly: the document is
// what PUT /_feint/faults takes, so what was armed is what the file says and
// nothing is reconstructed from a command line.
//
// Not byte for byte, and the first version of this comment said it was. The
// file is decoded strictly and the decoded rules are marshalled again by
// callFaults, so a zero-valued key the file spells out is dropped on the way.
// Semantically identical today; the claim was still false, and a false claim
// about what crosses a boundary is how a reader builds on something that is
// not there.
//
// There is deliberately no --seed here, for the reason reportNoSeed prints:
// nothing an official client sends carries a stable request identity, so a
// seeded decision would rest on arrival order, which concurrent clients
// reorder. A committed file replays; a seed would only look as if it did.

// faultCampaign is a campaign file: the document PUT /_feint/faults takes.
//
// Decoded strictly. A misspelled key in a campaign is a rule that silently
// does less than its author meant, and a fault that does not fire reads from
// the outside exactly like a client that survived it, which is the confusion
// the whole fault seam is built to avoid.
type faultCampaign struct {
	Faults []faultRule `json:"faults"`
}

// loadFaultCampaign reads and decodes the file, and refuses an empty one.
//
// An empty campaign is refused rather than treated as "arm nothing": somebody
// who passed --fault asked for failures, and a run that silently injected none
// would hand them a green result they would believe.
func loadFaultCampaign(path string) ([]faultRule, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the operator names their own campaign file
	if err != nil {
		return nil, fmt.Errorf("--fault %s: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var campaign faultCampaign
	if err := decoder.Decode(&campaign); err != nil {
		return nil, fmt.Errorf("--fault %s: %w\n  A campaign file is the document PUT /_feint/faults takes: "+
			"{\"faults\": [{\"operation\": \"…\", \"status\": 503, \"nth\": 3}]}", path, err)
	}
	if len(campaign.Faults) == 0 {
		return nil, fmt.Errorf("--fault %s names no rule: a campaign that arms nothing would make this run "+
			"look like a client that survived every failure", path)
	}
	return campaign.Faults, nil
}

// armFaultCampaign writes the campaign to the lab, and fails the run when it
// is refused.
//
// Every bound of a rule is checked by the emulator at the moment it is written
// — an unmounted operation, a status the pack renders in no dialect, nth with
// times — and this reports that sentence rather than re-deriving it. A refused
// campaign stops the run before the child starts, which is this command's rule
// everywhere else: a refusal four minutes in has already cost what it exists
// to save.
func armFaultCampaign(target string, rules []faultRule) error {
	if err := refuseTransportBelowItsSchema(target, rules); err != nil {
		return err
	}
	document, status, err := callFaultsStatus(http.MethodPut, faultsEndpoint(target), rules)
	switch {
	case err == nil:
	case status == http.StatusOK:
		// Applied, then answered with something that is not a fault document
		// (a proxy's HTML page, a truncated body). The lab holds the rules.
		// TestARunClearsACampaignWhoseAnswerDoesNotDecode fails without this.
		return fmt.Errorf("arming the campaign: %w; refused before the command started, %s",
			err, clearArmed(target))
	case status == 0:
		// Nothing answered, which includes a PUT that timed out after the lab
		// applied it: this command cannot know, so it says so.
		return fmt.Errorf("arming the campaign: %w; it may be armed there: `feint fault clear --addr %s` clears it",
			err, strings.TrimPrefix(strings.TrimPrefix(target, "http://"), "https://"))
	default:
		// Refused: the lab armed nothing.
		return fmt.Errorf("arming the campaign: %w", err)
	}
	if len(document.Faults) != len(rules) {
		// Unreachable through the endpoint, which replaces the whole set with
		// what it was sent. Said rather than assumed: a run whose campaign was
		// partly armed would report failures nobody asked for, or miss the one
		// the suite is asserting on.
		// TestARunClearsACampaignArmedWithAnotherRuleCount fails without the clear.
		return fmt.Errorf("arming the campaign: %d rule(s) were sent and %d are armed; "+
			"refused before the command started, %s", len(rules), len(document.Faults), clearArmed(target))
	}
	// The lab answers the rules it armed, and a lab of another build can arm
	// a different rule than the one sent while answering 200 (#968). Measured
	// on 2026-11-16 against the released v0.16.0 binary, which serves faults
	// schema 1: a rule with `"nth": 2` was accepted and echoed without `nth`,
	// and the child met the 503 on calls 1, 2 and 3. A campaign that fires on
	// every call where it asked for the second is a run that tests something
	// else, and its receipt would say so to nobody.
	//
	// So each rule the lab echoes is compared with the rule sent, and a knob
	// the lab did not keep stops the run before the child starts. The rules
	// it did arm are cleared first: on an attached lab they would otherwise
	// meet the next client.
	//
	// TestARunRefusesACampaignTheLabArmedDifferently fails without this, and
	// TestARunArmsEveryKnobOnTheEmulatorItShipsWith if it refuses a lab that
	// kept every knob.
	for i, sent := range rules {
		echoed := document.Faults[i]
		lost := knobsNotKept(sent, echoed)
		if len(lost) == 0 {
			continue
		}
		what := fmt.Sprintf("armed rule %d (%s) without %s", i+1, sent.Operation, strings.Join(lost, " and "))
		if sent.Operation != echoed.Operation {
			what = fmt.Sprintf("answered rule %d (%s) as %s", i+1, sent.Operation, echoed.Operation)
		}
		older := ""
		if document.SchemaVersion < faultsSchemaTransport {
			older = " Every release up to v0.16.0 serves schema 1, which has no nth and no transport."
		}
		return fmt.Errorf("arming the campaign: the lab %s, so it would not do what the campaign says; "+
			"refused before the command started, %s.\n  This lab %s on /_feint/faults.%s "+
			"Use an emulator from the same release as this feint CLI",
			what, clearArmed(target), labSchemaWords(document.SchemaVersion), older)
	}
	return nil
}

// clearArmed clears what a refused campaign left armed, and answers the words
// the refusal ends with. A failed clear is said, never reported as done.
// TestARefusalWhoseClearFailsSaysSo fails if the failure reads as a success.
func clearArmed(target string) string {
	if _, err := callFaults(http.MethodDelete, faultsEndpoint(target), nil); err != nil {
		return fmt.Sprintf("and what it armed could NOT be cleared (%v): it is still armed there", err)
	}
	return "and cleared what it had armed"
}

// labSchemaWords names the lab's faults schema the way every refusal here
// does, and never as `schema_version 0`: a document without the key did not
// say what it is.
func labSchemaWords(schema int) string {
	if schema <= 0 {
		return "does not name its schema_version"
	}
	return fmt.Sprintf("serves schema_version %d", schema)
}

// refuseTransportBelowItsSchema says, before anything is armed, that a rule
// asking for a transport failure needs a lab at faultsSchemaTransport or later.
//
// The lab's own answer misleads: measured on 2026-11-16, the released v0.16.0
// binary (schema 1) refuses `{"transport":"close"}` with `rule "…" asks for
// nothing: set a status, a truncation or a delay`, which sends a reader to
// look for a typo in a rule that is correct for this CLI. So the schema is read
// first, and only when a rule carries a transport: a campaign without one
// costs no extra round trip. A document that does not name its schema is
// below 3 too, because it promises nothing.
//
// TestARunSaysTransportNeedsSchema3BeforeArming fails without this, and
// TestARunArmsTransportOnASchema3Lab if the bound is off by one.
func refuseTransportBelowItsSchema(target string, rules []faultRule) error {
	index := slices.IndexFunc(rules, func(r faultRule) bool { return r.Transport != "" })
	if index < 0 {
		return nil
	}
	current, err := callFaults(http.MethodGet, faultsEndpoint(target), nil)
	if err != nil {
		return fmt.Errorf("arming the campaign: reading the lab's faults schema first: %w", err)
	}
	if current.SchemaVersion < faultsSchemaTransport {
		which := labSchemaWords(current.SchemaVersion)
		return fmt.Errorf("arming the campaign: rule %d (%s) asks for transport %q, which needs /_feint/faults "+
			"schema_version %d or later, and this lab %s; nothing was armed.\n"+
			"  Every release up to v0.16.0 serves schema 1 and answers such a rule with \"asks for nothing\". "+
			"Use an emulator from the same release as this feint CLI",
			index+1, rules[index].Operation, rules[index].Transport, faultsSchemaTransport, which)
	}
	return nil
}

// knobsNotKept names the knobs a rule was sent with that the lab's echo does
// not carry with the same value. Status, delay, times and nth are compared
// even when the campaign left them at zero: a lab that arms a knob nobody
// asked for (a default 503 on a transport-only rule) does not do what the
// campaign says either. The delay is compared as text: the
// emulator of this tree echoes it verbatim (faultView.Delay is the string
// it was sent), so a duration parse would be tolerance no lab needs.
func knobsNotKept(sent, armed faultRule) []string {
	var lost []string
	if sent.Operation != armed.Operation {
		lost = append(lost, "its operation")
	}
	if sent.Status != armed.Status {
		lost = append(lost, "status")
	}
	if sent.Delay != armed.Delay {
		lost = append(lost, "delay")
	}
	if sent.Times != armed.Times {
		lost = append(lost, "times")
	}
	if sent.Nth != armed.Nth {
		lost = append(lost, "nth")
	}
	if sent.Truncate != nil && (armed.Truncate == nil || *armed.Truncate != *sent.Truncate) {
		lost = append(lost, "truncate_bytes")
	}
	if sent.Transport != "" && sent.Transport != armed.Transport {
		lost = append(lost, "transport")
	}
	return lost
}

// ruleHits is one armed rule as the run receipt reports it: what the rule
// answered, which every schema publishes, schema 1 included (#968).
type ruleHits struct {
	Operation string `json:"operation"`
	Hits      int    `json:"hits"`
	Spent     bool   `json:"spent"`
}

// readFaultState reads what the LAB had armed and what it did, before the
// teardown takes the lab away.
//
// It is read on every run, not only on a run that armed a campaign, and that
// is the correction to a defect a review reproduced. `--endpoint` names a lab
// this command does not own; a campaign armed on one is still armed after the
// run exits, because nothing cleared it. A second run against the same lab,
// with no --fault at all, had its child answered by the first run's 503 and
// published a receipt saying `faults_armed: false`. Two views of one run
// contradicting each other, in the artefact a suite reads.
//
// So the receipt reports what the lab had, which is the only thing it can
// honestly claim. `Server.FaultsArmed` exists for this question and had no
// caller anywhere; this is it, over HTTP, because the asker is a separate
// process.
//
// Best effort, like callsSoFar beside it and for the same reason: a receipt is
// not worth failing a run that otherwise succeeded. The difference shows in the
// render, which says the state could not be read rather than printing an empty
// one — an empty receipt and an unread receipt are different facts, and "no
// fault fired" is the one a reader would act on.
//
// schema is the document's schema_version, handed on rather than dropped
// (#928): below schema 2 the lab publishes no receipt, and renderFaultReceipt
// states it rather than read an absent receipt as an empty one.
//
// rules are the armed rules with their hit counts (#968): the one positive
// answer a schema 1 lab gives, and published at every schema so the JSON has
// one shape whatever the lab.
func readFaultState(target string) (rules []ruleHits, fired []firedFault, dropped, schema int, ok bool) {
	document, err := callFaults(http.MethodGet, faultsEndpoint(target), nil)
	if err != nil {
		return nil, nil, 0, 0, false
	}
	for _, rule := range document.Faults {
		rules = append(rules, ruleHits{Operation: rule.Operation, Hits: rule.Hits, Spent: rule.Spent})
	}
	return rules, document.Fired, document.FiredDropped, document.SchemaVersion, true
}

// disarmFaultCampaign clears what this run armed.
//
// Only what this run armed: a run that armed nothing never touches the lab's
// rules, because clearing someone else's campaign would be the same
// over-reach in the other direction.
//
// Errors are reported and never fail the run: the child has already finished,
// and a teardown that fails a passing run teaches the wrong lesson. The
// receipt is read before this, so what it reports is unaffected.
func disarmFaultCampaign(target string, stderr io.Writer) {
	if _, err := callFaults(http.MethodDelete, faultsEndpoint(target), nil); err != nil {
		fmt.Fprintf(stderr, "feint run: the campaign could not be cleared from %s: %v\n", target, err)
		fmt.Fprintln(stderr, "  It is still armed there, and the next client to call those operations will meet it.")
	}
}

// faultsEndpoint builds the admin URL from the endpoint `feint run` holds,
// which is a URL rather than the host:port `feint fault` takes.
func faultsEndpoint(target string) string {
	return strings.TrimSuffix(target, "/") + "/_feint/faults"
}

// renderFaultReceipt prints the campaign's lines under the run receipt.
//
// It prints the heading even when nothing fired, because "armed a campaign and
// nothing fired" is a result a reader must see: it means the operations named
// were never called, which is a defect in the campaign rather than resilience
// in the client.
func renderFaultReceipt(w interface{ Write([]byte) (int, error) }, r runReceipt) {
	if !r.FaultsRead {
		// Said on every run, armed or not. Silence here would read as "no fault
		// was armed", which is the one thing an unread state cannot establish.
		fmt.Fprintln(w, "  faults         the lab's fault state could not be read")
		return
	}
	if !r.FaultsArmed {
		return
	}
	if len(r.Faults) == 0 {
		who := "the lab had a campaign armed"
		if r.FaultsCampaign {
			who = "armed"
		}
		// "None fired" is drawn from an empty receipt, and only a lab that
		// publishes a receipt can make it mean anything (#928). Below
		// faultsSchemaReceipt, which is every release up to v0.16.0, `fired` is
		// absent, so its emptiness says nothing about whether a rule fired.
		// What schema 1 does publish is a hit count per rule, and since #968
		// that is what is printed: "0 hit(s)" is a rule that answered nothing,
		// read from the lab rather than from an absence.
		//
		// TestARunReceiptAgainstASchema1LabCountsEachRule fails without the
		// per-rule lines.
		//
		// TestARunReceiptAgainstALabWithoutAReceiptClaimsNothing fails without
		// this branch, TestARunReceiptThatNamesNoSchemaClaimsNothing if a
		// missing schema_version is read as a receipt, and
		// TestARunReceiptWithAnEmptyReceiptStillSaysNoneFired if the branch
		// reaches a lab that does publish one.
		if r.FaultsSchema < faultsSchemaReceipt {
			which := fmt.Sprintf("schema_version %d", r.FaultsSchema)
			if r.FaultsSchema <= 0 {
				which = "it does not name its schema_version"
			}
			fmt.Fprintf(w, "  faults         %s, but this lab publishes no per-call receipt (%s), "+
				"only a count per rule\n", who, which)
			for _, rule := range r.FaultRules {
				spent := ""
				if rule.Spent {
					spent = ", spent"
				}
				fmt.Fprintf(w, "    %s: %d hit(s)%s\n", rule.Operation, rule.Hits, spent)
			}
			return
		}
		fmt.Fprintf(w, "  faults         %s, and none fired: no call reached the operations named\n", who)
		return
	}
	dropped := ""
	if r.FaultsDropped > 0 {
		dropped = fmt.Sprintf(", %d more the receipt could not hold", r.FaultsDropped)
	}
	whose := ""
	if !r.FaultsCampaign {
		// The lab had rules this run did not arm. Named, because a run that
		// met injected answers it never asked for is the surprise this field
		// exists to end.
		whose = " (armed on this lab before the run, not by it)"
	}
	fmt.Fprintf(w, "  faults         %d fired%s%s\n", len(r.Faults), whose, dropped)
	// The same sentence `feint fault list` prints, from the same function
	// (#928), above the lines it qualifies and on the receipt's own stream.
	// The lines themselves already went through deriveInFlight in callFaults,
	// so a firing still held when the child exited reads as in flight from
	// schema 2 on, and the rendering is exact there. What is left is a lab
	// whose document does not name its schema. The run's exit code is the
	// child's and does not move.
	//
	// TestARunReceiptSaysWhenTheLabNamesNoSchema fails without this print;
	// TestARunReceiptAgainstASchema2LabCarriesNoNote and
	// TestARunReceiptAgainstACurrentLabCarriesNoNote fail if it is printed
	// against a lab whose receipt is exact. Both are mutated in
	// tools/falsify/specs/an-older-emulator-is-named.json.
	if note := faultsSchemaNote(r.FaultsSchema); note != "" {
		fmt.Fprintf(w, "    %s\n", note)
	}
	for _, line := range r.Faults {
		fmt.Fprintf(w, "    %s call %d: %s\n", line.Operation, line.Rank, describeFiredLine(line))
	}
}
