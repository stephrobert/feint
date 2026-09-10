package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// maxPages caps the pagination walk. 100 releases per page, so this is 10 000
// releases: far beyond any project this serves, and a bound is what keeps a
// misbehaving API from turning a scheduled job into an unbounded loop.
const maxPages = 100

// apiBase is a variable rather than a constant so the pagination walk and the
// failure paths can be driven against httptest. A collector whose only proof is
// "it worked against github.com once" has no test for the page-100 boundary or
// for a 503, which are exactly the two that matter on a scheduled job.
var apiBase = "https://api.github.com"

// FetchReleases reads every release of a repository, following pagination.
//
// The token is optional: the endpoint is public and unauthenticated calls work,
// but they are rate-limited to 60 per hour per IP, which a shared CI runner
// exhausts. GITHUB_TOKEN raises that to 1 000 for the repository.
func FetchReleases(repo string) ([]Release, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	var all []Release

	for page := 1; page <= maxPages; page++ {
		url := fmt.Sprintf("%s/repos/%s/releases?per_page=100&page=%d", apiBase, repo, page)
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("User-Agent", "feint-adoption-metrics")
		if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}

		resp, err := client.Do(req)
		if err != nil {
			// The API being unreachable must fail the run rather than write a
			// snapshot of what was collected so far: a partial snapshot is
			// indistinguishable from a collapse in downloads once it is in the
			// CSV, and the CSV is append-only.
			return nil, fmt.Errorf("GET %s: %w", url, err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GET %s: %s: %s", url, resp.Status, snippet(body))
		}
		if readErr != nil {
			return nil, fmt.Errorf("GET %s: %w", url, readErr)
		}

		var batch []Release
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, fmt.Errorf("GET %s: %w", url, err)
		}
		if len(batch) == 0 {
			return all, nil
		}
		all = append(all, batch...)
	}
	return nil, fmt.Errorf("%s has more than %d pages of releases, which is not credible; refusing a partial read", repo, maxPages)
}

func snippet(b []byte) string {
	const max = 200
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}
