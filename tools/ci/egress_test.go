package ci

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// What leaves a CI runner, asked of every harden-runner step of every workflow
// rather than of the ones that were measured (#988).
//
// On 2026-11-16 every step ran `egress-policy: audit`, so harden-runner wrote
// down where each job went and stopped nothing. Three of those destinations
// were real services that the job had no reason to call, measured in the
// `domain resolved` lines of the last three green runs of every workflow:
//
//   - checkpoint-api.hashicorp.com, in 3 runs of 3 of every job that runs
//     Terraform: its version check, which CHECKPOINT_DISABLE turns off;
//   - sentry-par.scaleway.com, in 3 runs of 3 of the scw-cli, fields and image
//     jobs: the scw CLI's panic reporter (cmd/scw/main.go, v2.56.3), which no
//     setting turns off, so only the network can;
//   - www.google.com, clients2.google.com and content-autofill.googleapis.com,
//     in 3 runs of 3 of the page job: headless Chrome's background services.
//
// The rules below hold the fix in place: a block-mode list never names one of
// those hosts, every workflow that installs Terraform sets CHECKPOINT_DISABLE
// for all its jobs, and mise.toml sets it for every local task. They also hold
// the shape harden-runner v2.22.3 reads, because its agent (step-security/agent
// v0.18.0, config.go parseEndpoints) splits each entry on ":" and ignores an
// Atoi error: "github.com:https" becomes port 0 and the host is unreachable,
// with no error anywhere.
//
// What block is not: a barrier against a leak somebody has decided on. Block
// mode narrows what a CI job reaches and makes an unexpected lookup visible; it
// does not stop a decided leak, because github.com:443 alone accepts a `git
// push` to any repository. docs/limits.md says so, with what else stays open.
//
// A wildcard is held to one more rule: the part it leaves free must sit inside
// a zone GitHub owns and hands to nobody. productionresultssa*.blob.core.windows.net
// failed it, since anybody can create an Azure storage account whose name starts
// with productionresultssa; it was dropped from every list on 2026-11-16, and
// the agent still allows it on its own (docs/limits.md).
//
// Each rule has a table that asks one question per case, with an input that
// fails that question only, and a walk over the real files.
// tools/falsify/specs/the-runner-reaches-only-what-was-measured.json removes
// each question in turn and requires its case to go red.

// telemetryHosts are the hosts a block list must never name: each is a call to
// a real service that the job measured making and has no use for.
var telemetryHosts = map[string]string{
	"checkpoint-api.hashicorp.com":    "Terraform's version check: set CHECKPOINT_DISABLE instead",
	"sentry-par.scaleway.com":         "the scw CLI's panic reporter",
	"www.google.com":                  "headless Chrome's background services",
	"clients2.google.com":             "headless Chrome's background services",
	"content-autofill.googleapis.com": "headless Chrome's background services",
}

// ownedWildcardZones are the zones a wildcard may leave free: each is a domain
// GitHub owns whose names it does not hand to a third party. A wildcard whose
// free part could end anywhere else could match a name an attacker registers.
var ownedWildcardZones = []string{".githubapp.com", ".actions.githubusercontent.com"}

// wildcardOwned answers whether the free part of a one-wildcard host stays in
// a zone of ownedWildcardZones.
func wildcardOwned(host string) bool {
	_, after, _ := strings.Cut(host, "*")
	for _, zone := range ownedWildcardZones {
		if strings.HasSuffix(after, zone) && !strings.Contains(strings.TrimSuffix(after, zone), "*") {
			return true
		}
	}
	return false
}

// hardenUses marks the step; its `with:` keys sit two spaces deeper than the
// step's own keys.
var hardenUses = regexp.MustCompile(`^(\s*)(?:- )?uses:\s*step-security/harden-runner@`)

// endpointShape is what the agent reads correctly: a host, at most one `*`
// anywhere in it, a colon, and a decimal port.
var endpointShape = regexp.MustCompile(`^([a-z0-9*.-]+):([0-9]+)$`)

// hardenStep is one harden-runner step as written: its policy, and its
// allowed endpoints with comments stripped.
type hardenStep struct {
	line      int
	policy    string
	hasPolicy bool
	hasList   bool
	endpoints []string
}

// hardenSteps reads every harden-runner step of a workflow. A step is the
// `uses:` line plus every following line indented deeper than the step's
// dash, up to the next line that is not.
func hardenSteps(workflow string) []hardenStep {
	lines := strings.Split(workflow, "\n")
	var steps []hardenStep
	for i, line := range lines {
		m := hardenUses.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		// The step's dash: two columns left of `uses:` when `uses:` is not
		// the dash line itself.
		dash := len(m[1])
		if !strings.Contains(line, "- uses:") {
			dash -= 2
		}
		step := hardenStep{line: i + 1}
		listIndent := -1
		for _, next := range lines[i+1:] {
			trimmed := strings.TrimSpace(next)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			indent := len(next) - len(strings.TrimLeft(next, " "))
			if indent <= dash {
				break
			}
			if listIndent >= 0 {
				if indent > listIndent {
					step.endpoints = append(step.endpoints, strings.Fields(stripInlineComment(trimmed))...)
					continue
				}
				listIndent = -1
			}
			key, value, found := strings.Cut(trimmed, ":")
			if !found {
				continue
			}
			value = strings.TrimSpace(stripInlineComment(value))
			switch key {
			case "egress-policy":
				step.hasPolicy = true
				step.policy = strings.Trim(value, `'"`)
			case "allowed-endpoints":
				step.hasList = true
				if value == ">" || value == "|" || value == ">-" || value == "|-" {
					listIndent = indent
				} else {
					step.endpoints = append(step.endpoints, strings.Fields(strings.Trim(value, `'"`))...)
				}
			}
		}
		steps = append(steps, step)
	}
	return steps
}

// hardenStepProblems answers every reason one step does not hold, or nothing
// when it does. A shape it does not recognise is a refusal.
func hardenStepProblems(step hardenStep) []string {
	var problems []string
	switch {
	case !step.hasPolicy:
		// The action's own default is block, and with no list that blocks
		// nothing it was told about: a step nobody configured.
		problems = append(problems, "no egress-policy: the action defaults to block, and nobody chose it")
	case step.policy == "audit":
		if step.hasList {
			problems = append(problems, "allowed-endpoints under audit: the list reads like a control and is ignored")
		}
	case step.policy == "block":
		if len(step.endpoints) == 0 {
			problems = append(problems, "block with no allowed-endpoints")
		}
	default:
		problems = append(problems, fmt.Sprintf("egress-policy %q is neither audit nor block", step.policy))
	}
	for _, entry := range step.endpoints {
		m := endpointShape.FindStringSubmatch(entry)
		if m == nil {
			problems = append(problems, fmt.Sprintf("%q is not host:port, and the agent reads a bad port as 0", entry))
			continue
		}
		if port, err := strconv.Atoi(m[2]); err != nil || port < 1 || port > 65535 {
			problems = append(problems, fmt.Sprintf("%q names no port a connection can use", entry))
		}
		if strings.Count(m[1], "*") > 1 {
			problems = append(problems, fmt.Sprintf("%q has more than one wildcard, and the agent matches only the first", entry))
		} else if strings.Contains(m[1], "*") && !wildcardOwned(m[1]) {
			problems = append(problems, fmt.Sprintf("%q leaves free a part a third party could register a name in", entry))
		}
		if why, ok := telemetryHosts[m[1]]; ok {
			problems = append(problems, fmt.Sprintf("%q is a call to a real service the job has no use for: %s", entry, why))
		}
	}
	return problems
}

func TestHardenStepProblemsAsksEachQuestion(t *testing.T) {
	good := "      - name: Harden runner\n" +
		"        uses: step-security/harden-runner@46b06cea07f9e64621fdf066ba77ac6915163d43 # v2.22.3\n" +
		"        with:\n" +
		"          # sentry-par.scaleway.com is named in a comment, which is not a list entry\n" +
		"          egress-policy: block\n" +
		"          allowed-endpoints: >\n" +
		"            github.com:443\n" +
		"            # a comment inside the list is not an entry either\n" +
		"            hosted-compute-*.githubapp.com:443\n" +
		"      - name: Checkout\n" +
		"        uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1\n"
	with := func(old, repl string) string {
		if !strings.Contains(good, old) {
			t.Fatalf("case does not apply: %q is not in the good step", old)
		}
		return strings.Replace(good, old, repl, 1)
	}
	cases := []struct {
		name     string
		workflow string
		want     string
	}{
		{"a measured block list holds", good, ""},
		{"audit with no list holds", with("egress-policy: block\n          allowed-endpoints: >\n            github.com:443\n            # a comment inside the list is not an entry either\n            hosted-compute-*.githubapp.com:443\n", "egress-policy: audit\n"), ""},
		{"no egress-policy at all", with("          egress-policy: block\n", ""), "no egress-policy"},
		{"a policy spelled otherwise", with("egress-policy: block", "egress-policy: blocked"), "neither audit nor block"},
		{"a list under audit", with("egress-policy: block", "egress-policy: audit"), "under audit"},
		{"block with nothing allowed", with("          allowed-endpoints: >\n            github.com:443\n            # a comment inside the list is not an entry either\n            hosted-compute-*.githubapp.com:443\n", ""), "block with no allowed-endpoints"},
		{"an entry with no port", with("            github.com:443\n", "            github.com\n"), "not host:port"},
		{"a port out of range", with("            github.com:443\n", "            github.com:70000\n"), "no port a connection can use"},
		{"two wildcards", with("hosted-compute-*.githubapp.com:443", "hosted-*-compute-*.githubapp.com:443"), "more than one wildcard"},
		{"a wildcard over a zone anybody can register in", with("            github.com:443\n", "            github.com:443\n            productionresultssa*.blob.core.windows.net:443\n"), "third party could register"},
		{"a telemetry host allowed", with("            github.com:443\n", "            github.com:443\n            sentry-par.scaleway.com:443\n"), "real service"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			steps := hardenSteps(c.workflow)
			if len(steps) != 1 {
				t.Fatalf("read %d harden-runner steps, want 1", len(steps))
			}
			problems := strings.Join(hardenStepProblems(steps[0]), "; ")
			if c.want == "" {
				if problems != "" {
					t.Fatalf("refused a step that holds: %s", problems)
				}
				return
			}
			if !strings.Contains(problems, c.want) {
				t.Fatalf("want a refusal naming %q, got %q", c.want, problems)
			}
		})
	}
}

func TestEveryHardenRunnerStepHolds(t *testing.T) {
	steps, blocked := 0, 0
	for _, name := range workflowFiles(t) {
		raw := rawWorkflow(t, name)
		found := hardenSteps(raw)
		// The reader's witness: every `uses:` of the action was read as a step.
		if want := strings.Count(raw, "step-security/harden-runner@"); len(found) != want {
			t.Errorf("%s: read %d harden-runner steps of %d: the reader missed a shape", name, len(found), want)
		}
		for _, step := range found {
			steps++
			if step.policy == "block" {
				blocked++
			}
			for _, problem := range hardenStepProblems(step) {
				t.Errorf("%s:%d: %s", name, step.line, problem)
			}
		}
	}
	if steps == 0 || blocked == 0 {
		t.Fatalf("read %d harden-runner steps, %d in block mode: this test would pass by having nothing to look at", steps, blocked)
	}
}

// installsTerraform marks a workflow that puts Terraform on the runner.
var installsTerraform = regexp.MustCompile(`releases\.hashicorp\.com/terraform|TERRAFORM_VERSION:`)

// checkpointProblem answers why a workflow that installs Terraform does not
// switch its version check off for every job, or "" when it does or installs
// none. The variable must sit in the workflow-level `env:`, so a job added
// later inherits it.
func checkpointProblem(workflow string) string {
	if !installsTerraform.MatchString(workflow) {
		return ""
	}
	// "\nenv:\n" at column 0 is the workflow's own; a job's sits at four spaces.
	_, env, found := strings.Cut("\n"+workflow, "\nenv:\n")
	if !found {
		return "installs Terraform and has no workflow-level env: CHECKPOINT_DISABLE reaches no job"
	}
	for _, line := range strings.Split(env, "\n") {
		// The next top-level key ends the block: past it, a job's own env
		// would answer for the whole workflow.
		if !strings.HasPrefix(line, " ") && strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "#") {
			break
		}
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || key != "CHECKPOINT_DISABLE" {
			continue
		}
		if v := strings.Trim(strings.TrimSpace(stripInlineComment(value)), `'"`); v != "1" {
			return fmt.Sprintf("CHECKPOINT_DISABLE is %q, not '1'", v)
		}
		return ""
	}
	return "installs Terraform and its workflow-level env does not set CHECKPOINT_DISABLE: '1'"
}

func TestCheckpointProblemAsksEachQuestion(t *testing.T) {
	good := "on:\n  pull_request:\nenv:\n  TERRAFORM_VERSION: '1.13.3'\n  CHECKPOINT_DISABLE: '1'\n\njobs:\n  a:\n    runs-on: ubuntu-24.04\n"
	cases := []struct{ name, workflow, want string }{
		{"set at the top holds", good, ""},
		{"no Terraform, nothing asked", "on:\n  pull_request:\n\njobs:\n  a:\n    runs-on: ubuntu-24.04\n", ""},
		{"not set", strings.Replace(good, "  CHECKPOINT_DISABLE: '1'\n", "", 1), "does not set"},
		{"set to another value", strings.Replace(good, "CHECKPOINT_DISABLE: '1'", "CHECKPOINT_DISABLE: '0'", 1), "not '1'"},
		{"no workflow-level env", "on:\n  pull_request:\n\njobs:\n  a:\n    env:\n      TERRAFORM_VERSION: '1.13.3'\n      CHECKPOINT_DISABLE: '1'\n", "no workflow-level env"},
		{"set in one job only", "on:\n  pull_request:\nenv:\n  TERRAFORM_VERSION: '1.13.3'\n\njobs:\n  a:\n    env:\n      CHECKPOINT_DISABLE: '1'\n", "does not set"},
		{"installed from the release site", strings.Replace(strings.Replace(good, "  CHECKPOINT_DISABLE: '1'\n", "", 1), "TERRAFORM_VERSION: '1.13.3'", "X: https://releases.hashicorp.com/terraform/1.13.3", 1), "does not set"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := checkpointProblem(c.workflow)
			if c.want == "" && got != "" {
				t.Fatalf("refused a workflow that holds: %s", got)
			}
			if c.want != "" && !strings.Contains(got, c.want) {
				t.Fatalf("want a refusal naming %q, got %q", c.want, got)
			}
		})
	}
}

func TestEveryWorkflowThatInstallsTerraformSilencesItsCheckpoint(t *testing.T) {
	installing := 0
	for _, name := range workflowFiles(t) {
		raw := rawWorkflow(t, name)
		if installsTerraform.MatchString(raw) {
			installing++
		}
		if problem := checkpointProblem(raw); problem != "" {
			t.Errorf("%s: %s", name, problem)
		}
	}
	// conformance.yml, released-linux.yml and runtime-proof.yml on 2026-11-16.
	// None found is a run that cannot be judged, not a pass.
	if installing == 0 {
		t.Fatal("no workflow installs Terraform: the marker no longer matches what the workflows write")
	}
}

// miseCheckpointProblem answers why mise.toml does not switch the version
// check off for every local task, or "" when it does. Only the top-level
// `[env]` table reaches every task.
func miseCheckpointProblem(toml string) string {
	table := ""
	for _, line := range strings.Split(toml, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			table = trimmed
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok || strings.TrimSpace(key) != "CHECKPOINT_DISABLE" {
			continue
		}
		if table != "[env]" {
			return fmt.Sprintf("CHECKPOINT_DISABLE is set under %s, which reaches one task, not every one", table)
		}
		if v := strings.Trim(strings.TrimSpace(value), `"'`); v != "1" {
			return fmt.Sprintf("CHECKPOINT_DISABLE is %q, not \"1\"", v)
		}
		return ""
	}
	return "mise.toml's [env] does not set CHECKPOINT_DISABLE, so a local suite asks HashiCorp for a newer version"
}

func TestMiseCheckpointProblemAsksEachQuestion(t *testing.T) {
	good := "[env]\nFEINT_ADDR = \"127.0.0.1:4599\"\nCHECKPOINT_DISABLE = \"1\"\n\n[tasks.check]\nrun = \"true\"\n"
	cases := []struct{ name, toml, want string }{
		{"set in [env] holds", good, ""},
		{"not set", strings.Replace(good, "CHECKPOINT_DISABLE = \"1\"\n", "", 1), "does not set"},
		{"set to another value", strings.Replace(good, "CHECKPOINT_DISABLE = \"1\"", "CHECKPOINT_DISABLE = \"\"", 1), "not \"1\""},
		{"set under one task", "[env]\nFEINT_ADDR = \"x\"\n\n[tasks.check.env]\nCHECKPOINT_DISABLE = \"1\"\n", "reaches one task"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := miseCheckpointProblem(c.toml)
			if c.want == "" && got != "" {
				t.Fatalf("refused a file that holds: %s", got)
			}
			if c.want != "" && !strings.Contains(got, c.want) {
				t.Fatalf("want a refusal naming %q, got %q", c.want, got)
			}
		})
	}
}

func TestMiseSilencesTheCheckpointForEveryLocalTask(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "mise.toml"))
	if err != nil {
		t.Fatalf("read mise.toml: %v", err)
	}
	if problem := miseCheckpointProblem(string(raw)); problem != "" {
		t.Error(problem)
	}
}
