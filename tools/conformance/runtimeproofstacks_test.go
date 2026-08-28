package conformance

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The stack gate is played by a scheduled job, which is the whole of #504.
//
// `conformance:functional` is the only thing in this repository that applies
// the example stacks against real machines, and nothing in CI ran it. It is
// what surfaced the isolation-detach race — an ACL deleted while the daemon
// resolved it at `incus start`, killing machines behind a green apply — a
// defect that cost three pull requests and was older than the whole range
// bisected. No leg played the stacks, and `mise run conformance` skips the
// stack suites without a runtime, so that class had no gate at all.
//
// Three properties, and the second is the one an ordinary "the workflow
// mentions the script" test would miss: the gate has to be a job of its own.
// Appended to the incus-ovn leg it would queue behind six suites that were red
// on four of the last seven scheduled nights, and a step that is never reached
// is a gate that never runs.
func TestTheStackGateIsPlayedByAScheduledJob(t *testing.T) {
	root := repoRoot(t)
	body := readFile(t, filepath.Join(root, ".github", "workflows", "runtime-proof.yml"))

	gate := "tools/conformance/functional.sh"
	if !strings.Contains(body, gate) {
		t.Fatalf("runtime-proof.yml never runs %s: the only gate that applies the example stacks "+
			"against real machines is played by nothing again (#504)", gate)
	}

	jobs := workflowJobs(t, body)
	// The reader proves it can find before it judges: without the split, every
	// containment question below would be asked of an empty string.
	for _, name := range []string{"runtime", "stacks", "streak", "report"} {
		if _, ok := jobs[name]; !ok {
			t.Fatalf("no `%s:` job found in runtime-proof.yml (found %v): the reader is the "+
				"suspect, not the workflow", name, sortedJobNames(jobs))
		}
	}

	if !strings.Contains(jobs["stacks"], gate) {
		t.Errorf("the `stacks` job does not run %s, so whatever it does it is not the stack gate", gate)
	}
	if strings.Contains(jobs["runtime"], gate) {
		t.Error("the stack gate is a step of the `runtime` job again: it then queues behind the " +
			"ssh suites, which were red on four of the last seven scheduled nights, and a step " +
			"that is never reached is a gate that never runs (#504)")
	}
	// Half an hour of passes does not fit that leg's budget either, and a job
	// that times out is a verdict nobody wrote.
	if !strings.Contains(jobs["stacks"], "timeout-minutes:") {
		t.Error("the `stacks` job declares no timeout, so a hung apply holds a runner until " +
			"GitHub's own six-hour ceiling")
	}
	// The mode is exported rather than left to the default, so the announcement
	// a reader meets in the log carries a provenance they can check against the
	// job's own name.
	if !strings.Contains(jobs["stacks"], "FEINT_VM=incus-ovn") {
		t.Error("the `stacks` job does not export FEINT_VM, so the gate announces `this gate's " +
			"default` for a mode this job's name claims: that is #574's shape, not its fix")
	}

	// A job outside the night's verdict is a job nobody reads. `streak` counts
	// the workflow's own conclusion and `report` opens the issue, so both wait
	// for this one.
	for _, name := range []string{"streak", "report"} {
		if !strings.Contains(jobs[name], "needs: [runtime, stacks]") {
			t.Errorf("the `%s` job does not wait for `stacks`, so a night whose stack gate went "+
				"red is summarised, and possibly closed, before anybody looked at it", name)
		}
	}
}

// The two jobs that build an Incus host build the same one (#504).
//
// The `stacks` job repeats the incus-ovn leg's setup deliberately: extracting
// it into a composite action while that leg is red would make any new red
// ambiguous between the gate and the refactor. Duplication is only defensible
// when something holds the copies together, and this is that thing — a pin that
// moves in one job and not the other fails `mise run check` rather than a night
// three weeks later.
func TestBothRuntimeJobsPinTheSameHost(t *testing.T) {
	root := repoRoot(t)
	jobs := workflowJobs(t, readFile(t, filepath.Join(root, ".github", "workflows", "runtime-proof.yml")))

	for _, pin := range []struct{ what, line string }{
		{"the Zabbly signing key", "ZABBLY_FPR: '4EFC590696CB15B87C73A3AD82CC8797C838DCFD'"},
		{"the Incus packages", "incus incus-client netcat-openbsd"},
		{"the OVN packages", "ovn-central ovn-host openvswitch-switch"},
		{"the northbound socket", "network.ovn.northbound_connection unix:/run/ovn/ovnnb_db.sock"},
		{"the runner's Docker FORWARD policy", "sudo iptables -P FORWARD ACCEPT"},
		{"the Terraform version", "TERRAFORM_VERSION: '1.13.3'"},
	} {
		for _, job := range []string{"runtime", "stacks"} {
			if !strings.Contains(jobs[job], pin.line) {
				t.Errorf("the `%s` job no longer pins %s (%q): the two jobs claim to build the "+
					"same host and one of them has drifted", job, pin.what, pin.line)
			}
		}
	}
}

// workflowJobs splits a workflow into its top-level jobs, keyed by id.
//
// String work rather than a YAML parser, because this module carries no
// dependencies: a job starts on a line indented by exactly two spaces under
// `jobs:` and ends where the next one starts.
func workflowJobs(t *testing.T, body string) map[string]string {
	t.Helper()
	_, after, found := strings.Cut(body, "\njobs:\n")
	if !found {
		t.Fatal("the workflow declares no `jobs:` block: the reader is the suspect")
	}
	header := regexp.MustCompile(`^ {2}([A-Za-z0-9_-]+):\s*$`)
	out := map[string]string{}
	current := ""
	var lines []string
	flush := func() {
		if current != "" {
			out[current] = strings.Join(lines, "\n")
		}
	}
	for _, line := range strings.Split(after, "\n") {
		if m := header.FindStringSubmatch(line); m != nil {
			flush()
			current, lines = m[1], nil
			continue
		}
		lines = append(lines, line)
	}
	flush()
	return out
}

func sortedJobNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
