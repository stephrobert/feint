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

It installs two hook types, and the second one is easy to miss: `pre-commit` and
`commit-msg`. The configuration declares both through
`default_install_hook_types`, so the single command is enough.

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

The whole of it, in five points:

1. Read the upstream SDK source for the exact shapes, not the web docs.
2. Trace the real client (`scw -D <command>`) and emulate the **whole** sequence:
   a create is rarely one call.
3. Declare the upstream operation on every route; declare what you do not serve
   in `Declined()`, with a reason.
4. Tests: lifecycle round-trip, pagination, scoping, one per error shape. Fuzz any
   new request decoder.

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
mise run conformance    # scw, oapi-cli, exo, Terraform, OpenTofu, and the probe
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
