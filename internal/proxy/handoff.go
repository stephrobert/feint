package proxy

import (
	"net/url"
	"strings"
	"sync"
)

// A recorder that stops recording without saying so is worse than one that
// refuses, and this file is about the second of the two ways that happens here.
//
// The first is loud: a client that signs the host it was configured with gets a
// 401 from the real cloud, nothing is distorted, and docs/proxy.md has always
// said so. The second is silent. Some APIs hand the client an address in a
// response body — Exoscale publishes an `api-endpoint` per zone in `GET
// /v2/zone` — and a client that follows it walks away from the proxy for
// everything after. A full session that produced about ninety exchanges through
// a rewriter recorded **eight** through the proxy alone, and the transcript said
// nothing about the other eighty-two. It looked complete.
//
// So the proxy notices when it has just handed the client somewhere else. Not by
// knowing which field any provider uses — that would put three provider names in
// a tool that has none, and a fourth API would be silent again — but by the
// shape of the thing: an absolute URL in a response body naming a host that is
// not the one the client is talking to.
//
// This is a finding rather than a diagnostic, in the same sense `unnamed` is: it
// is the recorder saying what its own transcript does not cover.

// handoff counts responses that named a host other than this proxy, and keeps
// the hosts so the report can name them.
type handoff struct {
	mu    sync.Mutex
	count int64
	hosts map[string]int64
}

// note records one response body that pointed elsewhere.
//
// `self` is the authority the client addressed — r.Host — rather than the
// listen address: behind a name, an alias or a container port mapping those
// differ, and what matters is whether the client is being sent away from what it
// is currently talking to.
func (h *handoff) note(self string, body []byte) {
	elsewhere := hostsElsewhere(self, body)
	if len(elsewhere) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.count++
	if h.hosts == nil {
		h.hosts = map[string]int64{}
	}
	for _, host := range elsewhere {
		h.hosts[host]++
	}
}

// seen answers how many responses pointed elsewhere, and at which hosts.
func (h *handoff) seen() (int64, map[string]int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]int64, len(h.hosts))
	for host, n := range h.hosts {
		out[host] = n
	}
	return h.count, out
}

// schemes are the two an in-band endpoint can carry. A body naming `ws://` or a
// bare hostname is not a redirect a client follows for its next API call, and
// widening this would turn every documentation link in an error message into a
// finding.
var schemes = []string{"https://", "http://"}

// hostsElsewhere finds absolute URLs in a body whose host is not `self`.
//
// A scan rather than a JSON walk, deliberately: the body may be XML, may be a
// form, may be truncated at the recording cap mid-document, and a walk that
// needs to parse would go blind on all three. What is being looked for is a
// string with a scheme in it, which survives all of them.
func hostsElsewhere(self string, body []byte) []string {
	if self == "" || len(body) == 0 {
		return nil
	}
	text := string(body)

	seen := map[string]bool{}
	var out []string
	for _, scheme := range schemes {
		for from := 0; ; {
			at := strings.Index(text[from:], scheme)
			if at < 0 {
				break
			}
			at += from
			from = at + len(scheme)

			raw := text[at:]
			if end := strings.IndexAny(raw, "\"'`\\ \t\r\n,)]}<>"); end >= 0 {
				raw = raw[:end]
			}
			parsed, err := url.Parse(raw)
			if err != nil || parsed.Host == "" {
				continue
			}
			if sameAuthority(parsed.Host, self) || seen[parsed.Host] {
				continue
			}
			seen[parsed.Host] = true
			out = append(out, parsed.Host)
		}
	}
	return out
}

// sameAuthority compares hosts without being fooled by a default port.
//
// A client addressing `127.0.0.1:4701` and a body naming `http://127.0.0.1:4701`
// are the same place; so are `example.test` and `example.test:80` over http.
// Getting this wrong would report a handoff on every response of a proxy that
// correctly points at itself, which is the false positive that would make the
// whole signal ignored.
func sameAuthority(a, b string) bool {
	return strings.EqualFold(trimDefaultPort(a), trimDefaultPort(b))
}

func trimDefaultPort(host string) string {
	for _, port := range []string{":80", ":443"} {
		if strings.HasSuffix(host, port) {
			return strings.TrimSuffix(host, port)
		}
	}
	return host
}
