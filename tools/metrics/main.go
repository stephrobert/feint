// Command metrics measures how much of a project's published software is
// actually downloaded, from the only figure GitHub serves: an asset's
// download_count.
//
// It is deliberately not part of the feint binary. feint emulates clouds; this
// reads a repository's release page. Folding it in would put an outbound HTTP
// call to github.com inside the emulator's binary for a subject that has
// nothing to do with emulation, and docs/adoption-metrics.md promises that
// feint itself phones nobody.
//
//	go run ./tools/metrics collect --config tools/metrics/projects/feint.json
//	go run ./tools/metrics report  --config tools/metrics/projects/feint.json
//	go run ./tools/metrics report  --json
//
// What it does NOT measure, stated where somebody reading the code will see it:
// download_count counts downloads, never people. One CI job that runs hourly
// outweighs a hundred humans, and on this repository the evidence is direct —
// checksums.txt is downloaded slightly MORE often than every binary combined,
// which is the signature of an install script verifying before it runs.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "collect":
		err = collect(os.Args[2:])
	case "report":
		err = report(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "metrics: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: metrics <collect|report> [flags]

  collect   read the repository's releases and append today's snapshot
  report    compute the figures from the snapshots already taken

flags:
  --config <path>   the project description (default tools/metrics/projects/feint.json)
  --dir <path>      where the data lives (default metrics)
  --json            report only: machine-readable output
  --date <date>     collect only: override today, for a replay
`)
}

// dataFiles answers the two paths a project's data lives in.
func dataFiles(dir, project string) (csvPath, jsonPath string) {
	return filepath.Join(dir, project+"-release-downloads.csv"),
		filepath.Join(dir, project+"-latest.json")
}

func collect(args []string) error {
	fs := flag.NewFlagSet("collect", flag.ExitOnError)
	cfgPath := fs.String("config", "tools/metrics/projects/feint.json", "the project description")
	dir := fs.String("dir", "metrics", "where the data lives")
	date := fs.String("date", "", "override today (YYYY-MM-DD)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := LoadConfig(*cfgPath)
	if err != nil {
		return err
	}
	day := *date
	if day == "" {
		day = time.Now().UTC().Format("2006-01-02")
	}

	releases, err := FetchReleases(cfg.cfg.Repository)
	if err != nil {
		return err
	}
	if len(releases) == 0 {
		return fmt.Errorf("%s has no release at all, so there is nothing to measure", cfg.cfg.Repository)
	}

	snap, err := Parse(cfg, releases, day)
	if err != nil {
		return err
	}

	csvPath, jsonPath := dataFiles(*dir, cfg.cfg.Project)
	history, err := ReadHistory(csvPath)
	if err != nil {
		return err
	}
	snap.CompareWith(history.Before(day))

	if err := Append(csvPath, snap); err != nil {
		return err
	}
	// Re-read so the report is built from what was actually written, not from
	// what this process believes it wrote.
	history, err = ReadHistory(csvPath)
	if err != nil {
		return err
	}
	rep := Build(cfg.cfg.Project, history, time.Now())
	rep.Warnings = append(rep.Warnings, snap.Warnings...)
	if err := writeJSON(jsonPath, rep); err != nil {
		return err
	}

	fmt.Printf("%s: %d binary downloads across %d rows, written to %s\n",
		day, snap.Total(), len(snap.Rows), csvPath)
	for _, w := range snap.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	return nil
}

func writeJSON(path string, rep *Report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644) //nolint:gosec // a committed, non-secret figure sheet
}

func report(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	cfgPath := fs.String("config", "tools/metrics/projects/feint.json", "the project description")
	dir := fs.String("dir", "metrics", "where the data lives")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := LoadConfig(*cfgPath)
	if err != nil {
		return err
	}
	csvPath, _ := dataFiles(*dir, cfg.cfg.Project)
	history, err := ReadHistory(csvPath)
	if err != nil {
		return err
	}
	if len(history.Dates) == 0 {
		return fmt.Errorf("%s holds no snapshot yet; run `metrics collect` first", csvPath)
	}
	rep := Build(cfg.cfg.Project, history, time.Now())

	if *asJSON {
		b, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	printReport(rep, history)
	return nil
}

func printReport(rep *Report, h *History) {
	dates := h.SortedDates()
	fmt.Printf("%s adoption metrics\n\n", strings.ToUpper(rep.Project[:1])+rep.Project[1:])
	fmt.Printf("Total binary downloads : %s\n", thousands(rep.TotalBinaryDownloads))
	fmt.Printf("Latest release         : %s\n", rep.LatestRelease)
	fmt.Printf("Snapshots              : %d, from %s to %s\n\n", len(dates), dates[0], dates[len(dates)-1])

	if len(rep.Recent) > 0 {
		fmt.Println("RECENT")
		for _, k := range []string{"downloads_1d", "downloads_7d", "downloads_30d"} {
			if v, ok := rep.Recent[k]; ok {
				fmt.Printf("  %-16s %+d\n", k, v)
			}
		}
		fmt.Println()
	} else {
		fmt.Printf("RECENT\n  no window is closed yet: %d snapshot(s) taken, the first on %s\n\n",
			len(dates), dates[0])
	}

	fmt.Printf("%-12s %10s\n", "VERSION", "DOWNLOADS")
	for _, kv := range sortedByValue(rep.Versions) {
		fmt.Printf("%-12s %10s\n", kv.k, thousands(kv.v))
	}
	fmt.Println()

	fmt.Printf("%-20s %10s %8s\n", "PLATFORM", "DOWNLOADS", "%")
	total := rep.TotalBinaryDownloads
	for _, kv := range sortedByValue(rep.Platforms) {
		pct := 0.0
		if total > 0 {
			pct = float64(kv.v) * 100 / float64(total)
		}
		fmt.Printf("%-20s %10s %7.1f%%\n", kv.k, thousands(kv.v), pct)
	}

	if len(rep.Adoption) > 0 {
		fmt.Printf("\nADOPTION, per release, from its own publication day\n")
		versions := make([]string, 0, len(rep.Adoption))
		for v := range rep.Adoption {
			versions = append(versions, v)
		}
		sort.Strings(versions)
		for _, v := range versions {
			fmt.Printf("  %-12s", v)
			for _, j := range []string{"J+1", "J+7", "J+30"} {
				if n, ok := rep.Adoption[v][j]; ok {
					fmt.Printf("  %s %-6d", j, n)
				}
			}
			fmt.Println()
		}
	} else {
		fmt.Printf("\nADOPTION\n  no point measurable yet: GitHub serves the current counter and never\n" +
			"  its history, so a J+1, J+7 or J+30 whose day fell before collection\n" +
			"  started cannot be recovered. Points whose day is still ahead will\n" +
			"  appear as the snapshots reach them.\n")
	}

	if len(rep.ActiveOldReleases) > 0 {
		fmt.Printf("\nOLDER RELEASES STILL BEING DOWNLOADED (last 7 days)\n")
		for _, a := range rep.ActiveOldReleases {
			fmt.Printf("  %-12s +%d\n", a.Version, a.Downloads)
		}
		fmt.Printf("  A pinned CI, an install script or a third party's image looks like this.\n")
	}

	for _, w := range rep.Warnings {
		fmt.Fprintf(os.Stderr, "\nwarning: %s\n", w)
	}
}

type kv struct {
	k string
	v int
}

func sortedByValue(m map[string]int) []kv {
	out := make([]kv, 0, len(m))
	for k, v := range m {
		out = append(out, kv{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].v != out[j].v {
			return out[i].v > out[j].v
		}
		return out[i].k < out[j].k
	})
	return out
}

// thousands groups digits with a narrow space, the French convention this
// project's own documentation uses.
func thousands(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
