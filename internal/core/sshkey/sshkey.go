// Package sshkey reads the OpenSSH one-line public key format.
//
// It exists because two packs had written it twice. The copies drifted, as
// copies do: one checked that the key material was valid base64 and one did not,
// so `ssh-ed25519 !!!!not-base64-at-all!!!! x` was refused by Scaleway and
// accepted by Outscale. And one hashed the decoded key and the other hashed the
// whole line, comment included, so the same key had two fingerprints and neither
// client could match the one `ssh-keygen -l` prints.
//
// CLAUDE.md gives the test for where this belongs: a line that could be written
// identically for another provider belongs in the core. Reading an OpenSSH key
// is the same everywhere; what differs is which field an API publishes, and that
// stays in the packs.
package sshkey

import (
	"crypto/md5" //nolint:gosec // the SSH fingerprint format is MD5, not a security decision
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
)

// ErrNotAKey is what Parse answers for anything that is not an OpenSSH public
// key in its one-line form.
var ErrNotAKey = errors.New("not an OpenSSH public key")

// Key is a parsed public key: its algorithm, its decoded material, and the
// comment a client usually puts at the end.
type Key struct {
	// Algorithm is the first field, "ssh-ed25519" and the like. An API that
	// publishes a key type publishes this, rather than a constant: hardcoding
	// one made every key created through the Outscale pack answer "ssh-rsa",
	// including ed25519 ones.
	Algorithm string
	// Blob is the decoded key material, which is what a fingerprint is computed
	// over — not the line, and not the base64.
	Blob []byte
	// Comment is everything after the material, usually user@host. It carries
	// no meaning and must not reach a fingerprint: renaming it would otherwise
	// change the fingerprint of the same key.
	Comment string
}

// algorithms are the ones OpenSSH emits today. Refusing an unknown one is not
// pedantry: the value ends up in ssh_authorized_keys, and a machine that boots
// holding bytes sshd will not read is a machine nobody can log into, which the
// API would go on describing as reachable.
var algorithms = map[string]bool{
	"ssh-rsa":                            true,
	"ssh-ed25519":                        true,
	"ssh-dss":                            true,
	"ecdsa-sha2-nistp256":                true,
	"ecdsa-sha2-nistp384":                true,
	"ecdsa-sha2-nistp521":                true,
	"sk-ssh-ed25519@openssh.com":         true,
	"sk-ecdsa-sha2-nistp256@openssh.com": true,
}

// Parse reads the one-line form and refuses everything else.
//
// The one-line part is a security check rather than tidiness. The value is
// concatenated into a YAML document by text/template, and strings.Fields splits
// on newlines exactly like spaces — which is how a multi-line "key" once passed
// a shape check and opened a top-level key in a cloud-config. So the control
// characters are refused before anything is split.
func Parse(key string) (Key, error) {
	// Trimmed first, then checked. A key read from a file ends with a newline —
	// `ssh-keygen` writes one, `cat key.pub` carries it, and the Exoscale
	// conformance suite feeds exactly that — so refusing every control character
	// before trimming rejected legitimate keys. It was caught by the real client:
	// `exo compute ssh-key register` failed on a file this repository wrote
	// itself.
	//
	// What must stay refused is a control character *inside* the value, which is
	// what opens a second top-level key in a cloud-config. TrimSpace only takes
	// leading and trailing whitespace, so a multi-line payload still fails here.
	key = strings.TrimSpace(key)
	if strings.ContainsAny(key, "\n\r\x00") {
		return Key{}, ErrNotAKey
	}
	fields := strings.Fields(key)
	if len(fields) < 2 {
		return Key{}, ErrNotAKey
	}
	if !algorithms[fields[0]] {
		return Key{}, ErrNotAKey
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return Key{}, ErrNotAKey
	}
	// The material names its own algorithm, and it must be the one the line
	// declares. Without this, "ssh-ed25519 AAAA" parses: the first field is a
	// known algorithm and the second decodes as base64, so both checks above
	// pass over three bytes that are not a key at all.
	//
	// Well formed is not valid. The real cloud makes the same check and says so
	// — `scw iam ssh-key create public-key="ssh-ed25519 AAAA <comment>"`
	// answered 400 `invalid key type: ssh-ed25519` on 2026-08-21, recorded in
	// corpus/scaleway/scw-refusals.jsonl, where this emulator answered 200 and
	// stored it. The consequence is not cosmetic: the value goes into
	// ssh_authorized_keys, and a machine holding bytes sshd will not read is a
	// machine nobody can log into that the API still describes as reachable —
	// which is the sentence the algorithms table above already writes.
	//
	// TestAKeyWhoseMaterialNamesAnotherAlgorithmIsRefused fails without this.
	if embedded, ok := sshString(blob); !ok || embedded != fields[0] {
		return Key{}, ErrNotAKey
	}
	return Key{
		Algorithm: fields[0],
		Blob:      blob,
		Comment:   strings.Join(fields[2:], " "),
	}, nil
}

// sshString reads the first length-prefixed string of an SSH blob, which is
// where every public key names its own algorithm (RFC 4253, section 6.6).
func sshString(blob []byte) (string, bool) {
	if len(blob) < 4 {
		return "", false
	}
	n := uint64(binary.BigEndian.Uint32(blob[:4]))
	if n > uint64(len(blob))-4 {
		return "", false
	}
	return string(blob[4 : 4+n]), true
}

// String renders the key in its canonical one-line form.
//
// A caller that stores what the client sent stores whatever surrounded it: a key
// read from a file arrives with its trailing newline, and cloud-init then
// refuses it as "authorized key 0 carries a control character" — measured with
// the real exo CLI, on a file this repository writes itself. What is stored is
// the key, not the bytes it travelled in.
func (k Key) String() string {
	out := k.Algorithm + " " + base64.StdEncoding.EncodeToString(k.Blob)
	if k.Comment != "" {
		out += " " + k.Comment
	}
	return out
}

// Valid reports whether a string is a public key, for the callers that only
// need the verdict.
func Valid(key string) bool {
	_, err := Parse(key)
	return err == nil
}

// FingerprintMD5 is the colon-separated MD5 form, the one `ssh-keygen -l -E md5`
// prints and the one both emulated APIs publish.
//
// Computed over the decoded blob, which is what makes it comparable: hashing the
// whole line instead gave a value no client could reproduce, and made the
// fingerprint change when only the comment changed.
func (k Key) FingerprintMD5() string {
	sum := md5.Sum(k.Blob) //nolint:gosec // the format is MD5 by definition
	hexed := hex.EncodeToString(sum[:])
	parts := make([]string, 0, len(hexed)/2)
	for i := 0; i+2 <= len(hexed); i += 2 {
		parts = append(parts, hexed[i:i+2])
	}
	return strings.Join(parts, ":")
}

// FingerprintMD5 of a key given as text, for a caller holding the line rather
// than a parsed key. An unparseable key has no fingerprint, and answering ""
// rather than a hash of the garbage keeps that visible.
func FingerprintMD5(key string) string {
	parsed, err := Parse(key)
	if err != nil {
		return ""
	}
	return parsed.FingerprintMD5()
}
