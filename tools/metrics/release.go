package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Asset is the part of a GitHub release asset this tool reads.
type Asset struct {
	Name          string `json:"name"`
	DownloadCount int    `json:"download_count"`
}

// Release is the part of a GitHub release this tool reads.
type Release struct {
	TagName     string     `json:"tag_name"`
	Draft       bool       `json:"draft"`
	Prerelease  bool       `json:"prerelease"`
	PublishedAt *time.Time `json:"published_at"`
	Assets      []Asset    `json:"assets"`
}

// Row is one measurement: one asset of one release, on one day.
type Row struct {
	Date        string // YYYY-MM-DD, the day the snapshot was taken
	Version     string
	Asset       string
	OS          string
	Arch        string
	Downloads   int
	PublishedAt string // RFC3339, or empty for a release that was never published
}

// Snapshot is one day's reading of every release.
type Snapshot struct {
	Date     string
	Rows     []Row
	Warnings []string
}

// Parse turns the GitHub payload into the rows a snapshot is made of.
//
// What it deliberately keeps, against the reflex to filter it out:
//
//   - a prerelease and a release whose binaries were downloaded zero times are
//     both kept. A zero is a measurement, and dropping the row would make the
//     delta against tomorrow's non-zero impossible to compute.
//   - a draft is skipped, because its assets are not reachable by anybody but
//     the repository's writers, so its counter measures nothing about adoption.
//
// It refuses, rather than guesses, on two things: an asset that matched no
// declared pattern (see Classify), and a published release carrying none of the
// expected binaries.
//
// TestADraftIsSkippedAndAPrereleaseIsKept and
// TestAReleaseCarryingOnlyCompanionsIsRefused fail without this.
func Parse(cfg *compiled, releases []Release, date string) (*Snapshot, error) {
	snap := &Snapshot{Date: date}
	var unknown []string

	for _, r := range releases {
		if r.Draft {
			continue
		}
		binaries := 0
		for _, a := range r.Assets {
			switch cfg.Classify(a.Name) {
			case KindIgnored:
				continue
			case KindUnknown:
				unknown = append(unknown, fmt.Sprintf("%s/%s", r.TagName, a.Name))
				continue
			}
			binaries++
			goos, arch := cfg.Platform(a.Name)
			published := ""
			if r.PublishedAt != nil {
				published = r.PublishedAt.UTC().Format(time.RFC3339)
			}
			snap.Rows = append(snap.Rows, Row{
				Date:        date,
				Version:     r.TagName,
				Asset:       a.Name,
				OS:          goos,
				Arch:        arch,
				Downloads:   a.DownloadCount,
				PublishedAt: published,
			})
		}
		// A release with no asset at all is a real and harmless state: a tag
		// pushed while the build is still running looks exactly like this, and
		// tomorrow's snapshot will carry its binaries. What is not harmless is
		// a release carrying assets of which none is a binary, because that is
		// the packaging change this tool exists to notice.
		if binaries == 0 && len(r.Assets) > 0 {
			return nil, fmt.Errorf(
				"release %s carries %d asset(s) and not one of them matched a binary pattern; "+
					"either the release workflow renamed the binaries or binary_patterns is stale, "+
					"and counting on regardless would report a drop in adoption that never happened",
				r.TagName, len(r.Assets))
		}
	}

	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf(
			"%d asset(s) matched neither binary_patterns nor ignore_patterns: %s. "+
				"Add each one to whichever list it belongs to; an asset this tool does not "+
				"recognise is a decision for a human, not a silent zero",
			len(unknown), strings.Join(unknown, ", "))
	}
	if len(snap.Rows) == 0 {
		return nil, fmt.Errorf("no published release carries a binary, so there is nothing to measure")
	}

	sort.Slice(snap.Rows, func(i, j int) bool {
		if snap.Rows[i].Version != snap.Rows[j].Version {
			return snap.Rows[i].Version < snap.Rows[j].Version
		}
		return snap.Rows[i].Asset < snap.Rows[j].Asset
	})
	return snap, nil
}

// Total is the figure every report leads with.
func (s *Snapshot) Total() int {
	n := 0
	for _, r := range s.Rows {
		n += r.Downloads
	}
	return n
}

// key identifies one asset of one version across snapshots, which is what a
// delta is computed on.
func (r Row) key() string { return r.Version + "\x00" + r.Asset }

// CompareWith records what moved since the previous snapshot, and warns rather
// than fails when a counter went backwards.
//
// A counter that decreases is not corruption and must not stop the collection:
// GitHub renumbers when an asset or a whole release is deleted and recreated,
// which the release procedure here does on a botched tag. But it silently
// breaks every delta computed across that day, so it is reported.
//
// TestACounterGoingBackwardsWarnsWithoutFailing fails without this.
func (s *Snapshot) CompareWith(previous *Snapshot) map[string]int {
	deltas := map[string]int{}
	if previous == nil {
		return deltas
	}
	before := map[string]int{}
	for _, r := range previous.Rows {
		before[r.key()] = r.Downloads
	}
	for _, r := range s.Rows {
		was, seen := before[r.key()]
		if !seen {
			continue
		}
		if r.Downloads < was {
			s.Warnings = append(s.Warnings, fmt.Sprintf(
				"%s/%s went from %d to %d. GitHub renumbers when an asset or a release is "+
					"deleted and recreated; the delta across this day is not usable",
				r.Version, r.Asset, was, r.Downloads))
		}
		deltas[r.key()] = r.Downloads - was
	}
	return deltas
}
