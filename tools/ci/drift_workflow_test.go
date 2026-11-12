package ci

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The drift scan runs every night and reports through the night-report
// mechanism (#705), and the two outcomes it has to keep apart are held here
// rather than asserted in the workflow's comments.
//
// What is held, read off the workflow as text since a workflow only runs on
// GitHub's runners:
//
//   - the schedule is nightly, not Monday: the automation the whole project
//     hangs on had a blind window of up to seven days;
//   - a job adopts .github/workflows/night-report.yml after every other job,
//     on the schedule alone, under always(): a night where the scan could not
//     conclude — a clone that would not fetch, a build that broke, a push that
//     was refused — opens one issue, updated, closed by the first green night,
//     instead of leaving a job log nobody opens;
//   - the pull request stays conditional on a real diff: a nightly job that
//     opened a pull request every night would teach everyone to close them
//     unread, which is the failure mode the mechanism exists to avoid. The
//     surface moving is the mechanism working, not a red night, and
//     TestADriftNightWhereTheSurfaceMovedIsNotARedNight holds that half on
//     the script.
func TestTheDriftWorkflowReportsEveryNight(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "drift.yml"))
	if err != nil {
		t.Fatalf("read drift.yml: %v", err)
	}
	workflow := string(body)

	nightly := regexp.MustCompile(`(?m)^\s+- cron: '\d+ \d+ \* \* \*'`)
	if !nightly.MatchString(workflow) {
		t.Error("the drift scan is not on a nightly cron: the blind window between an upstream " +
			"change and anyone learning of it is the week #705 measured")
	}

	night := jobSection(workflow, "night")
	if night == "" {
		t.Fatal("drift.yml has no `night` job: a night where the scan could not conclude leaves a job log and nothing else")
	}
	if !strings.Contains(night, "uses: ./.github/workflows/night-report.yml") {
		t.Error("the night job does not adopt night-report.yml, the mechanism runtime-proof.yml already uses")
	}
	if !strings.Contains(night, "\n    if: always() && github.event_name == 'schedule'\n") {
		t.Error("the night job is not `always()` on the schedule alone: a failed scan would skip the very " +
			"job that reports it, or a pull request run would shout about a run somebody is reading")
	}
	needs := regexp.MustCompile(`(?m)^\s+needs:\s*\[([^\]]+)\]`).FindStringSubmatch(night)
	if needs == nil {
		t.Fatal("the night job needs nothing, so it judges a run whose jobs have not finished")
	}
	for _, job := range []string{"drift", "propose", "report"} {
		if !strings.Contains(needs[1], job) {
			t.Errorf("the night job does not wait for `%s`: a failure there would be judged green", job)
		}
	}

	propose := jobSection(workflow, "propose")
	if !strings.Contains(propose, "\n    if: github.event_name == 'schedule' && (needs.drift.outputs.changed == 'true' || needs.drift.outputs.contracts == 'true')\n") {
		t.Error("the pull request is no longer conditional on a real diff of either half: a pull request " +
			"every night teaches everyone to close them unread, and a condition on the coverage half " +
			"alone skipped the job entirely on a night where only the API descriptions moved")
	}
}

// TestTheDriftPullRequestCarriesEveryArtefactTheScanRegenerates is cited by the
// upload step of drift.yml.
//
// ## The defect it closes, as it was measured
//
// The nightly regenerates two kinds of artefact, and they are two different
// measurements: `coverage/` comes from reading a released Go SDK, `contracts/`
// from the documents the providers serve. The job ran both. It uploaded only
// the first, so the second died with the runner:
//
//   - the branch the nightly pushed on 2026-11-09 (97c54a0) holds README.md and
//     two files under coverage/, and nothing under contracts/;
//   - contracts/scaleway.json had not moved since 2026-10-19 (#803) while the
//     providers' documents moved under it every night;
//   - on 2026-11-12 a fresh extraction answered +328 lines: the two baremetal
//     commitment operations a triage was waiting for, and twenty-one newly
//     declared query parameters, six of them on routes this emulator serves and
//     was dropping without saying so (#884).
//
// An outside contributor hit it as a side effect of a triage. Nothing in the
// repository would have said it: the issue reports what is undecided, and a
// contract that has not travelled leaves nothing undecided to report.
//
// ## What is asserted, and why the condition is part of it
//
// Three things, because the defect had three independent halves and fixing one
// leaves the mechanism dead: the artefact must carry each path, `propose` must
// stage each path, and both must run when EITHER half moved. Keyed on the
// coverage diff alone, a descriptions-only night uploaded nothing at all, and
// the download one job over then failed on a missing artefact.
func TestTheDriftPullRequestCarriesEveryArtefactTheScanRegenerates(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "drift.yml"))
	if err != nil {
		t.Fatalf("read drift.yml: %v", err)
	}
	workflow := string(body)

	// What the scan writes back into the checkout: gate.sh update writes
	// coverage/ and then runs `feint docs`, which regenerates README.md,
	// README.fr.md and docs/ from it, and tools/contract/update.sh writes
	// contracts/.
	//
	// README.fr.md is in this list because the same command writes it, not
	// because a drift night was seen changing it: the translated banner counts
	// mounted routes, so a triage that adds a route changes it and a baseline
	// refresh does not. A path the generator writes and the artefact drops is
	// the defect, whether or not it has fired yet.
	regenerated := []string{"coverage/", "contracts/", "README.md", "README.fr.md", "docs/"}

	upload := stepSection(workflow, "Upload the refreshed coverage")
	if upload == "" {
		t.Fatal("drift.yml no longer uploads the refreshed artefacts: propose checks out main fresh and would stage nothing")
	}
	for _, path := range regenerated {
		if !strings.Contains(upload, "\n            "+path+"\n") {
			t.Errorf("the artefact does not carry %s: the step regenerates it and it dies with the runner, "+
				"which is how contracts/scaleway.json went twenty-four days without moving", path)
		}
	}
	if !strings.Contains(upload, "steps.contracts.outputs.changed == 'true'") {
		t.Error("the upload is keyed on the coverage diff alone: a night where only the API descriptions " +
			"moved uploads nothing, and the download in propose fails on a missing artefact")
	}

	propose := jobSection(workflow, "propose")
	add := regexp.MustCompile(`(?m)^\s+git add ([^\n]+)$`).FindStringSubmatch(propose)
	if add == nil {
		t.Fatal("the propose job stages nothing")
	}
	// The staged paths as arguments, not as text: anything after a `#` is a
	// comment the shell never passes to git, and a substring search would read
	// a commented-out path as staged.
	staged := add[1]
	if hash := strings.Index(staged, "#"); hash >= 0 {
		staged = staged[:hash]
	}
	for _, path := range regenerated {
		if !slices.Contains(strings.Fields(staged), path) {
			t.Errorf("propose does not stage %s, so the pull request is born without it: `git add %s`", path, add[1])
		}
	}
}

// TestTheTriageViewCoversEveryProductTheGateScans is cited by the triage step
// of drift.yml and by the `products` mode of tools/drift/gate.sh.
//
// ## The defect it closes, as it was measured
//
// The drift night draws two things from the same scan, and they came from two
// different product lists:
//
//   - the verdict, from tools/drift/gate.sh, over eleven Scaleway products;
//   - the triage view attached to the issue, from this workflow's own copy of
//     the list, over three: instance, vpc, ipam.
//
// So an undecided operation in any of the other eight products was counted in
// the verdict and could not appear in the view. The issue said "the nightly
// scan found operations upstream that nobody has triaged" over a view that did
// not hold them. Measured on the two baremetal commitment operations of #875:
// baremetal joined the gate on 2026-09-06 and never joined this list, and the
// operations sat undecided for fourteen days (#886).
//
// ## What is asserted
//
// Three things, because one is not enough. The workflow must ask gate.sh rather
// than carry a list; gate.sh must answer without a feint binary, since the
// triage step runs under always() on a night where the build may have failed;
// and the answer must match the products the committed baseline was written
// from, which is what makes "the gate scans these" a fact rather than a
// comment. A list that stops matching the baseline is the configuration skew
// gate.sh's own header was written for.
func TestTheTriageViewCoversEveryProductTheGateScans(t *testing.T) {
	root := filepath.Join("..", "..")

	out, err := exec.Command(filepath.Join(root, "tools", "drift", "gate.sh"), "products").Output()
	if err != nil {
		t.Fatalf("tools/drift/gate.sh products: %v (the triage view cannot name the products without it)", err)
	}
	scanned := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(scanned) < 2 {
		t.Fatalf("gate.sh products answered %q, which is not a product list", string(out))
	}

	// Against the products the committed baseline was written from: the gate
	// and the record of what it measured cannot disagree without the verdict
	// losing its meaning.
	raw, err := os.ReadFile(filepath.Join(root, "coverage", "scaleway-baseline.json"))
	if err != nil {
		t.Fatalf("read the Scaleway baseline: %v", err)
	}
	var baseline struct {
		Products []string `json:"products"`
	}
	if err := json.Unmarshal(raw, &baseline); err != nil {
		t.Fatalf("read the Scaleway baseline: %v", err)
	}
	recorded := slices.Clone(baseline.Products)
	measured := slices.Clone(scanned)
	slices.Sort(recorded)
	slices.Sort(measured)
	if !slices.Equal(recorded, measured) {
		t.Errorf("gate.sh scans %v and the committed baseline was written from %v: the verdict and the "+
			"record of what it measured disagree, which is the skew gate.sh exists to make impossible",
			measured, recorded)
	}

	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "drift.yml"))
	if err != nil {
		t.Fatalf("read drift.yml: %v", err)
	}
	triage := stepSection(string(body), "The triage view")
	if triage == "" {
		t.Fatal("drift.yml no longer builds a triage view: the issue would report a count and name nothing")
	}
	if !strings.Contains(triage, "tools/drift/gate.sh products") {
		t.Error("the triage view does not ask gate.sh for the product list: a second copy of the list is " +
			"how the view came to cover three products while the gate scanned eleven")
	}
	// The literal the step used to carry. Named rather than inferred, because
	// `--products` with any hardcoded list is the defect, not this one spelling.
	products := regexp.MustCompile(`--products ([a-z,]+)`).FindStringSubmatch(triage)
	if products != nil {
		t.Errorf("the triage view carries its own product list %q: it will be narrower than the gate's the "+
			"first time a product joins gate.sh, which is exactly how baremetal was missed", products[1])
	}
}

// stepSection is the text of one step of a workflow, from its name to the next
// step or the end of the file.
func stepSection(workflow, name string) string {
	start := strings.Index(workflow, "- name: "+name+"\n")
	if start < 0 {
		return ""
	}
	rest := workflow[start+1:]
	next := regexp.MustCompile(`(?m)^      - name: `).FindStringIndex(rest)
	if next == nil {
		return rest
	}
	return rest[:next[0]]
}

// jobSection is the text of one job of a workflow, from its key to the next
// job's key or the end of the file.
func jobSection(workflow, job string) string {
	start := strings.Index(workflow, "\n  "+job+":\n")
	if start < 0 {
		return ""
	}
	rest := workflow[start+1:]
	next := regexp.MustCompile(`(?m)^  [a-z][a-z-]*:\n`).FindStringIndex(rest[len(job)+4:])
	if next == nil {
		return rest
	}
	return rest[:len(job)+4+next[0]]
}
