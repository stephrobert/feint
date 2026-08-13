package store_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/store"
)

// The rule #133 exists for: a snapshot is understood or refused.
//
// The scenario the external review described — "the snapshot carries a field
// this version no longer knows, the field is ignored, the restore succeeds" —
// was not a risk, it was the behaviour. Measured before any of this was written:
// a resource carrying `encryption_key_ref` restored without complaint, and the
// following Snapshot wrote it back without the field. The store was coherent,
// wrong, and green.
//
// These tests are that scenario inverted into a control.

// envelope wraps resources the way Snapshot writes them.
func envelope(resources string) string {
	return `{"format":"feint-snapshot","version":1,"resources":[` + resources + `]}`
}

// oneServer is a resource in the shape Snapshot produces — Go field names, since
// resource.Resource declares no JSON tags.
const oneServer = `{
  "ID": "11111111-1111-4111-8111-111111111111",
  "Kind": "instance/server",
  "Tenant": {"Provider": "scaleway", "Project": "p", "Zone": "fr-par-1"},
  "State": "running",
  "Created": "2026-01-01T00:00:00Z",
  "Updated": "2026-01-01T00:00:00Z",
  "Attrs": {"name": "prod"},
  "Runtime": {"machine": "feint-scw-1"}
}`

// What a client stores must come back byte for byte, which is the half a guard
// that refuses everything would also pass.
func TestASnapshotOfThisVersionRoundTrips(t *testing.T) {
	s := store.New()
	if err := s.Restore(strings.NewReader(envelope(oneServer))); err != nil {
		t.Fatalf("restore a snapshot of this version: %v", err)
	}
	if got := s.Len(); got != 1 {
		t.Fatalf("the store holds %d resources, want 1", got)
	}

	var written bytes.Buffer
	if err := s.Snapshot(&written); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	// The header is what makes the next restore decidable.
	for _, want := range []string{`"format": "feint-snapshot"`, `"version": 1`, `"resources"`} {
		if !bytes.Contains(written.Bytes(), []byte(want)) {
			t.Errorf("the snapshot does not carry %s:\n%s", want, written.String())
		}
	}

	// And it reloads into another store, which is what the format is for.
	again := store.New()
	if err := again.Restore(bytes.NewReader(written.Bytes())); err != nil {
		t.Fatalf("reload what we just wrote: %v", err)
	}
	if got := again.Len(); got != 1 {
		t.Fatalf("the reloaded store holds %d resources, want 1", got)
	}
	res, found := again.Get("scaleway", "instance/server", "11111111-1111-4111-8111-111111111111")
	if !found {
		t.Fatal("the resource did not survive the round trip")
	}
	if res.Runtime["machine"] != "feint-scw-1" || res.Attrs["name"] != "prod" {
		t.Errorf("the resource came back changed: %+v", res)
	}
}

// A snapshot written by a newer feint is refused, naming both versions.
//
// An operator whose restore fails needs to know which of the two binaries to
// change, which a bare "cannot restore" never tells them.
func TestASnapshotFromTheFutureIsRefused(t *testing.T) {
	s := store.New()
	future := `{"format":"feint-snapshot","version":99,"resources":[` + oneServer + `]}`

	err := s.Restore(strings.NewReader(future))
	if err == nil {
		t.Fatal("a snapshot from version 99 restored into a build that reads version 1")
	}
	for _, want := range []string{"99", "1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name version %s: %v", want, err)
		}
	}
	if s.Len() != 0 {
		t.Errorf("the refused snapshot was loaded anyway: %d resources", s.Len())
	}

	// A format that is not ours is refused too, and says what this build reads.
	err = s.Restore(strings.NewReader(`{"format":"terraform-state","version":1,"resources":[]}`))
	if err == nil || !strings.Contains(err.Error(), "feint-snapshot") {
		t.Errorf("a foreign format was not refused by name: %v", err)
	}
}

// An unknown field on a resource is refused, and the field is named.
//
// This is the review's scenario exactly. Attrs stays open on purpose — its keys
// are data a pack chose, not schema — so the test asserts both halves: the
// unknown envelope-level field is refused, and an unknown Attrs key is kept.
func TestAnUnknownResourceFieldIsRefused(t *testing.T) {
	s := store.New()
	fromTheFuture := envelope(`{
	  "ID": "11111111-1111-4111-8111-111111111111",
	  "Kind": "instance/server",
	  "Tenant": {"Provider": "scaleway", "Zone": "fr-par-1"},
	  "State": "running",
	  "Attrs": {"name": "prod"},
	  "EncryptionKeyRef": "a-field-this-build-never-heard-of"
	}`)

	err := s.Restore(strings.NewReader(fromTheFuture))
	if err == nil {
		t.Fatal("a resource carrying an unknown field restored silently, which is the defect #133 names")
	}
	if !strings.Contains(err.Error(), "EncryptionKeyRef") {
		t.Errorf("the refusal does not name the field, so nobody can act on it: %v", err)
	}
	if s.Len() != 0 {
		t.Errorf("the refused snapshot was loaded anyway: %d resources", s.Len())
	}

	// The accepting half: a pack invents Attrs keys freely, and refusing those
	// would make every new field a breaking change to the snapshot format.
	withNewAttr := envelope(`{
	  "ID": "22222222-2222-4222-8222-222222222222",
	  "Kind": "instance/server",
	  "Tenant": {"Provider": "scaleway", "Zone": "fr-par-1"},
	  "State": "running",
	  "Attrs": {"name": "prod", "a_key_no_test_knows": {"nested": true}}
	}`)
	if err := s.Restore(strings.NewReader(withNewAttr)); err != nil {
		t.Fatalf("an unknown Attrs key was refused, and Attrs is data rather than schema: %v", err)
	}
	res, _ := s.Get("scaleway", "instance/server", "22222222-2222-4222-8222-222222222222")
	if res == nil || res.Attrs["a_key_no_test_knows"] == nil {
		t.Error("the unknown Attrs key was dropped, which is the silent loss one level down")
	}
}

// A snapshot written before the envelope is refused, with the remedy in the
// message rather than a decoder complaint.
//
// Accepting it as "version 0" was the other option, and it loses the property
// this change is for: such a file has already been through a decoder that drops
// unknown fields, so nobody can say whether it is complete. Refusing it is a
// decision; accepting it silently would be the same silence one version later.
func TestALegacyBareArrayIsRefusedWithARemedy(t *testing.T) {
	s := store.New()

	err := s.Restore(strings.NewReader("[" + oneServer + "]"))
	if err == nil {
		t.Fatal("a pre-#133 bare array restored, and its completeness cannot be established")
	}
	// The message has to be actionable: an operator holding this file needs to
	// know what to do, not that their JSON is invalid.
	for _, want := range []string{"format", "feint-snapshot", "resources"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not tell the operator how to convert the file (missing %q): %v", want, err)
		}
	}

	// An empty array too, which is how tests and scripts used to clear a store.
	if err := s.Restore(strings.NewReader("[]")); err == nil {
		t.Error("a bare empty array was accepted, and the header is what makes a file readable")
	}

	// And genuinely corrupt input stays a decode error rather than being
	// explained as a legacy file, which would send an operator converting
	// something that is not a snapshot at all.
	err = s.Restore(strings.NewReader("{not json"))
	if err == nil || strings.Contains(err.Error(), "no format header") {
		t.Errorf("corrupt input was explained as a legacy snapshot: %v", err)
	}
}

// A resource with no provider still lands somewhere findable rather than
// vanishing into a key nothing queries.
func TestARestoredResourceKeepsItsIdentity(t *testing.T) {
	s := store.New()
	if err := s.Restore(strings.NewReader(envelope(oneServer))); err != nil {
		t.Fatalf("restore: %v", err)
	}
	all := s.List("instance/server", resource.Tenant{Provider: "scaleway"})
	if len(all) != 1 {
		t.Fatalf("listing the restored kind found %d, want 1", len(all))
	}
	if all[0].Tenant.Zone != "fr-par-1" {
		t.Errorf("the tenant did not survive: %+v", all[0].Tenant)
	}
}
