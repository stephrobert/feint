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

## What they do not prove

Nothing here boots a machine, filters a packet or isolates a subnet: that needs
`--vm` and a host with Incus. [`docs/confidence.md`](../../docs/confidence.md)
says row by row what changes when you have one, keyed on the capability the
runtime declares rather than on a mode name.
