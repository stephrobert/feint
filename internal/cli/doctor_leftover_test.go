package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pointTempDirAt makes os.TempDir() answer root for the test, or skips.
func pointTempDirAt(t *testing.T, root string) {
	t.Helper()
	t.Setenv("TMPDIR", root)
	if os.TempDir() != root {
		t.Skipf("this platform does not take TMPDIR, so the scan cannot be pointed at a fixture")
	}
}

// TestDoctorIgnoresARunDirectoryItDidNotMake is cited by
// checkLeftoverRunAuthorities. /tmp is anybody's: a name read there becomes a
// command the reader pastes and a path --fix removes. Each planted directory
// below passes every question but one, and the dead owner makes it a leftover
// the report would otherwise print in its `rm -rf` line.
func TestDoctorIgnoresARunDirectoryItDidNotMake(t *testing.T) {
	cases := []struct {
		name  string
		plant func(t *testing.T, root string) string // returns the planted name
	}{
		{"a name that ends a command and starts another", func(t *testing.T, root string) string {
			n := runDirPrefix + "1;touch${IFS}PWNED;#"
			writeRunDir(t, root, n, deadPID(t))
			return n
		}},
		{"a command substitution", func(t *testing.T, root string) string {
			n := runDirPrefix + "$(id>&2)"
			writeRunDir(t, root, n, deadPID(t))
			return n
		}},
		{"an escape sequence for the terminal", func(t *testing.T, root string) string {
			n := runDirPrefix + "1\x1b[2J\r"
			writeRunDir(t, root, n, deadPID(t))
			return n
		}},
		{"a link to a directory that would pass", func(t *testing.T, root string) string {
			target := filepath.Join(root, "elsewhere")
			writeRunDir(t, root, "elsewhere", deadPID(t))
			n := runDirPrefix + "4040404"
			if err := os.Symlink(target, filepath.Join(root, n)); err != nil {
				t.Fatalf("symlink: %v", err)
			}
			return n
		}},
		{"a directory others can enter", func(t *testing.T, root string) string {
			n := runDirPrefix + "5050505"
			writeRunDir(t, root, n, deadPID(t))
			if err := os.Chmod(filepath.Join(root, n), 0o755); err != nil { //nolint:gosec // the fixture is the wrong mode on purpose
				t.Fatalf("chmod: %v", err)
			}
			return n
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			pointTempDirAt(t, root)
			planted := c.plant(t, root)
			got := checkLeftoverRunAuthorities()
			text := got.title + got.detail + strings.Join(got.commands, "\n") + strings.Join(got.paths, "\n")
			if got.state != verdictOK || strings.Contains(text, planted) || len(got.commands) != 0 {
				t.Fatalf("a directory feint did not make reached the report: %+v", got)
			}
		})
	}

	// The accepting half: the same dead owner in a directory of the exact
	// shape is reported, and its path is the one in the command.
	root := t.TempDir()
	pointTempDirAt(t, root)
	writeRunDir(t, root, runDirPrefix+"6060606", deadPID(t))
	got := checkLeftoverRunAuthorities()
	want := "rm -rf " + filepath.Join(root, runDirPrefix+"6060606")
	if got.state != verdictWarn || len(got.commands) != 1 || got.commands[0] != want {
		t.Fatalf("a real leftover is not reported with its exact path (want %q): %+v", want, got)
	}
}

// The owner question cannot be planted without root, so it is asked of
// runDirIsOurs directly, with the reason, for each refusal and the acceptance.
func TestRunDirIsOursSaysWhyItRefuses(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, runDirPrefix+"7070707")
	writeRunDir(t, root, filepath.Base(good), 0)
	if err := runDirIsOurs(good, os.Getuid()); err != nil {
		t.Fatalf("a directory feint run would make is refused: %v", err)
	}
	if err := runDirIsOurs(good, os.Getuid()+1); err == nil || !strings.Contains(err.Error(), "does not belong to you") {
		t.Fatalf("another user's directory is accepted, or for another reason: %v", err)
	}
	odd := filepath.Join(root, runDirPrefix+"7x")
	writeRunDir(t, root, filepath.Base(odd), 0)
	if err := runDirIsOurs(odd, os.Getuid()); err == nil || !strings.Contains(err.Error(), "not a name feint run gives") {
		t.Fatalf("a name MkdirTemp never gives is accepted, or for another reason: %v", err)
	}
}

// The second line of defence: whatever reaches a printed command is one shell
// word. A TMPDIR with a quote in it is legal and must not split the command.
func TestThePrintedRemoveCommandIsQuoted(t *testing.T) {
	root := filepath.Join(t.TempDir(), "it's here")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pointTempDirAt(t, root)
	writeRunDir(t, root, runDirPrefix+"8080808", deadPID(t))
	got := checkLeftoverRunAuthorities()
	want := "rm -rf '" + strings.ReplaceAll(filepath.Join(root, runDirPrefix+"8080808"), "'", `'\''`) + "'"
	if len(got.commands) != 1 || got.commands[0] != want {
		t.Fatalf("the remove command is %q, want %q", got.commands, want)
	}
	if plain := shellQuote("/tmp/feint-run-1"); plain != "/tmp/feint-run-1" {
		t.Fatalf("a plain path was quoted: %q", plain)
	}
}

// TestFixCountsARecreatedNameAsRemoved is cited by buildFixPlan's re-check:
// a name recreated after the removal, not ours, is not a failure of --fix.
// The accepting half: a directory that is still ours after the step fails it.
func TestFixCountsARecreatedNameAsRemoved(t *testing.T) {
	for _, c := range []struct {
		name     string
		recreate func(path string) error
		code     int
	}{
		{"recreated by someone else", func(path string) error { return os.Mkdir(path, 0o755) }, exitOK},
		{"still ours", func(path string) error { return os.Mkdir(path, 0o700) }, exitError},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, runDirPrefix+"3030303")
			writeRunDir(t, root, filepath.Base(path), 0)
			deps := (&fixWorld{s: readyStation()}).deps()
			deps.remove = func(p string) error {
				if err := os.RemoveAll(p); err != nil {
					return err
				}
				return c.recreate(p)
			}
			plan := buildFixPlan([]check{{id: "leftovers", state: verdictWarn, fixKey: "leftovers", paths: []string{path}}}, deps, false)
			var out bytes.Buffer
			code := runFix(context.Background(), &out, strings.NewReader(""), false, true, doctorView{mode: "incus-ovn"}, plan, deps)
			if code != c.code || (c.code == exitError && !strings.Contains(out.String(), "is still there")) {
				t.Fatalf("want exit %d (exit %d):\n%s", c.code, code, out.String())
			}
		})
	}
}

// TestTheLeftoverNotesComeBeforeTheCommandSentence is cited by
// checkLeftoverRunAuthorities: the sentence ending in "safe:" leads into the
// command, so nothing comes between them.
func TestTheLeftoverNotesComeBeforeTheCommandSentence(t *testing.T) {
	for _, c := range []struct {
		live int
		want string
	}{
		{1, "1 more belongs to a run still going, and is left alone."},
		{2, "2 more belong to a run still going, and are left alone."},
	} {
		root := t.TempDir()
		pointTempDirAt(t, root)
		writeRunDir(t, root, runDirPrefix+"2020202", deadPID(t))
		for i := 0; i < c.live; i++ {
			writeRunDir(t, root, fmt.Sprintf("%s%d", runDirPrefix, 2020210+i), os.Getpid())
		}
		got := checkLeftoverRunAuthorities()
		note, lead := strings.Index(got.detail, c.want), strings.Index(got.detail, "removing them is safe:")
		if note < 0 || lead < note || strings.Count(got.detail, "removing them is safe:") != 1 || !strings.HasSuffix(got.detail, "so removing them is safe:") {
			t.Errorf("%d live: want %q, before the sentence that leads into the command: %q", c.live, c.want, got.detail)
		}
	}
	// The sibling note agrees in number too.
	if one, many := moreOf(1, "has", "have"), moreOf(3, "has", "have"); one != "1 more has" || many != "3 more have" {
		t.Errorf("moreOf: %q, %q", one, many)
	}
}

// TestFixRemovesOnlyARunDirectoryThatIsStillOurs is cited by buildFixPlan's
// removal step: the directory is asked again at the moment of removal, since
// /tmp can change between the report and the yes.
func TestFixRemovesOnlyARunDirectoryThatIsStillOurs(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(t *testing.T, path string, deps *fixDeps)
		reason string // "" when the removal must happen
	}{
		{"unchanged", func(*testing.T, string, *fixDeps) {}, ""},
		{"swapped for a link", func(t *testing.T, path string, _ *fixDeps) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), path); err != nil {
				t.Fatal(err)
			}
		}, "is not a directory"},
		{"opened to others", func(t *testing.T, path string, _ *fixDeps) {
			if err := os.Chmod(path, 0o755); err != nil { //nolint:gosec // the wrong mode on purpose
				t.Fatal(err)
			}
		}, "has mode 755"},
		{"another user's", func(_ *testing.T, _ string, deps *fixDeps) { deps.uid++ }, "does not belong to you"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, runDirPrefix+"9090909")
			writeRunDir(t, root, filepath.Base(path), 0)
			var removed []string
			deps := (&fixWorld{s: readyStation()}).deps()
			deps.remove = func(p string) error { removed = append(removed, p); return os.RemoveAll(p) }
			leftover := check{id: "leftovers", state: verdictWarn, fixKey: "leftovers", paths: []string{path}}
			c.change(t, path, &deps)
			plan := buildFixPlan([]check{leftover}, deps, false)
			var out bytes.Buffer
			code := runFix(context.Background(), &out, strings.NewReader(""), false, true, doctorView{mode: "incus-ovn"}, plan, deps)
			if c.reason == "" {
				if code != exitOK || len(removed) != 1 {
					t.Fatalf("a directory still ours was not removed (exit %d, %q):\n%s", code, removed, out.String())
				}
				return
			}
			if code != exitError || len(removed) != 0 || !strings.Contains(out.String(), c.reason) {
				t.Fatalf("want a refusal naming %q and nothing removed (exit %d, %q):\n%s", c.reason, code, removed, out.String())
			}
		})
	}
}
