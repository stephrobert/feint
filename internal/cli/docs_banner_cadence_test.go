package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The banner said "384 of the 409 mounted operations are driven by a real
// client, on every pull request: ... and machines really boot: an ssh login on
// each provider's own default account, isolated subnets, a firewall that
// filters."
//
// One sentence, one cadence, two populations. The clients do run on every pull
// request. The machines do not: .github/workflows/runtime-proof.yml is the only
// workflow that installs Incus and OVN, and it triggers on schedule and
// workflow_dispatch. The pull-request path runs `--vm off`, where no machine
// starts at all.
//
// It sat under a "do not edit by hand" marker while being a Go literal, which
// is what docs/conformance.md calls "generated is not derived".
//
// So the cadence is now read off the workflow. This test is the pair of halves:
// a workflow that does run on pull requests must make the banner say so, and
// one that does not must not. Pin the sentence back to a constant and the first
// case fails.
func TestTheBannerReadsTheCadenceOffTheWorkflow(t *testing.T) {
	for _, c := range []struct {
		name string
		on   string
		want string
	}{
		{
			name: "nightly only, which is what this repository has",
			on:   "on:\n  schedule:\n    - cron: '31 2 * * *'\n  workflow_dispatch:\n",
			want: "every night",
		},
		{
			name: "the day it also runs on pull requests, the banner says so",
			on:   "on:\n  pull_request:\n  schedule:\n    - cron: '31 2 * * *'\n",
			want: "on every pull request",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "runtime-proof.yml")
			if err := os.WriteFile(path, []byte("name: p\n"+c.on+"\njobs: {}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := proofCadence(path)
			if err != nil {
				t.Fatalf("read the cadence: %v", err)
			}
			if got != c.want {
				t.Errorf("cadence %q, want %q", got, c.want)
			}
		})
	}

	t.Run("a workflow that triggers on neither is an error, not a guess", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "runtime-proof.yml")
		if err := os.WriteFile(path, []byte("name: p\non:\n  workflow_dispatch:\n\njobs: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := proofCadence(path); err == nil {
			t.Error("a workflow nobody schedules produced a cadence; the banner would state a " +
				"frequency no measurement supports")
		}
	})

	// And the repository's own workflow, which is the one the banner describes.
	t.Run("the committed workflow", func(t *testing.T) {
		root := repoRoot(t)
		path := filepath.Join(root, ".github", "workflows", "runtime-proof.yml")
		got, err := proofCadence(path)
		if err != nil {
			t.Fatalf("read the cadence: %v", err)
		}
		readme, err := os.ReadFile(filepath.Join(root, "README.md"))
		if err != nil {
			t.Fatal(err)
		}
		banner := between(string(readme), "<!-- safety:start -->", "<!-- safety:end -->")
		if banner == "" {
			t.Fatal("README.md has no safety block; the markers moved and this check went blind")
		}
		if !strings.Contains(banner, got) {
			t.Errorf("runtime-proof.yml runs %q and the banner does not say it:\n%s", got, banner)
		}
	})
}
