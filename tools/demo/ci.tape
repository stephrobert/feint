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
# The last frame matters as much as the apply. What a mock cannot show is that
# nothing survives it: `docker ps` empty, and the port answering nothing.

Output docs/assets/ci.gif

Set Shell bash
Set FontSize 16
Set Width 1100
Set Height 620
Set Padding 12
Set TypingSpeed 35ms
Set Theme "Catppuccin Mocha"

Hide
# The image tag is read from the CHANGELOG rather than written here: a tape that
# names a version goes stale the day after a release, which is the defect the
# generated blocks in the README exist to avoid.
Type "export REPO=$PWD && export PS1='$ ' && export DEMO=$(mktemp -d)" Enter
Sleep 1s
Type "export TAG=$(grep -m1 -oE '^## \\[[0-9]+\\.[0-9]+\\.[0-9]+\\]' $REPO/CHANGELOG.md | tr -d '#[] ')" Enter
Sleep 1s
Type "cp $REPO/tools/demo/main.tf $DEMO/ && cd $DEMO" Enter
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
Sleep 1500ms

Type 'docker run -d --rm --name feint -p 4599:4599 ghcr.io/stephrobert/feint:v$TAG'
Enter
Sleep 6s

Type "curl -s localhost:4599/_feint/health | jq -c '{providers, machines}'"
Enter
Sleep 4s

Type "# The real Scaleway Terraform provider, pointed at it."
Enter
Sleep 1500ms

Type "terraform apply -auto-approve -input=false -no-color | tail -3"
Enter
Sleep 14s

Type "# What it actually drove, counted by the emulator itself."
Enter
Sleep 1500ms

Type "curl -s localhost:4599/_feint/conformance | jq -c '{driven: (.calls | length), violations: (.violations | length)}'"
Enter
Sleep 4s

Type "# And nothing survives it."
Enter
Sleep 1500ms

Type "docker stop feint >/dev/null && docker ps --filter name=feint --format '{{.Names}}' | wc -l"
Enter
Sleep 5s

Type "curl -s --max-time 2 localhost:4599/_feint/health || echo 'nothing answers on 4599'"
Enter
Sleep 4s
