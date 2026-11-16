package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// How doctor prints (#974). Stéphane validated the layout on 2026-11-16 from a
// mock-up: checks grouped by what they serve, problems before successes outside
// the ordered setup steps, each problem as what is wrong, why it matters, then
// the command on its own line, and a summary that ends with what to run.
//
// Everything goes to stdout, the tally of failures also to stderr as before.
// The exit codes do not change: 1 when a check fails, 0 otherwise.

type doctorView struct {
	mode     string
	distro   distro
	verbose  bool
	color    bool
	playbook bool   // tools/install/ansible/local.yml is reachable from here
	checkout string // the absolute path of the feint clone it is in
}

const (
	doctorWidth   = 80
	localPlaybook = "tools/install/ansible/local.yml"
	playbookCmd   = "ansible-playbook --ask-become-pass " + localPlaybook
)

var groupOrder = []string{groupEmulator, groupMachines, groupClients, groupTools, groupHost, groupStack}

func probeDistro(p hostProbe) distro {
	text, err := p.osRelease()
	if err != nil {
		return distro{}
	}
	return parseOSRelease(text)
}

// playbookFound answers whether the one-command setup exists where doctor
// would tell the reader to run it from, and in which clone. Stéphane,
// 2026-11-16: never point at an absent file. A file of that name is not
// enough: it has to sit in a feint checkout, the one whose go.mod declares
// this module, or doctor would hand a root playbook from any directory to
// ansible-playbook --ask-become-pass. TestTheSummaryNamesThePlaybookOnlyWhenItIsThere
// and TestThePlaybookIsOfferedOnlyFromAFeintCheckout fail without the conditions.
func playbookFound() (bool, string) {
	return playbookIn(".")
}

func playbookIn(dir string) (bool, string) {
	info, err := os.Stat(filepath.Join(dir, localPlaybook))
	if err != nil || info.IsDir() || !inFeintCheckout(dir) {
		return false, ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false, ""
	}
	return true, abs
}

// inFeintCheckout answers whether dir is the root of a feint clone: its go.mod
// declares this module.
func inFeintCheckout(dir string) bool {
	mod, err := os.ReadFile(filepath.Join(dir, "go.mod")) //nolint:gosec // the go.mod of the directory doctor runs in
	return err == nil && firstLine(string(mod)) == "module "+feintModule
}

// feintModule is this repository's module path, as go.mod declares it.
const feintModule = "github.com/stephrobert/feint"

// colorWanted is true on a terminal only, and never with NO_COLOR set
// (https://no-color.org: any value, even empty, turns colour off).
func colorWanted(w io.Writer) bool {
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	f, ok := w.(*os.File)
	return ok && isTerminal(f)
}

func (v doctorView) paint(code, text string) string {
	if !v.color {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (v doctorView) mark(c check) string {
	switch {
	case c.state == verdictFail || (c.step && !c.optional && c.state != verdictOK):
		return v.paint("31", "✗")
	case c.state == verdictWarn:
		return v.paint("33", "!")
	default:
		return v.paint("32", "✓")
	}
}

// stepNumbers numbers the missing setup steps in the order they appear, which
// is the order setupChecks emits: the order of the work.
func stepNumbers(checks []check) map[string]int {
	numbers := map[string]int{}
	for _, c := range checks {
		if c.step && c.state != verdictOK && c.id != "" {
			numbers[c.id] = len(numbers) + 1
		}
	}
	return numbers
}

func wrap(text string, indent int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		if line != "" && indent+len([]rune(line))+1+len([]rune(word)) > doctorWidth {
			lines = append(lines, line)
			line = ""
		}
		if line == "" {
			line = word
		} else {
			line += " " + word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// renderDoctor prints the report and returns the number of failed checks.
func renderDoctor(w io.Writer, checks []check, v doctorView) int {
	numbers := stepNumbers(checks)
	header := "feint doctor  ·  " + v.distro.label()
	if v.mode != "" {
		header += "  ·  checking --vm " + v.mode
	}
	fmt.Fprintln(w, v.paint("1", header))

	failed := 0
	for _, group := range groupOrder {
		var members []check
		for _, c := range checks {
			if c.group == group {
				members = append(members, c)
			}
		}
		if len(members) == 0 {
			continue
		}
		if group != groupMachines {
			members = problemsFirst(members)
		}
		title := group
		if group == groupMachines {
			title += " (--vm " + v.mode + ")"
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, v.paint("1", title)+rightCount(title, members))

		lastBody := false
		var compact []string
		flush := func() {
			if len(compact) == 0 {
				return
			}
			for i, line := range wrap(strings.Join(compact, "  ·  "), 5) {
				lead := "     "
				if i == 0 {
					lead = "  " + v.paint("32", "✓") + "  "
				}
				fmt.Fprintln(w, lead+strings.ReplaceAll(line, "\x00", " "))
			}
			compact = nil
		}
		lastClient := -1
		for i, c := range members {
			if group == groupClients && c.fixKey == "clients" && c.state != verdictOK {
				lastClient = i
			}
		}
		for i, c := range members {
			if c.state == verdictFail {
				failed++
			}
			// Versions of tools that are fine read best on one line.
			if c.state == verdictOK && !v.verbose && (group == groupClients || group == groupTools) {
				compact = append(compact, strings.ReplaceAll(c.title, " ", "\x00"))
				continue
			}
			flush()
			body := c.state != verdictOK && (c.detail != "" || c.fix != "" || len(c.commands) > 0)
			if v.verbose && (c.detail != "" || c.verbose != "") {
				body = true
			}
			if (body && i > 0) || (!body && lastBody) {
				fmt.Fprintln(w)
			}
			lastBody = body

			title := c.title
			if n, ok := numbers[c.id]; ok {
				title = fmt.Sprintf("%d. %s", n, title)
			}
			for _, need := range c.needs {
				if n, ok := numbers[need]; ok && c.state != verdictOK {
					title += fmt.Sprintf("  (do step %d first)", n)
					break
				}
			}
			if c.after != "" && c.state != verdictOK {
				title += "  (" + c.after + ")"
			}
			for j, line := range wrap(title, 5) {
				lead := "     "
				if j == 0 {
					lead = "  " + v.mark(c) + "  "
				}
				fmt.Fprintln(w, lead+line)
			}
			if c.state != verdictOK || v.verbose {
				for _, text := range []string{c.detail, c.fix} {
					for _, line := range wrap(text, 8) {
						fmt.Fprintln(w, "        "+line)
					}
				}
			}
			if v.verbose {
				for _, line := range wrap(c.verbose, 8) {
					fmt.Fprintln(w, "        "+v.paint("2", line))
				}
			}
			if c.state != verdictOK {
				for _, cmd := range c.commands {
					fmt.Fprintln(w, "          "+cmd)
				}
			}
			// Right under the last client it installs, before jq: printed
			// after jq's own command it read as part of jq's remedy.
			if i == lastClient {
				fmt.Fprintln(w, "        Install these into ~/.local/bin, each checked against its")
				fmt.Fprintln(w, "        published SHA-256 (nothing else is changed):")
				fmt.Fprintln(w, "          feint doctor --fix --clients --vm off")
			}
		}
		flush()
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, strings.Repeat("─", doctorWidth))
	for _, line := range summary(checks, v, numbers) {
		fmt.Fprintln(w, line)
	}
	return failed
}

// groupNeedsNewLogin is said wherever the playbook is offered, above the
// commands: it adds the reader to incus-admin, and a plain `feint images`
// pasted with the playbook ran without the group and was refused. sg ships
// with Debian and Fedora and needs no new login. TestTheSummarySaysTheGroupNeedsANewLogin
// fails when the note follows the commands or the images run outside sg.
const (
	groupNeedsNewLogin = "It adds you to incus-admin, and only a new login picks the group up: `sg incus-admin` runs the images build in that group without one."
	imagesInNewGroup   = "sg incus-admin -c 'feint images'"
)

// wrapIndent wraps text for the summary, each line led by indent spaces.
func wrapIndent(text string, indent int) []string {
	lines := wrap(text, indent)
	for i := range lines {
		lines[i] = strings.Repeat(" ", indent) + lines[i]
	}
	return lines
}

// problemsFirst keeps the order within each kind: fails, then warnings, then
// what is fine.
func problemsFirst(checks []check) []check {
	out := make([]check, 0, len(checks))
	for _, want := range []verdict{verdictFail, verdictWarn, verdictOK} {
		for _, c := range checks {
			if c.state == want {
				out = append(out, c)
			}
		}
	}
	return out
}

func rightCount(title string, members []check) string {
	steps, optional, warns, fails := 0, 0, 0, 0
	for _, c := range members {
		switch {
		case c.step && c.optional && c.state != verdictOK:
			optional++
		case c.step && c.state != verdictOK:
			steps++
		case c.state == verdictFail:
			fails++
		case c.state == verdictWarn:
			warns++
		}
	}
	var parts []string
	if steps > 0 {
		parts = append(parts, countOf(steps, "step", "steps")+" missing")
	}
	if optional > 0 {
		parts = append(parts, fmt.Sprintf("%d optional", optional))
	}
	if fails > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", fails))
	}
	if warns > 0 {
		parts = append(parts, fmt.Sprintf("%d to look at", warns))
	}
	if len(parts) == 0 {
		return ""
	}
	text := strings.Join(parts, ", ")
	pad := doctorWidth - len([]rune(title)) - len([]rune(text))
	if pad < 2 {
		pad = 2
	}
	return strings.Repeat(" ", pad) + text
}

func countOf(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// summary is the last word: how many steps are missing and the one command
// that fixes them, or that everything the mode needs is present.
func summary(checks []check, v doctorView, numbers map[string]int) []string {
	var out []string
	others, fails, imagesMissing, cleanable, ansibleMissing := 0, 0, false, false, false
	// Optional steps (OVN under --vm auto) are numbered with the others, for
	// the order of the work, and counted apart: auto runs without them.
	// TestAutoCountsTheOVNStepsAsOptional fails when they join the count.
	steps, optional := 0, 0
	for _, c := range checks {
		ansibleMissing = ansibleMissing || (c.id == "tool-ansible" && c.state != verdictOK)
		if _, ok := numbers[c.id]; ok {
			if c.optional {
				optional++
			} else {
				steps++
			}
		}
		switch {
		case c.step && c.state != verdictOK:
			imagesMissing = imagesMissing || c.id == stepImages
		case c.state == verdictFail:
			fails++
		case c.state == verdictWarn:
			others++
			cleanable = cleanable || c.fixKey == "leftovers"
		}
	}
	off := v.mode == "off" || v.mode == "none"
	switch {
	case steps > 0 || optional > 0:
		if steps > 0 {
			out = append(out, countOf(steps, "step", "steps")+" missing for --vm "+v.mode+".")
		} else {
			out = append(out, "Everything --vm "+v.mode+" needs is present.")
		}
		if optional > 0 {
			out = append(out, wrapIndent("Also missing, and optional: "+countOf(optional, "step", "steps")+". Without them, auto picks a mode where two VPCs can reach each other.", 0)...)
		}
		if v.playbook {
			out = append(out, "", "Fix them in one go, from the feint clone in "+v.checkout+".")
			if ansibleMissing {
				out = append(out, wrapIndent("It needs Ansible: install uv, then Ansible (see Repository tools).", 2)...)
			}
			out = append(out, wrapIndent("The playbook asks for your sudo password once, does every step but the images, and installs the conformance clients.", 2)...)
			out = append(out, wrapIndent(groupNeedsNewLogin, 2)...)
			out = append(out, "    "+playbookCmd)
			if imagesMissing {
				out = append(out, "    "+imagesInNewGroup)
			}
			out = append(out, "", "Or step by step: run the commands above, in their numbered order.")
		} else {
			out = append(out, "", "Run the commands above, in their numbered order.")
		}
		out = append(out, "Or let feint run the steps that need no sudo, and print the others:",
			"    feint doctor --fix --vm "+v.mode)
	case off:
		out = append(out, "Machines were not checked (--vm off). The emulator itself needs nothing more.")
	default:
		out = append(out, "Everything --vm "+v.mode+" needs is present.")
	}
	if fails > 0 {
		out = append(out, countOf(fails, "check failed", "checks failed")+": see the ✗ lines above.")
	}
	if others > 0 {
		line := countOf(others, "other thing", "other things") + " to look at, marked ! above."
		out = append(out, line)
		if cleanable && steps == 0 {
			out = append(out, "Remove the leftovers with:", "    feint doctor --fix --vm "+v.mode)
		}
	}
	return out
}
