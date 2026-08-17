package exoscale_test

import (
	"net/http"
	"testing"
)

// The query filters #271 turned up.
//
// GET /v2/template?visibility=private answered the public catalogue, each entry
// declaring "visibility": "public" inside a response filtered to private — the
// reply refuting its own request. The cause was structural, not a wrong branch:
// the handler discarded its *http.Request, so no query parameter could ever
// matter. Four more handlers in this pack had the same signature while their
// operations declared filters, and every test here holds one of them to the
// contract's word. The fresh-store private list is the case that lied, so it
// is asserted first.

func templatesOf(t *testing.T, h http.Handler, query string) []map[string]any {
	t.Helper()
	status, list := callRaw(h, "GET", "/v2/template"+query, "")
	if status != http.StatusOK {
		t.Fatalf("list templates %q: status %d (%v)", query, status, list)
	}
	raw, ok := list["templates"].([]any)
	if !ok {
		t.Fatalf("list templates %q answered no templates array: %v", query, list)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		view, _ := entry.(map[string]any)
		out = append(out, view)
	}
	return out
}

func aRegisteredTemplate(t *testing.T, h http.Handler, name string) string {
	t.Helper()
	status, out := callRaw(h, "POST", "/v2/template", `{
		"name": "`+name+`",
		"url": "https://example.invalid/`+name+`.qcow2",
		"checksum": "0123456789abcdef0123456789abcdef",
		"default-user": "debian"
	}`)
	if status != http.StatusOK {
		t.Fatalf("register %s: status %d (%v)", name, status, out)
	}
	ref, _ := out["reference"].(map[string]any)
	id, _ := ref["id"].(string)
	if id == "" {
		t.Fatalf("register %s: the operation names no resource (%v)", name, out)
	}
	return id
}

func TestTemplateVisibilityIsHonoured(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)

	// On a fresh store the honest answer to "my templates" is none — this is
	// the exact read that answered the public catalogue (#271).
	if got := templatesOf(t, h, "?visibility=private"); len(got) != 0 {
		t.Fatalf("a fresh store answered %d private template(s): %v", len(got), got)
	}

	registered := aRegisteredTemplate(t, h, "registered-private")

	private := templatesOf(t, h, "?visibility=private")
	if len(private) != 1 {
		t.Fatalf("after one register the private list holds %d template(s): %v", len(private), private)
	}
	if id, _ := private[0]["id"].(string); id != registered {
		t.Errorf("the private list answers %q, want the registered %q", id, registered)
	}
	if v, _ := private[0]["visibility"].(string); v != "private" {
		t.Errorf("a private-filtered entry declares visibility %q", v)
	}

	// The public list is Exoscale's catalogue and never the tenant's: mixing
	// them is how a client counting an organisation's templates counts the
	// provider's.
	for _, view := range templatesOf(t, h, "?visibility=public") {
		if v, _ := view["visibility"].(string); v != "public" {
			t.Errorf("a public-filtered entry declares visibility %q: %v", v, view)
		}
		if id, _ := view["id"].(string); id == registered {
			t.Errorf("the registered template leaked into the public list")
		}
	}
	if got := templatesOf(t, h, "?visibility=public"); len(got) == 0 {
		t.Errorf("the public catalogue is empty; the create path dies on it")
	}

	// Outside the enum their document closes, the emulator refuses rather than
	// picking a visibility the client never asked for.
	if status, _ := callRaw(h, "GET", "/v2/template?visibility=confidential", ""); status != http.StatusBadRequest {
		t.Errorf("visibility=confidential answered %d, want 400", status)
	}
}

func TestTemplateFamilyIsHonoured(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)

	for _, view := range templatesOf(t, h, "?visibility=public&family=debian") {
		if f, _ := view["family"].(string); f != "debian" {
			t.Errorf("a debian-filtered entry declares family %q: %v", f, view)
		}
	}
	if got := templatesOf(t, h, "?visibility=public&family=debian"); len(got) == 0 {
		t.Errorf("the catalogue's own debian family filters to nothing")
	}
	// A family nothing carries is an empty list, not the whole catalogue.
	if got := templatesOf(t, h, "?visibility=public&family=plan9"); len(got) != 0 {
		t.Errorf("an unknown family answered %d template(s)", len(got))
	}

	// A registered template files under the family its view declares, in the
	// private world only.
	registered := aRegisteredTemplate(t, h, "filed-by-family")
	private := templatesOf(t, h, "?visibility=private&family=linux")
	if len(private) != 1 {
		t.Fatalf("the private linux family holds %d template(s): %v", len(private), private)
	}
	if id, _ := private[0]["id"].(string); id != registered {
		t.Errorf("the private linux family answers %q, want %q", id, registered)
	}
}

func TestSecurityGroupVisibilityIsHonoured(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)

	groupsOf := func(query string) []any {
		t.Helper()
		status, list := callRaw(h, "GET", "/v2/security-group"+query, "")
		if status != http.StatusOK {
			t.Fatalf("list security groups %q: status %d (%v)", query, status, list)
		}
		raw, _ := list["security-groups"].([]any)
		return raw
	}

	// No parameter is the CLI's own spelling of private — measured, it sends
	// nothing for its default — so both must answer the organisation's groups.
	if got := groupsOf(""); len(got) == 0 {
		t.Errorf("the paramless list lost the default security group")
	}
	if got := groupsOf("?visibility=private"); len(got) == 0 {
		t.Errorf("the private list lost the default security group")
	}
	// This emulator publishes no public group, and saying so beats answering
	// the private ones under a public label.
	if got := groupsOf("?visibility=public"); len(got) != 0 {
		t.Errorf("the public list answered %d group(s) this emulator never published", len(got))
	}
	if status, _ := callRaw(h, "GET", "/v2/security-group?visibility=shared", ""); status != http.StatusBadRequest {
		t.Errorf("visibility=shared answered %d, want 400", status)
	}
}

func instancesOf(t *testing.T, h http.Handler, query string) []map[string]any {
	t.Helper()
	status, list := callRaw(h, "GET", "/v2/instance"+query, "")
	if status != http.StatusOK {
		t.Fatalf("list instances %q: status %d (%v)", query, status, list)
	}
	raw, _ := list["instances"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		view, _ := entry.(map[string]any)
		out = append(out, view)
	}
	return out
}

func TestInstanceListFiltersAreHonoured(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)

	pool := aPool(t, h, "managed", 2)
	lone := anInstance(t, h, "lone")

	// manager-id is how an instance pool counts its own members; unfiltered it
	// counted everything the store holds.
	managed := instancesOf(t, h, "?manager-id="+pool)
	if len(managed) != 2 {
		t.Fatalf("the pool's manager-id filter answers %d instance(s), want its 2 members", len(managed))
	}
	for _, view := range managed {
		manager, _ := view["manager"].(map[string]any)
		if manager["id"] != pool {
			t.Errorf("a manager-filtered instance carries manager %v", view["manager"])
		}
	}
	if got := instancesOf(t, h, "?manager-id="+pool+"&manager-type=instance-pool"); len(got) != 2 {
		t.Errorf("adding manager-type=instance-pool changed the answer to %d instance(s)", len(got))
	}

	// The standalone instance is reachable by the address the API published on
	// it, and only it comes back.
	status, view := callRaw(h, "GET", "/v2/instance/"+lone, "")
	if status != http.StatusOK {
		t.Fatalf("get instance: status %d", status)
	}
	address, _ := view["public-ip"].(string)
	if address == "" {
		t.Fatalf("the instance publishes no public-ip; the create contract changed under this test")
	}
	byAddress := instancesOf(t, h, "?ip-address="+address)
	if len(byAddress) != 1 {
		t.Fatalf("the ip-address filter answers %d instance(s), want 1", len(byAddress))
	}
	if id, _ := byAddress[0]["id"].(string); id != lone {
		t.Errorf("the ip-address filter answers %q, want %q", id, lone)
	}
}

// labels is declared by their document as a bare untyped string, egoscale v3
// exposes no option for it, and any wire format this emulator picked would be
// invented. Refused out loud, never silently unfiltered — the decision is in
// docs/limits.md.
func TestInstanceListRefusesTheLabelsFilter(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)
	if status, _ := callRaw(h, "GET", "/v2/instance?labels=env", ""); status != http.StatusBadRequest {
		t.Errorf("the labels filter answered %d, want an explicit 400", status)
	}
	// manager-type is a closed enum in their document; a value outside it is a
	// request nothing upstream defines.
	if status, _ := callRaw(h, "GET", "/v2/instance?manager-type=elastic", ""); status != http.StatusBadRequest {
		t.Errorf("manager-type=elastic answered %d, want 400", status)
	}
}

func TestEventWindowIsValidated(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)

	status, list := callRaw(h, "GET", "/v2/event?from=2026-08-01T00:00:00Z&to=2026-08-02T00:00:00Z", "")
	if status != http.StatusOK {
		t.Fatalf("a well-formed window answered %d (%v)", status, list)
	}
	if events, ok := list["events"].([]any); !ok || len(events) != 0 {
		t.Errorf("the emulated audit trail answered %v, want an empty list", list["events"])
	}
	// Their document declares both bounds as date-time; a value outside the
	// format is refused, not dropped by a handler that never looked.
	if status, _ := callRaw(h, "GET", "/v2/event?from=yesterday", ""); status != http.StatusBadRequest {
		t.Errorf("a malformed from answered %d, want 400", status)
	}
	if status, _ := callRaw(h, "GET", "/v2/event?to=2026-13-45", ""); status != http.StatusBadRequest {
		t.Errorf("a malformed to answered %d, want 400", status)
	}
}

func TestBlockVolumeInstanceFilterIsHonoured(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)
	instance := anInstance(t, h, "with-disk")

	volume := func(name string) string {
		status, out := callRaw(h, "POST", "/v2/block-storage", `{"name": "`+name+`", "size": 10}`)
		if status != http.StatusOK {
			t.Fatalf("create volume %s: status %d (%v)", name, status, out)
		}
		ref, _ := out["reference"].(map[string]any)
		id, _ := ref["id"].(string)
		return id
	}
	attached := volume("attached")
	volume("floating")
	if status, _ := callRaw(h, "PUT", "/v2/block-storage/"+attached+":attach",
		`{"instance": {"id": "`+instance+`"}}`); status != http.StatusOK {
		t.Fatalf("attach: status %d", status)
	}

	status, list := callRaw(h, "GET", "/v2/block-storage?instance-id="+instance, "")
	if status != http.StatusOK {
		t.Fatalf("filtered list: status %d", status)
	}
	volumes, _ := list["block-storage-volumes"].([]any)
	if len(volumes) != 1 {
		t.Fatalf("the instance-id filter answers %d volume(s), want the 1 attached", len(volumes))
	}
	view, _ := volumes[0].(map[string]any)
	if id, _ := view["id"].(string); id != attached {
		t.Errorf("the instance-id filter answers %q, want %q", id, attached)
	}

	// Both still exist unfiltered: the filter narrows, it does not lose.
	_, all := callRaw(h, "GET", "/v2/block-storage", "")
	if volumes, _ := all["block-storage-volumes"].([]any); len(volumes) != 2 {
		t.Errorf("the unfiltered list answers %d volume(s), want 2", len(volumes))
	}
}
