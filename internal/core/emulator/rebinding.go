package emulator

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Loopback is not the boundary people think it is.
//
// The emulator listens on 127.0.0.1, accepts every credential without checking
// one, and with --vm starts containers with the operator's privileges. That is
// stated in SECURITY.md and it is a reasonable posture for a development tool —
// but it rests on "only this machine can reach it", and a browser breaks that
// assumption.
//
// DNS rebinding is the attack: a page the operator visits resolves
// attacker.example to 127.0.0.1, and the browser then issues same-origin
// requests to the emulator on the attacker's behalf. The listen address stops
// nothing; the packets really do come from the loopback interface. An audit
// confirmed it here — a request carrying `Host: evil.example` was answered 200.
//
// The defence is the one Caddy's admin API, Syncthing and the Go pprof endpoints
// all use: require the Host header to name the loopback. A browser cannot forge
// it, because it is derived from the URL the page asked for.
//
// It applies only when the emulator is listening on a loopback address. An
// operator who passed --addr 0.0.0.0 has asked to be reachable by name, and
// second-guessing that would break the thing they explicitly requested.
type rebindingGuard struct {
	next  http.Handler
	guard bool
}

// GuardRebinding wraps h when listening on addr would otherwise leave the
// emulator reachable from a browser page. Applied by the command that owns the
// listen address; the bare Handler stays unguarded so tests and in-process users
// are not made to care.
func GuardRebinding(h http.Handler, addr string) http.Handler {
	return rebindingGuard{next: h, guard: LoopbackListen(addr)}
}

func (g rebindingGuard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The Host check closes DNS rebinding and only that. A page can also reach
	// here without rebinding anything, with a "simple request" the browser sends
	// without a preflight: a form POST, or fetch() with Content-Type text/plain.
	// Those carry the target's own Host, so the check above waves them through —
	// and an audit created a server that way. With --vm, that starts a container.
	//
	// Origin is what tells them apart. No browser omits it on a cross-origin
	// request, and no API client sends one: scw, oapi-cli, exo, Terraform and
	// curl all leave it empty. So an Origin that is present and not local is a
	// page talking, and a page has no business here.
	if g.guard && r.Header.Get("Origin") != "" && !originIsLocal(r.Header.Get("Origin")) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "refused: this request carries Origin " + r.Header.Get("Origin") +
				", so it comes from a web page rather than an API client. " +
				"feint answers tools, not browsers.",
		})
		return
	}
	if g.guard && !hostIsLocal(r.Host) {
		// 403 rather than 421: the request reached the right server, and it is
		// the name it used that is refused. The body says which, because the
		// alternative is an operator debugging a proxy for an hour.
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "refused: this emulator listens on the loopback interface and only answers " +
				"requests addressed to it. The Host header said " + r.Host + ". " +
				"If you meant to reach it by name, start it with --addr on the address that name resolves to.",
		})
		return
	}
	g.next.ServeHTTP(w, r)
}

// LoopbackListen reports whether a listen address keeps the emulator on the
// local machine. A bare port, or one bound to every interface, does not.
//
// Exported because the refusal at start-up must ask exactly this question. Two
// implementations of "is this local" would eventually disagree, and the one that
// drifted would be the one nobody tested — which is how the browser guard came
// to be silently disarmed by an address nothing validated.
func LoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// originIsLocal reports whether an Origin header names this machine.
//
// "null" is refused: a sandboxed iframe and a file:// page both send it, and
// neither is a client of this emulator.
func originIsLocal(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return hostIsLocal(u.Host)
}

// hostIsLocal reports whether a Host header names this machine.
//
// An empty Host is allowed: HTTP/1.0 clients and some SDK code paths send none,
// and a browser always sends one, so refusing it would break real clients
// without closing the hole.
func hostIsLocal(host string) bool {
	if host == "" {
		return true
	}
	name, _, err := net.SplitHostPort(host)
	if err != nil {
		name = host
	}
	name = strings.Trim(name, "[]")
	if strings.EqualFold(name, "localhost") {
		return true
	}
	// Any name under .localhost resolves to the loopback per RFC 6761, and
	// container tooling uses names like host.docker.internal that do not.
	if strings.HasSuffix(strings.ToLower(name), ".localhost") {
		return true
	}
	ip := net.ParseIP(name)
	return ip != nil && ip.IsLoopback()
}
