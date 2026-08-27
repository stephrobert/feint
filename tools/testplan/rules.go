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
// Three properties make this a control rather than a suggestion, and each is held
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
//
// `Unproven` is not decoration. The plan replaces a run that would have proved
// more, and the part it drops has to be *printed* rather than quietly forgone —
// otherwise this tool becomes the thing it exists to prevent: a green that
// describes a smaller world than the reader believes.

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
	},

	// ----------------------------------------------------------- the runtime
	{
		Path: "internal/core/machine/",
		Why:  "the only layer that acts on the operator's own machine",
		Runs: []string{"FEINT_VM=incus-ovn mise run conformance:leg -- runtime"},
		Unproven: "the leg runs under OVN; a bridge is a different verdict for isolation alone, " +
			"and the stacks' functional proof is `FEINT_VM=incus-ovn mise run conformance:functional`",
	},
	{
		Path:     "internal/core/cloudinit/",
		Why:      "the boot payload a pack hands its machines",
		Runs:     []string{"FEINT_VM=incus-ovn mise run conformance:leg -- runtime"},
		Unproven: "a rendered document is proved by a machine that booted on it, which is `mise run conformance:ssh`",
	},
	{
		Path:     "internal/core/network/",
		Why:      "addressing plans, subnets, firewall bindings",
		Runs:     []string{"FEINT_VM=incus-ovn mise run conformance:leg -- runtime"},
		Unproven: "isolation between two VPCs is delivered by OVN alone; under a bridge that assertion skips itself",
	},

	// -------------------------------------------------------- the shared core
	{
		Path: "internal/core/emulator/",
		Why:  "Env, Pack, Route, the single-port mount — all three packs ride it",
		Runs: []string{"conformance:leg -- probe", "conformance:leg -- fields"},
		Unproven: "a mount collision fails `NewServer` at boot, so `mise run check` already covers it; " +
			"what these legs add is that the three packs still answer through it",
	},
	{
		Path:     "internal/core/resource/",
		Why:      "the neutral resource every pack stores",
		Runs:     []string{"conformance:leg -- probe", "conformance:leg -- fields"},
		Unproven: "a change to the stored shape is proved across a restart by `mise run conformance:environment`",
	},
	{
		Path:     "internal/core/store/",
		Why:      "memory store and JSON snapshot, restored as untrusted input",
		Runs:     []string{"conformance:leg -- probe", "conformance:environment"},
		Unproven: "a snapshot written by one version and read by another is not exercised by any leg",
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
	},
	{
		Path:     "coverage/",
		Why:      "the versioned artefacts the gates read",
		Runs:     []string{"drift:check", "evidence:update"},
		Unproven: "`evidence:update` drives the whole pass by construction — it is the one caller allowed to write the record",
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
		Path:     "internal/core/sshkey/",
		Why:      "the keys a machine is handed at boot",
		Runs:     []string{"FEINT_VM=incus-ovn mise run conformance:ssh"},
		Unproven: "that suite is not part of `mise run conformance`, which must stay runnable with no runtime",
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
		Unproven: "scaleway/network.sh belongs to the runtime leg, not to this one",
	},
	{
		Path:     "tools/conformance/outscale/",
		Why:      "the Outscale suites",
		Runs:     []string{"conformance:leg -- octl", "conformance:leg -- terraform"},
		Unproven: "outscale/network.sh and outscale/balancer.sh belong to the runtime leg",
	},
	{
		Path:     "tools/conformance/exoscale/",
		Why:      "the Exoscale suites",
		Runs:     []string{"conformance:leg -- exo-cli", "conformance:zones"},
		Unproven: "exoscale/network.sh belongs to the runtime leg",
	},
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
	{
		Path:     "tools/conformance/",
		Why:      "the shared harness: doorstep, score, faults, refusals, stacks, functional",
		Runs:     []string{"conformance:leg -- fields"},
		Unproven: "score.sh judges the field gate on the `fields` leg alone; on every other leg it prints and judges nothing",
	},
	{
		Path:     "tools/falsify/",
		Why:      "the falsification harness and its specs",
		Runs:     []string{"falsify:lint"},
		Unproven: "`falsify:lint` is the cheap half; `mise run falsify:all` replays every mutation and belongs to the night",
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
	},
	{
		Path:     "tools/",
		Why:      "repository tooling",
		Runs:     nil,
		Unproven: "this is the one catch-all here, and it covers scripts whose own Go tests run in `mise run check`",
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
