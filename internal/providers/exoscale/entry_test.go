package exoscale_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/providers/exoscale"
)

// What a whole-pack audit found on the Exoscale entry paths: nothing was
// checked. Each test below fails without its fix, and each was falsified by
// removing the fix in a copy outside the repository.

// A real key, from ssh-keygen. Every fixture in this repository that used a
// plausible-looking string instead passed because the pack did not check.
const realKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIr6pEFlAFO3YU0DNW/r8SkpjdbptN9ockkO2BtIolSD conformance@feint"

// A create must carry what the API description declares required.
//
// contracts/exoscale.json declares disk-size, instance-type and template
// required; the pack checked only name, which upstream does not require. An
// instance created with only a name answered 200 with template {"id": ""}, and
// `exo compute instance list` then printed "unable to retrieve Compute instance
// type" and stopped listing at it — four instances in the store, one in the
// CLI's answer. docs/limits.md accepts plausible-but-unknown ids; an absent
// reference is a different class.
func TestExoscaleRefusesAnIncompleteCreate(t *testing.T) {
	h := serve(t)
	for _, body := range []string{
		`{"name":"bare"}`,
		`{"name":"n","template":{"id":"11111111-1111-4111-8111-111111111111"},"disk-size":10}`,
		`{"name":"n","instance-type":{"id":"21624abb-764e-4def-81d7-9fc54b5957fb"},"disk-size":10}`,
		`{"name":"n","instance-type":{"id":"21624abb-764e-4def-81d7-9fc54b5957fb"},"template":{"id":"11111111-1111-4111-8111-111111111111"}}`,
	} {
		if rec, out := call(t, h, "POST", "/v2/instance", body); rec.Code == http.StatusOK {
			t.Errorf("%s was accepted: %v", body, out)
		}
	}
	// The accepting half, or the check would only be a way to refuse.
	rec, out := call(t, h, "POST", "/v2/instance", `{
		"name":"complete",
		"instance-type":{"id":"21624abb-764e-4def-81d7-9fc54b5957fb"},
		"template":{"id":"11111111-1111-4111-8111-111111111111"},
		"disk-size":10
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("a complete create was refused: %d %v", rec.Code, out)
	}
}

// The pack refuses what is not an OpenSSH key, like its two siblings.
//
// Anything was stored: a name carrying a newline, and a multi-line "key"
// embedding a cloud-config directive. There was no injection while the material
// was never rendered — and it is rendered now, which is why this comes first.
func TestExoscaleRefusesWhatIsNotAKey(t *testing.T) {
	h := serve(t)
	for _, body := range []string{
		`{"name":"k","public-key":"definitely not a key"}`,
		`{"name":"k","public-key":"ssh-ed25519 !!!!not-base64-at-all!!!! user@host"}`,
		"{\"name\":\"k\",\"public-key\":\"ssh-rsa AAAA\\nruncmd:\\n  - touch /tmp/pwned\"}",
		"{\"name\":\"evil\\nkey\",\"public-key\":\"" + realKey + "\"}",
	} {
		if rec, out := call(t, h, "POST", "/v2/ssh-key", body); rec.Code == http.StatusOK {
			t.Errorf("accepted %s: %v", body, out)
		}
	}
	if rec, out := call(t, h, "POST", "/v2/ssh-key",
		`{"name":"good","public-key":"`+realKey+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("a real key was refused: %d %v", rec.Code, out)
	}
}

// An instance names its keys through either form the API documents.
//
// Their create-instance operation declares both "ssh-key" (Instance SSH Key)
// and "ssh-keys" (Instance SSH Keys), with neither deprecated. The pack read
// only the plural, so a client sending one key had it accepted and dropped.
func TestExoscaleReadsBothKeyForms(t *testing.T) {
	for _, form := range []struct {
		what string
		body string
	}{
		{"singular", `"ssh-key":{"name":"mine"}`},
		{"plural", `"ssh-keys":[{"name":"mine"}]`},
	} {
		t.Run(form.what, func(t *testing.T) {
			h := serve(t)
			call(t, h, "POST", "/v2/ssh-key", `{"name":"mine","public-key":"`+realKey+`"}`)
			call(t, h, "POST", "/v2/instance", `{
				"name":"host",
				"instance-type":{"id":"21624abb-764e-4def-81d7-9fc54b5957fb"},
				"template":{"id":"11111111-1111-4111-8111-111111111111"},
				"disk-size":10,`+form.body+`
			}`)

			_, out := call(t, h, "GET", "/v2/instance", "")
			instances, _ := out["instances"].([]any)
			if len(instances) != 1 {
				t.Fatalf("no instance created: %v", out)
			}
			instance, _ := instances[0].(map[string]any)
			refs, _ := instance["ssh-keys"].([]any)
			if len(refs) != 1 {
				t.Fatalf("the %s form was dropped: %v", form.what, instance["ssh-keys"])
			}
			ref, _ := refs[0].(map[string]any)
			if ref["name"] != "mine" {
				t.Errorf("the instance names %v", ref["name"])
			}
			// And the fingerprint is resolved, not invented.
			if fp, _ := ref["fingerprint"].(string); !strings.Contains(fp, ":") {
				t.Errorf("no fingerprint resolved for the key: %v", ref)
			}
		})
	}
}

// The key material never leaves through the API.
//
// It is stored now, so that a machine can carry it. Their schema does not
// declare public-key on a response, and `additionalProperties: false` would
// refuse it — but the point is not the schema: a public key is not secret, and
// publishing what a client did not ask for is how a pack starts inventing.
func TestExoscaleNeverPublishesKeyMaterial(t *testing.T) {
	h := serve(t)
	call(t, h, "POST", "/v2/ssh-key", `{"name":"mine","public-key":"`+realKey+`"}`)
	for _, path := range []string{"/v2/ssh-key", "/v2/ssh-key/mine"} {
		rec, _ := call(t, h, "GET", path, "")
		if strings.Contains(rec.Body.String(), "AAAAC3Nza") {
			t.Errorf("%s published the key material: %s", path, rec.Body.String())
		}
	}
}

// The registered key reaches the machine.
//
// The pack stored a name and a fingerprint and dropped the material, so a
// registered key could never open the instance it was attached to: the machine
// booted with empty cloud-init — no user provisioned, no sshd on a minimal
// image — while the API published an address on it. Both sibling packs passed
// their keys; this one did not, which is the fixed-on-one-side class again.
//
// It also makes Boot.User real: cloudinit.Render returns "" when there are no
// keys, so the field CLAUDE.md celebrates as existing because of Exoscale was
// dead code on the only pack that motivated it.
func TestAnExoscaleKeyReachesTheMachine(t *testing.T) {
	driver := &recordingRuntime{}
	h := serveWith(t, machine.Use(driver))

	call(t, h, "POST", "/v2/ssh-key", `{"name":"mine","public-key":"`+realKey+`"}`)
	call(t, h, "POST", "/v2/instance", `{
		"name":"host",
		"instance-type":{"id":"21624abb-764e-4def-81d7-9fc54b5957fb"},
		"template":{"id":"11111111-1111-4111-8111-111111111111"},
		"disk-size":10,
		"ssh-keys":[{"name":"mine"}],
		"auto-start":true
	}`)

	keys := driver.keys()
	if len(keys) == 0 {
		t.Fatal("the machine was started with no authorized key")
	}
	if keys[0] != realKey {
		t.Errorf("the machine carries %q, want the key that was registered", keys[0])
	}
	// And the boot carries the template's login, which is the whole reason
	// Boot.User exists.
	if driver.user() == "" {
		t.Error("the boot names no user, so Boot.User is still dead code here")
	}
}

// recordingRuntime keeps the boot it was handed, so a test can assert on what
// reached the machine rather than on what the pack meant to send.
type recordingRuntime struct {
	mu    sync.Mutex
	specs []machine.Spec
}

func (r *recordingRuntime) Name() string                   { return "recording" }
func (r *recordingRuntime) Available(context.Context) bool { return true }

func (r *recordingRuntime) Start(_ context.Context, spec machine.Spec) (machine.Machine, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.specs = append(r.specs, spec)
	return machine.Machine{Name: spec.Name, Running: true}, nil
}

func (r *recordingRuntime) Stop(context.Context, string) error   { return nil }
func (r *recordingRuntime) Remove(context.Context, string) error { return nil }

func (r *recordingRuntime) Inspect(context.Context, string) (machine.Machine, bool, error) {
	return machine.Machine{}, false, nil
}

func (r *recordingRuntime) EnsureNetwork(context.Context, machine.NetworkSpec) error { return nil }
func (r *recordingRuntime) Attach(context.Context, string, machine.Attachment) error { return nil }
func (r *recordingRuntime) RemoveNetwork(context.Context, string) error              { return nil }

func (r *recordingRuntime) keys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.specs) == 0 {
		return nil
	}
	return r.specs[0].AuthorizedKeys
}

func (r *recordingRuntime) user() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.specs) == 0 {
		return ""
	}
	return r.specs[0].User
}

func serveWith(t *testing.T, drv machine.Runtime) http.Handler {
	t.Helper()
	env := emulator.DefaultEnv()
	env.UseMachines(drv)
	srv, err := emulator.NewServer(env, exoscale.New(env))
	if err != nil {
		t.Fatalf("build the server: %v", err)
	}
	return srv.Handler()
}

// Quotas are counted, not invented.
//
// `exo limits` is a first-class command of the official CLI and it answered 404
// for two releases: the quota routes were neither served nor declined, which is
// the least defensible state a route can be in. A quota is one of the few
// figures an emulator can state honestly — the limit is its own claim, like the
// catalogue, and the usage is a fact it holds.
func TestQuotasAreCountedNotInvented(t *testing.T) {
	h := serve(t)

	usage := func(resource string) float64 {
		_, out := call(t, h, "GET", "/v2/quota", "")
		quotas, _ := out["quotas"].([]any)
		for _, entry := range quotas {
			q, _ := entry.(map[string]any)
			if q["resource"] == resource {
				used, _ := q["usage"].(float64)
				return used
			}
		}
		t.Fatalf("no quota for %s: %v", resource, out)
		return -1
	}

	if before := usage("instance"); before != 0 {
		t.Fatalf("a fresh emulator reports %v instances in use", before)
	}
	call(t, h, "POST", "/v2/instance", `{
		"name":"one",
		"instance-type":{"id":"21624abb-764e-4def-81d7-9fc54b5957fb"},
		"template":{"id":"11111111-1111-4111-8111-111111111111"},
		"disk-size":10
	}`)
	if after := usage("instance"); after != 1 {
		t.Errorf("after one create the quota reports %v, want 1 — a figure that does not move is invented", after)
	}

	// The by-name read the CLI makes, and a name that answers to nothing.
	if rec, _ := call(t, h, "GET", "/v2/quota/instance", ""); rec.Code != http.StatusOK {
		t.Errorf("get-quota answered %d", rec.Code)
	}
	if rec, _ := call(t, h, "GET", "/v2/quota/not-a-resource", ""); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown quota answered %d, want 404", rec.Code)
	}
}

// The Terraform provider is refused, because this emulator can only half serve
// it.
//
// exoscale/exoscale 0.70.0 honours EXOSCALE_API_ENDPOINT for its egoscale v3
// client and builds a v2 client with no endpoint option. An apply therefore
// splits: some resources answer from here and the rest are created on the real
// / A provider older than the floor is refused, and one at the floor is served.
//
// Until v0.71.0 this provider honoured EXOSCALE_API_ENDPOINT for its egoscale v3
// client and built a v2 client with no endpoint option at all, so an apply
// neither failed nor worked: it SPLIT between the emulator and a paying account.
// Upstream fixed that in #576; the refusal became a floor, and it moved on a
// measurement rather than on a release note.
//
// Measured 2026-09-05 on examples/stacks/exoscale, the published provider with
// no dev_overrides and no fork, through `feint proxy --forward
// '*.exoscale.com=<emulator>'` — which records every host asked for and sends it
// to the emulator, so a provider that ignores its endpoint is caught rather than
// obeyed:
//
//	v0.70.0   57 requests to api-ch-{dk-2,gva-2}.exoscale.com
//	v0.71.0   none
//
// The first row is the control that makes the second mean anything.
func TestAProviderBelowTheFloorIsRefused(t *testing.T) {
	h := serve(t)

	req := httptest.NewRequest(http.MethodGet, "/v2/zone", nil)
	req.Header.Set("User-Agent",
		"Exoscale-Terraform-Provider/0.70.0 (abc1234) Terraform-SDK/2.34.0 egoscale/2")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a provider that splits its calls was served: %d %s", rec.Code, rec.Body.String())
	}
	// The refusal has to say what is happening and what to do about it, or an
	// operator reads it as the emulator being broken. It no longer offers a way
	// past itself: the escape hatch existed to test a candidate fix by hand, and
	// the fix is released.
	for _, want := range []string{"billable", "v0.71.0", "0.70.0"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("the refusal does not mention %q: %s", want, rec.Body.String())
		}
	}
	if strings.Contains(rec.Body.String(), "FEINT_EXOSCALE_ALLOW_TERRAFORM") {
		t.Errorf("the refusal still offers an escape whose subject is gone: %s", rec.Body.String())
	}

	// The exo CLI, and anything else, is served normally. A guard that refused
	// every client would pass this test's first half and break the product.
	plain := httptest.NewRequest(http.MethodGet, "/v2/zone", nil)
	plain.Header.Set("User-Agent", "exoscale-cli/1.95.1")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, plain)
	if rec.Code != http.StatusOK {
		t.Errorf("the exo CLI was refused too: %d %s", rec.Code, rec.Body.String())
	}
}

// The accepting half, without which a guard that refused every version of this
// provider would pass the test above and leave the product exactly where the
// suspension left it.
func TestAProviderAtTheFloorIsServed(t *testing.T) {
	h := serve(t)

	for _, agent := range []string{
		// The floor itself, and the agent shape measured off the wire.
		"Exoscale-Terraform-Provider/0.71.0 (c4e8499d) Terraform-SDK/v2.40.0 egoscale/0.102.3",
		// And above it.
		"Exoscale-Terraform-Provider/1.2.0 (abc1234) Terraform-SDK/v2.40.0",
		// A version this cannot read is served rather than refused: the refusal
		// is for a version measured splitting, not for everything unfamiliar.
		"Exoscale-Terraform-Provider/next (abc1234)",
	} {
		req := httptest.NewRequest(http.MethodGet, "/v2/zone", nil)
		req.Header.Set("User-Agent", agent)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%q was refused: %d %s", agent, rec.Code, rec.Body.String())
		}
	}
}

// The status this provider answers for a key it cannot read is 409, not 400.
//
// Measured on 2026-08-21 against a real ch-gva-2 account: `POST /v2/ssh-key`
// carrying `not a public key` answered `409 {"message":"Public key is
// invalid"}`, and the exchange is in corpus/exoscale/exo-refusals.jsonl. This
// pack answered 400. Both refuse, and a client branches on which.
//
// This is the test the comment in registerSSHKey names. The sibling above
// asserts that an unreadable key is refused at all, which stays true whichever
// 4xx is answered — so it cannot hold this, and that is why there are two.
func TestExoscaleAnswersTheCloudsStatusForAKeyItCannotRead(t *testing.T) {
	h := serve(t)
	rec, out := call(t, h, "POST", "/v2/ssh-key", `{"name":"k","public-key":"not a public key"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("a key this pack cannot read answered %d (%v), want 409 as the cloud does", rec.Code, out)
	}
	// And a name already taken keeps answering the same status, which is the
	// other half of what the cloud spends 409 on here.
	if rec, out := call(t, h, "POST", "/v2/ssh-key",
		`{"name":"taken","public-key":"`+realKey+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("a real key was refused: %d %v", rec.Code, out)
	}
	if rec, out := call(t, h, "POST", "/v2/ssh-key",
		`{"name":"taken","public-key":"`+realKey+`"}`); rec.Code != http.StatusConflict {
		t.Fatalf("a name already taken answered %d (%v), want 409", rec.Code, out)
	}
}

// Detach completes the machine package's driver contract; *recordingRuntime needs no behaviour here.
func (r *recordingRuntime) Detach(context.Context, string, string) error { return nil }
