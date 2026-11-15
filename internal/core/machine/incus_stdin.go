package machine

import (
	"encoding/json"
	"fmt"
	"strings"
)

// instanceConfig renders the document `incus init` reads on stdin: the
// instance's config map, under the `config` key of api.InstancePut (#910).
//
// Built by a serialiser, never by Sprintf or a template, which is this
// repository's rule for a structured format: the user data is a client's own
// document, and a text render of it is an injection into the very file that
// decides what the machine runs. The standard library has no YAML encoder and
// this package takes no dependency, but YAML 1.2 is a superset of JSON, so the
// JSON encoding of the map is a YAML document with the same meaning — with one
// gap, closed by yamlSafe below.
//
// Only `config` is sent. The devices and the profiles stay on argv, where they
// are names this driver vetted, and leaving them out of the document is what
// keeps the CLI's own merge rules for --network, -d and --no-profiles exactly
// as they were: create.go overrides profiles and devices from stdin only when
// the document carries them.
func instanceConfig(config map[string]string) ([]byte, error) {
	doc, err := json.Marshal(struct {
		Config map[string]string `json:"config"`
	}{Config: config})
	if err != nil {
		return nil, err
	}
	return yamlSafe(doc), nil
}

// maxConfigKey is the longest key, as instanceConfig writes it (JSON-quoted,
// yamlSafe-escaped, quotes included), that a YAML reader still takes for a key.
//
// YAML caps an implicit key at 1024 characters, and the readers apply the cap
// to the quoted keys of a flow mapping too. Measured on 2026-11-15 with
// gopkg.in/yaml.v2 v2.4.0, gopkg.in/yaml.v3 v3.0.1 and go.yaml.in/yaml/v4
// v4.0.0-rc.3, as the first key of the map and as a later one: 1024 decodes,
// 1025 makes all three refuse the whole document ("did not find expected ','
// or '}'"). The readers count characters and this counts bytes, so a key with
// multi-byte runes is refused a little early, never accepted late.
const maxConfigKey = 1024

// instanceConfigFits asks, before anything reaches the runtime, whether the
// configuration Start would feed `incus init` is one the CLI reads as meant:
//
//   - every environment entry is KEY=VALUE with a key. On argv the CLI refused
//     the other shapes itself; on stdin they would silently become an empty
//     variable, or a key named `environment.`;
//   - every key fits maxConfigKey once encoded, or the reader refuses the whole
//     create — the user data with it — over one label.
//
// Neither is reachable from a client today: the labels are the binding's and
// the packs' own, and no pack sets Env. Asked anyway because both fields are
// the pack's to fill. No refusal quotes the value: an environment entry may be
// a secret, and a key over a kilobyte is noise in a log line.
// TestAnEnvironmentEntryThatIsNotKeyValueIsRefused and
// TestAConfigKeyAYAMLReaderWouldRefuseIsRefused fail without it.
func instanceConfigFits(spec Spec) error {
	for i, e := range spec.Env {
		key, _, ok := strings.Cut(e, "=")
		if !ok || key == "" {
			return fmt.Errorf("environment entry %d of %s is not KEY=VALUE", i, spec.Name)
		}
		if n := encodedKeyLen("environment." + key); n > maxConfigKey {
			return fmt.Errorf("environment entry %d of %s has a key of %d bytes once encoded, over the %d a YAML reader accepts", i, spec.Name, n, maxConfigKey)
		}
	}
	for k := range spec.Labels {
		if n := encodedKeyLen("user." + k); n > maxConfigKey {
			return fmt.Errorf("a label of %s has a key of %d bytes once encoded, over the %d a YAML reader accepts", spec.Name, n, maxConfigKey)
		}
	}
	return nil
}

// encodedKeyLen is the length of key as instanceConfig writes it.
func encodedKeyLen(key string) int {
	encoded, err := json.Marshal(key)
	if err != nil {
		return maxConfigKey + 1
	}
	return len(yamlSafe(encoded))
}

// yamlSafe escapes the characters JSON leaves raw and a YAML reader would not
// read back as written.
//
// encoding/json escapes every C0 control character, U+2028 and U+2029, and
// writes everything else as it is. Two families survive that a YAML reader
// does not keep, both measured on 2026-11-15 against gopkg.in/yaml.v2 v2.4.0,
// gopkg.in/yaml.v3 v3.0.1 and go.yaml.in/yaml/v4 v4.0.0-rc.3, decoding the
// document this function produces into api.InstancePut's config field:
//
//   - DEL, the C1 controls and the noncharacters U+FFFE and U+FFFF are outside
//     YAML's c-printable set, and all three readers refuse the whole document
//     with "control characters are not allowed": a client's user data carrying
//     one was accepted on argv and would make the create fail on stdin;
//   - NEL (U+0085) is printable but a line break to YAML, so inside a quoted
//     scalar the readers fold it into a space: accepted, and silently altered.
//
// So only what a reader keeps verbatim is written raw. Every rune escaped here
// sits in the Basic Multilingual Plane, inside a JSON string (the structure
// json.Marshal emits is ASCII without whitespace), so `\uXXXX` is a valid escape
// in both languages and the value read back is the string that went in.
//
// TestTheStdinDocumentCarriesNoCharacterYAMLRefuses fails without it.
func yamlSafe(doc []byte) []byte {
	var b strings.Builder
	b.Grow(len(doc))
	for _, r := range string(doc) {
		if yamlVerbatim(r) {
			b.WriteRune(r)
			continue
		}
		fmt.Fprintf(&b, `\u%04x`, r)
	}
	return []byte(b.String())
}

// yamlVerbatim reports whether a YAML reader keeps r as written inside a
// double-quoted scalar: YAML 1.2's c-printable set (§5.1), minus what it reads
// as a line break (NEL, LS, PS, and the C0 breaks json.Marshal never emits raw
// anyway) and minus the byte order mark, which a reader may take for an
// encoding marker rather than content.
func yamlVerbatim(r rune) bool {
	switch {
	case r >= 0x20 && r <= 0x7e:
		return true
	case r >= 0xa0 && r <= 0xd7ff:
		return r != 0x2028 && r != 0x2029
	case r >= 0xe000 && r <= 0xfffd:
		return r != 0xfeff
	case r >= 0x10000 && r <= 0x10ffff:
		return true
	}
	return false
}
