package machine

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// The machine images feint boots, and why it builds its own (#203).
//
// No image on the upstream server carries an ssh daemon. Measured on
// images:ubuntu/24.04, ubuntu/24.04/cloud, debian/12/cloud and
// alpine/3.21/cloud, each looked at twice — as early as the container answers
// and again after `cloud-init status --wait` returns, so a daemon installed at
// boot would still have been caught. All four answered ABSENT, with nothing
// listening on port 22.
//
// So this emulator installs one at first boot, in
// internal/core/cloudinit/templates/*.yaml.tmpl. That install is what needs
// outbound internet; outbound is what needs NAT; NAT is why every machine is put
// on a managed bridge; and that bridge is the second, unpublished address a
// Scaleway server carries here and does not carry on the real cloud (#202).
//
// A real cloud image has the daemon in it — Scaleway does not apt-install
// openssh when a server boots. Building one is therefore the faithful shape
// rather than a workaround, and #202 cannot close honestly without it.
//
// TestEveryRequiredImageDeclaresItsSource fails without this table.

// ImagePrefix is the alias namespace every image this emulator builds lives
// under. It is also what makes an image ours: nothing outside this prefix is
// ever deleted or rebuilt, the same rule Binding.ours and mustOwn apply to
// machines and networks.
const ImagePrefix = "feint"

// ImageSpec is one image to build: where it comes from, and what has to be added
// to it. Every field is data rather than a branch, so a fifth family is a row
// and not a code change.
type ImageSpec struct {
	// Name is the family and version, e.g. "ubuntu/24.04". The alias is
	// ImagePrefix + "/" + Name.
	Name string
	// Source is the upstream image to start from.
	Source string
	// Package carries the ssh daemon, under the name this family's package
	// manager knows it by — "openssh" on Alpine, "openssh-server" elsewhere.
	Package string
	// Service is what enables it at boot: "ssh" on Debian and Ubuntu, "sshd"
	// on Alpine and the RHEL family.
	Service string
	// Manager is the package manager: apt, apk or dnf.
	Manager string
}

// Alias is the local image alias this spec publishes to.
func (s ImageSpec) Alias() string { return ImagePrefix + "/" + s.Name }

// RequiredImages is the set feint needs to boot machines.
//
// One row per cloud-init template family, and that is not a coincidence: an
// image set narrower than internal/core/cloudinit/templates/ makes the templates
// lie about distributions nobody can actually boot.
func RequiredImages() []ImageSpec {
	return []ImageSpec{
		{Name: "ubuntu/24.04", Source: "images:ubuntu/24.04/cloud", Package: "openssh-server", Service: "ssh", Manager: "apt"},
		{Name: "ubuntu/22.04", Source: "images:ubuntu/22.04/cloud", Package: "openssh-server", Service: "ssh", Manager: "apt"},
		{Name: "debian/12", Source: "images:debian/12/cloud", Package: "openssh-server", Service: "ssh", Manager: "apt"},
		{Name: "alpine/3.21", Source: "images:alpine/3.21/cloud", Package: "openssh", Service: "sshd", Manager: "apk"},
		{Name: "almalinux/9", Source: "images:almalinux/9/cloud", Package: "openssh-server", Service: "sshd", Manager: "dnf"},
	}
}

// ImageStatus is what the host holds for one required image.
type ImageStatus struct {
	Spec ImageSpec
	// Fingerprint is what the local store answers, empty when the image is not
	// there. It is reported rather than checked against a recorded value: an
	// image built on two machines from the same source is not bit-identical, so
	// pinning the fingerprint would fail for a reason that has nothing to do
	// with the image being right.
	Fingerprint string
}

// Present reports whether the image is in the local store.
func (s ImageStatus) Present() bool { return s.Fingerprint != "" }

// ImageLister is the part of a driver that can answer what images the host
// holds. Optional, like Capable: a driver that cannot answer counts as holding
// none, so the check reports work to do rather than asserting nothing is
// missing.
type ImageLister interface {
	// LocalImages answers the alias to fingerprint mapping of every image the
	// host holds under the emulator's own prefix.
	LocalImages(ctx context.Context) (map[string]string, error)
}

// ImageInventory answers the status of every required image, sorted by name so
// two runs report the same order.
//
// A driver that does not implement ImageLister yields every image as missing,
// never as present: an undeclared property counts as absent, which is the same
// rule CapabilitiesOf applies. Reporting "nothing is missing" because nobody
// could look is the failure this project exists to remove.
func ImageInventory(ctx context.Context, driver Driver) ([]ImageStatus, error) {
	specs := RequiredImages()
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })

	held := map[string]string{}
	if lister, ok := driver.(ImageLister); ok {
		var err error
		held, err = lister.LocalImages(ctx)
		if err != nil {
			return nil, err
		}
	}

	out := make([]ImageStatus, 0, len(specs))
	for _, spec := range specs {
		out = append(out, ImageStatus{Spec: spec, Fingerprint: held[spec.Alias()]})
	}
	return out, nil
}

// MissingImages is the subset of the inventory that is not on the host.
func MissingImages(inventory []ImageStatus) []ImageSpec {
	var out []ImageSpec
	for _, status := range inventory {
		if !status.Present() {
			out = append(out, status.Spec)
		}
	}
	return out
}

// ImageNames joins the names of a spec list, for a one-line diagnostic.
func ImageNames(specs []ImageSpec) string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	return strings.Join(names, ", ")
}

// installCommands is what turns the upstream image into ours, per package
// manager. Data rather than a switch inside the build loop, so a family whose
// manager is new is a row here and the build stays one shape.
func installCommands(spec ImageSpec) ([][]string, error) {
	switch spec.Manager {
	case "apt":
		return [][]string{
			{"sh", "-c", "DEBIAN_FRONTEND=noninteractive apt-get update -qq"},
			{"sh", "-c", "DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends " + spec.Package},
			{"systemctl", "enable", spec.Service},
			{"sh", "-c", "apt-get clean && rm -rf /var/lib/apt/lists/*"},
		}, nil
	case "apk":
		return [][]string{
			{"apk", "add", "--no-cache", spec.Package},
			{"rc-update", "add", spec.Service, "default"},
		}, nil
	case "dnf":
		return [][]string{
			{"sh", "-c", "dnf install -y -q " + spec.Package},
			{"systemctl", "enable", spec.Service},
			{"sh", "-c", "dnf clean all"},
		}, nil
	}
	return nil, fmt.Errorf("no install recipe for package manager %q", spec.Manager)
}

// generaliseCommands strip what must never travel inside an image.
//
// Host keys above all. Baked in, every machine this emulator ever boots would
// present the same fingerprint, and a user's known_hosts would accept any of
// them for any other — a machine identity that is not an identity. cloud-init
// regenerates them when they are absent, which is why they are removed rather
// than left to be overwritten.
//
// TestTheBuildRemovesWhatMustNotTravelInAnImage holds the recipe here;
// tools/images/verify.sh holds the property itself, by booting two machines from
// one image and requiring two different host keys. The second is the one that
// matters and it cannot live in a unit test: nothing here can boot a machine.
func generaliseCommands() [][]string {
	return [][]string{
		{"sh", "-c", "rm -f /etc/ssh/ssh_host_*"},
		{"sh", "-c", ": > /etc/machine-id"},
		{"sh", "-c", "rm -f /var/lib/dbus/machine-id || true"},
		// cloud-init must run again from scratch on machines built from this
		// image, or the build's own first boot is the only one it ever has.
		{"sh", "-c", "cloud-init clean --logs --seed 2>/dev/null || true"},
		{"sh", "-c", "rm -rf /var/log/* /tmp/* 2>/dev/null || true"},
	}
}
