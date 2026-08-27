// Command testplan says which conformance runs a diff has earned, and which it
// has not.
//
// The whole pass is 1331 s under `FEINT_VM=incus-ovn` and 256 s without a
// runtime, measured on 2026-08-27. The `probe` leg is 0.7 s. Almost no change
// needs the first, and the cost of not knowing which is that everybody either
// runs everything — which is how a batch of three issues spent three full
// passes — or runs nothing and says so afterwards.
//
// This reads the diff and answers. It is not a suggestion engine: `--check`
// exits 2 when a changed path is triaged by nobody, because an unmapped path is
// an absence and must not be read as "nothing to run".
//
//	mise run testplan                 # against origin/main, prints the plan
//	mise run testplan -- --since HEAD~1
//	mise run testplan -- --check      # exit 2 on an un-triaged path
//	git diff --name-only | mise run testplan -- --paths -
//
// What it deliberately does not do is run anything. A tool that both chose the
// runs and performed them would be believed about both, and the choice is the
// part a human has to be able to disagree with.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// command runs git, and only git.
//
// The name is a constant rather than a parameter, and the arguments come from
// literals in plan.go — never from a flag, an environment variable or a path
// out of the diff. That is what makes the //nolint honest: gosec's G204 is
// about a subprocess an attacker can steer, and there is no route from input to
// this call site. Reviewing it means checking that claim, not the annotation.
func command(dir, name string, args ...string) *exec.Cmd {
	if name != "git" {
		panic("testplan runs git and nothing else: " + name)
	}
	cmd := exec.Command(name, args...) //nolint:gosec // literal arguments, see above
	cmd.Dir = dir
	return cmd
}

func main() {
	since := flag.String("since", "origin/main", "the base to diff against")
	check := flag.Bool("check", false, "exit 2 when a changed path is triaged by no rule")
	fromStdin := flag.String("paths", "", "read repository-relative paths from a file, or - for stdin")
	flag.Parse()

	root, err := repoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "testplan:", err)
		os.Exit(1)
	}

	var paths []string
	if *fromStdin != "" {
		paths, err = readPaths(*fromStdin)
	} else {
		paths, err = changed(root, *since)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "testplan:", err)
		os.Exit(1)
	}

	if len(paths) == 0 {
		fmt.Printf("No path changed against %s. Nothing to run.\n", *since)
		return
	}

	p := build(root, paths)
	fmt.Printf("%d path(s) changed against %s\n\n", len(paths), *since)
	fmt.Print(p.String())

	if len(p.Untriaged) > 0 && *check {
		os.Exit(2)
	}
}

func readPaths(from string) ([]string, error) {
	in := os.Stdin
	if from != "-" {
		f, err := os.Open(from)
		if err != nil {
			return nil, err
		}
		defer f.Close() //nolint:errcheck // read-only
		in = f
	}
	var paths []string
	scan := bufio.NewScanner(in)
	for scan.Scan() {
		if line := strings.TrimSpace(scan.Text()); line != "" {
			paths = append(paths, line)
		}
	}
	return paths, scan.Err()
}
