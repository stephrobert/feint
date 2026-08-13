package emulator_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// The round trip against the real handlers, which no other test covered.
//
// `feint snapshot` is tested against an httptest server that imitates the
// emulator, so it proves the client and not the contract between them. The
// claim being made — that a snapshot is exactly what `serve --state` writes, and
// that the two are interchangeable by construction — is a claim about these two
// handlers and the store behind them. It has to be tested here or it is a
// promise.
func TestStateRoundTripsThroughTheRealHandlers(t *testing.T) {
	env := emulator.DefaultEnv()
	env.Store.Put(&resource.Resource{
		ID:     "11111111-1111-1111-1111-111111111111",
		Kind:   "server",
		Tenant: resource.Tenant{Provider: "scaleway", Project: "p"},
		State:  "running",
		Attrs:  map[string]any{"name": "demo"},
	})
	srv, err := emulator.NewServer(env)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	res, err := ts.Client().Get(ts.URL + "/_feint/state")
	if err == nil {
		defer func() { _ = res.Body.Close() }()
	}
	if err != nil {
		t.Fatalf("read the state: %v", err)
	}
	body := readAll(t, res)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("read answered %d: %s", res.StatusCode, body)
	}
	if !bytes.Contains(body, []byte("demo")) {
		t.Fatalf("the snapshot does not carry the resource:\n%s", body)
	}

	// Emptied, then restored from the bytes the emulator itself produced.
	//
	// The envelope is not decoration here: a bare `[]` is refused since #133,
	// because a file with no format header cannot be shown to be complete.
	empty := `{"format":"feint-snapshot","version":1,"resources":[]}`
	if err := env.Store.Restore(strings.NewReader(empty)); err != nil {
		t.Fatalf("empty the store: %v", err)
	}
	if env.Store.Len() != 0 {
		t.Fatalf("the store still holds %d resources", env.Store.Len())
	}

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/_feint/state", bytes.NewReader(body))
	res, err = ts.Client().Do(req)
	if err == nil {
		defer func() { _ = res.Body.Close() }()
	}
	if err != nil {
		t.Fatalf("write the state: %v", err)
	}
	answer := struct {
		Status    string `json:"status"`
		Resources int    `json:"resources"`
	}{}
	if err := json.Unmarshal(readAll(t, res), &answer); err != nil {
		t.Fatalf("decode the answer: %v", err)
	}
	if res.StatusCode != http.StatusOK || answer.Resources != 1 {
		t.Fatalf("write answered %d with %d resources", res.StatusCode, answer.Resources)
	}
	if env.Store.Len() != 1 {
		t.Fatalf("the store holds %d resources after the restore", env.Store.Len())
	}
	if _, ok := env.Store.Get("scaleway", "server", "11111111-1111-1111-1111-111111111111"); !ok {
		t.Fatal("the restored resource is not the one that was snapshotted")
	}
}

// A body that is not a snapshot must be refused with a reason, not accepted
// into a store the next request reads.
func TestStateRefusesABodyItCannotRead(t *testing.T) {
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/_feint/state", strings.NewReader("{not json"))
	res, err := ts.Client().Do(req)
	if err == nil {
		defer func() { _ = res.Body.Close() }()
	}
	if err != nil {
		t.Fatalf("write the state: %v", err)
	}
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("answered %d, want 400", res.StatusCode)
	}
	if !bytes.Contains(readAll(t, res), []byte("error")) {
		t.Fatal("the refusal carries no reason")
	}
}

// The size guard: a body past the limit is refused rather than decoded into
// memory. SECURITY.md declares a crash reachable from a request in scope, and
// this is the one endpoint that reads an unbounded body.
func TestStateRefusesAnOversizedBody(t *testing.T) {
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// One byte past the limit, as a JSON array that would otherwise decode.
	oversized := bytes.NewBuffer(make([]byte, 0, 64<<20+64))
	oversized.WriteString(`[{"id":"`)
	oversized.Write(bytes.Repeat([]byte("a"), 64<<20))
	oversized.WriteString(`"}]`)

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/_feint/state", bytes.NewReader(oversized.Bytes()))
	res, err := ts.Client().Do(req)
	if err == nil {
		defer func() { _ = res.Body.Close() }()
	}
	if err != nil {
		// A refused body can surface as a transport error rather than a status,
		// which is an acceptable outcome for this test: what matters is that the
		// store was not replaced.
		if env.Store.Len() != 0 {
			t.Fatalf("the oversized body reached the store: %d resources", env.Store.Len())
		}
		return
	}
	if res.StatusCode == http.StatusOK {
		t.Fatal("an oversized body was accepted")
	}
	if env.Store.Len() != 0 {
		t.Fatalf("the oversized body reached the store: %d resources", env.Store.Len())
	}
}

// readAll does not close the body: the caller does, right where it opened it.
// Closing here instead hid the release from bodyclose, which cannot see through
// a call — and from a reader, who had to open this function to learn whether the
// response they were holding was already spent.
func readAll(t *testing.T, res *http.Response) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(res.Body); err != nil {
		t.Fatalf("read the body: %v", err)
	}
	return buf.Bytes()
}
