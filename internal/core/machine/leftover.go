package machine

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
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
// The interface being gone is not the only way a service outlives its network
// (#342): on 2026-08-18 a dnsmasq (pid 612421) held 10.50.2.1 on a bridge that
// had survived *with* it — the network object was gone from the runtime, the
// interface and the service were not. The question the operator needs answered
// is therefore about the network, not the interface: a service whose network
// the runtime no longer knows is a leftover whichever half of its plumbing
// survived. TestLeftoverDHCPFindsTheServiceWhoseNetworkIsGone fails when only
// the vanished-interface half is asked.
//
// The rule of cli.go applies unchanged: well formed is not authorised. A
// dnsmasq on the operator's host may belong to libvirt, to another Incus
// project or to the operator's own lab, and none of those is ours to name,
// let alone to signal. A process is attributable to this emulator only when
// its --interface carries the prefix NetworkName writes AND the network it
// served is demonstrably gone — the interface no longer exists, or no project
// of the runtime knows a network under that name while the process runs off
// the runtime's own state directory. A service whose network is alive is
// somebody's working arrangement, whatever its name.
// TestLeftoverDHCPRefusesAProcessItCannotAttribute holds the refusal, and the
// falsification spec dhcp-leftover-ownership.json proves the tests bite.

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
// whose network is gone — the interface with it, or the network object alone.
type DHCPLeftover struct {
	PID int
	// Interface is the fnt- interface the service was started for.
	Interface string
	// Addresses are the listen addresses the service still holds; the gateway
	// of the block the next run will fail to take.
	Addresses []string
	// InterfaceAlive says the interface survived alongside its service (#342):
	// what died is the network object. The interface is then a bridge the
	// runtime no longer manages, which the sweep must name and never delete —
	// nothing left on the host proves the emulator created it.
	InterfaceAlive bool
}

// String names the leftover the way clean and doctor report it.
func (l DHCPLeftover) String() string {
	held := strings.Join(l.Addresses, ", ")
	if held == "" {
		held = "an address"
	}
	if l.InterfaceAlive {
		return fmt.Sprintf("dnsmasq pid %d holds %s for %s, an interface that outlived its network", l.PID, held, l.Interface)
	}
	return fmt.Sprintf("dnsmasq pid %d holds %s for the vanished interface %s", l.PID, held, l.Interface)
}

// LeftoverDHCP scans the host's processes for DHCP services that outlived the
// emulator's networks. Read-only: it opens /proc, stats /sys/class/net, asks
// the runtime for its network listing, and issues no signal and no mutating
// command. A host with no /proc has no Incus runtime either, so it reports
// nothing rather than an error.
func LeftoverDHCP() ([]DHCPLeftover, error) {
	procs, err := hostProcesses()
	if err != nil {
		return nil, err
	}
	return leftoverDHCP(procs, interfaceGone, networkUnknown()), nil
}

// leftoverDHCP classifies the given processes. gone answers whether a named
// interface is absent from the host; unknown answers whether the runtime
// positively has no network under the name. Both are parameters so a test can
// hold the classification without owning the station's interfaces.
func leftoverDHCP(procs []HostProcess, gone, unknown func(string) bool) []DHCPLeftover {
	var out []DHCPLeftover
	for _, proc := range procs {
		if leftover, ok := classifyDHCP(proc, gone, unknown); ok {
			out = append(out, leftover)
		}
	}
	return out
}

// classifyDHCP answers the two questions for one process, in order: is it
// attributable to this emulator, and is it a leftover. Both attribution
// criteria are required — the prefix alone would claim a live service, and
// "network gone" alone would claim libvirt's or the operator's own dnsmasq.
//
// A vanished interface settles both at once. An interface that survived does
// not (#342): there the network object must be demonstrably absent from the
// runtime, and the process must run off the runtime's own state directory —
// the strongest evidence is demanded exactly when the subject is still alive,
// because the cost of a wrong claim is a signal to somebody's working service.
func classifyDHCP(proc HostProcess, gone, unknown func(string) bool) (DHCPLeftover, bool) {
	if len(proc.Argv) == 0 || filepath.Base(proc.Argv[0]) != "dnsmasq" {
		return DHCPLeftover{}, false
	}
	interfaces := flagValues(proc.Argv, "--interface")
	if len(interfaces) == 0 {
		// libvirt's dnsmasq carries its configuration in a file, not on argv;
		// nothing here says whose it is, so it is not ours to classify.
		return DHCPLeftover{}, false
	}
	alive := false
	for _, name := range interfaces {
		// Ours by the prefix NetworkName writes, well formed enough to become
		// a /sys path, and its network demonstrably gone. A process serving
		// several interfaces is attributable only if every one of them passes:
		// a mixed set is somebody else's arrangement, and it is left alone.
		if !ownedNetwork(name) || !safeName.MatchString(name) {
			return DHCPLeftover{}, false
		}
		if gone(name) {
			continue
		}
		if !unknown(name) || !runtimeServiceOf(proc, name) {
			return DHCPLeftover{}, false
		}
		alive = true
	}
	return DHCPLeftover{
		PID:            proc.PID,
		Interface:      interfaces[0],
		Addresses:      flagValues(proc.Argv, "--listen-address"),
		InterfaceAlive: alive,
	}, true
}

// runtimeServiceOf reports that a process runs off the runtime's own state
// directory for the named network — the --conf-file and --dhcp-leasefile Incus
// puts on every dnsmasq it launches. It is the extra evidence the live-interface
// case demands: an fnt- name on argv is claimable by anybody, a path under the
// runtime's state directory only by a service the runtime itself started.
func runtimeServiceOf(proc HostProcess, network string) bool {
	marker := "/var/lib/incus/networks/" + network + "/"
	for _, arg := range proc.Argv {
		if strings.Contains(arg, marker) {
			return true
		}
	}
	return false
}

// networkUnknown builds the predicate for "the runtime positively has no
// network under this name", from one listing across every project. Conservative
// three times over: a runtime that cannot be asked, a listing that cannot be
// decoded, and a name found in any project all answer false, so the predicate
// can only ever promote a process on a listed-nowhere name. Without the
// all-projects listing a live network in a neighbouring project would read as
// absent, which is the exact wrong-claim classifyDHCP refuses to make.
func networkUnknown() func(string) bool {
	out, err := exec.Command("incus", "query", "/1.0/networks?recursion=1&all-projects=true").Output()
	if err != nil {
		return func(string) bool { return false }
	}
	var items []struct {
		Name    string `json:"name"`
		Managed bool   `json:"managed"`
	}
	if err := json.Unmarshal(out, &items); err != nil {
		return func(string) bool { return false }
	}
	managed := make(map[string]bool, len(items))
	for _, item := range items {
		if item.Managed {
			managed[item.Name] = true
		}
	}
	return func(name string) bool { return !managed[name] }
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
	return signalLeftover(leftover, procArgv, interfaceGone, networkUnknown(), sendSignal, syscall.SIGTERM)
}

// probeSignal is the signal that is not one. Number 0 runs every permission
// check the kernel would run for a real signal and then delivers nothing, which
// is the only acceptable shape for a question whose subject is a process
// nobody here started. It is named rather than written as a literal so that the
// single place it could quietly become a real signal has a name to search for.
const probeSignal = syscall.Signal(0)

// CanEndLeftover answers whether this user may end a leftover, without ending
// it. A nil error means a sweep run by this user would get through; anything
// else is the reason it would not, os.ErrPermission being the one that matters
// (#375).
//
// It exists because the sweep's usual refusal is not "this is not ours" but
// "this belongs to the incus user", and until now the only way to learn that
// was to run the sweep and read its failure. That cost the runtime leg of
// `mise run evidence:update` three consecutive runs: each one died on a dnsmasq
// the sweep could see, name and not signal, and the printed remedy waited for a
// human to notice it. A question that can be asked before the run is what turns
// that into a doorstep refusal.
//
// The re-classification of signalLeftover is not skipped for the probe, and the
// reason is the same as for the signal: a probe aimed at a pid the scan no
// longer attributes would be asking about a stranger. Refusing to answer costs
// nothing; answering about the wrong process is how the pid recycling this file
// already guards against comes back through the check meant to prevent it.
//
// TestCanEndLeftoverAsksWithoutEnding fails when the probe carries a real
// signal.
func CanEndLeftover(leftover DHCPLeftover) error {
	return signalLeftover(leftover, procArgv, interfaceGone, networkUnknown(), sendSignal, probeSignal)
}

// sendSignal is the one path to the kernel both callers share, so the signal
// they differ by is the only thing they differ by.
func sendSignal(pid int, sig syscall.Signal) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(sig)
}

func signalLeftover(leftover DHCPLeftover, argvOf func(int) ([]string, error), gone, unknown func(string) bool, signal func(int, syscall.Signal) error, sig syscall.Signal) error {
	argv, err := argvOf(leftover.PID)
	if err != nil {
		// The process is already gone, which is the goal state, not a failure.
		return nil
	}
	current, ok := classifyDHCP(HostProcess{PID: leftover.PID, Argv: argv}, gone, unknown)
	if !ok || current.Interface != leftover.Interface {
		return fmt.Errorf("pid %d is no longer the DHCP service that outlived %s; refusing to signal it",
			leftover.PID, leftover.Interface)
	}
	return signal(leftover.PID, sig)
}

// A BlockHolder is any DHCP service, whoever owns it, holding an address
// inside a block the emulator wants (#342). Attribution is deliberately not
// part of the question: at the moment a network create has just failed, the
// wanted block is known exactly, and the operator's question is who sits on
// it, not whose process it is. Data only — a holder that was never attributed
// to the emulator is never signalled, so the answer names and does not touch.
type BlockHolder struct {
	PID int
	// Command is the process name, as /proc shows it.
	Command string
	// Address is the listen address inside the wanted block.
	Address string
}

// String names the holder the way the create error reports it.
func (h BlockHolder) String() string {
	return fmt.Sprintf("%s pid %d listens on %s", h.Command, h.PID, h.Address)
}

// DHCPHolders scans the host for DHCP services listening inside the given
// block, whoever owns them. Read-only, like LeftoverDHCP above.
func DHCPHolders(block netip.Prefix) []BlockHolder {
	procs, err := hostProcesses()
	if err != nil {
		return nil
	}
	return dhcpHolders(procs, block)
}

func dhcpHolders(procs []HostProcess, block netip.Prefix) []BlockHolder {
	var out []BlockHolder
	for _, proc := range procs {
		if len(proc.Argv) == 0 || filepath.Base(proc.Argv[0]) != "dnsmasq" {
			continue
		}
		for _, listen := range flagValues(proc.Argv, "--listen-address") {
			addr, err := netip.ParseAddr(listen)
			if err != nil || !block.Contains(addr) {
				continue
			}
			out = append(out, BlockHolder{
				PID:     proc.PID,
				Command: filepath.Base(proc.Argv[0]),
				Address: listen,
			})
			break
		}
	}
	return out
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
