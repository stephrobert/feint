package machine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// safeName is what a machine or network name may contain.
//
// It used to be described as a guard against a future caller rather than a
// filter for today's input, on the grounds that names came from resource IDs and
// a fixed prefix. That stopped being true the day `PUT /_feint/state` was added:
// a snapshot carries `Resource.Runtime`, the backing machine name lives there,
// and the store restores it verbatim. An audit reproduced the consequence — a
// crafted snapshot followed by a DELETE made the driver run
// `incus delete --force production-database`.
//
// So every path that turns a name into a process argument checks it, including
// the read-only ones: the leading character rules out a name starting with `-`,
// which `incus` would read as a flag rather than an instance.
var safeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{1,127}$`)

// ownedNetwork reports whether a network name is one the emulator derives.
//
// safeName above answers "can this become a process argument safely", which is a
// question about form. This one answers "is this ours", which is the question
// every destructive path actually needs, and the two had been confused: an audit
// obtained `incus network delete incusbr0`, the host's default bridge, from a
// crafted snapshot followed by an ordinary DELETE. `incusbr0` is perfectly well
// formed, and that was the whole problem.
//
// The check is the prefix NetworkName writes, so it needs no runtime call and
// cannot fail for its own reasons. mustOwn, which reads the label the emulator
// puts on a network, is the stronger form and belongs on the paths that can
// afford a round trip.
func ownedNetwork(name string) bool {
	return strings.HasPrefix(name, NetworkPrefix+"-")
}

// MaxNetworkNameLen is the longest network name a runtime accepts, because the
// name becomes a host network interface and Linux caps those at 15 characters
// (IFNAMSIZ, minus the terminator). Measured, not assumed: Incus rejects a
// sixteenth character with "Network interface is too long".
//
// It matters because provider resource IDs are UUIDs, which never fit. Packs
// derive a name with NetworkName rather than truncating an ID and hoping.
const MaxNetworkNameLen = 15

// NetworkPrefix marks every network the emulator creates, whichever pack asked
// for it.
//
// Three characters because the whole name must fit MaxNetworkNameLen, and what
// is left after the prefix is the digest that makes it unique. It is shared
// rather than per-provider so that nothing outside a pack has to know the
// providers exist: the watcher and the sweep recognise the emulator's work by
// this marker, and a fourth pack needs no change in internal/core.
//
// Which provider owns a network is carried by its label, where it belongs.
const NetworkPrefix = "fnt"

// NetworkName derives a stable network name from a prefix and a resource ID.
//
// The result is the prefix, a dash and a digest of the ID, cut to fit
// MaxNetworkNameLen. A digest rather than a truncated ID: two Scaleway UUIDs
// share their first characters often enough that truncation would collide, and
// a collision here silently merges two emulated subnets into one bridge.
func NetworkName(prefix, id string) string {
	sum := sha256.Sum256([]byte(id))
	room := MaxNetworkNameLen - len(prefix) - 1
	if room < 1 {
		return prefix[:MaxNetworkNameLen]
	}
	digest := hex.EncodeToString(sum[:])
	if room > len(digest) {
		room = len(digest)
	}
	return prefix + "-" + digest[:room]
}

// notFoundPhrases are what a runtime says when an object is missing. Matching
// prose is a last resort, kept for the paths that have no object to ask about,
// and it is deliberately narrow: a broader match once read "Storage pool not
// found" as "the instance is gone", so Remove reported success and left the
// instance running.
var notFoundPhrases = []string{
	"instance not found",
	"network not found",
	"network acl not found",
	"network forward not found",
	"storage volume not found",
	"no such object",
	"no such container",
}

// isNotRunning reports the daemon's answer when a command needs a running
// instance and there is none. Attaching a NIC to a stopped machine is the
// ordinary Terraform order — attach, then power on — so the guest-side step
// failing there is expected, and reporting it as a failed attachment sent
// operators looking for a problem that was not one.
func isNotRunning(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "is not running")
}

// isAgentNotReady recognises a virtual machine whose guest agent has not
// started yet. It is distinct from isNotRunning on purpose: the instance is
// running, and it will answer in a few seconds. Reading the two as one is what
// made a VM report its attachment as failed and never take its address.
func isAgentNotReady(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "agent isn't currently running") ||
		strings.Contains(strings.ToLower(err.Error()), "agent is not currently running")
}

// isNotFound recognises "it does not exist" from an error message.
//
// Prefer gone(): the daemon answers 404 over its API, which is a fact rather
// than a sentence that changes between releases and locales. This remains for
// the calls that carry no object identity to ask about. Note the absence of a
// bare "not found": it is what made "Storage pool not found" read as "the
// instance is gone".
func isNotFound(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, phrase := range notFoundPhrases {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}

// runCLI executes a runtime CLI with explicit arguments. Arguments are passed as
// a slice and never interpreted by a shell, so a resource name can never become
// a command.
func runCLI(ctx context.Context, binary string, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, binary, args...) //nolint:gosec // fixed binary, arguments never shell-interpreted
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s %s: %s", binary, args[0], msg)
	}
	return stdout.Bytes(), nil
}
