package proxy

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode"
)

// A host a client named cannot forge a line in the proxy's log.
//
// CodeQL reported go/log-injection twice on refuse (alerts 35 and 36, read
// live on 2026-09-06): the host off the CONNECT line reaches the record, and
// so does the recipe built around it. net/http refuses a malformed Host before
// the handler runs, so this is the second wall; it is measured here at the
// value the proxy hands to the logger, read through ReplaceAttr before the
// handler quotes it — slog.TextHandler would quote a newline on its own and a
// test reading its output would stay green with the sanitiser removed.
// tools/falsify/specs/a-log-line-cannot-be-forged.json removes the call and
// requires this test to go red.
func TestARefusedHostCannotForgeALogLine(t *testing.T) {
	var mu sync.Mutex
	var seen []slog.Attr
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, a)
			return a
		},
	}))
	f := &Forward{log: log}

	forged := "evil.example\nlevel=ERROR msg=\"the proxy was compromised\" attacker=yes"
	f.refuse(httptest.NewRecorder(), forged)

	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("nothing was logged, so nothing was measured")
	}
	readable := false
	for _, a := range seen {
		v := a.Value.String()
		if i := strings.IndexFunc(v, unicode.IsControl); i >= 0 {
			t.Errorf("attribute %s reached the logger carrying a control character at %d: a handler "+
				"that does not quote writes it as a record of its own\n%s=%q", a.Key, i, a.Key, v)
		}
		if strings.Contains(v, "evil.example") {
			readable = true
		}
	}
	if !readable {
		t.Error("no attribute names evil.example: the host was hidden where an operator needs to read it")
	}
}
