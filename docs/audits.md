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
| 2026-11-13 | `rules-challenger` | `CLAUDE.md`, `.claude/`, the gates, the ruleset | **the audit register did not exist** while `CLAUDE.md` described it; the audit cadence has no trigger; `evidence:verify` is run by nothing; the `fields` leg is not a required check though it is the only complete one; `prepush` is described with three different lists (4 / 7 / 9 tasks); `store.Restore` does not ask the question `CLAUDE.md` calls the safest; the project allowlist pre-authorises `incus delete *` | the six skills and seven commands beyond their headers; which agents have actually run (nothing records it) |
| 2026-11-13 | `security-challenger` | `feint mutate` (#193), unmerged | no path to the operator's machine, but **no test asserts that `Runtime` is out of reach**; the verb read-modify-writes the whole store with no precondition, so a concurrent write is lost in silence; `decodeState` drops unknown resource fields while its comment promises the opposite | the concurrency window by execution (read only); `evidence.go` and `compat.go`, out of scope |
| 2026-11-13 | `terraform-challenger` | `examples/stacks/`, `tools/conformance/`, both engines | **two Exoscale data sources read back empty at apply time**, no error, behind a green gate; `docs/limits.md` §#520 names a cause refuted since v0.71.0 and claims nothing drives a stack CI runs twice per pass; the stacks gate prints output counts and compares them to nothing | what the provider actually returns for those two reads; `terraform import`, `-replace`, `-target`, which no suite has ever driven |
| 2026-11-13 | `security-challenger` | `internal/core/machine`, `cloudinit`, `proxy`, `s3` | **a crafted snapshot removed the default route of any instance on the host** (egress path asked neither question); **the proxy wrote non-JSON bodies in the clear**, signatures included; `Attach` asks neither question where `Detach` asks both (#909); the whole cloud-config goes through argv (#910) | the Incus driver under a real runtime; `internal/core/emulator/ui.go` |
| 2026-11-13 | `doc-auditor` | `README*`, `docs/`, the generated blocks | the safety banner promised machines boot "on every pull request" when only a nightly workflow boots them; "isolated subnets" with no mode named; "nothing created anywhere" against four measured escape paths; `limits:check` red on `main` | 62 limits sections one by one; `install.md`, `proxy.md`, `routes.md` |
| 2026-11-13 | `go-reviewer` | #851, the fault receipt | a firing in flight landed in the next campaign; the receipt was documented "oldest first" and was ordered by completion; `feint fault add` resets every armed rule's call counter | the conformance suites, not played by it |
| 2026-11-13 | `contract-guardian` | #851, published shapes | the CHANGELOG carried two `Added` sections under one release; two pages promised determinism with no condition | the French pages; `faults.sh` not played |
| 2026-11-13 | `falsifier` | the guards added by #851 | **the epoch guard's test did not bite** — it passed with the guard removed, because the slot bound already produced the same empty receipt | the other specs of `falsify:all` |

## What this register already says

Eight audits in one day, on a repository that holds itself to a high standard,
and every one found something. Five of the findings are the same defect wearing
different clothes: **a control that cannot fail**. A register that says so is
worth more than eight reports nobody reads twice.

The rows also say what nobody has looked at yet. That column is the one to read
before choosing the next audit.
