package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/stephrobert/feint/internal/core/machine"
)

// The waste ledger (#426).
//
// Why it exists rather than a prose log. #316, #342, #375 and #386 each fixed
// one symptom of one family — an emulated object outliving the run that made it
// — and nobody could see the family, because every sighting lived in the log of
// a failed run and died with it. #386 was only understood because somebody read
// two failure logs side by side. A sweep that *counts* what it finds turns the
// cleanup into an instrument: you stop fixing leftovers one at a time and start
// seeing which mechanism produces them, and how often.
//
// One line per object, JSON, so it aggregates. `feint clean --format json` on
// its own answers "what is on this host now"; appended across runs
// (`feint clean --format json >> ledger.jsonl`) it answers the question that
// matters, which is which mechanism produces the most waste:
//
//	jq -s 'group_by(.why) | map({why: .[0].why, n: length})' ledger.jsonl
//
// It is deliberately not a committed artefact. docs/proxy.md states the rule
// this repository works under — name a path, a type, a status and a position,
// never a value — and a versioned file of what one workstation happened to be
// holding is a value. So nothing here is written into the tree, and no line
// carries a hostname, an absolute path or a routable address: an object's kind,
// the name the emulator itself derived for it, and what happened to it.

// leftoverRecord is one object a sweep found, and the five things a maintainer
// needs in order to act on it without re-reading a run log.
type leftoverRecord struct {
	// Run ties every line of one invocation together, so lines from different
	// runs can be counted apart after they are concatenated. It is a timestamp
	// and a random suffix: it identifies the run, never the station.
	Run string `json:"run"`
	// Kind and Name are what the object is and which one it is.
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Attribution is how this run knows the object is the emulator's, which is
	// the only thing that entitles it to touch it: the name prefix the emulator
	// derives, or the label it writes. "none" never appears on a removal.
	Attribution string `json:"attribution"`
	// Owner is the process owner where the host can say, which is the case that
	// decides whether this user may end a DHCP service at all.
	Owner string `json:"owner,omitempty"`
	// Stage says when in a run this was seen: at the doorstep, before anything
	// started, or during the sweep at the end.
	Stage string `json:"stage"`
	// Why is the finding this ledger exists for, and the third value is the one
	// no return code ever reveals.
	Why string `json:"why"`
	// Action is what this run did about it.
	Action string `json:"action"`
	// Row is the runtime's own record of an object, verbatim, and it appears
	// only where a repair reaches past the runtime's commands to remove one
	// (#455). Printing it is what makes the removal reversible by the operator
	// rather than by trust, so it belongs on the line that records the removal
	// and nowhere else.
	Row string `json:"row,omitempty"`
}

// The vocabulary, closed on purpose: a free-text field cannot be grouped, and a
// ledger that cannot be grouped names no mechanism.
const (
	// whyUnswept is an object found before anything was tried on it.
	whyUnswept = "not-attempted-yet"
	// whyRefused is a destruction that was attempted and said no. Visible in a
	// log already; here it is countable.
	whyRefused = "refused"
	// whySurvived is the one that is invisible everywhere else: the destruction
	// reported success and the object is still there on the next read. It is
	// found by reading the host after the sweep rather than by trusting the
	// sweep's own return, which is the whole method.
	whySurvived = "survived-a-successful-delete"
	// whyUnreadable is the third outcome a reader must have. A survey that
	// failed produces this and never an empty list: "I could not look" and
	// "there is nothing" are different facts, and reporting the first as the
	// second is how an inventory once called a live account empty.
	whyUnreadable = "could-not-look"
)

const (
	actionRemoved   = "removed"
	actionReported  = "reported-only"
	actionLeftStuck = "left-this-user-cannot-end-it"
	actionNone      = "none"
)

const (
	stageDoorstep = "doorstep"
	stageSweep    = "sweep"
	// stageClosing is the same question as the doorstep, asked once a run's
	// emulator has stopped rather than before it started (#521). It is a stage
	// of its own and not a flavour of the doorstep because the ledger exists to
	// name *which mechanism* produces waste: "a previous run left this" and
	// "the run that just ended left this" are the two different findings, and
	// counting them together loses the only one an operator can act on today.
	stageClosing = "closing"
)

// leftoverMoment says when in a run the machine-and-network question is being
// asked. It changes no verdict — the same objects refuse at both moments — and
// it changes every sentence, because the wrong sentence sends the reader to the
// wrong run.
//
// Measured on 2026-08-28: the incus-ovn leg of runtime-proof.yml failed on a
// GitHub runner nothing had ever touched with "a previous run left 0 machine(s)
// and 2 network(s) on this host", naming objects the very same job had created
// eight steps earlier. There was no previous run. A reader who believed the
// sentence would have gone looking at the schedule instead of at the ssh suites.
type leftoverMoment string

const (
	// momentInFlight is a run asking about itself, which is why the question is
	// not asked at all: see reportStuckLeftovers.
	momentInFlight leftoverMoment = ""
	// momentDoorstep is before anything of this run has started, so anything
	// found belongs to somebody else's run.
	momentDoorstep leftoverMoment = "doorstep"
	// momentClosing is after this run's emulator has stopped, so anything found
	// is this run's own leak — the form #521 gave `mise run conformance`, which
	// runtime-proof.yml had not been given.
	momentClosing leftoverMoment = "closing"
)

// asks reports whether this moment asks the machine-and-network question.
func (m leftoverMoment) asks() bool { return m == momentDoorstep || m == momentClosing }

// stage names the ledger stage of this moment.
func (m leftoverMoment) stage() string {
	if m == momentClosing {
		return stageClosing
	}
	return stageDoorstep
}

// newRunID identifies one invocation. Random rather than a counter because two
// runs may append to one file from two shells, and time alone collides.
func newRunID(now time.Time) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A ledger line is worth more than the uniqueness of its key, so a
		// failed read degrades to the timestamp alone rather than dropping the
		// record. It is not a security value.
		return now.UTC().Format(time.RFC3339)
	}
	return now.UTC().Format(time.RFC3339) + "/" + hex.EncodeToString(b[:])
}

// ledger writes the records of one invocation, in the format the caller asked
// for. Text is the default and is unchanged from what this command always
// printed, because tools/conformance/scaleway/network.sh reads those lines.
type ledger struct {
	out    io.Writer
	run    string
	asJSON bool
}

func newLedger(out io.Writer, asJSON bool, now time.Time) *ledger {
	return &ledger{out: out, run: newRunID(now), asJSON: asJSON}
}

// prose writes the human sentences this command has always printed, and only in
// text mode. JSON mode must stay parseable as JSON Lines end to end: a single
// prose line in the stream turns `jq -s` into an error, and a ledger a maintainer
// cannot pipe is a ledger nobody queries.
// TestTheLedgerIsParseableEndToEnd fails without this.
func (l *ledger) prose(format string, args ...any) {
	if l.asJSON {
		return
	}
	fmt.Fprintf(l.out, format, args...)
}

// record writes one line. Text mode prints the same sentence a human already
// read in this command's output; JSON mode prints the aggregatable form.
func (l *ledger) record(rec leftoverRecord) {
	rec.Run = l.run
	if !l.asJSON {
		fmt.Fprintf(l.out, "  %s %s: %s, %s\n", rec.Kind, rec.Name, rec.Why, rec.Action)
		return
	}
	line, err := json.Marshal(rec)
	if err != nil {
		// Unreachable for this struct, and still not silent: a ledger that
		// swallows its own failure is the instrument this file exists to stop
		// trusting blindly.
		fmt.Fprintf(l.out, "{\"run\":%q,\"why\":%q}\n", l.run, whyUnreadable)
		return
	}
	fmt.Fprintf(l.out, "%s\n", line)
}

// surveyRuntime is the seam the tests replace. It is on the host read rather
// than on the driver because "does this runtime name resolve" and "what does
// this runtime hold" are two questions, and reap_test.go asserts on the first
// while every doorstep test needs the second silenced (#426).
var surveyRuntime = surveyLeftovers

// surveyLeftovers reads what the runtime holds, and distinguishes the three
// outcomes rather than two. A driver that cannot survey is not an empty host:
// it is a host nobody looked at, and it says so.
func surveyLeftovers(ctx context.Context, rt machine.Runtime) (machine.Leftovers, bool, error) {
	left, asked, err := rt.Survey(ctx)
	if err != nil {
		return machine.Leftovers{}, asked, err
	}
	return left, asked, nil
}

// recordAll writes one line per object of a survey, sorted so two runs of the
// same host produce comparable output.
func (l *ledger) recordAll(left machine.Leftovers, stage, why, action string) {
	for _, group := range []struct {
		kind        string
		names       []string
		attribution string
	}{
		// Machines and networks carry a label the emulator wrote; a rule set
		// carries no config at all, so it is recognised by the description
		// EnsureFirewall writes. Saying which is which is the "who produced it"
		// column, and it is also the answer to whether this run was ever
		// entitled to touch the object.
		//
		// The network line said "name-prefix:fnt-" until 2026-08-28, and it was
		// a column lying about its own subject: Incus.Survey selects networks
		// by `user.feint.provider` exactly as it does machines, and the first
		// network the leg of that day reported was `feint-uplink`, which does
		// not carry the prefix the column claimed had identified it. A ledger
		// whose attribution column can name a mark the object does not carry is
		// a ledger that cannot be used to decide what may be touched.
		// TestTheLedgerAttributesEachObjectToTheMarkItWasFoundBy fails without
		// the correction.
		{"machine", left.Machines, "label:" + machine.LabelKey},
		{"network", left.Networks, "label:" + machine.LabelKey},
		{"rule-set", left.Firewalls, "description:" + machine.FirewallDescription},
	} {
		names := append([]string(nil), group.names...)
		sort.Strings(names)
		for _, name := range names {
			l.record(leftoverRecord{
				Kind:        group.kind,
				Name:        name,
				Attribution: group.attribution,
				Stage:       stage,
				Why:         why,
				Action:      action,
			})
		}
	}
}

// refuseRuntimeLeftovers is the doorstep of #426: a run may not start while the
// host still holds what a previous one made.
//
// It refuses rather than sweeping, for the reason guard.sh already gives about
// the DHCP orphan: this command is asked the question by a conformance suite,
// and a suite that silently destroyed objects on the operator's host would be a
// worse defect than the one it works around. What it owes instead is the exact
// remedy, as one command with nothing to retype.
//
// It refuses on machines and networks and not on rule sets alone: a rule set is
// removed by the sweep with the network it guards and holds no address block,
// so refusing on one by itself would fire on a host nothing was going to fail
// on — which is how a doorstep gets disarmed, and is the lesson mutation 7 of
// unkillable-dhcp-orphan.json holds.
// The driver is resolved by the caller and passed in, since the check now asks
// this runtime two questions rather than one and resolving it twice would let
// them disagree about which host they are talking about.
func refuseRuntimeLeftovers(out io.Writer, led *ledger, vm string, rt machine.Runtime, moment leftoverMoment) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stage := moment.stage()
	left, surveyable, err := surveyRuntime(ctx, rt)
	if !surveyable {
		// --vm off, and every driver that cannot be asked. Said out loud rather
		// than returned in silence: a precondition that passes quietly on the
		// very case it exists for reads as green when it never ran.
		led.prose("runtime leftovers: the %s runtime cannot be surveyed, so nothing was asked\n", vm)
		return nil
	}
	if err != nil {
		led.record(leftoverRecord{Kind: "survey", Name: rt.Name(), Attribution: "none",
			Stage: stage, Why: whyUnreadable, Action: actionNone})
		return fmt.Errorf("could not look at what the %s runtime holds, so this host cannot be called clean: %w",
			rt.Name(), err)
	}
	if len(left.Machines) == 0 && len(left.Networks) == 0 {
		reportRuleSetsLeftBehind(out, led, vm, left, stage)
		led.prose("no machine or network %s is left on this runtime\n", moment.whose())
		return nil
	}

	led.recordAll(left, stage, whyUnswept, actionReported)
	if !led.asJSON {
		fmt.Fprintf(out, "\n%s left %d machine(s) and %d network(s) on this host.\n",
			moment.subject(), len(left.Machines), len(left.Networks))
		fmt.Fprintf(out, "%s\n", moment.consequence())
		// One command, nothing to copy out of the text above it. #375 measured
		// what the other shape costs: a remedy that needs a pid retyped out of a
		// log did not get run for three consecutive failures.
		fmt.Fprintf(out, "\nRun:  feint clean --vm %s\n", vm)
	}
	return fmt.Errorf("%d machine(s) and %d network(s) %s still hold this host",
		len(left.Machines), len(left.Networks), moment.whose())
}

// reportRuleSetsLeftBehind names the rule sets a run left when nothing refuses
// on them, which is every time: refuseRuntimeLeftovers refuses on machines and
// networks alone, because a rule set holds no address block and refusing on one
// would fire on a host nothing was going to fail on (unkillable-dhcp-orphan's
// mutation 7 holds that).
//
// Not refusing is not the same as not saying. Until 2026-08-28 a host holding
// nothing but rule sets of this emulator was reported "no machine or network of
// an earlier run is left" and the rule sets were never printed at all — they
// appeared in the ledger only when some *other* object had already made the
// check refuse. So the one artefact that accumulates one object per session on
// an operator's station (a Scaleway project default group is minted per run)
// was invisible precisely on the hosts where it was the only thing left.
//
// An expected remainder is admitted with its reason written, never in silence.
// TestTheDoorstepNamesRuleSetsItDoesNotRefuseOn fails without this.
func reportRuleSetsLeftBehind(out io.Writer, led *ledger, vm string, left machine.Leftovers, stage string) {
	if len(left.Firewalls) == 0 {
		return
	}
	led.recordAll(machine.Leftovers{Firewalls: left.Firewalls}, stage, whyUnswept, actionReported)
	if led.asJSON {
		return
	}
	fmt.Fprintf(out, "\n%d rule set(s) of this emulator are on this host, and nothing here refuses on them:\n",
		len(left.Firewalls))
	fmt.Fprintf(out, "they hold no address block, so no run fails for their sake. They are still waste —\n"+
		"`feint clean --vm %s` removes them, and a graceful `feint stop` gives back the ones\n"+
		"nothing uses, so a rule set surviving one is a finding rather than housekeeping.\n", vm)
}

// subject names who left what this moment found, in the sentence's own words.
func (m leftoverMoment) subject() string {
	if m == momentClosing {
		return "this run"
	}
	return "a previous run"
}

// whose is the same fact in the possessive, for the error a caller reads.
func (m leftoverMoment) whose() string {
	if m == momentClosing {
		return "of this run"
	}
	return "of an earlier run"
}

// consequence is why it matters, and it differs because the reader's next step
// differs: before a run, the leftovers are what will make it fail later; after
// one, they are what this run failed to clean up.
func (m leftoverMoment) consequence() string {
	if m == momentClosing {
		return "Its clients were supposed to delete what they created, and the emulator gives its own\n" +
			"plumbing back on the way out (machine.PlumbingReleaser). What is named above is neither,\n" +
			"so it is this run's leak — and the next run would have been blamed for it."
	}
	return "They hold their address blocks, and the next run asks for those blocks under new\n" +
		"names, so it fails on \"Address already in use\" thirty steps in instead of here."
}

// survivors returns the objects present in both surveys: the ones a sweep was
// asked to remove, reported no error for, and did not remove.
//
// This is the reading the coordinator of #426 called the least visible case,
// and it is the reason the sweep surveys twice instead of trusting its own
// Pruned counts. Measured on this repository the same week: DELETE on a private
// network answered 204 while `incus network list` still showed the bridge.
func survivors(before, after machine.Leftovers) machine.Leftovers {
	keep := func(was, is []string) []string {
		still := map[string]bool{}
		for _, name := range is {
			still[name] = true
		}
		out := []string{}
		for _, name := range was {
			if still[name] {
				out = append(out, name)
			}
		}
		return out
	}
	return machine.Leftovers{
		Machines:  keep(before.Machines, after.Machines),
		Networks:  keep(before.Networks, after.Networks),
		Firewalls: keep(before.Firewalls, after.Firewalls),
	}
}

// dhcpRecord turns a leftover DHCP service into a ledger line.
//
// Its "why" is whySurvived and not whyRefused, and that is the finding rather
// than a naming choice: a dnsmasq of this shape exists because a network was
// deleted, the delete reported success, and the service kept running with the
// block. #316 swept it, #342 taught doctor to name it and #375 refused on it at
// the doorstep; classing it here is what lets those three be counted as one
// mechanism instead of three incidents.
//
// The owner is the fact that decides the remedy, so it is a column: the
// runtime starts its DHCP services under its own account, and nothing in this
// process may signal what it did not start.
func dhcpRecord(leftover machine.DHCPLeftover, stage, why, action string) leftoverRecord {
	owner := ""
	if action == actionLeftStuck {
		owner = "another-user"
	}
	return leftoverRecord{
		Kind: "dhcp-service",
		// The interface, never the pid: a pid is meaningless once the process is
		// gone and cannot be grouped across runs, while the interface names the
		// network whose teardown produced this.
		Name:        leftover.Interface,
		Attribution: "name-prefix:" + machine.NetworkPrefix + "-",
		Owner:       owner,
		Stage:       stage,
		Why:         why,
		Action:      action,
	}
}
