package outscale_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/store"
	"github.com/stephrobert/feint/internal/providers/outscale"
)

// Listeners after the create (#344).
//
// The two operations exist for one thing a first apply never does: editing a
// `listeners` block on a load balancer that already stands. Every success shape
// goes through call(), so it is checked against Outscale's own OpenAPI document
// — both responses carry the whole LoadBalancer, and a response that dropped it
// would leave the provider with nothing to store.

// lbWithOneListener is a Net, a Subnet and an internal balancer holding one
// HTTP listener on port 80 — the shape the surveyed stacks write. It returns
// the Subnet, so a caller can build a second balancer on it.
func lbWithOneListener(t *testing.T, ts *httptest.Server, name string) string {
	t.Helper()
	doc := contractDoc(t)
	_, subnetID := netAndSubnet(t, ts, "10.61.0.0/16", "10.61.1.0/24")
	call(t, ts, doc, "CreateLoadBalancer",
		`{"LoadBalancerName":"`+name+`","LoadBalancerType":"internal","Subnets":["`+subnetID+`"],`+
			`"Listeners":[{"BackendPort":80,"BackendProtocol":"HTTP","LoadBalancerPort":80,"LoadBalancerProtocol":"HTTP"}]}`)
	return subnetID
}

// frontPorts is what the emulator says the balancer listens on, asked of the
// emulator rather than of a response the same call produced.
func frontPorts(t *testing.T, ts *httptest.Server, name string) []int {
	t.Helper()
	out := call(t, ts, contractDoc(t), "ReadLoadBalancers",
		`{"Filters":{"LoadBalancerNames":["`+name+`"]}}`)
	lbs, _ := out["LoadBalancers"].([]any)
	if len(lbs) != 1 {
		t.Fatalf("ReadLoadBalancers answered %d balancers named %s: %v", len(lbs), name, out)
	}
	lb, _ := lbs[0].(map[string]any)
	listeners, _ := lb["Listeners"].([]any)
	ports := make([]int, 0, len(listeners))
	for _, raw := range listeners {
		listener, _ := raw.(map[string]any)
		port, _ := listener["LoadBalancerPort"].(float64)
		ports = append(ports, int(port))
	}
	return ports
}

// The whole day-2 sequence the provider drives, in the order it drives it.
//
// Providers 1.1.3, 1.7.0 and 1.8.0 all delete the departing front ports before
// creating the arriving ones, so a single-listener port change passes through a
// balancer with no listener at all. Both halves are asserted against the
// emulator, and the second plan of the real client is what this stands in for.
func TestAListenerPortMovesThroughDeleteThenCreate(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)
	lbWithOneListener(t, ts, "day2-lb")

	out := call(t, ts, doc, "DeleteLoadBalancerListeners",
		`{"LoadBalancerName":"day2-lb","LoadBalancerPorts":[80]}`)
	if _, ok := out["LoadBalancer"].(map[string]any); !ok {
		t.Fatalf("DeleteLoadBalancerListeners answered no LoadBalancer: %v", out)
	}
	if got := frontPorts(t, ts, "day2-lb"); len(got) != 0 {
		t.Fatalf("the balancer still listens on %v after its only listener was deleted", got)
	}

	out = call(t, ts, doc, "CreateLoadBalancerListeners",
		`{"LoadBalancerName":"day2-lb","Listeners":[{"BackendPort":80,"BackendProtocol":"HTTP",`+
			`"LoadBalancerPort":8080,"LoadBalancerProtocol":"HTTP"}]}`)
	lb, ok := out["LoadBalancer"].(map[string]any)
	if !ok {
		t.Fatalf("CreateLoadBalancerListeners answered no LoadBalancer: %v", out)
	}
	// The response and the later read must agree: the provider stores the
	// response, and plans the next run against the read.
	listeners, _ := lb["Listeners"].([]any)
	if len(listeners) != 1 {
		t.Fatalf("the create answered %d listeners, want 1: %v", len(listeners), lb)
	}
	if got := frontPorts(t, ts, "day2-lb"); len(got) != 1 || got[0] != 8080 {
		t.Fatalf("the balancer listens on %v, want [8080]", got)
	}
}

// A second listener joins the first rather than replacing it.
func TestASecondListenerIsAddedBesideTheFirst(t *testing.T) {
	ts := newServer(t)
	lbWithOneListener(t, ts, "two-listeners")

	call(t, ts, contractDoc(t), "CreateLoadBalancerListeners",
		`{"LoadBalancerName":"two-listeners","Listeners":[{"BackendPort":8443,"BackendProtocol":"TCP",`+
			`"LoadBalancerPort":443,"LoadBalancerProtocol":"TCP"}]}`)

	got := frontPorts(t, ts, "two-listeners")
	if len(got) != 2 {
		t.Fatalf("the balancer listens on %v, want both 80 and 443", got)
	}
}

// One front port, one listener — on the add path and on the create path alike.
//
// The refusal is load-bearing rather than tidy: two listeners on one port are
// two runtime listeners on one port, which the balancer cannot build, so storing
// them would leave the API describing a balancer the runtime had refused.
//
// The wording is asserted too, and that is not decoration. Provider 1.1.3
// retries for five minutes on any error whose text contains "DuplicateListener"
// (resource_outscale_load_balancer.go:707), because on a real account the
// condition is transient. Here it never is, so the token must not appear or an
// accurate refusal becomes a five-minute hang.
func TestTwoListenersOnOnePortAreRefused(t *testing.T) {
	ts := newServer(t)
	subnetID := lbWithOneListener(t, ts, "dup-lb")

	status, out := post(t, ts, "CreateLoadBalancerListeners",
		`{"LoadBalancerName":"dup-lb","Listeners":[{"BackendPort":9000,"BackendProtocol":"HTTP",`+
			`"LoadBalancerPort":80,"LoadBalancerProtocol":"HTTP"}]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("adding a listener on a port already served answered %d, want 400: %v", status, out)
	}
	if got := frontPorts(t, ts, "dup-lb"); len(got) != 1 {
		t.Fatalf("the refused listener was stored anyway: %v", got)
	}
	if strings.Contains(errorDetails(out), "DuplicateListener") {
		t.Errorf("the refusal carries the token provider 1.1.3 retries on for five minutes: %v", out)
	}

	// The same rule on the create, because a rule enforced on one path only is
	// a rule the other path does not have.
	status, out = post(t, ts, "CreateLoadBalancer",
		`{"LoadBalancerName":"dup-at-create","LoadBalancerType":"internal","Subnets":["`+subnetID+`"],`+
			`"Listeners":[{"BackendPort":80,"BackendProtocol":"HTTP","LoadBalancerPort":80,"LoadBalancerProtocol":"HTTP"},`+
			`{"BackendPort":90,"BackendProtocol":"HTTP","LoadBalancerPort":80,"LoadBalancerProtocol":"HTTP"}]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("creating a balancer with two listeners on port 80 answered %d, want 400: %v", status, out)
	}
}

// A server certificate cannot arrive through the side door.
//
// CreateLoadBalancer refuses ServerCertificateId because the certificate family
// is declined; the add path takes the same ListenerForCreation and had to refuse
// it too, or the declined family would be reachable by another name.
func TestAListenerCannotSmuggleAServerCertificate(t *testing.T) {
	ts := newServer(t)
	lbWithOneListener(t, ts, "cert-lb")

	status, out := post(t, ts, "CreateLoadBalancerListeners",
		`{"LoadBalancerName":"cert-lb","Listeners":[{"BackendPort":8443,"BackendProtocol":"SSL",`+
			`"LoadBalancerPort":443,"LoadBalancerProtocol":"SSL",`+
			`"ServerCertificateId":"orn:ows:idauth::123456789012:server-certificate/x"}]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("a listener naming a server certificate answered %d, want 400: %v", status, out)
	}
	if got := frontPorts(t, ts, "cert-lb"); len(got) != 1 {
		t.Fatalf("the refused listener was stored anyway: %v", got)
	}
}

// A balancer that does not exist is a 404-shaped refusal, not a silent success.
func TestListenerOperationsOnAnUnknownBalancerAreRefused(t *testing.T) {
	ts := newServer(t)

	status, out := post(t, ts, "CreateLoadBalancerListeners",
		`{"LoadBalancerName":"nothing-here","Listeners":[{"BackendPort":80,"BackendProtocol":"HTTP",`+
			`"LoadBalancerPort":80,"LoadBalancerProtocol":"HTTP"}]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("adding a listener to an unknown balancer answered %d: %v", status, out)
	}

	status, out = post(t, ts, "DeleteLoadBalancerListeners",
		`{"LoadBalancerName":"nothing-here","LoadBalancerPorts":[80]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("deleting a listener from an unknown balancer answered %d: %v", status, out)
	}
}

// The arguments the SDK marks required are required.
func TestListenerOperationsRequireTheirArguments(t *testing.T) {
	ts := newServer(t)
	lbWithOneListener(t, ts, "args-lb")

	for _, tc := range []struct{ action, body string }{
		{"CreateLoadBalancerListeners", `{"LoadBalancerName":"args-lb","Listeners":[]}`},
		{"CreateLoadBalancerListeners", `{"Listeners":[{"BackendPort":80,"LoadBalancerPort":80,"LoadBalancerProtocol":"HTTP"}]}`},
		{"DeleteLoadBalancerListeners", `{"LoadBalancerName":"args-lb","LoadBalancerPorts":[]}`},
		{"DeleteLoadBalancerListeners", `{"LoadBalancerPorts":[80]}`},
		{"DeleteLoadBalancerListeners", `{"LoadBalancerName":"args-lb","LoadBalancerPorts":[70000]}`},
	} {
		status, out := post(t, ts, tc.action, tc.body)
		if status != http.StatusBadRequest {
			t.Errorf("%s %s answered %d, want 400: %v", tc.action, tc.body, status, out)
		}
	}
	if got := frontPorts(t, ts, "args-lb"); len(got) != 1 || got[0] != 80 {
		t.Fatalf("a refused request changed the balancer: %v", got)
	}
}

// errorDetails is the Details of the first error, which is where this pack's
// error writer puts it; reading them at the root finds "".
func errorDetails(answer map[string]any) string {
	errs, _ := answer["Errors"].([]any)
	if len(errs) == 0 {
		return ""
	}
	first, _ := errs[0].(map[string]any)
	details, _ := first["Details"].(string)
	return details
}

// A refused listener batch publishes nothing.
//
// Store.Update writes its draft back and notifies the observer for every
// callback that returns nil, so a refusal reported beside a nil return would
// announce "updated" for a balancer nothing changed. The watcher behind
// /_feint/events is that observer, and an event nobody can explain is the kind
// of noise that makes a change feed useless for diagnosing anything.
//
// The refusal travels as an error instead, which aborts the write and the
// notification together.
func TestARefusedListenerBatchPublishesNoEvent(t *testing.T) {
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, outscale.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	lbWithOneListener(t, ts, "quiet-lb")

	// Observed only from here, so the create's own events are not counted.
	var mu sync.Mutex
	var updates int
	env.Store.Observe(func(ev store.Event) {
		if ev.Action != store.EventUpdated {
			return
		}
		mu.Lock()
		updates++
		mu.Unlock()
	})

	status, out := post(t, ts, "CreateLoadBalancerListeners",
		`{"LoadBalancerName":"quiet-lb","Listeners":[{"BackendPort":9000,"BackendProtocol":"HTTP",`+
			`"LoadBalancerPort":80,"LoadBalancerProtocol":"HTTP"}]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("the duplicate port was accepted (%d), so this test measures nothing: %v", status, out)
	}

	mu.Lock()
	got := updates
	mu.Unlock()
	if got != 0 {
		t.Fatalf("a refused batch published %d update event(s) for a balancer nothing changed", got)
	}

	// The positive control, so a test that observes nothing at all cannot pass:
	// an accepted batch does publish.
	call(t, ts, contractDoc(t), "CreateLoadBalancerListeners",
		`{"LoadBalancerName":"quiet-lb","Listeners":[{"BackendPort":8443,"BackendProtocol":"TCP",`+
			`"LoadBalancerPort":443,"LoadBalancerProtocol":"TCP"}]}`)
	mu.Lock()
	got = updates
	mu.Unlock()
	if got == 0 {
		t.Fatal("an accepted batch published nothing either, so the observer is not wired and the check above is empty")
	}
}

// A dry run removes no listener.
//
// DeleteLoadBalancerListeners is a destructive path, and the pack's own history
// is why this is asserted rather than assumed: DryRun was once honoured per
// handler, and an audit ran `DeleteVms {"DryRun": true}` and watched the machine
// go. The wrapper now sits on every route, and a route that stopped going
// through p.route would lose it silently.
func TestADryRunRemovesNoListener(t *testing.T) {
	ts := newServer(t)
	lbWithOneListener(t, ts, "dryrun-lb")

	if status, out := post(t, ts, "DeleteLoadBalancerListeners",
		`{"LoadBalancerName":"dryrun-lb","LoadBalancerPorts":[80],"DryRun":true}`); status != http.StatusOK {
		t.Fatalf("a dry run should answer, not refuse: %d %v", status, out)
	}
	if got := frontPorts(t, ts, "dryrun-lb"); len(got) != 1 || got[0] != 80 {
		t.Fatalf("the dry run removed the listener: %v", got)
	}

	if status, out := post(t, ts, "CreateLoadBalancerListeners",
		`{"LoadBalancerName":"dryrun-lb","Listeners":[{"BackendPort":8443,"BackendProtocol":"TCP",`+
			`"LoadBalancerPort":443,"LoadBalancerProtocol":"TCP"}],"DryRun":true}`); status != http.StatusOK {
		t.Fatalf("a dry run should answer, not refuse: %d %v", status, out)
	}
	if got := frontPorts(t, ts, "dryrun-lb"); len(got) != 1 {
		t.Fatalf("the dry run added a listener: %v", got)
	}
}
