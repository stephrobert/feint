package replay_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/replay"
	"github.com/stephrobert/feint/internal/trace"
)

// What a replay needs to stop being an emulator-only tool (#359).
//
// The same comparison runs at this emulator, where a request costs nothing, and
// at a provider, where a POST is an invoice and a DELETE is somebody's property.
// The two seams below are what the second direction adds, and they are here
// rather than in the caller because both need the request *as it will go out* —
// after rebinding, which only this package knows.

// recording is one guard's log, and it is the assertion: what actually went out.
type recording struct {
	refuse    func(replay.Attempt) string
	attempted []replay.Attempt
	answered  []int
}

func (g *recording) Before(a replay.Attempt) string {
	g.attempted = append(g.attempted, a)
	if g.refuse == nil {
		return ""
	}
	return g.refuse(a)
}

func (g *recording) After(_ replay.Attempt, status int, _ any) {
	g.answered = append(g.answered, status)
}

// A request a guard refuses is not sent, and is neither a match nor a
// divergence. Three assertions rather than one: the verdict, the reason, and —
// the one that matters — that the endpoint never saw it.
func TestAGuardRefusalIsNotSentAndIsNotADivergence(t *testing.T) {
	var reached int
	server := answers(t, func(*http.Request) (int, string) {
		reached++
		return 201, `{"server":{"id":"` + freshID + `"}}`
	})
	guard := &recording{refuse: func(a replay.Attempt) string {
		if a.Method == http.MethodPost {
			return "this run does not create"
		}
		return ""
	}}

	rep, err := replay.Run(context.Background(),
		[]trace.Exchange{exchange(t, createLine(`{"server":{"id":"`+recordedID+`"}}`))},
		replay.Options{Endpoint: server.URL, Client: &http.Client{Timeout: 5 * time.Second},
			Table: table(t), Guard: guard})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if reached != 0 {
		t.Fatalf("a refused request reached the endpoint %d time(s)", reached)
	}
	if rep.RefusedCount != 1 || rep.Divergent != 0 || rep.Matched != 0 {
		t.Fatalf("a refusal was counted as something else: %+v", rep)
	}
	if rep.Results[0].Verdict != replay.Refused || rep.Results[0].Refused != "this run does not create" {
		t.Fatalf("the refusal does not carry the guard's own sentence: %+v", rep.Results[0])
	}
	if len(guard.answered) != 0 {
		t.Fatal("a refused request produced an answer to record")
	}
}

// The guard judges the request that will actually go out, not the one on disk.
//
// A recorded DELETE names the identifier the cloud minted last time; after
// rebinding it names whatever this run just created. A guard handed the recorded
// path would be asking about an object that exists nowhere, and would wave
// through the one request that can destroy something.
func TestAGuardSeesTheRequestAfterRebinding(t *testing.T) {
	server := answers(t, func(r *http.Request) (int, string) {
		if r.Method == http.MethodPost {
			return 201, `{"server":{"id":"` + freshID + `"}}`
		}
		return 200, `{"server":{"id":"` + freshID + `"}}`
	})
	guard := &recording{}
	if _, err := replay.Run(context.Background(), []trace.Exchange{
		exchange(t, createLine(`{"server":{"id":"`+recordedID+`"}}`)),
		exchange(t, getLine()),
	}, replay.Options{Endpoint: server.URL, Client: &http.Client{Timeout: 5 * time.Second},
		Table: table(t), Guard: guard}); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(guard.attempted) != 2 {
		t.Fatalf("the guard saw %d attempt(s), want 2", len(guard.attempted))
	}
	read := guard.attempted[1]
	if strings.Contains(read.Path, recordedID) {
		t.Fatalf("the guard was asked about the recorded identifier: %s", read.Path)
	}
	if !strings.Contains(read.Path, freshID) {
		t.Fatalf("the guard was not asked about the identifier this run created: %s", read.Path)
	}
	if guard.answered[0] != 201 {
		t.Fatalf("the guard was handed status %d for the create", guard.answered[0])
	}
}

// A seeded binding is sent before anything has been learned.
//
// Rebinding otherwise needs a read before the first write, and a recording that
// opens on a create has none: corpus/scaleway/terraform.jsonl's first line is a
// POST carrying a project identifier that belongs to no account the replay is
// pointed at. This is the half that makes the second direction work at all, and
// it was inert the first time round — the substitution was skipped whenever
// nothing had been *learned* yet, which on such a recording is forever.
func TestASeededBindingIsSentBeforeAnythingIsLearned(t *testing.T) {
	var got map[string]any
	server := answers(t, func(r *http.Request) (int, string) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		return 201, `{"server":{"id":"` + freshID + `"}}`
	})
	line := `{"method":"POST","path":"` + serverPath + `","operation":"instance/v1/API.CreateServer",` +
		`"status":201,"mounted":true,` +
		`"req":{"headers":{"Content-Type":"application/json"},"body":{"name":"web-1","project_id":"` + recordedID + `"}},` +
		`"res":{"headers":{"Content-Type":"application/json"},"body":{"server":{"id":"` + recordedID + `"}}}}`

	if _, err := replay.Run(context.Background(), []trace.Exchange{exchange(t, line)},
		replay.Options{Endpoint: server.URL, Client: &http.Client{Timeout: 5 * time.Second},
			Table: table(t), Bind: map[string]string{"project_id": freshIPOne}}); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if got["project_id"] != freshIPOne {
		t.Fatalf("the first request carried project_id %v, want the seeded one", got["project_id"])
	}
}

// A seed replaces an identifier, and leaves whatever else that field may carry
// alone.
//
// The seed's contract is "wherever the recording carries an identifier at this
// field name, send this one". A field is not always an identifier — Scaleway's
// own clients accept a project by name — and a seed that rewrote every value
// under the name would send a request the recording never made, which is the
// class of defect this whole direction exists to detect rather than to commit.
func TestASeedReplacesAnIdentifierAndNotWhateverElseTheFieldMayCarry(t *testing.T) {
	var got map[string]any
	server := answers(t, func(r *http.Request) (int, string) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		return 201, `{"server":{"id":"` + freshID + `"}}`
	})
	line := `{"method":"POST","path":"` + serverPath + `","operation":"instance/v1/API.CreateServer",` +
		`"status":201,"mounted":true,` +
		`"req":{"headers":{"Content-Type":"application/json"},"body":{"project_id":"my-project-alias"}},` +
		`"res":{"headers":{"Content-Type":"application/json"},"body":{"server":{"id":"` + recordedID + `"}}}}`

	if _, err := replay.Run(context.Background(), []trace.Exchange{exchange(t, line)},
		replay.Options{Endpoint: server.URL, Client: &http.Client{Timeout: 5 * time.Second},
			Table: table(t), Bind: map[string]string{"project_id": freshIPOne}}); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if got["project_id"] != "my-project-alias" {
		t.Fatalf("the seed rewrote a value that is not an identifier: %v", got["project_id"])
	}
}

// A seed with no field would apply where nothing else names a value — every
// path segment of every URL. Refused rather than ignored.
func TestASeedWithNoFieldIsRefused(t *testing.T) {
	for name, bind := range map[string]map[string]string{
		"no field": {"": freshID},
		"no value": {"project_id": ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := replay.Run(context.Background(), nil, replay.Options{
				Endpoint: "http://127.0.0.1:1", Client: &http.Client{Timeout: time.Second},
				Table: table(t), Bind: bind})
			if err == nil {
				t.Fatal("a half-spelled seed was accepted")
			}
		})
	}
}

func getLine() string {
	return `{"method":"GET","path":"` + serverPath + `/` + recordedID + `","operation":"instance/v1/API.GetServer",` +
		`"status":200,"mounted":true,` +
		`"res":{"headers":{"Content-Type":"application/json"},"body":{"server":{"id":"` + recordedID + `"}}}}`
}
