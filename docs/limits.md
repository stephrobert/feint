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
accept. For a long time this document called that *"a project of its own"* — an
estimate nobody had made, which is the wrong shape for a refusal here. #76 asked
for the number; the section below is that number, measured. The short version:
the certificate half is cheap and safe, the DNS half is neither, and the whole
blocker reduces to this one product on this one client.

The SDK and the CLI are better off: they honour `SCW_S3_ENDPOINT`. So an S3
workflow driven by `scw` or by an SDK can already point at MinIO today; only the
Terraform path is blocked.

The consequence has since been observed live, from a stranger's stack rather
than a fixture ([#262](https://github.com/stephrobert/feint/issues/262),
[examples/stacks/surveyed.md](../examples/stacks/surveyed.md)): with
`SCW_API_URL` pointing at this emulator, the provider still sent its
`CreateBucket` to the real `s3.fr-par.scw.cloud`, which answered 403 on the
fake credentials. Nothing was created and nothing was billed — but the request
left the machine. A configuration carrying `scaleway_object_bucket` talks to
the real endpoint no matter where the rest of it is pointed, and any sentence
here promising that traffic never leaves your machine has to carve out this
one product on this one client.

## The cost of DNS/TLS interception, measured (#76)

The refusal above rested on an unmeasured cost. Measured against the real
clients on a hardened Ubuntu workstation, without a byte leaving the machine (a
loopback HTTPS server standing in for the cloud), it breaks into four numbers
and one surprise: **the halves are inverted**. #76 wrote the cost as *"DNS
interception plus a certificate the provider will accept"*, as if both were the
hard part. The certificate is the easy, safe half; the DNS redirect is the hard,
dangerous one.

### 1. How many endpoints are actually hardcoded: one product, one client

The blocker is *"an endpoint built in code that no setting overrides"*. Swept
across three providers' Terraform providers, CLIs and Go SDKs, that set is
**one**:

| product | client | reachable by a setting? |
|---|---|---|
| Scaleway Object Storage | Terraform provider | **no** — `newS3Client` hardcodes `https://s3.<region>.scw.cloud`, virtual-host, no env var, no attribute |
| Scaleway Object Storage | SDK, `scw` CLI | yes — `SCW_S3_ENDPOINT` |
| Exoscale SOS (object storage) | `exo` CLI, Terraform provider | yes — `sos_endpoint` / `--sos-endpoint`, honoured by both |
| Outscale (all served) | oapi-cli, Terraform provider | yes — `endpoints.api` / `OSC_ENDPOINT_API` |
| every compute/network API, 3 providers | all clients | yes — `SCW_API_URL`, `EXOSCALE_API_ENDPOINT`, Outscale `endpoint` |

So the coverage cap #76 worried about is real but narrow: it is **Object Storage
through Terraform, on Scaleway**, and nothing else. Not a dozen scattered
endpoints — one product, one client. MinIO plus `SCW_S3_ENDPOINT` already covers
the SDK and CLI S3 paths; only this one corner needs DNS/TLS.

(The Exoscale Terraform provider's v2-client split is a *different* defect — a
missing `ClientOptWithAPIEndpoint` call, not a hardcoded host — and it is
DNS-independent: an endpoint option fixes it, filed upstream as
[#573][exo-573]. It does not belong on this list.)

### 2. What each client needs to accept a locally minted certificate — proven

A local CA was minted with the standard library and an HTTPS listener stood up
on loopback. Then each official client was pointed at it. The results, from the
server's own handshake log:

| client | knob that works | proven by |
|---|---|---|
| `scw` (Go) | `SSL_CERT_FILE` | `scw instance server create` completed end to end over local TLS |
| `exo` (Go) | `SSL_CERT_FILE` | `GET /v2/zone` handshake accepted; refused as `x509: unknown authority` without it |
| **terraform-provider-scaleway** (Go plugin) | `SSL_CERT_FILE`, **inherited from `terraform`'s env** | `terraform apply` created 5 resources over local TLS; the plugin is a separate process and it saw the parent's `SSL_CERT_FILE` |
| `curl` | `SSL_CERT_FILE`, `--cacert`, `CURL_CA_BUNDLE` | 200 with, refused without |
| `oapi-cli` (static binary, own trust store) | **none of the CA env vars**; only `--insecure` | ignored `SSL_CERT_FILE` and `CURL_CA_BUNDLE`; irrelevant here, Outscale has no hardcoded endpoint |

Two things settle the certificate question:

- **The Terraform plugin inherits the environment.** This was the open doubt in
  #76 — a provider plugin is a child process go-plugin spawns — and it is
  answered: `SSL_CERT_FILE` set before `terraform apply` reached the Scaleway
  provider and it trusted the CA. So the durable, disqualifying option — a CA
  installed into the operator's *system* trust store — is **not needed** for any
  Go client. One process-scoped environment variable does it.
- **`SSL_CERT_FILE` is scoped to the one command.** It dies with the process,
  touches nothing else, and leaves no trace — exactly the property this tool's
  pitch (*no account, no bill, no trace*) requires.

### 3. Whether a DNS server is needed — and the real blocker

No. A DNS *server* is the expensive answer and the measured case does not need
it. What it needs is to make one hardcoded name resolve to loopback, and that is
where the cost actually lives, because on a modern hardened Linux there is **no
per-process, disposable, unprivileged** way to do it for the exact client that
matters:

| mechanism | scope | verdict for the Scaleway S3 case (AWS SDK Go v2, static, pure-Go resolver) |
|---|---|---|
| `curl --resolve` / `--connect-to` | one command | works — proven landing `s3.fr-par.scw.cloud` and `<bucket>.s3.fr-par.scw.cloud` locally with a wildcard cert — but **curl only**; the SDK has no equivalent |
| `HOSTALIASES` (glibc) | one process | only the cgo resolver, and only single-label names — **useless for a dotted FQDN** |
| `LD_PRELOAD` getaddrinfo shim | one process | only cgo-resolver binaries. Measured: `scw` is dynamically linked with cgo getaddrinfo (interceptable); `exo` is static pure-Go (not). Terraform providers build `CGO_ENABLED=0` — **not interceptable** |
| network namespace + bind-mounted `/etc/hosts` | disposable | **works, with one named AppArmor profile.** The station sets `apparmor_restrict_unprivileged_userns=1`, and the first measurement read `unshare -r` → `EPERM` as "namespaces are blocked". Re-measured: the namespace is created and root maps fine; what the restriction removes is *capabilities inside it*, so `ip link add` answers `RTNETLINK: Operation not permitted`. `tools/install/apparmor/feint` grants `userns` to one binary path and the interface is created — see below |
| edit `/etc/hosts` | whole machine, persistent | works for every client, but it is a **durable change to the operator's machine** — the thing the pitch forbids |

So for the one blocked client — the pure-Go, statically linked AWS SDK inside the
Scaleway Terraform provider, which offers no `--resolve` — the cheap scoped
mechanisms (`curl --resolve`, `HOSTALIASES`, an `LD_PRELOAD` shim) all miss it,
each for a different reason. What reaches it is a network namespace, and that
one is available.

#### The namespace row was wrong, and the correction is worth reading

The first measurement concluded that this station blocks unprivileged user
namespaces, from `unshare -r` answering `EPERM`. Re-measured with the exit codes
read properly — the earlier reading took `$?` through a pipe, which is the
false-verdict shape this repository keeps meeting — the three questions separate:

| asked | answer |
|---|---|
| `unshare --user --net true` | **0** — the namespace is created |
| a uid map putting this user at root inside it | **works** — `id -u` answers 0 |
| `ip link add dummy0 type dummy` inside it | `RTNETLINK answers: Operation not permitted` |

So the restriction does not stop the namespace. Ubuntu transitions the process
into the stock `unprivileged_userns` profile, whose first line is
`audit deny capability` — the namespace exists and is powerless, which is a
better design than refusing outright and reads exactly the same from a script
that only checks whether `unshare` failed.

**The fix is one named profile, not a sysctl.**
`tools/install/apparmor/feint` grants `userns` to the feint binary path and
nothing else, in the same shape the distribution ships for `lxc-unshare`,
`bwrap` and a dozen others. Setting `kernel.apparmor_restrict_unprivileged_userns=0`
would lift the restriction for *every* program on the host; this lifts it for one
path.

Measured on a witness binary, both ways: with the profile loaded the interface is
created; unload it and `RTNETLINK: Operation not permitted` comes straight back.

```bash
sudo install -m 0644 tools/install/apparmor/feint /etc/apparmor.d/feint
sudo apparmor_parser -r /etc/apparmor.d/feint
```

Nothing in feint requires it. Without the profile, the paths that need a
namespace refuse and say so; every other command works unchanged.

### 4. What the standard library gives for free

The certificate half is as cheap as #76 guessed. A CA, a leaf covering
`s3.<region>.scw.cloud` **and** `*.s3.<region>.scw.cloud`, and an HTTPS listener
are under 100 lines of pure `crypto/x509` and `crypto/tls`, **no dependency**.
One caveat the wildcard exposes: `*.s3.<region>.scw.cloud` is single-level, so a
bucket name containing a dot (`my.bucket.s3.<region>.scw.cloud`) is not covered —
measured, `curl` refuses it. There is no DNS server in the standard library, but
per number 3 the measured case does not need one.

This is no longer a projection: `internal/proxy/intercept.go` mints exactly that
CA and leaf from the standard library alone, and `feint proxy --intercept` serves
the recorder over TLS with it. See number 6.

### 5. What is dangerous when it is on — and it is not the certificate

A process that trusts a feint-minted CA **and** resolves a real cloud hostname to
loopback is exactly how a real `terraform apply` silently hits a local emulator.
The measurement moves the danger: the certificate is safe because `SSL_CERT_FILE`
is process-scoped, but **the name redirect is the hazard**, and it is the half
that resists being scoped. `/etc/hosts` is machine-wide and persistent; an entry
left behind sends the operator's next *real* apply to a dead local port (loud) or
a stale emulator (silent, and the exact failure this project exists to avoid).
Whatever is ever retained here must scope the redirect as narrowly as the cert —
a devcontainer with its own hosts file, an explicit and temporary entry the
operator makes and removes — and must never install a CA into the system store or
edit `/etc/hosts` on the operator's behalf. Number 6 is how that scoping is done
in practice: the redirect lives in a namespace the client owns, never in the
operator's own `/etc/hosts`.

### 6. Built, and where the redirect is now permitted

The two halves stopped being a projection. `feint proxy --intercept
<host>[,<host>]` serves the recorder (see `docs/proxy.md`) over HTTPS with a
certificate `internal/proxy/intercept.go` mints from the standard library alone:
a short-lived, non-CA leaf covering the names a redirected client will address. It
writes the CA to a temporary file for `SSL_CERT_FILE`, prints the redirect recipe,
and removes the CA on exit. The scoping number 5 demands is structural here — the
command installs nothing into the system store and writes no `/etc/hosts`, because
it has no code that could.

That leaves one question, and number 3 answered it for the station: where the
*name* resolves to loopback, disposably and without a durable trace. Measured
across the places an operator actually has, rather than assumed:

| place | verdict | how, and what it costs the machine |
|---|---|---|
| a rootless container (podman, docker) | **works, no host trace** | the container has its own network namespace and its own `/etc/hosts`; `--add-host=<name>:host-gateway` redirects the name and `SSL_CERT_FILE` carries the CA. It reaches the namespace through the setuid `newuidmap` helper, a path the `apparmor_restrict_unprivileged_userns` sysctl does not touch, so it needs no profile — measured here, `grep <name> /etc/hosts` on the host stays empty afterwards |
| feint's own user + network namespace | **works with the named profile** | `tools/install/apparmor/feint` (number 3); feint itself opens the namespace, so the profile covers it. A separately-launched `unshare` or `ip` is not covered, by design, and that is the right architecture: a tool that asks the operator for `sudo unshare` has already lost *no trace* |
| Incus container (`--vm incus`) | **works** | a container is a namespace; already piloted by this repository |
| a GitHub hosted runner (`ubuntu-24.04`) | **the container path works; feint's own namespace needs the profile** | measured, not assumed (workflow run `31791022679`, `.github/workflows/userns-probe.yml`, image `20260810.271`, kernel `6.17-azure`): the runner carries the *same* `apparmor_restrict_unprivileged_userns=1`. Bare namespace creates (`exit 0`), the root map is refused (`exit 1`) — an unconfined `unshare` is as powerless here as on the station. Rootless podman works out of the box and leaves no host trace (`grep` on the host stays empty), so the container row is the portable CI path. feint's own namespace would need `tools/install/apparmor/feint` loaded first, which a runner permits through passwordless sudo |

The through-line: in every permitted place the redirect is as disposable and as
narrowly scoped as `SSL_CERT_FILE` itself. It lives in a namespace the operator's
next *real* apply never enters, and it vanishes when that namespace does — which is
exactly what number 5 says any retained redirect must do.

**Why this was built now: #92, not S3.** The driver was the recorder, not Object
Storage. `feint proxy` records a real client by being its configured endpoint, but
a cloud that republishes its own address in a response body walks the client away:
Exoscale's `GET /v2/zone` hands back `https://api-ch-gva-2.exoscale.com/v2`, the
client follows it, and a session worth about ninety exchanges recorded **eight**
(#92). The plain proxy cannot hold a client it does not resolve for. Interception
can: with the republished name resolving to the proxy in a namespace of its own and
the CA trusted, the client follows the republished address straight back and the
whole session is kept. `TestInterceptionRecordsThePostHandoffExchanges` reproduces
it without an account — one session driven twice against the same recorder, the
republished name resolving to the proxy, then away — and records the whole thing
one way, only the pre-handoff exchange the other. Eight-of-ninety, and the fix, on
one run.

### The verdict

**Refused, now with numbers behind it — and the refusal changes shape twice.**
Object Storage through Terraform stays out, but no longer for the reason first
written. The certificate was never the project it was called: it is `feint proxy
--intercept`, under 100 lines of standard library, accepted by every Go client
including the Terraform plugin through one process-scoped environment variable. And
the name redirect, first read as undeliverable without touching the machine, is
deliverable after all — in a namespace the client owns (number 6): a rootless
container, or feint's own namespace under one named profile. What is left is not a
feasibility wall but an operator ceremony — the client must run inside that
namespace — and whether the S3-through-Terraform corner is worth that ceremony is a
product call, the only thing still holding the refusal.

The blocker is one product on one client, so the MinIO + `SCW_S3_ENDPOINT` page
remains the right answer for the S3 workflow, and the refusal caps coverage by
exactly one corner rather than quietly bounding the whole project. If
Object-Storage-through-Terraform is ever wanted, it is a roadmap item with a
named owner and a shape already measured: `SSL_CERT_FILE` (safe) plus an
explicit, disposable name redirect the operator opts into (a devcontainer or a
temporary hosts entry) — never a system trust-store install, never a hosts file
this binary edits itself. Whether that corner is worth the operator ceremony is a
product call; it is no longer an unmeasured one.

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

**What an unknown identifier can no longer do is boot a substitute.** Measured
in #83, on all three packs: with a runtime configured (`--vm incus`, `incus-vm`,
`incus-ovn`), an image identifier no catalogue held was silently replaced at
boot — ask for Alpine, boot Ubuntu — while the API kept reporting the identifier
the client sent. Scaleway's resolution matched labels by substring, so `centos`,
`rocky` and `ubuntu_focal` all became Ubuntu 22.04 without a word.

Since then the create still succeeds and the boot refuses: the machine reaches
the provider's own failed state (`stopped` on Scaleway and Outscale, which
declare no error state for a machine; `error` on Exoscale) and the emulator's
log names the identifier. The state published is the one the effect produced,
not the one the intention aimed at. With `--vm off`, the default, nothing boots
and nothing changes — the control plane keeps accepting, exactly as this
section promises.

Two alternatives lost, and why:

- **Refusing at the create**, as the real clouds do, would turn the emulated
  catalogue into a whitelist and break the paragraph above: a configuration
  hardcoding a production image UUID must keep applying, because that is the
  first thing a team points at the emulator.
- **Substituting out loud** — a warning in the log, a mark on the resource —
  keeps a machine whose `/etc/os-release` contradicts the API for as long as it
  runs. A cloud-init that installs an Alpine package, or a playbook that
  branches on the OS family, still gets the wrong operating system with every
  signal saying success. A boot that fails with a stated reason is the only
  answer that cannot be misread.

An identifier resolves to nothing in **two** ways, and they end in the same
refusal without being the same case. An identifier nobody ever created is a
typo the control plane accepted, as above. An image the client **registered** —
Outscale serves `CreateImage`, and Scaleway snapshots and images are planned to
follow it (#7) — is the more embarrassing one: `ReadImages` lists it, yet this
emulator keeps records, not disk contents, so there are no bytes to boot.
Booting the source's base image instead would silently drop whatever the client
baked into the image — and the golden-image workflow is precisely the one where
that difference is the point — so it is refused like the first case, and the
log says which of the two it was. If the emulator ever captures disk contents
(the runtime could: `incus publish` exists), that refusal is the line to
replace.

Two details follow from the same decision. The Scaleway marketplace answers one
fixed UUID **per label**, so Terraform — which resolves a label into a UUID and
sends the UUID back — still names the distribution it chose; a single shared
UUID is how `image = "debian_bookworm"` used to boot an Ubuntu. And image and
login resolve **together**: whatever a pack resolves an identifier to carries
the login that image provisions — root on Scaleway, `outscale` on Outscale, the
template's own `default-user` on Exoscale — because the right distribution with
the wrong login is still a machine nobody can enter.

## A Scaleway server's root volume type: what is writable, and what is not

**`sbs_volume` works since SW-3.** `tools/conformance/scaleway/terraform/`
declares it, and the apply, the empty second plan and the destroy all pass. The
limit this section used to describe — no usable value at all — is over.

It is worth keeping how it read, because the fixture is the part that stings:

> Omitting the block is the way through, and it is what
> `tools/conformance/scaleway/terraform/` does — which is why the suite is green
> and shows none of this.

A fixture that avoids the one input that breaks is a test that cannot fail. The
fixture now declares the block, and would go red if the fallback stopped working.

Measured by @vde-dis on #8, with OpenTofu 1.12.5 and `scaleway/scaleway` 2.80.0:

- **`b_ssd` will not plan**, and that is upstream's decision, not this
  emulator's. From provider 2.79 on it is refused before any request leaves:
  *"b_ssd volumes are not supported anymore. Remove explicit b_ssd volume_type,
  migrate to sbs or downgrade terraform."*
- **`sbs_volume` used to plan for ever**, because the emulator overrode the type
  to `b_ssd` and the value read back never matched the value sent. It is now
  honoured: the disk is created in `block/v1`, and the provider reads it back
  through the fallback it always used — `instance.GetVolume` first, then
  `block.GetVolume` on a typed 404.
- **The local types (`l_ssd`, `scratch`) are still overridden**, and that has its
  own reason, unchanged: the emulated catalogue declares
  `volumes_constraint.min_size` at 0 and the CLI sums local volumes against it,
  so attaching one would make the CLI refuse the very creation it just asked for.

What is still not emulated behind an SBS volume is the storage itself: the size,
the class and the IO/s are recorded and answered, and nothing is written
anywhere. `perf_iops` is a number a client reads back, not a rate anything
measures.

Volume encryption is refused rather than faked: `kms_key_id` names a key in
Scaleway's Key Manager, which this emulator does not serve, so a create carrying
one is rejected. Accepting it would let a client read its own key back from a
volume nothing encrypts.

## Lifecycle transitions are immediate

A server goes from `stopped` to `running` within the action call. Real hardware
takes a minute; reproducing that delay locally would only make every client wait
for information that does not exist here.

The states clients *check* are preserved: deleting a running server is refused
with `transient_state`, because Terraform depends on that error.

**What follows from this is that a refusal which only exists during a transient
state cannot be reproduced**, and one has been measured. Against a real Outscale
account on 2026-08-08: `CreateVolume` answers `State: "creating"`, and a
`CreateSnapshot` issued before the volume settles is refused with
`409 InvalidVolumeState` (code `6007`); a snapshot is born `in-queue` with
`Progress: 0` and only later `completed`. Here a volume is `available` and a
snapshot `completed` at once, so that refusal never fires and a script which
snapshots a volume immediately succeeds locally and can fail on the real cloud.

Serving it was tried and reverted, and the reason is worth keeping: reproducing
the refusal requires the transient state to exist, and any guard written for a
state this emulator cannot reach is a control that can never fire — the "a
comment is not a control" defect, in code rather than in prose.

The line, for whoever adds the next resource: **a state invariant is served when
the state it names is reachable here.** `LinkVolume` on an already-linked volume,
`DeleteVolume` on a linked one, retyping a running machine, deleting a Net that
still holds a subnet — all reachable, all refused, all tested. A refusal that
would need an artificial delay to become reachable is not served, and belongs in
this list instead.

## What survives a dead emulator, in one table

The store is memory: a dead process loses every emulated resource, and that is
the model, not a defect. The machines are different. A container the runtime
started does not die with the process that asked for it, so the policy below
exists, and it is deterministic — measured on 2026-08-13 by running every row
that can be triggered (`tools/conformance/crash.sh` triggers them on every run
where a runtime is configured).

| Event | The store | The machines, networks and rule sets |
|---|---|---|
| Graceful exit (Ctrl-C, SIGTERM, `feint stop`) | lost, and **said out loud**: `feint stop` names the count it is about to discard when no `--state` was recorded (saved first with `--state`) | stay, labelled `user.feint.provider` |
| Graceful exit with `--cleanup` | lost (saved first with `--state`) | swept before exit, counted out loud |
| SIGKILL, crash, power loss | lost | stay, labelled — nothing had a chance to run |
| Restart | starts empty, with the same notice `stop` prints, because `restart` goes through it | **named, never adopted**: startup warns "labelled machines from a previous run exist; nothing was adopted, `feint clean` removes them", listing them by name |
| `feint clean` | untouched (it is a separate process) | everything labelled is removed; the runtime is queried, and the sweep reports what it could not remove instead of claiming success |

The store's own line was documentation only until #182. The model was right and
this page stated it, but the sentence was read *after* being bitten: an operator
reaching for `restart` mid-session paid with the whole fixture and learnt why
here, later. `stop` now says it at the moment it happens, on stderr, once, and
only when something is actually lost: with `--state` recorded it stays quiet,
because a warning on every healthy stop is the pattern people are trained to
ignore.

Why restart never adopts: the store that gave those machines meaning died with
the previous process. A machine resurrected from the runtime would be state
without an owner — trusted for the same bad reason a restored snapshot used to
be trusted, and `snapshot.go` documents where that leads. The startup notice
names the leftovers precisely so the operator decides, with
`TestStartupNamesTheLeftoversItDidNotAdopt` holding the line and
`tools/conformance/crash.sh` proving the whole sequence — kill, survive
labelled, warn, sweep to zero — against a real runtime, the runtime queried
directly rather than the store.

The notice keys on machines. Networks and rule sets alone stay silent: an
empty emulated bridge is reused under its own name by the next run or refused
as a block conflict out loud, the OVN uplink is deliberately kept across runs,
and a warning that fired on every healthy restart would train everyone to
ignore it — the exact way a gate dies, already measured on this repository.

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

## A public address is the provider's value, made to answer on the host

Two defensible answers existed for which address a client reads on a server
(#116): the runtime address the machine got from its bridge, or the fictional
one the pack allocated from `203.0.113.0/24` (TEST-NET-3, RFC 5737). This
emulator publishes **the fictional one, and routes it for real**: the address
`public_ips[0].address` reports is the address the machine carries on its
interface, reachable from the host that runs the emulator, filtered by the
server's security group. `ssh root@203.0.113.2` opens a shell.

The rejected option — publishing the runtime address — is rejected for three
measured reasons. It desynchronises the two views of one attachment, since
`GET /ips/{id}` must keep answering the address it allocated, and the real API
never lets `server.public_ips[].address` differ from the flexible IP it names.
It changes with the `--vm` mode and the operator's host, so the same test would
read different "public" addresses on two machines, which is the half-truth this
project exists to avoid. And it was never needed: the network conformance suite
had already proven the fictional address can genuinely answer through the
group; what #116 measured was two ordering holes, not a wrong address plane.

The two holes, and where they are held:

- **An address attached before the boot was never routed.** `attachAddress`
  ran while the server had no machine and silently did nothing, and nothing
  replayed it at poweron. Now the promised addresses ride the launch as device
  route keys — set while the instance is cold, because editing them on a live
  OVN NIC re-plugs the device and the guest loses its DHCP lease with nothing
  left to renew it — and poweron replays the guest half.
  `TestPowerOnRoutesAnAddressAttachedBeforeBoot` and
  `TestPublicAddressesAreRoutedBeforeTheFirstBoot` hold it.
- **A machine with no private NIC had no lawful interface for the route.** It
  used to boot on the operator's default profile bridge, which the driver
  rightly refuses to route through (`mustOwn`), and covering that NIC with a
  firewall meant *overriding* a profile device — a re-plug after boot that cost
  the guest its DHCP lease: `incus list` showed RUNNING with no IPv4 at all.
  Machines with no attachment now boot on the emulator's own labelled network
  (`fnt-default`, `10.209.84.0/24`, deliberately obscure like the OVN uplink's
  block), created on first use and removed by the sweep.
  `TestAMachineWithNoAttachmentBootsOnTheEmulatorsOwnNetwork` holds it.

`dynamic_ip_required` follows the same mechanism (#117): poweron allocates an
ephemeral address from the same block — suppressed when a flexible IP is
already attached, which is upstream's own precedence — publishes it as a
`dynamic: true` entry of `public_ips`, and releases it on stop, standby,
terminate and delete alike. It never appears in `/ips`, because upstream never
lists it there. `TestADynamicAddressFollowsThePowerCycle` holds the cycle.

Bounds, stated rather than implied:

- The address answers **from the host that runs the emulator** (and from the
  emulated machines). It is a documentation address on purpose; nothing routes
  it beyond the host, and that is the point — a test that half-works against
  the real internet is worse than an address that visibly goes nowhere.
- A **subnet-internal** address — Outscale's `PrivateIp`, Exoscale's
  `public-ip`, both of which this emulator fills with the machine's own
  address — answers the host in bridge mode and not in OVN mode, and the
  runtime declares which (`capabilities.private_from_host`). The cause is
  isolation's own machinery: the OVN router that separates two VPCs by
  construction also SNATs the host's connections on the way back, so the
  handshake never completes — measured, sshd up and answering its neighbours
  while the host read the port as closed. The routed public plane crosses
  that boundary in both modes, which is why every pack's ssh chain logs in
  through it: a Scaleway flexible IP, an Outscale `LinkPublicIp`, an Exoscale
  elastic IP — each is genuinely routed to the machine, and each pack draws
  from its own RFC 5737 block (TEST-NET-3, -2 and -1 respectively) so two
  emulated clouds on one host can never route the same /32 to two machines.
- On a **virtual machine** (`--vm incus-vm`), the host half of the route is in
  place from the first boot, and the guest half — the address on the guest's
  own interface — lands on the first read after the agent answers, the same
  read that publishes a VM's address.
- An address attached to a **running** server in OVN mode still bounces the
  NIC for an instant (the route keys are not live-updatable there; the driver
  restores what it can, and a DHCP-owned lease is the runtime's to re-issue).
  Attach before boot when the order is yours to choose; it always is in
  Terraform, where the IP and the server share a plan.
- A stored address — a flexible IP's, a dynamic one — is revalidated before it
  reaches the driver: one outside the emulated block is refused and logged,
  never routed, because a restored snapshot carries these values verbatim.
  `TestAPoisonedStoredAddressIsNeverRouted` holds the refusal.

### One address reaches one machine, and Exoscale is why that needs saying

Exoscale's Elastic IP is designed to be held by **several instances at once**: an
Elastic IP carries a healthcheck, and the platform sends the address to whichever
instance is passing it. Measured on `ch-gva-2` rather than assumed — `exo compute
instance elastic-ip attach` was accepted twice for one address, and both instances
then reported holding `185.19.28.243`.

The emulator keeps that in its control plane, because it is what the real one
does: both instances go on listing the Elastic IP, and a client reading either of
them sees what Exoscale would show.

**The runtime cannot follow.** Two containers answering ARP for one `/32` make the
host pick arbitrarily, and an emulator that publishes an address its machine may
or may not carry has given up the one thing it exists to promise. So the address
is routed to **the most recently attached instance**, and taken back from the
previous holder before it is handed over.

That rule is feint's, not Exoscale's. There is no healthcheck here, and inventing
an election would put a winner in a client's hands that nothing measured. If you
are testing a failover, the emulator will not perform it: attach the address to
the instance you want to reach.

The same bookkeeping serves all three packs (`machine.Binding.RouteAddress`), so
the runtime carries one address on one machine whatever the pack's API allows —
Scaleway moving a flexible IP on request, Outscale refusing without `AllowRelink`,
Exoscale accepting both holders.

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

Nor is there one deeper in the client. `egoscale/v2` reads no environment
variable of its own, and the `.exoscale.com` suffix is compiled into
`v2/api/request.go`. The option that would do it, `ClientOptWithAPIEndpoint`,
**exists in `egoscale/v2` and is never called by the provider** — a grep over
`exoscale/` and `pkg/` returns nothing. Three sites build a v2 client without
it: `CreateClient`, `getClient`, and the plugin-framework provider's
`Configure`.

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
which is upstream work. It is filed as
[exoscale/terraform-provider-exoscale#573][exo-573], with the mechanism, the
three construction sites and a reproduction. Until it lands, `feint env
exoscale` prints the warning on stderr, where `eval` cannot swallow it.

### The patched provider, while upstream decides

The fix is four lines per site, so it is also carried on a fork, pinned:

- [`stephrobert/terraform-provider-exoscale@fix/v2-client-honours-api-endpoint`][fork],
  commit `2e78b42`, branched from `de9d60c2` (0.70.0 plus six commits).

**This recipe is a snapshot, and nothing re-checks it.** It was last verified on
2026-08-11: the branch tip was still `2e78b42` and the build below succeeded.
No gate clones a third-party repository — deliberately, that would put someone
else's availability in this project's CI — so past that date the honest claim
is "it worked then", not "it works". The recipe checks out the measured commit
rather than the branch tip for the same reason: a tip can move under a reader,
a commit cannot. If the build breaks or the fork disappears, check
[exoscale/terraform-provider-exoscale#573][exo-573] first — upstream landing an
endpoint option is the outcome that makes this whole section obsolete, and the
released provider is then the thing to use.

It passes `ClientOptWithAPIEndpoint` at the three sites, and nothing else.
Terraform resolves it without a registry, through `dev_overrides`:

```bash
git clone -b fix/v2-client-honours-api-endpoint \
  https://github.com/stephrobert/terraform-provider-exoscale
cd terraform-provider-exoscale && git checkout 2e78b42
go build -o /tmp/tfp/terraform-provider-exoscale .

cat > /tmp/dev.tfrc <<'RC'
provider_installation {
  dev_overrides { "exoscale/exoscale" = "/tmp/tfp" }
  direct {}
}
RC

eval "$(feint env exoscale)"
export TF_CLI_CONFIG_FILE=/tmp/dev.tfrc
terraform apply          # no `init` for an overridden provider
```

Measured against this emulator, with a security group (v3 client) and an SSH key
(v2 client) in one configuration:

```text
exoscale_security_group.v3_side: Creation complete after 0s
exoscale_ssh_key.v2_side:        Creation complete after 0s
Apply complete! Resources: 2 added, 0 changed, 0 destroyed.
```

Both calls arrived — `POST /v2/security-group` and `POST /v2/ssh-key`, 200 each
on `/_feint/trace` — the second plan was empty, and `destroy` removed both. The
same configuration on the published 0.70.0 creates the security group here and
sends the SSH key to `api-ch-gva-2.exoscale.com`.

`FEINT_EXOSCALE_ALLOW_TERRAFORM=1` is still required: the emulator refuses by
user agent, and the fork does not change the user agent it sets.

**One limit the fork does not lift.** `setEndpointFromContext` in `egoscale/v2`
rewrites the request host from the zone context *unless the configured host is
an IP literal*. The fork is therefore honoured end to end for
`http://127.0.0.1:4599/v2` — which is what `feint env exoscale` prints — and a
**hostname** endpoint such as `http://gateway.internal:8080` would still be
rewritten back to `*.exoscale.com`. Closing that half is a change in `egoscale`,
not in the provider.

**It does not count towards conformance, and must not.** The north star of this
project is that *the official client cannot tell the difference*; a client this
project patched is no longer the official client. What the fork proves is real
and worth having — that the rest of the emulated Exoscale surface holds under
Terraform, and that #573 is the only thing in the way — but it is a weaker claim
than a route driven by a published client, and adding the two together would
repeat the error `probed` exists to avoid. Exoscale's *preview* label came off
on what `exo` proves, at EXO-2, and not on this.

[exo-573]: https://github.com/exoscale/terraform-provider-exoscale/issues/573
[fork]: https://github.com/stephrobert/terraform-provider-exoscale/tree/fix/v2-client-honours-api-endpoint

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

## An Exoscale block volume holds no bytes, and every number it publishes says so

The thirteen block-storage operations (EXO-4) are a control plane. A volume is a
record carrying a size, a state and the instance holding it; nothing is
allocated, nothing is written, and no machine gains a disk when one is attached.
A `--vm` run does not change that — the machine driver knows nothing about this
product, and a volume attached to a running container is a fact of the API and
of nothing else.

What follows from it, stated rather than left for a reader to find out:

- **A snapshot is instantaneous and costs nothing**, because it copies nothing.
  Its `size` and `volume-size` are both the volume's declared size — there is no
  compression to model, and a ratio invented here would be fiction a client could
  compute against.
- **`blocksize` is 4096 on every volume.** One storage class, one number; a value
  varying per volume would be arithmetic nobody measured.
- **`encrypted` is always true**, which is Exoscale's own default rather than a
  claim about this emulator. No byte is encrypted because no byte exists.
- **A resize is a number changing.** The refusal to shrink is real and enforced,
  because that one has a consequence a client can hit; growing is a field write.

What *is* enforced is every relation a client's plan depends on: one volume
attaches to one instance and refuses a second, an attached volume refuses its
delete, a volume with snapshots refuses its delete, and a detach of nothing is an
error rather than a success. Those are the rules a `destroy` walks in order, and
they are driven by `exo compute block-storage` on every pull request.

**One refusal's wording is load-bearing, and only Terraform could show it.** The
provider's destroy calls detach unconditionally, and tells a tolerable refusal
from a real failure by reading the message:
`strings.HasSuffix(err.Error(), "Volume not attached")`. So this emulator answers
exactly that sentence. It first answered *"the volume is not attached to an
instance"* — the same fact, refused the same way, and `terraform destroy` died on
`unable to detach volume` for every volume that had never been attached. `exo`
cannot show this: it does not detach before deleting. Measured against the
patched provider [pinned below](#the-patched-provider-while-upstream-decides),
which is what that fork is for — a check by hand that does not count towards
conformance, and did find something.

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

### What a green contract run does and does not prove

The schema check is one-directional: it catches a field a response *invents*,
and it can only catch an *omitted* field where the provider declared it
`required` — which Scaleway does on 9% of its schemas, Outscale on 27%,
Exoscale on 35%. On the rest, an omission never violates anything.

Since #88 the other direction has its own control, and its precision was
measured before its semantics were chosen. Every answer a conformance run
provokes is compared with the full property list the provider's document
declares; an absence fails the run (`fields.missing` on `/_feint/conformance`,
gated by `tools/conformance/score.sh`) **only when a recorded real-cloud answer
carries the field too**. The corroboration is not caution, it is arithmetic:
on the first instrumented run, of the 106 declared-but-absent fields a
recording could arbitrate, 83 were absent from the real cloud's answer as well
— pagination tokens that only exist when a further page does, client tokens
echoed only when sent. A gate that failed on all of that would be red on
purpose, and a gate that is red on purpose is a gate somebody turns off.

What no source can arbitrate is published rather than failed on:
`fields.unconfirmed` lists, per operation, every field only the document
vouches for — 317 fields across 97 operations at the time of writing. Each
entry is one recording away (`feint shapes --record`) from becoming either a
failure or nothing. That list is this page's kind of sentence: it names
exactly what nobody has proven.

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
| Exoscale | `2.0.0` | 473 | *assumed* by this emulator |
| Outscale | `1.42.0` | 655 | **declared** by the provider |
| Scaleway | `instance/v1, instance/v2alpha1, vpc/v2, ipam/v1, iam/v1alpha1, marketplace/v2, block/v1, block/v1alpha1` | 476 | **declared** by the provider |
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

## What the browser check covers on the page, and what it does not

The page under `/_feint/ui` is held by five things. Four read the asset as text
and one runs it, and the difference matters when reading a green check.

Read as text, in Go, on every `go test`:

- `TestTheEmbeddedPageNamesNoProvider` greps the shipped files for the names the
  packs declare, so a provider name written into the page fails the build.
- `TestThePageNeverBuildsMarkupFromAString` refuses `innerHTML` and its family,
  which is the cloudinit lesson applied to HTML.
- `TestEveryNodeTheScriptWritesToExists` ties every identifier the script looks
  up to an element the document carries.
- `TestThePageAddsOnlyGETRoutes` enumerates the mux — about the server rather
  than the page, and the only one a rewrite of the script could not defeat.

Run, in a real browser, by `tools/ui/screenshots.sh` (`mise run docs:ui`), on
every pull request in CI and on demand locally:

- the page is loaded against a live emulator, and the harness waits until it has
  rendered that emulator's data;
- **eighteen assertions compare what the document displays with what the
  endpoints answered**: the routes mounted, driven, probed and never proven; the
  driver name and the resource count; a created resource with its identifier, its
  kind and one of its attributes; a refusal reason, in full, in the product it
  belongs to; a call in the log with its path, the field no handler read, and the
  one that found no route mounted; the number of rows in the drill-down behind
  the headline figure; and that the page threw no exception on the way.

So a renamed node, a region that fails to render, a number written into the wrong
element, or a script that throws halfway now fail CI. That is the hole this
section used to describe, and it is closed for the values above.

**Where the guarantee stops**, said plainly because a reader will take this as a
promise:

- It is a smoke test of one state of the page, not a test of its behaviour. The
  harness clicks three things — one legend button, one product, one resource —
  and asserts what appears. Pausing the log, the "problems only" filter, the
  search box, the theme toggle, the reconnect after a dropped stream and the
  no-flicker refresh are all exercised by nobody.
- It asserts values, never appearance. A stylesheet that renders every card
  invisible, illegible or overlapping passes: the nodes still carry the right
  text. Only a human looking at the images catches that.
- It runs one browser engine. Chromium is what CI has; Firefox and Safari are
  unmeasured, and this page uses `color-mix()`, `<details>` styling and
  `EventSource`, all of which are supported everywhere and none of which anyone
  here has checked outside Chromium.
- It needs a browser. Without one the script exits 3 and says so — loudly, never
  as a silent pass — and on that machine the page's DOM is simply unchecked.

## The screenshots are gated on the page, never on their pixels

`docs/assets/ui/*.png` are regenerated by `mise run docs:ui` and committed. The
freshness gate is `feint docs --check`, which the pre-commit hook, `mise run
docs:check` and `tools/release/preflight.sh` already run — one rail, not a second
one somebody has to remember.

What it compares is a digest of the three files the page is made of, recorded in
`docs/assets/ui/manifest.json` when the images were written, against the page the
binary serves. Change the stylesheet without regenerating and the gate fails.

What it does **not** compare is the images themselves, and that is a decision
rather than an omission. This page renders wall-clock values by design — the time
of each call, the age of each resource — so two captures a second apart differ;
and the same capture taken on a workstation and on a runner differs again,
because font rendering does. A gate demanding byte equality would be red
permanently, and a permanently red gate gets disarmed, which is worse than no
gate because it still looks like a control.

The consequence to hold on to: the pictures are guaranteed to be *of this page*,
and nothing guarantees they are *good pictures of it*. That is a human's job at
review time, and the images are in the pull request diff for exactly that.

## `feinttest` does not run on GitHub's arm64 runners

`feinttest.Start` pulls the published image and runs it through the local
container runtime. That works on this station, on amd64 CI runners, and under
`docker run --platform linux/arm64` with qemu — and it does not work on
`ubuntu-24.04-arm`, where the container starts and dies at once:

```
feinttest: the emulator never answered on http://127.0.0.1:33437: context deadline exceeded
container exited (code 255), and it wrote nothing
```

Both tests of the package, twice, on 17 August 2026. The exit code and the empty
log are the whole of what is known: the image is a multi-arch index carrying a
real arm64 binary — `docker manifest inspect` lists `linux/arm64`, and that
variant serves its routes here under emulation — so the runner's runtime refuses
something the binary is not doing. Nothing available here reproduces it, and a
guess written down would be the kind of sentence this document exists to avoid.

**What follows from it.** The arm64 leg of the test matrix does not set
`FEINT_TESTCONTAINER`, so the package skips there. Every other leg sets it, and
`go test ./...` on a developer's machine skips it too unless asked — the package
reaches a registry, and `mise run check` is offline by contract.

The diagnosis above is itself a fix: the first version of this package passed
`--rm`, so a container that died was gone before anything could read it and the
CI printed `No such container` where the reason should have been.
`TestAFailedStartSaysWhatTheContainerDid` is the control, and
`tools/falsify/specs/container-diagnosis.json` puts the flag back and requires
it to fail.

**What is not affected.** The image itself. `feint start`, the OCI image as a CI
service, and the composite action all run on arm64; this is about a Go test
driving a container runtime, on one class of runner.

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

## A declared query parameter is served or refused, never dropped — and `labels` is the refused one

`GET /v2/template?visibility=private` used to answer the public catalogue,
each entry declaring `"visibility": "public"` inside a response filtered to
`private` (#271). The cause was a handler that discarded its request, so no
query parameter could have an effect — and the same signature sat on four more
Exoscale operations whose contract declares filters. All five now read what
their operation declares: `visibility` and `family` on list-templates,
`visibility` on list-security-groups, `manager-id`, `manager-type` and
`ip-address` on list-instances, `instance-id` on list-block-storage-volumes,
and list-events validates its `from`/`to` window (over an audit trail that is
empty by design, see above). A gate holds the rule from here on:
`TestDeclaredQueryParametersAreRead` fails any route whose contract declares
query parameters while its handler never reads the query.

Two answers differ from the real cloud, and both are the honest half of a
choice rather than an accident:

- **`labels` on list-instances is refused with a 400**, not implemented. Their
  document types it as a bare string with no format and no description,
  egoscale v3 exposes no option that sends it, so any wire encoding this
  emulator picked would be an invented format — the thing rule 4 exists to
  forbid. A client that sends it learns so at the moment it happens;
  the real cloud would filter. `TestInstanceListRefusesTheLabelsFilter` holds
  the refusal.
- **`?visibility=public` on list-security-groups answers an empty list.** The
  real cloud publishes public security groups of its own; this emulator
  publishes none, and listing the private groups under a public label would be
  the same lie #271 names, pointed the other way.

## The per-parameter half: 18 Scaleway list operations, 72 parameters, each served or refused (#277)

#271's gate catches a handler that never reads its query at all. Its comment
names what it cannot see — a handler that reads *some* declared parameters and
drops the rest — and #277 measured that residual class on this pack: every list
read its page, so the gate was green, while `?order_by=created_at_desc`
answered ascending. The per-operation gate
(`TestNoDeclaredQueryParameterIsDroppedByItsHandler`) now requires every
declared parameter to be named in its own handler's call graph, and everything
it found is served, with these decisions where the emulator's model and the
real cloud part ways:

- **Every `order_by` (and instance/v1's `order`) is served with the SDK's
  documented default.** A bare list answers `created_at_asc` where block, vpc
  and iam declare it, `created_at_desc` where instance and ipam do — including
  a bare `scw instance server list`, which now answers newest first like the
  real API. A value outside the operation's enum is a 400 naming the
  parameter, never a silently different order.
- **`order_by=attached_at_*` on ipam ListIPs is refused with a 400.** This
  emulator records no attachment time, and sorting by a stand-in would answer
  an order nobody asked for.
- **`include_deleted` on block lists is read and never widens the answer.**
  Deletion here is immediate — the store retains no `deleted` volume or
  snapshot for the flag to reveal. The state filter is real code on the
  default path; the difference from the real cloud, which retains deleted
  volumes for a while, is this line.
- **`s3_integration_enabled=true` matches nothing.** No VPC or Private Network
  here integrates with Object Storage, which is not emulated (see above), so
  true truthfully answers an empty list and false answers everything.
- **`arch` and `type` on marketplace ListLocalImages are equalities against
  the one published image.** `arch=arm64` or `type=instance_local` answers an
  empty list — the catalogue is x86_64 and `instance_sbs` — where dropping the
  filter answered an image of the wrong architecture with a 200.
- **`without_ip=false` on ListServers filters nothing.** The SDK documents
  only the true direction ("list Instances that are not attached to a public
  IP"); a complementary meaning for false would be invented.
- **`disabled` on iam ListSSHKeys is an equality on the field when present.**
  The SDK comment ("defines whether to include disabled SSH keys") could also
  read as a widener over a default exclusion, but nothing upstream states that
  default, and a bare list here has always answered every key.
- **`organization`/`organization_id` filters scope to the whole account.**
  One organization lives here and identifiers are unchecked by decision (see
  above), so a named organization resolves to that account — `scopeOf`'s
  long-standing rule, now applied to every list that declares the parameter.
  The equality reading was tried first and the CLI refuted it within the
  hour: `scw iam ssh-key list` names its configured organization on every
  call, and nothing obliges a client's configuration to spell the emulator's
  constant, so comparing answered "no keys" about keys the same client had
  just created. `project`/`project_id` stay real filters: a project is an
  isolation boundary this emulator does honour.

`TestServersHonourTheDeclaredOrder`, `TestServersFilterByLinks`,
`TestBlockListsHonourTheDeclaredFilters`, `TestVPCListsHonourTheDeclaredFilters`,
`TestIPAMListHonoursOrderAndResourceFilters`,
`TestInstanceListsHonourTheirRemainingFilters`,
`TestSSHKeysHonourTheDeclaredParameters` and
`TestLocalImagesHonourTheirDeclaredParameters` ask with non-default values —
the whole class survived every suite that only asked for defaults — and
`tools/falsify/specs/scaleway-list-parameters.json` proves each family's test
red without its fix.

## ReadTags does not list an internet service, because upstream names no type for one

Outscale's `Tag` carries a `ResourceType`, and their OpenAPI declares it as a
bare string. The values come from the SDK instead, where they are a deliberate
patch: `TagResourceType` in `osc-sdk-go/pkg/osc/client.gen.go`, twenty of them,
listed in the `enum` block of `patch.yaml`.

An internet service is not among the twenty — and their `InternetService` schema
declares `Tags`, which the Terraform provider sets. Both are true at once, and
they cannot both be honoured in the flat view.

So the emulator splits the two questions rather than picking one:

- **`CreateTags` and `DeleteTags` accept it.** This is what
  [#99](https://github.com/stephrobert/feint/issues/99) was: the pack answered
  `the resource igw-… does not exist` about a resource it was serving, and an
  `outscale_internet_service` with a `tags` block failed its apply.
- **`ReadTags` leaves it out.** Every row of that view carries a
  `ResourceType`, and there is no value upstream declares for this one.
  Inventing `internet-gateway` because AWS spells it that way is the invented
  format rule 4 forbids, and it would be indistinguishable from a measured
  value to anyone reading the answer.

The tag is not lost: `ReadInternetServices` returns it on the resource, which is
where the provider reads it back. `TestReadTagsOmitsAKindUpstreamDoesNotName`
pins both halves.

This is one row of `taggable` in `internal/providers/outscale/tags.go`, the only
one with an empty type. If Outscale adds a value, that field is where it goes.

## Outscale's gateways and NAT move records, not packets

`InternetService`, `LinkInternetService`, `NatService` and the routes that
name them are served, and none of them makes a packet flow.

`LinkPublicIp` is no longer on that list: a linked address is routed to the
Vm's machine, answers from the host that runs the emulator, and
`ssh outscale@<PublicIp>` opens a shell — the outscale ssh conformance suite
drives exactly that. The limit was real while the machines sat on the
operator's default bridge, which the driver rightly refuses to route through;
they boot on emulator-owned networks now.

The rest is structural rather than unbuilt, and the difference matters because
everything around it was buildable and got built. The emulator has no data
plane beyond that host: a NAT service is a managed appliance in a facility
this machine is not in. A public address allocated here comes from
`198.51.100.0/24` — TEST-NET-2, reserved by RFC 5737 and routed nowhere on
purpose, so beyond this host an address goes visibly nowhere rather than
quietly somewhere.

What *is* real is the resource algebra, and it is what a plan actually depends
on: an address a NAT service holds refuses to be released, a gateway refuses to
be deleted while linked, a Net refuses to go while a gateway is attached to it,
a route through a gateway that is not linked to the Net is refused, and a
subnet refuses to vanish under a NAT service placed in it. `terraform apply`
of the provider's own `examples/net_vm`, its second plan and its `destroy` all
pass against this, which is exactly what those refusals are for.

So: a plan that builds a routable topology applies, reads back and destroys
correctly. A machine inside it still cannot reach the internet. Use the emulator
to test the shape of your infrastructure, never its connectivity.

## An Outscale Vm's options round-trip as data; their behavioural half has nothing to act on here

`BootMode`, `Performance` and `VmInitiatedShutdownBehavior` used to be
accepted at create with a 200 while every read answered a constant of the
pack — the client asked `medium`/`restart`/`legacy`, the same create's answer
said `high`/`stop`/`uefi`, and a Terraform stack setting any of them
re-planned the same in-place change for ever (#276, the #268 pattern on
per-machine scalars). They are stored and restituted now, on the create and
on UpdateVm where upstream declares them, values validated against their
enums, and `Performance` honours upstream's own precedence: a performance
flag inside the `VmType` (`tinavW.cXrYpZ`) wins over the parameter. The same
sweep covers the neighbours with the same symptom: `TpmEnabled`,
`ActionsOnNextBoot.SecureBoot`, `ShutdownBehaviorConfiguration` (whose
defaults are now the SDK's own "By default" lines — GuestAction `stop`,
HostAction `restart`; the old constant said `stop`/`stop`) and UpdateVm's
`IsSourceDestChecked`.

What is served is the datum. The behavioural half of these fields has nothing
to act on in this emulator, and saying so is the difference between an echo
and a lie:

- `VmInitiatedShutdownBehavior` and `ShutdownBehaviorConfiguration` describe
  what the platform does when the **guest** shuts itself down. No path here
  watches a guest-initiated shutdown — `StopVms` stops the machine whatever
  the field says, which matches upstream, where the API stop is not a
  VM-initiated one. A `terminate` behaviour will therefore never terminate a
  machine here, because the event that would trigger it is never observed.
- `TpmEnabled` and `ActionsOnNextBoot.SecureBoot` round-trip; no vTPM and no
  secure-boot state is presented to any guest the runtime boots.
- `IsSourceDestChecked` round-trips; nothing enforces the check on traffic.
- `BsuOptimized` stays the constant `false` on every read, and that one is
  upstream's own behaviour, not this pack's shortcut: "This parameter is not
  available. It is present in our API for the sake of historical
  compatibility with AWS" (osc-sdk-go client.gen.go:3029).

## An Outscale Net peering carries traffic under OVN, and only there

The `NetPeering` family is served — create, accept, reject, delete, read, with
the SDK's own states (`pending-acceptance`, `active`, `rejected`, `failed`,
`deleted`) and its refusals (accepting or rejecting anything but a pending
one, deleting a rejected or failed one). Three of the upstream behaviours
cannot exist here, and each is stated rather than approximated:

- **One account.** Upstream, the owner of the accepter Net accepts, and a
  pending peering is deletable only by the requester. The emulator's single
  account owns both ends of every peering, so the identity rules are satisfied
  by construction and only the state machine is measurable. An
  `AccepterOwnerId` naming any other account is answered as an unknown Net,
  because in a one-account world that is what it is.
- **`expired` is unreachable.** Upstream it is what seven days of silence
  produce; no clock here advances a state on its own.
- **`failed` has one reachable door.** Upstream it is what overlapping IP
  ranges produce, but this emulator refuses to *create* two overlapping Nets
  in the first place — every Net backs a real block on the host — so the only
  overlap left is a Net peered with itself.

What an **accepted** peering does depends on the runtime mode, same rule as
"Subnet isolation depends on the runtime mode" above:

- **Under `--vm incus-ovn`**, accepting the peering peers the backing networks
  of the two Nets the runtime's own way (`network peer`), and deleting it
  separates them again. The outscale network suite asserts the whole cycle —
  unreachable before, unreachable while pending, reachable once active,
  unreachable after delete — gated on the declared `capabilities.isolation`,
  never on a mode name.
- **Under `--vm incus` (bridges)**, two Nets already reach each other, so an
  accepted peering grants nothing a measurement could see, and the suite skips
  and says so.
- **With `--vm off`**, the whole lifecycle is control plane, proven by
  `oapi-cli` and the Terraform provider.

One deliberate simplification either way: upstream, traffic flows only once
both Nets' route tables carry a route through the peering. Here the acceptance
alone grants reachability, and the route stays a record — the same limit as
"Outscale's gateways and NAT move records, not packets" one section up. Do not
use the emulator to prove a peering's routing configuration; use it to prove
the plan's shape and the state machine.

## A Scaleway VPC route is a record, and the reachability is the peering's

Scaleway's custom routes (`scaleway_vpc_route`, `vpc/v2/API.CreateRoute` and
its family) are served since SW-4, and they are records: create, read, update
and delete round-trip for the Terraform provider, the nexthop is validated to
exist, and no runtime mode programs it. A route sending `192.168.42.0/24`
through an instance NIC is a row the client reads back, not a path packets
follow.

What *is* real is what a routing-enabled VPC delivers between its own Private
Networks, and it does not come from these records: `EnableRouting` reconciles
the machine driver's isolation the moment it flips — under OVN the VPC's
networks are peered, in bridge mode their mutual reject rules are lifted — so
two networks of a routing VPC reach each other and two networks of two VPCs
still do not, mode permitting (see "Subnet isolation depends on the runtime
mode"). `TestEnableRoutingReconcilesThePeering` in
`internal/providers/scaleway` pins that link to the driver.

Same rule as Outscale's gateways, one paragraph up: use the routes to test the
shape of a plan, never to test where traffic goes. The day a nexthop is worth
programming for real, it will arrive as a declared driver capability, measured
under OVN, not as a silent upgrade of these records.

## Only Scaleway hands a security group to the runtime

`internal/providers/scaleway/firewall.go` is the only file in any pack that
references `machine.Firewaller`. An **Outscale** or **Exoscale** security group
is served as a control plane, echoed back, and reconciled onto nothing: with a
runtime configured, every port of the machine stays open whatever the group
says, and the API answers success on every rule.

That was published as the opposite until #180. `(*Incus).Capabilities()`
declares `firewall: true` in every mode, one set for the whole process, and this
repository tells a consumer to key on the capability rather than on a mode name.
Following that advice, a user probed a port a deny-default group should have
closed and found it answering.

`/_feint/health` now carries both halves, and the honest check is their
conjunction:

```console
$ curl -s localhost:4599/_feint/health | jq '{capabilities: .capabilities.firewall, enforced: .enforced.firewall}'
{
  "capabilities": true,
  "enforced": ["scaleway"]
}
```

`capabilities.firewall` is what the runtime can do. `enforced.firewall` is who
asks it to. A pack absent from that list either does not wire it or has not
said, and a consumer cannot tell those apart — which is intended, because both
mean the same thing to whoever is about to open a socket.

## Whether a Scaleway security group filters is not measured under every mode

The security-group family — `CreateSecurityGroup`, its rules, and the groups a
machine and its interfaces wear — is served as a control plane by all three
packs. For Scaleway the rules reach the runtime; whether they are *enforced* on
traffic under `FEINT_VM=incus-ovn` has **not been measured**, and this section
says so rather than claiming a limit.

The distinction is the one the firewall section above makes for Scaleway, where
enforcement is measured within stated bounds. Nothing equivalent has been run
for Outscale groups. The open question named in the roadmap is a rule sourced by
*group* rather than by CIDR, which needs an OVN selector; the emulator's own
`machine.Capabilities` is where an answer would be declared, and it declares
nothing here today.

Until somebody runs it: assume the groups this pack serves describe a policy and
apply none of it, and do not use the emulator to check that a rule blocks
anything.
