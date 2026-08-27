package scaleway_test

import (
	"net/http"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
)

// The measurement of #83, replayed against the pack the way the issue took it:
// a create naming an image no catalogue holds answers 201 — docs/limits.md
// declares identifiers unchecked, deliberately — and under a runtime the
// poweron must not start a stand-in. The server reaches this provider's own
// failed state, stopped, and the runtime is never asked for anything.
func TestAnUnknownImageDoesNotBootASubstitute(t *testing.T) {
	rt := newFakeRuntime()
	close(rt.release) // nothing here needs to hold a start open
	ts := newRuntimeTestServer(t, machine.Use(rt))
	const zone = "/instance/v1/zones/fr-par-1"

	status, out := do(t, ts, "POST", zone+"/servers",
		`{"name":"demo","commercial_type":"DEV1-S","image":"totalement-inconnue"}`)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d, the control plane must keep accepting", status)
	}
	server, _ := out["server"].(map[string]any)
	id, _ := server["id"].(string)

	do(t, ts, "POST", zone+"/servers/"+id+"/action", `{"action":"poweron"}`)

	_, out = do(t, ts, "GET", zone+"/servers/"+id, "")
	server, _ = out["server"].(map[string]any)
	if state, _ := server["state"].(string); state != "stopped" {
		t.Fatalf("state %q, want stopped: running on a substitute is the defect under test", state)
	}
	if n := rt.starts.Load(); n != 0 {
		t.Fatalf("the runtime was asked %d time(s) to boot an image nobody can name", n)
	}
}

// The marketplace answers one image per label. A single shared UUID is how
// `image = "debian_bookworm"` used to boot an Ubuntu under Terraform, which
// resolves a label through this route and sends the UUID back; an unknown
// label answers a UUID that no boot resolves, instead of the default image's.
func TestTheMarketplaceAnswersOneImagePerLabel(t *testing.T) {
	rt := newFakeRuntime()
	close(rt.release)
	ts := newRuntimeTestServer(t, machine.Use(rt))

	idOf := func(label string) string {
		_, out := do(t, ts, "GET", "/marketplace/v2/local-images?image_label="+label, "")
		images, _ := out["local_images"].([]any)
		if len(images) != 1 {
			t.Fatalf("%s: %d local images, want 1", label, len(images))
		}
		image, _ := images[0].(map[string]any)
		id, _ := image["id"].(string)
		return id
	}

	jammy, bookworm := idOf("ubuntu_jammy"), idOf("debian_bookworm")
	if jammy == bookworm {
		t.Fatalf("two labels share the UUID %s: the second one boots the first one's image", jammy)
	}
	if unknown := idOf("rocky"); unknown == jammy || unknown == bookworm {
		t.Fatalf("an unknown label answered a served image's UUID %s", unknown)
	}
}

// The accepting half, end to end: the label whose accidental survival named
// the issue. Asking for alpine must boot an Alpine, as root, because on
// Scaleway the login belongs to the cloud and travels with the resolution.
func TestAKnownImageBootsWhatItNamesWithItsLogin(t *testing.T) {
	rt := newFakeRuntime()
	close(rt.release)
	ts := newRuntimeTestServer(t, machine.Use(rt))
	const zone = "/instance/v1/zones/fr-par-1"

	status, out := do(t, ts, "POST", zone+"/servers",
		`{"name":"demo","commercial_type":"DEV1-S","image":"alpine"}`)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d", status)
	}
	server, _ := out["server"].(map[string]any)
	id, _ := server["id"].(string)

	do(t, ts, "POST", zone+"/servers/"+id+"/action", `{"action":"poweron"}`)

	_, out = do(t, ts, "GET", zone+"/servers/"+id, "")
	server, _ = out["server"].(map[string]any)
	if state, _ := server["state"].(string); state != "running" {
		t.Fatalf("state %q, want running: a guard that refuses everything breaks the product", state)
	}
	if len(rt.specs) != 1 {
		t.Fatalf("%d boots, want 1", len(rt.specs))
	}
	if got := rt.specs[0]; got.Image != "alpine:3.21" || got.User != "root" {
		t.Fatalf("booted image=%q user=%q, want alpine:3.21 as root", got.Image, got.User)
	}
}
