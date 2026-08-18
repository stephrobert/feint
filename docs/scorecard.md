# The OpenSSF Scorecard alerts, one by one

The badge on the README links to a score. This page says what each open alert
means here, which ones are work and which ones are a property of a repository
this age with this many maintainers — and, for the second kind, what would
actually change them rather than what would merely raise the number.

The [live score](https://securityscorecards.dev/viewer/?uri=github.com/stephrobert/feint)
is the authority on the number; this page is the authority on what the number
means here. The README's badge points at this page rather than at the viewer,
because a reader who clicks a 7.7 wants to know why it is not a 10, and the
viewer answers that with a list of check names.

Measured on **2026-08-18**: score **7.9**, five checks below 10
(Branch-Protection 4, Code-Review 0, Maintained 0, CII-Best-Practices 5,
Contributors 3).

The rule this page is written under: **a check is not raised by satisfying its
detector.** Every one of these has a shape that scores well and means nothing —
requiring approvals a bypass then skips, a badge claiming practices nobody
follows. That is the same failure as a comment describing a control nobody
applies, which this repository spends most of its own machinery catching.

## Branch-Protection — 4/10, and the four warnings are one decision

Scorecard lists four:

- `branch protection settings apply to administrators` is disabled
- `branch main does not require approvers`
- `codeowners review is not required on branch main`
- `last push approval` is disabled

Three of them are the same fact wearing three hats: **one maintainer cannot
approve their own pull request.** Setting any of the last three closes the only
path this repository has to `main`. The usual way around that is to require
approvals *and* keep an admin bypass — which scores better on two counts while
enforcing neither. That is the gaming this page refuses.

The first one is a real trade, and it was weighed rather than defaulted. The
ruleset keeps a bypass for the repository admin role:

```json
"bypass_actors": [{ "actor_id": 5, "actor_type": "RepositoryRole",
                    "bypass_mode": "pull_request" }]
```

**What that bypass does and does not allow** is the whole of the decision, and
`bypass_mode` is the word that carries it. `"pull_request"`, not `"always"`: the
admin can merge a pull request the rules would hold, and **cannot push to `main`
directly**. Deletion and non-fast-forward stay closed to everyone. So what the
bypass buys is one escape hatch — merging when a required check is red for a
reason that is not the code.

That case is not hypothetical: on 2026-08-17 the Poutine job failed on
`curl: (35) Recv failure: Connection reset by peer` while fetching its own
binary. Nothing about the change was wrong, and with no bypass the only options
are re-running the job until the network cooperates, or being unable to ship.

The cost is stated plainly, because a decision with only benefits listed is a
justification: **a gate the owner can wave through measures the owner's
discipline, not the code.** Nothing enforces that the hatch stays for network
flakes rather than for a red test on a Friday. What makes it visible instead of
invisible is that every use is a merge on a pull request whose checks are on the
record — an audit trail, not a prevention.

**What would change all four:** a second person with write access who reviews.
Not a setting. Until then `require_code_owner_review` stays false, and
`.github/CODEOWNERS` says so in its own header rather than claiming otherwise —
it claimed otherwise until 2026-08-17, which is the same defect as a comment
describing a control nobody applies.

## Code-Review — 0/10, and it measures the same thing

*"Found 0/30 approved changesets."* Every change here lands through a pull
request with checks that all run, and none of them carries a human approval,
because there is one human. The score is accurate; what it measures is the
number of reviewers, not whether changes are reviewed against anything.

What this repository substitutes for a second reader is machinery, and the
substitution is the whole project: a change is judged by whether the official
client drives it (`mise run conformance`), whether the upstream surface still
matches (`drift:check`), whether the response matches the provider's own
description (the contract check), and whether the guard it adds actually bites
(`mise run falsify`). That is not a claim Scorecard can read, and it does not
replace a reviewer. Both statements are true at once.

## Maintained — 0/10 until 2026-10-27

*"Project was created within the last 90 days."* The repository was created on
**2026-07-29**. This check is 0 for every repository under 90 days old whatever
it does, and it clears itself on **2026-10-27** given continued activity. There
is no action, and anything that looks like one — backdating, activity for the
sake of the counter — is the gaming this page refuses.

## CII-Best-Practices — 5/10, the Passing badge, earned 2026-08-17

*"badge detected: Passing."* The badge was earned on **2026-08-17** at
[bestpractices.dev](https://www.bestpractices.dev/projects/14111) — 100% of the
Passing questionnaire, 9% into Silver — by declaring only what exists here: the
security policy, the public tracker, the documented contribution process, the
build anybody can run, the tests that gate every change, static analysis in CI,
the licence, and the signed releases with SBOM and SLSA provenance.

Scorecard maps the tiers to 2 (In Progress), 5 (Passing), 7 (Silver) and
10 (Gold). Silver is the next honest step and it is partly form-filling, partly
the same ceiling as everything below: several of its criteria, and Gold's
outright, ask for a second maintainer or review by someone other than the
author. The form advances when those facts change, not before.

## Contributors — 3/10

Contributors from at least two organisations. One author, so far. Same shape as
the two above: it moves when somebody else contributes, not when a file changes.

## The reference next door, and where its half-point lives

`stephrobert/secure-python-pipeline` is this maintainer's hardened-pipeline
reference. Compared check by check via the public API (its scan of 2026-08-17
against this repository's scan of 2026-08-18): it scores **8.4** to this
repository's **7.9**, and fifteen of the eighteen checks are identical —
every workflow-earned check is 10/10 on both sides, so there is nothing left to
transpose from its CI. The three that differ:

- **CII-Best-Practices 5 here, 2 there** — this repository is ahead (Passing
  against In Progress).
- **Branch-Protection 10 there, 4 here** and **Code-Review 2 there, 0 here** —
  the whole of its lead, and both come from one mechanism. Its ruleset requires
  two approving reviews, code-owner review and last-push approval with zero
  bypass actors; the approvals that let its pull requests merge come, on the
  public record, from a second account of the same person (same name, same
  company on the profile), plus one third-party approval. That satisfies the
  detector; it does not add a second reader.

Adopting those settings here would either freeze `main` — there is no second
account — or manufacture approvals, which is the shape this page already refuses
twice above. So the 0.5 gap to the reference is the price of that refusal, and
it is paid knowingly: the honest exit is the same as for Code-Review and
Contributors, a second human with write access, not a setting and not a login.

## The CodeQL alerts, and why five of them are false

The same code-scanning page carries five CodeQL findings, all `go/log-injection`
(read live from the code-scanning API on 2026-08-18): one on
`outscale/publicips.go`, three on `exoscale/elasticips.go` — the refusal to
route, plus two flows into the route-failure error — and one on
`outscale/privateips.go`, where an attach failure logs the values it could not
carry.

**The dataflow the query describes is real.** Every flagged value is
client-controlled, and two of the five sit on the very branch where
`netip.ParseAddr` refused it, so it can be any string at all — including one
carrying a newline and a forged `level=ERROR` record.

**The sink is what the query cannot see.** This emulator logs through
`slog.TextHandler`, which quotes any value carrying a space, an `=` or a control
character. Measured with that exact payload before anything was concluded:

```
time=… level=WARN msg="refusing to route an address outside the emulated elastic block"
  address="192.0.2.1\nlevel=ERROR msg=\"the emulator was compromised\" attacker=yes" instance=i-1234
```

One line out. The newline is written as the two characters `\n` inside quotes,
and no forged field reaches the record.

So the finding is a false positive — and dismissing it in a web interface is a
sentence no future change consults. `TestALoggedClientValueCannotForgeALine`
holds the sink instead: it fails the day the emulator's logger stops escaping,
whether by a hand-rolled handler, a format that does not quote, or a message
built with `fmt.Sprintf` into a sink that does not.

Worth stating explicitly, because CLAUDE.md's own rule points the other way:
this repository refuses a dangerous value **at the door** rather than escaping it
at the render. The exception here is deliberate — the value being logged is the
one the emulator has just *refused*, and sanitising it would hide from an
operator exactly what was rejected.

## What is *not* here, because it is already 10/10

Pinned-Dependencies, Token-Permissions, Dangerous-Workflow, Security-Policy,
Signed-Releases, Fuzzing, SAST, Vulnerabilities, Dependency-Update-Tool,
License, Packaging, Binary-Artifacts, CI-Tests. Those are earned by construction
in `.github/workflows/` and `tools/governance/`, and the verification a user
actually performs — cosign bundle, checksums, SLSA provenance — is written out in
[install.md](install.md).
