package ci

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fetch.sh survives a transient outage and refuses to sit on a permanent one.
//
// The nightly conformance run of 2026-09-25 died on `curl: (22) ... 504`
// installing the Scaleway CLI, from a step that already said `--retry 3`. The
// gap was never the retry — measured against a local server, `--retry 3` does
// send four requests on a 504 — it was the seven seconds they span, against an
// outage measured at roughly twenty minutes on 2026-09-14.
//
// These tests drive the real script. The budget is lowered through the
// environment so they run in seconds; what is asserted is the behaviour, and
// the defaults live in the script with the measurement that sized them.

// runFetch drives the script against a URL and answers the exit code.
func runFetch(t *testing.T, url, dest string, attempts, delay int) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join("fetch.sh"), url, dest)
	cmd.Env = append(os.Environ(),
		"FEINT_FETCH_ATTEMPTS="+strconv.Itoa(attempts),
		"FEINT_FETCH_DELAY="+strconv.Itoa(delay),
		"FEINT_FETCH_MAX_TIME=30",
	)
	out, err := cmd.CombinedOutput()
	code := 0
	var exit *exec.ExitError
	if err != nil {
		if ok := asExitError(err, &exit); ok {
			code = exit.ExitCode()
		} else {
			t.Fatalf("run fetch.sh: %v", err)
		}
	}
	return code, string(out)
}

func asExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok { //nolint:errorlint // the concrete type is the subject
		*target = e
		return true
	}
	return false
}

// An asset that answers 504 and then recovers is fetched, not abandoned.
//
// This is the night of 2026-09-25 replayed: the first requests fail with the
// gateway's own error, and the payload is there a few seconds later.
func TestFetchSurvivesATransientOutage(t *testing.T) {
	var seen atomic.Int32
	const failures = 3
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if seen.Add(1) <= failures {
			w.WriteHeader(http.StatusGatewayTimeout)
			return
		}
		fmt.Fprint(w, "the asset")
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "asset")
	code, out := runFetch(t, srv.URL+"/asset", dest, 6, 1)
	if code != 0 {
		t.Fatalf("fetch.sh gave up on an outage that ended: exit %d\n%s", code, out)
	}
	if got := int(seen.Load()); got <= failures {
		t.Fatalf("the server saw %d request(s) for %d failure(s): the retries did not happen",
			got, failures)
	}
	body, err := os.ReadFile(dest) //nolint:gosec // a path this test made
	if err != nil {
		t.Fatalf("read what was fetched: %v", err)
	}
	if string(body) != "the asset" {
		t.Errorf("fetched %q, want the payload the server answered once it recovered", body)
	}
}

// An outage that does not end fails, rather than hanging on a runner.
//
// The budget is a budget: a gate that waits forever is a gate nobody can read
// the log of, and the point of #731 is that a night has to end saying something.
func TestFetchGivesUpOnAnOutageThatDoesNotEnd(t *testing.T) {
	var seen atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		seen.Add(1)
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer srv.Close()

	start := time.Now()
	code, _ := runFetch(t, srv.URL+"/asset", filepath.Join(t.TempDir(), "asset"), 4, 1)
	if code == 0 {
		t.Fatal("fetch.sh reported success against a server that never recovered")
	}
	if elapsed := time.Since(start); elapsed > 25*time.Second {
		t.Errorf("it took %s to give up; the max-time budget is not holding", elapsed)
	}
	if got := int(seen.Load()); got < 2 {
		t.Errorf("the server saw %d request(s): it gave up without retrying at all", got)
	}
}

// A 404 is a pin naming something that does not exist, and is not retried.
//
// Retrying it would turn a clear error — the asset is not there, fix the
// version — into the same slow red as a real outage, and the log would say the
// same thing for two different problems.
func TestFetchDoesNotRetryAMissingAsset(t *testing.T) {
	var seen atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		seen.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	start := time.Now()
	code, _ := runFetch(t, srv.URL+"/nope", filepath.Join(t.TempDir(), "asset"), 6, 2)
	if code == 0 {
		t.Fatal("fetch.sh reported success on a 404")
	}
	if got := int(seen.Load()); got != 1 {
		t.Errorf("the server saw %d request(s) for a 404: a missing asset is not an outage, "+
			"and retrying it makes a clear error look like a slow one", got)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("a 404 took %s to report; it should be immediate", elapsed)
	}
}

// Nothing is written when the fetch fails.
//
// A zero-byte file left behind is worse than none: the checksum step that
// follows would compare against it and fail with a confusing message, or an
// install step would put an empty binary on the PATH.
func TestFetchLeavesNothingBehindWhenItFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "asset")
	if code, _ := runFetch(t, srv.URL+"/nope", dest, 2, 1); code == 0 {
		t.Fatal("fetch.sh reported success on a 404")
	}
	if _, err := os.Stat(dest); err == nil {
		t.Error("a file was left at the destination after a failed fetch: the checksum step " +
			"that follows would compare against it")
	}
}

// No workflow downloads a release asset behind the helper's back.
//
// Without this, the next install step written in this repository gets the same
// seven-second budget the old ones had, and the fix lasts exactly until someone
// adds a tool. The measurement that sized the budget lives in fetch.sh; a curl
// written next to it does not read that file.
//
// A curl reaching the local emulator is a different thing and stays allowed:
// what is refused is fetching something over the network into a file.
func TestNoWorkflowFetchesAnAssetWithoutTheHelper(t *testing.T) {
	dir := filepath.Join("..", "..", ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read workflows: %v", err)
	}

	found := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".yml" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, entry.Name())) //nolint:gosec // a path from our own walk
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") || !strings.Contains(trimmed, "curl ") {
				continue
			}
			// Reading the emulator this repository just started is not a fetch.
			if strings.Contains(trimmed, "127.0.0.1") || strings.Contains(trimmed, "localhost") {
				continue
			}
			if !strings.Contains(trimmed, "-o ") && !strings.Contains(trimmed, "--output") {
				continue
			}
			found++
			t.Errorf("%s:%d downloads with curl instead of tools/ci/fetch.sh, so it keeps the "+
				"seven-second budget that failed the night of 2026-09-25:\n    %s",
				entry.Name(), i+1, trimmed)
		}
	}
	if found == 0 {
		t.Log("every workflow download goes through the helper")
	}
}

// The defaults are the measured ones, and lowering them is a decision a test
// has to refuse.
//
// Every test above drives the script with the budget lowered through the
// environment, so they run in seconds — and none of them touches the default.
// Falsify showed what that costs: setting `attempts=4`, the exact value that
// failed the night of 2026-09-25, left all of them green.
//
// So the threshold is asserted here, and it is the calculation rather than the
// file: at the 67% failure rate measured on 2026-09-14, four attempts leave a
// 19.8% chance of failing anyway and eight bring it under 4%. Eight is the
// floor; fetch.sh chooses ten.
func TestTheFetchDefaultsAreTheMeasuredOnes(t *testing.T) {
	body, err := os.ReadFile("fetch.sh")
	if err != nil {
		t.Fatalf("read fetch.sh: %v", err)
	}
	script := string(body)

	defaults := map[string]int{
		"FEINT_FETCH_ATTEMPTS": 0,
		"FEINT_FETCH_MAX_TIME": 0,
	}
	for name := range defaults {
		marker := "${" + name + ":-"
		at := strings.Index(script, marker)
		if at < 0 {
			t.Fatalf("fetch.sh declares no default for %s", name)
		}
		rest := script[at+len(marker):]
		end := strings.IndexByte(rest, '}')
		if end < 0 {
			t.Fatalf("the default for %s is not closed", name)
		}
		value, convErr := strconv.Atoi(rest[:end])
		if convErr != nil {
			t.Fatalf("the default for %s is %q, not a number", name, rest[:end])
		}
		defaults[name] = value
	}

	// Eight attempts is where the measured outage stops being likely to win.
	if got := defaults["FEINT_FETCH_ATTEMPTS"]; got < 8 {
		t.Errorf("the default is %d attempts: at the 67%% failure rate measured on 2026-09-14 "+
			"that leaves a %.0f%% chance of a red night, and the whole point of this script "+
			"is that four attempts already failed", got, 100*pow(2.0/3.0, got))
	}
	// And it ends: a gate that waits forever produces a log nobody can read.
	if got := defaults["FEINT_FETCH_MAX_TIME"]; got <= 0 || got > 600 {
		t.Errorf("the retry ceiling is %ds: it must be finite so a night ends with a verdict, "+
			"and short enough that a runner is not held for the whole outage", got)
	}
}

func pow(base float64, n int) float64 {
	out := 1.0
	for range n {
		out *= base
	}
	return out
}
