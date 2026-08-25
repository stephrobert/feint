package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

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
	// `feint images resolve <id>...` is the lookup half of the boot refusal
	// (#465): it asks the providers' public listings what an opaque identifier
	// names, and prints the FEINT_BOOT_IMAGES declaration to make of it. A
	// subcommand rather than a flag because it takes identifiers, not specs,
	// and shares nothing with the build loop below but the family table.
	if len(args) > 0 && args[0] == "resolve" {
		return imagesResolve(args[1:], stdout, stderr)
	}
	// `feint images remove <family/version>...` is the explicit, targeted
	// removal that `feint clean` deliberately is not: an image is the cache
	// that spares the next run its minutes of build, never the leftover of a
	// killed run, so it goes only when somebody names it. The driver refuses
	// anything the emulator did not publish — the alias is spelled from the
	// prefix, so an operator's image cannot even be named here.
	if len(args) > 0 && args[0] == "remove" {
		return imagesRemove(args[1:], stdout, stderr)
	}
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
	// The station question beside the warm-up question (#465): a boot that
	// derived an image put it under the prefix without any warm-up row naming
	// it, and an inventory that only answered the list would leave it
	// invisible on the operator's own machine.
	derived, err := machine.DerivedImages(ctx, driver)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return 1
	}

	if *checkOnly {
		return reportImages(inventory, derived, *only, stdout)
	}

	// Asked once, up front, so a runtime that cannot build says so before the
	// first image rather than in the middle of the set. The build itself goes
	// through machine.BuildIfMissing below, which is the seam the boot
	// path (machine.EnsureImage) drives too.
	if _, ok := driver.(machine.ImageBuilder); !ok {
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
		// Through the shared seam rather than straight at the driver: it holds
		// the per-image exclusion and the second look, so this command and a boot
		// building on the fly (#465) cannot build the same image at once — two
		// callers, one lock, written once. Measured on 2026-08-25: two builds
		// that met on the old fixed-name builder killed each other, one on
		// `apt-get update … 137`, the other on a publish whose rootfs moved.
		made, err := machine.BuildIfMissing(ctx, driver, status.Spec, stdout)
		if err != nil {
			fmt.Fprintf(stderr, "feint: %s: %v\n", status.Spec.Name, err)
			return 1
		}
		if !made {
			// Another run built it while this one waited. Said rather than
			// counted as a build: the operator asked and something else
			// answered.
			fmt.Fprintf(stdout, "  built by another run while this one waited\n")
			skipped++
			continue
		}
		built++
	}

	if built == 0 && skipped == 0 {
		// Outside the warm-up set the recipe can still derive it (#465):
		// `feint images --only fedora/44` builds the row the set does not
		// carry, which is also what the boot-failure log tells an operator to
		// run. A name no family covers is refused with the families that are.
		spec, ok := machine.SpecFor(strings.Replace(*only, "/", ":", 1))
		if !ok {
			fmt.Fprintf(stderr, "feint: no recipe derives %q (families: %s)\n",
				*only, strings.Join(machine.Families(), ", "))
			return 1
		}
		fmt.Fprintf(stdout, "== %s (outside the warm-up set, derived from the family table)\n", spec.Alias())
		made, err := machine.BuildIfMissing(ctx, driver, spec, stdout)
		if err != nil {
			fmt.Fprintf(stderr, "feint: %s: %v\n", spec.Name, err)
			return 1
		}
		if made {
			built++
		} else {
			fmt.Fprintf(stdout, "  already on the station\n")
			skipped++
		}
	}
	fmt.Fprintf(stdout, "\n%d built, %d already present\n", built, skipped)
	reportDerived(derived, stdout)
	return 0
}

// reportImages prints the inventory and answers 2 when something is missing, the
// exit code every other gate in this project uses for "the world moved and
// nobody triaged it".
//
// Two questions, kept apart on purpose (#465): what the warm-up set requires
// and lacks — that is what the exit code grades — and what this emulator has
// put on this station, which includes the images a boot derived. A derived
// image is neither missing nor wrong, so it never moves the exit code; it is
// named, because an image an inventory cannot name is a silent residue on
// somebody else's machine.
func reportImages(inventory, derived []machine.ImageStatus, only string, stdout io.Writer) int {
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
	if only == "" {
		reportDerived(derived, stdout)
	}
	if missing > 0 {
		fmt.Fprintf(stdout, "\n%d missing; `feint images` builds them\n", missing)
		return 2
	}
	fmt.Fprintln(stdout, "\nevery machine image is present")
	return 0
}

// reportDerived names what the station holds under the prefix beyond the
// warm-up set, with the removal gesture: `feint clean` deliberately removes no
// image — an image is the asset that spares the next run its minutes of build,
// not the leftover of a killed run — so this line is the only place a derived
// image is ever seen.
func reportDerived(derived []machine.ImageStatus, stdout io.Writer) {
	for _, status := range derived {
		fmt.Fprintf(stdout, "  derived  %-16s %s (built on demand; `incus image delete %s` removes it)\n",
			status.Spec.Name, short(status.Fingerprint), machine.ImagePrefix+"/"+status.Spec.Name)
	}
}

// imagesRemove deletes images this emulator published, each named by its
// family/version half. It says what it is about to remove — alias and
// fingerprint — before removing it, and refuses a name the station does not
// hold under the prefix rather than silently ignoring it.
func imagesRemove(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("images remove")
	fs.SetOutput(stderr)
	vm := fs.String("vm", "auto", "machine runtime holding the images: auto, incus, incus-vm, incus-ovn")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	names := fs.Args()
	if len(names) == 0 {
		fmt.Fprintln(stderr, "usage: feint images remove <family/version>...")
		fmt.Fprintln(stderr, "  removes an image this emulator published; `feint images --check` lists them")
		return 1
	}

	driver, err := machineDriver(*vm, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return 1
	}
	remover, ok := driver.(interface {
		RemoveImage(context.Context, string) error
	})
	if !ok {
		fmt.Fprintln(stderr, "feint: this runtime holds no images to remove")
		return 1
	}
	ctx := context.Background()
	held := map[string]string{}
	if lister, canAsk := driver.(machine.ImageLister); canAsk {
		if listed, err := lister.LocalImages(ctx); err == nil {
			held = listed
		}
	}

	for _, name := range names {
		alias := machine.ImagePrefix + "/" + name
		fingerprint, present := held[alias]
		if !present {
			// Refused out loud, never skipped: a removal that ignores an
			// unknown name teaches the operator nothing about their typo.
			fmt.Fprintf(stderr, "feint: this emulator published no image called %q; `feint images --check` lists what it did\n", name)
			return 1
		}
		fmt.Fprintf(stdout, "removing %s (%s)\n", alias, short(fingerprint))
		if err := remover.RemoveImage(ctx, name); err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return 1
		}
	}
	return 0
}

func short(fingerprint string) string {
	if len(fingerprint) > 12 {
		return fingerprint[:12]
	}
	return fingerprint
}
