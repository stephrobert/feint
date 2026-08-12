// Package cloudinit renders the #cloud-config that provisions an emulated
// machine's default account.
//
// The templates are taken from dsoxlab (src/dsoxlab/templates/cloud-init/),
// where they were proven on real lab VMs. Their comments are kept verbatim
// because they document verified traps rather than preferences, the sharpest one
// being lock_passwd: cloud-init locks the account by default, and OpenSSH 9+
// then refuses the login even with a valid key. A hand-written cloud-config
// misses that and produces a machine that boots, holds the right key, and never
// answers.
//
// feint embeds copies rather than depending on dsoxlab at runtime: the binary
// stays self-contained. Keeping them aligned is a deliberate, small cost.
package cloudinit

import (
	"embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed templates/*.tmpl
var templates embed.FS

// Spec is what the provisioning depends on.
type Spec struct {
	// Distribution selects the template: ubuntu, debian, almalinux (which also
	// covers Rocky and Fedora).
	Distribution string
	// Hostname is set on the machine, as a real cloud does from the server name.
	Hostname string
	// User is the login the cloud provisions: root on Scaleway, outscale on
	// Outscale, the template's own user on Exoscale.
	User string
	// AuthorizedKeys are the public keys installed for User.
	AuthorizedKeys []string
	// InstallSSHD adds openssh-server. Cloud images already carry it; the
	// minimal LXC images do not, and then nothing answers on port 22.
	InstallSSHD bool
}

// checkInjection refuses a value that would break out of the field it is
// rendered into.
//
// The templates are text/template, which concatenates without escaping, so every
// value here lands in a YAML document verbatim. A newline in any of them opens a
// top-level key of the caller's choosing: an audit turned an SSH public key into
// runcmd, bootcmd and write_files, and the same holds for a hostname, both being
// values a client supplies through the API.
//
// Refused rather than escaped, and refused here rather than in each pack. Escaping
// would mean re-implementing a YAML quoter for three templates and hoping; and a
// pack-side check would have to be written three times, which is how one of them
// ends up missing it. Nothing legitimate is lost: neither a hostname nor a
// one-line OpenSSH key contains a control character.
func (s Spec) checkInjection() error {
	const bad = "\n\r\x00"
	if strings.ContainsAny(s.Hostname, bad) {
		return fmt.Errorf("cloud-init: hostname carries a control character")
	}
	if strings.ContainsAny(s.User, bad) {
		return fmt.Errorf("cloud-init: user carries a control character")
	}
	for i, key := range s.AuthorizedKeys {
		if strings.ContainsAny(key, bad) {
			return fmt.Errorf("cloud-init: authorized key %d carries a control character", i)
		}
	}
	return nil
}

// Render returns the cloud-config, or an empty string when there is no key to
// install: provisioning an account nobody can reach is pointless.
func Render(spec Spec) (string, error) {
	if len(spec.AuthorizedKeys) == 0 {
		return "", nil
	}
	if spec.User == "" {
		spec.User = "root"
	}
	if spec.Hostname == "" {
		spec.Hostname = "feint"
	}
	if err := spec.checkInjection(); err != nil {
		return "", err
	}

	name := templateFor(spec.Distribution)
	tmpl, err := template.ParseFS(templates, "templates/"+name)
	if err != nil {
		return "", fmt.Errorf("parse cloud-init template %s: %w", name, err)
	}

	var out strings.Builder
	if err := tmpl.Execute(&out, spec); err != nil {
		return "", fmt.Errorf("render cloud-init template %s: %w", name, err)
	}
	return out.String(), nil
}

// templateFor maps a distribution onto its template. Unknown values fall back to
// Debian, whose conventions (group sudo, unit ssh) cover most images an operator
// is likely to point the emulator at.
func templateFor(distribution string) string {
	d := strings.ToLower(distribution)
	switch {
	case strings.Contains(d, "ubuntu"):
		return "ubuntu.yaml.tmpl"
	// Alpine before the default, because falling through to Debian asked it for
	// four things it cannot do: bash it does not ship, a sudo group it calls
	// wheel, an openssh-server package apk names openssh, and systemctl where
	// it runs OpenRC. The machine booted, the API said running, and nothing
	// answered on port 22.
	// TestAlpineGetsItsOwnConventions fails without this.
	case strings.Contains(d, "alpine"):
		return "alpine.yaml.tmpl"
	case strings.Contains(d, "alma"), strings.Contains(d, "rocky"),
		strings.Contains(d, "fedora"), strings.Contains(d, "centos"), strings.Contains(d, "rhel"):
		return "almalinux.yaml.tmpl"
	default:
		return "debian.yaml.tmpl"
	}
}
