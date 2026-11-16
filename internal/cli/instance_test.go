package cli

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

// fakeProcComm replaces the /proc seam for the length of one test, so a liveness
// decision about pid 1 is taken against a name the test chose rather than
// against the process this runner happens to be.
func fakeProcComm(t *testing.T, comm string, err error) {
	t.Helper()
	restore := procComm
	procComm = func(int) (string, error) { return comm, err }
	t.Cleanup(func() { procComm = restore })
}

// TestARecordNamingPidOneIsStaleRatherThanForeign is cited by alive().
//
// Measured on 2026-11-13: a run refused to start while its port was free,
// because the instance record named pid 1. Signal 0 on init answers EPERM to an
// unprivileged caller, and alive() read that as Foreign — a process this tool
// did not start and must never signal — so every message built from it said
// "another emulator" or "no longer belongs to feint" about init, and the advice
// they carry ("kill it") names a pid this tool would refuse to signal (#924).
//
// Nothing this tool starts is pid 1 on a host where it is started by a shell, so
// the record is stale by construction and the port is free.
func TestARecordNamingPidOneIsStaleRatherThanForeign(t *testing.T) {
	if os.Getpid() == 1 {
		t.Skip("this process is pid 1; a record naming 1 can be the emulator reading it")
	}
	// The runner's own init is not necessarily named systemd, and the point of
	// this test is the branch that reads the name. Pin it.
	fakeProcComm(t, "systemd", nil)
	home := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", home)
	t.Setenv("XDG_STATE_HOME", "")

	const addr = "127.0.0.1:4599"
	dir := writeInstance(t, addr, 1)

	inst, err := loadInstance(addr)
	if err != nil || inst == nil {
		t.Fatalf("load the record: %+v %v", inst, err)
	}
	if got := inst.alive(); got != Dead {
		t.Fatalf("a record naming pid 1 reads as %v; init is not an emulator this tool started", got)
	}

	// The words matter as much as the enum: a stop that reports a recycled pid
	// sends the operator looking for a process that is not there.
	var stdout, stderr bytes.Buffer
	if code := stop([]string{"--addr", addr}, &stdout, &stderr); code != exitOK {
		t.Fatalf("stopping a record naming pid 1 failed (%d): %s%s", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), "no longer belongs to feint") {
		t.Errorf("a record naming pid 1 was reported as a recycled pid: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "already stopped") {
		t.Errorf("the record was discarded without saying it was stale: %s", stdout.String())
	}
	if _, err := os.Stat(dir + "/instance.json"); !os.IsNotExist(err) {
		t.Errorf("the stale record survived the stop: %v", err)
	}
}

// TestAPidOneRecordInsideTheEmulatorsOwnImageIsNotStale is cited by alive() and
// pidOneIsThisEmulator.
//
// The image this repository builds runs the emulator as pid 1 (Dockerfile,
// ENTRYPOINT /feint). From `docker exec <container> feint status` this process
// is not pid 1 while the record — naming 1 — is right, so the early "pid 1 is
// init" return cannot be unconditional. /proc/1/comm is what tells the two
// apart: a name that is this program falls through to the ordinary checks, and
// anything else is stale.
func TestAPidOneRecordInsideTheEmulatorsOwnImageIsNotStale(t *testing.T) {
	if os.Getpid() == 1 {
		t.Skip("this process is pid 1; the exec case this test models needs another pid")
	}
	fakeProcComm(t, "feint", nil)

	// The decision itself, which needs no signal and no real init.
	if !pidOneIsThisEmulator("/feint") {
		t.Fatal("pid 1 named feint and the record was still read as stale")
	}
	// Both spellings the comm check accepts: the literal, and the base name of
	// the binary the record carries.
	if !namesThisEmulator("feint", "/usr/local/bin/feint-linux") {
		t.Fatal("the literal name did not match")
	}
	if !namesThisEmulator("feint-linux", "/usr/local/bin/feint-linux") {
		t.Fatal("the binary's base name did not match")
	}
	if namesThisEmulator("feint-other", "/usr/local/bin/feint-linux") {
		t.Fatal("a different command name matched this emulator's binary")
	}

	// And the whole decision: the record is not discarded as stale. The rest is
	// the machine's own answer — root in a container reaches the comm check and
	// answers Alive, an unprivileged exec cannot signal pid 1 and answers
	// Foreign — so the assertion is the one this change owns: not Dead.
	inst := &Instance{PID: 1, Addr: "127.0.0.1:1", Binary: "/feint"}
	if got := inst.alive(); got == Dead {
		t.Fatalf("a record naming pid 1 was discarded although pid 1 names this emulator: %v", got)
	}
}

// TestAnUnreadablePidOneNameIsStale pins the fallback: a host without /proc has
// init at pid 1, and nothing this tool starts there is init.
func TestAnUnreadablePidOneNameIsStale(t *testing.T) {
	fakeProcComm(t, "", errors.New("no /proc on this host"))
	if pidOneIsThisEmulator("/feint") {
		t.Fatal("an unreadable /proc/1/comm was read as this emulator")
	}
}
