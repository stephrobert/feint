package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// mergedBound is how many merged pull requests the assembly reads, the most
// recently created first. The report prints it: a payload whose first half was
// merged before them is not seen.
const mergedBound = 50

// closedPages bounds the walk through closed pull requests looking for the
// merged ones, so a repository that closes far more than it merges cannot make
// one review read its whole history.
const closedPages = 10

type assemblyPart struct {
	Bound       string     `json:"bound"`
	MergedRead  int        `json:"merged_read"`
	Compared    []compared `json:"compared"`
	Links       []link     `json:"identifier_links"`
	SharedFiles []shared   `json:"shared_files"`
	Unread      []finding  `json:"unread"`
}

type compared struct {
	Number int    `json:"number"`
	Author string `json:"author"`
	Why    string `json:"why"`
}

// link is a Go identifier declared by one pull request and present in the
// added lines of another.
type link struct {
	Identifier string `json:"identifier"`
	DeclaredIn int    `json:"declared_in"`
	UsedIn     int    `json:"used_in"`
	File       string `json:"file"`
	Line       int    `json:"line"`
	Excerpt    string `json:"excerpt"`
}

type shared struct {
	File    string `json:"file"`
	Numbers []int  `json:"with"`
}

// trusted mirrors tools/ci/sensitive-paths.sh: the owner, a member, a
// collaborator. Every other association, one GitHub adds later included, is
// outside.
func trusted(association string) bool {
	switch association {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	}
	return false
}

type listedPull struct {
	Number            int     `json:"number"`
	MergedAt          *string `json:"merged_at"`
	User              user    `json:"user"`
	AuthorAssociation string  `json:"author_association"`
}

func readAssembly(c *client, repo string, pr *pull, files []prFile) (assemblyPart, error) {
	a := assemblyPart{}
	var others []compared

	open, err := list[listedPull](c, fmt.Sprintf("repos/%s/pulls?state=open&per_page=100", repo))
	if err != nil {
		return a, err
	}
	for _, p := range open {
		if p.Number == 0 || p.User.Login == "" {
			return a, cannot("an open pull request has no number or author")
		}
		if p.Number != pr.Number && strings.EqualFold(p.User.Login, pr.User.Login) {
			others = append(others, compared{Number: p.Number, Author: escape(p.User.Login), Why: "open, same author"})
		}
	}

	merged := 0
	exhausted := false
	for page := 1; page <= closedPages && merged < mergedBound; page++ {
		var ps []listedPull
		if err := c.get(fmt.Sprintf("repos/%s/pulls?state=closed&sort=created&direction=desc&per_page=100&page=%d", repo, page), &ps); err != nil {
			return a, err
		}
		if len(ps) == 0 {
			exhausted = true
			break
		}
		for _, p := range ps {
			if p.Number == 0 || p.User.Login == "" || p.AuthorAssociation == "" {
				return a, cannot("a closed pull request has no number, author or association")
			}
			if p.MergedAt == nil || merged >= mergedBound {
				continue
			}
			merged++
			if p.Number == pr.Number || trusted(p.AuthorAssociation) || p.User.Type == "Bot" {
				continue
			}
			others = append(others, compared{Number: p.Number, Author: escape(p.User.Login), Why: "merged, outside account (" + escape(p.AuthorAssociation) + ")"})
		}
	}
	a.MergedRead = merged
	switch {
	case exhausted:
		a.Bound = fmt.Sprintf("all %d merged pull requests of the repository were read", merged)
	case merged < mergedBound:
		a.Bound = fmt.Sprintf("%d merged pull requests found in the %d most recent pages of closed ones; older merged ones are not read", merged, closedPages)
	default:
		a.Bound = fmt.Sprintf("the %d most recently created merged pull requests; older ones are not read", mergedBound)
	}
	a.Compared = others

	mine := collect(files)
	byFile := map[string][]int{}
	for _, o := range others {
		ofiles, err := c.files(repo, o.Number, nil)
		if err != nil {
			return a, err
		}
		theirs := collect(ofiles)
		for _, u := range theirs.unread {
			a.Unread = append(a.Unread, finding{Rule: "unread", File: escape(u), Detail: fmt.Sprintf("a Go file of #%d whose diff the API does not serve: not compared", o.Number)})
		}
		a.Links = append(a.Links, crossUse(mine, theirs, pr.Number, o.Number)...)
		a.Links = append(a.Links, crossUse(theirs, mine, o.Number, pr.Number)...)
		for f := range theirs.paths {
			if mine.paths[f] {
				byFile[f] = append(byFile[f], o.Number)
			}
		}
	}
	for _, u := range mine.unread {
		a.Unread = append(a.Unread, finding{Rule: "unread", File: escape(u), Detail: "a Go file of this pull request whose diff the API does not serve: not compared"})
	}
	for f, ns := range byFile {
		a.SharedFiles = append(a.SharedFiles, shared{File: escape(f), Numbers: ns})
	}
	sort.Slice(a.SharedFiles, func(i, j int) bool { return a.SharedFiles[i].File < a.SharedFiles[j].File })
	return a, nil
}

// side is what one pull request contributes to the cross-check: the Go
// identifiers its added lines declare at top level, where each word of its
// added Go lines appears, the paths it touches, and the Go files it could not
// be read for.
type side struct {
	declared map[string]bool
	words    map[string][]where
	paths    map[string]bool
	unread   []string
}

type where struct {
	file string
	line int
	text string
}

var (
	declLine  = regexp.MustCompile(`^(?:func\s+(?:\([^)]*\)\s*)?|var\s+|const\s+|type\s+)([A-Za-z_]\w*)`)
	groupOpen = regexp.MustCompile(`^(?:var|const|type)\s*\($`)
	groupName = regexp.MustCompile(`^\t([A-Za-z_]\w*)(?:\s|=|$)`)
	word      = regexp.MustCompile(`[A-Za-z_]\w*`)
)

var goKeywords = map[string]bool{
	"break": true, "case": true, "chan": true, "const": true, "continue": true, "default": true,
	"defer": true, "else": true, "fallthrough": true, "for": true, "func": true, "go": true,
	"goto": true, "if": true, "import": true, "interface": true, "map": true, "package": true,
	"range": true, "return": true, "select": true, "struct": true, "switch": true, "type": true, "var": true,
}

// maxUses bounds how many places one identifier is quoted per pair of pull
// requests, so a common name does not drown the report.
const maxUses = 3

func collect(files []prFile) side {
	s := side{declared: map[string]bool{}, words: map[string][]where{}, paths: map[string]bool{}}
	for _, f := range files {
		s.paths[f.Filename] = true
		if f.PreviousFilename != "" {
			s.paths[f.PreviousFilename] = true
		}
		if !isGo(f.Filename) || f.Status == "removed" {
			continue
		}
		if f.Patch == nil {
			s.unread = append(s.unread, f.Filename)
			continue
		}
		lines, err := parsePatch(*f.Patch)
		if err != nil {
			s.unread = append(s.unread, f.Filename)
			continue
		}
		inGroup := false
		for _, l := range lines {
			// A group opened in one hunk is not known to be open in the
			// next: the lines between them are not in the patch.
			if l.kind == '-' || l.kind == '@' {
				inGroup = inGroup && l.kind == '-'
				continue
			}
			switch {
			case groupOpen.MatchString(l.text):
				inGroup = true
				continue
			case inGroup && strings.HasPrefix(l.text, ")"):
				inGroup = false
				continue
			}
			if l.kind != '+' {
				continue
			}
			if m := declLine.FindStringSubmatch(l.text); m != nil {
				s.declared[m[1]] = true
			} else if m := groupName.FindStringSubmatch(l.text); inGroup && m != nil {
				s.declared[m[1]] = true
			}
			code, _, _ := strings.Cut(l.text, "//") // a word in a comment is not a use
			for _, w := range word.FindAllString(code, -1) {
				s.words[w] = append(s.words[w], where{file: f.Filename, line: l.n, text: l.text})
			}
		}
	}
	for n := range s.declared {
		if n == "_" || n == "init" || n == "main" || goKeywords[n] {
			delete(s.declared, n)
		}
	}
	return s
}

func crossUse(decl, use side, declNum, useNum int) []link {
	names := make([]string, 0, len(decl.declared))
	for n := range decl.declared {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []link
	for _, n := range names {
		for i, w := range use.words[n] {
			if i == maxUses {
				break
			}
			out = append(out, link{Identifier: n, DeclaredIn: declNum, UsedIn: useNum, File: escape(w.file), Line: w.line, Excerpt: excerpt(w.text)})
		}
	}
	return out
}
