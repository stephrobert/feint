# What has been audited, and what was found

An audit is not a review. A review asks whether a change is correct; an audit
asks where this product does not keep its promise. The second finds things the
first cannot, because the first only ever looks at what somebody just wrote.

This file exists so that the second kind does not start from zero every time.
Without it, the same layers get looked at twice and the same corners never do.

## How it is filled

**By the orchestrator, not by the agents.** An agent reports; whoever asked for
the audit writes the row. That keeps the responsibility where the decision is,
and it is why no agent definition mentions this file.

One row per audit, newest first. An audit that found nothing is recorded too,
with the paths it read: three months later, that is what says a layer was looked
at and by whom.

## The register

| date | role | scope | found | not looked at |
|---|---|---|---|---|
| 2026-11-14 | `falsifier` | PR #926 second cycle, the contributor's answer | the three blocking points **closed**, each driven rather than read: a failing job in neither list now refuses the night, `judged-jobs: ''` and five other empty spellings now fail the guard, and a judged job at `in_progress` refuses instead of being dropped. His own additions go further than the review asked: the tests read the lists and the env wiring out of the workflows, so narrowing a list reddens the Go tests and commenting out `NIGHT_JUDGED_JOBS:` is caught, a failure mode neither reviewer had named. **Residual of the shape it answers**: `nonEmptyList`'s switch ends in `default: return true`, so `judged-jobs: >-` passes the guard while YAML yields an empty string | no Go test, no `mise run check`, no `tools/falsify` run on his branch (executing a contributor's branch is refused in this session); the `>-` case traced by hand, the sandbox refused the extracted copy; no API call, so the matrix display names rest on his own fixtures |
| 2026-11-14 | `falsifier` | #923 / the new falsify check, two cycles | **four BLOCKER, all the same shape: the guard this lot adds was not held by its own tests.** `covered_paths`' package filter could be deleted and the selftest stayed green, because a one-package module makes "in this package" and "anywhere" the same set; the unbuildable-listing branch passed through the ordinary missing-name path; the pre-copy wiring in `main()` could be removed and reprinted TEST STILL PASSED verbatim; and a stale **subtest** name was still accepted, with 13 committed mutations naming one, nine of them rows of a single table. Second cycle: nine mutations, nine bites, all closed. Two more taken in the lot, one of them the lot's own lesson unapplied to its own new code (`ran_under` ignored its exit code) | `mise run falsify:all`; `mise run falsify:selftest` through mise rather than python directly, both cycles; the four behaviours no case holds, left to #938 |
| 2026-11-14 | `doc-auditor` | #923, the three sentences it changes | no published page made false, and the two sentences claiming the lint needed no Go are both corrected. **Two MUST inside the lot**: a comment stating a mechanism the code does not have ("the module path is new on every run") where the path is a constant, and a cost published as 3.0 s warm that measures 0.25 s to 0.56 s depending only on which Python runs it. **Five pre-existing stale numbers** (#937), and `falsify:lint` run by no pull request while `CONTRIBUTING.md` says prepush is what a pull request refuses (#936) | `falsify:all` after the change, so the added per-spec cost is unmeasured; the 12.6 s counterfactual; a cold-cache measurement; the 251 specs' contents beyond counting |
| 2026-11-14 | `falsifier` | #909 / PR #931, the `Attach` ownership guard | the attack test **passed against a guard reading the wrong name** (`ownedNetwork(name)` for `ownedNetwork(att.Network)`): every row drove one machine name that `ownedNetwork` also refuses, so the refusal happened for the wrong reason and only the accepting half caught it — fixed in the lot, mutation added, re-measured as biting; **no test in the package catches the check moving out of the per-machine lock** (#934), a completed run, where all five other plausible defects are caught; five tests wait on a bare channel, so a guard refusing early makes them **hang instead of fail** (#935), which truncated three of its own measurements | `go test -race` on any mutant, and the subject of #934 is a lock; anything outside `./internal/core/machine/`; whether `store.Restore` guards the frontier |
| 2026-11-14 | `security-challenger` | #909 / PR #931, and the sibling paths it did not touch | no blocker on the lot: `Attach` is closed, nothing emitted before the probe, fail-closed on a probe failure, and the guard's input is not attacker-writable. Three siblings open: **the cold twin, `incus.go:508` and `:704`, accept any well-formed network from a restored `Runtime`** (#932) and that is the boot path, so it runs on every poweron; `RouteAddress`'s managed branch asks the network and never the instance it writes to (#933), its own comment saying so; `Observe` and `Door` (#920, already open) still exec into the instance whether the attach was refused or not | no run against a real Incus: every finding is argv-level; `internal/core/cloudinit` not opened at all; `incus_prune.go`, `leftover.go`, `incus_traps.go` read only at grep level; the `PUT /_feint/state` handler around `store.Restore` |
| 2026-11-14 | `falsifier` | PR #926, an external contributor's falsification claims | the pull request's note that `falsify.py` **cannot run on this station is false**, measured twice on the spec it names itself, exit 0, and its stated mechanism is wrong too (the harness passes the parent environment through, and `systemd-run --user --scope` inherits it) — so "all 5 mutations bite" was an assertion, not a measurement; its fifth mutation is itself well formed; **two of the three hops the judged list travels are tested by nothing**, the Go tests re-declaring the four job names as a const rather than reading either workflow | whether his mutations bite on HIS tree, which only he can run; no checkout of a contributor's branch, by rule |
| 2026-11-14 | `go-reviewer` | PR #926, `tools/ci/night-report.sh` and the workflows | the static guard tests for the **string** `judged-jobs:`, so `judged-jobs: ''` passes it while the script falls back to judging every completed job; **the closure check does not exist and the comment says it does** — a job that exists, fails and is not named is dropped in silence and the night reads green; the `completed` filter runs after the narrowing, so a judged job still running is dropped and the night exits 0. Clean: exact-match semantics, `set -euo pipefail` discipline, the zero-jobs and truncated-page paths | no build, no test, no script run from the contributor's branch, by rule; no GitHub API call, so the matrix display names rest on his own fixtures |
| 2026-11-14 | `contract-guardian` | #925 / PR #929, the fault receipt's published shape | **a real client's assertion broken with nothing local to say so**: changing the receipt sentence broke the grep in `tools/conformance/faults.sh`, a suite `prepush` does not play, and the two README sentences carrying it are hand-written so `docs:check` could not see them either; the lot's stated reason for the schema bump was **false** (a version 3 reader could infer the state from an undocumented two-field invariant) and was replaced by the true one; `faultsDocument.SchemaVersion` is decoded and read nowhere (#928) | the French pages beyond the one sentence; `-count=N` does not re-run the frozen population, so a stability claim cited the wrong instrument and was re-measured |
| 2026-11-13 | `rules-challenger` | `CLAUDE.md`, `.claude/`, the gates, the ruleset | **the audit register did not exist** while `CLAUDE.md` described it; the audit cadence has no trigger; `evidence:verify` is run by nothing; the `fields` leg is not a required check though it is the only complete one; `prepush` is described with three different lists (4 / 7 / 9 tasks); `store.Restore` does not ask the question `CLAUDE.md` calls the safest; the project allowlist pre-authorises `incus delete *` | the six skills and seven commands beyond their headers; which agents have actually run (nothing records it) |
| 2026-11-13 | `security-challenger` | `feint mutate` (#193), unmerged | no path to the operator's machine, but **no test asserts that `Runtime` is out of reach**; the verb read-modify-writes the whole store with no precondition, so a concurrent write is lost in silence; `decodeState` drops unknown resource fields while its comment promises the opposite | the concurrency window by execution (read only); `evidence.go` and `compat.go`, out of scope |
| 2026-11-13 | `terraform-challenger` | `examples/stacks/`, `tools/conformance/`, both engines | **two Exoscale data sources read back empty at apply time**, no error, behind a green gate; `docs/limits.md` §#520 names a cause refuted since v0.71.0 and claims nothing drives a stack CI runs twice per pass; the stacks gate prints output counts and compares them to nothing | what the provider actually returns for those two reads; `terraform import`, `-replace`, `-target`, which no suite has ever driven |
| 2026-11-13 | `security-challenger` | `internal/core/machine`, `cloudinit`, `proxy`, `s3` | **a crafted snapshot removed the default route of any instance on the host** (egress path asked neither question); **the proxy wrote non-JSON bodies in the clear**, signatures included; `Attach` asks neither question where `Detach` asks both (#909); the whole cloud-config goes through argv (#910) | the Incus driver under a real runtime; `internal/core/emulator/ui.go` |
| 2026-11-13 | `doc-auditor` | `README*`, `docs/`, the generated blocks | the safety banner promised machines boot "on every pull request" when only a nightly workflow boots them; "isolated subnets" with no mode named; "nothing created anywhere" against four measured escape paths; `limits:check` red on `main` | 62 limits sections one by one; `install.md`, `proxy.md`, `routes.md` |
| 2026-11-13 | `go-reviewer` | #851, the fault receipt | a firing in flight landed in the next campaign; the receipt was documented "oldest first" and was ordered by completion; `feint fault add` resets every armed rule's call counter | the conformance suites, not played by it |
| 2026-11-13 | `contract-guardian` | #851, published shapes | the CHANGELOG carried two `Added` sections under one release; two pages promised determinism with no condition | the French pages; `faults.sh` not played |
| 2026-11-13 | `falsifier` | the guards added by #851 | **the epoch guard's test did not bite** — it passed with the guard removed, because the slot bound already produced the same empty receipt | the other specs of `falsify:all` |

## What this register already says

Sixteen audits over two days, on a repository that holds itself to a high
standard, and every one found something. Most of the findings are the same
defect wearing different clothes: **a control that cannot fail**. A register
that says so is worth more than sixteen reports nobody reads twice.

The 2026-11-14 rows add a second pattern, and it is worth naming separately:
**several found a control whose verdict was true but drawn from the wrong
population**. The attack test that refused for the wrong reason, the
static guard satisfied by an empty value, the night that judged jobs it had
not enumerated. None of the three was a wrong answer; all three were a right
answer to a question nobody had checked was the one being asked.

They also say something about the method rather than the code. Most of them
ran on work that had already been written and, in two cases, already pushed:
the defects were not in a diff anybody was reading. And three agents that day
reached their turn limit without delivering a report, which cost a relaunch
each. A brief that does not fit the agent's budget returns nothing, however
good the audit was.

The rows also say what nobody has looked at yet. That column is the one to read
before choosing the next audit.
