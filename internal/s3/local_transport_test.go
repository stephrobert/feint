package s3

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestLocalTransportPreservesCloudBucketAndLiteralKeyWithoutFallback(t *testing.T) {
	var requests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.EscapedPath() != "/_feint/s3/example/region-1/a.b.bucket/folder//a/../percent%252F" ||
			r.URL.RawQuery != "tagging=&x-id=GetObjectTagging" {
			t.Errorf("mapped request lost bucket or literal key: %s", r.URL.String())
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	profile := Profile{Provider: "example", Region: "region-1", CloudHost: "s3.example.invalid"}
	transport, err := NewLocalTransport(target.URL, []Profile{profile})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	for _, authority := range []string{"a.b.bucket.s3.example.invalid", "s3.example.invalid/a.b.bucket"} {
		r, err := http.NewRequestWithContext(t.Context(), "GET",
			"https://"+authority+"/folder//a/../percent%252F?tagging=&x-id=GetObjectTagging", nil)
		if err != nil {
			t.Fatal(err)
		}
		original := r.URL.String()
		response, err := transport.RoundTrip(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := response.Body.Close(); err != nil || response.StatusCode != http.StatusNoContent || r.URL.String() != original {
			t.Fatal("successful mapping mutated the caller or failed:", err, response.StatusCode)
		}
	}
	before := requests.Load()
	for _, source := range []string{
		"https://other.invalid/key", "https://s3.example.invalid.evil.invalid/key",
		"https://bad_bucket.s3.example.invalid/key", "http://s3.example.invalid/key",
		"https://user@s3.example.invalid/key", "https://s3.example.invalid:444/key",
	} {
		r, err := http.NewRequestWithContext(t.Context(), "GET", source, nil)
		if err != nil {
			t.Fatal(err)
		}
		if response, err := transport.RoundTrip(r); err == nil {
			if err := response.Body.Close(); err != nil {
				t.Fatal(err)
			}
			t.Fatal("undeclared request reached the local transport:", source)
		}
	}
	if requests.Load() != before || before != 2 {
		t.Fatal("refusal dialled despite unknown authority or accepting controls failed:", requests.Load(), before)
	}
}

func TestLocalTransportRejectsUnsafeTargetsAndOverlappingDeclarations(t *testing.T) {
	profile := Profile{Provider: "example", Region: "region-1", CloudHost: "s3.example.invalid"}
	for _, target := range []string{
		"http://192.0.2.1:1234", "http://localhost:1234", "https://127.0.0.1:1234",
		"http://127.0.0.1", "http://user@127.0.0.1:1234", "http://127.0.0.1:1234/path",
		"http://127.0.0.1:1234?query", "http://127.0.0.1:1234#fragment",
	} {
		if _, err := NewLocalTransport(target, []Profile{profile}); err == nil {
			t.Fatal("unsafe target admitted:", target)
		}
	}
	for _, profiles := range [][]Profile{
		nil, {profile, profile},
		{profile, {Provider: "other", Region: "region-1", CloudHost: "bucket.s3.example.invalid"}},
		{{Provider: "example", Region: "region-1", CloudHost: ""}},
	} {
		if _, err := NewLocalTransport("http://127.0.0.1:1234", profiles); err == nil {
			t.Fatal("ambiguous or empty authority declarations admitted:", profiles)
		}
	}
	valid, err := NewLocalTransport("http://[::1]:1234", []Profile{profile})
	if err != nil {
		t.Fatal("IPv6 loopback socket refused:", err)
	}
	valid.CloseIdleConnections()
	if response, err := valid.RoundTrip(&http.Request{URL: &url.URL{Scheme: "https", Host: "s3.example.invalid", Fragment: "discarded"}}); err == nil {
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		t.Fatal("fragment was silently discarded")
	}
}
