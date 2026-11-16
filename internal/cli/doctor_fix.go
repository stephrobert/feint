package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// `feint doctor --fix` (#974): the remedies applied instead of printed, on the
// terms Stéphane set on 2026-11-16 for a command that acts on the user's machine.
//
//   - Opt-in. Plain doctor changes nothing. --fix prints the whole plan first,
//     every action as the command it will run, then asks [y/N] on a terminal.
//     --yes skips the question. Without a terminal and without --yes it runs
//     nothing: a script that forgot --yes must not change a host by accident.
//   - Never sudo. A step whose commands need root is printed as one block for
//     the reader to run, never executed: TestFixNeverRunsARootStep fails when
//     one reaches the runner.
//   - Each step is re-checked after it ran, by the same checks the report
//     reads, and the run stops at the first step that fails or does not pass
//     its re-check: TestFixStopsAtTheFirstFailure and
//     TestFixDoesNotTrustAStepWithoutItsRecheck.
//   - It touches what docs/install.md prescribes or what feint itself created
//     (the /tmp/feint-run-* directories whose owner has exited, re-checked at
//     the moment of removal), and nothing else.
//   - Idempotent by construction: the plan is built from the checks, so a
//     station where every check passes yields an empty plan, and --fix says
//     nothing was changed (TestFixTwiceChangesNothingTheSecondTime).

type fixStep struct {
	title string
	shows []string // what the plan prints: the command, or what is downloaded
	run   func(ctx context.Context) error
	// verify re-checks after run. ok false stops the run, with why.
	verify func(ctx context.Context) (ok bool, why string)
}

type fixPlan struct {
	steps []fixStep
	root  []string // printed for the reader, never run
	// notes are what the plan leaves out and why, printed with it.
	notes []string
	// deferred are user steps that wait on a root step, printed after it.
	deferred []string
}

// fixDeps is everything --fix does to the host, so a test drives the whole
// apply path through fakes. The Incus driver's injectable runner is the model.
type fixDeps struct {
	run        func(ctx context.Context, name string, args ...string) error
	recheck    func() []check
	remove     func(path string) error
	ownerAlive func(dir string) bool
	install    func(ctx context.Context, c clientPin) error
	clientDir  string // "" when HOME is unknown: no client is installed then
	lookPath   func(string) (string, error)
	// uid is whose /tmp/feint-run-* directories may be removed: runDirIsOurs
	// is asked again with it just before each removal.
	uid int
}

func realFixDeps(recheck func() []check) fixDeps {
	self, err := os.Executable()
	if err != nil {
		self = "feint"
	}
	// No HOME, no client: filepath.Join("", ".local", "bin") is a relative
	// path, and the binaries would land in whatever directory doctor ran from.
	dir := ""
	if home, err := os.UserHomeDir(); err == nil && filepath.IsAbs(home) {
		dir = filepath.Join(home, ".local", "bin")
	}
	return fixDeps{
		run: func(ctx context.Context, name string, args ...string) error {
			if name == "feint" {
				name = self
			}
			cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // the user-level commands of buildFixPlan, printed before they run
			cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
			return cmd.Run()
		},
		recheck:    recheck,
		remove:     os.RemoveAll,
		ownerAlive: func(dir string) bool { alive, known := runOwnerState(dir); return alive || !known },
		install: func(ctx context.Context, c clientPin) error {
			return installClient(ctx, c, dir, httpDownload, func(path string) string {
				return runQuiet(path, clientVersionArgs(c.binary)...)
			})
		},
		lookPath:  exec.LookPath,
		clientDir: dir,
		uid:       os.Getuid(),
	}
}

// passes re-reads the checks and answers whether the one with this id passes.
func passes(deps fixDeps, id string) (bool, string) {
	for _, c := range deps.recheck() {
		if c.id == id {
			if c.state == verdictOK {
				return true, ""
			}
			return false, "the check still says: " + c.title
		}
	}
	return false, "the check " + id + " was not found when re-checking"
}

// clientOnPath re-checks an installed client the way the report reads it:
// through PATH, by the same check. A binary written to ~/.local/bin that PATH
// does not reach, or that another one shadows, is not installed for the
// suites, and the reason says which. TestFixSaysWhyAnInstalledClientIsNotSeen
// fails when the re-check reads the file it wrote instead.
func clientOnPath(deps fixDeps, binary, target string) (bool, string) {
	ok, why := passes(deps, "client-"+binary)
	if ok {
		return true, ""
	}
	found, err := deps.lookPath(binary)
	switch {
	case err != nil:
		return false, target + " is installed, but " + filepath.Dir(target) + " is not on your PATH: add it, for example export PATH=\"$HOME/.local/bin:$PATH\""
	case found != target:
		return false, target + " is installed, but " + found + " comes first on your PATH"
	}
	return false, why
}

// buildFixPlan turns the failing checks into steps, in the report's order.
func buildFixPlan(checks []check, deps fixDeps, withClients bool) fixPlan {
	var plan fixPlan
	rootPending := map[string]bool{}
	for _, c := range checks {
		if c.state == verdictOK {
			continue
		}
		switch {
		case c.root && len(c.commands) > 0:
			plan.root = append(plan.root, c.commands...)
			rootPending[c.id] = true
		case c.step && (c.id == stepInit || c.id == stepImages):
			blocked := false
			for _, need := range c.needs {
				blocked = blocked || rootPending[need]
			}
			if blocked {
				plan.deferred = append(plan.deferred, c.commands...)
				rootPending[c.id] = true
				continue
			}
			id := c.id
			title, args := "Initialise Incus (storage pool and network bridge)", []string{"incus", "admin", "init", "--minimal"}
			if id == stepImages {
				title, args = "Build the machine images (a few minutes; starts a temporary container)", []string{"feint", "images"}
			}
			plan.steps = append(plan.steps, fixStep{title: title, shows: []string{strings.Join(args, " ")},
				run:    func(ctx context.Context) error { return deps.run(ctx, args[0], args[1:]...) },
				verify: func(context.Context) (bool, string) { return passes(deps, id) }})
		case c.fixKey == "leftovers":
			for _, path := range c.paths {
				p := path
				plan.steps = append(plan.steps, fixStep{title: "Remove " + p + " (its feint run has ended)", shows: []string{"rm -rf " + shellQuote(p)},
					run: func(context.Context) error {
						// Re-asked at the moment of removal: /tmp is anybody's,
						// so the directory may have been swapped for a link or
						// another user's since the report, and it is only
						// feint's to delete while its owner is gone.
						// TestFixRemovesOnlyARunDirectoryThatIsStillOurs
						// fails without either question.
						if err := runDirIsOurs(p, deps.uid); err != nil {
							return fmt.Errorf("%w; left alone", err)
						}
						if deps.ownerAlive(p) {
							return fmt.Errorf("%s belongs to a run that is going again; left alone", p)
						}
						return deps.remove(p)
					},
					// Gone, or no longer ours: /tmp is anybody's, and
					// another user who recreates the name after the removal
					// must not make --fix report a failure.
					// TestFixCountsARecreatedNameAsRemoved fails when the
					// re-check asks only whether the name exists.
					verify: func(context.Context) (bool, string) {
						if _, err := os.Lstat(p); err == nil && runDirIsOurs(p, deps.uid) == nil {
							return false, p + " is still there"
						}
						return true, ""
					}})
			}
		case c.fixKey == "clients" && withClients && deps.clientDir == "":
			plan.notes = append(plan.notes, "Not installing "+strings.TrimPrefix(c.id, "client-")+": your home directory is unknown, so there is no ~/.local/bin to put it in.")
		case c.fixKey == "clients" && withClients:
			for _, pin := range clientPins {
				if c.id != "client-"+pin.binary {
					continue
				}
				pc := pin
				target := filepath.Join(deps.clientDir, pc.binary)
				shows := []string{"download " + pc.base + "/" + pc.asset}
				// Nothing runs before the [y/N]: what the target answers is
				// the report's reading when PATH led the report to it, and
				// is not asked otherwise. TestTheFixPlanRunsNothingBeforeTheQuestion
				// fails when the plan runs the target.
				if _, err := os.Lstat(target); err == nil {
					line := "replaces " + target
					if c.foundAt == target && c.answered != "" {
						line += " (it answered: " + c.answered + ")"
					}
					shows = append(shows, line)
				}
				plan.steps = append(plan.steps, fixStep{
					title: fmt.Sprintf("Install %s %s into %s, SHA-256 checked", pc.binary, strings.TrimPrefix(pc.version, "v"), deps.clientDir),
					shows: shows,
					run:   func(ctx context.Context) error { return deps.install(ctx, pc) },
					verify: func(context.Context) (bool, string) {
						return clientOnPath(deps, pc.binary, target)
					}})
			}
		}
	}
	return plan
}

// runFix prints the plan, asks, applies, re-checks, and stops at the first
// failure. It returns 1 when a step failed, 0 otherwise.
func runFix(ctx context.Context, w io.Writer, in io.Reader, tty, yes bool, v doctorView, plan fixPlan, _ fixDeps) int {
	fmt.Fprintln(w, v.paint("1", "feint doctor --fix  ·  "+v.distro.label()+"  ·  --vm "+v.mode))
	fmt.Fprintln(w)
	if len(plan.steps) == 0 && len(plan.root) == 0 && len(plan.deferred) == 0 && len(plan.notes) == 0 {
		fmt.Fprintln(w, "Nothing to do: every step feint can fix is done. Nothing was changed.")
		return exitOK
	}
	fmt.Fprintln(w, "Nothing has been changed yet. Here is the plan.")
	if len(plan.steps) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "feint will run %s itself, as you, without sudo:\n", countOf(len(plan.steps), "step", "steps"))
		for i, s := range plan.steps {
			fmt.Fprintf(w, "  %d. %s\n", i+1, s.title)
			for _, line := range s.shows {
				fmt.Fprintln(w, "       "+line)
			}
		}
	}
	printRoot := func() {
		if len(plan.root) == 0 {
			return
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, "feint will NOT run these commands, because they need root.")
		fmt.Fprintln(w, "Run them yourself, then run `feint doctor --fix` again:")
		for _, line := range plan.root {
			fmt.Fprintln(w, "    "+line)
		}
		if len(plan.deferred) > 0 {
			fmt.Fprintln(w, "  and after them, as you:")
			for _, line := range plan.deferred {
				fmt.Fprintln(w, "    "+line)
			}
		}
		if v.playbook {
			// The checkout's absolute path, quoted, so the line runs from
			// wherever it is pasted. TestTheFixPlanNamesThePlaybookByItsAbsolutePath
			// fails on a relative one.
			fmt.Fprintln(w, "  Or all of the root steps in one command (needs Ansible):")
			fmt.Fprintln(w, "    cd "+shellQuote(v.checkout)+" && "+playbookCmd)
		}
	}
	printRoot()
	for _, note := range plan.notes {
		fmt.Fprintln(w)
		fmt.Fprintln(w, note)
	}
	if len(plan.steps) == 0 {
		return exitOK
	}

	fmt.Fprintln(w)
	if !yes {
		if !tty {
			fmt.Fprintf(w, "Nothing was run: there is no terminal to ask for confirmation. Add --yes to run the %s without asking.\n",
				countOf(len(plan.steps), "step", "steps"))
			return exitOK
		}
		fmt.Fprintf(w, "Run the %s now? [y/N] ", countOf(len(plan.steps), "step", "steps"))
		answer, _ := bufio.NewReader(in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			fmt.Fprintln(w, "Nothing was run.")
			return exitOK
		}
		fmt.Fprintln(w)
	}

	total := len(plan.steps)
	for i, s := range plan.steps {
		label := fmt.Sprintf("  [%d/%d] %s ", i+1, total, s.title)
		err := s.run(ctx)
		why := ""
		if err != nil {
			why = err.Error()
		} else if ok, reason := s.verify(ctx); !ok {
			why = "it ran, but " + reason
		}
		if why != "" {
			fmt.Fprintln(w, label+v.paint("31", "FAILED"))
			for _, line := range wrap(why, 8) {
				fmt.Fprintln(w, "        "+line)
			}
			fmt.Fprintln(w)
			fmt.Fprintf(w, "Stopped at step %d.", i+1)
			if i+1 < total {
				fmt.Fprintf(w, " Not run: steps %d to %d.", i+2, total)
			}
			fmt.Fprintln(w)
			if len(plan.root) > 0 {
				fmt.Fprintln(w, "Still needs root: the commands printed above.")
			}
			fmt.Fprintln(w, "Run `feint doctor` to see where the station stands.")
			return exitError
		}
		fmt.Fprintln(w, label+v.paint("32", "done, checked"))
	}
	fmt.Fprintln(w)
	if len(plan.root) > 0 {
		fmt.Fprintln(w, "Done. Still needs root: the commands printed above, then `feint doctor --fix` again.")
	} else {
		fmt.Fprintln(w, "Done. Run `feint doctor` to see the station.")
	}
	return exitOK
}
