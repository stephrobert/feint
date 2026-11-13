#!/usr/bin/env bash
# Ask govulncheck whether an advisory touches the pinned Go, and say so in a
# way a workflow can act on.
#
# The question is the one #865 asks on a schedule: an advisory for the pinned
# toolchain is a pin that has to move, and the move is a pull request. Three
# answers, never two:
#
#   clean        no advisory touches the pinned Go
#   advisory     an advisory touches it; the fixed version is the pin's next value
#   inconclusive the scan could not conclude, and nothing below may be read as
#                either of the first two
#
# The verdict is read from the findings, never from the exit code. govulncheck
# -json exits 0 whether or not it found anything (measured on 2026-11-13: the
# same run that reports GO-2026-6859 exits 0, and the clean one exits 0 too;
# only the human-readable form exits 3 on a finding). A caller that trusted the
# exit code would report every advisory night clean.
#
# Only the standard library is judged here. A finding in a required module is
# a dependency's vulnerability, not a pin's, and osv-scanner.yml already watches
# those weekly; this script counts them in its conclusion so they are not
# silently dropped, and does not let them open a pin-bump pull request they
# cannot fix.
#
# Exit codes follow the repository's contract: 0 ok, 1 error, 2 drift.
#   0  clean
#   1  the scan could not conclude (govulncheck errored, its output did not
#      parse, or it ran under a toolchain other than the pinned one, so the
#      measurement is not of the pin)
#   2  an advisory touches the pinned Go
#
# Usage: vulncheck-scan.sh
#
#   GOVULNCHECK_BIN  the scanner to run (default: govulncheck from PATH)
#
# Writes verdict, advisory, found, fixed, db and conclusion to $GITHUB_OUTPUT
# when it is set, and prints the conclusion to stdout either way: a silent
# schedule is indistinguishable from a dead one (#705), and the conclusion is
# what proves this night ran.
#
# The controls on this file: tools/ci/vulncheck_scan_test.go drives it with a
# stubbed govulncheck and canned -json output.
set -euo pipefail

bin="${GOVULNCHECK_BIN:-govulncheck}"
db=""

root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
cd "${root}"

conclusion() {
  if [ -n "${GITHUB_OUTPUT:-}" ]; then
    {
      echo "verdict=${1}"
      echo "advisory=${2}"
      echo "found=${3}"
      echo "fixed=${4}"
      echo "db=${db}"
      echo "conclusion=${5}"
    } >>"${GITHUB_OUTPUT}"
  fi
  echo "${5}"
}

pin="$(grep -E '^go[[:space:]]*=[[:space:]]*"[^"]+"' mise.toml 2>/dev/null \
  | head -n 1 | sed -E 's/^go[[:space:]]*=[[:space:]]*"([^"]+)".*/\1/' || true)"
if [ -z "${pin}" ]; then
  conclusion inconclusive "" "" "" "could not conclude: mise.toml names no Go pin" >&2
  exit 1
fi

# A version is newer than another when it compares greater, field by field,
# in pure shell. There is no sort -V here on purpose: BSD sort does not have
# it, and a lexical sort would call 1.9.0 newer than 1.10.0. A version that is
# not three numeric fields is not newer than anything, so a malformed
# fixed_version cannot become the pin's next value.
newer_than() {
  [[ "$1" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || return 1
  [[ "$2" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || return 1
  local a1 a2 a3 b1 b2 b3
  IFS=. read -r a1 a2 a3 <<< "$1"
  IFS=. read -r b1 b2 b3 <<< "$2"
  [ "${a1}" -ne "${b1}" ] && { [ "${a1}" -gt "${b1}" ]; return; }
  [ "${a2}" -ne "${b2}" ] && { [ "${a2}" -gt "${b2}" ]; return; }
  [ "${a3}" -gt "${b3}" ]
}

out="$(mktemp)"
err="$(mktemp)"
trap 'rm -f "${out}" "${err}"' EXIT

status=0
"${bin}" -json ./... >"${out}" 2>"${err}" || status=$?
if [ "${status}" != 0 ] && [ "${status}" != 3 ]; then
  tail -n 5 "${err}" >&2 || true
  conclusion inconclusive "" "" "" "could not conclude: govulncheck exited ${status}" >&2
  exit 1
fi

# An empty output is not a clean scan: -json always prints at least its
# config object, so nothing at all means the scanner died quietly.
if [ ! -s "${out}" ]; then
  conclusion inconclusive "" "" "" "could not conclude: govulncheck -json printed nothing" >&2
  exit 1
fi

if ! db="$(jq -r -s '[.[] | select(.config != null) | .config.db_last_modified] | .[0] // empty' "${out}" 2>/dev/null)" \
  || ! findings="$(jq -c -s '[.[] | select(.finding != null)]' "${out}" 2>/dev/null)"; then
  conclusion inconclusive "" "" "" "could not conclude: govulncheck -json output did not parse" >&2
  exit 1
fi

stdlib="$(printf '%s' "${findings}" | jq -c '[.[] | select(((.finding.trace // []) | map(select(.module == "stdlib"))) | length > 0)]')"
other="$(printf '%s' "${findings}" | jq -r '[.[] | select(((.finding.trace // []) | map(select(.module == "stdlib"))) | length == 0)] | length')"

if [ "$(printf '%s' "${stdlib}" | jq -r 'length')" = 0 ]; then
  line="clean: govulncheck found no advisory touching the pinned Go ${pin}"
  [ "${other}" != 0 ] && line="${line}; ${other} finding(s) in required modules are not this mechanism's to fix (osv-scanner.yml watches those)"
  [ -n "${db}" ] && line="${line} (db ${db})"
  conclusion clean "" "${pin}" "" "${line}"
  exit 0
fi

# The trace names the toolchain the finding was measured under. Anything
# other than the pin means the scan measured a different toolchain, and a
# verdict about the pin drawn from another toolchain is the silently-wrong
# kind: the pin test would still be green, and the pull request would move a
# pin the scan never judged.
found="$(printf '%s' "${stdlib}" | jq -r '[.[] | (.finding.trace // []) | map(select(.module == "stdlib")) | .[0].version] | unique | .[]' | sed 's/^v//')"
if [ "$(printf '%s\n' "${found}" | sort -u | wc -l)" != 1 ] || [ "${found}" != "${pin}" ]; then
  conclusion inconclusive "" "" "" "could not conclude: the scan ran under Go ${found}, not the pinned ${pin}" >&2
  exit 1
fi

# The fixed version is the pin's next value, and it has to be newer than the
# pin: a finding whose fixed version is the pin or older contradicts the
# finding itself, and bumping backwards is not a correction. The maximum is
# taken by comparing, not by sorting: BSD sort has no -V, and a lexical sort
# would call 1.9.0 newer than 1.10.0. mapfile is not used for the same reason:
# it arrived in bash 4, and macOS ships bash 3.2.
fixed=""
while IFS= read -r f; do
  if ! newer_than "${f}" "${pin}"; then
    conclusion inconclusive "" "" "" "could not conclude: a finding is fixed in ${f}, which is not newer than the pinned ${pin}" >&2
    exit 1
  fi
  if [ -z "${fixed}" ] || newer_than "${f}" "${fixed}"; then
    fixed="${f}"
  fi
done < <(printf '%s' "${stdlib}" | jq -r '[.[] | .finding.fixed_version] | unique | .[]' | sed 's/^v//')
if [ -z "${fixed}" ]; then
  conclusion inconclusive "" "" "" "could not conclude: no fixed version arrived for an advisory touching the pin" >&2
  exit 1
fi
advisory="$(printf '%s' "${stdlib}" | jq -r '[.[] | .finding.osv] | unique | sort | join(",")')"

line="advisory ${advisory} touches the pinned Go ${found}, fixed in ${fixed}"
[ "${other}" != 0 ] && line="${line}; ${other} finding(s) in required modules are not this mechanism's to fix (osv-scanner.yml watches those)"
[ -n "${db}" ] && line="${line} (db ${db})"
conclusion advisory "${advisory}" "${found}" "${fixed}" "${line}"
exit 2
