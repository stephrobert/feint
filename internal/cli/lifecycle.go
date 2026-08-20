package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// The operator's side of the emulator: start it, wait for it, stop it.
//
// None of the three comparable emulators backgrounds itself — floci, LocalStack
// and ministack all delegate detaching to `docker run -d`. They are not being
// lazy: a JVM and a CPython process cannot cleanly daemonise, so Docker is their
// only option. A single static Go binary can, and that is the one place this
// project can be better than all three at once, for about two hundred lines.
//
// The evidence that these verbs are the right ones is not a unit test: it is
// that tools/conformance/ can drop its `&` and its hand-rolled curl loop and use
// them instead. If the suite cannot be rewritten onto them, they are wrong.

// serveFlags are the flags `start` records so `restart` can replay them and
// `status` can report them. Kept beside serve's own declaration on purpose: a
// flag added there and forgotten here silently stops surviving a restart.
type serveFlags struct {
	addr      string
	state     string
	vm        string
	cleanup   bool
	logLevel  string
	contracts string
}

// args renders the flags back into a `serve` command line.
func (f serveFlags) args() []string {
	out := []string{"serve", "--addr", f.addr, "--vm", f.vm, "--log-level", f.logLevel}
	if f.state != "" {
		out = append(out, "--state", f.state)
	}
	if f.contracts != "" {
		out = append(out, "--contracts", f.contracts)
	}
	if f.cleanup {
		out = append(out, "--cleanup")
	}
	return out
}

func bindServeFlags(fs *flag.FlagSet) *serveFlags {
	f := &serveFlags{}
	fs.StringVar(&f.addr, "addr", DefaultAddr, "listen address")
	fs.StringVar(&f.state, "state", "", "load and persist the store to this JSON file")
	fs.StringVar(&f.vm, "vm", "off", "back powered-on servers with real machines: off, incus, incus-vm, incus-ovn, auto")
	fs.BoolVar(&f.cleanup, "cleanup", false, "remove the machines and networks this run created before exiting")
	fs.StringVar(&f.logLevel, "log-level", "info", "log verbosity: error, warn, info, debug")
	fs.StringVar(&f.contracts, "contracts", "", "directory of API contracts; every response is checked against them")
	return f
}

// start detaches a serve, records it, and waits for it to answer.
func start(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("start")
	flags := bindServeFlags(fs)
	timeout := fs.Duration("timeout", 30*time.Second, "how long to wait for the emulator to answer")
	detach := fs.Bool("detach", false, "return as soon as the child is spawned, without waiting for health")
	foreground := fs.Bool("foreground", false, "do not detach; equivalent to `feint serve`")
	if err := fs.Parse(args); err != nil {
		return exitError
	}

	if *foreground {
		// An alias rather than a duplicate, so a container image has a sane
		// entrypoint without a second code path to keep in step.
		return run(stderr, func() error { return serve(flags.args()[1:], stdout) })
	}

	// Refuse to adopt a running instance. Printing the endpoint and failing is
	// what tells an operator their second `start` did nothing, where silently
	// succeeding would have them drive an emulator configured by an earlier
	// command with different flags.
	existing, err := loadInstance(flags.addr)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	if existing != nil {
		switch existing.alive() {
		case Alive:
			fmt.Fprintf(stderr, "feint: already running on %s (pid %d); stop it first\n",
				existing.Addr, existing.PID)
			return exitError
		case Foreign:
			fmt.Fprintf(stderr, "feint: a stale instance recorded pid %d, which now belongs to something else; discarding it\n",
				existing.PID)
			if err := existing.remove(); err != nil {
				fmt.Fprintf(stderr, "feint: %v\n", err)
				return exitError
			}
		case Dead:
			if err := existing.remove(); err != nil {
				fmt.Fprintf(stderr, "feint: %v\n", err)
				return exitError
			}
		}
	}

	inst, err := spawn(*flags)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	if *detach {
		fmt.Fprintf(stdout, "feint started on %s (pid %d), not waiting for health\n", inst.Addr, inst.PID)
		return exitOK
	}

	if err := awaitHealthy(inst, *timeout); err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	fmt.Fprintf(stdout, "feint listening on %s (pid %d)\n", inst.Addr, inst.PID)
	fmt.Fprintf(stdout, "  logs: %s\n", inst.Log)
	return exitOK
}

// spawn re-execs this binary as a detached `serve`.
//
// Go has no fork, so detaching is a re-exec: Setsid puts the child in its own
// session, which is what makes it survive the shell that started it. stdin comes
// from /dev/null because a background process reading a terminal stops the whole
// job; stdout and stderr are appended to the log, which is the only place a
// failure after startup can be seen.
func spawn(flags serveFlags) (*Instance, error) {
	dir, err := instanceDir(flags.addr)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	binary, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("cannot find this binary to re-exec it: %w", err)
	}

	logPath := filepath.Join(dir, "feint.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // a path this package builds
	if err != nil {
		return nil, err
	}
	defer func() { _ = logFile.Close() }()

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return nil, err
	}
	defer func() { _ = devNull.Close() }()

	cmd := exec.Command(binary, flags.args()...) //nolint:gosec // the binary is this one, the args are ours
	cmd.Stdin = devNull
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	inst := &Instance{
		PID:     cmd.Process.Pid,
		Addr:    flags.addr,
		Started: time.Now().UTC().Format(time.RFC3339),
		Binary:  binary,
		Args:    flags.args(),
		Log:     logPath,
		Dir:     dir,
	}
	if err := inst.save(); err != nil {
		// The child is running and unrecorded, which is worse than not having
		// started it: nothing could stop it afterwards.
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("could not record the instance, so it was stopped again: %w", err)
	}
	// Reaped in the background so the child does not become a zombie while the
	// parent polls. The parent exits shortly after either way, at which point
	// init adopts it.
	go func() { _ = cmd.Wait() }()
	return inst, nil
}

// awaitHealthy polls until the emulator answers, gives up early when the
// child is already gone, and refuses an answer that names another process.
//
// Detecting the child's death is the difference between a useful error and a
// useless one. A port already bound, a missing contracts directory or an Incus
// daemon refusing all produce an immediate exit, and polling health for thirty
// seconds before printing "timed out" hides the actual message. So the loop
// checks liveness too, and prints the tail of the log when the process is gone.
//
// The identity check is the point of the loop, not a nicety. Measured on
// 2026-08-19 (#309): with a stale emulator holding the port, the spawned child
// died on the bind error, this loop took the stale process's health answer as
// the child's, and `start` printed "listening (pid N)" about a pid that was
// already dead — after which every suite measured the previous build.
// TestStartRefusesAForeignAnswerOnItsAddress fails without the refusal.
func awaitHealthy(inst *Instance, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if id, ok := probeIdentity(inst.Addr, time.Second); ok {
			if id == nil || id.PID != inst.PID {
				return fmt.Errorf("%s is already served by %s — not the emulator this command just started (pid %d), "+
					"whose log is %s.\nStop the other one first (feint stop --addr %s, or kill it), or pick another address",
					inst.Addr, describeForeign(id), inst.PID, inst.Log, inst.Addr)
			}
			return nil
		}
		if inst.alive() != Alive {
			_ = inst.remove()
			return fmt.Errorf("the emulator exited immediately:\n%s", tail(inst.Log, 12))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not answer within %s; last lines of %s:\n%s",
				inst.Addr, timeout, inst.Log, tail(inst.Log, 12))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// describeForeign names the process a health answer said it came from, for a
// refusal message. A nil identity is an emulator too old to carry one
// (schema_version < 3), which is still a stranger: whatever it serves, it is
// not what this checkout just built.
func describeForeign(id *identity) string {
	if id == nil {
		return "another emulator that predates identity (health schema_version < 3)"
	}
	return fmt.Sprintf("another emulator (pid %d, started %s)", id.PID, id.Started)
}

// tail returns the last n lines of a file, for an error message. A file that
// cannot be read produces a note rather than a second error: the caller is
// already reporting a failure and a nested one helps nobody.
func tail(path string, n int) string {
	raw, err := os.ReadFile(path) //nolint:gosec // a path this package builds
	if err != nil {
		return "  (no log at " + path + ")"
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for i, line := range lines {
		lines[i] = "  " + line
	}
	return strings.Join(lines, "\n")
}

// stop signals the recorded instance and waits for it to go.
func stop(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("stop")
	addr := fs.String("addr", DefaultAddr, "listen address of the instance to stop")
	timeout := fs.Duration("timeout", 15*time.Second, "how long to wait after SIGTERM before SIGKILL")
	if err := fs.Parse(args); err != nil {
		return exitError
	}

	inst, err := loadInstance(*addr)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	if inst == nil {
		fmt.Fprintf(stderr, "feint: nothing recorded on %s\n", *addr)
		return exitError
	}

	switch inst.alive() {
	case Dead:
		fmt.Fprintf(stdout, "already stopped; discarding the stale record for %s\n", inst.Addr)
		return removeOrFail(inst, stderr)
	case Foreign:
		// The pid was recycled. Signalling it would kill a stranger, which is
		// the one thing this must never do.
		fmt.Fprintf(stderr, "feint: pid %d no longer belongs to feint; the record is stale and was discarded, nothing was signalled\n",
			inst.PID)
		return removeOrFail(inst, stderr)
	case Alive:
	}

	// What this stop is about to throw away, said at the moment it happens
	// rather than on a page read afterwards (#182).
	if notice := discardNotice(inst); notice != "" {
		fmt.Fprint(stderr, notice)
	}

	if err := syscall.Kill(inst.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	// --cleanup may legitimately need time: it tears down machines and networks
	// before exiting, and killing it mid-sweep leaves them behind.
	deadline := time.Now().Add(*timeout)
	for time.Now().Before(deadline) {
		if inst.alive() == Dead {
			fmt.Fprintf(stdout, "stopped %s (pid %d)\n", inst.Addr, inst.PID)
			return removeOrFail(inst, stderr)
		}
		time.Sleep(100 * time.Millisecond)
	}

	if err := syscall.Kill(inst.PID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	// Said out loud: SIGKILL skips the cleanup, so machines and networks may
	// have been left on the host. `feint clean` is the way out.
	fmt.Fprintf(stderr, "feint: %s did not exit within %s and was killed; run `feint clean` if it was serving machines\n",
		inst.Addr, *timeout)
	return removeOrFail(inst, stderr)
}

func removeOrFail(inst *Instance, stderr io.Writer) int {
	if err := inst.remove(); err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	return exitOK
}

// wait polls until the emulator answers. This is the CI verb, and the reason it
// exists is in the conformance suites: every one of them opened with a
// hand-rolled `for _ in $(seq 1 40); do curl -sf … && break; sleep 0.25; done`.
//
// It does not require a recorded instance: an emulator started any other way —
// in a container, by another tool, in the foreground of another terminal — is
// still something a script needs to wait for.
func waitCommand(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("wait")
	addr := fs.String("addr", DefaultAddr, "listen address to wait for")
	timeout := fs.Duration("timeout", 30*time.Second, "how long to wait")
	if err := fs.Parse(args); err != nil {
		return exitError
	}

	deadline := time.Now().Add(*timeout)
	for {
		if healthy(*addr, time.Second) {
			fmt.Fprintf(stdout, "%s is ready\n", *addr)
			return exitOK
		}
		if time.Now().After(deadline) {
			// Exit 1, not a new code: the exit-code contract is 0 ok, 1 error,
			// 2 drift, and CI depends on exactly those three.
			fmt.Fprintf(stderr, "feint: %s did not answer within %s\n", *addr, *timeout)
			return exitError
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// restart stops the recorded instance and starts it again with the flags it was
// started with, which is the reason those flags are recorded at all.
func restart(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("restart")
	addr := fs.String("addr", DefaultAddr, "listen address of the instance to restart")
	timeout := fs.Duration("timeout", 30*time.Second, "how long to wait for it to answer again")
	if err := fs.Parse(args); err != nil {
		return exitError
	}

	inst, err := loadInstance(*addr)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	if inst == nil {
		fmt.Fprintf(stderr, "feint: nothing recorded on %s; use `feint start`\n", *addr)
		return exitError
	}
	// Captured before stopping, because stop removes the record.
	replay := append([]string(nil), inst.Args[1:]...)

	if code := stop([]string{"--addr", *addr}, stdout, stderr); code != exitOK {
		return code
	}
	return start(append(replay, "--timeout", timeout.String()), stdout, stderr)
}

// logs prints the detached run's log.
func logs(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("logs")
	addr := fs.String("addr", DefaultAddr, "listen address of the instance")
	follow := fs.Bool("f", false, "follow the log as it grows")
	lines := fs.Int("n", 50, "how many trailing lines to print (0 for all)")
	if err := fs.Parse(args); err != nil {
		return exitError
	}

	inst, err := loadInstance(*addr)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	if inst == nil {
		fmt.Fprintf(stderr, "feint: nothing recorded on %s\n", *addr)
		return exitError
	}

	if *lines > 0 {
		fmt.Fprintln(stdout, strings.TrimPrefix(tail(inst.Log, *lines), "  "))
	} else {
		raw, readErr := os.ReadFile(inst.Log) //nolint:gosec // the path comes from the instance record
		if readErr != nil {
			fmt.Fprintf(stderr, "feint: %v\n", readErr)
			return exitError
		}
		_, _ = stdout.Write(raw)
	}
	if !*follow {
		return exitOK
	}

	// Follow by polling rather than by inotify: one dependency-free loop, and
	// the log of a local emulator does not grow fast enough for the difference
	// to matter.
	file, err := os.Open(inst.Log) //nolint:gosec // the path comes from the instance record
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	buf := make([]byte, 4096)
	for {
		n, readErr := file.Read(buf)
		if n > 0 {
			_, _ = stdout.Write(buf[:n])
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			fmt.Fprintf(stderr, "feint: %v\n", readErr)
			return exitError
		}
		if n == 0 {
			time.Sleep(200 * time.Millisecond)
		}
	}
}

// discardNotice answers the line to print before a stop that will lose the
// session's resources, or "" when it will not.
//
// The store is memory and `docs/limits.md` says so in its lifecycle table, but
// that sentence lives on a page a user reads *after* being bitten. At the only
// moment the information is actionable, the verb that destroys the session was
// mute (#182). `restart` is the sharper case: an operator reaches for it
// mid-session and pays with the whole fixture.
//
// Three properties, each one a "what must not happen" from that issue:
//
//   - it never fires when --state was recorded, because the data is being kept
//     and a warning on every healthy stop is the pattern people are trained to
//     ignore. That check comes first, so a --state run pays no request at all;
//   - the count comes from the endpoint `status` already reads, never from a
//     second reader that could drift from it;
//   - it is best-effort. An instance that no longer answers health is stopped
//     exactly as before, within the same timeout, and the notice is dropped
//     rather than waited for.
//
// One honest note about that last line, because falsifying it is what found it.
// The `err != nil` half of the condition is **not load-bearing today**: measured
// on Go 1.26, json.Decoder.Decode buffers the whole value before assigning, so a
// truncated or refused answer leaves Resources at zero and the second half
// already returns "". No mutation can distinguish the two, and a guard no test
// can distinguish is not a control — it is kept as stated insurance against a
// future encoding/json that assigns as it parses, which would turn a failed read
// into a false accusation. Stated rather than implied, so nobody later reads it
// as a proven guard.
//
// stderr, one line, never a prompt and never a refusal: CI drives stop, and its
// exit codes are a frozen surface.
//
// TestStopSaysWhatItIsAboutToDiscard fails without this.
func discardNotice(inst *Instance) string {
	for _, arg := range inst.Args {
		if arg == "--state" || strings.HasPrefix(arg, "--state=") {
			return ""
		}
	}
	health, err := fetchHealth(inst.Addr)
	if err != nil || health.Resources == 0 {
		return ""
	}
	return fmt.Sprintf("feint: discarding %d resource(s) (started without --state); "+
		"`feint snapshot save <name>` before stopping would have kept them\n", health.Resources)
}
