package outscale_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/outscale"
)

// The two pointing mistakes this pack owns, answered in its own dialect (#179).
//
// The first was the worst answer in the whole emulator, because it was
// confident and wrong: given `/api/v1/api/v1/CreateVms` it replied "feint does
// not serve api/v1/CreateVms" — and `CreateVms` *is* served. A team whose first
// oapi-cli call reads that concludes the coverage table lied, when what happened
// is that their endpoint carries the prefix and the CLI appended it again.
//
// Both stay 404 and both stay in the Outscale error envelope, because clients
// parse it. Only the sentence changes.
func TestAMispointedOutscaleClientIsToldWhichSideIsWrong(t *testing.T) {
	ts := outscaleServer(t)

	for _, tc := range []struct {
		name    string
		path    string
		wants   []string
		refuses string
	}{
		{
			name: "the endpoint carries the prefix and the client appended it again",
			path: "/api/v1/api/v1/CreateVms",
			// The remedy must name the flag: since #286 the flagless default
			// prints the path'd shape for the Terraform provider >= 1.7, so
			// pointing this client at it would recreate the very request
			// this error is answering.
			wants: []string{"twice", "bare host", "feint env outscale --client oapi-cli"},
			// The confident, wrong answer: it must not name the operation as
			// unserved, because it is served.
			refuses: "does not serve",
		},
		{
			name: "the deprecated osc-cli addresses another API version",
			path: "/api/latest/ReadVms",
			// The remedy must name the client this project actually drives, and
			// that is octl since #460: outscale/oapi-cli is archived upstream, so
			// sending a reader there would be sending them to a dead CLI.
			wants: []string{"osc-cli", "deprecated", "octl"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := postText(t, ts, tc.path)
			if status != http.StatusNotFound {
				t.Errorf("the request stays refused: got %d, want 404", status)
			}
			for _, want := range tc.wants {
				if !strings.Contains(body, want) {
					t.Errorf("the answer never mentions %q:\n%s", want, body)
				}
			}
			if tc.refuses != "" && strings.Contains(body, tc.refuses) {
				t.Errorf("the answer still says %q, which is the confident wrong "+
					"sentence this replaces:\n%s", tc.refuses, body)
			}
			// The envelope survives: a client decodes an API error rather than
			// choking on text/plain.
			if !strings.Contains(body, "OperationNotEmulated") {
				t.Errorf("the Outscale error envelope was lost:\n%s", body)
			}
		})
	}
}

// And an action that genuinely is not served keeps the answer it always had.
// A hint that fired on everything would bury the two cases above.
func TestAnActionThatIsGenuinelyUnservedStillSaysSo(t *testing.T) {
	ts := outscaleServer(t)
	// CreateNetAccessPoint rather than CreateNetPeering, which this test used
	// while the peering family was untriaged: it is served now, and the
	// specimen has to move to something that stays on the work list (#172).
	status, body := postText(t, ts, "/api/v1/CreateNetAccessPoint")
	if status != http.StatusNotFound {
		t.Errorf("got %d, want 404", status)
	}
	if !strings.Contains(body, "does not serve CreateNetAccessPoint") {
		t.Errorf("an unserved action must still be named as unserved:\n%s", body)
	}
}

func outscaleServer(t *testing.T) *httptest.Server {
	t.Helper()
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, outscale.New(env))
	if err != nil {
		t.Fatalf("mount the outscale pack: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// postText returns the body as text: the package's own `post` decodes JSON,
// and what these assertions are about is the sentence a human reads.
func postText(t *testing.T, ts *httptest.Server, path string) (int, string) {
	t.Helper()
	resp, err := ts.Client().Post(ts.URL+path, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, sb.String()
}
