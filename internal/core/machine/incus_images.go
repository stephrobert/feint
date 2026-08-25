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
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("the build instance never answered")
}

func (d *Incus) execInBuilder(ctx context.Context, builder string, command []string) error {
	args := append([]string{"exec", builder, "--"}, command...)
	if _, err := d.run(ctx, args...); err != nil {
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
