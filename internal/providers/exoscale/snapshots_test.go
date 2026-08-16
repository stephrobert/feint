package exoscale_test

import (
	"net/http"
	"testing"
)

// Snapshots, the templates they promote into, and the triage that put them here
// (#173).
//
// Seven snapshot operations and four template ones belonged to no open batch:
// the archived roadmap had folded them into a machine batch that closed without
// them, which is precisely the "not triaged yet" that Declined() refuses to
// hold. Each is decided now — served here, or refused with its reason.

func anInstance(t *testing.T, h http.Handler, name string) string {
	t.Helper()
	status, created := callRaw(h, "POST", "/v2/instance", `{
		"name": "`+name+`",
		"instance-type": {"id": "21624abb-764e-4def-81d7-9fc54b5957fb"},
		"template": {"id": "11111111-1111-4111-8111-111111111111"},
		"disk-size": 10
	}`)
	if status != http.StatusOK {
		t.Fatalf("create %s: status %d (%v)", name, status, created)
	}
	ref, _ := created["reference"].(map[string]any)
	id, _ := ref["id"].(string)
	if id == "" {
		t.Fatalf("the create operation names no resource: %v", created)
	}
	return id
}

func aSnapshot(t *testing.T, h http.Handler, instanceID, name string) string {
	t.Helper()
	body := `{}`
	if name != "" {
		body = `{"name":"` + name + `"}`
	}
	status, out := callRaw(h, "POST", "/v2/instance/"+instanceID+":create-snapshot", body)
	if status != http.StatusOK {
		t.Fatalf("create-snapshot: status %d (%v)", status, out)
	}
	ref, _ := out["reference"].(map[string]any)
	id, _ := ref["id"].(string)
	if id == "" {
		t.Fatalf("the snapshot operation names no resource: %v", out)
	}
	return id
}

// The whole path a client walks: snapshot an instance, read it back, list it,
// and delete it.
func TestASnapshotIsCreatedReadAndDeleted(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)
	instance := anInstance(t, h, "snapshotted")
	id := aSnapshot(t, h, instance, "before-the-change")

	status, snapshot := callRaw(h, "GET", "/v2/snapshot/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("get: status %d", status)
	}
	if name, _ := snapshot["name"].(string); name != "before-the-change" {
		t.Errorf("the snapshot came back named %q", name)
	}
	// It names the instance it was taken from, which is how a client finds what
	// a snapshot belongs to and how revert knows where to go back.
	held, _ := snapshot["instance"].(map[string]any)
	if got, _ := held["id"].(string); got != instance {
		t.Errorf("the snapshot names instance %q, want %q", got, instance)
	}
	if state, _ := snapshot["state"].(string); state != "ready" {
		t.Errorf("state is %q; an emulator that lingers in snapshotting only makes clients wait", state)
	}

	status, list := callRaw(h, "GET", "/v2/snapshot", "")
	if status != http.StatusOK {
		t.Fatalf("list: status %d", status)
	}
	snapshots, _ := list["snapshots"].([]any)
	if len(snapshots) != 1 {
		t.Errorf("the list carries %d snapshot(s), want 1", len(snapshots))
	}

	if status, _ := callRaw(h, "DELETE", "/v2/snapshot/"+id, ""); status != http.StatusOK {
		t.Fatalf("delete: status %d", status)
	}
	if status, _ := callRaw(h, "GET", "/v2/snapshot/"+id, ""); status != http.StatusNotFound {
		t.Errorf("a deleted snapshot still answers %d", status)
	}
}

// A snapshot with no name is named after its instance, which is what upstream
// does and what a client that never passes one relies on to tell two apart.
func TestASnapshotWithoutANameIsNamedAfterItsInstance(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)
	instance := anInstance(t, h, "unnamed-source")
	id := aSnapshot(t, h, instance, "")

	_, snapshot := callRaw(h, "GET", "/v2/snapshot/"+id, "")
	name, _ := snapshot["name"].(string)
	if len(name) <= len("unnamed-source") || name[:len("unnamed-source")] != "unnamed-source" {
		t.Errorf("the snapshot is named %q; it should carry the instance's name", name)
	}
}

// The refusal, and the reason it matters more than the acceptance: upstream
// reverts a stopped instance, because the disk is replaced underneath it. An
// emulator that accepted a revert on a running machine would let a plan through
// that the real cloud stops.
func TestRevertingARunningInstanceIsRefused(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)
	instance := anInstance(t, h, "running-one")
	id := aSnapshot(t, h, instance, "point-in-time")

	if status, _ := callRaw(h, "PUT", "/v2/instance/"+instance+":start", ""); status != http.StatusOK {
		t.Fatalf("start: status %d", status)
	}
	status, out := callRaw(h, "POST", "/v2/instance/"+instance+":revert-snapshot", `{"id":"`+id+`"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("reverting a running instance answered %d, want 400 (%v)", status, out)
	}

	// And the accepting half, or a guard that refuses everything would pass.
	if status, _ := callRaw(h, "PUT", "/v2/instance/"+instance+":stop", ""); status != http.StatusOK {
		t.Fatalf("stop: status %d", status)
	}
	if status, out := callRaw(h, "POST", "/v2/instance/"+instance+":revert-snapshot",
		`{"id":"`+id+`"}`); status != http.StatusOK {
		t.Fatalf("reverting a stopped instance answered %d (%v)", status, out)
	}
}

// A snapshot belongs to the instance it was taken from. Reverting to somebody
// else's is not something the API offers, and accepting it would put a client's
// data on a machine it never named.
func TestRevertingToAnotherInstancesSnapshotIsRefused(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)
	mine := anInstance(t, h, "mine")
	theirs := anInstance(t, h, "theirs")
	id := aSnapshot(t, h, theirs, "not-yours")

	if status, _ := callRaw(h, "POST", "/v2/instance/"+mine+":revert-snapshot",
		`{"id":"`+id+`"}`); status != http.StatusBadRequest {
		t.Errorf("an instance was reverted to another one's snapshot: status %d", status)
	}
}

// The golden-image path, and the join between the two families that got them
// triaged together: a machine a client configured becomes something it can boot
// again.
func TestASnapshotIsPromotedIntoATemplate(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)
	instance := anInstance(t, h, "to-promote")
	snapshot := aSnapshot(t, h, instance, "golden")

	status, out := callRaw(h, "POST", "/v2/snapshot/"+snapshot+":promote",
		`{"name":"golden-image","description":"cut from a configured machine"}`)
	if status != http.StatusOK {
		t.Fatalf("promote: status %d (%v)", status, out)
	}
	ref, _ := out["reference"].(map[string]any)
	id, _ := ref["id"].(string)

	// It joins the list a client resolves a template out of, beside the fixed
	// catalogue, because a client asking by name cannot tell the two apart.
	status, list := callRaw(h, "GET", "/v2/template", "")
	if status != http.StatusOK {
		t.Fatalf("list templates: status %d", status)
	}
	templates, _ := list["templates"].([]any)
	found := false
	for _, raw := range templates {
		entry, _ := raw.(map[string]any)
		if got, _ := entry["id"].(string); got == id {
			found = true
		}
	}
	if !found {
		t.Errorf("the promoted template is not in the list a client resolves from: %v", list)
	}
}

// A registered template is tenant data beside the catalogue, and the catalogue
// keeps working — the trap CLAUDE.md opens with is that a client reads the
// inventory before it creates anything.
func TestARegisteredTemplateJoinsTheCatalogue(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)

	status, out := callRaw(h, "POST", "/v2/template", `{
		"name": "brought-my-own",
		"url": "https://example.invalid/disk.qcow2",
		"checksum": "0123456789abcdef0123456789abcdef",
		"default-user": "debian"
	}`)
	if status != http.StatusOK {
		t.Fatalf("register: status %d (%v)", status, out)
	}
	ref, _ := out["reference"].(map[string]any)
	id, _ := ref["id"].(string)

	status, template := callRaw(h, "GET", "/v2/template/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("get: status %d", status)
	}
	// The login the template declares, which is this provider's own shape and
	// the reason machine.Boot.User exists.
	if user, _ := template["default-user"].(string); user != "debian" {
		t.Errorf("the template publishes default-user %q, want debian", user)
	}

	// The catalogue is still there: a client that resolves a built-in by name
	// must not have lost it because somebody registered one.
	_, list := callRaw(h, "GET", "/v2/template", "")
	templates, _ := list["templates"].([]any)
	if len(templates) < 2 {
		t.Errorf("the list carries %d template(s); the fixed catalogue is gone", len(templates))
	}

	if status, _ := callRaw(h, "PUT", "/v2/template/"+id, `{"name":"renamed"}`); status != http.StatusOK {
		t.Fatalf("update: status %d", status)
	}
	_, template = callRaw(h, "GET", "/v2/template/"+id, "")
	if name, _ := template["name"].(string); name != "renamed" {
		t.Errorf("the update answered 200 and the template is still %q", name)
	}

	if status, _ := callRaw(h, "DELETE", "/v2/template/"+id, ""); status != http.StatusOK {
		t.Fatalf("delete: status %d", status)
	}
	if status, _ := callRaw(h, "GET", "/v2/template/"+id, ""); status != http.StatusNotFound {
		t.Errorf("a deleted template still answers %d", status)
	}
}

// The setting a client makes when it registers a template, kept rather than
// dropped.
//
// `exo compute instance-template register` sends
// application-consistent-snapshot-enabled on every call, and the handler read
// every other field of the request and not that one. Nothing was red: the
// register answered 200, the template read back, and the client believed it had
// set something the emulator had thrown away. The omission gate found it the
// first time a real client drove the route (#174) — this test is the same
// verdict without a CLI, so a future edit of templateAttrs cannot lose it again.
func TestARegisteredTemplateKeepsItsSnapshotSetting(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)

	register := func(t *testing.T, body string) map[string]any {
		t.Helper()
		status, out := callRaw(h, "POST", "/v2/template", body)
		if status != http.StatusOK {
			t.Fatalf("register: status %d (%v)", status, out)
		}
		ref, _ := out["reference"].(map[string]any)
		id, _ := ref["id"].(string)
		status, template := callRaw(h, "GET", "/v2/template/"+id, "")
		if status != http.StatusOK {
			t.Fatalf("get: status %d", status)
		}
		return template
	}

	asked := register(t, `{
		"name": "consistent",
		"url": "https://example.invalid/disk.qcow2",
		"checksum": "0123456789abcdef0123456789abcdef",
		"application-consistent-snapshot-enabled": true
	}`)
	if enabled, _ := asked["application-consistent-snapshot-enabled"].(bool); !enabled {
		t.Errorf("the template dropped the setting it was registered with: %v", asked)
	}

	// And the default is off rather than absent: a client reading the field on a
	// template that never asked for it must find false, not nothing — upstream
	// declares it on every template, so an absent key is a shape a client can
	// trip on.
	silent := register(t, `{
		"name": "plain",
		"url": "https://example.invalid/disk.qcow2",
		"checksum": "0123456789abcdef0123456789abcdef"
	}`)
	enabled, present := silent["application-consistent-snapshot-enabled"].(bool)
	if !present {
		t.Errorf("the template does not publish the field at all: %v", silent)
	}
	if enabled {
		t.Errorf("a template that asked for nothing promises application-consistent snapshots")
	}
}

// Control characters are refused at the door, before the store, which is this
// repository's stated order of preference for anything that may reach a
// structured format later.
func TestATemplateNameWithControlCharactersIsRefused(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)
	if status, _ := callRaw(h, "POST", "/v2/template",
		"{\"name\":\"one\\nruncmd:\\n  - touch /tmp/pwned\"}"); status != http.StatusBadRequest {
		t.Errorf("a template name carrying a newline was accepted: status %d", status)
	}
}

// The account read the pack already refused to decline, and had never served
// (#173). TestQuotasAndOrganisationAreNotDeclined kept it in scope; nothing
// required it to answer, which is the shape a decision left half-taken takes.
func TestTheOrganizationIsServedAndStable(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)
	status, first := callRaw(h, "GET", "/v2/organization", "")
	if status != http.StatusOK {
		t.Fatalf("get-organization answers %d", status)
	}
	id, _ := first["id"].(string)
	if id == "" {
		t.Fatal("the organization carries no id")
	}
	// Stable, because Terraform stores everything it reads and an identifier
	// that moves between reads is a permanent diff.
	_, second := callRaw(h, "GET", "/v2/organization", "")
	if again, _ := second["id"].(string); again != id {
		t.Errorf("the organization id changed between two reads: %q then %q", id, again)
	}
}

// Empty and served, not declined: a client reads events to explain a failure,
// and a 404 there turns "nothing happened" into "this emulator is broken".
func TestEventsAreServedEmptyRatherThanRefused(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)
	status, out := callRaw(h, "GET", "/v2/event", "")
	if status != http.StatusOK {
		t.Fatalf("list-events answers %d", status)
	}
	events, ok := out["events"].([]any)
	if !ok {
		t.Fatalf("the answer carries no events list: %v", out)
	}
	if len(events) != 0 {
		t.Errorf("the emulator records no audit trail and answered %d event(s)", len(events))
	}
}

// A TPM is attached at boot, so upstream refuses it on a running instance.
func TestEnablingTPMOnARunningInstanceIsRefused(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)
	instance := anInstance(t, h, "tpm-candidate")
	if status, _ := callRaw(h, "PUT", "/v2/instance/"+instance+":start", ""); status != http.StatusOK {
		t.Fatalf("start: status %d", status)
	}
	if status, _ := callRaw(h, "POST", "/v2/instance/"+instance+":enable-tpm", ""); status != http.StatusBadRequest {
		t.Errorf("a TPM was attached to a running instance")
	}

	// The accepting half, and the round-trip: the field is the client's own and
	// must come back, since nothing below honours it.
	if status, _ := callRaw(h, "PUT", "/v2/instance/"+instance+":stop", ""); status != http.StatusOK {
		t.Fatalf("stop: status %d", status)
	}
	if status, _ := callRaw(h, "POST", "/v2/instance/"+instance+":enable-tpm", ""); status != http.StatusOK {
		t.Fatalf("enable-tpm on a stopped instance was refused")
	}
	_, read := callRaw(h, "GET", "/v2/instance/"+instance, "")
	if enabled, _ := read["tpm-enabled"].(bool); !enabled {
		t.Errorf("the emulator answered 200 and the instance does not carry tpm-enabled: %v", read)
	}
}

// The reset list is closed, and that is a security question rather than tidiness:
// Attrs is restored verbatim from a snapshot, so a field name the client sent is
// not a reason to delete whatever it points at.
func TestResettingAFieldNobodyDeclaredIsRefused(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)
	instance := anInstance(t, h, "resettable")

	if status, _ := callRaw(h, "DELETE", "/v2/instance/"+instance+"/instance-type", ""); status != http.StatusBadRequest {
		t.Errorf("a field nobody declared resettable was cleared")
	}
	// And the declared one works, or the guard refuses everything and proves
	// nothing.
	if status, _ := callRaw(h, "PUT", "/v2/instance/"+instance, `{"labels":{"keep":"no"}}`); status != http.StatusOK {
		t.Fatalf("labels update refused")
	}
	if status, _ := callRaw(h, "DELETE", "/v2/instance/"+instance+"/labels", ""); status != http.StatusOK {
		t.Fatalf("resetting labels was refused")
	}
	_, read := callRaw(h, "GET", "/v2/instance/"+instance, "")
	if labels, present := read["labels"]; present && labels != nil {
		if asMap, ok := labels.(map[string]any); ok && len(asMap) > 0 {
			t.Errorf("the reset answered 200 and the labels are still %v", labels)
		}
	}
}
