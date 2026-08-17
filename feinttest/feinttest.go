// Package feinttest starts Feint for a Go test and hands back the endpoint.
//
// It exists because an emulator enters somebody's test suite through their test
// framework, not through their README. Without it, a Go test that wants a cloud
// has to start the binary, poll until it answers, and remember to stop it —
// which is the loop the lifecycle verbs remove for shells and nothing removed
// for code.
//
//	cloud := feinttest.Start(t)
//
//	client, _ := scw.NewClient(
//	    scw.WithAPIURL(cloud.Endpoint()),
//	    scw.WithAuth("SCWXXXXXXXXXXXXXXXXX", "11111111-1111-1111-1111-111111111111"),
//	)
//	// …drive the official SDK against it
//
// (The enclosing `func Test…` is left out on purpose: the citation gate reads
// every Test name in a comment as a test this file claims exists, and it is
// right not to try telling an example from a claim.)
//
// # Why this is not testcontainers-go
//
// It would be the obvious dependency, and it is the wrong one here: `go.mod` has
// three lines, and a pull of testcontainers-go brings the Docker client and its
// transitive tree into every consumer of this module. The repository's rule is
// that a dependency is justified in the pull request that adds it, and the
// justification would have been convenience.
//
// So the container runtime is driven the way this project already drives one:
// through its command-line tool, exactly as internal/core/machine drives Incus.
// Anybody who wants a testcontainers module can write one over this in twenty
// lines; nobody has to pay for it who does not.
//
// # What it starts
//
// The published image, control plane only. Real machines behind --vm need the
// binary on a host with Incus, and an image that promised to start containers
// from inside a container would be the half-truth docs/limits.md refuses. A test
// that needs machines should drive the binary directly.
package feinttest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// DefaultImage is what Start pulls when a caller names nothing.
//
// A version, never `latest`: a mutable tag means a test that passed yesterday
// and fails today against an image nobody chose. Bumped with each release, and a
// caller who wants another one passes it.
const DefaultImage = "ghcr.io/stephrobert/feint:v0.8.0"

// Cloud is a running emulator.
type Cloud struct {
	endpoint  string
	container string
	runtime   string
}

// Endpoint is the base URL the official clients should be pointed at.
func (c *Cloud) Endpoint() string { return c.endpoint }

// Option configures Start.
type Option func(*config)

type config struct {
	image   string
	timeout time.Duration
}

// WithImage runs another image than DefaultImage — a newer release, or one built
// from a branch.
func WithImage(ref string) Option { return func(c *config) { c.image = ref } }

// WithTimeout bounds how long Start waits for the emulator to answer.
func WithTimeout(d time.Duration) Option { return func(c *config) { c.timeout = d } }

// Start runs the emulator in a container and returns once it answers.
//
// The test is skipped, not failed, when no container runtime is installed: a
// suite that cannot find Docker has learned nothing about the code under test,
// and failing there would teach contributors to distrust the suite. It *is*
// failed when a runtime exists and the emulator will not come up, because that
// is a fact about this software.
//
// Cleanup is registered with t.Cleanup rather than left to the caller: a
// container leaked by a failing test is a port held on somebody's machine until
// they find it by hand.
func Start(t *testing.T, opts ...Option) *Cloud {
	t.Helper()

	cfg := config{image: DefaultImage, timeout: 60 * time.Second}
	for _, opt := range opts {
		opt(&cfg)
	}

	runtime, err := containerRuntime()
	if err != nil {
		t.Skipf("feinttest: %v", err)
	}

	cloud, err := launch(runtime, cfg)
	// Registered before the error is read: a launch that started a container
	// and then failed to reach it has still left a container, and that is the
	// case where forgetting to remove it hurts most.
	if cloud != nil {
		t.Cleanup(cloud.stop)
	}
	if err != nil {
		t.Fatalf("feinttest: %v", err)
	}
	return cloud
}

// launch does the work and returns an error rather than failing a test, so that
// the failure path itself can be exercised — see
// TestAFailedStartSaysWhatTheContainerDid, which is the whole reason this is not
// inlined into Start.
func launch(runtime string, cfg config) (*Cloud, error) {
	// A free port asked of the kernel rather than a constant: two packages
	// running in parallel with `go test ./...` would otherwise fight over 4599,
	// and the loser's failure would name a port rather than a cause.
	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("no free port: %w", err)
	}

	name := fmt.Sprintf("feint-test-%d-%d", os.Getpid(), port)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()

	// The image reference is the only value here a caller supplies, and the
	// caller is the test itself: this package is imported by test code that
	// already decides what to run. `runtime` comes from a closed list, `name`
	// and the publish mapping are built here. Named rather than waved through,
	// because the day this is called with a value from anywhere else — a
	// configuration file, an environment variable — that reasoning stops being
	// true and this comment is what a reader will check it against.
	// No --rm. The flag looks like the tidy choice and destroys the only
	// evidence there is: a container that dies on startup is gone before
	// anything can read its log, and `docker logs` then answers "No such
	// container" — which is precisely what this package printed when the ARM
	// leg of #247 went red, in place of the reason. Removal happens in the
	// cleanup below, after the diagnosis. TestAFailedStartSaysWhatTheContainerDid
	// fails when the flag comes back.
	run := exec.CommandContext(ctx, runtime, "run", "--detach", //nolint:gosec // the image is the test's own choice; see above
		"--name", name,
		"--publish", fmt.Sprintf("127.0.0.1:%d:4599", port),
		cfg.image)
	if out, err := run.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%s run failed: %w\n%s", runtime, err, out)
	}

	cloud := &Cloud{
		endpoint:  fmt.Sprintf("http://127.0.0.1:%d", port),
		container: name,
		runtime:   runtime,
	}
	if err := cloud.wait(ctx); err != nil {
		return cloud, fmt.Errorf("the emulator never answered on %s: %w\n%s", cloud.endpoint, err, cloud.diagnose())
	}
	return cloud, nil
}

// diagnose answers what the container did, for a Start that timed out.
//
// Two questions, because they have different answers: whether the container is
// still running — a live container that answers nothing is a different fault
// from one that died — and what it said. A caller reading "exited (code 1)"
// knows to look at the log below it; a caller reading "running" knows the
// process is up and the port is not reaching it, which is a host problem and
// not this software's.
func (c *Cloud) diagnose() string {
	state, err := exec.Command(c.runtime, "inspect", //nolint:gosec // a name this package built
		"--format", "{{.State.Status}} (code {{.State.ExitCode}})", c.container).CombinedOutput()
	line := strings.TrimSpace(string(state))
	if err != nil {
		line = fmt.Sprintf("the container could not be inspected: %v: %s", err, line)
	}
	logs, err := exec.Command(c.runtime, "logs", c.container).CombinedOutput() //nolint:gosec // a name this package built
	if err != nil {
		return fmt.Sprintf("container %s; its log could not be read: %v: %s", line, err, logs)
	}
	if len(logs) == 0 {
		return fmt.Sprintf("container %s, and it wrote nothing", line)
	}
	return fmt.Sprintf("container %s; its log:\n%s", line, logs)
}

// stop removes the container. Errors are reported rather than swallowed: a
// leftover container holds a port, and a silent cleanup failure is how somebody
// spends an afternoon on a port already in use.
//
// `rm --force` rather than `stop`, because the container may already have
// exited — that is the case Start most wants to survive, and `stop` on a dead
// container is an error that says nothing while leaving the name taken.
func (c *Cloud) stop() {
	if out, err := exec.Command(c.runtime, "rm", "--force", c.container).CombinedOutput(); err != nil { //nolint:gosec // a name this package built
		fmt.Fprintf(os.Stderr, "feinttest: could not remove %s: %v\n%s", c.container, err, out)
	}
}

// wait polls the emulator's own health endpoint until it answers.
//
// Its answer is read rather than its status alone: a listener that accepts and
// answers nothing is exactly what a port check cannot tell from a working one,
// and that difference cost this repository a suite that hung instead of failing.
func (c *Cloud) wait(ctx context.Context) error {
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/_feint/health", nil)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if err == nil {
			body := make([]byte, 512)
			n, _ := res.Body.Read(body)
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK && strings.Contains(string(body[:n]), "providers") {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// containerRuntime answers the tool to drive, preferring Docker and accepting
// Podman, which is what a Fedora or RHEL workstation has.
func containerRuntime() (string, error) {
	for _, candidate := range []string{"docker", "podman"} {
		if _, err := exec.LookPath(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", errors.New("no container runtime: install docker or podman, or point your test at a `feint start` you run yourself")
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port, nil
}
