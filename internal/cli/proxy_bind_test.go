package cli

import (
	"bytes"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestTheProxyAnnouncesTheAddressItBound is cited by proxyCommand. The banner
// was printed before the bind, so `--addr 127.0.0.1:0` announced `:0`, never
// the port the system chose, and a client pointed at what the banner said
// could reach nothing. The port printed must be a real one, and the listener
// must be there when the line is read.
func TestTheProxyAnnouncesTheAddressItBound(t *testing.T) {
	var stderr syncBuffer
	record := filepath.Join(t.TempDir(), "run.jsonl")
	code := make(chan int, 1)
	go func() {
		code <- proxyCommand([]string{
			"--upstream", "http://127.0.0.1:1", "--record", record, "--addr", "127.0.0.1:0",
		}, nil, &stderr)
	}()

	const prefix = "feint proxy listening on "
	var announced string
	deadline := time.Now().Add(10 * time.Second)
	for announced == "" && time.Now().Before(deadline) {
		for _, line := range strings.Split(stderr.String(), "\n") {
			if after, ok := strings.CutPrefix(line, prefix); ok {
				announced = strings.TrimSpace(after)
			}
		}
		select {
		case c := <-code:
			t.Fatalf("the proxy returned %d before announcing anything:\n%s", c, stderr.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
	if announced == "" {
		t.Fatalf("no %q line within 10 s:\n%s", prefix, stderr.String())
	}

	_, port, err := net.SplitHostPort(announced)
	if err != nil || port == "0" || port == "" {
		t.Errorf("the banner announces %q, not an address with the port the system chose", announced)
	} else if conn, err := net.DialTimeout("tcp", announced, 2*time.Second); err != nil {
		t.Errorf("the banner announces %s, and nothing listens there: %v", announced, err)
	} else {
		_ = conn.Close()
	}
	if want := "point a client at http://" + announced; !strings.Contains(stderr.String(), want) {
		t.Errorf("the hint does not name the bound address %s:\n%s", announced, stderr.String())
	}

	// The real path out: the proxy armed signal.NotifyContext before it
	// announced, so this SIGTERM stops it rather than the test binary.
	//
	// And a safety net of the test's own, registered before the kill: a proxy
	// that did not catch SIGTERM (a mutant arming NotifyContext late, or on
	// another signal) would otherwise let the default action kill the whole
	// test binary, `signal: terminated`, every later test unrun. That is a
	// crash, not a failure; with the net the check below fails it cleanly
	// (go-reviewer, lot A cycle 3).
	safety := make(chan os.Signal, 1)
	signal.Notify(safety, syscall.SIGTERM)
	defer signal.Stop(safety)
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signal the proxy: %v", err)
	}
	select {
	case <-code:
	case <-time.After(10 * time.Second):
		t.Fatalf("the proxy did not stop on SIGTERM within 10 s:\n%s", stderr.String())
	}
}

// A bind that fails says so and announces nothing: the banner used to come
// first, so the operator read "listening on" and then the error.
func TestAProxyThatCannotBindAnnouncesNothing(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("take a port: %v", err)
	}
	defer func() { _ = taken.Close() }()

	var stdout, stderr bytes.Buffer
	record := filepath.Join(t.TempDir(), "run.jsonl")
	args := []string{
		"--upstream", "http://127.0.0.1:1", "--record", record, "--addr", taken.Addr().String(),
	}
	if code := proxyCommandBounded(t, args, &stdout, &stderr); code != exitError {
		t.Fatalf("exit %d on a port already taken, want %d:\n%s", code, exitError, stderr.String())
	}
	if !strings.Contains(stderr.String(), "listen tcp") {
		t.Errorf("the failed bind is not reported:\n%s", stderr.String())
	}
	if strings.Contains(stderr.String(), "listening on") {
		t.Errorf("a proxy that could not bind announced that it was listening:\n%s", stderr.String())
	}
}
