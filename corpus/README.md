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

```bash
feint serve &
feint replay corpus/scaleway/terraform.jsonl --endpoint http://127.0.0.1:4599
```

Exit 2 means the emulator and a real cloud disagree, and that is the point of
the file. **Replay against a fresh emulator**: `feint serve --state …` restores
a snapshot, and a list operation then answers objects this corpus never created.

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

## What the first run found

Replaying these two files against the emulator of 2026-08-21: `terraform.jsonl`
matched on all 16 exchanges, `scw-cli.jsonl` on 33 of 58 with 16 unserved and
8 or 9 divergent, in three families.

- **The catalogue is a fixed subset.** `fr-par-1` publishes 136 commercial
  types over three pages; the emulator serves 18, on purpose
  (`internal/providers/scaleway/catalog.go`). The pack declines that shape, but
  the decline is keyed `GET /instance/v1/zones/fr-par-1/products/servers`, which
  is the shapes catalogue's spelling — the replay joins on the mounted operation
  name, so it never sees it and reports every missing type.
- **The default VPC carries no tags.** Scaleway's own default VPC answers
  `tags: ["default"]`; a fresh emulator answers `[]`.
- **The IAM SSH-key lifecycle cannot be replayed at all**, and that is the
  instrument rather than the emulator: the proxy redacts the value under any
  JSON key whose *name* contains "key", so `public_key` reaches the transcript
  as `REDACTED`, `sshkey.Parse` refuses it, and the create answers 400 — taking
  the read and the delete that follow it with it.

**"8 or 9" is not a rounding.** Six runs of the same file against six fresh
emulators graded `vpc/v2/API.ListPrivateNetworks` divergent three times and
matched three times. The cause is in the replay's rebinding, not here: this
account's `project_id` and `organization_id` are the same string, so one
recorded value has two candidate bindings whose emulator counterparts differ,
and `bindings.learn` walks a Go map — whichever field it visits first wins.
When the organisation wins, the create files its network under a project the
unfiltered list does not cover. A gate built on this corpus (#353) has to settle
that before it can call a red run a defect.
