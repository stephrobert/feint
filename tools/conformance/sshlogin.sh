#!/usr/bin/env bash
# The login assertions the three ssh suites share (#501).
#
# Sourced, not executed. The caller defines fail() and ok(), the way guard.sh
# is consumed.
#
# Why this file exists. Each suite used to end its login step with one
# unguarded command substitution whose remote command ended in `grep -c`:
#
#   remote="$(ssh … 'hostname; id -un; grep -c feint-conformance ~/.ssh/authorized_keys')"
#
# Two defects in one line, and together they cost runtime-proof.yml five
# scheduled nights (2026-08-22 to 2026-08-26), silently:
#
#   - `grep -c` exits 1 when the count is zero, a perfectly ordinary case, and
#     the line had no `|| fail`: under `set -euo pipefail` the script died right
#     there without a word. "The key lost its comment", "the default account
#     moved" and "ssh dropped" were indistinguishable.
#   - the count was taken on the key COMMENT, which is not a property any
#     provider promises: Scaleway's real cloud keeps the algorithm and the
#     material and drops the comment (measured 2026-08-21 on a real fr-par
#     account; the pack emulates it in sshkeys.go, held by
#     TestASSHKeyIsPublishedWithoutItsComment, merged in b71d085 on
#     2026-08-21 at 17:10 — after that night's 02:51 scheduled run, which is
#     why 08-21 is green and every night after it is red).
#
# So the red was DESERVED, and merely unreadable: #363 made the emulator more
# faithful to upstream, and this assertion was the thing lagging behind. The
# five red nights before those (08-16 to 08-20) were a different cause
# entirely — missing images, #335, fixed by #338 on 08-20 — so the suite went
# red, was repaired, held one night, and went red again for an unrelated
# reason with nobody told. Two successive causes, one green night between
# them: that is the argument of #502, and of every message below.
#
# So the rules this file encodes, for all three suites at once — a block
# written three times is a fix that survives in two of them:
#
#   - every assertion carries its own message, and no grep ever decides an
#     exit status;
#   - the key assertion bears on the MATERIAL, never the comment.
#
# tools/conformance/sshlogin_test.go drives assert_login against a stub ssh,
# and tools/falsify/specs/ssh-login-names-what-it-lacks.json replays those
# tests with each guard neutralised.

# assert_login <user@host> <identity-file> <expected-user> <public-key-file>
#
# Logs in and asserts, one message each: the shell answers, the account is the
# one the provider provisions, authorized_keys is readable, and the registered
# key's material is in it.
assert_login() {
  local target="$1" identity="$2" want_user="$3" pubkey="$4"
  local remote_host remote_user authorized key_material

  ssh_login() {
    ssh -F /dev/null -i "$identity" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
      -o BatchMode=yes "$target" "$@"
  }

  remote_host="$(ssh_login 'hostname')" \
    || fail "hostname failed over ssh for $target: the session opens but the machine cannot say its name"
  remote_user="$(ssh_login 'id -un')" \
    || fail "id -un failed over ssh for $target: the session opens but the shell does not answer"
  [ "$remote_user" = "$want_user" ] \
    || fail "logged in as '$remote_user', not '$want_user': the provider's default login moved"
  authorized="$(ssh_login 'cat ~/.ssh/authorized_keys')" \
    || fail "authorized_keys is not readable in $want_user's home for $target: the login worked, so sshd honoured a key this file does not show"
  key_material="$(cut -d' ' -f2 <"$pubkey")"
  grep -qF "$key_material" <<<"$authorized" \
    || fail "the registered key's material is missing from authorized_keys for $target ($(wc -l <<<"$authorized") line(s) present): sshd honoured the key, so it lives somewhere this file is not"
  ok "logged in: $remote_host $remote_user, registered key present in authorized_keys"
}
