package machine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// An interrupted run can leave the runtime holding a resource that outlives
// the object it belonged to: a dnsmasq still bound to its gateway address
// after the fnt- interface under it is gone (#316). `ip addr` shows nothing,
// `incus network list` shows nothing, and the sweep in incus_prune.go finds
// nothing to remove, because all three look at objects and the leftover is a
// process. The next run that wants the same block then dies minutes in on
// "dnsmasq: failed to create listening socket: Address already in use" —
// measured twice, on 2026-08-18 and 2026-08-19.
//
// The rule of cli.go applies unchanged: well formed is not authorised. A
// dnsmasq on the operator's host may belong to libvirt, to another Incus
// project or to the operator's own lab, and none of those is ours to name,
// let alone to signal. A process is attributable to this emulator only when
// its --interface carries the prefix NetworkName writes AND that interface no
// longer exists; one whose interface is alive is not a leftover at all, it is
// somebody's working service. TestLeftoverDHCPRefusesAProcessItCannotAttribute
// holds the refusal, and the falsification spec dhcp-leftover-ownership.json
// proves the test bites.

// HostProcess is one process on the operator's host, as read from /proc. The
// command line of a foreign process is data it wrote about itself, not
// something this emulator verified, which is why attribution below demands
// the interface to be both ours by prefix and demonstrably gone.
type HostProcess struct {
	PID  int
	Argv []string
}

// DHCPLeftover is a DHCP service that outlived the emulator network it served:
// a dnsmasq whose --interface names a network only this emulator derives, and
// whose interface no longer exists on the host.
type DHCPLeftover struct {
	PID int
	// Interface is the vanished fnt- interface the service was started for.
	Interface string
	// Addresses are the listen addresses the service still holds; the gateway
	// of the block the next run will fail to take.
	Addresses []string
}

// String names the leftover the way clean and doctor report it.
func (l DHCPLeftover) String() string {
	held := strings.Join(l.Addresses, ", ")
	if held == "" {
		held = "an address"
	}
	return fmt.Sprintf("dnsmasq pid %d holds %s for the vanished interface %s", l.PID, held, l.Interface)
}

// LeftoverDHCP scans the host's processes for DHCP services that outlived the
// emulator's networks. Read-only: it opens /proc and stats /sys/class/net,
// and issues no command and no signal. A host with no /proc has no Incus
// runtime either, so it reports nothing rather than an error.
func LeftoverDHCP() ([]DHCPLeftover, error) {
	procs, err := hostProcesses()
	if err != nil {
		return nil, err
	}
	return leftoverDHCP(procs, interfaceGone), nil
}

// leftoverDHCP classifies the given processes. gone answers whether a named
// interface is absent from the host; it is a parameter so a test can hold the
// classification without owning the station's interfaces.
func leftoverDHCP(procs []HostProcess, gone func(string) bool) []DHCPLeftover {
	var out []DHCPLeftover
	for _, proc := range procs {
		if leftover, ok := classifyDHCP(proc, gone); ok {
			out = append(out, leftover)
		}
	}
	return out
}

// classifyDHCP answers the two questions for one process, in order: is it
// attributable to this emulator, and is it a leftover. Both attribution
// criteria are required — the prefix alone would claim a live service, and
// "interface gone" alone would claim libvirt's or the operator's own dnsmasq.
func classifyDHCP(proc HostProcess, gone func(string) bool) (DHCPLeftover, bool) {
	if len(proc.Argv) == 0 || filepath.Base(proc.Argv[0]) != "dnsmasq" {
		return DHCPLeftover{}, false
	}
	interfaces := flagValues(proc.Argv, "--interface")
	if len(interfaces) == 0 {
		// libvirt's dnsmasq carries its configuration in a file, not on argv;
		// nothing here says whose it is, so it is not ours to classify.
		return DHCPLeftover{}, false
	}
	for _, name := range interfaces {
		// Ours by the prefix NetworkName writes, well formed enough to become
		// a /sys path, and demonstrably gone. A process serving several
		// interfaces is attributable only if every one of them passes: a mixed
		// set is somebody else's arrangement, and it is left alone.
		if !ownedNetwork(name) || !safeName.MatchString(name) || !gone(name) {
			return DHCPLeftover{}, false
		}
	}
	return DHCPLeftover{
		PID:       proc.PID,
		Interface: interfaces[0],
		Addresses: flagValues(proc.Argv, "--listen-address"),
	}, true
}

// flagValues collects the values of a repeatable flag, in both spellings a
// command line carries: --flag=value and --flag value. Incus writes the first;
// the second costs nothing and keeps the parse honest about what dnsmasq
// itself accepts.
func flagValues(argv []string, flag string) []string {
	var out []string
	for i := 0; i < len(argv); i++ {
		if value, found := strings.CutPrefix(argv[i], flag+"="); found {
			out = append(out, value)
			continue
		}
		if argv[i] == flag && i+1 < len(argv) {
			out = append(out, argv[i+1])
			i++
		}
	}
	return out
}

// TerminateLeftover ends a leftover DHCP service with SIGTERM, after
// re-establishing at the moment of the signal that the pid still is that
// leftover. Pids are reused, and the time between a scan and a signal is
// exactly where a recycled pid would turn a sweep into a shot at an innocent
// process; so the classification runs again on the live /proc entry, and
// anything that no longer matches is refused.
// TestTerminateLeftoverRefusesAPidThatIsNoLongerTheLeftover fails without the
// re-check.
func TerminateLeftover(leftover DHCPLeftover) error {
	return terminateLeftover(leftover, procArgv, interfaceGone, func(pid int) error {
		process, err := os.FindProcess(pid)
		if err != nil {
			return err
		}
		return process.Signal(syscall.SIGTERM)
	})
}

func terminateLeftover(leftover DHCPLeftover, argvOf func(int) ([]string, error), gone func(string) bool, signal func(int) error) error {
	argv, err := argvOf(leftover.PID)
	if err != nil {
		// The process is already gone, which is the goal state, not a failure.
		return nil
	}
	current, ok := classifyDHCP(HostProcess{PID: leftover.PID, Argv: argv}, gone)
	if !ok || current.Interface != leftover.Interface {
		return fmt.Errorf("pid %d is no longer the DHCP service that outlived %s; refusing to signal it",
			leftover.PID, leftover.Interface)
	}
	return signal(leftover.PID)
}

// hostProcesses reads every process the host will show, pid and command line.
// A process that vanishes between the listing and the read is skipped: /proc
// is a moving target by construction.
func hostProcesses() ([]HostProcess, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read /proc: %w", err)
	}
	var procs []HostProcess
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		argv, err := procArgv(pid)
		if err != nil || len(argv) == 0 {
			continue
		}
		procs = append(procs, HostProcess{PID: pid, Argv: argv})
	}
	return procs, nil
}

// procArgv reads one process's command line. /proc/<pid>/cmdline is
// NUL-separated and world-readable, which is what makes this survey possible
// without privileges.
func procArgv(pid int) ([]string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil, err
	}
	fields := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	if len(fields) == 1 && fields[0] == "" {
		return nil, nil
	}
	return fields, nil
}

// interfaceGone reports that a named interface is absent from the host.
// Conservative on purpose: only a clean "does not exist" counts as gone, so a
// transient read failure can never promote a live service to leftover.
func interfaceGone(name string) bool {
	_, err := os.Stat(filepath.Join("/sys/class/net", name))
	return errors.Is(err, os.ErrNotExist)
}
