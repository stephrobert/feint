package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/s3"
)

// The lab half of `feint run` (#848): the command owns the emulator, the
// child's environment and the teardown, so that Feint infrastructure leaves
// the user's mental model rather than being a sequence they must remember.
//
// The interception half lives in run.go and came first (#849). Keeping them in
// one command was not a naming accident: #848's tenth required behaviour is
// "never silently reaches a real cloud through a known escape path", which is
// exactly what the interception already does, so the lab wraps it rather than
// being a second command that must remember to use it.
//
// ## What it refuses to do by itself
//
// It does not start a machine runtime. `--runtime` is #848's ninth behaviour
// and it is deliberately absent here: starting containers is a side effect on
// the operator's station, `up` asks for it through a declaration with its own
// refusal rules, and inheriting those rules is a change of its own. A flag
// that silently started containers would be the opposite of what this command
// is for.

// labReadyTimeout is how long the owned emulator has to answer.
//
// Short on purpose: this is an in-process HTTP server on loopback with no
// machine runtime behind it, so a second is already generous and a minute
// would turn a port collision into a minute of silence.
const labReadyTimeout = 15 * time.Second

// ownedLab is an emulator this command started and must therefore stop.
type ownedLab struct {
	addr     string
	endpoint string
	dir      string
	// session is the lab's identity, #848's first required behaviour. It is
	// the private directory's basename, which is already unique per run and
	// already the thing an operator would have to name to clean up by hand.
	// Inventing a second identifier would give two names to one lab.
	session string
}

// freeLoopbackAddr answers an address nothing is listening on.
//
// Racy by nature — anything may take the port between the close and the
// start — and that is acceptable here because the loser is loud: `start`
// refuses a port it cannot bind and says so. The alternative, letting the
// emulator pick and reading the port back, would need a channel into a
// detached process that does not exist.
func freeLoopbackAddr() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("find a free loopback port: %w", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return "", fmt.Errorf("release the probed port: %w", err)
	}
	return addr, nil
}

// startOwnedLab brings up an emulator this run owns, with a byte store of its
// own so the S3 doors exist and the interception has something to intercept.
//
// It composes the existing `start` verb rather than a second way to run an
// emulator, which is the shape `up` already uses: one lifecycle, given a
// written-down command line. A second spawn path would be a second place for
// the instance registry, the readiness wait and the refusal to adopt a running
// instance to drift.
//
// Everything it prints goes to stderr, and that is a contract rather than a
// habit: with --json, stdout carries the receipt and nothing else, so a suite
// can read it without stripping progress lines first. #848 asks for a
// machine-readable answer "without parsing prose", and a progress line on
// stdout is prose.
// TestARunOwnsItsLabAndLeavesNothing fails without this.
func startOwnedLab(mode string, stderr io.Writer) (*ownedLab, error) {
	addr, err := freeLoopbackAddr()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "feint-lab-")
	if err != nil {
		return nil, fmt.Errorf("create the lab's private directory: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("read back the lab's directory: %w", err)
	}
	// Verified, not set, for the reason runChild's own directory is: a Chmod
	// writes an assumption down as a value nobody reads back.
	if mode := info.Mode().Perm(); mode&^os.FileMode(0o700) != 0 {
		return nil, fmt.Errorf("%s is %#o, wider than the 0700 this run requires", dir, mode)
	}

	args := labArgs(addr, dir, mode)
	if code := start(args, io.Discard, stderr); code != exitOK {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(stderr, "feint run: could not remove %s: %v\n", dir, err)
		}
		return nil, fmt.Errorf("the lab did not start on %s", addr)
	}
	fmt.Fprintf(stderr, "feint run: lab on http://%s\n", addr)
	return &ownedLab{addr: addr, endpoint: "http://" + addr, dir: dir,
		session: filepath.Base(dir)}, nil
}

// close stops the lab and removes its bytes. Both halves are attempted even
// when the first fails: a stop that errors must not leave the directory
// behind too.
func (l *ownedLab) close(stderr io.Writer) error {
	var failures []string
	if code := stop([]string{"--addr", l.addr}, io.Discard, stderr); code != exitOK {
		failures = append(failures, "the lab on "+l.addr+" did not stop")
	}
	if err := os.RemoveAll(l.dir); err != nil {
		failures = append(failures, fmt.Sprintf("could not remove %s: %v", l.dir, err))
	}
	if len(failures) != 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

// keep abandons the lab on purpose and prints how to reach and remove it.
//
// On stderr with the rest of the prose, so that --json keeps its stream. The
// exact follow-up command is printed rather than described: #848 asks for it,
// and an operator debugging a failed run should not have to reconstruct a
// temporary path from a sentence.
func (l *ownedLab) keep(stderr io.Writer) {
	fmt.Fprintf(stderr, "feint run: kept, as --keep-on-failure asked\n")
	fmt.Fprintf(stderr, "  endpoint   http://%s\n", l.addr)
	fmt.Fprintf(stderr, "  bytes      %s\n", l.dir)
	fmt.Fprintf(stderr, "  inspect    curl -s http://%s/_feint/resources | jq .\n", l.addr)
	fmt.Fprintf(stderr, "  then       feint stop --addr %s && rm -rf %s\n", l.addr, l.dir)
}

// hazardVariables answers the environment variables the packs have MEASURED to
// reroute their own clients to the real cloud.
//
// Discovered rather than listed. The names already live in the packs, behind
// the lookup seam `EnvHazards` takes, and a copy of them in this file is a copy
// that the next pack's measurement would leave behind — silently, since a
// variable nobody strips simply keeps working. So this hands EnvHazards a
// lookup that records what it asks for and throws the warnings away.
//
// Called twice, with "absent" and with "present", because a pack may ask for
// one name only inside the branch another name opened; the union is what both
// walks together could reach. It is still a best effort and the comment says
// so rather than the code pretending otherwise.
//
// TestARunStripsEveryVariableAPackMeasuredAsAReroute fails without this.
func hazardVariables() []string {
	srv, _, err := newServer(nil)
	if err != nil {
		// Another caller owns reporting a server that cannot be built; here it
		// means no name is known, and stripping nothing is the honest answer.
		return nil
	}
	var asked []string
	record := func(answer bool) func(string) (string, bool) {
		return func(name string) (string, bool) {
			if !slices.Contains(asked, name) {
				asked = append(asked, name)
			}
			if answer {
				return "recorded by feint run", true
			}
			return "", false
		}
	}
	for _, pack := range srv.Packs() {
		hazards, ok := pack.(packEnvHazards)
		if !ok {
			continue
		}
		_ = hazards.EnvHazards(record(false))
		_ = hazards.EnvHazards(record(true))
	}
	slices.Sort(asked)
	return asked
}

// childEnvironment is what this command decides the child may see.
type childEnvironment struct {
	// strip are variables removed from the inherited environment.
	strip []string
	// set are KEY=VALUE pairs added after the strip, so an export this command
	// resolved always wins over one the operator's shell happened to carry.
	set []string
}

// apply removes then adds, in that order.
func (c childEnvironment) apply(environ []string) []string {
	kept := make([]string, 0, len(environ)+len(c.set))
	for _, pair := range environ {
		name, _, ok := strings.Cut(pair, "=")
		if ok && slices.Contains(c.strip, name) {
			continue
		}
		// A variable this command is about to set is dropped here rather than
		// shadowed: exec gives the last assignment, but a duplicated name in
		// the environment of a child that reads it with C getenv is a coin
		// toss, and nothing is gained by leaving the loser in.
		if ok && slices.ContainsFunc(c.set, func(s string) bool {
			key, _, found := strings.Cut(s, "=")
			return found && key == name
		}) {
			continue
		}
		kept = append(kept, pair)
	}
	return append(kept, c.set...)
}

// providerExports resolves the environment a child needs, from the same source
// `feint env` prints: each pack's own Env.
//
// A provider name selects one pack; empty takes them all, which is what a
// command driving two clouds in one run needs and what `feint env` cannot do
// because it writes shell syntax for one.
//
// TestARunExportsTheSameEnvironmentAsFeintEnv fails without this.
func providerExports(endpoint, provider string) ([]string, []string, error) {
	packs, err := packsFor(emulator.DefaultEnv())
	if err != nil {
		return nil, nil, err
	}
	var exports, served []string
	for _, pack := range packs {
		if provider != "" && pack.Name() != provider {
			continue
		}
		served = append(served, pack.Name())
		for name, value := range pack.Env(endpoint).Vars {
			exports = append(exports, name+"="+value)
		}
	}
	if provider != "" && len(served) == 0 {
		names := make([]string, 0, len(packs))
		for _, pack := range packs {
			names = append(names, pack.Name())
		}
		slices.Sort(names)
		return nil, nil, fmt.Errorf("no pack named %q; this build serves %s",
			provider, strings.Join(names, ", "))
	}
	slices.Sort(exports)
	slices.Sort(served)
	return exports, served, nil
}

// runReceipt is what a run reports, in prose and in JSON.
//
// The JSON is not a rendering of the prose: #848 asks for a machine-readable
// answer "without parsing prose", so both are written from the same struct and
// neither is derived from the other's text.
type runReceipt struct {
	SchemaVersion int    `json:"schema_version"`
	Lab           string `json:"lab"`
	// Session names this lab, so that two runs on one machine are told apart
	// in a log and a kept lab can be found again. Empty for an attached one:
	// its identity is not this command's to give.
	Session  string `json:"session"`
	Endpoint string `json:"endpoint"`
	// Runtime and Capabilities are read from the emulator's own /_feint/health
	// rather than from the flag this command passed, which is #848's fifth
	// behaviour and the repository's rule twice over: a capability is
	// DECLARED by the driver, never deduced from a mode's name, and what the
	// receipt reports must be what the process can prove rather than what the
	// caller asked for.
	Runtime      string   `json:"runtime"`
	Capabilities []string `json:"capabilities"`
	// RuntimeProof is what some pack actually hands to the runtime, which is
	// not the same list: a capability can be declared and wired by nobody.
	RuntimeProof []string `json:"runtime_proof"`
	Providers    []string `json:"providers"`
	Intercepted  []string `json:"intercepted"`
	Command      []string `json:"command"`
	Calls        int      `json:"calls"`
	// CallsTruncated says the emulator's ring held everything it was asked
	// for. Reported rather than hidden: a count silently capped at the ring
	// size is a number a script would assert on without knowing it was a
	// window. The emulator publishes the window for exactly this reason.
	CallsTruncated bool     `json:"calls_truncated"`
	Stripped       []string `json:"stripped"`
	// Exit is the child's code, and NEGATIVE means a signal killed it, which
	// is what Go answers and what the prose above says in words. A reader of
	// the JSON gets the number; a reader of the receipt gets the sentence.
	// Reset is empty when --reset-after was not asked for, and otherwise
	// says what happened: a run that reset and a run that did not are
	// different facts, and an empty Unreconciled must not be read as
	// "it converged".
	Reset string `json:"reset,omitempty"`
	// Unreconciled names what the reset could not bring back, which is
	// #853's own wording: a reset reports exactly what it could not
	// reconcile.
	Unreconciled []string `json:"unreconciled,omitempty"`
	Exit         int      `json:"exit"`
	Cleanup      string   `json:"cleanup"`
	// RealCloud is a field and not a sentence render() prints, because the
	// prose and the JSON must not be able to disagree about the one claim a
	// reader of this receipt cares about most.
	RealCloud string `json:"real_cloud"`
	// The campaign half of #851, and three fields rather than one because a
	// review reproduced what happens when they are merged.
	//
	// FaultsArmed is what the LAB had armed during this run, read from it,
	// whoever armed it. FaultsCampaign is whether THIS run armed one with
	// --fault. The first version published only the second under the first's
	// name, so a second run against a shared lab met the first run's injected
	// 503 and published `faults_armed: false`.
	//
	// FaultsRead says the state came back at all, because an unread state and
	// an empty one are different facts and only one of them means "no fault
	// was armed".
	FaultsArmed    bool         `json:"faults_armed"`
	FaultsCampaign bool         `json:"faults_campaign"`
	FaultsRead     bool         `json:"faults_read"`
	Faults         []firedFault `json:"faults,omitempty"`
	FaultsDropped  int          `json:"faults_dropped,omitempty"`
}

// runReceiptSchemaVersion is versioned from the first release, because a suite
// reading this in CI is a consumer on day one.
//
// It is NOT frozen, and this note used to say it was. There is no fixture for
// the run receipt in testdata/frozen/, so TestASurfaceChangeDemandsItsVersionBump
// never judges it and the only tests that touch this constant compare it with
// itself. The bump below is therefore voluntary, and the mechanism that makes
// the other schema versions unable to lie does not cover this one. #918 carries
// the fixture that would.
// 2 adds the fault campaign (#851): faults_armed, faults_read, faults and
// faults_dropped. A version 1 consumer reads a run that injected failures as
// one that simply failed.
const runReceiptSchemaVersion = 2

// callsSoFar reads the emulator's own trace counter.
//
// Best effort on purpose: a receipt is not worth failing a run that otherwise
// passed, so an unreachable endpoint answers zero and says nothing. What it
// must never do is report a confident number it did not read, which is why the
// truncation flag travels with the count.
func callsSoFar(endpoint string) (int, bool) {
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Get(strings.TrimSuffix(endpoint, "/") + "/_feint/trace")
	if err != nil {
		return 0, false
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return 0, false
	}
	var body struct {
		Count int `json:"count"`
		Kept  int `json:"kept"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil {
		return 0, false
	}
	return body.Count, body.Kept > 0 && body.Count >= body.Kept
}

// render writes the compact receipt #848 describes.
func (r runReceipt) render(w io.Writer) {
	verdict := "passed"
	switch {
	case r.Exit < 0:
		// Go answers -1 for a process a signal killed, and -1 is a number
		// nobody can act on. The word is what a reader needs; the field keeps
		// the number for a machine, and the JSON documents it.
		verdict = "failed (the command was killed by a signal)"
	case r.Exit != 0:
		verdict = fmt.Sprintf("failed (%d)", r.Exit)
	}
	fmt.Fprintf(w, "feint run: %s\n", verdict)
	row := func(key, value string) { fmt.Fprintf(w, "  %-14s %s\n", key, value) }
	row("lab", r.Lab+" "+r.Endpoint)
	row("providers", strings.Join(r.Providers, ", "))
	row("command", strings.Join(r.Command, " "))
	calls := fmt.Sprintf("%d", r.Calls)
	if r.CallsTruncated {
		calls += " (the emulator's trace ring is full; this is a window, not a total)"
	}
	row("calls", calls)
	row("intercepted", strings.Join(r.Intercepted, ", "))
	if len(r.Stripped) != 0 {
		row("stripped", strings.Join(r.Stripped, ", ")+" (measured to reroute clients)")
	}
	if r.Runtime != "" {
		runtime := r.Runtime
		if len(r.Capabilities) != 0 {
			runtime += " (" + strings.Join(r.Capabilities, ", ") + ")"
		}
		row("runtime", runtime)
	}
	if len(r.RuntimeProof) != 0 {
		row("runtime proof", strings.Join(r.RuntimeProof, ", "))
	}
	if r.Reset != "" {
		row("reset", r.Reset)
		for _, difference := range r.Unreconciled {
			fmt.Fprintf(w, "    %s\n", difference)
		}
	}
	renderFaultReceipt(w, r)
	row("cleanup", r.Cleanup)
	// Deliberately narrow wording, and a field rather than a literal here:
	// "no known escape observed" is what #848's own example prints and what
	// this can honestly say — the doors are intercepted, the measured reroute
	// variables are gone from the child, and nothing claims that an unmeasured
	// path does not exist.
	row("real cloud", r.RealCloud)
}

// renderJSON writes the same receipt for a machine.
func (r runReceipt) renderJSON(w io.Writer) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(r)
}

// stackEscapes answers the measured escapes in the Terraform configuration
// around this run that this run would NOT catch.
//
// #848's safety boundary asks for the hazard analysis `feint doctor` runs, and
// a refusal before the child starts. What it cannot ask for is a refusal on
// every hazard, because the hazards the packs report are precisely the ones
// this command exists to neutralise: scaleway_object_* hardcodes
// s3.<region>.scw.cloud, an aws provider points at sos-<zone>.exo.io, and
// intercepting exactly those is what #849 shipped.
//
// So the question is not "is there a hazard" but "is this one covered", and it
// is answered structurally rather than by reading the warning's prose: a
// hazard reported by pack P is covered when this run mounted a door for P, and
// is an escape when it did not. The doors are the interception; a provider
// with none has nothing catching its traffic.
//
// Silence off a stack, like doctor's: no Terraform file here means no analysis
// and no rows, so `feint run -- go test` in a Go repository is not interrogated
// about a stack that does not exist.
//
// TestARunRefusesAnEscapeNoDoorOfItsOwnWouldCatch fails without this.
func stackEscapes(profiles []s3.Profile) []string {
	dir, err := os.Getwd()
	if err != nil {
		return nil
	}
	config, files := stackConfig(dir)
	if files == 0 {
		return nil
	}
	srv, _, err := newServer(nil)
	if err != nil {
		return nil
	}
	covered := make(map[string]bool, len(profiles))
	for _, profile := range profiles {
		covered[profile.Provider] = true
	}
	var escapes []string
	for _, pack := range srv.Packs() {
		hazards, ok := pack.(packStackHazards)
		if !ok || covered[pack.Name()] {
			continue
		}
		for _, warning := range hazards.StackHazards(config) {
			escapes = append(escapes, pack.Name()+": "+warning)
		}
	}
	return escapes
}

// writeReceipt puts the JSON receipt where the caller asked for it.
//
// A path rather than a stream, because stdout is the child's: a run whose
// command prints anything at all would otherwise hand a reader a receipt with
// a Terraform plan wrapped around it. `-` is still offered for the case where
// the command is known to be quiet, and it is the caller saying so rather
// than this command assuming it.
//
// TestTheJSONReceiptGoesWhereItWasAskedFor fails without this.
func writeReceipt(receipt runReceipt, path string, stdout io.Writer) error {
	if path == "-" {
		return receipt.renderJSON(stdout)
	}
	file, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open the receipt: %w", err)
	}
	if err := receipt.renderJSON(file); err != nil {
		_ = file.Close()
		return fmt.Errorf("write the receipt: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close the receipt: %w", err)
	}
	return nil
}

// labHealth reads what the emulator says about its own runtime.
//
// From /_feint/health and not from the flag this command passed, because a
// capability is DECLARED by the driver and an undeclared one counts as absent
// (machine.CapabilitiesOf). Reporting the mode name would tell a reader what
// was asked for; this tells them what answered.
//
// Best effort, like the call count: a receipt is not worth failing a run that
// otherwise passed, and an empty runtime row is visibly empty.
//
// TestTheReceiptReportsTheRuntimeTheEmulatorDeclares fails without this.
func labHealth(endpoint string) (string, []string, []string) {
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Get(strings.TrimSuffix(endpoint, "/") + "/_feint/health")
	if err != nil {
		return "", nil, nil
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return "", nil, nil
	}
	var body struct {
		Machines     string              `json:"machines"`
		Capabilities map[string]bool     `json:"capabilities"`
		Enforced     map[string][]string `json:"enforced"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil {
		return "", nil, nil
	}
	held := make([]string, 0, len(body.Capabilities))
	for name, declared := range body.Capabilities {
		if declared {
			held = append(held, name)
		}
	}
	slices.Sort(held)
	// The proof level #848's receipt example calls "runtime proof", and it is
	// the INTERSECTION of the two halves the emulator publishes separately:
	// `capabilities.<axis>` says the runtime can deliver it, `enforced.<axis>`
	// says some pack hands it over. Either alone is a lie, and the first
	// version of this function shipped one: under `--vm off` it reported
	// "runtime proof: balancing, firewall" beside "runtime: none", because
	// the packs still ASK when nothing answers. That is the same defect #180
	// and #481 recorded on the health payload itself, reproduced here within
	// an hour of reading their comment.
	proof := make([]string, 0, len(body.Enforced))
	for axis, packs := range body.Enforced {
		if len(packs) != 0 && body.Capabilities[axis] {
			proof = append(proof, axis)
		}
	}
	slices.Sort(proof)
	return body.Machines, held, proof
}

// labArgs is the command line the owned lab starts with.
//
// A function so it can be read without spawning anything: startOwnedLab goes
// through `start`, which re-execs os.Executable(), so the only way to assert
// what the lab is given is to assert it here.
//
// --cleanup whenever a runtime is in play, because #848's sixth behaviour is
// "cleans up Feint-owned runtime objects on normal exit" and stopping the
// emulator does not do that: the machines and networks it created outlive the
// process. `serve --cleanup` sweeps exactly what the emulator made, by the
// labels it wrote. Not under `off`, where there is nothing to sweep and the
// flag would only be noise in a recorded command line.
//
// Contracts when they are there and skipped when they are not: a released
// binary run from a stranger's stack directory has none beside it, and
// refusing would make the command unusable exactly where #848 says it must
// work.
//
// TestAnOwnedLabSweepsItsRuntimeObjects fails without this.
func labArgs(addr, dir, mode string) []string {
	args := []string{
		"--addr", addr,
		"--vm", mode,
		"--timeout", labReadyTimeout.String(),
		"--objectstore", filepath.Join(dir, "objects"),
	}
	if mode != "off" {
		args = append(args, "--cleanup")
	}
	if stat, err := os.Stat("contracts"); err == nil && stat.IsDir() {
		args = append(args, "--contracts", "contracts")
	}
	return args
}
