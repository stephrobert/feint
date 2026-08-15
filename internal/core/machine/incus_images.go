package machine

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strings"
	"time"
)

// builderName is the instance an image build runs in.
//
// Under the emulator's own machine prefix on purpose: a build interrupted
// halfway leaves it behind, and `feint clean` sweeps what carries the prefix, so
// the leftover is removed by the tool the operator already runs rather than by
// remembering it exists.
const builderName = "feint-imagebuild"

// LocalImages implements ImageLister.
//
// Only aliases under the emulator's own prefix are reported. An operator's own
// images are none of this code's business, and reporting them would be the first
// step towards deleting one.
func (d *Incus) LocalImages(ctx context.Context) (map[string]string, error) {
	out, err := d.run(ctx, "image", "list", ImagePrefix+"/", "-f", "csv", "-c", "lf")
	if err != nil {
		return nil, fmt.Errorf("list local images: %w", err)
	}
	held := map[string]string{}
	reader := csv.NewReader(strings.NewReader(string(out)))
	reader.FieldsPerRecord = -1
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read the image list: %w", err)
		}
		if len(row) < 2 {
			continue
		}
		// One image can carry several aliases; the column is comma-joined
		// inside a quoted field, so each is taken on its own.
		for _, alias := range strings.Split(row[0], ",") {
			alias = strings.TrimSpace(alias)
			if strings.HasPrefix(alias, ImagePrefix+"/") {
				held[alias] = strings.TrimSpace(row[1])
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

	_ = d.removeBuilder(ctx)
	defer func() { _ = d.removeBuilder(ctx) }()

	say("launching %s", spec.Source)
	// Labelled like every other object this driver creates, so `feint clean`
	// sweeps a builder an interrupted run left behind. An unlabelled leftover is
	// one this emulator would refuse to touch, which is the right rule applied
	// to the wrong object.
	if _, err := d.run(ctx, "launch", spec.Source, builderName,
		"--config", "user."+LabelKey+"=imagebuild"); err != nil {
		return fmt.Errorf("launch %s: %w", spec.Source, err)
	}
	if err := d.waitForBuilder(ctx); err != nil {
		return err
	}

	say("installing %s and enabling %s", spec.Package, spec.Service)
	commands, err := installCommands(spec)
	if err != nil {
		return err
	}
	for _, command := range commands {
		if err := d.execInBuilder(ctx, command); err != nil {
			return err
		}
	}

	say("removing host keys, machine id and cloud-init state")
	for _, command := range generaliseCommands() {
		// Best effort: a distribution without dbus has no dbus machine id to
		// remove, and failing there would refuse an image for a file that was
		// never supposed to exist.
		_ = d.execInBuilder(ctx, command)
	}

	say("publishing %s", spec.Alias())
	if _, err := d.run(ctx, "stop", builderName); err != nil {
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
	if _, err := d.run(ctx, "publish", builderName, "--alias", spec.Alias()); err != nil {
		return fmt.Errorf("publish %s: %w", spec.Alias(), err)
	}
	return nil
}

// waitForBuilder blocks until the builder answers and cloud-init has finished,
// because installing a package while cloud-init is still holding the package
// manager is a race that fails intermittently — the worst kind.
func (d *Incus) waitForBuilder(ctx context.Context) error {
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := d.run(ctx, "exec", builderName, "--", "true"); err == nil {
			// cloud-init is absent on some images and that is not an error.
			_, _ = d.run(ctx, "exec", builderName, "--", "cloud-init", "status", "--wait")
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

func (d *Incus) execInBuilder(ctx context.Context, command []string) error {
	args := append([]string{"exec", builderName, "--"}, command...)
	if _, err := d.run(ctx, args...); err != nil {
		return fmt.Errorf("%s in the build instance: %w", strings.Join(command, " "), err)
	}
	return nil
}

func (d *Incus) removeBuilder(ctx context.Context) error {
	_, err := d.run(ctx, "delete", "--force", builderName)
	if err != nil && !isNotFound(err) {
		return err
	}
	return nil
}
