//go:build conformance

package cli

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/conformancetest"
	"github.com/stephrobert/feint/internal/s3"
)

// TestMountedOOSOfficialOctl drives the real octl against the local OOS door.
//
// It is the first Outscale Object Storage client path this project has, and
// #850 asks for exactly one: "one OUTSCALE or OOS client path is driven if the
// current official tooling exposes it". It does — octl v0.0.32 carries
// `octl storage`, whose `api` subcommand lists S3's own operation names,
// because osc-sdk-go's pkg/oos is built on aws-sdk-go-v2/service/s3.
//
// ## What makes this a proof rather than a round-trip against myself
//
// The empty-listing assertion below was compared against the REAL OOS on
// 2026-11-09, with the same octl binary and the same subcommand:
//
//	the account, read-only   ->  []   (2 bytes)
//	this door                ->  []   (2 bytes)
//
// identical. That cloud reading was one GET, created nothing, and the account
// held no bucket before or after; the credentials never passed through the
// harness, since octl reads ~/.osc/config.json itself. This test does not
// repeat that call — a test that needs an account is a test nobody can run —
// but the expectation it encodes came from it rather than from a guess.
//
// ## What it does not prove
//
// One client, three gestures. It says nothing about ACL, CORS, ownership or
// tagging discovery on OOS, whose shapes nobody has recorded, and the Outscale
// profile leaves those switches off for that reason. It does not prove multipart
// or byte round-trips through this door either; those have their own suites for
// the two doors that measured them.
func TestMountedOOSOfficialOctl(t *testing.T) {
	const door = "outscale/eu-west-2"
	ts, doors := storageServer(t, true, door)
	if doors == nil {
		t.Fatal("the OOS door did not mount")
	}
	endpoint := ts.URL + s3.DoorPath("outscale", "eu-west-2")
	octl := conformancetest.OCTL(t, endpoint)

	// 1. The listing the client opens with, and the gesture that was refused
	//    until ListBuckets stopped living behind BucketConfiguration. Measured
	//    against the account: an empty inventory is `[]`.
	t.Log("- the listing octl opens with, against an empty door")
	out := octl.Run(t, "--no-upgrade", "storage", "api", "ListBuckets", "--output", "json")
	var listed []map[string]any
	if err := json.Unmarshal(out, &listed); err != nil {
		t.Fatalf("octl's ListBuckets output is not a JSON array: %v %s", err, out)
	}
	if len(listed) != 0 {
		t.Fatalf("an empty door listed %d bucket(s): %s", len(listed), out)
	}
	t.Logf("  ok: [] over zero buckets, the shape the account answers")

	// 2. A bucket the client creates, and the same listing finding it. Two
	//    gestures rather than one, because a create whose result is read back
	//    by nothing is a 200 and not a proof.
	t.Log("- a bucket octl creates, found by the listing afterwards")
	const bucket = "feint-oos-conformance"
	octl.Run(t, "--no-upgrade", "storage", "api", "CreateBucket", "--Bucket", bucket)
	out = octl.Run(t, "--no-upgrade", "storage", "api", "ListBuckets", "--output", "json")
	if err := json.Unmarshal(out, &listed); err != nil {
		t.Fatalf("ListBuckets output is not a JSON array after a create: %v %s", err, out)
	}
	found := false
	for _, entry := range listed {
		if name, _ := entry["Name"].(string); name == bucket {
			found = true
			if created, _ := entry["CreationDate"].(string); created == "" {
				t.Fatalf("the bucket carries no creation date: %s", out)
			}
		}
	}
	if !found || len(listed) != 1 {
		t.Fatalf("the created bucket is not the one bucket listed: %s", out)
	}
	t.Logf("  ok: created and found, with a creation date")

	// 3. The measured refusal. The envelope itself is asserted byte for byte in
	//    internal/s3 against docs/evidence/outscale-oos-missing-bucket.json;
	//    what this adds is that the official client SURFACES it rather than
	//    choking on it, which a hand-made HTTP request cannot show.
	t.Log("- a bucket that does not exist, refused in the recorded dialect")
	// What this asserts, and what it deliberately does not.
	//
	// octl does NOT surface the S3 error code. Measured against the REAL OOS on
	// 2026-11-09, same binary, same subcommand, a bucket that does not exist:
	//
	//	The server returned an error
	//	{ "Message": null, "ErrorCodeOverride": null }
	//
	// byte for byte what this door produces. So the client prints an error and
	// discards the code, and a test asserting "NoSuchBucket" in octl's output
	// would fail against Outscale itself — this one did, twice, first through
	// HeadBucket (no body at all) and then through ListObjectsV2 (a body the
	// client drops). Both failures were mine, and the cloud reading is what
	// told them apart from an emulator defect.
	//
	// The envelope IS asserted, byte for byte, in internal/s3 against
	// docs/evidence/outscale-oos-missing-bucket.json. What belongs here is the
	// only thing this client can show: that an absent bucket is refused rather
	// than invented, and that the refusal reaches the caller as a failure.
	refused, err := octl.Attempt(t, "--no-upgrade", "storage", "api", "ListObjectsV2",
		"--Bucket", "feint-oos-absent")
	if err == nil {
		t.Fatalf("octl was told an absent bucket exists: %s", refused)
	}
	if text := string(refused); !strings.Contains(text, "The server returned an error") {
		t.Fatalf("the refusal did not reach the client at all: %s", text)
	}
	// And the listing is unchanged by the refusal: the absent bucket was not
	// quietly created by being asked for, which is the failure a 200 on
	// CreateBucket-by-accident would look like.
	out = octl.Run(t, "--no-upgrade", "storage", "api", "ListBuckets", "--output", "json")
	if err := json.Unmarshal(out, &listed); err != nil || len(listed) != 1 {
		t.Fatalf("asking for an absent bucket changed the inventory: %v %s", err, out)
	}
	t.Logf("  ok: refused as a failure, and the inventory is untouched")

	// 4. Bytes, which is what #850 means by "PUT bytes [...] GET bytes": the
	//    object store holds what the client sent, and the client gets the same
	//    bytes back out. Random, not text, because a byte path that passes
	//    through a conversion shows nothing on printable input.
	t.Log("- bytes through the door, out and back, compared by digest")
	payload := make([]byte, 4096)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	sent := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(sent, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	octl.Run(t, "--no-upgrade", "storage", "api", "PutObject",
		"--Bucket", bucket, "--Key", "probe.bin", "--Body", sent)

	// -O, and it is not a convenience. `storage object download` without an
	// output file HANGS rather than printing to stdout: measured, the call did
	// not return and had to be killed. And `api GetObject` is not the way
	// either, with or without -o raw — it answers the operation's JSON with
	// `"Body": {}`, 1032 bytes of envelope and none of the object, so a test
	// comparing its output would compare metadata and call it bytes.
	//
	// The key is POSITIONAL on this subcommand, not --Key. Three wrong forms
	// preceded this one.
	back := filepath.Join(t.TempDir(), "back.bin")
	octl.Run(t, "--no-upgrade", "-O", back, "storage", "object", "download",
		"--bucket", bucket, "probe.bin")
	got, err := os.ReadFile(back)
	if err != nil {
		t.Fatalf("the download wrote no file: %v", err)
	}
	if len(got) != len(payload) {
		t.Fatalf("the object came back as %d bytes, not %d", len(got), len(payload))
	}
	if sha256.Sum256(got) != sha256.Sum256(payload) {
		t.Fatal("the object came back with the right length and the wrong bytes")
	}
	t.Logf("  ok: %d bytes round-tripped, sha256 identical", len(got))
}
