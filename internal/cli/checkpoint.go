package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/machine"
)

// `feint checkpoint` and `feint reset`: transaction-like test isolation (#853).
//
//	feint checkpoint baseline
//	# run tests, create resources, fail halfway
//	feint reset baseline
//
// The emulator owns the state transitions, and these two own the VERDICT.
// That split is the whole design, and #853 states why in one line: "reset is
// not successful because JSON was restored". So a reset here is judged by
// comparing what a client can observe before and after, never by the status
// code of the call that performed it.
//
// ## What a checkpoint records locally, and why
//
// The emulator holds the state. These commands additionally record the
// OBSERVED state beside the instance, because that is what a later reset is
// measured against. Deriving it from the restored store instead would compare
// a restore to itself: the containers and the bytes would be out of the
// picture, which is exactly the half that can diverge.
//
// ## The exit code
//
// 0 when the observable state converged, 1 when it did not, with every
// difference named. Not 2: this repository reserves that for upstream drift,
// and a reset that failed to deliver its guarantee is an error in the ordinary
// sense. A reset that printed differences and exited 0 would be a gate nobody
// can build on.

// checkpointStateVersion is the shape of the file a checkpoint records beside
// its instance. Frozen from the start, like the emulator's own schemas: a
// suite that reads it is a consumer on day one.
const checkpointStateVersion = 1

// recordedCheckpoint is what `feint checkpoint` writes and `feint reset` reads.
type recordedCheckpoint struct {
	SchemaVersion int      `json:"schema_version"`
	Name          string   `json:"name"`
	Taken         string   `json:"taken"`
	Runtime       string   `json:"runtime"`
	State         observed `json:"state"`
}

// checkpointArgName takes the leading name off the arguments, so that the
// flags behind it are still flags.
//
// Shared with the refusals a name must survive: the byte store writes a
// directory per checkpoint, so a name is a path component before it is a
// name, and the emulator refuses the same shapes at its own door. Refusing
// here too means the operator hears it before a request is sent.
//
// TestACheckpointNameIsTakenBeforeTheFlags fails without this.
func checkpointArgName(args []string, stderr io.Writer, verb string) (string, []string, bool) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintf(stderr, "feint %s: name one checkpoint: feint %s <name> [flags]\n", verb, verb)
		return "", nil, false
	}
	name := args[0]
	if name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		fmt.Fprintf(stderr, "feint %s: %q is not a checkpoint name: no path separators, no . or ..\n", verb, name)
		return "", nil, false
	}
	return name, args[1:], true
}

// checkpointCommand takes a named checkpoint of a running lab.
func checkpointCommand(args []string, stdout, stderr io.Writer) int {
	// The name comes first and the flags after it, the way `feint snapshot
	// save <name> --addr …` already reads. Go's flag package stops at the
	// first non-flag argument, so a name parsed by it swallows every flag
	// behind it into the positionals — which is exactly what the first
	// measurement of this command did, refusing `feint checkpoint baseline
	// --addr 127.0.0.1:4621` on its own usage line.
	// The flag set is built BEFORE the name is taken off, and that order is
	// not cosmetic: `feint checkpoint -h` must print the flags, and the first
	// version refused on its own usage line instead. The frozen-surface
	// observation found it, because a verb that builds no flag set freezes
	// nothing.
	fs := newFlagSet("checkpoint")
	addr := fs.String("addr", DefaultAddr, "listen address of the emulator to checkpoint")
	if len(args) != 0 && strings.HasPrefix(args[0], "-") {
		// -h, --help, or a mistyped flag: let the flag set answer, which is
		// the only thing that can print the usage.
		if err := fs.Parse(args); err != nil {
			return exitError
		}
	}
	name, rest, ok := checkpointArgName(args, stderr, "checkpoint")
	if !ok {
		return exitError
	}
	if err := fs.Parse(rest); err != nil {
		return exitError
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "feint checkpoint: unexpected argument %q; one name, then flags\n", fs.Arg(0))
		return exitError
	}
	endpoint := "http://" + *addr

	// The emulator's half first: a checkpoint whose state was observed and
	// whose store was never taken is a checkpoint a reset cannot honour, and
	// the observation would read as a success.
	answer, err := callCheckpoint(http.MethodPost, endpoint+"/_feint/checkpoint/"+name)
	if err != nil {
		fmt.Fprintf(stderr, "feint checkpoint: %v\n", err)
		return exitError
	}

	state, runtimeName, err := observeFor(endpoint)
	if err != nil {
		fmt.Fprintf(stderr, "feint checkpoint: the checkpoint was taken and could not be read back: %v\n", err)
		return exitError
	}
	if err := writeRecordedCheckpoint(*addr, recordedCheckpoint{
		SchemaVersion: checkpointStateVersion, Name: name,
		Taken: time.Now().UTC().Format(time.RFC3339), Runtime: runtimeName, State: state,
	}); err != nil {
		fmt.Fprintf(stderr, "feint checkpoint: %v\n", err)
		return exitError
	}

	fmt.Fprintf(stdout, "checkpoint %q: %d resource(s), %d object(s), %d machine(s)\n",
		name, len(state.Resources), len(state.Objects), len(state.Machines))
	if carried, _ := answer["bytes"].(bool); !carried {
		// Said rather than left out: a reader who resets expecting bytes back
		// would otherwise find none and blame the reset.
		fmt.Fprintln(stdout, "  bytes: this emulator serves no object store, so none are part of it")
	}
	return exitOK
}

// resetCommand returns a lab to a checkpoint and judges whether it converged.
func resetCommand(args []string, stdout, stderr io.Writer) int {
	// The name is optional here, so the split is by shape rather than by
	// position: a leading argument that does not start with a dash is the
	// name, everything else is flags. Same reason as above.
	fs := newFlagSet("reset")
	addr := fs.String("addr", DefaultAddr, "listen address of the emulator to reset")
	asked, rest := "", args
	if len(args) != 0 && !strings.HasPrefix(args[0], "-") {
		asked, rest = args[0], args[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return exitError
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "feint reset: unexpected argument %q; one name at most, then flags\n", fs.Arg(0))
		return exitError
	}
	endpoint := "http://" + *addr

	recorded, err := readRecordedCheckpoint(*addr)
	if err != nil {
		fmt.Fprintf(stderr, "feint reset: %v\n", err)
		fmt.Fprintf(stderr, "  Take one first: feint checkpoint <name> --addr %s\n", *addr)
		return exitError
	}
	name := recorded.Name
	if asked != "" {
		name = asked
	}
	if name != recorded.Name {
		// Refused rather than guessed. The recorded state is what a verdict
		// compares against, and comparing one checkpoint's emulator state to
		// another's observation would report differences that are not the
		// reset's fault.
		fmt.Fprintf(stderr, "feint reset: this instance recorded %q, not %q, so a verdict on %q would compare the wrong states\n",
			recorded.Name, name, name)
		return exitError
	}

	answer, err := callCheckpoint(http.MethodPost, endpoint+"/_feint/reset/"+name)
	if err != nil {
		fmt.Fprintf(stderr, "feint reset: %v\n", err)
		return exitError
	}
	reportReconciliation(stdout, answer)

	state, _, err := observeFor(endpoint)
	if err != nil {
		fmt.Fprintf(stderr, "feint reset: the reset ran and could not be read back: %v\n", err)
		return exitError
	}

	// The verdict, and the only one worth having: what a client can observe,
	// compared with what it could observe at the checkpoint.
	differences := recorded.State.diff(state)
	if len(differences) == 0 {
		fmt.Fprintf(stdout, "reset %q: the lab converged\n", name)
		return exitOK
	}
	fmt.Fprintf(stderr, "feint reset: %q did not converge; %d difference(s) remain:\n", name, len(differences))
	for _, difference := range differences {
		fmt.Fprintf(stderr, "  %s\n", difference)
	}
	if recreates, _ := answer["recreates"].(bool); !recreates {
		fmt.Fprintln(stderr, "  This reset removes what the checkpoint does not hold and recreates nothing:")
		fmt.Fprintln(stderr, "  a machine the checkpoint had and the host no longer does needs a boot only")
		fmt.Fprintln(stderr, "  its pack can render, and docs/limits.md says so.")
	}
	return exitError
}

// reportReconciliation prints what the emulator says it did, before the
// verdict says whether it was enough.
func reportReconciliation(stdout io.Writer, answer map[string]any) {
	destroyed := stringsOf(answer["destroyed"])
	skipped := stringsOf(answer["skipped"])
	if len(destroyed) != 0 {
		fmt.Fprintf(stdout, "  destroyed: %s\n", strings.Join(destroyed, ", "))
	}
	if len(skipped) != 0 {
		// A record nobody could act on is what an operator needs to hear
		// about: it is the difference between "nothing to do" and "this run
		// has no runtime".
		fmt.Fprintf(stdout, "  not acted on: %s\n", strings.Join(skipped, ", "))
	}
}

func stringsOf(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

// observeFor reads the observable state, resolving the runtime the emulator
// actually declares.
//
// From /_feint/health rather than from a flag, for the reason the run receipt
// does the same: a capability is declared by the driver, and asking the host
// for a mode nobody is running would refuse on a station that has none.
func observeFor(endpoint string) (observed, string, error) {
	name, _, _ := labHealth(endpoint)
	rt := machine.Runtime{}
	if name != "" && name != "none" && name != "off" {
		resolved, err := resolveRuntime(name, io.Discard)
		if err != nil {
			// Not fatal: the host half stays empty and the verdict says so by
			// comparing fewer axes. Refusing here would make a reset
			// impossible on a station whose runtime went away between the
			// checkpoint and now, which is the moment a reset is most needed.
			return mustObserve(endpoint, rt), name, nil
		}
		rt = resolved
	}
	return mustObserve(endpoint, rt), name, nil
}

func mustObserve(endpoint string, rt machine.Runtime) observed {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	state, err := observeLab(ctx, endpoint, rt)
	if err != nil {
		// Returned empty rather than propagated, because the caller already
		// distinguishes "could not read it back" from "it did not converge"
		// and an empty state would make every axis differ.
		return observed{}
	}
	return state
}

// callCheckpoint drives one of the emulator's checkpoint endpoints.
func callCheckpoint(method, url string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(context.Background(), method, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the emulator did not answer: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	var body map[string]any
	_ = json.NewDecoder(io.LimitReader(res.Body, maxStateBody)).Decode(&body)
	if res.StatusCode != http.StatusOK {
		if message, ok := body["error"].(string); ok {
			return nil, fmt.Errorf("%s", message)
		}
		return nil, fmt.Errorf("the emulator answered %d", res.StatusCode)
	}
	return body, nil
}

// checkpointPath is where the observed state lives: beside the instance
// record, because it belongs to that emulator and goes when it does.
func checkpointPath(addr string) (string, error) {
	dir, err := instanceDir(addr)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "checkpoint.json"), nil
}

func writeRecordedCheckpoint(addr string, recorded recordedCheckpoint) error {
	path, err := checkpointPath(addr)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(recorded, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o600)
}

func readRecordedCheckpoint(addr string) (recordedCheckpoint, error) {
	path, err := checkpointPath(addr)
	if err != nil {
		return recordedCheckpoint{}, err
	}
	body, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return recordedCheckpoint{}, fmt.Errorf("no checkpoint recorded for %s", addr)
	}
	var recorded recordedCheckpoint
	if err := json.Unmarshal(body, &recorded); err != nil {
		return recordedCheckpoint{}, fmt.Errorf("the recorded checkpoint is unreadable: %w", err)
	}
	if recorded.SchemaVersion != checkpointStateVersion {
		return recordedCheckpoint{}, fmt.Errorf(
			"the recorded checkpoint is version %d and this binary reads %d",
			recorded.SchemaVersion, checkpointStateVersion)
	}
	return recorded, nil
}
