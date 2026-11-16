#!/usr/bin/env bash
# The decision of the "Sensitive paths" job (.github/workflows/sensitive-paths.yml,
# #988): a pull request from outside the project that changes a sensitive path
# waits for the maintainer, who reads it and then APPROVES it as a review. The
# approval counts only on the commit it was given on.
#
# It reads only the event payload ($GITHUB_EVENT_PATH) and the GitHub API. It
# never reads the pull request's code: the job checks out the BASE commit to get
# this script and tools/ci/sensitive-paths.txt, so a pull request that edits
# either one is judged by the version on main. A pull request that edits the
# workflow itself is not: GitHub runs the workflow file of the pull request's
# merge commit. What protects against that is written in the workflow's header,
# and it is not this script.
#
# Exit codes:
#   0  the pull request may merge as far as this check is concerned
#   1  it cannot be judged (a payload field missing, an API call failed, a file
#      list shorter than the pull request announces, a head that moved during
#      the run); the job is red, nothing is judged
#   2  refused: a sensitive path is changed and no approval covers the head commit
#
# Tested by tools/ci/sensitive_paths_test.go with a stubbed `gh`; every refusal
# has a mutation in tools/falsify/specs/an-outside-pull-request-waits-on-a-sensitive-path.json.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly list="${here}/sensitive-paths.txt"

cannot_judge() {
  echo "sensitive-paths: cannot judge this run: $*" >&2
  exit 1
}

refuse() {
  echo "sensitive-paths: refused: $*" >&2
  exit 2
}

# Third-party text (file paths, logins) is printed between stop-commands
# markers, so a path named `::error::...` is shown and not executed as a
# workflow command. The token is random so the text cannot end the block.
print_untrusted() {
  local token
  token="$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
  echo "::stop-commands::${token}"
  printf '  %q\n' "$@"
  echo "::${token}::"
}

[[ -n "${GITHUB_EVENT_PATH:-}" && -r "${GITHUB_EVENT_PATH}" ]] ||
  cannot_judge "no readable event payload (GITHUB_EVENT_PATH)"
[[ "${GITHUB_REPOSITORY:-}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] ||
  cannot_judge "GITHUB_REPOSITORY is not owner/name"

event() {
  jq -er "$1" "${GITHUB_EVENT_PATH}" 2>/dev/null
}

# Both triggers (pull_request and pull_request_review) carry these three. The
# review event's pull request object carries no changed_files, which is why
# the count is read from the API below and not from here.
number="$(event '.pull_request.number | numbers')" || cannot_judge "the payload has no pull request number"
association="$(event '.pull_request.author_association | strings | select(length > 0)')" ||
  cannot_judge "the payload has no author_association"
head_sha="$(event '.pull_request.head.sha | strings | select(test("^[0-9a-f]{40}$"))')" ||
  cannot_judge "the payload has no 40-character head SHA"

# The one definition of trust, for the author here and for a reviewer below:
# the owner, a member of the owning organisation, a collaborator. Every other
# value, including one GitHub might add later, is outside: an unrecognised
# association must refuse, not pass.
#
# Dependabot is NOT trusted, deliberately. Its pull requests change go.mod and
# the SHA pins of .github/workflows by nature, which is the supply chain this
# check exists for; a compromised upstream release reaches the repository
# through exactly that pull request. The cost is one approval per Dependabot
# pull request (16 opened between 2026-07-30 and 2026-11-16).
readonly TRUSTED='["OWNER", "MEMBER", "COLLABORATOR"]'

# Trust follows who can push to the head branch, not only who opened the pull
# request (cycle 2 of #988). A collaborator who opens a pull request from a
# branch of a stranger's fork lets the stranger push to it, and every
# `synchronize` would pass unread. So the author is trusted only when the head
# lives in this repository, or in the author's own fork. A head whose
# repository is gone (a deleted fork, `head.repo: null`) is judged as an
# outside pull request rather than refused as unjudgeable: nobody can vouch
# for who pushed it, and an approval on its head commit is still a way out,
# where "cannot judge" would hold it red with none.
trusted="$(jq -r --argjson t "${TRUSTED}" '
  .pull_request as $p
  | ($p.base.repo.full_name // null) as $base
  | ($p.head.repo.full_name // null) as $head
  | ($p.head.repo.owner.login // null) as $owner
  | ($p.user.login // null) as $author
  | if ($t | index($p.author_association)) == null then "outside"
    elif ($base | type) != "string" or ($author | type) != "string" then "unknown"
    elif $head == $base or $owner == $author then "trusted"
    else "foreign-head" end' "${GITHUB_EVENT_PATH}")"
case "${trusted}" in
  trusted)
    echo "sensitive-paths: author association ${association}, head in this repository or the author's fork: trusted, nothing to check"
    exit 0
    ;;
  foreign-head)
    echo "sensitive-paths: author association ${association}, but the head branch lives in a repository the author does not own (or no longer exists): judged as an outside pull request"
    ;;
  outside) ;;
  *) cannot_judge "the payload names no base repository or no author login" ;;
esac

patterns=()
while IFS= read -r line || [[ -n "${line}" ]]; do
  [[ -z "${line//[[:space:]]/}" || "${line}" =~ ^[[:space:]]*# ]] && continue
  patterns+=("${line}")
done <"${list}"
((${#patterns[@]} > 0)) || cannot_judge "${list} lists no pattern"

workdir="$(mktemp -d)"
trap 'rm -rf "${workdir}"' EXIT

gh api "repos/${GITHUB_REPOSITORY}/pulls/${number}" >"${workdir}/pull.json" ||
  cannot_judge "pull request ${number} could not be read"
changed="$(jq -er '.changed_files | numbers' "${workdir}/pull.json" 2>/dev/null)" ||
  cannot_judge "pull request ${number} has no changed_files count"
current_head="$(jq -er '.head.sha | strings' "${workdir}/pull.json" 2>/dev/null)" ||
  cannot_judge "pull request ${number} has no head SHA"
# The check run belongs to the event's head commit. If the pull request moved
# on since, the files and reviews below describe another commit; the push that
# moved it starts its own run, and that run judges.
[[ "${current_head}" == "${head_sha}" ]] ||
  cannot_judge "the head moved from ${head_sha} to ${current_head} during this run; the run of the new head judges it"

gh api --paginate --slurp "repos/${GITHUB_REPOSITORY}/pulls/${number}/files?per_page=100" \
  >"${workdir}/files.json" || cannot_judge "the file list of pull request ${number} could not be read"

# Every page an array, every entry a filename. A shape this script does not know
# is not an empty list.
listed="$(jq -er '
  if type == "array" and all(.[]; type == "array") and all(.[][]; (.filename | type) == "string")
  then map(length) | add // 0
  else error("unexpected shape") end' "${workdir}/files.json" 2>/dev/null)" ||
  cannot_judge "the file list of pull request ${number} has a shape this script does not know"

# The files endpoint stops at 3000 entries. A list shorter than the pull
# request announces is not the list, and a sensitive file could be the one
# missing.
((listed == changed)) ||
  cannot_judge "the API listed ${listed} changed files and the pull request announces ${changed}"

# NUL-separated, because a git path may contain a newline.
jq -j '.[][] | (.filename, (.previous_filename // empty)) | (., "\u0000")' \
  "${workdir}/files.json" >"${workdir}/paths" || cannot_judge "the file list could not be split"

sensitive=()
while IFS= read -r -d '' path; do
  for pattern in "${patterns[@]}"; do
    # shellcheck disable=SC2053 # the pattern is meant to glob
    if [[ "${path}" == ${pattern} ]]; then
      sensitive+=("${path}")
      break
    fi
  done
done <"${workdir}/paths"

if ((${#sensitive[@]} == 0)); then
  echo "sensitive-paths: none of the ${listed} changed files is a sensitive path"
  exit 0
fi

echo "sensitive-paths: pull request ${number} (author association ${association}) changes ${#sensitive[@]} sensitive path(s):"
print_untrusted "${sensitive[@]}"

# The approval is a review, because a review is bound to a commit by GitHub
# itself: its commit_id is the commit the reviewer approved, which nobody else
# writes. A label is not: the `labeled` event carries the head at the moment
# the label lands, so a push one second before it was covered by an approval
# given to the previous commit (the first version of this check, cycle 1 of
# #988). A commit date is not either: the committer writes it.
#
# Per trusted reviewer, the latest review that decides (approved, changes
# requested, or dismissed: a dismissed approval comes back with state
# DISMISSED). Ordered by id, which GitHub assigns in increasing order. A
# comment-only review decides nothing, as on GitHub's own pull request page.
# A reviewer outside the project counts for nothing, either way: an outsider
# can neither approve nor block.
gh api --paginate --slurp "repos/${GITHUB_REPOSITORY}/pulls/${number}/reviews?per_page=100" \
  >"${workdir}/reviews.json" || cannot_judge "the reviews of pull request ${number} could not be read"
# One line for the kind, then one login per line: a GitHub login holds letters,
# digits, hyphens and the `[bot]` suffix, never a newline.
jq -er --arg sha "${head_sha}" --argjson trusted "${TRUSTED}" '
  if type == "array" and all(.[]; type == "array")
     and all(.[][]; (.id | type) == "number" and (.state | type) == "string")
  then
    [.[][]
     | select(.state == "APPROVED" or .state == "CHANGES_REQUESTED" or .state == "DISMISSED")
     | select(.author_association as $a | $trusted | index($a) != null)
     # A deleted account reviews as `user: null`. It can neither approve nor
     # block, and it must not make the whole list unjudgeable either.
     | select(.user.login | type == "string")]
    | group_by(.user.login) | map(max_by(.id))
    | (map(select(.state == "CHANGES_REQUESTED") | .user.login)) as $blocking
    | (map(select(.state == "APPROVED" and .commit_id == $sha) | .user.login)) as $approved
    | (map(select(.state == "APPROVED" and .commit_id != $sha) | .user.login)) as $stale
    | if ($blocking | length) > 0 then "blocked", $blocking[]
      elif ($approved | length) > 0 then "approved", $approved[]
      elif ($stale | length) > 0 then "stale", $stale[]
      else "none" end
  else error("unexpected shape") end' "${workdir}/reviews.json" >"${workdir}/verdict" 2>/dev/null ||
  cannot_judge "the reviews of pull request ${number} have a shape this script does not know"
# A read loop rather than mapfile: macOS ships bash 3.2, which has no mapfile,
# and this script's tests run on the macOS runners too (measured: 63 failures,
# "mapfile: command not found", on PR #993 before this line).
verdict=()
while IFS= read -r line; do verdict+=("${line}"); done <"${workdir}/verdict"
kind="${verdict[0]:-}"
who=("${verdict[@]:1}")

case "${kind}" in
  approved)
    echo "sensitive-paths: approved on head commit ${head_sha} by:"
    print_untrusted "${who[@]}"
    exit 0
    ;;
  blocked)
    echo "sensitive-paths: the latest review of these reviewers requests changes:"
    print_untrusted "${who[@]}"
    refuse "a trusted reviewer requests changes; this check passes once that reviewer approves the head commit, or the review is dismissed and another trusted reviewer approves it."
    ;;
  stale)
    echo "sensitive-paths: these approvals were given on another commit than ${head_sha}:"
    print_untrusted "${who[@]}"
    refuse "commits were pushed after the approval (or just before it was submitted), and an approval covers the commit that was read. Maintainer: read the new commits, then approve again."
    ;;
  none)
    refuse "these paths need the maintainer's review. Maintainer: read the change, then approve the pull request (Files changed, Review changes, Approve); the approval re-runs this check on the commit you approved."
    ;;
  *)
    cannot_judge "the review verdict is not one this script knows"
    ;;
esac
