# Release download metrics

[docs/adoption.md](adoption.md) asks the question that decides whether a 1.0
means anything: **does anybody other than the author run this?** Its scoreboard
counts configurations that apply, issues opened by strangers, providers that
mention the project. Every one of those measures a decision somebody made.

This page counts something weaker and says so plainly: **how many times a
published binary was fetched.** It is a useful number and a poor proxy, and the
two pages are kept apart so the poor proxy never gets read as the good one.

## What is measured

GitHub publishes a `download_count` on every release asset. Once a day, a
workflow reads all of them, keeps the four that are the software, and appends a
row per asset to an append-only CSV.

The four binaries, and nothing else:

```text
feint-linux-amd64
feint-linux-arm64
feint-darwin-amd64
feint-darwin-arm64
```

## What is not measured, and why the distinction is not pedantic

**`download_count` counts downloads. It never counts people.** One CI job that
runs hourly outweighs a hundred humans, and the gap is not hypothetical here.
Measured on 2026-09-09:

| | downloads |
|---|---|
| the four binaries, together | 1 254 |
| `checksums.txt` alone | 1 264 |

`checksums.txt` is fetched slightly **more** often than every binary combined.
Nobody reads a checksum file for pleasure: that is the signature of an install
script verifying before it runs, which is exactly what
[setup-feint](https://github.com/marketplace/actions/set-up-feint) does. Add
that `linux-amd64` is 96.3% of the total, and the shape of the traffic is a
fleet of runners, not a crowd of laptops.

**That is information, not noise.** A pipeline that installs feint every day is
a real use, and arguably a better one than a download somebody forgot about. It
is simply not a user count, so this page never writes one.

So: **"1 254 binary downloads"**. Never "1 254 users", never "1 254
installations".

## What is excluded from the total

Companion files are published beside every release and are deliberately not
counted: `checksums.txt`, `checksums.txt.cosign.bundle`, `sbom.cdx.json`,
`provenance.intoto.jsonl`.

Summing every asset would have reported **2 600** instead of 1 254 on
2026-09-09, an overstatement of 107%.

The filter is two explicit lists, not one. An asset matching neither
`binary_patterns` nor `ignore_patterns` **stops the collection** and names
itself, because the alternative is worse than a failure: the day the release
workflow adds `feint-windows-amd64` or renames an asset, a permissive filter
would report a smaller number, and a smaller number reads exactly like a drop in
adoption. This is the same decision `Declined()` makes for an unserved API
operation, for the same reason.

## What cannot be recovered, ever

GitHub serves the **current** counter and never its history. There is no
endpoint for "how many downloads had v0.12.1 accumulated on 3 September".

So a release's J+1, J+7 and J+30 exist only where the corresponding day fell
**after** collection started. Collection started on 2026-09-10:

- for v0.12.1, published on 2026-09-01, J+1 and J+7 are gone for good, while
  J+30 (1 October) is simply a snapshot away;
- for every release cut from now on, the three points will exist.

The report omits a point it cannot measure rather than printing a zero, and the
same rule governs `downloads_1d`, `downloads_7d` and `downloads_30d`: a window
no snapshot is old enough to close is absent, because "nothing was downloaded in
30 days" and "this tool has not been collecting for 30 days" are opposite facts
and a zero states the first while meaning the second.

## Where the data lives

On the **`metrics` branch**, not on `main`. The `main` ruleset carries
`pull_request` and `required_status_checks`, so a daily automated push to it is
refused; a pull request a day to land a row of numbers would be worse. The
orphan branch keeps main's history clean and the metrics history complete.

```text
metrics/feint-release-downloads.csv    append-only, one row per asset per day
metrics/feint-latest.json              the computed figures, for whatever renders them
```

The CSV columns are `date,version,asset,os,arch,downloads,published_at`.

## Reproducing the figures locally

Every number on this page comes from the CSV, so anybody holding it can
recompute them. Nothing is stored that the report cannot rebuild.

```bash
git fetch origin metrics
git worktree add .metrics metrics
mise run metrics:report -- --dir .metrics/metrics
mise run metrics:report -- --dir .metrics/metrics --json
```

To take a snapshot by hand, which is the same code the workflow runs:

```bash
mise run metrics:collect -- --dir .metrics/metrics
```

## Adding another project

The collector holds no knowledge of feint. A second project is a JSON file:

```json
{
  "project": "pepin",
  "repository": "stephrobert/pepin",
  "binary_patterns": ["^pepin-(?P<os>linux|darwin)-(?P<arch>amd64|arm64)$"],
  "ignore_patterns": ["^checksums\\.txt$", "^sbom\\.cdx\\.json$"]
}
```

The named groups `os` and `arch` are how a platform breakdown happens without
the collector knowing any project's naming scheme. A project that encodes no
platform in its asset names simply declares no groups and gets totals and
per-version figures.

```bash
go run ./tools/metrics collect --config tools/metrics/projects/pepin.json
```

## What was deliberately not built

No telemetry in feint. The binary calls nobody, generates no identifier, and
reads no IP, hostname or repository. Everything on this page comes from a public
counter GitHub already publishes, read from outside by a scheduled job.

That is a smaller measurement than telemetry would give, and it is the one that
does not require asking anybody to trust a promise.
