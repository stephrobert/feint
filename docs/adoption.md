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

What this project will not do, so the offer is not misread: invent quotas, prices
or capacity. [docs/limits.md](limits.md) is thirty sections of what is refused
and why, and that list is the reason the rest can be trusted.

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

| Configuration | Provider | Result | Issue |
|---|---|---|---|
| — | — | — | — |
