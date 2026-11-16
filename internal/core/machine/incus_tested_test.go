package machine

import "testing"

// TestIncusTestedIsARelease holds the one constant three consumers read
// (#979). A malformed value would render "0.0 recommended" on the install page
// and pin a version apt cannot find; the refusal here names it first.
func TestIncusTestedIsARelease(t *testing.T) {
	if got := IncusTestedSeries(); got == [2]int{} {
		t.Fatalf("IncusTested = %q is not a dotted release: the install page would recommend 0.0", IncusTested)
	}
	// The tested release cannot sit below the floor: a pin under IncusMinimum
	// would prove the runtime on a version the diagnostics call unsupported.
	series := IncusTestedSeries()
	if olderThan([3]int{series[0], series[1], 99}, IncusMinimum) {
		t.Errorf("IncusTested %s is below the floor %s", IncusTested, VersionText(IncusMinimum[:]))
	}
}

// One case per question seriesOf asks: is it dotted, is it whole, is it
// nothing but the version. Each refused case passes the other questions.
func TestSeriesOfReadsOnlyAWholeRelease(t *testing.T) {
	cases := []struct {
		in   string
		want [2]int
	}{
		{"7.6", [2]int{7, 6}},         // accepting half: the form Zabbly uses
		{"7.5.1", [2]int{7, 5}},       // accepting half: a point release keeps its series
		{"7", [2]int{}},               // no minor
		{"v7.6", [2]int{}},            // a tag, not the package's upstream part
		{"7.6 ", [2]int{}},            // trailing space: apt would match nothing
		{"7.6-ubuntu24.04", [2]int{}}, // the Debian revision belongs to the channel, not the pin
		{"", [2]int{}},                // empty: never a version
	}
	for _, c := range cases {
		if got := seriesOf(c.in); got != c.want {
			t.Errorf("seriesOf(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
