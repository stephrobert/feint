package cli

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/environment"
)

// Which client drives which pack, what says so, and the sentences derived from
// it.
//
// #592 is the defect this file owns. `README.md:41` said *"Run your Terraform
// against Scaleway, Outscale or Exoscale"* while `docs/confidence.md:48` said
// *"A Terraform run against Exoscale — no, and refused rather than
// half-served"*, and `feint up` had been refusing `iac.engine: terraform` for
// that pack since #525 landed on 2026-08-26. Two days of a front line promising
// the one client the doorstep turns away, through every green `docs:check` in
// between.
//
// None of the existing gates could see it, and they are not weak: they check
// **values**. A coverage count, a route total, an image tag, a client version,
// a `feint.yaml` field — each is a number or a name an artefact also holds, so
// each can be compared. *"Run your Terraform against Scaleway, Outscale or
// Exoscale"* is a **claim about capability**, and nothing owned it. A sentence
// nobody owns cannot be contradicted.
//
// So the matrix below owns them. Three properties make it more than a table,
// and the third is the one that matters:
//
//  1. **Every row names the instrument that establishes it**, the way
//     `Route.Operation` names an upstream operation. `proof` is not prose: it
//     is a value capabilityProblems resolves, and a row whose instrument does
//     not carry it fails its own check.
//  2. **The instruments already exist, and are asked rather than restated.** A
//     `supported` row is confirmed by clientsProvenInCI — the same scan of
//     .github/workflows/conformance.yml the status table and the client matrix
//     read. A `refused` row was confirmed by asking the pack's own VetoEngine
//     until #644 removed that mechanism with its last subject,
//     which is the code `up` and `down` consult before a process starts. A
//     matrix that disagreed with up.go would be #592 one storey lower.
//  3. **The check runs in both directions.** A row nothing establishes is
//     refused, and a pair an instrument establishes with no row is refused too.
//     One direction alone stops measuring the day its subject moves — the
//     lesson docs_stacks.go paid for.
//
// And the sentences are derived from it: renderPromise writes the README's
// promise in both locales, so the claim on line 41 is no longer a thing anybody
// types. What guards the rest is unownedCapabilityClaims, which reads every
// generated block of the front pages and requires a matrix row behind every
// client/provider pair it finds. Put the old sentence back inside a generated
// block and `docs --check` exits 2 —
// TestTheOldFalsePromiseIsCaughtByTheClaimReader is that mutation, kept as a
// test so the reader cannot quietly stop finding it.

const (
	capabilityStartMarker = "<!-- capability:start -->"
	capabilityEndMarker   = "<!-- capability:end -->"

	promiseStartMarker = "<!-- promise:start -->"
	promiseEndMarker   = "<!-- promise:end -->"
)

// capabilitySupport is the verdict of one row: whether this client may drive
// this pack at all.
type capabilitySupport string

const (
	// capabilitySupported — a real client drives this pack on every pull
	// request. Confirmed against the conformance workflow, never declared.
	capabilitySupported capabilitySupport = "supported"
	// capabilityRefused — pointing this client at this pack is stopped before a
	// process starts. Confirmed against the pack's own veto.
	capabilityRefused capabilitySupport = "refused"
)

// capabilityProof names the instrument a row rests on. Its value is resolved,
// not printed: capabilityProblems asks the named instrument whether it really
// carries the row.
type capabilityProof string

const (
	// provenInCI — .github/workflows/conformance.yml runs a suite that drives
	// this client against this pack, on every pull request. Read through
	// clientsProvenInCI, which is the scan the status table and the client
	// version matrix already share.
	provenInCI capabilityProof = "conformance workflow"
	// A second kind lived here until #644: `up.go VetoEngine`, the pack-side
	// veto that stopped `feint up` and `feint down` before any process
	// started. It had exactly one subject — no Terraform for Exoscale while
	// the published provider split its calls between this emulator and a
	// paying account — and upstream fixed that in v0.71.0. A proof kind no row
	// can rest on is a resolver nobody exercises, so it went with the
	// mechanism rather than staying as a name.
)

// capabilityControlPlane is the only mode any row can claim today, and the
// column is not decoration: it says what the row's proof covers. The
// conformance matrix starts its emulator with no machine runtime, so every
// proof below is a proof about the control plane — an API that answers, not a
// machine that boots. A row claiming more would need a run under FEINT_VM, and
// modeProblems refuses the whole column the day the workflow arms one without
// this file learning about it.
const capabilityControlPlane = "control plane"

// capabilityRow is one client against one pack.
type capabilityRow struct {
	// Provider is the pack's own name, the spelling clientsProvenInCI keys by
	// and Pack.Name() answers.
	Provider string
	// Client is the token in clientSources: the word prose uses for this
	// client, lowercased.
	Client string
	// Mode says what the proof covers. capabilityControlPlane today, for all of
	// them.
	Mode string
	// Support is the verdict.
	Support capabilitySupport
	// Reason says why, for a refused row. Empty on a supported one: "it works"
	// needs no excuse, and a reason there would be prose nothing checks.
	Reason string
	// Proof names the instrument that establishes the row.
	Proof capabilityProof
	// Marker is the token any sentence naming this refused pair must carry, so
	// that mentioning the refusal is possible and claiming the capability is
	// not. Empty on a supported row.
	//
	// It is what lets renderPromise write "Terraform joins that pack when a
	// release carries exoscale/terraform-provider-exoscale#573" without the
	// claim reader treating it as a promise — and what stops "Run your
	// Terraform against Scaleway, Outscale or Exoscale" from being written
	// again, since that sentence names the pair and carries nothing.
	Marker string
}

// capabilityMatrix is the whole of it, and it is deliberately short: a row per
// pair some instrument establishes, and nothing else. A pair neither the
// workflow drives nor a pack vetoes has no row, so a sentence claiming it is
// refused by unownedCapabilityClaims rather than passing on a row nobody
// earned.
var capabilityMatrix = []capabilityRow{
	{Provider: "scaleway", Client: "terraform", Mode: capabilityControlPlane, Support: capabilitySupported, Proof: provenInCI},
	{Provider: "scaleway", Client: "opentofu", Mode: capabilityControlPlane, Support: capabilitySupported, Proof: provenInCI},
	{Provider: "scaleway", Client: "scw", Mode: capabilityControlPlane, Support: capabilitySupported, Proof: provenInCI},

	{Provider: "outscale", Client: "terraform", Mode: capabilityControlPlane, Support: capabilitySupported, Proof: provenInCI},
	{Provider: "outscale", Client: "opentofu", Mode: capabilityControlPlane, Support: capabilitySupported, Proof: provenInCI},
	{Provider: "outscale", Client: "octl", Mode: capabilityControlPlane, Support: capabilitySupported, Proof: provenInCI},

	{Provider: "exoscale", Client: "exo", Mode: capabilityControlPlane, Support: capabilitySupported, Proof: provenInCI},
	{Provider: "exoscale", Client: "terraform", Mode: capabilityControlPlane, Support: capabilitySupported, Proof: provenInCI},
	{Provider: "exoscale", Client: "opentofu", Mode: capabilityControlPlane, Support: capabilitySupported, Proof: provenInCI},
}

// capabilityRowFor answers the row for one pair, or nil when nothing
// establishes it.
func capabilityRowFor(provider, client string) *capabilityRow {
	for i := range capabilityMatrix {
		if capabilityMatrix[i].Provider == provider && capabilityMatrix[i].Client == client {
			return &capabilityMatrix[i]
		}
	}
	return nil
}

// capabilityClientTokens is every client token the matrix and the claim reader
// know, taken from clientSources so the two lists cannot disagree.
func capabilityClientTokens() []string {
	out := make([]string, 0, len(clientSources))
	for _, c := range clientSources {
		out = append(out, c.token)
	}
	return out
}

// capabilityEngineOf answers the `iac.engine` name for a client token, or "" for
// a client that is not an engine `up` runs.
func capabilityEngineOf(token string) string {
	for _, c := range clientSources {
		if c.token == token {
			return c.engine
		}
	}
	return ""
}

// capabilityClientName spells a token the way the generated tables do.
func capabilityClientName(token string) string {
	for _, c := range clientSources {
		if c.token == token {
			return c.name
		}
	}
	return token
}

// ---------------------------------------------------------------------------
// Resolving the proofs
// ---------------------------------------------------------------------------

// packFacts is what the packs themselves say, read once: their names, and which
// engine each one refuses and why.
//
// The packs are asked rather than a table consulted, for the reason #592 is
// about: the refusal that matters is the one `up` and `down` execute, and a
// second declaration of it would be a second thing to disagree with. The names
// come from the same place so that a fourth pack is part of the vocabulary the
// claim reader searches for from the day it mounts a route, without this file
// learning its spelling.
type packFacts struct {
	// Providers is every mounted pack's own name.
	Providers []string
}

func readPackFacts() (packFacts, error) {
	srv, _, err := newServer(nil)
	if err != nil {
		return packFacts{}, err
	}
	facts := packFacts{}
	for _, p := range srv.Packs() {
		facts.Providers = append(facts.Providers, p.Name())
	}
	sort.Strings(facts.Providers)
	return facts, nil
}

// armedRuntime matches a `--vm` flag with a runtime armed. The mode column
// claims control plane, and it claims it about the workflow's own runs.
var armedRuntime = regexp.MustCompile(`(?m)^[^#\n]*--vm[= ]+([a-z-]+)`)

// capabilityProblems is what `feint docs` reports in both modes: every
// disagreement between the matrix and the instruments it names.
//
// Reported rather than rendered, on the terms docs.go states for the other
// comparisons: regenerating repairs none of it. A row whose proof does not hold
// needs a decision, not a rewrite.
//
// It answers nothing when the workflow is not there, because `feint docs` also
// regenerates a README outside this repository — the accommodation
// stackProofProblems makes, for the same reason. What stops that from being a
// check that skips itself is TestTheCapabilityChecksHaveASubjectToMeasure,
// which asserts in this repository that the population is not empty and that
// both directions really run.
func capabilityProblems(workflow string) []string {
	if _, err := os.Stat(workflow); os.IsNotExist(err) {
		return nil
	}
	// The packs are still asked, and the answer is still what a refusal would
	// be resolved against: readPackFacts is what capabilityClaimProblems below
	// reads for provider names. What it no longer carries is a veto map, since
	// no pack vetoes an engine (#644).
	if _, err := readPackFacts(); err != nil {
		return []string{fmt.Sprintf("cannot ask the packs what they serve: %v", err)}
	}

	var problems []string
	problems = append(problems, matrixShapeProblems()...)
	problems = append(problems, provenInCIProblems(workflow)...)
	problems = append(problems, modeProblems(workflow)...)
	sort.Strings(problems)
	return problems
}

// capabilityClaimProblems reads the pages back and reports every claim no
// matrix row carries.
//
// Separate from capabilityProblems above, and the separation is an ordering
// rather than a tidy-up. The checks above judge the *matrix* against the
// instruments and are true of the repository whatever any page says; this one
// judges *what a page says*, so in `--check` mode it reads the documents as
// they stand and in write mode it has to read them **after** the regeneration.
// Run before the writes, it judged the page the run was about to replace, and a
// legitimate change to the matrix could never land: the render was refused
// because of the block it was about to rewrite.
func capabilityClaimProblems(workflow string, pages []string) []string {
	if _, err := os.Stat(workflow); os.IsNotExist(err) {
		return nil
	}
	facts, err := readPackFacts()
	if err != nil {
		return []string{fmt.Sprintf("cannot ask the packs what they serve: %v", err)}
	}
	var problems []string
	for _, page := range pages {
		problems = append(problems, unownedCapabilityClaims(page, facts.Providers)...)
	}
	sort.Strings(problems)
	return problems
}

// matrixShapeProblems refuses a row that cannot be resolved at all: an unknown
// client, an unknown verdict, a refused row with no reason or no marker.
func matrixShapeProblems() []string {
	var problems []string
	known := map[string]bool{}
	for _, token := range capabilityClientTokens() {
		known[token] = true
	}
	// The engine names clientSources carries must be the ones `iac.engine` is
	// validated against, or a row would rest on a veto nothing can ever be
	// asked for.
	for _, c := range clientSources {
		if c.engine != "" && !slices.Contains(environment.Engines, c.engine) {
			problems = append(problems, fmt.Sprintf(
				"clientSources maps %s to the engine %q and internal/environment does not accept it: "+
					"no `feint.yaml` could declare it, so no pack could ever be asked to veto it",
				c.name, c.engine))
		}
	}

	seen := map[string]bool{}
	for _, row := range capabilityMatrix {
		key := row.Provider + "/" + row.Client
		if seen[key] {
			problems = append(problems, fmt.Sprintf(
				"capabilityMatrix carries %s twice: two rows about one pair are two answers to one "+
					"question, and capabilityRowFor would only ever read the first", key))
		}
		seen[key] = true
		if !known[row.Client] {
			problems = append(problems, fmt.Sprintf(
				"capabilityMatrix names the client %q for %s and clientSources in "+
					"internal/cli/docs_clients.go does not: the claim reader would never look for it",
				row.Client, row.Provider))
		}
		if row.Mode != capabilityControlPlane {
			problems = append(problems, fmt.Sprintf(
				"capabilityMatrix says %s is proved in mode %q and this repository can prove only %q: "+
					"a mode nothing runs is a column that reads as a measurement",
				key, row.Mode, capabilityControlPlane))
		}
		switch row.Support {
		case capabilitySupported:
			if row.Reason != "" || row.Marker != "" {
				problems = append(problems, fmt.Sprintf(
					"capabilityMatrix gives %s a reason or a marker and marks it supported: both belong "+
						"to a refusal, and a reader would take them for a caveat on a client that works", key))
			}
		case capabilityRefused:
			if strings.TrimSpace(row.Reason) == "" {
				problems = append(problems, fmt.Sprintf(
					"capabilityMatrix refuses %s with no reason: \"refused\" and \"refused because X\" "+
						"are different facts, and only the second is a decision", key))
			}
			if strings.TrimSpace(row.Marker) == "" {
				problems = append(problems, fmt.Sprintf(
					"capabilityMatrix refuses %s with no marker: no sentence could then name the pair "+
						"at all, not even to say it is refused", key))
			} else if !strings.Contains(row.Reason, row.Marker) {
				// The published table prints the reason beside the pair, so the
				// reason is itself a sentence naming that pair — and it has to
				// pass the rule every other sentence passes. Caught by the claim
				// reader on its first run against the generated block, which is
				// the check finding its own page rather than being told about
				// it.
				problems = append(problems, fmt.Sprintf(
					"capabilityMatrix refuses %s and its reason does not name %s: the published row "+
						"names the pair without naming what would change it, which is the shape this "+
						"whole table refuses everywhere else", key, row.Marker))
			}
		default:
			problems = append(problems, fmt.Sprintf(
				"capabilityMatrix gives %s the verdict %q, which is neither %q nor %q",
				key, row.Support, capabilitySupported, capabilityRefused))
		}
	}
	return problems
}

// provenInCIProblems resolves every `conformance workflow` proof against the
// workflow, in both directions.
func provenInCIProblems(workflow string) []string {
	// proofsPerClient rather than clientsProvenInCI, and the difference is one
	// this check was written wrong on first: clientOf maps one suite to the
	// cell "Terraform, OpenTofu", and clientsProvenInCI hands that cell back
	// verbatim for the status table to print. Compared against a client name it
	// matches neither of them, so every engine row read as unproven. The
	// transpose splits the cell, which is what a per-client question needs.
	proofs, err := proofsPerClient(workflow)
	if err != nil {
		return []string{fmt.Sprintf("cannot read which clients %s drives: %v", workflow, err)}
	}

	// Which pairs the workflow really establishes, as matrix tokens.
	drives := map[string]bool{}
	for name, list := range proofs {
		for _, c := range clientSources {
			if c.name != name {
				continue
			}
			for _, proof := range list {
				drives[proof.provider+"/"+c.token] = true
			}
		}
	}

	var problems []string
	claimed := map[string]bool{}
	for _, row := range capabilityMatrix {
		if row.Proof != provenInCI {
			continue
		}
		key := row.Provider + "/" + row.Client
		claimed[key] = true
		if row.Support != capabilitySupported {
			problems = append(problems, fmt.Sprintf(
				"capabilityMatrix rests %s on %s and does not mark it supported: that proof says a "+
					"client drives the pack, which is the only thing it can say", key, provenInCI))
			continue
		}
		if !drives[key] {
			problems = append(problems, fmt.Sprintf(
				"capabilityMatrix says %s is proved by the %s and %s runs no suite driving %s against "+
					"that pack: the row claims a proof that does not exist",
				key, provenInCI, workflow, capabilityClientName(row.Client)))
		}
	}

	// The other direction, and it is the one that keeps the matrix from going
	// quiet: a pair CI proves and nobody wrote down is a capability the
	// generated sentences may not mention, which understates the product as
	// surely as #592 overstated it.
	for key := range drives {
		if claimed[key] {
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"%s drives %s and capabilityMatrix has no row for it: every sentence claiming a client is "+
				"derived from that table, so a proven pair with no row cannot be written down anywhere",
			workflow, key))
	}
	return problems
}

// modeProblems keeps the mode column honest: every row says its proof covers
// the control plane, and the workflow those proofs come from must therefore run
// with no machine runtime armed.
//
// Positive rather than absent: the flag is looked for, and a value other than
// `off` is the finding. A check that passed because it found nothing would be
// the same check whether or not it ever looked.
func modeProblems(workflow string) []string {
	body, err := os.ReadFile(workflow) //nolint:gosec // a path this repository owns
	if err != nil {
		return []string{fmt.Sprintf("cannot read %s to check what the proofs cover: %v", workflow, err)}
	}
	var problems []string
	for _, m := range armedRuntime.FindAllStringSubmatch(string(body), -1) {
		if m[1] == "off" {
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"%s arms `--vm %s` and every capabilityMatrix row says its proof covers the %q: a run with "+
				"a machine runtime proves more than the column claims, and the column would be measuring "+
				"nothing", workflow, m[1], capabilityControlPlane))
	}
	return problems
}
