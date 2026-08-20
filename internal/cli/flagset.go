package cli

import "flag"

// Every verb builds its flags here rather than calling flag.NewFlagSet itself,
// and the reason is a gate that could not see what it was freezing (#334).
//
// The frozen CLI surface (#132) is what a pipeline outside this repository is
// allowed to key on. Until this seam existed, the test that freezes it built its
// observation by parsing the rendered `feint --help` — so the surface recorded
// what the help *claimed*, not what the binary *accepted*. `feint proxy
// --intercept` shipped in v0.9.0 accepted by the binary, named by
// `feint proxy --help`, and absent from the frozen surface, for six days,
// because no line of the general help mentioned it.
//
// The missing flag was the cheap half. The expensive half is the other
// direction: a flag deleted from a FlagSet while its line in the help survived
// would have kept the gate green, and that is the direction that breaks a
// consumer. A surface frozen against a rendered document freezes the document.
//
// So the observation is taken from the FlagSet itself, by flag.FlagSet.VisitAll,
// which means the test needs a way to reach the FlagSet a verb builds. That is
// all this file is: one constructor, and one seam the test arms.
//
// The tests that fail without it: TestTheFrozenSurfacesStillMatchTheirFixture
// on the "cli" surface (which now compares FlagSets against the fixture, and so
// goes red on a flag added *or removed*), and
// TestTheHelpNamesEveryFlagTheBinaryAccepts (which is the help's own,
// separate assertion, with a subject of its own).
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	if observeFlagSet != nil {
		observeFlagSet(name, fs)
	}
	return fs
}

// observeFlagSet is nil in every running binary: nothing in Run ever sets it,
// so the production path is one nil check.
//
// The frozen-surface test sets it, drives every dispatched verb as far as its
// flag parsing, and reads the FlagSets it collected. It is handed the set before
// the verb registers anything on it, which is deliberate — the pointer is what
// matters, and it is read after the verb has returned, by which time every
// fs.String/Bool/Int/Duration call has landed on it. There is no second copy to
// keep in step, which is the whole point.
var observeFlagSet func(name string, fs *flag.FlagSet)
