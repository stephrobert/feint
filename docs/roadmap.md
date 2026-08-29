# Roadmap

**Read this in another language:** [Français](./roadmap.fr.md)

What this project intends to do next, why, and how each item will be known to be
done. It is ordered by what unblocks a user, not by what is interesting to build.

What is *measured* rather than planned lives in [limits.md](limits.md), and how
the pieces fit together in [architecture.md](architecture.md).


## The order 0.11.0 is being worked in

Frozen 2026-08-22. Thirteen issues remain, and they are not independent: two of
them move the numbers the other eleven quote. The order below is the dependency,
not a preference.

**1 — Make the measurement trustworthy. Blocking.**

- **#398** — `behaviour` is not reproducible: 313 and 314 on two identical runs.
  Cause located in `soleClientFlightLocked`, which discards the attribution when
  two client requests are in flight, under a span covering the whole lifecycle
  of a Terraform running at `-parallelism=10`.
- **#406** — three published axis percentages are wrong, and nothing refuses a
  measured number written by hand outside a generated block.

*Why first:* the seven parity issues quote figures these two will move. Working
them in the other order means aiming at a moving target and republishing wrong
numbers. Both are short.

**2 — Answer the question that can cancel most of the remaining work.**

- **#407** — `shape` is the weakest axis by a wide margin, and **292 of its 318
  zeros are `unrecorded`**: operations a real client already drives, whose real
  answer was never kept. This repository already holds **619 recorded exchanges** in
  `corpus/`, and they do not feed `shapes/`. Whether they can is answerable
  **offline, without an account, in one sitting** — and if they can, most of the
  parity volume disappears without touching a cloud.

*Why here:* going back to three accounts for answers already committed would be
the worst possible order.

**3 — The image catalogue chain.**

- **#389**, then **#383**, then **#378** — one model: catalogue → a snapshot the
  store really holds → root BSU volume → Vm, where every published identifier
  names an object that exists. The last two close behind it with no work of
  their own.

**4 — Parity, once the targets stop moving.**

- **#414** first: more than half its operations are served and reached by
  nobody — the routes exist, the clients exist, nothing connected them.
- then **#413**, **#411**, **#412**, **#410**, and **#409** last (134 gaps).
- **#415** travels with them, and is partly a *decision*: an instance pool no
  supported client drives may be better declined with a reason than served
  without evidence. It also carries the question of committing the domain
  classifier, which still lives in a throwaway script.

**5 — Documentation, immediately before the tag.**

- **#403** — deliberately last: the roadmap, `confidence.md` and the README must
  describe the release's final state. Correcting them earlier means correcting
  them twice.

### The cut, if the release needs to ship sooner

The natural break is **after wave 3**: 0.11.0 would then deliver the
measurement, its trustworthiness and the catalogue chain, and the six parity
issues would become the core of the next release. A release that never ships
proves nothing to anybody. Taking that cut is a decision, not a slip, and it
belongs to the maintainer.

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
generated tables instead of freezing a figure that will rot. This page has paid
for that rule once already — its per-provider companions froze their counts on
2026-07-30 and every one of them was wrong within a fortnight (#127); they are
archives now, under [history/](history/).

---

## Now, what is being built

### The container image, control-plane only, shipped with the release

**Landed with #150**, and first published with 0.8.0.

This was a decision more than a feature, so here it is in writing: **the image
runs `feint serve` with `--vm off`, and emulates nothing but the control
plane.** The question that held it back, where machines started inside a
container would land, is answered the way the rest of the project already
answers it: the default mode needs no runtime, the conformance suite runs
without one in CI, and `serve` in the foreground is exactly what a container
entrypoint wants. Anyone who needs real machines runs the binary on a host with
Incus, which is the documented path and stays so.

Why it came before the adoption channels: the image is the format an emulator is
consumed in, and every channel below (`feinttest`, the compose file, the
`services:` block in GitLab CI — all since landed) waited on it. What the image must never become is
the nominal mode: the self-detaching static binary is the one thing none of the
comparable emulators can do, and leading with Docker would erase it.

**Proven by:** `release.yml` publishes a multi-arch image to ghcr.io, and
`conformance.yml`'s `image` job runs the Scaleway conformance suite from the
host against the emulator running inside that image, on every pull request.

### The golden-image workflow on Scaleway: both halves landed

The most expensive gap in the served surface was never a percentage, it was a
scenario: build an image with Packer or a `scw` script, attach a volume with an
ordinary Terraform module.

**The first half landed with SW-2 (#7, merged as #131).** Snapshots and images
are control-plane records: a client snapshots a volume, cuts an image from the
snapshot, lists it beside the fixed catalogue, and the deletion order is
enforced. Volume attachment is served. What an image cut here cannot do is
boot — this emulator keeps records, not disk contents — and it says so at the
boot instead of substituting a distribution ([limits.md](limits.md) carries the
refusal, #115 the decision). The `instance` untriaged column in the generated
tables reads zero, by decision: what was not served (placement groups among
them) is declined with its reason in the pack.

**The second half landed with SW-3 (#8, merged as #138).** `block/v1` and the
`sbs_volume` root volume closed the measured trap in [limits.md](limits.md),
where the provider read a volume back through an API no pack served and the
apply died on a 404. That page now opens its root-volume section with
`sbs_volume` working, which is what ending the item meant.

One detail worth keeping, because it was found by the client rather than by
reading the SDK: `scw` 2.56.3 calls `/block/v1alpha1` while the Terraform
provider calls `/block/v1`. Both are served, from the same handlers.

**Proven by:** `terraform apply` with a `scaleway_block_volume` and an
`sbs_volume` root volume, an empty second plan and a clean destroy, in the
Scaleway conformance suite.

### Exoscale was labelled preview, and the label came off at EXO-2

**Settled.** Exoscale is *starter*, alongside Outscale, since EXO-2.

The label was taken deliberately rather than allowed to slide: the pack shipped
marked **preview** because the official `exo` CLI drove it end to end while a
user still could not run a realistic workload against it. The in-between state,
served but not honestly usable, is the one that damages credibility, which is
this project's capital.

That premise is no longer true. EXO-2 serves the instance lifecycle, security
groups and their rules, anti-affinity groups, elastic IPs and their attachment,
and the `exo` suite drives every one of them — stop, start, reboot, scale,
resize, a delete refused while protected, an address published on an instance
and withdrawn.

**The exit condition itself was wrong, and it is worth recording why.** It read
*until the Terraform provider is proven against it*, which assumed the Exoscale
Terraform provider could be pointed at an emulator at all. Measurement refuted
that: the provider honours `EXOSCALE_API_ENDPOINT` for its egoscale v3 client
and builds a v2 one with no endpoint option, so an apply splits between the
emulator and a paying account. `ClientOptWithAPIEndpoint` exists in egoscale and
is never called; three sites build a v2 client without it. Filed upstream as
[exoscale/terraform-provider-exoscale#573][exo-573], with the mechanism and a
reproduction; the reasoning and a patched build are in
[limits.md](limits.md#the-exoscale-terraform-provider-is-refused-and-why).

A condition nobody here can reach is not a condition, it is a hostage. Keeping
it would have made this project's own published maturity depend on someone
else's tracker, for a duration nobody controls — while the thing the label was
warning about had already been fixed.

**What still separates Exoscale from *usable* is stated in the coverage
tables rather than in a word:** its untriaged column is the largest of the
three, it is generated, and it cannot flatter. This paragraph used to copy the
three numbers; they rotted slower than the archived documents' only because
they were younger.

[exo-573]: https://github.com/exoscale/terraform-provider-exoscale/issues/573

---

## The three IaaS layers, one sequence

Each provider's IaaS layer was measured and cut into batches in a snapshot
dated 2026-07-30 — [Scaleway](history/roadmap-2026-07-30-scaleway-iaas.md),
[Outscale](history/roadmap-2026-07-30-outscale-iaas.md),
[Exoscale](history/roadmap-2026-07-30-exoscale-iaas.md). Those documents are
**archives** now: the reasoning that ordered the batches is the record, the
figures are that day's, and each carries a banner saying so (#127). What is
current lives where it regenerates — the README's tables, [routes.md](routes.md)
— and what remains open lives in the wave milestones and their issues. This
section orders the batches of all three into one sequence and names the work
that cuts across them.

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
   module above; for Exoscale the ordinary instance stack — proven through
   `exo`, since its Terraform provider cannot be pointed here ([#573][exo-573]).
3. **The untriaged column reads zero for the products that provider's roadmap
   declares in scope** — zero by decision, served or declined with a reason,
   never by a widened denominator.

The third sentence is what keeps the first two honest over time: a scenario
proven once stays proven only because the gate fails when the surface under it
moves.

### The work that cuts across all three packs

Named here because each item looks local in any single batch and is not. Two
have landed and are kept because they now name the mechanism to imitate; three
are standing rules.

- **`Declined()` carries a reason** — landed with X-1, and the doctrine has
  since reached one level deeper: #122 gives a pack `DeclinedFields()`, a
  field of an observed response it knowingly does not serve, with the same
  no-placeholder guard on the reason. "Not served" and "not triaged" are
  different answers at every granularity.
- **The Terraform evidence question is settled per provider, not globally** —
  Scaleway and Outscale each have a fixture in `tools/conformance/`, and
  Outscale's drives the provider's own `examples/net_vm` plus its storage
  chain. Exoscale's is *impossible rather than missing*, measured and filed
  upstream ([#573][exo-573]); its evidence is the official CLI, and
  [limits.md](limits.md) explains why the patched-provider proof deliberately
  does not count.
- **The contract is extended with every product, never after it.** New
  products enter `tools/contract/` extraction in the same change as their
  routes. Outscale and Exoscale make this order mandatory: their
  `additionalProperties: false` contracts refuse an unextracted product's
  responses outright.
- **Every new lifecycle path takes the per-target lock** —
  `machine.Binding.Serialise`, which exists and is taken by all three packs —
  and proves it with a concurrency test, the way
  `TestConcurrentPowerOnStartsTheMachineOnce` does. Nothing will remind anyone
  of this; only the test does. (#134 proposes the scenario-level complement:
  invariants held under a deliberate barrage, not only each lock under its
  own race.)
- **Runtime backing arrives only as a declared capability.** Block storage,
  load balancers and gateways ship as control plane first; a driver that gains
  real backing declares it (`machine.Capabilities`), and an undeclared
  capability counts as absent.

### The sequence

Ordered by what unblocks a user, which is the same criterion as the rest of
this page. Batch identifiers are the ones the issues and milestones carry;
the archived documents explain how each batch was cut.

1. **The triage wave** — **done.** Scaleway, Outscale and Exoscale batch 1,
   carrying the `(operation, reason)` change (X-1). It turned three unreadable
   untriaged columns into work lists and put iam and marketplace under the
   gate they used to escape. Its evidence stands as stated: the gate returns 0
   on the baselines, and the untriaged columns of the generated tables are
   work lists, not walls.
2. **First Terraform proof for Outscale, and the machine lifecycle for
   Exoscale** — **done.** OSC-2 brought `terraform apply`, an empty second
   plan and a clean destroy into conformance; EXO-2 brought the lifecycle
   under `exo` and took the *preview* label off, as recorded above.
3. **The Scaleway golden-image scenario** — **done.** SW-2 (#7) merged as #131,
   SW-3 (#8) as #138. See the "Now" item, which is this wave.
4. **Networks that route** — **Outscale and Exoscale done, one open.** OSC-3 is
   merged: the provider's own `examples/net_vm` applies, re-plans empty and
   destroys. EXO-3 (#9) merged as #161: a private network is a range, and an
   attach leases from it. SW-4 (#11, IPAM lifecycle and the rest of vpc)
   remains, under the network-evidence rule: under OVN the
   claim is asserted, elsewhere it is skipped, and no document says "isolated"
   without naming the mode.
5. **Storage on the two starters** — **done.** OSC-4 is merged (volumes,
   snapshots, images, the storage chain in the Terraform fixture). EXO-4 (#12)
   is merged too: block storage, thirteen operations a real client drives,
   aligned with the relation rules Scaleway settled — stored on one side,
   computed on the other, deletion rules tested by the fixture's destroy.
6. **Load balancing and gateways** — **landed**, on all three packs: EXO-5 with
   #345, then SW-5, SW-6, OSC-5 and EXO-6. It was ranked last because nothing
   else depended on it and everything it depended on (IPAM, networks, the waiter
   discipline) is above; each arrived control plane first, with
   capability-gated backing after.

The waves are an order, not a schedule: a wave can start before the previous
one is fully green when its dependencies are, and an issue where an official
client breaks outranks the whole list, as the last section says.

### The operational view

One line per batch. Each line is an issue, steered from the
[project board](project.md); a closed issue is the proof the line is done, so
the table carries the state as an issue reference rather than a claim.

| ID | Wave | Delivers | State |
|---|--:|---|---|
| **X-1** | 1 | `Declined()` carries a reason per operation | done |
| **SW-1** | 1 | iam and marketplace under the gate; instance/vpc/ipam triaged | done (#4) |
| **OSC-1** | 1 | the non-IaaS half declined by name | done (#3) |
| **EXO-1** | 1 | the managed-service surface declined by name | done |
| **OSC-2** | 2 | `ProductCodes`, admin password, tags, root volume — the first Outscale `terraform apply` | done (#6) |
| **EXO-2** | 2 | instance lifecycle, security groups, elastic IPs — the *preview* label comes off | done (#5) |
| **SW-2** | 3 | snapshots, images, volume attach | done (#7) |
| **SW-3** | 3 | `block/v1` and the `sbs_volume` root volume | done (#8) |
| **OSC-3** | 4 | routable networking — `examples/net_vm` applies | done (#10) |
| **SW-4** | 4 | IPAM lifecycle and the rest of vpc | done (#11) |
| **EXO-3** | 4 | private networks and instance attachment | done (#9) |
| **OSC-4** | 5 | volumes, snapshots, images | done (#13) |
| **EXO-4** | 5 | block storage | done (#12) |
| **SW-5** | 6 | `lb/v1` ZonedAPI | done (#17) |
| **SW-6** | 6 | `vpcgw/v2` | done (#18) |
| **OSC-5** | 6 | load balancing | done (#16) |
| **EXO-5** | 6 | NLB | done (#345) |
| **EXO-6** | 6 | VPC and routes | done (#15) |

Sizes and operation lists stay with the issues and the archived documents,
where they can be argued against the measurements that justified them. The one
thing worth knowing here: SW-5 was the largest single batch of the sixteen.

### Start here

Three commands, in this order, before writing any code:

```bash
mise run upstream:sync                 # the scan reads a current checkout or nothing
mise run drift:check                   # 0: the baselines are current; 2: triage before planning
feint coverage --sdk .upstream/scaleway-sdk-go --products instance,vpc,ipam --format triage
```

The third one prints the untriaged work list for the products named — which
wave 1 emptied, so for these it reads zero today, and a new upstream operation
is exactly what makes it stop reading zero. If `drift:check` exits 2, that
triage comes before anything on this page: a baseline nobody has ruled on
makes every "the column reads zero" claim meaningless.

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

## The tooling that makes the sequence above cheaper

The waves are the product. This section is the tooling around them, and it
exists because of one comparison: LocalStack's feature grid, paid tiers
included, was read against this project. Most of that grid is not transposable,
and the reason is measured rather than felt — its differentiating features
(chaos injection, an IAM policy stream, a traffic inspector) were built on top
of near-complete coverage, where the generated tables in the README still show a
minority of the surface served and untriaged columns that are not yet all zero.
Copying the grid now would be a second storey on foundations still being poured,
and every feature added is one more surface to hold against an upstream that
moves by hundreds of operations a year.

The figures are deliberately not repeated here, for the reason stated at the top
of this page: a count frozen into prose rots, and the tables regenerate. The
issues linked below carry the numbers as measured on the day they were written,
which is what an issue is for.

So the useful question is not *which LocalStack features are missing here*. It
is **which of them lower the cost of coverage**, since coverage is what the
sequence above spends its time on. Three answered yes, and the first has since
landed.

### 1. Record what a real client and a real cloud say to each other — landed

The recording half **landed**: `feint proxy` records a redacted transcript of a
real client against a real cloud, `feint transcript --shape` reduces it to a
committable field tree — no values, no identifiers — and since #122 the shapes
gate compares the emulator's answers against those observed shapes **on every
pull request**, with `DeclinedFields()` carrying the reasoned refusals. The
sentence this section used to end on — *"the tool that produced the most
valuable measurements in this repository is the only one that was never
built"* — is settled, and the tool earned its keep on its first day wired: the
gate went red on a real divergence (`images[].default_bootscript`) in the same
branch that made the route comparable (#131).

The rest of the family landed with it, in 0.10.0. **`feint replay`** compares
the emulator's answer with the one the real cloud gave, exchange by exchange;
**`feint coverage --observed`** orders the untriaged column by what a client was
actually seen calling — which is what turned every wave above from a bet into a
count. Both are shipped, documented and gated.

And one measured trap governs both, which is why it stays written here: a client
that follows in-band endpoints walks away from the proxy mid-session, so a
recording is only as complete as the dialect allows.

**Evidence:** as each issue states. The rule that governs the family held and
keeps holding: a transcript contains neither a credential nor a secret from the
body, proven by a test that fails when the redaction call is removed; recording
happens on a human's own station, against their own account, never in CI.

### 2. The emulator as an importable package — #75

`Server.Handler() http.Handler` already exists; everything that would use it is
under `internal/`, so nothing outside this module can. Two items on this page
pay for that today: `feinttest` must start a published image to
reach a handler that could be a function call, and *"a fourth provider changes
nothing in `internal/core`"* is admitted below to be untested — as it must
remain, since three packs in one tree can share a mistake for a year without
noticing.

One commit answers both, because both need the same types to stop being
internal. It is also the one thing a container-shaped competitor cannot copy: a
container does not become a function call in someone's test binary.

The cost is real and belongs next to the benefit: whatever leaves `internal/`
becomes an API this project breaks people with. Deciding *which* types is
deciding how much future freedom to sell, so the answer is the smallest set that
passes the evidence, not the set that looks tidy.

**Evidence:** a module outside this one, with no `replace` directive, starts the
emulator in-process and drives it with the official Scaleway SDK; and that same
module defines a `Pack` of its own, importing nothing under `internal/`. The
second is the first real evidence for an architecture claim this page has been
making from the beginning.

### 3. Fault injection — landed

Landed in 0.11.0: `emulator.Faulter` and `/_feint/faults`. It was ranked third
here on an argument worth keeping, because it is the argument that got it built:
it does not lower the cost of coverage, so it earned its place elsewhere — it is
middleware over the `ServeMux`, it costs little, and what it produces is
measurements about the behaviour of official clients. Whether the Scaleway
Terraform provider really retries a 429, whether `exo`'s waiter converges on a
slow asynchronous operation. Nobody could test that without degrading a real
account.

It composes with the first item, and that is how the shape of an injected error
was settled rather than invented: a recorded transcript carries a real 429 with
the body the cloud actually sent.

### The arbitration to reopen: DNS interception and TLS termination — #76

Not a fourth queue item. A refusal whose cost was never measured, which is a
different defect and one this repository takes seriously everywhere else.

[limits.md](limits.md) declines object storage because the Scaleway Terraform
provider builds `https://s3.<region>.scw.cloud` in code, so redirecting it needs
DNS interception plus a certificate the provider accepts, *"which is a project
of its own"*. The first half is a measurement and it is right. The second half
is an estimate nobody has made, and rule 3's demand that a refusal carry a
reason is not satisfied by a size nobody has weighed.

What reopens it is not a wish for object storage. It is that the blocker is
generic: **every client whose endpoint is built in code rather than read from a
setting** is unreachable for the same reason, and this project does not know how
many of those exist. That number could settle the question in either direction.

**Measured, and refused with the numbers behind it.** The four measurements #76
asked for are done and written up in [limits.md](limits.md) under *The cost of
DNS/TLS interception, measured*. They invert the premise: the certificate was
the feared half and is the cheap, safe one, while the DNS redirect — the half
#76 named almost in passing — is the blocker.

- **How many hardcoded endpoints:** one. Swept across three Terraform providers,
  three CLIs and three SDKs, exactly one endpoint is built in code with no
  setting to override it — Scaleway Object Storage in the Terraform provider.
  Everything else, including Exoscale SOS and all of Outscale, is reachable
  through an endpoint setting. The coverage cap is one product on one client, not
  the dozen the reopening feared.
- **What accepts a locally minted certificate:** every Go client through one
  process-scoped `SSL_CERT_FILE` — proven by `scw` creating a server and, the
  open doubt, by the **Terraform provider plugin inheriting it** and applying
  five resources over local TLS. No system trust-store install is needed.
- **Whether a DNS server is needed:** no, but that is cold comfort. On a hardened
  Linux there is no per-process, disposable, unprivileged way to redirect the one
  hardcoded name for a static pure-Go plugin: `curl --resolve` is curl-only,
  `HOSTALIASES` misses dotted names, an `LD_PRELOAD` shim misses `CGO_ENABLED=0`
  binaries, and a network namespace needs a user namespace this station blocks.
  What is left — editing `/etc/hosts` — is a durable change to the operator's
  machine, which the *no trace* pitch forbids.
- **Standard-library cost:** under 100 lines of `crypto/x509` and `crypto/tls`,
  no dependency, for the CA half.

So object storage stays declined, and now the reason is `Declined()`-grade: not
*"a project of its own"* but *"the certificate is cheap and safe, the name
redirect is neither, and it buys one product on one client"*. If it is ever
retained, it is an item with a named owner and a measured shape —
`SSL_CERT_FILE` plus an operator-scoped, disposable name redirect (a
devcontainer, a temporary hosts entry), never a system trust-store install and
never a hosts file the binary edits itself.

**What #336 changed about the blocking half, and what it did not.** `feint proxy
--forward` (2026-08-20) accepts `CONNECT` and needs no name redirect at all: a
client that honours `HTTPS_PROXY` hands the proxy the hostname itself. So the
half that was called the blocker has a second door for **any client that reads
that variable** — measured on a Go client which installs no `Transport`, which is
the ordinary case.

**And #346 closed the one question that was left.** The Scaleway Terraform
provider's S3 client *does* honour `HTTPS_PROXY` for its built-in
`https://s3.<region>.scw.cloud`: measured on Linux on 2026-08-21, provider
2.81.0, `CreateBucket` arriving on this emulator through a `CONNECT` with
`aws-sdk-go-v2 … terraform-provider-scaleway/2.81.0` in the User-Agent, and a
control run showing the twelve refused CONNECTs the same apply produces when the
proxy does not name the host. The transcript and both readings are in
[limits.md](limits.md) number 7. The redirect therefore costs two process-scoped
environment variables on the exact client that blocked it, and the decline now
stands on its coverage argument alone — one product on one client, and an S3
surface nobody has costed — rather than on any part of the redirect being hard.

### Considered in the same pass, and not queued

Named rather than left floating, which is the same discipline as `Declined()`:

- **Declarative seed state** (#77), a fixture file a reviewer can read where a
  snapshot is a generated blob. Real, small, and behind the three above. Its
  file format is JSON, not YAML: there is no YAML parser in the standard
  library and a three-line `go.mod` is not spent on making a fixture prettier.
- **Least-privilege policy generation** — deriving a Scaleway IAM or Outscale
  EIM policy from the operations a run was observed to need. It is the strongest
  product idea in the comparison, it observes rather than verifies (so it does
  not disturb the decision never to check signatures), and the observer already
  holds most of the data. It waits because it is differentiation, and
  differentiation built on a surface this thin is a demo.
- **Deterministic control of transition times** — no longer a note on #26: it
  is **#124**, an observation-driven scheduler that makes a transient state
  reachable without a wall clock, so the refusals that live there can fire.
  Filed from the external review; the measured Outscale case
  (`409 InvalidVolumeState`) is its anchor.
- **An inspection TUI.** Superseded for now by the read-only page the binary
  serves about itself (#67, #68, #69), which shows the same data. Revisit only if
  that page proves to be the wrong surface.
- **A public per-provider coverage site.** The tables are already generated by
  `feint docs`; turning them into a static site is acquisition work, not
  coverage work, and it is ordered accordingly.
- **Multi-project and multi-organisation boundaries.** Filed here mostly to
  correct the premise: `resource.Tenant` already carries a Project, and the
  Scaleway pack already scopes SSH keys, security groups, volumes and IPs by
  the `project_id` the client sends. What is genuinely fixed is
  `organization_id`, and servers are not project-scoped. So this is a smaller
  and better-defined piece of work than it looks, and it still waits.

---

## The proof track: what two external reviews changed

Two passes of an external review (2026-08-13) were triaged into issues the way
a drift report is: verified against the tree first, enriched or refused with a
reason, never copied. The verdict that survived verification is one this page
already leans towards: **the route count now demonstrates the architecture;
what it does not yet demonstrate is how much a green run proves.** Spending the
next versions on proofs rather than surface is the proposal.

What that track has built is now described end to end in
[docs/conformance.md](conformance.md): the chain from the provider's own API
description to a pipeline reading a number out of the emulator, what each link
proves — and, since #170 closed, no link of it is enforced by prose alone
(#169, #170 and #171 all landed; that page's closing section carries the
detail).

The issues, most of them since delivered, each carrying its own evidence:

- **#123** — *done*: what is proven about an operation is a set of named proof
  axes (driven, contract, behaviour, dataplane…), computed from artefacts,
  published without ever being summed into a score.
- **#125** — *open*: the runtime-backed proof runs on a machine nobody here
  owns; it promotes the "Later" item below and carries its promotion rule.
- **#130** — *done*: one page answers what a user can validate here, per
  runtime mode, every row carrying its proof or its limit
  ([confidence.md](confidence.md)).
- **#132**, **#133** — *done*: this project's own contract surfaces (CLI, exit
  codes, `/_feint/*`, snapshots) frozen by tests; a snapshot is understood or
  refused, never silently half-read.
- **#134**, **#135** — *done*: concurrency invariants under a deliberate
  barrage; crash and restart behaviour stated once and proven by a kill.
- **#124**, **#126**, **#128** — *open* — and **#129** *done*: deterministic
  transient states, the opt-in strict catalogue and `feint exec` are fidelity
  and hardening still explicitly behind the proofs above; the
  release-workflow-pinned signature is delivered and documented in the README's
  install section.

**The arbitration they propose is #136**, and it is a proposal, not a
decision: version 0.8 buys *trust* (runtime CI, concurrency, crash
determinism, proof axes), 0.9 buys *contract* (frozen surfaces, snapshot v1,
machine-readable divergences, the OCI image), 1.0 buys *adoption* (the
confidence page, a reference CI example, `setup-feint`, the SemVer
commitment) — and no version buys a route count. The decision is the
author's; this page records that the question is now posed, and that the
waves above keep running either way.

---

## Next, what a user will ask for first

### What "probed" and "refused" prove gets tightened

The request side now has teeth: a field a client sends that no handler reads
fails the conformance run, which is the mechanism that caught a server retype
answering 200 while doing nothing. The two gaps this item named are closed,
and each closed on its stated evidence. A probe answered with a 4xx used to
count as *refused* with its error body never validated, so a wrong error shape
hid behind a right status code — the probe now validates every refusal body
against the provider's declared error schema, and a violation fails
`mise run conformance` (#162). Request parameters were not contractualised,
which is exactly where an ignored page-size parameter slipped through until a
real client noticed — the contracts now declare query parameters, the paged
probes vary the page size and assert the page they got (#166), and a declared
query parameter a handler never reads fails outright (#271,
`TestDeclaredQueryParametersAreRead`). The sentence that drove it stands: a
"probed" that proves little is worse than an honest gap, because it reads like
evidence.

### IAM under the drift gate — settled

**Done, with SW-1.** `iam` is a scanned product: it appears in the generated
coverage tables with a baseline of its own, and an upstream IAM operation
nobody has triaged makes `drift:check` exit 2 like any other product's would —
which was this item's stated evidence. It stays on this page for one release
because the state it fixed (served and unmeasured, the least defensible state
a route can be in) is worth remembering by name.

### A `setup-feint` GitHub Action and a GitLab CI template — landed

The lifecycle verbs were designed for CI (`start`, `wait`, `env`, stable exit
codes), so the action is a thin composite, not a project — and that is what
shipped. `stephrobert/setup-feint@v1` installs the released binary, verifies
its checksum before running it, and waits until the emulator answers
(`.github/actions/setup-feint/`, published from here and gated against the
copy, #245); the GitLab `services:` template, the compose file and the
GitHub Actions job live under [examples/](../examples/) (#244, #246).

**Evidence, met:** the example pipelines go from checkout to a passing
`terraform apply` against the emulator using only the published action or
template, and `examples/README.md` walks each one.

### A testcontainers-go module — landed as `feinttest`, deliberately not testcontainers

This is how an emulator enters other people's test suites. The section used to
say it must live in a separate repository, because the module would depend on
testcontainers while this repository's zero-dependency `go.mod` is enforced by
a pre-commit hook. What shipped (#247) refutes the premise rather than the
goal: [`feinttest/`](../feinttest/) lives *in* this repository, drives the
container CLI instead of importing testcontainers, and keeps the dependency
count at zero — `feinttest.Start(t)` starts the published image, hands back
the endpoint, and cleans up with the test. Its own doc comment carries the
"why this is not testcontainers-go" argument. A community testcontainers
wrapper remains possible on top; nothing here blocks it, and nothing here
waits for it.

**Evidence, met in half:** `feinttest`'s own tests start the published image
and prove it answers, isolated per test. The other half of the stated evidence
— an official SDK creating and deleting a server through it — is shown in the
package's doc comment and cannot be a test *here*: importing a provider SDK is
exactly the dependency this repository's three-line `go.mod` refuses, so that
proof belongs to the first consumer's suite, not this one.

### Conformance suites split per resource

Today one script per provider drives everything that provider serves. That makes
a suite grow without bound and, more practically, makes it impossible to run two
of them in parallel without them fighting over the same emulated account. The
surface cannot keep growing if the suite that proves it becomes the bottleneck.

**Evidence:** the suites run concurrently against one emulator and pass.

---

## After 1.0: the environment becomes the thing that travels

This is the product direction, written down before any of it is built so that
the boundary is on the page rather than in somebody's head. **Nothing here is
scheduled**, and none of it belongs to a 0.x release.

### The sentence

**Feint is not a general-purpose Devbox. Feint is the Devbox of your cloud
environment.**

Devbox makes a developer's *tools* reproducible — packages, versions, shell.
That problem is solved, by Devbox, Nix, mise and dev containers, and this project
has no business re-solving it. What is not solved is the other half: two
developers on the same repository do not get the same *cloud* to develop
against, and neither does CI.

Feint already holds most of the pieces. A control plane three official clients
drive, a machine runtime with real networks and firewalls, a snapshot format
designed to outlive its instance, a container image, declared runtime
capabilities, and an evidence record that says which of all that is proven. What
is missing is the thing that ties them to a repository.

### The shape

One file describing how to *bring the environment up* — never what the
infrastructure is, and never which tools to install:

```yaml
# feint.yaml
version: 1

cloud:
  provider: scaleway

runtime:
  mode: incus-ovn

iac:
  engine: opentofu
  directory: terraform

ready:
  - ssh:web-01
  - tcp:web-01:80
```

Then `feint up`, and a colleague who cloned the repository gets the same small
cloud: the control plane started, the client configured, the IaC applied, the
machines reachable, and the endpoints printed.

### Three things that must not be confused

This is the design constraint, and getting it wrong would turn Feint into a
worse Terraform:

| what | says | where it lives |
|---|---|---|
| `feint.yaml` | how to bring the environment up | this project |
| Terraform / OpenTofu | what the infrastructure is | the user's repository |
| a snapshot | what the state currently is | an artefact, shared as a file |

`feint.yaml` must never grow a resource block. The moment it describes a subnet,
this project has started rewriting Terraform badly.

### What it must not become

The risk here is not technical, it is scope. This repository was built with a
discipline — measured emulation, explicit refusals, a record that says what is
unproven — and a list of features nobody measured would dissolve it faster than
any bug.

So, explicitly out: package management, secret management, shell management,
remote development, IDE integration, an environment registry, collaborative
sync. Devbox composes with Feint; it is not absorbed by it.

The first step is **four primitives and no more**: the file, `up`, `down`, and
exporting an environment somebody else can reproduce.

### Why it is worth doing at all

Two things this project already has make it stronger here than the comparable
tools.

**A real dataplane.** LocalStack's Cloud Pods share state, and its dev-container
integration makes it a component of a reproducible environment — the direction
is right and they got there first. What they do not have is machines a
developer can `ssh` into, on subnets that are actually separate. Feint does,
under OVN, and `capabilities.isolation` is what says so rather than a claim.

**The same declaration in CI.** `feint up` on a laptop and `feint up` in a
workflow read one file, and the runtime difference is *declared* rather than
discovered: the control plane alone where no runtime exists, machines where one
does. That is the honest form of "it works on my machine", and it is the one
piece of this that fits the project's existing doctrine exactly.

### The criterion, which is what keeps this from dissolving

**A feature belongs to Feint if it helps create, reproduce, observe or test a
local cloud environment.** Governance, compliance posture, enterprise identity
and a general platform catalogue are other products, and saying so here is
cheaper than discovering it three features in.

That criterion also names the positioning better than "local cloud platform"
does: what this becomes is a **cloud lab** — the words keep the testing, the
experimenting and the proving, which is what this repository has actually built.

### The axes, and the ones that already existed

Four primitives first, and no more:

| # | primitive |
|---|---|
| #189 | the environment declaration |
| #190 | `feint up` and `feint down` |
| #191 | an environment somebody else can reproduce |
| #192 | the same declaration on a laptop and in CI |

Then the axes that exploit what this project has and the comparable tools do
not — real machines, real networks, and a record that says what is proven:

| # | axis | why it is ours |
|---|---|---|
| #193 | drift simulation | changing the cloud *behind* Terraform is the one situation no test here has ever produced |
| #194 | network assertions | OVN can answer "is the database reachable" instead of "is the rule declared" |
| #26 | fault injection | already filed; retries, backoff and idempotence have never been tested against a refusal |
| #124 | eventual consistency | already filed; a waiter nobody waits for is a waiter nobody tested |
| #73 | record and replay | already filed; a transcript attached to an issue is a bug anybody can reproduce |

Three of those five were open before this direction was written down. That is
worth noticing rather than glossing: they were filed one at a time, for local
reasons, and they turn out to be the same idea. Naming the direction is what
makes them a plan instead of a backlog.

None of it carries a milestone, and none should until 1.0 is out: a direction
with a date is a promise, and this page has spent a year learning what an
unkeepable promise costs.

### And then the runtime is somebody else's project

The obvious next step is to teach this driver about clusters. It is the wrong
one, and saying why is the most useful thing on this page.

Feint deliberately mixes two things, and for an emulator that is correct:

```text
a provider's API dialect  +  a local runtime
```

Materialising on real, persistent hardware makes the second half important
enough to deserve its own contract. A small IaaS above Incus answers a different
question from the one this project answers, and answering both in one binary is
how a repository acquires two products and does neither well.

So the split:

```text
Scaleway API ──┐
Outscale API  ─┼──►  Feint  ──►  a cloud API  ──►  Incus / OVN / storage
Exoscale API  ─┘   compatibility   somebody else's project
```

Feint keeps its question — *does my code written for this provider work?* — and
gains one more runtime backend beside `off`, `incus` and `incus-ovn`. The other
project answers *where does this infrastructure actually materialise?*, and is
usable without Feint at all: its own CLI, its own SDK, its own Terraform
provider.

**The coupling is the protocol, not a package.** No `import
github.com/…/feint/internal/…` in that project and none the other way. An HTTP
contract is a boundary that survives a rewrite in another language; a shared Go
package is a boundary that dissolves the first time somebody needs one more
field. This is the neutral-core rule of `internal/core`, applied one level up.

Two consequences worth writing down before anybody builds either side.

**Its API should be asynchronous from the first commit.** `POST /instances`
answering `202` with an operation to poll is not a refinement to add later:
retrofitting it means changing every client that ever consumed the synchronous
shape. Feint can answer `running` immediately because it emulates; a real
control plane cannot. That is also why #124 matters here — an emulator that only
ever answers instantly is one nobody's waiter code was tested against.

**What belongs to this repository is one backend and its boundary.** The other
project's API, its scheduler, its IPAM and its authentication are not issues
here, and filing them here would be the first step in merging the two again.

### The order, if all of it happens

| | |
|---|---|
| 1.0 | the emulator, stable and measured |
| 1.1 | portable environments — #189 to #192 |
| 1.2 | one more runtime backend, speaking an HTTP contract — #195 |
| 1.3 | that contract has a cloud behind it, in its own repository |
| 1.4 | drift and network assertions — #193, #194 |

Written as an order rather than a schedule. None of it has a date, and the one
thing this page has learned to avoid is a promise whose condition somebody else
controls.

---

## Later, decided, not scheduled

### `--vm` gets proven by CI, on a runner nobody owns — tracked by #125

**Landed, and it is a nightly job rather than a gate.**
`.github/workflows/runtime-proof.yml` installs Incus (Zabbly stable, pinned key)
and OVN on a GitHub-hosted `ubuntu-24.04`, wires the northbound connection, and
runs the network, ssh and crash suites in both `incus` and `incus-ovn`. Both legs
have passed, cross-VPC isolation asserted, on a machine nobody here owns.

What is still true, and is the only thing that should be read as a limit: **no
pull-request gate starts a machine runtime.** The job is advisory until its
promotion criterion is met, so the mode that carries the product's argument is
not yet something a contributor's pull request has to satisfy.

This paragraph said "no workflow in this repository starts a machine runtime"
until an audit measured it against the workflow added by the very release train
that shipped it. It had propagated to five places in two languages, and
`docs --check` reconducted it at every release because it compares the page with
its generator: it proves the form, never the claim. Verifying is not parsing,
committed on this project's own documentation — recorded here rather than
quietly reworded.

The groundwork below was measured on 2026-07-30, before the job existed, and the
combination it calls unproven has since been run:

- **Incus runs on a GitHub-hosted `ubuntu-24.04`.** `lxc/incus` drives its own
  `test/main.sh` there with real containers on zfs, btrfs, lvm and ceph.
- **OVN with the kernel datapath runs there too.** `ovn-org/ovn` runs
  `system-test` — the variant that loads `openvswitch.ko`, not the userspace one
  — on the same runner, after `apt install linux-modules-extra-$(uname -r)` and a
  hosts-file fix from its `.ci/linux-util.sh`.
- **Nobody ran the two together — until this job did.** The Incus CI contains no
  occurrence of `ovn`, and that repository has no OVN test suite at all, so the
  first evidence that the combination works on a hosted runner is this one.

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

The order is measure, then gate. The job runs nightly and by
`workflow_dispatch`, and moves onto `pull_request` only once its failure rate
over a stated number of nights is at a stated threshold — a number the Actions
history proves, not an opinion. That streak is not there yet, which is why this
is not a gate. A gate that is red on
the day it appears is a gate everyone learns to ignore, and this repository
already carries the note about what that costs.

**Evidence, met:** a CI job installs Incus from Zabbly plus OVN on a hosted runner,
wires the northbound connection, and `FEINT_VM=incus-ovn` runs the network suite
to completion — the subnet created through the emulated API, the address the API
published answering, and the isolation assertion passing rather than skipping.
Until then the install page says `--vm` is proven by the release table and never
by CI, and that sentence is generated, so it cannot quietly stop being true.

### Outscale reaches parity with Scaleway on the IaaS core — largely landed

Nobody else emulates Outscale, and its buyers (public sector, SecNumCloud) read
proofs rather than marketing. Most of what this item used to promise has since
been merged: the addressing plane (Net, Subnet, mask bounds, containment,
overlap, a real bridge, a Vm carrying the address the API published), routable
networking with the provider's own `examples/net_vm` applying, re-planning
empty and destroying (OSC-3), and the storage chain (OSC-4). The parity bar
this item named — that `net_vm` apply — is met, and [limits.md](limits.md)
records what the served topology does and does not move.

Load balancing (OSC-5) landed after it. The warning this paragraph used
to carry — that a rule sourced by *group* rather than by CIDR needs an OVN
selector before any batch promises Outscale group **enforcement** — was
answered by #475 the way the driver's own contract suggests: the runtime has
no group selector, so the member reference is expanded into the addresses the
member machines answer on, and re-expanded whenever a member boots or gains an
interface. Outscale and Exoscale hand their groups to the runtime now, within
the measured bounds [limits.md](limits.md) states (a routed NIC enforces
nothing by declaration, and the same-subnet sender-egress divergence); the
multi-subnet bound #491 tracked — the isolation set defeating a group's
default-deny — was closed by removing the OVN isolation set's catch-all
allow, proved on the three example stacks.

**Evidence:** for what landed, the conformance suite as it runs now; for the
remainder, the LB apply named in OSC-5.

### Object storage stays out, and the workaround gets a page

The reason is stated and measured in [limits.md](limits.md): the Scaleway
Terraform provider hardcodes `https://s3.<region>.scw.cloud`, so supporting it
needs DNS interception and TLS termination rather than an endpoint setting.
Emulating S3 is not the hard part and never was; reaching the emulator is.

**What that "no" rested on is now measured** — see the arbitration above and #76.
The estimate that once followed the measurement (*"a project of its own"*) has
been made: the blocker is one product on one client, not the generic drift the
reopening feared, and the cost lives in the DNS redirect, not the certificate.
So this item is a settled refusal again — this time with the numbers behind it,
in [limits.md](limits.md) — rather than one waiting on a cost.

What does not wait is the "here is how": the SDK and CLI paths honour
`SCW_S3_ENDPOINT`, so a documented feint-plus-MinIO page covers the S3 workflow
for everything except the Terraform path, which the page says plainly. That page
is worth writing whichever way #76 goes, because it is the answer for anyone who
needs S3 this month.

**Evidence:** the page's commands are executed in CI the way the README's are:
`scw` pointed at MinIO through `SCW_S3_ENDPOINT` puts and gets an object.

### Snapshot compatibility across versions — settled by #133, merged as #140

`feint snapshot` shipped, so the question stopped being hypothetical, and an
external review forced the measurement this item was waiting for: the snapshot
carried **no version field**, and `store.Restore` decoded with plain
`encoding/json`, so a field that version did not know was silently dropped and
the restore succeeded — the exact best-effort this project refuses everywhere
else.

It is now an envelope, `{"format": "feint-snapshot", "version": 1,
"resources": [...]}`, and `Restore` refuses what it cannot account for: another
format, another version, an unknown field. A legacy bare array is recognised as
such and refused by name rather than through a decoder error nobody can read.
Bumping `snapshotVersion` is a breaking change under RELEASING.md, which is what
makes the field worth carrying.

The adjacent case this item always named — loading a snapshot while a machine
runtime runs, which replaces the store without reconciling the real machines —
belonged to #135, and that closed too, merged as #141: what survives a dead
emulator is stated once, noticed at restart, and proven by a kill.

**Proven by:** the behaviour table #133 asked for, one test per row, in
`internal/core/store`: `TestASnapshotOfThisVersionRoundTrips`,
`TestASnapshotFromTheFutureIsRefused`, `TestALegacyBareArrayIsRefusedWithARemedy`
and `TestARestoredResourceKeepsItsIdentity`.

### A fourth provider

The architecture was built so that adding one changes nothing in
`internal/core`. That claim is untested: three packs is not enough to know
whether the seams are in the right place. The fourth is chosen by demand, not by
intuition. The gate it waited on — Exoscale losing its *preview* label — is
open since EXO-2, and [fourth-pack.md](fourth-pack.md) has since measured what
such a pack would touch, file by file, with the remedies ranked; the counts
live there, where they were measured, not here. #75 carries the stronger form
of the test: an out-of-tree pack that cannot compile against a misplaced seam,
with the deliberately hostile shape the review specified.

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
  it. Anything that would need a dependency lives in its own repository — and
  `feinttest` exists precisely because the obvious dependency was refused and
  the CLI-driving shape kept it at zero.
- **Telemetry, or an account. Ever.** "No account, no bill" is in the first line
  of the README and it is load-bearing.
- **A fourth provider before the third is usable.** Otherwise the result is
  three half-empty shopfronts instead of two full ones.
- **The container image as the nominal mode.** Self-detachment is the only point
  where Feint beats all three comparable emulators at once; a JVM or CPython
  process cannot daemonise cleanly, a static Go binary can. The image exists so
  the emulator can enter other people's tooling, not to replace the binary.
- **Checking that an identifier exists, by default.** A create naming an image
  the emulator never heard of succeeds, on all three packs, where the real
  clouds refuse. This is the limitation most likely to bite and it is
  deliberate, argued in [limits.md](limits.md): the emulator has no inventory,
  and a team pointing an existing Terraform configuration at it must not fail
  on a hardcoded production image UUID. What has changed since this was
  written: an unknown image can no longer *boot a substitute* — the boot fails
  and says why — and an **opt-in** validation mode, where an operator declares
  their own catalogue and gets their typos refused, is proposed as #126. The
  default never changes; any change must keep hardcoded production ids
  working.
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

### Refused after reading a competitor's grid

Added from the LocalStack comparison that produced the section above. These
exist there, several of them behind a paywall, and naming them is worth more
than letting them float as things nobody has ruled on — which is the same reason
`Declined()` takes a reason.

- **Hosted ephemeral instances and preview environments.** Their Cloud Sandbox
  runs the emulator on someone else's machine, reachable by a URL, which needs
  an account and a bill. That is a frontal contradiction with the first line of
  the README, and the contradiction is the point rather than a detail of
  packaging. Nothing about it becomes acceptable at a different price.
- **A Kubernetes operator, a Helm chart, or a cluster-side executor.** The
  audience is a developer's workstation and a CI runner, not a cluster. The
  container image already being built covers the `services:` block and the
  compose file, which is what that audience actually asks for. An operator would
  be maintained for a deployment shape nobody here has been asked for.
- **SSO, SCIM, shared workspaces, usage dashboards.** Enterprise seat features.
  They monetise an emulator; they do not emulate anything. A team that wants
  shared state has `feint snapshot` and a file.
- **A race on service count**, restated because the comparison makes the pull
  concrete: managed databases, managed Kubernetes, serverless. Already refused
  above, and the grid is exactly the pressure that refusal exists to resist.

### Posed, not decided: cost estimation

One item from that comparison is neither kept nor refused, and pretending
otherwise would be the dishonest option.

**Billing, quotas and capacity are refused above**, and correctly: an emulator
has no capacity to report, so any number it invents is a lie with a schema
around it. **Estimating what a `terraform plan` would cost** on the three
providers' public price lists is a different question. It invents nothing — the
grids are published — it answers something a user genuinely wants before an
apply, and nothing tools it today for the European clouds.

It is written down here because it does not fit either list. The most likely
answer is that it is an adjacent product and a separate binary rather than a
mode of this one: it needs no emulation, no store and no machine runtime, it
reads a plan and a price list, and folding it in would put a price table in a
repository whose whole discipline is that fixed tables are fiction
([limits.md](limits.md) says exactly that about the catalogue). Adjacent, not
included — but that is a leaning, not a decision, and it stays here until
somebody makes one.

---

## What would change this roadmap

An issue that says "this official client does something the emulator cannot
follow" outranks everything above. The order here is a guess about what people
need; a client that breaks is a fact about it.
