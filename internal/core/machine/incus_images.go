package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// builderPrefix marks the instance an image build runs in.
//
// Under the emulator's own machine prefix on purpose: a build interrupted
// halfway leaves it behind, and `feint clean` sweeps what carries the prefix, so
// the leftover is removed by the tool the operator already runs rather than by
// remembering it exists.
const builderPrefix = "feint-imagebuild"

// builderName is the instance one build runs in: the prefix, the image it is
// building, and the process building it.
//
// It used to be the single constant "feint-imagebuild", for every image and
// every process, and that made the builder a shared object with no owner.
// BuildImage force-deletes the builder on the way in and on the way out, so two
// builds meeting on that name delete and publish each other's container.
//
// Measured on 2026-08-25, both halves in the same minute: `feint serve --vm
// incus-ovn` building ubuntu/24.04 on the boot path (#392 put a second caller
// on this recipe) and a `feint images` started by hand in another terminal.
// The first died on `apt-get update … exit status 137` — SIGKILL, the container
// removed from under the exec — and the second on `incus publish: lstat
// …/rootfs/usr/lib/x86_64-linux-gnu/libsmartcols.so.1.1.0: no such file or
// directory`, a file that had just been deleted under the publish. Neither run
// could have told you what hit it.
//
// A Go lock closes the case inside one process (BuildIfMissing) and cannot
// close it across two, which is exactly the pair that failed. The name does:
// two processes now build in two containers, and two different images build in
// parallel by decision rather than by accident of a shared name.
//
// TestOneBuilderPerImageAndPerProcess fails without this.
func builderName(spec ImageSpec) string {
	slug := strings.NewReplacer("/", "-", ".", "-", ":", "-").Replace(spec.Name)
	return builderPrefix + "-" + slug + "-" + strconv.Itoa(os.Getpid())
}

// LocalImages implements ImageLister.
//
// Only aliases under the emulator's own prefix are reported. An operator's own
// images are none of this code's business, and reporting them would be the first
// step towards deleting one.
//
// JSON and not the csv column, and that is a measurement, not a taste: `incus
// image list -c l` truncates a second alias into "feint/debian/12 (1 more)",
// csv format included. Parsed from that column, an image carrying two aliases
// reported a name nothing publishes, its real aliases vanished, and `feint
// images --check` called a present image missing — the instrument lying before
// its subject, caught on 2026-08-25 by planting a second alias on a held
// image. TestLocalImagesSurviveASecondAlias fails against the csv version.
func (d *Incus) LocalImages(ctx context.Context) (map[string]string, error) {
	out, err := d.run(ctx, "image", "list", ImagePrefix+"/", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("list local images: %w", err)
	}
	var images []struct {
		Fingerprint string `json:"fingerprint"`
		Aliases     []struct {
			Name string `json:"name"`
		} `json:"aliases"`
	}
	if err := json.Unmarshal(out, &images); err != nil {
		return nil, fmt.Errorf("read the image list: %w", err)
	}
	held := map[string]string{}
	for _, image := range images {
		for _, alias := range image.Aliases {
			if strings.HasPrefix(alias.Name, ImagePrefix+"/") {
				held[alias.Name] = image.Fingerprint
			}
		}
	}
	return held, nil
}

// BuildImage produces one image and publishes it under its alias.
//
// It launches the upstream image, installs the ssh daemon, strips what must not
// travel, and publishes. Minutes, and a container running on the operator's
// host, which is why nothing calls this without being asked: `feint doctor`
// reports what is missing and names the command, it never builds.
//
// The builder is removed whether the build succeeded or not. A half-built
// instance left running is the failure mode that makes people distrust a tool
// that touches their machine.
func (d *Incus) BuildImage(ctx context.Context, spec ImageSpec, progress io.Writer) error {
	say := func(format string, args ...any) {
		if progress != nil {
			fmt.Fprintf(progress, "  "+format+"\n", args...)
		}
	}

	builder := builderName(spec)
	_ = d.removeBuilder(ctx, builder)
	defer func() { _ = d.removeBuilder(ctx, builder) }()

	say("launching %s", spec.Source)
	// Labelled like every other object this driver creates, so `feint clean`
	// sweeps a builder an interrupted run left behind. An unlabelled leftover is
	// one this emulator would refuse to touch, which is the right rule applied
	// to the wrong object.
	if _, err := d.run(ctx, "launch", spec.Source, builder,
		"--config", "user."+LabelKey+"=imagebuild"); err != nil {
		return fmt.Errorf("launch %s: %w", spec.Source, err)
	}
	if err := d.waitForBuilder(ctx, builder); err != nil {
		return err
	}

	say("installing %s and enabling %s", spec.Package, spec.Service)
	commands, err := installCommands(spec)
	if err != nil {
		return err
	}
	for _, command := range commands {
		if err := d.execInBuilder(ctx, builder, command); err != nil {
			return err
		}
	}

	say("removing host keys, machine id and cloud-init state")
	for _, command := range generaliseCommands() {
		// Best effort: a distribution without dbus has no dbus machine id to
		// remove, and failing there would refuse an image for a file that was
		// never supposed to exist.
		_ = d.execInBuilder(ctx, builder, command)
	}

	say("publishing %s", spec.Alias())
	if _, err := d.run(ctx, "stop", builder); err != nil {
		return fmt.Errorf("stop the builder: %w", err)
	}
	// A previous alias would make `publish` fail rather than replace, and a
	// rebuild is the ordinary way to pick up a security update. Best effort on
	// purpose: the first build of an image has nothing to delete, and Incus
	// answers "Image not found".
	//
	// That phrase is deliberately absent from isNotFound, and adding it there was
	// the wrong fix: TestIsNotFoundStaysNarrow exists because a message about one
	// kind of object passing for another is how a Remove once reported success and
	// left an instance running. An image is not an instance. So the tolerance
	// lives here, where it is scoped to one call whose failure cannot hide
	// anything: a genuine conflict surfaces at publish, loudly.
	_, _ = d.run(ctx, "image", "delete", spec.Alias())
	if _, err := d.run(ctx, "publish", builder, "--alias", spec.Alias()); err != nil {
		return fmt.Errorf("publish %s: %w", spec.Alias(), err)
	}
	return nil
}

// waitForBuilder blocks until the builder answers and cloud-init has finished,
// because installing a package while cloud-init is still holding the package
// manager is a race that fails intermittently — the worst kind.
func (d *Incus) waitForBuilder(ctx context.Context, builder string) error {
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := d.run(ctx, "exec", builder, "--", "true"); err == nil {
			// cloud-init is absent on some images and that is not an error.
			_, _ = d.run(ctx, "exec", builder, "--", "cloud-init", "status", "--wait")
			return d.waitForBuilderAddress(ctx, builder, deadline)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("the build instance never answered")
}

// waitForBuilderAddress waits for the thing the next command actually needs.
//
// Answering `exec -- true` proves an init is up. It does not prove the instance
// has an address, and the very next thing this build does is fetch a package
// over the network. The two were treated as one, and on 2026-08-28 the whole
// nightly runtime proof died on the difference, on both legs, before a suite
// ran:
//
//	apk add --no-cache openssh in the build instance:
//	  fetching https://dl-cdn.alpinelinux.org/alpine/v3.21/main: Permission denied
//
// Alpine is the one that shows it because Alpine is the one that boots in about
// a second: `exec -- true` answers almost at once, its cloud image carries no
// cloud-init so the wait above is a no-op, and apk runs before DHCP has handed
// out a lease. AlmaLinux, built in the same pass and from the same bridge,
// succeeded every time — systemd and cloud-init take long enough that the lease
// is there by the time dnf runs. What made a five-year-old race visible was a
// runner image that jumped three weeks and changed the timings; the race was
// always there.
//
// So this waits on the observable condition rather than on a proxy for it,
// which is #459's rule applied one layer down: the machine carries a global
// address, asked of the machine itself. A build instance that never gets one is
// refused by name, because a build that proceeds without an address fails later
// and blames the package repository.
//
// TestTheBuilderIsNotDeclaredReadyWithoutAnAddress fails without it.
func (d *Incus) waitForBuilderAddress(ctx context.Context, builder string, deadline time.Time) error {
	for time.Now().Before(deadline) {
		out, err := d.run(ctx, "exec", builder, "--",
			"ip", "-4", "-o", "addr", "show", "scope", "global")
		if err == nil && strings.TrimSpace(string(out)) != "" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("the build instance never carried a global address, so the " +
		"package fetch below would fail on the network rather than on the package")
}

// execInBuilder runs one step of a build inside the builder instance.
//
// Under the build cap and not the control cap (#641). `dnf install -y -q
// openssh-server` on almalinux/9 was killed at the control cap's 120 s on
// 2026-09-03 and took the nightly runtime proof with it; the night before, the
// same command had finished just under it. A package install waits on a mirror,
// which is not the thing 120 seconds was chosen to bound.
//
// TestABuildStepRunsUnderTheBuildCapAndNotTheControlCap fails without this.
func (d *Incus) execInBuilder(ctx context.Context, builder string, command []string) error {
	args := append([]string{"exec", builder, "--"}, command...)
	if _, err := d.runWithin(ctx, d.buildTimeout(), args...); err != nil {
		return fmt.Errorf("%s in the build instance: %w", strings.Join(command, " "), err)
	}
	return nil
}

func (d *Incus) removeBuilder(ctx context.Context, builder string) error {
	_, err := d.run(ctx, "delete", "--force", builder)
	if err != nil && !isNotFound(err) {
		return err
	}
	return nil
}

// RemoveImage deletes one image this emulator published, named by its
// family/version half ("fedora/44") — the explicit, asked-for removal that
// `feint clean` deliberately is not: clean removes what a killed run left
// half-alive, an image is the cache that spares the next run its minutes of
// build, and a sweep that deleted it would punish whoever runs clean between
// two tries.
//
// Two questions, both answered here at the destructive choke point. Ownership:
// the alias is built from ImagePrefix, the mark BuildImage itself writes, so
// an operator's image cannot even be spelled through this call — the same rule
// Binding.ours and mustOwn apply to machines and networks. Syntax: each half
// obeys the same charset a ref does, so no name reaches argv as a flag or a
// path. A name that fails either is refused out loud, never skipped.
//
// TestImageRemovalStaysInsideThePrefix fails without the guards.
func (d *Incus) RemoveImage(ctx context.Context, name string) error {
	family, version, found := strings.Cut(name, "/")
	if !found || !refToken(family) || !refToken(version) || strings.Contains(version, "/") {
		return fmt.Errorf("%q does not name an image of this emulator: want <family>/<version>", name)
	}
	alias := ImagePrefix + "/" + name
	if _, err := d.run(ctx, "image", "delete", alias); err != nil {
		return fmt.Errorf("remove %s: %w", alias, err)
	}
	return nil
}
