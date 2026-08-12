package cloudinit_test

import (
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/cloudinit"
)

func TestRenderInstallsTheKeyForRoot(t *testing.T) {
	out, err := cloudinit.Render(cloudinit.Spec{
		Distribution:   "ubuntu:22.04",
		Hostname:       "web-1",
		User:           "root",
		AuthorizedKeys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 demo"},
		InstallSSHD:    true,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	for _, want := range []string{
		"#cloud-config",
		"hostname: web-1",
		"- name: root",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 demo",
		"disable_root: false", // Scaleway logs in as root
		"openssh-server",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// The trap dsoxlab documented: cloud-init locks the account by default and
// OpenSSH 9+ then refuses a key-based login. Losing these two lines silently
// produces a machine that boots, holds the right key, and never answers.
func TestRenderNeverLocksTheAccount(t *testing.T) {
	for _, distro := range []string{"ubuntu", "debian", "almalinux", "rocky", "unknown-distro"} {
		t.Run(distro, func(t *testing.T) {
			out, err := cloudinit.Render(cloudinit.Spec{
				Distribution:   distro,
				User:           "cloud",
				AuthorizedKeys: []string{"ssh-ed25519 AAAA demo"},
			})
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if !strings.Contains(out, "lock_passwd: false") {
				t.Errorf("%s: lock_passwd must be forced to false:\n%s", distro, out)
			}
			if !strings.Contains(out, `passwd: "*"`) {
				t.Errorf("%s: passwd must be \"*\":\n%s", distro, out)
			}
		})
	}
}

func TestRenderGivesANonRootUserSudo(t *testing.T) {
	out, err := cloudinit.Render(cloudinit.Spec{
		Distribution:   "debian",
		User:           "outscale",
		AuthorizedKeys: []string{"ssh-ed25519 AAAA demo"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "- name: outscale") {
		t.Fatalf("the provider's user is missing:\n%s", out)
	}
	if !strings.Contains(out, "NOPASSWD:ALL") {
		t.Fatalf("a provisioned account needs sudo:\n%s", out)
	}
	if !strings.Contains(out, "disable_root: true") {
		t.Fatalf("root must stay closed when the cloud provisions its own user:\n%s", out)
	}
}

func TestRenderPicksTheRightAdminGroup(t *testing.T) {
	cases := map[string]string{
		"ubuntu":    "groups: [sudo]",
		"debian":    "groups: [sudo]",
		"almalinux": "groups: [wheel]",
		"rocky":     "groups: [wheel]",
	}
	for distro, want := range cases {
		out, err := cloudinit.Render(cloudinit.Spec{
			Distribution:   distro,
			User:           "cloud",
			AuthorizedKeys: []string{"ssh-ed25519 AAAA demo"},
		})
		if err != nil {
			t.Fatalf("%s: render: %v", distro, err)
		}
		if !strings.Contains(out, want) {
			t.Errorf("%s: expected %q:\n%s", distro, want, out)
		}
	}
}

func TestRenderWithoutKeysIsEmpty(t *testing.T) {
	out, err := cloudinit.Render(cloudinit.Spec{Distribution: "ubuntu", User: "root"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if out != "" {
		t.Fatalf("provisioning an account nobody can reach is pointless, got:\n%s", out)
	}
}

func TestRenderSkipsThePackageInstallWhenNotNeeded(t *testing.T) {
	out, err := cloudinit.Render(cloudinit.Spec{
		Distribution:   "ubuntu",
		User:           "root",
		AuthorizedKeys: []string{"ssh-ed25519 AAAA demo"},
		InstallSSHD:    false,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(out, "openssh-server") {
		t.Fatalf("cloud images already carry sshd, no package should be installed:\n%s", out)
	}
}

// directives drops the comment lines of a cloud-config, keeping what the
// machine acts on.
//
// Needed because the first version of the Alpine test asserted against the
// whole document and failed on its own template's prose: the comment explaining
// that apk names the package openssh rather than openssh-server contains the
// string openssh-server. A test that reads comments measures the text, not the
// behaviour.
func directives(config string) string {
	var kept []string
	for _, line := range strings.Split(config, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// TestAlpineGetsItsOwnConventions holds the four things Alpine does differently.
//
// It fell through to the Debian template, which asked it for bash it does not
// ship, a sudo group it calls wheel, an openssh-server package apk names
// openssh, and systemctl where it runs OpenRC. The container booted and the API
// reported it running; nothing answered on port 22, and an operator reading
// either signal had no reason to doubt it.
//
// Four assertions rather than one, because any single one of them silently
// undoes the login: a missing package means no daemon, an unstarted service
// means no listener, a wrong group means no sudo, and a shell that does not
// exist closes the session after the key was accepted — which reads like a
// refused key.
func TestAlpineGetsItsOwnConventions(t *testing.T) {
	out, err := cloudinit.Render(cloudinit.Spec{
		Distribution:   "alpine",
		User:           "cloud",
		AuthorizedKeys: []string{"ssh-ed25519 AAAA demo"},
		InstallSSHD:    true,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	acted := directives(out)

	for _, want := range []string{
		"groups: [wheel]", // not sudo: that group does not exist on Alpine
		"shell: /bin/sh",  // busybox ash; /bin/bash is not installed
		"\n  - openssh\n", // the apk package, exactly, not openssh-server
		"rc-update add sshd default",
		"rc-service sshd start",
		"\n  - sudo\n", // the NOPASSWD rule above is read by nobody without it
	} {
		if !strings.Contains(acted, want) {
			t.Errorf("the Alpine cloud-config is missing %q:\n%s", want, acted)
		}
	}

	// And none of the conventions that belong to the other families. Checked
	// against the directives alone, because the template's own comments name
	// these on purpose, to say why Alpine cannot use them.
	for _, unwanted := range []string{"openssh-server", "systemctl", "/bin/bash", "[sudo]"} {
		if strings.Contains(acted, unwanted) {
			t.Errorf("the Alpine cloud-config still carries %q, which it cannot honour:\n%s",
				unwanted, acted)
		}
	}
}

// Root needs neither sudo nor a shell line, and asking apk for a sudo package
// it will not use is a slower boot for nothing.
func TestAlpineAsRootInstallsOnlyWhatItNeeds(t *testing.T) {
	out, err := cloudinit.Render(cloudinit.Spec{
		Distribution:   "alpine",
		User:           "root",
		AuthorizedKeys: []string{"ssh-ed25519 AAAA demo"},
		InstallSSHD:    true,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	acted := directives(out)

	if !strings.Contains(acted, "\n  - openssh\n") {
		t.Errorf("root still needs the daemon:\n%s", acted)
	}
	if strings.Contains(acted, "\n  - sudo\n") {
		t.Errorf("root does not need sudo:\n%s", acted)
	}
}
