package main

// What each part of this tree costs to prove, declared rather than remembered.
//
// The rule this table encodes is the one the maintainer stated: *on ne joue que
// ce qui est nécessaire*. The whole pass exists and is measured — 1331 s under
// `FEINT_VM=incus-ovn`, 256 s without a runtime — and almost no change needs it.
// A change to `internal/contract` needs the `probe` leg, which measured **0.7 s**
// on 2026-08-27. Between those two numbers sits every wasted hour this repository
// has spent re-proving parts of itself that the diff never touched.
//
// Five properties make this a control rather than a suggestion, and each is held
// by a test in plan_test.go:
//
//  1. **A path nobody triaged is an error, not a cheap default.** `--check` exits
//     2 naming the path. The tempting default is "matched nothing, so run
//     nothing", and it is the same failure as an operation that appears upstream
//     and that nobody sorted: silence reads as a decision when it is an absence.
//     `TestEveryTrackedPathIsTriagedBySomeRule` walks `git ls-files` so a new
//     directory reddens the build on the commit that adds it.
//  2. **A rule that matches nothing is removed.** A line naming a path that no
//     longer exists rots exactly the way a stale limit rots, and reads as
//     coverage while covering nothing. `TestNoRuleMatchesNothing`.
//  3. **The machine layer is found by what the code imports, not by what this
//     table remembers.** A file that imports `internal/core/machine` earns the
//     runtime leg wherever it lives, because the import is readable and a
//     hand-kept list of file names forgets. See reachesMachineLayer in plan.go,
//     held by `TestAFileThatImportsTheMachineLayerEarnsTheRuntimeLeg` — which
//     carries its own control, a file importing nothing but fmt, so it cannot
//     pass over a build() that asks for the 590 s leg unconditionally.
//  4. **An `Unproven` sentence that names an artefact is read against it.** #567
//     wrote here that "a change to the stored shape is proved across a restart by
//     `mise run conformance:environment`", and that suite contains no `snapshot`
//     and no `--state`: false the day it was written, and greppable the same day.
//     See claims.go and `TestEveryUnprovenClaimHoldsAgainstTheArtefactItNames`.
//  5. **A rule may not prescribe a run that cannot drive what it governs.**
//     `functional.sh` routed to `conformance:leg -- fields` (#566/#477) is a real
//     leg, which runs, and which has no machine runtime — the one population that
//     gate refuses to work in. leg.sh declares both halves of that (which legs it
//     refuses without a runtime, and which suites each leg runs), so
//     `TestARuleMayNotPrescribeARunThatCannotDriveWhatItGoverns` reads it rather
//     than a list kept here.
//
// `Unproven` is not decoration. The plan replaces a run that would have proved
// more, and the part it drops has to be *printed* rather than quietly forgone —
// otherwise this tool becomes the thing it exists to prevent: a green that
// describes a smaller world than the reader believes.
//
// AND WHAT NONE OF THE FIVE SEES, because #588 counted it: a rule that names a
// real directory, prescribes a leg that runs, and is simply wrong about which
// population the defect lives in. #521 is exactly that — `-- runtime` is a real
// leg, it runs, and the network suites it carries end on their own `feint clean`,
// which sweeps the leak before any closing doorstep can see it. Nothing static
// sees that, which is why plan.go ends every plan by saying so.

// A rule maps a part of the tree to the runs that judge a change to it.
type rule struct {
	// Path is a directory prefix (ending in /), an exact file, or a *.suffix
	// glob. First-match does not apply: every matching rule contributes.
	Path string
	// Why names what this part of the tree is, in the reader's terms.
	Why string
	// Runs are the commands, cheapest first. A `mise run ` prefix is implied
	// for a bare task name; anything else is printed verbatim.
	Runs []string
	// Unproven is what the plan still does not establish, printed under it.
	// Empty means the runs are sufficient for this path, which is a claim.
	Unproven string
	// Cites is what the Unproven sentence rests on, read off the artefacts it
	// names rather than believed. A sentence naming a file or a task and citing
	// nothing is a failure: see claims.go, and #567, where a rule asserted that
	// a suite saving no state proved a stored shape across a restart.
	Cites []claim
}

// prepushIsTheWholeGate is the honest answer for a path whose change cannot
// reach a served response. It is not "no rule": it is a rule that says so.
const prepushIsTheWholeGate = ""

var rules = []rule{
	// ---------------------------------------------------------------- packs
	{
		Path: "internal/providers/scaleway/",
		Why:  "the Scaleway pack — routes, response shapes, errors",
		Runs: []string{"conformance:leg -- scw-cli", "conformance:leg -- fields"},
		Unproven: "the `fields` leg is the only one where the omission gate judges, so it is " +
			"required for a shape change and not merely for a new field; it does not run " +
			"the example stacks, the second Exoscale zone, `feint up`, or the probe",
		Cites: []claim{{
			About: "the `fields` leg is the only one where the omission gate judges",
			In:    "tools/conformance/leg.sh",
			Shows: []string{"FEINT_FIELD_GATE=1 tools/conformance/score.sh"},
		}},
	},
	{
		Path:     "internal/providers/outscale/",
		Why:      "the Outscale pack — POST /api/v1/<Action>, ResponseContext",
		Runs:     []string{"conformance:leg -- octl", "conformance:leg -- fields"},
		Unproven: "octl reshapes its answers unless a call pins `-o raw`; the leg proves the wire, not the CLI's rendering",
	},
	{
		Path: "internal/providers/exoscale/",
		Why:  "the Exoscale pack — REST /v2/<resource>, asynchronous operations",
		Runs: []string{"conformance:leg -- exo-cli", "conformance:leg -- fields"},
		Unproven: "no Terraform drives this pack until upstream exoscale/terraform-provider-exoscale#573 " +
			"is fixed (#525), and the second zone is `mise run conformance:zones`, which no leg carries",
		Cites: []claim{{
			About:  "the second zone is `mise run conformance:zones`, which no leg carries",
			In:     "tools/conformance/leg.sh",
			Absent: []string{"zones.sh"},
		}},
	},

	// ----------------------------------------------------------- the runtime
	{
		Path: "internal/core/machine/",
		Why:  "the only layer that acts on the operator's own machine",
		Runs: []string{"FEINT_VM=incus-ovn mise run conformance:leg -- runtime"},
		// This sentence used to read "a bridge is a different verdict for
		// isolation alone", and that is error one of the five #588 counts: the
		// bridge differed for the firewall too, which is where #574's defect
		// lived. Measured on 2026-08-28 against the driver's own declaration —
		// three capabilities turn on the mode, not one — and against the
		// firewall's construction, which differs without any capability saying
		// so. No mechanism found this: prose naming no artefact is not
		// greppable, and it was corrected by reading capabilities.go.
		Unproven: "the leg runs under OVN, and three declared capabilities turn on the mode — " +
			"isolation, balancing and private-from-host — so a bridge is a different verdict for " +
			"more than isolation. Its firewall is built differently too, with no capability " +
			"saying so (docs/limits.md: the bridge-mode isolation set keeps its catch-all), and " +
			"that is where #574's defect lived. The stacks' functional proof is " +
			"`FEINT_VM=incus-ovn mise run conformance:functional`",
		Cites: []claim{{
			About: "three declared capabilities turn on the mode — isolation, balancing and private-from-host",
			In:    "internal/core/machine/capabilities.go",
			Shows: []string{"Isolation: d.OVN", "Balancing: d.OVN", "PrivateFromHost: !d.OVN"},
		}, {
			About: "docs/limits.md: the bridge-mode isolation set keeps its catch-all",
			In:    "docs/limits.md",
			Shows: []string{"bridge-mode isolation set keeps its catch-all"},
		}, {
			About: "The stacks' functional proof is `FEINT_VM=incus-ovn mise run conformance:functional`",
			In:    "tools/conformance/functional.sh",
			Shows: []string{"examples/stacks"},
		}},
	},
	{
		Path:     "internal/core/cloudinit/",
		Why:      "the boot payload a pack hands its machines",
		Runs:     []string{"FEINT_VM=incus-ovn mise run conformance:leg -- runtime"},
		Unproven: "a rendered document is proved by a machine that booted on it, which is `mise run conformance:ssh`",
		Cites: []claim{{
			About: "a machine that booted on it, which is `mise run conformance:ssh`",
			In:    "tools/conformance/scaleway/ssh.sh",
			Shows: []string{"sshlogin.sh"},
		}},
	},
	{
		Path:     "internal/core/network/",
		Why:      "addressing plans, subnets, firewall bindings",
		Runs:     []string{"FEINT_VM=incus-ovn mise run conformance:leg -- runtime"},
		Unproven: "isolation between two VPCs is delivered by OVN alone; under a bridge that assertion skips itself",
		Cites: []claim{{
			About: "isolation between two VPCs is delivered by OVN alone",
			In:    "internal/core/machine/capabilities.go",
			Shows: []string{"Isolation: d.OVN"},
		}},
	},

	// -------------------------------------------------------- the shared core
	{
		Path: "internal/core/emulator/",
		Why:  "Env, Pack, Route, the single-port mount — all three packs ride it",
		Runs: []string{"conformance:leg -- probe", "conformance:leg -- fields"},
		Unproven: "a mount collision fails `NewServer` at boot and TestConflictingRoutesAreRejected " +
			"holds it, so `mise run check` already covers it; what these legs add is that the three " +
			"packs still answer through it",
		Cites: []claim{{
			About: "TestConflictingRoutesAreRejected holds it, so `mise run check` already covers it",
			In:    "internal/core/emulator/emulator_test.go",
			Shows: []string{"func TestConflictingRoutesAreRejected"},
		}},
	},
	// The two rules below carried #588's third error, and the correction is
	// measured rather than argued. `conformance:environment` is
	// tools/conformance/environment/up.sh: it starts an emulator, checks the
	// ready conditions it declared, stops it, and asserts nothing answers
	// afterwards. It contains no `snapshot` and no `--state`, and its fixture
	// says of itself that it declares no infrastructure — so it stores no
	// resource, saves no state, and reloads none. It was named here as the proof
	// of a stored shape across a restart, and it was named in the store's own
	// runs. Both are gone; what replaces them is the truth, cited.
	{
		Path: "internal/core/resource/",
		Why:  "the neutral resource every pack stores",
		Runs: []string{"conformance:leg -- probe", "conformance:leg -- fields"},
		Unproven: "no suite proves the stored shape across a restart: tools/conformance/environment/up.sh " +
			"neither saves nor loads state, and its fixture declares no infrastructure to store. " +
			"What holds the round trip is internal/core/store's own tests (#588)",
		Cites: []claim{{
			About:  "tools/conformance/environment/up.sh neither saves nor loads state",
			In:     "tools/conformance/environment/up.sh",
			Absent: []string{"snapshot", "--state"},
		}, {
			About:  "its fixture declares no infrastructure to store",
			In:     "tools/conformance/environment/fixture/feint.yaml",
			Absent: []string{"iac:"},
		}},
	},
	{
		Path: "internal/core/store/",
		Why:  "memory store and JSON snapshot, restored as untrusted input",
		Runs: []string{"conformance:leg -- probe"},
		Unproven: "no run here writes a snapshot and reads it back: tools/conformance/leg.sh drives " +
			"`--state` on no leg, and `conformance:environment` was named in these runs until #588 " +
			"measured that it saves no state either. The round trip is held by this package's own " +
			"tests, same version and across versions alike",
		Cites: []claim{{
			About:  "tools/conformance/leg.sh drives `--state` on no leg",
			In:     "tools/conformance/leg.sh",
			Absent: []string{"--state"},
		}, {
			About:  "`conformance:environment` was named in these runs until #588 measured that it saves no state either",
			In:     "tools/conformance/environment/up.sh",
			Absent: []string{"--state", "snapshot"},
		}},
	},

	// ------------------------------------------------------ what describes it
	{
		Path:     "internal/contract/",
		Why:      "the API descriptions, and the check that answers conform",
		Runs:     []string{"conformance:leg -- probe"},
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "internal/probe/",
		Why:      "the driver that walks every mounted route from its description",
		Runs:     []string{"conformance:leg -- probe"},
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "contracts/",
		Why:      "the extracted API descriptions the probe and --contracts read",
		Runs:     []string{"conformance:leg -- probe"},
		Unproven: "a contract loosened rather than tightened still passes; the probe proves conformity, not strictness",
	},

	// ------------------------------------------------------ the measurements
	{
		Path:     "internal/drift/",
		Why:      "the upstream surface scan, coverage and baseline",
		Runs:     []string{"drift:check"},
		Unproven: "the scan reads a vendored SDK clone; `mise run upstream:sync` decides which one",
		Cites: []claim{{
			About: "`mise run upstream:sync` decides which one",
			In:    "mise.toml",
			Shows: []string{"{{config_root}}/.upstream", "git clone --filter=blob:none"},
		}},
	},
	{
		Path:     "coverage/",
		Why:      "the versioned artefacts the gates read",
		Runs:     []string{"drift:check", "evidence:update"},
		Unproven: "`evidence:update` drives the whole pass by construction — it is the one caller allowed to write the record",
		Cites: []claim{{
			About: "it is the one caller allowed to write the record",
			In:    "mise.toml",
			Shows: []string{"FEINT_EVIDENCE_OUT"},
		}},
	},
	{
		Path:     "shapes/",
		Why:      "the frozen response shapes",
		Runs:     []string{"shapes:check"},
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "corpus/",
		Why:      "the recorded exchanges, replayed as refusals",
		Runs:     []string{"conformance:leg -- fields"},
		Unproven: "the `fields` leg is where refusals.sh runs; nothing else can raise the negative axis",
		Cites: []claim{{
			About: "the `fields` leg is where refusals.sh runs",
			In:    "tools/conformance/leg.sh",
			Shows: []string{"tools/conformance/refusals.sh"},
		}},
	},

	// -------------------------------- what the orphan guard named, one by one
	// There is no `internal/` catch-all below, and that is the design: a new
	// package under internal/ reddens `TestEveryTrackedPathIsTriagedBySomeRule`
	// on the commit that adds it, and somebody writes down what proving it
	// costs. A catch-all would answer "nothing to run" for a package nobody has
	// thought about yet, which is the one answer this tool must never give by
	// accident.
	{
		Path:     "internal/core/serialise/",
		Why:      "how a resource is written down and read back",
		Runs:     []string{"conformance:leg -- probe"},
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path: "internal/core/sshkey/",
		Why:  "the keys a machine is handed at boot",
		Runs: []string{"FEINT_VM=incus-ovn mise run conformance:ssh"},
		Unproven: "tools/conformance/leg.sh names no ssh suite, which is what keeps every leg " +
			"runnable with no runtime — and it is why that run is not part of `mise run conformance`",
		Cites: []claim{{
			About:  "tools/conformance/leg.sh names no ssh suite",
			In:     "tools/conformance/leg.sh",
			Absent: []string{"ssh.sh"},
		}},
	},
	{
		Path:     "internal/proxy/",
		Why:      "the recording proxy that captured the real clouds",
		Runs:     []string{"corpus:check"},
		Unproven: "nothing here re-records against a real cloud; a corpus is captured by hand, with credentials this repository does not hold",
	},
	{
		Path:     "internal/transcript/",
		Why:      "the recorded exchange format",
		Runs:     []string{"corpus:check"},
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "internal/corpus/",
		Why:      "the corpus reader",
		Runs:     []string{"corpus:check", "conformance:leg -- fields"},
		Unproven: "the refusals replay runs on the `fields` leg alone, and it is the only thing that can raise the negative axis",
	},
	{
		Path:     "internal/replay/",
		Why:      "replaying a corpus against the emulator",
		Runs:     []string{"conformance:leg -- fields"},
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "internal/shape/",
		Why:      "the frozen response shapes and their redaction",
		Runs:     []string{"shapes:check"},
		Unproven: "redaction is proved by what the committed shapes contain, not by what this package intends",
	},
	{
		Path:     "internal/upstream/",
		Why:      "reading the vendored SDK clones the drift scan measures",
		Runs:     []string{"drift:check"},
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "internal/environment/",
		Why:      "the declaration `feint up` applies",
		Runs:     []string{"conformance:environment"},
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "internal/release/",
		Why:      "the released surface, the formula and the tap",
		Runs:     []string{"release:check"},
		Unproven: "a release is proved by the release, and this repository has published one that its own gates called green",
	},
	{
		Path:     "internal/compat/",
		Why:      "the compatibility classification",
		Runs:     []string{"compat:check"},
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "internal/trace/",
		Why:      "the exchange trace",
		Runs:     nil,
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "internal/providers/",
		Why:      "what the three packs share at their own level",
		Runs:     []string{"conformance:leg -- fields"},
		Unproven: "a change here reaches all three packs, and the `fields` leg is the only one carrying all three clients",
	},

	// ------------------------------------------------------------- the binary
	{
		Path:     "internal/cli/",
		Why:      "the sub-commands: lifecycle, serve, doctor, snapshot, coverage",
		Runs:     []string{"conformance:environment"},
		Unproven: "`feint up`/`down` on a declaration is what that suite drives; a flag no declaration names is unexercised",
	},
	{
		Path:     "cmd/feint/",
		Why:      "the entry point, and nothing else",
		Runs:     nil,
		Unproven: prepushIsTheWholeGate,
	},

	// -------------------------------------------------------------- the stacks
	{
		Path: "examples/stacks/",
		Why:  "the example stacks, which are examples and tests at once",
		Runs: []string{"conformance:stacks", "FEINT_VM=incus-ovn mise run conformance:functional"},
		Unproven: "the Exoscale stack is applied by hand, never by CI: no gate here clones a third-party " +
			"repository and a patched client is not the official one (#525)",
	},
	{
		Path:     "examples/",
		Why:      "the example declarations and fixtures",
		Runs:     []string{"conformance:environment"},
		Unproven: prepushIsTheWholeGate,
	},

	// ------------------------------------------------------------ the harness
	{
		Path:     "tools/conformance/scaleway/",
		Why:      "the Scaleway suites",
		Runs:     []string{"conformance:leg -- scw-cli"},
		Unproven: "tools/conformance/scaleway/network.sh belongs to the runtime leg, and has its own rule below",
		Cites: []claim{{
			About: "tools/conformance/scaleway/network.sh belongs to the runtime leg",
			In:    "tools/conformance/leg.sh",
			Shows: []string{"tools/conformance/scaleway/network.sh"},
		}},
	},
	{
		Path: "tools/conformance/outscale/",
		Why:  "the Outscale suites",
		Runs: []string{"conformance:leg -- octl", "conformance:leg -- terraform"},
		Unproven: "tools/conformance/outscale/network.sh and tools/conformance/outscale/balancer.sh " +
			"belong to the runtime leg, and have their own rules below",
		Cites: []claim{{
			About: "tools/conformance/outscale/network.sh and tools/conformance/outscale/balancer.sh " +
				"belong to the runtime leg",
			In:    "tools/conformance/leg.sh",
			Shows: []string{"tools/conformance/outscale/network.sh", "tools/conformance/outscale/balancer.sh"},
		}},
	},
	{
		Path:     "tools/conformance/exoscale/",
		Why:      "the Exoscale suites",
		Runs:     []string{"conformance:leg -- exo-cli", "conformance:zones"},
		Unproven: "tools/conformance/exoscale/network.sh belongs to the runtime leg, and has its own rule below",
		Cites: []claim{{
			About: "tools/conformance/exoscale/network.sh belongs to the runtime leg",
			In:    "tools/conformance/leg.sh",
			Shows: []string{"tools/conformance/exoscale/network.sh"},
		}},
	},

	// The suites only the runtime leg runs, one rule each, because the directory
	// rules above prescribe client legs that do not invoke them at all (#588).
	//
	// This is the same category error as #566/#477 — functional.sh sent to a leg
	// with no machine runtime — and it is the one shape of it a machine can see:
	// leg.sh names these four files in the arm it *itself* refuses when
	// FEINT_VM is off, so the leg that runs them is the leg that needs a
	// runtime, and no other leg touches them. Held by
	// TestARuleMayNotPrescribeARunThatCannotDriveWhatItGoverns, which reads
	// leg.sh rather than this comment.
	{
		Path:     "tools/conformance/scaleway/network.sh",
		Why:      "the Scaleway dataplane suite, which only the runtime leg runs",
		Runs:     []string{runtimeLeg},
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "tools/conformance/outscale/network.sh",
		Why:      "the Outscale dataplane suite, which only the runtime leg runs",
		Runs:     []string{runtimeLeg},
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "tools/conformance/exoscale/network.sh",
		Why:      "the Exoscale dataplane suite, which only the runtime leg runs",
		Runs:     []string{runtimeLeg},
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "tools/conformance/outscale/balancer.sh",
		Why:      "the balancer dataplane suite, which only the runtime leg runs",
		Runs:     []string{runtimeLeg},
		Unproven: "the balancer gates on capabilities.balancing, which OVN alone declares; under a bridge it skips itself",
		Cites: []claim{{
			About: "capabilities.balancing, which OVN alone declares",
			In:    "internal/core/machine/capabilities.go",
			Shows: []string{"Balancing: d.OVN"},
		}},
	},

	// The ssh chains, which no leg carries either — found by hand while #588 was
	// measuring the sentence above, not by a mechanism. `mise run conformance`
	// must stay runnable with no runtime, so these three live outside every leg
	// and outside that task; the only thing that drives them is
	// `conformance:ssh`, and the client-leg rules above cannot.
	{
		Path:     "tools/conformance/scaleway/ssh.sh",
		Why:      "the Scaleway ssh chain: a key, a server, a real login",
		Runs:     []string{"FEINT_VM=incus-ovn mise run conformance:ssh"},
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "tools/conformance/outscale/ssh.sh",
		Why:      "the Outscale ssh chain: a key, a server, a real login",
		Runs:     []string{"FEINT_VM=incus-ovn mise run conformance:ssh"},
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "tools/conformance/exoscale/ssh.sh",
		Why:      "the Exoscale ssh chain: a key, a server, a real login",
		Runs:     []string{"FEINT_VM=incus-ovn mise run conformance:ssh"},
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "tools/conformance/sshlogin.sh",
		Why:      "the login the three ssh chains share",
		Runs:     []string{"FEINT_VM=incus-ovn mise run conformance:ssh"},
		Unproven: prepushIsTheWholeGate,
	},
	// WHAT IS STILL MIS-ROUTED HERE, named rather than left to be discovered.
	//
	// The general property is *a rule must prescribe a run that actually invokes
	// the file it governs*, and this table does not enforce it. Only the half a
	// machine can read today is: a suite the runtime leg alone carries, which is
	// the four above. By hand, four more files under the catch-all below earn
	// `conformance:leg -- fields`, a leg that runs none of them:
	// crash.sh, parity.sh, stacks.sh and witness.sh, each of which has a mise
	// task of its own. They are wrong on the expensive-and-useless side rather
	// than the dangerous one — the reader is sent to a leg that cannot go red
	// for their change — and fixing them is table work #588 deliberately did not
	// take on, because the issue is about what the tool says of itself.
	{
		Path:     "tools/conformance/shared/",
		Why:      "the helpers every suite sources, waiting included",
		Runs:     []string{"FEINT_VM=incus-ovn mise run conformance:leg -- runtime", "conformance:leg -- fields"},
		Unproven: "a helper changed here is read by suites in both legs, so both are the minimum",
	},
	{
		Path:     "tools/conformance/environment/",
		Why:      "the `feint up` / `feint down` suite",
		Runs:     []string{"conformance:environment"},
		Unproven: prepushIsTheWholeGate,
	},
	// The stack gate, by name, because the catch-all below sent a change to it
	// to `conformance:leg -- fields` — a leg with no machine runtime, which is
	// the one population this gate refuses to run in. Measured on this diff on
	// 2026-08-28: the plan for a change to functional.sh named five falsify
	// specs and two legs, and never the gate itself.
	{
		Path: "tools/conformance/functional.sh",
		Why:  "the stack gate — the only gate that applies the example stacks to real machines",
		Runs: []string{"FEINT_VM=incus-ovn mise run conformance:functional"},
		Unproven: "three passes on one host prove what one host holds; the CI job of " +
			".github/workflows/runtime-proof.yml is a second population and only the night plays it",
		Cites: []claim{{
			About: "the CI job of .github/workflows/runtime-proof.yml is a second population and only the night plays it",
			In:    ".github/workflows/runtime-proof.yml",
			Shows: []string{"tools/conformance/functional.sh", "cron:"},
		}},
	},
	{
		Path:     "tools/conformance/functionallib.sh",
		Why:      "the stack gate's verdicts, held by functional_test.go against planted defects",
		Runs:     []string{"FEINT_VM=incus-ovn mise run conformance:functional"},
		Unproven: "the unit tests judge each verdict; only the gate judges them against machines that really boot",
	},
	{
		Path:     "tools/conformance/",
		Why:      "the shared harness: doorstep, score, faults, refusals, stacks, functional",
		Runs:     []string{"conformance:leg -- fields"},
		Unproven: "tools/conformance/score.sh judges the field gate on the `fields` leg alone; on every other leg it prints and judges nothing",
		Cites: []claim{{
			About: "tools/conformance/score.sh judges the field gate on the `fields` leg alone",
			In:    "tools/conformance/leg.sh",
			Shows: []string{"FEINT_FIELD_GATE=1 tools/conformance/score.sh", "FEINT_FIELD_GATE=0 tools/conformance/score.sh"},
		}},
	},
	// Both callers of the runtime resolver, named: `evidence:update` is twenty
	// minutes and the stack gate is fifteen, so the cheap one is the run and the
	// expensive one is the bound.
	{
		Path:     "tools/runtime-mode.sh",
		Why:      "which runtime a task answers under, and the announcement that says so (#574)",
		Runs:     []string{"FEINT_VM=incus-ovn mise run conformance:functional"},
		Unproven: "`mise run evidence:update` is the other caller, and its twenty minutes are not in this plan; tools/evidence/mode_test.go is what holds its four outcomes offline",
		Cites: []claim{{
			About: "`mise run evidence:update` is the other caller",
			In:    "tools/evidence/mode.sh",
			Shows: []string{"runtime-mode.sh"},
		}, {
			About: "tools/evidence/mode_test.go is what holds its four outcomes offline",
			In:    "tools/evidence/mode_test.go",
			Shows: []string{"func Test"},
		}},
	},
	{
		Path:     "tools/falsify/",
		Why:      "the falsification harness and its specs",
		Runs:     []string{"falsify:lint"},
		Unproven: "`falsify:lint` is the cheap half; `mise run falsify:all` replays every mutation and belongs to the night",
		Cites: []claim{{
			About: "`mise run falsify:all` replays every mutation",
			In:    "mise.toml",
			Shows: []string{"falsify.py --all tools/falsify/specs"},
		}},
	},
	{
		Path:     "tools/drift/",
		Why:      "the drift gate",
		Runs:     []string{"drift:check"},
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "tools/docs/",
		Why:      "the documentation gates, limits acknowledgements included",
		Runs:     []string{"docs:check", "limits:check"},
		Unproven: "`limits:check` sees a section citing an issue that has since closed, and nothing else: the one limit that went false for ten days cited no issue at all",
		Cites: []claim{{
			About: "`limits:check` sees a section citing an issue that has since closed, and nothing else",
			In:    "mise.toml",
			Shows: []string{"tools/docs/limits-acks.py check"},
		}},
	},
	{
		Path:     "tools/",
		Why:      "repository tooling",
		Runs:     nil,
		Unproven: "this is the one catch-all here, and it covers scripts whose own Go tests run in `mise run check`",
		Cites: []claim{{
			About: "scripts whose own Go tests run in `mise run check`",
			In:    "mise.toml",
			Shows: []string{"[tasks.check]", "go test"},
		}},
	},

	// ------------------------------------------------------------ everything
	// else that is tracked. Each of these is a claim that a change there cannot
	// alter a served answer — which is why they are written down rather than
	// left to fall through a default.
	{
		Path:     "docs/",
		Why:      "prose, including what this project says it does not do",
		Runs:     []string{"docs:check", "limits:check"},
		Unproven: "a document can be wrong in ways no gate reads; three of fifty limits had gone false on 2026-08-27",
	},
	{
		Path:     ".github/",
		Why:      "workflows and repository configuration",
		Runs:     []string{"lint-shell"},
		Unproven: "actionlint, zizmor and poutine run in CI; a renamed job silently stops being a required check",
	},
	{
		Path:     "feinttest/",
		Why:      "the test helpers the packages share",
		Runs:     nil,
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "mise.toml",
		Why:      "the task definitions every run here goes through",
		Runs:     []string{"conformance:leg -- probe"},
		Unproven: "a task nobody runs locally is proved by the workflow that runs it, not by this plan",
	},
	{
		Path:     "go.mod",
		Why:      "the module, whose three lines are a rule of this project",
		Runs:     nil,
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "Dockerfile",
		Why:      "the published image",
		Runs:     []string{"conformance:image"},
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "*.md",
		Why:      "prose, wherever it lives and in either language",
		Runs:     []string{"docs:check"},
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "*.yml",
		Why:      "root configuration",
		Runs:     nil,
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "*.yaml",
		Why:      "root configuration",
		Runs:     nil,
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "*.toml",
		Why:      "root configuration",
		Runs:     nil,
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "*.lock",
		Why:      "a pinned dependency set",
		Runs:     nil,
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     ".editorconfig",
		Why:      "editor settings",
		Runs:     nil,
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     ".gitignore",
		Why:      "what this repository does not track",
		Runs:     nil,
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     ".dockerignore",
		Why:      "what the image build does not read",
		Runs:     nil,
		Unproven: prepushIsTheWholeGate,
	},
	{
		Path:     "LICENSE",
		Why:      "the licence",
		Runs:     nil,
		Unproven: prepushIsTheWholeGate,
	},
}
