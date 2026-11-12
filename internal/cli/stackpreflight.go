package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/stack"
)

// The preflight: what this configuration appears to need, answered before a lab
// starts and before a client runs (#888).
//
// WHAT IT IS FOR, MEASURED RATHER THAN ASSUMED
//
// On 2026-11-12, the repository's own Outscale example plus three resources
// this emulator declines was applied against a real emulator, provider 1.8.0:
//
//	Plan: 51 to add
//	48 resources created
//	Error: Unable to create Virtual Gateway — 404 OperationNotEmulated,
//	       "feint does not serve CreateVirtualGateway; see /_feint/routes"
//	Error: Unable to create Client Gateway — the same
//	50 entries left in the state
//
// So the message was never the problem: it names the operation and points at
// the route list. What it cost is the forty-eight creations before the news,
// the half-built lab to tear down, and the staging — outscale_vpn_connection
// was never attempted, its dependencies having failed, so fixing the two
// visible errors would have bought a second full apply to find the third.
//
// WHAT IT IS NOT FOR
//
// Not containment. Under `feint run` the transport already refuses a host it
// was not told about (#849), so a stack that would reach a real cloud is
// stopped by something stronger than a reading of its text. This refuses a run
// that is going to fail, which is a different and smaller promise.

// preflightPolicy is what a verdict does to the run.
type preflightPolicy string

const (
	// preflightAuto refuses a known-incompatible declaration and proceeds on
	// unknown, printing the verdict.
	//
	// Proceeding on unknown is deliberate and is where #888's own wording would
	// have gone wrong. Measured against the recorded-apply sets, most stacks
	// anybody brings hold at least one type nothing here has measured: refusing
	// on unknown would refuse nearly every real configuration, and a gate that
	// refuses everything is a gate people turn off.
	preflightAuto preflightPolicy = "auto"
	// preflightStrict also refuses unknown, for a CI that wants a measured
	// scope or nothing.
	preflightStrict preflightPolicy = "strict"
	// preflightOff skips it. Named rather than discovered: a check somebody
	// disables by deleting a line is one nobody can see was disabled.
	preflightOff preflightPolicy = "off"
)

// stackTarget reads a child command's argv for the three things the preflight
// needs: whether this is an engine at all, which one, and where it will read.
//
// `-chdir=` is honoured, and that is a defect fixed rather than a feature
// added: nothing in this package read it, while stackEscapes took its directory
// from os.Getwd(). So `feint run -- terraform -chdir=infra apply`, from a
// repository root with no top-level .tf, found no stack and the escape warning
// never fired. Read from the code on 2026-11-12.
//
// The subcommand decides too. `init`, `validate`, `fmt` and `providers` make no
// cloud call, and interrogating them about compatibility would be noise on
// commands that cannot fail for the reason this check exists.
func stackTarget(command []string) (dir string, engine stack.Engine, ok bool) {
	if len(command) == 0 {
		return "", "", false
	}
	switch filepath.Base(command[0]) {
	case "terraform":
		engine = stack.EngineTerraform
	case "tofu", "opentofu":
		engine = stack.EngineOpenTofu
	default:
		return "", "", false
	}

	dir = "."
	sub := ""
	for _, arg := range command[1:] {
		if chdir, found := strings.CutPrefix(arg, "-chdir="); found {
			dir = chdir
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if sub == "" {
			sub = arg
		}
	}

	// The subcommands that reach a provider. `console` is in because it
	// evaluates data sources, which are API reads; `show` and `output` are not,
	// because they read the state file.
	switch sub {
	case "plan", "apply", "destroy", "refresh", "import", "test", "console":
		return dir, engine, true
	}
	return "", "", false
}

// preflight reads the stack a child would read and answers what is known about
// it. ran is false when there was nothing to read, which is silence by design:
// `feint run -- go test` in a Go repository must not be interrogated about a
// stack that does not exist.
func stackPreflight(command []string) (rep stack.Report, ran bool) {
	dir, engine, ok := stackTarget(command)
	if !ok {
		return stack.Report{}, false
	}
	return stackPreflightDir(dir, engine)
}

// preflightDir is the same answer for a directory named outright, which is what
// `feint doctor --stack` gives an operator who wants the reading without the
// run.
func stackPreflightDir(dir string, engine stack.Engine) (stack.Report, bool) {
	mod := stack.Load(dir, engine)
	if len(mod.Files) == 0 && len(mod.Unreadable) == 0 {
		return stack.Report{}, false
	}
	srv, _, err := newServer(nil)
	if err != nil {
		// Some other check owns reporting a server that cannot be built, and a
		// preflight that invented a verdict without one would be worse than
		// none.
		return stack.Report{}, false
	}
	rep := stackReport(srv.Packs(), mod)
	rep.Engine = string(engine)
	return rep, true
}

// refuses reports whether this verdict stops the run under this policy.
func refuses(rep stack.Report, policy preflightPolicy) bool {
	switch policy {
	case preflightOff:
		return false
	case preflightStrict:
		return rep.Verdict != stack.VerdictCompatible
	default:
		return rep.Verdict == stack.VerdictIncompatible
	}
}

// writePreflight prints the report for a human.
//
// One block, not one line per declaration: a stack of forty types would bury
// the two rows that matter, which is the failure #888's own example output
// warns against.
func writePreflight(w io.Writer, rep stack.Report) {
	fmt.Fprintf(w, "Feint preflight — %s\n", rep.Dir)
	fmt.Fprintf(w, "  engine       %s\n", rep.Engine)
	fmt.Fprintf(w, "  files        %d read\n", len(rep.Files))

	served, refused, unknown := split(rep)
	fmt.Fprintf(w, "  served       %d type(s)\n", len(served))

	if len(refused) > 0 {
		fmt.Fprintf(w, "  refused      %d:\n", len(refused))
		for _, f := range refused {
			fmt.Fprintf(w, "                 %s — %s does not serve %s\n", f.Name, f.Provider, f.Operation)
			fmt.Fprintf(w, "                   %s\n", f.Reason)
		}
	}
	if len(unknown) > 0 {
		fmt.Fprintf(w, "  unknown      %d: %s\n", len(unknown), strings.Join(unknown, ", "))
		fmt.Fprintln(w, "                 nothing here has measured these; they are not refusals")
	}
	for _, u := range rep.Unresolved {
		fmt.Fprintf(w, "  not read     %s\n", u)
	}
	for _, u := range rep.Unread {
		fmt.Fprintf(w, "  unreadable   %s: %s\n", u.File, u.Reason)
	}

	fmt.Fprintln(w)
	switch rep.Verdict {
	case stack.VerdictIncompatible:
		fmt.Fprintln(w, "Result: NOT COMPATIBLE — a declared type this emulator refuses.")
	case stack.VerdictUnknown:
		fmt.Fprintln(w, "Result: UNKNOWN — part of this configuration has not been measured here.")
	default:
		fmt.Fprintln(w, "Result: COMPATIBLE within what has been measured.")
	}
	// Said on every verdict, including the green one, because it is the limit
	// that matters most and the green one is where it is easiest to forget:
	// `compatible` means every declared type was applied and destroyed against
	// this emulator by a recorded run. It is not a promise about attributes,
	// nested blocks, or anything past the control plane.
	fmt.Fprintln(w, "  `compatible` means a recorded run applied and destroyed these types here;")
	fmt.Fprintln(w, "  it is not a promise about every attribute, nor about behaviour past the")
	fmt.Fprintln(w, "  control plane. docs/limits.md says what this reading cannot cover.")
}

func split(rep stack.Report) (served, refused []stack.Finding, unknown []string) {
	for _, f := range rep.Findings {
		switch f.Standing {
		case stack.StandingServed:
			served = append(served, f)
		case stack.StandingDeclined:
			refused = append(refused, f)
		default:
			unknown = append(unknown, f.Name)
		}
	}
	sort.Strings(unknown)
	return served, refused, unknown
}

// writePreflightJSON writes the report where a CI can read it.
func writePreflightJSON(w io.Writer, rep stack.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

// runPreflight is the whole check as `feint run` performs it: read, print,
// decide. It returns true when the run must stop.
//
// Nothing has started when this answers. That is the point of where it is
// called: a refusal after the lab is up has already cost what it exists to
// save.
func runStackPreflight(command []string, policy preflightPolicy, jsonPath string, stdout, stderr io.Writer) bool {
	if policy == preflightOff {
		return false
	}
	rep, ran := stackPreflight(command)
	if !ran {
		return false
	}

	if jsonPath != "" {
		if err := writePreflightReport(jsonPath, rep, stdout); err != nil {
			fmt.Fprintf(stderr, "feint run: the preflight report could not be written: %v\n", err)
		}
	}

	if !refuses(rep, policy) {
		// Printed anyway when something is not settled, because a verdict only
		// shown on refusal teaches its reader that silence means measured.
		if rep.Verdict != stack.VerdictCompatible {
			writePreflight(stderr, rep)
			fmt.Fprintln(stderr, "  Proceeding: --preflight=strict refuses this, --preflight=off skips the reading.")
		}
		return false
	}

	writePreflight(stderr, rep)
	fmt.Fprintln(stderr)
	fmt.Fprintln(stderr, "feint run: stopping before the lab starts.")
	fmt.Fprintln(stderr, "  Nothing was created and no command ran. Remove the refused declarations,")
	fmt.Fprintln(stderr, "  or pass --preflight=off to run anyway and read what the emulator answers.")
	return true
}

func writePreflightReport(path string, rep stack.Report, stdout io.Writer) error {
	if path == "-" {
		return writePreflightJSON(stdout, rep)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := writePreflightJSON(f, rep); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// checkStackPreflight is `feint doctor --stack`: the same reading, asked for
// outright, with nothing started and nothing changed.
//
// It shares the analyzer with `feint run` rather than answering beside it,
// which is what #888 asks for and what checkStackHazards already does through
// stackConfig. Two readers of one configuration that could disagree would be
// worse than one.
//
// The verdict folds into doctor's own arithmetic on doctor's own rule: a
// refusal is a fail, because the next run dies with certainty — the reasoning
// checkLeftoverDHCP gives — and an unmeasured type is a warn, because it may
// well work and nothing here knows.
func checkStackPreflight(dir, engine string, asJSON bool, stdout, stderr io.Writer) []check {
	if dir == "" {
		return nil
	}
	mode := stack.EngineTerraform
	if engine == "opentofu" || engine == "tofu" {
		mode = stack.EngineOpenTofu
	}
	rep, ran := stackPreflightDir(dir, mode)
	if !ran {
		return []check{{
			title:  "a Terraform or OpenTofu configuration in " + dir,
			state:  verdictWarn,
			detail: "no configuration file was read there",
			fix:    "name the directory holding the root module's .tf files",
		}}
	}

	if asJSON {
		if err := writePreflightJSON(stdout, rep); err != nil {
			fmt.Fprintf(stderr, "feint doctor: %v\n", err)
		}
	} else {
		writePreflight(stdout, rep)
		fmt.Fprintln(stdout)
	}

	switch rep.Verdict {
	case stack.VerdictIncompatible:
		refused := rep.Refused()
		names := make([]string, 0, len(refused))
		for _, f := range refused {
			names = append(names, f.Name)
		}
		return []check{{
			title:  "the configuration in " + dir + " declares a type this emulator refuses",
			state:  verdictFail,
			detail: strings.Join(names, ", "),
			fix:    "remove those declarations, or read why in the report above; `feint run` refuses this stack",
		}}
	case stack.VerdictUnknown:
		return []check{{
			title:  "the configuration in " + dir + " holds something nothing here has measured",
			state:  verdictWarn,
			detail: "the report above names it",
			fix:    "it may well work; `feint run --preflight=strict` is the way to refuse it instead",
		}}
	default:
		return []check{{
			title: "every type the configuration in " + dir + " declares has been applied here before",
			state: verdictOK,
		}}
	}
}
