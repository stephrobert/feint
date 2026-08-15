# The conformance system

**Read this in another language:** [Français](./conformance.fr.md)

This project's claim is one sentence: *the official client cannot tell the
difference*. This page is how that sentence is kept honest — the whole chain,
from the provider's own API description down to a pipeline that reads a number
out of this emulator, and what each link does and does not prove.

It exists because the chain is now long enough that no single gate explains it,
and because a reader deserves to know which links are closed and which are not.
The open ones are named at the end, with their issues. A page that only
described the finished half would be the exact defect this project spends its
time removing.

[Architecture](architecture.md) covers how the code is laid out;
[limits.md](limits.md) covers what the emulator deliberately does not do. This
page covers how anything here gets to be called *proven*.

## The chain

```text
                       the provider's upstream SDK
                                   │
                    surface scan ──┴── baseline          drift gate
                                   │
                          API description artefact
                              (contracts/)
                                   │
                 ┌─────────────────┴─────────────────┐
                 │                                   │
         a real client drives              the probe drives every
      (scw, oapi-cli, exo, Terraform)       route from the contract
                 │                                   │
                 │           recordings of a real cloud
                 │                (shapes/)
                 │                     │             │
                 └─────────┬───────────┴─────────────┘
                           │
                    the evidence record
                 (seven axes, never summed)
                           │
                   /_feint/conformance
                    + schema_version
                           │
                     a CI consumer
```

Every arrow is a place a claim can be lost or invented, and each has its own
control below.

## Link 1 — the upstream, measured rather than followed

The differentiator of this project is that **the API is measured, not tracked by
hand**. Scaleway added 363 operations and removed 25 in twelve months; nobody
follows that from memory.

- **The surface scan** (`internal/drift`) reads the provider's official Go SDK,
  which is generated from their IDL and therefore exact. No network call, no
  guess. It reads every non-test `.go` file, not only the generated ones,
  because Scaleway hand-writes public entry points in `*_utils.go` that delegate
  to unexported generated methods.
- **The baseline** (`coverage/*-baseline.json`) is versioned. An operation that
  appears upstream and that nobody triaged fails CI with exit code 2.
- **Triage is binary and explicit.** An operation is served, or it is in
  `Declined()` with its reason in a comment. *Not done yet* and *out of scope*
  are not the same thing, and a decline that says neither is an untriaged
  operation wearing a decision's clothes.

Weekly, `.github/workflows/drift.yml` scans and opens a pull request when
something moved. The human work is triage.

## Link 2 — the contract, and what a green run does not prove

`contracts/` holds each provider's API description, extracted from their own
document. Every response the emulator writes while `--contracts` is on is
validated against it, and a violation fails the run.

This control is **one-directional**, and saying so is not a caveat, it is the
reason link 4 exists: it catches a field the emulator *invents*, and it can only
catch an *omitted* field where the provider marked it `required` — Scaleway does
on 11% of its schemas. [limits.md](limits.md#what-a-green-contract-run-does-and-does-not-prove)
carries the measured detail, including why the three descriptions are not
equally strong.

## Link 3 — two witnesses, and neither is the other

**A real client** is the only thing that can prove the claim, because the claim
is about clients. `tools/conformance/<provider>/` drives `scw`, `oapi-cli`,
`exo`, Terraform and OpenTofu against the emulator. What a suite asserts is what
is proven; nothing more. Two defects were `driven` the whole time they were
wrong.

**The probe** (`internal/probe`) drives every mounted route from the provider's
own API description, which is broader than any set of cases somebody thought to
write. Since #163 it *seeds*: before probing an operation it brings into being
what that operation needs, from the contract's request schema and from resources
it created earlier in the same run. No identifier is invented — every one comes
from a real create against the emulator.

The probe proves **protocol, not behaviour**. A well-shaped empty inventory
passes. Cursors and filters are not exercised. That is why the two numbers are
reported separately and **never added**: a route the probe reached stays on the
backlog until a client drives it.

Synthetic traffic is fenced off wherever a number faces a user: it feeds no
unread-fields report, no omission verdict, and no client-facing score. The rule
is one sentence — *synthetic traffic moves no client-facing number* — and it is
enforced, not stated.

## Link 4 — the recordings, the only source that looks the other way

`feint proxy` records a real client against a real cloud; `feint shapes` distils
those exchanges into a field tree per operation, committed under `shapes/` with
no values, only names and types.

This is the **only** control in the repository that looks in the omission
direction, and #88 made it a gate. A declared field fails a run when **both**
sources vouch for it — the document declares it and a recording carries it — and
no answer of the run ever carried it, though its container was served.

Both halves are needed, and each alone was measured wrong:

- the document alone **over-declares**: of 106 declared-but-absent fields a
  recording could arbitrate, 83 were absent from the real cloud's answer too;
- a recording alone **under-declares**: it covers only what somebody recorded.

What no source can arbitrate is **published rather than failed on**, under
`fields.unconfirmed`. Each entry is one `feint shapes --record` away from
becoming either a finding or nothing.

The verdict is over **a whole run**, and a run says so: `FEINT_FIELD_GATE=1` is
set by `mise run conformance` and by the `fields` leg of the conformance
workflow, which drive every client against a single emulator. Everywhere else
the findings print and judge nothing, because a leg that never exercises a
feature legitimately never serves the fields that feature produces. An
undeclared whole run counts as partial.

## Link 5 — the evidence record, seven axes, never summed

`coverage/evidence.json` is what all of the above adds up to, per operation.
Seven axes, each answering a different question, each meaning exactly what its
name says and no more:

| axis | what it says | what it does not say |
|---|---|---|
| `driven` | a real client reached this operation | that the suite asserted anything about it |
| `probed` | `response`, `refusal` or `none` — what the probe *validated* | that behaviour, cursors or filters work |
| `contract` | `clean`, `violating` or `unchecked` | that an unchecked operation is fine |
| `dataplane` | it was driven while a machine runtime was configured | that a machine-level assertion named it |
| `shape` | a recorded real-cloud answer covers it | that every field was compared |
| `behaviour` | a resource's full lifecycle was observed inside a declared assertion span | that this operation's own effect was asserted |
| `negative` | it really answered 4xx where a suite demanded a refusal | that every refusal it owes was reproduced |

**They are never summed.** A single number would let a weak axis carry a strong
one, which is the arithmetic version of the overstatement this project exists to
avoid. `contract: unchecked` is not a pass, and `probed: refusal` does not say a
success shape was ever seen.

The record is regenerated by `mise run evidence:update` from **two fresh legs** —
machines off with the probe, then machines on — joined by taking the stronger
answer per axis. That join is safe *only* because both legs are fresh, and a
narrowing is refused: an artefact that loses a runtime fails rather than
publishing a smaller truth quietly.

## Link 6 — the published surface, and its version

`/_feint/health`, `/_feint/routes`, `/_feint/conformance` and `/_feint/trace`
are what a pipeline reads. Since #132 each has a committed fixture under
`internal/cli/testdata/frozen/` — the field tree, never a value — and two tests
gate them:

- a shape that moves while the fixture does not fails;
- a fixture that moves while the declared `schema_version` does not fails.

The history is append-only. Changing a frozen surface on purpose is four steps
in one commit, written out in [RELEASING.md](../RELEASING.md#frozen-surfaces).

What is deliberately **not** frozen: the prose around the verbs, the values
behind every key, and the fields only some exchanges carry. A freeze that caught
those would go red on routine runs and be disarmed within the week.

## Where each gate runs

| gate | pre-commit | pull request | nightly | release |
|---|:-:|:-:|:-:|:-:|
| `gofmt`, `vet`, `golangci-lint`, `go test -race` | ✔ | ✔ | | ✔ |
| every route declares its upstream operation | ✔ | ✔ | | ✔ |
| generated sections match their artefacts | ✔ | ✔ | | ✔ |
| routes match the provider's API description | ✔ | ✔ | | ✔ |
| drift against the baseline (exit 2) | | ✔ | ✔ | ✔ |
| the real clients, per client | | ✔ | ✔ | asked of CI |
| the omission gate (whole run) | | ✔ (`fields` leg) | ✔ | asked of CI |
| the machine runtime, both modes | | | ✔ | |
| frozen surfaces and their versions | ✔ | ✔ | | ✔ |

The machine-runtime proof is deliberately not on the pull-request path. A gate
that reds on runner weather gets disarmed, which is worse than not running; its
promotion is earned by measurement — fourteen consecutive green scheduled runs,
counted by a job rather than by somebody's memory (#125).

## The rules the whole thing obeys

Four sentences govern every link above. Each was paid for.

- **A comment is not a control.** When a defect is fixed, the comment cites the
  test that fails without the fix. Three audits proved, independently, that a
  fix written in prose and never asserted survives for months because it reads
  like evidence. `/falsify` is the executable form: remove the guard in a copy
  outside the repository, and require the named test to go red — the mutation
  must compile, or every test fails and that looks exactly like success.
- **Generated is not derived.** A block under a "do not edit by hand" marker
  whose content is a constant one file away is a hand-written claim wearing a
  generator's clothes. It happened, twice.
- **An undeclared property counts as absent.** A driver capability nobody
  declares is missing, so a check skips rather than asserting what nobody
  promised. A run that does not declare it was whole counts as partial.
- **Understating a proof costs as much as overstating one.** An external review
  once recommended deleting a Terraform suite from the README because a table
  did not credit it. The suite applied twenty-one resources and passed.

## What is not closed yet

Three links in the chain above are stated but not enforced. They are open
issues, and they are named here because a page describing only the finished half
would be the defect this project removes.

- **A falsification proves a test bites on the day it is run, and nothing runs
  it again** ([#169](https://github.com/stephrobert/feint/issues/169)). Each
  mechanism was falsified at its pull request; nothing replays those mutations.
  A falsification claim in this repository has already been false — thirty green
  runs with a lock removed, recorded in the 0.8.0 CHANGELOG.
- **Nothing measures what a release does to a consumer**
  ([#170](https://github.com/stephrobert/feint/issues/170)). `schema_version` is
  the signal that lets a pipeline notice a break; nothing checks that a pipeline
  *can* notice. `probed` went from boolean to string, and a consumer reading it
  as truthy counts every refusal as a success.
- **The evidence record's freshness rule is written twice and enforced nowhere**
  ([#171](https://github.com/stephrobert/feint/issues/171)). *Deleting a
  conformance assertion must demote the operations it proved* appears in
  `internal/cli/evidence.go` and in `mise.toml`, and no test holds either
  sentence.

Until those close, the honest statement is the one this page opens with: the
chain is measured, link by link, and these three links are measured by prose.

## Reading the numbers yourself

```bash
feint serve --contracts contracts &
curl -s localhost:4599/_feint/conformance | jq '.evidence["instance/v1/API.ListServers"]'
curl -s localhost:4599/_feint/health | jq '{schema_version, capabilities}'
```

The generated tables in the [README](../README.md#status) and in
[docs/routes.md](routes.md) are rendered from the same artefacts, by
`mise run docs:coverage`, and `feint docs --check` exits 2 when a page and its
artefact disagree.
