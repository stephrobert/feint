package main

import (
	"fmt"
	"io"
	"strings"
)

type report struct {
	Repository string       `json:"repository"`
	Number     int          `json:"number"`
	Title      string       `json:"title"`
	Author     string       `json:"author"`
	Asso       string       `json:"author_association"`
	HeadSHA    string       `json:"head_sha"`
	BaseSHA    string       `json:"base_sha"`
	PathsFile  string       `json:"sensitive_paths_file"`
	Verdict    string       `json:"verdict"`
	Diff       diffPart     `json:"diff"`
	Account    accountPart  `json:"account"`
	Assembly   assemblyPart `json:"assembly"`
}

type diffPart struct {
	Files    []fileDesc `json:"files"`
	Findings []finding  `json:"findings"`
}

// toRead counts what sets exit 2: part 1's findings, part 3's identifier links
// and the files part 3 could not compare.
func (r *report) toRead() int {
	return len(r.Diff.Findings) + len(r.Assembly.Links) + len(r.Assembly.Unread)
}

func (r *report) finish() {
	r.Verdict = "nothing-to-read-first"
	if r.toRead() > 0 {
		r.Verdict = "read-first"
	}
	// Empty lists print as [] in JSON, never null: a reader that tests for a
	// key must not take a missing list for an unread one.
	if r.Diff.Findings == nil {
		r.Diff.Findings = []finding{}
	}
	if r.Assembly.Links == nil {
		r.Assembly.Links = []link{}
	}
	if r.Assembly.SharedFiles == nil {
		r.Assembly.SharedFiles = []shared{}
	}
	if r.Assembly.Unread == nil {
		r.Assembly.Unread = []finding{}
	}
	if r.Assembly.Compared == nil {
		r.Assembly.Compared = []compared{}
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func (c count) String() string {
	if c.Incomplete {
		return fmt.Sprintf("at least %d (GitHub marks the search incomplete)", c.Total)
	}
	return fmt.Sprint(c.Total)
}

func writeText(w io.Writer, r *report) {
	fmt.Fprintf(w, "External pull request review: %s#%d\n", r.Repository, r.Number)
	fmt.Fprintf(w, "  title:  %s\n", r.Title)
	fmt.Fprintf(w, "  author: %s (association %s)\n", r.Author, r.Asso)
	fmt.Fprintf(w, "  head %s, base %s\n", r.HeadSHA, r.BaseSHA)
	fmt.Fprintf(w, "  read through the GitHub API only; nothing of the pull request was checked out or run\n")
	fmt.Fprintf(w, "  every string from the pull request is printed escaped (\\xNN, \\uNNNN, \\UNNNNNNNN, a backslash doubled)\n\n")

	fmt.Fprintf(w, "1. The diff: %d files, %d findings to read first\n", len(r.Diff.Files), len(r.Diff.Findings))
	fmt.Fprintf(w, "   sensitive paths from %s, in this checkout, not in the pull request\n", r.PathsFile)
	if len(r.Diff.Findings) == 0 {
		fmt.Fprintf(w, "   nothing found by the rules (which is not a review: read the diff)\n")
	}
	for _, f := range r.Diff.Findings {
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		fmt.Fprintf(w, "   [%s] %s\n", f.Rule, loc)
		if f.Detail != "" {
			fmt.Fprintf(w, "       %s\n", f.Detail)
		}
		if f.Excerpt != "" {
			fmt.Fprintf(w, "       | %s\n", f.Excerpt)
		}
	}

	a := r.Account
	fmt.Fprintf(w, "\n2. The account: facts, not a score\n")
	fmt.Fprintf(w, "   login %s, type %s, created %s\n", a.Login, a.Type, a.CreatedAt)
	fmt.Fprintf(w, "   name given: %s, bio given: %s, followers %d, public repositories %d\n", yesNo(a.HasName), yesNo(a.HasBio), a.Followers, a.PublicRepos)
	fmt.Fprintf(w, "   pull requests opened on GitHub: %s in total, %s on %s (UTC, the day of this one)\n", a.PRsTotal, a.PRsThatDay, a.Day)
	floor := ""
	if a.ForksLowerBound {
		floor = " at least"
	}
	fmt.Fprintf(w, "   forks last pushed on %s:%s %d, among the %d most recently pushed repositories (a later push moves a fork out of this count)\n", a.Day, floor, a.ForksPushedDay, a.ReposRead)
	fmt.Fprintf(w, "   commits: %d, without an Assisted-by: trailer: %d\n", len(a.Commits), a.WithoutTrailer)
	for _, c := range a.Commits {
		t := "no Assisted-by: trailer"
		if c.AssistedBy != "" {
			t = "Assisted-by: " + c.AssistedBy
		}
		fmt.Fprintf(w, "     %s  %s\n", c.SHA, t)
	}

	s := r.Assembly
	fmt.Fprintf(w, "\n3. The assembly\n")
	fmt.Fprintf(w, "   bound: %s\n", s.Bound)
	if len(s.Compared) == 0 {
		fmt.Fprintf(w, "   compared with: none (no other open pull request of this author, no merged one from an outside account within the bound)\n")
	} else {
		parts := make([]string, 0, len(s.Compared))
		for _, c := range s.Compared {
			parts = append(parts, fmt.Sprintf("#%d %s (%s)", c.Number, c.Author, c.Why))
		}
		fmt.Fprintf(w, "   compared with: %s\n", strings.Join(parts, "; "))
	}
	fmt.Fprintf(w, "   Go identifiers declared on one side and present on the other: %d\n", len(s.Links))
	for _, l := range s.Links {
		fmt.Fprintf(w, "     %s: declared by #%d, in #%d at %s:%d\n", l.Identifier, l.DeclaredIn, l.UsedIn, l.File, l.Line)
		fmt.Fprintf(w, "       | %s\n", l.Excerpt)
	}
	for _, u := range s.Unread {
		fmt.Fprintf(w, "   [unread] %s: %s\n", u.File, u.Detail)
	}
	fmt.Fprintf(w, "   files also touched by a compared pull request: %d (listed, not counted as findings)\n", len(s.SharedFiles))
	for _, f := range s.SharedFiles {
		nums := make([]string, 0, len(f.Numbers))
		for _, n := range f.Numbers {
			nums = append(nums, fmt.Sprintf("#%d", n))
		}
		fmt.Fprintf(w, "     %s  with %s\n", f.File, strings.Join(nums, ", "))
	}

	fmt.Fprintf(w, "\nverdict: ")
	if n := r.toRead(); n > 0 {
		fmt.Fprintf(w, "%d things to read first (exit 2). This is where to look, not a judgement of intent.\n", n)
	} else {
		fmt.Fprintf(w, "nothing the rules know (exit 0). The diff still needs reading.\n")
	}
}
