package machine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
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
			return []byte(`[{"fingerprint":"abc123def456","aliases":[{"name":"` + held.Alias() + `"}]}]`), nil
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
			return []byte(`[{"fingerprint":"deadbeef00","aliases":[{"name":"production-base"}]},` +
				`{"fingerprint":"cafebabe11","aliases":[{"name":"` + ImagePrefix + `/ubuntu/24.04"}]}]`), nil
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
				out.WriteString("[")
				for i, alias := range aliases {
					if i > 0 {
						out.WriteString(",")
					}
					out.WriteString(`{"fingerprint":"fingerprint","aliases":[{"name":"` + alias + `"}]}`)
				}
				out.WriteString("]")
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

func TestSpecForDerivesEveryVersionOfAKnownFamily(t *testing.T) {
	cases := []struct {
		ref     string
		name    string
		source  string
		manager string
		service string
	}{
		// The measured hole of #465: the catalogue promises debian_trixie.
		{"debian:13", "debian/13", "images:debian/13/cloud", "apt", "ssh"},
		// A family the fixed table never carried; triplet rehearsed 2026-08-25.
		{"fedora:44", "fedora/44", "images:fedora/44/cloud", "dnf", "sshd"},
		// The version half must travel verbatim, capitals included.
		{"centos:9-Stream", "centos/9-Stream", "images:centos/9-Stream/cloud", "dnf", "sshd"},
		{"alpine:3.22", "alpine/3.22", "images:alpine/3.22/cloud", "apk", "sshd"},
		{"ubuntu:26.04", "ubuntu/26.04", "images:ubuntu/26.04/cloud", "apt", "ssh"},
		{"rockylinux:9", "rockylinux/9", "images:rockylinux/9/cloud", "dnf", "sshd"},
	}
	for _, c := range cases {
		spec, ok := SpecFor(c.ref)
		if !ok {
			t.Errorf("SpecFor(%q) derived nothing for a family the table holds", c.ref)
			continue
		}
		if spec.Name != c.name || spec.Source != c.source || spec.Manager != c.manager || spec.Service != c.service {
			t.Errorf("SpecFor(%q) = %+v, want name=%s source=%s manager=%s service=%s",
				c.ref, spec, c.name, c.source, c.manager, c.service)
		}
		if spec.Package == "" {
			t.Errorf("SpecFor(%q) carries no ssh package, so the build would produce the very image it replaces", c.ref)
		}
	}
}

func TestSpecForRefusesWhatNoRecipeCovers(t *testing.T) {
	refused := []string{
		"plan9:4",                // no recipe for the family: the guard of #465
		"talos:1.7",              // no ssh package to install; outside the form
		"",                       //
		"debian",                 // no version at all
		"debian:",                // empty version
		":13",                    // empty family
		"images:debian/13/cloud", // an explicit runtime ref is the caller's own image
		"debian:-13",             // a version that would reach argv looking like a flag
		"debian:13 evil",         // charset: whitespace never travels into a command
		"debian:13;true",         //
		"Debian:13",              // families are table keys, exactly
	}
	for _, ref := range refused {
		if spec, ok := SpecFor(ref); ok {
			t.Errorf("SpecFor(%q) = %+v, want a refusal", ref, spec)
		}
	}
}

func TestRequiredImagesDeriveFromTheFamilyTable(t *testing.T) {
	specs := RequiredImages()
	if len(specs) != 6 {
		t.Fatalf("%d warm-up rows, want 6: a ref outside the family table was silently dropped", len(specs))
	}
	seen := map[string]bool{}
	for _, spec := range specs {
		seen[spec.Name] = true
		ref := strings.Replace(spec.Name, "/", ":", 1)
		derived, ok := SpecFor(ref)
		if !ok || derived != spec {
			t.Errorf("warm-up row %s does not derive from the family table (got %+v, ok=%v): two spellings of one recipe", spec.Name, derived, ok)
		}
	}
	// The user-visible half of #465: the Scaleway catalogue serves
	// debian_trixie mapped on debian:13, so the warm-up set must hold it.
	if !seen["debian/13"] {
		t.Error("debian/13 is not in the warm-up set while the catalogue promises debian_trixie")
	}
}

func TestParseDeclaredImagesRefusesWhatItCannotBuild(t *testing.T) {
	t.Run("a valid declaration parses, login included", func(t *testing.T) {
		got, err := ParseDeclaredImages(" ami-a3ca408c=ubuntu:22.04, tmpl-1=fedora:44@fedora ,")
		if err != nil {
			t.Fatalf("refused a valid declaration: %v", err)
		}
		want := map[string]Image{
			"ami-a3ca408c": {Ref: "ubuntu:22.04"},
			"tmpl-1":       {Ref: "fedora:44", User: "fedora"},
		}
		if len(got) != len(want) || got["ami-a3ca408c"] != want["ami-a3ca408c"] || got["tmpl-1"] != want["tmpl-1"] {
			t.Fatalf("parsed %+v, want %+v", got, want)
		}
	})

	t.Run("nothing declared is nil, not empty", func(t *testing.T) {
		if got, err := ParseDeclaredImages("  "); err != nil || got != nil {
			t.Fatalf("got %v, %v for an empty declaration", got, err)
		}
	})

	t.Run("a bad entry refuses the whole set and says how to fix it", func(t *testing.T) {
		cases := map[string]string{
			"ami-x=plan9:4":                   "ubuntu", // the fix names the families a recipe exists for
			"garbage":                         "family", // the fix quotes the syntax
			"ami-x=debian:13,ami-x=debian:12": "twice",  // a duplicate is a typo, not a choice
			"=debian:12":                      "family", //
			"ami-x=debian:-13":                "family", // the ref guard holds here too
		}
		for entry, needle := range cases {
			got, err := ParseDeclaredImages(entry)
			if err == nil {
				t.Errorf("ParseDeclaredImages(%q) = %v, want a refusal", entry, got)
				continue
			}
			if !strings.Contains(err.Error(), needle) {
				t.Errorf("ParseDeclaredImages(%q) error %q does not carry %q, so the reader cannot act on it", entry, err, needle)
			}
		}
	})
}

func TestDerivedImagesAreNamedBesideTheWarmupSet(t *testing.T) {
	driver := &buildingDriver{held: map[string]string{
		"feint/ubuntu/24.04": "warmup-row",   // named by the set: not derived
		"feint/fedora/44":    "derived-here", // a boot derived it (#465)
		"feint/plan9/4":      "lost-family",  // under the prefix, no recipe: still named
		"operator/own":       "not-ours",     // outside the prefix: none of our business
	}}

	derived, err := DerivedImages(context.Background(), driver)
	if err != nil {
		t.Fatalf("DerivedImages: %v", err)
	}
	if len(derived) != 2 || derived[0].Spec.Name != "fedora/44" || derived[1].Spec.Name != "plan9/4" {
		t.Fatalf("derived %+v, want exactly fedora/44 and plan9/4: a warm-up row is not derived, an operator's image is not ours, and a prefix image with no recipe must not vanish", derived)
	}
	if derived[0].Spec.Source == "" || derived[0].Spec.Manager != "dnf" {
		t.Errorf("fedora/44 lost its derived recipe: %+v", derived[0].Spec)
	}
	if !derived[1].Present() {
		t.Error("a held image reports absent")
	}

	// A driver that cannot be asked holds nothing to report — never an error,
	// the same rule CapabilitiesOf applies.
	if got, err := DerivedImages(context.Background(), &recordingDriver{}); err != nil || got != nil {
		t.Fatalf("got %v, %v from a driver with no lister", got, err)
	}
}

// The instrument before the subject: `incus image list -c l` truncates a second
// alias into "feint/debian/12 (1 more)", csv included, and the csv parser
// turned that into a name nothing publishes while the real aliases vanished —
// `feint images --check` called a present image missing. Caught on 2026-08-25
// by planting a second alias on a held image; the fix is asking for JSON,
// which carries every alias whole.
func TestLocalImagesSurviveASecondAlias(t *testing.T) {
	var asked []string
	d := NewIncus()
	d.runner = func(_ context.Context, args ...string) ([]byte, error) {
		asked = append(asked, strings.Join(args, " "))
		return []byte(`[{"fingerprint":"7bf813f9e3ea","aliases":[` +
			`{"name":"` + ImagePrefix + `/debian/12"},{"name":"` + ImagePrefix + `/fedora/44"}]}]`), nil
	}

	held, err := d.LocalImages(context.Background())
	if err != nil {
		t.Fatalf("LocalImages: %v", err)
	}
	for _, alias := range []string{ImagePrefix + "/debian/12", ImagePrefix + "/fedora/44"} {
		if held[alias] != "7bf813f9e3ea" {
			t.Errorf("alias %s vanished from the listing: %v", alias, held)
		}
	}
	for alias := range held {
		if strings.Contains(alias, " ") {
			t.Errorf("held a truncation artefact as an alias: %q", alias)
		}
	}
	if len(asked) == 0 || !strings.Contains(asked[0], "--format json") {
		t.Fatalf("the listing did not ask for JSON, so a second alias will truncate: %v", asked)
	}
}

// The builder is ready when it carries an address, not when its init answers.
//
// `exec -- true` proves an init is up; the very next thing BuildImage does is
// fetch a package over the network. Treating the two as one killed the whole
// nightly runtime proof on 2026-08-28, on both legs, before a suite ran — apk
// on Alpine, whose cloud image carries no cloud-init and which boots in about a
// second, so the fetch beat DHCP to it. AlmaLinux in the same pass, from the
// same bridge, succeeded every time.
//
// The control that makes this test mean something is the second half: a builder
// that answers WITH an address must not be waited on at all, or the test would
// pass over a wait that always blocks.
func TestTheBuilderIsNotDeclaredReadyWithoutAnAddress(t *testing.T) {
	addressAsked := 0
	d := NewIncus()
	d.runner = func(_ context.Context, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "-- true"):
			return nil, nil
		case strings.Contains(joined, "cloud-init"):
			return nil, errors.New("cloud-init: not found")
		case strings.Contains(joined, "ip -4 -o addr show scope global"):
			addressAsked++
			return nil, nil // up, and holding no address
		}
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	err := d.waitForBuilder(ctx, "feint-build-x")
	if err == nil {
		t.Fatal("a builder with no global address was declared ready, so the package " +
			"fetch that follows would fail on the network and blame the repository")
	}
	if addressAsked == 0 {
		t.Fatal("the address was never asked for: this test would pass over a wait " +
			"that returns early for any other reason")
	}

	// The control: the same builder, carrying an address, is ready at once.
	d2 := NewIncus()
	d2.runner = func(_ context.Context, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "cloud-init"):
			return nil, errors.New("cloud-init: not found")
		case strings.Contains(joined, "ip -4 -o addr show scope global"):
			return []byte("2: eth0    inet 10.248.68.10/24 scope global eth0\n"), nil
		}
		return nil, nil
	}
	if err := d2.waitForBuilder(context.Background(), "feint-build-x"); err != nil {
		t.Fatalf("a builder holding 10.248.68.10 was refused: %v", err)
	}
}
