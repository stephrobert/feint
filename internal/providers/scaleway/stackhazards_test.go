package scaleway_test

import (
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// The escape behind every assertion here was measured live (#262): a surveyed
// stack pointed at this emulator through SCW_API_URL sent its CreateBucket to
// the real s3.fr-par.scw.cloud, and only the fake credentials' 403 made it
// harmless. Reproduced for #280 with egress cut to a dead proxy: the same
// apply created its instance IP locally and died on
// `Put "https://<bucket>.s3.fr-par.scw.cloud/"`.

// A stack carrying any scaleway_object_* resource reaches the real cloud, so
// its text must warn — and a stack without one must stay silent, because a
// warning that fires on a healthy configuration teaches people to ignore the
// one that matters.
func TestStackHazardsNameTheObjectStorageEscape(t *testing.T) {
	pack := scaleway.New(nil)

	clean := `
resource "scaleway_instance_ip" "local" {}
resource "scaleway_instance_server" "local" {
  type  = "DEV1-S"
  image = "ubuntu_jammy"
}
`
	if warnings := pack.StackHazards(clean); len(warnings) != 0 {
		t.Fatalf("a stack the emulator fully serves was warned about: %v", warnings)
	}

	for _, escape := range []string{
		`resource "scaleway_object_bucket" "state" { name = "x" }`,
		`resource "scaleway_object_bucket_policy" "p" {}`,
		`data "scaleway_object_bucket" "existing" { name = "x" }`,
	} {
		warnings := pack.StackHazards(escape)
		if len(warnings) == 0 {
			t.Fatalf("an Object Storage resource went unnamed; that stack's apply reaches the real cloud: %s", escape)
		}
		if !strings.Contains(warnings[0], "scaleway_object") || !strings.Contains(warnings[0], "s3.<region>.scw.cloud") {
			t.Fatalf("the warning names neither the resource family nor where it goes: %q", warnings[0])
		}
	}
}

// A literal *.scw.cloud host in the text — an S3 state backend is the surveyed
// shape (CentraleSupelec/kubic) — sends those requests there by construction.
func TestStackHazardsNameARealHost(t *testing.T) {
	pack := scaleway.New(nil)
	config := `
terraform {
  backend "s3" {
    endpoint = "https://s3.fr-par.scw.cloud"
    bucket   = "tf-state"
  }
}
`
	warnings := pack.StackHazards(config)
	if len(warnings) == 0 {
		t.Fatal("a real *.scw.cloud host went unnamed; terraform init contacts it before any resource")
	}
	if !strings.Contains(warnings[0], ".scw.cloud") {
		t.Fatalf("the warning does not name the host family it found: %q", warnings[0])
	}
}
