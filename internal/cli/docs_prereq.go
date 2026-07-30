package cli

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// What you need installed, and it is generated for the same reason the coverage
// tables are.
//
// The prerequisites table restated three measured values by hand: the Go
// version, the Incus floor and the Incus series every measurement was taken on.
// Two of the three have a source in this repository — `go.mod` and the constants
// the doctor checks against — and the page had a third copy of each. The audit of
// 2026-07-29 found the same class of defect three times over in the install
// documentation, including a floor stated as "Debian ships 6.0.x, too old" about
// a release that clears the floor exactly.
//
// A release is exactly when this goes wrong unnoticed: the numbers are still the
// ones somebody typed months earlier, and nothing on the page fails. So the
// section is spliced from the source, `feint docs --check` exits 2 when the page
// no longer matches, and the release preflight runs that check.

const (
	prereqStartMarker = "<!-- prereq:start -->"
	prereqEndMarker   = "<!-- prereq:end -->"

	goModPath = "go.mod"
	// ansibleClientPins installs the same clients as the conformance workflow,
	// on the machines that prove the install documentation. Two lists of the
	// same versions, and only one of them was ever read by a gate.
	ansibleClientPins = "tools/install/ansible/roles/feint_clients/defaults/main.yml"
)

var goDirective = regexp.MustCompile(`(?m)^go\s+(\d+\.\d+(?:\.\d+)?)`)

// goVersion reads the toolchain the module declares. Empty when go.mod is not
// there, which is the ordinary case for somebody running the binary outside a
// checkout.
func goVersion(path string) string {
	body, err := os.ReadFile(path) //nolint:gosec // a path this repository owns
	if err != nil {
		return ""
	}
	if m := goDirective.FindStringSubmatch(string(body)); m != nil {
		return m[1]
	}
	return ""
}

// renderPrereq writes the prerequisites table from the values the code and the
// module file already hold.
func renderPrereq(goMod string) string {
	floor := versionText(incusMinimum[:])
	series := versionText(incusRecommended[:])

	var b strings.Builder
	b.WriteString(docsGenerated)
	b.WriteString("\n\n| What | Version | What it buys you |\n|---|---|---|\n")

	if v := goVersion(goMod); v != "" {
		fmt.Fprintf(&b, "| Go | %s | Building from source. A released binary needs nothing. |\n", v)
	}
	fmt.Fprintf(&b, "| [Incus](https://linuxcontainers.org/incus/) | %s recommended, **%s minimum** |"+
		" `--vm`: powered-on servers become real containers or KVM machines you can `ssh` into. |\n", series, floor)
	b.WriteString("| OVN (`ovn-central`, `ovn-host`, Open vSwitch) | optional |" +
		" `--vm incus-ovn`: subnets that are actually separate, so two VPCs cannot reach each other. |\n")

	fmt.Fprintf(&b, "\n%s is a floor rather than a preference: below it the runtime refuses ACLs on a\n", floor)
	b.WriteString("NIC, and the failure reads like a Feint bug rather than a missing feature. Ubuntu\n")
	b.WriteString("24.04 ships 6.0.0 and will not move past it, so the Zabbly stable channel is the\n")
	fmt.Fprintf(&b, "practical way to a supported version. `feint doctor` checks all of this against\n"+
		"the same %s, and says what to install, which is the point of having it.\n", floor)
	return b.String()
}

// splicePrereq renders the prerequisites into a second document and reports
// whether it would change. Absent file, or a file that never claimed the marker,
// is not a failure: a binary installed from a release has no docs/ beside it.
func splicePrereq(path, goMod string) (bool, error) {
	updated, current, err := prereqSplice(path, goMod)
	if err != nil || updated == "" {
		return false, err
	}
	return updated != current, nil
}

func writeSplicedPrereq(path, goMod string) error {
	updated, _, err := prereqSplice(path, goMod)
	if err != nil {
		return err
	}
	if updated == "" {
		return nil
	}
	return os.WriteFile(path, []byte(updated), 0o644) //nolint:gosec // documentation is world-readable by design
}

// prereqSplice returns the rewritten document and the current one. An empty
// result means there was nothing to do.
func prereqSplice(path, goMod string) (updated, current string, err error) {
	if path == "" {
		return "", "", nil
	}
	body, err := os.ReadFile(path) //nolint:gosec // an operator-supplied path
	if os.IsNotExist(err) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	if !strings.Contains(string(body), prereqStartMarker) {
		return "", "", nil
	}
	out, err := spliceSection(string(body), prereqStartMarker, prereqEndMarker, renderPrereq(goMod))
	if err != nil {
		return "", "", err
	}
	return out, string(body), nil
}

// clientPinMismatches compares the client versions the conformance workflow
// installs with the ones the Ansible role installs.
//
// They are the same clients, pinned twice, and nothing compared them. The
// consequence is not hypothetical: `exo` was pinned to v1.86 in the workflow
// while the role pinned v1.95.6, and v1.86 ignores the endpoint key in its own
// configuration file and calls the real Exoscale. The role's own comment said so.
//
// An empty result means agreement, or that one of the files is absent — which is
// the normal case for a binary run outside a checkout, and never a failure.
func clientPinMismatches(workflow, ansible string) []string {
	pinned, err := pinnedVersions(workflow)
	if err != nil {
		return nil
	}
	body, err := os.ReadFile(ansible) //nolint:gosec // a path this repository owns
	if err != nil {
		return nil
	}

	// The role names its variables after the client, the workflow after the
	// same client in upper case. Mapped explicitly rather than derived: a
	// guessed mapping that stops matching goes quiet, which is the failure this
	// whole function exists to end.
	pairs := []struct{ workflowVar, ansibleVar string }{
		{"SCW_VERSION", "feint_clients_scw_version"},
		{"OAPI_VERSION", "feint_clients_oapi_version"},
		{"EXO_VERSION", "feint_clients_exo_version"},
		{"TERRAFORM_VERSION", "feint_clients_terraform_version"},
		{"TOFU_VERSION", "feint_clients_tofu_version"},
	}

	var out []string
	for _, p := range pairs {
		want, ok := pinned[p.workflowVar]
		if !ok {
			continue
		}
		pattern := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(p.ansibleVar) + `:\s*"?v?([^"\s]+)"?`)
		m := pattern.FindStringSubmatch(string(body))
		if m == nil {
			out = append(out, fmt.Sprintf("%s pins %s=%s and %s sets no %s",
				workflow, p.workflowVar, want, ansible, p.ansibleVar))
			continue
		}
		if got := m[1]; got != want {
			out = append(out, fmt.Sprintf("%s installs %s %s and %s installs %s: the machines that prove the install page do not run what CI runs",
				workflow, p.workflowVar, want, ansible, got))
		}
	}
	sort.Strings(out)
	return out
}
