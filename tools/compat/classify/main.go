// Command classify turns the two payloads compat.sh produced into verdicts.
//
// It is a thin front for internal/compat, which holds the judgement and its
// tests: a classifier written inline in a shell script is a classifier nothing
// can falsify, and this repository has already paid for controls that could not
// be shown to bite.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/stephrobert/feint/internal/compat"
)

type resultLine struct {
	Name    string `json:"name"`
	Surface string `json:"surface"`
	Source  string `json:"source"`
	Means   string `json:"means"`
	Before  string `json:"before"`
	After   string `json:"after"`
}

type versionLine struct {
	Surface     string `json:"surface"`
	Before      string `json:"before"`
	After       string `json:"after"`
	BeforeKnown bool   `json:"beforeKnown"`
	AfterKnown  bool   `json:"afterKnown"`
}

func readLines[T any](path string) ([]T, error) {
	file, err := os.Open(path) //nolint:gosec // a path this tool was handed
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	var out []T
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var item T
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, scanner.Err()
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: classify <results.json> <versions.json> <accepted.json>")
		os.Exit(1)
	}

	results, err := readLines[resultLine](os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "read the results: %v\n", err)
		os.Exit(1)
	}
	versionLines, err := readLines[versionLine](os.Args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "read the versions: %v\n", err)
		os.Exit(1)
	}
	// A run that measured nothing must not report success: an empty expression
	// list is the shape a broken harness takes, and it looks exactly like a clean
	// comparison.
	if len(results) == 0 {
		fmt.Fprintln(os.Stderr, "FAIL: no expression was evaluated, so this run measured nothing")
		os.Exit(1)
	}

	var accepted []compat.Accepted
	if raw, err := os.ReadFile(os.Args[3]); err == nil { //nolint:gosec // a path this tool was handed
		var file struct {
			Accepted []compat.Accepted `json:"accepted"`
		}
		if err := json.Unmarshal(raw, &file); err != nil {
			fmt.Fprintf(os.Stderr, "read the accepted list: %v\n", err)
			os.Exit(1)
		}
		accepted = file.Accepted
	}

	versions := map[string]compat.Versions{}
	for _, line := range versionLines {
		versions[line.Surface] = compat.Versions{
			Before:      line.Before,
			After:       line.After,
			BeforeKnown: line.BeforeKnown,
			AfterKnown:  line.AfterKnown,
		}
	}

	exprs := make([]compat.Expression, 0, len(results))
	for _, r := range results {
		exprs = append(exprs, compat.Expression{
			Name: r.Name, Surface: r.Surface, Source: r.Source,
			Means: r.Means, Before: r.Before, After: r.After,
		})
	}

	findings := compat.ClassifyAll(exprs, versions)
	sort.SliceStable(findings, func(i, j int) bool {
		return rank(findings[i].Verdict) < rank(findings[j].Verdict)
	})

	counts := map[compat.Verdict]int{}
	for _, finding := range findings {
		counts[finding.Verdict]++
		fmt.Printf("  %-18s %s\n", finding.Verdict, finding.Name)
		if finding.Verdict != compat.Compatible {
			fmt.Printf("      %s\n", finding.Source)
			fmt.Printf("      meant: %s\n", finding.Means)
			fmt.Printf("      %s\n", finding.Why)
		}
	}

	fmt.Printf("\n%d compatible, %d explicitly broken, %d silently wrong\n",
		counts[compat.Compatible], counts[compat.ExplicitlyBroken], counts[compat.SilentlyWrong])

	fail, stale := compat.Gate(findings, accepted)
	for _, name := range stale {
		fmt.Fprintf(os.Stderr, "FAIL: %q is accepted and matched nothing; a stale exemption is a "+
			"gate that stopped covering what it names\n", name)
	}
	for _, finding := range fail {
		fmt.Fprintf(os.Stderr, "FAIL: %s is silently wrong and is not accepted\n", finding.Name)
	}
	if len(fail) > 0 || len(stale) > 0 {
		os.Exit(1)
	}
	fmt.Println("no unaccepted silently-wrong expression: this release may be tagged")
}

func rank(v compat.Verdict) int {
	switch v {
	case compat.SilentlyWrong:
		return 0
	case compat.ExplicitlyBroken:
		return 1
	default:
		return 2
	}
}
