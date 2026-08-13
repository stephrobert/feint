# Covering the Exoscale IaaS layer, end to end

> [!NOTE]
> **Archived snapshot, dated 2026-07-30, frozen.** This is the measurement that
> cut this IaaS layer into batches, kept because the reasoning is the record.
> Nothing in it is refreshed: every count and every "today" is that date's, and
> several are known to be overtaken (route counts, lifecycle coverage, Terraform
> evidence). Do not quote this page for the present. Current figures live in the
> README's generated tables and [routes.md](../routes.md); batch state lives in
> the wave milestones and their issues; the sequence in
> [roadmap.md](../roadmap.md). Archived per #127: a dated truth left on an
> active page reads as a current one.

State of play, dated 2026-07-30. The figures come from the `internal/drift`
comparison run that day against `contracts/exoscale.json`, extracted from
Exoscale's own OpenAPI document, and from a checkout of
`exoscale/terraform-provider-exoscale` taken the same day (master). Exoscale has
no Go SDK scanner: `feint coverage --provider exoscale` reads the contract
artefact, not a checkout, and says so if asked for an SDK. Every figure copied
here rots with upstream: the source of truth stays `mise run drift:check`.

Same method and same rules as
[roadmap-2026-07-30-scaleway-iaas.md](roadmap-2026-07-30-scaleway-iaas.md), which carries the
architecture decisions common to all three packs.

## Summary

The pack serves 14 routes and declines **nothing**: 358 of 372 operations sit in
the untriaged column, which makes the coverage gate a wall rather than a work
list. That is the defining fact here, and it is not a code problem. Exoscale
publishes one flat API covering managed databases, Kubernetes, DNS, KMS and
inference alongside its IaaS, so roughly 250 of those 358 operations will never
be served and simply need a reason written down. What is genuinely IaaS is about
105 operations. The pack also serves no lifecycle action at all: `start`, `stop`
and `reset` on an instance do not exist, so `exo compute instance stop` fails
today. Six batches are proposed, in order: decline the non-IaaS surface, finish
the machine lifecycle and earn a first `terraform apply`, private networking,
block storage, the network load balancer, and the VPC product.

## 1. State of play, measured

### What the pack serves

`internal/providers/exoscale/pack.go:56-79` mounts 14 routes, each named with
the kebab-case `operationId` from Exoscale's own document rather than the Go
SDK's renamed form, and the comment at `pack.go:44-52` explains why: the renamed
names matched nothing when scanned.

| Area | Routes |
|---|--:|
| Instances (list, create, get, delete) | 4 |
| Operation polling | 1 |
| Catalogue (zones, templates, instance types) | 5 |
| SSH keys | 4 |

### What the measurement says

```text
provider exoscale: 372 operations upstream
  exoscale                  14 implemented    0 declined  358 unknown  (of 372)
  TOTAL                     14 implemented    0 declined  358 unknown
```

`Declined()` returns `nil`, and `pack.go:86-96` argues honestly that an empty
list beats a placeholder. That was right when the pack was a starter. It stops
being right the moment the surface is triaged, because a gate whose untriaged
column holds 96% of the API tells nobody anything: any upstream addition
disappears into a number nobody reads.

### The 358 untriaged, classified

Grouped by product family from `feint coverage --provider exoscale --contract
contracts/exoscale.json --format list`:

| Family | Operations | IaaS | Note |
|---|--:|---|---|
| DBaaS | 144 | no | PostgreSQL, MySQL, Kafka, OpenSearch, Valkey, Grafana, ClickHouse, and every user, database, endpoint and integration around them |
| Machine | 37 | yes | Instance lifecycle, protection, attachments, templates, snapshots |
| Networking | 27 | yes | Private networks, elastic IPs, security groups and rules, anti-affinity |
| SKS | 24 | no | Managed Kubernetes |
| IAM | 17 | no | Roles, API keys, access keys, organisation |
| DNS | 16 | no | Domains, records, reverse DNS |
| KMS | 16 | no | Keys, plus encrypt/decrypt/re-encrypt/generate-data-key |
| Block storage | 13 | yes | Volumes, snapshots, attach/detach, resize |
| AI | 21 | no | Models, deployments, inference API keys |
| Load balancer | 11 | yes | NLB and its services |
| VPC | 9 | yes | VPCs and their routes, a product the pack does not model at all |
| Instance pools | 8 | later | Autoscaling groups: IaaS-adjacent, out of the early batches |
| Accounts, quotas, deploy targets, events | 6 | yes | Small, read-only, on the path of several clients |
| SOS, billing, impact reports, users | ~9 | no | Object storage and account reporting |

**About 105 in, about 250 out.** As with Outscale, most of the triage work is
writing refusals.

## 2. What clients break on today

### No lifecycle at all

The pack serves create, read and delete, and nothing else. `start-instance`,
`stop-instance`, `reset-instance`, `reboot-instance`, `scale-instance` and
`resize-instance-disk` are all untriaged, so `exo compute instance stop` fails
against the emulator today. That is the shortest path to a client that cannot do
its job, and it is batch 2.

### The provider polls, constantly

The Terraform provider calls `client.Wait(...)` 89 times, measured across
`terraform-provider-exoscale` at master on 2026-07-30
(`grep -rF 'client.Wait(' --include='*.go' | wc -l`). Every mutation returns an Operation and
the provider blocks on it until it reaches a terminal state. The pack already
answers a successful operation immediately (`pack.go:276-297`) and stores it so a
double poll gets the same answer, which is the correct design; the constraint it
places on every future batch is that **each new mutation must mint an Operation
with the right `reference.command`**. That field was once hardcoded to
`"create-"+kind`, so a DELETE described itself as a creation, and a client
reading the reference to decide what finished was told the opposite of what
happened (`pack.go:263-266`). Nothing in the core enforces this; it is a pack
discipline, and it now applies over six products instead of one.

### Two SDK generations in one provider

`go.mod` of the provider pins both `egoscale v0.102.4` and `egoscale/v3
v3.1.42`. New resources (block storage, for one) use v3, older ones still use
v2. Both address the same `/v2` URL space, so the emulator sees one API; the
consequence is narrower but real: a response shape has to satisfy the older
decoder as well as the newer one, and only running the provider will say so.

### Block storage is already in the provider

`pkg/resources/block_storage/` declares `exoscale_block_storage_volume` and
`exoscale_block_storage_volume_snapshot`, calling `CreateBlockStorageVolume`,
`GetBlockStorageVolume`, `ResizeBlockStorageVolume`, `UpdateBlockStorageVolume`,
`CreateBlockStorageSnapshot` and `UpdateBlockStorageSnapshot`. So the product is
not speculative: a user writing an ordinary configuration reaches it.

### The asset already in place

The contract. `exo-cli.sh:128-131` reads `/_feint/conformance` and fails the
suite on any response that does not match Exoscale's published description. As
with Outscale, that guarantee applies for free to everything the batches add,
provided each new product is added to the contract extraction at the same time
as its routes.

## 3. What "the IaaS layer, end to end" means here

| Product | Ops | Status | Reason |
|---|--:|---|---|
| Instances and lifecycle | 4 served + 37 | In | The core; today the pack cannot even stop one |
| Catalogue (zones, types, templates) | 5 served | In, served | Measured as being on the critical path of a create (`pack.go:64-70`) |
| SSH keys | 4 served | In, served | The CLI registers one before it posts an instance |
| Security groups, anti-affinity | ~14 | In | Any realistic instance carries them |
| Elastic IPs | 11 | In | The published address is the project's own claim |
| Private networks and subnets | ~13 | In | Where the isolation argument lives |
| Block storage | 13 | In | Already reached by the Terraform provider |
| NLB | 11 | In | Fully IaaS |
| VPC and routes | 9 | In, later | A newer product, no pack model at all yet |
| Instance pools | 8 | Later | Autoscaling: IaaS-adjacent, but it multiplies instances, so it needs its own runtime thinking |
| Quotas, deploy targets, events | 6 | In, small | Cheap reads several clients make |
| DBaaS | 144 | Out | Managed databases: each is an entire product with its own runtime to feign |
| SKS | 24 | Out | Managed Kubernetes, same reason as Scaleway k8s and Outscale OKS |
| DNS, KMS, IAM, AI, SOS, billing | ~79 | Out | Adjacent services, none of them infrastructure a client provisions as compute or network |

## 4. The roadmap, by batch

### Batch 1: decline what will never be served (M in volume, S in risk)

- Scope: write roughly 250 refusals into `Declined()`, grouped by family with
  one reason per block: DBaaS (144), SKS (24), AI (21), KMS (16), DNS (16), IAM
  (17), SOS and billing (~9). List them by name, not by prefix, so an upstream
  addition under a declined family still shows up, which is what the Outscale
  pack already does for OKS.
- Expect the size to argue for itself: `Declined() []string` returning 250
  strings is unwieldy, and this is the batch that makes the
  `(operation, reason)` interface change proposed in the Scaleway roadmap
  worth doing across all three packs.
- Evidence: `gate.sh check` returns 0 after `mise run drift:update`; the
  untriaged column drops from 358 to about 105, and the README's Exoscale table
  starts describing a project rather than a gap.
- Risk: declining something a client actually calls. Mitigated by the order:
  every family above is a managed service, none of them is on any IaaS path.

### Batch 2: the machine lifecycle, and a first `terraform apply` (M)

- Scope: `start-instance`, `stop-instance`, `reboot-instance`, `reset-instance`,
  `scale-instance`, `resize-instance-disk`, `add-instance-protection` and its
  removal, `get-console-proxy-url` (or decline it with a reason: there is no
  console to proxy), plus security groups, their rules and anti-affinity groups,
  and elastic IPs with their instance attachment.
- Write `tools/conformance/exoscale/terraform/main.tf` and `terraform.sh`, plus
  a `conformance:terraform:exoscale` task.
- Evidence: `exo compute instance stop` then `start`, then reboot, scale,
  resize-disk, a delete refused while the instance is protected, a security
  group rule round-tripped, an anti-affinity group, and an elastic IP attached,
  published on the instance, detached and deleted. **This is the batch that
  removed the "preview" label** the README carried.

  The evidence was to have been a `terraform apply` of `exoscale_ssh_key`,
  `exoscale_security_group`, `exoscale_anti_affinity_group`,
  `exoscale_elastic_ip` and `exoscale_compute_instance`. That fixture does not
  exist and will not until upstream moves: the provider honours
  `EXOSCALE_API_ENDPOINT` for its v3 client only, so an apply splits between
  this emulator and a paying account. Filed as
  [exoscale/terraform-provider-exoscale#573](https://github.com/exoscale/terraform-provider-exoscale/issues/573);
  the measurement is in [limits.md](../limits.md#the-exoscale-terraform-provider-is-refused-and-why).
  `exo` proves the same behaviour, and it is the official client.
- Dependencies: batch 1 only for legibility. Risk: the provider's waiters. Every
  mutation must mint its Operation with the correct command.

### Batch 3: private networking (M)

- Scope: private networks, their subnets, `attach-instance-to-private-network`,
  `detach-instance-from-private-network`, `attach-instance-to-subnet`, and the
  external sources on a security group.
- Evidence: `terraform apply` with `exoscale_private_network` and an instance
  attached; under `FEINT_VM=incus-ovn`, a network suite on the model of
  `tools/conformance/scaleway/network.sh` proving the isolation the mode claims.
  Never claim isolation without naming the mode.
- Dependencies: batch 2. Risk: this is where a pack starts driving the machine
  layer, so the machine-driver-author skill and the ownership question
  (`mustOwn`) apply.

### Batch 4: block storage (M)

- Scope: the 13 operations, attach/detach and resize included.
- Evidence: `terraform apply` with `exoscale_block_storage_volume`,
  `exoscale_block_storage_volume_snapshot` and a volume attached to an instance;
  empty second plan.
- Dependencies: batch 2. Risk: the same bidirectional relation as everywhere
  else. The computed-fact model applies as is.

### Batch 5: the network load balancer (M)

- Scope: the 11 NLB operations, services included. Pure control plane.
- Evidence: `terraform apply` with `exoscale_nlb` and `exoscale_nlb_service`.
- Dependencies: batches 2 and 3.

### Batch 6: VPC (M)

- Scope: the 9 VPC operations and their routes. The pack models nothing here
  today, so this is a new resource kind rather than an extension.
- Evidence: `terraform apply` on the VPC resources the provider exposes, and
  under OVN mode a measurement of what routing can honestly be backed. Declare
  degraded rather than claim.
- Dependencies: batch 3.

Recommended order: 1, 2, 3, 4, 5, 6. Batch 1 is unglamorous and comes first
because every figure the project publishes about Exoscale is currently
unreadable. Batch 2 is the one that changes the pack's status.

## 5. What Exoscale adds to the architecture

**The asynchronous operation is a pack concern, and it must stay one.** Nothing
in `internal/core` knows what an Operation is, and nothing should: Scaleway and
Outscale answer synchronously. What the pack owes, over six products instead of
one, is that every mutation mints an operation with the right command and that
`trimOperations` (`pack.go:301-310`, bound at 512) still holds when a Terraform
run performs hundreds of mutations. 512 is comfortable today; a batch-4 apply
creating volumes and snapshots in a loop is the first thing likely to test it,
and the bound is a constant, not a measurement.

**The per-target lock is already taken here**, at `pack.go:234` in
`deleteInstance`, through `machine.Binding.Serialise`. The comment there names
the reason correctly: the same race the two other packs carry, which is why the
lock lives in the shared binding rather than in one pack that remembered it. New
lifecycle paths in batches 2 to 4 must keep taking it, proven by a concurrency
test.

**One catalogue lesson is already paid for here and worth restating**, because
it generalises: the catalogue endpoints used to be declined with an argument
that was sound about the API and false about the client (`pack.go:88-95`). `exo`
lists zones, types and templates and registers an SSH key before it posts
anything. Every batch above should assume the same asymmetry and measure the
client rather than read the specification.

## 6. What will not be done

- **DBaaS** (144 operations, 39% of the entire surface): each engine is a
  product with its own runtime. Emulating the control plane alone would hand out
  connection URIs pointing at nothing, which is the exact shape of dishonesty
  this project exists to avoid.
- **SKS**: managed Kubernetes, same decision as Outscale OKS and Scaleway k8s.
- **DNS and reverse DNS**: a zone the emulator serves resolves nowhere; the
  records would be inert.
- **KMS, including encrypt/decrypt**: real cryptography against a fake key store
  is worse than none, and a client that stores a ciphertext the emulator minted
  has lost data the moment the process stops.
- **IAM beyond SSH keys**: the emulator authenticates nothing.
- **AI, models and deployments**: not infrastructure, and nothing to feign.
- **SOS**: object storage, declined across the whole project for the reason
  measured in [limits.md](../limits.md).
- **Billing, impact and usage reports**: an emulator has no consumption. Serving
  figures would mean inventing them.

## Appendix: reproducing the measurements

```bash
mise run upstream:sync
feint coverage --provider exoscale --contract contracts/exoscale.json
feint coverage --provider exoscale --contract contracts/exoscale.json --format triage
feint coverage --provider exoscale --contract contracts/exoscale.json --format list
mise run drift:check
```

There is no `--sdk` form for Exoscale: the scanner does not exist, the contract
artefact is the surface, and `tools/contract/update.sh` re-extracts it from
`.upstream/exoscale-openapi.yaml`. The Terraform provider is measured against a
checkout (`git clone --depth 1
https://github.com/exoscale/terraform-provider-exoscale`), under `pkg/resources/`.
