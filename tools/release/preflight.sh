#!/usr/bin/env bash
# Everything that must be true before a tag exists.
#
# The reason this runs locally rather than in the workflow is timing. The
# workflow's guards only speak *after* the tag is pushed, and a pushed tag has to
# be deleted on both sides; a published release with a wrong binary cannot be
# replayed at all. So the checks that can be made offline are made here, before
# anything is irreversible.
#
# It reports every verdict rather than stopping at the first, because stopping
# first means running it five times in a row.
#
# Usage: tools/release/preflight.sh vX.Y.Z
# Exit:  0 ready to tag, 1 something is not.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.." || exit 1

VERSION="${1:-}"
GREEN=$'\033[32m'; RED=$'\033[31m'; DIM=$'\033[2m'; OFF=$'\033[0m'
[ -t 1 ] || { GREEN=""; RED=""; DIM=""; OFF=""; }

failures=0
ok()   { printf "  %s✔%s %s\n" "$GREEN" "$OFF" "$1"; }
ko()   { printf "  %s✘%s %s\n    %s%s%s\n" "$RED" "$OFF" "$1" "$DIM" "$2" "$OFF"; failures=$((failures + 1)); }

if [ -z "$VERSION" ]; then
  echo "usage: tools/release/preflight.sh vX.Y.Z" >&2
  exit 1
fi
case "$VERSION" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "FAIL: $VERSION is not a vX.Y.Z tag" >&2; exit 1 ;;
esac

echo "preflight for $VERSION"

# --- the repository is in a state worth tagging -----------------------------

if [ -z "$(git status --porcelain)" ]; then
  ok "the working tree is clean"
else
  ko "the working tree is dirty" "the tag would name a commit that is not what you tested"
fi

branch="$(git rev-parse --abbrev-ref HEAD)"
if [ "$branch" = "main" ]; then
  ok "on main"
else
  ko "on $branch, not main" "tag the branch releases are cut from, or say why not"
fi

if git rev-parse "$VERSION" >/dev/null 2>&1; then
  ko "$VERSION already exists locally" "git tag -d $VERSION, if it was a mistake"
else
  ok "$VERSION is free locally"
fi

if git remote | grep -q .; then
  if git ls-remote --tags origin "$VERSION" 2>/dev/null | grep -q .; then
    ko "$VERSION already exists on origin" "a published tag must never be moved"
  else
    ok "$VERSION is free on origin"
  fi
else
  ko "no git remote" "nothing can be released from a repository with no origin"
fi

# --- the checks the project already owns ------------------------------------

if mise run check >/dev/null 2>&1; then
  ok "mise run check"
else
  ko "mise run check fails" "gofmt, vet, lint or the tests"
fi

# Exit 2 is drift, 1 is an error, and neither may ship: a release published with
# untriaged upstream operations advertises a coverage figure that is not true.
mise run drift:check >/dev/null 2>&1
case $? in
  0) ok "no upstream drift since the baseline" ;;
  2) ko "upstream drift is untriaged" "mise run drift:update, then implement or decline" ;;
  *) ko "drift:check could not run" "the SDK checkouts may be missing: mise run upstream:sync" ;;
esac

./feint docs --check >/dev/null 2>&1
case $? in
  0) ok "the generated documentation matches the artefacts" ;;
  2) ko "the generated sections are stale" "mise run docs:coverage" ;;
  *) ko "docs --check could not run" "build the binary: mise run build" ;;
esac

# --- the number the commits imply -------------------------------------------
#
# The version is not a preference. Conventional Commits say what the increment
# is: a fix moves the patch, a feat the minor, a `!` marks the break, and
# major_version_zero keeps a break inside 0.x. So the tag being asked for is
# checkable against the commits since the last one, and a mismatch is worth a
# sentence before it is worth a release.
#
# It reports rather than refuses when commitizen is absent: a maintainer without
# the Python tooling must still be able to cut a release, and every other check
# here already had that property.
if command -v cz >/dev/null 2>&1; then
  # --yes so it never prompts, --dry-run so it writes nothing at all.
  implied="$(cz bump --dry-run --yes 2>/dev/null | grep -oE 'v?[0-9]+\.[0-9]+\.[0-9]+' | tail -1)"
  if [ -z "$implied" ]; then
    # No bump to compute: no conventional commit since the last tag, which is
    # the ordinary case for the first release.
    ok "no version increment to derive from the commits"
  elif [ "v${implied#v}" = "$VERSION" ]; then
    ok "$VERSION is what the commits since the last tag imply"
  else
    ko "the commits imply v${implied#v}, not $VERSION" \
       "read: cz bump --dry-run. Tag what the commits say, or say why in the CHANGELOG"
  fi
else
  ok "${DIM}skipped: commitizen is not installed, so the implied version was not derived${OFF}"
fi

# --- what a reader of the release will look for -----------------------------

if grep -q "^## \[${VERSION#v}\]" CHANGELOG.md 2>/dev/null; then
  ok "CHANGELOG.md has a section for ${VERSION#v}"
else
  ko "CHANGELOG.md has no ## [${VERSION#v}] section" \
     "move the Unreleased entries under it; the release body is read from there"
fi

# The coverage artefacts are what the README's tables and the drift gate both
# read. Stale ones make a release advertise a surface it does not serve.
if [ -z "$(git status --porcelain coverage/ contracts/)" ]; then
  ok "coverage/ and contracts/ are committed as they stand"
else
  ko "coverage/ or contracts/ has uncommitted changes" "commit them, or regenerate and commit"
fi

# --- the conformance claim --------------------------------------------------

# Not run here: it needs the four clients installed, and a release cut from a
# machine missing one of them would silently skip the very proof this project
# rests on. CI runs it on every push; this checks that it did.
# A check-run carries the name of the JOB, not of the workflow, and the
# conformance jobs are named after the client they drive: scw-cli, terraform,
# opentofu, oapi-cli, exo-cli, probe. Matching on "conformance" found nothing on
# a green run and refused every tag with "no conformance run found" — a gate that
# blocks the release it was written to authorise.
conformance_jobs='^(scw-cli|terraform|opentofu|oapi-cli|exo-cli|probe)$'

# And the query is written out rather than parameterised, because the previous
# attempt to parameterise it is what broke this gate a second time.
#
# `gh api --jq --arg jobs "$re" '<expr>'` looks like the jq CLI and is not: gh has
# no --arg, so --jq took the string "--arg" as its expression, the call failed,
# `2>/dev/null` swallowed the error, and an empty result was read as "no run
# found". The gate then refused a tag on a commit whose six conformance jobs were
# green — the exact failure the comment above says it fixed, one layer deeper.
#
# The lesson is the reason for the third branch below: a failed measurement and
# an absent result are different answers, and only one of them is about the
# repository.
if git remote | grep -q . && command -v gh >/dev/null 2>&1; then
  sha="$(git rev-parse HEAD)"
  if runs="$(gh api "repos/{owner}/{repo}/commits/$sha/check-runs?per_page=100" \
             --jq '.check_runs[] | "\(.name)\t\(.conclusion)"' 2>/dev/null)"; then
    state="$(printf '%s\n' "$runs" \
             | awk -F'\t' -v re="$conformance_jobs" '$1 ~ re { print $2 }' \
             | sort -u | paste -sd, -)"
    case "$state" in
      success)   ok "conformance is green on this commit" ;;
      "")        ko "no conformance run found for this commit" "push it and wait for CI" ;;
      *)         ko "conformance is $state on this commit" "the emulator's whole claim is that these pass" ;;
    esac
  else
    ko "could not ask CI about this commit" "gh api failed: check authentication, not the commit"
  fi
else
  printf "  %s·%s conformance on CI not checked (no remote, or gh is not installed)\n" "$DIM" "$OFF"
fi

echo
if [ "$failures" -gt 0 ]; then
  printf "%s%d check(s) failed; nothing was tagged.%s\n" "$RED" "$failures" "$OFF" >&2
  exit 1
fi
cat <<EOF
${GREEN}ready.${OFF} Then, and only then:

  git tag -a $VERSION -m "$VERSION"
  git push origin $VERSION

Pushing the tag is what publishes. It cannot be undone quietly: a tag has to be
deleted on both sides, and a release that reached the world has been downloaded.
EOF
