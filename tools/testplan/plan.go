package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The machine layer, named once. A file that imports it starts containers on
// the operator's own station whatever directory it lives in, and that is the
// one fact this tool refuses to take from the table in rules.go.
const machineLayer = "github.com/stephrobert/feint/internal/core/machine"

const runtimeLeg = "FEINT_VM=incus-ovn mise run conformance:leg -- runtime"

// plan is what a diff earns: the runs, why each was earned, and what is still
// unproven once they are green.
type plan struct {
	Runs     []planRun
	Unproven []string
	// Untriaged is the failure this tool exists to make loud: a changed path
	// that no rule names. It is never silently cheap and never silently
	// expensive — somebody has to write down what it costs.
	Untriaged []string
}

type planRun struct {
	Command string
	// Because is the human reason, so a reader can disagree with the plan
	// rather than only obey it.
	Because []string
}

// matches reports whether a rule's Path covers a repository-relative path.
//
// Three forms, and no regular expressions: a directory prefix ending in `/`, a
// `*.suffix` glob that applies at the repository root only, or an exact path.
// A suffix glob matches at any depth; longest-match then hands `docs/limits.md`
// to the `docs/` rule and `tools/conformance/README.md` to `*.md`, which is what
// each should get.
func matches(pattern, path string) bool {
	switch {
	case strings.HasSuffix(pattern, "/"):
		return strings.HasPrefix(path, pattern)
	case strings.HasPrefix(pattern, "*."):
		return strings.HasSuffix(path, pattern[1:])
	default:
		return path == pattern
	}
}

// governing returns the rule that decides a path: the most specific one that
// matches it.
//
// Longest prefix, not union of every match. Union was the first shape and it was
// wrong in a way that would have discredited the tool on its first use: a change
// to `tools/conformance/scaleway/scw-cli.sh` matches both that directory and the
// `tools/conformance/` rule above it, and the union sends the reader to the
// 208 s `fields` leg for a file the 7 s `scw-cli` leg drives entirely. A tool
// that over-prescribes is ignored exactly as fast as one that under-prescribes,
// and it is the same failure — the plan stops matching what the reader can see
// for themselves.
// Two questions, not one: what kind of file is this, and where does it live.
// Longest-prefix answers the second, and answering the second alone was wrong on
// this tool's own diff — `tools/conformance/README.md` earned the 208 s `fields`
// leg because it sits under the harness. A Markdown file is documentation
// wherever it lives, and no prose in this repository changes a served answer.
func documentation(path string) bool {
	return strings.HasSuffix(path, ".md") || path == "docs/"
}

func governing(path string) (rule, bool) {
	prose := strings.HasSuffix(path, ".md")
	best, found := rule{}, false
	for _, r := range rules {
		if !matches(r.Path, path) {
			continue
		}
		if prose && !documentation(r.Path) {
			continue
		}
		if !found || len(r.Path) > len(best.Path) {
			best, found = r, true
		}
	}
	return best, found
}

// reachesMachineLayer reports whether a Go file imports the machine layer
// directly.
//
// Directly, and not through `go list -deps`, on purpose. `internal/cli` depends
// on the machine layer transitively — every sub-command does, because `serve`
// carries `--vm` — so a transitive test would demand a 590 s leg for a change to
// the `version` sub-command and would be ignored within a week. What is being
// asked here is narrower and answerable: does *this file* speak to the layer
// that acts on the operator's station.
func reachesMachineLayer(root, path string) bool {
	if !strings.HasSuffix(path, ".go") {
		return false
	}
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, path), nil, parser.ImportsOnly)
	if err != nil {
		// A file the diff deleted, or one that does not parse. Neither is this
		// tool's verdict to give: `mise run check` is where a broken file is
		// reported, and answering "no runtime needed" because a parse failed
		// would be a cheap default earned by an error.
		return false
	}
	for _, imp := range f.Imports {
		if strings.Trim(imp.Path.Value, `"`) == machineLayer {
			return true
		}
	}
	return false
}

// build assembles the plan for a set of repository-relative paths.
func build(root string, paths []string) plan {
	var p plan
	because := map[string][]string{}
	order := []string{}
	unproven := map[string]bool{}

	add := func(cmd, why string) {
		if _, seen := because[cmd]; !seen {
			order = append(order, cmd)
		}
		for _, w := range because[cmd] {
			if w == why {
				return
			}
		}
		because[cmd] = append(because[cmd], why)
	}

	for _, path := range paths {
		r, matched := governing(path)
		if matched {
			for _, cmd := range r.Runs {
				add(normalise(cmd), r.Why)
			}
			if r.Unproven != prepushIsTheWholeGate {
				unproven[r.Unproven] = true
			}
		}
		// Measured rather than tabulated: a file that speaks to the machine
		// layer earns the runtime leg wherever it lives, including a pack file
		// the table above sends to a client leg alone.
		if reachesMachineLayer(root, path) {
			add(runtimeLeg, "imports "+machineLayer+", so it acts on the operator's own station")
		}
		if !matched {
			p.Untriaged = append(p.Untriaged, path)
		}
	}

	sort.Slice(order, func(i, j int) bool { return cost(order[i]) < cost(order[j]) })
	for _, cmd := range order {
		p.Runs = append(p.Runs, planRun{Command: cmd, Because: because[cmd]})
	}
	for u := range unproven {
		p.Unproven = append(p.Unproven, u)
	}
	sort.Strings(p.Unproven)
	sort.Strings(p.Untriaged)
	return p
}

// normalise turns a bare task name into the command a reader can paste.
func normalise(cmd string) string {
	if strings.HasPrefix(cmd, "mise ") || strings.Contains(cmd, "=") {
		return cmd
	}
	return "mise run " + cmd
}

// cost orders the plan cheapest first, from what was measured on 2026-08-27
// rather than from an impression. The numbers are in seconds on one station and
// they are not a promise: their job is to put the 0.7 s run above the 590 s one
// so a reader stops at the first red rather than at the last.
var measured = map[string]int{
	"conformance:leg -- probe":     1,   // 0.7
	"conformance:leg -- exo-cli":   4,   // 4.0
	"conformance:leg -- scw-cli":   7,   // 7.1
	"conformance:leg -- terraform": 45,  // 45.4
	"conformance:leg -- octl":      141, // 141.3
	"conformance:leg -- fields":    208, // 208.1
	"conformance:leg -- runtime":   590, // 590.0, and it needs a runtime
}

func cost(cmd string) int {
	for leg, seconds := range measured {
		if strings.Contains(cmd, leg) {
			return seconds
		}
	}
	if strings.Contains(cmd, "conformance:functional") {
		return 600
	}
	if strings.Contains(cmd, "conformance") {
		return 60
	}
	return 5
}

func (p plan) String() string {
	var b strings.Builder
	if len(p.Untriaged) > 0 {
		fmt.Fprintf(&b, "%d path(s) no rule triages:\n", len(p.Untriaged))
		for _, path := range p.Untriaged {
			fmt.Fprintf(&b, "  ! %s\n", path)
		}
		b.WriteString("\nAdd a rule to tools/testplan/rules.go saying what a change there costs.\n")
		b.WriteString("\"It matched nothing so run nothing\" is the default this refuses: an\n")
		b.WriteString("un-triaged path is an absence, and silence must not read as a decision.\n\n")
	}
	if len(p.Runs) == 0 && len(p.Untriaged) == 0 {
		b.WriteString("Nothing beyond `mise run prepush`.\n")
		b.WriteString("That is a claim this table makes, not a gap in it: no changed path can\n")
		b.WriteString("alter an answer a real client reads.\n")
	} else if len(p.Runs) > 0 {
		b.WriteString("Run these, cheapest first, and stop at the first red:\n\n")
		for _, r := range p.Runs {
			fmt.Fprintf(&b, "  %s\n", r.Command)
			for _, w := range r.Because {
				fmt.Fprintf(&b, "      because %s\n", w)
			}
		}
	}
	if len(p.Unproven) > 0 {
		b.WriteString("\nWhat this plan still does not prove:\n")
		for _, u := range p.Unproven {
			fmt.Fprintf(&b, "  · %s\n", u)
		}
	}
	return b.String()
}

// resolve turns the requested base into one this clone actually has.
//
// `origin/main` is the right default and it is not always present: a fresh
// clone that has never fetched, a worktree on a detached head, CI with a
// shallow checkout. The fallback is `main`, and if neither resolves this says
// so and stops. What it must never do is quietly diff against nothing and
// report an empty plan — a tool that answers "nothing to run" because it could
// not find the base has told the reader the most dangerous possible lie.
func resolve(root, since string) (string, error) {
	for _, candidate := range []string{since, strings.TrimPrefix(since, "origin/")} {
		if _, err := git(root, "rev-parse", "--verify", "--quiet", candidate+"^{commit}"); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("neither %q nor %q names a commit in this clone; pass --since",
		since, strings.TrimPrefix(since, "origin/"))
}

// changed lists the repository-relative paths a range touched.
func changed(root, since string) ([]string, error) {
	base, err := resolve(root, since)
	if err != nil {
		return nil, err
	}
	out, err := git(root, "diff", "--name-only", "--diff-filter=d", base+"...HEAD")
	if err != nil {
		return nil, err
	}
	// Uncommitted work counts: the plan is for what will be delivered, and a
	// tool that judged only the committed half would send its reader to a leg
	// that misses the file they are still editing.
	dirty, err := git(root, "diff", "--name-only", "--diff-filter=d", "HEAD")
	if err != nil {
		return nil, err
	}
	// And a file that exists but has never been added, which is the case this
	// tool was blind to on its own first run: a whole new package under
	// tools/ was invisible to `git diff`, so the plan described the one file
	// that happened to be tracked. A new directory is precisely what the
	// un-triaged check is for, and it would have been reached only after
	// somebody remembered to stage it.
	fresh, err := git(root, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var paths []string
	for _, line := range append(append(out, dirty...), fresh...) {
		if line != "" && !seen[line] {
			seen[line] = true
			paths = append(paths, line)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func git(root string, args ...string) ([]string, error) {
	cmd := command(root, "git", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.Split(strings.TrimRight(string(out), "\n"), "\n"), nil
}

func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	out, err := git(wd, "rev-parse", "--show-toplevel")
	if err != nil || len(out) == 0 {
		return "", fmt.Errorf("not a git repository: %w", err)
	}
	return out[0], nil
}
