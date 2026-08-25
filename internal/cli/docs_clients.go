package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Which client, in which version, proved what.
//
// The README said which clients prove which provider and never in what version.
// For a project whose whole argument is that the upstream moves — 363 operations
// added upstream in twelve months, measured — that is the one column a reader needs: a
// `scw` newer than the last conformance run can reject an answer that passed,
// and nothing on the page would say so.
//
// Nothing in the table is restated here. The versions come from the workflow
// that installs the clients, the provider constraints from the Terraform
// fixtures that pin them, and the provider column from the same workflow scan
// the status table reads.
//
// That last one was a constant in this file, under a marker saying "Do not edit
// by hand", and it was wrong in the way a hand-written claim is always wrong:
// it credited Terraform to Scaleway alone while `conformance.yml` had been
// running `tools/conformance/outscale/terraform.sh` on every pull request under
// both the terraform and the opentofu legs. Generated is not derived. An
// understated proof costs as much as an overstated one here — an external review
// recommended deleting Terraform from the README's Outscale row on the strength
// of this table, which would have erased a suite that applies twenty-one
// resources.
//
// TestTheClientMatrixCreditsEveryProviderCIProves fails without this.

const (
	clientsStartMarker = "<!-- clients:start -->"
	clientsEndMarker   = "<!-- clients:end -->"

	conformanceWorkflow = ".github/workflows/conformance.yml"
	// conformanceRoot holds one directory per provider, each with the fixtures
	// its suites run. The Terraform provider pins are read from under here.
	conformanceRoot = "tools/conformance"
	// terraformSuite is the suite whose clients answer through a Terraform
	// provider, and therefore the only rows that carry a provider constraint.
	terraformSuite = "terraform"
)

// clientSources ties a client to the workflow variable that pins its version.
//
// It deliberately no longer says which provider the client proves: that is the
// column this file exists to derive. A client the workflow drives and this list
// does not name is an error in renderClients, not a silent omission.
var clientSources = []struct {
	name     string
	variable string
}{
	{"`scw`", "SCW_VERSION"},
	{"Terraform", "TERRAFORM_VERSION"},
	{"OpenTofu", "TOFU_VERSION"},
	{"`octl`", "OCTL_VERSION"},
	{"`exo`", "EXO_VERSION"},
}

var (
	versionPattern = regexp.MustCompile(`(?m)^\s*([A-Z0-9_]+_VERSION):\s*'([^']+)'`)
	// providerPattern reads one entry of a fixture's required_providers block.
	// It is anchored on `required_providers` rather than on a provider's name so
	// that a fourth pack's fixture is read the day it lands, without this file
	// learning the provider's spelling.
	providerPattern = regexp.MustCompile(
		`(?s)required_providers\s*\{.*?source\s*=\s*"([^"]+)".*?version\s*=\s*"([^"]+)"`)
)

// clientProof is one thing CI proves about one client: the provider it drove,
// and through which suite. The suite is kept because only the Terraform one
// carries a provider constraint.
type clientProof struct {
	provider string // the lowercase directory under tools/conformance
	suite    string
}

// pinnedVersions reads the versions the conformance workflow installs.
func pinnedVersions(workflow string) (map[string]string, error) {
	body, err := os.ReadFile(workflow) //nolint:gosec // a path this repository owns
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for _, m := range versionPattern.FindAllStringSubmatch(string(body), -1) {
		out[m[1]] = strings.TrimPrefix(m[2], "v")
	}
	return out, nil
}

// proofsPerClient transposes the workflow scan: for each client, the providers
// it drives and the suite it drives them through.
func proofsPerClient(workflow string) (map[string][]clientProof, error) {
	runs, err := suitesRunInCI(workflow)
	if err != nil {
		return nil, err
	}

	seen := map[string]map[clientProof]bool{}
	for _, run := range runs {
		for _, name := range strings.Split(run.clients, ", ") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if seen[name] == nil {
				seen[name] = map[clientProof]bool{}
			}
			seen[name][clientProof{provider: run.provider, suite: run.suite}] = true
		}
	}

	out := map[string][]clientProof{}
	for name, proofs := range seen {
		list := make([]clientProof, 0, len(proofs))
		for proof := range proofs {
			list = append(list, proof)
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].provider != list[j].provider {
				return list[i].provider < list[j].provider
			}
			return list[i].suite < list[j].suite
		})
		out[name] = list
	}
	return out, nil
}

// providerName spells a conformance directory the way the page names the
// provider. Derived rather than mapped: a table of three names would be one more
// hand-written thing to disagree with the directory it describes.
func providerName(dir string) string {
	if dir == "" {
		return dir
	}
	return strings.ToUpper(dir[:1]) + dir[1:]
}

// terraformProviderPin reads the provider constraint a provider's Terraform
// fixture pins. The Terraform version alone proves nothing: what answers the
// emulator is the provider, and it is pinned in a different file from every
// other client — one file per provider.
func terraformProviderPin(root, provider string) string {
	body, err := os.ReadFile(filepath.Join(root, provider, terraformSuite, "main.tf")) //nolint:gosec // a path this repository owns
	if err != nil {
		return ""
	}
	if m := providerPattern.FindStringSubmatch(string(body)); m != nil {
		return fmt.Sprintf("`%s %s`", m[1], m[2])
	}
	return ""
}

// versionCell renders what CI installs for one client, plus the provider
// constraints when the client answers through a Terraform provider.
func versionCell(version string, proofs []clientProof, root string) string {
	var pins []string
	for _, proof := range proofs {
		if proof.suite != terraformSuite {
			continue
		}
		if pin := terraformProviderPin(root, proof.provider); pin != "" {
			pins = append(pins, pin)
		}
	}
	switch len(pins) {
	case 0:
		return version
	case 1:
		return version + " with provider " + pins[0]
	default:
		return version + " with providers " + strings.Join(pins, ", ")
	}
}

func renderClients(workflow, root string) (string, error) {
	versions, err := pinnedVersions(workflow)
	if err != nil {
		return "", err
	}
	proofs, err := proofsPerClient(workflow)
	if err != nil {
		return "", err
	}

	// A client CI drives and this file does not list would be proven and
	// invisible, which is the exact shape of the defect that produced this
	// change. Refuse rather than print a table that is short by one row.
	listed := map[string]bool{}
	for _, c := range clientSources {
		listed[c.name] = true
	}
	unlisted := make([]string, 0, len(proofs))
	for name := range proofs {
		if !listed[name] {
			unlisted = append(unlisted, name)
		}
	}
	if len(unlisted) > 0 {
		sort.Strings(unlisted)
		return "", fmt.Errorf(
			"%s drives %s and clientSources in internal/cli/docs_clients.go does not "+
				"list it: the table would leave out a client CI proves, which is what "+
				"this generator exists to stop",
			workflow, strings.Join(unlisted, ", "))
	}

	var b strings.Builder
	b.WriteString(docsGenerated)
	b.WriteString("\n\n| Client | Version proven in CI | Emulated provider |\n|---|---|---|\n")
	for _, c := range clientSources {
		version, ok := versions[c.variable]
		if !ok {
			version = "unpinned"
		}

		mine := proofs[c.name]
		names := make([]string, 0, len(mine))
		for _, proof := range mine {
			name := providerName(proof.provider)
			if len(names) == 0 || names[len(names)-1] != name {
				names = append(names, name)
			}
		}
		// A listed client no workflow drives is the mirror of the case refused
		// above, and it says so in the cell rather than reading as a proof.
		providers := "not run in CI"
		if len(names) > 0 {
			providers = strings.Join(names, ", ")
		}

		fmt.Fprintf(&b, "| %s | %s | %s |\n", c.name, versionCell(version, mine, root), providers)
	}
	b.WriteString("\nThese are the versions the conformance workflow installs and runs on every\n")
	b.WriteString("pull request and nightly. A newer client is not known to work until the pin\n")
	b.WriteString("moves and the suite passes on it, which is the whole point of pinning it.\n")
	return b.String(), nil
}

// The splice itself lives in docs(), next to the coverage and banner ones: the
// document is already in memory there, and a second reader of the same file
// would be a second chance for the two to disagree.
