package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// client runs `gh api`. bin is the gh binary; env, when set, replaces the
// environment of each call (the tests point it at a stub and its fixtures).
type client struct {
	bin string
	env []string
}

// callTimeout bounds one gh call. A paginated list of a few pages answers in
// seconds; a call still running after this is not going to answer.
const callTimeout = 2 * time.Minute

var (
	shaShape   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	loginShape = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})(?:\[bot\])?$`)
)

// gh runs one gh command and returns its standard output. A non-zero exit is
// a read that failed, and it is never mistaken for an empty answer.
func (c *client) gh(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, args...) //nolint:gosec // gh, resolved on PATH by main; the arguments are built here from validated values
	if c.env != nil {
		cmd.Env = c.env
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, cannot("gh %s failed (%v): %s", strings.Join(args, " "), err, lastLine(errb.String()))
	}
	return out.Bytes(), nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// get reads one answer and decodes it into v.
func (c *client) get(path string, v any) error {
	raw, err := c.gh("api", path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return cannot("the answer to %s is not the JSON expected (%v)", path, err)
	}
	return nil
}

// list reads every page of a list endpoint. --slurp makes gh print one array
// of pages; each page must itself be an array, or the shape is unknown.
func list[T any](c *client, path string) ([]T, error) {
	raw, err := c.gh("api", "--paginate", "--slurp", path)
	if err != nil {
		return nil, err
	}
	var pages [][]T
	if err := json.Unmarshal(raw, &pages); err != nil {
		return nil, cannot("the pages of %s are not the JSON expected (%v)", path, err)
	}
	var all []T
	for _, p := range pages {
		all = append(all, p...)
	}
	return all, nil
}

func (c *client) currentRepo() (string, error) {
	raw, err := c.gh("repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

type user struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type pull struct {
	Number            int     `json:"number"`
	Title             string  `json:"title"`
	State             string  `json:"state"`
	CreatedAt         string  `json:"created_at"`
	MergedAt          *string `json:"merged_at"`
	User              user    `json:"user"`
	AuthorAssociation string  `json:"author_association"`
	Head              struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		SHA string `json:"sha"`
	} `json:"base"`
	ChangedFiles *int `json:"changed_files"`
	Commits      *int `json:"commits"`
}

func (c *client) pull(repo string, number int) (*pull, error) {
	var p pull
	if err := c.get(fmt.Sprintf("repos/%s/pulls/%d", repo, number), &p); err != nil {
		return nil, err
	}
	switch {
	case p.Number != number:
		return nil, cannot("the API answered pull request %d when %d was asked", p.Number, number)
	case !loginShape.MatchString(p.User.Login):
		return nil, cannot("pull request %d has no author login this program can use (%q)", number, escape(p.User.Login))
	case !shaShape.MatchString(p.Head.SHA) || !shaShape.MatchString(p.Base.SHA):
		return nil, cannot("pull request %d has no 40-character head and base SHA", number)
	case p.ChangedFiles == nil || p.Commits == nil:
		return nil, cannot("pull request %d does not announce its changed_files and commits counts", number)
	case len(p.CreatedAt) < len("2006-01-02"):
		return nil, cannot("pull request %d has no creation date", number)
	}
	return &p, nil
}

type prFile struct {
	Filename         string  `json:"filename"`
	PreviousFilename string  `json:"previous_filename"`
	Status           string  `json:"status"`
	Additions        int     `json:"additions"`
	Deletions        int     `json:"deletions"`
	Changes          int     `json:"changes"`
	Patch            *string `json:"patch"`
}

// files reads the changed files of a pull request. want is the count the pull
// request announces, or nil when the caller has none (a list entry carries no
// count): the files endpoint stops at 3000 entries, and a head that moves
// between two calls changes the list, so a list shorter than announced may be
// missing exactly the file that matters.
func (c *client) files(repo string, number int, want *int) ([]prFile, error) {
	fs, err := list[prFile](c, fmt.Sprintf("repos/%s/pulls/%d/files?per_page=100", repo, number))
	if err != nil {
		return nil, err
	}
	for _, f := range fs {
		if f.Filename == "" || f.Status == "" {
			return nil, cannot("a changed file of pull request %d has no filename or status", number)
		}
	}
	if want != nil && len(fs) != *want {
		return nil, cannot("the API listed %d changed files of pull request %d and the pull request announces %d", len(fs), number, *want)
	}
	return fs, nil
}

// modes reads the git modes of a commit's tree, path by path. A truncated tree
// is a list with holes, so it cannot be judged.
func (c *client) modes(repo, sha string) (map[string]string, error) {
	var t struct {
		Truncated *bool `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
		} `json:"tree"`
	}
	path := fmt.Sprintf("repos/%s/git/trees/%s?recursive=1", repo, sha)
	if err := c.get(path, &t); err != nil {
		return nil, err
	}
	if t.Truncated == nil || t.Tree == nil {
		return nil, cannot("the tree of %s has no truncated flag or no entries", sha)
	}
	if *t.Truncated {
		return nil, cannot("the tree of %s is truncated by the API, so file modes cannot be read", sha)
	}
	m := make(map[string]string, len(t.Tree))
	for _, e := range t.Tree {
		m[e.Path] = e.Mode
	}
	return m, nil
}

func writeJSON(w interface{ Write([]byte) (int, error) }, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
