# What a mock cannot do: a real machine, on a real subnet, carrying the exact
# address the API just published, removed when the emulator stops.
#
# Outscale on purpose. Nobody else emulates it, its users are the ones who cannot
# test against a paying account, and its API names things the way an
# infrastructure person expects: Net, Subnet, Vm.
#
# Regenerate with `mise run demo:network`. Needs Incus with OVN (ovn-central,
# ovn-host, Open vSwitch), which is why it is a separate tape from quickstart:
# that one runs anywhere, this one needs a host that can start machines.
#
# MP4 rather than GIF: social timelines flatten an animated GIF to a still
# frame, and 960x540 at 20 frames stays readable at the size they render.
#
# The ssh login is deliberately absent: it works, and
# tools/conformance/scaleway/ssh.sh proves it on every `mise run conformance:ssh`,
# but it needs a key pair, an IAM registration and a wait for the daemon, which
# triples the recording for one line of output. A demo is not a test suite.

# MP4 only. The GIF of this demo lands around 2.4 MB, over the 2048 KB the
# check-added-large-files hook allows, and shrinking it far enough costs the
# legibility the demo exists for. LinkedIn and every other timeline take the MP4;
# the README keeps quickstart.gif, which is small because it is shorter.
# Uncomment to produce one anyway, and keep it out of the repository.
# Output docs/assets/network.gif
Output docs/assets/network.mp4

Set Shell bash
Set FontSize 15
Set Width 960
Set Height 540
Set Padding 10
Set Framerate 20
Set TypingSpeed 30ms
Set Theme "Catppuccin Mocha"

Hide
Type "export REPO=$PWD && export PS1='$ '" Enter
Sleep 1s
Type "cd $REPO && go build -o feint ./cmd/feint" Enter
Sleep 12s
Type "./feint stop >/dev/null 2>&1; export PATH=$REPO:$PATH" Enter
Sleep 3s
Type 'osc() { tools/demo/osc.sh "$@"; }'
Enter
Sleep 1s
Type "clear" Enter
Sleep 2s
Show

Type "# A control plane is easy to fake. A network is not."
Enter
Sleep 1500ms

Type "feint start --vm incus-ovn --cleanup"
Enter
Sleep 6s

Type "curl -s localhost:4599/_feint/health | jq -c .capabilities"
Enter
Sleep 3s

Type "# isolation: true. Two VPCs really cannot reach each other."
Enter
Sleep 2s

Type "# The official Outscale CLI, against the emulator."
Enter
Sleep 1500ms

Type "osc CreateNet --IpRange 10.190.0.0/16 | jq -c .Net"
Enter
Sleep 4s

Type "NET=$(osc ReadNets | jq -r .Nets[0].NetId)"
Enter
Sleep 2s

Type "osc CreateSubnet --NetId $NET --IpRange 10.190.1.0/24 | jq -c .Subnet"
Enter
Sleep 4s

Type "SUB=$(osc ReadSubnets | jq -r .Subnets[0].SubnetId)"
Enter
Sleep 2s

Type "osc CreateVms --ImageId ami-fe1a7001 --VmType tinav6.c1r1p2 --SubnetId $SUB | jq -c .Vms[0]"
Enter
Sleep 5s

Type "# The API published an address. Ask the runtime, not the emulator."
Enter
Sleep 2s

Type "incus list feint-osc --format compact"
Enter
Sleep 5500ms

Type "# Same address. A container really holds it, on that subnet."
Enter
Sleep 3s

Type "feint stop"
Enter
Sleep 8s

Type "incus list feint-osc --format compact"
Enter
Sleep 4s

Type "# Gone. It cleans up what it created."
Enter
Sleep 3s

Hide
Type "cd $REPO" Enter
Show
