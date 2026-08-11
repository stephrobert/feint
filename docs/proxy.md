# The proxy, and reading what it records

Everything this project knows about how an official client addresses a cloud was
found by putting a proxy between the client and the endpoint, reading the log,
and throwing the proxy away. `tools/conformance/README.md` records three such
findings, each from a throwaway proxy, none committed. `feint proxy` is that tool
built to stay, and `feint transcript` is what reads what it wrote.

The point is not to have a recording. The point is to **shorten the work of
whoever is building the emulator**. Serving one more operation used to mean four
guesses: which operation matters, what its request and response look like,
whether the handler got it right, and finding out only when the real client
rejected it. A recording of a real client against a real cloud replaces each
guess with a measurement.

## Record

Point an official client at the proxy and drive it as usual. The proxy forwards
to one upstream and writes one JSON object per exchange.

```bash
feint proxy --provider outscale \
  --upstream https://api.eu-west-2.outscale.com \
  --addr 127.0.0.1:4600 --record real.jsonl
# then, in another shell, drive the real client with its endpoint set to
# http://127.0.0.1:4600
```

Two properties matter and are enforced, not documented:

- **Credentials never reach the file.** Redaction is a property of the recorded
  type — the writer accepts a value that cannot exist without having been through
  the rules — so there is no path to the file for an unredacted exchange, not one
  that is discouraged, one that does not compile. The header carrying the
  signature stays; its value becomes `REDACTED`.
- **Loopback only**, unless `--expose-to-network`. Every request through the
  proxy carries a live credential belonging to whoever started it, and an open
  port would relay it.

## Read

`feint transcript` answers the three questions, in order of value.

**What to serve next**, measured rather than guessed:

```console
$ feint transcript real.jsonl
operations a real client called that no pack serves (20), most-called first:

  calls  resp.bytes  statuses      operation
      1        9210  200           /api/v1/ReadSecurityGroups
      1        4487  200           /api/v1/ReadRouteTables
      1        2096  200           /api/v1/ReadNics
      ...
```

The ranking is calls first, then the size of the largest response, because an
operation that answered a populated body is one a developer can implement from,
and one that answered an empty list is not. A `400` among the statuses means the
call needs a parameter the sweep did not send: the operation exists, its
populated shape is simply not in this recording.

**What the response must look like**, from what the cloud actually returned —
which is not the same as what the SDK says it *may* return, and it is the second
that breaks a client:

```console
$ feint transcript real.jsonl --shape ReadNics
response shape of ReadNics, as the real cloud returned it:

  array       Nics
  object      Nics[]
  string      Nics[].NicId
  object      Nics[].LinkPublicIp
  string      Nics[].LinkPublicIp.PublicIp
  ...
```

The shape is the union across every response and every array element, so a field
only some elements carry still appears. The array container and its element are
distinct paths (`Nics` is an array, `Nics[]` is its element), which is what lets
the diff below tell an empty list from a missing field.

**What the emulator already gets wrong**, before the code is written. Record the
same operation against the emulator too, then diff:

```console
$ feint transcript real.jsonl --shape ReadVolumes --against emulator.jsonl
ReadVolumes: fields the real cloud returns that the emulator gets wrong (2):

  real        emulator    field
  string      (absent)    Volumes[].SnapshotId
  object      (absent)    Volumes[].LinkedVolumes[]
```

A field the real cloud returns and the emulator omits is a defect no unit test
finds, because the unit test was written against the same reading of the SDK the
handler was. This is the project's "jamais de format inventé" rule, made
checkable.

## What a recording can promise

A transcript covers **what the client kept sending to the proxy**, and that is
narrower than "the session". Two of the three providers served here can take a
client out of the recording, for unrelated reasons, and the two fail
differently — which matters more than either mechanism, because one is loud and
the other was not.

| provider | mechanism | how it fails |
|---|---|---|
| Scaleway | none | records whole |
| Outscale | the signature covers `Host` | **refuses**: the cloud answers 401, nothing is distorted |
| Exoscale | the server returns an address in the body | **truncates**: recording stops, and used to say nothing |

Scaleway is the one that works, and that is why the trap went unnoticed for so
long: the provider this tool was built against signs nothing and never
republishes its own address, so it is the only one immune to both.

### The recorder says when it has handed the client away

The Exoscale case is the dangerous one, because a transcript that stops early
looks exactly like a session that ended early. Measured: a full Exoscale
session, seven resources created and swept on a real account, recorded **8
exchanges**; the same session with the endpoints rewritten recorded about
**ninety**. Nothing in the file said the other eighty-two existed.

So the proxy now counts responses that named a host other than the one the
client is addressing, and says so when the run ends:

```text
3 response(s) handed the client an address that is not this proxy: api-ch-dk-2.exoscale.com
  anything the client sent there is absent from this transcript. See docs/proxy.md.
```

It is detected by shape, not by field name: an absolute URL in a response body
whose host is not the client's. Naming `api-endpoint` would put one provider's
vocabulary in a tool that carries none, and the fourth API to do this would be
silent all over again.

Two properties matter as much as the detection, and each has its own test:

- **An answer naming the proxy itself is not a handoff.** The emulator's own
  `/v2/zone` publishes an endpoint pointing back at itself — `catalog.go` says
  that is the whole reason the route exists — so counting it would raise the
  alarm on every correct recording, and an alarm that fires on the normal case
  gets ignored.
- **A gzipped body is decompressed before the scan.** `scw` and `exo` both send
  `Accept-Encoding`, so on a real session nearly every body arrives compressed;
  a scan over the raw bytes would find nothing in any of them and report
  nothing found, which reads exactly like nothing being there.

### What this deliberately does not do

**It does not rewrite the address.** A recorder that edits the answer it is
recording is not a recorder, and the scratchpad rewriter used to capture the
Exoscale shapes was honest about shapes and lying about one field. If following
the endpoint is ever wanted, it belongs behind an explicit flag that records the
answer **as received** and marks that it rewrote what the client saw — which is
the open half of #92, deliberately not decided by this change.

**The other route is already there and needs no proxy.** `internal/upstream`
signs the real host and talks to it directly, so it has neither problem: it is
how `feint shapes --record` captures a real cloud's field tree. When a recording
would be truncated, that is the tool to reach for.

### What it cannot record: a signed client pointed at a real cloud

A reverse proxy cannot relay a SigV4-signed request to a real cloud when the
client signs the proxy's own host, and that bounds what a transcript can cover.

Measured against Outscale's `cloudgouv-eu-west-1`, same credential, same body:

| what the client signs as `host` | result |
|---|---|
| `api.<region>.outscale.com` | **200** |
| `127.0.0.1:4701` (what a configured client signs) | **401 AccessDenied** |

The signature covers the `Host` header, the proxy forwards with the upstream's
host, and the cloud validates against its own name. So a client that derives the
signed host from the endpoint it was configured with — `oapi-cli`, the Terraform
provider, any SDK — cannot be recorded against a **real** cloud through this
proxy. Only a client that lets the signed host be set independently of the
connection target can.

Two things follow, and the first one matters most:

- **The failure is a refusal, not a distortion.** The cloud answers 401 and
  nothing is silently altered, so no measurement this tool has produced is in
  doubt because of it.
- **Recording against the emulator is unaffected**, because it verifies no
  signature. Every "what does the provider actually call" question is answerable
  today; only "what does the real cloud answer *to this client*" is not.

Lifting it needs DNS and TLS interception so the client can be pointed at the
real hostname and still land here, which is #76 and deliberately not this tool.
Until then, a real-cloud recording is made with a client whose signed host can be
set — which is how the transcripts behind this page's examples were produced.

### The one caveat of the diff

The diff is only as sharp as the state parity between the two recordings. A field
reads `(absent)` because the emulator omits it **or** because the resource
sampled on one side did not exercise it — an unattached volume shows no
`LinkedVolumes`, on the real cloud or the emulated one. So populate the emulator
with a comparable resource before trusting an absence, and read a `(absent)` on a
field the sampled resource would not carry as "not shown here", not "omitted".
Type mismatches (a `number` upstream, a `string` in the emulator) do not have
this caveat: both sides had the field.

## Doing it against a real, billed account

The proxy is the first thing in this project that talks to a real cloud on a real
account, and the discipline that goes with it is not optional:

- **Read-only.** A sweep to learn a shape needs only `Read*` calls. Never a
  `Create`, `Delete`, `Update`, `Link` or `Stop` against an account you do not
  intend to change — the more so on a qualified region (Outscale's
  `cloudgouv-eu-west-1` is SecNumCloud), where the account carries real workloads.
- **The recording does not leave your machine.** It is redacted of credentials
  and still describes a real infrastructure: account number, resource
  identifiers, address ranges. It stays out of the repository. A fixture for a
  test is *built* from the observed shape with invented values, and says so.
- **This never runs in CI.** No account, no credential in a runner. A recording
  is made by a human on their own station.
