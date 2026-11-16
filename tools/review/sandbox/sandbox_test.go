// Package sandbox tests tools/review/sandbox.sh (#988, item 4): the command
// that runs the tests of a pull request from outside the project in a
// throwaway Incus container instead of on the maintainer's station.
//
// None of these tests needs Incus. They run the real script against a real
// throwaway git repository (an origin, a main, a refs/pull/7/head) and a
// stand-in `incus` (testdata/incus) that records every call and keeps just
// enough state to answer. What they prove is the logic of the script: what it
// refuses, in which order it acts, what it hands to incus, and that it stops
// the box. That the real runtime behaves as the stand-in says was measured by
// hand on 2026-11-16 and is recorded in the pull request; it is not proved here.
//
// Each guard has a mutation in
// tools/falsify/specs/a-pull-request-runs-only-in-a-box-with-no-way-out.json.
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stephrobert/feint/internal/hermetic"
)

func TestMain(m *testing.M) {
	hermetic.DropInheritedGit()
	os.Exit(m.Run())
}

type call []string

func (c call) joined() string { return strings.Join(c, " ") }

type result struct {
	code    int
	stdout  string
	stderr  string
	calls   []call
	elapsed time.Duration
	fake    string
}

func (r result) callsTo(sub string) []call {
	var out []call
	for _, c := range r.calls {
		if len(c) > 0 && c[0] == sub {
			out = append(out, c)
		}
	}
	return out
}

// index is the position of the first call whose line contains all of parts, or -1.
func (r result) index(parts ...string) int {
	for i, c := range r.calls {
		line := c.joined()
		ok := true
		for _, p := range parts {
			if !strings.Contains(line, p) {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

type fixture struct {
	t      *testing.T
	repo   string
	fake   string
	bin    string
	goroot string
}

func sh(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v in %s: %v\n%s", name, args, dir, err, out)
	}
	return string(out)
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Dir(to), filepath.Base(to), string(b))
}

const (
	goMod       = "module fixture\n\ngo 1.26\n"
	goModDeps   = "module fixture\n\ngo 1.26\n\nrequire golang.org/x/text v0.44.0\n"
	miseToml    = "[tools]\ngo = \"1.26.11\"\n"
	aGo         = "package a\n\nfunc One() int { return 1 }\n"
	pinnedGoVer = "go1.26.11"
)

type spec struct {
	base map[string]string // files of main, besides the defaults
	pr   func(t *testing.T, repo string)
	// goVersion is what the stand-in GOROOT says it is.
	goVersion string
}

// newFixture builds an origin with main and refs/pull/7/head, a clone that
// carries the script under test, and the stand-ins.
func newFixture(t *testing.T, s spec) *fixture {
	t.Helper()
	tmp := t.TempDir()
	origin := filepath.Join(tmp, "origin.git")
	repo := filepath.Join(tmp, "repo")
	sh(t, tmp, "git", "init", "-q", "--bare", "-b", "main", origin)
	sh(t, tmp, "git", "clone", "-q", origin, repo)
	for _, kv := range [][2]string{{"user.name", "t"}, {"user.email", "t@example.invalid"}, {"commit.gpgsign", "false"}} {
		sh(t, repo, "git", "config", kv[0], kv[1])
	}
	base := map[string]string{
		"mise.toml":     miseToml,
		"go.mod":        goMod,
		"a/a.go":        aGo,
		"gone/x.go":     "package gone\n",
		"docs/README":   "base\n",
		"main.go":       "package main\n\nfunc main() {}\n",
		"internal/b.go": "package internal\n",
	}
	for k, v := range s.base {
		base[k] = v
	}
	for k, v := range base {
		write(t, repo, k, v)
	}
	sh(t, repo, "git", "add", "-A")
	sh(t, repo, "git", "commit", "-q", "-m", "base")
	sh(t, repo, "git", "push", "-q", "origin", "HEAD:main")
	sh(t, repo, "git", "checkout", "-q", "-b", "pr")
	if s.pr != nil {
		s.pr(t, repo)
	} else {
		write(t, repo, "a/new_test.go", "package a\n")
	}
	sh(t, repo, "git", "add", "-A")
	sh(t, repo, "git", "commit", "-q", "--allow-empty", "-m", "pr")
	sh(t, repo, "git", "push", "-q", "origin", "HEAD:refs/pull/7/head")
	sh(t, repo, "git", "checkout", "-q", "main")

	// The script under test, and what it needs beside it. Untracked on purpose:
	// the pull request's archive must not depend on them.
	here, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	copyFile(t, filepath.Join(here, "..", "sandbox.sh"), filepath.Join(repo, "tools", "review", "sandbox.sh"))
	copyFile(t, filepath.Join(here, "escape.py"), filepath.Join(repo, "tools", "review", "sandbox", "escape.py"))

	f := &fixture{t: t, repo: repo, fake: filepath.Join(tmp, "fake"), bin: filepath.Join(tmp, "bin"), goroot: filepath.Join(tmp, "goroot")}
	if err := os.MkdirAll(f.fake, 0o755); err != nil {
		t.Fatal(err)
	}
	copyFile(t, filepath.Join(here, "testdata", "incus"), filepath.Join(f.bin, "incus"))
	copyFile(t, filepath.Join(here, "testdata", "go"), filepath.Join(f.bin, "go"))
	v := s.goVersion
	if v == "" {
		v = pinnedGoVer
	}
	write(t, f.goroot, "VERSION", v+"\ntime 2026-01-01\n")
	write(t, f.goroot, "bin/go", "#!/bin/sh\n")
	return f
}

type opts struct {
	env     []string
	args    []string
	signal  syscall.Signal // sent once the test stage has started, if non-zero
	maxWait time.Duration
}

func (f *fixture) run(o opts) result {
	f.t.Helper()
	if err := os.RemoveAll(f.fake); err != nil {
		f.t.Fatal(err)
	}
	if err := os.MkdirAll(f.fake, 0o755); err != nil {
		f.t.Fatal(err)
	}
	wait := o.maxWait
	if wait == 0 {
		wait = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	// The variables go through env(1) rather than cmd.Env: the process keeps the
	// environment TestMain scrubbed (no GIT_*), and env(1) execs the script.
	argv := []string{
		"PATH=" + f.bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_DIR=" + f.fake, "FAKE_GOROOT=" + f.goroot, "LC_ALL=C.UTF-8",
	}
	argv = append(argv, o.env...)
	argv = append(argv, "bash", "--noprofile", "--norc", filepath.Join(f.repo, "tools", "review", "sandbox.sh"))
	argv = append(argv, o.args...)
	cmd := exec.CommandContext(ctx, "env", argv...)
	cmd.Dir = f.repo
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	// A killed script can leave a child (the stand-in's sleep) holding the pipes:
	// the script's own end is what is measured, not its orphans'.
	cmd.WaitDelay = time.Second
	start := time.Now()
	if err := cmd.Start(); err != nil {
		f.t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if o.signal != 0 {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			b, _ := os.ReadFile(filepath.Join(f.fake, "calls.log"))
			if bytes.Contains(b, []byte("REVIEW_STAGE=test")) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		time.Sleep(300 * time.Millisecond)
		_ = cmd.Process.Signal(o.signal)
	}
	err := <-done
	code := 0
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.Is(err, exec.ErrWaitDelay):
		code = cmd.ProcessState.ExitCode()
	case errors.As(err, &ee):
		code = ee.ExitCode()
	default:
		f.t.Fatal(err)
	}
	if ctx.Err() != nil {
		f.t.Fatalf("the script did not end within %s", wait)
	}
	r := result{code: code, stdout: so.String(), stderr: se.String(), elapsed: time.Since(start), fake: f.fake}
	if b, err := os.ReadFile(filepath.Join(f.fake, "calls.log")); err == nil {
		for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
			if line != "" {
				r.calls = append(r.calls, strings.Split(line, "\t"))
			}
		}
	}
	f.afterEveryRun(r)
	return r
}

var instanceName = regexp.MustCompile(`^review-7-[0-9]{9,11}-[0-9a-f]{16}$`)

// afterEveryRun holds what is true of every run, whatever it was about: the
// script never deletes, and every instance it names is the one it launched.
func (f *fixture) afterEveryRun(r result) {
	f.t.Helper()
	launched := ""
	for _, c := range r.callsTo("launch") {
		if len(c) > 2 {
			launched = c[2]
		}
	}
	for _, c := range r.calls {
		if len(c) > 0 && (c[0] == "delete" || c[0] == "rm") {
			f.t.Errorf("the script issued a delete: %s", c.joined())
		}
		for _, a := range c[1:] {
			if !strings.HasPrefix(a, "review-") {
				continue
			}
			n, _, _ := strings.Cut(a, "/")
			if n != launched {
				f.t.Errorf("a call names the instance %q, which this run did not launch (%q): %s", n, launched, c.joined())
			}
		}
	}
	if launched != "" && !instanceName.MatchString(launched) {
		f.t.Errorf("the container name %q does not have the shape review-<n>-<epoch>-<16 hex>", launched)
	}
}

func (r result) failf(t *testing.T, format string, a ...any) {
	t.Helper()
	t.Fatalf(format+"\nexit %d\nstderr:\n%s\nstdout:\n%s\ncalls:\n%s", append(a, r.code, r.stderr, r.stdout, r.dump())...)
}

func (r result) dump() string {
	var b strings.Builder
	for _, c := range r.calls {
		b.WriteString("  " + c.joined() + "\n")
	}
	return b.String()
}

func (r result) refused(t *testing.T, msg string) {
	t.Helper()
	if r.code != 1 || !strings.Contains(r.stderr, msg) {
		r.failf(t, "want exit 1 with %q on stderr", msg)
	}
}

func (r result) wasStopped(t *testing.T) {
	t.Helper()
	launch := r.index("launch")
	stop := r.index("stop --force review-")
	if launch < 0 || stop < launch {
		r.failf(t, "the container was launched at %d and never stopped after (stop at %d)", launch, stop)
	}
	if _, err := os.Stat(filepath.Join(r.fake, "exists")); err == nil {
		r.failf(t, "the stand-in still holds the instance after the run")
	}
}

func TestAMalformedArgumentIsRefusedBeforeAnythingRuns(t *testing.T) {
	f := newFixture(t, spec{})
	cases := []struct {
		name string
		args []string
		msg  string
	}{
		{"empty-number", []string{""}, "pull request number must be a positive integer"},
		{"letters", []string{"abc"}, "pull request number must be a positive integer"},
		{"zero", []string{"0"}, "pull request number must be a positive integer"},
		{"leading-zero", []string{"012"}, "pull request number must be a positive integer"},
		{"negative", []string{"-1"}, "unknown option"},
		{"semicolon", []string{"7;id"}, "pull request number must be a positive integer"},
		{"space", []string{"7 8"}, "pull request number must be a positive integer"},
		{"too-long", []string{"99999999"}, "pull request number must be a positive integer"},
		{"substitution", []string{"$(id)"}, "pull request number must be a positive integer"},
		{"path", []string{"../7"}, "pull request number must be a positive integer"},
		{"flag-as-package", []string{"7", "-race"}, "malformed Go package pattern"},
		{"package-semicolon", []string{"7", "./a;id"}, "malformed Go package pattern"},
		{"package-dotdot", []string{"7", "./a/../b"}, "malformed Go package pattern"},
		{"package-absolute", []string{"7", "/etc"}, "malformed Go package pattern"},
		{"package-space", []string{"7", "./a b"}, "malformed Go package pattern"},
		{"package-substitution", []string{"7", "$(id)"}, "malformed Go package pattern"},
		{"timeout-text", []string{"--timeout", "abc", "7"}, "--timeout must be a number"},
		{"timeout-zero", []string{"--timeout", "0", "7"}, "--timeout must be a number"},
		{"timeout-too-small", []string{"--timeout", "4", "7"}, "--timeout must be a number"},
		{"timeout-too-large", []string{"--timeout", "7201", "7"}, "--timeout must be a number"},
		{"remote-image", []string{"--image", "images:debian/13", "7"}, "image must be a local alias"},
		{"image-space", []string{"--image", "a b", "7"}, "image must be a local alias"},
		{"pool-space", []string{"--pool", "x y", "7"}, "pool name is malformed"},
		{"unknown-option", []string{"--bogus", "7"}, "unknown option"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := f.run(opts{args: c.args})
			r.refused(t, c.msg)
			if len(r.calls) != 0 {
				r.failf(t, "a malformed argument reached incus")
			}
		})
	}
}

func TestAChangedDependencyFileIsRefused(t *testing.T) {
	for _, c := range []struct {
		name string
		pr   func(t *testing.T, repo string)
	}{
		{"go.mod", func(t *testing.T, r string) { write(t, r, "go.mod", goMod+"\n// changed\n") }},
		{"go.sum", func(t *testing.T, r string) { write(t, r, "go.sum", "x v1 h1:abc=\n") }},
		{"nested-go.mod", func(t *testing.T, r string) { write(t, r, "sub/go.mod", "module sub\n") }},
		{"nested-go.sum", func(t *testing.T, r string) { write(t, r, "sub/go.sum", "x\n") }},
		{"go.work", func(t *testing.T, r string) { write(t, r, "go.work", "go 1.26\n") }},
		{"go.work.sum", func(t *testing.T, r string) { write(t, r, "go.work.sum", "x\n") }},
		{"vendor", func(t *testing.T, r string) { write(t, r, "vendor/m/x.go", "package m\n") }},
		{"nested-vendor", func(t *testing.T, r string) { write(t, r, "sub/vendor/x.go", "package m\n") }},
		{"deleted-go.mod", func(t *testing.T, r string) { _ = os.Remove(filepath.Join(r, "go.mod")) }},
		{"go.mod-renamed-away", func(t *testing.T, r string) { sh(t, r, "git", "mv", "go.mod", "go.mod.old") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, spec{pr: func(t *testing.T, r string) {
				write(t, r, "a/new_test.go", "package a\n")
				c.pr(t, r)
			}})
			r := f.run(opts{args: []string{"7"}})
			r.refused(t, "changes a dependency file")
			if len(r.calls) != 0 {
				r.failf(t, "a pull request that changes a dependency file reached incus")
			}
		})
	}
}

func TestTheBoxIsEphemeralUnprivilegedAndLimited(t *testing.T) {
	f := newFixture(t, spec{})
	r := f.run(opts{args: []string{"7"}})
	if r.code != 0 {
		r.failf(t, "want a green run")
	}
	i := r.index("launch")
	if i < 0 {
		r.failf(t, "no launch")
	}
	// Whole arguments and whole lines, not substrings: `--ephemeral=false` and a
	// commented-out `# limits.cpu: "2"` both contain what a substring looks for.
	args := map[string]bool{}
	for _, a := range r.calls[i] {
		args[a] = true
	}
	for _, want := range []string{"--ephemeral", "--no-profiles"} {
		if !args[want] {
			r.failf(t, "the launch lacks the argument %s: %s", want, r.calls[i].joined())
		}
	}
	y, err := os.ReadFile(filepath.Join(r.fake, "launch.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	lines := map[string]bool{}
	for _, l := range strings.Split(string(y), "\n") {
		lines[l] = true
	}
	for _, want := range []string{
		`  security.privileged: "false"`, `  security.nesting: "false"`,
		`  security.guestapi: "false"`, `    size: 10GiB`,
		`  limits.cpu: "2"`, `  limits.memory: 4GiB`, `  limits.processes: "4096"`,
	} {
		if !lines[want] {
			t.Errorf("the box configuration lacks the line %s:\n%s", want, y)
		}
	}
	if !strings.Contains(string(y), "\n  user.review-sandbox: \"") {
		t.Errorf("the box carries no label:\n%s", y)
	}
}

func TestNothingOfTheStationEntersTheBox(t *testing.T) {
	f := newFixture(t, spec{})
	secrets := []string{"SSH_AUTH_SOCK=/run/user/1000/agent.sock", "STATION_SECRET=hunter2", "AWS_SECRET_ACCESS_KEY=s3kr1t"}
	r := f.run(opts{args: []string{"7"}, env: secrets})
	if r.code != 0 {
		r.failf(t, "want a green run")
	}
	allowed := regexp.MustCompile(`^(REVIEW_STAGE=[a-z-]+|HOME=/(root|home/review)|PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin|GOTOOLCHAIN=local|GOFLAGS=-mod=readonly|GOCACHE=/home/review/\.cache/go-build|CGO_ENABLED=0|GOPROXY=(off|https://proxy\.golang\.org))$`)
	home, _ := os.UserHomeDir()
	for _, c := range r.calls {
		line := c.joined()
		for _, bad := range []string{"hunter2", "s3kr1t", "agent.sock", "SSH_AUTH_SOCK", f.repo} {
			if strings.Contains(line, bad) {
				t.Errorf("something of the station reaches incus (%q): %s", bad, line)
			}
		}
		if home != "" && home != "/" && strings.Contains(line, home) {
			t.Errorf("the station's home reaches incus: %s", line)
		}
		for i, a := range c {
			if a == "--env" && i+1 < len(c) && !allowed.MatchString(c[i+1]) {
				t.Errorf("a variable outside the allowed list is passed to the box: %s", c[i+1])
			}
		}
		if len(c) > 3 && c[0] == "config" && c[1] == "device" && c[2] == "add" && !strings.Contains(line, " nic ") {
			t.Errorf("a device other than a NIC is added: %s", line)
		}
	}
	y, _ := os.ReadFile(filepath.Join(r.fake, "launch.yaml"))
	if n := strings.Count(string(y), "type: "); n != 1 || !strings.Contains(string(y), "type: disk") || strings.Contains(string(y), "source:") {
		t.Errorf("the box holds more than one root disk (%d devices) or a source:\n%s", n, y)
	}
}

func TestTheCodeEntersOnlyAfterTheNetworkIsMeasuredCut(t *testing.T) {
	archiveFetched := func(r result) bool { return r.index("file push", "pr.tar") >= 0 }
	for _, c := range []struct {
		name string
		env  string
		msg  string
	}{
		{"connection-succeeds", "FAKE_FORCE_CONNECT=1", "network is not cut"},
		{"interface-left", "FAKE_FORCE_IFACE=1", "interface besides lo"},
		{"nic-left-in-config", "FAKE_FORCE_NIC=1", "network device"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, spec{})
			r := f.run(opts{args: []string{"7"}, env: []string{c.env}})
			r.refused(t, c.msg)
			if archiveFetched(r) || r.index("REVIEW_STAGE=extract") >= 0 || r.index("REVIEW_STAGE=test") >= 0 {
				r.failf(t, "the code of the pull request entered a box whose network was not cut")
			}
			r.wasStopped(t)
		})
	}
	t.Run("modules-then-cut-then-code", func(t *testing.T) {
		f := newFixture(t, spec{base: map[string]string{"go.mod": goModDeps, "go.sum": "golang.org/x/text v0.44.0 h1:x=\n"}})
		r := f.run(opts{args: []string{"7"}})
		if r.code != 0 {
			r.failf(t, "want a green run")
		}
		add := r.index("config device add", "nic")
		dl := r.index("REVIEW_STAGE=modules")
		rm := r.index("config device remove")
		push := r.index("file push", "pr.tar")
		if add < 0 || add >= dl || dl >= rm || rm >= push {
			r.failf(t, "want NIC added, modules downloaded, NIC removed, then the archive pushed; got %d %d %d %d", add, dl, rm, push)
		}
		if i := r.index("REVIEW_STAGE=test"); i >= 0 && !strings.Contains(r.calls[i].joined(), "GOPROXY=off") {
			r.failf(t, "the tests run with a proxy")
		}
	})
	t.Run("no-requirement-no-network", func(t *testing.T) {
		f := newFixture(t, spec{})
		r := f.run(opts{args: []string{"7"}})
		if r.code != 0 || r.index("config device add") >= 0 || r.index("REVIEW_STAGE=modules") >= 0 {
			r.failf(t, "a go.mod without requirement must never open the network")
		}
	})
}

func TestTheCodeEntersAsAnArchiveOnly(t *testing.T) {
	f := newFixture(t, spec{pr: func(t *testing.T, r string) { write(t, r, "a/probe_test.go", "package a\n") }})
	// Work in the checkout that the pull request does not contain.
	write(t, f.repo, "UNTRACKED.txt", "station only\n")
	write(t, f.repo, "a/a.go", "package a\n\n// uncommitted edit of the station\n")
	r := f.run(opts{args: []string{"7"}})
	if r.code != 0 {
		r.failf(t, "want a green run")
	}
	pushes := 0
	for _, c := range r.callsTo("file") {
		if len(c) > 1 && c[1] == "push" {
			pushes++
			if strings.Contains(c.joined(), "-r") && strings.Contains(c.joined(), "--recursive") {
				t.Errorf("a recursive push: %s", c.joined())
			}
		}
	}
	if pushes != 2 { // the toolchain and the archive; no requirement, so no go.mod
		r.failf(t, "want 2 pushes (toolchain, archive), got %d", pushes)
	}
	out := sh(t, r.fake, "tar", "-tf", filepath.Join(r.fake, "pushed", "pr.tar"))
	if !strings.Contains(out, "a/probe_test.go") {
		t.Errorf("the archive does not hold the head commit's file:\n%s", out)
	}
	if strings.Contains(out, "UNTRACKED.txt") || strings.Contains(out, "tools/review/sandbox.sh") {
		t.Errorf("the archive holds a file of the station's checkout:\n%s", out)
	}
	got := sh(t, r.fake, "tar", "-xOf", filepath.Join(r.fake, "pushed", "pr.tar"), "a/a.go")
	if got != aGo {
		t.Errorf("a/a.go in the archive is not the committed one: %q", got)
	}
}

func TestTheBoxIsStoppedWhateverHappens(t *testing.T) {
	for _, c := range []struct {
		name string
		env  []string
		args []string
		code int
	}{
		{"green", nil, nil, 0},
		{"red", []string{"FAKE_TEST_RC=1"}, nil, 2},
		{"a-stage-fails", []string{"FAKE_RC_PREP=3"}, nil, 1},
		{"the-network-is-not-cut", []string{"FAKE_FORCE_CONNECT=1"}, nil, 1},
		{"the-image-launches-but-gocheck-fails", []string{"FAKE_GO_VERSION_OUT=go version go0 linux/amd64"}, nil, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, spec{})
			r := f.run(opts{args: append(append([]string{}, c.args...), "7"), env: c.env})
			if r.code != c.code {
				r.failf(t, "want exit %d", c.code)
			}
			r.wasStopped(t)
		})
	}
	t.Run("deadline-in-the-tests", func(t *testing.T) {
		f := newFixture(t, spec{})
		r := f.run(opts{args: []string{"--timeout", "5", "7"}, env: []string{"FAKE_SLEEP_TEST=40"}})
		r.refused(t, "deadline of 5s is exceeded")
		if r.elapsed > 25*time.Second {
			r.failf(t, "the deadline of 5 s took %s to be enforced", r.elapsed)
		}
		r.wasStopped(t)
	})
	t.Run("sigterm-while-the-tests-run", func(t *testing.T) {
		f := newFixture(t, spec{})
		r := f.run(opts{args: []string{"7"}, env: []string{"FAKE_SLEEP_TEST=40"}, signal: syscall.SIGTERM})
		if r.code != 143 {
			r.failf(t, "want exit 143 after SIGTERM")
		}
		if r.elapsed > 30*time.Second {
			r.failf(t, "the script waited %s for its child instead of stopping at once", r.elapsed)
		}
		r.wasStopped(t)
	})
}

func TestAnInstanceThatIsNotOursIsNeverStopped(t *testing.T) {
	f := newFixture(t, spec{})
	r := f.run(opts{args: []string{"7"}, env: []string{"FAKE_LABEL=somebody-elses"}})
	if len(r.callsTo("stop")) != 0 {
		r.failf(t, "an instance without this run's label was stopped")
	}
	if r.code != 1 || !strings.Contains(r.stderr, "does not carry this run's label") {
		r.failf(t, "want exit 1 and the refusal named")
	}
}

func TestAnImageThatIsNotOnTheStationIsNotLaunched(t *testing.T) {
	f := newFixture(t, spec{})
	r := f.run(opts{args: []string{"7"}, env: []string{"FAKE_IMAGE_MISSING=1"}})
	r.refused(t, "is not on this station")
	if r.index("launch") >= 0 {
		r.failf(t, "launched an image that is not there")
	}
}

func TestTheExitCodeSaysWhatWasMeasured(t *testing.T) {
	for _, c := range []struct {
		name string
		env  []string
		code int
		want string
	}{
		{"green", nil, 0, "tests green"},
		{"red", []string{"FAKE_TEST_RC=1"}, 2, "tests red"},
		{"go-test-did-not-finish", []string{"FAKE_TEST_RC=3"}, 1, "did not finish normally"},
		{"the-go-in-the-box-is-not-the-pinned-one", []string{"FAKE_GO_VERSION_OUT=go version go1.0.0 linux/amd64"}, 1, "stage gocheck failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, spec{})
			r := f.run(opts{args: []string{"7"}, env: c.env})
			if r.code != c.code || !strings.Contains(r.stdout+r.stderr, c.want) {
				r.failf(t, "want exit %d and %q", c.code, c.want)
			}
		})
	}
}

func TestTheHostGoMustBeThePinnedOne(t *testing.T) {
	f := newFixture(t, spec{goVersion: "go1.0.0"})
	r := f.run(opts{args: []string{"7"}})
	r.refused(t, "mise.toml pins go1.26.11")
	if r.index("launch") >= 0 {
		r.failf(t, "launched with a toolchain that is not the pinned one")
	}
}

func testedPackages(t *testing.T, r result) []string {
	t.Helper()
	i := r.index("REVIEW_STAGE=test")
	if i < 0 {
		r.failf(t, "the tests did not run")
	}
	c := r.calls[i]
	for j, a := range c {
		if a == "-count=1" {
			return c[j+1:]
		}
	}
	r.failf(t, "no -count=1 in the test call")
	return nil
}

func TestThePackagesAreTheOnesThePullRequestTouches(t *testing.T) {
	t.Run("derived-from-the-diff", func(t *testing.T) {
		f := newFixture(t, spec{pr: func(t *testing.T, r string) {
			write(t, r, "internal/cli/x.go", "package cli\n")
			write(t, r, "internal/cli/y_test.go", "package cli\n")
			write(t, r, "a/testdata/t.go", "package t\n")
			write(t, r, "_hidden/z.go", "package z\n")
			write(t, r, "docs/README", "changed\n")
			write(t, r, "main.go", "package main\n\nfunc main() { _ = 1 }\n")
			_ = os.RemoveAll(filepath.Join(r, "gone"))
		}})
		r := f.run(opts{args: []string{"7"}})
		got := strings.Join(testedPackages(t, r), " ")
		if got != "./internal/cli ." && got != ". ./internal/cli" {
			r.failf(t, "tested packages are %q, want ./internal/cli and . only", got)
		}
	})
	t.Run("named-by-the-maintainer", func(t *testing.T) {
		f := newFixture(t, spec{})
		r := f.run(opts{args: []string{"7", "./a/...", "./internal"}})
		if got := strings.Join(testedPackages(t, r), " "); got != "./a/... ./internal" {
			r.failf(t, "tested packages are %q", got)
		}
	})
	t.Run("a-named-directory-the-pull-request-lacks", func(t *testing.T) {
		f := newFixture(t, spec{})
		r := f.run(opts{args: []string{"7", "./nope"}})
		r.refused(t, "has no directory ./nope")
	})
	t.Run("no-go-file-touched", func(t *testing.T) {
		f := newFixture(t, spec{pr: func(t *testing.T, r string) { write(t, r, "docs/README", "changed\n") }})
		r := f.run(opts{args: []string{"7"}})
		r.refused(t, "adds or changes no Go package")
	})
	t.Run("unusual-directory-name", func(t *testing.T) {
		f := newFixture(t, spec{pr: func(t *testing.T, r string) { write(t, r, "we ird/x.go", "package w\n") }})
		r := f.run(opts{args: []string{"7"}})
		r.refused(t, "unusual name")
		if r.index("launch") >= 0 {
			r.failf(t, "went on with a directory name it cannot pass safely")
		}
	})
}

func runEscaper(t *testing.T, in []byte) string {
	t.Helper()
	here, _ := os.Getwd()
	cmd := exec.Command("python3", "-I", filepath.Join(here, "escape.py"))
	cmd.Stdin = bytes.NewReader(in)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// leftOver names what in out would still act on a terminal.
func leftOver(out string) []string {
	var bad []string
	b := []byte(out)
	for _, c := range b {
		if (c < 0x20 && c != '\n' && c != '\t') || c == 0x7f {
			bad = append(bad, "C0 or DEL byte")
			break
		}
	}
	if !utf8.ValidString(out) {
		bad = append(bad, "invalid UTF-8")
	}
	for _, r := range out {
		switch {
		case r >= 0x80 && r <= 0x9f:
			bad = append(bad, "C1 control")
		case r >= 0x200b && r <= 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0x2028, r == 0x2029, r == 0xfeff:
			bad = append(bad, "format or separator character")
		}
	}
	return bad
}

func TestTheEscaperLeavesNoControlCharacter(t *testing.T) {
	for _, c := range []struct {
		name string
		in   string
		want string
	}{
		{"escape-sequence", "a\x1b[2Jb", `a\x1b[2Jb`},
		{"carriage-return", "line\roverwritten", `line\x0doverwritten`},
		{"bell-and-nul", "x\x07y\x00z", `x\x07y\x00z`},
		{"del", "x\x7fy", `x\x7fy`},
		{"c1-csi", "x\u009b31my", `x\x9b31my`},
		{"bidi-override", "a\u202eb", `a\u202eb`},
		{"zero-width", "a\u200bb", `a\u200bb`},
		{"line-separator", "a\u2028b", `a\u2028b`},
		{"invalid-utf8", "a\xffb", `a\xffb`},
		{"plain-text-tab-newline-and-accents", "ok\tété\nfin", "ok\tété\nfin"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := runEscaper(t, []byte(c.in))
			if got != c.want {
				t.Errorf("escape(%q) = %q, want %q", c.in, got, c.want)
			}
			if bad := leftOver(got); len(bad) != 0 {
				t.Errorf("left over in %q: %v", got, bad)
			}
		})
	}
}

func TestTheOutputIsEscaped(t *testing.T) {
	f := newFixture(t, spec{})
	r := f.run(opts{args: []string{"7"}, env: []string{
		`FAKE_TEST_OUTPUT=FAIL\n\033[2J\033]0;owned\007 \302\233\061m\r\342\200\256x\n`,
		"FAKE_TEST_RC=1",
	}})
	if r.code != 2 {
		r.failf(t, "want a red run")
	}
	if bad := leftOver(r.stdout); len(bad) != 0 {
		r.failf(t, "the terminal receives %v", bad)
	}
	if !strings.Contains(r.stdout, `\x1b[2J`) || !strings.Contains(r.stdout, `\u202e`) {
		r.failf(t, "the escapes are not visible in the output")
	}
}

// TestADiskTheBoxCannotBoundIsRefused: Incus 7.6 accepts `size:` on a dir pool
// and applies nothing, so the script measures what the box sees before the
// archive or the toolchain is pushed.
func TestADiskTheBoxCannotBoundIsRefused(t *testing.T) {
	for _, c := range []struct{ name, size, msg string }{
		{"the-host-disk", "997000000000", "does not apply the 10GiB size"},
		{"unreadable", "none", "cannot read the size of the root disk"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, spec{})
			r := f.run(opts{args: []string{"7"}, env: []string{"FAKE_DISK_SIZE=" + c.size}})
			r.refused(t, c.msg)
			if r.index("file push") >= 0 || r.index("REVIEW_STAGE=prep") >= 0 {
				r.failf(t, "something was pushed into a box whose disk is not bounded")
			}
			r.wasStopped(t)
		})
	}
}
