package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// incus-release.sh (#979) compares the Incus release runtime-proof.yml pins with
// what the Zabbly stable channel serves, and keeps one issue open while they
// differ. These tests drive the real script with a stubbed `gh`, so the writes
// land in a log instead of on the repository.
//
// The fixture testdata/incus-release/zabbly-stable-noble-amd64.Packages is the
// channel's real index, fetched on 2026-11-16 from
// https://pkgs.zabbly.com/incus/stable/dists/noble/main/binary-amd64/Packages.gz:
// two builds of incus 7.6 (202611071352 and 202611072126) and nothing older,
// which is also why the workflow's pin is a glob over builds and not one build.
//
// One case per question the script asks. Each refused input passes every other
// question, so neutralising one decision turns exactly its case red —
// tools/falsify/specs/an-incus-release-is-noticed.json does that.

const realZabblyIndex = "testdata/incus-release/zabbly-stable-noble-amd64.Packages"

// incusStub answers the one read the script makes and records every write. A
// GH_STUB_LIST_FAILS set to 1 makes the read fail, the way a rate limit does.
const incusStub = `#!/bin/sh
set -eu
case "$1 $2" in
  "issue list")
    if [ "${GH_STUB_LIST_FAILS:-0}" = 1 ]; then
      echo "HTTP 403: API rate limit exceeded" >&2
      exit 1
    fi
    cat "${GH_STUB_OPEN}"
    ;;
  "issue view")
    if [ "${GH_STUB_VIEW_FAILS:-0}" = 1 ]; then
      echo "HTTP 502" >&2
      exit 1
    fi
    cat "${GH_STUB_VIEW}"
    ;;
  "issue comment"|"issue create"|"issue close"|"label create")
    printf '%s\n' "$*" >>"${GH_STUB_LOG}"
    prev=""
    for a in "$@"; do
      if [ "${prev}" = "--body-file" ]; then cat "${a}" >>"${GH_STUB_LOG}"; fi
      prev="${a}"
    done
    ;;
  *)
    echo "unexpected gh call: $*" >&2
    exit 64
    ;;
esac
`

const (
	noIncusIssue  = `[]`
	incusIssue    = `[{"number": 990, "title": "Zabbly stable serves an Incus the runtime proof does not pin"}]`
	otherDrift    = `[{"number": 800, "title": "Upstream API surface moved"}]`
	pinWorkflow76 = "env:\n  INCUS_VERSION: '7.6'\n"
)

// stanza writes one Packages entry the way aptly does.
func stanza(pkg, version string) string {
	return "Package: " + pkg + "\nVersion: " + version + "\nArchitecture: amd64\n\n"
}

type incusRun struct {
	workflow string // the workflow text; "" leaves the file absent
	index    string // the Packages text; "" leaves the file absent
	useReal  bool   // read the captured index instead of index
	open     string
	apply    bool
	listFail bool
	// lastWord is what the open issue last said (its latest comment, or its
	// body); viewFail makes reading it fail.
	lastWord string
	viewFail bool
}

func runIncusRelease(t *testing.T, r incusRun) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(incusStub), 0o755); err != nil { //nolint:gosec // a stub this test runs
		t.Fatal(err)
	}
	openFile := filepath.Join(dir, "open.json")
	if err := os.WriteFile(openFile, []byte(r.open), 0o600); err != nil {
		t.Fatal(err)
	}
	wf := filepath.Join(dir, "runtime-proof.yml")
	if r.workflow != "" {
		if err := os.WriteFile(wf, []byte(r.workflow), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	idx := filepath.Join(dir, "Packages")
	if r.useReal {
		idx = realZabblyIndex
	} else if r.index != "" {
		if err := os.WriteFile(idx, []byte(r.index), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(dir, "calls.log")
	view := filepath.Join(dir, "view.txt")
	if err := os.WriteFile(view, []byte(r.lastWord), 0o600); err != nil {
		t.Fatal(err)
	}
	viewFails := "0"
	if r.viewFail {
		viewFails = "1"
	}

	args := []string{"incus-release.sh"}
	if r.apply {
		args = append(args, "--apply")
	}
	args = append(args, wf, idx)
	cmd := exec.Command("bash", args...)
	fails := "0"
	if r.listFail {
		fails = "1"
	}
	cmd.Env = append(os.Environ(),
		"PATH="+dir+":"+os.Getenv("PATH"),
		"GH_STUB_OPEN="+openFile,
		"GH_STUB_LOG="+log,
		"GH_STUB_LIST_FAILS="+fails,
		"GH_STUB_VIEW="+view,
		"GH_STUB_VIEW_FAILS="+viewFails,
		"GITHUB_REPOSITORY=stephrobert/feint",
		"FEINT_RUN_URL=https://example.invalid/run/1",
	)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !asExitError(err, &exit) {
			t.Fatalf("run incus-release.sh: %v\n%s", err, out)
		}
		code = exit.ExitCode()
	}
	written, _ := os.ReadFile(log) //nolint:gosec // a path this test made
	return code, string(out), string(written)
}

// The accepting half, on the channel's real index: the pin is what Zabbly
// serves, so nothing is written and the run is green. A script that refused
// everything would pass every case below and fail this one.
func TestIncusReleaseIsQuietWhenThePinIsTheChannelsLatest(t *testing.T) {
	code, out, written := runIncusRelease(t, incusRun{workflow: pinWorkflow76, useReal: true, open: otherDrift, apply: true})
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
	if !strings.Contains(out, "verdict: the pin is the channel's latest release") {
		t.Errorf("no equal verdict:\n%s", out)
	}
	if written != "" {
		t.Errorf("an equal night wrote to GitHub, and the drift issue of somebody else is not ours to close:\n%s", written)
	}
}

// Equal again, and our issue is open: the first matching night closes it.
func TestIncusReleaseClosesItsIssueWhenThePinMatchesAgain(t *testing.T) {
	code, out, written := runIncusRelease(t, incusRun{workflow: pinWorkflow76, useReal: true, open: incusIssue, apply: true})
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
	if !strings.Contains(written, "issue close 990") {
		t.Errorf("the issue was not closed:\n%s\n--- writes ---\n%s", out, written)
	}
}

// Newer upstream, pin no longer served: the issue names the release, links its
// announcement, and says the nightly install will fail. Without --apply the
// exit is 2 and nothing is written.
func TestIncusReleaseNamesANewerRelease(t *testing.T) {
	index := stanza("incus", "1:7.7-ubuntu24.04-202612010000") + stanza("incus-client", "1:7.7-ubuntu24.04-202612010000")
	code, out, written := runIncusRelease(t, incusRun{workflow: pinWorkflow76, index: index, open: noIncusIssue})
	if code != 2 {
		t.Fatalf("exit %d, want 2 (drift):\n%s", code, out)
	}
	if written != "" {
		t.Errorf("a run without --apply wrote:\n%s", written)
	}
	for _, want := range []string{
		"Zabbly stable now serves **Incus 7.7**; the runtime proof pins **7.6**.",
		"The channel no longer serves 7.6",
		"https://github.com/lxc/incus/releases/tag/v7.7.0",
		"https://discuss.linuxcontainers.org/search?q=Incus%207.7%20has%20been%20released",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the body lacks %q:\n%s", want, out)
		}
	}
}

// A newer release while the pin is still served (a release day, both in the
// index): the body says tonight's proof still installs the pin.
func TestIncusReleaseSaysWhenThePinIsStillServed(t *testing.T) {
	index := stanza("incus", "1:7.6-ubuntu24.04-202611072126") + stanza("incus", "1:7.7-ubuntu24.04-202612010000")
	code, out, _ := runIncusRelease(t, incusRun{workflow: pinWorkflow76, index: index, open: noIncusIssue})
	if code != 2 {
		t.Fatalf("exit %d, want 2:\n%s", code, out)
	}
	if !strings.Contains(out, "The channel still serves 7.6") {
		t.Errorf("the body does not say the pin is still installable:\n%s", out)
	}
}

// The comparison is numeric. Lexically 7.9 sorts after 7.10, so a text sort
// calls 7.9 the latest and a pin of 7.9 equal: silence over a newer release.
func TestIncusReleaseComparesVersionsNumerically(t *testing.T) {
	index := stanza("incus", "1:7.10-ubuntu24.04-1") + stanza("incus", "1:7.9-ubuntu24.04-1")
	code, out, _ := runIncusRelease(t, incusRun{workflow: "env:\n  INCUS_VERSION: '7.9'\n", index: index, open: noIncusIssue})
	if code != 2 || !strings.Contains(out, "latest on Zabbly stable: 7.10") {
		t.Fatalf("exit %d, want 2 with 7.10 as the latest:\n%s", code, out)
	}
}

// A pin ahead of the channel is a difference too, said as such.
func TestIncusReleaseNamesAPinTheChannelDoesNotHave(t *testing.T) {
	code, out, _ := runIncusRelease(t, incusRun{workflow: "env:\n  INCUS_VERSION: '7.7'\n", useReal: true, open: noIncusIssue})
	if code != 2 {
		t.Fatalf("exit %d, want 2:\n%s", code, out)
	}
	if !strings.Contains(out, "the pin names a release the channel does not have") {
		t.Errorf("the body does not say the pin is ahead:\n%s", out)
	}
}

// Only the package named exactly `incus` is the runtime. incus-client at a newer
// version passes every other question; a prefix match would report it.
func TestIncusReleaseReadsOnlyTheIncusPackage(t *testing.T) {
	index := stanza("incus", "1:7.6-ubuntu24.04-1") + stanza("incus-client", "1:7.7-ubuntu24.04-1")
	code, out, _ := runIncusRelease(t, incusRun{workflow: pinWorkflow76, index: index, open: noIncusIssue})
	if code != 0 {
		t.Fatalf("exit %d, want 0: incus-client is not the runtime:\n%s", code, out)
	}
}

// With --apply: no issue open creates one with the label and the exact title;
// ours open gets a comment; somebody else's drift issue is never commented.
func TestIncusReleaseOpensOrUpdatesOneIssue(t *testing.T) {
	index := stanza("incus", "1:7.7-ubuntu24.04-1")

	code, out, written := runIncusRelease(t, incusRun{workflow: pinWorkflow76, index: index, open: otherDrift, apply: true})
	if code != 0 {
		t.Fatalf("exit %d, want 0 once the issue is written:\n%s", code, out)
	}
	if !strings.Contains(written, "issue create --repo stephrobert/feint --title Zabbly stable serves an Incus the runtime proof does not pin --label drift") {
		t.Errorf("no issue created with the label and title:\n%s", written)
	}
	if strings.Contains(written, "issue comment 800") {
		t.Errorf("somebody else's drift issue was commented:\n%s", written)
	}

	_, _, written = runIncusRelease(t, incusRun{workflow: pinWorkflow76, index: index, open: incusIssue, apply: true})
	if !strings.Contains(written, "issue comment 990") || strings.Contains(written, "issue create") {
		t.Errorf("an open issue was not updated in place:\n%s", written)
	}
}

// Every way the run cannot be judged exits 1 and writes nothing. Each input is
// sound on every other question, and the reason is asserted because the exit
// code alone answers all of them.
func TestIncusReleaseRefusesWhatItCannotJudge(t *testing.T) {
	good := stanza("incus", "1:7.6-ubuntu24.04-1")
	cases := []struct {
		name   string
		run    incusRun
		reason string
	}{
		{"no pin", incusRun{workflow: "env:\n  OTHER: '1'\n", index: good}, "declares no INCUS_VERSION"},
		{"two pins", incusRun{workflow: "env:\n  INCUS_VERSION: '7.6'\njobs:\n  x:\n    env:\n      INCUS_VERSION: '7.5'\n", index: good}, "several INCUS_VERSION values"},
		{"pin not a release", incusRun{workflow: "env:\n  INCUS_VERSION: 'v7.6'\n", index: good}, "is not a dotted release"},
		{"index absent", incusRun{workflow: pinWorkflow76}, "cannot read the Packages index"},
		{"index without incus", incusRun{workflow: pinWorkflow76, index: stanza("incus-client", "1:7.6-ubuntu24.04-1")}, "lists no incus package"},
		{"unknown version shape", incusRun{workflow: pinWorkflow76, index: good + stanza("incus", "1:7.7~rc1-ubuntu24.04-1")}, "a shape this script does not know"},
		{"issue lookup refused", incusRun{workflow: pinWorkflow76, index: stanza("incus", "1:7.7-ubuntu24.04-1"), listFail: true, apply: true}, "could not list the open issues"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.run.open = noIncusIssue
			code, out, written := runIncusRelease(t, c.run)
			if code != 1 {
				t.Errorf("exit %d, want 1 (cannot judge):\n%s", code, out)
			}
			if !strings.Contains(out, c.reason) {
				t.Errorf("the refusal does not say %q:\n%s", c.reason, out)
			}
			if written != "" {
				t.Errorf("a run that could not judge wrote to GitHub:\n%s", written)
			}
		})
	}
}

// The same pin written twice, identically, is one pin: the two jobs of
// runtime-proof.yml may each carry it without the run refusing.
func TestIncusReleaseAcceptsOnePinWrittenTwice(t *testing.T) {
	wf := "env:\n  INCUS_VERSION: '7.6'\njobs:\n  x:\n    env:\n      INCUS_VERSION: \"7.6\"\n"
	code, out, _ := runIncusRelease(t, incusRun{workflow: wf, useReal: true, open: noIncusIssue})
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
}

// The open issue is commented once per release seen, not once a night (#979).
// One case per question: the issue already names tonight's release (silent),
// it names another (commented), and it cannot be read (exit 1, nothing
// written, never a guess either way).
func TestIncusReleaseCommentsOncePerRelease(t *testing.T) {
	index := stanza("incus", "1:7.7-ubuntu24.04-1")
	named := func(v string) string {
		return "The channel serves " + v + ".\n\nRelease notes for " + v + ":\n\n- link\n"
	}

	code, out, written := runIncusRelease(t, incusRun{workflow: pinWorkflow76, index: index, open: incusIssue, apply: true, lastWord: named("7.7")})
	if code != 0 || strings.Contains(written, "issue comment") {
		t.Errorf("exit %d; the issue already naming 7.7 was commented again:\n%s\n%s", code, written, out)
	}
	if !strings.Contains(out, "already names 7.7") {
		t.Errorf("the plan does not say why nothing is added:\n%s", out)
	}

	_, _, written = runIncusRelease(t, incusRun{workflow: pinWorkflow76, index: index, open: incusIssue, apply: true, lastWord: named("7.6.1")})
	if !strings.Contains(written, "issue comment 990") {
		t.Errorf("an issue naming another release was not commented:\n%s", written)
	}

	code, out, written = runIncusRelease(t, incusRun{workflow: pinWorkflow76, index: index, open: incusIssue, apply: true, viewFail: true})
	if code != 1 || written != "" || !strings.Contains(out, "could not read issue #990") {
		t.Errorf("an unreadable issue: exit %d, wrote %q:\n%s", code, written, out)
	}
}
