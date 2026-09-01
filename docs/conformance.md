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
      (scw, octl, exo, Terraform)           route from the contract
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

- **The second witness** (`coverage/contract-only.json`) closes the gap the
  first three cannot see. All of them read the *scan*, so an operation the
  provider publishes and their Go SDK never wrapped is outside `total` — and
  `unknown: 0` is then exact over a set that does not contain it. `feint
  coverage --document ... --contract-only ...` cross-checks the two inventories
  and exits 2 on an operation nobody decided about.

Weekly, `.github/workflows/drift.yml` scans and opens a pull request when
something moved. The human work is triage.

### What `total` counts, and why a document can declare more

`total` is the **SDK surface**, deliberately, and that is a decision rather than
an accident (#622). The official clients this project exists to satisfy — `scw`,
the Terraform provider, the SDK itself — are built on that SDK, so an operation
it never wrapped is one none of them can reach.

The two inventories agree by construction for two of the three providers:
Exoscale's coverage *is* its document, and Outscale's SDK is generated from
theirs. Scaleway is where they part, because its contract comes from the portal's
`schema.yml` per product while its coverage comes from a Go SDK scan. Six
operations sit in that gap: five `PUT` whole-object siblings in `instance/v1`
and `iam/v1alpha1.CheckPermissions`.

Nothing is wrong at runtime — an unserved route answers 501 and points at
`/_feint/routes`. What was wrong is that those six had been offered to no
triage, and *not done yet* and *out of scope* are different answers. They are in
`coverage/contract-only.json` now, each with a status and a reason, and a seventh
appearing there fails the gate rather than passing unseen.

They stay out of `total` because the denominator would otherwise mean two things
at once. A client generated from the document rather than from the SDK — which is
where #622 came from — reads that file to know where it stands.

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
is about clients. `tools/conformance/<provider>/` drives `scw`, `octl`,
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

### `driven: false` says why, or it is a failure

An axis that is absent states a fact; it does not explain one, and for `driven`
that gap was the debt #174 measured. Fifty-three operations were mounted and
unproven, and nothing separated `instance/v1/API.UpdateServer` — every in-place
change a `terraform apply` makes, missed because the fixture only ever created
and destroyed — from `osc/Client.ReadPublicIpRanges`, which no fixture has a
reason to call. The first was work anybody could do; the second is not, and the
record read them the same.

So a route now declares it: `emulator.Route.Undriven` carries one line saying
why no official client reaches that operation, and
`TestEveryUndrivenOperationSaysWhy` fails when the recorded run leaves an
operation undriven with nothing said, **and** when a reason survives the client
that came to drive it. The second half is the one that keeps this honest: the
reasons for the whole `block/v1` write path were true until the Terraform
fixture declared a `scaleway_block_volume`, and a sentence that outlives its
cause reads exactly like a considered decision.

The reasons are printed under each pack in [routes.md](routes.md), and the
README banner counts them apart from the rest, so what is left is a work list
rather than a number.

The record is regenerated by `mise run evidence:update` from **two fresh legs** —
machines off with the probe, then machines on — joined by taking the stronger
answer per axis. That join is safe *only* because both legs are fresh, and a
narrowing is refused: an artefact that loses a runtime fails rather than
publishing a smaller truth quietly.

### An injected refusal is not evidence

The emulator can be made to refuse on purpose: `PUT /_feint/faults` arms a rule
naming an operation and a status, off by default, deterministic, cleared by a
`DELETE`. It exists because `negative` stood at 34 of 357 while every other axis
stood far above it — this emulator proved what its routes answer when everything
goes well and almost nothing about what they answer when it does not,
so a client's degradation paths could only be simulated in that client's own
tests.

**What an injected refusal proves is what the client does, and nothing about the
real cloud.** A 403 here is not evidence that Scaleway answers 403 to that call,
or with those fields; it is evidence that `scw` decodes a `permissions_denied`
body as `PermissionsDeniedError` rather than as a missing resource, that
`terraform apply` survives two 503s on `ReadNets`, that `exo` retries a 503 five
times before giving up. Those are facts about the client, they are the ones a
consumer needs, and they were unobservable before. What the real cloud answers
where remains a question only a recording can settle — link 4 above, and it is a
different mechanism on purpose.

The bodies still come from upstream: the shape is the provider's own error
struct, and where an SDK names a `type` for a status (Scaleway's
`permissions_denied`, `denied_authentication`) the pack emits that type so the
client's own dispatch fires. Where none is named — nobody here has measured how
Scaleway spells a 429 — the value says plainly that it is this emulator's, since
publishing a plausible one would be inventing a fact about a provider.

And the bound that keeps the numbers honest: **an answer the injector produced
moves no axis.** The operation stays `driven: false`, its response is not
contract-checked, its fields join no union, and the emulator *refuses* to close a
`negative` assertion span on it. Injected answers are counted apart, under
`injected` on `/_feint/conformance`, and `tools/conformance/score.sh` fails any
run that carries one — so fault injection cannot raise the very number it was
built to expose. `tools/conformance/faults.sh` drives the four real clients
through all of this, on an emulator of its own, on its own port.

## Link 6 — the published surface, and its version

`/_feint/health`, `/_feint/routes`, `/_feint/conformance`, `/_feint/trace` and
`/_feint/faults` are what a pipeline reads. Since #132 each has a committed
fixture under `internal/cli/testdata/frozen/` — the field tree, never a value —
and two tests gate them:

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
| every declared falsification still bites | | | ✔ | |
| frozen surfaces and their versions | ✔ | ✔ | | ✔ |
| a consumer's expressions against the previous release (`compat:check`) | | | | ✔ |

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
  must compile, or every test fails and that looks exactly like success. The
  mutations are declared and replayed, which is the section below.
- **Generated is not derived.** A block under a "do not edit by hand" marker
  whose content is a constant one file away is a hand-written claim wearing a
  generator's clothes. It happened, twice.
- **An undeclared property counts as absent.** A driver capability nobody
  declares is missing, so a check skips rather than asserting what nobody
  promised. A run that does not declare it was whole counts as partial.
- **Understating a proof costs as much as overstating one.** An external review
  once recommended deleting a Terraform suite from the README because a table
  did not credit it. The suite applied twenty-one resources and passed.

## The guards, replayed

Everything above is held by tests, and a test can stop biting without anybody
noticing. That is not a hypothetical here: the 0.8.0 CHANGELOG states that
*neutralising any of the three locks makes the barrage go red on the first
attempt*, and thirty consecutive runs later stayed green with a lock removed. A
falsification proves a test bites **on the day it is run**, and every falsification
of the 0.9 train was run once, by hand, in a script deleted with its branch.

So the mutations are declared next to the guards they neutralise, in
`tools/falsify/specs/*.json` — file, exact edit, and the test that must go red —
and replayed nightly:

```bash
mise run falsify:all           # every declared falsification
mise run falsify -- tools/falsify/specs/mispointed.json   # one of them
mise run falsify:selftest      # the harness against its own history
```

Each mutation is applied in a copy outside the working tree, one at a time, and
three verdicts are distinguished rather than merged:

| verdict | what it means |
|---|---|
| the test bit | the guard is measured, today |
| **test still passed** | the guard is not measured, and the test is what to fix |
| **did not compile** | void, not favourable: every test fails, which reads like success |
| **did not apply** | the code moved out from under the declaration |

The last two are the ones that make this worth running. Warning about them was
not enough — the compile mistake voided three verdicts in one day, in three
unrelated issues — so the harness refuses a mutation that drops a name the
original expression used, and demands the neutralising form (`… && false`,
`(… || true)`) instead.

The first full replay was worth its cost immediately, and not by finding rot in
the emulator. Of eight specs, four did not hold: three had been written in the
deleting style the rule refuses, one named two guards that #179's final form had
removed, and one declared a test that did not correspond to the guard beneath it
— two `synthetic` conditions one screen apart in the same file, one belonging to
#88 and the other to #163. That last one is the case the replay exists for: the
mutation compiled, and the named test stayed green.

## What is not closed yet

Nothing, as of 2026-08-18 — and the date matters more than the sentence,
because this section has emptied one issue at a time and a new link that opens
belongs here the day it is stated.

The last one closed with
[#170](https://github.com/stephrobert/feint/issues/170): nothing measured what
a release does to a consumer. `schema_version` is the signal that lets a
pipeline notice a break, and nothing checked that a pipeline *could* notice —
`probed` went from boolean to string, and a consumer reading it as truthy
counts every refusal as a success. `mise run compat:check` now builds the
previous release out of this repository's own history, runs expressions a
consumer could legitimately have written against both binaries, and sorts each
into compatible, explicitly broken or **silently wrong**; one unaccepted
silently-wrong verdict refuses the tag, enforced by `tools/release/preflight.sh`.
What it found against 0.8 — including the one boundary it cannot protect — is
in [RELEASING.md](../RELEASING.md#what-the-measurement-found-and-the-one-boundary-it-cannot-protect).

The two closed before it are folded into the page above.
[#171](https://github.com/stephrobert/feint/issues/171) gave the evidence record
a provenance the join compares, so deleting a conformance assertion demotes what
it proved instead of being a sentence written twice;
[#169](https://github.com/stephrobert/feint/issues/169) is the replay described
one section up.

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
