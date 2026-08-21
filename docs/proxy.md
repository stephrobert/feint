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

A client the cloud walks away from by republishing its own address needs one more
flag, `--intercept`; it has its own section below.

## Record a client whose endpoint is compiled in

The command above needs a client you can redirect. Some cannot be: Pépin's
collectors hold `https://api.scaleway.com`, `https://api-{zone}.exoscale.com/v2`
and `https://api.{region}.{host}/api/v1` in their source, and adding a
configurable endpoint was **refused** on their own delivery audit — every
collection request carries a live secret key, so a redirectable endpoint is a way
to send a tenant's credentials to a host of somebody else's choosing. The
redirect belongs outside the tool being measured.

`--forward` is that outside. It makes the proxy a **forward** proxy: it accepts
`CONNECT api.scaleway.com:443`, terminates the TLS with a certificate minted for
the run, records the exchange, and re-originates to the real host.

```bash
feint proxy --forward api.scaleway.com,'*.exoscale.com' --record real.jsonl
#   forwarding for api.scaleway.com, *.exoscale.com
#   CA written to /tmp/feint-intercept-ca-1655051664.pem (a temporary file, removed on exit)
#   drive a client whose endpoint is compiled in with:
#     export HTTPS_PROXY=http://127.0.0.1:4600
#     export SSL_CERT_FILE=/tmp/feint-intercept-ca-1655051664.pem
```

Nothing changes in the client. A Go client that installs no `Transport` inherits
`http.DefaultTransport`, which honours `HTTPS_PROXY` on its own; `SSL_CERT_FILE`
is what makes it trust the certificate the tunnel presents. Measured end to end
on 2026-08-20 against a local HTTPS server, with a client whose endpoint is a
constant:

> **On macOS, `SSL_CERT_FILE` does not do this**, and the sentence above is
> therefore true of Linux only. Go's `crypto/x509` reads the system keychain on
> Darwin, and the code path that consults `SSL_CERT_FILE` carries a build
> constraint excluding it. Measured rather than read: the first CI run of this
> feature on `macos-15` failed with `x509: certificate signed by unknown
> authority` while the proxy logged `the client did not complete the tunnel
> handshake`.
>
> The tunnel itself works there — the repository's own test proves it on macOS
> by handing the child an explicit pool. What does not work is the part that
> makes this feature worth having: *not touching the client*. On macOS the
> client must be given the CA some other way (its own `tls.Config`, or the
> authority added to the keychain), which is a change to the tool you were
> trying to measure. Plan for a Linux runner, a container, or a VM when the
> point is to trace something you must not modify.

```json
{"seq":1,"host":"api.localhost:8443","method":"POST",
 "path":"/instance/v1/zones/fr-par-1/servers","query":"x-amz-signature=REDACTED",
 "operation":"instance/v1/API.CreateServer","provider":"scaleway","status":200,
 "req":{"headers":{"User-Agent":"pepin-shaped-collector/1.0",
                   "X-Auth-Token":"REDACTED","X-Consumer":"REDACTED"}},
 "res":{"headers":{"X-Session-Token":"REDACTED"},
        "body":{"servers":[{"id":"c2f1a7b0-…","public_ip":{"address":"51.15.0.1"}}]}}}
```

The cloud received every one of those values in full — the recorder alters
nothing on the wire — and the file holds none of them.

Four properties, each of them a requirement rather than a precaution, and each
with a test that fails without it (`tools/falsify/specs/forward-proxy.json`
replays all seven):

- **The redaction survives the interception.** The tunnel records through the
  same `capture` as everything else, so there is no second path to the writer to
  forget. `TestASecretHeaderIsStillRedactedThroughCONNECT` drives a real TLS
  session and asserts both ends of it, including an `X-Consumer` header — the
  name a denylist wrote out in clear while redacting an `X-Auth-Token` holding
  the same value.
- **Loopback only, and `--expose-to-network` does not lift it here.** A forward
  proxy holds an authority whose certificates a client has been told to trust:
  off loopback it is not a relay but a machine that decrypts and files whatever
  anyone who can reach the port sends it.
- **The authority is ephemeral and never installed.** The CA is generated in
  memory at startup, written to one temporary file, and removed when the command
  returns. It never goes near the system trust store, and the leaf it signs
  cannot itself sign anything.
- **Only the hosts you name are intercepted.** A `CONNECT` to any other host is
  refused with a 403, counted, and reported at exit. Refused rather than relayed
  blind, because a blind relay writes a transcript that silently misses
  exchanges — the failure the handoff counter below exists to report. A wildcard
  covers one label (`*.exoscale.com` matches `api-ch-gva-2.exoscale.com` and
  never `a.b.exoscale.com`), and `--forward '*'` is refused outright: that flag
  would be a wiretap on everything the measured process does.

`--forward` and `--upstream` are two different proxies and are not passed
together; neither are `--forward` and `--intercept`, which are two ways to reach
the same interception (`--intercept` serves TLS on the listener itself, for a
client redirected by name — see [limits.md](limits.md)).

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

## Replay: is what we serve right?

`feint transcript` reads a recording. `feint replay` *reissues* it, which is the
one thing no passive diff can do — it needs no second recording, and it produces
a verdict over the whole run rather than one operation at a time.

```console
$ feint replay real.jsonl --endpoint http://127.0.0.1:4599
instance/v1/API.CreateIP                         PASS
instance/v1/API.CreateServer                     DIFF
  order:    server.public_ips[].id is ordered 0,1 upstream, 1,0 here
instance/v1/API.GetServer                        PASS
lb/v1/ZonedAPI.CreateLB                          NOT SERVED

3 matched, 1 divergent, 1 not served, 0 without a recorded answer
0 field(s) knowingly not served, each printed above with its reason
4 recorded identifier(s) rebound to the one this emulator minted
not served is a work item, not a failure: rank it with `feint coverage --observed <recording>`
```

Exit **2** on a divergence, **1** only when the tool itself failed — the file is
unreadable, the emulator does not answer. **Not served is neither**: it is the
work queue below, and the day it fails a build is the day somebody stops
recording.

### What it compares, and what it deliberately does not

| aspect | compared |
|---|---|
| status | exact |
| fields present | exact, minus what a pack's `DeclinedFields()` excuses |
| types | exact |
| values | only where a pack declares `emulator.InvariantValue` |
| ordering | only where a pack declares `emulator.InvariantOrder` |

The last two lines are where a first version goes wrong in *both* directions,
and each has a defect behind it.

Comparing every value would paint every run red — identifiers, timestamps and
addresses differ by construction — and the tool would be ignored within the
week. Comparing none would let an emulator that accepts a name and answers
another pass, which is the "argument the API accepted and then ignored" family.
So a pack declares the handful it knows: for Scaleway, the `name` and the
`commercial_type` a create's client always sends.

Comparing every list's order would invent a contract the cloud never stated.
Comparing none would have missed **#320**, where `Server.public_ips` came back in
store order rather than in the order the create named — a Terraform plan diff
that never converges. So the Scaleway pack declares that one order, for
`CreateServer`, `GetServer` and `UpdateServer`, and the report counts value
checks and order checks separately so a declaration that evaluated nothing
cannot read as one that held.

### Identifiers are rebound, not compared

A recorded run addresses the objects the cloud minted for it. This emulator mints
its own, so replaying a recorded path verbatim would answer 404 on every read.

The replay therefore learns, from each answer, which recorded identifier this
emulator answered in its place, and substitutes it into every later request:
whole path segments, whole query values, whole body strings, and only for values
shaped like something a cloud hands out — a UUID, an address, an Outscale
`i-<hex>`. A shape a fourth provider invents is not recognised, and the honest
consequence is a replay that reports divergences on that provider's reads rather
than one that silently substitutes something it guessed.

This is what makes the first thing worth checking reachable at all: **a
transcript recorded against the emulator replays against a *fresh* emulator with
zero divergences.** If replay cannot agree with the emulator about the emulator,
nothing it says about a real cloud is worth reading.

### Nothing it read is printed

A recording is an account's inventory (the table further down says so field by
field), so a finding names a path, a type, a status and a *position* — never a
value from either side. An out-of-order list is reported as "0,1 answered as
1,0", and the request path is anonymised before it is printed. That is a
property with a test, not an intention: `TestTheReplayReportRepublishesNoValue
FromTheRecording`, falsified by removing the anonymisation.

## Rank: what is worth serving next?

`feint replay` says whether what is served is right. `feint coverage --observed`
says what is worth serving next, and together they close the loop: a stack is
recorded, the recording ranks the backlog, the backlog is implemented, and the
replay proves it.

```console
$ feint coverage --provider exoscale --contract contracts/exoscale.json \
    --observed recordings/
provider exoscale: 1 declined operation(s) a recorded client called anyway, most-called first

  calls  client       operation
      7  exo          exoscale/v2.list-dns-domains
         declined: authoritative DNS is a public service with real resolvers behind it, and […]

281 declined operation(s) in all; 280 of them no recorded client called.
0 upstream operation(s) nobody has triaged; 0 of them no recorded client called.
93 implemented operation(s); 2 of them this recording exercised.
106 recorded call(s) this provider's document describes no operation for: another
provider's traffic in the same file, or a product outside the committed contract.
```

Every refusal in this repository carries a reason, which is the discipline. None
of them carries a **demand**, and that is what this reads out of a recording.

**Two facts, counted apart and never summed.** "Nobody called it" says nothing
about whether the decision was right; "nobody triaged it" is what fails the drift
gate, and a call count neither excuses nor creates it. An operation nobody called
is a count rather than a row, because a ranking that carries every refusal is the
alphabet again.

**Why `--contract` is required.** `feint proxy` names an exchange from the
*mounted routes* — that is what `emulator.Table` is for — so a call to a declined
operation carries no operation name at all. Only the provider's own document can
say that `GET /v2/dns-domain` is `list-dns-domains`. For Scaleway the reach is
the ten API versions `contracts/scaleway.json` carries; ranking a decline in a
product the emulator has not started means adding that product to
`tools/contract/scaleway-products.txt` first.

**The client column is a closed vocabulary** — `terraform`, `opentofu`, `scw`,
`exo`, `oapi-cli`, `sdk`, `unknown` — and never the raw `User-Agent`, which is
the one request header the proxy writes down in full and which carries whatever
the build put in it. The mapping was measured rather than guessed, and two
entries contradict the obvious guess: `scw` announces `scaleway-sdk-go/… (…)
scaleway-cli/2.56.3`, the SDK leading and the CLI trailing, and `exo` spells
itself `exocli`, not `exoscale-cli`.

**`feint coverage` without `--observed` is untouched.** The observed view renders
*instead of* the report, never beside it, so `--format json` keeps producing
`coverage/<provider>-coverage.json` byte for byte and `tools/drift/gate.sh` is
unaffected. That is the mechanism this repository rests on, and improving it must
not put it at risk.

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

### Keeping a client the cloud walked away: `--intercept`

A plain proxy holds a client only for as long as the client keeps addressing it.
The handoff above is the case where it stops: the cloud answers with its own real
name, the client believes it, and everything after that is somebody else's
conversation.

`--intercept` is the answer, and it changes one thing only — the client reaches
this proxy **by the cloud's name** instead of by `127.0.0.1`:

```bash
feint proxy --provider exoscale \
  --upstream https://api-ch-gva-2.exoscale.com \
  --intercept api-ch-gva-2.exoscale.com,api-ch-dk-2.exoscale.com \
  --record real.jsonl
```

The listener then serves HTTPS with a short-lived, non-CA leaf covering exactly
those names, minted by `internal/proxy/intercept.go` from the standard library
alone — no dependency, no `openssl`. The command prints the two lines a client
needs and removes the CA when it exits:

```text
  intercepting HTTPS for api-ch-gva-2.exoscale.com, api-ch-dk-2.exoscale.com
  CA written to /tmp/feint-intercept-ca-1234.pem (a temporary file, removed on exit)
  point a client at this proxy by name, in a namespace of its own, e.g.:
    export SSL_CERT_FILE=/tmp/feint-intercept-ca-1234.pem
    # resolve api-ch-gva-2.exoscale.com to this proxy (a container's own /etc/hosts, never yours):
    #   podman run --add-host=api-ch-gva-2.exoscale.com:host-gateway ... , proxy reachable on :4600
```

**The certificate is the cheap half; the name is the half that has to be
scoped.** This command installs nothing into the system trust store and never
edits your `/etc/hosts`, because it has no code that could: `SSL_CERT_FILE` is
process-scoped, and the redirect is yours to make inside a namespace the client
owns — a rootless container, feint's own user namespace under
`tools/install/apparmor/feint`, or an Incus container. A redirect left behind in
a machine-wide hosts file sends your next *real* `terraform apply` to a local
port, which is the exact failure this project exists to avoid.
[limits.md](limits.md), *The cost of DNS/TLS interception*, measures each place
the redirect is permitted and what it costs the machine.

What it bought, measured: an Exoscale session worth about ninety exchanges
recorded **eight** without it (#92). `TestInterceptionRecordsThePostHandoffExchanges`
drives the same session twice against one recorder — the republished name
resolving to the proxy, then away — and keeps the whole session one way, only the
pre-handoff exchange the other.

### What this deliberately does not do

**It does not rewrite the address.** A recorder that edits the answer it is
recording is not a recorder, and the scratchpad rewriter used to capture the
Exoscale shapes was honest about shapes and lying about one field. That refusal
still stands, and #92 was closed without lifting it: `--intercept` keeps the
client by making the republished name resolve here, so the body reaches the
transcript exactly as the cloud wrote it.

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

Lifting it needs the client to address the real hostname and still land here.
Two doors now do that, and both mint the certificate the same way — the cost is
measured in [limits.md](limits.md), *The cost of DNS/TLS interception*: the TLS
half is cheap and every Go client accepts a locally minted CA through
`SSL_CERT_FILE`.

- `--intercept` redirects by **name**, and that half is the expensive one:
  pointing a hostname at loopback has no client-scoped, unprivileged mechanism
  for a static pure-Go client, so it needs a namespace of the client's own (a
  container's `/etc/hosts`).
- `--forward` redirects by **proxy**, and needs no name redirect at all: the
  client asks for `api.eu-west-2.outscale.com` and the proxy re-originates to
  that same host, with the `Host` header it received.

**What that means for a signed client is reasoning, not a measurement.** The
signature covers `Host`; through a tunnel the client signs the cloud's own name,
and the header reaches the cloud unaltered, which
`TestASecretHeaderIsStillRedactedThroughCONNECT` asserts against a local server.
Whether a real
`oapi-cli` against a real Outscale account then answers 200 has **not** been
measured here, and this project takes no real-account measurement in CI. If you
have such an account, that is the run worth making, and this page will say what
it found rather than what it expects.

Until then, a real-cloud recording is made with a client whose signed host can be
set — which is how the transcripts behind this page's examples were produced.

Until 2026-08-20 this paragraph said the opposite: *"which is #76 and
deliberately not this tool"*. The flag shipped in v0.9.0, `docs/limits.md` sent
readers here to use it, and this page went on refusing it — the same defect as
[#334](https://github.com/stephrobert/feint/issues/334), one document further
out. #76 and #92 are closed as delivered.

### The one caveat of the diff

The diff is only as sharp as the state parity between the two recordings. A field
reads `(absent)` because the emulator omits it **or** because the resource
sampled on one side did not exercise it — an unattached volume shows no
`LinkedVolumes`, on the real cloud or the emulated one. So populate the emulator
with a comparable resource before trusting an absence, and read a `(absent)` on a
field the sampled resource would not carry as "not shown here", not "omitted".
Type mismatches (a `number` upstream, a `string` in the emulator) do not have
this caveat: both sides had the field.

## What a recording contains, and what to sanitise before sharing it

A transcript is redacted of credentials and is **not** anonymous. It is the
inventory of a real account, written down by something whose whole purpose is to
alter nothing. Read this table before a recording leaves the machine it was made
on.

| field | what it carries | after redaction |
|---|---|---|
| `host` | the authority the client addressed | verbatim — the cloud, the zone, sometimes the region |
| `path`, `query` | the request line | verbatim, **identifiers included**: `/servers/{a real UUID}`. Only a parameter whose *name* carries a credential becomes `REDACTED` |
| `operation`, `provider` | what the pack calls this route | invented here, carries nothing of the account |
| `req.headers` | the request's header names | names in full, values dropped unless the name is on the allowlist (`Accept`, `Content-Type`, `User-Agent`, `Host`, …) |
| `req.body`, `res.body` | the payloads | **verbatim**, except values under a key naming a credential. This is the measurement, and it is why the file is worth having |
| `res.headers` | the answer's header names | same rule as the request's |

So the bodies hold what the account holds: resource and project identifiers,
machine and bucket names, public and private addresses, tags, and whatever a
colleague put in a description field. The proxy writes the transcript `0600` and
prints nothing of it, and that is the whole of what the tool can do for you.

**Sanitise before you share, and sanitise completely.** Partial sanitisation is
the trap, not an improvement: Pépin's delivery audit opened on a real instance
UUID left in a fixture whose IP address had been scrubbed — the scrubbing is what
made the file look reviewed. Before a recording, or anything derived from one,
enters a repository or an issue:

1. Replace every identifier — UUIDs, project and account numbers, resource names
   — with invented values, in `path` and in the bodies alike.
2. Replace every address: public IPs, private ranges, DNS names of your own.
3. Re-read the bodies for free text: descriptions, tags, key names, bucket
   names, the name of the person who created the resource.
4. Search the result for the account's own identifiers one last time. `grep` for
   the project UUID is thirty seconds and it is the step the audit's fixture
   skipped.

A test fixture is **built from the observed shape with invented values**, and
says so, rather than being a recording with a few values crossed out. The field
tree is what carries the knowledge; the values never did.

Three things in this repository already do that for you, and none of them is the
transcript: `feint shapes --record` stores field trees with identifiers folded
into `{id}` and no values at all, which is why `shapes/*.json` is committed;
`feint transcript --shape` prints the same tree out of a recording; and
`feint transcript --sanitise`, below, converts the whole recording.

## A transcript you can commit

`shapes/*.json` is committable and throws away exactly what a replay grades
beyond the field tree: the **status**, the **order**, and the sequence itself.
So a recording of a real cloud could never reach `feint replay`, and the replay
had only ever met its own output (#351).

```bash
feint transcript real.jsonl --sanitise corpus/scaleway/scw-cli.jsonl \
  --contract contracts/scaleway.json
```

```text
58 exchange(s) written to corpus/scaleway/scw-cli.jsonl
953 distinct value(s) replaced by a synthetic one of the same shape
1837 value(s) kept: a literal of the API, a word a pack vouches for, a number, a boolean
cross-checked against the recording: no value of the account survived
```

The output is a transcript like any other — `feint replay`, `feint transcript`
and `feint shapes` read it unchanged — with every value replaced.

| kept | dropped |
|---|---|
| method, status, the sequence and its order | every identifier, every address, every CIDR |
| the segments the provider's document states are literals of the path | every other path segment |
| query parameter **names** | query parameter values |
| body field names, JSON types, list order | body values |
| numbers, booleans, nulls | the recording's own timings and clock |
| header values that are HTTP's own vocabulary | every other header value |
| the word that named the client in the User-Agent | the rest of the User-Agent |
| what a pack vouches for (`emulator.Vocabulary`) and what the contract enumerates | everything else |

### Why it still replays

Because the replay already rebinds (above). A transcript whose identifiers are
synthetic is replayed exactly like one whose identifiers are real: the replay
binds `00000000-0000-4000-8000-000000000003` to whatever this emulator answers,
the same way it binds a UUID a cloud handed out.

That is what makes the substitution **shape-preserving** rather than blanket. A
UUID becomes a UUID, an address an address from `198.18.0.0/15`, a CIDR a CIDR
of the same prefix length, an OpenSSH public key a valid OpenSSH public key, a
timestamp a timestamp. A value replaced by a bare `REDACTED` would break the
request that carries it and retype the field that holds it — which is the defect
the proxy's own redaction produced in #73, where nine `null` fields became
strings and read back as nine divergences.

Two things follow, and each has a test that fails without it
(`tools/falsify/specs/sanitised-corpus.json` replays all twelve):

- **the same original always gets the same replacement**, or the identifier a
  create answered would not be the one the read that follows addresses;
- **a value the API validates against a closed list survives**: the zones and
  regions a pack vouches for, and every value the provider's own document
  enumerates. Measured rather than designed — without the second, the first real
  Scaleway corpus replayed four list operations as **400**, because
  `order_by=created_at_desc` had become a synthetic string, and a 400 the
  sanitiser manufactured reads exactly like an emulator defect.

### Default deny, and what it costs

The rule is not a list of what to remove. A redaction by *name* answers "does
this look like a secret" and never "is this not one", which is the trap the
whole of the section above is about. So a value stays only if this repository
publishes it: a literal of the path the provider's document writes, a word a
pack vouches for, a boolean, a run of at most six digits (a page, a size, a
port — Outscale's twelve-digit account number is a *string*, and it goes).

That costs something, and the cost is stated rather than hidden: **a path the
provider's document does not describe loses every segment.** The exchange stays,
with its method, its status and its field tree, and the command lists what it
blanked. Recovering the name means adding the product to
`tools/contract/<provider>-products.txt` and regenerating the contract — never
guessing which segments looked harmless.

Two residuals worth knowing, neither of them repairable by a rule:

- **equality survives.** Two fields holding one value still hold one value after
  the substitution, which is what makes the file replayable and what tells a
  reader that an account's `project_id` and `organization_id` were the same
  string. Nothing of either is published; the fact that they were equal is.
- **a number is kept.** In these three dialects an identifier is a string, and
  the numbers are sizes, counts, ports and prefix lengths that the emulator
  validates a request against. A provider minting a numeric identifier would
  defeat this, and that is an assumption to revisit rather than a property
  proved.

### What checks it

Two controls, both before the file exists, and the command writes nothing if
either speaks:

- **the cross-reference.** Every value of the output is looked up in the source
  recording, and one that is in both — and is not on the short list above — is a
  leak. It does not depend on the sanitiser being right about what a value *is*,
  so it catches the identifier shape a fourth provider invents. This is
  `docs/proxy.md`'s own last step, executable: *"search the result for the
  account's own identifiers one last time"*.
- **the alphabet.** Every value of the output must be one this tool minted or
  one the document publishes. An allowlist over the artefact, not a search for
  dangerous-looking shapes.

Then two tests read the committed files back:
`TestTheCommittedCorpusCarriesOnlyWhatASanitisedTranscriptMay` runs the alphabet
over them, and `TestNoCommittedCorpusCarriesAnIdentifier` reads the bytes and
knows nothing of the rules that produced them — a UUID outside the synthetic
namespace, an address outside the two synthetic spaces, an email or a PEM block
fails it whatever the sanitiser believes.

`corpus/README.md` carries the account rules for making one: free resources
only, everything destroyed, the destruction proved by a read, and the secret
never read by anything but the client that owns it.

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
