# Three platform stacks, two applied on every pull request

These are not snippets. Each stack is the shape a platform team ends up with,
written the way the surveyed third-party stacks are written — modules,
`for_each` over typed variables, a `terraform.tfvars.example`, zones asked
from the API rather than hardcoded. The Scaleway and Outscale stacks run
against Feint in CI on every pull request — apply, an empty second plan,
destroy — with no cloud account and nothing billed. The Exoscale one is run
by hand, for a reason [measured below](#the-exoscale-stack-needs-the-patched-provider).

## Running one

```bash
feint start
cd examples/stacks/scaleway && terraform init && terraform apply
```

Every variable has a working default; `terraform.tfvars.example` in each
directory shows what an override looks like.

## The stacks

Resource counts are measured — the number the apply reports — not estimated.

### `scaleway/` — 37 resources, 6 machines

- **Builds**: two VPCs (workload and management), three private networks and
  a route between them, three per-tier security groups, a bastion as the only
  public door, a web tier with IPAM-booked addresses and data volumes, an
  application tier with no public address driven by a typed `for_each` map,
  block-storage volumes with their own snapshots, a golden image cut from a
  snapshot, cloud-init everywhere.
- **Real motifs it reproduces** (sources in [`surveyed.md`](surveyed.md)):
  IPAM-first addressing and the `one(pn.ipv6_subnets).subnet` read from
  sergelogvinov/terraform-talos; the named-workers map from the same stack's
  typed tiers; the `tfvars.example` hygiene from kiwinet-infra-cloud.
- **Keeps under watch**: [#270] — the IPv6 `/64` a private network must
  publish; the output consuming it dies on apply against an emulator that
  omits it, and a stack already applied then cannot even destroy. Also the
  volume → snapshot → image chain, built and deliberately not booted from
  ([#83], see the comment in the file).

### `outscale/` — 38 resources, 3 machines

- **Builds**: two Nets from a reusable `modules/net` module, four subnets —
  the public tier spread over both subregions — a peering and the route
  through it, an internet service and public route table, a NAT service with
  its own public IP and the private route table whose default route crosses
  it, per-tier security groups, a keypair, a standalone NIC, public IPs
  linked to Vms, a volume attached to a machine, an image from a snapshot.
- **Real motifs it reproduces**: the Net-plus-subnets module from
  chimere-eu/ztiac; `data "outscale_subregions"` indexed past `[0]` from
  davmartini/ocp_outscale; explicit `placement_subregion_name` beside the
  subnet from michaelcourcy/kasten-on-outscale; NAT-behind-the-public-tier
  from all four substantial surveyed stacks; the created-in-stack keypair
  from osc-k8s-rke-cluster.
- **Keeps under watch**: [#249] — a route pointing at a Net peering; [#250] —
  a tagged NIC reading back with its tags; [#268] — a Vm's placement must
  round-trip or the second plan re-plans for ever; [#269] — the subregion
  catalogue must match the write path, or indexing the data source dies at
  plan. The route through the NAT service is coverage the conformance
  fixtures do not have: they create a NAT service and never route through it.

### `exoscale/` — 13 resources, 3 machines

- **Builds**: two managed private networks with declared lease ranges,
  per-tier security groups (one rule naming the other group rather than an
  address), an anti-affinity group, an elastic IP with a healthcheck in front
  of a web instance, a block-storage volume attached to that instance with
  its snapshot, and an instance pool for the application tier.
- **Real motifs it reproduces**: the healthchecked elastic IP from
  PhilippeChepy/terraform-exoscale-vault — the only surveyed stack that came
  out entirely green; the group-names-group rule and instance pools from
  appuio/terraform-openshift4-exoscale; the persistent-data-volume chain from
  HealsCodes/ephemeral-devbox; the explicit template `visibility` filter
  whose reads produced the [#271] transcript.
- **Keeps under watch**: [#271] — a query parameter the API document declares
  must be served or refused, never dropped; the elastic-ip healthcheck block
  must read back as sent. It is also the Terraform reader of the
  block-storage and instance-pool work of #12 and #232, which the exo CLI
  suite otherwise exercises alone.
- **Not run by CI** — it needs the patched provider,
  [measured below](#the-exoscale-stack-needs-the-patched-provider).

## What a run proves, and the rule that makes the stacks grow

Each run asserts three things, and the middle one does the finding:

```bash
terraform apply                        # the emulator answered
terraform plan -detailed-exitcode      # must exit 0: it read back what was sent
terraform destroy                      # nothing wedges on a half-truth
```

An apply that succeeds proves the emulator answered. An empty second plan
proves it read back what the provider sent — a 200 with a constant inside it
fails here and nowhere else.

`tools/conformance/*/terraform/` already holds fixtures. They prove what
somebody thought to assert; these stacks exercise what somebody actually
writes, and the difference was measured rather than argued — twice:

- Within an hour of the first stack being applied: [#249] (a route could not
  point at a Net peering — the suite asserted peering state machines and
  never routed through one) and [#250] (a tagged NIC read back without its
  tags — the suite tagged Nets, Vms and volumes, never an interface).
- Then the same argument turned outward: fifteen stacks from GitHub, five per
  provider, written by people who had never seen this repository, applied
  against the emulator. [`surveyed.md`](surveyed.md) is that register (#262),
  and it produced [#268], [#269], [#270] and [#271].

**The rule: every defect a stack finds gets the resource that found it added
to a stack**, so the next run keeps checking it. That is what keeps these
tests rather than demonstrations, and it is why they grow.

## What CI runs them against, and why not the published tag

Every pull request applies the Scaleway and Outscale stacks twice: against
the emulator built from the branch (the `terraform` and `opentofu` legs — the
change under review), and against the container image built from the branch
with the release's own recipe (the `image` job — the packaging a user pulls).
Not against the last tag on ghcr.io, and that is arithmetic rather than
preference: a published image is immutable, and the rule above makes the
stacks grow every time they find a defect, so the two must diverge exactly
when the stacks do their job. Measured before it was decided — v0.8.0
predates the fixes for [#249] and [#250], both stacks exercise those fixes,
and a gate on the published tag would have been red by construction from its
first run. The commit a tag is cut from gets the same image run on `main`
after the merge, which is what makes the image eventually published a proven
one.

## The Exoscale stack needs the patched provider

The published Exoscale provider builds two clients: one honours
`EXOSCALE_API_ENDPOINT`, the other has `.exoscale.com` compiled in. An apply
therefore does not fail — it **splits**, half against the emulator and half
against a paying account. Feint refuses that client by its user agent rather
than serving half of it, and the whole measurement is in
[docs/limits.md](../../docs/limits.md#the-exoscale-terraform-provider-is-refused-and-why),
with the pinned fork and the `dev_overrides` recipe.

To run it:

```bash
FEINT_EXOSCALE_ALLOW_TERRAFORM=1 feint start
export TF_CLI_CONFIG_FILE=/tmp/dev.tfrc     # the dev_overrides from limits.md
eval "$(feint env exoscale)"
cd examples/stacks/exoscale && terraform apply   # no init: the override resolves the provider
```

**CI does not run it, on purpose.** No gate here clones a third-party
repository — that would put somebody else's availability in this project's
pipeline — and a client this project patched is not the official client, so
it could not count towards conformance in any case. It was last applied by
hand on 2026-08-18: apply, an empty second plan, destroy — 13 resources, zero
contract violations.

## Offering your stack: what we ask, and what we do with it

The fifteen in [`surveyed.md`](surveyed.md) are the strongest instrument this
project has, and their weakness is that **we chose the fifteen**. A stack whose
authors run it as a required gate on their own infrastructure is different
evidence: it changes without asking us, on their schedule, against providers
they resolve fresh. One such offer arrived on 2026-08-19 ([#327]) and asked the
right question — *tell us the contract you want it to meet* — so here it is,
before the next one asks.

### The contract

1. **A public repository, a commit, and a licence that lets us replay it.**
   "We ran your stack" names nothing without the commit it was at. Every entry
   in the register carries repository, commit and licence for exactly this
   reason: a reader has to be able to redo it without asking anybody.
2. **A root somebody actually applies.** Terraform or OpenTofu, either; the
   engine is not the point. What is the point is that the configuration exists
   for its own sake and moves when its authors need it to. A stack written to
   please this emulator measures this emulator against itself.
3. **Every provider constrained, in the root and in every module.** Not
   necessarily exact — a floor is worth having and is often *why* a lane is
   useful, since a `~> 2.68` is what walked into Scaleway 2.81.0 and told us
   about it. What matters is that the constraint is declared and readable, so
   that a red lane can name which version answered. This is the one point the
   original four did not have, and it is the one our own directory failed on:
   `examples/stacks/outscale/modules/net` was applied on every pull request
   while declaring no constraint at all, and nothing said so until the
   generated table of [#325] existed. It is checked here now
   ([`docs/clients.md`](../../docs/clients.md)), which is the least we can do
   before asking it of somebody else.
4. **apply → empty second plan → destroy, and the destroy on the failure path
   too.** The middle one does the finding, and the last one is not pedantry:
   this repository's own runner hung its cleanup on a `RETURN` trap that never
   fired where it mattered, and every rerun after a failure then died on its own
   leftovers instead of the defect.
5. **No credentials at all, and no fallback to any.** Every official client in
   these three ecosystems falls back to the operator's stored credentials when
   an endpoint is missing, so a lane that merely *lacks* credentials is not the
   same thing as a lane that *fails* without them. Ours reaches for the second
   ([#280]); the offer above arrived having reached the same rule
   independently, which is the strongest evidence either of us has that it is
   the right one.
6. **feint installed at a named version, verified against its checksum.** Not
   `latest`: a mutable reference installs a binary neither of us can name
   afterwards, which makes a red lane unattributable to anything.
7. **A stated wall.** What the stack asks for that this emulator declines, named
   in advance. Without it, a red that is a declined product reads as a defect,
   and both sides spend a day finding that out.

Two things the first draft of this list had and that do not survive contact:
the **number of providers** (one is fine; two proves nothing extra) and
**OpenTofu specifically** (point 2 covers it).

### What we do with it, and what we will not

**We record it and replay it on demand. We do not put it in our CI.** The
argument is not about trust, and it is worth stating plainly because the offer
was generous:

- A third party's repository changes without our decision. A required gate that
  can go red for a reason nobody here chose is a gate whose red we cannot act
  on, and a gate whose red cannot be acted on is one everybody learns to skip —
  the same reasoning that keeps `conformance` out of this repository's
  pre-commit hook.
- **No gate here clones a third-party repository.** That would put somebody
  else's availability inside this project's pipeline. The rule predates this
  offer: it is why the Exoscale stack above is run by hand.

What we do instead, and it is what actually paid: **their report is evidence,
and it is credited as theirs.** The break in Scaleway provider 2.81.0 was found
by a downstream lane and reported to us, which is how [#325] and [#326] exist.
A stack we had vendored ourselves would have found it whenever somebody thought
to re-record it. So the exchange we ask for is a report when a lane goes red,
with the transcript — not a webhook.

And what we owe back: the stack named in the register with what it exercises,
the findings credited to whoever found them, and a break we caused treated as
ours to fix.

## What they do not prove

Nothing here boots a machine, filters a packet or isolates a subnet: that
needs `--vm` and a host with Incus.
[`docs/confidence.md`](../../docs/confidence.md) says row by row what changes
when you have one, keyed on the capability the runtime declares rather than
on a mode name.

[#83]: https://github.com/stephrobert/feint/issues/83
[#249]: https://github.com/stephrobert/feint/issues/249
[#250]: https://github.com/stephrobert/feint/issues/250
[#268]: https://github.com/stephrobert/feint/issues/268
[#269]: https://github.com/stephrobert/feint/issues/269
[#270]: https://github.com/stephrobert/feint/issues/270
[#271]: https://github.com/stephrobert/feint/issues/271
[#280]: https://github.com/stephrobert/feint/issues/280
[#325]: https://github.com/stephrobert/feint/issues/325
[#326]: https://github.com/stephrobert/feint/issues/326
[#327]: https://github.com/stephrobert/feint/issues/327
