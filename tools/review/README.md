# Reviewing a pull request from outside the project

Two commands, for the maintainer, written after 2026-11-16 (#988): that day the
code of a pull request from an account with no public identity was run on the
maintainer's station to verify it. Read first, then run in a box, never on the
host.

## 1. Read: `tools/review/external-pr.sh <number>`

Read-only. It never checks out or runs the pull request's code: everything goes
through `gh api`. It prints three parts.

1. **The diff, what to read first**: paths from `tools/ci/sensitive-paths.txt`
   (the list the "Sensitive paths" check uses), and added lines that
   start a process, reach the network (a new URL with its host), read or set
   the environment, add an `init()`, a `//go:generate`, `//go:linkname`,
   `unsafe`, a long base64 or hex string, a new `uses:` action, a change to
   `go.mod` or `go.sum`, an `env` in a falsify spec, a control or bidi
   character, a file made executable, a symlink or a submodule.
2. **The account, facts and not a score**: age, name and bio given or not,
   followers, pull requests opened on GitHub in total and on the day, forks
   pushed that day, and the `Assisted-by:` trailer commit by commit.
3. **The assembly**: Go identifiers declared by this pull request and used by
   another open one of the same author or by merged code from another outside
   account, in both directions, and the files they share.

Exit codes: `0` nothing the rules know, `2` something to read first (a finding
in part 1, or an identifier link in part 3), `1` cannot judge (an API call
failed, an answer was truncated or of an unknown shape). It never prints
"nothing found" over a read that failed. `--json` gives the same report to a
machine.

**What it does not see**, by construction:
- a payload written to dodge line rules (`"ex"+"ec"`, an import alias,
  reflection, generated code): the report says it is not a review, and the
  diff still has to be read;
- the assembly compares Go only, and only declarations at file level: a shell
  or Python script calling another is not linked;
- it compares with the 50 most recently merged pull requests and prints that
  bound; older outside contributions (#821, #806 on 2026-11-16) are not read;
- search counters and "forks pushed that day" are read at run time and move
  during the day (measured: 177 then 182 pull requests on 2026-11-16);
- an outside author's trust is read from today's `author_association`, not the
  one of the day their pull request merged.

## 2. Run in a box: `tools/review/sandbox.sh <number> [packages]`

Runs `go test` on the pull request's head inside a throwaway Incus container,
never on the station. Measured on Incus 7.6, 2026-11-16, on #987 with
`--pool review`: green, the tests themselves 37 s.

**It needs a storage pool that enforces disk sizes.** The container asks for
a 10 GiB root disk and the script measures what the box sees (`df` inside it)
before anything is pushed in; above 10 GiB, or unreadable, it refuses (exit
1). On a `dir` pool, which is Incus' default, Incus 7.6 accepts the size and
does not apply it: measured 2026-11-16, 3 GiB written into a 1 GiB disk, on
the station's root partition. On the maintainer's station the pool is
`review`, LVM, 30 GiB in a loop file (`incus storage create review lvm
size=30GiB`, created 2026-11-16): there a 1 GiB disk stopped a 2 GiB write
at 364 MiB. Pass it with `--pool review`.

- The container is `--ephemeral --no-profiles` (it deletes itself when it
  stops; the script never runs `incus delete`), unprivileged, without nesting,
  limited to 2 CPUs, 4 GiB with no swap, 4096 processes, named
  `review-<n>-<epoch>-<16 hex>` and labelled with the same 16 hex; it only
  ever stops an instance whose label it set in this run. The instance's
  guest API (`/dev/incus/sock`) is disabled (`security.guestapi: false`).
- **No network while the pull request's code runs.** If the base branch's
  `go.mod` requires modules, they are downloaded first, from the base, with
  the network; the NIC is then removed, and the script measures the cut three
  ways (no NIC, only `lo`, an outbound IPv4 and IPv6 connection that fails)
  before the code goes in. Measured: a test dialling `1.1.1.1:443` fails with
  `network is unreachable`.
- **Nothing from the station.** The code enters as a `git archive` of the head
  commit pushed with `incus file push`; no mount, no Incus socket, no
  `SSH_AUTH_SOCK`, no inherited variable; the tests run as uid 2000. The Go
  toolchain pinned by `mise.toml` is pushed in, not downloaded.
- A pull request that changes `go.mod`, `go.sum`, `go.work` or `vendor/` is
  refused before anything starts: new dependencies are read first.
- Exit codes: `0` tests green, `2` tests red, `1` cannot judge (container not
  ready, network not cut, dependencies changed, deadline exceeded). Output is
  escaped before it reaches the terminal and capped at 4 MiB.

**What it does not protect**:
- **the kernel is shared** with the station: a kernel, LXC or Incus flaw is a
  way out. A VM (`incus-vm`) would give a guest kernel of its own and leave
  only the hypervisor; no VM image exists on the station today;
- **disk is capped only where the pool enforces it**: the script refuses a
  box whose root disk reads larger than 10 GiB in `df`. A `dir` pool on a
  partition of 10 GiB or less would read under the bound with no quota and
  pass, and a pool that enforced a quota without showing it in `df` would be
  refused;
- tests run **without `-race` and without cgo** (no C compiler in the image),
  so this is not `mise run check`;
- tests run on the pull request's head, **not on its merge with `main`**;
- a test that needs the network or Incus fails by construction.

Running it is the maintainer's decision for each pull request: read it with
`external-pr.sh` first.
