#!/usr/bin/env bash
# Build the machine images feint boots, with an ssh daemon already in them.
#
#   tools/images/build.sh                 # every family
#   tools/images/build.sh ubuntu/24.04    # one of them
#
# Why this exists (#203). No image on the upstream server carries an ssh daemon:
# measured on images:ubuntu/24.04, ubuntu/24.04/cloud, debian/12/cloud and
# alpine/3.21/cloud, looked at twice each — as early as the container answers and
# again after `cloud-init status --wait` — and all four answered ABSENT with
# nothing listening on port 22.
#
# So feint installs it at first boot, in
# internal/core/cloudinit/templates/*.yaml.tmpl. That install is what needs
# outbound internet; outbound is what needs NAT; NAT is why every machine is put
# on a managed bridge; and that bridge is the second, unpublished address a
# Scaleway server carries here and does not carry on the real cloud (#202).
#
# A real cloud image has the daemon in it. Scaleway does not apt-install openssh
# when a server boots. So this is not a workaround, it is the faithful shape, and
# #202 cannot close honestly without it.
#
# The recipe is deliberately `launch, install, publish` rather than distrobuilder:
# it needs nothing installed that Incus does not already provide, and it produces
# the image on the machine that will use it. distrobuilder buys reproducibility
# from a declarative manifest and costs a build dependency; that trade is open
# and recorded in #203, not settled here.
set -euo pipefail

ALIAS_PREFIX=feint
BUILDER=feint-image-builder

# family/version : upstream image, the package that carries sshd, the service
# name, and the package manager. Four families because
# internal/core/cloudinit/templates/ has four, and an image set narrower than the
# templates makes the templates lie.
targets() {
  cat <<'LIST'
ubuntu/24.04     images:ubuntu/24.04/cloud     openssh-server ssh   apt
ubuntu/22.04     images:ubuntu/22.04/cloud     openssh-server ssh   apt
debian/12        images:debian/12/cloud        openssh-server ssh   apt
alpine/3.21      images:alpine/3.21/cloud      openssh        sshd  apk
almalinux/9      images:almalinux/9/cloud      openssh-server sshd  dnf
LIST
}

log() { printf '  %s\n' "$*"; }

cleanup_builder() {
  incus delete --force "$BUILDER" >/dev/null 2>&1 || true
}

install_ssh() {
  local manager=$1 package=$2 service=$3
  case "$manager" in
    apt)
      incus exec "$BUILDER" -- sh -c 'DEBIAN_FRONTEND=noninteractive apt-get update -qq'
      incus exec "$BUILDER" -- sh -c \
        "DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends $package"
      incus exec "$BUILDER" -- systemctl enable "$service"
      incus exec "$BUILDER" -- sh -c 'apt-get clean && rm -rf /var/lib/apt/lists/*'
      ;;
    apk)
      incus exec "$BUILDER" -- apk add --no-cache "$package"
      incus exec "$BUILDER" -- rc-update add "$service" default
      ;;
    dnf)
      incus exec "$BUILDER" -- sh -c "dnf install -y -q $package"
      incus exec "$BUILDER" -- systemctl enable "$service"
      incus exec "$BUILDER" -- sh -c 'dnf clean all'
      ;;
    *) echo "unknown package manager $manager" >&2; return 1 ;;
  esac
}

# What must NOT travel in the image.
#
# Host keys above all: baked in, every machine this emulator ever boots would
# present the same fingerprint, and a user's known_hosts would accept any of them
# for any other. cloud-init regenerates them when they are absent, which is why
# they are removed here rather than left "to be overwritten".
#
# The machine id goes for the same reason one level down: DHCP leases and
# systemd identity derive from it.
generalise() {
  incus exec "$BUILDER" -- sh -c 'rm -f /etc/ssh/ssh_host_*'
  incus exec "$BUILDER" -- sh -c ': > /etc/machine-id'
  incus exec "$BUILDER" -- sh -c 'rm -f /var/lib/dbus/machine-id' || true
  # cloud-init must run again from scratch on the machines built from this
  # image, or its first boot here is the only first boot it ever has.
  incus exec "$BUILDER" -- sh -c 'cloud-init clean --logs --seed 2>/dev/null || true'
  incus exec "$BUILDER" -- sh -c 'rm -rf /var/log/* /tmp/* 2>/dev/null || true'
}

build_one() {
  local name=$1 source=$2 package=$3 service=$4 manager=$5
  local alias="$ALIAS_PREFIX/$name"

  echo "== $alias  (from $source)"
  cleanup_builder
  log "launching the upstream image"
  incus launch "$source" "$BUILDER" >/dev/null

  log "waiting for it to answer"
  for _ in $(seq 1 60); do
    incus exec "$BUILDER" -- true >/dev/null 2>&1 && break
    sleep 2
  done
  incus exec "$BUILDER" -- cloud-init status --wait >/dev/null 2>&1 || true

  log "installing $package and enabling $service"
  install_ssh "$manager" "$package" "$service"

  log "removing what must not travel (host keys, machine id, cloud-init state)"
  generalise

  log "publishing"
  incus stop "$BUILDER" >/dev/null
  incus image delete "$alias" >/dev/null 2>&1 || true
  incus publish "$BUILDER" --alias "$alias" >/dev/null
  cleanup_builder

  local fingerprint
  fingerprint=$(incus image info "$alias" 2>/dev/null | awk '/^Fingerprint:/ {print $2}')
  log "fingerprint: $fingerprint"
  printf '%s %s\n' "$alias" "$fingerprint"
}

main() {
  local want=${1:-}
  local built=0
  # Read the whole list first, and never iterate a stream the loop body will
  # inherit as stdin: `incus launch` reads stdin as an InstancePut, so it ate the
  # remaining lines and failed with "cannot construct !!str ubuntu/... into
  # api.InstancePut". Measured, not anticipated.
  local -a rows=()
  local row
  while IFS= read -r row; do
    [ -n "$row" ] && rows+=("$row")
  done < <(targets)

  for row in "${rows[@]}"; do
    # shellcheck disable=SC2086 # the table is ours and its fields never contain spaces
    set -- $row
    local name=$1 source=$2 package=$3 service=$4 manager=$5
    if [ -n "$want" ] && [ "$want" != "$name" ]; then continue; fi
    build_one "$name" "$source" "$package" "$service" "$manager" </dev/null
    built=$((built + 1))
    echo
  done

  if [ "$built" = "0" ]; then
    echo "no target matched ${want:-<all>}" >&2
    exit 1
  fi

  echo "== images now available locally"
  incus image list "$ALIAS_PREFIX/" -f csv -c lfd 2>/dev/null | sed 's/^/   /'
}

trap cleanup_builder EXIT
main "$@"
