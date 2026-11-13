package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// The verb exists so a suite can finally see a drift (#193).
//
// Everything here runs against a real emulator — its own store, its own state
// door, its own /_feint/conformance — because the three facts worth asserting
// live on three different sides of that door: the attribute changed, the sweep
// refused what it had to refuse, and the run is marked. A fake door would prove
// the first and silently pass the other two.

// mutateEmulator returns a running emulator, its address, and its store.
func mutateEmulator(t *testing.T) (string, *emulator.Env) {
	t.Helper()
	env := emulator.DefaultEnv()
	packs, err := packsFor(env)
	if err != nil {
		t.Fatalf("build the packs: %v", err)
	}
	srv, err := emulator.NewServer(env, packs...)
	if err != nil {
		t.Fatalf("build the server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts.Listener.Addr().String(), env
}

// seed puts one resource in the store directly, which is what a pack's handler
// would have done. Deliberately not through the state door: the door is what
// the verb under test uses, and seeding through it would make the fixture and
// the subject the same mechanism.
func seed(env *emulator.Env, id, kind string, attrs map[string]any) {
	env.Store.Put(&resource.Resource{
		ID:      id,
		Kind:    kind,
		Tenant:  resource.Tenant{Provider: "scaleway", Project: "p", Zone: "fr-par-1"},
		State:   "running",
		Created: time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC),
		Updated: time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC),
		Attrs:   attrs,
	})
}

func mutateRun(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := Run(append([]string{"feint"}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

// attrOf reads one top-level attribute back out of the store.
func attrOf(t *testing.T, env *emulator.Env, kind, id, key string) (any, bool) {
	t.Helper()
	res, ok := env.Store.Peek("scaleway", kind, id)
	if !ok {
		t.Fatalf("%s/%s is gone from the store", kind, id)
	}
	v, present := res.Attrs[key]
	return v, present
}

func TestMutateChangesWhatNoClientCould(t *testing.T) {
	addr, env := mutateEmulator(t)
	seed(env, "11111111-1111-4111-8111-111111111111", "instance/server", map[string]any{
		"name": "web-01",
		"tags": []any{"before", "keep"},
		"zone": "fr-par-1",
	})

	cases := []struct {
		name  string
		args  []string
		check func(t *testing.T)
	}{
		{
			name: "an existing attribute changes and the resource stays findable",
			args: []string{"mutate", "instance/server", "web-01", "--addr", addr, "--set", `zone="nl-ams-1"`},
			check: func(t *testing.T) {
				v, _ := attrOf(t, env, "instance/server", "11111111-1111-4111-8111-111111111111", "zone")
				if v != "nl-ams-1" {
					t.Errorf("zone reads %v, want nl-ams-1", v)
				}
				if name, _ := attrOf(t, env, "instance/server", "11111111-1111-4111-8111-111111111111", "name"); name != "web-01" {
					t.Errorf("the resource lost its name: %v", name)
				}
			},
		},
		{
			name: "a list element changes by index",
			args: []string{"mutate", "instance/server", "web-01", "--addr", addr, "--set", `tags.0="after"`},
			check: func(t *testing.T) {
				v, _ := attrOf(t, env, "instance/server", "11111111-1111-4111-8111-111111111111", "tags")
				tags, ok := v.([]any)
				if !ok || len(tags) != 2 || tags[0] != "after" || tags[1] != "keep" {
					t.Errorf("tags read %v, want [after keep]", v)
				}
			},
		},
		{
			name: "the resource answers to its identifier too",
			args: []string{"mutate", "instance/server", "11111111-1111-4111-8111-111111111111",
				"--addr", addr, "--set", `state_detail="booting"`},
			check: func(t *testing.T) {
				v, _ := attrOf(t, env, "instance/server", "11111111-1111-4111-8111-111111111111", "state_detail")
				if v != "booting" {
					t.Errorf("state_detail reads %v, want booting", v)
				}
			},
		},
		{
			name: "--unset removes the attribute",
			args: []string{"mutate", "instance/server", "web-01", "--addr", addr, "--unset", "tags"},
			check: func(t *testing.T) {
				if _, present := attrOf(t, env, "instance/server", "11111111-1111-4111-8111-111111111111", "tags"); present {
					t.Error("tags survived --unset, so no client will ever see it disappear")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := mutateRun(tc.args...)
			if code != exitOK {
				t.Fatalf("exited %d: %s%s", code, out, errOut)
			}
			tc.check(t)
		})
	}
}

// A kind or a name nobody holds says what to do about it, and exits non-zero.
//
// The message is asserted, not only the code: an operator who mistypes a kind
// gets a list of the kinds there are, and that list is the whole difference
// between "it did not work" and "I know what to type next".
func TestMutateRefusesWhatItCannotFind(t *testing.T) {
	addr, env := mutateEmulator(t)
	seed(env, "22222222-2222-4222-8222-222222222222", "instance/server", map[string]any{"name": "web-01"})

	cases := []struct {
		name string
		args []string
		says string
	}{
		{
			name: "an unknown kind names the kinds there are",
			args: []string{"mutate", "instance/nope", "web-01", "--addr", addr, "--set", `a=1`},
			says: "instance/server",
		},
		{
			name: "an unknown name points at the listing",
			args: []string{"mutate", "instance/server", "web-99", "--addr", addr, "--set", `a=1`},
			says: "--list",
		},
		{
			name: "a mutation that changes nothing is refused",
			args: []string{"mutate", "instance/server", "web-01", "--addr", addr},
			says: "--set",
		},
		{
			name: "a value that is not JSON says how to quote a string",
			args: []string{"mutate", "instance/server", "web-01", "--addr", addr, "--set", `name=web-02`},
			says: "is not JSON",
		},
		{
			name: "--unset on an attribute that is not there is refused",
			args: []string{"mutate", "instance/server", "web-01", "--addr", addr, "--unset", "nothing-here"},
			says: "no attribute",
		},
		{
			name: "an index outside the list is refused",
			args: []string{"mutate", "instance/server", "web-01", "--addr", addr, "--set", `name.4="x"`},
			says: "not a container",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := mutateRun(tc.args...)
			if code == exitOK {
				t.Fatalf("exited 0 on %v: %s", tc.args, out)
			}
			if !strings.Contains(errOut, tc.says) {
				t.Errorf("the refusal never says %q:\n%s", tc.says, errOut)
			}
		})
	}
}

// The invariant sweep, and the half that matters: nothing is written.
//
// Two live servers cannot hold one address — storetest.Sweep is the emulator's
// own word on it. A mutation that produces that state tests nothing, so it is
// refused; and it is refused *before* the PUT, so the store still holds what it
// held. A version that wrote first and complained after would leave the
// emulator in exactly the impossible state this refuses to create.
func TestAMutationThatBreaksTheSweepWritesNothing(t *testing.T) {
	addr, env := mutateEmulator(t)
	seed(env, "33333333-3333-4333-8333-333333333333", "instance/server", map[string]any{
		"name": "web-01", "address": "203.0.113.10",
	})
	seed(env, "44444444-4444-4444-8444-444444444444", "instance/server", map[string]any{
		"name": "web-02", "address": "203.0.113.11",
	})

	code, out, errOut := mutateRun("mutate", "instance/server", "web-02", "--addr", addr,
		"--set", `address="203.0.113.10"`)
	if code == exitOK {
		t.Fatalf("the emulator accepted one address on two live servers: %s", out)
	}
	if !strings.Contains(errOut, "203.0.113.10") || !strings.Contains(errOut, "nothing was written") {
		t.Errorf("the refusal does not name the address or say nothing was written:\n%s", errOut)
	}

	if v, _ := attrOf(t, env, "instance/server", "44444444-4444-4444-8444-444444444444", "address"); v != "203.0.113.11" {
		t.Errorf("web-02 holds %v: the refused mutation was written anyway", v)
	}
	if mutations := env.Store.Mutations(); len(mutations) != 0 {
		t.Errorf("a refused mutation marked the run: %v", mutations)
	}
}

// A finding that was already there is not this mutation's doing.
//
// The sweep's strict reading flags pairs a pack declares legitimate, and a
// Scaleway store holds one: the server and the instance/ip record that *is*
// its address. Comparing absolutely rather than by delta would refuse every
// mutation on such a store, for a finding the mutation did not create — a
// guard that refuses everything passes the attack and breaks the product.
func TestAPreexistingSweepFindingDoesNotBlockAnUnrelatedMutation(t *testing.T) {
	addr, env := mutateEmulator(t)
	seed(env, "55555555-5555-4555-8555-555555555555", "instance/server", map[string]any{
		"name": "web-01", "address": "203.0.113.20",
	})
	seed(env, "66666666-6666-4666-8666-666666666666", "instance/server", map[string]any{
		"name": "web-02", "address": "203.0.113.20",
	})

	code, out, errOut := mutateRun("mutate", "instance/server", "web-01", "--addr", addr,
		"--set", `tags=["drifted"]`)
	if code != exitOK {
		t.Fatalf("a mutation unrelated to the standing finding was refused (%d): %s%s", code, out, errOut)
	}
	if v, _ := attrOf(t, env, "instance/server", "55555555-5555-4555-8555-555555555555", "tags"); v == nil {
		t.Error("the mutation reported success and changed nothing")
	}
}

// --list on an empty emulator says it is empty, rather than printing a header.
func TestMutateListDoesNotLieOnAnEmptyEmulator(t *testing.T) {
	addr, env := mutateEmulator(t)

	code, out, errOut := mutateRun("mutate", "--list", "--addr", addr)
	if code != exitOK {
		t.Fatalf("--list exited %d: %s", code, errOut)
	}
	if !strings.Contains(out, "no resource") {
		t.Errorf("an empty emulator listed something:\n%s", out)
	}
	if strings.Contains(out, "KIND") {
		t.Errorf("a header was printed over nothing:\n%s", out)
	}

	seed(env, "77777777-7777-4777-8777-777777777777", "instance/server", map[string]any{"name": "web-01"})
	code, out, errOut = mutateRun("mutate", "--list", "--addr", addr)
	if code != exitOK {
		t.Fatalf("--list exited %d: %s", code, errOut)
	}
	for _, want := range []string{"instance/server", "77777777-7777-4777-8777-777777777777", "web-01"} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing never names %q:\n%s", want, out)
		}
	}
}

// The boundary (#26, #193): a mutated emulator says so, and did not before.
//
// Both halves are asserted on one emulator, in order. The "before" half is what
// makes the "after" half mean anything: a field that is always non-empty would
// pass the second assertion and measure nothing.
func TestAMutationMarksTheEmulatorAndItDidNotSaySoBefore(t *testing.T) {
	addr, env := mutateEmulator(t)
	seed(env, "88888888-8888-4888-8888-888888888888", "instance/server", map[string]any{
		"name": "web-01", "tags": []any{"before"},
	})

	if before := mutationsOn(t, addr); len(before) != 0 {
		t.Fatalf("a fresh emulator already reports %v as mutated", before)
	}

	if code, out, errOut := mutateRun("mutate", "instance/server", "web-01", "--addr", addr,
		"--set", `tags=["after"]`); code != exitOK {
		t.Fatalf("the mutation exited %d: %s%s", code, out, errOut)
	}

	after := mutationsOn(t, addr)
	if len(after) != 1 {
		t.Fatalf("the run reports %d mutations, want 1: %v", len(after), after)
	}
	for _, want := range []string{"set", "instance/server", "tags"} {
		if !strings.Contains(after[0], want) {
			t.Errorf("the record never says %q: %q", want, after[0])
		}
	}

	// And it cannot be laundered: a clean state loaded over it restores clean
	// resources and does not restore a clean history.
	clean := `{"format":"feint-snapshot","version":1,"resources":[]}`
	req, err := http.NewRequest(http.MethodPut, "http://"+addr+"/_feint/state", strings.NewReader(clean)) //nolint:noctx // a test server
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("load a clean state: %v", err)
	}
	_ = resp.Body.Close()
	if still := mutationsOn(t, addr); len(still) != 1 {
		t.Errorf("loading a clean snapshot laundered the mark: %v", still)
	}
	if n := env.Store.Len(); n != 0 {
		t.Errorf("the clean load did not replace the resources: %d left", n)
	}
}

func mutationsOn(t *testing.T, addr string) []string {
	t.Helper()
	resp, err := http.Get("http://" + addr + "/_feint/conformance") //nolint:noctx // a test server
	if err != nil {
		t.Fatalf("read the conformance record: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var view emulator.ConformanceView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatalf("decode the conformance record: %v", err)
	}
	return view.Mutations
}

// The marking is structural, not a flag somebody remembers to set.
//
// encodeMutatedState is the only producer of a body for `PUT /_feint/state` in
// mutate.go, and it refuses to make one that carries no record. A future second
// mutation path that forgot to mark the run would have to write a second
// encoder to get past this.
func TestAnUnrecordedMutationIsRefused(t *testing.T) {
	doc := &mutateState{Format: "feint-snapshot", Version: 1}
	if _, err := encodeMutatedState(doc, nil); err == nil {
		t.Fatal("a state with no record of its mutation was encoded happily")
	}
	body, err := encodeMutatedState(doc, []string{"2026-11-13T09:00:00Z set k id a=1"})
	if err != nil {
		t.Fatalf("a recorded mutation was refused: %v", err)
	}
	if !bytes.Contains(body, []byte(`"mutations"`)) {
		t.Errorf("the body carries no ledger: %s", body)
	}
}

// The property that keeps a mutation off the operator's machine, and nothing
// asserted it: `Runtime` holds the names the Incus driver turns into commands,
// and a crafted one reached `incus exec <that name>` as recently as this
// morning. `applyMutations` only walks `Attrs`, which is one uncommented line
// away from being otherwise.
//
// Found by a security review of this branch: "it tends to a single line that no
// test holds".
func TestAMutationCannotReachRuntime(t *testing.T) {
	addr, env := mutateEmulator(t)
	const id = "22222222-2222-4222-8222-222222222222"
	seed(env, id, "instance/server", map[string]any{"name": "web-01"})

	res, ok := env.Store.Peek("scaleway", "instance/server", id)
	if !ok {
		t.Fatal("the seed is not in the store")
	}
	res.Runtime = map[string]string{"machine": "feint-scw-" + id}
	env.Store.Put(res)

	for _, path := range []string{"runtime.machine", "Runtime.machine", "runtime"} {
		t.Run(path, func(t *testing.T) {
			code, _, errOut := mutateRun("mutate", "instance/server", "web-01",
				"--addr", addr, "--set", path+`="incusbr0"`)
			if code == exitOK {
				t.Errorf("the verb accepted --set %s; a runtime name is what the driver turns "+
					"into a command, and incusbr0 is the host's own bridge", path)
			}
			_ = errOut
		})
	}

	// Whatever happened above, the runtime map is untouched, byte for byte.
	after, _ := env.Store.Peek("scaleway", "instance/server", id)
	if got := after.Runtime["machine"]; got != "feint-scw-"+id {
		t.Errorf("the runtime name reads %q after the attempts, want it unchanged", got)
	}

	// The accepting half: an ordinary attribute still mutates.
	if code, _, errOut := mutateRun("mutate", "instance/server", "web-01",
		"--addr", addr, "--set", `name="web-02"`); code != exitOK {
		t.Fatalf("an ordinary mutation was refused too: %s", errOut)
	}
}

// A resource field this build cannot read is refused, not dropped.
//
// `json.Unmarshal` into []*resource.Resource ignores unknown keys, so a verb
// from build N talking to an emulator from build N+1 used to rewrite every
// resource without the field N+1 added. The comment above decodeState claimed
// the opposite, citing #133.
func TestAResourceFieldThisBuildCannotReadIsRefused(t *testing.T) {
	doc := []byte(`{"format":"feint.state","version":1,"resources":[` +
		`{"id":"x","kind":"instance/server","state":"running","attrs":{},` +
		`"something_a_newer_build_wrote":42}]}`)

	if _, err := decodeState(doc); err == nil {
		t.Error("a resource carrying an unknown field decoded cleanly; writing that document " +
			"back would drop the field from every resource, silently")
	}

	// The accepting half: the same document without the stray key reads fine.
	clean := []byte(`{"format":"feint.state","version":1,"resources":[` +
		`{"id":"x","kind":"instance/server","state":"running","attrs":{}}]}`)
	if _, err := decodeState(clean); err != nil {
		t.Errorf("an ordinary state document was refused: %v", err)
	}
}

// A write that lands between the read and the write is refused, not lost.
//
// The verb reads the whole store and writes the whole store back, and `Restore`
// rebuilds the map from what it receives — so a resource some client created in
// between used to vanish, with no error and no trace. That is this verb's own
// scenario: the point is to drift a resource while a suite is running.
//
// The front here is a stub, deliberately, and only for the concurrency: it
// forwards to a real emulator and makes the store appear to move once. Driving
// a genuine race would measure the scheduler, not the guard.
//
// This narrows the window rather than closing it. The real fix is a revision
// the state door checks on write, which is #915.
func TestAMutationRefusesWhenTheStoreMovedUnderIt(t *testing.T) {
	addr, env := mutateEmulator(t)
	seed(env, "33333333-3333-4333-8333-333333333333", "instance/server",
		map[string]any{"name": "web-01", "zone": "fr-par-1"})

	var gets int
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets++
		}
		res, err := http.DefaultClient.Do(&http.Request{
			Method: r.Method,
			URL:    &url.URL{Scheme: "http", Host: addr, Path: r.URL.Path},
			Body:   r.Body,
			Header: r.Header.Clone(),
		})
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer func() { _ = res.Body.Close() }()
		body, _ := io.ReadAll(res.Body)
		// The second read is the one taken just before writing: make it differ,
		// as a client creating a resource would.
		if r.Method == http.MethodGet && gets == 2 {
			body = append([]byte(nil), body...)
			body = bytes.Replace(body, []byte(`"format"`), []byte(`"x":1,"format"`), 1)
		}
		w.WriteHeader(res.StatusCode)
		_, _ = w.Write(body)
	}))
	defer front.Close()

	code, _, errOut := mutateRun("mutate", "instance/server", "web-01",
		"--addr", front.Listener.Addr().String(), "--set", `zone="nl-ams-1"`)
	if code == exitOK {
		t.Errorf("the mutation was written although the store had moved; whatever a client "+
			"created in between is now gone\n%s", errOut)
	}
	if v, _ := attrOf(t, env, "instance/server", "33333333-3333-4333-8333-333333333333", "zone"); v != "fr-par-1" {
		t.Errorf("the store reads zone=%v; the refusal must leave it untouched", v)
	}
}
