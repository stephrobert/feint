// Command review prints what a maintainer reads first in a pull request from
// outside the project (#988, item 3). It is the sequence the orchestrator ran
// by hand on 2026-11-16 for #985, #986, #987 and #946, written down once.
//
// It reads the GitHub API through `gh api` and nothing else. It never checks
// out the pull request, never builds it, never runs it: every byte of the pull
// request it sees is a string in an API answer, scanned and printed escaped.
//
// The report has three parts, in the order they matter:
//
//  1. the diff: sensitive paths (the list of tools/ci/sensitive-paths.txt, read
//     from the local checkout and never from the pull request) and the added
//     lines that hold a construction a reviewer must read (process execution,
//     network, environment, init, directives, unsafe, encoded strings, new
//     actions, go.mod, a falsify spec's env, files that become executable);
//  2. the account: facts about its age, its volume of pull requests and forks
//     on the day, and the Assisted-by: trailer of each commit. Facts, no score;
//  3. the assembly: Go identifiers declared on one side and used on the other,
//     between this pull request and the open pull requests of the same author
//     and the merged pull requests of outside accounts (bounded, and the bound
//     is printed), and the files they share.
//
// Exit codes, stable because a script reads them:
//
//	0  the report is complete and neither part 1 nor part 3 holds anything to read
//	1  it cannot be judged: an API call failed, an answer had a shape this
//	   program does not know, a list was shorter than GitHub announced, or the
//	   sensitive-path list could not be read. Nothing is reported as clean
//	2  the report is complete and holds at least one finding to read first
//
// Part 3's identifier links count towards 2 as well as part 1's findings: the
// assembled payload is the threat the cross-check exists for, and a script that
// read 0 would skip it. Shared files and part 2 never change the code.
//
// Tested by tools/review/review_test.go through a stubbed `gh`; every detection
// and every "cannot judge" branch has a mutation in
// tools/falsify/specs/an-outside-pull-request-is-read-before-it-runs.json.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
)

const (
	exitClean    = 0
	exitCannot   = 1
	exitFindings = 2
)

var repoShape = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func main() {
	gh, err := exec.LookPath("gh")
	if err != nil {
		fmt.Fprintln(os.Stderr, "external-pr: cannot judge: gh is not on PATH")
		os.Exit(exitCannot)
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, &client{bin: gh}))
}

// run is main without the process: the tests call it with a client whose
// binary is a stub.
func run(args []string, stdout, stderr io.Writer, c *client) int {
	fs := flag.NewFlagSet("external-pr", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the report as JSON")
	repo := fs.String("repo", "", "owner/name (default: the repository gh resolves from the working directory)")
	pathsFile := fs.String("paths-file", "tools/ci/sensitive-paths.txt", "the sensitive-path list, read from this checkout")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: external-pr [--json] [--repo owner/name] [--paths-file path] <pull request number>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitCannot
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return exitCannot
	}
	number, err := strconv.Atoi(fs.Arg(0))
	if err != nil || number < 1 {
		fmt.Fprintf(stderr, "external-pr: cannot judge: %s is not a pull request number\n", escape(fs.Arg(0)))
		return exitCannot
	}

	r, err := review(c, *repo, number, *pathsFile)
	if err != nil {
		var cj *cannotJudge
		if !errors.As(err, &cj) {
			cj = &cannotJudge{why: err.Error()}
		}
		if *asJSON {
			writeJSON(stdout, map[string]any{"verdict": "cannot-judge", "reason": escape(cj.why)})
		}
		fmt.Fprintf(stderr, "external-pr: cannot judge: %s\n", escape(cj.why))
		return exitCannot
	}
	if *asJSON {
		writeJSON(stdout, r)
	} else {
		writeText(stdout, r)
	}
	if r.toRead() > 0 {
		return exitFindings
	}
	return exitClean
}

// cannotJudge is the third outcome. Every failed read returns one, and run
// turns it into exit 1 with no report: a partial report reads like a whole one.
type cannotJudge struct{ why string }

func (e *cannotJudge) Error() string { return e.why }

func cannot(format string, a ...any) error {
	return &cannotJudge{why: fmt.Sprintf(format, a...)}
}

func review(c *client, repo string, number int, pathsFile string) (*report, error) {
	patterns, err := loadPatterns(pathsFile)
	if err != nil {
		return nil, err
	}
	if repo == "" {
		if repo, err = c.currentRepo(); err != nil {
			return nil, err
		}
	}
	if !repoShape.MatchString(repo) {
		return nil, cannot("repository %q is not owner/name", repo)
	}

	pr, err := c.pull(repo, number)
	if err != nil {
		return nil, err
	}
	files, err := c.files(repo, number, pr.ChangedFiles)
	if err != nil {
		return nil, err
	}
	headModes, err := c.modes(repo, pr.Head.SHA)
	if err != nil {
		return nil, err
	}
	baseModes, err := c.modes(repo, pr.Base.SHA)
	if err != nil {
		return nil, err
	}

	r := &report{
		Repository: repo,
		Number:     number,
		Title:      escape(pr.Title),
		Author:     escape(pr.User.Login),
		Asso:       escape(pr.AuthorAssociation),
		HeadSHA:    pr.Head.SHA,
		BaseSHA:    pr.Base.SHA,
		PathsFile:  pathsFile,
	}
	findings, err := scanDiff(files, patterns, headModes, baseModes)
	if err != nil {
		return nil, err
	}
	r.Diff = diffPart{Files: describeFiles(files, headModes), Findings: findings}

	if r.Account, err = readAccount(c, repo, pr); err != nil {
		return nil, err
	}
	if r.Assembly, err = readAssembly(c, repo, pr, files); err != nil {
		return nil, err
	}
	r.finish()
	return r, nil
}
