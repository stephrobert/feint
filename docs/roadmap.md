# Roadmap

What this project intends to do next, why, and how each item will be known to be
done. It is ordered by what unblocks a user, not by what is interesting to build.

What is *measured* rather than planned lives in [limits.md](limits.md), and how
the pieces fit together in [architecture.md](architecture.md).

## How to read this

Every item states its **evidence**: the thing that will be true when it is done,
expressed as something a machine can check. "Terraform applies" is evidence.
"The code supports it" is not: this project's whole claim is that a unit test
proves nothing about a response shape, and a roadmap written in intentions would
be the same mistake in another form.

Percentages of the upstream surface appear in the README and are generated from
the committed coverage artefacts. They are deliberately absent here, because a
roadmap that tracks a percentage optimises for the percentage. The same goes for
counts: where an item below depends on a number, the evidence points at the
generated tables instead of freezing a figure that will rot.

---

## Now, what is being built

### The container image, control-plane only, shipped with the release

This is a decision more than a feature, so here it is in writing: **the image
runs `feint serve` with `--vm off`, and emulates nothing but the control
plane.** The question that held it back, where machines started inside a
container would land, is answered the way the rest of the project already
answers it: the default mode needs no runtime, the conformance suite runs
without one in CI, and `serve` in the foreground is exactly what a container
entrypoint wants. Anyone who needs real machines runs the binary on a host with
Incus, which is the documented path and stays so.

Why now: the image is the format an emulator is consumed in. Every adoption
channel below (testcontainers, a compose file, a `services:` block in GitLab CI)
waits on it, and each week without one is a week a potential user writes a
competitor's name into their compose file instead. What the image must never
become is the nominal mode: the self-detaching static binary is the one thing
none of the comparable emulators can do, and leading with Docker would erase it.

**Evidence:** the release workflow pushes a multi-arch image to ghcr.io, and a
CI job runs the Scaleway conformance suite from the host against the emulator
running inside that image.

### The golden-image workflow on Scaleway: snapshots, images, volume attachment

The most expensive gap in the served surface is not a percentage, it is a
scenario. Anyone who builds machine images with Packer or a `scw` script needs
`CreateSnapshot` and `CreateImage`; any ordinary Terraform module attaches a
`scaleway_instance_volume` to a server and needs `AttachServerVolume` and
`DetachServerVolume`. All of these sit in the untriaged column of the generated
coverage tables today, which means a user hits them on their first realistic
configuration. The work is to implement that family end to end and to decline
the rest of the untriaged `instance` operations with a reason, so the column
empties by decision rather than by accretion.

**Evidence:** a Terraform module declaring a server, an attached volume, a
snapshot of that volume and an image built from the snapshot applies and
destroys in the conformance suite, with no diff on re-plan; and the `instance`
untriaged column in the README's generated tables reads zero.

This item is batches 2 and 3 of
[roadmap-scaleway-iaas.md](roadmap-scaleway-iaas.md), which carries the
operation lists, the measured client behaviour behind them, and the
architecture decisions the work depends on. The batch documents are the
detail; this page only says why this scenario outranks the others.

### Exoscale is labelled preview, and the label has a mechanical exit

The official `exo` CLI drives the pack end to end and a conformance suite proves
it, but a user cannot run a realistic workload against it yet, and the generated
tables say so. The in-between state, served but not honestly usable, is the one
that damages credibility, which is this project's capital. So the decision is
taken rather than allowed to slide: the pack ships marked **preview**, in the
README status table and in the release notes, until the Terraform provider is
proven against it.

**Evidence:** the word *preview* appears next to Exoscale in the README status
table at release, and the commit that removes it is the commit in which
`terraform apply` and `destroy` with the Exoscale provider pass in conformance
with no drift on re-read. The batch that earns that commit is batch 2 of
[roadmap-exoscale-iaas.md](roadmap-exoscale-iaas.md).

---

## The three IaaS layers, one sequence

Each provider now has a measured roadmap for its IaaS layer —
[Scaleway](roadmap-scaleway-iaas.md), [Outscale](roadmap-outscale-iaas.md),
[Exoscale](roadmap-exoscale-iaas.md). Each states what "IaaS" means for that
provider, which official-client command breaks on each missing product today,
and the batches that close it. This section does not repeat any of that — a
figure copied into two files diverges — it orders the batches of all three into
one sequence and names the work that cuts across them.

### What "acceptable coverage" means, and does not

Not a percentage. A percentage of the upstream surface rewards serving a
hundred easy reads over the one write a user's first `terraform apply` dies on,
and the generated tables in the README already carry the percentages for
whoever wants them. Coverage is acceptable for a provider when three sentences
are true, each checkable by a machine:

1. **The official CLI runs the machine lifecycle end to end** — create, list,
   get, stop, start, delete, with an SSH key registered and an address
   published — against the emulator, in the conformance suite.
2. **A realistic Terraform configuration applies, re-plans empty, and destroys
   cleanly**, with contracts on. "Realistic" is not chosen here: for Outscale
   it is the provider's own `examples/net_vm`; for Scaleway the golden-image
   module above; for Exoscale the ordinary instance stack (key, security
   group, anti-affinity, elastic IP, instance).
3. **The untriaged column reads zero for the products that provider's roadmap
   declares in scope** — zero by decision, served or declined with a reason,
   never by a widened denominator.

The third sentence is what keeps the first two honest over time: a scenario
proven once stays proven only because the gate fails when the surface under it
moves.

### The work that cuts across all three packs

Named here because each item looks local in any single roadmap and is not:

- **`Declined()` grows a reason**: the interface becomes `(operation, reason)`
  so the report can say *why* an operation is refused. One change, three
  packs, and it must land before the Exoscale triage writes ~250 refusals
  against the old signature. Proposed in the Scaleway roadmap (batch 1),
  justified by the Exoscale one (batch 1).
- **Two Terraform fixtures do not exist yet.** Scaleway has
  `tools/conformance/scaleway/terraform/`; Outscale and Exoscale have no
  Terraform evidence at all, which is the single biggest gap between "the CLI
  passes" and "usable". Each fixture arrives with its `terraform.sh` and mise
  task, on the Scaleway model.
- **The contract is extended with every product, never after it.** New
  products enter `tools/contract/` extraction (for Scaleway,
  `scaleway-products.txt`) in the same change as their routes. Outscale and
  Exoscale make this order mandatory: their `additionalProperties: false`
  contracts refuse an unextracted product's responses outright.
- **Every new lifecycle path takes the per-target lock** —
  `machine.Binding.Serialise`, which exists and is taken by all three packs —
  and proves it with a concurrency test, the way
  `TestConcurrentPowerOnStartsTheMachineOnce` does. Nothing will remind anyone
  of this; only the test does.
- **Runtime backing arrives only as a declared capability.** Block storage,
  load balancers and gateways ship as control plane first; a driver that gains
  real backing declares it (`machine.Capabilities`), and an undeclared
  capability counts as absent.

### The sequence

Ordered by what unblocks a user, which is the same criterion as the rest of
this page. Batch numbers refer to the per-provider documents.

1. **The triage wave** — Scaleway batch 1, Outscale batch 1, Exoscale batch 1,
   carrying the `(operation, reason)` change. No new route, and it is first
   anyway: it turns three unreadable untriaged columns into work lists, puts
   iam and marketplace under the gate they currently escape, and makes every
   later batch's "the column reads zero" evidence possible.
   **Evidence:** `tools/drift/gate.sh check` returns 0 against the three new
   baselines, and the README's generated tables show untriaged columns that
   are work lists, not walls.
2. **First Terraform proof for Outscale and Exoscale** — Outscale batch 2,
   Exoscale batch 2. The cheapest work with the largest status change: both
   packs already pass their CLI suites, and each needs a handful of
   operations (a catalogue field and tags on one side, the machine lifecycle
   on the other) plus the missing fixture. This is the wave that removes
   Exoscale's *preview* label, on the condition already stated above.
   **Evidence:** `terraform apply`, empty second plan, clean destroy, in
   conformance, for both providers.
3. **The Scaleway golden-image scenario** — Scaleway batches 2 and 3, the
   "Now" item above. It stays third rather than first only because Scaleway
   already serves a usable core while the other two waves each take a
   provider from "demo" to "provable"; it remains the most expensive known
   gap for the provider with the most users.
   **Evidence:** as stated in the "Now" item.
4. **Networks that route** — Outscale batch 3 (`examples/net_vm` applies),
   Scaleway batch 4 (IPAM lifecycle and vpc completed), Exoscale batch 3
   (private networks). Grouped because they share the network-evidence rule:
   under OVN the claim is asserted, elsewhere it is skipped, and no document
   says "isolated" without naming the mode.
   **Evidence:** each batch's own conformance run, per its document.
5. **Storage on the two starters** — Outscale batch 4, Exoscale batch 4,
   aligned with the volume relations Scaleway batch 3 already settled: every
   relation stored on one side, computed on the other, deletion rules tested
   by the fixture's destroy.
   **Evidence:** the storage applies named in each document.
6. **Load balancing and gateways** — Scaleway batches 5 and 6, Outscale
   batch 5, Exoscale batches 5 and 6. Last because nothing else depends on
   them and everything they depend on (IPAM, networks, the waiter discipline)
   is above; each is control plane first, capability-gated backing later.
   **Evidence:** the applies named in each document, waiters converging
   without timeouts.

The waves are an order, not a schedule: a wave can start before the previous
one is fully green when its dependencies are, and an issue where an official
client breaks outranks the whole list, as the last section says.

### The operational view

The waves above say why. This table says what to open. One line per batch, with
the identifier used from here on, what it delivers, where the work lands, and
the one command that ends it. Each line is an issue, and the issues are steered
from the [project board](project.md). Operation lists and the measurements behind them
stay in the per-provider documents: a count copied here would diverge from the
scan the day upstream moves.

| ID | Wave | Delivers | Where the work lands | Ends when | After |
|---|--:|---|---|---|---|
| **X-1** | 1 | `Declined()` carries a reason per operation | `internal/core/emulator/emulator.go:98`, the three packs, `internal/drift/report.go` | `mise run check` passes and the report prints a reason | — |
| **SW-1** | 1 | iam and marketplace under the gate; instance/vpc/ipam triaged | `tools/drift/gate.sh:31`, `internal/providers/scaleway/pack.go` | `tools/drift/gate.sh check` → 0 on the new baseline | X-1 |
| **OSC-1** | 1 | the non-IaaS half declined by name | `internal/providers/outscale/declined.go` | same gate, untriaged column is a work list | X-1 |
| **EXO-1** | 1 | the managed-service surface declined by name | `internal/providers/exoscale/pack.go` (`Declined()` today returns `nil`) | same gate | X-1 |
| **OSC-2** | 2 | `ProductCodes`, admin password, tags, root volume | `catalog.go`, `vms.go`, new `volumes.go`; new `tools/conformance/outscale/terraform/` | `terraform apply` + empty plan + destroy, in conformance | OSC-1 |
| **EXO-2** | 2 | instance lifecycle, security groups, elastic IPs | `machines.go`, new files; new `tools/conformance/exoscale/terraform/` | same, and the *preview* label comes off | EXO-1 |
| **SW-2** | 3 | snapshots, images, placement groups, volume attach | new files beside `volumes.go`; `tools/conformance/scaleway/terraform/main.tf` | the golden-image module applies and destroys | SW-1 |
| **SW-3** | 3 | `block/v1` and the `sbs_volume` root volume | new `block.go`, `catalog.go`, `tools/contract/scaleway-products.txt` | `terraform apply` with `scaleway_block_volume` | SW-2 |
| **OSC-3** | 4 | routable networking: security groups, public IPs, NICs, route tables | new files in `internal/providers/outscale/`; fix `tools/conformance/outscale/oapi-cli.sh:268` | the provider's own `examples/net_vm` applies | OSC-2 |
| **SW-4** | 4 | IPAM lifecycle and the rest of vpc | `ipam.go`, `vpc.go`, `internal/core/machine/incus_ovn.go` | `scaleway_ipam_ip` applies; under OVN the ACL filters | SW-1 |
| **EXO-3** | 4 | private networks and instance attachment | new files; a `network.sh` for Exoscale | apply, plus the OVN assertion when the mode declares it | EXO-2 |
| **OSC-4** | 5 | volumes, snapshots, images | `internal/providers/outscale/` | the storage apply named in its document | OSC-2 |
| **EXO-4** | 5 | block storage | new `block.go` | volume + snapshot + attachment apply | EXO-2 |
| **SW-5** | 6 | `lb/v1` ZonedAPI | new `lb.go`, contract extraction | the full LB stack applies, waiters converge | SW-4 |
| **SW-6** | 6 | `vpcgw/v2` | new `vpcgw.go` | gateway + gateway network + PAT rule apply | SW-4 |
| **OSC-5** | 6 | load balancing | new `lb.go` | the LB apply named in its document | OSC-3 |
| **EXO-5** | 6 | NLB | new `nlb.go` | `exoscale_nlb` + service apply | EXO-3 |
| **EXO-6** | 6 | VPC and routes | new `vpc.go` | the VPC apply, backing declared or declared degraded | EXO-3 |

Sizes are in the per-provider documents, where they can be argued against the
operation lists that justify them. The only ones worth knowing here: SW-5 is the
largest single batch, and X-1 is the smallest with the widest blast radius,
which is why it is first.

### Start here

Three commands, in this order, before writing any code:

```bash
mise run upstream:sync                 # the scan reads a current checkout or nothing
mise run drift:check                   # 0: the baselines are current; 2: triage before planning
feint coverage --sdk .upstream/scaleway-sdk-go --products instance,vpc,ipam --format triage
```

The third one prints the work list SW-1 empties. Its Outscale and Exoscale
equivalents are in the appendices of their documents. If `drift:check` exits 2,
that triage comes before anything on this page: a baseline nobody has ruled on
makes every "the column reads zero" claim below meaningless.

### When a batch is done

The same four conditions everywhere, in the order they fail fastest:

1. `mise run check` passes: gofmt, vet, golangci-lint, `go test -race`.
2. Every new route declares its upstream `Route.Operation`, and
   `TestEveryRouteDeclaresAnOperation` proves it.
3. `mise run conformance` passes, including the batch's own new evidence. A unit
   test alone closes nothing: only a real client driving the route does.
4. `tools/drift/gate.sh check` returns 0, with the batch's operations served or
   declined **with a reason**, never left untriaged.

A batch that satisfies 1, 2 and 4 but not 3 is not done; it is a shape nobody
has shown to a real client.

---

## Next, what a user will ask for first

### What "probed" and "refused" prove gets tightened

The request side now has teeth: a field a client sends that no handler reads
fails the conformance run, which is the mechanism that caught a server retype
answering 200 while doing nothing. Two gaps remain, both measured. A probe
answered with a 4xx counts as *refused* and its error body is never validated
against the contract, so a wrong error shape hides behind a right status code;
and request parameters are not contractualised, which is exactly where an
ignored page-size parameter slipped through until a real client noticed. A
"probed" that proves little is worse than an honest gap, because it reads like
evidence.

**Evidence:** a refused probe's error body is validated against the provider's
own error schema and a violation fails `mise run conformance`; and the list-route
probes vary the page size and assert the page they got.

### IAM under the drift gate

IAM routes are mounted against a surface the coverage gate does not scan, which
is the least defensible state a route can be in: served, and unmeasured. The
README admits it in the generated tables; admitting it is not fixing it. The
practical pull is real too: API keys are an ordinary Terraform target, if only
to bootstrap a CI runner against the emulator.

**Evidence:** `iam` appears as a scanned product in the generated coverage
tables, and an upstream IAM operation nobody has triaged makes `drift:check`
exit 2 like any other product's would.

### A `setup-feint` GitHub Action and a GitLab CI template

The lifecycle verbs were designed for CI (`start`, `wait`, `env`, stable exit
codes), so the action is a thin composite, not a project. It is listed after the
image because the GitLab `services:` path consumes the image, and because an
action that exists before the scenario above works would install a tool that
fails on the first realistic module.

**Evidence:** an example repository's CI, on GitHub and on GitLab, goes from
checkout to a passing `terraform apply` against the emulator using only the
published action or template.

### A testcontainers-go module

This is how an emulator enters other people's test suites. It lives in a
separate repository, because the module must depend on testcontainers while this
repository's zero-dependency `go.mod` is enforced by a pre-commit hook, and Go
comes first because it is the language of all three providers' SDKs. Java and
Python follow the same pattern only after the Go module has users.

**Evidence:** a `go test` in the module's repository starts the published image,
points the official Scaleway SDK at it, and creates and deletes a server.

### Conformance suites split per resource

Today one script per provider drives everything that provider serves. That makes
a suite grow without bound and, more practically, makes it impossible to run two
of them in parallel without them fighting over the same emulated account. The
surface cannot keep growing if the suite that proves it becomes the bottleneck.

**Evidence:** the suites run concurrently against one emulator and pass.

---

## Later, decided, not scheduled

### `--vm` gets proven by CI, on a runner nobody owns

Today **no workflow in this repository starts a machine runtime**: zero
`FEINT_VM`, zero `incus`, and the `network.sh` suites are never invoked in CI.
The mode that carries the product's argument — real machines, real addresses,
two VPCs that cannot reach each other — is proven only by the eight virtual
machines in [install.md](install.md) and on the author's station. That is a
measurement nobody else can reproduce by opening a pull request, which is the
definition of evidence this project refuses everywhere else.

It stays *later* rather than *now* because the whole thing rests on one unproven
combination, and the two halves under it are measured (2026-07-30, on the
upstream projects' own CI):

- **Incus runs on a GitHub-hosted `ubuntu-24.04`.** `lxc/incus` drives its own
  `test/main.sh` there with real containers on zfs, btrfs, lvm and ceph.
- **OVN with the kernel datapath runs there too.** `ovn-org/ovn` runs
  `system-test` — the variant that loads `openvswitch.ko`, not the userspace one
  — on the same runner, after `apt install linux-modules-extra-$(uname -r)` and a
  hosts-file fix from its `.ci/linux-util.sh`.
- **Nobody runs the two together.** The Incus CI contains no occurrence of
  `ovn`, and the repository has no OVN test suite at all.

One trap and one unknown. The trap is **AppArmor**: that same `.ci/linux-util.sh`
runs `aa-teardown` and disables the service, which reads like a prerequisite and
is not — it cites an Ubuntu AppArmor bug and works around it for binaries built
from source outside any packaged profile. A packaged install needs none of it,
measured: AppArmor loaded with 180 profiles, four of them Incus's own, and the
network suite passing. [install.md](install.md) now says so, because a job that
copies the upstream recipe wholesale would disable a mandatory access control
system on every runner and call it setup. The unknown is **arm64**: neither
upstream project exercises it on a hosted runner, so a green run there would be
this repository's first arm64 evidence of any kind, not merely of `--vm`.

The order is measure, then gate. The job lands behind `workflow_dispatch`, is
run by hand, and moves onto `pull_request` only once it is green. A gate that is
red on the day it appears is a gate everyone learns to ignore, and this
repository already carries the note about what that costs.

**Evidence:** a CI job installs Incus from Zabbly plus OVN on a hosted runner,
wires the northbound connection, and `FEINT_VM=incus-ovn` runs the network suite
to completion — the subnet created through the emulated API, the address the API
published answering, and the isolation assertion passing rather than skipping.
Until then the install page says `--vm` is proven by the release table and never
by CI, and that sentence is generated, so it cannot quietly stop being true.

### Outscale reaches parity with Scaleway on the IaaS core

Nobody else emulates Outscale, and its buyers (public sector, SecNumCloud) read
proofs rather than marketing, which this repository is full of. The addressing
plane already landed: Net, Subnet, mask bounds, containment, overlap, a real
bridge on the host, a Vm carrying the address the API published. Today
`SecurityGroupIds` on a create is refused with a 400 rather than silently
dropped, which is the honest placeholder.

The batches that close the gap — networking, storage, load balancing, each with
its conformance evidence — are in
[roadmap-outscale-iaas.md](roadmap-outscale-iaas.md), sequenced with the other
two providers in the section above. One warning belongs here because it is an
architecture decision rather than pack work: a rule sourced by *group* rather
than by CIDR needs an OVN selector, not a static rule set, and that network-model
question must be answered before the security-group batch promises enforcement.

**Evidence:** the provider's own `examples/net_vm` configuration applies,
re-plans empty and destroys against the emulator, which is the parity bar that
document sets.

### Object storage stays out, and the workaround gets a page

The reason is stated and measured in [limits.md](limits.md): the Scaleway
Terraform provider hardcodes `https://s3.<region>.scw.cloud`, so supporting it
needs DNS interception and TLS termination rather than an endpoint setting. That
decision stands. What changes is that the "no" gets a "here is how": the SDK and
CLI paths honour `SCW_S3_ENDPOINT`, so a documented feint-plus-MinIO page covers
the S3 workflow for everything except the Terraform path, which the page says
plainly.

**Evidence:** the page's commands are executed in CI the way the README's are:
`scw` pointed at MinIO through `SCW_S3_ENDPOINT` puts and gets an object.

### Snapshot compatibility across versions

`feint snapshot` shipped, so the question is no longer hypothetical: must a
snapshot taken by one version load into the next? The answer has to be chosen
before somebody depends on it by accident. Either answer is acceptable; an
undocumented one is not. The adjacent case is already known and stated nowhere
useful: loading a snapshot while a machine runtime runs replaces the store
without reconciling the real machines, and the decision here must say what
happens then.

**Evidence:** CI loads a snapshot written by the previous release into the
current binary and the store answers, or the load is refused with an error that
names both versions; whichever it is, [limits.md](limits.md) says so.

### A fourth provider

The architecture was built so that adding one changes nothing in
`internal/core`. That claim is untested: three packs is not enough to know
whether the seams are in the right place. The fourth is chosen by demand, not by
intuition, and not before Exoscale has lost its preview label (see Not planned).

**Evidence:** a new pack is added without a single line changing under
`internal/core`.

### Packages: Homebrew, nixpkgs, AUR

Low urgency by design: `go install` works today and a released binary needs
nothing. Channels are added as they are asked for, and none is announced on
faith.

**Evidence:** an install channel appears in the README only after a CI job has
installed from it and driven `scw instance server create` against the result.

---

## Not planned, and why

Saying this out loud is the same discipline as `Declined()` in the packs: "not
triaged" and "out of scope" are different answers, and only the second belongs
here.

- **American clouds.** The European gap is the moat: LocalStack exists for AWS
  and Azurite for Azure, and nothing existed for these three. AWS, GCP or Azure
  would also drown the drift mechanism under thousands of operations and make
  the claim "measured, not followed" untenable. Both reasons are structural, so
  this is not a matter of demand.
- **A race on service count.** Databases, Kubernetes control planes, serverless:
  each is a product in its own right, and doing one badly costs more credibility
  than not doing it at all. Ten façade services would destroy the one thing that
  distinguishes this project, a data plane that keeps its promises.
- **Any external Go dependency.** A three-line `go.mod` is a security argument
  for a tool that will run inside everyone's CI, and a pre-commit hook enforces
  it. Anything that needs a dependency (the testcontainers module above) lives
  in its own repository.
- **Telemetry, or an account. Ever.** "No account, no bill" is in the first line
  of the README and it is load-bearing.
- **A fourth provider before the third is usable.** Otherwise the result is
  three half-empty shopfronts instead of two full ones.
- **The container image as the nominal mode.** Self-detachment is the only point
  where Feint beats all three comparable emulators at once; a JVM or CPython
  process cannot daemonise cleanly, a static Go binary can. The image exists so
  the emulator can enter other people's tooling, not to replace the binary.
- **Checking that an identifier exists.** A create naming an image the emulator
  never heard of succeeds, on all three packs, where the real clouds refuse. This
  is the limitation most likely to bite and it is deliberate, argued in
  [limits.md](limits.md): the emulator has no inventory, and a team pointing an
  existing Terraform configuration at it must not fail on a hardcoded production
  image UUID, the one thing that has nothing to do with what they are testing.
  The revisit condition and the file to change are named there; any change must
  keep hardcoded production ids working.
- **Emulating a provider's console or web UI.** The audience drives APIs.
- **Billing, quotas or capacity.** An emulator has no capacity to report. Where
  a number is required by a schema it is plausible and fixed, and
  [limits.md](limits.md) says so rather than pretending.
- **A Docker machine runtime.** Retired deliberately: emulating a cloud means
  emulating its network, which needs a bridge on a chosen block, a fixed address
  per interface from boot, and rules actually enforced. Incus provides all
  three; Docker provides one and a half. The measurements are in
  [limits.md](limits.md). Reintroducing it means answering that first.
- **Verifying signatures.** Every credential is accepted on purpose, so the tool
  runs without an account. [SECURITY.md](../SECURITY.md) states the consequence.

---

## What would change this roadmap

An issue that says "this official client does something the emulator cannot
follow" outranks everything above. The order here is a guess about what people
need; a client that breaks is a fact about it.
