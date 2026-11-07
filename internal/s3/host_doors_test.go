package s3

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func hostFixture(t *testing.T) *Doors {
	t.Helper()
	_, store := fixture(t)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("control-witness"))
	})
	doors, err := MountHosts(next, store, []Profile{
		{Provider: "scaleway", Region: "fr-par", MissingBucket: ScalewayMissingBucket},
		{Provider: "exoscale", Region: "ch-gva-2", MissingBucket: ExoscaleMissingBucket},
		{Provider: "exoscale", Region: "ch-dk-2", MissingBucket: ExoscaleMissingBucket},
	}, 512, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return doors
}

func TestHostDoorsSharePrefixBytesWithoutCollapsingNamespaces(t *testing.T) {
	doors := hostFixture(t)
	ts := httptest.NewServer(doors.Handler("127.0.0.1:4599"))
	defer ts.Close()
	proxy, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
	defer client.CloseIdleConnections()
	wireKey := "folder//a/../%2F%252F%3F%23"
	for i, door := range doors.Snapshot().Doors {
		if door.Host != DoorHost(door.Provider, door.Region) {
			t.Fatal("host report lost its explicit namespace:", door)
		}
		target := "http://" + door.Host + "/bucket"
		doorRequest(t, client, "PUT", target, "", 200)
		payload := strings.Repeat("host", i+1)
		doorRequest(t, client, "PUT", target+"/"+wireKey, payload, 200)
		got := doorRequest(t, ts.Client(), "GET", ts.URL+door.Path+"bucket/"+wireKey, "", 200)
		if string(got) != payload {
			t.Fatalf("prefix did not expose the same host bytes: %q", got)
		}
	}
	for i, door := range doors.Snapshot().Doors {
		got := doorRequest(t, client, "GET", "http://"+door.Host+"/bucket/"+wireKey, "", 200)
		if string(got) != strings.Repeat("host", i+1) {
			t.Fatal("a later host write contaminated another provider or region:", door)
		}
		if doors.Snapshot().HTTPRequests["http://"+door.Host+" 200"] != 3 {
			t.Fatal("host evidence did not count the actual completed exchanges")
		}
	}
	if got := doorRequest(t, ts.Client(), "GET", ts.URL+"/native", "", 200); string(got) != "control-witness" {
		t.Fatal("host mounting displaced the control plane")
	}
}

func TestHostDoorsRefuseAmbiguousProxyTargets(t *testing.T) {
	doors := hostFixture(t)
	handler := doors.Handler("127.0.0.1:4599")
	host := DoorHost("exoscale", "ch-gva-2")
	for _, tc := range []struct {
		name, target, host, method string
	}{
		{"unknown", "http://unknown.s3.feint.invalid/native", "unknown.s3.feint.invalid", "GET"},
		{"undeclared region", "http://exoscale.foreign.s3.feint.invalid/native", "exoscale.foreign.s3.feint.invalid", "GET"},
		{"authority mismatch", "http://foreign.example/native", host, "GET"},
		{"loopback Host forgery", "http://foreign.example/native", "localhost", "GET"},
		{"origin form", "/native", host, "GET"},
		{"https", "https://" + host + "/bucket", host, "HEAD"},
		{"credentials", "http://user@" + host + "/bucket", host, "HEAD"},
		{"connect", "http://" + host + "/bucket", host, "CONNECT"},
		{"explicit port alias", "http://" + host + ":80/bucket", host + ":80", "HEAD"},
		{"case alias", "http://" + strings.ToUpper(host) + "/bucket", strings.ToUpper(host), "HEAD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.target, nil)
			r.Host, r.RemoteAddr = tc.host, "127.0.0.1:1234"
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 403 || bytes.Contains(w.Body.Bytes(), []byte("control-witness")) {
				t.Fatalf("ambiguous target reached a handler: status=%d body=%q", w.Code, w.Body)
			}
		})
	}
	// A known authority cannot reach reports, state, health or native routes.
	for _, path := range []string{ReportPath, "/_feint/state", "/_feint/health", "/native"} {
		r := httptest.NewRequest("GET", "http://"+host+path, nil)
		r.RemoteAddr = "127.0.0.1:1234"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code == 200 || bytes.Contains(w.Body.Bytes(), []byte("control-witness")) {
			t.Fatal("S3 host escaped into the control plane:", path, w.Code, w.Body)
		}
	}
}

func TestHostDoorsRetainOriginPeerAndUploadDeadlineGuards(t *testing.T) {
	doors := hostFixture(t)
	handler := doors.Handler("0.0.0.0:4599")
	target := "http://" + DoorHost("exoscale", "ch-gva-2") + "/bucket"
	for _, tc := range []struct{ peer, origin string }{
		{"192.0.2.10:1234", ""},
		{"127.0.0.1:1234", "https://foreign.example"},
		{"127.0.0.1:1234", "http://localhost"},
	} {
		r := httptest.NewRequest("HEAD", target, nil)
		r.RemoteAddr = tc.peer
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("host mode weakened the S3 peer/Origin boundary:", tc, w.Code)
		}
	}
	r := httptest.NewRequest("PUT", target, nil)
	r.RemoteAddr = "127.0.0.1:1234"
	unsupported := httptest.NewRecorder()
	handler.ServeHTTP(unsupported, r)
	if unsupported.Code != 503 {
		t.Fatal("host upload bypassed its read deadline:", unsupported.Code)
	}
	w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	handler.ServeHTTP(w, r)
	if w.Code != 200 || w.deadline.IsZero() {
		t.Fatal("valid loopback host PUT was refused or unbounded:", w.Code, w.deadline)
	}
}

func TestHostDoorsRequireExplicitActivation(t *testing.T) {
	doors, _ := mountedFixture(t)
	r := httptest.NewRequest("HEAD", "http://"+DoorHost("exoscale", "ch-gva-2")+"/bucket", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	doors.Handler("127.0.0.1:4599").ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("prefixed-only deployment accepted a synthetic host:", w.Code)
	}
	for _, door := range doors.Snapshot().Doors {
		if door.Host != "" {
			t.Fatal("prefixed-only deployment advertised host activation")
		}
	}
}
