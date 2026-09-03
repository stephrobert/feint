package machine

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Two builds must never work in one container, and the proof has to be at the
// level of the commands: the failure it prevents is invisible above argv.
//
// Measured on 2026-08-25, when #392 put a second caller on this recipe: a
// `feint serve --vm incus-ovn` building ubuntu/24.04 on the boot path and a
// `feint images` in another terminal, both on the fixed-name builder. One died
// on `apt-get update … exit status 137` — its container removed under the exec
// — and the other on `incus publish: lstat …/libsmartcols.so.1.1.0: no such
// file or directory`, a file deleted under the publish.
//
// Two halves, because they close two different cases: the lock covers one
// process (BuildIfMissing), and the name covers two.

// buildRecorder is an injected runner: it records argv, answers the calls
// BuildImage makes, and can hold the first build open so a second one has to
// meet it.
type buildRecorder struct {
	mu    sync.Mutex
	calls [][]string
	// published is the alias store the recorded `image list` answers from, so a
	// second caller sees what the first one built.
	published map[string]bool
	// entered is signalled when a launch is seen; release holds that launch
	// until the test lets go.
	entered chan string
	release chan struct{}
}

func newBuildRecorder() *buildRecorder {
	return &buildRecorder{
		published: map[string]bool{},
		entered:   make(chan string, 8),
		release:   make(chan struct{}),
	}
}

func (r *buildRecorder) run(_ context.Context, args ...string) ([]byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string(nil), args...))
	published := make([]string, 0, len(r.published))
	for alias := range r.published {
		published = append(published, alias)
	}
	r.mu.Unlock()

	switch {
	// A real builder carries an address, and BuildImage now waits for one
	// before it fetches a package (#583). A recorder that answered nothing here
	// would make every build in this file wait out its three-minute deadline —
	// which is what it did for one run, and is why this case says what it
	// stands for rather than returning a bare string.
	case len(args) > 3 && args[0] == "exec" && args[3] == "ip":
		return []byte("2: eth0    inet 10.248.68.10/24 scope global eth0\n"), nil
	case len(args) > 1 && args[0] == "image" && args[1] == "list":
		out := "["
		for i, alias := range published {
			if i > 0 {
				out += ","
			}
			out += `{"fingerprint":"fingerprint","aliases":[{"name":"` + alias + `"}]}`
		}
		return []byte(out + "]"), nil
	case args[0] == "launch":
		r.entered <- args[2]
		<-r.release
		return nil, nil
	case args[0] == "publish":
		r.mu.Lock()
		r.published[args[len(args)-1]] = true
		r.mu.Unlock()
		return nil, nil
	}
	return nil, nil
}

func (r *buildRecorder) launched() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, call := range r.calls {
		if call[0] == "launch" {
			out = append(out, call[2])
		}
	}
	return out
}

func TestOneBuilderPerImageAndPerProcess(t *testing.T) {
	t.Run("one build at a time per image", func(t *testing.T) {
		rec := newBuildRecorder()
		d := &Incus{runner: rec.run}
		spec := RequiredImages()[0]

		done := make(chan bool, 2)
		build := func() {
			made, err := BuildIfMissing(context.Background(), d, spec, nil)
			if err != nil {
				t.Errorf("build: %v", err)
			}
			done <- made
		}
		go build()
		<-rec.entered // the first caller holds the container
		go build()

		// The witness, and it has to be this shape. Releasing the first caller
		// as soon as the second is launched proves nothing: the second would
		// then find the image published and skip, lock or no lock, and the
		// falsification said so — the mutation that removes the lock left this
		// test green on its first form. So the assertion is about what reaches
		// argv **while the first caller is still inside the recipe**: with the
		// lock, the second emits nothing at all; without it, a second builder
		// is launched within microseconds.
		for i := 0; i < 60; i++ {
			if len(rec.launched()) > 1 {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if names := rec.launched(); len(names) != 1 {
			t.Fatalf("launched %v while the first build was still running, want the second caller queued", names)
		}
		close(rec.release)

		built := 0
		for i := 0; i < 2; i++ {
			if <-done {
				built++
			}
		}
		if built != 1 {
			t.Fatalf("%d of the two callers built the image, want exactly 1", built)
		}
		if names := rec.launched(); len(names) != 1 {
			t.Fatalf("launched %v, want one builder for two concurrent callers", names)
		}
	})

	t.Run("the container is named for its image and its process", func(t *testing.T) {
		// The accepting half, and the cross-process one: two different images
		// build in two different containers by decision, and another process
		// building the same image never lands on this one's name.
		first, second := builderName(RequiredImages()[0]), builderName(RequiredImages()[1])
		if first == second {
			t.Fatalf("two images share the builder %q, so they cannot build side by side", first)
		}
		for _, name := range []string{first, second} {
			if !strings.HasPrefix(name, builderPrefix+"-") {
				t.Errorf("%q is outside the sweep's prefix, so a leftover would be nobody's", name)
			}
			if !strings.HasSuffix(name, "-"+strconv.Itoa(os.Getpid())) {
				t.Errorf("%q does not name this process, so two runs share one container", name)
			}
			if strings.ContainsAny(name, "/:. ") {
				t.Errorf("%q is not a name a runtime will take", name)
			}
		}
	})

	t.Run("an image the station already holds is not rebuilt", func(t *testing.T) {
		rec := newBuildRecorder()
		close(rec.release)
		spec := RequiredImages()[0]
		rec.published[spec.Alias()] = true
		d := &Incus{runner: rec.run}

		made, err := BuildIfMissing(context.Background(), d, spec, nil)
		if err != nil || made {
			t.Fatalf("made=%v err=%v, want no build for an image that is already there", made, err)
		}
		if names := rec.launched(); len(names) != 0 {
			t.Fatalf("launched %v for an image the station holds", names)
		}
	})
}

// The destructive half of the image lifecycle answers both questions at its
// own choke point: a name outside <family>/<version> shape never reaches argv,
// and the alias is always built from the emulator's own prefix, so an
// operator's image cannot even be spelled. Asserted at the argv level with the
// injected runner, because that is where the damage would happen.
func TestImageRemovalStaysInsideThePrefix(t *testing.T) {
	rec := newBuildRecorder()
	close(rec.release)
	d := &Incus{runner: rec.run}

	// The refusing half: not one of these may emit a command.
	for _, name := range []string{"", "fedora", "a/b/c", "../etc", "-rf/whatever", "fedora/44 x", "fedora/-44"} {
		if err := d.RemoveImage(context.Background(), name); err == nil {
			t.Errorf("RemoveImage(%q) did not refuse", name)
		}
	}
	for _, call := range rec.calls {
		t.Errorf("a refused name still reached the runtime: %v", call)
	}

	// The accepting half: the emulator's own image is removable, and the argv
	// names the prefixed alias and nothing else.
	if err := d.RemoveImage(context.Background(), "fedora/44"); err != nil {
		t.Fatalf("RemoveImage(fedora/44): %v", err)
	}
	want := []string{"image", "delete", ImagePrefix + "/fedora/44"}
	if len(rec.calls) != 1 || strings.Join(rec.calls[0], " ") != strings.Join(want, " ") {
		t.Fatalf("argv %v, want exactly %v", rec.calls, want)
	}
}

// deadlineRecorder answers like buildRecorder and, for each call, keeps how long
// the caller gave it. That is the only way this property is observable: a test
// cannot wait out a ten-minute cap, but it can read the deadline the call runs
// under and say which cap set it.
type deadlineRecorder struct {
	mu    sync.Mutex
	calls []deadlineCall
}

type deadlineCall struct {
	args []string
	left time.Duration
	set  bool
}

func (r *deadlineRecorder) run(ctx context.Context, args ...string) ([]byte, error) {
	call := deadlineCall{args: append([]string(nil), args...)}
	if deadline, ok := ctx.Deadline(); ok {
		call.left, call.set = time.Until(deadline), true
	}
	r.mu.Lock()
	r.calls = append(r.calls, call)
	r.mu.Unlock()

	switch {
	case len(args) > 3 && args[0] == "exec" && args[3] == "ip":
		return []byte("2: eth0    inet 10.248.68.10/24 scope global eth0\n"), nil
	case len(args) > 1 && args[0] == "image" && args[1] == "list":
		return []byte("[]"), nil
	}
	return nil, nil
}

// find answers the first call whose argv joins to something containing want.
func (r *deadlineRecorder) find(want string) (deadlineCall, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, call := range r.calls {
		if strings.Contains(strings.Join(call.args, " "), want) {
			return call, true
		}
	}
	return deadlineCall{}, false
}

// A command run inside the build instance gets the build cap, and a control
// command gets the control cap (#641).
//
// The defect this holds: every `incus` call went through one 120 s cap, chosen
// for control commands — a control plane slower than that is broken, not busy.
// `incus exec <builder> -- dnf install -y -q openssh-server` on almalinux/9 went
// through it too and was killed at exactly that limit on 2026-09-03, taking the
// whole nightly runtime proof with it. The night before, the same command had
// finished just under. An intermittent red whose cause is a fixed limit applied
// to the wrong kind of work.
//
// Both halves are asserted. A guard that gave everything ten minutes would pass
// the first and break what the control cap is for.
func TestABuildStepRunsUnderTheBuildCapAndNotTheControlCap(t *testing.T) {
	rec := &deadlineRecorder{}
	d := &Incus{runner: rec.run}
	spec := RequiredImages()[0]

	if _, err := BuildIfMissing(context.Background(), d, spec, nil); err != nil {
		t.Fatalf("build: %v", err)
	}

	install, ok := rec.find(spec.Package)
	if !ok {
		t.Fatalf("no call installed %s: %v", spec.Package, rec.calls)
	}
	if !install.set {
		t.Fatal("the install ran with no deadline at all, so nothing bounds a wedged mirror")
	}
	if install.left <= 2*time.Minute {
		t.Errorf("the install had %s, which is the control cap: a package install waits on a mirror, "+
			"and 120 s is what killed the nightly proof", install.left.Round(time.Second))
	}

	// The control cap still binds what it was chosen for.
	launch, ok := rec.find("launch")
	if !ok {
		t.Fatalf("no launch call: %v", rec.calls)
	}
	if !launch.set {
		t.Fatal("a control command ran with no deadline")
	}
	if launch.left > 3*time.Minute {
		t.Errorf("a control command had %s: the build cap leaked onto the control path, and a "+
			"daemon that never answers would hang for minutes", launch.left.Round(time.Second))
	}
}
