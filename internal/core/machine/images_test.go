package machine

import (
	"context"
	"strings"
	"testing"
)

// The image table is data, and a row that names no source or no package is a
// row that fails at build time on the operator's station rather than here.
func TestEveryRequiredImageDeclaresItsSource(t *testing.T) {
	seen := map[string]bool{}
	for _, spec := range RequiredImages() {
		if spec.Name == "" || spec.Source == "" || spec.Package == "" || spec.Service == "" {
			t.Errorf("incomplete image spec: %+v", spec)
		}
		if seen[spec.Name] {
			t.Errorf("%s is declared twice", spec.Name)
		}
		seen[spec.Name] = true
		if !strings.HasPrefix(spec.Alias(), ImagePrefix+"/") {
			t.Errorf("%s publishes outside the emulator's own prefix: %s", spec.Name, spec.Alias())
		}
		if _, err := installCommands(spec); err != nil {
			t.Errorf("%s: %v", spec.Name, err)
		}
	}
}

// Every cloud-init template family must have an image, or the template promises
// a distribution nobody can boot.
//
// The list is spelled here rather than read from the templates directory on
// purpose: the point is that the two sets agree, and a check that derives one
// from the other agrees with itself.
func TestEveryCloudInitFamilyHasAnImage(t *testing.T) {
	families := map[string]bool{}
	for _, spec := range RequiredImages() {
		families[strings.SplitN(spec.Name, "/", 2)[0]] = true
	}
	for _, want := range []string{"almalinux", "alpine", "debian", "ubuntu"} {
		if !families[want] {
			t.Errorf("internal/core/cloudinit/templates/%s.yaml.tmpl exists and no image does", want)
		}
	}
}

// A driver that cannot answer must yield every image as missing, never as
// present.
//
// This is the same rule CapabilitiesOf applies to an undeclared capability, and
// it matters more here than it looks: the opposite default would report "nothing
// is missing" because nobody could look, which is the exact shape of claim this
// project exists to remove.
func TestADriverThatCannotListImagesReportsThemAllMissing(t *testing.T) {
	inventory, err := ImageInventory(context.Background(), Noop{})
	if err != nil {
		t.Fatalf("inventory of a silent driver: %v", err)
	}
	if len(inventory) != len(RequiredImages()) {
		t.Fatalf("inventory covers %d image(s), the table declares %d", len(inventory), len(RequiredImages()))
	}
	missing := MissingImages(inventory)
	if len(missing) != len(RequiredImages()) {
		t.Errorf("a driver that answers nothing reported %d missing of %d; silence must not read as present",
			len(missing), len(RequiredImages()))
	}
}

// The accepting half: an image the host holds is reported present, with its
// fingerprint, and one it does not is not.
//
// Both halves, because a check that reported everything missing would pass the
// test above and make `feint images` rebuild what already exists on every run.
func TestAnImageTheHostHoldsIsReportedPresent(t *testing.T) {
	specs := RequiredImages()
	if len(specs) < 2 {
		t.Skip("the table needs two rows for this to mean anything")
	}
	held := specs[0]

	d := NewIncus()
	d.runner = func(_ context.Context, args ...string) ([]byte, error) {
		if strings.HasPrefix(strings.Join(args, " "), "image list") {
			return []byte(held.Alias() + ",abc123def456\n"), nil
		}
		return nil, nil
	}

	inventory, err := ImageInventory(context.Background(), d)
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	for _, status := range inventory {
		switch status.Spec.Name {
		case held.Name:
			if !status.Present() {
				t.Errorf("%s is on the host and was reported missing", held.Name)
			}
			if status.Fingerprint != "abc123def456" {
				t.Errorf("fingerprint %q, want abc123def456", status.Fingerprint)
			}
		default:
			if status.Present() {
				t.Errorf("%s is not on the host and was reported present", status.Spec.Name)
			}
		}
	}
	if names := ImageNames(MissingImages(inventory)); strings.Contains(names, held.Name) {
		t.Errorf("the missing list names an image the host holds: %s", names)
	}
}

// Only the emulator's own images are ever reported.
//
// An operator's images are none of this code's business, and a list that
// included them would be the first step towards a rebuild deleting one — the
// same boundary mustOwn draws for instances and networks.
func TestAnOperatorsOwnImagesAreNotReported(t *testing.T) {
	d := NewIncus()
	d.runner = func(_ context.Context, args ...string) ([]byte, error) {
		if strings.HasPrefix(strings.Join(args, " "), "image list") {
			return []byte("production-base,deadbeef00\n" + ImagePrefix + "/ubuntu/24.04,cafebabe11\n"), nil
		}
		return nil, nil
	}
	held, err := d.LocalImages(context.Background())
	if err != nil {
		t.Fatalf("LocalImages: %v", err)
	}
	if _, found := held["production-base"]; found {
		t.Error("an image outside the emulator's prefix was reported as ours")
	}
	if held[ImagePrefix+"/ubuntu/24.04"] != "cafebabe11" {
		t.Errorf("our own image was not reported: %v", held)
	}
}

// The image must not carry host keys, and the build must say so by removing
// them.
//
// Baked in, every machine this emulator boots would present one fingerprint and
// a user's known_hosts would accept any of them for any other. cloud-init
// regenerates them when absent, which is why they are removed rather than left
// to be overwritten. tools/images/verify.sh holds the same property against two
// real machines; this holds it against the recipe.
func TestTheBuildRemovesWhatMustNotTravelInAnImage(t *testing.T) {
	joined := ""
	for _, command := range generaliseCommands() {
		joined += strings.Join(command, " ") + "\n"
	}
	for _, want := range []string{"/etc/ssh/ssh_host_", "/etc/machine-id", "cloud-init clean"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the build keeps %s in the image", want)
		}
	}
}

// The image the emulator built is preferred over the upstream one, and the
// fallback is announced rather than silent.
//
// This is what makes #203 reach the machines: building images changes nothing
// until the driver boots them. The accepting half and the refusing half are both
// here, because a resolver that always returned the upstream ref would pass a
// test that only checked the fallback, and one that always returned ours would
// break every host that has not run `feint images`.
func TestTheBuiltImageIsPreferredWhenTheHostHoldsIt(t *testing.T) {
	withImages := func(aliases ...string) *Incus {
		d := NewIncus()
		d.runner = func(_ context.Context, args ...string) ([]byte, error) {
			if strings.HasPrefix(strings.Join(args, " "), "image list") {
				var out strings.Builder
				for _, alias := range aliases {
					out.WriteString(alias + ",fingerprint\n")
				}
				return []byte(out.String()), nil
			}
			return nil, nil
		}
		return d
	}

	ours := ImagePrefix + "/ubuntu/24.04"
	if got := withImages(ours).resolveImage(context.Background(), "ubuntu:24.04"); got != ours {
		t.Errorf("the host holds %s and the driver booted %s", ours, got)
	}

	// Nothing built: the upstream image, so a first contact still works.
	if got := withImages().resolveImage(context.Background(), "ubuntu:24.04"); got != "images:ubuntu/24.04/cloud" {
		t.Errorf("with no image built the driver must fall back upstream, got %s", got)
	}

	// A different version must not borrow another's image.
	if got := withImages(ours).resolveImage(context.Background(), "ubuntu:22.04"); got != "images:ubuntu/22.04/cloud" {
		t.Errorf("ubuntu:22.04 resolved to %s while only 24.04 is built", got)
	}

	// An explicit Incus reference is the caller naming an image; it is honoured.
	if got := withImages(ours).resolveImage(context.Background(), "images:debian/13"); got != "images:debian/13" {
		t.Errorf("an explicit reference was rewritten to %s", got)
	}
}
