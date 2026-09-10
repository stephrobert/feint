package main

import (
	"path/filepath"
	"testing"
	"time"
)

// buildHistory appends one snapshot per given day, each carrying the counters
// that day's map names. A version absent from the map is absent from that
// snapshot, which is how a release that does not exist yet is expressed.
func buildHistory(t *testing.T, days []struct {
	day    string
	counts map[string]int
}, published map[string]string) *History {
	t.Helper()
	path := filepath.Join(t.TempDir(), "d.csv")
	cfg := testConfig(t)
	for _, d := range days {
		var rels []Release
		for v, n := range d.counts {
			rels = append(rels, release(v, published[v], n, 0, 0, 0))
		}
		snap, err := Parse(cfg, rels, d.day)
		if err != nil {
			t.Fatal(err)
		}
		if err := Append(path, snap); err != nil {
			t.Fatal(err)
		}
	}
	h, err := ReadHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// TestAdoptionOmitsOnlyThePointsThatFellBeforeCollection is the whole
// availability rule, and it is decided per point rather than per release.
//
// v0.12.1 was published on 2026-09-01 and collection starts on 2026-09-10: its
// J+1 (the 2nd) and J+7 (the 8th) are unrecoverable, while its J+30 (the 1st of
// October) is simply a snapshot away. A rule written per release would have
// thrown all three away.
func TestAdoptionOmitsOnlyThePointsThatFellBeforeCollection(t *testing.T) {
	published := map[string]string{"v0.12.1": "2026-09-01"}
	days := []struct {
		day    string
		counts map[string]int
	}{
		{"2026-09-10", map[string]int{"v0.12.1": 376}},
		{"2026-10-01", map[string]int{"v0.12.1": 402}},
	}
	h := buildHistory(t, days, published)
	now, _ := time.Parse("2006-01-02", "2026-10-01")
	rep := Build("feint", h, now)

	curve := rep.Adoption["v0.12.1"]
	if curve == nil {
		t.Fatal("no curve at all for a release whose J+30 is measurable")
	}
	if _, ok := curve["J+1"]; ok {
		t.Error("J+1 was published for a date that passed before collection started")
	}
	if _, ok := curve["J+7"]; ok {
		t.Error("J+7 was published for a date that passed before collection started")
	}
	if got := curve["J+30"]; got != 402 {
		t.Errorf("J+30 is %d, want 402: a release's cumulative counter on day 30 IS its adoption at J+30", got)
	}
}

// TestAdoptionIsTheCumulativeCounterWithNothingSubtracted. A release's counter
// starts at zero when it is published, so subtracting a later origin reading
// would erase whatever it earned before the first snapshot that saw it.
func TestAdoptionIsTheCumulativeCounterWithNothingSubtracted(t *testing.T) {
	published := map[string]string{"v0.14.0": "2026-09-11"}
	days := []struct {
		day    string
		counts map[string]int
	}{
		{"2026-09-10", map[string]int{}},               // before it exists
		{"2026-09-12", map[string]int{"v0.14.0": 20}},  // J+1
		{"2026-09-18", map[string]int{"v0.14.0": 90}},  // J+7
		{"2026-10-11", map[string]int{"v0.14.0": 347}}, // J+30
	}
	// The first day carries no release at all, which Parse refuses; give it one
	// other release so the snapshot is legal.
	days[0].counts = map[string]int{"v0.13.0": 7}
	published["v0.13.0"] = "2026-09-07"

	h := buildHistory(t, days, published)
	now, _ := time.Parse("2006-01-02", "2026-10-11")
	rep := Build("feint", h, now)

	curve := rep.Adoption["v0.14.0"]
	if curve == nil {
		t.Fatal("no curve for a release published after collection started")
	}
	for point, want := range map[string]int{"J+1": 20, "J+7": 90, "J+30": 347} {
		if got := curve[point]; got != want {
			t.Errorf("%s is %d, want %d", point, got, want)
		}
	}
}

// TestAStaleSnapshotDoesNotAnswerForAMissedPoint: a month-long gap in
// collection must not let a later reading wear J+7's label.
func TestAStaleSnapshotDoesNotAnswerForAMissedPoint(t *testing.T) {
	published := map[string]string{"v1.0.0": "2026-09-11"}
	days := []struct {
		day    string
		counts map[string]int
	}{
		{"2026-09-10", map[string]int{"v1.0.0": 0}},
		{"2026-11-20", map[string]int{"v1.0.0": 5000}}, // the job was down for two months
	}
	h := buildHistory(t, days, published)
	now, _ := time.Parse("2006-01-02", "2026-11-20")
	rep := Build("feint", h, now)

	if n, ok := rep.Adoption["v1.0.0"]["J+7"]; ok {
		t.Errorf("J+7 answered %d from a snapshot 63 days late", n)
	}
}
