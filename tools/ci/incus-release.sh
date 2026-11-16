#!/usr/bin/env bash
# Does the Zabbly stable channel still serve the Incus the runtime proof pins? (#979)
#
# Incus is the only runtime behind --vm, and Dependabot cannot see it: it comes
# from the Zabbly apt channel, which no Dependabot ecosystem reads. Until #979
# the runtime proof installed whatever the channel had published last, and the
# repository named 7.2 while the station ran 7.6. Now runtime-proof.yml pins one
# release (INCUS_VERSION, equal to machine.IncusTested by
# TestTheRuntimeProofInstallsTheTestedIncus), and this script is what notices
# when the channel moves away from it.
#
# It reads the channel's own Packages index, the file apt reads on the runner,
# rather than the GitHub releases of lxc/incus: the question is "what would the
# runtime proof install tonight", and a tag can exist days before its package,
# or never be packaged (lxc/incus has no v7.5.0 tag; the series shipped as
# 7.5.1).
#
# Usage: incus-release.sh [--apply] <workflow-file> <packages-file>
#
#   workflow-file  the workflow that pins the release (runtime-proof.yml); the
#                  pin is its INCUS_VERSION, and exactly one value of it
#   packages-file  the uncompressed Packages index of the suite the runtime
#                  proof's runner reads (noble, amd64)
#
# Exit codes follow the repository's contract: 0 ok, 1 error, 2 drift.
#   0  the channel's latest Incus is the pinned release; or --apply wrote the
#      issue that says it is not
#   1  this run cannot be judged: no pin, two pins, an index with no incus
#      package, a version in a shape this script does not know, or GitHub
#      refused the issue lookup. Never 0: "could not look" is not "nothing
#      moved"
#   2  the channel's latest Incus is not the pinned release (without --apply)
#
# With --apply it opens or comments ONE issue while the two differ, and closes
# it the first night they match. Without it, it prints the verdict and the body
# it would post, and writes nothing.
#
# The controls: tools/ci/incus_release_test.go drives every branch with a
# stubbed `gh`, and tools/falsify/specs/an-incus-release-is-noticed.json replays
# those tests with each decision neutralised.
set -euo pipefail

usage() {
  echo "usage: incus-release.sh [--apply] <workflow-file> <packages-file>" >&2
  exit 1
}

apply=false
if [ "${1:-}" = "--apply" ]; then
  apply=true
  shift
fi
[ "$#" -eq 2 ] || usage
workflow="$1"
packages="$2"

repo="${GITHUB_REPOSITORY:-stephrobert/feint}"
run_url="${FEINT_RUN_URL:-}"
label="drift"
title="Zabbly stable serves an Incus the runtime proof does not pin"

# A release as the channel spells the upstream part of a package version: two
# or three dotted numbers, nothing else. Anything else is refused, never
# compared: "7.7~rc1" or a renamed scheme must stop this run, not answer it.
release_re='^[0-9]+\.[0-9]+(\.[0-9]+)?$'

# THE PIN. Exactly one value, read from the workflow the proof runs. Zero means
# the variable was renamed or deleted, two means the jobs no longer agree with
# each other, and in both cases there is no single "pinned release" to compare.
if [ ! -r "${workflow}" ]; then
  echo "incus-release: cannot read ${workflow}" >&2
  exit 1
fi
pins="$(sed -nE "s/^[[:space:]]*INCUS_VERSION:[[:space:]]*['\"]?([^'\"[:space:]]*)['\"]?[[:space:]]*\$/\1/p" "${workflow}" | sort -u)"
if [ -z "${pins}" ]; then
  echo "incus-release: ${workflow} declares no INCUS_VERSION; there is no pin to compare" >&2
  exit 1
fi
if [ "$(printf '%s\n' "${pins}" | wc -l)" -ne 1 ]; then
  echo "incus-release: ${workflow} declares several INCUS_VERSION values:" >&2
  printf '%s\n' "${pins}" | sed 's/^/  /' >&2
  exit 1
fi
pinned="${pins}"
if ! [[ "${pinned}" =~ ${release_re} ]]; then
  echo "incus-release: the pin '${pinned}' is not a dotted release" >&2
  exit 1
fi

# THE CHANNEL. Every stanza whose Package is exactly `incus` (not incus-client,
# incus-base, incus-extra), its Version stripped of the epoch and of the Debian
# revision: `1:7.6-ubuntu24.04-202611072126` is 7.6.
if [ ! -r "${packages}" ]; then
  echo "incus-release: cannot read the Packages index ${packages}" >&2
  exit 1
fi
versions="$(awk '
  /^$/ { pkg = ""; next }
  /^Package:/ { pkg = $2; next }
  /^Version:/ { if (pkg == "incus") print $2 }
' "${packages}")"
if [ -z "${versions}" ]; then
  echo "incus-release: ${packages} lists no incus package; this run cannot be judged" >&2
  exit 1
fi

served=""
while IFS= read -r v; do
  upstream="${v#*:}"
  upstream="${upstream%%-*}"
  if ! [[ "${upstream}" =~ ${release_re} ]]; then
    echo "incus-release: the channel lists incus version '${v}', a shape this script does not know; refusing to compare" >&2
    exit 1
  fi
  served="${served}${upstream}"$'\n'
done <<<"${versions}"
served="$(printf '%s' "${served}" | sort -uV)"
latest="$(printf '%s\n' "${served}" | tail -n 1)"

pin_served=false
if printf '%s\n' "${served}" | grep -qxF "${pinned}"; then
  pin_served=true
fi

# One issue, found by label plus exact title, as drift-report.sh does: the
# label is shared with the upstream-surface issue, the title is not. A lookup
# that fails stops the run, never reads as "no issue": that reading would open
# a second one.
if ! listing="$(gh issue list --repo "${repo}" --state open --label "${label}" --json number,title)"; then
  echo "incus-release: could not list the open issues of ${repo}" >&2
  exit 1
fi
existing="$(jq -r --arg t "${title}" '[.[] | select(.title == $t)] | first | .number // empty' <<<"${listing}")"

echo "pinned ${pinned}; the channel serves: $(printf '%s\n' "${served}" | tr '\n' ' ')"
echo "latest on Zabbly stable: ${latest}"

body_file="$(mktemp)"
trap 'rm -f "${body_file}"' EXIT

if [ "${latest}" = "${pinned}" ]; then
  echo "verdict: the pin is the channel's latest release"
  {
    echo "Zabbly stable serves Incus ${pinned} again, the release runtime-proof.yml pins."
    if [ -n "${run_url}" ]; then
      printf '\nChecked by %s\n' "${run_url}"
    fi
  } > "${body_file}"
  if [ -n "${existing}" ]; then
    echo "plan: close issue #${existing}"
    if [ "${apply}" = true ]; then
      gh issue close "${existing}" --repo "${repo}" --comment "$(cat "${body_file}")"
    fi
  else
    echo "plan: nothing to do"
  fi
  exit 0
fi

# Which way the two differ. sort -V puts the older first; the pin is never
# equal to latest here.
if [ "$(printf '%s\n%s\n' "${pinned}" "${latest}" | sort -V | tail -n 1)" = "${latest}" ]; then
  direction="newer"
else
  direction="older"
fi

# GitHub tags the series with three parts (v7.6.0) and once skipped the .0
# (7.5 shipped as v7.5.1), so the tag link is a best guess and the two search
# links are the ones that always land.
tag="${latest}"
[[ "${tag}" =~ ^[0-9]+\.[0-9]+$ ]] && tag="${tag}.0"

{
  if [ "${direction}" = newer ]; then
    echo "Zabbly stable now serves **Incus ${latest}**; the runtime proof pins **${pinned}**."
  else
    echo "The runtime proof pins **Incus ${pinned}**, and the newest release Zabbly stable serves is **${latest}**: the pin names a release the channel does not have."
  fi
  echo
  if [ "${pin_served}" = true ]; then
    echo "The channel still serves ${pinned}, so the nightly runtime proof installs it tonight."
  else
    echo "The channel no longer serves ${pinned}, so the nightly runtime proof cannot install it: its \`Install Incus\` steps fail until the pin moves."
  fi
  echo
  echo "Release notes for ${latest}:"
  echo
  echo "- https://github.com/lxc/incus/releases/tag/v${tag}"
  echo "- https://github.com/lxc/incus/releases?q=v${latest}&expanded=true"
  echo "- https://discuss.linuxcontainers.org/search?q=Incus%20${latest}%20has%20been%20released"
  echo
  echo "To move the pin (#979):"
  echo
  echo "1. read the changelog from ${pinned} to ${latest} and sort each entry into docs/runtime-incus.md;"
  echo "2. set \`machine.IncusTested\` (internal/core/machine/incus_tested.go), \`INCUS_VERSION\` (.github/workflows/runtime-proof.yml) and \`feint_host_incus_version\` (tools/install/ansible/roles/feint_host/vars/debian.yml) to ${latest}, and re-read \`OVNMinimum\`, \`OVSMinimum\` and the system minimums beside it in the new release's notes;"
  echo "3. \`mise run docs:coverage\`, then \`go test ./tools/ci/ -run Incus\`;"
  echo "4. \`FEINT_VM=incus-ovn mise run conformance:leg -- runtime\` on a station running ${latest}."
  echo
  echo "This issue is updated while the two differ and closed the first night they match."
  if [ -n "${run_url}" ]; then
    printf '\nChecked by %s\n' "${run_url}"
  fi
} > "${body_file}"

# One comment per release seen, not one per night (#979): the open issue is
# commented only when the release it last named is not tonight's. Its last
# word (the latest comment, or the body when there is none) is read; a read
# that fails stops the run rather than guessing either way.
# TestIncusReleaseCommentsOncePerRelease fails without it.
already=false
if [ -n "${existing}" ]; then
  if ! last="$(gh issue view "${existing}" --repo "${repo}" --json body,comments --jq '([.body] + [.comments[].body]) | last')"; then
    echo "incus-release: could not read issue #${existing} to tell whether ${latest} was already reported" >&2
    exit 1
  fi
  if grep -qxF "Release notes for ${latest}:" <<<"${last}"; then
    already=true
  fi
fi

echo "verdict: the channel's latest (${latest}) is ${direction} than the pin (${pinned})"
if [ "${already}" = true ]; then
  echo "plan: issue #${existing} already names ${latest}; nothing to add"
elif [ -n "${existing}" ]; then
  echo "plan: comment on issue #${existing}"
else
  echo "plan: create the issue"
fi
echo "title: ${title}"
echo "--- body ---"
cat "${body_file}"
echo "--- end ---"

if [ "${apply}" != true ]; then
  exit 2
fi

if [ "${already}" = true ]; then
  :
elif [ -n "${existing}" ]; then
  gh issue comment "${existing}" --repo "${repo}" --body-file "${body_file}"
else
  # The label may not exist on a fresh repository; create it once, as
  # drift-report.sh does for the same label.
  gh label create "${label}" --repo "${repo}" --color FBCA04 \
    --description "The upstream API surface moved and needs triage" 2>/dev/null || true
  gh issue create --repo "${repo}" --title "${title}" --label "${label}" --body-file "${body_file}"
fi
