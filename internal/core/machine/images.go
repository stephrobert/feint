package machine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
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

// ImageFamily is what every version of one operating-system family shares: the
// package that carries the ssh daemon, the service that enables it at boot, and
// the package manager that installs it. The version is deliberately absent — it
// is the part that varies per boot, and SpecFor passes it through untouched.
type ImageFamily struct {
	Package string
	Service string
	Manager string
}

// imageFamilies is the recipe table, keyed by the family half of a Ref (#465).
//
// The irregular part of the recipe is per family, never per version: the source
// is always images:<family>/<version>/cloud, and the triplet below is the whole
// difference between building a Debian and building an Alpine. Enumerating
// versions here was the defect #465 names — the Scaleway catalogue served
// debian_trixie mapped on debian:13 while the table stopped at debian/12, so
// the label the emulator itself publishes booted a machine without ssh.
//
// Every triplet is verified by execution, not assumed: ubuntu/debian/alpine/
// almalinux by the existing builds, debian/13 and fedora/44 by rehearsals on
// 2026-08-25 (each command rc=0, `images-inventaire.md` §3). rockylinux and
// centos publish on images: and carry the dnf family's packaging; their first
// build is their proof, and a failure there is loud, not silent.
//
// A family absent from this table derives nothing, on purpose: deriving a spec
// for plan9:4 would produce a build that cannot work. The guard keeps the
// announced fallback of resolveImage for unknown families, and
// TestSpecForRefusesWhatNoRecipeCovers fails without it.
func imageFamilies() map[string]ImageFamily {
	apt := ImageFamily{Package: "openssh-server", Service: "ssh", Manager: "apt"}
	dnf := ImageFamily{Package: "openssh-server", Service: "sshd", Manager: "dnf"}
	return map[string]ImageFamily{
		"ubuntu":     apt,
		"debian":     apt,
		"alpine":     {Package: "openssh", Service: "sshd", Manager: "apk"},
		"almalinux":  dnf,
		"rockylinux": dnf,
		"centos":     dnf,
		"fedora":     dnf,
	}
}

// Families lists the family keys a recipe exists for, sorted so the refusal
// message that quotes them reads the same on every run.
func Families() []string {
	out := make([]string, 0, len(imageFamilies()))
	for family := range imageFamilies() {
		out = append(out, family)
	}
	sort.Strings(out)
	return out
}

// splitRef takes a runtime-agnostic Ref apart and answers false for anything
// that is not a well-formed family:version pair. An explicit runtime reference
// ("images:debian/13/cloud") is the caller naming an image of their own, and
// this emulator neither builds nor touches those.
//
// Both halves are checked against a closed charset because both become an argv
// and an alias: the version travels into `incus launch images:<f>/<v>/cloud`
// and into the published alias, and a Ref can arrive through FEINT_BOOT_IMAGES
// or a restored snapshot — an input from outside, whatever it looks like. This
// is the syntax half only; whether a recipe exists is SpecFor's question, and
// keeping the two apart is the "well formed is not authorised" rule.
func splitRef(ref string) (family, version string, ok bool) {
	if strings.Contains(ref, "/") {
		return "", "", false
	}
	family, version, found := strings.Cut(ref, ":")
	if !found || !refToken(family) || !refToken(version) {
		return "", "", false
	}
	return family, version, true
}

// refToken accepts the characters a family or version may carry. The first rune
// must be alphanumeric so no token can ever reach a command line looking like a
// flag, the same reason safeName exists.
func refToken(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i, c := range s {
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if i == 0 && !alnum {
			return false
		}
		if !alnum && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// ImageAlias is the local alias a runtime-agnostic Ref publishes to:
// "ubuntu:24.04" is held as "feint/ubuntu/24.04".
//
// Named once and used twice on purpose: the driver resolves a boot through it
// (resolveImage) and SpecFor derives what to build through it. Two spellings
// of the same mapping is how one of them ends up looking for an alias nothing
// publishes.
func ImageAlias(ref string) (string, bool) {
	family, version, ok := splitRef(ref)
	if !ok {
		return "", false
	}
	return ImagePrefix + "/" + family + "/" + version, true
}

// SpecFor derives the image to build from a Ref: the family selects the
// package, service and manager, the version travels through (#465).
//
// Enumerating was the previous shape, and it made the struct's own comment a
// lie — "a fifth family is a row and not a code change" was true for a family
// and false for a version, so debian:13 silently derived nothing and the boot
// fell back to an upstream image with no ssh daemon. A known family at any
// version now yields a buildable spec; an unknown family still yields nothing,
// which keeps the announced fallback for what genuinely has no recipe.
//
// TestSpecForDerivesEveryVersionOfAKnownFamily and
// TestSpecForRefusesWhatNoRecipeCovers fail without this.
func SpecFor(ref string) (ImageSpec, bool) {
	family, version, ok := splitRef(ref)
	if !ok {
		return ImageSpec{}, false
	}
	recipe, known := imageFamilies()[family]
	if !known {
		return ImageSpec{}, false
	}
	return ImageSpec{
		Name:    family + "/" + version,
		Source:  "images:" + family + "/" + version + "/cloud",
		Package: recipe.Package,
		Service: recipe.Service,
		Manager: recipe.Manager,
	}, true
}

// RequiredImages is the set `feint images` builds ahead of time, so an operator
// can warm a station before a run. Derivation serves the boot path; this list
// serves the warm-up — two questions, one recipe, which is why every row is
// derived through SpecFor rather than spelled out beside it.
//
// One row per cloud-init template family, and that is not a coincidence: an
// image set narrower than internal/core/cloudinit/templates/ makes the templates
// lie about distributions nobody can actually boot. debian:13 is here because
// the Scaleway catalogue promises debian_trixie today (#465); fedora stays
// derive-on-demand, with one demander in the surveyed stacks and a recipe that
// costs nothing to hold.
func RequiredImages() []ImageSpec {
	refs := []string{
		"ubuntu:24.04",
		"ubuntu:22.04",
		"debian:12",
		"debian:13",
		"alpine:3.21",
		"almalinux:9",
	}
	out := make([]ImageSpec, 0, len(refs))
	for _, ref := range refs {
		spec, ok := SpecFor(ref)
		if !ok {
			// A warm-up ref outside the family table is a defect of this file;
			// TestRequiredImagesDeriveFromTheFamilyTable is what fails first.
			continue
		}
		out = append(out, spec)
	}
	return out
}

// DeclaredImageSyntax is the shape one FEINT_BOOT_IMAGES entry takes, quoted by
// the refusal message and the parse errors so the reader never has to open the
// code to learn it.
const DeclaredImageSyntax = "<identifier>=<family>:<version>[@login]"

// ParseDeclaredImages reads the operator's own identifier declarations, the
// form FEINT_BOOT_IMAGES carries: comma-separated entries, each mapping one
// opaque identifier onto the operating system the operator knows it to be —
// "ami-a3ca408c=ubuntu:22.04", with an optional @login for the clouds where the
// login belongs to the image.
//
// This is the door through the boot refusal, and it is the operator's own
// assertion rather than the emulator guessing: a substitution nobody asked for
// was refused in #392, and what replaces it is a declaration somebody signs.
// The keys are opaque strings; nothing here knows any provider's vocabulary.
//
// Every entry is validated here, at the entry, and a bad one refuses the whole
// set: a typo that silently dropped an entry would resurface hours later as a
// refused boot, blamed on the stack. Unknown family, malformed ref, duplicate
// identifier — each error names the entry and the families a recipe exists for.
//
// TestParseDeclaredImagesRefusesWhatItCannotBuild fails without the checks.
func ParseDeclaredImages(s string) (map[string]Image, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	out := map[string]Image{}
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		id, spec, found := strings.Cut(entry, "=")
		id, spec = strings.TrimSpace(id), strings.TrimSpace(spec)
		if !found || id == "" || spec == "" {
			return nil, fmt.Errorf("entry %q: want %s", entry, DeclaredImageSyntax)
		}
		ref, login, _ := strings.Cut(spec, "@")
		if _, ok := SpecFor(ref); !ok {
			return nil, fmt.Errorf("entry %q: %q is not a ref this emulator can build; want %s with a family among %s",
				entry, ref, DeclaredImageSyntax, strings.Join(Families(), ", "))
		}
		if _, dup := out[id]; dup {
			return nil, fmt.Errorf("entry %q: %s is declared twice", entry, id)
		}
		out[id] = Image{Ref: ref, User: login}
	}
	return out, nil
}

// ImageBuilder is the optional half of a Driver that can build the images
// RequiredImages names.
//
// It is the seam `feint images` drives, named here so EnsureImage can drive the
// same one. Two builders would be two recipes, and a defect fixed in one would
// survive in the other — the shape CLAUDE.md names as the most expensive
// mistake made in this repository.
type ImageBuilder interface {
	BuildImage(ctx context.Context, spec ImageSpec, progress io.Writer) error
}

// imageBuilds serialises builds by alias.
//
// Two servers powered on at once against a station that holds neither image
// would otherwise each build it: minutes spent twice, and two containers
// working on one alias. A lock per alias rather than one global lock, for the
// reason the store lock already taught here — a global one would queue every
// boot behind one download, while two different images have no reason to wait
// for each other.
//
// It is taken in exactly one place, BuildIfMissing, and that is the point: a
// control every caller has to remember is a control one caller will forget.
// `feint images` and the boot path (EnsureImage) are the two callers today, and
// neither takes it itself.
//
// What it cannot do is span processes, and the pair that actually collided on
// 2026-08-25 was a `feint serve` and a `feint images` in another terminal. That
// half is closed by the builder's name rather than by a lock: see builderName.
var imageBuilds sync.Map // alias -> *sync.Mutex

func imageBuildLock(alias string) *sync.Mutex {
	mu, _ := imageBuilds.LoadOrStore(alias, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// BuildIfMissing builds one image unless the station already holds it, and
// reports whether it built anything.
//
// The one seam every build goes through: `feint images` for the warm-up set,
// and EnsureImage for the image a boot derives. Both get the exclusion and the
// second look for free, which is why neither writes them.
//
// The second look is not belt and braces. A caller checks the inventory, waits
// for whoever holds the lock, and by the time it gets in the image it wanted
// exists — building it again would cost minutes for nothing and republish an
// alias somebody may be booting from.
//
// TestOneBuilderPerImageAndPerProcess fails without this.
func BuildIfMissing(ctx context.Context, driver Driver, spec ImageSpec, progress io.Writer) (bool, error) {
	builder, canBuild := driver.(ImageBuilder)
	if !canBuild {
		return false, fmt.Errorf("the %s runtime cannot build images", driver.Name())
	}
	mu := imageBuildLock(spec.Alias())
	mu.Lock()
	defer mu.Unlock()

	if lister, canAsk := driver.(ImageLister); canAsk {
		held, err := lister.LocalImages(ctx)
		// A station that cannot be asked is not a station that holds nothing:
		// the build goes ahead, because the caller asked for this image and
		// refusing on a failed question would be worse than building twice.
		if err == nil {
			if _, present := held[spec.Alias()]; present {
				return false, nil
			}
		}
	}
	if err := builder.BuildImage(ctx, spec, progress); err != nil {
		return false, err
	}
	return true, nil
}

// EnsureImage makes sure the station holds the image behind ref, building it at
// boot when the family table derives a recipe and the station lacks the result
// (#465). It reports whether it built one.
//
// "On the fly" is the decision this implements: a station that has never run
// `feint images` still boots debian:13 with an ssh daemon, at the price of one
// announced build on the first boot. Building is a slow side effect on
// somebody's machine, so it is announced when it starts and when it ends, with
// the image and how long it took: a treatment that says nothing is
// indistinguishable from one that is stuck.
//
// A ref outside the family table is not an error — it returns (false, nil) and
// the boot keeps resolveImage's announced fallback, because "no recipe" and
// "the recipe failed" must not read the same. A build that does fail is an
// error, and the caller refuses the boot with it rather than falling back: for
// a derived ref the upstream image is the very source the build could not
// fetch, so the fallback would re-fail with a raw driver error where a refusal
// can name the ref and the source.
//
// TestAFailedImageBuildRefusesTheBootAndNamesTheSource and
// TestABootDerivesAndBuildsTheImageItNames fail without this.
func EnsureImage(ctx context.Context, driver Driver, ref string, log *slog.Logger) (bool, error) {
	if log == nil {
		log = slog.Default()
	}
	spec, ours := SpecFor(ref)
	if !ours {
		return false, nil
	}
	lister, canAsk := driver.(ImageLister)
	_, canBuild := driver.(ImageBuilder)
	if !canAsk || !canBuild {
		// An undeclared capability counts as absent, so this says "nobody could
		// look" rather than "the image is there": the boot goes on, and the
		// driver's own fallback warning is what the operator reads next.
		log.Debug("this runtime cannot be asked about base images, so none is built",
			"image", ref, "runtime", driver.Name())
		return false, nil
	}
	held, err := lister.LocalImages(ctx)
	if err != nil {
		log.Warn("could not ask the station which base images it holds, so none is built",
			"image", ref, "error", err)
		return false, nil
	}
	if _, present := held[spec.Alias()]; present {
		return false, nil
	}

	log.Warn("building an image this station does not hold yet, so the boot has something to run",
		"image", ref, "alias", spec.Alias(), "source", spec.Source,
		"consequence", "minutes on this first boot, and outbound network to fetch it",
		"fix", "`feint images` builds the warm-up set ahead of time, `feint images --check` says what is missing")
	// Detached from the caller's context on purpose. A client that gives up on
	// its request — Terraform's timeout, a Ctrl-C — would otherwise kill the
	// build halfway and leave the builder instance on the station, which is the
	// leftover this project sweeps rather than creates. The build finishes, the
	// next boot finds the image, and the cap is there so a wedged download
	// cannot hold the lock for ever.
	buildCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), imageBuildTimeout)
	defer cancel()
	started := time.Now()
	built, err := BuildIfMissing(buildCtx, driver, spec, nil)
	if err != nil {
		// Said here with the duration; the caller owns the refusal and its
		// message, because only it knows the resource and the provider.
		log.Error("could not build the image this boot derived",
			"image", ref, "alias", spec.Alias(), "source", spec.Source,
			"after", time.Since(started).Round(time.Second).String(), "error", err)
		return false, err
	}
	if !built {
		log.Warn("another run had already built this image, and the boot goes on",
			"image", ref, "alias", spec.Alias())
		return false, nil
	}
	log.Warn("the image is built and the boot goes on",
		"image", ref, "alias", spec.Alias(), "in", time.Since(started).Round(time.Second).String())
	return true, nil
}

// imageBuildTimeout caps one build. Three minutes go to waiting for the builder
// to answer (waitForBuilder), the rest to a package install over whatever
// network the station has; twenty minutes is well past both and still short of
// forever.
const imageBuildTimeout = 20 * time.Minute

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

// DerivedImages answers the images the station holds under the emulator's
// prefix that no warm-up row names — the ones a boot derived on demand (#465),
// and anything published under the prefix by hand.
//
// The two questions used to coincide: while the recipe table was enumerated,
// what the station could hold under the prefix was exactly what the fixed list
// named, and an inventory of the list was an inventory of the station.
// Derivation splits them — a boot builds feint/fedora/44 without the warm-up
// set ever naming it — and an image the inventory cannot name is the silent
// residue this project hunts.
//
// A derived image is neither missing nor wrong, and `feint clean` deliberately
// removes no image, warm-up or derived alike: clean removes what a killed run
// left half-alive, and an image is the asset that spares the next run its
// minutes of build — the conformance suite runs clean, and sweeping images
// there would rebuild the set on every pass. Visibility is therefore the whole
// control, and the report names the removal gesture beside each derived row.
//
// TestDerivedImagesAreNamedBesideTheWarmupSet fails without this.
func DerivedImages(ctx context.Context, driver Driver) ([]ImageStatus, error) {
	lister, ok := driver.(ImageLister)
	if !ok {
		return nil, nil
	}
	held, err := lister.LocalImages(ctx)
	if err != nil {
		return nil, err
	}
	named := map[string]bool{}
	for _, spec := range RequiredImages() {
		named[spec.Alias()] = true
	}
	var out []ImageStatus
	for alias, fingerprint := range held {
		if named[alias] || !strings.HasPrefix(alias, ImagePrefix+"/") {
			continue
		}
		name := strings.TrimPrefix(alias, ImagePrefix+"/")
		spec, known := SpecFor(strings.Replace(name, "/", ":", 1))
		if !known {
			// Under the prefix and outside every recipe: named all the same. A
			// family the table lost would otherwise vanish from the inventory
			// while staying on the station.
			spec = ImageSpec{Name: name}
		}
		out = append(out, ImageStatus{Spec: spec, Fingerprint: fingerprint})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Spec.Name < out[j].Spec.Name })
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
