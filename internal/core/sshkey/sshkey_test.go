package sshkey

import (
	"encoding/base64"
	"encoding/binary"
	"testing"
)

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

// Well formed is not valid, and the line is not the key.
//
// "ssh-ed25519 AAAA" has a known algorithm in its first field and valid base64
// in its second, which is everything the parser checked until the material was
// read: three zero bytes, no length prefix, no algorithm name, nothing sshd
// would load. The real cloud refuses it and names the reason — 400 `invalid key
// type: ssh-ed25519`, measured on 2026-08-21 and recorded in
// corpus/scaleway/scw-refusals.jsonl, where this emulator answered 200.
//
// This is the test the comment in Parse names. Remove the embedded-algorithm
// check and the first two cases below parse.
func TestAKeyWhoseMaterialNamesAnotherAlgorithmIsRefused(t *testing.T) {
	refused := []struct{ why, key string }{
		{"material too short to carry a length prefix", "ssh-ed25519 AAAA comment"},
		{"material naming another algorithm", "ssh-ed25519 " + blobNaming("ssh-rsa")},
		{"a length prefix longer than the material", "ssh-ed25519 AAAA////"},
	}
	for _, c := range refused {
		if _, err := Parse(c.key); err == nil {
			t.Errorf("%s: parsed, want refused", c.why)
		}
	}
	// And a key whose material names its own algorithm still parses, so the
	// check refuses the mismatch rather than the family.
	if _, err := Parse("ssh-ed25519 " + blobNaming("ssh-ed25519") + " who@where"); err != nil {
		t.Errorf("a key whose material names its own algorithm was refused: %v", err)
	}
}

// blobNaming builds the base64 of an SSH blob whose first string is the given
// algorithm, followed by 32 bytes of material.
func blobNaming(algorithm string) string {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(algorithm)))
	blob := append(append([]byte{}, length[:]...), algorithm...)
	binary.BigEndian.PutUint32(length[:], 32)
	blob = append(blob, length[:]...)
	return base64.StdEncoding.EncodeToString(append(blob, make([]byte, 32)...))
}
