# Covering the Outscale IaaS layer, end to end

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
surface scan run that day against the `.upstream/osc-sdk-go` checkout, and from
a checkout of `outscale/terraform-provider-outscale` taken the same day
(master). Every figure copied here rots with upstream: the source of truth stays
`mise run drift:check` and `feint coverage --provider outscale --sdk
.upstream/osc-sdk-go`.

Same method and same rules as
[roadmap-2026-07-30-scaleway-iaas.md](roadmap-2026-07-30-scaleway-iaas.md), which carries the
architecture decisions common to all three packs. This document does not repeat
them; it says what Outscale adds. The French working notes are in
`notes/roadmap-outscale-iaas.md`, kept outside this repository.

## Summary

The pack serves 20 routes (VMs, catalogue, Net/Subnet, keypairs) and
`oapi-cli.sh` passes end to end, contract included. Of the 199 untriaged
operations, roughly half are IaaS (97) and half are not (102: IAM, DirectLink,
VPN, flexible GPUs, dedicated groups). What breaks first is not a missing
product, it is a missing field: the emulated catalogue publishes no
`ProductCodes`, which makes the Terraform provider call `ReadAdminPassword` on
every VM it reads back, and that route does not exist. Outscale has no Terraform
evidence at all today, which is the real gap against Scaleway. Five batches are
proposed, in order: decline the 102 out-of-scope operations, make a minimal
`terraform apply` pass, routable networking, storage, load balancing.

## 1. State of play, measured

### What the pack serves

`internal/providers/outscale/pack.go:70-105` mounts 20 routes, each with its
`Route.Operation` (`osc/Client.<Action>`):

| Area | Actions | File |
|---|--:|---|
| VMs and lifecycle | 7 | `vms.go` |
| Catalogue (types, images, regions, subregions) | 4 | `catalog.go` |
| Nets and Subnets | 6 | `nets.go` |
| Keypairs | 3 | `keypairs.go` |

### What the measurement says

```text
provider outscale: 262 operations upstream
  oks                        0 implemented   27 declined    0 unknown  (of 27)
  osc                       20 implemented   16 declined  199 unknown  (of 235)
  TOTAL                     20 implemented   43 declined  199 unknown
```

Unlike Scaleway, the surface is small and single-version: no v2alpha1 inflates
the numbers, so 199 untriaged means 199 decisions genuinely waiting.

### The 199 untriaged, classified

Grouped by resource family, rule by rule, most specific first
(`LoadBalancerTag` is load balancing, not tagging). The script is throwaway, the
command feeding it is not: `feint coverage --provider outscale --sdk
.upstream/osc-sdk-go --format list`.

| Family | Operations | IaaS | Contents |
|---|--:|---|---|
| IAM | 56 | no | User, UserGroup, Policy, PolicyVersion, AccessKey, ApiAccessRule, Ca, OutscaleLogin |
| Networking | 52 | yes | Nic, PublicIp, SecurityGroup(+Rule), RouteTable, Route, InternetService, NatService, NetPeering, DhcpOption, NetAccessPoint, PrivateIp |
| Hardware and sundry | 25 | no | FlexibleGpu, DedicatedGroup, ProductType, VmTemplate, VmGroup, Catalog, Location, CO2Emission |
| Load balancing | 23 | yes | LoadBalancer, Listener(+Rule), Policy, Tags, VmsInLoadBalancer, ServerCertificate |
| Carrier connectivity | 21 | no | DirectLink, DirectLinkInterface, VpnConnection, ClientGateway, VirtualGateway |
| Storage | 14 | yes | Volume, Snapshot, Image |
| Machine | 8 | yes | Tags, AdminPassword, ConsoleOutput, VmsState, Authentication |

**97 in, 102 out.** That is the most useful fact in this document: more than half
the triage work is writing refusals, not code.

### What is already refused, and why

`declined.go` declines 43 operations, and the distinction is already clean
there: real inventory and billing (accounts, consumption, prices, quotas, API
log), the price catalogues, the export tasks that write into Object Storage, and
the 27 OKS operations listed one by one so upstream additions stay visible. The
comment at `declined.go:26-28` says exactly the right thing about `ReadVmTypes`:
the catalogue of shapes stays to be served, the catalogue of prices does not.

## 2. What clients break on today

Outscale has no Terraform fixture: `tools/conformance/outscale/` holds only
`oapi-cli.sh` and `network.sh`. The CLI passes; Terraform has never been run.
The points below are therefore measured against provider source, not against an
execution.

1. **`ProductCodes` missing from the catalogue, which is the catalogue trap in
   another form.** `resource_outscale_vm.go:1016-1020` decides whether to fetch
   the admin password like this:

   ```go
   // Do not fetch admin password for non-Windows VMs
   if lo.Some(vm.ProductCodes, []string{"0001", "0004", "0006"}) {
       return "", nil
   }
   resp, err := client.ReadAdminPassword(ctx, ...)
   ```

   The emulated catalogue images (`catalog.go:49-51`) carry no `ProductCodes` at
   all. `lo.Some` over an empty collection is false, so the provider calls
   `ReadAdminPassword` on **every** VM it reads back, Linux included, and the
   route is not mounted. Same family as "declining the catalogue breaks the
   CLI": no operation is missing, an absent field sends the client down the
   wrong branch. Two fixes, and both are needed: publish a Linux `ProductCodes`
   on images and VMs, and serve `ReadAdminPassword` (which must then return an
   empty password rather than invent one).
2. **`UpdateVolume` is called by the VM resource itself** (measured in the same
   file), for the root volume. Storage is therefore not optional the moment a
   clean `terraform apply` is the goal.
3. **The provider's own canonical example says what a realistic configuration
   uses.** `examples/net_vm/` carries thirteen resources: `outscale_net`,
   `outscale_subnet`, `outscale_internet_service`,
   `outscale_internet_service_link`, `outscale_route_table`, `outscale_route`,
   `outscale_route_table_link`, `outscale_security_group`,
   `outscale_security_group_rule`, `outscale_public_ip`,
   `outscale_public_ip_link`, `outscale_keypair`, `outscale_vm`. Four of those
   thirteen are served (`outscale_net`, `outscale_subnet`, `outscale_keypair`,
   `outscale_vm`). That is the target of batch 3.
4. **`ReadQuotas` and `ReadAccounts` are declined, and that refusal holds.**
   Measured: the provider only calls them from *data sources*
   (`data_source_outscale_quota.go`, `data_source_outscale_account.go`), never
   from a resource. The refusal breaks no `apply`, only a configuration that
   explicitly reads its quotas. Keep them declined, with that measurement in the
   comment, which `declined.go` does not yet carry.

### The asset not to lose

The contract. `contracts/outscale.json` is extracted from Outscale's own OpenAPI
document, in which **643 of 650 schemas declare `additionalProperties: false`**
(`pack.go:12-13`). Every served response is therefore checked against the
official description during conformance, and `oapi-cli.sh:310-315` reads the
verdict. It is the strongest guarantee of the three packs, and it applies for
free to everything the batches below add. Corollary: each new product served
must be added to the contract extraction at the same time as its routes, never
afterwards.

### The check that will turn against us

`oapi-cli.sh:268` picks an unserved operation among `ReadNics`, `ReadVolumes`,
`ReadSecurityGroups`, `ReadPublicIps` and `ReadRouteTables` to verify the 404 is
decodable. All five are in batches 3 and 4. The script anticipates this and
fails with "add a still-unserved action to the list above". Handle it inside the
batch, not as a CI surprise.

## 3. What "the IaaS layer, end to end" means here

The same boundary as Scaleway, applied to Outscale's vocabulary: something is
IaaS when a client provisions it as generic infrastructure and the emulator can
serve it honestly.

| Product | Ops | Status | Reason |
|---|--:|---|---|
| Vm and lifecycle | 7 served + 8 | In | The core, already served; tags, state, console and password remain |
| Net, Subnet | 6 served | In, served | The addressing arithmetic is already proven by `oapi-cli.sh:83-141` |
| Nic, PublicIp, PrivateIp | ~13 | In | Any realistic VM carries them |
| SecurityGroup and rules | 5 | In | On the path of the provider's own canonical example |
| RouteTable, Route, InternetService, NatService | ~16 | In | What makes a Net routable; without them the emulated network is a dead end |
| Volume, Snapshot, Image | 14 | In | `UpdateVolume` is already called by the VM resource |
| LoadBalancer and satellites | 23 | In | Fully IaaS, but after the rest |
| NetPeering, NetAccessPoint, DhcpOption | ~10 | Later | In by nature, out of the early batches; peering follows the same network-evidence rule as Scaleway |
| IAM (User, Policy, AccessKey, Ca…) | 56 | Out | The emulator authenticates nothing; serving authorisation objects would be an empty promise |
| DirectLink, VPN, gateways | 21 | Out | Carrier connectivity: nothing to back, nothing to prove |
| FlexibleGpu, DedicatedGroup, VmTemplate, VmGroup | 25 | Out | Dedicated hardware and proprietary orchestration, same reason as Scaleway baremetal |
| OKS | 27 | Already declined | Managed service |
| Accounts, prices, quotas, exports | 16 | Already declined | Real inventory |

## 4. The roadmap, by batch

### Batch 1: decline the 102 that are not IaaS (S)

- Scope: add the IAM (56), carrier connectivity (21) and hardware-and-sundry
  (25) families to `declined.go`, by name, with a reason per block the way the
  file already does for OKS.
- Add the measurement from point 2.4 as a comment: `ReadQuotas` and
  `ReadAccounts` are only read by data sources, so the refusal breaks no
  `apply`. A refusal carrying its measurement is a refusal that can be revisited.
- Evidence: `gate.sh check` returns 0 after `mise run drift:update`; the
  untriaged column drops from 199 to 97 and becomes a work list rather than a
  wall.
- Risk: none. This is the batch that makes the others legible.

### Batch 2: Outscale's first `terraform apply` (S, high value)

- Scope: `ProductCodes` published by the catalogue and by a VM view;
  `ReadAdminPassword` served (empty response, never an invented password);
  `UpdateVolume` and `ReadVolumes` served at least for the root volume;
  `CreateTags`, `ReadTags`, `DeleteTags` (the provider calls them on almost every
  resource); `ReadVmsState`.
- Write `tools/conformance/outscale/terraform/main.tf` and
  `tools/conformance/outscale/terraform.sh` on the model of the Scaleway suite,
  plus a `conformance:terraform:outscale` task in `mise.toml`.
- Evidence: `terraform apply` of an `outscale_keypair` and an `outscale_vm`,
  empty second plan, clean destroy. This is Outscale's first Terraform evidence,
  and this batch is what creates it.
- Dependencies: none. Risk: the contract will refuse any response carrying a
  field their document does not declare, which is the intended behaviour and
  will fail the first attempts.

### Batch 3: routable networking (M)

- Scope: `SecurityGroup` and `SecurityGroupRule` (5), `PublicIp` and its link
  (5), `Nic` and its links (6+2), `RouteTable`, `Route` and the links (~10),
  `InternetService` and its link (5), `NatService` (3).
- Evidence: `terraform apply` of the provider's own `examples/net_vm/`, adapted
  to the emulated catalogue's identifiers, empty second plan, destroy. Evidence
  the provider supplies itself beats a fixture written here to prove a point.
- Fix `oapi-cli.sh:268` in the same batch: all five candidates become served.
- Dependencies: batch 2. Risk: the security group must genuinely filter under
  `FEINT_VM=incus`, or `network.sh` will contradict what the API publishes. The
  shared layer already does this for Scaleway; it is wiring, not a rewrite.

### Batch 4: storage (M)

- Scope: `Volume` (6), `Snapshot` (4), `Image` (3, alongside the already-served
  `ReadImages`).
- To decline: the export tasks, already refused, do not move.
- Evidence: `terraform apply` with `outscale_volume`, `outscale_volumes_link`,
  `outscale_snapshot` and an image created from a snapshot; `oapi-cli` over the
  same path.
- Dependencies: batch 2, for attachment consistency. Risk: the bidirectional
  volume/VM relation, identical to Scaleway. The computed-fact model applies
  as is.

### Batch 5: load balancing (L)

- Scope: the 23 operations of the LoadBalancer family, `ServerCertificate`
  included. Pure control plane, immediate states.
- Evidence: `terraform apply` with `outscale_load_balancer`,
  `outscale_load_balancer_listener_rule`, `outscale_load_balancer_vms`, empty
  second plan.
- Dependencies: batches 2 and 3. Risk: size, and a strict contract over rich
  responses.

Recommended order: 1, 2, 3, 4, 5. Batch 2 is small and changes the pack's status
in the README ("starter" becomes defensible by something other than the CLI).

## 5. What Outscale adds to the architecture

Nothing that contradicts the four decisions in the Scaleway roadmap; two points
specific to this pack.

**The per-target lock is already here, and already taken correctly.** Checked
rather than assumed: `machine.Binding.Serialise`
(`internal/core/machine/serialise.go:90`) is taken at `vms.go:333` and, in
`DeleteVms`, **inside** the loop over identifiers (`vms.go:428`, in an anonymous
function so the `defer` releases on every pass). That is exactly the right shape,
since `StartVms`/`StopVms`/`DeleteVms` accept a list: a lock taken around the
loop would make ten targets wait on one slow effect. The pack carries no debt
here; what matters is that batches 3 to 5 keep taking that lock on every new
destructive path, and prove it with a concurrency test the way
`TestConcurrentPowerOnStartsTheMachineOnce` already does on the Scaleway side.

The `addresses` mutex (`pack.go:39-43`) stays pack-global, and rightly so: it
guards no single target but the choice of a free block, which is computed over
the whole set.

**The contract dictates the order of work inside a batch.** On Scaleway, an
approximate shape passes conformance until a client complains. Here,
`additionalProperties: false` refuses it immediately. That is an advantage,
provided the product's contract is extracted **before** its handlers are
written: `tools/contract/update.sh` first, routes second.

## 6. What will not be done

- **Outscale IAM** (56 operations): the emulator authenticates nothing, it
  accepts any v4 signature without checking it. Serving users, policies and
  access keys would produce objects with no effect, which is a structural lie.
- **DirectLink, VPNs and gateways** (21): connectivity towards a carrier or a
  remote site. Nothing to back, nothing to prove, and a pure control plane would
  pass tests while demonstrating nothing.
- **FlexibleGpu, DedicatedGroup, VmTemplate, VmGroup** (25): dedicated hardware
  and proprietary orchestration, same reason as Scaleway baremetal.
- **OKS**: already declined, one decision and twenty-seven operations.
- **Object Storage and the export tasks**: already declined; the measurement is
  in [limits.md](../limits.md) and it is not about Outscale, it is about S3.

## Appendix: reproducing the measurements

```bash
mise run upstream:sync
feint coverage --provider outscale --sdk .upstream/osc-sdk-go
feint coverage --provider outscale --sdk .upstream/osc-sdk-go --format triage
feint coverage --provider outscale --sdk .upstream/osc-sdk-go --format list
mise run drift:check
```

The Terraform provider is measured against a checkout
(`git clone --depth 1 https://github.com/outscale/terraform-provider-outscale`),
under `internal/services/oapi/` for the SDK calls and `examples/` for the
configurations the provider itself treats as canonical.
