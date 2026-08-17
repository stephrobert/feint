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

Measured on **2026-08-17**: score **7.7**, five checks below 10.

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

## CII-Best-Practices — 0/10, and it is one form away

*"No effort to earn an OpenSSF best practices badge detected."* The badge is
awarded by [bestpractices.dev](https://www.bestpractices.dev) after a
self-assessment, and it needs an account this repository's tooling cannot create.

It is worth doing, because most of the questionnaire is already answered by
things that exist here: a stated security policy (`SECURITY.md`), a public bug
tracker, a documented contribution process, a build that anybody can run
(`mise run check`), tests that gate every change, static analysis in CI, a
declared licence, signed releases with SBOM and SLSA provenance, and a
vulnerability-disclosure process with a stated delay.

**Declare only what is met.** The passing level asks for things this project does
not have yet — notably two or more developers with commits in the last year,
which is the Code-Review and Contributors ceiling again.

## Contributors — 3/10

Contributors from at least two organisations. One author, so far. Same shape as
the two above: it moves when somebody else contributes, not when a file changes.

## What is *not* here, because it is already 10/10

Pinned-Dependencies, Token-Permissions, Dangerous-Workflow, Security-Policy,
Signed-Releases, Fuzzing, SAST, Vulnerabilities, Dependency-Update-Tool,
License, Packaging, Binary-Artifacts, CI-Tests. Those are earned by construction
in `.github/workflows/` and `tools/governance/`, and the verification a user
actually performs — cosign bundle, checksums, SLSA provenance — is written out in
[install.md](install.md).
