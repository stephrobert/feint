# Adoption snapshots

This branch carries data, never code. One row per release asset per day, plus
the computed figures.

It is orphan on purpose: `main`'s ruleset carries `pull_request` and
`required_status_checks`, so a daily automated push to `main` is refused, and a
pull request a day to land a row of numbers would be worse. Keeping the history
here leaves `main`'s history clean and this one complete.

- `metrics/feint-release-downloads.csv` — append-only, one row per asset per day
- `metrics/feint-latest.json` — the computed figures

**`download_count` counts downloads, never people.** What that does and does not
license anybody to claim is written in `docs/adoption-metrics.md` on `main`.

Written by `.github/workflows/adoption-metrics.yml`. To recompute by hand:

```bash
git fetch origin metrics && git worktree add .metrics metrics
mise run metrics:report -- --dir .metrics/metrics
```
