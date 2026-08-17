# The OpenSSF Best Practices questionnaire, answered from this repository

The CII-Best-Practices check is 0/10 because nobody has filled the form at
[bestpractices.dev](https://www.bestpractices.dev). The form needs an account and
a human; this file is everything else — each criterion of the **passing** level
with the answer this repository can defend and the artefact that proves it.

Working notes, not documentation: the audience is whoever fills the form.

**Two rules, and they are the same rule.** Answer `Met` only where the artefact
exists and says what the criterion asks. Where it does not, `Unmet` is the
answer, and the URL is the honest one. A badge earned by declaring things this
project does not do would be a `docs/scorecard.md` violation written on somebody
else's website.

Repository: `https://github.com/stephrobert/feint`
Prepared: 2026-08-17, against `v0.8.0`.

## Basics

| Criterion | Answer | Justification to paste |
|---|---|---|
| `description_good` | Met | README's first lines: a local emulator for Scaleway, Outscale and Exoscale — one binary, one port, no account. |
| `interact` | Met | GitHub issues, open. |
| `contribution` | Met | `CONTRIBUTING.md` — how to build, the conventional-commit requirement, the AI-assisted contribution rule. |
| `contribution_requirements` | Met | Same file: commit format, the checks a change must pass (`mise run check`, `conformance`, `drift:check`), the pull request template. |
| `license_location` | Met | `LICENSE` at the root. |
| `floss_license` / `floss_license_osi` | Met | Apache-2.0. |
| `documentation_basics` | Met | README plus `docs/` — architecture, conformance, limits, install, routes. |
| `documentation_interface` | Met | `docs/routes.md` is generated from the mounted routes; `feint --help` documents every verb, and a test compares the two lists against the READMEs. |
| `sites_https` | Met | GitHub. |
| `discussion` | Met | GitHub issues. |
| `english` | Met | English is the source; the French pages say so and defer to it. |
| `maintained` | Met | Active. Note this is the project's own claim, not Scorecard's `Maintained` check, which is a 90-day age gate — see `docs/scorecard.md`. |

## Change control

| Criterion | Answer | Justification to paste |
|---|---|---|
| `repo_public` | Met | Public. |
| `repo_track` | Met | Git. |
| `repo_interim` | Met | Every change lands on `main` through a pull request; intermediate commits are public on the branch. |
| `repo_distributed` | Met | Git. |
| `version_unique` | Met | Semantic version derived from the commits by `cz bump`; `CONTRIBUTING.md` states why the subject format is not a style preference. |
| `version_semver` | Met | SemVer, `major_version_zero` while under 1.0. |
| `version_tags` | Met | `v0.8.0` and predecessors. |
| `release_notes` | Met | Every GitHub release carries written notes — see `v0.8.0`, which describes the defect fixed and the falsification that proves it. |
| `release_notes_vulns` | Unmet, honestly | No vulnerability has been fixed in a release yet, so there is nothing to have listed. Answer `N/A` if the form allows it; otherwise `Unmet` with this sentence. |

## Reporting

| Criterion | Answer | Justification to paste |
|---|---|---|
| `report_process` | Met | `CONTRIBUTING.md` and `SECURITY.md`. |
| `report_tracker` | Met | GitHub issues. |
| `report_responses` | Met | Issues are answered; the drift bot opens one weekly and it is triaged. |
| `enhancement_responses` | Met | The roadmap issues are the record of what was accepted, declined, or deferred, each with a reason. |
| `report_archive` | Met | GitHub issues, public. |
| `vulnerability_report_process` | Met | `SECURITY.md` — private advisory at `/security/advisories/new`, with a fallback address. |
| `vulnerability_report_private` | Met | Same. |
| `vulnerability_report_response` | Met | `SECURITY.md`: acknowledgement within a week. |

## Quality

| Criterion | Answer | Justification to paste |
|---|---|---|
| `build` | Met | `go build ./...`, or `mise run build`. No dependency beyond Go itself. |
| `build_common_tools` | Met | Go toolchain; `mise` orchestrates and is not required to build. |
| `build_floss_tools` | Met | All of it. |
| `test` | Met | `go test ./...`, plus the conformance suite driving `scw`, `oapi-cli`, `exo`, Terraform and OpenTofu. |
| `test_invocation` | Met | `mise run check`, documented in `CONTRIBUTING.md`. |
| `test_most` | Met | Every pack has unit tests, a concurrency barrage and a conformance suite; `coverage/evidence.json` records per operation what is proven and by what. |
| `test_continuous_integration` | Met | `.github/workflows/go.yml` and `conformance.yml` on every pull request. |
| `test_policy` | Met | Stated in `CLAUDE.md` and `CONTRIBUTING.md`: a new route is proven by a real client, not by a unit test. |
| `tests_are_added` | Met | Required by the pull request template, and the reviewer's check is one command. |
| `tests_documented_added` | Met | Same template. |
| `warnings` | Met | `gofmt`, `go vet`, `golangci-lint`, `go test -race`. |
| `warnings_fixed` | Met | `mise run check` is clean; a pre-commit hook and CI both enforce it. |
| `warnings_strict` | Met | `golangci-lint` with the repository's own configuration; zero findings is the standing state. |

## Security

| Criterion | Answer | Justification to paste |
|---|---|---|
| `know_secure_design` | Met | `docs/limits.md` and `SECURITY.md` state the threat model explicitly: any credential is accepted, the listener is loopback, and a `Host` check defends against DNS rebinding. |
| `know_common_errors` | Met | `CLAUDE.md` documents the classes this project has met and now guards: command injection into the runtime CLI, YAML injection through a text template, restored state as untrusted input. |
| `crypto_published` | N/A | The project publishes no cryptographic protocol of its own. |
| `crypto_call` | Met | Go standard library only. |
| `crypto_floss` | Met | Same. |
| `crypto_keylength`, `crypto_working`, `crypto_weaknesses`, `crypto_pfs`, `crypto_password_storage`, `crypto_random` | N/A, with one sentence | The emulator **parses** provider signatures (Signature v4, `X-Auth-Token`, `EXO2-HMAC-SHA256`) and verifies none of them, by design and stated in `SECURITY.md`. It stores no password and issues no credential. Randomness for identifiers comes from `crypto/rand`. |
| `delivery_mitm` | Met | Releases are signed with keyless Cosign and carry SLSA provenance and an SBOM; `docs/install.md` gives the verification a user actually runs, checksums included. |
| `delivery_unsigned` | Met | Same. |
| `vulnerabilities_fixed_60_days` | Met | None outstanding; `osv-scanner`, `govulncheck` and Dependabot run on every pull request and weekly. |
| `vulnerabilities_critical_fixed` | Met | Same. |
| `no_leaked_credentials` | Met | TruffleHog runs in CI and as a pre-commit hook. The fake credentials under `tools/conformance/*/fake-credentials.env` are deliberately public and documented as such. |

## Analysis

| Criterion | Answer | Justification to paste |
|---|---|---|
| `static_analysis` | Met | `golangci-lint`, `gosec`, CodeQL, plus the workflow scanners (`actionlint`, `zizmor`, `poutine`, `plumber`). |
| `static_analysis_common_vulnerabilities` | Met | CodeQL and `gosec`. |
| `static_analysis_fixed` | Met | Zero findings is the standing state; a finding blocks the pull request. |
| `static_analysis_often` | Met | Every pull request. |
| `dynamic_analysis` | Met | `mise run fuzz` fuzzes the request decoders (`go test -fuzz`), and `go test -race` runs on every change. The conformance suite drives real clients against a live emulator. |
| `dynamic_analysis_unsafe` | Met | Race detector on every run. |
| `dynamic_analysis_enable_assertions` | Met | The barrages and the assertion spans (`POST /_feint/assert`) are checks the emulator refuses to close when its own observation does not support them. |
| `dynamic_analysis_fixed` | Met | Same standing state. |

## The three that need a decision rather than a lookup

1. **`release_notes_vulns`** — nothing to list yet. Say so.
2. **The `crypto_*` family** — the honest answer is that this project handles
   credentials it never verifies, on purpose. Do not answer `Met` on the basis
   that Go's standard library is used somewhere: the criteria ask about the
   software's own use, and the useful sentence is the one from `SECURITY.md`.
3. **The `silver` and `gold` levels** — do not start them. Both require two or
   more developers with commits in the last year, which is the same ceiling as
   Scorecard's Code-Review and Contributors checks. `docs/scorecard.md` says what
   would move it, and it is not a form.
