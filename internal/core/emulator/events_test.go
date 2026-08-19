package emulator_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/scaleway"
	"github.com/stephrobert/feint/internal/trace"
)

// drive and post make one call and release the connection. The bodies are of no
// interest here — the log is what is being measured — but a response left open
// holds a connection, and the slow-reader test below makes several hundred.
func drive(t *testing.T, url string) {
	t.Helper()
	resp, err := http.Get(url) //nolint:noctx // a test against a local httptest server
	if err != nil {
		t.Fatalf("drive the emulator: %v", err)
	}
	_ = resp.Body.Close()
}

func post(t *testing.T, url, body string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body)) //nolint:noctx // same
	if err != nil {
		t.Fatalf("drive the emulator: %v", err)
	}
	_ = resp.Body.Close()
}

// traceOf reads the ring through the endpoint a script would use.
func traceOf(t *testing.T, handler http.Handler) []trace.Exchange {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_feint/trace", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("the trace answered %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Count     int              `json:"count"`
		Kept      int              `json:"kept"`
		Exchanges []trace.Exchange `json:"exchanges"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode the trace: %v", err)
	}
	if out.Count != len(out.Exchanges) {
		t.Errorf("the trace says %d exchanges and carries %d", out.Count, len(out.Exchanges))
	}
	if out.Kept == 0 {
		t.Error("the trace does not say how many it keeps, so a script cannot know what window it is reading")
	}
	return out.Exchanges
}

// The sequence a client walks, in order, with what each call was not read for.
//
// This is the whole feature. The conformance counters say how many times an
// operation was called and nothing about when, in what order, or with what
// answer — so the working loop of this project, "run the client and find which
// route died", was an hour of tcpdump. The sequence below is the one the
// Scaleway suite drives before it creates anything, which is the trap CLAUDE.md
// records under "décliner le catalogue casse le CLI".
func TestTheLogKeepsTheOrderAClientWalked(t *testing.T) {
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, scaleway.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	handler := srv.Handler()

	drive := func(method, path, body string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)
	}

	zone := "/instance/v1/zones/fr-par-1"
	drive(http.MethodGet, zone+"/products/servers", "")
	drive(http.MethodGet, "/marketplace/v2/local-images", "")
	// One field the handler does not declare, on a call that succeeds. This is
	// the line the log exists for. The field is real upstream
	// (CreateServerRequest.AdminPasswordEncryptionSSHKeyID) and read by no
	// handler here; placement_group played this role until #285 served it.
	drive(http.MethodPost, zone+"/servers", `{"name":"demo","commercial_type":"DEV1-S","admin_password_encryption_ssh_key_id":"nope"}`)
	drive(http.MethodGet, zone+"/servers", "")
	// And one route nobody mounted, in the middle of the walk.
	drive(http.MethodPost, "/instance/v1/zones/fr-par-1/does-not-exist", `{}`)

	exchanges := traceOf(t, handler)
	if len(exchanges) != 5 {
		t.Fatalf("the log kept %d of 5 calls: %+v", len(exchanges), exchanges)
	}

	// Order, and it is the point: a set would not have caught the defect this
	// feature exists to find.
	wantPaths := []string{
		zone + "/products/servers",
		"/marketplace/v2/local-images",
		zone + "/servers",
		zone + "/servers",
		zone + "/does-not-exist",
	}
	for i, want := range wantPaths {
		if exchanges[i].Path != want {
			t.Errorf("call %d was %s, want %s", i, exchanges[i].Path, want)
		}
		if exchanges[i].Seq != int64(i+1) {
			t.Errorf("call %d carries seq %d; a reader cannot detect a gap", i, exchanges[i].Seq)
		}
		if exchanges[i].At.IsZero() {
			t.Errorf("call %d carries no time", i)
		}
	}

	created := exchanges[2]
	if created.Operation == "" || created.Provider == "" {
		t.Errorf("the create carries no operation or provider: %+v", created)
	}
	if created.Status != http.StatusCreated {
		t.Errorf("the create answered %d, want 201", created.Status)
	}
	if len(created.Unread) == 0 {
		t.Error("a field no handler read was not reported on the line that carried it, " +
			"which is the one signal here that names a causal defect")
	}

	// What does not exist reads as what does not exist, rather than as a bare
	// 404 the reader has to interpret.
	missing := exchanges[4]
	if missing.Mounted {
		t.Error("a path no route serves is recorded as mounted")
	}
	if missing.Operation != "" {
		t.Errorf("an unmounted path was attributed to %q", missing.Operation)
	}
	// The status is the pack's own dialect for "no such operation" — this one
	// answers 501, deliberately, so an SDK meets an API error rather than
	// net/http's text/plain. What the log has to carry is that status, whatever
	// it is, and the fact that nothing served the path.
	if missing.Status < 400 {
		t.Errorf("the unmounted path was recorded as answering %d", missing.Status)
	}
}

// The log is about the clients, never about the page reading it.
//
// The page polls four /_feint endpoints every two seconds. Recording those would
// put several entries per second into a 256-entry ring, so a client's calls
// would fall out of it within a minute of a tab being left open — and the log
// would grow busy on an emulator no client had ever touched.
func TestTheLogIgnoresTheEmulatorsOwnEndpoints(t *testing.T) {
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, scaleway.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	handler := srv.Handler()

	for _, path := range []string{
		"/_feint/health", "/_feint/routes", "/_feint/conformance",
		"/_feint/resources", "/_feint/trace", "/_feint/nothing-here",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	}
	if held := traceOf(t, handler); len(held) != 0 {
		t.Fatalf("the log recorded the emulator's own endpoints: %+v", held)
	}

	// The accepting half: a provider path still lands. A filter that dropped
	// everything would pass the assertion above and make the whole feature a
	// blank panel.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/instance/v1/zones/fr-par-1/servers", nil))
	if held := traceOf(t, handler); len(held) != 1 {
		t.Fatalf("a client call was dropped with the emulator's own: %+v", held)
	}
}

// The ring is bounded, and says so by leaving a gap in the sequence.
//
// An emulator left running overnight under a loop of applies must not grow
// without end. The sequence numbers are what makes the bound honest: a reader
// that sees 300 follow 44 knows entries were dropped, where a silently truncated
// list would read as a complete one.
func TestTheLogIsBoundedAndSaysWhereItCut(t *testing.T) {
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, scaleway.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	handler := srv.Handler()

	const drives = 300
	for i := 0; i < drives; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/instance/v1/zones/fr-par-1/servers", nil))
	}

	held := traceOf(t, handler)
	if len(held) != 256 {
		t.Fatalf("the log kept %d entries, want the ring's 256", len(held))
	}
	if held[0].Seq != drives-256+1 {
		t.Errorf("the oldest kept entry is seq %d, want %d: the ring dropped the wrong end", held[0].Seq, drives-256+1)
	}
	if held[len(held)-1].Seq != drives {
		t.Errorf("the newest entry is seq %d, want %d", held[len(held)-1].Seq, drives)
	}
}

// The stream carries what the ring already holds, then what happens next.
//
// A page opened after the interesting call must still show it, and a call made
// while the page is open must arrive without a refresh. Both halves, because
// either alone is a stream that looks like it works.
func TestTheStreamReplaysTheRingThenFollowsLive(t *testing.T) {
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, scaleway.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	if !srv.MountUI(emulator.UI{Addr: "127.0.0.1:4599"}) {
		t.Fatal("the page was not mounted")
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// One call before anybody is listening.
	drive(t, ts.URL+"/instance/v1/zones/fr-par-1/servers")

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/_feint/events", nil)
	if err != nil {
		t.Fatalf("build the stream request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open the stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("the stream answered Content-Type %q", ct)
	}

	lines := bufio.NewScanner(resp.Body)
	next := func() trace.Exchange {
		t.Helper()
		for lines.Scan() {
			line := lines.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var x trace.Exchange
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &x); err != nil {
				t.Fatalf("decode a streamed exchange: %v", err)
			}
			return x
		}
		t.Fatal("the stream ended before an exchange arrived")
		return trace.Exchange{}
	}

	if first := next(); first.Seq != 1 {
		t.Errorf("the stream opened on seq %d; a page opened after the fact sees nothing", first.Seq)
	}

	// And now one made while the stream is open.
	post(t, ts.URL+"/instance/v1/zones/fr-par-1/servers", `{"name":"live","commercial_type":"DEV1-S"}`)
	live := next()
	if live.Method != http.MethodPost || live.Seq != 2 {
		t.Errorf("the live call arrived as %+v", live)
	}
}

// The stream survives the write timeout the server sets for everything else.
//
// `serve` sets WriteTimeout on the http.Server, which is right for every
// response this emulator produces and wrong for the one that never ends: it
// would cut the page's stream after the timeout, for as long as the tab stayed
// open. Clearing the deadline is scoped to this handler, and this test is the
// only thing that can tell whether it was done — the symptom otherwise is a
// browser silently reconnecting every minute, which looks like nothing at all.
func TestTheStreamOutlivesTheServersWriteTimeout(t *testing.T) {
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, scaleway.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	if !srv.MountUI(emulator.UI{Addr: "127.0.0.1:4599"}) {
		t.Fatal("the page was not mounted")
	}

	ts := httptest.NewUnstartedServer(srv.Handler())
	// Short enough to keep the test under a second, long enough that the
	// connection is genuinely idle across it. serve uses 60s for the same
	// reason: a response that is not a stream must not be held open.
	ts.Config.WriteTimeout = 250 * time.Millisecond
	ts.Start()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/_feint/events")
	if err != nil {
		t.Fatalf("open the stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Idle past the deadline, then make a call. Without the cleared deadline the
	// write below fails inside the handler, the connection closes, and the read
	// returns EOF instead of the exchange.
	time.Sleep(600 * time.Millisecond)
	drive(t, ts.URL+"/instance/v1/zones/fr-par-1/servers")

	done := make(chan trace.Exchange, 1)
	go func() {
		lines := bufio.NewScanner(resp.Body)
		for lines.Scan() {
			line := lines.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var x trace.Exchange
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &x) == nil {
				done <- x
				return
			}
		}
		close(done)
	}()

	select {
	case x, ok := <-done:
		if !ok {
			t.Fatal("the stream was cut by the server's write timeout: a page would reconnect every minute")
		}
		if x.Seq == 0 {
			t.Errorf("an exchange arrived without a sequence: %+v", x)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing arrived on the stream")
	}
}
