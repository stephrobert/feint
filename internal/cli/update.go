package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Whether a newer release exists, asked for rather than volunteered.
//
// Every emulator that phones home on start does it for its own benefit, and this
// project's roadmap puts "telemetry, or an account. Ever." under Not planned. So
// the check is a command a user types. Nothing runs it in the background, no
// timer, no cache file written on a start nobody asked to be measured, and the
// first line of `feint version` stays what it always was.
//
// What travels: a plain GET to the GitHub releases API. GitHub sees an IP and a
// user agent, which is what fetching any URL costs; feint sends no identifier, no
// configuration, no usage. `--check` is opt-in per invocation, and
// FEINT_NO_UPDATE_CHECK=1 refuses it outright for anyone scripting this binary
// somewhere it must not reach the network.
//
// It never fails the caller. A version check that breaks a CI job because
// api.github.com was slow would be a worse defect than the staleness it reports.

const (
	// releasesAPI is the endpoint, formatted with the repository slug read from
	// go.mod at build time is not possible — the binary has no source tree — so
	// the slug is the module path this binary was built from.
	releasesAPI = "https://api.github.com/repos/%s/releases/latest"
	// updateSlug is the repository this binary is released from. It matches the
	// module path in go.mod; docs_release.go reads that file for the same value
	// when it generates the install commands.
	updateSlug = "stephrobert/feint"
	// updateTimeout bounds the whole exchange. A user waiting on a CLI has a few
	// seconds of patience, and a hung check must not become a hung command.
	updateTimeout = 5 * time.Second
)

// githubRelease is the sliver of the API answer this needs. Everything else is
// deliberately not decoded: a reader of a format that takes less of it is a
// reader that keeps working when the writer grows a field.
type githubRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

// versionCheck is `feint version --check`.
func versionCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	check := fs.Bool("check", false, "ask GitHub whether a newer release exists")
	if err := fs.Parse(args); err != nil {
		return exitError
	}

	fmt.Fprintln(stdout, released())
	if !*check {
		return exitOK
	}

	if os.Getenv("FEINT_NO_UPDATE_CHECK") != "" {
		fmt.Fprintln(stdout, "update check disabled by FEINT_NO_UPDATE_CHECK")
		return exitOK
	}

	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	defer cancel()

	latest, err := latestRelease(ctx, http.DefaultClient, fmt.Sprintf(releasesAPI, updateSlug))
	if err != nil {
		// Reported, never fatal: not knowing is not the same as being current,
		// and the exit code belongs to the command the user actually ran.
		fmt.Fprintf(stderr, "feint: could not check for a newer release: %v\n", err)
		return exitOK
	}

	fmt.Fprint(stdout, updateMessage(Version, latest))
	return exitOK
}

// latestRelease asks for the newest published release.
func latestRelease(ctx context.Context, client *http.Client, url string) (githubRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return githubRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "feint/"+Version)

	resp, err := client.Do(req)
	if err != nil {
		return githubRelease{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// 404 is the ordinary answer for a repository with no release yet, and
		// 403 is the rate limit an unauthenticated caller meets. Both are worth
		// telling apart from a parse failure.
		return githubRelease{}, fmt.Errorf("the releases API answered %s", resp.Status)
	}
	// Bounded like every other body this project reads: an answer that is not a
	// release document must not be able to fill memory.
	var out githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return githubRelease{}, fmt.Errorf("the releases API answered something that is not a release: %w", err)
	}
	if out.TagName == "" {
		return githubRelease{}, errors.New("the releases API answered a release with no tag")
	}
	return out, nil
}

// updateMessage is what the user reads. Separated from the fetch so the wording
// is testable without a network.
func updateMessage(current string, latest githubRelease) string {
	newer, comparable := isNewer(current, latest.TagName)
	switch {
	case !comparable:
		// A binary built from source carries "dev", and a pseudo-version from
		// `go install` carries a commit. Neither compares to a tag, and claiming
		// either is out of date would be a guess.
		return fmt.Sprintf("the latest release is %s (this build reports %q, which is not a released version)\n"+
			"  %s\n", latest.TagName, current, latest.HTMLURL)
	case newer:
		var b strings.Builder
		fmt.Fprintf(&b, "a newer release is available: %s\n\n", latest.TagName)
		b.WriteString(updateCommand(latest.TagName))
		fmt.Fprintf(&b, "\n  release notes: %s\n", latest.HTMLURL)
		return b.String()
	default:
		return "this is the latest release\n"
	}
}

// updateCommand is the command to run, pinned to the version being installed.
//
// The same shape the install page generates, and pinned for the same reason: a
// mutable reference downloads whatever is newest, which cannot be checked against
// a hash. Told rather than run — a CLI that updates itself is a CLI that rewrites
// the binary somebody verified.
func updateCommand(tag string) string {
	return fmt.Sprintf(`  base=https://github.com/%s/releases/download/%s
  curl -fsSLO "$base/feint-$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')"
  curl -fsSLO "$base/checksums.txt"
  sha256sum -c checksums.txt --ignore-missing
`, updateSlug, tag)
}

// isNewer compares two semantic versions, reporting whether the comparison meant
// anything. Written here rather than pulled in: three integers and a split is not
// worth the first external dependency this module would ever have.
func isNewer(current, latest string) (newer, comparable bool) {
	c, okC := semver(current)
	l, okL := semver(latest)
	if !okC || !okL {
		return false, false
	}
	for i := range c {
		if l[i] != c[i] {
			return l[i] > c[i], true
		}
	}
	return false, true
}

// semver parses vX.Y.Z, with or without the v, and refuses anything else —
// including a pre-release suffix, which this project does not publish and would
// order wrongly if guessed at.
func semver(s string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
