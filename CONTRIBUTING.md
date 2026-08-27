# Contributing to Feint

**Read this in another language:** [Français](./CONTRIBUTING.fr.md)

## The one rule

**A change to emulated surface is not done until a real client exercised it.**

Unit tests assert what we believe an API does. `scw`, `terraform` and the official
SDKs assert what it actually is. Run `mise run conformance` and say so in the pull
request; if a suite could not run locally, state that explicitly.

## Getting started

```bash
mise install          # pinned Go, Python, uv and the linters
pre-commit install    # the local gates; see below, this one is not optional
mise run check        # what CI runs on a pull request
mise run serve        # the emulator on 127.0.0.1:4599
```

**`pre-commit install` is a real step, not a courtesy.** Git hooks live in
`.git/hooks/`, which is not versioned, so a fresh clone has none of them however
complete `.pre-commit-config.yaml` looks. Without that command, the checks that
enforce this project's own rules never run on your machine: that a route
declares its upstream operation, that no external dependency crept into
`go.mod`, that the generated documentation still matches the artefacts it
describes, that a response still matches the provider's API description. CI
catches all of it, but hours later and in front of everybody.

It installs three hook types, and the last two are easy to miss: `pre-commit`,
`commit-msg` and `pre-push`. The configuration declares all three through
`default_install_hook_types`, so the single command is enough.

### Before you push

The `pre-push` hook runs `mise run prepush`: `check`, `docs:check`,
`drift:check`, `shapes:check`, `corpus:check`, `falsify:selftest` and
`falsify:lint`.
Deterministic, offline, seconds. It is what a pull request will refuse without
ever depending on a runner's weather.

**It is not enough**, and what it misses depends on what you changed. You do not
have to remember which — ask:

```bash
mise run testplan          # what this diff has earned, cheapest run first
```

It reads the diff, prints the runs, and prints what they still **do not** prove.
It runs nothing itself: the choice is the half a human has to be able to
disagree with. `mise run prepush` calls it with `--check`, so every push says
what to run next, and refuses a path no rule triages — "it matched nothing, so
run nothing" is the cheap default, and an un-triaged path is an absence, not a
decision (#564).

It also prints **the falsifications the diff has earned**, read off
`tools/falsify/specs/` rather than remembered: 128 specs name 186 distinct files,
and a spec that mutates a file you touched is the subset of `falsify:all` your
change can have stopped biting. Every batch here has followed that discipline by
hand and said so in its pull request, which means it has also been forgotten by
hand — `mise run check` cannot see a guard whose test still passes over nothing.

A `_test.go` file or a `testdata/` tree earns no conformance leg at all, for the
same reason prose does not: nothing compiled into tests alone reaches a client.
It is still triaged, so a test file in a package no rule names still reddens.

The costs it orders by are measured, one pass each on 2026-08-27:

| run | measured | what it carries |
|---|--:|---|
| `conformance:leg -- probe` | **0.7 s** | every mounted route driven from its own API description, no client |
| `conformance:leg -- exo-cli` | 4.0 s | the real `exo` |
| `conformance:leg -- scw-cli` | 7.1 s | the real `scw` |
| `conformance:leg -- terraform` | 45.4 s | both Terraform fixtures, on either engine |
| `conformance:leg -- octl` | 141.3 s | the real `octl` |
| `conformance:leg -- fields` | 208.1 s | all four clients, faults, refusals — **the only leg where the omission gate judges** |
| `conformance:leg -- runtime` | 590 s | the four suites that need real machines; refuses to run without `FEINT_VM` |
| `mise run conformance` | 256 s | everything but the runtime suites, which skip themselves |
| `FEINT_VM=incus-ovn mise run conformance` | **1331 s** | all of it |

**One decision dominates every other**: does the change reach
`internal/core/machine`. Answer it right and the work costs seconds; answer it
wrong and it costs 590 s or 1331 s. The rest of that table is worth tens of
seconds — `fields` at 208 s against the whole runtime-free pass at 256 s saves
48 s, which is nothing. So `testplan` answers that one question by **reading the
imports** of the files you changed rather than by consulting a list of directory
names, which is the half of it a table would get wrong first.

**The plan replaces the full pass locally. It never replaces the CI matrix.**
Every other gate here adds proof; this one subtracts it, and that inverts the
failure mode — a wrong rule is silent. With CI untouched, the worst case of a
wrong rule is a red pull request, which is exactly what CI is for. What no test
can catch is a rule that names a real directory and prescribes the wrong leg:
the two mechanical rots are guarded in `tools/testplan/plan_test.go`, the
semantic one is not, and it is written down rather than pretended away.

The table it reads, in short form:

| what your change touches | run as well | what that proves |
|---|---|---|
| a route, a response shape, an error | `mise run conformance` | that a real client still passes |
| a gate, a score, a verdict about a run | `mise run conformance:leg -- <leg>` | that it holds on a **partial** run |
| a guard, a validation, a refusal | `mise run falsify -- <spec.json>`, then declare it in `tools/falsify/specs/` | that the test fails without it, on every later day too |
| `internal/core/machine`, a network, a firewall | `FEINT_VM=incus-ovn mise run conformance:leg -- runtime` while you work, `FEINT_VM=incus-ovn mise run conformance` once before you push | that the runtime accepts what the driver sends |
| an artefact under `coverage/` | `mise run evidence:update` | that the record did not narrow |

**The second row is the one that cost two red pull requests.**
`mise run conformance` drives every suite against a single emulator, which is
exactly the population a verdict like *no answer of this run carried this field*
assumes: a whole-run gate is green there **by construction**. CI does not do
that. The conformance workflow splits the clients across a matrix, one emulator
per leg, so every leg but `fields` is a partial run — the `probe` leg drives no
client at all, and the `terraform` leg drives no `octl`.

`mise run conformance:leg -- probe` reproduces one of those legs locally, and
the reproduction is proved rather than assumed: put back the guard that was
missing and the leg names, locally, the same fields the failing CI job listed.
`TestEveryMatrixLegCanBeReproducedLocally` now holds the two lists together, so
a leg the matrix renames cannot stay unreproducible.

**The fourth row's leg is `runtime`, and it is not a matrix entry.** The four
suites that need real machines belong to `runtime-proof.yml`, not to the
conformance matrix, and until #459 nothing reproduced them but the whole
`FEINT_VM=incus-ovn mise run conformance` — 1331 s measured on 2026-08-27, of
which those four suites are 675 s and the client suites in front of them 656 s
that a change to the machine layer never needed. The leg itself measured 590 s
on the same station, doorstep to doorstep:

```bash
FEINT_VM=incus-ovn mise run conformance:leg -- runtime
```

It refuses to run with no runtime rather than letting its suites skip
themselves: a leg asked for by name that measures nothing must not report
success.

The general form, worth carrying elsewhere: **a check whose verdict is about
"this run" must be exercised on the poorest run that will trigger it, never on
the richest.** The richest is the one you have in front of you, which is what
makes the mistake so easy.

### Falsifying a guard

A test that passes proves nothing on its own: it may be asserting something that
was already true. `mise run falsify -- tools/falsify/specs/<name>.json` removes
the guard in a copy **outside** the repository and requires the named test to go
red.

It refuses one mutation shape outright, before copying anything, and the reason
is measured rather than theoretical. On one day in August 2026 the same mistake
voided a verdict three times, in three unrelated issues:

```go
if ok && fe.EnforcesFirewall() {   ->  if ok {        // fe is now unused
if strings.TrimSpace(s) == "" {    ->  if false {     // s  is now unused
if err != nil || n == 0 {          ->  if n == 0 {    // err is now unused
```

Go refuses to compile an unused variable, so each mutation broke the build — and
a mutation that does not build fails *every* test, which looks exactly like the
guard being proven. So: **never delete the term that mentions a name.**
Neutralise the condition with every name still evaluated:

```go
if ok && fe != nil {
if strings.TrimSpace(s) == "\x00never" {
if (err != nil && false) || n == 0 {
```

`mise run falsify -- --selftest` holds that rule against those four real cases
and against their fixes, and `mise run prepush` runs it. Both halves matter: a
rule that refused every mutation would pass the first and make the tool useless.

**Declare the mutation, do not just run it.** A falsification proves a test bites
*on the day it is run*, and one run in a branch that later disappears leaves a
claim about the past. This repository has already published one that was false —
the 0.8.0 CHANGELOG says neutralising any of three locks reds the barrage, and
thirty runs later stayed green with a lock removed. So the mutation goes in
`tools/falsify/specs/`, beside the guard, and `mise run falsify:all` replays
every one of them nightly (`.github/workflows/falsify.yml`).

The first full replay found four of eight specs no longer holding, and none of
it was rot in the emulator: three mutations were written in the deleting style,
one named two guards a later rewrite had removed, and one declared a test that
did not match the guard under it. That last one compiled and left the named test
green, which is the case the replay exists for, and which no amount of `go test`
can see. `mise run falsify:proof` is that claim measured: it loosens an assertion
until it cannot fail, then requires the replay to go red naming it while the
suite stays green.

`mise run conformance` is deliberately **not** in the hook. It needs `scw`,
`octl`, `exo` and Terraform installed and takes minutes; as a hook it would
fail on a missing binary rather than on your code, and the reflex it would teach
is `--no-verify`, which turns off every hook at once. A gate people routinely
skip is worse than no gate.

### If you are contributing from a fork

Nothing will run on your pull request until a maintainer approves it. GitHub
holds every workflow in `action_required` for contributions from forks, which is
the right default — it is what stops a fork from editing a workflow to read this
repository's secrets — but it means `gh pr checks` answers "no checks reported"
and you see no feedback at all, not even a failing one.

So the local gates are not a nicety for you, they are the only ones you have
until someone looks:

```bash
mise run check         # what CI runs on every pull request
mise run conformance   # the real clients, for anything touching a route or a suite
```

A pull request has already been merged here that broke the Exoscale conformance
suite, with nothing on the page to say so. Running the suite yourself is what
would have shown it, and it is why the AI-assisted checklist asks whether you
ran it rather than whether you expect it to pass.

The upstream SDKs the drift scan reads are cloned on demand:

```bash
mise run upstream:sync
```

## Adding emulated surface

The whole of it, in four points:

1. Read the upstream SDK source for the exact shapes, not the web docs.
2. Trace the real client (`scw -D <command>`) and emulate the **whole** sequence:
   a create is rarely one call.
3. Declare the upstream operation on every route; declare what you do not serve
   in `Declined()`, with a reason.
4. Tests: lifecycle round-trip, pagination, scoping, one per error shape. Fuzz any
   new request decoder.

## Offering a stack you already run

The most useful contribution here is not always code. A Terraform or OpenTofu
root that you run as a required gate on your own infrastructure is evidence we
cannot manufacture: it changes on your schedule, against providers you resolve
fresh, and that is how the break in Scaleway provider 2.81.0 reached us (#325).

What we ask of one, what we do with it, and what we deliberately do **not** do
with it — no, we will not clone your repository in our CI — is written out in
[`examples/stacks/README.md`](examples/stacks/README.md#offering-your-stack-what-we-ask-and-what-we-do-with-it).
The register of the ones already replayed is
[`examples/stacks/surveyed.md`](examples/stacks/surveyed.md).

## Upstream moved

The weekly workflow opens a pull request when the provider API changes. Triaging
it is the whole job: each new operation ends up implemented or declined with a
reason, never silent, and an orphan route is investigated before the baseline is
refreshed. "Not done yet" and "out of scope" are different answers and only the
second belongs in `Declined()`.

## Issues

Three forms, one automated flow, one rule.

**How an issue is born.** A broken official client uses *An official client did
not behave* — that report outranks the whole roadmap: the order there is a guess
about what people need, a client that breaks is a fact. A route you need uses
*An operation is missing*. A batch of [docs/roadmap.md](docs/roadmap.md) uses
*Roadmap batch*, one issue per identifier from its table. The weekly drift
workflow opens and updates its own issue under the `drift` label; nobody opens
that one by hand.

**How a title reads.** Two forms, because issues here do two different things,
and mixing them costs the ability to cite one.

A **defect** is titled by the sentence that says what breaks, with no prefix:
*A stopped Vm outside a Subnet loses its PrivateIp*, *DryRun: false makes the
project's own conformance gate fail*. A symptom, not a diagnosis and not a
proposed fix — the diagnosis is often wrong at the time the issue is opened, and
the title outlives it.

A **unit of delivery** is titled `<CODE>-<n>: ` then what the batch makes true.
The codes are `SW`, `OSC`, `EXO` for a pack, `UI` for the interface, `X` for
anything that crosses them: *OSC-2: ProductCodes, admin password, tags*. The
code is not decoration. Commits close batches by naming it — `Closes #6
(OSC-2)` — docs/roadmap.md orders by it, and the *Roadmap batch* template has a
field for it. **An issue without a code is an issue no commit can name.**

Six issues were opened in one afternoon without one, while that template field
had existed all along. Anything a title is supposed to carry gets carried by
whoever remembers, so read this section before opening rather than after.

A question to be settled is neither of the two: it opens with `Reopen` or
`Decide` and states the question, because a title that reads like a batch
invites someone to implement what has not been decided.

**What closes an issue.** The command in its "closed by" field, run and green —
never an intention. Roadmap batches close on the four "When a batch is done"
conditions of docs/roadmap.md, which every batch issue repeats verbatim rather
than paraphrases.

**How the labels read.** Four axes, at most one label from each. What it is:
`bug`, `enhancement`, `roadmap`, `drift`. Which pack: `scaleway`, `outscale`,
`exoscale` — absent means cross-cutting. Which shared layer, only when it is
the subject: `core`, `machine`, `conformance`. State: `blocked`, with the
blocker named in the body. There is no `wontfix` label: the project's word for
out of scope is a decline in the pack, with a reason.

**Milestones** are the six waves of the roadmap's sequence — an order, not a
schedule, so they carry no due dates.

`tools/issues/setup.sh` creates the labels and milestones (`--dry-run` shows
what it would do); `tools/issues/batches/` holds the eighteen batch
definitions, ready to become issues.

**The batches are steered from a project board** — views, fields, and what
"ready to start" means are in [docs/project.md](docs/project.md). Batch issues
carry native blocked-by dependencies, and the `Unblock` workflow moves the
`blocked` label when a blocker closes; nobody maintains it by hand.

## Commits

[Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/), scope by
area:

```text
feat(scaleway): emulate flexible IP attach/detach
fix(core): keep list order stable across pages
chore(drift): refresh the upstream baseline
```

**The subject is not a style preference: the version is derived from it.**
`cz bump` reads the commits since the last tag and decides the increment — `fix`
moves the patch, `feat` the minor, a `!` before the colon marks a break, and
before 1.0 that break stays inside `0.x` rather than announcing 1.0.0. A subject
that does not parse is a release that computes the wrong number, and the number
is what users pin.

Checked twice, by the same tool: the `commit-msg` hook refuses the message as you
write it, and the `Commits` workflow checks every subject a pull request adds —
because a hook lives in `.git/hooks/`, which no clone carries.

```bash
cz check --rev-range origin/main..HEAD   # what CI will say
cz bump --dry-run                        # the version your commits imply
```

## AI-assisted contributions

**They are welcome, under one condition, and the condition is not about AI.**

This project happens to be unusually well equipped for this question. Most
maintainers rejecting AI-generated pull requests are rejecting an asymmetry: the
contributor spends a minute, the reviewer spends an hour proving the change is
wrong. curl states it as a principle — *a contribution should be worth more to
the project than the time it takes to review it.*

Here, the burden of proof is already machinery. A change is not judged by how it
was written; it is judged by whether the official client drives it. That check
costs the reviewer one command and it cannot be argued with:

```bash
mise run check          # gofmt, vet, lint, go test -race
mise run conformance    # scw, octl, exo, Terraform, OpenTofu, and the probe
mise run drift:check    # the upstream surface still matches the baseline
```

So the rule is the same for everyone: **bring the evidence, not the intention.**
A pull request that says "this emulates DeleteSnapshot" and shows nothing is
refused whether a human or a model wrote it. One whose conformance suite passes
is worth reviewing whatever produced it.

Three things follow, and they are not negotiable.

### 1. Disclose it

Use the `Assisted-by:` git trailer, naming the tool and the model version. This
is the convention the Linux kernel, Fedora, Mesa and QGIS converged on, and it
exists so that a future reader of `git log` knows what to re-check.

```text
feat(outscale): emulate Nic, LinkNic and UnlinkNic

Assisted-by: Claude Code (claude-opus-5)
```

Disclose when a model wrote a substantive part of the change. Do not bother for
spelling, formatting, or a completion that saved you three keystrokes.

`Co-Authored-By` for a tool stays forbidden, and so does `Signed-off-by` from
anything that is not a person. Those trailers assert authorship and certify
origin; a model cannot do either. The human submitting is the author, and takes
responsibility for every line — licensing included.

### 2. Run it before you send it

Not "the tests should pass" — **run them**. Specifically:

- `mise run conformance`, or at minimum the suite of the provider you touched. A
  unit test never proves a response shape; only the real client does. This is
  the rule that catches the failure mode AI-assisted changes have most often
  here: a plausible field name that the provider's API does not define.
- With `--contracts contracts`, so every response is validated against the
  provider's own OpenAPI document. A field a model invented fails the run.
- Check `/_feint/conformance | jq .unread_request_fields`. A field declared on a
  request struct and never read is invisible to that report — it is the one blind
  spot, and it is how a Vm reported success while going nowhere.

### 3. Do not send what you have not read

The specific failure this project would suffer is not bad code; it is
**confident code that emulates an API nobody checked**. A model will happily
produce a handler for `CreateSnapshot` with a response shape that reads
perfectly and matches nothing upstream, and it will write a test asserting its
own invention — which is exactly the defect
[`docs/limits.md`](docs/limits.md) records the project already shipped once, with
an invented `private_ip` that its own suite read back.

So: if you cannot say where a field name came from — their SDK, their OpenAPI
document, or a run of the real client — do not send it. "The model produced it"
is not a source. That is the whole of it: **in case of doubt, read the SDK, not
the documentation, and never invent a format.**

### What gets refused on sight

- A pull request whose conformance suite was not run, and that says so or shows it.
- A batch of changes across several packs with no single reviewable claim.
- A change that adds a route without `Route.Operation` at the upstream name, or
  without updating `coverage/`. The pre-commit hooks catch both; a pull request
  that failed them and was sent anyway is a signal about the rest.
- Anything whose stated justification is that it improves a metric — coverage
  percentage, number of routes — rather than unblocking a client.

None of this is aimed at AI. It is what the project already asked of everybody;
writing it down is what makes the answer the same for both.

## Code

Go, standard library first. A new dependency is a decision to justify in the pull
request, not a detail. Beyond that: explicit errors, no premature abstraction,
tests beside the code, and a comment that says why rather than what.

## Security

Do not open a public issue for a vulnerability. Report it privately through GitHub
private vulnerability reporting.

Remember what Feint is: a service that authenticates nobody and grants
everything. Any change that makes it easier to expose on a real network deserves
scrutiny.
