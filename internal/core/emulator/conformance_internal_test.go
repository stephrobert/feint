package emulator

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stephrobert/feint/internal/contract"
)

// A response the API does not describe as JSON is not compared as JSON.
//
// GetServerUserData answers the raw cloud-init blob a client stored, text/plain,
// and decoding it as JSON reported a violation on a route behaving exactly as its
// own API describes. It surfaced the first time a real client drove the route
// (#174): the probe sends and receives JSON, so the check and the traffic had
// been agreeing with each other and with nothing else.
func TestANonJSONResponseIsNotComparedAsJSON(t *testing.T) {
	o := newObserver(map[string]*contract.Doc{}, nil, &faultSet{}, nil)
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "text/plain")
	rec.WriteHeader(http.StatusOK)
	_, _ = rec.WriteString("#cloud-config\npackages:\n  - htop\n")

	recorded := &recorder{ResponseWriter: rec, status: http.StatusOK, body: bytes.NewBufferString("#cloud-config\n")}
	if vs := o.check(&contract.Doc{}, "instance/v1/API.GetServerUserData", recorded, false); len(vs) != 0 {
		t.Errorf("a text/plain body was compared as JSON: %v", vs)
	}

	// And a JSON body still is, or the guard would excuse every response by
	// leaving the header off.
	jsonRec := httptest.NewRecorder()
	jsonRec.Header().Set("Content-Type", "application/json")
	jsonRec.WriteHeader(http.StatusOK)
	recorded = &recorder{ResponseWriter: jsonRec, status: http.StatusOK, body: bytes.NewBufferString("not json at all")}
	if vs := o.check(&contract.Doc{}, "instance/v1/API.GetServerUserData", recorded, false); len(vs) == 0 {
		t.Error("a body announced as JSON and not parseable was accepted")
	}
}
