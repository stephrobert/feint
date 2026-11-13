#!/usr/bin/env bash
# Move every Go pin in the repository to a new patch version, together.
#
# The pins are the one in mise.toml and every `go-version:` in
# .github/workflows/*.yml. They move together or not at all: a workflow left on the old
# patch is a toolchain the scan never judged, and internal/cli's
# TestTheGoWorkflowPinsTheVersionMiseDoes exists to refuse exactly that. The
# count is counted at runtime, never written down: a number in a comment goes
# stale the hour a workflow is added.
#
# Comment lines are prose, and prose quotes versions: the paragraph in go.yml
# explaining why the minor pin failed names the versions it warns about. Only
# non-comment lines move, which is the same rule that test reads by.
#
# Usage: vulncheck-bump.sh <new-version>
#
# Exit codes follow the repository's contract: 0 ok, 1 error.
#   0  every pin moved to the new version
#   1  the arguments are unusable, the new version is not newer than the pin,
#      or a pin did not move
#
# The controls on this file: tools/ci/vulncheck_bump_test.go drives it on a
# scratch tree.
set -euo pipefail

# newer_than A B -> true when A is strictly newer than B. Pure shell: BSD
# sort has no -V, and a lexical sort would call 1.9.0 newer than 1.10.0. A
# version that is not three numeric fields is not newer than anything.
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

# rewrite_in_place <file> <sed-expression...>: sed -i is not portable — BSD
# sed takes the next argument as the backup suffix, so -E would become a
# suffix and the expression a filename. The substitution writes to a
# temporary file and the original is overwritten from it, so the file keeps
# its permissions and no backup file is left behind; a failing sed aborts
# before the overwrite, because -e stops the script there.
rewrite_in_place() {
  local file="$1"
  shift
  local tmp
  tmp="$(mktemp)"
  sed -E "$@" "${file}" > "${tmp}"
  cat "${tmp}" > "${file}"
  rm -f "${tmp}"
}

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <new-version>" >&2
  exit 1
fi

new="$1"
if ! [[ "${new}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "vulncheck-bump: ${new} is not a patch version (want N.N.N)" >&2
  exit 1
fi

root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
cd "${root}"

pin="$(grep -E '^go[[:space:]]*=[[:space:]]*"[^"]+"' mise.toml 2>/dev/null \
  | head -n 1 | sed -E 's/^go[[:space:]]*=[[:space:]]*"([^"]+)".*/\1/')"
if [ -z "${pin}" ]; then
  echo "vulncheck-bump: mise.toml names no Go pin" >&2
  exit 1
fi

if [ "${new}" = "${pin}" ]; then
  echo "vulncheck-bump: the pin is already ${new}; nothing to move" >&2
  exit 1
fi
if ! newer_than "${new}" "${pin}"; then
  echo "vulncheck-bump: ${new} is not newer than the pinned ${pin}; refusing to bump backwards" >&2
  exit 1
fi

# How many workflow pins there are to move, counted the way the pin test
# counts them: non-comment lines naming a go-version. (grep -c prints 0 and
# exits 1 on no match, so `|| true` keeps the count without tripping -e.)
pins_before=0
for file in .github/workflows/*.yml; do
  pins_before=$((pins_before + $(grep -v -E '^[[:space:]]*#' "${file}" | grep -c -E "go-version: '[^']+'" || true)))
done
if [ "${pins_before}" = 0 ]; then
  echo "vulncheck-bump: no workflow pins a Go version; nothing to move" >&2
  exit 1
fi

rewrite_in_place mise.toml "s/^go = \"[^\"]+\"/go = \"${new}\"/"

moved=0
for file in .github/workflows/*.yml; do
  before="$(grep -v -E '^[[:space:]]*#' "${file}" | grep -c -E "go-version: '${new}'" || true)"
  rewrite_in_place "${file}" "/^[[:space:]]*#/!s/go-version: '[^']+'/go-version: '${new}'/g"
  after="$(grep -v -E '^[[:space:]]*#' "${file}" | grep -c -E "go-version: '${new}'" || true)"
  moved=$((moved + after - before))
done

# Every workflow pin must now name the new version, and mise.toml must agree
# with them: a half-moved tree is worse than an unmoved one, because the pin
# test would fail on it and the pull request would be born red.
remaining=0
for file in .github/workflows/*.yml; do
  total="$(grep -v -E '^[[:space:]]*#' "${file}" | grep -c -E "go-version: '[^']+'" || true)"
  named="$(grep -v -E '^[[:space:]]*#' "${file}" | grep -c -E "go-version: '${new}'" || true)"
  remaining=$((remaining + total - named))
done
if [ "${remaining}" != 0 ]; then
  echo "vulncheck-bump: ${remaining} workflow pin(s) still name a version other than ${new}" >&2
  exit 1
fi

now="$(grep -E '^go[[:space:]]*=[[:space:]]*"[^"]+"' mise.toml \
  | head -n 1 | sed -E 's/^go[[:space:]]*=[[:space:]]*"([^"]+)".*/\1/')"
if [ "${now}" != "${new}" ]; then
  echo "vulncheck-bump: mise.toml still pins ${now}" >&2
  exit 1
fi

echo "moved $((moved + 1)) pins to ${new} (mise.toml and ${pins_before} workflow pins)"
