package scaleway_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// FuzzCreateServer mutates the one input the emulator does not control: the JSON
// body a client posts. Whatever arrives, the handler must answer a well-formed
// JSON response and never panic, because a panic here takes down every other
// provider sharing the process.
func FuzzCreateServer(f *testing.F) {
	f.Add(`{"name":"web","commercial_type":"DEV1-S"}`)
	f.Add(`{"name":"","commercial_type":""}`)
	f.Add(`{"tags":["a","b"],"name":"x","commercial_type":"DEV1-S"}`)
	f.Add(`{"dynamic_ip_required":true}`)
	f.Add(`{`)
	f.Add(``)
	f.Add(`null`)
	f.Add(`[]`)
	f.Add(`{"name":{"nested":"wrong type"}}`)

	ts := newTestServer(f)

	f.Fuzz(func(t *testing.T, body string) {
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/instance/v1/zones/fr-par-1/servers", strings.NewReader(body))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("unexpected status %d for body %q", resp.StatusCode, body)
		}

		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("response is not valid JSON for body %q: %v", body, err)
		}
		if resp.StatusCode == http.StatusBadRequest && out["type"] != "invalid_arguments" {
			t.Fatalf("rejection did not carry the SDK-visible error type: %v", out)
		}
	})
}

// FuzzBookIP does the same for the IPAM book decoder, the request body SW-4
// introduced: nested source and resource objects, an optional address, and a
// path that allocates. Whatever arrives, a well-formed JSON answer, no panic.
func FuzzBookIP(f *testing.F) {
	f.Add(`{"source":{"private_network_id":"x"}}`)
	f.Add(`{"source":{"subnet_id":""},"address":"10.0.0.300"}`)
	f.Add(`{"source":null,"is_ipv6":true}`)
	f.Add(`{"resource":{"mac_address":"zz","name":"\n"},"tags":[""]}`)
	f.Add(`{"address":42}`)
	f.Add(`{`)
	f.Add(``)
	f.Add(`null`)
	f.Add(`[]`)

	ts := newTestServer(f)

	f.Fuzz(func(t *testing.T, body string) {
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/ipam/v1/regions/fr-par/ips", strings.NewReader(body))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		switch resp.StatusCode {
		case http.StatusCreated, http.StatusBadRequest, http.StatusNotFound:
		default:
			t.Fatalf("unexpected status %d for body %q", resp.StatusCode, body)
		}

		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("response is not valid JSON for body %q: %v", body, err)
		}
	})
}
