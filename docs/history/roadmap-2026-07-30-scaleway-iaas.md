# Covering the Scaleway IaaS layer, end to end

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
surface scan run that day against the `.upstream/scaleway-sdk-go` checkout at
commit `06ce682` (2026-07-28), and from a checkout of
`scaleway/terraform-provider-scaleway` taken the same day (master).
Every figure copied here rots with upstream: the source of truth stays
`mise run drift:check` and `feint coverage --sdk .upstream/scaleway-sdk-go
--products <list>`, which replay the measurement in seconds.

The French working notes this document was translated from are in
`notes/roadmap-scaleway-iaas.md`, kept outside this repository. Where the
two disagree, the measurement commands in the appendix settle it.

## Summary

The Scaleway pack serves 55 routes across five products (instance, vpc, ipam,
iam, marketplace) and the real clients pass: `scw`, Terraform, the probe. A
complete IaaS layer adds four products (block, lb, vpcgw, plus finishing
instance, vpc and ipam), roughly 150 operations to triage of which about a
hundred to serve. The path that breaks first today is an `sbs_volume` root
volume: the Terraform provider reads `/block/v1` as the fallback from
`/instance/v1`, and the fallback answers 404. Six batches are proposed, in
order: an honest gate (iam and marketplace), finish instance/v1, block/v1, ipam
and vpc in full, lb/v1, vpcgw/v2. Three architecture decisions come before the
first batch that touches a lifecycle: where the per-target lock lives (the lock
itself exists and is taken; see section 6), the model for references between
resources, and where runtime backing stops.

## 1. State of play, measured

### What the pack serves

`internal/providers/scaleway/pack.go:49-144` mounts 55 routes, each with its
`Route.Operation` under the exact SDK name:

| Area | Routes | File |
|---|--:|---|
| Servers (list, create, get, update, delete, action) | 6 | `servers.go` |
| Flexible IPs | 5 | `ips.go` |
| Security groups and rules | 11 | `securitygroups.go`, `firewall.go` |
| Private NICs | 4 | `privatenics.go` |
| IPAM (ListIPs, GetIP) | 2 | `ipam.go` |
| User data | 4 | `userdata.go` |
| Instance volumes | 5 | `volumes.go` |
| VPC and Private Networks | 10 | `vpc.go` |
| Catalogue (types, images, marketplace) | 3 | `catalog.go`, `images.go` |
| IAM SSH keys | 5 | `sshkeys.go` |

### What the measurement says

Output of `feint coverage --sdk .upstream/scaleway-sdk-go --products
instance,vpc,ipam` on 2026-07-30 (the per-version breakdown is new, see the
Coverage tooling section):

```text
provider scaleway: 179 operations upstream
  instance                  37 implemented   73 declined   27 unknown  (of 137)
    v1                      37 implemented   14 declined   27 unknown  (of 78)
    v2alpha1                 0 implemented   59 declined    0 unknown  (of 59)
  ipam                       2 implemented    1 declined    7 unknown  (of 10)
    v1                       2 implemented    0 declined    7 unknown  (of 9)
    v1alpha1                 0 implemented    1 declined    0 unknown  (of 1)
  vpc                       10 implemented    0 declined   22 unknown  (of 32)
  TOTAL                     49 implemented   74 declined   56 unknown
```

How to read those numbers correctly: on instance/v1, the real scope, 37 of 78
operations are served, 14 refused with a reason, 27 left to triage. The 59
declined under v2alpha1 are the alpha rewrite refused wholesale
(`pack.go:222-291`) and represent no work at all.

The whole SDK surface, every product except `std`, is 1636 operations at commit
`06ce682` (measured with `coverage --products <all>`). The IaaS layer targeted
here is about 250 of them.

### The gap between the gate and the pack

The drift gate only covers `instance,vpc,ipam` (`tools/drift/gate.sh:31`,
variable `FEINT_PRODUCTS`), while the pack also serves iam (5 routes) and
marketplace (1 route). Those six routes are therefore served but unwatched: an
iam operation added upstream fails no CI. Measured by adding the products to the
command: iam weighs 83 operations (5 served, 78 untriaged), marketplace 8 (1
served, 7 untriaged). That is batch 1.

### What is refused, and why

`Declined()` (`pack.go:177-292`) already separates the two families:

- Permanently out of scope: the real inventory (dashboard, availability,
  quotas), default rules nobody measured, the legacy block migration, snapshot
  export to Object Storage (measured in [limits.md](../limits.md), the S3 endpoint
  is hardcoded in the provider), the metadata API (169.254.42.42, a different
  surface), ipam/v1alpha1 (a superseded draft).
- Explicit temporary refusal: instance/v2alpha1 wholesale, listed operation by
  operation so upstream additions stay visible.

### The 56 untriaged, grouped

Output of `--format triage`: placement groups (9), snapshots (5), images (4),
volume attach/detach (6, file-system attach included), VPC routes (4 plus
ListRoutesWithNexthop), VPC ACLs
(2), VPC ingress rules (5), VPC connectors (5), ListSubnets and overlaps (2),
DHCP/routing/propagation (3), IPAM lifecycle (7), assorted instance calls (3).
Batches 2 and 4 address all of them, or decline them with a reason.

## 2. Coverage tooling

What was run to judge it: `feint coverage` in all four formats (text, json,
triage, list), with and without `--products`, across the fourteen candidate
products; `tools/drift/gate.sh check` (exit 0, no drift on 2026-07-30); and a
read of `internal/drift/report.go` together with the consumers of its JSON
(`internal/cli/docs.go:316`, `tools/drift/gate.sh`).

Verdict: the tool was nearly enough. Per-product filtering, three statuses,
orphan routes, baseline, human and machine formats — all present. One real gap
showed up while preparing this document: the report was blind to API versions.
"instance: 137 operations, 73 declined" reads as a product refused three
quarters over, when 59 of those 73 are the v2alpha1 rewrite and v1 is mostly
served. Same problem for triaging block (13 v1 ops, 14 v1alpha1 to decline) or
vpcgw (37 superseded v1 ops, 27 v2 ops to serve): the triage decision is taken
per version, and getting that view meant piping `--format list` through awk.

What changed (`internal/drift/report.go`, `report_test.go`):

- `Entry` now carries the version (`Version`), which `Compare` copies from
  `Operation.Version` instead of discarding it.
- `Report.Versions()` returns per-version counts; the text format adds one line
  per version under any product that has several (nothing for a single-version
  product, so the gate's signal stays dense); the JSON gains a `versions` field
  per product, additive, ignored by the docs generator which reads the existing
  fields.
- Three new tests, falsified: removing the version copy in `Compare` makes
  `TestVersionsSplitsAProductAcrossItsAPIVersions`,
  `TestWriteTextBreaksDownMultiVersionProducts` and
  `TestWriteJSONCarriesPerVersionCounts` fail, and the fix makes them pass —
  run, not assumed.
- The `coverage/*-coverage.json` artefacts were regenerated with the same
  commands `gate.sh update` uses; the baselines are unchanged (their format
  carries no version); `gate.sh check` still returns 0; `mise run check` passes
  (gofmt, vet, golangci-lint, `go test -race`, run on 2026-07-30).

Deliberately not done: carrying the reason for a refusal into the report would
mean changing the `Declined() []string` signature on the `emulator.Pack`
interface for all three packs. It is worth doing — the reason lives today in
comments only a reader of the code ever sees — but it is an interface change
rather than a tooling tweak, so it is proposed in batch 1.

## 3. What "the IaaS layer, end to end" means here

The boundary: something is IaaS when a client provisions it as generic
infrastructure (compute, block storage, network, addressing, load balancing) and
the emulator can serve it honestly, which means with real conformance and, when
a runtime is configured, backed by something that exists. Not IaaS: a managed
service (database, Kubernetes), physical hardware there is no useful way to
feign, or a surface the official clients cannot be pointed at the emulator.

| Product | Surface (2026-07-30) | Status | Reason |
|---|--:|---|---|
| instance/v1 | 78 ops | In, to finish | The core; 37 served, 27 to triage |
| block/v1 | 13 ops | In | Replaces instance volumes; the Terraform provider already reads it as a fallback |
| vpc/v2 | 32 ops | In, to finish | 10 served; routes, ACLs, subnets remain |
| ipam/v1 | 9 ops | In, to finish | 2 served; BookIP is the pivot for lb and vpcgw |
| lb/v1 (ZonedAPI) | 54 ops | In | IaaS in the full sense; `scaleway_lb` is a common Terraform resource |
| lb/v1 (regional API) | 53 ops | To decline | Deprecated; the provider only uses ZonedAPI (measured under `services/lb/`) |
| vpcgw/v2 | 27 ops | In | NAT egress for a Private Network; the provider imports v2 (`services/vpcgw/public_gateway.go`) |
| vpcgw/v1 | 37 ops | To decline | Superseded by v2 |
| marketplace/v2 | 8 ops | In (minimal) | Already indispensable to the CLI; finish the triage |
| iam/v1alpha1 | 83 ops | SSH keys only | Keys are IaaS (machine login); policies, groups and applications are not |
| flexibleip/v1alpha1 | 11 ops | Out | Elastic Metal flexible IPs, not Instance; follows the fate of baremetal |
| account/v3 | 11 ops | Later, if measured | Serve only if a client is measured reading it (not measured to date) |
| baremetal, dedibox, applesilicon | 37+99+24 ops | Out | Hardware: backing is impossible and a pure control plane would prove nothing |
| k8s, rdb, redis, mongodb, … | 34+… ops | Out | Managed services, not IaaS |
| Serverless (function, container, jobs) | | Out | PaaS |
| Object Storage | outside the Go SDK | Out | Measured ([limits.md](../limits.md)): the S3 endpoint is built by hand, unreachable without DNS and TLS |
| Domain/DNS, secret, key manager | | Out | Adjacent services, not the IaaS layer |
| instance/v2alpha1, block/v1alpha1, ipam/v1alpha1 | 59+14+1 ops | Declined | Superseded or alpha versions, refused wholesale and by name |

## 4. What clients break on today, and the reads that precede a write

The value criterion: which official-client command fails. Measured against the
Terraform provider source (checkout of 2026-07-30):

1. **`sbs_volume` root volume.** The provider reads every volume through
   `GetUnknownVolume`
   (`internal/services/instance/instancehelpers/block.go:133-159`):
   `instance.GetVolume` first, and if the answer is a typed 404
   (`errors.As(&scw.ResourceNotFoundError{})`), it falls back to
   `block.GetVolume`. Today that fallback hits an unmounted route. Two
   consequences: serving `/block/v1` is the highest-value unblock, and the
   instance 404 must keep its exact typed shape (`errors.go:9-12` already
   documents it).
2. **`scaleway_ipam_ip`.** The provider calls BookIP, GetIP, UpdateIP and
   ReleaseIP (`internal/services/ipam/ip.go`, measured). Only List and Get are
   served, so any `terraform apply` carrying that resource fails.
3. **`scaleway_lb` and its network attachment.** Creation goes through
   `ZonedAPICreateLBRequest`, and then the provider *waits*: `WaitForLb` loops
   on GetLB until `status=ready` (`services/lb/waiters.go:12-25`). Attaching to
   a Private Network passes `ipam_ip_ids`
   (`services/lb/private_network.go:107-111`), so lb depends on ipam.
4. **`scaleway_vpc_public_gateway`**: the provider imports `vpcgw/v2`; the
   historical DHCP resources stay on v1 but are deprecated.
5. **`scw instance snapshot|image|placement-group …`**: immediate 404, the
   routes do not exist.

Traps of the "declining the catalogue breaks the CLI" family, looked for on the
new products:

- block: `ListVolumeTypes` is that product's catalogue; declining it would
  reproduce the `min_size` trap [limits.md](../limits.md) records. Serve it
  small and fixed, the way `catalog.go` does.
- lb: `ListLBTypes` plays the same role; and every transient state must converge
  to `ready` immediately, or each provider waiter becomes a twenty-minute
  timeout.
- vpcgw: `ListGatewayTypes`, same reasoning.
- The `scw block|lb` CLI may make further preliminary reads: not measured, to be
  instrumented when each batch opens by replaying the command against the
  emulator and reading the 404s in the log (which is exactly what `feint logs`
  shows).

## 5. The roadmap, by batch

Every batch ends with its real conformance evidence: a unit test alone closes
nothing, and [CONTRIBUTING.md](../../CONTRIBUTING.md) states why. Operation names
are the SDK's own, checkable
with `--format list`.

### Batch 1: an honest gate (S)

- Scope: move `FEINT_PRODUCTS` to `instance,vpc,ipam,iam,marketplace`
  (`tools/drift/gate.sh:31`) and triage the 85 operations that appear: decline
  the bulk of iam (policies, applications, groups, API keys, JWT, logs — outside
  IaaS, only SSH key management stays) and the 7 unserved marketplace operations
  except `GetLocalImage` if the probe needs it; refresh the baseline with
  `mise run drift:update`.
- Take the opportunity to carry refusal reasons: `Declined()` becomes a list of
  `(operation, reason)` surfaced by the report. An interface change across all
  three packs, small but cross-cutting.
- Evidence: `gate.sh check` returns 0 against the new baseline; the unchanged
  conformance suite passes.
- Risk: none technical; the cost is the triage itself.

### Batch 2: finish instance/v1 (M)

- Scope, the 27 untriaged: `CreateSnapshot`, `GetSnapshot`, `ListSnapshots`,
  `UpdateSnapshot`, `DeleteSnapshot`; `CreateImage`, `ListImages`,
  `UpdateImage`, `DeleteImage`; `CreatePlacementGroup`, `GetPlacementGroup`,
  `ListPlacementGroups`, `UpdatePlacementGroup`, `SetPlacementGroup`,
  `DeletePlacementGroup`, `GetPlacementGroupServers`,
  `SetPlacementGroupServers`, `UpdatePlacementGroupServers`; `AttachVolume`,
  `DetachVolume`, `AttachServerVolume`, `DetachServerVolume`;
  `ListServerActions`; `UpdatePrivateNIC`; `ReleaseIPToIpam`.
- To decline: `AttachServerFileSystem`, `DetachServerFileSystem` (the File
  Storage product is not served, reason: "follows the fate of file/v1").
- Evidence: `scw instance snapshot create` then an image from that snapshot; a
  `terraform apply` carrying `scaleway_instance_placement_group` and an extra
  attached volume; empty second plan; destroy.
- Dependencies: none. Main risk: attachment semantics (an attached volume
  changes the server's `volumes` and the root volume refuses detachment); this
  is the first genuinely bidirectional relation, see section 6.

### Batch 3: block/v1 (M)

- Scope: `CreateVolume`, `GetVolume`, `ListVolumes`, `UpdateVolume`,
  `DeleteVolume`, `CreateSnapshot`, `GetSnapshot`, `ListSnapshots`,
  `UpdateSnapshot`, `DeleteSnapshot`, `ListVolumeTypes` (the catalogue, small
  and fixed). Then the `sbs_volume` root volume on the instance side:
  `createServer` accepts the type and materialises the volume in block.
- To decline: `block/v1alpha1` wholesale (14, superseded),
  `ExportSnapshotToObjectStorage` and `ImportSnapshotFromObjectStorage` (same
  measured reason as `instance/v1/API.ExportSnapshot`).
- Evidence: `terraform apply` with `scaleway_block_volume`,
  `scaleway_block_snapshot`, and a `scaleway_instance_server` whose
  `root_volume.volume_type = "sbs_volume"`; empty second plan; `scw block volume
  create/list/delete`.
- Dependencies: batch 2, for attachment consistency. Main risk: the provider's
  double look (the 404 fallback measured in 4.1); block's `references` field,
  through which the provider finds the server holding a volume, is a relation
  that needs modelling properly.

### Batch 4: ipam/v1 and vpc/v2 in full (M)

- ipam scope: `BookIP`, `ReleaseIP`, `ReleaseIPSet`, `UpdateIP`, `AttachIP`,
  `DetachIP`, `MoveIP`. The pack's address allocator already exists
  (`pack.go:31-39`, `addresses` lock); BookIP makes it client-drivable.
- vpc scope: `ListSubnets`, `ListSubnetOverlaps`, `GetACL`, `SetACL`,
  `CreateRoute`, `GetRoute`, `UpdateRoute`, `DeleteRoute`,
  `ListRoutesWithNexthop`, `EnableRouting`, `EnableDHCP`,
  `EnableCustomRoutesPropagation`, ingress rules (5).
- To decline: `CreateVPCConnector` and its four siblings (VPC interconnection,
  out of scope until OVN mode has measured peering).
- Evidence: `terraform apply` with a `scaleway_ipam_ip` booked and then carried
  by a private NIC; `scw ipam ip list`; under `FEINT_VM=incus-ovn`, the ACL set
  by SetACL actually filters (an extension of
  `tools/conformance/scaleway/network.sh`).
- Dependencies: none strictly. Risk: SetACL touches
  `internal/core/machine/incus_ovn.go`, so the machine-driver-author skill and
  the ownership question (`mustOwn`) both apply.

### Batch 5: lb/v1, ZonedAPI (L)

- Scope: the 54 operations of `lb/v1/ZonedAPI` (LBs, IPs, backends, frontends,
  ACLs, certificates, routes, private networks, `ListLBTypes` as a fixed
  catalogue). Control plane first: `ready` states immediately, the way servers
  behave with no runtime.
- To decline: the 53 operations of `lb/v1/API`, the deprecated regional API the
  provider never calls (measured under `services/lb/`).
- Evidence: a full `terraform apply`: `scaleway_lb_ip`, `scaleway_lb`,
  `scaleway_lb_backend`, `scaleway_lb_frontend`, `scaleway_lb_acl`, attachment
  to a Private Network through `ipam_ip_ids`, empty second plan, destroy; `scw
  lb lb create/get/delete`.
- Dependencies: batch 4 (BookIP and the IPAM ids the attachment needs), vpc.
- Main risk: size (54 operations, the largest batch) and the state machine the
  provider's waiters observe; every object (LB, backend, certificate) has its
  own status and the provider reads all of them.

### Batch 6: vpcgw/v2 (M)

- Scope: the 27 operations of `vpcgw/v2` (gateways, gateway networks, IPs, PAT
  rules, bastion, `ListGatewayTypes` as a fixed catalogue).
- To decline: `vpcgw/v1` wholesale (37, superseded).
- Evidence: `terraform apply` with `scaleway_vpc_public_gateway`,
  `scaleway_vpc_gateway_network` and a PAT rule; under OVN mode, measure whether
  egress NAT can genuinely be backed (the OVN logical router can do it) and
  otherwise declare it degraded, never claim it.
- Dependencies: batches 4 (IPAM) and vpc. Risk: the temptation to promise real
  NAT; the capability rule (section 6) settles it.

Recommended order: 1, 2, 3, 4, 5, 6. Batches 2 and 3 unblock the most clients
for the least cost; 5 is the largest and only makes sense after 4.

## 6. What the architecture has to absorb

This is the part that decides the health of the repository. Four questions,
settled here, to be confirmed before the batches they gate. None of them puts a
provider name into `internal/core` (rule 5), so everything below holds for
Outscale and Exoscale too, which this document does not otherwise cover.

### The per-target lock, before batch 2

*Corrected on 2026-07-30 after checking the code: this paragraph described the
lock as something to build. It exists.* `machine.Binding.Serialise`
(`internal/core/machine/serialise.go:90`) is a lock keyed by `provider|id`, all
three packs take it, and `serverAction` (`servers.go:468`) already does, with the
test that proves it named in its comment:
`TestConcurrentPowerOnStartsTheMachineOnce` fails without that line.

What remains to decide is therefore something finer. First, every new lifecycle
path will have to take that lock and nothing will remind anyone: attaching a
block volume during a poweron, attaching a Private Network to an LB during its
creation, `UpgradeGateway` during a delete. The only guard that holds is a
concurrency test per path, on the model of the one that exists.

Second, the real architectural question: `Serialise` lives on `machine.Binding`,
that is, in the runtime layer, while block and lb are pure control plane with no
machine to drive. The comment at `serialise.go:84-86` already covers the case
("safe with no runtime configured, and packs take it there too"), so nothing is
broken, but a pack taking a *machine* lock to serialise a *volume* write routes
its dependency through the wrong concept. To settle before batch 3: either the
lock moves up into `emulator.Env` with `machine.Binding` re-exposing it, or the
binding is accepted as the serialisation point for every lifecycle and
documented as such. Never a global lock: starting a container takes tens of
seconds and would queue every server behind it.

### References between resources, before batch 3

The current model is the computed fact: a server's `public_ips` is recomputed
from the IP resources (`servers.go:586-605`), a NIC's address from IPAM
(`ipam.go:117`), and a security group refuses deletion by listing its holders.
That model is the right one — a denormalised copy has already lied here, as the
comment on `view` recounts — and it belongs in the packs: a generic relation
graph in the core would be an abstraction with no second consumer. What the core
can offer without compromising itself is nothing beyond `store.List` plus a
filter, which is enough today. The discipline to write into the
provider-pack-author skill: every relation is stored on one side only (the side
that creates it) and computed on the other; block's `references`, a server's
`volumes` and an LB's `ipam_ip_ids` will all follow that pattern. The point to
watch is deletion: every new product must decide what a delete refuses (an
attached volume, an IP booked by an LB), and that is a pack invariant, tested by
conformance (the Terraform fixture destroys everything in reverse order, which
exercises it for free).

### Transient states: stay immediate, and say so

`serverAction` applies transitions immediately and `limits.md:63` owns that. The
provider's waiters (LB, gateway, block `in_use`) read statuses: serving `ready`
immediately satisfies all of them and lies no more than the emulator already
does, since the published state stays the one the effect produced (a failed
start yields the binding's `FailedState`, `internal/core/machine/binding.go:315`).
Decision: no simulated
intermediate states (`starting`, `attaching`) until a measured client depends on
one; `resource.Resource.State` being a free-form string, the day a client
demands it the pack can carry it without touching the core.

### Runtime backing: a declared capability or nothing

`machine.Capabilities` (`capabilities.go:19`) and the `Capable` interface
already exist: machines, addresses, firewall, isolation. The boundary decision:

- Block Storage in batch 3 is pure control plane. Real backing (a custom Incus
  volume attached to the container) is possible later; it will only arrive
  carried by a new capability (`volumes`) declared by the driver, never inferred
  from the mode.
- LB in batch 5 is pure control plane too. Backing a real balancer (HAProxy in a
  runtime machine) is tempting but it is an entire product; the rule that an
  undeclared capability counts as absent protects the conformance suite in the
  meantime.
- vpcgw is the one candidate where OVN backing may be cheap (the logical router
  already exists in incus-ovn mode): to be measured in batch 6, not promised.

### Paging, filters, contract, probe: passable, with two debts

`paging.go` covers the in-house convention (330 of the SDK's 336
`List*Response` types carry `total_count`, the measurement is cited in
`paging.go:8-11`) and reads both spellings, `per_page` and `page_size`: lb and
vpcgw fit inside it. List filters stay per-pack (`filterServers`,
`servers.go:710`) and that is right. Two real debts: the contract only covers
five products (`tools/contract/scaleway-products.txt`), so block, lb/zoned and
vpcgw must be added there, checking the portal slug (the file itself documents
that lb is called `load-balancer/zoned`); and the probe (`internal/probe`)
refuses to invent identifiers, so long dependency chains (a frontend needs an LB
and a backend) will test its planning in batch 5 — if it cannot keep up, the
batch says so and the probe improves, not the other way round.

## 7. What will not be done

- **Object Storage**: measured impossible to redirect cleanly
  ([limits.md](../limits.md)); S3 emulation exists elsewhere, and the project's
  README already says so. A settled decision for as long as the provider builds
  the endpoint by hand.
- **The metadata API** (169.254.42.42): a different surface with a different
  auditor, already declined with a reason (`pack.go:207-217`).
- **Baremetal, Dedibox, Apple Silicon, and the flexibleip that serves them**: an
  emulator whose claim is "the published address really answers" has nothing
  honest to say about hardware. A pure control plane would pass tests and prove
  nothing, which is the definition of the work this project refuses.
- **Managed services** (k8s, rdb, redis, mongodb, kafka…) and **serverless**:
  not the IaaS layer; each is an entire product with its own runtime to feign.
- **IAM beyond SSH keys**: policies, groups, applications. The emulator
  authenticates nothing (`limits.md:72`), so serving authorisation objects would
  be an empty promise. Declined by name in batch 1.
- **instance/v2alpha1**: already declined wholesale; the decision gets revisited
  when the surface settles as v2 (the scan will flag it).
- **VPC connectors and peering**: declined in batch 4 until OVN mode has
  measured peering; this extends the isolation claim, so it deserves its own
  network evidence rather than a ticked box.

## Appendix: reproducing the measurements

```bash
mise run upstream:sync
feint coverage --sdk .upstream/scaleway-sdk-go --products instance,vpc,ipam
feint coverage --sdk .upstream/scaleway-sdk-go \
  --products instance,vpc,ipam,iam,marketplace,block,lb,vpcgw,flexibleip --format json
feint coverage --sdk .upstream/scaleway-sdk-go --products block --format list
mise run drift:check      # 0: nothing moved; 2: triage before planning
git -C .upstream/scaleway-sdk-go log -1 --format='%H %ci'
```

The Terraform provider is measured against a checkout
(`git clone --depth 1 https://github.com/scaleway/terraform-provider-scaleway`),
under `internal/services/<product>`, looking for the SDK request types actually
constructed.
