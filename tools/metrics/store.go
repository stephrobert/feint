package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// header is the CSV's first line, and the shape every reader depends on.
var header = []string{"date", "version", "asset", "os", "arch", "downloads", "published_at"}

// History is every snapshot ever taken, keyed by date.
type History struct {
	Dates map[string]*Snapshot
}

// ReadHistory loads the append-only CSV. A missing file is the first run, not
// an error.
func ReadHistory(path string) (*History, error) {
	h := &History{Dates: map[string]*Snapshot{}}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return h, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	for i, rec := range rows {
		if i == 0 {
			if len(rec) != len(header) || rec[0] != header[0] {
				return nil, fmt.Errorf("%s: unexpected header %v, want %v", path, rec, header)
			}
			continue
		}
		if len(rec) != len(header) {
			return nil, fmt.Errorf("%s line %d: %d fields, want %d", path, i+1, len(rec), len(header))
		}
		n, err := strconv.Atoi(rec[5])
		if err != nil {
			return nil, fmt.Errorf("%s line %d: downloads %q: %w", path, i+1, rec[5], err)
		}
		date := rec[0]
		if h.Dates[date] == nil {
			h.Dates[date] = &Snapshot{Date: date}
		}
		h.Dates[date].Rows = append(h.Dates[date].Rows, Row{
			Date: date, Version: rec[1], Asset: rec[2], OS: rec[3], Arch: rec[4],
			Downloads: n, PublishedAt: rec[6],
		})
	}
	return h, nil
}

// SortedDates answers every snapshot date, oldest first.
func (h *History) SortedDates() []string {
	out := make([]string, 0, len(h.Dates))
	for d := range h.Dates {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// Latest is the most recent snapshot, or nil when there is none.
func (h *History) Latest() *Snapshot {
	d := h.SortedDates()
	if len(d) == 0 {
		return nil
	}
	return h.Dates[d[len(d)-1]]
}

// Before answers the most recent snapshot strictly older than date.
func (h *History) Before(date string) *Snapshot {
	var best string
	for _, d := range h.SortedDates() {
		if d < date {
			best = d
		}
	}
	if best == "" {
		return nil
	}
	return h.Dates[best]
}

// Append writes one snapshot at the end of the CSV.
//
// It refuses a date the file already carries rather than appending a second
// one. Two snapshots for one day would double every figure a naive sum
// produces and make the delta for that day read as zero, and the workflow can
// legitimately run twice in a day: once on its schedule and once by hand
// through workflow_dispatch.
//
// TestASecondSnapshotForTheSameDayIsRefused fails without this.
func Append(path string, snap *Snapshot) error {
	existing, err := ReadHistory(path)
	if err != nil {
		return err
	}
	if _, already := existing.Dates[snap.Date]; already {
		return fmt.Errorf("%s already carries a snapshot for %s", path, snap.Date)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}

	fresh := len(existing.Dates) == 0
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644) //nolint:gosec // a committed, non-secret metrics history
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	w := csv.NewWriter(f)
	if fresh {
		if err := w.Write(header); err != nil {
			return err
		}
	}
	for _, r := range snap.Rows {
		if err := w.Write([]string{
			r.Date, r.Version, r.Asset, r.OS, r.Arch,
			strconv.Itoa(r.Downloads), r.PublishedAt,
		}); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}
