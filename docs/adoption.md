# What this project asks for, and how it will know it worked

Every figure Feint publishes measures itself: routes mounted, operations driven,
axes earned. All of them are honest, and none of them answers the question that
decides whether a 1.0 means anything — **does anybody other than its author run
this?**

This page is the ask and the scoreboard. It is deliberately short on adjectives:
what is counted here can be checked by somebody else.

## The ask, if you use Scaleway, Outscale or Exoscale

Take a Terraform or OpenTofu configuration **you actually use**, point it at
Feint, and open an issue when something breaks.

```bash
feint start
eval "$(feint env scaleway)"     # or outscale
terraform plan
```

Nothing to install beyond one binary, no account, no credentials, nothing
billed. If your configuration applies, re-plans empty and destroys, say so — that
is a data point too, and it is the one this project cannot generate for itself.

One honest sentence before you point anything here: **the APIs Feint serves run
locally; a product outside that scope is reached by your client at the real
endpoint**, with whatever credentials your shell holds — Object Storage is the
measured case, and only fake credentials made the measured escape harmless.
`feint doctor`, run from the stack directory, warns about the measured escape
paths before the apply does, and
[docs/limits.md](limits.md#a-run-presented-as-local-can-still-reach-the-real-cloud-280)
names them all, with the tested ways to cut egress for the run.

### Pointing an Outscale stack here, per provider generation

The fifteen-stack survey ([the register](../examples/stacks/surveyed.md))
measured four Outscale provider generations and three endpoint mechanisms.
This table is that knowledge, moved to the page that invites the pointing;
every row was re-measured against the emulator on 2026-08-19 ([#286]):

| your provider | the recipe | given the other shape |
|---|---|---|
| `outscale/outscale` >= 1.7 (current; 1.8.0 measured) | `eval "$(feint env outscale)"` — the printed `OSC_ENDPOINT_API` carries `/api/v1`, which this generation wants in the value | a fast 404 naming the missing prefix (a six-minute retry backoff before the emulator learned to say so) |
| `outscale/outscale` 1.1.x (1.1.3 measured), or oapi-cli | `eval "$(feint env outscale --client oapi-cli)"` — the bare host; these clients append `/api/v1` themselves | 1.1.x dies client-side in seconds: `invalid port ":4599%2Fapi%2Fv1"`; oapi-cli gets a 404 saying the endpoint carries `/api/v1` twice |
| `outscale-dev/*` 0.x | none exists: these read no endpoint variable at all. 0.5.3 honours only an `endpoints { api = … }` block and needs TLS interception; 0.7.0 crashes in its own error path. Measured once in the survey register, not replayed since | — |

Two values in one shell cannot both win, which is why the flag exists rather
than a second variable. And one escape is worth knowing before the apply,
because it ignores every value feint prints: **with `OSC_PROFILE` set, the
1.1.x provider reads `~/.osc/config.json` and never reads
`OSC_ENDPOINT_API`** — a run that looks local reaches
`https://api.<region>.outscale.com` with that profile's credentials
(measured on 1.1.3; 1.8.0 honours the endpoint despite the profile).
`feint env outscale` and `feint doctor` now warn when the shell carries it.
The legacy credential names (`OUTSCALE_ACCESSKEYID`/`OUTSCALE_SECRETKEYID`)
were measured too: they do **not** override the endpoint on 1.1.3 or 1.8.0
while the exports stand, but they are real-cloud credentials one lost export
away from being used, and the same warning names them.

[#286]: https://github.com/stephrobert/feint/issues/286

**The goal for 1.0: ten real configurations that apply, re-plan empty and destroy
with no cloud account.**

That target is better than a route count in one specific way: it cannot be
inflated. A configuration either applies or it does not.

### Why your configuration is worth more than another feature

Two realistic stacks written in-house surfaced two defects within an hour —
[#249](https://github.com/stephrobert/feint/issues/249), a route that could not
point at a Net peering, and [#250](https://github.com/stephrobert/feint/issues/250),
a tagged interface that never read its tags back. Neither was visible to the
conformance suite, and the reason is the same in both cases: **a suite proves
what somebody thought to assert.** Yours will exercise what somebody actually
writes, which is a different set.

## If you work at a cloud provider

The offer is concrete rather than a request for attention:

> Feint can be the local test backend for your own Terraform examples and SDK
> tests — no account, no credentials, no resources created.

Your own clients already run against it on every pull request here — `scw`,
`oapi-cli`, `exo`, Terraform and OpenTofu — and the upstream surface of your SDK
is scanned weekly, so an operation you add shows up as untriaged until somebody
decides about it.

Two things would change what this project can prove about your cloud:

1. **A sandbox account, or redacted recordings.** The shapes gate compares this
   emulator's answers with what the real cloud returned; recording needs an
   account. Without one, whole families of operations are *served* rather than
   *served and compared against the real thing*, and
   [docs/confidence.md](confidence.md) says so row by row.
2. **A mention.** A line in your documentation, or a `services:` block in one of
   your repositories, reaches more of the people who need this than any feature
   here.

### The three doors, if you want to try it before answering

Nothing here needs an account, and none of the three takes longer than a minute:

- **In a Go test** — `feinttest.Start(t)` starts the published image and hands
  back the endpoint, adding **zero dependencies** to your module
  ([feinttest/](../feinttest/)).
- **In GitHub Actions** — `uses: stephrobert/setup-feint@v1` installs the
  released binary, verifies its checksum before running it, and exports what
  your client needs
  ([marketplace](https://github.com/marketplace/actions/set-up-feint),
  [examples/github-actions/](../examples/github-actions/)).
- **Anywhere a container runs** — `ghcr.io/stephrobert/feint` as a service, with
  the GitLab and compose forms in [docs/install.md](install.md) and
  [examples/](../examples/).

And if you would rather see what it does to a real configuration than run it:
[examples/stacks/surveyed.md](../examples/stacks/surveyed.md) is fifteen
third-party Terraform stacks applied against this emulator, with what worked,
what did not, and which of your products they reached for.

What this project will not do, so the offer is not misread: invent quotas, prices
or capacity. [docs/limits.md](limits.md) states, section by section, what is
refused and why, and that list is the reason the rest can be trusted.

Adding a pack for a fourth cloud is measured work rather than a rewrite —
[docs/fourth-pack.md](fourth-pack.md) counts what it costs, honestly, including
the parts that got more expensive.

## What is counted, and what is not

Stars measure attention. These measure use, and each one is checkable by anybody:

| Signal | How to check it |
|---|---|
| External repositories running Feint | GitHub code search for `ghcr.io/stephrobert/feint` and `setup-feint` |
| Issues opened by somebody who is not the author | the tracker |
| Distinct contributors | the contributor list |
| A provider referencing it publicly | their documentation, newsletter, or a pipeline of their own |
| Real configurations that pass | the list below |

**Failures are published here too.** A configuration that does not apply is the
most valuable line on this page, and hiding it would turn the whole exercise into
a marketing claim — which is the one thing this repository is built not to be.

## The list

Empty, as of 2026-08-17. The first entry will be somebody else's.

What exists already is the inverse exercise, and it does not count here on
purpose: fifteen strangers' published stacks — five per provider — were pulled
from GitHub and applied against Feint in-house
([#262](https://github.com/stephrobert/feint/issues/262), the register is
[examples/stacks/surveyed.md](../examples/stacks/surveyed.md), four defects
filed). That measures what public configurations do to this emulator. This
list measures the thing the survey cannot: somebody who is not the author
choosing to run it.

| Configuration | Provider | Result | Issue |
|---|---|---|---|
| — | — | — | — |
