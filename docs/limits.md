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
one product on this one client. The escape-path section below (#280) carries
the full measured list, what warns before the run, and the egress cuts that
were tested rather than described.

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

### 7. The Terraform S3 client honours `HTTPS_PROXY` — measured (#346)

Everything above rests on the redirect being the hard half. `feint proxy
--forward` (#336) changed one term of that: a Go client that installs no
`Transport` inherits `http.DefaultTransport`, which honours `HTTPS_PROXY`, so a
compiled-in name can be intercepted with **no DNS trick and no `/etc/hosts`**.
Whether the Scaleway Terraform provider's **S3** client is such a client was
unknown, and #336 said so rather than predicting.

It is. Measured on Linux on **2026-08-21**, terraform 1.15.4 with
`scaleway/scaleway 2.81.0`, against this repository's own emulator — no account,
no real endpoint, the public fake credentials of
`tools/conformance/scaleway/fake-credentials.env`. The recipe is the two
variables and nothing else, plus the mapping #357 added so the terminated host
lands on the emulator instead of the real cloud:

```bash
feint serve --addr 127.0.0.1:4760 &
feint proxy --record s3.jsonl --addr 127.0.0.1:4761 \
  --forward 's3.fr-par.scw.cloud=http://127.0.0.1:4760,*.s3.fr-par.scw.cloud=http://127.0.0.1:4760'
# then, on a scaleway_object_bucket:
export HTTPS_PROXY=http://127.0.0.1:4761 SSL_CERT_FILE=/tmp/feint-intercept-ca-….pem
terraform apply -auto-approve
```

One tunnel terminated, one exchange recorded, and the User-Agent names the
client beyond argument:

```json
{"seq":1,"method":"PUT","path":"/","host":"feint-346-measurement.s3.fr-par.scw.cloud",
 "status":404,"mounted":false,
 "req":{"headers":{"Authorization":"REDACTED","X-Amz-Content-Sha256":"REDACTED",
   "User-Agent":"aws-sdk-go-v2/1.43.4 … api/s3#1.107.0 terraform-provider-scaleway/2.81.0"}}}
```

The `PUT /` is `CreateBucket`, virtual-host style, and the `404` is **this
emulator** answering: feint serves no object storage, so no pack claims that
route (`"mounted": false`). Nothing left the machine.

**Read the two failures apart, because only one of them closes the door.** A
negative result would have to say *which* negative it was, and the control run
shows exactly what the other one looks like. Driven again with the same
environment against a proxy that does **not** name the S3 host:

```text
recorded 0 exchange(s)
0 tunnel(s) terminated
12 connection(s) were refused because --forward does not name their host:
  feint-346-measurement.s3.fr-par.scw.cloud
```

```text
Error: operation error S3: CreateBucket, exceeded maximum number of attempts, 3,
request send failed, Put "https://feint-346-measurement.s3.fr-par.scw.cloud/": Forbidden
```

The client emitted `CONNECT` for the compiled-in name twelve times and the proxy
turned each one down. That is *"arrived, and was refused"* — the door opened. The
other negative, *"did not honour the proxy"*, would have left the proxy with **no
CONNECT at all** and the client complaining about a certificate, which is what
macOS produces (number 2's warning in [proxy.md](proxy.md)) and why this was
measured on Linux.

So the hard half of #76 has a third door, and this one costs an operator
nothing: no namespace, no `/etc/hosts`, no privileged port, no AppArmor profile.
**It does not by itself retain object storage** — what it removes is the
redirect from the cost, not the S3 surface from the work — but the arbitration in
the verdict below now rests on the coverage argument alone, since the ceremony it
weighed has gone to zero. Reopening it is its own issue, with its own numbers.

### The verdict

**Refused, now with numbers behind it — and the refusal changes shape twice.**
Object Storage through Terraform stays out, but no longer for the reason first
written. The certificate was never the project it was called: it is `feint proxy
--intercept`, under 100 lines of standard library, accepted by every Go client
including the Terraform plugin through one process-scoped environment variable. And
the name redirect, first read as undeliverable without touching the machine, is
deliverable after all — in a namespace the client owns (number 6): a rootless
container, or feint's own namespace under one named profile. And since #346
(number 7) it is not even that: the one blocked client honours `HTTPS_PROXY`, so
the redirect costs two environment variables. What is left is not a feasibility
wall and no longer an operator ceremony either — it is the coverage argument
alone: whether the S3-through-Terraform corner is worth emulating an S3 surface,
which is a product call and the only thing still holding the refusal.

The blocker is one product on one client, so the MinIO + `SCW_S3_ENDPOINT` page
remains the right answer for the S3 workflow, and the refusal caps coverage by
exactly one corner rather than quietly bounding the whole project. If
Object-Storage-through-Terraform is ever wanted, it is a roadmap item with a
named owner and a shape already measured, and number 7 shortened it again:
`SSL_CERT_FILE` and `HTTPS_PROXY`, both process-scoped, both proven on that exact
client — never a system trust-store install, never a hosts file this binary edits
itself, and now not even a namespace. What is left to cost is the S3 surface
itself; that is a product call, and it is no longer an unmeasured one.

## A run presented as local can still reach the real cloud (#280)

Every sentence this project writes about locality has to be the one that is
true: **the APIs Feint serves run locally; a client can compose its own
endpoint for a product outside that scope, and then its requests go where they
always went.** From the outside the two runs are indistinguishable — that is
the whole problem — and the failure that would actually hurt is not the 403
the survey measured on fake credentials. It is somebody with real credentials
in their environment — a developer's shell, a CI job that also deploys —
running a stack they believe is sandboxed.

### The measured escape paths, and what says something today

| path | measured | what warns today |
|---|---|---|
| Scaleway Object Storage through Terraform: `scaleway_object_*` hardcodes `https://s3.<region>.scw.cloud` (top of this file) | live on a surveyed stack (#262, flatcar-k3s): `CreateBucket` at the real endpoint, 403 on fake credentials. Reproduced for #280 with egress cut to a dead proxy: **the same apply created its instance IP on this emulator and died on `Put "https://<bucket>.s3.fr-par.scw.cloud/"` — one run, half local, half not** | `feint doctor` and `feint env scaleway`, from the stack directory: the configuration's own text names the resource family |
| an S3 state backend or an aws provider pointed at real object storage | three surveyed stacks: kubic (state on Object Storage, endpoint in `backend.conf`), eu-data-platform (state on `sos-ch-gva-2.exo.io`, inline), platform (aws provider at `sos-<zone>.exo.io`, 403 at the real endpoint) | the same scan, when the host is written in the `.tf` text — the kubic shape keeps its endpoint in a `backend.conf` the scan does not read, and stays invisible to it |
| Outscale, `OSC_PROFILE` set: provider 1.1.x reads `~/.osc/config.json` and ignores `OSC_ENDPOINT_API` | #286, on 1.1.3: the plan left for `api.<region>.outscale.com` while the emulator received nothing | `feint doctor` and `feint env outscale`, since #286 — this one lives in the shell, not in the stack |
| the Exoscale Terraform provider's split client: egoscale v2 built with no endpoint option | #262/#284, section below: an apply splits between this emulator and the real cloud | **the emulator itself refuses that client by user agent** — the one escape that must pass through the front door to do damage, so the front door is where it is stopped |

The scan behind the first two rows reads the Terraform files around `feint
doctor` and `feint env` (`*.tf`, `*.tf.json`, `*.tofu`, comment-stripped, dot
directories skipped) and matches the measured signatures each pack declares —
`internal/providers/*/stackhazards.go`. Warnings, never failures, and three
silence rules the tests hold: a directory whose own top level carries no
Terraform file produces nothing — Terraform only runs where root module files
sit, so a workspace that merely *contains* projects is not a stack
(`TestADirectoryOfProjectsIsNotAStack`, while a rooted stack's `modules/` are
scanned); a commented-out resource produces nothing
(`TestAStackHazardInACommentStaysSilent`) — a warning that fires on dead text
is a warning people learn to ignore; and this repository's own fixtures scan
clean. The `ok` row states its own scope ("checks the measured list") because
that is all it checks.

### What is out of reach, and why no guard here can promise more

Feint controls neither the client's process nor its DNS. `feint proxy` sees
every request that reaches it and, by construction, none that does not. The
scan above reads text, so it cannot see a value passed at runtime
(`-backend-config`, a variable), a module fetched at init, or the next product
whose client composes its endpoint upstream tomorrow — the weekly drift scan
reads SDK surfaces, not endpoint construction, so it will not see that one
either. **In general, the escape is undetectable from inside the emulator, and
an approximate guard would be worse than none: it would license exactly the
belief it fails to protect.** What exists is a measured list, said as such,
plus the one boundary that does not depend on the client cooperating — the
network the run executes in.

### Cutting egress, measured both ways

The tripwire costs one line and no privilege, and it is how the reproduction
above was run — the escape became a loud failure naming its destination
instead of a silent success:

```bash
HTTPS_PROXY=http://127.0.0.1:9 NO_PROXY=127.0.0.1,localhost terraform apply
```

```text
Error: … CreateBucket, … Put "https://feint-escape-repro.s3.fr-par.scw.cloud/":
proxyconnect tcp: dial tcp 127.0.0.1:9: connect: connection refused
```

The emulator, on loopback, is reached directly through `NO_PROXY`; everything
else dies on a proxy that is not listening, and the error names the host that
was contacted. Scope, honestly: proxy variables are honoured, not enforced —
measured on Terraform with the Scaleway provider (above) and on the Exoscale
provider (#284, same technique); a client that ignores them walks straight
past. A tripwire, not a boundary.

The boundary is a network with no route out. Measured on rootless podman
(4.9.3, netavark), no privilege and no host trace:

```bash
podman network create --internal feint-noegress
# feint on it (feint serve --addr 0.0.0.0:4599 --expose-to-network), the client beside it:
# GET /_feint/health → HTTP/1.0 200 OK
# connect s3.fr-par.scw.cloud:443 → "Network is unreachable", immediately
```

One caveat the measurement exposed: on an `--internal` network the container's
DNS still *resolves* real names (aardvark-dns forwards to the host's
resolver), so the cut is at connect, not at lookup — a name leaks as a query,
a connection goes nowhere. In CI the same property is whatever your runner's
egress policy provides; a hosted runner with open egress protects nothing, and
no flag here can change that. That sentence is the honest end of this section:
where the network permits the escape, Feint can at most name the measured
paths before the run — which is what `feint doctor` now does.

## Managed Kubernetes is not emulated, and a CRUD-only version is refused (#283)

Kapsule (Scaleway) and SKS (Exoscale) are the most-demanded unserved surface
the survey measured ([#262](https://github.com/stephrobert/feint/issues/262)):
SKS alone made two of the five Exoscale stacks *not applicable*, and the
survey's own conclusion is that the public Scaleway ecosystem lives in Kapsule,
RDB, LB and Object Storage. High demand does not say what "supporting it"
means, so [#283](https://github.com/stephrobert/feint/issues/283) asked the
question that decides the cost: **how far must the cluster work after it is
created?**

That is measurable, and it was measured twice, on 2026-08-19.

**On the surveyed stacks: three of three continue past Terraform.**
CentraleSupelec/kubic wires `kubernetes` and `helm` providers from
`kubeconfig[0]` and installs nine Helm releases (Argo CD, cert-manager,
Prometheus, Loki, Vault, Velero). datamindedbe/eu-data-platform feeds a `kubernetes` provider from
`exoscale_sks_kubeconfig` and creates namespaces and secrets in the same
layer. camptocamp/terraform-exoscale-sks does not even wait for a provider: a
`null_resource` polls the cluster endpoint's `/healthz` for five minutes and
fails the apply on timeout, then shells out to the `exo` CLI for a kubeconfig.
Not one observed stack treats the cluster as a record.

**On the wild population: about half of Kapsule and two thirds of SKS.** A
GitHub code search for `resource "scaleway_k8s_cluster"` and `resource
"exoscale_sks_cluster"` in HCL returned 127 and 37 repositories. After
excluding the providers' own repositories and demos, verbatim copies, one
Scaleway mock, and collapsing classroom, interview-task and same-author
duplicates into one unit each:

| provider | distinct units | continue into `kubernetes`/`helm`/`kubectl` in the same configuration | stop at cluster + pool + outputs |
|---|---|---|---|
| Scaleway Kapsule | 102 | 50 | 52 |
| Exoscale SKS | 20 | 13 | 7 |

The stop column is a floor, not a population that a CRUD emulation would
serve: it is dominated by classroom exercises, and the substantial stacks in
it output the kubeconfig with instructions to run `kubectl` next
(jpetazzo/container.training's lab harness consumes it seconds later). A
cluster is created to be talked to.

**The verdict, and it is the same for both providers: refused at every level
short of a real control plane.**

- **CRUD-only (create, read, update, delete, answer the stored attributes) is
  refused.** The clients themselves force the lie: `scaleway_k8s_pool` waits
  for the pool — nodes included — to reach `ready` by default
  (`wait_for_pool_ready`, measured in the provider's `pool.go`), and the
  camptocamp module blocks on a live `/healthz`. Serving
  the API without a control plane means answering `ready` for an API server
  that does not exist and issuing a kubeconfig that points nowhere — a lying
  200 by construction, on exactly the field the majority of the measured
  demand consumes one resource later. A 501 naming the product is honest; a
  ready cluster with no API server is not, and that distinction is this
  project's founding rule.
- **Reproducing the managed service** (versions, autoscaling, CNI, CCM, CSI,
  upgrades, maintenance windows) is refused permanently: feint would become a
  different product.
- **The only admissible shape is a real local control plane** behind `--vm`,
  through the machine runtime, handing back a kubeconfig that answers — the
  same rule that makes a public address here "the provider's value, made to
  answer on the host". That shape is not scheduled: it puts somebody else's
  software lifecycle inside this project (the version the API claims versus
  the one the runtime ships, nodepools as real joined nodes, a
  `LoadBalancer` service with no CCM behind it sitting `pending` forever),
  and each of those is a place to start lying at one remove. If it is ever
  built, it is its own issue with those costs measured first.

Until then the refusal is served where a reader's client hits it: the Exoscale
pack declines every `sks-*` operation by name with this reason
(`internal/providers/exoscale/pack.go`), and `/k8s/v1/` answers with a
Scaleway error envelope from the not-served prefix list
(`internal/providers/scaleway/pack.go`). The measured cost of the refusal is
the survey's: platform-shaped stacks stay not applicable, and serving the rest
of their products would not free them — eu-data-platform needs SKS *and*
DBaaS *and* SOS, so it stays blocked whatever
[#284](https://github.com/stephrobert/feint/issues/284) decides.

## The catalogue is a whitelist, and its values are measured

Server types, prices and images are a small fixed table
(`internal/providers/scaleway/catalog.go`). The emulator has no fleet, no
inventory and no price list. It serves a table anyway because the clients read
it before creating anything: a 404 there makes `scw instance server create`
fail outright.

The 0.10.0 survey (#279) measured that the table is more than pre-flight
scenery: **the Terraform provider validates a server's type against
`/products/servers` before it creates anything, so a type outside the table
fails a stack at plan** — two of five surveyed stacks died exactly there.
Three consequences, each a decision this page records:

- **The rows are measured, not invented.** The served types are an excerpt of
  the real answer to `GET
  https://api.scaleway.com/instance/v1/zones/fr-par-1/products/servers`
  (public, no authentication), captured 2026-08-19 and embedded verbatim as
  `catalog_servers.json`: every family the emulator carries, every size of it
  (PLAY2, DEV1, GP1, PRO2), plus `STARDUST1-S`, which a surveyed stack named.
  The one deviation is `per_volume_constraint`, served empty and declared to
  the shapes gate, because a bound for local volumes this emulator never
  attaches would enter the client's size arithmetic with nothing behind it.
- **What the emulator will never enforce about a type stays unenforced.** The
  RAM, CPU count, GPU count, bandwidth and prices of a row are answers, not
  behaviour: nothing meters a byte or bills a cent, and with a machine runtime
  every server boots the same class of container whatever its type claims.
  Treat any capacity, price or availability answer as decoration — it is now
  *accurate* decoration, which is strictly less misleading, and still
  decoration.
- **One table serves every zone, and the real cloud varies by zone.** The real
  fr-par-1 lists 136 types where fr-par-3 lists 41, and `STARDUST1-S` exists
  in three zones of nine. A plan naming `STARDUST1-S` in `fr-par-2` passes
  here and fails against the real region. Zone-accurate inventory would mean
  carrying nine tables of a moving target; the divergence is accepted and
  stated instead.

**The recording of that difference is committed, and it is a value rather than
a shape.** `corpus/scaleway/scw-cli.jsonl` carries the real fr-par-1 answer,
all three pages of it, beside the emulator's own. The 118 types this table does
not stock are therefore measured rather than asserted — and the two gates that
read that recording agree on what they are: **a key of that map is data, not a
field**, which is `transcript.DataKeyed`, shared by `feint shapes --check` and
`feint corpus --check` so the same artefact cannot be graded two ways. Read as
fields they were 127 of the 136 findings the first corpus run produced, saying
one thing 127 times over everything else the file had to report (#355). What
*is* graded is the shape of an entry both sides carry, which is why the missing
`per_volume_constraint.l_ssd` bound is declined explicitly, in both spellings
the gates join on.

**There is no ARM row, and that is a refusal with a reason, not a gap.** The
one ARM type a surveyed stack asked for, `COPARM1-2C-8G`, is absent from all
nine zones of the real catalogue (measured 2026-08-19, every page, while
genuinely end-of-service families — START1, VC1, X64 — are still listed with
`end_of_service: true`): Scaleway withdrew the family, and resurrecting it
here would let a plan pass that production refuses.
`TestTheRetiredArmFamilyStaysRetired` keeps it out. The current ARM families
(`BASIC2-A*`, `STANDARD2-A*`) are real and could be carried the day a stack
asks — but an arm64 row costs more than a paste: the emulated marketplace is
x86_64 (its `arch` filter truthfully answers arm64 requests with an empty
list, #277), and a machine runtime boots containers on the **host's**
architecture, so an arm64 row must arrive with an arm64 image story or its
servers can never boot. The demand-driven rule above applies, with that bill
attached.

**An unknown type is still accepted at create.** The section below argues this
for image identifiers, and the reasoning transfers whole: a configuration that
names a type this table lacks — including a real one the excerpt has not
caught up with — must not die on the one thing that has nothing to do with
what it is testing. The clients that care already refuse client-side against
`/products/servers` (that refusal is the measured wall of #279, and growing
the table is its fix); an emulator-side refusal would add a second wall for
raw SDK users and catch nothing the first does not. The real cloud does
refuse an unknown type, so this is a divergence, recorded here on purpose.

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

### The Scaleway half of the same list, measured on 2026-08-24

A recording of a real `fr-par` account (#427) put three more entries on it, and
all three are one decision seen from three products. **Forty-seven of the
divergences that recording found are this paragraph**, which is why they are
written here rather than patched one at a time.

- **A block snapshot is `available` the instant it is cut.** Upstream it is
  born in a transient state, and a `DeleteSnapshot` issued while it settles is
  refused with `412`. Here the first delete succeeds, so the recorded second
  delete meets nothing (`404` against the cloud's `204`) and the read between
  them finds no snapshot.
- **A load balancer is gone the instant it is deleted.** Upstream the read that
  follows a `DeleteLB` still answers `200`, with `status: to_delete`, and only
  the read after that answers `404`. Here the first read already answers `404`.
- **A public gateway is `running` the instant it is created.** Upstream it is
  `allocating` for a few seconds, and both a `UpdateGateway` and a
  `CreateGatewayNetwork` issued in that window are refused with `409` and a body
  naming the state (`current_state: allocating`). Here both succeed.

The `409` is the interesting one, because it is the shape a client branches on
and this emulator can never answer it: the state that produces it is not
reachable here, which is the rule two paragraphs up. Serving the refusal would
mean inventing the window, and a guard for a state nothing can enter is a
control that can never fire.

### A recording can be short of a request, and this one is

Not a limit of the emulator, recorded here because it reads exactly like one.
In `corpus/scaleway/scw-billed-shapes.jsonl` the public gateway goes from
`200` on its seventh read to `404` on its eighth **with no `DELETE` anywhere in
the file**, and its address follows: the recorded `DeleteIP` answers `404` and
the recorded `GetIP` answers `404` too. The destruction did not travel through
the proxy.

It is stated rather than assumed. The file holds three recording sessions whose
own sequence numbers run 1..14, 1..43 and 1..65 with no gap, so nothing was
dropped between the request that read the gateway and the request that could not
find it; and no `DELETE` on `/vpc-gw/v2/…/gateways/` exists in that file at all.

So the eleven findings on those three exchanges measure the recording and not
the pack: this emulator was never asked to delete the gateway, and answering
`404` for an object nobody destroyed would be the lie the whole project exists
to avoid. They go when the gateway is recorded again with its destruction in
the transcript.

### A server's root volume lives in `instance/v1` here and in `block` upstream

The largest single divergence the 2026-08-24 recording found, and it is one
default. `CreateServer` with no `volumes` in the body is answered by `fr-par`
with `volumes: {"0": {"volume_type": "sbs_volume"}}` — a *block* volume — and
the recording then reads that volume three times through
`block/v1alpha1/API.GetVolume` and deletes it there. This emulator gives such a
server a `b_ssd` volume in `instance/v1`, so all four of those calls answer
`404`: **forty-three findings, one default.**

`sbs_volume` is honoured when a client asks for it, and
`tools/conformance/scaleway/terraform/main.tf` asks for it, so the path itself
is proven end to end by the real provider. What is not done is making it the
default, and the reason is measured rather than assumed: the whole
`instance/v1` volume surface reads a server's root disk out of the instance
store. Flipping the default reds ten tests at once — `CreateSnapshot` and
`CreateImage` cannot find the volume to snapshot, `attach-volume` and
`detach-volume` refuse it, and terminate stops carrying it away. That is a
batch of its own, not a line in a handler, and it is the same shape of decision
as the asynchronous-delete entry above: a lifecycle that belongs to every kind,
changed in one place.

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

### One leftover lives below the table: a DHCP service without its interface

Every row above sweeps *objects* — machines, networks, rule sets the runtime
can list. #316 measured, twice (2026-08-18 and 2026-08-19), a leftover no
object listing shows: a network's interface disappears while its `dnsmasq`
lives on, still bound to the gateway address. `ip addr` shows nothing, `incus
network list` shows nothing; only `ss -lnp` disagrees, and the next run that
wants the block dies minutes in on `dnsmasq: failed to create listening
socket: Address already in use`. Knowing that `ss` is the third place to look
cost three ten-minute runs the first time.

The attribution criterion is stricter than a label, because a process carries
none: a `dnsmasq` is the emulator's leftover only when its `--interface`
carries the `fnt-` prefix only this emulator derives **and** that interface no
longer exists. One whose interface is alive is somebody's working service,
whoever owns it — this station runs libvirt's and two other Incus projects'
`dnsmasq` beside feint's, and none of them is ours to name, let alone signal.

Four consumers share the one check (`internal/core/machine/leftover.go`):
`feint doctor` reports it and touches nothing; `feint clean` ends it, after
re-checking at the moment of the signal that the pid still is that leftover
(pids are reused); `feint clean --check` answers the same question without
ending anything, which is what the conformance suites ask before they start;
and the network-create error names the process when its listen address falls
inside the failing block. `TestLeftoverDHCPRefusesAProcessItCannotAttribute`
holds the refusal, and `tools/falsify/specs/dhcp-leftover-ownership.json`
proves the test bites.

**Nothing here escalates, and the remedy is a command rather than a paragraph.**
The runtime's `dnsmasq` runs as the `incus` user, so an ordinary sweep cannot
signal it: `feint clean` says so and exits 1 instead of claiming a clean host,
and every suite that takes an address block asks `feint clean --check` on its
doorstep (`guard_leftovers`, `tools/conformance/guard.sh`) rather than meeting
the state twelve steps into a run. That was measured — the runtime leg of
`mise run evidence:update` failed three times in a row, each time after every
client suite had already run, with the right remedy printed and nobody running
it (#375).

What none of them does is acquire a privilege it did not have. A conformance
suite that escalated to end a daemon it did not start would be a worse defect
than the one it works around: it is the question `mustOwn` asks of the driver,
one layer up, and a process nobody here created is not ours to end. So the
elevation is the operator's, in one line — `sudo feint clean --vm <mode>`, the
same sweep run by somebody who may signal it, re-asking every ownership question
at the moment of the signal. The permission probe behind `--check` is signal 0:
the kernel runs the check it would run for a real signal and delivers nothing,
which is the only acceptable shape for a question whose subject belongs to
somebody else.

One half of the remedy stays manual, and it is the #342 case above: when the
bridge survived alongside its service, ending the service leaves the interface
holding the same address, and nothing on the host proves this emulator created
that bridge. Both commands are printed; only the second needs a human to decide
the bridge is theirs.

**A leftover is not always debris of an earlier run, and a doorstep cannot
prevent the other kind.** #316 and #342 both measured leftovers surviving a run;
on 2026-08-21 the machines-on leg of `mise run evidence:update` produced one of
its own, in bridge mode, from a network it had created minutes earlier. The
runtime listed that network as unmanaged while its bridge and its `dnsmasq`
stayed up, and the emulator's log names the moment it broke:

```text
could not isolate the subnet's network network=fnt-8488bc9e9e1
  error="detach isolation from fnt-8488bc9e9e1: incus network:
         open /var/lib/incus/networks/fnt-8488bc9e9e1/dnsmasq.raw:
         no such file or directory"
```

The same run's log carries the load that produced it: NIC ACL writes failing on
`Unknown or missing host side veth device`, instance updates refused with
`Instance is busy running a "delete" operation`, a network delete refused with
`The network is currently in use`. So a network object can die under a bridge
this emulator created while the bridge and its service stay up, and the
doorstep then catches it at the next suite rather than at the start of the leg.

**It is deterministic, and the lifecycle has a name.** The leg was run twice
that evening and failed both times, in the same suite, on a subnet of
`examples/stacks/outscale/main.tf` — `10.50.1.0/24` the first time,
`10.50.2.0/24` the second, both inside the `10.50.0.0/16` that stack declares.
Each failure is preceded by the pair above: two subnets torn down in the same
second, one answering `Network not found` (gone cleanly) and the other
answering `open …/dnsmasq.raw: no such file or directory` — a detach of the
isolation arriving at a network whose state directory the delete has already
removed. The one that answers the second way is the one left standing. Worth
noting because #316's original measurement holds `10.50.2.1`: the three issues
of this family are downstream of the same teardown.

What #375 made cheap is the diagnosis and the remedy; the birth of that
leftover was a separate defect, and #386 is where it was closed. In OVN mode it
does not arise at all: an OVN network carries no `dnsmasq`, and
`FEINT_VM=incus-ovn mise run conformance` passed twice the same evening.

**What closed it: a lock named after the network, and a question asked under
it.** Two requests reach one network because a reconciliation lists the store
and a concurrent delete removes a member it listed — `terraform destroy` tears
down subnets in parallel, so this is the ordinary case rather than the unlucky
one. The driver now takes `serialise.Lock("incus.network." + name)` in
`EnsureNetwork`, `IsolateNetwork` and `RemoveNetwork`, so no config edit of a
network is in flight while its delete runs; and `IsolateNetwork`, holding that
lock, asks the daemon whether the network is still there before it edits
anything. Both halves are needed, and each has its own falsification in
`tools/falsify/specs/teardown-race.json`: the lock alone still lets a delete
that won it run first, and the question alone is a time-of-check a delete
crosses. Per network and never global — a global lock would queue every subnet
of a stack behind one delete, which is the mistake `internal/core/machine/
serialise.go` already records having made once.

A detach that could not happen is reported rather than counted as done:
`machine.ErrNetworkGone` is what the driver returns, and `ReconcileIsolation`
logs it at warn, naming the network. Warn and not error, because no rule set
was needed and none is missing — an error line here would fire on every
parallel destroy and teach a reader to skip it, which is how a log stops being
evidence. The rule set that isolated the network is dropped by the delete
itself, since the pass that used to drop it is now the one that refuses to run
against a network that is gone.

### The other producer of that family: a destruction nobody sent (#426)

The section above reads `The network is currently in use` as one symptom among
the load of a parallel destroy. It is not. It was the whole of a second
producer, and it stayed invisible for four issues because the shape that hides
it is a delete that **answers success**.

The driver had `Attach` and no counterpart. So deleting a Scaleway private NIC
only forgot it in the store, and the device stayed on the container: the API
answered `204` while `incus config device show` still listed the interface.
`DeletePrivateNetwork` then called `RemoveNetwork`, Incus refused with `The
network is currently in use` — correctly, a machine was still on it — the pack
logged that at error level and answered `204` anyway. The bridge, its rule set
and its `dnsmasq` outlived the run holding the block.

Measured on 2026-08-24 by inventorying the host around three consecutive runs of
`tools/conformance/stacks.sh` under `--vm incus`:

| run | exit | what it left |
|---|---|---|
| 1 | **0** | three bridges, three rule sets, three `dnsmasq` |
| 2 | 1 | nothing — it never got that far |
| 3 | 1 | nothing |

Runs 2 and 3 died on `Address already in use` for the blocks run 1 left. So the
failure is not intermittent: it is deterministic with one run of delay, and a
**passing** run is what arms the next one. Which of the three bridges survived
is Terraform's destroy scheduling, which is why the block named moves between
runs — the blocks themselves are fixed in `examples/stacks/scaleway/main.tf` and
are never chosen by the emulator.

Three things changed, and the third is the one that generalises:

- `machine.Driver.Detach`, required rather than optional, asking both ownership
  questions and removing only a device the instance itself carries. Both packs
  that attach now detach; the Exoscale handler had documented the gap as
  unclosable ("the driver deliberately has no hot-unplug"), which is how one
  defect lives in two packs.
- `DeletePrivateNetwork` refuses with `precondition_failed` instead of logging.
  A network reported gone while its bridge holds the block is the same lie as a
  network created while nothing exists, and the create path already refused its
  half.
- **The sweep reads the host after itself.** `feint clean` surveys, prunes, then
  surveys again, and anything present in both was asked to go, said nothing, and
  stayed. That is the only way this class is visible at all: no return value the
  remover produces can report it. `--format json` records one line per object
  with why it stayed, so the question is now "which mechanism produces the
  waste" answered by `jq` rather than by reading four issues.

`feint clean --check --doorstep` refuses a run whose host still holds a previous
run's machines or networks. The flag is separate from `--check` because the two
questions have different safe moments: `guard_leftovers` is asked before a run
starts *and* twelve steps into one, and mid-run those objects belong to the
emulator that is running. Asked at both, it failed a leg for owning what it had
just created — measured, and now held by
`TestOnlyTheDoorstepAsksWhatAnEarlierRunLeft`.

## Authentication is accepted, never verified

No signature is checked, on any provider. Credentials must merely be well-formed,
because the SDKs validate their shape client-side before sending anything.

This means Feint must never be exposed on a network you do not control. It is a
development tool that grants everything to everyone, by design.

**A refusal can now be produced on purpose, and that changes nothing here.**
`PUT /_feint/faults` makes a named operation answer 401 or 403 (among others),
which is what lets a client's degradation path be observed at all. It is not
authentication: nothing inspects the credential, the rule fires on the operation
whatever the caller sent, and clearing it makes the same request succeed. An
injected refusal proves what the *client* does with a refusal, never that this
emulator — or the real cloud — would refuse that call.

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

**The bound a routed NIC adds (#337).** A server that joins no private network
gets its public address on a `routed` NIC (#202) — and a routed NIC accepts
**no security option at all**. Measured on Incus 7.2, cold, one key at a time,
and the wording of the same refusal on the 7.3 CI runner:

| device | key | Incus answers |
|---|---|---|
| `nictype=routed` | `security.acls` | `Invalid device option "security.acls"` |
| `nictype=routed` | `security.acls.default.ingress.action` | `Invalid device option …` |
| `nictype=routed` | `security.ipv4_filtering`, `security.mac_filtering` | `Invalid device option …` |
| `nic network=<managed>` | the same keys, together | `Network ACL "…" does not exist` |

The last row is the contrast that settles it: a NIC of a managed network
accepts the option names and complains only about the missing ACL; on a routed
NIC they are not options. There is no other per-NIC filtering mechanism to fall
back to, so **a security group that restricts traffic is not enforced on a
server whose only interface is a routed NIC** — a server with a public address
and no private network. The runtime declares it instead of pretending:
`capabilities.firewall_public_only` is `false` on `/_feint/health`, a rule set
bound to such a machine comes back as `machine.ErrFirewallUnenforceable`
rather than being half-sent, and the pack logs the declared limit as a warning
naming this section. The default security group — pure accept, filtering
nothing upstream — binds nothing anywhere, which is the faithful translation
of "filters nothing" and why an ordinary `scw instance server create` raises
no alarm at all.

A server *with* a private network keeps the full claim: its NICs are on
managed networks, the group attaches to every one of them, and a flexible IP —
routed through the filtered NIC — stays covered, as the paragraph below
measures.

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

## Placement is recorded, never enforced (#285)

A placement group is a scheduling constraint, and this emulator has no
scheduler: with `--vm off` nothing runs anywhere, and with a runtime every
machine is a container or VM on the single host that started feint. There is no
second hypervisor for `max_availability` to spread onto, and `policy_mode:
enforced` refuses nothing here — a server in an enforced spread group boots
exactly like any other. A stack that needs machines to actually land apart is
not testing that property against feint.

The family is served anyway, for the reason security groups are served without
a runtime: everything a driven client does with a placement group is
create, read back and store. Measured on the Terraform provider at both pins
this repository drives — 2.43.0 (the surveyed terraform-talos stack) and
2.81.0 (the conformance fixture) — `policy_respected` is a Computed attribute
the provider only ever `d.Set`s; nothing waits on it, branches on it, or fails
an apply over it.

What keeps the record honest is that one field. `policy_respected` is computed
from the single-host reality at read time, never from the policy's wish:

- `low_latency` reads **true** — machines grouped on the same hardware is the
  one thing this emulator delivers by construction;
- `max_availability` reads **true** until two of the group's servers are
  running, and **false** from then on — two running members of a spread group
  share the only host there is, and saying otherwise is the lie the family was
  declined over before #285.

A server that is not running counts for nothing: it sits on no hypervisor, the
same doctrine that makes the server view answer `location: null` for it. On
the server endpoints (`Server.placement_group.policy_respected`) the value is
pinned **false**, because the SDK documents that the real API always answers
false there.

The same limit covers **Exoscale anti-affinity groups**, served since the
starter pack and used by the surveyed Exoscale stack: membership is recorded
and read back, and no machine is refused for exhausting hosts the way the real
platform refuses one when its anti-affinity group runs out of hypervisors.
Exoscale's API exposes no `policy_respected` equivalent, so there is no field
to keep honest — the record is the whole surface, and this section is where
its non-effect is stated.

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

### An Exoscale instance carries an address its API publishes nowhere (measured 2026-08-24)

`tools/conformance/parity.sh` drives one equivalent request through the three
providers' own surfaces and counts what the **host** carries, read from the
runtime rather than from the API under test. Run against `main` on 2026-08-24
with `FEINT_VM=incus`, it reports four divergences with a single root cause.

| row | scaleway | outscale | exoscale |
|---|---|---|---|
| a machine on a private network, no public address asked for | 1 iface / 1 addr | 1 / 1 | **2 / 2** |
| the same, one public address explicitly requested | 1 / 2 | 1 / 2 | **2 / 3** |

The extra address is `192.0.2.1` on `eth0`. The instance's own read answers
`public_ip: null`, and no other field of the API names it, so it is carried and
published nowhere. It is present from boot: the first run aborted before any
Elastic IP existed and already measured `2/2`, which rules out a leftover from
an earlier attachment.

Two things follow, and the second is the reason this is written here rather than
fixed in passing:

- **The parity claim itself is sound.** Remove that one address and all four
  findings go: both rows read the same interface and address counts on the three
  clouds, and the orphan check reads zero. The equality this suite asserts is not
  too strong for Exoscale.
- **What the fix should be is a product decision, not a patch.** Either the
  machine must not carry the address, or the API must publish it —
  `machines.go` states the intent ("Exoscale's eth0 is the public interface, the
  address this pack publishes as public-ip is the primary interface's"), and the
  measurement says the published half is missing. Choosing costs a reading of
  what a real Exoscale instance answers when no Elastic IP is attached.

Until then `mise run conformance:parity` runs on demand and is deliberately not
in the `conformance` aggregate: a red suite inside the gate every other change is
judged by teaches people to skip the gate.

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

## An Outscale machine owns a root volume, and that volume holds no bytes

Since #378 and #389, `CreateVms` cuts every machine a root BSU volume from the
snapshot its image names, `ReadVolumes` answers for it, `ReadVms` publishes it
under `/dev/sda1`, and `DeleteVms` destroys it — `DeleteOnVmDeletion` is `true`
on it, which is what the recorded account answers on its own machine in every
read of its life.

The chain behind it exists so that no identifier a response publishes is
decorative: the image names a snapshot `ReadSnapshots` answers for, the snapshot
sizes the volume, the volume names the machine. A fictional root `VolumeId` was
tried once and the Terraform provider resolved it — `volume vol-rooti149 not
found` ended a whole conformance run.

**What that volume is not is a disk.** It carries a size, a type, a state and a
provenance; it holds no bytes, exactly like every other volume here. Three
consequences a user can meet:

- **Growing it changes a number and nothing else.** There is no filesystem to
  extend, and nothing inside the machine sees the new size.
- **`CreateImage` from a `VmId` answers an empty `BlockDeviceMappings`.** An
  image is a copy of a disk's bytes, and there are none to copy, so no snapshot
  of the machine's device is cut. Cutting an image from a *snapshot* works and
  is what `tools/conformance/outscale/terraform/storage.tf` drives. A machine
  created from such an image still gets a root volume, with a size and **no**
  `SnapshotId`: naming one would be a relation that resolves and is false.
- **"Nothing left behind" now includes a volume per machine.** A suite that
  counts what a teardown leaves has one more object per machine to account for,
  and it goes with its machine rather than surviving it.

The catalogue's own three snapshots are the emulator's, not the client's:
`DeleteSnapshot` refuses one with a `ResourceConflict` naming the catalogue,
because deleting it would leave every catalogue image pointing at a snapshot no
read answers for. Their `VolumeId` names the volume each image was cut from, and
that volume is gone — which is the state every OMI of a real account is in, and
one this emulator reaches by ordinary means (`CreateVolume`, `CreateSnapshot`,
`DeleteVolume`). It is the one identifier of the chain that names nothing, and
the one no client follows.

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

### An operation the document says answers nothing

A missing response schema in `contracts/*.json` has three causes, and only one
of them can be checked. The artefact distinguishes them because folding them
together is what left thirty-one served Scaleway operations reading `unchecked`
on the contract axis, and thirty-two reading `none` on `probed`, for months
(#429).

- **The document declares a body.** `response` names its schema, and the answer
  is validated against it. 306 of Scaleway's 370 documented operations, all 236
  of Outscale's, 371 of Exoscale's 374.
- **The document declares a success with no content at all.** Scaleway writes
  `204: {description: ''}` on 64 operations, and it is the only provider here
  that does. `noContent` carries the status, and the answer is held to it in both
  directions — a body where none is declared, and a status the document does not
  name. **This is a validation, not a silence**, and reading it as one is what
  put those thirty-one at zero.

  The 64 are not simply "the DELETEs", and that is what makes the field worth
  reading rather than the method: 52 of Scaleway's 56 DELETEs are here, and the
  other four — `vpcgw/v2.DeleteGateway`, `vpcgw/v2.DeleteGatewayNetwork`,
  `lb/v1.RemoveBackendServers`, `lb/v1.UnsubscribeFromLb` — declare a body and
  answer one. Twelve operations that are not DELETEs are here too, the `Set*`
  user-data and cloud-init family among them.
- **The document declares a body this extraction cannot name.** A top-level
  array, a free-form object, a media type that is not JSON. Three Exoscale
  operations are in this case — `list-events`, `get-sks-cluster-inspection`,
  `list-sks-cluster-deprecated-resources` — and they stay `unchecked`, correctly:
  nothing about the emulator's answer is known.

One thing the probe cannot reach, and it is a property of the probe rather than
of any pack: `instance/v1/API.{Get,Set,Delete}ServerUserData` address a key by
name, and no call in a probe run produces one. Measured — a server the probe
creates answers `{"user_data":[]}`, because the only operation that could put a
key there is the one that needs the key. A client invents the name
(`scw instance user-data set key=cloud-init` does), and the probe may not invent
anything. Those three earn the contract axis from client traffic and stay at
zero on `probed`.

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
| Scaleway | `instance/v1, instance/v2alpha1, vpc/v2, ipam/v1, iam/v1alpha1, marketplace/v2, block/v1, block/v1alpha1, lb/v1, vpcgw/v2` | 604 | **declared** by the provider |
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

## Exoscale has one zone per process, and the reason is the client

The API description enumerates eight zone names and this emulator publishes
one per process. It is not an oversight and it is not laziness — it is what
the official CLI forced.

Serving all eight was the obvious fix and it was worse. The CLI queries **every**
zone it is told about and merges the answers, so eight zone entries pointing at
one emulator turned a single instance into eight identical rows in
`exo compute instance list`. A resource duplicated per zone is a defect a user
sees on their first command. At Exoscale a zone is a property of the endpoint
(`api-<zone>.exoscale.com`), so one endpoint honestly serves one zone.

*Which* zone is the operator's choice since #278: `FEINT_EXOSCALE_ZONE`
selects any zone their document publishes, and unset keeps `ch-dk-2`, the
CLI's own default — serving a zone the CLI does not default to makes every
unflagged command fail before it calls anything, on
`find zone: not found in ListZonesResponse`. Three of the five surveyed
Exoscale stacks target another zone (#262), which is what made the choice
worth a knob.

Two consequences, stated rather than hidden:

- a client asking for a zone the process does not serve is refused, where the
  real cloud would serve it. That is a visible, honest difference; the
  alternative — one endpoint pretending to be eight zones — is a silent, wrong
  one. *Where* the refusal lands depends on the client, because the two
  families do opposite things with the zone list. The exo CLI merges every row
  into every listing, so it keeps the single row and a mismatch dies inside it
  as `find zone: not found in ListZonesResponse`. The Terraform provider
  (behind `FEINT_EXOSCALE_ALLOW_TERRAFORM=1`) resolves endpoints by name and
  never merges, so its zone list also carries a signpost row for each of the
  seven other published zones, pointing at `/v2/unserved-zone/<zone>` — and
  the next call is refused naming the deployment's zone, the resolved zone and
  the `FEINT_EXOSCALE_ZONE` remedy. Measured on `exoscale_domain`, which the
  provider resolves through a hardcoded `ch-gva-2`: before the signpost its
  apply died client-side as `find zone: "ch-gva-2" not found in
  ListZonesResponse`, a message that sends the reader after their zone
  configuration when DNS is simply not served (#262, #284); after it, the same
  apply is refused with the mismatch named, and a wrong-zone create is refused
  instead of silently served as the deployment's zone
  (`TestAnUnservedZoneSignpostNamesTheMismatch`).
- on any zone but `ch-dk-2`, `exo compute instance-type list` and `show` fail
  with `find zone: "ch-dk-2" not found`, and that is the client, measured at
  its source: exo 1.95.1 passes its compiled default to the v3 zone switch
  (`cmd/compute/instance_type/instance_type_list.go:85`, with
  `cmd/common.go:15` fixing `DefaultZone = "ch-dk-2"`); no flag, config or
  env reaches it. Invisible against the real cloud, whose zone list always
  names `ch-dk-2`. Every other command of the driven flows takes `--zone` or
  honours the account default; `tools/conformance/exoscale/zones.sh` pins
  the exact failure so the day exo heals it, the suite says so.

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

## An Outscale load balancer distributes packets inside its network, and nowhere else

The LBU family is served as far as the surveyed stacks exercise it (#281):
create, read, update (health check, security groups, secured cookies),
register/link and unlink backend Vms, delete. Three of the five surveyed
Outscale stacks (#262) stand on exactly that lifecycle, and all three apply,
re-plan empty and destroy against it.

Since #344 the listeners can also be moved after the create
(`CreateLoadBalancerListeners`, `DeleteLoadBalancerListeners`), which is a
second-apply operation and never a first one: `CreateLoadBalancer` carries its
listeners inline. Providers 1.1.3, 1.7.0 and 1.8.0 all call the pair from their
Update path and from nowhere else, and all three delete the departing front port
before creating the arriving one, so a single-listener port change really does
pass through a balancer holding no listener at all. That transient state is
allowed rather than refused, and the runtime follows it: a balancer with no
listener left is withdrawn from the host instead of going on distributing on a
port the API has stopped listing.

What a `200` from `CreateLoadBalancer` means here, stated rather than implied:

- **The configuration is recorded and round-trips.** Listeners, backends,
  health-check settings, tags, the security groups and the subnet come back
  field for field, and the delete guards hold the same algebra as the rest of
  the pack: a subnet or a security group under a balancer refuses to go.
- **Its own private address distributes, under `--vm incus-ovn` and there
  only.** A balancer's `PrivateIp` is an address of the Subnet it sits in, and
  it is handed to `incus network load-balancer` on the Subnet's own network:
  connections from inside that network are spread over the registered Vms, an
  unlinked Vm stops receiving them, and deleting the balancer takes it off the
  host. Measured on 2026-08-20 with three machines on one OVN network — two
  backends answering their own name on `:80`, one client — 6/6 answered at t0
  and 6/6 again a minute later, over both backends each time, and 6/6 to the
  survivor after an unlink. `tools/conformance/outscale/balancer.sh` is that
  measurement, replayed on demand.

  The claim is declared, never deduced: `/_feint/health` answers
  `capabilities.balancing`, the OVN mode alone sets it, startup verification
  clears it on a host with no OVN wiring, and a build that does not know the
  key answers nothing — which reads as absent. A suite must gate on it and
  never on a mode name.
- **The public face distributes nothing, and that is a measurement too.** The
  `DnsName` follows the measured format
  (`<name>-<digits>.<region>.lbu.outscale.com`, `internal-` prefix included)
  and resolves nowhere; the public address of an internet-facing balancer comes
  from `203.0.113.0/24` — TEST-NET-3, RFC 5737, routed nowhere on purpose, and
  deliberately not the block ReadPublicIps allocates from, because the real
  service associates an address the account does not own.

  The reason it stays that way is on the record (#315, measured 2026-08-19). A
  VIP *outside* the network, delegated through the uplink's `ipv4.routes`,
  answered 6/6 probes at t0, 6/6 at t+60s, and **0/6 from t+180s onwards**,
  permanently, `ip neigh flush` included: the runtime announces such an address
  with a burst of gratuitous ARPs at creation time and never again — the same
  defect `internal/core/machine/incus_ovn.go` records for network forwards. The
  driver's `ipv4.routes.external` path holds indefinitely and is strictly
  one-machine, so it cannot carry a multi-backend VIP either.

  So `EnsureBalancer` **refuses** a listen address outside the network's own
  block rather than configuring one. A balancer that passes a test and fails
  three minutes later is worse than a balancer that was never claimed.
- **No backend health exists, and none is invented.** `ReadVmsHealth` stays
  declined, and it stays declined *after* the dataplane landed: `incus network
  load-balancer info` answers "No load-balancer health information available",
  so nothing probes a backend even under OVN, and a backend reported `UP` that
  nothing checked is the exact answer this project exists to refuse. The
  health-check *settings* round-trip because Terraform plans on them; the
  health *states* do not exist until something measures them.
- **The stored health-check defaults are the vendor's own** (interval 30,
  timeout 5, unhealthy 2, healthy 10, TCP on the first listener's backend
  port — the defaults Outscale's user guide documents), so a stack that never
  touches `outscale_load_balancer_attributes` reads back what a fresh real
  balancer would say.

What is not served, by name and on purpose: the public-Cloud form
(`SubregionNames` without a Net — no surveyed stack takes it, and nothing is
measured about what it answers), access-log enablement (there is no OOS
bucket here to publish into), listener policies, listener rules, LBU tag
CRUD, and server certificates. Each answers a refusal naming the line, never
a silent 200.

**The next wall a day-2 edit meets is the LBU tag CRUD, and it is named rather
than left to be found.** Measured on 2026-08-21 with provider 1.8.0: changing a
`tags` block on an existing `outscale_load_balancer` answers `Error: Unable to
update Load Balancer` carrying `feint does not serve DeleteLoadBalancerTags`,
and the plan then stays at `0 to add, 1 to change, 0 to destroy`. It is out of
#344's scope because that issue served the path carrying traffic and a tag
reaches no runtime; the demand for it is now written down rather than guessed.

**`DeregisterVmsInLoadBalancer` is refused on reachability, not on demand.**
Provider 1.1.3 is the only version whose code contains the call, on the update
path of the load balancer's own `backend_vm_ids`, and that path panics before a
request is built: the attribute is declared `schema.TypeList`
(`resource_outscale_load_balancer.go:150`) and the update casts it to
`*schema.Set` (`:726`). Measured 2026-08-21 against this emulator —
`interface conversion: interface {} is []interface {}, not *schema.Set`, an
upstream defect rather than an emulator one. Providers 1.7.0 and 1.8.0 removed
the call; detaching a backend goes through `UnlinkLoadBalancerBackendMachines`,
which is served.

**One choice here is not a measurement, and says so.** Naming a front port that
carries no listener in `DeleteLoadBalancerListeners` is accepted rather than
refused: nothing here has watched a real account answer that request, and what
the caller asks for — that these ports carry no listener afterwards — is already
true of a port that carried none. A refusal would have been just as much of a
guess, and a riskier one, since it would break a client that retried a
half-applied update.

## A Scaleway load balancer and public gateway record their configuration; nothing forwards packets

The lb/v1 ZonedAPI and vpc-gw/v2 families are served as far as the measured
clients exercise them (#282): the surveyed kubic and terraform-talos stacks,
Scaleway's own LB and VPC modules, `scw lb` and `scw vpc-gw`. The dataplane
#315 built for the Outscale LBU has not been wired here: the mechanism is the
same and the pack is not, so the honest statement is that a Scaleway balancer
still records its configuration and forwards nothing. `capabilities.balancing`
says what the *runtime* can do, never what a given pack asked it for, and this
pack asks for nothing.

What a `200` means here, stated rather than implied:

- **The configuration is recorded and round-trips.** The balancer, its
  backends (pools, millisecond timeouts, the one-of-seven health-check
  config), frontends, inline ACLs and routes come back field for field; so do
  the gateway, its IP and the GatewayNetwork. The wrong destroy order gets a
  refusal — a backend under a frontend, a gateway under a connection — never
  a silent success.
- **No traffic is forwarded.** A balancer's IPv4 comes from `198.51.100.0/24`
  (TEST-NET-2), a gateway's from `192.0.2.0/24` (TEST-NET-1) — RFC 5737,
  routed nowhere on purpose, distinct from the instance flexible block so no
  two products ever publish the same address. The gateway NATs nothing, pushes
  no route into a machine, and its bastion accepts no connection, which is why
  the bastion allow-list operations are declined rather than recorded.
- **No backend health exists, and none is invented.** `GetLBStats` and
  `ListBackendStats` stay declined: nothing probes a backend, and a backend
  reported `UP` that nothing checked is the exact answer this project exists
  to refuse. The health-check *settings* round-trip because Terraform plans
  on them; the health *states* do not exist until something measures them.
- **Both attachment spellings are served.** SDK generations up to
  v1.0.0-beta.29 attach a Private Network at
  `/lbs/{id}/private-networks/{pnID}/attach`; the current one says
  `/lbs/{id}/attach-private-network`. terraform-provider-scaleway v2.43 — the
  pin of a surveyed stack — sends the old one, production still accepts it,
  and the emulator does too (`Route.Legacy` carries the measurement).
- **The attachment's address is a first-class IPAM citizen.** An attach
  without `ipam_ids` books an address from the Private Network's own pool; a
  GatewayNetwork does the same, or holds the `ipam_ip_id` the client booked
  first. Both read back through `/ipam/v1` filtered by
  `resource_type=lb_server` or `vpc_gateway_network`, which is exactly how
  the Terraform provider resolves them.

Only vpc-gw **v2** is served. The portal publishes no v1 document any more
(measured 2026-08-19) and every mounted route here is checked against the
portal's document, so v1 is declined wholesale, by name: a provider pinned
below 2.52 — terraform-talos's `~> 2.43.0` among them — meets a named 501 on
`/vpc-gw/v1/...`, and the recorded fix is the provider bump to ≥ 2.52, the
release that moved the product onto v2. Also declined by name: MigrateLB and
UpgradeGateway (a capacity move nothing performs), certificates (nothing
terminates TLS), subscribers (no event to deliver), the PAT rules (a rule
recorded and never applied is indistinguishable from protection), and both
type catalogues (`ListLBTypes`, `ListGatewayTypes` — unmeasured inventory; the
gateway create still refuses an offer outside VPC-GW-S/M/L/XL, the #279
lesson).

## An Exoscale network load balancer records its configuration, names its backends, and grades none of them

The NLB family is served whole since #345 — the balancer, its services, and the
two per-field resets — after a year declined by #14. What #14 refused is what
this section exists to keep refused, and it is worth stating in the same breath
as what is now served.

**The configuration round-trips, and a real client converges on it.** Name,
description, labels, and per service the protocol, the ports, the strategy, the
instance pool and the whole healthcheck block come back field for field. The
example stack under `examples/stacks/exoscale/` applies with an `exoscale_nlb`
and an `exoscale_nlb_service`, re-plans empty, and destroys clean (15 resources,
measured 2026-08-21 with the patched provider this document pins).

**The health of a backend is not measured here, and none is invented.** A
service publishes `healthcheck-status`, and every entry it publishes carries the
backend's `public-ip` and **no `status`**. That is upstream's own shape rather
than a compromise: the element schema `load-balancer-server-status` declares no
required property, so an entry naming a server with no verdict on it is
well-formed. The official CLI prints it as
`{"instance_ip":"192.0.2.2","status":""}`, which is the honest sentence — these
are the servers behind the service, and nothing graded them.

The two alternatives were both worse, and both were considered rather than
skipped. Publishing `success` is the fabrication #14 declined the family over.
Publishing an empty array reads as *this service has no backend*, which is a
claim about the pool, false for every pool this emulator holds, and one a client
could plan against.

**What is not measured about it, said plainly.** No recording of a live NLB
exists here: `shapes/exoscale.json` carries `GET /v2/load-balancer` from an
account that held none, so it pins the envelope key and nothing inside it.
What is measured is that their published document allows an entry with no
`status`, and that the official CLI and the Terraform provider both accept one.
Whether the real API ever omits the field on a service it is actually probing is
a question a recording would settle and nothing here answers.

**Nothing forwards a packet, and the reason is an address rather than a
missing feature.** The Outscale LBU distributes real connections under
`--vm incus-ovn` because its `PrivateIp` is an address of the Subnet it sits in,
and `machine.EnsureBalancer` accepts exactly that. An Exoscale NLB publishes one
address, `ip`, and their `load-balancer` schema declares no other — no subnet,
no private network, no counterpart to `PrivateIp`. That single address comes
from `192.0.2.0/24` (TEST-NET-1, RFC 5737), which is outside every emulated
network's own block.

Measured on 2026-08-21 against a live `incus-ovn` host, on an OVN network of
this emulator's own making (10.63.7.0/24):

- `EnsureBalancer` with `192.0.2.1` — the address this pack gives an NLB —
  answers *"balancer … listens on 192.0.2.1, which is outside … 's own block
  10.63.7.0/24: an address the runtime has to announce goes dark within minutes
  (#315)"*;
- the same call with `10.63.7.240` and one backend answers `<nil>`, so the
  refusal is about the address and not about the call;
- and the daemon itself refuses the public address before any guard of ours is
  consulted: *Failed creating load balancer: Uplink network doesn't contain
  `"192.0.2.1/32"` in its routes*.

So `capabilities.balancing` is irrelevant to this family: the pack never asks
the runtime at all, because the only call it could make is one whose refusal is
guaranteed. `machine.Balancer` needed no provider-shaped concession to reach
that answer, and `internal/core` gained no Exoscale knowledge — what is missing
is an address upstream does not publish, and no field of an interface can supply
one.

**Two client facts that are the pack's and not the API's, recorded because they
surprised.**

- A service mutation's operation refers to the **balancer**, never to the
  service. egoscale v2 passes that reference straight to
  `GetNetworkLoadBalancer` and finds the new service by diffing the balancer's
  list (`v2/network_load_balancer_service.go:121` at v0.102.4), so referring to
  the service makes `terraform apply` fail with `Get …/v2/load-balancer/<service
  id>: resource not found`. Measured; the exo CLI cannot arbitrate it, because
  it resolves every object by listing and never reads a reference.
- **This CLI clears no field of this family.** Every other family here records
  that the CLI clears a field by sending the update with an empty value; on the
  NLB it does not. `exo compute load-balancer update --description ""` sends
  `PUT {}`, and the service form sends only the healthcheck block it re-sends on
  every call. The per-field DELETEs are served because their document declares
  them, and no published client issues one.

**What a service's backends are.** The members of the instance pool it targets,
which is where upstream takes them from too — a service names a pool, never a
list of machines. Pool members carry a public address since #345 (their pool's
`public-ip-assignment` decides, `inet4` by default); before that they carried
none, and every service in front of a pool answered an empty backend list.

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

## A Scaleway VPC's Network ACL is a record, and nothing at the edge applies it

`vpc/v2/API.GetACL` and `vpc/v2/API.SetACL` are served since #343, and they are
records in the exact sense the section above gives a custom route: the whole
rule set round-trips for `scw vpc rule get/set` and for `scaleway_vpc_acl`, the
protocols and actions are held to the SDK's own enums, the sources and
destinations are parsed as CIDRs, and **no runtime mode programs a filter at the
VPC edge**. A rule dropping `0.0.0.0/0` here closes nothing.

They were declined until #343, under a reason worth repeating because half of it
still stands: *"a filter recorded but never applied is indistinguishable from
protection"*. What changed is not the enforcement — it is who is told. A 501
stopped the client that was only ever going to read its rules back, and said
nothing to anyone about enforcement; this page says it, in the place a reader
looks for it, and the pack's own file repeats it. The refusal protected nobody
and cost every stack that declares the resource.

What decided the split was a measurement rather than the SDK's shape
(2026-08-21, recorded through `feint proxy --record` and ranked with
`feint coverage --observed`):

| operation | what called it | verdict |
|---|---|---|
| `vpc/v2/API.GetACL`, `SetACL` | `scw vpc rule get/set`, and the official provider's `scaleway_vpc_acl` | served, as records |
| the five `*IngressRule` | nothing: `scw` has no ingress-rule subcommand, no surveyed stack names `scaleway_vpc_ingress_rule` | still declined |
| the five `*VPCConnector` | `scw vpc vpc-connector list/create`, both recorded taking a 501 | still declined, and the demand is real: peering two VPCs is the one property the bridge mode cannot deliver |

The last row is the one to read twice. A recorded call is not on its own a
reason to serve: the connectors are declined **despite** the demand, because
answering them would report done the very thing this project measures the
absence of. Demand decides what is worth serving; it never decides what can be
served honestly.

Use the ACL to test the shape of a plan and the round-trip of a rule set. Never
use it to test whether a packet is dropped. The day a rule is worth programming
for real it arrives as a declared driver capability, measured under OVN, not as
a silent upgrade of these records.

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
