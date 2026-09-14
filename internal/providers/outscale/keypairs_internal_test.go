package outscale

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// The key this repository uses everywhere, from ssh-keygen.
const aRealKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIr6pEFlAFO3YU0DNW/r8SkpjdbptN9ockkO2BtIolSD conformance@feint"

// createKeypair posts one body straight at the handler and returns the status
// and the decoded answer. Internal, because these tests read what was stored
// rather than what was published: the key material never leaves through the
// API, so no external assertion can see the thing that reaches a machine.
func createKeypair(t *testing.T, p *Pack, name, key string) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"KeypairName": name, "PublicKey": key})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	rec := httptest.NewRecorder()
	p.createKeypair(rec, httptest.NewRequest(http.MethodPost, "/api/v1/CreateKeypair", strings.NewReader(string(body))))
	var decoded map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&decoded)
	return rec.Code, decoded
}

func keypairPack(t *testing.T) *Pack {
	t.Helper()
	return New(emulator.DefaultEnv())
}

// Both forms of one key are the same key, and the machine gets the same line.
//
// Outscale documents PublicKey as Base64-encoded, and octl v0.0.32 is the first
// of their own clients to obey: it takes --PublicKey as a file path and sends
// base64 of its bytes, where v0.0.31 sent the line verbatim and the real cloud
// answered 200 to it. Reading only the bare form answered 400 to every v0.0.32
// create — measured on 2026-09-14, `PublicKey is not an OpenSSH public key`.
//
// Two claims, and the second is the one that matters: the type and the
// fingerprint must not depend on the envelope, and what reaches
// ssh_authorized_keys must be the key rather than the bytes it travelled in. A
// stored envelope, or a stored trailing newline from the file octl encoded,
// boots a machine cloud-init refuses to configure.
func TestAKeypairReadsTheEncodedFormTheApiDocuments(t *testing.T) {
	// The trailing newline is not decoration: `octl --PublicKey ./id.pub`
	// encodes the file, and ssh-keygen writes one. The wire measurement above
	// ends in "...ZmVpbnQK", and that K is the newline.
	encoded := base64.StdEncoding.EncodeToString([]byte(aRealKey + "\n"))
	if encoded == aRealKey {
		t.Fatal("the two forms are the same string: this test would prove nothing")
	}

	p := keypairPack(t)
	for _, form := range []struct {
		name  string
		value string
	}{
		{"bare", aRealKey},
		{"encoded", encoded},
	} {
		status, out := createKeypair(t, p, form.name, form.value)
		if status != http.StatusOK {
			t.Fatalf("%s form refused: %d %v", form.name, status, out)
		}
	}

	bare := p.keypairByName("bare")
	encodedRes := p.keypairByName("encoded")
	if bare == nil || encodedRes == nil {
		t.Fatal("a create answered 200 and stored nothing")
	}
	for _, field := range []string{"KeypairType", "KeypairFingerprint"} {
		if bare.Attrs[field] != encodedRes.Attrs[field] {
			t.Errorf("%s differs with the envelope: bare %v, encoded %v — it is one key",
				field, bare.Attrs[field], encodedRes.Attrs[field])
		}
	}

	keys := p.authorizedKeys("encoded")
	if len(keys) != 1 {
		t.Fatalf("the encoded keypair hands %d keys to a machine, want 1", len(keys))
	}
	if keys[0] != aRealKey {
		t.Errorf("the machine gets %q, want the key itself %q", keys[0], aRealKey)
	}
	if strings.ContainsAny(keys[0], "\n\r\x00") {
		t.Errorf("the stored key carries a control character: %q — cloud-init refuses that", keys[0])
	}
}

// The envelope is not a way past the refusal.
//
// Every value the bare form refuses must be refused encoded too. This is the
// half a tolerant decoder gets wrong: base64 hides the newline that opens a
// top-level key in a cloud-config, so a check that runs before the decoder
// stops running at all.
//
// The cases are the ones TestAKeypairRefusesWhatIsNotAKey already states in the
// bare form, so the two halves cannot drift apart.
func TestAnEncodedKeypairIsHeldToTheSameRefusals(t *testing.T) {
	p := keypairPack(t)
	for i, bad := range []string{
		"definitely not a key",
		"ssh-rsa AAAA\nruncmd:\n  - touch /tmp/pwned",
		"ssh-ed25519 !!!!not-base64-at-all!!!! user@host",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI= trailing-garbage-in-material",
	} {
		encoded := base64.StdEncoding.EncodeToString([]byte(bad))
		if status, out := createKeypair(t, p, "bad"+string(rune('a'+i)), encoded); status == http.StatusOK {
			t.Errorf("accepted base64(%q) as a public key: %v", bad, out)
		}
	}
	// And base64 of nothing at all, which decodes cleanly to a string that is
	// not a key: a decoder that stops at "it decoded" would take it.
	if status, _ := createKeypair(t, p, "empty-envelope", base64.StdEncoding.EncodeToString([]byte("hello"))); status == http.StatusOK {
		t.Error("accepted base64 of a word that is not a key")
	}
}
