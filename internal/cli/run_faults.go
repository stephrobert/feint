package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
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
	document, err := callFaults(http.MethodPut, faultsEndpoint(target), rules)
	if err != nil {
		return fmt.Errorf("arming the campaign: %w", err)
	}
	if len(document.Faults) != len(rules) {
		// Unreachable through the endpoint, which replaces the whole set with
		// what it was sent. Said rather than assumed: a run whose campaign was
		// partly armed would report failures nobody asked for, or miss the one
		// the suite is asserting on.
		return fmt.Errorf("arming the campaign: %d rule(s) were sent and %d are armed",
			len(rules), len(document.Faults))
	}
	return nil
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
func readFaultState(target string) (armed int, fired []firedFault, dropped int, ok bool) {
	document, err := callFaults(http.MethodGet, faultsEndpoint(target), nil)
	if err != nil {
		return 0, nil, 0, false
	}
	return len(document.Faults), document.Fired, document.FiredDropped, true
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
	for _, line := range r.Faults {
		switch line.Transport {
		case emulator.TransportClose:
			fmt.Fprintf(w, "    %s call %d: the connection was dropped\n", line.Operation, line.Rank)
		case emulator.TransportHang:
			fmt.Fprintf(w, "    %s call %d: the connection was held open, no answer\n",
				line.Operation, line.Rank)
		default:
			if line.Abandoned {
				fmt.Fprintf(w, "    %s call %d: the client left before the answer\n",
					line.Operation, line.Rank)
				continue
			}
			fmt.Fprintf(w, "    %s call %d answered %d\n", line.Operation, line.Rank, line.Status)
		}
	}
}
