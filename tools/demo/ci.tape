# The forty-five seconds a CI reader needs: a container, a real Terraform apply,
# and nothing left behind (#252).
#
# The other two tapes show the binary, which is the mode that fits a laptop. This
# one shows the door a pipeline actually uses — an OCI image in a `services:`
# block — because that is the reader who has to decide whether Feint fits their
# workflow, and the README showed them the wrong mode for months.
#
# Regenerate with `mise run demo:ci`. Everything here is executed for real: the
# image is pulled from the registry, the official Terraform provider applies
# against it, and the container is removed. A recording nobody can re-run is a
# claim, and the whole argument of this project is that a claim is not a proof.
#
# Prove it before recording it: `tools/demo/ci.sh` runs this exact sequence where
# a non-zero exit is visible, and `mise run demo:ci` runs it first. The first cut
# of this GIF recorded four errors in a row — an empty tag, a container that
# never started, a port nothing listened on, an apply against no catalogue — and
# was published without being watched. A recording cannot fail; a script can, so
# the script is what says the demo is true.
#
# The last frame matters as much as the apply. What a mock cannot show is that
# nothing survives it: `docker ps` empty, and the port answering nothing.

Output docs/assets/ci.gif

Set Shell bash
Set FontSize 16
Set Width 1100
Set Height 620
Set Padding 12
Set TypingSpeed 25ms
Set Theme "Catppuccin Mocha"

Hide
# The image tag is read from the CHANGELOG rather than written here: a tape that
# names a version goes stale the day after a release, which is the defect the
# generated blocks in the README exist to avoid.
Type "export REPO=$PWD && export PS1='$ ' && export DEMO=$(mktemp -d)" Enter
Sleep 1s
# One backslash, not two. A tape types its line into the shell verbatim, so `\\[`
# arrived as two characters, the grep matched nothing, and TAG was empty — which
# is how the first cut recorded `docker run … feint:v` and then three more errors
# caused by it. tools/demo/ci.sh runs this same line where a failure is visible.
Type "export TAG=$(grep -m1 -oE '^## \[[0-9]+\.[0-9]+\.[0-9]+\]' $REPO/CHANGELOG.md | tr -d '#[] ')" Enter
Sleep 1s
Type "cp $REPO/tools/demo/main.tf $DEMO/ && cd $DEMO" Enter
Sleep 1s
# The environment the official provider needs, out of frame: it is six exports of
# values that mean nothing, and watching them typed teaches nobody anything. The
# apply on screen is the real one, and it needs them — without them the recorded
# apply failed on a catalogue it never reached.
Type "source $REPO/tools/demo/env.sh" Enter
Sleep 1s
# Pulled and pre-initialised out of frame: a pull and a provider download take
# longer than anybody will watch, and neither is what this shows.
Type "docker pull ghcr.io/stephrobert/feint:v$TAG >/dev/null 2>&1" Enter
Sleep 30s
Type "terraform init -input=false >/dev/null 2>&1" Enter
Sleep 25s
Type "clear" Enter
Sleep 2s
Show

Type "# A cloud API in a container. No account, no credentials, no bill."
Enter
Sleep 1200ms

# The version is written out rather than expanded, and that is deliberate: vhs
# types the literal text, so `$TAG` reached the screen as `$TAG` — the command
# worked, and no viewer could copy the line. A hard-coded version in a recording
# is the kind of figure this repository refuses, so tools/demo/ci.sh fails when
# this line and the CHANGELOG disagree.
Type "docker run -d --rm --name feint -p 4599:4599 ghcr.io/stephrobert/feint:v0.10.0"
Enter
Sleep 4s

Type "curl -s localhost:4599/_feint/health | jq -c '{providers, machines}'"
Enter
Sleep 4s

Type "# The real Scaleway Terraform provider, pointed at it."
Enter
Sleep 1200ms

Type "terraform apply -auto-approve -input=false -no-color | tail -3"
Enter
# 14s, not less: the apply takes between ten and fourteen seconds against the
# published image, and at 10s the recording ended mid-apply — the four steps
# after this one were typed into a shell that had not come back, so the GIF
# stopped at the apply and the count and the teardown were never filmed. Found
# by looking at the frames, which is the only way this is ever found.
Sleep 14s

Type "# What it actually drove, counted by the emulator itself."
Enter
Sleep 1200ms

Type "curl -s localhost:4599/_feint/conformance | jq -c '{driven: (.calls | length), violations: (.violations | length)}'"
Enter
Sleep 4s

Type "# And nothing survives it."
Enter
Sleep 1200ms

Type "docker stop feint >/dev/null && docker ps --filter name=feint --format '{{.Names}}' | wc -l"
Enter
Sleep 4s

Type "curl -s --max-time 2 localhost:4599/_feint/health || echo 'nothing answers on 4599'"
Enter
Sleep 4s
