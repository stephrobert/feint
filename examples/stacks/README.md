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
