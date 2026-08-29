package cli

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/environment"
)

// `service:<name>:<port>` (#600), and the night that asked for it.
//
// On 2026-08-29 (run 33229486194) the Scaleway stack declared four ready
// conditions — one route and three counts, all control plane — `feint up`
// confirmed every one of them, and 310 ms later its web machine was listening on
// 53 and not on 443. `up` did not lie: the vocabulary could not say what the
// stack needed, so every suite downstream guessed, at its own moment and its own
// budget.
//
// These drive `waitReady` against an inventory that answers exactly what the
// emulator answers, and against a socket that really accepts — the same shape as
// up_wait_test.go, for the reason its header states: the decision is tested
// apart from the act.

// declWithMachines is declFor with a runtime declared. A service condition does
// not apply under `runtime.mode: off`, so a test that used declFor would skip
// the very thing it measures — which is how the first version of this file went
// green for the wrong reason before the announcement assertion caught it.
func declWithMachines(t *testing.T, addr string, ready ...string) *environment.File {
	t.Helper()
	src := "version: 1\nemulator:\n  addr: " + addr + "\nruntime:\n  mode: incus-ovn\nready:\n"
	for _, item := range ready {
		src += "  - " + item + "\n"
	}
	decl, err := environment.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return decl
}

// inventoryOf serves one resource under both naming shapes, so a single helper
// covers Scaleway's `attrs.name` and Outscale's `Name` tag.
func inventoryOf(t *testing.T, shape, name, address string) *httptest.Server {
	t.Helper()
	var attrs string
	switch shape {
	case "name":
		attrs = fmt.Sprintf(`{"name":%q}`, name)
	case "tag":
		attrs = fmt.Sprintf(`{"Tags":[{"Key":"Other","Value":"x"},{"Key":"Name","Value":%q}]}`, name)
	default:
		t.Fatalf("unknown shape %q", shape)
	}
	runtime := `{}`
	if address != "" {
		runtime = fmt.Sprintf(`{"machine":"feint-scw-1","address":%q}`, address)
	}
	body := fmt.Sprintf(`{"count":1,"resources":[{"kind":"instance/server","attrs":%s,"runtime":%s}]}`,
		attrs, runtime)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_feint/resources" {
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// listener opens a real socket and answers its address, so the condition is
// proved against something that accepts rather than against a mock that says it
// would.
func listener(t *testing.T) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

// The accepting half. Both naming shapes, because a reader that resolved only
// one would make the condition a Scaleway feature.
func TestAServiceConditionIsMetWhenTheNamedMachineAccepts(t *testing.T) {
	for _, shape := range []string{"name", "tag"} {
		t.Run(shape, func(t *testing.T) {
			host, port := listener(t)
			srv := inventoryOf(t, shape, "platform-web-0", host)

			decl := declWithMachines(t, strings.TrimPrefix(srv.URL, "http://"),
				fmt.Sprintf("service:platform-web-0:%d", port))
			var out bytes.Buffer
			if _, err := waitReady(decl, 5*time.Second, &out); err != nil {
				t.Fatalf("a machine that accepts was reported not ready: %v\n%s", err, out.String())
			}
			if !strings.Contains(out.String(), "platform-web-0 answers on port") {
				t.Errorf("the wait never announced what it was waiting for:\n%s", out.String())
			}
		})
	}
}

// The refusing half, and it is the one that carries the night. A machine whose
// unit has not started must NOT be reported ready, and the failure must name the
// address it dialled — the difference between "the workload is late" and "the
// emulator is broken".
func TestAServiceConditionIsNotMetWhileTheUnitIsStillComingUp(t *testing.T) {
	// A port nothing listens on: the socket is opened to reserve the number and
	// closed at once, so the dial is refused rather than merely slow.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	closed := ln.Addr().(*net.TCPAddr)
	_ = ln.Close()

	srv := inventoryOf(t, "name", "platform-web-0", closed.IP.String())
	decl := declWithMachines(t, strings.TrimPrefix(srv.URL, "http://"),
		fmt.Sprintf("service:platform-web-0:%d", closed.Port))

	var out bytes.Buffer
	started := time.Now()
	_, err = waitReady(decl, 300*time.Millisecond, &out)
	if err == nil {
		t.Fatal("a machine listening on nothing was reported ready, which is the 2026-08-29 night")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("the wait took %s for a 300ms deadline: it hangs rather than fails", elapsed)
	}
	if !strings.Contains(err.Error(), "does not accept a connection") {
		t.Errorf("the failure never says what it dialled: %v", err)
	}
}

// A name no resource carries is not "not yet". It is a declaration and a stack
// that have drifted apart, and the reason must say so rather than read as a slow
// boot forever.
func TestAServiceConditionNamingNobodySaysSo(t *testing.T) {
	srv := inventoryOf(t, "name", "platform-app-0", "10.0.0.9")
	decl := declWithMachines(t, strings.TrimPrefix(srv.URL, "http://"), "service:platform-web-0:443")

	var out bytes.Buffer
	_, err := waitReady(decl, 300*time.Millisecond, &out)
	if err == nil {
		t.Fatal("a condition naming no resource was reported met")
	}
	if !strings.Contains(err.Error(), "no resource named \"platform-web-0\"") {
		t.Errorf("the failure never names what it could not find: %v", err)
	}
}

// The addresses are taken by TYPE and not by key. The packs agree on "address"
// today, but that agreement is a Binding field, so a reader keyed on the
// spelling would write into the CLI a convention the core left to each pack.
// Here the record holds the machine name, a joined pair of addresses and prose:
// only the two addresses may be dialled, and the prose must never be.
func TestTheAddressesAreReadByTypeAndNotByKey(t *testing.T) {
	host, port := listener(t)
	body := fmt.Sprintf(`{"count":1,"resources":[{"kind":"instance/server",`+
		`"attrs":{"name":"platform-web-0"},`+
		`"runtime":{"machine":"feint-scw-1","published":"10.0.0.254, %s","note":"never dialled"}}]}`,
		host)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_feint/resources" {
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	decl := declWithMachines(t, strings.TrimPrefix(srv.URL, "http://"),
		fmt.Sprintf("service:platform-web-0:%d", port))
	var out bytes.Buffer
	if _, err := waitReady(decl, 5*time.Second, &out); err != nil {
		t.Fatalf("an address under an unconventional key was not dialled: %v\n%s", err, out.String())
	}
}

// The inapplicable case, and it must be LOUD. Under `runtime.mode: off` there is
// no machine, so the question does not apply — but a condition that passed in
// silence would read, in a journal six weeks later, exactly like a condition
// that was measured and held.
//
// This is what lets examples/stacks/scaleway declare one and still run in both
// modes, which its own main.tf says an example has to do.
func TestAServiceConditionDoesNotApplyWithoutMachinesAndSaysSo(t *testing.T) {
	srv := inventoryOf(t, "name", "platform-web-0", "")
	// declFor, deliberately: no runtime section, so the mode is `off`.
	decl := declFor(t, strings.TrimPrefix(srv.URL, "http://"), "service:platform-web-0:443")

	var out bytes.Buffer
	if _, err := waitReady(decl, 300*time.Millisecond, &out); err != nil {
		t.Fatalf("a condition about machines failed an environment that starts none: %v", err)
	}
	if !strings.Contains(out.String(), "not asked") {
		t.Errorf("the skip was silent, so a reader cannot tell it from a proof:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "service:platform-web-0:443") {
		t.Errorf("the skip does not name what was not asked:\n%s", out.String())
	}
	if strings.Contains(out.String(), "ok: service:") {
		t.Errorf("a condition nothing evaluated was reported met:\n%s", out.String())
	}
}

// The forms, refused by name at load rather than at the end of a wait (#190).
func TestAServiceConditionThatDoesNotReadIsRefusedAtLoad(t *testing.T) {
	for _, raw := range []string{
		"service:platform-web-0",
		"service::443",
		"service:platform-web-0:",
		"service:platform-web-0:0",
		"service:platform-web-0:65536",
		"service:platform-web-0:https",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := environment.ParseCondition(raw); err == nil {
				t.Fatalf("accepted, so `up` would wait on a condition nothing evaluates")
			}
		})
	}
	got, err := environment.ParseCondition("service:platform-web-0:443")
	if err != nil {
		t.Fatalf("the form was refused: %v", err)
	}
	if got.Name != "platform-web-0" || got.Port != 443 {
		t.Errorf("parsed as %+v", got)
	}
}
