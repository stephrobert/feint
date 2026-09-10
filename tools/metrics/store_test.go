package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestASecondSnapshotForTheSameDayIsRefused. The workflow can legitimately run
// twice in a day, once on its schedule and once through workflow_dispatch, and
// the CSV is append-only: a second row set would double every naive sum and
// make that day's delta read as zero.
func TestASecondSnapshotForTheSameDayIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.csv")
	cfg := testConfig(t)
	snap, err := Parse(cfg, []Release{release("v1.0.0", "2026-01-01", 5, 0, 0, 0)}, "2026-01-02")
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, snap); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, snap); err == nil {
		t.Fatal("a second snapshot for 2026-01-02 was appended")
	}
	h, err := ReadHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Dates["2026-01-02"].Total(); got != 5 {
		t.Errorf("total is %d, want 5: the refused append still wrote rows", got)
	}
}

// TestTheCSVRoundTrips holds the format the site and any future reader depend on.
func TestTheCSVRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.csv")
	cfg := testConfig(t)
	snap, err := Parse(cfg, []Release{release("v1.0.0", "2026-01-01", 7, 1, 2, 3)}, "2026-01-02")
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, snap); err != nil {
		t.Fatal(err)
	}
	h, err := ReadHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	back := h.Dates["2026-01-02"]
	if len(back.Rows) != len(snap.Rows) {
		t.Fatalf("%d rows read, %d written", len(back.Rows), len(snap.Rows))
	}
	for i := range snap.Rows {
		if back.Rows[i] != snap.Rows[i] {
			t.Errorf("row %d: read %+v, wrote %+v", i, back.Rows[i], snap.Rows[i])
		}
	}
}

// TestAMissingFileIsTheFirstRun, not an error.
func TestAMissingFileIsTheFirstRun(t *testing.T) {
	h, err := ReadHistory(filepath.Join(t.TempDir(), "absent.csv"))
	if err != nil {
		t.Fatalf("a missing file must read as an empty history: %v", err)
	}
	if len(h.Dates) != 0 || h.Latest() != nil {
		t.Error("an absent file produced snapshots")
	}
}

// TestPaginationWalksEveryPage covers the >100 releases case the brief names.
func TestPaginationWalksEveryPage(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		page := r.URL.Query().Get("page")
		var batch []Release
		switch page {
		case "1", "2":
			for i := 0; i < 100; i++ {
				batch = append(batch, release(fmt.Sprintf("v0.%s.%d", page, i), "2026-01-01", 1, 0, 0, 0))
			}
		default:
			batch = nil // the empty page ends the walk
		}
		_ = json.NewEncoder(w).Encode(batch)
	}))
	defer srv.Close()

	old := apiBase
	apiBase = srv.URL
	defer func() { apiBase = old }()

	got, err := FetchReleases("o/r")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 200 {
		t.Errorf("%d releases, want 200 across two full pages", len(got))
	}
	if pages != 3 {
		t.Errorf("%d requests, want 3 (two full pages then the empty one)", pages)
	}
}

// TestAnUnavailableAPIFailsRatherThanWritingAPartialSnapshot. The CSV is
// append-only, so a partial read committed today is indistinguishable from a
// collapse in downloads forever after.
func TestAnUnavailableAPIFailsRatherThanWritingAPartialSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			_ = json.NewEncoder(w).Encode([]Release{release("v1.0.0", "2026-01-01", 1, 0, 0, 0)})
			return
		}
		http.Error(w, `{"message":"Server Error"}`, http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	old := apiBase
	apiBase = srv.URL
	defer func() { apiBase = old }()

	got, err := FetchReleases("o/r")
	if err == nil {
		t.Fatalf("a 503 on page 2 was accepted, returning %d releases", len(got))
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("the error does not carry the status: %v", err)
	}
}

// TestTrailingWindowsStayAbsentUntilTheyClose. "Nothing was downloaded in 30
// days" and "this tool has not been collecting for 30 days" are opposite facts,
// and a zero says the first while meaning the second.
func TestTrailingWindowsStayAbsentUntilTheyClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.csv")
	cfg := testConfig(t)
	for i, day := range []string{"2026-03-01", "2026-03-03"} {
		snap, err := Parse(cfg, []Release{release("v1.0.0", "2026-01-01", 100+i*10, 0, 0, 0)}, day)
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
	now, _ := time.Parse("2006-01-02", "2026-03-03")
	rep := Build("feint", h, now)

	if _, ok := rep.Recent["downloads_30d"]; ok {
		t.Error("a 30-day window closed on a 2-day history")
	}
	if got, ok := rep.Recent["downloads_1d"]; !ok || got != 10 {
		t.Errorf("downloads_1d is %d (present: %v), want 10", got, ok)
	}
}
