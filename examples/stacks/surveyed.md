# Fifteen strangers' stacks, applied against this emulator

The register for [#262](https://github.com/stephrobert/feint/issues/262):
five substantial Terraform stacks per provider, written by people who have
never seen this repository, applied against feint on 2026-08-17. Every entry
carries what a reader needs to replay it without asking anybody — repository,
commit, licence, the harness environment, the verdict, every edit made and
why it was unavoidable, and the issues it produced.

The defects this exercise filed: [#268](https://github.com/stephrobert/feint/issues/268)
(a Vm's placement does not round-trip), [#269](https://github.com/stephrobert/feint/issues/269)
(one declared subregion contradicts the write path), [#270](https://github.com/stephrobert/feint/issues/270)
(a Private Network without its IPv6 subnet), [#271](https://github.com/stephrobert/feint/issues/271)
(the template list ignores its visibility filter). The regression that did
**not** reappear is worth a line too: ztiac routes through a Net peering —
the exact #249 shape, written by strangers — and applied with an empty
second plan.

## Replayed on `main@23f57c1` for the 0.9.0 qualification (2026-08-18)

The register was replayed the day after it was written, on the commit
candidate for 0.9.0, with the four fixes it produced (#268–#271) merged.
Same harness, same commits, same edits, stacks run sequentially; the only
harness deltas were a freshly minted intercept CA and re-seeded
prerequisites (keypairs, templates, the devbox snapshot, the talos images).
Each entry below carries a **Replayed** line; the original verdicts stand
above them as the before-picture.

What the replay measured, in one paragraph. All four fixes hold on the
stacks that revealed them: kasten's second plan is now empty (#268),
ocp_outscale went from plan-blocked at zero resources to 30 applied,
converged and destroyed (#269), talos destroys cleanly and reads a real
IPv6 `/64` off its Private Network (#270), and openshift4's
`visibility=private` read answers exactly the organisation's templates
(#271). Two findings came out of the replay, one issue each: **ztiac lost
its empty second plan** to the same catalogue enforcement that fixed #269 —
its hardcoded `cloudgouv-eu-west-1a/b/c` zones are now refused with an
explicit 400, 95 → 53 resources ([#290]) — and **one new lying 200**:
concurrent `CreateSecurityGroupRule` calls can lose an acknowledged rule,
found when terraform-outscale-k3s's destroy tripped over the phantom
([#289]). No re-plan of any replayed stack showed an in-place change:
everything that applied read back as created.

[#289]: https://github.com/stephrobert/feint/issues/289
[#290]: https://github.com/stephrobert/feint/issues/290

## How every stack was pointed here

One emulator, one recording proxy in front of it (the transcripts quoted in
the issues come from it):

```bash
go build -o feint ./cmd/feint
FEINT_EXOSCALE_ALLOW_TERRAFORM=1 ./feint serve --addr 127.0.0.1:4610 --contracts contracts
./feint proxy --addr 127.0.0.1:4611 --upstream http://127.0.0.1:4610 --record /tmp/rec.jsonl
```

**Name the runtime, always.** The command above carries no `--vm`, which means
`off`: the measurements of 2026-08-17 and of 2026-08-18 ran with **no machine
runtime at all**, and every server in them was a record. #262's delivery comment
says `--vm off` in as many words, but this register did not, and the silence was
later read as evidence that no runtime had been asked for — an empty cell is
information about the report, not about what was ordered. The 2026-08-24 section
at the end of this file is the first pass on a real one, `--vm incus-ovn`.

**So the runtime is a required datum of any replay, exactly like the commit and
the recorded edits: a replay that does not name its `--vm` is not reproducible,
and its silence will one day be re-read as a fact.**

Per provider, everything rides the environment — no stack needs an endpoint
edit **except** where the table below says so:

| provider | environment that reaches the emulator | trap, measured |
|---|---|---|
| Scaleway (provider 2.x) | `feint env scaleway`, with `SCW_API_URL` pointing at the proxy | Object Storage still goes to the real `s3.<region>.scw.cloud` (docs/limits.md); measured live on flatcar-k3s, a 403 from the real endpoint. Since #280, `feint doctor` and `feint env scaleway` warn from the stack directory when its text carries `scaleway_object_*` or a real `*.scw.cloud` host |
| Outscale 1.7+ | `OSC_ENDPOINT_API=http://…:4611/api/v1` — the path belongs in the value | without the path: six-minute retry backoff |
| Outscale CLI `octl` (0.0.31, measured 2026-08-25) | `OSC_ENDPOINT_API=http://…:4611/api/v1` — the same shape as 1.7+, and for the same reason: both read `osc-sdk-go`, whose default endpoint template is `%s://api.%s.outscale.com/api/v1` | given the bare host it posts `/<Call>` at the root, a plain 404. Also: `--config`/`--profile` make the FILE win over the environment, so `endpoints.api` there must carry the path too — the opposite precedence to the archived `oapi-cli` |
| Outscale 1.1.x, and the archived `oapi-cli` | `OSC_ENDPOINT_API=http://…:4611` — **no path**; these clients append `/api/v1` themselves | with the path: `invalid port ":4611%2Fapi%2Fv1"` — the legacy client URL-escapes it; `oapi-cli` requests `/api/v1/api/v1/<Call>` and gets the doubled-prefix 404 |
| Outscale 0.x (`outscale-dev/*`) | none exists. Zero exchanges reached the recorder with `OSC_ENDPOINT_API` and `OUTSCALE_OAPI_URL` both set | 0.5.3 honours only the `endpoints{api=…}` block, prepends `https://` to it, and needs `feint proxy --intercept localhost` + `SSL_CERT_FILE`; 0.7.0 honours nothing found and SIGSEGVs in its own create-retry error path |
| Exoscale | the pinned fork (docs/limits.md, commit `2e78b42`) via `dev_overrides`, `EXOSCALE_API_ENDPOINT=http://…:4611/v2`, and the emulator started with `FEINT_EXOSCALE_ALLOW_TERRAFORM=1` | the published provider splits between emulator and paying account — never point it here |

Also set `OUTSCALE_ACCESSKEYID`/`OUTSCALE_SECRETKEYID` **unset** for the 1.x
providers: with the legacy names present, 1.1.3 takes a config path that
ignores `OSC_ENDPOINT_API` and talks to the real API.

**Correction, 2026-08-19 (#286).** Re-measured in isolation, the trigger above
is wrong. Four combinations of the legacy credential names against 1.1.3 —
alone, beside the `OSC_*` pair, with credentials in the provider block, with
and without `OSC_REGION` — all reached the emulator while `OSC_ENDPOINT_API`
was set, and 1.8.0 behaves the same. What ignores `OSC_ENDPOINT_API` is the
**old-profile path**: with `OSC_PROFILE` set (or `profile` in the provider
block), 1.1.3's `providerConfigureClient` calls `setProviderDefaultEnv` — the
only reader of `OSC_ENDPOINT_API` — only when `IsOldProfileSet` says no
profile is in force. Reproduced: `OSC_PROFILE=default` plus a profile without
an `endpoints` key sent the plan to `https://api.<region>.outscale.com` while
the emulator received nothing. The harness that produced the line above
carried `~/.osc/config.json` in a disposable `HOME` (ztiac), which is how the
two triggers were conflated. `feint env outscale` and `feint doctor` now warn
on both: the profile variables as the measured escape, the legacy names as
real-cloud credentials one lost export away from being signed with.

Scores are `/10` and answer one question — *does the repository as published
let a reader run this infrastructure?* — from observable facts (pins,
backend, secrets, docs, maintenance). They are independent of whether feint
could serve the stack: a product feint declines says nothing about the
stack's quality.

## Outscale — five of five reached the emulator, four applied

### 1. outscale/osc-k8s-rke-cluster — applied after a recorded edit

- **Repo** <https://github.com/outscale/osc-k8s-rke-cluster> @
  `7427b9835045f72f922a2e2c4572baa52afabe70` — BSD-3-Clause. Archived
  official example: Net, two subnets, five security groups, three keypairs,
  four public IPs, bastion + control-plane + worker Vms, RKE and ansible on
  top. `terraform.tfvars` is committed with working values.
- **Verdict**: applied — 50 resources created and destroyed. The RKE/ansible
  tail (local `shell` provider against the fictional bastion IP) fails
  locally and keeps the dependent `outscale_image` from being attempted;
  every request the emulator received answered 200, except
  `CreateLoadBalancer`, declined by name.
- **Recorded edit** (`providers.tf`): pin `outscale = { version = "~> 1.1.0" }`.
  The stack pins nothing, latest (1.8.0) refuses its own two `tags` blocks
  client-side — `Duplicate Tag key … The following keys are duplicates: []`
  — before a single HTTP call (zero recorder exchanges). An upstream
  regression against the vendor's own archived example.
- **Harness input**: `TF_VAR_access_key_id/secret_key_id/region` (fake), 1.1
  endpoint shape.
- **Declined reached**: `CreateLoadBalancer`.
- **Score 7/10** — committed tfvars and real docs get it close; the unpinned
  provider is exactly what broke it, and there is no backend.
- **Replayed 2026-08-18, `main@23f57c1`**: identical — 50 created, the local
  RKE/ansible tail times out against the fictional bastion, re-plan
  `17 to add / 0 to change` (all behind that tail), 50 destroyed.
- **Replayed 2026-08-19, `feat/281-outscale-lbu`**: the LBU wall is down —
  `outscale_load_balancer.lb-kube-apiserver` and its
  `outscale_load_balancer_attributes` health check (TCP 6443) now apply,
  and `local_file.kube-apiserver-url` receives a well-formed
  `dns_name`; 53 created, re-plan `14 to add / 0 to change` (the three
  fewer are exactly the LBU trio), 53 destroyed cleanly (`DeleteLoadBalancer`
  then the SG and subnet it stood on). The remaining residue is the recorded
  RKE/ansible tail against the fictional bastion, unchanged;
  `outscale_load_balancer_vms.backend_vms` still sits behind it (its
  control-plane Vms need the image that tail builds), so this stack's
  measured LBU trace is Create ×1, Read ×7, Update ×1, Delete ×1.

### 2. chimere-eu/ztiac — applied as-is, both templates

- **Repo** <https://github.com/chimere-eu/ztiac> @
  `093330c7964da5ae33a812389833036b9cfacbc5` — MIT. Zero-Trust IaC on
  SecNumCloud Outscale (France 2030 co-funded): reusable VPC module,
  `templates/outscale/advanced-network` (three Nets × NAT-per-AZ ×
  public/private route tables, a peering, and the routes through it) and
  `templates/outscale/two-tier-architecture` (VMs, keypairs from files,
  internal + public LBU).
- **Verdict**: `advanced-network` applied — **95 resources, empty second
  plan, clean destroy**; the #249 fix held under a stranger's configuration.
  `two-tier-architecture` applied 47 of 49; the two `outscale_load_balancer`
  are declined by name and are the only re-plan residue.
- **Edits**: none. Credentials come from `~/.osc/config.json` (profile
  `default`): a disposable `HOME` carried that file with
  `endpoints.api = http://127.0.0.1:4611/api/v1`.
- **Harness input** (two-tier): `image_id=ami-a3ca408c`,
  `vm_type=tinav5.c2r4p2`, `allowed_cidr=["0.0.0.0/0"]`, a public-key path.
- **Note**: the templates hardcode `cloudgouv-eu-west-1a/b/c` zones. On
  2026-08-17 this passed only because feint stored any subregion string
  verbatim — the write-path half of the #269 asymmetry. The #269 fix closed
  that door and took this stack down with it (53 resources, 42 proposed
  re-adds: the emulator was frozen to `eu-west-2`); #290 made the region a
  datum instead. Replayed 2026-08-18 at this same commit against
  `FEINT_OUTSCALE_REGION=cloudgouv-eu-west-1`, response contracts on:
  **95 resources, empty second plan, clean destroy** — the survey's reference
  figure, now reached with every zone declared by `ReadSubregions` and
  validated by the write paths, instead of stored unchecked.
- **Declined reached**: `CreateLoadBalancer` ×2.
- **Score 8/10** — documented templates, no secret in the tree, floor-pinned
  provider (`>= 1.1.3`, not exact) and no backend keep it off 9.
- **Replayed 2026-08-18, `main@23f57c1`**: **regressed, filed [#290]** — the
  asymmetry the note above pointed at bit the other way. The #269 fix makes
  the catalogue the authority every write checks, and the hardcoded
  `cloudgouv-eu-west-1a/b/c` zones are now refused: `CreateSubnet` answers
  400 `[4001] the Subregion cloudgouv-eu-west-1a does not exist in
  eu-west-2`. `advanced-network`: 53 of 95 applied (the peering and all 14
  routes through it — the #249 shape — still apply and destroy cleanly),
  re-plan `42 to add / 0 to change`, 53 destroyed. `two-tier-architecture`:
  25 of 49, re-plan `29 to add / 0 to change`, 25 destroyed. An explicit
  refusal replacing a verbatim 200, and the register's only fully
  converging stranger stack lost to it: the decision is #290's.
- **Replayed 2026-08-19, `feat/281-outscale-lbu`** (`two-tier-architecture`,
  emulator started with `FEINT_OUTSCALE_REGION=cloudgouv-eu-west-1` per
  #290): **converged, LBU included** — applied **54 of 54** (the 49 as
  surveyed plus the internal and public `outscale_load_balancer`, their two
  `outscale_load_balancer_vms` and the `outscale_load_balancer_attributes`
  health check), second plan `No changes.`, 54 destroyed. The replay also
  overturned a source reading: provider 1.8.0 attaches backends through
  `LinkLoadBalancerBackendMachines`, where 1.1.3's code says
  `RegisterVmsInLoadBalancer` — both are served because both were measured
  (#281). Provider 1.8.0's destroy sweep asks `ReadLoadBalancers` with an
  empty body before every `DeleteSecurityGroup`; the pack's SG delete guard
  is the refusal that sweep exists for.

### 3. davmartini/ocp_outscale — could not be applied (plan-blocked), filed #269

- **Repo** <https://github.com/davmartini/ocp_outscale> @
  `1c7fd7b917707fc9616ed3cfcb9222069587f756` — no licence file. OpenShift on
  Outscale in three stages; stage `01_ocp_create_network` carries the Net,
  four subnets spread over data-sourced AZs, NAT ×2, route tables ×4, EIPs.
- **Verdict**: could not be applied. `data.outscale_subregions` answers one
  element and `main.tf` indexes `subregions[1]`: plan dies, zero resources.
  That is #269, and this stack is its witness.
- **Recorded edits** (`config.tf`): the pinned `outscale-dev/outscale` 0.5.3
  reads no endpoint environment variable at all (proven by an empty
  recorder), so its only redirect — `endpoints { api = "localhost:4612" }` —
  was added, served over TLS by `feint proxy --intercept localhost` because
  0.5.3 also hardwires `https://`.
- **Harness input**: nine TF_VARs (keys, region, vm type, image id, keypair
  name, three DNS IPs).
- **Score 6/10** — staged layout and a thorough README, but a dead-namespace
  exact pin (0.5.3), no licence, no backend.
- **Replayed 2026-08-18, `main@23f57c1`**: **plan-blocked → applied** — the
  #269 fix verified on its witness. `ReadSubregions` answers both of
  eu-west-2's zones, `subregions[1]` indexes, and the whole stage applies:
  30 resources, empty re-plan, 30 destroyed. Same recorded edits still
  required (0.5.3's `endpoints` block, the TLS intercept on 4612).

### 4. pli01/terraform-outscale-k3s — applied after recorded edits

- **Repo** <https://github.com/pli01/terraform-outscale-k3s> @
  `e68aa435590fd7c5075dd06392c97f9560ca94a1` — no licence file. k3s on
  Outscale: base/bastion/app modules, Net + subnets + NAT, 15 SG rules,
  cloud-init that installs k3s, two LBUs.
- **Verdict**: applied — 37 resources; the two `outscale_load_balancer` (and
  their vm attachments) are the only failures, declined by name; no in-place
  drift on anything created; clean destroy.
- **Recorded edits**: `source = "outscale/outscale", version = "~> 1.1.0"`
  in root + three module `versions.tf`. Unpinned `outscale-dev/outscale`
  resolves to 0.7.0, which reaches no endpoint this harness could offer and
  crashes (SIGSEGV, `resource_outscale_internet_service.go:55`) in its own
  error path. An `endpoints` block accepted by 0.5.3 crashes 1.1.3 at
  Configure, so the env redirect is the only shape that serves both.
- **Prerequisite seeded through the served API**: the keypair the stack
  references (`CreateKeypair` + public key) — feint refused the Vm with
  upstream's own `5063 InvalidResource` until it existed, which is correct.
- **Declined reached**: `CreateLoadBalancer` ×2.
- **Score 5/10** — modules and CI scaffolding, but an unpinned provider on a
  deprecated namespace, token-shaped variables (`dockerhub_token`,
  `github_token`) that nothing documents, no licence, no backend.
- **Replayed 2026-08-18, `main@23f57c1`**: applied 37 as surveyed, LBU ×2
  declined, same re-plan residue — and the destroy that was clean **found
  [#289]**: of the stack's 15 concurrently created security group rules,
  one was acknowledged 200 and never stored, so `destroy` died revoking a
  rule the emulator never held (`[5063] the security group rule on
  sg-08bf69ac does not exist`), 34 of 37 destroyed. The one new lying 200
  of this replay; reduced and re-verified in the issue.
- **Replayed 2026-08-19, `feat/281-outscale-lbu`**: **converged** — the LBU
  wall is down (#281). Applied **41 of 41** (the 37 as surveyed plus the two
  `outscale_load_balancer` and their two `outscale_load_balancer_vms`),
  second plan `No changes.`, 41 destroyed. Provider 1.1.3's measured LBU
  sequence: CreateLoadBalancer ×2, RegisterVmsInLoadBalancer ×2,
  ReadLoadBalancers ×14, then UnlinkLoadBalancerBackendMachines ×2 and
  DeleteLoadBalancer ×2 on destroy.

### 5. michaelcourcy/kasten-on-outscale — applied after a recorded edit, filed #268

- **Repo** <https://github.com/michaelcourcy/kasten-on-outscale> @
  `f1fcc874f62f7a9d19c0f35a1e01963d08a781b4` — no licence file. RKE + Kasten
  lab: Net, four subnets over two AZs, NAT, five Vms, route tables, EIPs.
- **Verdict**: applied — 30 resources, clean destroy — and the second plan
  never converges: the worker placed in `eu-west-2b` reads back
  `eu-west-2a`. That is #268; the reduced single-resource reproduction is in
  the issue and was re-verified after reduction.
- **Recorded edit** (`main.tf`): `outscale/outscale ~> 1.1.0` replacing the
  exact pin `outscale-dev/outscale 0.2.0` (2021), which predates every
  redirect this harness has.
- **Prerequisite seeded**: keypair `michael`.
- **Score 4/10** — a working lab, but an ancient exact pin, a committed
  `terraform.tfstate.backup`, no licence, prerequisites a reader must
  reverse-engineer.
- **Replayed 2026-08-18, `main@23f57c1`**: **converged** — the #268 fix
  verified on its witness. 30 applied, the `eu-west-2b` worker reads back
  `eu-west-2b` (measured in the transcript: 4 Vms placed in 2a, 1 in 2b),
  **empty second plan** where the survey re-planned for ever, 30 destroyed.

**Outscale mean: 6.0.** Few stacks, but every one carries a full network
plane. The ecosystem's real hazard is client-side: four provider
generations, three endpoint mechanisms, two upstream crashes — the emulator
answered everything the modern provider sent.

## Exoscale — three applied, two blocked by unserved products

### 1. appuio/terraform-openshift4-exoscale — applied after recorded edits, filed #271

- **Repo** <https://github.com/appuio/terraform-openshift4-exoscale> @
  `46016eac5f08f7140925999216d1fbc4ea2ca30b` — no licence file at the
  commit. VSHN's production OpenShift 4 on Exoscale: private network,
  five security groups + rules, ssh key, anti-affinity, bootstrap/master/
  infra/storage/worker instance pools and instances, DNS, an lb module.
- **Verdict**: applied — 42 resources, no in-place drift, 43 destroyed. The
  DNS branch (`exoscale_domain` + records) cannot apply: DNS is not served,
  and the failure surfaces as `find zone "ch-gva-2" not found in
  ListZonesResponse` because the DNS client resolves through the zone list
  (single-zone catalogue, `internal/providers/exoscale/catalog.go` — a
  measured decision). Since #278, a deployment configured with
  `FEINT_EXOSCALE_ZONE=ch-gva-2` answers that resolution, and the same
  branch fails naming its real cause instead — `feint does not serve
  /v2/dns-domain` (measured on the route, 2026-08-19). Since #284 the
  unconfigured deployment stopped misleading too: its zone list signposts
  the zones it does not serve to the Terraform provider, and the same
  `exoscale_domain` apply is refused with the mismatch named — "this
  deployment serves zone ch-dk-2, and the client resolved zone ch-gva-2 …
  Restart with FEINT_EXOSCALE_ZONE=ch-gva-2 …" (measured with the patched
  provider against both deployments, 2026-08-19). Its
  `visibility=private` template reads produced the #271 transcript.
- **Recorded edit** (`security_groups.tf`): `icmp_code = 0` beside
  `icmp_type = 8` — the stack pins provider 0.68.0, only the 0.70-based
  patched fork reaches the emulator, and 0.70 refuses the pair client-side.
- **Harness inputs**: nine TF_VARs (cluster id, domain, rhcos template name,
  ssh key, four VSHN-internal strings, `lb_count=0` — the lb module's
  template datasource nulls out under `dev_overrides`, a provider-side
  effect feint's answers do not explain; its exchanges were well-formed and
  the same datasource works in isolation).
- **Prerequisite seeded through the served API**: `register-template`
  ×2 — `rhcos-4.15` (the stack's documented "upload the RHCOS template
  first" step) and `Linux Ubuntu 22.04 LTS 64-bit` (the lb module hardcodes
  that catalogue name; the fictional catalogue stops at 24.04).
- **Declined reached**: DNS (domain + records); NLB untouched here.
- **Score 7/10** — renovate-maintained, modular, pinned exactly; loses on
  required variables only a VSHN insider can fill and no licence file.
- **Replayed 2026-08-18, `main@23f57c1`**: the #271 fix verified on its
  witness — `GET /v2/template?visibility=private` answers exactly the two
  registered private templates, not the public catalogue. 43 applied (one
  more than surveyed), the unserved DNS branch is the only failure, re-plan
  carries only that branch (`0 to change`), 43 destroyed.

### 2. PhilippeChepy/platform — applied as-is (base layer)

- **Repo** <https://github.com/PhilippeChepy/platform> @
  `84791e3fd06e52084c7e05efcb0d39b65513dabc` — no licence file. A PaaS on
  Exoscale: packer, four terraform layers, Vault, Kubernetes. Layer
  `terraform-base` applied: PKI (tls provider), 11 security groups + rules,
  anti-affinity groups, instance pools, NLB, SOS backup buckets through the
  aws provider.
- **Verdict**: applied — 19 resources, clean destroy. Blocked branches:
  `exoscale_nlb` (`POST /v2/load-balancer`, not served, refused by name) and
  the SOS buckets (aws provider pointed at the real `sos-<zone>.exo.io`;
  fake credentials, 403, nothing spent). Later layers need a live Vault and
  were out of scope by design.
- **Edits**: none. `locals.tf` is the documented operator file (from
  `locals.tf.example`): zones set to `ch-dk-2`, admin networks pinned.
- **Declined reached**: NLB (+5 `exoscale_nlb_service` behind it); SOS.
- **Score 7/10** — genuinely documented layering and an example operator
  file; floor pins (`>= 0.49`) and the multi-layer Vault coupling keep it
  from 8.
- **Replayed 2026-08-18, `main@23f57c1`**: identical — 19 applied, NLB
  refused by name, SOS at the real endpoint on fake credentials, re-plan
  `6 to add / 0 to change`, 19 destroyed.
- **Replayed 2026-08-19 on the #278 branch, against
  `FEINT_EXOSCALE_ZONE=de-fra-1`**: the zone pin is gone from the harness.
  `locals.tf` keeps the example's own `platform_zone = "de-fra-1"` — the
  default the survey had to overwrite, because the emulator froze `ch-dk-2` —
  and the operator file's only remaining input is the admin-network pin.
  Same figures as the reference: **19 applied, re-plan `6 to add / 0 to
  change`, 19 destroyed**; the six re-adds are the NLB branch (refused, its
  five services behind it) and the SOS pair at the real
  `sos-de-muc-1.exo.io` on fake credentials, exactly as on `ch-dk-2`.
- **Replayed 2026-08-21 on the #345 branch, against a `de-fra-1` emulator**:
  the NLB branch is no longer refused. **20 applied** where every earlier
  replay gave 19 — the extra resource is `exoscale_nlb.load_balancer` — and
  `module.vault_cluster.data.exoscale_nlb.endpoint` resolves, which is the
  per-id read the exo CLI never makes. Re-plan **`5 to add / 0 to change`**
  against the previous `6`, **20 destroyed**. The five are the two SOS buckets
  at the real `sos-de-muc-1.exo.io` on fake credentials, the vault module's
  instance pool, the `local_file` behind it, and the one
  `exoscale_nlb_service` behind that: the service is blocked by the SOS
  branch, not by this emulator.

### 3. PhilippeChepy/terraform-exoscale-vault — applied after a recorded edit, fully green

- **Repo** <https://github.com/PhilippeChepy/terraform-exoscale-vault> @
  `62d27eeb1712a65443c0c1b76308291ed3686ea5` — no licence file. A Vault
  cluster module: instance pool, elastic IP with healthcheck, security
  groups referencing each other, anti-affinity.
- **Verdict**: **applied, empty second plan, clean destroy — 7 resources,
  zero failures.** The only third-party stack of the fifteen that is
  entirely green end to end.
- **Recorded edit** (`terraform.tf`): comment out
  `experiments = [module_variable_optional_attrs]` — the experiment
  concluded in Terraform 1.3; 1.15 refuses to load the module as published.
- **Harness inputs**: zone `ch-dk-2`, a name, a template id (unchecked by
  design), ssh key name, domain.
- **Score 6/10** — clean, focused module; unusable on any current Terraform
  without that one-line edit, floor-pinned, no licence.
- **Replayed 2026-08-18, `main@23f57c1`**: identical — 7 applied, empty
  second plan, 7 destroyed. Still the only stack of the fifteen that is
  entirely green end to end.

### 4. camptocamp/terraform-exoscale-sks — not applicable, and the reason is the finding

- **Repo** <https://github.com/camptocamp/terraform-exoscale-sks> @
  `2f610759ef55f7f5557d2e7267c64c7270993421` — no licence file. SKS cluster
  + nodepools + security groups.
- **Verdict**: not applicable, twice over. Its resource generation
  (`exoscale_affinity`) was removed from the provider around 0.55, so **no
  provider able to reach this emulator can even parse it**; and SKS is not
  served (`POST /v2/sks-cluster` absent from `/_feint/routes`).
- **Declined reached**: SKS (by inspection; the plan never got that far).
- **Score 3/10** — archived-generation resources with no upper pin means it
  no longer runs against the real cloud either; that is an observation about
  publication rot, not about its authors' 2021 code.
- **Replayed 2026-08-18, `main@23f57c1`**: identical — the reachable
  provider still refuses `exoscale_affinity` at parse, nothing to run.

### 5. datamindedbe/eu-data-platform — not applicable: every product it needs is unserved

- **Repo** <https://github.com/datamindedbe/eu-data-platform> @
  `119712adeb198c403da3ac7eaa102a1d60f15325` — MIT. A data platform on a
  European cloud (19★, 2026): SKS cluster + nodepool, DBaaS PostgreSQL,
  SOS buckets, security groups, Zitadel and Kubernetes on top.
- **Verdict**: not applicable. Its state backend is S3-on-SOS with the
  endpoint interpolated in the backend block (OpenTofu ≥ 1.8 syntax; plain
  Terraform refuses at init), pointed at the real
  `sos-ch-gva-2.exo.io` — and its cloud resources are SKS, DBaaS and SOS,
  none served. The zone is hardcoded `ch-gva-2` in `locals.tf`, which the
  single-zone catalogue would refuse at the first zone-scoped call anyway.
- **Declined reached**: SKS, DBaaS, SOS; zone `ch-gva-2`.
- **Score 7/10** — recent, mise-driven, state declared, MIT; hardcoded zone
  and tofu-only backend syntax are the friction, and both are visible facts
  a reader meets in the first ten minutes.
- **Replayed 2026-08-18, `main@23f57c1`**: identical — `terraform init`
  still refuses the interpolated backend block; nothing reaches any
  endpoint.

**Exoscale mean: 6.0.** The ecosystem splits cleanly in two: compute-shaped
stacks (instance pools, SGs, EIPs) apply and converge; platform-shaped ones
live in SKS/DBaaS/SOS and cannot start. Three of five pin or default to a
zone other than `ch-dk-2` — the measurement that became
`FEINT_EXOSCALE_ZONE` (#278), verified above on this register's own
platform entry.

## Scaleway — four applied in part, one not applicable

### 1. sergelogvinov/terraform-talos (scaleway/) — applied in part, filed #270

- **Repo** <https://github.com/sergelogvinov/terraform-talos> @
  `1edaa02095b2c981760ca7590ab5fdfbba6465af`, directory `scaleway/` — MIT,
  209★, maintained. Talos Kubernetes: VPC, private network, public gateway,
  IPAM-booked addresses, placement groups, per-tier security groups, LB,
  instances from packer-built images, sops-encrypted secrets.
- **Verdict**: applied in part — 12 resources (VPC, PN, IPAM ×2, SGs,
  ssh key, instance IPs). Three walls: `placement_groups` (declined —
  `coverage/scaleway-coverage.json` says so by name), `vpc-gw/v1` (product
  outside the scanned surface), and #270 — the templates read
  `one(pn.ipv6_subnets).subnet`, feint's PN has no IPv6 subnet, and even
  `terraform destroy` wedges on the null. The witness stack for #270.
- **Edits**: none to the stack. Operator inputs recreated: a sops age key +
  `terraform.tfvars.sops.json` (the `kubernetes` map: apiDomain, domain,
  clusterName/ID/Secret, token/tokenMachine, ca/caMachine, service/pod
  subnets), `~/.ssh/terraform.pub` in a disposable HOME, packer-built images
  stood in by the served chain volume → snapshot → `CreateImage`
  (`talos-system-disk-amd64`/`arm64`), and
  `controlplane = {count=2, type="DEV1-L"}` because the default count is 0
  and the default type (`COPARM1-2C-8G`, ARM) is outside the fictional
  catalogue.
- **Declined reached**: placement groups, vpc-gw, lb (via `type_lb` unused
  here), the ARM instance-type family.
- **Score 7/10** — encrypted secrets and real maintenance; the sops schema
  is undocumented (reverse-engineered from templates) and apply-nothing
  defaults cost the rest.
- **Replayed 2026-08-18, `main@23f57c1`**: the #270 fix verified on its
  witness — the third wall fell. The Private Network publishes its IPv6
  `/64` (`fdfe:5964:94ef:a41c::/64` in the transcript),
  `one(pn.ipv6_subnets).subnet` evaluates, zero null errors, and the
  destroy that wedged completes: 12 of 12. The two declined walls stand as
  designed (placement groups ×2, vpc-gw, each a 501 naming its route).
- **Replayed 2026-08-19, `feat/279-server-catalogue`**: same harness, same
  12 of 12 applied and destroyed with the DEV1-L override — and the type
  table is no longer the reason that override exists. #279 measured that
  the real catalogue withdrew COPARM1 from all nine zones (end-of-service
  families are still listed; COPARM1 is not), so the stack's published
  default fails against production exactly as it fails here, and feint
  refuses it deliberately rather than by poverty
  (`TestTheRetiredArmFamilyStaysRetired`). Run with the default type
  anyway (count 2, type left at `COPARM1-2C-8G`), the plan is accepted
  (21 to add) and the apply stops at the same two declined walls —
  placement groups and vpc-gw — before any server names its type. Both
  seeded talos images resolve, the arm64 one included.
- **Replayed 2026-08-19, `feat/285-placement-groups`**: the placement
  wall fell. Same harness (fresh age key, re-seeded talos images,
  `controlplane = {count=2, type="DEV1-L"}`), 17 resources applied —
  both placement groups (`controlplane` enforced/max_availability, `web`),
  **both controlplane servers**, their IPs, IPAM bookings and SGs — where
  every earlier replay died on the placement-group 501 before any server
  existed. The recording shows what the pinned provider (~> 2.43.0) does
  with the group: v1 create ×2, get ×8, delete ×2, plus
  `placement_group` on each server create and the detach-on-destroy
  `PATCH {"placement_group": null}` ×2 — record and read-back only,
  nothing that depends on machines landing apart, which is the
  measurement #285 turned into `docs/limits.md`'s "recorded, never
  enforced" entry. The stack's `policy_respected` reads honest: `true` on
  the group at create (nothing running), `false` per server (the SDK pins
  the server-embedded value false). One wall stands: vpc-gw (#282's
  family) — the apply stops on `scaleway_vpc_public_gateway_ip.main`'s
  501 and `terraform destroy` completes cleanly, 17 of 17. One new
  finding, visible only now that servers apply: `ip_ids` re-plans as an
  in-place swap because `server.public_ips` does not preserve the order
  the create named (v4 first, v6 second in the config; read back
  reversed) — filed as [#320](https://github.com/stephrobert/feint/issues/320).

- **Replayed 2026-08-19, `fix/320-address-order-and-shape`**: the fourth
  wall, revealed the moment #285's replay let the servers apply — the
  stack's `ip_ids = [v4.id, v6.id]` re-planned the same two-way swap for
  ever (#320) — no longer stands. Measured through `feint proxy --record`
  at the stack's own pin (`~> 2.43.0`) on the reduced witness of
  instances-controlplane.tf (two reserved IPs, one server naming them
  against their creation order): the create sends `public_ips` in the
  config's order, the answer now carries that same order, and the witness
  applies, plans empty and destroys. The cause was order alone:
  `Server.public_ips` answered in store order, the provider rebuilds
  `ip_ids` from it index by index (2.43.0 `flattenServerIPIDs`,
  types.go:99) and its apply path is set-based `UpdateIP` calls that
  cannot reorder. The `id → fr-par-1/id` half of the diff travels with
  the swap: `dsf.Locality` (locality.go:10) suppresses a bare-vs-zoned
  pair at the same index, and the bare id is what the real API serves
  (`ServerIP.ID`, instance_sdk.go:1476). The full stack's servers still
  wait on the placement-group wall (#285, in flight).

- **Replayed 2026-08-19, `feat/282-scaleway-lb-and-gateway`** — with
  `type_lb = "LB-S"` added to the controlplane override, so the LB half the
  survey never exercised runs. **The LB wall fell**: 17 of 30 applied at the
  pinned `~> 2.43.0` — both `scaleway_lb_ip` (v4 and v6), the balancer on
  the Private Network through the *pre-beta.30 attach spelling*
  (`/lbs/{id}/private-networks/{pn}/attach`, which the 2.43-vendored SDK
  sends and the register's `feint proxy` transcript caught; the emulator
  serves both spellings, `Route.Legacy`), the api backend with its HTTPS
  health check, the api frontend with two inline ACLs. Second plan:
  `0 to change`. Destroy: 17 of 17. Two walls stand: placement groups
  (#285's, in flight) and `vpc-gw/v1` — declined **by name** now, because
  the portal publishes no v1 document any more. Replayed again with the
  recorded edits a v1-pinned user must make anyway (provider `~> 2.52.0`,
  the release that moved vpcgw onto v2, and dropping `routed_ip_enabled`,
  which ≥ 2.52 removed from the schema): **the gateway wall fell too** —
  20 applied, `scaleway_vpc_public_gateway_ip` →
  `scaleway_vpc_public_gateway` → `scaleway_vpc_gateway_network` with
  `ipam_config` included, `0 to change`, 20 destroyed. The one wall left in
  either variant is placement groups; the four http/https LB children sit
  behind it through the `data.scaleway_ipam_ips.web` edge.

- **Both walls fell on separate branches, and nothing has yet replayed
  them together.** The two entries above were measured the same day on
  `feat/285-placement-groups` and `feat/282-scaleway-lb-and-gateway`:
  each names as its remaining wall exactly what the other removed —
  placement groups for one, `vpc-gw` for the other. That the stack now
  applies end to end is therefore an inference, not a measurement, and
  this register does not record inferences. The next replay on a `main`
  carrying both is what will say, and it is what this line exists to
  demand.

### 2. ioandev/scaleway-flatcar-k3s — applied in part

- **Repo** <https://github.com/ioandev/scaleway-flatcar-k3s> @
  `85cb05dc8f96bddc3bc646d348d95deefe0169c9` — no licence file. Flatcar k3s:
  golden image imported through Object Storage, servers with private NICs,
  IPAM, SGs, Cloudflare DNS.
- **Verdict**: applied in part — 9 resources (PN, SGs, IPAM ×3, instance
  IP). The image pipeline stops at `scaleway_object_bucket`: the provider
  sent `CreateBucket` to the **real** `s3.fr-par.scw.cloud` (403 from
  Scaleway's actual gateway) — the documented hardcoded-endpoint limit
  (docs/limits.md), observed live from a stranger's stack while
  `SCW_API_URL` pointed here. Cloudflare fails on fake credentials
  (external). Servers sit behind both. No drift on what applied.
- **Edits**: none. Harness inputs: domain, key path, a dummy image file,
  token-shaped fake Cloudflare credentials (its provider block correctly
  nulls the token when unset, but the DNS records still need the API).
- **Declined reached**: Object Storage (by the client's own routing).
- **Score 7/10** — recent, careful conditionals, honest README; no licence,
  no backend.
- **Replayed 2026-08-18, `main@23f57c1`**: identical — 9 applied,
  `CreateBucket` still routed by the provider to the real
  `s3.fr-par.scw.cloud` (403), Cloudflare external, `0 to change`,
  9 destroyed.

### 3. HealsCodes/ephemeral-devbox — applied in part

- **Repo** <https://github.com/HealsCodes/ephemeral-devbox> @
  `cb43927f616f48e391a75f0053c8a56f063c3c52` — MIT-0. An ephemeral devbox
  with a persistent encrypted block volume: block snapshot → volume →
  instance, SG, Tailscale enrolment.
- **Verdict**: applied in part — the served block/v1 chain worked:
  `data.scaleway_block_snapshot` found the seeded snapshot, the volume was
  created **from** it, plus the SG. The server sits behind
  `tailscale_tailnet_key`, which needs the real Tailscale API (401 on fake
  credentials). Destroy trips over its own choreography under partial state:
  the local-exec that rotates the snapshot meets feint's (correct)
  `precondition failed: resource is still in use`.
- **Edits**: none. Prerequisite seeded through the served API: block volume
  + snapshot named `devbox-persistent-snapshot`, which is the stack's own
  first-run step.
- **Declined reached**: none.
- **Score 6/10** — small and honest, MIT-0, documented; external SaaS on the
  critical path and floor pins.
- **Replayed 2026-08-18, `main@23f57c1`**: identical — the block/v1 chain
  applies from the re-seeded snapshot (3 resources), Tailscale still walls
  the server, and the destroy trips on the stack's own snapshot rotation
  meeting feint's correct `resource is still in use` refusal.

### 4. CentraleSupelec/kubic — not applicable: every product it needs is unserved

- **Repo** <https://github.com/CentraleSupelec/kubic> @
  `c326f47a4a9079453242e13e2d1829cc640020f9` — MIT, 41★, a French
  engineering school's real deployment. Kapsule cluster + pool, LB IP,
  Helm/Vault/OVH on top; state on Scaleway Object Storage (`backend "s3"`,
  `backend.conf` documented).
- **Verdict**: not applicable here: `terraform init` needs the S3 backend
  (Object Storage — the one documented DNS/TLS-bound gap), and its
  resources are Kapsule and LB, neither served. Recorded rather than
  half-run.
- **Declined reached**: Kapsule, LB, Object Storage.
- **Score 8/10** — pinned, documented, state declared, maintained; the best
  publication hygiene of the fifteen even though feint can serve none of it.
- **Replayed 2026-08-18, `main@23f57c1`**: identical by inspection — the S3
  backend and the Kapsule/LB resources are unchanged at the pinned commit;
  recorded rather than half-run, as in the survey.
- **Replayed 2026-08-19, `feat/282-scaleway-lb-and-gateway`** — run rather
  than recorded this time, with two harness inputs: a
  `backend_override.tf` switching the S3 backend to `local` (its published
  backend is Object Storage, the documented DNS/TLS gap) and dummy values
  for its 21 required helm/argocd variables. The apply reaches the emulator
  and dies on its first resource: `scaleway_k8s_cluster` →
  `/k8s/v1/regions/fr-par/clusters`, a named 501. The next wall is Kapsule
  by name. Its `scaleway_lb_ip` — the LB demand this stack carries — is
  served since #282, but this stack wires it behind the cluster
  (`project_id = scaleway_k8s_cluster…`), so it is never attempted here;
  the same resource applies standalone in the conformance fixture.

### 5. Rookain-Kiwi/kiwinet-infra-cloud — applied in part

- **Repo** <https://github.com/Rookain-Kiwi/kiwinet-infra-cloud> @
  `78c97c9d7d7af0099fc0af33ebcad08dcfacbb13` — no licence file. A small
  association's hosting: instance + flexible IP + SG + private network +
  account ssh key, cloud-init.
- **Verdict**: applied in part — 4 of 5 (IP, SG, PN, ssh key). The server is
  refused **by the provider's own pre-check**: it validates
  `STARDUST1-S` against the emulated `/products/servers` table
  (`DEV1-*`, `GP1-XS`, `PLAY2-*`, `PRO2-XXS`) and stops before any create.
  The fictional catalogue acting as a client-side whitelist, measured.
- **Edits**: none. Harness inputs: project id, ssh public key.
- **Declined reached**: the `STARDUST1` type family.
- **Score 6/10** — tfvars.example and docs for a small real deployment;
  floor pin `~> 2.0`, no backend, no licence.
- **Replayed 2026-08-18, `main@23f57c1`**: identical — 4 of 5 applied, the
  provider's own pre-check still refuses `STARDUST1-S` against the
  fictional type table, `0 to change`, clean destroy.
- **Replayed 2026-08-19, `feat/279-server-catalogue`**: **applies whole — 5
  of 5.** The catalogue now carries `STARDUST1-S` with the values the real
  fr-par-1 publishes for it, the provider's pre-check passes, the server is
  created with its `debian_bookworm` image and cloud-init, re-plan says `No
  changes`, 5 destroyed. The last wall this stack had is gone.

### Annex — surveyed and set aside

- **kalisio/kaabah** @ `445ed7c…` (32★, MIT): pre-0.13 provider wiring —
  Terraform 1.x resolves its unqualified `scaleway` to `hashicorp/scaleway`,
  which does not exist; running it means adding `required_providers` to root
  and modules. Generational rot, same class as camptocamp's SKS module.
- **stefanprodan/scaleway-swarm-terraform**, **s4l1h/scaleway-k3s-cluster**:
  provider 1.x resources (`scaleway_server`) against an API that no longer
  exists; unappliable against emulator and cloud alike.

**Scaleway mean: 6.8** (7, 7, 6, 8, 6). The healthiest publication hygiene
of the three — and the least of it lands on feint's served surface: the
public Scaleway ecosystem lives in Kapsule, RDB, LB and Object Storage.

## The sixteenth, offered rather than surveyed (2026-08-19)

A downstream consumer — **OpenAether**, OpenTofu plus Talos on Scaleway and
Outscale — offered their lane as a sixteenth entry ([#327]). It is not one, and
the distinction is the point of this register rather than a formality: **every
entry above was replayed here**, and this one has not been. What follows is
their report, attributed, plus the decision it forced.

**Reported by them, not replayed here.** Their figures, verbatim from the report
of 2026-08-19: Scaleway **8 applied → empty re-plan → 8 destroyed**, Outscale
**27 applied → empty re-plan → 27 destroyed**, with feint installed pinned and
checksum-verified and **no credentials at all, deliberately** — "if anything in
it ever needs one, the lane must fail rather than quietly reach a real account",
which is [#280]'s escape-path rule arrived at independently, downstream. No
number in this paragraph is a measurement of this repository, and none is
reconciled against one: the two lanes were run on their infrastructure, at a
commit we do not hold.

**What it has already paid for.** It is the lane that caught Scaleway provider
2.81.0 moving private NICs onto `instance/v2alpha1` — the break that produced
[#325] and [#326]. That is the argument for the whole category: their providers
resolve fresh on their schedule, so the ecosystem moved under them a day before
anybody here would have re-recorded anything.

**What it exercises, as reported**: two providers in one project, a Talos
cluster's shape on each, and the full apply → empty re-plan → destroy cycle on
both. Which resources, in what versions, with which constraints, is not stated
in the offer and is not guessed here.

**The wall it stands at**: unknown. The report names no product feint declined
to serve them, and an absence of complaint is not a measurement of coverage.

**What is not measured**, and it is most of what an entry above carries: the
repository URL, the commit, the licence, the provider constraints, the client
versions, the harness, and every resource count as observed from this side. A
row that filled those cells from the paragraph above would be the exact failure
this register exists to avoid.

**The decision (#327): recorded here, replayed on demand, and not wired into
this repository's CI.** Two reasons, neither about trust. A third party's
repository changes without our decision, so a required gate over it can go red
for a reason nobody here chose — and a red nobody can act on is what teaches
people to skip a gate. And no gate here clones a third-party repository, which
would put somebody else's availability inside this pipeline; that rule predates
the offer and is why the Exoscale stack is run by hand. The contract we asked
them to meet, written for whoever offers the next one, is in
[`README.md`](README.md#offering-your-stack-what-we-ask-and-what-we-do-with-it).

[#280]: https://github.com/stephrobert/feint/issues/280
[#325]: https://github.com/stephrobert/feint/issues/325
[#326]: https://github.com/stephrobert/feint/issues/326
[#327]: https://github.com/stephrobert/feint/issues/327

## The declined products these fifteen reached for, counted

| provider | product/behaviour | stacks that asked | note |
|---|---|---|---|
| Outscale | LBU (`CreateLoadBalancer`) | 3 (ztiac, k3s, rke-cluster) | the one Outscale product every substantial stack wants — **served since #281**: all three replayed 2026-08-19, LBU resources included, second plans empty |
| Outscale | ≥2 subregions | 2 (ocp, kasten) | #269 / #268 |
| Scaleway | Object Storage | 3 (flatcar, kubic, ducklake*) | the documented DNS/TLS gap, seen live |
| Scaleway | Kapsule / managed k8s | 1 of 5 + most of the rejected pool | dominates the public ecosystem |
| Scaleway | LB | 2 (kubic, talos') | **served since #282**: talos's whole chain applied 2026-08-19 (both attach spellings — its ~>2.43 pin sends the pre-beta.30 one); kubic's lb_ip is served but sits behind Kapsule in its own graph |
| Scaleway | vpc-gw (public gateway) | 2 (talos, vpc-module*) | **v2 served since #282**: the talos chain applied 2026-08-19 under a ≥2.52 provider; v1 declined by name (the portal withdrew its document), so the pinned 2.43 meets a named 501 |
| Scaleway | placement groups | 1 (talos) | declined today |
| Scaleway | instance types outside the table (`STARDUST1-S`, `COPARM1-*`) | 2 (kiwinet, talos) | the provider pre-validates against `/products/servers`, so the fictional table is a hard whitelist — resolved by #279: `STARDUST1-S` is served from the measured catalogue (kiwinet applies whole), and `COPARM1-*` is refused because the real cloud withdrew the family |
| Exoscale | SKS | 3 (camptocamp, eu-data, WhizUs*) | dominates that ecosystem |
| Exoscale | DNS (`exoscale_domain`) | 1 (openshift4) | fails as a zone-lookup error, which misleads |
| Exoscale | NLB | 1 (platform) | **served since #345**: platform's `exoscale_nlb` and its `data "exoscale_nlb"` apply and read back, replayed 2026-08-21 (20 applied against 19, re-plan `5 to add` against `6`); its one `exoscale_nlb_service` stays behind the SOS branch |
| Exoscale | DBaaS | 1 (eu-data) | |
| Exoscale | SOS | 2 (platform, eu-data) | |
| Exoscale | a zone other than `ch-dk-2` | 3 (eu-data `ch-gva-2`, platform `de-fra-1` default, openshift4's DNS client) | the single-zone decision is measured (catalog.go), and this is its measured cost |

\* surveyed but outside the fifteen.

The client-side harvest matters as much: two upstream provider crashes
(outscale 0.7.0 SIGSEGV, 1.1.3 Configure crash on an `endpoints` block), one
upstream client-side regression (outscale 1.8.0 `Duplicate Tag key … []`
against its vendor's own example), one concluded-experiment refusal, one
dead registry namespace, and one provider whose S3 calls ignore the
redirect entirely. **Six of fifteen stacks needed no edit at all; five
needed exactly one recorded edit whose cause was the provider ecosystem,
not this emulator.**

## Replayed under `--vm incus-ovn` on `main@104a6d4` (2026-08-24)

**This is the first pass of this register on a machine runtime.** Every figure
above was measured with `--vm off`, where a server is a record and nothing boots.
Every figure here was measured with `--vm incus-ovn`, where a server is an Incus
instance on an OVN network, with a real address and a real firewall. That single
change moves the subject of the measurement, so these are not a before-and-after
of the emulator's quality — they are two different questions, and the older
answers stand unedited above as the photograph of before.

**What it can therefore conclude**: whether the control plane still answers the
same way when its answers have consequences, and what the runtime does with what
the packs hand it. **What it cannot**: anything about products the emulator does
not serve. A machine runtime does not make a route appear, and no wall below moved
because of it.

### The pass in one paragraph

Fifteen entries, all replayed. **Ten are unchanged from their reference, three of
them now backed by something real**: kiwinet applies its 5 with a container that
carries the address the API published and holds the stack's own cloud-init;
terraform-exoscale-vault applies its 7 with **three running machines** and is
still the only entirely green stack of the fifteen; ocp_outscale applies its 30
with a real bastion. openshift4, platform, flatcar-k3s, devbox, kubic and the two
recorded-inapplicable entries answer exactly as recorded. **One improved, and it
is the one this register demanded**: `terraform-talos` had its two walls removed
on separate branches, and the register refused to record the inference — measured
together on `main` at last, it applies **30 of 33** with `0 to change` and
destroys clean, the only wall left being `vpc-gw/v1`, declined by name. **Four
regressed, and every one of them for the same single cause**: the emulated image
catalogues hold a handful of identifiers, every stranger's stack hardcodes a real
one, and under a machine runtime an unbootable image is no longer harmless — the
emulator honestly answers `stopped` where `--vm off` answered `running`. That is
`docs/limits.md`'s decision working exactly as written, and its class-level price
had never been measured. **Across the fifteen, five machines actually started**,
and that number is the finding: kiwinet's one, ocp_outscale's one and
terraform-exoscale-vault's three — the only three entries whose image or template
identifier comes from the served catalogue rather than from a production cloud.

Four defects came out of it, none of them reachable without a runtime, and all
four are the same shape — the API describes something the host does not have:
[#454] (a security group with an IPv6 ICMP rule loses its whole firewall),
[#455] (an orphaned OVN network becomes permanently unremovable and
`clean --check` does not see it), [#456] (a Net with several subnets fails to
peer, and its destroy fails), [#457] (a load balancer whose backends are on
another subnet is accepted and never built). Two more findings were **not** filed
because measurement disproved them: the refusal to boot a registered image is
documented and deliberate, and the `Missing VIP target(s)` error self-heals when
the backends arrive — proven by a witness that registered one and watched the
balancer appear.

[#454]: https://github.com/stephrobert/feint/issues/454
[#455]: https://github.com/stephrobert/feint/issues/455
[#456]: https://github.com/stephrobert/feint/issues/456
[#457]: https://github.com/stephrobert/feint/issues/457

### How the runtime pass differs from the harness above

Same commits, same recorded edits, same seeded prerequisites. Three additions:
the emulator runs `--vm incus-ovn --cleanup`; an `incus monitor --type=lifecycle`
starts **before** it, so no launch can precede the recorder; and the health probe
compares `instance.pid` and the declared runtime with what it started — a probe
that only asked "does the port answer" passed against a stale `--vm off` emulator
and voided two entries before it was fixed.

Every entry whose verdict differs from its reference was re-run **alone under
`--vm off`, everything else identical**, so that code and runtime could be told
apart. Where that was not possible, the entry says so.

### Per-entry

**Outscale 1 — outscale/osc-k8s-rke-cluster** (`7427b98`): **regressed, runtime.**
Measured **twice** under `--vm incus-ovn`, the second time deliberately, from a
clean station and with a hard per-command cap: **52 then 51 applied** against the
reference's 53, re-plan `16 to add / 1 to destroy` then `17 to add / 1 to
destroy`, 52 then 51 destroyed, **0 machines both times**. The one-resource
spread between two runs of the same stack is ordering: the apply aborts on the
tainted bastion, and which of its independent siblings had finished by then
varies. Both runs agree on everything that matters.

`outscale_vm.bastion` is tainted because `ami-a3ca408c`, the OMI the vendor's own
archived example hardcodes, is in no emulated catalogue: the boot is refused by
name and the provider reports `expected state running but found stopped`. The
RKE/ansible tail that every earlier replay recorded as "times out against the
fictional bastion" **never ran at all** here, because the bastion it depends on
never existed — the one thing this campaign hoped a real runtime might unblock,
and the image wall sits upstream of it.

*Separation: abandoned, and the reason is named.* The `--vm off` re-run does not
terminate: the stack's own `ansible.cfg` sets `retries=999` with a 60-second
timeout against an unroutable address. The first attempt to stop it is worth
recording, because it produced a four-hour ghost — see the harness note at the
end of this section. Attribution therefore rests on the mechanism, identical to
the four entries below whose separation did complete, and is stated as an
inference rather than a measurement.

**Outscale 2 — chimere-eu/ztiac** (`093330c`), `advanced-network`: **regressed,
runtime, and this is the pass's worst result.** 80 applied against 95, re-plan
`15 to add`, and **the destroy fails**, leaving 2 resources in state and two OVN
networks plus two ACLs on the host that nothing can remove. The cause is [#456]:
a Net with several subnets fails to peer (`More than one matching network peer
was found`), the subnets' backing networks cannot then be deleted, and
`DeleteNet` refuses with its own correct 409 (`the Net vpc-… still holds 2
subnet(s)`). The register's only fully converging stranger stack, and the #249
shape it was famous for, is what breaks first on a machine runtime.

`two-tier-architecture`: **regressed, runtime.** 54 applied — the reference
figure exactly — and 54 destroyed, but the second plan is `0 to add, 5 to change`
where it was `No changes.`: all five changes are `state = "stopped" -> "running"`
on the five Vms, whose `ami-a3ca408c` the catalogue does not hold. Both of its
load balancers also failed to reach the host, and that is [#457]: their backends
are on the private subnet while they listen on the public one, which OVN refuses
and the API never mentions.

**Outscale 3 — davmartini/ocp_outscale** (`1c7fd7b`): **conforme, and machine-backed
— and it is the campaign's natural experiment.** 30 applied, `No changes.`,
30 destroyed, identical to the reference, **with 1 machine started**:
`outscale_vm.vm-pub-aza` came up as a real container in 21 seconds.

It boots for one reason, and the reason has to be declared because it is a
harness choice rather than a recorded one: the register names "nine TF_VARs
(keys, region, vm type, image id, keypair name, three DNS IPs)" **without naming
the image id**, so this replay supplied `ami-00000001`, an OMI the emulated
catalogue holds. Same pack, same runtime, same evening as the four Outscale
entries that started nothing on `ami-a3ca408c` / `ami-538af795` / `ami-47899c77`.
The only variable that differs is whether the identifier is in the catalogue, and
it decides the whole outcome. Same recorded edit (0.5.3's `endpoints` block) and
the same TLS intercept on 4612, whose locally-minted CA the run trusts through
`SSL_CERT_FILE`.

**Outscale 4 — pli01/terraform-outscale-k3s** (`e68aa43`): **regressed,
runtime — separated.** Under `--vm incus-ovn`: 35 applied, re-plan `8 to add /
1 to destroy`, 34 destroyed, 0 machines, `ami-47899c77` refused by name. Under
`--vm off`, everything else identical: **41 applied, `No changes.`** — the
reference exactly. The difference is the runtime alone.

**Outscale 5 — michaelcourcy/kasten-on-outscale** (`f1fcc87`): **regressed,
runtime — separated.** Under `--vm incus-ovn`: 29 applied, re-plan `6 to add /
5 to destroy`, 29 destroyed, 0 machines; all five Vms tainted on `ami-538af795`.
Under `--vm off`: **30 applied, `No changes.`, 30 destroyed** — the reference
exactly, and #268's convergence still holds. The runtime alone.

**Exoscale 1 — appuio/terraform-openshift4-exoscale** (`46016ea`): **conforme.**
43 applied, `0 to change`, 43 destroyed — the reference to the resource. The only
failure is the unserved DNS branch, and it now refuses with #284's explicit
sentence rather than a zone-lookup error: "this deployment serves zone ch-dk-2,
and the client resolved zone ch-gva-2 from GET /v2/zone … Restart with
FEINT_EXOSCALE_ZONE=ch-gva-2". The re-plan is `42 to add, 0 to change`, and all
42 are that branch and what sits behind it — the lb module, whose
`data.exoscale_domain.cluster` cannot resolve, and `module.infra`'s four compute
instances. 0 machines: everything that would boot is downstream of the DNS
branch, so none was ever attempted. Both registered templates resolve, and
`visibility=private` still answers exactly the two of them (#271 holds).

**Exoscale 2 — PhilippeChepy/platform** (`84791e3`), `terraform-base` against a
`de-fra-1` emulator: **conforme.** 20 created, re-plan `5 to add / 0 to change`,
20 destroyed — the 2026-08-21 reference to the resource. Its only failures are
the two SOS buckets at the real `sos-de-muc-1.exo.io` on fake credentials, which
is the documented external gap; the emulator logged nothing. 0 machines: the
instance pools name templates the served catalogue does not hold.

**Exoscale 3 — PhilippeChepy/terraform-exoscale-vault** (`62d27ee`):
**conforme, and machine-backed.** 7 applied, `No changes.`, 7 destroyed — and
**3 machines really started**, the Vault cluster's instance pool at its default
size of three. Still the only third-party stack of the fifteen that is entirely
green end to end, and now green with something running. It boots because its
`template_id` is taken from the served catalogue rather than hardcoded, which is
the whole difference between this entry and the eight that started nothing.

**Exoscale 4 — camptocamp/terraform-exoscale-sks** (`2f61075`): **conforme, and
the reason was triggered rather than restated.** With its providers installed and
its four variables supplied, so that nothing else could be the cause:
`terraform validate` and `terraform plan` both exit 1 on
`Error: Invalid resource type … "exoscale_affinity"` at `main.tf` line 7. One
exchange reached the recorder — the harness's own health probe — so nothing of
this stack ever addressed the emulator, exactly as recorded. SKS remains unserved
and the plan never got near it.

**Exoscale 5 — datamindedbe/eu-data-platform** (`119712a`): **conforme, and the
reason was triggered rather than restated.** Pointed at
`infra/exoscale-platform/app`, which is where the backend actually lives:
`terraform init` exits 1 with `Error: Variables not allowed` on both
`bucket = var.bucket_name` and
`endpoint = "https://sos-${local.zone}.exo.io"`. Nothing reached any endpoint.

One detail measured while triggering it, which confirms the register's
parenthetical rather than contradicting it: **OpenTofu 1.11.4 accepts the
syntax** — it gets past the parse and stops at `backend.s3 depends on
var.bucket_name which is not available`, i.e. a missing input, not a forbidden
interpolation. So "OpenTofu ≥ 1.8 syntax, plain Terraform refuses at init" is
right, and the tofu half now has a measurement behind it too.

**Scaleway 1 — sergelogvinov/terraform-talos** (`1edaa02`, `scaleway/`):
**improved — and this is the line the register demanded.** #285 (placement
groups) and #282 (LB and gateway) fell on separate branches, each naming as its
remaining wall exactly what the other had removed, and the register refused to
record the inference: "the next replay on a `main` carrying both is what will
say, and it is what this line exists to demand." Measured, on `main@104a6d4`,
with `type_lb = "LB-S"` and `controlplane = {count=2, type="DEV1-L"}`:

- under `--vm off`, **30 of 33 applied**, re-plan `3 to add, 0 to change`, 30
  destroyed clean. The three are `scaleway_vpc_public_gateway_ip`,
  `scaleway_vpc_public_gateway` and `scaleway_vpc_gateway_network` — the
  `vpc-gw/v1` wall the register itself records as declined **by name**, because
  the portal withdrew the v1 document and the stack pins `~> 2.43.0`, which
  speaks v1. Every other wall is gone: placement groups, LB, IPv6 subnet,
  address order, the catalogue.
- under `--vm incus-ovn`, **25 applied**, re-plan `7 to add / 2 to destroy`, 25
  destroyed. The delta is the two controlplane servers, tainted: their image is a
  *registered* image, built by the register's own prerequisite recipe
  (volume → snapshot → `CreateImage`), and `docs/limits.md` refuses to boot one
  deliberately — "this emulator keeps records, not disk contents, so there are no
  bytes to boot".

So: the improvement is the **code**, present in both modes; the shortfall is the
**runtime**, present only under `ovn`.

**Scaleway 2 — ioandev/scaleway-flatcar-k3s** (`85cb05d`): **conforme.** 9
applied, `0 to change`, 9 destroyed — the reference exactly. `CreateBucket` still
leaves for the real `s3.fr-par.scw.cloud` whatever `SCW_API_URL` says, and
Cloudflare still fails on token-shaped fakes; both are external and neither
touched the emulator.

**Scaleway 3 — HealsCodes/ephemeral-devbox** (`cb43927`): **conforme.** 3 applied
from the re-seeded block snapshot, and the destroy still trips on the stack's own
snapshot rotation meeting feint's correct `resource is still in use` refusal,
leaving 2 — which is what the reference records, down to the shape of the
failure. Tailscale still walls the server on fake credentials, so no machine was
ever asked for.

**Scaleway 4 — CentraleSupelec/kubic** (`c326f47`): **conforme.** Run rather than
recorded, with the same two harness inputs (the S3 backend overridden to local,
21 dummy helm/argocd variables). 0 applied: the apply reaches the emulator and
dies on its first resource, `scaleway_k8s_cluster` → `/k8s/v1/regions/fr-par/clusters`,
a named 501. Kapsule is the next wall by name, exactly as recorded.

**Scaleway 5 — Rookain-Kiwi/kiwinet-infra-cloud** (`78c97c9`): **conforme, and
machine-backed.** 5 applied, `No changes.`, 5 destroyed — identical to the
`feat/279` reference — with **1 machine really started**. Read back between apply
and destroy, which is what only this pass could do: `feint-scw-a041c7de-…`
RUNNING with `203.0.113.2` on `eth0`, the API publishing `public_ip=203.0.113.2`
— **the same address the machine carries**, not a plausible-looking number — and
the container holding the stack's own `cloud-init.yml`, French comment box
included, with `cloud-init status` answering inside it. It boots because
`debian_bookworm` is a catalogue label rather than a hardcoded id.

### The figures, with their denominators

| | |
|---|---|
| entries replayed | **15 of 15** (the sixteenth, OpenAether/#327, is out of scope by the register's own decision) |
| entries abandoned | **0** — one *separation* run was abandoned, O1's, named in its entry |
| conforme | **10** |
| improved | **1** (talos) |
| regressed | **4** (rke-cluster, ztiac, k3s, kasten) — ztiac is one entry whose *two* templates both regressed |
| harness broken, nothing measured | **0 at the end**; **7 attempts** along the way, every one re-run (§ below) |
| **machines really started** | **5** — kiwinet 1, ocp_outscale 1, vault 3 |
| resources created, summed over the fifteen `--vm incus-ovn` runs | **381** (`Creation complete` lines) |
| resources destroyed | **386** — larger on purpose, see below |
| recorded exchanges over those fifteen runs | **2 396** |

`destroyed` exceeds `created` by five, and the gap is a **net of two opposite
effects** rather than one number:

- **destroyed more than created**, entry by entry: a server that was created and
  then failed to reach `running` is *tainted* — it is in state and gets
  destroyed, but it never printed a `Creation complete` line. rke-cluster +1,
  k3s +1, kasten +5, talos +2.
- **created more than destroyed**, where a destroy did not finish: ztiac −2
  (#456) and devbox −2 (its own snapshot rotation, as recorded).

1 − 2 + 1 + 5 + 2 − 2 = 5. Both numbers are given precisely because neither alone
says what happened, and the tidier sentence — "the gap is the machines refused" —
would have been wrong.

Every regression was separated by a `--vm off` re-run except O1's, and **every
separation that ran reproduced its reference exactly**: ztiac 95 and 54,
k3s 41, kasten 30, talos 30. So the four regressions are the runtime, and the
code did not move under any of them.

### What went wrong in the harness, since it reads like findings otherwise

Seven attempts measured this replay's harness rather than the emulator, and every
one was re-run. They are listed because each produced a plausible number: an
invented SSH key the Scaleway SDK rightly refused (kiwinet 4 of 5); a pin patcher
that treated "already pinned" as "matched nothing" and silently emptied two
separation runs; a TLS proxy started without `--record`, caught by the run's own
guard; a resource counter that counted data sources, inflating talos by exactly
its three; `ssh_key` passed as a name where openshift4 wants key material; E5's
`init` run at the repo root, where there is no backend block, so it passed and
proved nothing, and E4 planned without providers or variables so it died on the
wrong error; and the two below.

Two more are worth repeating outside this repository, because both are about a
control that was green while being wrong.

**A health probe that could not say whose emulator answered.** A run was killed
harshly, its trap never fired, and its `--vm off` emulator kept port 4610. The
next two runs' health checks — `curl -sf /_feint/health` — **passed against that
stale emulator**, which was holding another stack's resources in the wrong
runtime mode, while feint's own log said `refusing to serve on 127.0.0.1:4610: it
is already served by another emulator (pid 121004)`. Two entries were measured
against the wrong runtime and discarded. The probe now compares `instance.pid`
and the declared runtime against what it started and refuses a non-empty
emulator, and a witness proves it refuses a squatter.

**A sweep that killed itself, and a cap that died with its parent.** The command
meant to stop the non-terminating O1 separation was
`pkill -f campaign-outscale.sh; …; pkill -f "run-O1.sh"; …; pkill -f "terraform
apply"`. `pkill -f` matches the **whole command line**, and the shell running
that very command contains those strings, so the sweep killed itself before
reaching its last clause. The emulator died; `terraform` did not. Its ansible
tail then ran **3 h 57** against an address nothing carried any more, on a host
with no emulator and no machine at all, and it was the maintainer who noticed at
04:15 and sent it TERM — not any control of mine. The `timeout 3600` that should
have bounded it was on the wrapper I had killed: **a cap that lives in the parent
is not a cap.** Both are fixed: every `apply`, `plan` and `destroy` now carries
its own `timeout`, and the sweeper's patterns are anchored (`^…`) so they cannot
match the shell that runs them. O1 was then re-measured under `--vm incus-ovn`
from a clean station, which is the second of the two runs in its entry above.

The ghost also leaves a fact that belongs to the register rather than to the
harness: for those four hours the station carried **two OVN networks and no
emulator and no machine**. Those networks are #455's, and their surviving an
emulator's death by hours is the same defect seen from a third angle.

## Replayed under `--vm incus-ovn` on `main@72d861d` (2026-08-25), in two passes

**Runtime: `--vm incus-ovn`**, named here because the register requires it of
any replay. Same harness as the 2026-08-24 section above — one emulator per
stack, an `incus monitor --type=lifecycle` started *before* it, a health probe
that compares `instance.pid` and the declared runtime, a `timeout` on every
`apply`, `plan` and `destroy`, and `feint clean --vm incus` with a host read-back
between entries. Same fifteen repositories at the same commits (verified 15 of
15), same recorded edits, same seeded prerequisites.

Every figure below was measured against a binary built from `72d861d`. `main`
moved once while the campaign ran, to `a80b4c0`, which touches only
`examples/stacks/{scaleway,outscale,exoscale}/main.tf` — no Go, no contract, no
coverage artefact — so it cannot have moved anything measured here; the commit
named is the one the emulator was built from, which is the datum a replay needs.

Eight fixes landed between the two sections, five of them aimed at what the
first runtime pass had revealed: [#455] (the sweep no longer traps the host it
cleans), [#456] (a Net of several subnets peers), [#454] (an ICMP rule reads its
address family, so a refused rule set stops reading as a success), [#457] (a
balancer whose backends are elsewhere is refused by name, and its ports are
omitted when it has no target), [#465] (image recipes derive by family, and an
opaque identifier is the operator's to declare), plus [#460], [#464], [#466] and
[#469].

### Why two passes, and why they are never mixed

[#465] shipped a door the previous campaign did not have: an opaque image
identifier **can be declared** —
`FEINT_BOOT_IMAGES='<id>=<family>:<version>[@login]'` — and `feint images
resolve <id>` asks the providers' public listings what to declare. Four of the
fifteen stacks are blocked by exactly that, so the campaign was run twice:

- **Pass 1, comparable**: no declaration at all. It is the pass that compares to
  2026-08-24 at identical conditions, and it is the only one that says what the
  eight fixes changed.
- **Pass 2, declared**: `FEINT_BOOT_IMAGES` filled in for the four. It says what
  the mechanism buys, and nothing about the fixes. **A machine that boots
  because of a declaration is not a machine a fix unblocked**, which is why the
  figures below never appear in one table.

### Pass 1, the comparable one — the figures with their denominators

| | |
|---|---|
| entries replayed | **15 of 15** |
| conforme | **10** |
| improved | **1** (talos, in the same sense as 2026-08-24) |
| regressed | **4** (rke-cluster, ztiac, k3s, kasten) |
| harness broken, nothing measured | **0 at the end**; one attempt, named below |
| **machines really started** | **5** — kiwinet 1, ocp_outscale 1, vault 3 |
| resources created | **379** (`Creation complete` lines) |
| resources destroyed | **385** |
| recorded exchanges | **2 388** |

The distribution is the one of 2026-08-24, machine count included. **At equal
conditions the eight fixes unblocked no machine, and none of them was about
that.** What they changed is visible entry by entry rather than in the totals,
and it is ztiac that carries it.

`destroyed` exceeds `created` by six, netting two opposite effects, as in the
previous pass: tainted resources that are in state and get destroyed without ever
printing a creation line (rke +1, k3s +1, kasten +5, talos +2), against destroys
that did not finish (ztiac −1, devbox −2).

| entry | applied | second plan | destroyed | left | machines | exchanges | verdict |
|---|---|---|---|---|---|---|---|
| O1 osc-k8s-rke-cluster | 52 | 16 add / 1 destroy | 52 | 0 | 0 | 195 | regressed (image), unchanged |
| O2 ztiac `advanced-network` | **77** | 18 add | 76 | **1** | 0 | 762 (both templates) | regressed, **new cause** |
| O2 ztiac `two-tier` | 54 | 0 add / 5 change | 54 | 0 | 0 | | regressed (image), unchanged |
| O3 ocp_outscale | 30 | `No changes.` | 30 | 0 | **1** | 217 | conforme, machine-backed |
| O4 terraform-outscale-k3s | 35 | 8 add / 1 destroy | 34 | 0 | 0 | 221 | regressed (image), unchanged |
| O5 kasten-on-outscale | 29 | 6 add / 5 destroy | 29 | 0 | 0 | 217 | regressed (image), unchanged |
| E1 openshift4-exoscale | 43 | 42 add / 0 change | 43 | 0 | 0 | 226 | conforme |
| E2 platform | 20 | 5 add / 0 change | 20 | 0 | 0 | 97 | conforme |
| E3 terraform-exoscale-vault | 7 | `No changes.` | 7 | 0 | **3** | 48 | conforme, machine-backed |
| E4 terraform-exoscale-sks | n/a | — | — | — | 0 | 1 | conforme, triggered |
| E5 eu-data-platform | n/a | — | — | — | 0 | 1 | conforme, triggered |
| S1 terraform-talos | 25 | 7 add / 2 destroy | 25 | 0 | 0 | 262 | improved, unchanged |
| S2 scaleway-flatcar-k3s | 9 | 0 to change | 9 | 0 | 0 | 54 | conforme |
| S3 ephemeral-devbox | 3 | 0 to change | 1 | 2 | 0 | 29 | conforme |
| S4 kubic | 0 | 15 add | 0 | 0 | 0 | 2 | conforme |
| S5 kiwinet-infra-cloud | 5 | `No changes.` | 5 | 0 | **1** | 57 | conforme, machine-backed |

**What the fixes held, measured on the stack that revealed them.** ztiac
`advanced-network` used to leave two OVN networks and two ACLs the host could
not remove; the station comes back to zero now ([#455]). Its peering applies and
its fourteen routes through it come back clean, and nothing peering-shaped
remains in the residual plan ([#456]). ztiac `two-tier` applies **54 of 54** —
its reference — where the two load balancers used to fail the apply; they are
refused by name in the log now, and the API goes on describing them ([#457]).

**What replaced it on ztiac `advanced-network` is a different defect, and a
worse-behaved one** ([#473]): OVN subnet creation is serialised on one lock,
+5.6 s per subnet, so the tenth of fifteen crosses the emulator's 60-second
write deadline. The connection is cut on a create that then succeeds, the
client's retry meets its own subnet as a 409 `IpRange 10.2.1.0/24 overlaps the
subnet on 10.2.1.0/24`, and the destroy fails on a Net holding six subnets
Terraform never knew it had. 77 of 95, one resource left in state. The curve is
in every run of the campaign — the first network always costs ~11 s and each one
after adds ~5.6 s — and ztiac is simply the only surveyed stack with enough
subnets to reach the cliff.

**The harness attempt that measured nothing.** The first O3 run was killed in
the middle of its `destroy` by my own tool's timeout: the run had been launched
with `nohup` from the same shell that then waited on it, so both shared a
process group. Terraform logged `Stopping operation…` and `Plugin did not
respond`; nothing about the emulator was measurable from it. Re-run from a clean
station in its own session (`setsid`), which is the O3 line above. It is listed
because a killed destroy leaves a plausible-looking log, and the fourth category
exists so that it is never counted as a regression.

### Pass 2, declared — what [#465]'s door buys

`FEINT_BOOT_IMAGES` filled in for the four entries whose only wall is an opaque
identifier. Everything else identical.

| entry | declaration | applied | second plan | destroyed | machines |
|---|---|---|---|---|---|
| O5 kasten | `ami-538af795=ubuntu:22.04` | **30** | `No changes.` | 30 | **5** |
| O4 k3s | `ami-47899c77=debian:12` | **41** | `No changes.` | 41 | **3** |
| O2 `two-tier` | `ami-a3ca408c=ubuntu:22.04` | **54** | `No changes.` | 54 | **5** |
| O1 rke-cluster | `ami-a3ca408c=ubuntu:22.04` | **53** | 14 add / 0 change | 53 | **1** |

**Four of four reach their `--vm off` reference under a real runtime**, second
plans included: 30, 41, 54 and 53 are the register's own figures for these
stacks with no machine runtime at all. **Fourteen machines started where pass 1
started none**, and the four regressions of pass 1 are, in this pass, four
convergences. 178 created, 178 destroyed, 1 049 exchanges.

The declared versions are **not** what the identifiers name, and that has to be
said plainly. `feint images resolve` reports `ami-538af795` as Ubuntu 18.04 and
`ami-47899c77` as Debian 9, and both are versions the `images:` server has
withdrawn, so neither can be built. What is above is a substitution the operator
signs — the nearest buildable version — exactly the gesture [#465] made
declarable. Measured on the unsubstituted form, kasten declared as printed
(`ami-538af795=ubuntu:18.04`): **29 applied, 0 machines**, the declaration
honoured, the build attempted, and the boot refused with `The requested image
couldn't be found` — the same figures as no declaration at all. That gap is
[#476].

**O1 is the only one that does not converge, and the reason is the finding.**
Its apply hit the harness's 900-second cap (`TF_APPLY_TIMED_OUT=900s`,
`TF_APPLY_EXIT=124`) inside `shell_script.bastion-playbook` — the RKE/ansible
tail that **no replay in this register has ever run**, because until this pass
the bastion it depends on had never existed. It ran for 12 min 30 s against a
live container whose ssh port answers from the station, and did not finish. The
53 resources and the `14 to add / 0 to change` re-plan are the reference
figures; only the tail is new, and it is the stack's own local provisioner, not
this emulator.

### The defects this pass filed

Five, each with its reproduction, none reachable without a machine runtime:

- [#473] fifteen OVN subnets queue on one lock, the tenth create is cut at 60 s,
  its retry meets its own subnet as a conflict, and ztiac loses a third of itself;
- [#475] two packs of three never hand a security group to the host — an
  Outscale Vm and three Exoscale machines run with **no ACL** while the API
  describes one, and a witness proves the port the group forbids answers;
- [#476] `feint images resolve` prints a `FEINT_BOOT_IMAGES` line that cannot
  boot, for two of the three identifiers the surveyed stacks hardcode;
- [#477] an Exoscale operation feint declines is refused with a bare 404 the
  official client reads as an ordinary empty answer — three of them inside the
  register's only fully green stack;
- [#474] fourteen `ERROR` lines over fifteen replays are all one documented
  refusal, while the sibling refusal 200 ms later is a `WARN`.

One finding was **not** filed because measurement disproved it: `DeleteSnapshot`
answers `400` with `type: precondition_failed`, which looked like the wrong
status until `scw/errors.go`'s `unmarshalStandardError` showed the SDK
dispatches on the body's `type` and never on the status, and this repository's
own tests already pin 400 against the real cloud.

### The station, in delta

Zero. No `feint-` instance, no `fnt-`/`iso-fnt-` network, no pack ACL, and the
uplink swept — before the campaign as after it. Twenty Incus images before, twenty
after, the same ten `feint/*` aliases (`almalinux/9`, `alpine/3.21`,
`alpine/3.22`, `debian/12`, `debian/13`, `fedora/44`, `rockylinux/10`,
`ubuntu/22.04`, `ubuntu/24.04`, `ubuntu/26.04`), read as JSON because the column
form truncates an alias list to its first entry and `(7 more)`. **No image was
built along the way**: the declared versions were chosen among those the station
already held, and the one build the campaign did trigger — `ubuntu/18.04`,
deliberately — failed at the image server and left nothing behind.

One process on the station is **not** this campaign's and is recorded so that
the next reader does not attribute it here: `incus monitor --type=lifecycle`,
pid 121003, started 2026-08-25 at 00:18:53 and still running twenty hours later.
That is the lifecycle recorder of the 2026-08-24 pass, from the run whose trap
never fired — the same episode that section describes. Every monitor this
campaign started exited with its run.

[#454]: https://github.com/stephrobert/feint/issues/454
[#455]: https://github.com/stephrobert/feint/issues/455
[#456]: https://github.com/stephrobert/feint/issues/456
[#457]: https://github.com/stephrobert/feint/issues/457
[#460]: https://github.com/stephrobert/feint/issues/460
[#464]: https://github.com/stephrobert/feint/issues/464
[#465]: https://github.com/stephrobert/feint/issues/465
[#466]: https://github.com/stephrobert/feint/issues/466
[#469]: https://github.com/stephrobert/feint/issues/469
[#473]: https://github.com/stephrobert/feint/issues/473
[#474]: https://github.com/stephrobert/feint/issues/474
[#475]: https://github.com/stephrobert/feint/issues/475
[#476]: https://github.com/stephrobert/feint/issues/476
[#477]: https://github.com/stephrobert/feint/issues/477
