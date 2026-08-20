package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/stephrobert/feint/internal/core/machine"
)

// `feint images` builds the machine images this emulator boots (#203).
//
// Why the emulator ships its own. No image on the upstream server carries an ssh
// daemon: measured on images:ubuntu/24.04, ubuntu/24.04/cloud, debian/12/cloud
// and alpine/3.21/cloud, each looked at twice — as early as the container
// answers and again after `cloud-init status --wait` — and all four answered
// ABSENT with nothing listening on port 22.
//
// So a machine built from an upstream image installs one at first boot, which
// needs outbound internet, which needs NAT, which is why every machine is put on
// a managed bridge — and that bridge is the second, unpublished address a
// Scaleway server carries here and does not carry on the real cloud. A real
// cloud image has the daemon in it. This makes ours look like one.
//
// Separate from `doctor` on purpose: doctor diagnoses and this acts. Building
// launches a container and takes minutes, which is a side effect on the
// operator's station, and this project asks before those rather than after.
func images(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("images")
	fs.SetOutput(stderr)
	vm := fs.String("vm", "auto", "machine runtime to build with: auto, incus, incus-vm, incus-ovn")
	only := fs.String("only", "", "build just this one, e.g. ubuntu/24.04")
	checkOnly := fs.Bool("check", false, "report what is missing and exit 2 if anything is, building nothing")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	driver, err := machineDriver(*vm, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return 1
	}
	if _, isNoop := driver.(machine.Noop); isNoop {
		fmt.Fprintln(stderr, "feint: no machine runtime answers, so there is nowhere to build an image")
		fmt.Fprintln(stderr, "       `feint doctor --vm incus` says what is missing")
		return 1
	}

	ctx := context.Background()
	inventory, err := machine.ImageInventory(ctx, driver)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return 1
	}

	if *checkOnly {
		return reportImages(inventory, *only, stdout)
	}

	builder, ok := driver.(interface {
		BuildImage(context.Context, machine.ImageSpec, io.Writer) error
	})
	if !ok {
		fmt.Fprintln(stderr, "feint: this runtime cannot build images")
		return 1
	}

	built, skipped := 0, 0
	for _, status := range inventory {
		if *only != "" && *only != status.Spec.Name {
			continue
		}
		if status.Present() {
			fmt.Fprintf(stdout, "== %s already built (%s)\n", status.Spec.Alias(), short(status.Fingerprint))
			skipped++
			continue
		}
		fmt.Fprintf(stdout, "== %s\n", status.Spec.Alias())
		if err := builder.BuildImage(ctx, status.Spec, stdout); err != nil {
			fmt.Fprintf(stderr, "feint: %s: %v\n", status.Spec.Name, err)
			return 1
		}
		built++
	}

	if built == 0 && skipped == 0 {
		fmt.Fprintf(stderr, "feint: no image is called %q\n", *only)
		return 1
	}
	fmt.Fprintf(stdout, "\n%d built, %d already present\n", built, skipped)
	return 0
}

// reportImages prints the inventory and answers 2 when something is missing, the
// exit code every other gate in this project uses for "the world moved and
// nobody triaged it".
func reportImages(inventory []machine.ImageStatus, only string, stdout io.Writer) int {
	missing := 0
	for _, status := range inventory {
		if only != "" && only != status.Spec.Name {
			continue
		}
		if status.Present() {
			fmt.Fprintf(stdout, "  ok       %-16s %s\n", status.Spec.Name, short(status.Fingerprint))
			continue
		}
		fmt.Fprintf(stdout, "  missing  %-16s from %s\n", status.Spec.Name, status.Spec.Source)
		missing++
	}
	if missing > 0 {
		fmt.Fprintf(stdout, "\n%d missing; `feint images` builds them\n", missing)
		return 2
	}
	fmt.Fprintln(stdout, "\nevery machine image is present")
	return 0
}

func short(fingerprint string) string {
	if len(fingerprint) > 12 {
		return fingerprint[:12]
	}
	return fingerprint
}
