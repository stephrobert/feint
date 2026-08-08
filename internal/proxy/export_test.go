package proxy

import (
	"net/http"
	"net/http/httptest"
	"time"
)

// SampleRecord builds one [Redacted] for the tests that exercise the writer
// rather than the recording.
//
// It lives here, in a file the compiler includes only when testing, precisely
// because [Redacted] has no exported constructor: outside a test there is still
// exactly one way to obtain one and it redacts. A helper of this shape in
// production code would be the second path to the writer that the type exists to
// prevent, so it must not be one.
//
// It goes through capture rather than building the struct directly, so a test of
// the writer is fed the same thing the proxy feeds it.
func SampleRecord() Redacted {
	return capture(seen{
		at:        time.Now().UTC(),
		elapsed:   time.Millisecond,
		req:       httptest.NewRequest(http.MethodGet, "/v2/zone", nil),
		reqBody:   &body{max: DefaultMaxBody},
		status:    http.StatusOK,
		resHeader: http.Header{},
		resBody:   &body{max: DefaultMaxBody},
	})
}
