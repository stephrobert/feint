package main

import (
	"fmt"
	"regexp"
	"strings"
)

// accountPart holds facts about the author. None of them changes the exit
// code: an old account can be hostile and a new one honest, so the reader
// weighs them, the program does not.
type accountPart struct {
	Login           string       `json:"login"`
	Type            string       `json:"type"`
	CreatedAt       string       `json:"created_at"`
	HasName         bool         `json:"has_name"`
	HasBio          bool         `json:"has_bio"`
	Followers       int          `json:"followers"`
	PublicRepos     int          `json:"public_repos"`
	Day             string       `json:"day"`
	PRsTotal        count        `json:"pull_requests_total"`
	PRsThatDay      count        `json:"pull_requests_that_day"`
	ForksPushedDay  int          `json:"forks_last_pushed_that_day"`
	ReposRead       int          `json:"repositories_read"`
	ForksLowerBound bool         `json:"forks_count_is_a_lower_bound"`
	Commits         []commitFact `json:"commits"`
	WithoutTrailer  int          `json:"commits_without_assisted_by"`
}

// count is a search total. GitHub marks a search it did not finish as
// incomplete; the total is then at least this much, and the report says so
// instead of printing it as exact.
type count struct {
	Total      int  `json:"total"`
	Incomplete bool `json:"incomplete"`
}

type commitFact struct {
	SHA        string `json:"sha"`
	AssistedBy string `json:"assisted_by,omitempty"`
}

// reposRead is how many of the author's repositories, most recently pushed
// first, are read to count the forks pushed on the day of the pull request.
const reposRead = 100

// assistedBy is a git trailer: a line of its own, key first. Git reads trailer
// keys without regard to case.
var assistedBy = regexp.MustCompile(`(?im)^assisted-by:[ \t]*(.+)$`)

func readAccount(c *client, repo string, pr *pull) (accountPart, error) {
	login := pr.User.Login // shape checked by client.pull before it reaches a path
	a := accountPart{Login: escape(login), Day: pr.CreatedAt[:10]}

	var u struct {
		Login       string  `json:"login"`
		Type        string  `json:"type"`
		CreatedAt   string  `json:"created_at"`
		Name        *string `json:"name"`
		Bio         *string `json:"bio"`
		Followers   *int    `json:"followers"`
		PublicRepos *int    `json:"public_repos"`
	}
	if err := c.get("users/"+login, &u); err != nil {
		return a, err
	}
	if !strings.EqualFold(u.Login, login) || u.CreatedAt == "" || u.Followers == nil || u.PublicRepos == nil {
		return a, cannot("the account %s has a shape this program does not know", escape(login))
	}
	a.Type, a.CreatedAt = escape(u.Type), escape(u.CreatedAt)
	a.HasName = u.Name != nil && strings.TrimSpace(*u.Name) != ""
	a.HasBio = u.Bio != nil && strings.TrimSpace(*u.Bio) != ""
	a.Followers, a.PublicRepos = *u.Followers, *u.PublicRepos

	var err error
	if a.PRsTotal, err = search(c, "author:"+login+"+type:pr"); err != nil {
		return a, err
	}
	if a.PRsThatDay, err = search(c, "author:"+login+"+type:pr+created:"+a.Day); err != nil {
		return a, err
	}

	var repos []struct {
		Fork     *bool  `json:"fork"`
		PushedAt string `json:"pushed_at"`
	}
	if err := c.get(fmt.Sprintf("users/%s/repos?sort=pushed&direction=desc&per_page=%d", login, reposRead), &repos); err != nil {
		return a, err
	}
	for _, r := range repos {
		if r.Fork == nil {
			return a, cannot("a repository of %s has no fork flag", escape(login))
		}
		if *r.Fork && strings.HasPrefix(r.PushedAt, a.Day) {
			a.ForksPushedDay++
		}
	}
	a.ReposRead = len(repos)
	// Every repository read was a fork pushed that day: the next page may
	// hold more, so the count is a floor.
	a.ForksLowerBound = len(repos) == reposRead && a.ForksPushedDay == reposRead

	commits, err := list[struct {
		SHA    string `json:"sha"`
		Commit *struct {
			Message *string `json:"message"`
		} `json:"commit"`
	}](c, fmt.Sprintf("repos/%s/pulls/%d/commits?per_page=100", repo, pr.Number))
	if err != nil {
		return a, err
	}
	if len(commits) != *pr.Commits {
		return a, cannot("the API listed %d commits of pull request %d and the pull request announces %d", len(commits), pr.Number, *pr.Commits)
	}
	for _, cm := range commits {
		if !shaShape.MatchString(cm.SHA) || cm.Commit == nil || cm.Commit.Message == nil {
			return a, cannot("a commit of pull request %d has a shape this program does not know", pr.Number)
		}
		f := commitFact{SHA: cm.SHA[:12]}
		if m := assistedBy.FindStringSubmatch(*cm.Commit.Message); m != nil {
			f.AssistedBy = truncate(escape(strings.TrimSpace(m[1])), 80)
		} else {
			a.WithoutTrailer++
		}
		a.Commits = append(a.Commits, f)
	}
	return a, nil
}

func search(c *client, q string) (count, error) {
	var s struct {
		Total      *int  `json:"total_count"`
		Incomplete *bool `json:"incomplete_results"`
	}
	if err := c.get("search/issues?q="+q+"&per_page=1", &s); err != nil {
		return count{}, err
	}
	if s.Total == nil || s.Incomplete == nil {
		return count{}, cannot("the search %s answered without total_count or incomplete_results", escape(q))
	}
	return count{Total: *s.Total, Incomplete: *s.Incomplete}, nil
}
