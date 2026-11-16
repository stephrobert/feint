#!/usr/bin/env bash
# Run the tests of a pull request from outside the project in a throwaway
# Incus container, never on the station (#988, item 4):
#
#   tools/review/sandbox.sh [--timeout SECONDS] [--image ALIAS] [--pool NAME] \
#       <pull request number> [Go package...]
#
# The code of the pull request is somebody else's code. Running `go test` on
# the maintainer's own station executes it with that station's account, keys
# and network (it happened on 2026-11-16). This command runs it in a container
# that holds nothing of the station and cannot reach anything.
#
# Exit codes: 0 the tests are green, 2 the tests are red, 1 the pull request
# could not be judged (container not ready, network not cut, go.mod changed,
# deadline exceeded, malformed argument). Reading the code comes first:
# tools/review/external-pr.sh prints what to read before anything runs.
#
# What the container is, and the test that fails without each part
# (tools/review/sandbox/sandbox_test.go, mutations in
# tools/falsify/specs/a-pull-request-runs-only-in-a-box-with-no-way-out.json):
#   - named review-<number>-<epoch>-<16 hex>, labelled user.review-sandbox=<the
#     same 16 hex>, and only an instance that carries this run's label is ever
#     stopped (TestAnInstanceThatIsNotOursIsNeverStopped);
#   - ephemeral, no profile, unprivileged, no nesting, limited in CPU, memory
#     and processes (TestTheBoxIsEphemeralUnprivilegedAndLimited);
#   - a root disk and nothing else: no NIC, no mount of the station, no Incus
#     socket, no variable of the station (TestNothingOfTheStationEntersTheBox);
#   - the network exists only for `go mod download`, from the base of the
#     pull request's go.mod and go.sum, and only when that go.mod requires a
#     module; it is cut, and the cut is measured three ways, BEFORE the code
#     of the pull request is pushed (TestTheCodeEntersOnlyAfterTheNetworkIsMeasuredCut);
#   - a pull request that changes go.mod, go.sum, go.work or vendor/ is
#     refused: new dependencies are read before they are downloaded
#     (TestAChangedDependencyFileIsRefused);
#   - the code enters as a `git archive` of the head commit, pushed with
#     `incus file push` (TestTheCodeEntersAsAnArchiveOnly);
#   - the whole run is bounded, and the container is stopped (an ephemeral
#     container is deleted by its stop) by a trap whatever happens
#     (TestTheBoxIsStoppedWhateverHappens);
#   - the output is read through an escaper before it reaches the terminal
#     (TestTheOutputIsEscaped).
set -euo pipefail
umask 077

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "${here}/../.." && pwd)"
escaper="${here}/sandbox/escape.py"

timeout_s=1200
image='feint/debian/13'
pool='default'

usage() {
  cat >&2 <<'EOF'
usage: tools/review/sandbox.sh [--timeout SECONDS] [--image ALIAS] [--pool NAME] <pull request number> [package...]
  packages are Go package patterns such as ./internal/cli or ./internal/...
  with none, the packages holding a Go file the pull request adds or changes.
EOF
  exit "${1:-1}"
}

cannot() {
  printf 'sandbox: cannot judge: %s\n' "$*" >&2
  exit 1
}

warn() { printf 'sandbox: %s\n' "$*" >&2; }

# ---- arguments: every one is checked before anything else is done -----------

while (($#)); do
  case "$1" in
    --timeout) (($# >= 2)) || usage; timeout_s="$2"; shift 2 ;;
    --image) (($# >= 2)) || usage; image="$2"; shift 2 ;;
    --pool) (($# >= 2)) || usage; pool="$2"; shift 2 ;;
    -h | --help) usage 0 ;;
    --) shift; break ;;
    -*) cannot "unknown option $(printf '%q' "$1")" ;;
    *) break ;;
  esac
done

re_timeout='^[1-9][0-9]{0,3}$'
[[ ${timeout_s} =~ ${re_timeout} ]] || cannot "--timeout must be a number of seconds from 5 to 7200"
((timeout_s >= 5 && timeout_s <= 7200)) || cannot "--timeout must be a number of seconds from 5 to 7200"

# A local alias only: no remote prefix (a colon), so nothing is ever pulled.
re_image='^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$'
[[ ${image} =~ ${re_image} ]] || cannot "image must be a local alias such as feint/debian/13"
re_pool='^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$'
[[ ${pool} =~ ${re_pool} ]] || cannot "pool name is malformed"

(($# >= 1)) || usage
pr="$1"
shift
re_pr='^[1-9][0-9]{0,6}$'
[[ ${pr} =~ ${re_pr} ]] || cannot "the pull request number must be a positive integer, got $(printf '%q' "${pr}")"

# A package pattern: ".", "./a/b", "./a/...", "./..." and nothing else. A word
# that starts with a dash would be a go flag, a `..` segment would leave the tree.
re_pkg='^\.(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*(/\.\.\.)?$'
want_pkgs=()
for p in "$@"; do
  [[ ${p} =~ ${re_pkg} ]] || cannot "malformed Go package pattern $(printf '%q' "${p}")"
  want_pkgs+=("${p}")
done

for t in git incus python3 tar gzip timeout go od tr; do
  command -v "${t}" >/dev/null 2>&1 || cannot "${t} is not installed or not on PATH"
done

# ---- state, deadline, cleanup ----------------------------------------------

work="$(mktemp -d)"
start=${SECONDS}
created=0
name=''
token=''
left=0

calc_left() {
  left=$((timeout_s - (SECONDS - start)))
  ((left > 0)) || cannot "the deadline of ${timeout_s}s is exceeded"
}

# Every external command runs under what is left of the global deadline. It is
# started in the background and waited for, because bash runs a trap only when
# the foreground command ends: a SIGTERM during a 20-minute test would wait the
# 20 minutes (measured on 2026-11-16 with the sleeping test of the proof).
# Its input is bounded_stdin, /dev/null unless a call names a file.
child=0
bounded_stdin=/dev/null
bounded() {
  calc_left
  local rc=0
  timeout --kill-after=5 "${left}" "$@" <"${bounded_stdin}" &
  child=$!
  wait "${child}" || rc=$?
  child=0
  if ((rc == 124 || rc == 137)); then
    cannot "the deadline of ${timeout_s}s is exceeded"
  fi
  return "${rc}"
}

step() { printf 'sandbox: [%3ss] %s\n' "$((SECONDS - start))" "$*" >&2; }

show_file() { python3 -I "${escaper}" <"$1" >&2 || true; }

# Stops the container, and only a container that carries THIS run's label: a
# well-formed name is not proof the instance is ours (CLAUDE.md, "Bien formé
# n'est pas autorisé"). An ephemeral container is deleted by its own stop.
# shellcheck disable=SC2329 # called by the EXIT trap, which shellcheck does not follow
stop_container() {
  [[ ${created} == 1 ]] || return 0
  created=0
  local label=''
  if ! label="$(timeout 30 incus config get "${name}" user.review-sandbox 2>/dev/null)"; then
    if timeout 30 incus info "${name}" >/dev/null 2>&1; then
      warn "cannot read the label of ${name}: left alone, check it with incus list"
      return 1
    fi
    return 0 # already gone: an ephemeral container deletes itself when it stops
  fi
  if [[ ${label} != "${token}" ]]; then
    warn "${name} does not carry this run's label: not touched"
    return 1
  fi
  timeout 120 incus stop --force "${name}" >/dev/null 2>&1 || true
  if timeout 30 incus info "${name}" >/dev/null 2>&1; then
    warn "${name} still exists after its stop: remove it by hand"
    return 1
  fi
  return 0
}

# shellcheck disable=SC2329 # called by the EXIT trap, which shellcheck does not follow
cleanup() {
  local rc=$?
  trap - EXIT
  stop_container || rc=1
  rm -rf "${work}"
  exit "${rc}"
}
trap cleanup EXIT
# shellcheck disable=SC2329 # called by the INT and TERM traps, which shellcheck does not follow
on_signal() {
  trap - INT TERM
  if ((child > 0)); then kill -TERM "${child}" 2>/dev/null || true; fi
  exit "$1"
}
trap 'on_signal 130' INT
trap 'on_signal 143' TERM

# ---- read the pull request, read-only ---------------------------------------

cd "${root}"
# A hook exports GIT_DIR and friends, and a child git obeys them first (#955).
while IFS= read -r v; do unset "${v}"; done < <(git rev-parse --local-env-vars)

step "fetching pull/${pr}/head and main (read-only)"
bounded git fetch --quiet --no-tags --no-recurse-submodules origin "refs/pull/${pr}/head" >/dev/null 2>&1 \
  || cannot "cannot fetch refs/pull/${pr}/head from origin"
head_sha="$(git rev-parse --verify --quiet 'FETCH_HEAD^{commit}')" || cannot "refs/pull/${pr}/head is not a commit"
bounded git fetch --quiet --no-tags --no-recurse-submodules origin refs/heads/main >/dev/null 2>&1 \
  || cannot "cannot fetch refs/heads/main from origin"
main_sha="$(git rev-parse --verify --quiet 'FETCH_HEAD^{commit}')" || cannot "refs/heads/main is not a commit"
base_sha="$(git merge-base "${main_sha}" "${head_sha}")" || cannot "the pull request shares no history with main"
re_sha='^[0-9a-f]{40,64}$'
[[ ${head_sha} =~ ${re_sha} && ${base_sha} =~ ${re_sha} ]] || cannot "unexpected commit identifier"

changed=()
while IFS= read -r -d '' f; do changed+=("${f}"); done \
  < <(git diff --name-only -z --no-renames "${base_sha}" "${head_sha}")
added_or_modified=()
while IFS= read -r -d '' f; do added_or_modified+=("${f}"); done \
  < <(git diff --name-only -z --no-renames --diff-filter=AM "${base_sha}" "${head_sha}")

is_dependency_file() {
  case "$1" in
    go.mod | */go.mod | go.sum | */go.sum | go.work | */go.work | go.work.sum | */go.work.sum | vendor/* | */vendor/*) return 0 ;;
  esac
  return 1
}

refused=0
for f in "${changed[@]}"; do
  if is_dependency_file "${f}"; then
    warn "the pull request changes $(printf '%q' "${f}")"
    refused=1
  fi
done
((refused == 0)) || cannot "the pull request changes a dependency file (go.mod, go.sum, go.work, vendor/): new dependencies are read first, and downloaded by nobody here"

# ---- which packages -----------------------------------------------------------

re_dir='^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*$'
pkgs=()
if ((${#want_pkgs[@]} > 0)); then
  for p in "${want_pkgs[@]}"; do
    case "${p}" in
      *...) ;;
      .) ;;
      *) git cat-file -e "${head_sha}:${p#./}" 2>/dev/null || cannot "the pull request has no directory ${p}" ;;
    esac
    pkgs+=("${p}")
  done
else
  declare -A seen=()
  for f in "${added_or_modified[@]}"; do
    case "${f}" in *.go) ;; *) continue ;; esac
    case "/${f}" in */testdata/* | */vendor/* | */_*) continue ;; esac
    d="$(dirname -- "${f}")"
    if [[ ${d} == . ]]; then
      p='.'
    else
      [[ ${d} =~ ${re_dir} ]] || cannot "a Go file sits in a directory with an unusual name: name the packages yourself"
      p="./${d}"
    fi
    if [[ -z ${seen[${p}]:-} ]]; then
      seen[${p}]=1
      pkgs+=("${p}")
    fi
  done
fi
((${#pkgs[@]} > 0)) || cannot "the pull request adds or changes no Go package: name the packages to test"

# ---- the toolchain: the pinned one, from this checkout, never downloaded -------

re_go='^go[[:space:]]*=[[:space:]]*"([0-9]+\.[0-9]+\.[0-9]+)"'
want_go=''
while IFS= read -r line; do
  if [[ ${line} =~ ${re_go} ]]; then
    want_go="${BASH_REMATCH[1]}"
    break
  fi
done <"${root}/mise.toml"
[[ -n ${want_go} ]] || cannot "mise.toml pins no Go version"
goroot="$(go env GOROOT)"
[[ -x ${goroot}/bin/go && -f ${goroot}/VERSION ]] || cannot "the host Go has no GOROOT to copy"
have_go=''
read -r have_go <"${goroot}/VERSION" || true
[[ ${have_go} == "go${want_go}" ]] || cannot "the host Go is ${have_go:-unknown}, mise.toml pins go${want_go}"

# ---- what goes in: an archive, and the base go.mod ----------------------------

git archive --format=tar --output="${work}/pr.tar" "${head_sha}" || cannot "git archive failed"
archive_size="$(wc -c <"${work}/pr.tar")"
((archive_size <= 209715200)) || cannot "the archive is larger than 200 MiB"
git show "${base_sha}:go.mod" >"${work}/base-go.mod" 2>/dev/null || cannot "the base commit has no go.mod"
needs_modules=0
if grep -Eq '^[[:space:]]*require[[:space:]]' "${work}/base-go.mod"; then
  needs_modules=1
  git show "${base_sha}:go.sum" >"${work}/base-go.sum" 2>/dev/null || cannot "the base go.mod requires modules and the base has no go.sum"
fi

step "packing the pinned Go toolchain (go${want_go})"
bounded tar -C "${goroot}" --owner=0 --group=0 --numeric-owner \
  --exclude=./test --exclude=./doc --exclude=./misc -czf "${work}/goroot.tgz" . \
  || cannot "cannot pack the Go toolchain"

# ---- the box ----------------------------------------------------------------

token="$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"
name="review-${pr}-$(date +%s)-${token}"
re_name='^review-[1-9][0-9]{0,6}-[0-9]{9,11}-[0-9a-f]{16}$'
[[ ${name} =~ ${re_name} ]] || cannot "the container name is malformed"

timeout 30 incus image info "${image}" >/dev/null 2>&1 || cannot "the image ${image} is not on this station"

# The root disk is bounded: a test that writes without end must not fill the
# station's partition. Incus 7.6 ACCEPTS `size:` on a `dir` pool and applies
# nothing (measured on 2026-11-16: 3 GiB written to a 1 GiB root disk, `df` in
# the box showing the host's 929G). So the size is a request, and the box is
# measured below, before anything of the pull request enters.
disk_size=10GiB
disk_limit=10737418240

# One root disk, and nothing else. `--no-profiles` removes the default NIC,
# `--ephemeral` makes the stop a deletion.
cat >"${work}/launch.yaml" <<EOF
config:
  user.review-sandbox: "${token}"
  security.privileged: "false"
  security.nesting: "false"
  security.guestapi: "false"
  limits.cpu: "2"
  limits.memory: 4GiB
  limits.memory.swap: "false"
  limits.processes: "4096"
  boot.autostart: "false"
devices:
  root:
    type: disk
    path: /
    pool: ${pool}
    size: ${disk_size}
EOF

step "launching ${name} from ${image}"
created=1
bounded_stdin="${work}/launch.yaml"
bounded incus launch "${image}" "${name}" --ephemeral --no-profiles >"${work}/launch.log" 2>&1 \
  || { show_file "${work}/launch.log"; cannot "the container did not launch"; }
bounded_stdin=/dev/null

user_env=(
  --env HOME=/home/review
  --env PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin
  --env GOTOOLCHAIN=local
  --env GOFLAGS=-mod=readonly
  --env GOCACHE=/home/review/.cache/go-build
  --env CGO_ENABLED=0
)

# box_root STAGE CMD... runs as root in the box, box_user as the unprivileged
# account. Neither forwards anything of the station: only the --env above.
box_root() {
  local stage="$1"
  shift
  bounded incus exec "${name}" --force-noninteractive --env "REVIEW_STAGE=${stage}" --env HOME=/root -- "$@" </dev/null
}
box_user() {
  local stage="$1" proxy="$2" cwd="$3"
  shift 3
  bounded incus exec "${name}" --force-noninteractive --user 2000 --group 2000 --cwd "${cwd}" \
    --env "REVIEW_STAGE=${stage}" --env "GOPROXY=${proxy}" "${user_env[@]}" -- "$@" </dev/null
}

stage_fail() { # stage_fail STAGE: show the stage's log, then give up
  show_file "${work}/stage.log"
  cannot "the stage $1 failed"
}

step "waiting for the box"
ready=0
for _ in $(seq 1 30); do
  if box_root wait true >"${work}/stage.log" 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
((ready == 1)) || stage_fail wait

step "measuring that the root disk is bounded"
box_size="$(box_root disk df --output=size -B1 / 2>/dev/null | tail -n 1 | tr -d ' ')" || box_size=''
re_size='^[0-9]+$'
if ! [[ ${box_size} =~ ${re_size} ]]; then
  cannot "cannot read the size of the root disk in the box"
fi
if ((box_size > disk_limit)); then
  cannot "the pool ${pool} does not apply the ${disk_size} size of the root disk (the box sees ${box_size} bytes): a test that writes without end would fill the station. Use a pool whose driver enforces quotas (zfs, btrfs, lvm) with --pool NAME"
fi

step "preparing the box: account, toolchain"
bounded incus file push --mode 0600 "${work}/goroot.tgz" "${name}/root/goroot.tgz" >"${work}/stage.log" 2>&1 || stage_fail push-goroot
box_root prep sh -c 'set -eu; useradd -m -u 2000 -s /bin/bash review; mkdir -p /usr/local/go /home/review/pr /home/review/base; tar -xzf /root/goroot.tgz -C /usr/local/go; rm -f /root/goroot.tgz; chown -R review:review /home/review' \
  >"${work}/stage.log" 2>&1 || stage_fail prep

if ((needs_modules == 1)); then
  step "the base go.mod requires modules: network for go mod download, then cut"
  bounded incus file push --uid 2000 --gid 2000 --mode 0600 "${work}/base-go.mod" "${name}/home/review/base/go.mod" >"${work}/stage.log" 2>&1 || stage_fail push-base
  bounded incus file push --uid 2000 --gid 2000 --mode 0600 "${work}/base-go.sum" "${name}/home/review/base/go.sum" >"${work}/stage.log" 2>&1 || stage_fail push-base
  bounded incus config device add "${name}" net0 nic network=incusbr0 name=eth0 >"${work}/stage.log" 2>&1 || stage_fail net-add
  # shellcheck disable=SC2016 # the $ belongs to the shell in the box, not to this one
  box_root wait-net sh -c 'for i in $(seq 1 30); do getent hosts proxy.golang.org >/dev/null && exit 0; sleep 1; done; exit 1' \
    >"${work}/stage.log" 2>&1 || stage_fail wait-net
  box_user modules 'https://proxy.golang.org' /home/review/base /usr/local/go/bin/go mod download >"${work}/stage.log" 2>&1 || stage_fail modules
  bounded incus config device remove "${name}" net0 >"${work}/stage.log" 2>&1 || stage_fail net-remove
fi

# The network is measured cut three ways, before the code of the pull request
# is pushed: no NIC in the instance, no interface but lo in the box, and a
# connection that fails.
step "measuring that the network is cut"
devices="$(bounded incus config device show "${name}" 2>/dev/null)" || cannot "cannot read the devices of the box"
case "${devices}" in
  *"type: nic"*) cannot "the box still has a network device" ;;
esac
ifaces="$(box_root ifaces ls /sys/class/net 2>/dev/null)" || cannot "cannot list the interfaces of the box"
[[ ${ifaces} == lo ]] || cannot "the box has an interface besides lo"
probe='import socket,sys
for addr, fam in (("1.1.1.1", socket.AF_INET), ("2606:4700:4700::1111", socket.AF_INET6)):
    s = socket.socket(fam, socket.SOCK_STREAM)
    s.settimeout(4)
    try:
        s.connect((addr, 443))
    except OSError:
        continue
    sys.exit(1)
sys.exit(0)'
box_root netcheck python3 -c "${probe}" >"${work}/stage.log" 2>&1 || cannot "a connection out of the box succeeded: the network is not cut"

step "pushing the pull request's code (git archive of ${head_sha:0:12})"
bounded incus file push --uid 2000 --gid 2000 --mode 0600 "${work}/pr.tar" "${name}/home/review/pr.tar" >"${work}/stage.log" 2>&1 || stage_fail push-archive
box_user extract off /home/review tar -xf /home/review/pr.tar -C /home/review/pr >"${work}/stage.log" 2>&1 || stage_fail extract
# `go test` exits 1 when a test fails. So does anything else that fails with 1
# (a timeout that is not GNU's, an exec that did not start): prove first that
# the pinned go runs in the box, so that a 1 below is a verdict about the tests.
box_user gocheck off /home/review/pr /usr/local/go/bin/go version >"${work}/stage.log" 2>&1 || stage_fail gocheck
[[ "$(cat "${work}/stage.log")" == "go version go${want_go} linux/"* ]] || stage_fail gocheck

# ---- run the tests ---------------------------------------------------------------

step "go test ${pkgs[*]}"
rc=0
{
  {
    rc=0
    bounded incus exec "${name}" --force-noninteractive --user 2000 --group 2000 --cwd /home/review/pr \
      --env REVIEW_STAGE=test --env GOPROXY=off "${user_env[@]}" -- \
      /usr/local/go/bin/go test -count=1 "${pkgs[@]}" 2>&1 || rc=$?
    # After a signal the main shell has removed ${work} already: say nothing.
    { echo "${rc}" >"${work}/test.rc"; } 2>/dev/null || true
  } | head -c 4194304 >"${work}/test.raw"
} &
child=$!
wait "${child}" || true
child=0
# No rc file: the run was cut short (deadline) before the exit status was written.
{ read -r rc <"${work}/test.rc"; } 2>/dev/null || rc=125

printf -- '----- go test output of pull/%s (control characters escaped) -----\n' "${pr}"
python3 -I "${escaper}" <"${work}/test.raw"
printf -- '----- end of output -----\n'

summary="pull/${pr} at ${head_sha:0:12}, packages ${pkgs[*]}"
case "${rc}" in
  0)
    printf 'sandbox: tests green: %s\n' "${summary}"
    exit 0
    ;;
  1)
    printf 'sandbox: tests red: %s\n' "${summary}"
    exit 2
    ;;
  125) cannot "the deadline of ${timeout_s}s is exceeded while go test ran; the output above is partial" ;;
  *) cannot "go test did not finish normally (exit ${rc}); the output above is partial" ;;
esac
