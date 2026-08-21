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

Both were recorded on 2026-08-21 against a real Scaleway account in `fr-par`,
through `feint proxy`. Nothing billed was created: a VPC, a private network and
an IAM SSH key are free, and each was destroyed with the destruction proved by a
read that answered 404.

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
Several Outscale profiles on the maintainer's station also name third-party
organisations, two of them in a sovereign region. That is #354, with its own
guard rails.

## What the runs found

Replaying these two files against the emulator of 2026-08-21: `terraform.jsonl`
matched on all 16 exchanges, `scw-cli.jsonl` on 34 of 58 with 16 unserved and 8
divergent, in three families. All eight are listed in `corpus/accepted.json`
with their reason and are fixed by #355; the gate is green because it carries
them, and it goes red the moment one is fixed and its entry is not deleted.

- **The catalogue is a fixed subset.** `fr-par-1` publishes 136 commercial types
  over three pages; the emulator serves 18, on purpose
  (`internal/providers/scaleway/catalog.go`). The pack declines that shape, but
  the decline is keyed `GET /instance/v1/zones/fr-par-1/products/servers`, which
  is the shapes catalogue's spelling — the replay joins on the mounted operation
  name, so it never sees it and reports every missing type. 136 findings.
- **The default VPC carries no tags.** Scaleway's own default VPC answers
  `tags: ["default"]`; a fresh emulator answers `[]`. A defect of the emulator,
  and the first this corpus surfaced.
- **The IAM SSH-key lifecycle cannot be replayed at all**, and that is the
  instrument rather than the emulator: the proxy redacts the value under any
  JSON key whose *name* contains "key", so `public_key` reaches the transcript
  as `REDACTED`, `sshkey.Parse` refuses it, and the create answers 400 — taking
  the read and the delete that follow it with it.

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
