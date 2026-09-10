package main

import (
	"sort"
	"strconv"
	"time"
)

// Report is every figure the CLI prints and latest.json carries.
type Report struct {
	GeneratedAt          string         `json:"generated_at"`
	Project              string         `json:"project"`
	TotalBinaryDownloads int            `json:"total_binary_downloads"`
	LatestRelease        string         `json:"latest_release"`
	Platforms            map[string]int `json:"platforms"`
	Versions             map[string]int `json:"versions"`

	// Recent are the deltas over the trailing windows. A window with no
	// snapshot old enough to close it is absent from the map rather than
	// present at zero: "nothing was downloaded in 30 days" and "this tool has
	// not been collecting for 30 days" are opposite facts, and a zero says the
	// first while meaning the second.
	Recent map[string]int `json:"recent,omitempty"`

	// Adoption is J+1 / J+7 / J+30 per release, and it can only ever exist for
	// releases published after the first snapshot: GitHub serves the current
	// counter and never its history, so the curve for a release cut before this
	// tool existed is not late, it is unrecoverable.
	Adoption map[string]map[string]int `json:"adoption,omitempty"`

	// ActiveOldReleases names releases that are not the newest and still take
	// downloads. Not an anomaly: a pinned CI, an install script or a third
	// party's image looks exactly like this, and that is worth seeing.
	ActiveOldReleases []ActiveRelease `json:"active_old_releases,omitempty"`

	Warnings []string `json:"warnings,omitempty"`
}

// ActiveRelease is one older release still being consumed.
type ActiveRelease struct {
	Version   string `json:"version"`
	Downloads int    `json:"downloads_in_window"`
	Days      int    `json:"window_days"`
}

// windows are the trailing deltas the report publishes.
var windows = map[string]int{"downloads_1d": 1, "downloads_7d": 7, "downloads_30d": 30}

// Build computes every figure from the history alone, so the numbers a report
// prints can be recomputed by anybody holding the CSV.
func Build(project string, h *History, now time.Time) *Report {
	latest := h.Latest()
	rep := &Report{
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Project:     project,
		Platforms:   map[string]int{},
		Versions:    map[string]int{},
	}
	if latest == nil {
		return rep
	}

	rep.TotalBinaryDownloads = latest.Total()
	for _, r := range latest.Rows {
		rep.Versions[r.Version] += r.Downloads
		if r.OS != "" || r.Arch != "" {
			rep.Platforms[r.OS+"-"+r.Arch] += r.Downloads
		} else {
			rep.Platforms[r.Asset] += r.Downloads
		}
	}
	rep.LatestRelease = newestPublished(latest)
	rep.Warnings = latest.Warnings

	rep.Recent = trailing(h, latest, now)
	rep.Adoption = adoption(h)
	rep.ActiveOldReleases = activeOld(h, latest, rep.LatestRelease, now)
	return rep
}

// newestPublished is the release with the most recent publication date, which
// is not the same as the largest tag: 0.9.0 sorts after 0.13.0 as a string.
func newestPublished(s *Snapshot) string {
	best, bestAt := "", ""
	for _, r := range s.Rows {
		if r.PublishedAt > bestAt {
			best, bestAt = r.Version, r.PublishedAt
		}
	}
	return best
}

// trailing computes each window's delta, and omits a window the history is too
// short to close.
func trailing(h *History, latest *Snapshot, now time.Time) map[string]int {
	out := map[string]int{}
	for name, days := range windows {
		cutoff := now.UTC().AddDate(0, 0, -days).Format("2006-01-02")
		var base *Snapshot
		for _, d := range h.SortedDates() {
			if d <= cutoff {
				base = h.Dates[d]
			}
		}
		if base == nil || base.Date == latest.Date {
			continue
		}
		out[name] = latest.Total() - base.Total()
	}
	return out
}

// staleDays is how far past a J+N date a snapshot may sit and still answer for
// it. A daily job that missed a run stays usable; a month-long gap does not,
// because the counter kept climbing in between and the figure would be a later
// day's reading wearing J+7's label.
const staleDays = 2

// adoption reads each release's cumulative counter at its own J+1, J+7 and J+30.
//
// There is nothing to subtract, and getting that wrong is the easy mistake: a
// release's counter starts at zero the moment it is published, so its cumulative
// value on day N IS its adoption at J+N. An earlier version of this function
// subtracted the reading at the origin snapshot, which silently erased whatever
// the release earned before the first snapshot that saw it.
//
// What genuinely cannot be recovered is a J+N whose date fell before this tool
// ever ran: GitHub serves the current counter and never its history. For a
// release published on 2026-09-01 with collection starting on 2026-09-10, J+1
// and J+7 are gone for good while J+30 is simply a snapshot away. So the
// availability is decided per point, not per release.
//
// TestAdoptionOmitsOnlyThePointsThatFellBeforeCollection fails without this.
func adoption(h *History) map[string]map[string]int {
	dates := h.SortedDates()
	if len(dates) == 0 {
		return nil
	}
	first := dates[0]

	published := map[string]string{}
	for _, d := range dates {
		for _, r := range h.Dates[d].Rows {
			if r.PublishedAt != "" && published[r.Version] == "" {
				published[r.Version] = r.PublishedAt[:10]
			}
		}
	}

	out := map[string]map[string]int{}
	for version, day := range published {
		curve := map[string]int{}
		for _, n := range []int{1, 7, 30} {
			target := addDays(day, n)
			if target < first {
				// The instant passed before anybody was looking.
				continue
			}
			at := snapshotOnOrAfter(h, target)
			if at == nil || at.Date > addDays(target, staleDays) {
				continue
			}
			curve["J+"+strconv.Itoa(n)] = versionTotal(at, version)
		}
		if len(curve) > 0 {
			out[version] = curve
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// activeOld names releases other than the newest that still moved in the last
// 7 days, busiest first.
func activeOld(h *History, latest *Snapshot, newest string, now time.Time) []ActiveRelease {
	const days = 7
	cutoff := now.UTC().AddDate(0, 0, -days).Format("2006-01-02")
	var base *Snapshot
	for _, d := range h.SortedDates() {
		if d <= cutoff {
			base = h.Dates[d]
		}
	}
	if base == nil || base.Date == latest.Date {
		return nil
	}
	var out []ActiveRelease
	for _, r := range latest.Rows {
		if r.Version == newest {
			continue
		}
		if d := versionTotal(latest, r.Version) - versionTotal(base, r.Version); d > 0 {
			if !containsVersion(out, r.Version) {
				out = append(out, ActiveRelease{Version: r.Version, Downloads: d, Days: days})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Downloads > out[j].Downloads })
	return out
}

func containsVersion(list []ActiveRelease, v string) bool {
	for _, a := range list {
		if a.Version == v {
			return true
		}
	}
	return false
}

func versionTotal(s *Snapshot, version string) int {
	n := 0
	for _, r := range s.Rows {
		if r.Version == version {
			n += r.Downloads
		}
	}
	return n
}

func snapshotOnOrAfter(h *History, date string) *Snapshot {
	for _, d := range h.SortedDates() {
		if d >= date {
			return h.Dates[d]
		}
	}
	return nil
}

func addDays(date string, n int) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return t.AddDate(0, 0, n).Format("2006-01-02")
}
