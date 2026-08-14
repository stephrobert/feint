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
# It reports rather than refuses when commitizen cannot be reached at all: a
# maintainer without the Python tooling must still be able to cut a release, and
# every other check here already had that property.
#
# But "cannot be reached" is not the same as "is not on the PATH", and treating
# them as one skipped this check on the station that cuts the releases — where
# the commitizen pre-commit hook runs on every single commit, through pre-commit's
# own isolated environment. So v0.3.0 was published with the number derived by
# hand rather than from the commits. uv is already a declared tool of this
# project (mise.toml), so uvx runs commitizen without installing anything, at the
# version .pre-commit-config.yaml pins — the same one the hook uses, because two
# versions of the same tool disagreeing about an increment is a defect nobody
# would think to look for.
cz_version="$(sed -n '/commitizen-tools\/commitizen/,/rev:/s/.*rev: *v\{0,1\}\([0-9.]*\).*/\1/p' \
  .pre-commit-config.yaml | head -1)"
cz_cmd=""
if command -v cz >/dev/null 2>&1; then
  cz_cmd="cz"
elif command -v uvx >/dev/null 2>&1 && [ -n "$cz_version" ]; then
  cz_cmd="uvx --from commitizen==$cz_version cz"
fi

if [ -n "$cz_cmd" ]; then
  # --yes so it never prompts, --dry-run so it writes nothing at all.
  implied="$($cz_cmd bump --dry-run --yes 2>/dev/null | grep -oE 'v?[0-9]+\.[0-9]+\.[0-9]+' | tail -1)"
  if [ -z "$implied" ]; then
    # No bump to compute: no conventional commit since the last tag, which is
    # the ordinary case for the first release.
    ok "no version increment to derive from the commits"
  elif [ "v${implied#v}" = "$VERSION" ]; then
    ok "$VERSION is what the commits since the last tag imply"
  else
    ko "the commits imply v${implied#v}, not $VERSION" \
       "read: $cz_cmd bump --dry-run. Tag what the commits say, or say why in the CHANGELOG"
  fi
else
  ok "${DIM}skipped: neither cz nor uvx is available, so the implied version was not derived${OFF}"
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

# --- the verification recipe the install page prints -------------------------

# Run against the PREVIOUS release, with the exact identity docs/install.md
# publishes, extracted from the page rather than retyped.
#
# The point is not to check cosign works. It is that the recipe a reader copies
# is the one that matches what the release workflow actually signs: the two
# drifted silently before #129, where the published identity accepted any
# workflow of this repository — a claim about who owns the repo, not about what
# built the file. A recipe nobody has run against a real artefact is a comment.
#
# The previous release, because this one does not exist yet. If it verifies, the
# identity is right for the workflow that will sign the next one too, since the
# same workflow signs both.
recipe_identity="$(grep -o "\-\-certificate-identity-regexp '[^']*'" docs/install.md | head -1 | sed "s/.*'\(.*\)'/\1/")"
if [ -z "$recipe_identity" ]; then
  ko "docs/install.md publishes no cosign identity" "the generated install block changed shape; check internal/cli/docs_release.go"
elif ! command -v cosign >/dev/null 2>&1; then
  ok "cosign absent, verification recipe not exercised (CI runs it)"
else
  # The slug comes from the remote rather than a constant: a fork running this
  # verifies its own releases, and a constant would send it to somebody else's.
  slug="$(git remote get-url origin 2>/dev/null | sed -E 's#(git@|https://)github\.com[:/]##; s#\.git$##')"
  previous="$(git tag --list 'v*' --sort=-v:refname | grep -v "^${VERSION}$" | head -1)"
  workdir="$(mktemp -d)"
  if [ -n "$previous" ] && gh release download "$previous" --repo "$slug" \
       --pattern 'checksums.txt*' --dir "$workdir" >/dev/null 2>&1; then
    if (cd "$workdir" && cosign verify-blob --bundle checksums.txt.cosign.bundle \
          --certificate-identity-regexp "$recipe_identity" \
          --certificate-oidc-issuer https://token.actions.githubusercontent.com \
          checksums.txt) >/dev/null 2>&1; then
      # And it has to be able to fail: an identity that accepts everything
      # verifies everything, which is the width #129 closed.
      wrong="${recipe_identity/release/drift}"
      if (cd "$workdir" && cosign verify-blob --bundle checksums.txt.cosign.bundle \
            --certificate-identity-regexp "$wrong" \
            --certificate-oidc-issuer https://token.actions.githubusercontent.com \
            checksums.txt) >/dev/null 2>&1; then
        ko "the published identity accepts another workflow of this repository" \
           "anchor it on .github/workflows/release.yml@refs/tags/v (#129)"
      else
        ok "the published verification recipe verifies $previous, and refuses another workflow"
      fi
    else
      ko "the recipe in docs/install.md does not verify $previous" \
         "the page and the release workflow disagree about who signs"
    fi
  else
    ok "no previous release to verify the recipe against"
  fi
  rm -rf "$workdir"
fi

# --- the container verification recipe ---------------------------------------

# Same discipline as the blob recipe above, one artefact later: the image
# reference and the identity are extracted from the page rather than retyped,
# the previous release is what exists to verify, and the identity must also
# refuse another workflow. A previous release that has no image is reported and
# tolerated rather than failed — the image ships from the first tag cut after
# the Dockerfile landed — because release.yml runs this same recipe against the
# image it has just pushed, where "no image" is impossible.
image_ref="$(grep -o 'cosign verify ghcr\.io/[^ ]*' docs/install.md | head -1 | sed 's/^cosign verify //')"
if [ -z "$image_ref" ]; then
  ko "docs/install.md publishes no container image reference" \
     "the generated container block changed shape; check internal/cli/docs_container.go"
elif ! command -v cosign >/dev/null 2>&1; then
  ok "cosign absent, container recipe not exercised (CI runs it)"
elif [ -z "$recipe_identity" ]; then
  : # already reported: the page publishes no identity at all
else
  previous="$(git tag --list 'v*' --sort=-v:refname | grep -v "^${VERSION}$" | head -1)"
  if [ -z "$previous" ]; then
    ok "no previous release to verify the container recipe against"
  else
    prev_ref="${image_ref%:*}:${previous}"
    # triangulate resolves the manifest without verifying anything: it is the
    # cheapest way to tell "no image at this tag" from "an image that fails
    # verification", and only the second is about this repository.
    if ! cosign triangulate "$prev_ref" >/dev/null 2>&1; then
      ok "no image published at $prev_ref; release.yml verifies the one this release pushes"
    elif cosign verify "$prev_ref" \
           --certificate-identity-regexp "$recipe_identity" \
           --certificate-oidc-issuer https://token.actions.githubusercontent.com >/dev/null 2>&1; then
      # And it has to be able to fail, like the blob recipe above (#129).
      wrong="${recipe_identity/release/drift}"
      if cosign verify "$prev_ref" \
           --certificate-identity-regexp "$wrong" \
           --certificate-oidc-issuer https://token.actions.githubusercontent.com >/dev/null 2>&1; then
        ko "the image identity accepts another workflow of this repository" \
           "anchor it on .github/workflows/release.yml@refs/tags/v (#129)"
      else
        ok "the container recipe verifies $prev_ref, and refuses another workflow"
      fi
    else
      ko "the recipe in docs/install.md does not verify $prev_ref" \
         "the page and the release workflow disagree about who signs the image"
    fi
  fi
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
# `image` is in the list because the release publishes one: a tag whose image
# job never ran would push a container nobody has driven a client against.
conformance_jobs='^(scw-cli|terraform|opentofu|oapi-cli|exo-cli|probe|image)$'

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
