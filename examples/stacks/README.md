# Two platform stacks, applied on every pull request

These are not snippets. Each one is the shape a platform team ends up with, and
each runs against Feint in CI — apply, an empty second plan, destroy — with no
cloud account and nothing billed.

```bash
feint start
cd examples/stacks/scaleway && terraform init && terraform apply
```

| Stack | What it carries |
|---|---|
| [`scaleway/`](scaleway/main.tf) | two VPCs, three private networks and a route between them, three security groups, a bastion, a web tier with IPAM-booked addresses, an application tier with no public address, a golden image cut from a snapshot, block-storage volumes with their own snapshots, cloud-init everywhere — six machines |
| [`outscale/`](outscale/main.tf) | two Nets with a peering and the route across it, a public subnet with an internet service and a route table, a private subnet, a shared-services Net, a standalone NIC, per-tier security groups, public IPs linked to Vms, a volume attached to a machine, an image from a snapshot |
| [`exoscale/`](exoscale/main.tf) | two private networks, per-tier security groups (one naming the other rather than an address), an anti-affinity group, a web instance holding an elastic IP, and an instance pool for the application tier — **needs the patched provider, see below** |

## Why they exist, and it is not decoration

`tools/conformance/*/terraform/` already holds fixtures. They prove what somebody
thought to assert. These stacks exercise **what somebody actually writes**, and
the difference was measured rather than argued: within an hour of the first one
being applied, two defects surfaced that every existing gate was blind to.

- **[#249](https://github.com/stephrobert/feint/issues/249)** — a route could not
  point at a Net peering, so two peered Nets stayed unreachable. The suite
  creates peerings and asserts their state machine; it never routes through one.
- **[#250](https://github.com/stephrobert/feint/issues/250)** — a tagged NIC read
  back without its tags, so a Terraform plan never converged. The suite tagged
  Nets, Vms and volumes, and never an interface.

Neither was visible to a schema check. A missing route target answers a clean
400, and a view publishing a constant empty list has exactly the shape an
untagged NIC should have: the form was right and the fact was wrong.

## The rule when you change one

**Every defect a stack finds gets its resource added to the stack.** That is what
keeps them tests rather than demonstrations, and it is the same rule the
conformance suites obey.

The assertion that does the finding is the middle one:

```bash
terraform plan -detailed-exitcode      # must be empty
```

An apply that succeeds proves the emulator answered. An empty second plan proves
it read back what the provider sent.

## What CI runs them against, and why not the published tag

Every pull request applies them twice: against the emulator built from the
branch (the `terraform` and `opentofu` legs — the change under review), and
against the container image built from the branch with the release's own recipe
(the `image` job — the packaging a user pulls). Not against the last tag on
ghcr.io, and that is arithmetic rather than preference: a published image is
immutable, and the rule above makes the stacks grow every time they find a
defect, so the two must diverge exactly when the stacks do their job. Measured
before it was decided — v0.8.0 predates the fixes for [#249] and [#250], both
stacks exercise those fixes, and a gate on the published tag would have been
red by construction from its first run. The commit a tag is cut from gets the
same image run on `main` after the merge, which is what makes the image
eventually published a proven one.

[#249]: https://github.com/stephrobert/feint/issues/249
[#250]: https://github.com/stephrobert/feint/issues/250

The published Exoscale provider builds two clients: one honours
`EXOSCALE_API_ENDPOINT`, the other has `.exoscale.com` compiled in. An apply
therefore does not fail — it **splits**, half against the emulator and half
against a paying account. Feint refuses that client by its user agent rather than
serving half of it, and the whole measurement is in
[docs/limits.md](../../docs/limits.md#the-exoscale-terraform-provider-is-refused-and-why),
with the pinned fork and the `dev_overrides` recipe.

To run it:

```bash
FEINT_EXOSCALE_ALLOW_TERRAFORM=1 feint start
export TF_CLI_CONFIG_FILE=/tmp/dev.tfrc     # the dev_overrides from limits.md
eval "$(feint env exoscale)"
cd examples/stacks/exoscale && terraform apply
```

**CI does not run it, on purpose.** No gate here clones a third-party repository
— that would put somebody else's availability in this project's pipeline — and a
client this project patched is not the official client, so it could not count
towards conformance in any case. It was applied by hand on 2026-08-17: apply, an
empty second plan, destroy, and zero contract violations.

## What they do not prove

Nothing here boots a machine, filters a packet or isolates a subnet: that needs
`--vm` and a host with Incus. [`docs/confidence.md`](../../docs/confidence.md)
says row by row what changes when you have one, keyed on the capability the
runtime declares rather than on a mode name.
