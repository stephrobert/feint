# What a fourth provider pack touches

An audit with one question: is adding a fourth cloud cheap? Measured on
`integration` at 6ece38c (2026-08-10), with the three existing packs as the
sample.

> **Read the dates before the numbers.** The body of this document is the
> 2026-08-10 measurement, kept as it was written. What has moved since is
> collected under [Since the audit](#since-the-audit) at the end: two of its five
> recommendations have landed, the packs have grown, and the one figure worth
> defending — zero provider-named code in `internal/core` — is now held by a test
> instead of by an audit somebody has to redo. The rest of the numbers are a
> snapshot of a repository that has had a milestone happen to it, and should be
> re-measured before anything is concluded from them. They are a good sample because they disagree on everything a fourth
provider could disagree on: Scaleway signs nothing and is redirected by one
variable, Outscale signs the Host header of a POST-action API, Exoscale follows
an address the server returns and writes its verbs inside a path segment
(`PUT /instance/{id}:stop`).

The method matters as much as the verdict: `grep` finds **226 mentions of a
provider name in 47 non-test files outside `internal/providers/`**, which reads
as heavy coupling and is not. **21 lines of them are code**; the rest are
comments citing a measured example, which is this repository's documentation
style. The map below covers the 21, plus the non-Go surfaces (`mise.toml`,
workflows, conformance).

Headline first: **`internal/core` contains zero provider-named code.** All of
its mentions are comments. Rule 5 holds under measurement, including in
`machine/` and `cloudinit/`, where provider differences are fields
(`Binding.Prefix`, `Boot.User`, `AddressKey`) that a fourth pack fills without
opening the package.

## The security finding, first

`internal/proxy/redact.go` redacts by **name pattern**: eight substrings
(`auth`, `token`, `key`, `secret`, `signature`, `cookie`, `passw`,
`credential`). The three known dialects are covered because their carriers
happen to be named `X-Auth-Token` and `Authorization`, and a test holds those
three by name. A fourth dialect is covered only if its header names happen to
contain one of the eight substrings. Real schemes exist whose credential
travels under a name that matches none of them (a consumer key, a session id,
an access header named after the vendor). `feint proxy` pointed at such a cloud
writes that value verbatim into the transcript.

This is the "well-formed is not authorized" family again: the rule validates
the *shape of a name* where the property that matters is *this value is a
credential*. The list being over-inclusive on purpose does not help a name it
never sees.

Two remedies, smallest first:

- **Allowlist request headers.** Request headers are almost never load-bearing
  in a transcript (`Content-Type`, `Accept`, `User-Agent` are); redact every
  other request-header value by default and keep the pattern rule for bodies
  and responses. This is redaction by construction, the same property
  `internal/upstream` already has by not recording request headers at all,
  and that package is the model here. The proving test records an exchange
  whose credential header matches no pattern and asserts the transcript
  carries no value.
- Or an operator-declared carrier (`feint proxy --carrier X-Foo-Session`),
  which works but is a control someone forgets exactly once.

Until one of them lands, the rule is: **recording a client of an unknown cloud
requires reading its auth scheme against the carriers list first.** That is a
comment where a control should be, which is why this section is first.

## The map

### Category 1: data the pack provides; nothing shared changes

The healthy majority. All of it is keyed by `Pack.Name()` and consumed by
generic loaders:

| Surface | How the fourth pack plugs in |
|---|---|
| Routes, operations, refusals | `Pack.Routes()`, `Route.Operation`, `Declined()` |
| Environment for real clients | `Pack.Env()`; `feint env` iterates mounted packs and names a provider nowhere |
| Contract | `contracts/<name>.json`, generic `contract.Load` + `drift.ScanContract` |
| Coverage and baseline | `coverage/<name>-{coverage,baseline}.json`, generic reader/writer |
| Shapes catalogue | `shapes/<name>.json`, generic `shape.Catalogue` |
| Machine lifecycle | `machine.Binding` fields; the sequence is shared, the vocabulary is the pack's |
| Probe | reads the contract, no provider code |
| Transcript / proxy / trace | provider is a string field in the exchange |

`internal/cli/env.go` is the reference implementation of the doctrine: it
derives everything from the mounted packs and states the test in its own
comment ("could this line be written identically for another provider?").

### Category 2: shared code the fourth pack must edit

Eleven files, roughly 43 additive lines, none of which changes the behaviour
of the three existing packs. Each entry says what would make it data, or why it
should stay code.

Two entries this audit originally counted — hardcoded provider lists in
`internal/cli/shapes_check.go` and `internal/cli/docs.go` — were fixed on
2026-08-11: both now derive the list from the mounted packs, so a fourth pack
is covered by the shapes gate and printed by the banner by construction.
`TestShapesCheckCoversEveryMountedPack` holds the first; the second is held by
`feint docs --check` itself, which regenerates the banner from whatever
`packsFor` mounts.

| File | Edit | Remedy |
|---|---|---|
| `internal/cli/cli.go` (`packsFor`) | +2: import, one constructor | Keep. An explicit registration list beats `init()` magic, and it is the single mount point. |
| `internal/upstream/upstream.go` | +3: `Provider` const, one `case` in `New` | Keep for now. The three dialect conditionals (`if c.Provider == Outscale`) are doctrine-compliant while exactly one provider deviates; the day a second POST-action provider arrives they become fields of a dialect struct, not before. |
| `internal/upstream/credentials.go` | +2 dispatch, then a reader (category 3 content) | Keep. Reading a CLI's config file is provider knowledge; the one-file-per-provider seam already exists (`sign_*.go` pattern). |
| `internal/upstream/reads.go` | +10 to 15 lines of curated data | Keep. The file says why it is curated, and the entries are data already. |
| `internal/cli/status.go:289` (`productsOfProvider`) | +1 data row | Acceptable. Display-only and it degrades honestly (its comment says so). Could become a `Pack` method the day a second consumer appears. |
| `internal/cli/doctor.go` (clients table) | +1 row | Data row, keep. |
| `internal/cli/docs_clients.go` | +1 to 2 rows | Data row, keep. |
| `internal/cli/banner.go:100` | +1 word | The comment explains the wording is deliberate; a human sentence is not a table. |
| `mise.toml` | ~10 lines: upstream:sync source, conformance tasks | Additive task entries. |
| `tools/contract/update.sh` | ~5 lines: one `extract` block | Additive. |
| `.github/workflows/drift.yml` | ~5 lines: checkout + one coverage call | Additive. |

One real duplication exists *today*, not hypothetically: the Outscale dialect
(key prefix, method, path) is written twice, in `upstream.request` /
`operationName` and again in `shapes_check.callIdentity`. Two callers, one
knowledge; a fix in one survives broken in the other, which is this
repository's named failure pattern. The remedy is one exported function in
`upstream` that both consume. Small, and it has its two callers already.

### Category 3: irreducible provider work

Measured against the Exoscale history (the honest baseline: bootstrap at
389ad0e, then 98f8df6 serving 30 more operations in one recorded session):

| Work | Measured cost |
|---|---|
| The pack itself | ~79 lines/operation including tests (Exoscale: 2 562 code + 1 076 test for 46 served ops). Ten operations: ~1 100 to 1 500 lines with scaffolding and catalogue fiction. |
| Signer | `sign.go` is 146 lines for three providers. Budget 40 to 90 lines, **plus one trap**: each of the three signatures hid one (a throttle disguised as a parameter error, a Host that must be signed, an order that must match the SDK). The fourth will have its own, and only the live cloud reveals it. |
| Credentials reader | ~50 to 70 lines (`credentials.go` is 237 for three). |
| Conformance suite | ~80 lines of shell at bootstrap (`exo-cli.sh` started at 79), growing with coverage. Depends on which real client the cloud has, which is a decision, not code. |
| Contract source | If the provider publishes OpenAPI: ~5 lines in `update.sh`, because the third pack already paid for the generic extractor (`extract-outscale.py` −132, `extract-openapi.py` +185) and `ScanContract` reads any `contract.Doc`. If it publishes something else: one converter, ~150 to 200 lines, after which everything downstream is unchanged. The scan is designed to host a fourth reader, not to have three. |
| One core dialect lesson | **2 of 3 packs taught the core one URL or body idiom** (Outscale: the body consumed before the handler; Exoscale: `{id}:action` routing, +135 lines in `emulator/table.go` with its own test). Budget one such change; history shows it lands dialect-neutral and breaks nothing, but it is the least predictable line item. |

## The verdict

**Largely industrial.** A fourth pack serving ten operations and proving them
costs, measured rather than guessed:

- **~43 additive lines across 11 shared files**, none of which can regress the
  three existing packs (every edit is a new case, row, or task);
- **~1 500 lines in its own directory** (pack, tests, conformance script);
- **~100 to 350 lines of adapters** (signer, credentials, possibly a contract
  converter, possibly one core routing lesson);
- **six classes of human decision**: which real client proves it, the drift
  triage into served/declined, the catalogue fiction values, the curated read
  list, the credential carriers of its dialect, and (once the mechanism below
  exists) the field-level declines.

The Exoscale session served 30 operations, conformance green, in one recorded
day (2 090 insertions, everything included). Ten operations for a fourth cloud
is on the order of half such a session, *provided* the operator has an account
to record against; without one, the shapes half of the proof does not exist.

What is **not** industrial today is three things, none structural:

## Recommendations, ordered by what they gain

1. **Make proxy redaction hold for dialects nobody has read** (security,
   above). Allowlist request headers; prove it with a recorded exchange whose
   credential carrier matches no pattern.
2. **Give the shapes gate a way to record a decision.** Done, 2026-08-13
   (#91): `emulator.FieldDecline` is the transposition of `Declined()` to
   fields — declared in the pack via the optional `FieldDecliner` interface,
   `{operation key, field path, reason}`, subtracted by `feint shapes --check`,
   and counted in its output so a growing declined list stays visible.
   Pack-side code rather than a sidecar JSON, for the same reason `Declined()`
   is code: a decision needs a review path and a reason next to it, and
   `shapes/*.json` is regenerated, which is exactly where a human decision must
   not live. The six fields that kept the gate red are now declared: Scaleway's
   `per_volume_constraint.l_ssd` (a constraint for local volumes the emulator
   never attaches, and `docs/limits.md` already documents why `min_size` stays
   0), and Exoscale's `zones[].id` and `security-groups[].visibility` (live on
   the wire, absent from the published OpenAPI this emulator enforces as
   closed; the 98f8df6 commit message recorded the decision in prose, which is
   the one place a gate cannot read). The two Exoscale entries have since gone:
   #370 and #371 moved the contract instead, through
   `tools/contract/exoscale-recorded-fields.yaml`, and the same staleness rule
   named below is what made deleting the declines compulsory once the pack
   answered the fields. A reason faces the same guard as an
   operation's (`TestEveryDeclinedFieldSaysWhy`), and a decline that excuses
   nothing fails the gate as stale (`TestAStaleFieldDeclineFailsTheGate`), so
   the list cannot rot. The gate is wired into every pull request — a go.yml
   step and `TestTheRepositorysShapesAreServedOrDeclined` inside `go test` —
   which was the point of building it.
3. **Derive provider lists from mounted packs** in `shapes_check.go` and
   `docs.go`. Done, 2026-08-11: both gates read `srv.Packs()`, the pattern
   `env.go` already used. "Invisible to the gate" became the honest "no
   catalogue; nothing to check", and `TestShapesCheckCoversEveryMountedPack`
   fails if the list is ever written by hand again.
4. **Deduplicate the Outscale call identity** between `upstream` and
   `shapes_check` (one exported function, two existing callers).
5. **Watch, do not build:** the day a second provider deviates from the
   GET-a-path dialect in `upstream`, fold the conditionals into a dialect
   declaration (fields, per the doctrine). With one deviant, the `if` is the
   honest form.

## What was not measured

- Whether the fourth signature hides a fourth trap. Each of the three did, and
  each was found only against the live cloud. No account, no measurement.
- The conformance cost. It depends on which real clients the fourth cloud has
  (a CLI, a Terraform provider, both, neither), and that choice is the first
  human decision of the pack, upstream of any line of code.
- Whether the fourth URL idiom fits the router. Two of three did not; the
  budget above assumes one bounded lesson, but its size is the least certain
  number in this document.
- The shapes proof end-to-end for a hypothetical pack: `--record` requires an
  account by design, so a fourth pack built without one ships with the gate
  honestly saying "nothing to check", which recommendation 3 at least makes
  visible.

## Since the audit

Re-read on **2026-08-17**, one milestone later. Only what changed is listed; the
body above stands as the 2026-08-10 measurement.

**Recommendation 4 has landed.** `upstream.OutscaleCall(action) (key, method,
path)` exists, and `shapes_check.callIdentity` calls it: the dialect is written
once and its two consumers read the same function. That was the audit's one
*real* duplication, named as this repository's failure pattern, and it is gone.
Recommendations 2 and 3 were already marked done in the body. What is left open
is 1 (proxy redaction for a dialect nobody has read) and 5 (watch, do not build),
and 1 is still the one with a security consequence.

**The headline figure is now a control rather than a claim.** "`internal/core`
contains zero provider-named code" was true on 2026-08-10, true again on
2026-08-17, and until now nothing would have noticed it stop being true.
`TestTheCoreNamesNoProvider` (`internal/cli/discipline_test.go`) walks every
non-test file under `internal/core` and fails on a provider name in an identifier
or a string literal. It is falsified by reintroducing the exact defect an audit
once found — the event filter listing `"scw-"`, `"osc-"`, `"exo-"` — and it
reports all three.

**The rule it holds was reworded, because this document refuted the old one.**
`architecture.md` claimed nothing outside a pack's directory should need to
change, while the map above counts eleven shared files. Both were defended and
only one could be true as written. What holds is narrower:

> Adding a provider requires no behavioural change to `internal/core`; the
> external registration and integration points may receive additive data.

**The size figures are stale, and by a lot.** The packs on 2026-08-17, code and
test lines counted the same way as the body:

| pack | code | test | operations mounted |
|---|---|---|---|
| Scaleway | 8 713 | 6 093 | 102 |
| Outscale | 8 299 | 5 315 | 85 |
| Exoscale | 4 549 | 2 542 | 72 |

The body's "~79 lines per operation including tests" came from Exoscale at 46
served operations; the same pack now sits at ~98 for 72, and the three together
average ~137 (35 511 lines over 259 operations). The per-operation cost has gone **up**, not down, which is what
should be expected as the cheap operations get served first and the shared
proofs get finer — every route now owes a contract check, a shapes comparison,
an evidence row, and either a client or a stated reason (#174). None of that
existed at the ratio the body quotes.

**What still has not been measured**, and is the reason this section is not a
new audit: the eleven-file, forty-three-line figure for the shared surface. It
requires judging, file by file, whether a fourth pack would have to edit it, and
the answer moved when `packsFor`, the shapes gate and the docs banner started
deriving their lists from the mounted packs. Re-measure it before quoting it.
