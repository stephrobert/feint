package main

import (
	"strings"
	"testing"
	"time"
)

// testConfig is feint's own description, so the tests exercise the shipped
// patterns rather than a convenient invention.
func testConfig(t *testing.T) *compiled {
	t.Helper()
	c, err := LoadConfig("projects/feint.json")
	if err != nil {
		t.Fatalf("loading the shipped configuration: %v", err)
	}
	return c
}

func at(day string) *time.Time {
	p, _ := time.Parse("2006-01-02", day)
	return &p
}

// release builds one, with the four binaries plus the four companions this
// repository actually publishes.
func release(tag, day string, counts ...int) Release {
	names := []string{
		"feint-linux-amd64", "feint-linux-arm64",
		"feint-darwin-amd64", "feint-darwin-arm64",
	}
	r := Release{TagName: tag, PublishedAt: at(day)}
	for i, n := range names {
		c := 0
		if i < len(counts) {
			c = counts[i]
		}
		r.Assets = append(r.Assets, Asset{Name: n, DownloadCount: c})
	}
	for _, n := range []string{
		"checksums.txt", "checksums.txt.cosign.bundle",
		"sbom.cdx.json", "provenance.intoto.jsonl",
	} {
		r.Assets = append(r.Assets, Asset{Name: n, DownloadCount: 999})
	}
	return r
}

// TestOnlyBinariesAreCounted is the whole point of the filter, and the figure
// it guards was measured: on 2026-09-09 the four binaries totalled 1254 while
// every asset summed to 2600. Counting the companions would have overstated
// adoption by 107%.
func TestOnlyBinariesAreCounted(t *testing.T) {
	cfg := testConfig(t)
	snap, err := Parse(cfg, []Release{release("v1.0.0", "2026-01-01", 10, 1, 2, 3)}, "2026-01-02")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Rows) != 4 {
		t.Fatalf("%d rows, want 4: the companions leaked into the count", len(snap.Rows))
	}
	if got := snap.Total(); got != 16 {
		t.Errorf("total is %d, want 16 (the four binaries); the companions carry 999 each", got)
	}
}

// TestAnUnexpectedAssetStopsTheCollection is the refusal that keeps a packaging
// change from reading as a drop in adoption.
func TestAnUnexpectedAssetStopsTheCollection(t *testing.T) {
	cfg := testConfig(t)
	for _, name := range []string{
		"feint-windows-amd64",  // an architecture added upstream
		"feint-linux-riscv64",  // another one
		"feint_linux_amd64",    // a separator changed
		"feint-linux-amd64.gz", // the packaging changed
	} {
		r := release("v1.0.0", "2026-01-01", 1, 1, 1, 1)
		r.Assets = append(r.Assets, Asset{Name: name, DownloadCount: 50})
		_, err := Parse(cfg, []Release{r}, "2026-01-02")
		if err == nil {
			t.Errorf("%s was accepted; an asset matching neither list must stop the run", name)
			continue
		}
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the refusal for %s does not name it: %v", name, err)
		}
	}
}

// TestAReleaseCarryingOnlyCompanionsIsRefused is the same defect facing the
// other way, and it needs a release whose every asset is RECOGNISED, or the
// unknown-asset guard answers first and this one is never reached. Falsifying
// caught exactly that: the first version of this test passed with the guard
// removed, because its asset name matched neither list.
//
// The real shape is a release whose binary build failed while the companion
// steps succeeded: checksums and an SBOM are published, no binary is, and every
// name is one the configuration knows. Counting on would report the release as
// downloaded zero times rather than as broken.
func TestAReleaseCarryingOnlyCompanionsIsRefused(t *testing.T) {
	cfg := testConfig(t)
	r := Release{TagName: "v2.0.0", PublishedAt: at("2026-01-01"), Assets: []Asset{
		{Name: "checksums.txt", DownloadCount: 400},
		{Name: "sbom.cdx.json", DownloadCount: 12},
	}}
	_, err := Parse(cfg, []Release{r}, "2026-01-02")
	if err == nil {
		t.Fatal("a release carrying companions and no binary was accepted")
	}
	if !strings.Contains(err.Error(), "v2.0.0") {
		t.Errorf("the refusal does not name the release: %v", err)
	}
}

// TestARenamedBinaryIsRefused is the neighbouring case, caught by the other
// guard: a name matching neither list stops the run.
func TestARenamedBinaryIsRefused(t *testing.T) {
	cfg := testConfig(t)
	r := Release{TagName: "v2.0.0", PublishedAt: at("2026-01-01"), Assets: []Asset{
		{Name: "feint_v2.0.0_linux_amd64.tar.gz", DownloadCount: 400},
		{Name: "checksums.txt", DownloadCount: 400},
	}}
	if _, err := Parse(cfg, []Release{r}, "2026-01-02"); err == nil {
		t.Fatal("a release whose binaries were all renamed was accepted, and would have reported a collapse")
	}
}

// TestADraftIsSkippedAndAPrereleaseIsKept holds two cases that look alike and
// are not: a draft's assets are unreachable by anybody but the repository's
// writers, so its counter measures nothing; a prerelease is public, so it does.
func TestADraftIsSkippedAndAPrereleaseIsKept(t *testing.T) {
	cfg := testConfig(t)
	draft := release("v9.9.9", "2026-01-01", 500, 0, 0, 0)
	draft.Draft = true
	pre := release("v2.0.0-rc1", "2026-01-01", 7, 0, 0, 0)
	pre.Prerelease = true

	snap, err := Parse(cfg, []Release{draft, pre, release("v1.0.0", "2026-01-01", 3, 0, 0, 0)}, "2026-01-02")
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Total(); got != 10 {
		t.Errorf("total is %d, want 10 (7 prerelease + 3 release, the draft's 500 excluded)", got)
	}
	for _, r := range snap.Rows {
		if r.Version == "v9.9.9" {
			t.Error("the draft produced rows")
		}
	}
}

// TestAReleaseWithNoAssetIsHarmless: a tag pushed while the build is still
// running looks exactly like this, and tomorrow's snapshot carries its binaries.
func TestAReleaseWithNoAssetIsHarmless(t *testing.T) {
	cfg := testConfig(t)
	bare := Release{TagName: "v3.0.0", PublishedAt: at("2026-01-02")}
	snap, err := Parse(cfg, []Release{bare, release("v1.0.0", "2026-01-01", 5, 0, 0, 0)}, "2026-01-02")
	if err != nil {
		t.Fatalf("a release still building must not fail the run: %v", err)
	}
	if got := snap.Total(); got != 5 {
		t.Errorf("total is %d, want 5", got)
	}
}

// TestAZeroIsAMeasurementAndIsKept: dropping the row would make tomorrow's
// delta against a non-zero impossible to compute.
func TestAZeroIsAMeasurementAndIsKept(t *testing.T) {
	cfg := testConfig(t)
	snap, err := Parse(cfg, []Release{release("v1.0.0", "2026-01-01", 0, 0, 0, 0)}, "2026-01-02")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Rows) != 4 {
		t.Fatalf("%d rows, want 4: a zero was dropped", len(snap.Rows))
	}
}

// TestNoReleaseAtAllIsRefused: the CSV is append-only, so an empty snapshot
// would be indistinguishable from a collapse forever after.
func TestNoReleaseAtAllIsRefused(t *testing.T) {
	cfg := testConfig(t)
	if _, err := Parse(cfg, nil, "2026-01-02"); err == nil {
		t.Fatal("an empty release list was accepted")
	}
}

// TestThePlatformComesFromTheNamedGroups proves the split is configuration,
// not code: a project naming its assets differently only changes its JSON.
func TestThePlatformComesFromTheNamedGroups(t *testing.T) {
	cfg := testConfig(t)
	snap, err := Parse(cfg, []Release{release("v1.0.0", "2026-01-01", 1, 2, 3, 4)}, "2026-01-02")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"feint-linux-amd64":  "linux/amd64",
		"feint-linux-arm64":  "linux/arm64",
		"feint-darwin-amd64": "darwin/amd64",
		"feint-darwin-arm64": "darwin/arm64",
	}
	for _, r := range snap.Rows {
		if got := r.OS + "/" + r.Arch; got != want[r.Asset] {
			t.Errorf("%s split as %s, want %s", r.Asset, got, want[r.Asset])
		}
	}
}

// TestACounterGoingBackwardsWarnsWithoutFailing. GitHub renumbers when an asset
// or a release is deleted and recreated, which this project's release procedure
// does on a botched tag. Failing would stop collection for good; ignoring would
// publish a delta that is silently wrong.
func TestACounterGoingBackwardsWarnsWithoutFailing(t *testing.T) {
	cfg := testConfig(t)
	before, err := Parse(cfg, []Release{release("v1.0.0", "2026-01-01", 100, 0, 0, 0)}, "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	after, err := Parse(cfg, []Release{release("v1.0.0", "2026-01-01", 40, 0, 0, 0)}, "2026-01-02")
	if err != nil {
		t.Fatal(err)
	}
	deltas := after.CompareWith(before)
	if len(after.Warnings) == 0 {
		t.Fatal("a counter that went from 100 to 40 produced no warning")
	}
	if !strings.Contains(after.Warnings[0], "100") || !strings.Contains(after.Warnings[0], "40") {
		t.Errorf("the warning does not carry both figures: %q", after.Warnings[0])
	}
	if d := deltas["v1.0.0\x00feint-linux-amd64"]; d != -60 {
		t.Errorf("delta is %d, want -60: the figure is reported as measured, not clamped", d)
	}
}

// TestADeletedReleaseLeavesTheHistoryAlone: a release that vanishes from the
// payload must not resurrect as a zero row, which would read as everybody
// un-downloading it.
func TestADeletedReleaseLeavesTheHistoryAlone(t *testing.T) {
	cfg := testConfig(t)
	before, err := Parse(cfg, []Release{
		release("v1.0.0", "2026-01-01", 100, 0, 0, 0),
		release("v1.0.1", "2026-01-01", 50, 0, 0, 0),
	}, "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	after, err := Parse(cfg, []Release{release("v1.0.0", "2026-01-01", 110, 0, 0, 0)}, "2026-01-02")
	if err != nil {
		t.Fatal(err)
	}
	after.CompareWith(before)
	for _, r := range after.Rows {
		if r.Version == "v1.0.1" {
			t.Error("the deleted release produced a row in the new snapshot")
		}
	}
	if len(after.Warnings) != 0 {
		t.Errorf("a deleted release produced a warning about a counter: %v", after.Warnings)
	}
}

// TestAConfigurationThatCountsNothingIsRefused: a stale binary_patterns would
// otherwise classify every asset as unknown, and an empty list would classify
// none at all.
func TestAConfigurationThatCountsNothingIsRefused(t *testing.T) {
	for _, c := range []Config{
		{Project: "p", Repository: "o/r"},
		{Repository: "o/r", BinaryPatterns: []string{"^x$"}},
		{Project: "p", BinaryPatterns: []string{"^x$"}},
		{Project: "p", Repository: "o/r", BinaryPatterns: []string{"^(unclosed"}},
	} {
		if _, err := c.compile(); err == nil {
			t.Errorf("%+v was accepted", c)
		}
	}
}
