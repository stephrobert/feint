package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Where a detached emulator records itself, and how a caller decides whether it
// is still there.
//
// This is the part the lifecycle brief calls the whole difficulty, and it is:
// starting a process is easy, and knowing whether the one you recorded an hour
// ago is still the one holding that pid is not. A recycled pid signalled by
// `feint stop` is somebody else's process killed by this tool, which is the
// single worst thing a developer tool can do to a workstation.

// snapshotsDirName is the one place this name is written.
//
// It has to be shared rather than repeated, because two directories answer to
// it under different roots: snapshots follow persistentHome and instance state
// follows stateHome, and those two resolve to the same path whenever
// XDG_RUNTIME_DIR is unset or XDG_STATE_HOME points at it. When they coincide,
// reapStaleInstances walks over the snapshot directory, and a name spelled twice
// is a name that will one day be spelled once — which here means deleting work
// an operator saved on purpose.
const snapshotsDirName = "snapshots"

// Instance is what one detached emulator recorded about itself.
type Instance struct {
	PID     int      `json:"pid"`
	Addr    string   `json:"addr"`
	Started string   `json:"started_at"`
	Binary  string   `json:"binary"`
	Args    []string `json:"args"`
	Log     string   `json:"log"`
	// Dir is the directory holding this instance's files. Not serialised: it is
	// where the file was read from, so writing it would let the two disagree.
	Dir string `json:"-"`
}

// stateHome resolves where instance state lives, following the XDG basedir
// spec in the order the specification gives.
//
// XDG_RUNTIME_DIR first, because it is what runtime state is for and it is
// cleaned on logout — a pidfile surviving a reboot is the stale case nobody
// wants to hit twice. Then XDG_STATE_HOME, then its documented default. The
// three cases exist so a container with neither variable set still works.
func stateHome() (string, error) {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "feint"), nil
	}
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "feint"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no XDG_RUNTIME_DIR, no XDG_STATE_HOME, and no home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "feint"), nil
}

// persistentHome resolves where state that must survive a logout lives.
//
// Same spec, one entry shorter: XDG_RUNTIME_DIR is skipped, because on a Linux
// desktop it is a tmpfs wiped when the session ends. That is the right property
// for a pidfile and the wrong one for a snapshot somebody named in order to come
// back to it.
func persistentHome() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "feint"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no XDG_STATE_HOME and no home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "feint"), nil
}

// instanceDir is one directory per listen address.
//
// Keyed by the address rather than by a fixed name, so two emulators can run at
// once. The conformance suites will want that the day they run providers in
// parallel, and retrofitting a key is worse than choosing one now.
func instanceDir(addr string) (string, error) {
	home, err := stateHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, flattenAddr(addr)), nil
}

// flattenAddr turns a listen address into a directory name. Separators become
// underscores; an empty host keeps its colon's place so ":4599" and "x:4599" do
// not collide.
func flattenAddr(addr string) string {
	replacer := strings.NewReplacer(":", "_", "/", "_", "[", "", "]", "")
	return replacer.Replace(addr)
}

// loadInstance reads the recorded instance for an address. It returns nil with
// no error when nothing is recorded, because "not running" is an answer rather
// than a failure.
func loadInstance(addr string) (*Instance, error) {
	dir, err := instanceDir(addr)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "instance.json")) //nolint:gosec // a path this package builds
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var inst Instance
	if err := json.Unmarshal(raw, &inst); err != nil {
		return nil, fmt.Errorf("%s: the instance file is not readable: %w", dir, err)
	}
	inst.Dir = dir
	return &inst, nil
}

// save writes the instance file, creating its directory.
func (i *Instance) save() error {
	if err := os.MkdirAll(i.Dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(i, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(i.Dir, "instance.json"), raw, 0o600)
}

// remove deletes the instance file. A missing file is success: the caller wants
// it gone, and it is.
//
// The directory and its log survive on purpose. `feint logs --addr` reads that
// file, and the moment an operator wants it most is right after the emulator
// stopped — a crash is read after the fact or not at all. Reaping is therefore
// not stop's job; it is reapStaleInstances, called by `feint clean`, where the
// operator has asked for a sweep rather than an exit.
func (i *Instance) remove() error {
	err := os.Remove(filepath.Join(i.Dir, "instance.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// reapStaleInstances removes the state directories of emulators that are gone,
// and returns how many went.
//
// Measured on a development station after one day: fourteen directories for two
// live emulators. `stop` clears the record and leaves the directory holding the
// log, which is right on its own (see remove) and wrong in aggregate — nothing
// ever collected them, so `/run/user/1000/feint/` stopped being readable as an
// answer to "what is running". Twelve of the fourteen described nothing at all.
//
// Stale means one of three things, and the third is the one that matters: no
// instance file at all (a stopped emulator's leftovers), a recorded pid that
// nothing holds any more, or a pid that now belongs to a stranger. That last
// case is exactly what alive() calls Foreign, and reusing it here rather than
// re-deriving it is the point: the rule for "is this still ours" is written
// once, and a reaper that got it wrong would delete the directory of a running
// emulator.
//
// A live instance is never touched, whatever its age. An emulator someone left
// running overnight is not litter.
//
// TestCleanReapsOnlyDeadInstances fails without this.
func reapStaleInstances() (int, error) {
	home, err := stateHome()
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(home)
	if errors.Is(err, os.ErrNotExist) {
		// Nothing was ever started. That is not a failure.
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	reaped := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(home, entry.Name())

		// Snapshots are not instance state and outlive every emulator by design;
		// removing them would destroy work an operator named in order to come
		// back to it.
		if entry.Name() == snapshotsDirName {
			continue
		}

		raw, readErr := os.ReadFile(filepath.Join(dir, "instance.json")) //nolint:gosec // a path this package builds
		switch {
		case errors.Is(readErr, os.ErrNotExist):
			// No record: a stopped emulator's leftovers.
		case readErr != nil:
			// Unreadable rather than absent. Left alone and reported by the
			// caller's error, because deleting what cannot be read is how a
			// sweep destroys something it never identified.
			return reaped, fmt.Errorf("%s: %w", dir, readErr)
		default:
			var inst Instance
			if err := json.Unmarshal(raw, &inst); err != nil {
				return reaped, fmt.Errorf("%s: the instance file is not readable: %w", dir, err)
			}
			inst.Dir = dir
			if inst.alive() == Alive {
				continue
			}
		}

		if err := os.RemoveAll(dir); err != nil {
			return reaped, err
		}
		reaped++
	}
	return reaped, nil
}

// Liveness is what a recorded instance turned out to be.
type Liveness int

const (
	// Dead: nothing holds that pid.
	Dead Liveness = iota
	// Alive: the pid exists and it is this emulator.
	Alive
	// Foreign: the pid exists and belongs to something else. The instance file
	// is stale and the pid was recycled — the one case where signalling would
	// kill an unrelated process.
	Foreign
)

// alive decides whether a recorded instance is still the process that recorded
// itself, and it is deliberately suspicious.
//
// Three questions, in order of cost. Does the pid exist at all — signal 0 is the
// portable way to ask without sending anything. Is it still this program —
// /proc/<pid>/comm answers on Linux, and where /proc does not exist the question
// falls back to the only evidence that cannot be faked by a pid: whether the
// recorded address answers this emulator's own health endpoint.
//
// The fallback is not a formality. A pid check alone is what makes a tool signal
// a stranger after a reboot recycles the number, and the health probe is what
// distinguishes "the emulator I started" from "some process that inherited its
// pid".
func (i *Instance) alive() Liveness {
	if i.PID <= 0 {
		return Dead
	}
	// Signal 0 performs error checking without sending a signal: ESRCH means no
	// such process, EPERM means it exists and belongs to somebody else.
	err := syscall.Kill(i.PID, 0)
	switch {
	case errors.Is(err, syscall.ESRCH):
		return Dead
	case errors.Is(err, syscall.EPERM):
		// It exists and this user cannot signal it. Whatever it is, it is not a
		// process this tool started, so it must never be signalled.
		return Foreign
	case err != nil:
		return Foreign
	}

	if comm, readErr := os.ReadFile("/proc/" + strconv.Itoa(i.PID) + "/comm"); readErr == nil {
		name := strings.TrimSpace(string(comm))
		if name == filepath.Base(i.Binary) || name == "feint" {
			return Alive
		}
		return Foreign
	}

	// No /proc: ask the address whether this emulator answers on it. A stranger
	// holding the pid will not.
	if healthy(i.Addr, 500*time.Millisecond) {
		return Alive
	}
	return Foreign
}

// healthy probes the emulator's own health endpoint.
//
// It is the only check that proves the thing listening is this emulator rather
// than anything else that happened to bind the port, which matters for `wait`:
// waiting for a port to open would return as soon as an unrelated server bound
// it, and the caller would then drive a stranger.
func healthy(addr string, timeout time.Duration) bool {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(healthURL(addr)) //nolint:noctx // the timeout is the client's
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return false
	}
	var body struct {
		Status    string   `json:"status"`
		Providers []string `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false
	}
	// Both fields, because a bare 200 with "ok" in it could come from anything.
	return body.Status == "ok" && len(body.Providers) > 0
}

// healthURL builds the health address from a listen address, filling in the
// host a bare ":4599" leaves out.
func healthURL(addr string) string {
	host := addr
	if strings.HasPrefix(addr, ":") {
		host = "127.0.0.1" + addr
	}
	return "http://" + host + "/_feint/health"
}
