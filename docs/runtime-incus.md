# The Incus release behind `--vm`, and what changed between the ones we used

Incus is the only machine runtime behind `--vm`. This page says which Incus
release the runtime proof runs on, how that release is followed, and what
changed upstream between the releases this repository has named. It exists
because none of that was written down before #979: the code named 7.2, the
nightly proof installed whatever Zabbly had published last, and the
maintainer's station ran 7.6.

## The release, and where it is written

**One constant: `machine.IncusTested`** (`internal/core/machine/incus_tested.go`),
today **7.6**. Everything else reads it or is held to it by a test:

| who | how it gets the version | what refuses a disagreement |
|---|---|---|
| `feint doctor` and the prerequisites table of README.md and [install.md](install.md) | `incusRecommended` reads the constant | `feint docs --check` exits 2 (in `mise run prepush`) |
| `.github/workflows/runtime-proof.yml` | `INCUS_VERSION`, installed as `incus=1:<version>-*` | `TestTheRuntimeProofInstallsTheTestedIncus` (`tools/ci/incus_pin_test.go`) |
| `.github/workflows/incus-release.yml` | reads `INCUS_VERSION` from runtime-proof.yml | its own tests, `tools/ci/incus_release_test.go` |
| the install role, `tools/install/ansible/roles/feint_host` (Debian family) | `feint_host_incus_version`, installed as `incus=1:<version>-*` | `TestTheInstallRoleInstallsTheTestedIncus` (`tools/ci/incus_pin_test.go`) |

The floor is a different fact: `machine.IncusMinimum`, **6.0.5** since this lot.
It was 6.0.4, the first release that accepts ACLs on a NIC (`machine.IncusNICACLs`
keeps that fact). 6.0.4 still orders its nftables firewall rules wrongly in
bridge mode, a security bug fixed in 6.0.5 by lxc/incus#2222 (J3 below), and
feint's bridge-mode firewall claims assume that fix. On a 6.0.4 daemon,
`serve --vm incus`, `incus-vm` or `incus-ovn` refuses to start on a 6.0.4 daemon and names the reason, as it does for any capability a mode asked for by name cannot deliver (`internal/cli/cli.go`, read, no test drives it); `--vm auto` passes over OVN, starts the bridge mode with the firewall and isolation withdrawn, and the driver then writes no rule set and no isolation set (`TestABelowFloorHostWritesNoRuleSet`,
`TestABelowFloorHostWritesNoIsolation`); `feint doctor` reports Incus as too
old. All name that reason. The withdrawal applies in every mode, OVN included:
a host below the floor is not supported, even though the floor's reason is a
bridge-mode fix. A host between the floor and the tested release runs
everything feint asks for. The tested release is what was proven, not what is
required.

### Why the pin is a glob over builds

The workflow installs `incus=1:7.6-*`, not one exact package version. Measured
on 2026-11-16 in the channel's index
(`https://pkgs.zabbly.com/incus/stable/dists/noble/main/binary-amd64/Packages.gz`):
Zabbly stable lists **two builds of 7.6 and nothing older**
(`1:7.6-ubuntu24.04-202611071352` and `1:7.6-ubuntu24.04-202611072126`). A pin
on one build would stop installing at the next rebuild. The glob still refuses
any other upstream release: `1:7.6-*` does not match 7.6.1 or 7.7.

The consequence is that **the pin breaks when Zabbly drops the release**, which
on this evidence happens when the next one is published. The runtime proof then
fails at its `Install Incus` step, says the pinned release is no longer served,
and lists what the channel does serve. That is on purpose: a proof that silently
ran on another release would say nothing about what it was measured on, which
is the defect #979 was filed for.

### The user path is not pinned, on purpose

The install page ([install.md](install.md)) and the commands `feint doctor`
prints install the newest Incus of the Zabbly stable channel, unpinned. The
reason is the measurement above: Zabbly keeps only its latest release, so a
pinned command handed to a user would stop installing at the next release.
What is pinned is what proves: runtime-proof.yml and the install role
(`tools/install/ansible/roles/feint_host`, `feint_host_incus_version`). The gap
between the two is said, not hidden: when the installed Incus is not the tested
release, `feint doctor` warns `Incus X is installed; feint is tested on
<IncusTested>` (`TestDoctorSaysWhenIncusIsNotTheTestedRelease`). It is a
warning, never a failure: above `IncusMinimum` everything runs.

Fedora and Rocky do not use Zabbly at all: Fedora 43 and 44 ship Incus 6.23,
the `neelc/incus` COPR builds 7.0.0 (both read on 2026-11-16, see
[install.md](install.md)). Neither is the tested release, and neither was
measured.

### How a new release is noticed

Dependabot cannot do it: it reads no apt channel. `incus-release.yml` runs every
day, reads the same index the runtime proof's runner reads (noble, amd64), and
compares the newest `incus` with the pin. While they differ it keeps **one**
issue open (label `drift`, title "Zabbly stable serves an Incus the runtime
proof does not pin"), naming the release and linking its notes. The first day
they match again closes it. An index it cannot read is a red night, reported by
`night-report.yml`, never a quiet one.

### Moving the pin

1. Read the changelog from the pinned release to the new one and add it to the
   table below, every entry sorted.
2. Set `machine.IncusTested`, `INCUS_VERSION` in runtime-proof.yml and
   `feint_host_incus_version` in the install role's `vars/debian.yml` to the new
   release (the tests refuse one without the others). Re-read
   `machine.OVNMinimum`, `machine.OVSMinimum` and the system minimums
   (`KernelMinimum`, `NftablesMinimum`, `DnsmasqMinimum`) in the new release's
   notes: they are the old release's, not a law.
3. `mise run docs:coverage`, then `go test ./tools/ci/ -run Incus`.
4. `FEINT_VM=incus-ovn mise run conformance:leg -- runtime` on a station running
   the new release.

## The record of releases

| release | where it was proven | date |
|---|---|---|
| 7.2 | the maintainer's station; every `docs/limits.md` measurement that names 7.2 | 2026-08 to 2026-11-15 |
| 7.6 | the maintainer's station, `FEINT_VM=incus-ovn mise run conformance:leg -- runtime`, green, 1466 s | 2026-11-16 |

The 7.6 line is the green runtime leg, plus the sections of `docs/limits.md`
that name Incus 7.6 with the date 2026-11-16, re-measured or re-read on it. The
other sections still name the release they were measured on. What the tables
below mark *possible* or *likely*, and no test or measurement holds, is
written once in `docs/limits.md`,
[Incus releases between the floor (6.0.5) and the tested one (7.6) are read, not measured](limits.md#incus-releases-between-the-floor-605-and-the-tested-one-76-are-read-not-measured),
with the measurement that would settle each group of rows.

## Incus 7.3 to 7.6, sorted for feint

Written 2026-11-16 for #979. **A reading, not a measurement**: every verdict
below comes from a changelog line, and nothing was run against Incus to write
it. The only measurement on 7.6 is the green runtime leg above. Where an entry
says "test", it names what would settle it.

### Sources

| release | date | read |
|---|---|---|
| 7.3.0 | 2026-07-31 | GitHub release body, <https://github.com/lxc/incus/releases/tag/v7.3.0> (about 150 PR titles). No forum announcement found. |
| 7.4.0 | 2026-08-27 | <https://discuss.linuxcontainers.org/t/incus-7-4-has-been-released/27169> and <https://github.com/lxc/incus/releases/tag/v7.4.0> (about 140 PR titles) |
| 7.5.1 | 2026-09-25 | <https://discuss.linuxcontainers.org/t/incus-7-5-has-been-released/27273> and <https://github.com/lxc/incus/releases/tag/v7.5.1>. There is no `v7.5.0` tag; the 7.5 series shipped as 7.5.1. |
| 7.6.0 | 2026-10-30 | <https://discuss.linuxcontainers.org/t/incus-7-6-has-been-released/27462> and <https://github.com/lxc/incus/releases/tag/v7.6.0> (about 190 PR titles) |

What this reading cannot see:

- GitHub release bodies are PR titles. "Various bugfixes", "Fix various OVN
  bugs", "Network fixes" hide the real change; those entries are marked
  **ambiguous** and carry the PR number so the next reader can open it.
- The forum pages were read through a summarising fetch, not verbatim.
- Releases 6.0.4 to 7.2, and the 6.0.x LTS line, are read in
  [their own section](#incus-604-to-72-and-the-60x-lts-line-sorted-for-feint)
  below.
- PR diffs were not read.

### Impact: a behaviour feint depends on may have changed

Rows marked *possible* or *likely* that no test or measurement holds are
grouped, with what would settle them, in
[limits.md](limits.md#incus-releases-between-the-floor-605-and-the-tested-one-76-are-read-not-measured).

**likely** means the line names the mechanism feint uses; **possible**, same
area but too short to be sure; **ambiguous**, only "bugfixes".

| # | release, PR | what changed | where feint depends on it | verdict, and what settles it |
|---|---|---|---|---|
| I1 | 7.6, #4097 | `security.mac_filtering`, `security.ipv4_filtering`, `security.ipv6_filtering` on OVN NICs, enforced by OVN port security | `incus_firewall.go`: `security.acls` is the one key an OVN NIC updates in place (`UpdatableFields` at v7.2.0); any other key re-plugs the device | **measured on 7.6, 2026-11-16**: on a running container's OVN NIC, `security.ipv4_filtering` and `security.mac_filtering` kept ifindex 306 and the address, while a default-action key moved it to 310 with no address ([limits.md](limits.md#subnet-isolation-depends-on-the-runtime-mode), the OVN re-plug passage). feint sets neither flag. The routed-NIC claim (#337) is not contradicted: 7.6 names OVN NICs only. |
| I2 | 7.5, #4016, #4035 | ACL actions checked against the host firewall; rules it cannot apply are refused; better revert of ACL changes | the ACL PUT (`incus_firewall.go`), the isolation ACL (`incus_isolate.go`), `securitygroups.go` ("one bad rule and the whole PUT is refused") | **half measured on 7.6, 2026-11-16**: `allow-stateless` on a bridge network is refused whole, which is why the bridge modes refuse it first (`capabilities.stateless_rules`). What remains, the PUT of every other action feint emits (reject, drop, allow) on 7.6 and a bad rule still refusing the whole PUT, is grouped in [limits.md](limits.md#incus-releases-between-the-floor-605-and-the-tested-one-76-are-read-not-measured). |
| I3 | 7.4, #3897 | "Reverse enforcement logic for cross-network OVN rules (internal/external)" | subnet isolation between two VPCs (`incus_isolate.go`, `incus_ovn_isolate.go`) | **possible**; "reverse" does not say of what. The runtime leg's isolation checks passed on 7.6. |
| I4 | 7.3, #3587; 7.4, #3879 | persistent logical switch ports; MAC bindings fixed on deletion | the start/stop/delete races feint serialises (`incus.go` detach and re-plug, "Cannot find security ACL ID") | **possible**. Settle by running the race test against a real 7.6 with the lock removed in a scratch copy: if it no longer bites, the comment's timing is stale, not the lock. |
| I5 | 7.5, #4066, #3995 | peering with child networks; a test covering several peers on one network | `incus_ovn.go`: two pending halves aiming at one network make every create fail ("More than one matching network peer was found"); `peer delete` leaves an `Errored` row | **measured on 7.6, 2026-11-16**: the three-network sequence of `incus_ovn.go` replayed on the station ends `Created`, and a delete now wrecks only the far half of its own pair ([limits.md](limits.md#two-more-of-incus-peering-rules-and-why-a-net-of-three-subnets-found-them-456), "Both rules changed upstream"). The fix is in 7.0.2 and 7.5.1+, in no 6.0.x (J1), so the workaround stays. |
| I6 | 7.6, #4105, #4138, #4157, #4218; 7.5, #3988; 7.3, #3635, #3676 | OVN creation retries, error handling, stale connections | the English error strings feint matches ("network not found", "already exists", "Network not found" in peer cascades; `cli.go`, `incus_traps.go`, `incus_balancer.go`) | **ambiguous**. "Improve OVN error handling" may reword messages. Settle with one live probe per matched phrase, keeping the 7.2 text beside the 7.6 text. |
| I7 | 7.4, #3775; 7.6, #4103 | proxy ARP instead of NAT rules for OVN `l2proxy`; `ipvX.nat.address` with the default ingress mode | public addresses published with `ipv4.routes.external` (`incus_ovn.go`, `incus.go`); the #496 limit (ARP unanswered on `feint-uplink`) | **likely**. Settle with the runtime leg's public-address checks (green on 7.6) plus the #496 probe (tcpdump on `feint-uplink`). |
| I8 | 7.4, #3772, #3835 | live update of `ipv4/ipv6.address.external`; external NAT on auto-assigned OVN addresses | none today | an improvement rather than an impact: see E3. |
| I9 | 7.5, #3997, #4017; 7.6, #4268, #4296, #4215 | concurrent `onStop` prevented; start/restart race fixed; force stop no longer stuck | `runUntilFree` waits out "busy" refusals (three phrases measured on 7.2, `incus.go`), the `stop --force` path | **likely**. If the busy refusals are gone or reworded, the phrase list is dead code. Settle by recording which of the three phrases still appear on 7.6 under the existing race tests. |
| I10 | 7.3 to 7.6, #3785, #3784, #3889 | event ordering, connection liveness | `incus_watch.go` (stream silent forever, both lifecycle events per cycle, "Instance storage pool not found") | **possible**. Settle with the watch tests against a live 7.6. |
| I11 | 7.3, #3714 | server-side filtering fixed | none found: feint lists with `?recursion=1` and filters in Go | low. |
| I12 | 7.4, #3838, #3851; 7.5, #3934; 7.6, #4247 | image locking, compression write errors, access check on publish, cleanup on refresh | `incus_images.go` (publish a builder instance, "Image not found") | **possible**, low risk: the access check targets restricted clients and feint runs as the local admin. Settle with `feint images` on 7.6. |
| I13 | 7.6, #4230 | device names restricted | feint names devices `eth0`, `eth1` and a per-network name (`incus.go`) | **ambiguous**: the rule is not in the release body. Read #4230, then create a machine with the longest device name feint generates. |
| I14 | 7.4 (forum) | **OVS and OVN minimums raised to 3.3.0 and 24.03.0** | the install table of [install.md](install.md) pairs Zabbly Incus with the distribution's OVN | **impact, by reading**. Ubuntu 24.04 ships OVN 24.03 and OVS 3.3, exactly the new floor. **Debian 12 ships OVN 23.03.1 and OVS 3.1.0** (sources.debian.org, bookworm, read 2026-11-16), below it: the Debian 12 row of the install table named a pairing Incus 7.4 and later no longer support. **Ubuntu 22.04 ships OVN 22.03.8 and OVS 2.17.12** (jammy-updates, Launchpad, read 2026-11-16), below it too. Now marked not supported for `incus-ovn` in [install.md](install.md); `feint doctor` prints no OVN command on Debian 12 or Ubuntu 22.04 (`TestDebian12GetsNoOVNCommand`), reads the version of an OVN already installed on any distribution against `machine.OVNMinimum` and `machine.OVSMinimum` (`TestAnOVNOlderThanTheTestedIncusNeedsIsAStep`), and the install role refuses OVN there. Not measured on a Debian 12 machine. |
| I15 | 7.4, #3906; 7.5 | DHCPv6 with `security.ipv6_filtering`; IPv6 filtering without `br_netfilter`; OVN uplinks inherit the bridge MTU | feint sets no `bridge.mtu` | **possible**. Settle by comparing `incus network show feint-uplink` and the guest MTU on 7.6. |
| I16 | 7.4, #3758 | more config keys hidden as sensitive | feint reads `security.acls`, `ipv4.address` and device keys, none sensitive | low, not checked live. |
| I17 | 7.3, #3636 | a storage metric renamed | no use in feint | none. |

### Improvements feint could use

Each one needs a lot or an issue of its own before anything is built; issue
#981 carries all eleven until then, the last five (E7 to E11) as low or
unclear.

| # | release, PR | what it offers | what it could change in feint |
|---|---|---|---|
| E1 | 7.6, #4097 | MAC and IP filtering on OVN NICs (I1) | "source address spoofing refused" for OVN machines, which real cloud hosts do. Check what each cloud documents first (rule 4: no invented behaviour), and whether setting the keys re-plugs a live NIC. |
| E2 | 7.5, 7.6 #4091, #4066 | OVN child networks: a child shares its parent's router, uplink and peerings, keeps its own subnet, DHCP, DNS, ACLs and NAT | the subnets of one VPC on one logical router, closer to a real VPC; could remove the intra-VPC peering mesh and the "More than one matching network peer" trap (#456). Needs a measurement that a child stays isolated from a sibling with ACLs attached. |
| E3 | 7.4 #3772, #3835; 7.6 #4103 | a directly settable external address on the NIC | could replace route-based publication for some modes and drop the re-plug caused by changing `ipv4.address` (`incus_address.go`). Which NIC types the live update covers is not said: read #3772. |
| E4 | 7.3 #3587, 7.4 #3879 | persistent switch ports, MAC binding fix | may remove the re-plug races and the per-network lock's cost; only after the I4 measurement. |
| E5 | 7.4 #3871, #3912 | `incus query --data-file` | request bodies currently travel as `--data` arguments, visible in `ps`; a file or stdin would hide them. Hardening, not a feature. |
| E6 | 7.5 #4020, #4014; 7.6 #4074 | load balancer health lifecycle events; IPv6 health monitors fixed | a real backend health state for the load balancer APIs, which today "grade none" of their backends; the event stream is already read by `incus_watch.go`. |
| E7 | 7.4 | `security.protection.start` | no cloud API in scope has a start-protection field. Low. |
| E8 | 7.6, #4118 | `boot.priority=skip`, NICs skipped by default | probably the VM boot order, not NIC attachment. Read #4118 before using. |
| E9 | 7.5 | `dns.include_hosts` on bridges | keeps the station's `/etc/hosts` out of bridge-mode guests' DNS. Does not address the 90 s resolver wait. Low. |
| E10 | 7.4 | multicast snooping on OVN, `ipv6.ra` | only if a pack ever needs router advertisements off. Low. |
| E11 | 7.6, #4159, #4130 | "Various VM bugfixes" | the VM NIC hot-plug limit in `docs/limits.md` is not claimed fixed by anything read; re-measure on 7.6. |

### Irrelevant to feint, one line each

- Storage drivers other than the default pool (ZFS, btrfs, LVM, Ceph, Linstor,
  S3 buckets, qcow2, NBD, quotas): feint uses the default pool only.
- Clustering (evacuation, healing, rebalance, certificates): feint is one node.
- VM internals (QMP, migration, SR-IOV, NVRAM, TPM, GPU, balloon, BSD and
  Windows agents): feint asks nothing of them.
- Authentication and authorization (OIDC, OpenFGA, JWT, ACME, trust store):
  feint talks to the local socket as admin.
- Client UX (`list --all-remotes`, `file push`, completion, installers): feint
  reads JSON through `incus query`.
- OCI containers: feint boots system containers and VMs.
- LXC, CRIU, seccomp, AppArmor, idmap, cgroup checks, I/O and NIC burst limits:
  no feint path sets them.
- Networking feint does not use: BGP, network forwards, zones, restricted
  project networks, the nftables template race (a daemon-internal race).
- Docs, translations, dependency bumps, the Go baseline, upstream CI, database
  internals.
- Security advisories of 7.3 to 7.6 (image export path traversal, private image
  import, OVN ACL log events leaking across projects, guest-API image export):
  they concern multi-project or non-root use, and feint runs in the default
  project as admin. **Not an impact, but a reason to run a current release.**

## Incus 6.0.4 to 7.2, and the 6.0.x LTS line, sorted for feint

Written 2026-11-16 for #979, in the same form as the section above. **A
reading, not a measurement**: changelog lines, forum commit lists and, where
marked "source", upstream files read at a tag. Nothing was run against these
releases. This part matters for the floor: feint supports every release from
`machine.IncusMinimum` (6.0.5) up, and the 6.0.x LTS line only receives
backports, so a fix of a 6.x feature release reached a 6.0.x user only if the
6.0.5 or 6.0.6 commit list carries it.

### Sources

| release | date | read |
|---|---|---|
| 6.0.5 LTS | 2025-08-15 | <https://discuss.linuxcontainers.org/t/incus-6-0-5-lts-has-been-released/24445>, raw text, 781 commit lines |
| 6.0.6 LTS | 2026-03-16 | <https://discuss.linuxcontainers.org/t/incus-6-0-6-lts-has-been-released/26380>, raw text, 796 commit lines |
| after 6.0.6 | none | `compare v6.0.6...stable-6.0`: 111 commits, all security hardening, last 2026-10-30. 6.0 is security-only since 7.0. No 6.0.7 on 2026-11-16. |
| 6.1 to 6.23 | 2024-05-07 to 2026-03-27 | GitHub release bodies (PR titles); of the forum posts only 6.23 was read |
| 7.0.0 LTS | 2026-05-01 | <https://discuss.linuxcontainers.org/t/incus-7-0-lts-has-been-released/26641> ("Breaking changes") and the GitHub body |
| 7.0.1, 7.0.2 | 2026-07-10, 2026-10-16 | GitHub `compare` only; the bodies are empty. The 7.0.x tags look cut from main, not from a narrow branch: 7.0.2 carries much of 7.1 to 7.5. Not understood, not inferred from. |
| 7.1.0, 7.2.0 | 2026-05-29, 2026-06-26 | GitHub bodies; the 7.2 forum post (headings) |

What this reading cannot see: backports were found by commit title in the
6.0.5 and 6.0.6 lists, so a fix backported under another title is missed;
6.1 to 6.22 forum posts were not read; PR diffs were read for three entries
only (#2985, #3278, e3c6e740fe).

### Impact: a behaviour feint depends on may have changed

Rows marked *possible* or *likely* that no test or measurement holds are
grouped in
[limits.md](limits.md#incus-releases-between-the-floor-605-and-the-tested-one-76-are-read-not-measured).

| # | release, PR | what changed | where feint depends on it | verdict, and what feint does |
|---|---|---|---|---|
| J1 | 7.0.2, 7.5.1; e3c6e740fe (lxc/incus#3987) | OVN mutual peer lookups scoped to the target network | `incus_ovn.go` peering locks and rebuild (#456) | **likely**, read in `driver_ovn.go` at each tag: present in 7.0.2 and 7.5.1+, absent from every 6.0.x (6.0.6 and the `stable-6.0` tip included), 7.0.0, 7.0.1, 7.3, 7.4. The workaround stays: the floor is a 6.0.x. `docs/limits.md` and the driver comment now say so. |
| J2 | 6.22, #2985; in 6.0.6 | an OVN NIC's default egress action `allow` becomes `allow-related` (stateful) | none | **no effect on a feint claim.** On an OVN NIC feint sets only `security.acls` and never a default action (`incus_firewall.go`: openness is a catch-all rule inside the set, because a default-action key re-plugs the NIC); default actions are set on bridge NICs only, which this OVN change does not touch. |
| J3 | 6.14, #2222; in 6.0.5 | nftables rule ordering, "Fix security issue due to rule ordering in nftables (ACLs + isolation)" | bridge-mode ACLs and isolation | **likely, bridge mode.** The changelog does not say what 6.0.4 let through. Acted on: **the floor is 6.0.5** (`machine.IncusMinimum`). On 6.0.4 a mode asked for by name refuses to start; `--vm auto` starts the bridge mode with the firewall and isolation withdrawn, and no rule set or isolation set is written; `feint doctor` reports Incus as too old. All name this fix. |
| J4 | 6.16, #2433; in 6.0.6 | an ACL name as a rule's match source; directional port groups | group-to-group rules, if feint emits an ACL name as source | **possible**. This driver expands a group reference into blocks before it reaches the runtime (`incus_firewall.go` header), so no ACL name is emitted. Not measured. |
| J5 | 6.19, #2600 (in 6.0.6); 7.0, #3140 (not in 6.0.x) | OVN port removal; stale port UUIDs cleaned from ACL port groups on stop | the per-network lock against `Cannot find security ACL ID` (`incus.go`) | **possible to likely**. The lock stays for the 6.0.x floor. Settle with the race tests on 6.0.6 and 7.0.2 with the lock removed in a scratch copy. |
| J6 | 6.22, #2979; in 6.0.6 | an uplink range or gateway change re-sets up the dependent OVN networks | batched writes to `feint-uplink` (`incus_ovn.go`, #496) | **possible**. Not measured: time one uplink write with 15 OVN networks on 6.0.6 and 7.6. |
| J7 | 7.0, #3278; not in 6.0.x | routed NIC host routes carry a next hop | `ipv4.routes` on routed NICs | **possible**. The runtime leg on 7.6 passes; 6.0.x keeps the old form. |
| J8 | 7.0, #3271, #3276; 7.1, #3373; 6.0.6, #2555 | external addresses refused when another network, forward or balancer holds them | balancer VIPs (`incus_balancer.go`) | **possible**: a new refusal text feint does not match. Not measured. |
| J9 | 6.21, #2827 (in 6.0.6); 7.1, #3423; 7.0, #3226 | `$` refused in API names; device names validated | names feint gives instances, networks, ACLs, devices; `safeName` | **possible**. `safeName` should stay narrower than upstream; not checked against the new rules. |
| J10 | 6.0.5; 6.19, #2633 (in 6.0.6) | event stream race; no lifecycle events for pending entities | `incus_watch.go` | **possible**. Not measured on 6.0.5. |
| J11 | 6.13, #2024; in 6.0.5 | wait up to 10 s for northd to allocate an OVN IP | feint gives each NIC a static address | low. |
| J12 | 6.13, #2091; 6.0.5, 6.0.6; 6.22, #2981 | static uplink gateway MAC | no use found in feint | none unless feint sets `gateway.hwaddr`. |
| J13 | 7.0 (forum, "Breaking changes") | minimums: Linux 6.12, nftables 1.0.0, dnsmasq 2.90, Open vSwitch 2.15.0, OVN 23.03.0 (OVN and OVS raised again by 7.4, I14) | the install page and `feint doctor` | **likely**. Acted on: `machine.KernelMinimum`, `NftablesMinimum`, `DnsmasqMinimum`; `feint doctor` reads the three without root and warns below them; [install.md](install.md) lists them. What fails below them was not measured. |
| J14 | 6.12, #1884 (not in 6.0.x); 6.21, #2832 (in 6.0.6) | concurrent restarts; hooks blocked until the daemon is ready | start and stop paths (`incus.go`) | **possible**. As J5. |
| J15 | 6.4 to 7.2, several | other OVN fixes, titles only ("Fix network peering" #2294, "OVS/OVN related fixes" #3453, …) | none named | **ambiguous**. Only the runtime leg on the target release settles them. |
| J16 | 6.9, #1587; 6.1, #810 | ACLs on bridge NICs with the nftables driver; stateless rules | bridge-mode firewall; `capabilities.stateless_rules` | **none**. `allow-stateless` is in `acl_ovn.go` at 6.0.4 (source), so OVN stateless holds on the floor. #1587 was backported to the LTS before the floor under another title: at tag `v6.0.4`, `internal/server/device/nic_bridged.go` holds 13 occurrences of `security.acls` (0 at `v6.0.0`) and feeds `acl.FirewallACLRules` to the nftables driver's `InstanceSetupBridgeFilter(…, aclRules []ACLRule)` (source read 2026-11-16). This agrees with `docs/limits.md`. The bridge firewall tests were not run on a 6.0.x daemon. |
| J17 | 6.11, #1776; 6.18, #2589 | MAC address prefix | no use in `internal/core/machine` (grep) | none. |

### Improvements feint could use

Eleven candidates (peering without the workaround on 7.0.2 and 7.5.1+, stale
port cleanup, an external address on the NIC from 6.0.5, static gateway MAC,
default egress `allow-related`, NIC limits, OVN DNS options, address sets,
forward SNAT, `incus wait`, server-side filtering): the eleven of this section
are in #981, items 7 to 17. None is built in #979.

### Irrelevant to feint, grouped

Storage drivers; clustering and IncusOS; VM internals; container and OCI
features; authentication and API hardening (the 6.0.6, 7.0 and 7.2 security
advisories concern images, backups and restricted projects, while feint runs as
admin in the default project); CLI conveniences; networking feint does not use
(BGP, zones, OVN interconnect, tunnels, macvlan, VRF, SR-IOV); build, docs and
translations.
