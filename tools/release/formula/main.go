// Command formula prints the Homebrew formula a release is installed by.
//
// It is a thin front for internal/release, which holds the refusals and their
// tests. Everything it does itself is reading three files this repository owns:
//
//	go run ./tools/release/formula --version 0.9.0 --checksums checksums.txt
//
// `--checksums -` reads the list on stdin, which is how tools/release/tap.sh
// feeds it the file it just fetched from the release.
//
// Exit: 0 the formula is on stdout, 1 it could not be produced. There is no
// exit 2 here: this renders, it does not judge. The verdict is tap.sh's.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/stephrobert/feint/internal/release"
)

const (
	exitOK    = 0
	exitError = 1
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("formula", flag.ContinueOnError)
	fs.SetOutput(stderr)
	version := fs.String("version", "", "the released version, without its leading v")
	checksums := fs.String("checksums", "", "the release's checksums.txt, or - for stdin")
	goMod := fs.String("go-mod", "go.mod", "the module file the repository slug is read from")
	license := fs.String("license", "LICENSE", "the licence file the SPDX identifier is read from")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if *version == "" || *checksums == "" {
		fmt.Fprintln(stderr, "formula: --version and --checksums are required; tools/release/tap.sh fills both in")
		return exitError
	}

	list, err := read(*checksums)
	if err != nil {
		fmt.Fprintf(stderr, "formula: %v\n", err)
		return exitError
	}
	mod, err := os.ReadFile(*goMod)
	if err != nil {
		fmt.Fprintf(stderr, "formula: %v\n", err)
		return exitError
	}
	licence, err := os.ReadFile(*license)
	if err != nil {
		fmt.Fprintf(stderr, "formula: %v\n", err)
		return exitError
	}
	spdx, err := release.LicenseID(string(licence))
	if err != nil {
		fmt.Fprintf(stderr, "formula: %v\n", err)
		return exitError
	}

	out, err := release.Formula(release.Spec{
		Slug:      release.SlugFromModule(string(mod)),
		Version:   *version,
		License:   spdx,
		Checksums: string(list),
	})
	if err != nil {
		fmt.Fprintf(stderr, "formula: %v\n", err)
		return exitError
	}
	fmt.Fprint(stdout, out)
	return exitOK
}

func read(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}
