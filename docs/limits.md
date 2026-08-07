# Limits

What Feint deliberately does not do, and why. Stating this precisely matters
more than the feature list: an emulator that lies about its coverage is worse
than one that is small.

## Object Storage is not emulated

Scaleway Object Storage is S3-compatible, so emulating it is not the hard part
(MinIO does it already). The obstacle is **how clients reach it**.

Every other Scaleway product can be redirected with one setting: `SCW_API_URL`
for the SDK and CLI, `api_url` for the Terraform provider. Object Storage cannot.
The Terraform provider builds the endpoint in code:

```go
// internal/services/object/helpers_object.go
endpoint := "https://s3." + region + ".scw.cloud"
```

and addresses buckets virtual-host style, `https://<bucket>.s3.<region>.scw.cloud`.
Redirecting that needs DNS interception plus a TLS certificate the provider will
accept, which is a project of its own.

The SDK and the CLI are better off: they honour `SCW_S3_ENDPOINT`. So an S3
workflow driven by `scw` or by an SDK can already point at MinIO today; only the
Terraform path is blocked.

## The catalogue is fiction

Server types, prices, images and zones are a small fixed table
(`internal/providers/scaleway/catalog.go`). The emulator has no fleet, no
inventory and no price list, and it will never pretend to reflect the real ones.

It serves them anyway because the clients read them before creating anything: a
404 there makes `scw instance server create` fail outright. Treat any capacity,
price or availability answer as decoration.

## Identifiers are not checked against anything

A create that names an image, a template or a machine type the emulator has never
heard of **succeeds**. Scaleway answers 201 for an image UUID that exists
nowhere, Outscale accepts `ami-99999999`, Exoscale accepts an invented template
id. The real clouds refuse all three.

This is deliberate, and it is the limitation on this page most likely to bite.
The reason is the same one that makes the catalogue fiction: the emulator has no
inventory, so the only ids it could recognise are the handful it invents. A
configuration that hardcodes a production image UUID — the most common way a team
first points an existing Terraform at the emulator — would then fail on the one
thing that has nothing to do with what they are testing.

The cost is real and worth stating plainly: **a typo in an image id is not caught
here, and will be caught in production.** Feint proves that a request is
well-formed and that the response is shaped like the provider's, not that the
resources it names exist.

If you need that check, it belongs in your own validation. If the trade-off ever
turns out to be the wrong one, the place to change it is `resolveImage` in
`internal/providers/scaleway/images.go`, and the change must come with a way to
keep hardcoded production ids working.

## Lifecycle transitions are immediate

A server goes from `stopped` to `running` within the action call. Real hardware
takes a minute; reproducing that delay locally would only make every client wait
for information that does not exist here.

The states clients *check* are preserved: deleting a running server is refused
with `transient_state`, because Terraform depends on that error.

## Authentication is accepted, never verified

No signature is checked, on any provider. Credentials must merely be well-formed,
because the SDKs validate their shape client-side before sending anything.

This means Feint must never be exposed on a network you do not control. It is a
development tool that grants everything to everyone, by design.

## Docker was removed: it cannot back an emulated network

Feint shipped a Docker driver and no longer does. Incus is the only machine
runtime, and the reason is the network, not a preference between runtimes.

Emulating a cloud means emulating its addressing plan, and that needs four
things from the runtime. Docker gives one and a half of them.

**A network carrying a chosen block.** Both can do this: `docker network create
--subnet=10.0.0.0/24` and `incus network create n ipv4.address=10.0.0.1/24` are
equivalent. This is the half that works.

**A fixed address on a machine.** Docker takes `--ip` only for the first
network, and only on a user-defined one; every extra interface goes through
`docker network connect`, which means the machine comes up on one address and
acquires the others later. An emulated server with two private NICs, ordinary on
Scaleway, therefore cannot be started in one shot with both addresses known.
Incus takes them as device keys, at launch and afterwards, so the address the API
published is the address the machine carries from its first boot.

**Enforceable rules.** This is the decisive one. A security group has to be
something other than documentation, or the emulator repeats the flaw every local
AWS emulator has: MiniStack states that "security group rules are stored but
never filter traffic", and floci publishes host ports through socat sidecars
while its own documentation admits "the source CIDR value itself is not
enforced". Docker offers no rule layer: enforcing anything means writing iptables
or nftables rules into a container namespace by hand, then keeping them
consistent with the control plane. Incus has `network acl` natively, with ingress
and egress rules carrying action, protocol, ports and source, attachable to a
network or to a single NIC, enforced by the daemon. A security group maps onto it
almost term for term.

**A real machine when the test needs one.** A container shares the host kernel,
so it cannot carry a sysctl, a kernel module, or a systemd unit touching the boot
path. `incus launch --vm` gives a genuine KVM machine with its own kernel, which
Docker cannot do at all.

The cost of keeping Docker was not the 244 lines of driver: it was that every
network feature would have had to be written twice, once properly and once in a
degraded form, and that the degraded form would have set the ceiling for what the
emulator could honestly claim. Podman is not a substitute either; it shares the
same model. What is lost is real and accepted: Docker starts in under a second
where an Incus container takes a few, Docker is installed on more machines, and
CI images carry it more often. `--vm off` remains the default, so nothing in the
conformance suite depends on any runtime being present.

## The firewall enforces, within stated bounds

A security group here filters real packets, which is unusual enough to be worth
stating precisely, along with what it does not do.

**What is enforced.** The group's default policies and its rules become an Incus
network ACL attached to every interface of the machine. A port no rule opens
refuses the connection, authorising one opens it without a restart, and revoking
it closes it again. All four are checked end to end against a live daemon.

**What this requires.** `security.acls` on a bridged NIC exists from Incus
**6.0.4** onwards. On 6.0.0, which Ubuntu 24.04 ships and will not move past, the
option is rejected outright and nothing is enforced. An ACL attached to a
*network* is accepted from 6.0, but only filters between the bridge and the
host, so it cannot separate two servers of the same subnet, which is precisely
what a security group is for. With an older runtime the emulator still serves
the whole product, and filters nothing: the log says so, once per group.

**What has no equivalent here.** Nothing in the Scaleway rule shape, as it
happens. A rule carries a protocol, a direction, an action, an `ip_range` and a
port range, and every one of them translates. There is no rule sourced by
another security group to worry about: that is the AWS model, where
`UserIdGroupPair` exists; `instance/v1.SecurityGroupRule` has only `ip_range`.
The `stateful` flag translates too, onto the runtime's `allow` and
`allow-stateless`.

The bound is that none of this applies to a server with no backing machine,
which is the default configuration and what CI runs.

The group covers a **flexible IP**. The address is routed through the NIC
device's `ipv4.routes` (`nic_bridged.go` at v7.2.0 lists it among the device's
own fields and applies it host-side), so host traffic towards it crosses the
same bridge port the rule set filters. Measured with a deny-all group: fifteen
runs, public address dropping every time, and the counter-proof both ways —
authorise the port and the same probe connects, revoke it and it drops again.

An earlier version of this file reported the opposite: that in the conformance
suite the public address answered through a denying group. That was the suite
misreading its own probe, not the firewall: a dropped connection is killed by
`timeout` with an empty output, which the assertion then matched as a successful
answer. The verdict of every probe in `network.sh` is now an exit code.

## Subnet isolation depends on the runtime mode

Upstream, two private networks of two different VPCs do not reach each other.
Whether the emulator delivers that depends on which `--vm` mode backs it.

**With `--vm incus` (bridges), they reach each other.** The cause is the
runtime's, and it is documented: "traffic between managed bridge networks on
the same server isn't NATed as it's routed directly between the bridges". Two
attempts were measured. A rule set attached to the network holds when nothing
else is attached, and stops holding once the NICs carry a security group of
their own. Adding the foreign blocks to that NIC rule set did not hold either.
Both are in the tree, because they cost nothing and separate the simple case;
neither is trusted, and the conformance suite reports the state rather than
asserting a separation that is not there.

**With `--vm incus-ovn`, they do not.** An OVN network is a logical network
with its own router: another OVN network is simply not on it, so the
separation is the topology's, not a rule's. Measured before any code was
written: two OVN networks, one instance on each, ten probes per direction,
zero connections, with the control probe confirming the listener answered.
Two networks of one routing VPC are joined the runtime's own way, with
`network peer` — five probes, five connections once peered. The conformance
suite asserts the cross-VPC separation as a hard failure in this mode, where
the bridge mode keeps it a documented skip.

Measured again on 2026-07-29, both modes end to end: **16 checks green on
`incus` with the isolation one skipped, 17 green on `incus-ovn` with nothing
skipped for want of a capability.** Exactly one assertion of the whole suite
changes verdict with the mode, and the second skip that remains in both — a
machine carrying an address the API does not publish — is not mode-dependent.

Since that run the suite no longer compares a mode name: it reads
`capabilities.isolation` from `/_feint/health`, so a runtime that *declares*
isolation and fails to deliver it is a hard failure, and one that declares none
is skipped rather than silently passed. What a mode can prove is declared by the
driver (`machine.Capabilities`) instead of inferred from its name.

The mode has prerequisites the bridge mode does not — `ovn-central`,
`ovn-host`, and an Open vSwitch pointed at the local northbound socket — so it
is asked for by name — but `--vm auto` tries it *first* and falls back to the
bridge, which is the reverse of what this paragraph said until the ordering was
fixed. The old order was backwards for the reason that matters here: an operator
who had installed all three got the one mode that cannot isolate two VPCs, and
nothing told them. What auto chose is printed at startup, with its isolation
capability beside it.
Three behavioural differences are accepted and worth knowing. A security
group's `drop` default answers as a reject on an OVN NIC (the NIC's own
default for unmatched traffic; the port is closed either way, but an RST is
visible where upstream is silent), because the NIC-level default-action keys
cannot be changed on a live OVN NIC without re-plugging it —
`UpdatableFields` in the runtime's `nic_ovn.go` at v7.2.0 lists no
`security.*` key but `security.acls` itself (the rest are `limits.*` and
`connected`), and the re-plug was measured to cost the guest every
address it carried. A flexible IP still rides the NIC's own route keys as in
bridge mode (`ipv4.routes.external`, whose l2proxy ingress mode answers ARP
for the address on the uplink and delivers packets carrying the public
destination), but since those keys are not live-updatable either, attaching
or detaching one bounces the interface for an instant while the driver
restores the guest's addresses. And between two servers of one subnet whose
groups both restrict traffic, the sender's group wins: the runtime evaluates
every NIC rule set in a single pipeline where an egress allow (priority 300,
or 111 for a default) is tested before the receiver's ingress default
(priority 100) — the constants are in the runtime's `acl_ovn.go` at v7.2.0,
and the bypass was measured before being believed. The emulator narrows the
gap the only faithful way available: a group that enforces nothing, such as
the default security group, attaches nothing, so the common case — one
restrictive group probed from an unrestricted neighbour — filters exactly as
upstream does. Across two subnets the receiver's policy always holds, because
traffic enters through the router port and no sender rule matches there.

## A private NIC cannot be hot-plugged into a running virtual machine

`--vm incus-vm` gives each server its own kernel. It does not give it a private
NIC after boot: Incus refuses to add the device to a running virtual machine,
and says why.

```text
Failed to start device "eth1": Failed adding NIC device:
GenericError: PCI: slot 0 function 0 not available for virtio-net-pci,
in use by virtio-balloon-pci,id=qemu_balloon
```

Measured on Incus 7.x with the emulator's own fixture: a server created, started
and then attached to a private network. The container modes attach it without
trouble, because a veth pair needs no PCI slot.

Two things follow, and both are in the code rather than only here.

**The NIC says so.** When the runtime refuses the attachment, the private NIC is
answered as `syncing_error`, which is a state `PrivateNICState` declares. It used
to stay `available` while the failure lived in a log line, so the API published
an address through IPAM that the guest never carried — the one failure this
project exists to avoid. Polling the guest for three minutes confirmed it never
took the address.

**The address is applied on the guest's own interface, once its agent answers.**
Two separate defects were fixed on the way to measuring this one: the driver
passed Incus's device name (`eth1`) into a guest that names the interface
`enp6s0`, and it configured the interface before the virtual machine's agent had
started, which answers `VM agent isn't currently running` rather than "not
running". Both are fixed and tested through the injectable runner; neither is
enough to work around the PCI limit above.

The order that does work with `incus-vm` is to attach before the first boot,
which is what `terraform apply` does when the NIC is part of the same plan as
the server.

## DryRun answers, and does not validate

Outscale declares `DryRun` on every action, and the real API uses it to say
whether the request *would* have been accepted: it validates the arguments and
answers without changing anything.

This emulator honours the first half. The flag is answered at the mount point,
before the handler runs, so nothing is created, started or deleted — which is
the property that matters for a host, and the one a client relies on to probe
safely.

It does not honour the second half. A dry run of a malformed request answers 200
here and 400 upstream, because no handler ever sees it. A client using `DryRun`
as a validator therefore learns nothing from this emulator, and a client using it
to avoid a side effect is served correctly.

The reason it is not implemented per handler is measured rather than aesthetic:
the first attempt honoured the flag inside six handlers, and Outscale declares it
on all twenty served actions. `DeleteVms --DryRun true` destroyed the machine —
a control implemented per handler was missing from exactly the destructive ones.
Answering at the mount point cannot have that gap, and gives up validation to
get there.

`TestDryRunReachesNoHandler` holds the half that is served.

## The Exoscale Terraform provider is refused, and why

`terraform apply` works against Scaleway here, through the provider's `api_url`
attribute. It does not work against Exoscale, and the reason is not something
this emulator can fix.

`exoscale/exoscale` 0.70.0 builds **two** clients in `pkg/provider/provider.go`:

```go
// egoscale v3
if ep := os.Getenv("EXOSCALE_API_ENDPOINT"); ep != "" {
    opts = append(opts, exov3.ClientOptWithEndpoint(exov3.Endpoint(ep)), ...)
}
```

and an egoscale **v2** client, created with no endpoint option at all. The
variable is therefore honoured for one and ignored for the other.

An apply does not fail cleanly and does not work: it **splits**. Some resources
answer from this emulator, and the rest are created on the real cloud, in the
same run, with whatever credentials the environment holds.

Measured on 0.70.0 without a byte leaving the machine — outbound traffic routed
to a proxy that was not listening, so the attempt is visible and cannot succeed:

```text
Error: Post "https://api-ch-gva-2.exoscale.com/v2/ssh-key"
       proxyconnect tcp: dial tcp 127.0.0.1:4740: connect: connection refused
```

with `EXOSCALE_API_ENDPOINT=http://127.0.0.1:4733/v2` set. The emulator saw
nothing.

**There is no endpoint setting to reach for.** `tofu providers schema -json`
lists five provider attributes — `key`, `secret`, `timeout`, `environment`,
`sos_endpoint` — and their own documentation lists the same five. `sos_endpoint`
is Object Storage only; `environment` composes a `%s-%s.exoscale.com` domain.
Neither points anywhere local.

**So the emulator refuses that client**, by the user agent it sets itself
(`Exoscale-Terraform-Provider/…`), with a message saying what is happening. Half
serving it is the worst of the three outcomes: a half-success is
indistinguishable from working until the invoice arrives.

The refusal can be lifted by someone who understands the split and wants the
half this emulator can serve:

```bash
FEINT_EXOSCALE_ALLOW_TERRAFORM=1 feint serve
```

That variable is named rather than hidden on purpose. A guard with no way past
it gets worked around by copying the emulator, which teaches nobody anything.

The `exo` CLI is unaffected and is driven by the conformance suite: it reads
`EXOSCALE_API_ENDPOINT` for everything.

Closing this properly needs an endpoint option on the provider's v2 client,
which is upstream work. Until then, `feint env exoscale` prints the warning on
stderr, where `eval` cannot swallow it.

## A terminated Vm stays terminated, and stays

Deleting an Outscale Vm here leaves the record readable, reporting
`State: terminated`, and it stays that way until the emulator restarts.

The visibility is not a convenience, it is required. The Terraform provider
answers `DeleteVms` by polling `ReadVms` until the Vm reports `terminated`; a
record that vanished makes it read an empty list, and the plugin crashes outright
— "Plugin did not respond", on every destroy. The real API keeps a terminated Vm
readable for the same reason: a state a client waits for has to be observable.

What differs is how long. Upstream, a terminated Vm disappears after a few
minutes. Here it never does, because this emulator has no clock of its own to
expire anything on and inventing one would mean a background timer whose only
purpose is to make a resource vanish while a test is looking at it.

The practical consequences, none of them silent:

- `ReadVms` with no filter lists terminated machines. A client counting machines
  must filter on `VmStates`, which this pack serves — that is what the filter is
  for, and it is why refusing an unserved filter matters.
- A terminated Vm holds nothing: it is ignored when a Subnet is deleted and when
  addresses are counted. Otherwise `terraform destroy` failed on the Subnet,
  naming the machine it had just terminated.
- `feint restart` clears them, like everything else: the store is in memory.

## Outscale and Exoscale are starters

Both packs exist to prove the core stays protocol-neutral: three genuinely
different dialects, one store, one port. Outscale covers the machine lifecycle,
its inventory and its addressing plane; Exoscale its instances, its catalogue and
its SSH keys — enough for the official CLI to create, list and delete end to end.
The generated tables in the README carry the current counts; this paragraph
deliberately does not. Adding to them is ordinary work
(see the `provider-pack-author` skill), not a redesign.

## The contracts do not guarantee the same thing

Every response the emulator writes is checked against the provider's own API
description. The three descriptions are not equally strong, and the artefact
under `contracts/` records which case it is rather than leaving a reader to
assume they are alike.

The table below is generated from those artefacts by `feint docs`, and
`feint docs --check` fails when it drifts. That is not decoration: this section
previously read *"Exoscale — assumed. 7 of 299 schemas carry it"*, a denominator
that had gone stale and a numerator nobody could recompute. Rewriting it by hand
produced a worse number still — 464 of 468 — which measured the extractor's own
`--assume-closed` flag rather than anything Exoscale declares. A figure that
counts your own assumption and reads like evidence is the exact failure this
project exists to avoid.

## `feint start` detaches on Linux, and not on macOS

`start` backgrounds the emulator itself, with no `&` and no container runtime,
and the README makes a point of it: a JVM or a CPython process cannot cleanly
daemonise, a static Go binary can.

Measured on 2026-07-30, on this repository's first public CI run: on both macOS
runners (`macos-15`, Apple Silicon, and `macos-15-intel`) the detached process
exits immediately and `feint start` reports it. `feint serve` in the foreground
serves the three control planes there normally, which is what the cross-platform
job asserts.

So the claim holds where it was measured, Linux, and nowhere else yet. On macOS,
run `feint serve` and background it yourself. The cause is not diagnosed and no
fix is promised here; what is promised is that the page says which platform the
sentence was measured on, which is the whole difference between a claim and a
proof.

<!-- contracts:start -->
<!-- Generated by `mise run docs:coverage`. Do not edit by hand. -->

| Provider | API description | Schemas | Unknown fields |
|---|---|--:|---|
| Exoscale | `2.0.0` | 468 | *assumed* by this emulator |
| Outscale | `1.41.0` | 650 | **declared** by the provider |
| Scaleway | `instance/v1, vpc/v2, ipam/v1, iam/v1alpha1, marketplace/v2` | 263 | **declared** by the provider |
<!-- contracts:end -->

**Declared** means the provider wrote `additionalProperties: false` themselves:
refusing an unknown field enforces *their* rule, and a violation is theirs to
answer for. **Assumed** means their document is silent and this emulator closes
the schemas anyway — a deliberate choice, because a field their document does not
describe is a field this project has no way to emulate faithfully, but one that
is the emulator's own and could be wrong. The distinction is recorded so nobody
reads the second as the first.

Scaleway sits apart again: its descriptions are generated from protobuf, where
the wrapper types (`google.protobuf.StringValue` and its family) carry no
`additionalProperties` at all because the concept does not exist upstream. The
extractor unwraps them, which is why the policy reads declared without a count
that would mean anything.

## The drift report only covers started products

The CI gate watches the products the emulator has begun serving. Asking it to
account for all 1700 upstream operations would fail forever and train everyone to
ignore it. Widening the scope is a decision to make when a product is started,
not before.

## Exoscale has one zone, and the reason is the client

The API description enumerates eight zone names and this emulator publishes one:
`ch-dk-2`. It is not an oversight and it is not laziness — it is what the
official CLI forced.

Serving a zone the CLI does not default to makes every unflagged command fail
before it calls anything: `exo` resolves its zone first and stops on
`find zone: not found in ListZonesResponse`. So the emulated zone has to be
`ch-dk-2`, the CLI's own default.

Serving all eight was the obvious fix and it was worse. The CLI queries **every**
zone it is told about and merges the answers, so eight zone entries pointing at
one emulator turned a single instance into eight identical rows in
`exo compute instance list`. A resource duplicated per zone is a defect a user
sees on their first command.

The consequence, stated rather than hidden: a client asking for `ch-gva-2` gets
`no such zone`, where the real cloud would serve it. That is a visible, honest
difference. The alternative — one endpoint pretending to be eight zones — is a
silent, wrong one.
