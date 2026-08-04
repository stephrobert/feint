package sshkey

import "testing"

// A key from a file ends with a newline, and that is not an injection.
//
// The first version of Parse refused every control character before trimming,
// so `exo compute ssh-key register conformance key.pub` failed on a file this
// repository writes itself. What must stay refused is a control character
// inside the value: that is what opens a second top-level key when the value
// reaches a YAML template.
func TestAKeyFromAFileIsAccepted(t *testing.T) {
	const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIr6pEFlAFO3YU0DNW/r8SkpjdbptN9ockkO2BtIolSD conformance@feint"
	for _, form := range []string{key, key + "\n", key + "\r\n", "  " + key + "  \n"} {
		if !Valid(form) {
			t.Errorf("refused a key with surrounding whitespace: %q", form)
		}
	}
	for _, bad := range []string{
		"ssh-rsa AAAA\nruncmd:\n  - touch /tmp/pwned",
		"ssh-ed25519 AAAA\rwrite_files:",
		key + "\nruncmd: [touch /tmp/pwned]",
	} {
		if Valid(bad) {
			t.Errorf("accepted a value carrying an inner control character: %q", bad)
		}
	}
}

// What is stored is the key, not the bytes it travelled in.
//
// Storing the raw value kept the trailing newline of a key read from a file,
// and cloud-init then refused it: "authorized key 0 carries a control
// character". Measured with the real exo CLI, creating an instance that went to
// state error rather than running.
func TestTheCanonicalFormCarriesNoSurroundings(t *testing.T) {
	const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIr6pEFlAFO3YU0DNW/r8SkpjdbptN9ockkO2BtIolSD conformance@feint"
	for _, form := range []string{key, key + "\n", "  " + key + "\r\n"} {
		parsed, err := Parse(form)
		if err != nil {
			t.Fatalf("Parse(%q): %v", form, err)
		}
		if got := parsed.String(); got != key {
			t.Errorf("String() gave %q, want the canonical %q", got, key)
		}
	}
}
