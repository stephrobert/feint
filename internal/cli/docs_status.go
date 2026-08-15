package cli

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// The status table: which clients actually prove each provider, read from the
// workflow that runs them.
//
// It was written by hand, and it drifted the way a hand-written claim does. The
// README said Outscale was proven by `oapi-cli` and Terraform. Both were true —
// the fixture exists, applies twenty-one resources, plans empty and destroys
// clean — but the Terraform suite ran only where somebody typed
// `mise run conformance`, never in a workflow. A regression in it would have
// reached a release without one red check.
//
// A reader comparing the README with the workflow found it. That is the only way
// a hand-written claim is ever found, and the reason this table stopped being
// one.
//
// The source is `.github/workflows/conformance.yml`, deliberately: "proven" here
// means proven where anybody can reproduce it by opening a pull request, not
// proven on the author's station. A suite that exists and is never run in CI
// therefore does not appear — which is exactly the gap that was missed, made
// visible rather than argued about.
//
// TestTheStatusTableNamesOnlyClientsCIRuns fails without this.

const (
	statusStartMarker = "<!-- status:start -->"
	statusEndMarker   = "<!-- status:end -->"
)

// suiteInvocation matches the way the workflow runs a conformance suite:
// tools/conformance/<provider>/<suite>.sh
var suiteInvocation = regexp.MustCompile(`tools/conformance/([a-z]+)/([a-z-]+)\.sh`)

// clientOf maps a suite to the client it drives, as a reader would name it.
//
// A suite absent from this map is an error rather than a skip: a new client
// proving a provider must appear in the table, and silently dropping the one
// name nobody mapped is how a table understates what is proven — the mirror of
// the defect above, and just as wrong.
var clientOf = map[string]string{
	"scw-cli":   "`scw`",
	"oapi-cli":  "`oapi-cli`",
	"exo-cli":   "`exo`",
	"terraform": "Terraform, OpenTofu",
	// Neither names a client: the probe drives every route from the provider's
	// own API description, and the network and ssh suites prove the machine
	// behind the answer rather than a client's compatibility. Mapped to nothing
	// on purpose, so they are known rather than unmatched.
	"network": "",
	"ssh":     "",
	"crash":   "",
}

// providerRow is what the table prints for one provider, beyond the clients.
// The protocol and the maturity are editorial: no artefact states them, and
// pretending to derive them would be the invented measurement this file exists
// to remove.
type providerRow struct {
	name     string
	protocol string
	maturity string
}

var statusRows = []providerRow{
	{"Scaleway", "REST JSON, `X-Auth-Token`", "usable"},
	{"Outscale", "`POST /api/v1/<Action>`, AWS Signature v4", "starter"},
	{"Exoscale", "REST `/v2/<resource>`, asynchronous operations", "starter"},
}

// suiteRun is one conformance invocation the workflow makes: a provider, the
// suite script, and the clients that script drives as clientOf spells them.
//
// Two tables read this one scan. The status table above asks which clients prove
// a provider; the client matrix in docs_clients.go asks the transpose, which
// provider a client proves. Deriving both from the same pass is the point: a
// second reader of the same workflow would be a second chance for the two tables
// to disagree, which is the defect each of them was filed for.
type suiteRun struct {
	provider string
	suite    string
	// clients is the cell clientOf holds, verbatim. One suite can name several
	// clients — `terraform.sh` is driven by both Terraform and OpenTofu — and the
	// two consumers want it differently: the status table prints the cell whole,
	// the client matrix splits it. Splitting here would reorder the status table
	// for no reason, so each caller splits when it needs to.
	clients string
}

// suitesRunInCI scans the workflow for conformance invocations.
//
// An unmapped suite is an error rather than a skip: a new client proving a
// provider must appear in both tables, and silently dropping the one name nobody
// mapped is how a table understates what is proven.
func suitesRunInCI(workflowPath string) ([]suiteRun, error) {
	body, err := os.ReadFile(workflowPath) //nolint:gosec // a path this repository owns
	if err != nil {
		return nil, err
	}

	var runs []suiteRun
	for _, match := range suiteInvocation.FindAllStringSubmatch(string(body), -1) {
		provider, suite := match[1], match[2]
		client, known := clientOf[suite]
		if !known {
			return nil, fmt.Errorf(
				"%s runs tools/conformance/%s/%s.sh and no client is mapped to it: "+
					"add it to clientOf in internal/cli/docs_status.go, so the status "+
					"table names it instead of quietly leaving it out",
				workflowPath, provider, suite)
		}
		if client == "" {
			continue
		}
		runs = append(runs, suiteRun{provider: provider, suite: suite, clients: client})
	}
	return runs, nil
}

// clientsProvenInCI reads the workflow and answers, per provider, the clients it
// drives. The returned map is keyed by the lowercase provider directory.
func clientsProvenInCI(workflowPath string) (map[string][]string, error) {
	runs, err := suitesRunInCI(workflowPath)
	if err != nil {
		return nil, err
	}

	found := map[string]map[string]bool{}
	for _, run := range runs {
		if found[run.provider] == nil {
			found[run.provider] = map[string]bool{}
		}
		found[run.provider][run.clients] = true
	}

	out := map[string][]string{}
	for provider, clients := range found {
		names := make([]string, 0, len(clients))
		for name := range clients {
			names = append(names, name)
		}
		sort.Strings(names)
		out[provider] = names
	}
	return out, nil
}

// renderStatus builds the table.
func renderStatus(workflowPath string) (string, error) {
	proven, err := clientsProvenInCI(workflowPath)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(docsGenerated + "\n\n")
	b.WriteString("| Provider | Protocol | Proven by, in CI | Maturity |\n|---|---|---|---|\n")
	for _, row := range statusRows {
		clients := proven[strings.ToLower(row.name)]
		cell := "nothing yet"
		if len(clients) > 0 {
			cell = strings.Join(clients, ", ")
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", row.name, row.protocol, cell, row.maturity)
	}
	b.WriteString("\nRead *proven by* narrowly: these are the clients a workflow drives on every\n" +
		"pull request. A suite that exists and runs only under `mise run conformance`\n" +
		"is a proof somebody has to take, not one this table can claim.\n")
	return b.String(), nil
}

// spliceStatus reports whether the status table in path is out of date, and
// leaves it alone. Absent markers mean a file that never claimed to carry it.
func spliceStatus(path, workflowPath string) (bool, error) {
	current, err := os.ReadFile(path) //nolint:gosec // a path this repository owns
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !strings.Contains(string(current), statusStartMarker) {
		return false, nil
	}
	rendered, err := renderStatus(workflowPath)
	if err != nil {
		return false, err
	}
	updated, err := spliceSection(string(current), statusStartMarker, statusEndMarker, rendered)
	if err != nil {
		return false, err
	}
	return updated != string(current), nil
}

func writeSplicedStatus(path, workflowPath string) error {
	current, err := os.ReadFile(path) //nolint:gosec // same path as above
	if err != nil {
		return err
	}
	rendered, err := renderStatus(workflowPath)
	if err != nil {
		return err
	}
	updated, err := spliceSection(string(current), statusStartMarker, statusEndMarker, rendered)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(updated), 0o644) //nolint:gosec // documentation is world-readable by design
}
