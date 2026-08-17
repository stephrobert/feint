# Finishing the OpenSSF Best Practices badge — the answers, ready to paste

The project is registered: **[#14111](https://www.bestpractices.dev/projects/14111)**,
badge `in_progress`, **72 %** of the passing level on 2026-08-17. Scorecard sees
it — its CII check moved from *"score is 0: no effort detected"* to *"score is 2:
badge detected: InProgress"* the same day. Reaching `passing` takes it to 5.

**Twenty-three criteria are unanswered.** Every one of them is below, in the
order the form asks, with the answer to select and the text to paste. Nothing
here is invented: each justification points at a file in this repository or at a
measurement, and where the honest answer is *not met*, it says so.

Working notes, not documentation. Update them when the answers stop being true.

---

## Basics

### `homepage_url` — the project's home page

**Answer:** the URL is already recorded
(`https://blog.stephane-robert.info/docs/cloud/outils/feint`). The criterion asks
that the page **succinctly describe what the software does**. Mark *Met* once the
page opens with a sentence to that effect; the README's own line is the one to
reuse:

> A local emulator for the European clouds — Scaleway, Outscale, Exoscale. One
> binary, one port, no account.

If the page is not up yet, put the repository URL instead — GitHub renders the
README, which satisfies the criterion literally.

---

## Change control

### `repo_interim` — interim versions between releases

**Answer: Met.**

> Every change lands on `main` through a pull request, and the branch carrying it
> is public for the whole time it is open. Interim commits are therefore
> available to anyone before they are part of a release. Releases are tagged
> (`v0.8.0`), and the version is derived from the commit subjects by commitizen,
> so an interim state is always identifiable relative to the last tag.

---

## Reporting

### `report_url` — where to report bugs

**Answer: Met**, with the URL:

```
https://github.com/stephrobert/feint/issues
```

### `report_responses` — the project responds to bug reports

**Answer: Met.**

> Reports are answered. The tracker also receives an automated drift issue every
> week, which is triaged rather than closed: each entry is an upstream API
> operation that must be implemented or declined with a stated reason
> (`docs/routes.md` prints the decisions).

### `enhancement_responses` — the project responds to enhancement requests

**Answer: Met.**

> Enhancement requests are answered, including with a refusal and its reason. The
> roadmap issues are the record: each closed batch states what was served, what
> was declined and why, and `docs/roadmap.md` carries decisions that were taken
> against the project's own earlier plan — for example an exit condition that was
> dropped because measurement refuted it.

### `report_archive` — the reports are archived and publicly readable

**Answer: Met.**

```
https://github.com/stephrobert/feint/issues?q=is%3Aissue
```

### `release_notes_vulns` — release notes name the vulnerabilities fixed

**Answer: N/A.** The form makes the justification **required** for this one, and
refuses the answer without it. Paste this — it meets both conditions the
criterion itself names, *no publicly known vulnerabilities* and *applies only to
the project results, not to its dependencies*:

```
No publicly known run-time vulnerability has ever been fixed in a release of this
project, so no release note has one to identify. Releases do carry written notes:
https://github.com/stephrobert/feint/releases

The disclosure process for when it happens is stated in SECURITY.md
(https://github.com/stephrobert/feint/blob/main/SECURITY.md): reports go through a
private GitHub Security Advisory, acknowledged within a week, and the advisory
names the reporter unless they prefer otherwise. A fixed vulnerability would
therefore be identified both in its advisory and in the release note.

Dependency vulnerabilities are out of this criterion's scope, and are covered
anyway: govulncheck and OSV-Scanner run on every pull request and weekly, with
Dependabot for the Go modules and the GitHub Actions.
```

---

## Security — the two "know" criteria

### `know_secure_design`

**Answer: Met.**

> The threat model is stated rather than implied. `SECURITY.md` says what this
> software is — a development tool that accepts every credential without
> verifying any, answers on plain HTTP and holds its state in memory — and what
> follows: do not expose it to a network you do not control. It also documents
> the one attack a listen address does not stop, DNS rebinding, and the defence
> in force: when bound to a loopback address the emulator refuses requests whose
> `Host` header names anything else, which a browser cannot forge. `docs/limits.md`
> carries thirty sections of what is not emulated, each with its cost.

### `know_common_errors`

**Answer: Met.**

> `CLAUDE.md` documents the classes of defect this project has met and now guards
> against, each with the control that catches it: command injection into the
> runtime CLI (a name that becomes a command argument is validated for syntax
> *and* checked for ownership), YAML injection through a text template (refused
> at intake rather than escaped at render), and restored state treated as
> untrusted input at the persistence boundary. Each guard is falsified — the fix
> is removed in a copy outside the repository and the corresponding test must
> fail (`mise run falsify`).

---

## Security — cryptography

All of these are **N/A**, and the same sentence explains the family. The form
requires a justification for each — and on 2026-08-17 the seven already answered
carried none, which the API shows plainly next to the ones that do
(`delivery_mitm`, `static_analysis`). Paste this in every one of the eleven,
including the seven already marked N/A:

> This software emulates cloud APIs. It **parses** provider authentication
> schemes — AWS Signature v4, `X-Auth-Token`, `EXO2-HMAC-SHA256` — and verifies
> none of them, deliberately and stated in `SECURITY.md`: the point is to run
> without an account. It stores no password, issues no credential, and
> establishes no secure channel of its own; it listens on plain HTTP on a
> loopback address by design. Identifiers are generated with `crypto/rand`
> (`internal/core/emulator`, RFC 4122 v4 UUIDs). Released artefacts are signed
> with keyless Cosign and verified by checksum and SLSA provenance, which is the
> `delivery_mitm` criterion rather than one of these.

| Criterion | Answer |
|---|---|
| `crypto_published` | N/A — publishes no cryptographic protocol of its own |
| `crypto_keylength` | N/A — establishes no keys |
| `crypto_working` | N/A — no broken algorithm is used, none is used at all |
| `crypto_weaknesses` | N/A — same |
| `crypto_pfs` | N/A — no key agreement |
| `crypto_password_storage` | N/A — stores no password |
| `crypto_random` | **Met** — `crypto/rand` for every generated identifier |
| `crypto_used_network` | N/A — the emulator is a local listener on plain HTTP by design, documented in `SECURITY.md` |
| `crypto_tls12` | N/A — same |
| `crypto_certificate_verification` | N/A — makes no outbound TLS connection in normal operation |
| `crypto_verification_private` | N/A — same |

`crypto_random` is the one to answer **Met** rather than N/A: the software does
generate values that must be unpredictable, and it uses the right source.

---

## Security — delivery and vulnerabilities

### `delivery_unsigned` — the download is verifiable

**Answer: Met.**

> Every release publishes `checksums.txt`, a keyless Cosign bundle over it, SLSA
> build provenance and a CycloneDX SBOM. `docs/install.md` gives the exact
> verification a user runs: `cosign verify-blob` establishes who produced the
> checksum list, then `sha256sum -c` verifies the bytes against it. One signature
> covers every artefact because every hash is in that file, and the identity
> pattern is anchored at both ends so it names the release workflow rather than
> the repository.

### `vulnerabilities_fixed_60_days`

**Answer: Met.**

> No known vulnerability is outstanding. Dependencies are checked on every pull
> request by `govulncheck` and OSV-Scanner, and weekly on a schedule; Dependabot
> covers the Go modules and the GitHub Actions. `go.mod` has three lines, which
> is itself a policy — a new external dependency has to be justified in the pull
> request.

### `vulnerabilities_critical_fixed`

**Answer: Met** — same sentence.

### `no_leaked_credentials`

**Answer: Met.**

> TruffleHog runs in CI and as a pre-commit hook, so a secret is refused before
> the commit exists. The credentials under `tools/conformance/*/fake-credentials.env`
> are deliberately public and documented as such: they are well-formed but
> meaningless values the emulator accepts without verifying, and the suites use
> them so a real client can be pointed at the emulator without an account.

---

## The last one

### `maintained` — the project is actively maintained

**Answer: Met.**

> Actively developed. Note this is the project's own claim; OpenSSF Scorecard's
> `Maintained` check is a different thing — a 90-day age gate that returns 0 for
> any repository younger than that whatever it does, and clears on 2026-10-27 for
> this one. `docs/scorecard.md` states the distinction.

---

## What to expect afterwards

Answering these twenty-three moves the badge from `in_progress` to `passing`, and
Scorecard's CII check from **2** to **5**.

**Do not start silver or gold.** Both require two or more developers with commits
in the last year, which is the same ceiling as Scorecard's Code-Review and
Contributors checks. `docs/scorecard.md` says what moves that, and it is not a
form.
