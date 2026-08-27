package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
)

// imageHost is a driver whose image list is dictated, so a test can stand on a
// host it does not have.
//
// machine.Noop is embedded rather than the whole Driver interface reimplemented:
// what is under test is the diagnostic, and everything else a driver does is
// noise here. Adding LocalImages is what makes it an ImageLister, which is the
// one behaviour that matters.
type imageHost struct {
	machine.Noop
	held map[string]string
}

func (h imageHost) LocalImages(context.Context) (map[string]string, error) {
	return h.held, nil
}

func hostWith(aliases ...string) imageHost {
	held := map[string]string{}
	for _, alias := range aliases {
		held[alias] = "fingerprint"
	}
	return imageHost{held: held}
}

// The diagnostic names what is missing and how to build it (#203).
//
// A check that reports a problem without saying what to do sends the reader to
// the source, which is the rule the `fix` field of every check exists for. Here
// it matters more than usual: the reason an image is needed at all is three
// steps away from the symptom — no upstream image ships an ssh daemon, so the
// machine installs one at first boot, so it needs outbound network, so it needs
// an interface nothing publishes.
func TestDoctorNamesTheMissingImagesAndHowToBuildThem(t *testing.T) {
	all := machine.RequiredImages()
	if len(all) < 2 {
		t.Skip("the image table needs two rows for this to mean anything")
	}

	// One built, the rest missing: the ordinary state of a station where
	// somebody ran `feint images` before the table grew.
	got := checkImages(context.Background(), machine.Use(hostWith(all[0].Alias())))
	if got.state != verdictWarn {
		t.Errorf("missing images reported as %v, want a warning", got.state)
	}
	for _, spec := range all[1:] {
		if !strings.Contains(got.detail, spec.Name) {
			t.Errorf("the diagnostic does not name %s: %q", spec.Name, got.detail)
		}
	}
	if strings.Contains(got.detail, all[0].Name) {
		t.Errorf("the diagnostic names an image the host holds: %q", got.detail)
	}
	if !strings.Contains(got.fix, "feint images") {
		t.Errorf("the fix does not name the command that builds them: %q", got.fix)
	}

	// And the accepting half. A complete set says so rather than staying
	// silent: a diagnostic that only speaks when something is wrong leaves the
	// reader unable to tell "checked and fine" from "never looked".
	var every []string
	for _, spec := range all {
		every = append(every, spec.Alias())
	}
	got = checkImages(context.Background(), machine.Use(hostWith(every...)))
	if got.state != verdictOK {
		t.Errorf("a complete image set reported as %v, want ok (detail %q)", got.state, got.detail)
	}
}

// A host that cannot answer must not read as a host with nothing missing.
//
// The same rule CapabilitiesOf applies to an undeclared capability. Reporting
// "every image is present" because nobody could look is the shape of claim this
// project exists to remove.
func TestDoctorDoesNotReadSilenceAsACompleteImageSet(t *testing.T) {
	got := checkImages(context.Background(), machine.Use(machine.Noop{}))
	if got.state == verdictOK {
		t.Errorf("a driver that lists nothing reported an ok: %q / %q", got.title, got.detail)
	}
}
