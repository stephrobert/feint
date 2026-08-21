# The corpus: what a real cloud actually answered

`feint replay` compares this emulator's answer with a recorded one. Until this
directory existed it had only ever been proved on the identity case — a
recording made against the emulator, replayed against the emulator — because a
transcript of a real cloud is the inventory of somebody's account and could not
be committed (#351).

These files are that transcript with every value replaced by a synthetic one of
the same shape. They keep what a replay grades — the status, the field trees and
their types, the order of every list, the sequence of the exchanges — and hold
none of the identifiers, addresses, names and free text the account carried.
`docs/proxy.md`, *A transcript you can commit*, states the format and what it
guarantees.

## The gate

Nothing here is worth anything unless something replays it, so something does,
on every pull request:

```bash
mise run corpus:check      # or: feint corpus --check
```

It starts an emulator of its own per file, replays every exchange, and compares.
Thirty milliseconds, offline, no credential and no client binary — which is
exactly why it can be a gate where `mise run conformance` cannot: a hook that
fails on an absent binary teaches `--no-verify`, and that disarms every other
hook at once. It runs in `mise run prepush` and in `.github/workflows/go.yml`.

Three verdicts, never blurred:

| | |
|---|---|
| a **divergence** the acceptance list does not carry | exit 2 |
| an operation **no route serves** | printed, #74's queue, never counted |
| a corpus that **could not be read**, or compared nothing | exit 1 |

The third is the one that gets forgotten, and it is asserted rather than hoped
for: an empty file, an empty directory, and a file whose every exchange is
unserved are each red, each with their own message
(`TestACorpusThatComparesNothingIsRed`). **A corpus that replays nothing is a
failure, never a pass.**

It has a fourth shape, and this directory shipped it for its whole first life. A
corpus can replay everything and still run **none** of the comparisons the packs
declare, because a value and an order are compared only where an invariant names
an operation, and all of Scaleway's name a server. The run prints how many of
each actually ran, and it is red when the packs declare invariants of a kind and
the corpus exercises none of them
(`TestACorpusThatRunsNoDeclaredComparisonIsRed`). *Recording something billed*
below is how that hole was closed, and #343 is why it existed.

`corpus/accepted.json` carries the divergences this repository records rather
than fixes, each with its reason and the issue that retires it, and an entry
excusing nothing fails the gate — so the day a defect is fixed, the gate demands
that its exemption go. The same file dates each recording, and an undated corpus
is red.

One file at a time, against a **fresh** emulator each time: a corpus is a causal
sequence that creates what it later reads, so replaying two into one store makes
a list answer objects the recording never created. The same reason
`feint serve --state …` is the wrong thing to replay against by hand:

```bash
feint serve &
feint replay corpus/scaleway/terraform.jsonl --endpoint http://127.0.0.1:4599
```

## How this corpus ages

A cloud changes. A recording made today describes the cloud of today, and a gate
that fails because the *cloud* moved is a gate somebody disables — taking all of
its coverage with it. So `warn_after_days` in `corpus/accepted.json` **warns and
never fails**, and the reason is a limit of the measurement rather than a
preference: this gate holds one side of the comparison. A red run says the
emulator and the recording disagree, and nothing in the process knows which of
the two moved. Failing on age would be asserting exactly what it cannot measure.

The half that can arbitrate is #359, which replays this corpus against the
**cloud**. Until it exists, the warning names the file, its age and the
procedure below, and 180 days is a chosen horizon rather than a measured one:
about two releases, and short enough that a surface which gained 453 SDK methods
in twelve months has not silently outrun the file.

## What is here

| file | client | what it drives |
|---|---|---|
| `scaleway/terraform.jsonl` | terraform-provider-scaleway 2.81.0 | a VPC and a private network: create, read, update, destroy, plus the refresh reads of two `plan`s |
| `scaleway/scw-cli.jsonl` | scw 2.56.3 | the reads every stack makes before it creates anything (server types, marketplace, servers, IPs, volumes, security groups, images, snapshots, placement groups, VPCs, private networks, IPAM, SSH keys), the same free lifecycle by hand, an IAM SSH key, and two deliberate 404s |
| `exoscale/exo-cli.jsonl` | exo 1.95.1 (egoscale v3.1.36) | the reads every stack makes first (zones, instance types, templates under an explicit `visibility`, ssh keys, security groups, anti-affinity groups, private networks, instances, pools, elastic IPs, block storage, load balancers, quotas), two deliberate 404s, and the free lifecycle: an SSH key, two security groups with a rule each (one on `0.0.0.0/0`, one naming the other group), an anti-affinity group, and a private network created, read, updated, read again and deleted |
| `outscale/oapi-cli-catalogue.jsonl` | oapi-cli 0.13.0 | the five operations a real Outscale endpoint answers **with no account at all**: `ReadRegions`, `ReadVmTypes`, `ReadPublicIpRanges`, `ReadPublicCatalog`, `ReadFlexibleGpuCatalog` |

The two Scaleway files were recorded on 2026-08-21 against a real Scaleway
account in `fr-par`, through `feint proxy`. Nothing billed was created: a VPC, a
private network and an IAM SSH key are free, and each was destroyed with the
destruction proved by a read that answered 404.

`exoscale/exo-cli.jsonl` was recorded the same day against a real Exoscale
account in `ch-gva-2`, under the same rules: an SSH key, a security group, an
anti-affinity group and a private network are free, while an instance, an
elastic IP, a block-storage volume, an NLB and an SKS cluster are billed and
none was created. Every delete is proved inside the recording by a read
answering 404 or a list answering empty, and the account ends as it began.

**`outscale/oapi-cli-catalogue.jsonl` was recorded against no account**, and
that is a measurement rather than a shortcut. Driven with the public placeholder
pair of `tools/conformance/outscale/fake-credentials.env`, in a config file
holding exactly one profile named on the command line, five operations answer
200 to an unknown access key and every authenticated one answers 400
`InvalidParameterValue` 4120 (measured 2026-08-21: `ReadSubregions`,
`ReadCatalog`, `ReadCatalogs`, `ReadQuotas` and `ReadNets` all refused). A
provider's own catalogue is therefore recordable from any station, with no
account to put at risk and no inventory in the answers. **The account half of
Outscale is still to do** (#354): several profiles on the maintainer's station
name third-party organisations and two sit in the sovereign
`cloudgouv-eu-west-1`, so the profile to record against is named by a human
before anything is driven, never guessed.

| `scaleway/scw-instance.jsonl` | scw 2.56.3 | the **billed** lifecycle: one flexible IP reserved, one DEV1-S created with it, read, listed, renamed and its address list reconciled, one deliberate 404, then the server, its root volume and the IP destroyed, each destruction proved by a read |

All three were recorded on 2026-08-21 against a real Scaleway account in
`fr-par`, through `feint proxy`. The first two created nothing billed: a VPC, a
private network and an IAM SSH key are free.

**The third one did, with the owner's explicit and bounded consent, and it is
the only reason two of this repository's own checks run at all.** See *Recording
something billed* below.

## Recording something billed, and why one recording had to be

`feint replay` compares presence and type everywhere. Two things it compares
only where a pack declares an invariant: **a value** and **an order**. Both
declarations exist because both have already cost this repository a defect, and
the order of `Server.public_ips` is #320, which cost a pull request.

Every one of those declarations lives on `CreateServer`, `GetServer` and
`UpdateServer`. A server is billed. The free-resources rule above therefore
produced a corpus that reached **none** of them: `values_checked` was 0,
`orders_checked` was 0, and the gate printed *"0 divergent finding(s)"* over the
top, which reads as "nothing is wrong" and meant "those comparisons did not
happen". A check that never ran had been reading as a check that passed for the
whole first life of this directory.

So one **DEV1-S** and one **flexible IP** were created in `fr-par`, on the
maintainer's account, with consent given for exactly those two objects and
nothing else. They existed from **17:32:27 to 17:32:30 +02:00 on 2026-08-21**:
three seconds, because the server was created `stopped=true` and never booted,
which is all a recording of the control plane needs. The full inventory before
and the full inventory after are identical, family by family, and every
destruction was proved by a read.

The result is `corpus/scaleway/scw-instance.jsonl`, and it moved the gate from
`values_checked=0, orders_checked=0` to **2 and 6**. `feint corpus --check` now
prints both counts and **fails when the packs declare invariants of a kind and
the corpus runs none of them**, so this hole cannot reopen silently. The
condition is the packs' own declaration: a repository declaring nothing is not
asked to exercise anything, because a control that fires where there is nothing
to control is how a gate gets disabled.

**When a measurement seems to need a billed resource, the rule above still
holds: stop and ask.** What made this one defensible was not that it was cheap,
it was that the owner said which two objects, in which region, and for how long,
and that the script proved the account was as it was found.

## Recording another one

The procedure, and the account rules that are not negotiable. They come from
#270's run against the same account, which held.

1. **Take a starting inventory before creating anything**, and keep it. You
   cannot say "the account is as I found it" without the two lists to compare.

   ```bash
   for kind in "vpc vpc" "vpc private-network" "iam ssh-key" \
               "instance server" "instance ip" "instance volume" "lb lb"; do
     # shellcheck disable=SC2086
     scw $kind list -o json >"before-${kind// /-}.json"
   done
   ```

2. **Free resources only.** A VPC, a private network and an SSH key are free. An
   instance, a flexible IP, a volume, a snapshot, a load balancer, a gateway, a
   Kubernetes cluster and a database are **billed**. If a measurement seems to
   need one, stop and say so in the report rather than creating it: whoever owns
   the account decides whether the corpus is worth the euro.

3. **Name everything `feint-corpus-*`**, so a human scanning the console finds
   what this run made at a glance.

4. **Destroy everything, and prove it with a read.** Every script carries a
   `trap … EXIT` so an abort cannot orphan an object, and the proof is a `get`
   that answers 404 or a `list` that answers empty — never the exit code of the
   delete.

5. **Never read, print, copy or write the secret key.** `scw` and the Terraform
   provider read `~/.config/scw/config.yaml` themselves; a stack that names
   `access_key` and `secret_key` is a stack that puts them in a state file.

6. **No account identifier reaches a committed artefact.** That is what the
   sanitisation below is for, and it refuses to write anything when a value of
   the recording survives.

7. **An identifier you invent for a deliberate 404 must not be spelled the way
   the sanitiser spells its own.** A `get` on
   `00000000-0000-4000-8000-000000000093` is a fine way to record a 404 and a
   bad way to survive step 6: the audit finds that string in the recording *and*
   in the artefact, cannot tell "invented by the session" from "a value of the
   account that got through", and refuses the whole run — which is the right
   answer to an ambiguity it must not resolve by guessing. Use something outside
   that shape (`11111111-2222-4333-8444-555555555555`) and let the sanitiser
   replace it like any other.

Then record and convert:

```bash
feint proxy --provider scaleway --upstream https://api.scaleway.com \
  --addr 127.0.0.1:4611 --record raw.jsonl &
SCW_API_URL=http://127.0.0.1:4611 scw vpc vpc create name=feint-corpus-vpc …
# terraform: set the provider's api_url to the proxy, and no credentials at all

feint transcript raw.jsonl --sanitise corpus/scaleway/<name>.jsonl \
  --contract contracts/scaleway.json
rm raw.jsonl        # the recording is not the artefact, and it stays off the repository
```

`--sanitise` cross-references its output against the recording before it writes
anything, and `go test ./internal/cli/ -run Corpus` reads the committed files
back and refuses any value outside the alphabet a sanitised transcript may
carry.

**Outscale and Exoscale are not simply the same commands.** A reverse proxy
cannot relay a SigV4-signed request to a real Outscale account (the signature
covers `Host`), and Exoscale hands a client an address that is not the proxy
mid-session; `docs/proxy.md` measures both and says which flag answers which.
`--forward` answers both at once, and the two recipes below are what #354
measured.

### Exoscale

```bash
feint proxy --provider exoscale --forward '*.exoscale.com' \
  --addr 127.0.0.1:4611 --record raw.jsonl
# in another shell, with the CA path the command printed:
export HTTPS_PROXY=http://127.0.0.1:4611 SSL_CERT_FILE=/tmp/feint-intercept-ca-*.pem
exo -A <account> compute security-group create feint-corpus-web …
```

`exo` is a Go client that installs no `Transport`, so `HTTPS_PROXY` and
`SSL_CERT_FILE` are all it takes and nothing about the client changes. Name the
account with `-A` on **every** call: a client that picks when nobody chose will
eventually pick the wrong one. The handoff `--intercept` exists for does not
bite here, because a forward proxy is reached by the cloud's own name: a run
that lists across zones is reported at exit as *"51 response(s) handed the
client an address that is not this proxy"*, and every one of those addresses was
tunnelled too.

### Outscale

```bash
feint proxy --provider outscale --forward api.<region>.outscale.com \
  --addr 127.0.0.1:4611 --record raw.jsonl
export HTTPS_PROXY=http://127.0.0.1:4611
oapi-cli --config <one-profile.json> --profile=<name> --insecure ReadVmTypes
```

Two things are measured and neither is obvious.

- **`oapi-cli` honours neither `SSL_CERT_FILE` nor `CURL_CA_BUNDLE`**, so the
  tunnel's certificate is refused with `remote error: tls: bad certificate` and
  the client retries into a backoff that reads like an unreachable endpoint.
  `--insecure` is what gets past it, and the hop it relaxes is loopback: the
  proxy still verifies its own hop to the cloud. This is the one client of the
  three for which `--forward`'s "nothing changes in the client" is not true.
- **The signed host is the cloud's own**, because the client asks for
  `api.<region>.outscale.com` and the proxy re-originates to that same host with
  the `Host` header it received. Measured on 2026-08-21 with the placeholder
  credentials: `ReadRegions` answered **200** through the tunnel and every
  authenticated call answered the cloud's own 4120 rather than a transport
  failure. What a *valid* credential then answers is still unmeasured, and
  `docs/proxy.md` says so rather than what it expects.

Name the profile with `--profile` and hand `--config` a file holding that one
profile: there is then no `default` to fall back to, and no stored profile of
the station can be presented by a code path nobody read. **`cloudgouv-*` is
never a recording target.**

## What the runs found

### Exoscale and Outscale (#354)

`exoscale/exo-cli.jsonl` replays 132 of its 203 exchanges clean and reports
**three fields the cloud answers and this emulator omits**, each with an
exemption in `corpus/accepted.json` naming the issue that deletes it:

| operation | field | findings | issue |
|---|---|---|---|
| `exoscale/v2.list-zones` | `zones[].id` | 51 | [#370](https://github.com/stephrobert/feint/issues/370) |
| `exoscale/v2.list-security-groups` | `security-groups[].visibility` | 44 | [#371](https://github.com/stephrobert/feint/issues/371) |
| `exoscale/v2.get-security-group` | `visibility` | 2 | [#371](https://github.com/stephrobert/feint/issues/371) |
| `exoscale/v2.list-security-groups` | `security-groups[].rules[].security-group.name` | 8 | [#371](https://github.com/stephrobert/feint/issues/371) |

**All three have one root, and it is the argument for this whole directory.**
`contracts/exoscale.json` does not declare any of them either: it is generated
from Exoscale's own published description, and the cloud answers fields that
description does not carry. So the shapes gate, the probe and the pack agree
with one another because they read the same document, and no control that reads
a document could ever have disagreed. It is the family of #352's
`has_s3_integration`, one provider further out.

`outscale/oapi-cli-catalogue.jsonl` replays **three matched, zero divergent, two
unserved**: `ReadRegions`, `ReadVmTypes` and `ReadPublicIpRanges` answer the
shape the cloud answers, and `ReadPublicCatalog` and `ReadFlexibleGpuCatalog`
are #74's queue. Small, and it is the first comparison this repository has made
between its Outscale pack and the cloud rather than a document.

**Four defects of the instrument had to go first, again**, and between them they
hid the entire private-network lifecycle behind about twenty findings, none of
them the emulator's. `0.0.0.0/0` had no replacement of its own shape, so the
audit found it on both sides and refused to write anything; a dotted netmask
went through the address mint and came out a host address, so the create
answered `400 netmask is not a usable IPv4 netmask`; `start-ip` and `end-ip`
were minted in the order the walk met them, so the range ran backwards and the
same create answered `400 end-ip is below start-ip`; and a counter shifted by a
/20's twelve bits walked out of `198.18.0.0/15` on Outscale's 90 public blocks.
Each is falsified in `tools/falsify/specs/sanitised-corpus.json`, and the
CHANGELOG carries the whole of it.

**One limit is worth writing down rather than rediscovering.** The synthetic
IPv4 space is one /15: it holds 512 /24s and 32 /20s, and a recording carrying
more blocks of one length than that has no shape-preserving sanitisation at all.
It is refused, by name, as a replacement handed out twice
(`TestASpaceWithNoRoomLeftIsRefusedRatherThanOverrun`) — never written past the
end of the block. `ReadPublicIpRanges` publishes 90 blocks today and fits;
another provider's whole address space might not.

### Scaleway (#352)

**The billed recording found five, and none of them is the instrument.** That is
worth saying, because the first run's eight were mostly the instrument: two were
the replay grading data-keyed maps as fields, five were the proxy's own
redaction destroying `public_key`. #355 fixed the instrument. What
`scw-instance.jsonl` surfaced is the emulator and the cloud disagreeing, in
twenty-six findings with five causes, each carried in `accepted.json` with its
reason and the issue that deletes its entry:

- **#365, the root volume.** The cloud gave the DEV1-S an **SBS** root volume
  and `scw` read it through `block/v1alpha1`, answering 200; this emulator
  attaches a local `instance/v1` volume, so the read and the delete answer 404.
- **#366**, two keys the cloud writes and this emulator omits: `bootscript`
  (`null`) and `extra_networks` (`[]`). Untriaged: they are either served or
  declared in `DeclinedFields()`, and a corpus exemption is neither.
- **#367**, `image.from_server` is an empty string on the wire and `null` here.
- **#368**, an attached public IP publishes no `gateway` and drops its own tags,
  in `public_ip` and in every element of `public_ips`.
- **#369**, `createServer` honours a project the request names and `listServers`
  scopes an unfiltered list to the default project, so the list answers zero
  where the cloud answered one.

They are classified and **not** fixed, deliberately: classifying a divergence and
correcting it are two pieces of work, and doing them in one pass is how a
classification becomes whatever the patch happened to make green.

And the one that did **not** diverge is the point of the exercise: the order of
`Server.public_ips` matched on the create, on the four reads and on the update.
That is #320's guard, and this recording is the first thing in this repository
that has ever exercised it against a real cloud's answer.

Both files replay against the emulator of 2026-08-21 with **no divergence and no
exemption**: `terraform.jsonl` matches on all 16 exchanges, `scw-cli.jsonl` on 42
of 58 with 16 unserved. `corpus/accepted.json` carried an empty `accepted` list
after this run, and that was a result rather than a default — the gate went in
carrying the eight divergences the first run found, each with #355 written
beside it, and the staleness rule made their deletion compulsory the day the
emulator stopped producing them. The four entries the file carries today are
Exoscale's, above.

The eight had three causes, and saying which was the work.

- **Two were the instrument, not the emulator.** `feint replay` graded the keys
  of the commercial-type catalogue as *fields*: `fr-par-1` publishes 136 types
  and this catalogue stocks 18 on purpose, so 127 entries of an inventory read
  as 127 missing fields — while `feint shapes --check` held the opposite rule on
  the same artefact and reported none of them. A key of a map whose keys are
  data is a value, and values are compared only where a pack declares an
  invariant. One rule now, `transcript.DataKeyed`, read by both gates.
- **One was a declaration in the wrong dialect.** The pack argued the missing
  `per_volume_constraint.l_ssd` bound to the gate that joins on the catalogue
  key (`GET /instance/v1/zones/fr-par-1/products/servers`) and to no other, so
  the replay — which joins on the mounted operation name — met no refusal and
  called nine deliberate omissions nine divergences. The decision is now spelled
  in both dialects.
- **Five were one substitution the recorder made.** The proxy redacts the value
  under any JSON key whose *name* contains `key`, so `public_key` reached the
  transcript as `REDACTED`, `sshkey.Parse` refused it, the create answered 400
  where the cloud answered 200, and the read and the delete that followed
  answered 404 for that one reason. **The IAM SSH-key lifecycle was
  unrecordable.** The redaction now writes down a value whose own format proves
  it is published — an OpenSSH public key line, in a body, read by the same
  `internal/core/sshkey` the packs authenticate with — and nothing else moved:
  headers keep their allowlist, the query keeps the denylist, and a container
  named for a credential is still replaced whole. `docs/proxy.md`, *The one
  value a credential-shaped name does not take with it*, states it with its
  falsification.

**Fixing the instrument is what let the emulator's own defects be seen**, and
there were three:

- **The default VPC carried no tags.** Scaleway's own answers `tags:
  ["default"]`; a fresh emulator answered `[]`. Measured twice on 2026-08-21, by
  `scw vpc vpc list` against a real account and by this recording.
- **`CreateSSHKey` answered 201 where the wire carried 200** — the same family
  as the two `vpc/v2` creates #270 found by hand, and invisible until the key's
  lifecycle could be replayed at all, because the 400 above hid it.
- **An SSH key was published with the comment the client sent**, where the cloud
  drops it. This one the corpus *records* without grading: the sanitiser
  replaces both strings with valid synthetic keys, and the request's and the
  answer's became two **different** synthetic keys — which is the fact, visible
  in the committed file. Confirmed straight at the cloud on 2026-08-21 (98 bytes
  and three fields in, 80 bytes and two fields out) and asserted by a test of
  its own, because a value is not something this gate can hold.

That second one is the argument for the whole chain in one line: a defect that
three other controls could not see, found by replaying a recording of the real
cloud, and only after the recording stopped lying about itself.

**It used to be "8 or 9", and that was not a rounding.** Six runs of the same
file against six fresh emulators graded `vpc/v2/API.ListPrivateNetworks`
divergent three times and matched three times. The cause was in the replay's
rebinding rather than here: this account's `project_id` and `organization_id`
are the same string, so one recorded value had two candidate bindings whose
emulator counterparts differ, and `bindings.learn` walked a Go map — whichever
field it visited first won. When the organisation won, the create filed its
network under a project the unfiltered list does not cover, and the replay
reported a divergence it had manufactured itself.

#353 settled it before the gate went in, because a non-deterministic gate is a
gate that gets disarmed the first time it seems to lie. A binding is now scoped
to the field name it was observed under — `project_id` resolves to the project
this emulator minted and `organization_id` to the organisation — and the walk
that learns them is sorted, so a value with no field to scope it resolves the
same way on every run. `TestOneRecordedValueUnderTwoFieldsBindsByFieldName` and
`TestTheSameRecordingBindsTheSameWayOnEveryRun` hold both halves, and
`TestTheCommittedCorpusPassesItsOwnGateAndSaysTheSameThingTwice` holds the
whole run.

## What it still cannot see

Stated here rather than discovered again. A container whose *name* matches the
redaction's denylist is replaced whole, elements included: `ssh_keys` matches
`key`, so `iam/v1alpha1/API.ListSSHKeys` reaches this corpus as a single string
and the shape of its elements is not graded. `feint replay` reports those as
`redacted` findings — counted out loud, never mistaken for a comparison that
happened — and the alternative, descending into every object somebody called
`credentials`, is the larger risk. Ten paths of `scw-cli.jsonl` are also blanked
entirely because `contracts/scaleway.json` does not describe them; the exchanges
and their shapes are kept, and the command that wrote the file lists what it
blanked.

The same rule costs Exoscale the same thing, on the same field name:
`exoscale/v2.list-ssh-keys` answers `{"ssh-keys": [...]}`, `ssh-keys` matches
`key`, and the array reaches this corpus as one string. Three of the nine
`redacted` findings that file reports are that. The key's own lifecycle is still
graded, because `register-ssh-key` carries the key in a field whose *value*
proves it is published (an OpenSSH public key line, read by the same
`internal/core/sshkey` the packs authenticate with) and `get-ssh-key` answers a
name and a fingerprint under no credential-shaped name at all.

**And the account half of Outscale is not here yet**, which is a gap rather than
a limit: the catalogue file covers what any station can record, and nothing of
Nets, Subnets, security groups, route tables or keypairs. #354 says why, and the
answer is a rule rather than an obstacle — the profile is named by a human
before anything is driven.
