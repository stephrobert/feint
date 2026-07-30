#!/usr/bin/env bash
# The branch ruleset, applied and verified from the file that declares it.
#
# A JSON file describing a protection nobody applies is the defect this
# repository already found twice, in .poutine.yml and in .plumber.yaml: a
# configuration with no consumer reads as a control in force. So the file is
# never the claim — this script is, and it works in both directions:
#
#   apply   push .github/rulesets/main.json to the repository
#   check   fail (exit 2) when the live ruleset differs from the file
#
# Why a ruleset rather than classic branch protection, which is what most
# repositories reach for: rulesets are readable with the default GITHUB_TOKEN.
# Classic protection is not, so OpenSSF Scorecard's Branch-Protection check and
# plumber's branchMustBeProtected both need a privileged fine-grained PAT
# (Administration: read) stored as a secret. One fewer privileged credential to
# create, scope, store and rotate is worth more than the difference in features.
#
# Usage: tools/governance/ruleset.sh {apply|check|show} [owner/repo]
# Exit:  0 in agreement, 2 drifted, 1 could not tell.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.." || exit 1

ACTION="${1:-check}"
FILE=".github/rulesets/main.json"

REPO="${2:-}"
if [ -z "$REPO" ]; then
  REPO="$(git remote get-url origin 2>/dev/null | sed -E 's#(git@github.com:|https://github.com/)##; s#\.git$##')"
fi
if [ -z "$REPO" ]; then
  echo "no repository: pass owner/repo, or add an origin remote" >&2
  exit 1
fi
if [ ! -f "$FILE" ]; then
  echo "missing $FILE" >&2
  exit 1
fi

need() { command -v "$1" >/dev/null 2>&1 || { echo "$1 is required" >&2; exit 1; }; }
need gh
need jq

# What is compared, and why it is not equality.
#
# The API answers with more than was declared: ids, timestamps, _links, and
# default parameters the file never mentions (`required_reviewers: []` appeared
# on the very first apply). Demanding equality would make this gate red forever
# on fields nobody decided, which is how a gate becomes something people disable.
#
# So the question asked is containment: **everything the file declares is in
# force**. A field the repository adds and the file ignores is not drift; a field
# the file declares and the repository contradicts is.
# shellcheck disable=SC2016  # this is jq source, not shell: nothing here may expand
DECLARED_IN_FORCE='
def in_force($want; $got):
  if ($want | type) == "object" then
    ($got | type) == "object" and
    all($want | keys_unsorted[];
        . as $k | ($got | has($k)) and in_force($want[$k]; $got[$k]))
  elif ($want | type) == "array" then
    ($got | type) == "array" and
    ($want | length) == ($got | length) and
    all(range(0; $want | length); in_force($want[.]; $got[.]))
  else $want == $got
  end;
'

# Sorted so that the order of the required checks, which is not a decision, does
# not read as one.
normalise() {
  jq -S '
    .rules = ((.rules // []) | sort_by(.type) | map(
      if .parameters.required_status_checks
      then .parameters.required_status_checks |= (map({context}) | sort_by(.context))
      else . end))
    | .bypass_actors = ((.bypass_actors // []) | sort_by(.actor_id))
    | del(.id, .created_at, .updated_at, ._links, .source, .source_type,
          .node_id, .current_user_can_bypass, .links)
  '
}

live_ruleset() {
  local id
  id="$(gh api "repos/${REPO}/rulesets" --jq '.[] | select(.target == "branch") | .id' 2>/dev/null | head -1)"
  [ -z "$id" ] && return 1
  gh api "repos/${REPO}/rulesets/${id}" 2>/dev/null
}

case "$ACTION" in
  apply)
    if live=$(live_ruleset); then
      id="$(printf '%s' "$live" | jq -r '.id')"
      echo "updating ruleset ${id} on ${REPO}"
      gh api --method PUT "repos/${REPO}/rulesets/${id}" --input "$FILE" >/dev/null || exit 1
    else
      echo "creating the ruleset on ${REPO}"
      gh api --method POST "repos/${REPO}/rulesets" --input "$FILE" >/dev/null || exit 1
    fi
    echo "applied. Verify with: $0 check"
    ;;

  show)
    live_ruleset | jq '.' || { echo "no branch ruleset on ${REPO}" >&2; exit 2; }
    ;;

  check)
    if ! live=$(live_ruleset); then
      echo "no branch ruleset on ${REPO}: main is unprotected" >&2
      echo "apply the declared one: $0 apply" >&2
      exit 2
    fi
    want="$(normalise < "$FILE")"
    got="$(printf '%s' "$live" | normalise)"

    # Bypass actors are privileged information. A workflow's GITHUB_TOKEN reads
    # the ruleset and gets `"bypass_actors": []` whatever the ruleset actually
    # carries, so comparing them would fail this gate on every pull request for
    # a difference that is not there. Measured: the same call with an
    # Administration-scoped token returns the actor the file declares.
    #
    # Dropped from the comparison and *said*, never dropped silently: a gate
    # that quietly stops checking something is the failure this repository keeps
    # finding. Verifying that half needs a privileged token, and a privileged
    # token has no business in a pull_request run.
    if [ "$(printf '%s' "$got" | jq -c '.bypass_actors')" = "[]" ] &&
       [ "$(printf '%s' "$want" | jq -c '.bypass_actors')" != "[]" ]; then
      echo "note: bypass actors are not readable with this token; that half is unverified." >&2
      want="$(printf '%s' "$want" | jq 'del(.bypass_actors)')"
      got="$(printf '%s' "$got" | jq 'del(.bypass_actors)')"
    fi
    if jq -n --argjson want "$want" --argjson got "$got" \
         "${DECLARED_IN_FORCE} in_force(\$want; \$got)" | grep -q true; then
      echo "every rule declared in ${FILE} is in force on ${REPO}"
      exit 0
    fi
    echo "the ruleset on ${REPO} no longer matches ${FILE}:" >&2
    diff <(printf '%s\n' "$want") <(printf '%s\n' "$got") | head -40 >&2
    echo >&2
    echo "left is the file, right is what the repository enforces." >&2
    exit 2
    ;;

  *)
    echo "usage: $0 {apply|check|show} [owner/repo]" >&2
    exit 1
    ;;
esac
