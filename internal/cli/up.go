package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/environment"
)

// `feint up` and `feint down`: the verbs that read feint.yaml (#190).
//
// A declaration nothing reads is a comment, so this is the reading. What it is
// not is a second lifecycle: `start`, `stop`, `status` and `wait` exist and are
// a frozen surface, and this composes them rather than growing its own flags and
// its own bugs. Every emulator these verbs bring up is one `feint stop`,
// `feint logs` and `feint status` know about, because it *is* one of theirs.
//
// The order is #190's, and the first step is the one that matters most:
//
//  1. check what the host can deliver, and refuse before anything is started;
//  2. start the control plane with the contracts and the state the file names;
//  3. configure the client from the pack's own Env — `feint env` already
//     answers it, so nothing new is invented here;
//  4. run the IaC engine the file names, in the directory it names;
//  5. wait for the ready conditions, each with a deadline and each said out
//     loud while it waits;
//  6. print the endpoints, and what proved them.
//
// Four traps this replaces, each measured by hand on 2026-08-24 against the
// example stacks, each of them a parameter somebody had to reconstitute by
// reading a script:
//
//   - `examples/stacks/outscale` carries a local module, so copying `*.tf`
//     alone does not run. The engine runs in the declared directory, in place.
//   - Scaleway and Outscale declare an `endpoint` variable whose default is
//     127.0.0.1:4599. Pointed at a port nothing listens on, Terraform blocks to
//     its own ceiling and blames the provider. `iac.vars` carries it, with the
//     one substitution this file has.
//   - Exoscale declares no such variable and takes its endpoint from
//     EXOSCALE_API_ENDPOINT, path included. That value comes from the pack's own
//     Env, never from a field, so no reader has to learn which provider wants
//     its path inside the value.
//   - `emulator.env` is set before the spawn, because FEINT_* knobs are read
//     inside the emulator's process and exporting one afterwards does nothing.
//     One name is refused there outright since #525 — see checkEnvName in
//     internal/environment — and the engine a pack vetoes is refused at the
//     doorstep here, before any process starts.

// upTimeout bounds the whole of step 5. Every condition is also announced
// before it is waited on, because a wait with no output is indistinguishable
// from a broken emulator.
const defaultReadyTimeout = 2 * time.Minute

func up(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("up")
	file := fs.String("file", environment.DefaultFile, "the environment declaration to read")
	runtimeMode := fs.String("runtime", "", "override the declared runtime mode, deliberately and out loud")
	timeout := fs.Duration("timeout", defaultReadyTimeout, "how long the ready conditions have, together")
	skipIaC := fs.Bool("no-iac", false, "bring the control plane up and do not run the engine")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "feint: unexpected argument %q; up takes flags only\n", fs.Arg(0))
		return exitError
	}

	decl, code := readDeclaration(*file, *runtimeMode, stdout, stderr)
	if decl == nil {
		return code
	}

	// Step 1. Everything that can be refused is refused here, before a process
	// is spawned or a container is started. A run that dies four minutes in on
	// a missing binary has spent those minutes teaching nothing.
	if err := preflight(decl, *skipIaC, stdout); err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	// The emulator's own knobs, set before the spawn because the child inherits
	// this process's environment and some of them are read server-side.
	for name, value := range decl.Emulator.Env {
		if err := os.Setenv(name, value); err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
		fmt.Fprintf(stdout, "  %s=%s (the emulator's own environment)\n", name, value)
	}

	// Step 2: the existing verb, with the flags the file names. Not a second
	// way to run an emulator — the same one, given a written-down command line.
	if code := start(startArgs(decl), stdout, stderr); code != exitOK {
		return code
	}

	if decl.Snapshot.Load != "" {
		fmt.Fprintf(stdout, "- loading the snapshot %q\n", decl.Snapshot.Load)
		if code := snapshotLoad([]string{decl.Snapshot.Load, "--addr", decl.Emulator.Addr}, stdout, stderr); code != exitOK {
			return code
		}
	}

	// Steps 3 and 4.
	if decl.IaC.Engine != "" && !*skipIaC {
		if err := runEngine(decl, "apply", stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
	}

	// Step 5. Skipped, out loud, when the engine that builds what the conditions
	// describe was skipped: waiting for resources nobody asked to create would
	// fail every time, and a flag whose only outcome is a failure is a flag
	// nobody uses. Said rather than silent — that is the whole difference
	// between a skip and a lie.
	proved := true
	switch {
	case *skipIaC && decl.IaC.Engine != "" && len(decl.Ready) > 0:
		proved = false
		fmt.Fprintf(stdout, "- not waiting: --no-iac skipped %s, and the ready conditions describe what it builds (%s)\n",
			decl.IaC.Engine, strings.Join(decl.Ready, ", "))
	default:
		if err := waitReady(decl, *timeout, stdout); err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
	}

	// Step 6.
	printEndpoints(decl, proved, stdout)
	return exitOK
}

func down(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("down")
	file := fs.String("file", environment.DefaultFile, "the environment declaration to read")
	keep := fs.Bool("keep-emulator", false, "destroy the infrastructure and leave the emulator running")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "feint: unexpected argument %q; down takes flags only\n", fs.Arg(0))
		return exitError
	}

	decl, code := readDeclaration(*file, "", stdout, stderr)
	if decl == nil {
		return code
	}

	// The engine first: destroying the infrastructure needs the API that is
	// about to be stopped. A `down` that stopped the emulator first would leave
	// a state file describing resources nothing can reach.
	failed := exitOK
	if decl.IaC.Engine != "" {
		// The same doorstep `up` has, because #525 measured this exact verb:
		// a `feint down` on the Exoscale stack resolved the published provider
		// and its refresh sent five signed requests to api-ch-*.exoscale.com
		// before failing on the fake signature. A vetoed engine never becomes
		// a process here either. The skip is said out loud rather than
		// reported as a failure: the teardown this verb owes — the emulator
		// and its state — still happens below, and it is the whole of what
		// exists when no engine was ever allowed to build anything.
		//
		// TestDownSkipsAVetoedEngineOutLoudAndNeverRunsIt fails when this
		// check is removed.
		reason, err := engineVeto(decl)
		switch {
		case err != nil:
			fmt.Fprintf(stderr, "feint: %v\n", err)
			failed = exitError
		case reason != "":
			fmt.Fprintf(stdout, "- %s destroy skipped: %s\n", decl.IaC.Engine, reason)
			fmt.Fprintf(stdout, "  the emulator held whatever existed, and the stop below discards it; "+
				"a terraform.tfstate left in %s describes nothing\n", decl.IaC.Directory)
		default:
			if err := runEngine(decl, "destroy", stdout, stderr); err != nil {
				// Reported and carried on: leaving the emulator running because the
				// engine failed would leave the operator with both problems.
				fmt.Fprintf(stderr, "feint: %v\n", err)
				failed = exitError
			}
		}
	}
	if *keep {
		return failed
	}
	// `stop` says what it discards when no state file was recorded (#182), and
	// this inherits that rather than reimplementing it.
	if code := stop([]string{"--addr", decl.Emulator.Addr}, stdout, stderr); code != exitOK {
		return code
	}
	return failed
}

// readDeclaration loads the file and applies the one deliberate override.
//
// A nil file means the caller returns the code beside it: the two-value return
// keeps the refusal and its exit code in one place, where a bool would have
// each caller invent one.
func readDeclaration(path, override string, stdout, stderr io.Writer) (*environment.File, int) {
	decl, err := environment.Load(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(stderr, "feint: no %s here. It is the declaration of one environment — "+
				"which provider, which runtime, which engine — and `docs/environment.md` "+
				"has the shortest one that works.\n", path)
			return nil, exitError
		}
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return nil, exitError
	}
	fmt.Fprintf(stdout, "%s: %s, runtime %s", path, describeCloud(decl), decl.Runtime.Mode)
	if decl.IaC.Engine != "" {
		fmt.Fprintf(stdout, ", %s in %s", decl.IaC.Engine, decl.IaC.Directory)
	}
	fmt.Fprintln(stdout)

	// A field the schema knows and no verb reads yet is said out loud. Accepting
	// it in silence and applying half the file is the exact lie this project
	// exists to avoid, and a warning is the honest form of "declared, not read".
	for _, path := range decl.Unread() {
		fmt.Fprintf(stderr, "warning: `%s` is declared and no verb reads it yet\n", path)
	}

	if override != "" {
		if override != decl.Runtime.Mode {
			// Out loud, and only ever downward from what the file asked: this is
			// the deliberate difference #192 asks to be declared rather than
			// discovered. A silent downgrade is what `up` refuses.
			fmt.Fprintf(stdout, "  runtime %s, overridden to %s by --runtime\n", decl.Runtime.Mode, override)
		}
		decl.Runtime.Mode = override
	}
	return decl, exitOK
}

func describeCloud(decl *environment.File) string {
	if decl.Cloud.Provider == "" {
		return "no provider named"
	}
	return decl.Cloud.Provider
}

// preflight is step 1, and the only step allowed to be slow is the one that
// asks the host about its runtime.
//
// Everything here answers the same question — can this host deliver what the
// file declares — and every answer names both what is missing and how to get
// it. A guard with no way past it gets worked around by copying the emulator,
// which teaches nobody anything; that is why FEINT_BOOT_IMAGES exists and it is
// why each refusal below carries its command.
func preflight(decl *environment.File, skipIaC bool, stdout io.Writer) error {
	if decl.Cloud.Provider != "" {
		srv, _, err := newServer(nil)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(srv.Packs()))
		found := false
		for _, p := range srv.Packs() {
			names = append(names, p.Name())
			if p.Name() == decl.Cloud.Provider {
				found = true
			}
		}
		sort.Strings(names)
		if !found {
			return fmt.Errorf("`cloud.provider: %s`, and this binary serves %s",
				decl.Cloud.Provider, strings.Join(names, ", "))
		}
	}

	if decl.IaC.Engine != "" && !skipIaC {
		// The pack's veto comes before the host questions, because it is not a
		// host question: no install and no directory makes it right to run an
		// engine whose resolved provider splits between this emulator and the
		// real cloud. #525 measured that split reaching api-ch-*.exoscale.com
		// from this very verb's sibling, so the refusal falls here, before any
		// process starts — the emulator-side guard never sees those requests.
		//
		// TestUpRefusesAVetoedEngineBeforeStartingAnything fails when this
		// check is removed.
		reason, err := engineVeto(decl)
		if err != nil {
			return err
		}
		if reason != "" {
			return fmt.Errorf("`iac.engine: %s` is refused for `cloud.provider: %s`: %s\n\n%s",
				decl.IaC.Engine, decl.Cloud.Provider, reason, waysPastTheEngineVeto(decl))
		}
		dir := decl.Resolve(decl.IaC.Directory)
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("`iac.directory: %s` resolves to %s, which is not a directory",
				decl.IaC.Directory, dir)
		}
		if _, err := exec.LookPath(decl.IaC.Engine); err != nil {
			return fmt.Errorf("`iac.engine: %s` and no %s on PATH; install it, or name the other one "+
				"(%s)", decl.IaC.Engine, decl.IaC.Engine, strings.Join(environment.Engines, ", "))
		}
	}

	// The runtime, asked of the host rather than deduced from the mode's name.
	// This is the same check `serve` makes at startup (#181) — a mode the host
	// cannot deliver is refused naming the missing half — moved ahead of
	// everything so that nothing has started when it fires.
	rt, err := resolveRuntime(decl.Runtime.Mode, stdout)
	if err != nil {
		// A guard with no way past it gets worked around by copying the
		// emulator, which teaches nobody anything — the reasoning that named
		// FEINT_BOOT_IMAGES rather than hiding it. So the refusal carries the
		// three doors: read the whole diagnosis, run this environment with less
		// on purpose, or change what it asks for.
		return fmt.Errorf("%w\n\n%s", err, waysPastTheRuntimeRefusal(decl))
	}
	if len(decl.Runtime.Images) == 0 {
		return nil
	}
	if !rt.Runs() {
		fmt.Fprintf(stdout, "  runtime.images: not checked, nothing boots under `%s`\n", decl.Runtime.Mode)
		return nil
	}
	return checkDeclaredImages(decl, rt, stdout)
}

// resolveRuntime is machineDriver, behind a name a test can replace.
//
// The seam exists because the refusal it guards is host-dependent in the one
// direction that matters: on a station with OVN wired, no mode is refused, and
// a test asserting the refusal there would measure the station rather than the
// verb. What the refusal *says* is machineDriver's and is proved next door
// (machine.TestVerifyNarrowsWhatTheHostCannotDeliver); what this seam lets a
// test assert is that `up` asks before it starts anything, and that the answer
// carries a way through.
var resolveRuntime = machineDriver

// packEngineVeto is the optional half of a pack whose published IaC providers
// cannot drive this emulator at all, so that pointing an engine at it must be
// refused rather than half served.
//
// Optional and declared here rather than on emulator.Pack, for the reason
// packEnvHazards is: which client splits is provider knowledge and lives in
// the pack; this verb only carries the refusal to the doorstep, before any
// process starts. A pack whose engines work (Scaleway, Outscale) does not
// implement a method to say so — TestOnlyTheExoscalePackVetoesAnEngine holds
// that boundary.
type packEngineVeto interface {
	// VetoEngine answers why the named engine must not run against this pack,
	// or "" when it may. The reason has to name what remains possible: a wall
	// with no door beside it gets worked around by copying the emulator.
	VetoEngine(engine string) string
}

// engineVeto asks the declared provider's pack whether the declared engine may
// run at all. Both callers — `up` before anything starts, `down` before the
// destroy — ask the same question, because #525 measured the escape on `down`:
// the doorstep that guards only the apply leaves the destroy to send the same
// requests to the same real cloud.
func engineVeto(decl *environment.File) (string, error) {
	if decl.Cloud.Provider == "" || decl.IaC.Engine == "" {
		return "", nil
	}
	srv, _, err := newServer(nil)
	if err != nil {
		return "", err
	}
	for _, p := range srv.Packs() {
		if p.Name() != decl.Cloud.Provider {
			continue
		}
		if veto, ok := p.(packEngineVeto); ok {
			return veto.VetoEngine(decl.IaC.Engine), nil
		}
	}
	return "", nil
}

// waysPastTheEngineVeto is the door beside that wall, on the model of
// waysPastTheRuntimeRefusal below: every line is a command or an edit the
// reader can make now. What replaces the engine — the pack's own CLI — is
// named by the veto reason itself, because which client that is belongs to the
// pack.
func waysPastTheEngineVeto(decl *environment.File) string {
	where := decl.Path
	if where == "" {
		where = environment.DefaultFile
	}
	return fmt.Sprintf(`Nothing was started. Two ways on:
  feint up --no-iac                   the emulator alone, and the client the refusal names beside it
  %s                          drop iac.engine, and the declaration stops asking for it`, where)
}

// waysPastTheRuntimeRefusal is the door beside the wall.
//
// Every line of it is a command or an edit the reader can make now. A refusal
// that only states the problem is one people route around by copying the
// emulator and deleting the check, and this repository has written that
// reasoning down once already.
func waysPastTheRuntimeRefusal(decl *environment.File) string {
	where := decl.Path
	if where == "" {
		where = environment.DefaultFile
	}
	return fmt.Sprintf(`Nothing was started. Three ways on, in the order they cost:
  feint doctor --vm %s        the whole diagnosis, including what to install
  feint up --runtime off              run this environment without machines, and say so
  %s                          change runtime.mode to what this host delivers (%s)`,
		decl.Runtime.Mode, where, strings.Join(environment.RuntimeModes, ", "))
}

// checkDeclaredImages holds the declaration to what the station actually has.
//
// It never builds: a build launches a container and takes minutes, which is a
// side effect this project asks for rather than performs on the way past. So it
// names what is missing and the command that makes it, which is the difference
// between a refusal and a dead end.
//
// Three outcomes, never two, and the third is the one a two-way reader would get
// wrong. The warm-up set (`feint images`) is enumerated; the boot path also
// *derives* an image on demand for a family and version outside it (#465). So a
// name the warm-up set does not carry is not an error — it is a first boot that
// will cost minutes — and saying that is worth more than refusing something the
// runtime can do.
//
// TestADeclaredImageTheStationLacksIsRefusedWithTheCommandThatBuildsIt fails
// without the refusal, and its sibling holds the accepting half.
func checkDeclaredImages(decl *environment.File, rt machine.Runtime, stdout io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	inventory, err := rt.Inventory(ctx)
	if err != nil {
		return fmt.Errorf("reading the machine images: %w", err)
	}
	derived, err := rt.DerivedInventory(ctx)
	if err != nil {
		return fmt.Errorf("reading the images this station derived: %w", err)
	}

	held := map[string]bool{}
	buildable := make([]string, 0, len(inventory))
	for _, status := range inventory {
		buildable = append(buildable, status.Spec.Name)
		if status.Present() {
			held[status.Spec.Name] = true
		}
	}
	for _, status := range derived {
		held[status.Spec.Name] = true
	}

	var missing, onDemand []string
	for _, name := range decl.Runtime.Images {
		switch {
		case held[name]:
		case contains(buildable, name):
			missing = append(missing, name)
		default:
			onDemand = append(onDemand, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("`runtime.images` needs %s, which this station does not hold.\n"+
			"  Build it:  feint images --only %s\n"+
			"  Or drop the line, and the first boot that needs it derives one, which costs minutes",
			strings.Join(missing, ", "), missing[0])
	}
	for _, name := range onDemand {
		fmt.Fprintf(stdout, "  runtime.images: %s is outside the warm-up set; "+
			"the first boot that needs it derives one, which costs minutes\n", name)
	}
	if held := len(decl.Runtime.Images) - len(onDemand); held > 0 {
		fmt.Fprintf(stdout, "  runtime.images: %d of %d present on this station\n",
			held, len(decl.Runtime.Images))
	}
	return nil
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// startArgs renders the declaration into the command line `feint start` takes.
//
// One direction only, and that is the point: the flags stay the CLI's, the file
// only says which values they carry. A field here that named no flag would be a
// second source for something the binary already decides.
func startArgs(decl *environment.File) []string {
	args := []string{
		"--addr", decl.Emulator.Addr,
		"--vm", decl.Runtime.Mode,
		"--log-level", decl.Emulator.LogLevel,
	}
	if decl.Emulator.State != "" {
		args = append(args, "--state", decl.Resolve(decl.Emulator.State))
	}
	if decl.Emulator.Contracts != "" {
		args = append(args, "--contracts", decl.Resolve(decl.Emulator.Contracts))
	}
	if decl.Emulator.Cleanup {
		args = append(args, "--cleanup")
	}
	return args
}

// runEngine is steps 3 and 4: the client's environment, then the engine.
//
// The engine's own output goes straight through, never a summary of it. When
// `terraform apply` fails, its error is what the developer needs, and a wrapper
// that reformats it has thrown away the only useful thing in the run.
func runEngine(decl *environment.File, action string, stdout, stderr io.Writer) error {
	dir := decl.Resolve(decl.IaC.Directory)
	env, err := engineEnvironment(decl)
	if err != nil {
		return err
	}

	steps := [][]string{{"init", "-input=false"}}
	switch action {
	case "apply":
		steps = append(steps, []string{"apply", "-input=false", "-auto-approve"})
	case "destroy":
		steps = append(steps, []string{"destroy", "-input=false", "-auto-approve"})
	}
	for _, step := range steps {
		fmt.Fprintf(stdout, "- %s %s in %s\n", decl.IaC.Engine, step[0], dir)
		cmd := exec.Command(decl.IaC.Engine, step...) //nolint:gosec // the engine is one of two names the schema accepts
		cmd.Dir = dir
		cmd.Env = env
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s %s: %w", decl.IaC.Engine, step[0], err)
		}
	}
	return nil
}

// engineEnvironment is step 3: what a real client of this provider needs, taken
// from the pack itself.
//
// Nothing here names a provider. `feint env` prints these same variables from
// Pack.Env, and this reads the same method rather than a copy of its output —
// which is what keeps the endpoint form (Exoscale carries its /v2 path inside
// the value; Scaleway does not) a fact with one owner.
//
// The order below is load-bearing, not stylistic: the caller's environment
// first, the pack's variables appended after, because exec.Cmd lets the last
// duplicate win. That is what makes real credentials exported in the shell
// lose to the pack's deliberately public pair — the one property that stood
// between #525's five escaped requests and a real account, and it stood
// unasserted until that incident. TestThePacksOwnCredentialsOutrankTheCallersShell
// fails when the two halves are appended the other way around.
func engineEnvironment(decl *environment.File) ([]string, error) {
	out := os.Environ()
	// TF_IN_AUTOMATION and TF_INPUT are what tell the engine no human is
	// watching. Without them a missing value opens a prompt on a stdin that is
	// not a terminal, and the run hangs rather than failing.
	out = append(out, "TF_IN_AUTOMATION=1", "TF_INPUT=0")

	if decl.Cloud.Provider != "" {
		srv, _, err := newServer(nil)
		if err != nil {
			return nil, err
		}
		for _, p := range srv.Packs() {
			if p.Name() != decl.Cloud.Provider {
				continue
			}
			env := p.Env(decl.Endpoint())
			names := make([]string, 0, len(env.Vars))
			for name := range env.Vars {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				out = append(out, name+"="+env.Vars[name])
			}
		}
	}
	// TF_VAR_ rather than -var, and the difference is measured: an undeclared
	// TF_VAR_ is ignored, where `-var endpoint=…` fails outright on a stack that
	// declares no such variable. Exoscale's is exactly that stack, and one
	// mechanism has to serve all three.
	for name, value := range decl.EngineVars() {
		out = append(out, "TF_VAR_"+name+"="+value)
	}
	return out, nil
}

// waitReady is step 5. Every condition is announced before it is waited on and
// named when its deadline passes: a hang with no output is indistinguishable
// from a broken emulator, and this repository has already paid for that.
func waitReady(decl *environment.File, timeout time.Duration, stdout io.Writer) error {
	conditions := decl.Conditions()
	if len(conditions) == 0 {
		return nil
	}
	deadline := time.Now().Add(timeout)
	for _, condition := range conditions {
		fmt.Fprintf(stdout, "- waiting: %s\n", condition.Describe())
		if err := awaitCondition(decl, condition, deadline); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "  ok: %s\n", condition.Raw)
	}
	return nil
}

func awaitCondition(decl *environment.File, condition environment.Condition, deadline time.Time) error {
	var last string
	for {
		met, why := conditionMet(decl, condition)
		if met {
			return nil
		}
		last = why
		if time.Now().After(deadline) {
			return fmt.Errorf("the ready condition %q was never met: %s (waiting for %s)",
				condition.Raw, last, condition.Describe())
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// conditionMet answers one condition, and answers *why not* when it is not met.
//
// Three outcomes, never two: met, not met with a reason, and unreadable with a
// reason. A reader that mapped a failed call to "not met" would report a broken
// emulator as a resource that has not appeared yet, which is the family of
// defect measurement-integrity names first.
func conditionMet(decl *environment.File, condition environment.Condition) (bool, string) {
	switch condition.Kind {
	case environment.HTTPCondition:
		client := &http.Client{Timeout: 3 * time.Second}
		res, err := client.Get(decl.Endpoint() + condition.Path)
		if err != nil {
			return false, err.Error()
		}
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode >= 400 {
			return false, fmt.Sprintf("the emulator answered %d", res.StatusCode)
		}
		return true, ""
	case environment.TCPCondition:
		conn, err := net.DialTimeout("tcp", condition.Address, 3*time.Second)
		if err != nil {
			return false, err.Error()
		}
		_ = conn.Close()
		return true, ""
	case environment.ResourceCondition:
		held, err := countKindInInventory(decl.Endpoint(), condition.Resource)
		if err != nil {
			return false, err.Error()
		}
		if held < condition.Count {
			return false, fmt.Sprintf("the emulator holds %d", held)
		}
		return true, ""
	default:
		return false, "no reader for this condition"
	}
}

// countKindInInventory reads the emulator's own inventory, which is
// provider-neutral by construction: the store holds resources whose kind is a
// value, so this counts a pack nobody has written yet.
func countKindInInventory(endpoint, kind string) (int, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Get(endpoint + "/_feint/resources")
	if err != nil {
		return 0, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("the inventory answered %d", res.StatusCode)
	}
	// The shape is the emulator's own, and it is decoded strictly: a body this
	// does not understand is an error rather than a zero, because a silent zero
	// reads exactly like an emulator nobody has used yet.
	var body struct {
		Resources []struct {
			Kind string `json:"kind"`
		} `json:"resources"`
	}
	decoder := json.NewDecoder(io.LimitReader(res.Body, maxStateBody))
	if err := decoder.Decode(&body); err != nil {
		return 0, fmt.Errorf("reading the inventory: %w", err)
	}
	count := 0
	for _, r := range body.Resources {
		if r.Kind == kind {
			count++
		}
	}
	return count, nil
}

// printEndpoints is step 6: where the environment is, and what proved it.
func printEndpoints(decl *environment.File, proved bool, stdout io.Writer) {
	fmt.Fprintf(stdout, "\nup: %s\n", decl.Endpoint())
	if decl.Cloud.Provider != "" {
		fmt.Fprintf(stdout, "  clients:  eval \"$(feint env %s)\"\n", decl.Cloud.Provider)
	}
	fmt.Fprintf(stdout, "  page:     %s/_feint/ui\n", decl.Endpoint())
	fmt.Fprintf(stdout, "  logs:     feint logs --addr %s\n", decl.Emulator.Addr)
	fmt.Fprintf(stdout, "  state:    feint status --addr %s\n", decl.Emulator.Addr)
	// What proved it, and never what would have. A summary that lists a
	// condition nothing evaluated is the shape of every green verdict this
	// project exists to distrust.
	switch {
	case len(decl.Ready) == 0:
	case proved:
		fmt.Fprintf(stdout, "  proved:   %s\n", strings.Join(decl.Ready, ", "))
	default:
		fmt.Fprintf(stdout, "  proved:   nothing — the ready conditions were skipped with the engine\n")
	}
	fmt.Fprintf(stdout, "  down:     feint down\n")
}
