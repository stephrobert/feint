package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// What a stop is about to throw away, said at the moment it happens (#182).
//
// The store is memory and docs/limits.md says so in its lifecycle table, but
// that sentence lives on a page a user reads after being bitten. `restart` is
// the sharper case: an operator reaches for it mid-session and pays with the
// whole fixture.
//
// Every case below is one of that issue's "what must not happen", and the
// silent ones matter as much as the loud one: a warning on every healthy stop
// is the pattern people are trained to ignore.
func TestStopSaysWhatItIsAboutToDiscard(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		resources int
		answers   bool // does the health endpoint answer at all
		truncated bool // does it answer with a body that decodes only halfway
		want      string
	}{
		{
			name:      "resources held and no --state recorded",
			args:      []string{"serve", "--addr", "x", "--vm", "off"},
			resources: 4,
			answers:   true,
			want:      "discarding 4 resource(s)",
		},
		{
			name:      "--state recorded, so nothing is lost",
			args:      []string{"serve", "--addr", "x", "--state", "/tmp/s.json"},
			resources: 4,
			answers:   true,
			want:      "",
		},
		{
			name:      "--state=value spelled with an equals sign",
			args:      []string{"serve", "--addr", "x", "--state=/tmp/s.json"},
			resources: 4,
			answers:   true,
			want:      "",
		},
		{
			name:      "an empty store loses nothing",
			args:      []string{"serve", "--addr", "x"},
			resources: 0,
			answers:   true,
			want:      "",
		},
		{
			name:      "an instance that no longer answers is stopped as before",
			args:      []string{"serve", "--addr", "x"},
			resources: 4,
			answers:   false,
			want:      "",
		},
		{
			// The case that makes the error branch load-bearing rather than
			// decorative. A truncated body decodes far enough to set the count
			// and then fails: trusting it would accuse a stop of discarding
			// four resources on the word of a response that never arrived
			// whole. Found by falsifying — the first version of this test used
			// a 500, whose empty body leaves the count at zero, so the
			// zero-check caught it and the error branch proved nothing.
			name:      "a truncated answer is not trusted for its count",
			args:      []string{"serve", "--addr", "x"},
			resources: 4,
			truncated: true,
			answers:   true,
			want:      "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if !tc.answers {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				if tc.truncated {
					_, _ = fmt.Fprintf(w, `{"status": "ok", "resources": %d`, tc.resources)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status": "ok", "resources": tc.resources,
				})
			}))
			t.Cleanup(ts.Close)

			inst := &Instance{Addr: strings.TrimPrefix(ts.URL, "http://"), Args: tc.args}
			got := discardNotice(inst)

			switch {
			case tc.want == "" && got != "":
				t.Errorf("nothing is being lost here, so the stop must stay quiet; got %q", got)
			case tc.want != "" && !strings.Contains(got, tc.want):
				t.Errorf("the notice must name what is lost.\n got: %q\nwant substring: %q", got, tc.want)
			}
			if got != "" && !strings.Contains(got, "snapshot save") {
				t.Errorf("the notice must name the way out, not only the loss: %q", got)
			}
		})
	}
}
