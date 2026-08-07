// Package emulator wires provider packs onto a single HTTP surface.
//
// The three supported clouds do not collide in URL space: Scaleway serves
// /<product>/v<N>/..., Outscale serves POST /api/v1/<Action>, Exoscale serves
// /v2/<resource>. One mux can therefore host all of them at once on one port,
// which is what makes a multi-provider emulator practical: a single container,
// a single endpoint to point every SDK at.
package emulator

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/store"
	"github.com/stephrobert/feint/internal/trace"
)

// Env carries everything a pack needs from the core. Now and NewID are injected
// so tests can pin timestamps and identifiers and compare golden output.
type Env struct {
	Store *store.Store
	Now   func() time.Time
	NewID func() string
	// Machines backs powered-on servers with something that actually runs.
	// Defaults to the metadata-only driver, so nothing ever starts unless the
	// operator asked for it.
	Machines machine.Driver
	// Log is where packs report what they could not do. A machine runtime that
	// fails must never break the control plane, which makes the log the only
	// place the operator can learn why nothing started.
	Log *slog.Logger
	// Contracts maps a provider name to its API description, when one has been
	// loaded. With one, every response that provider emits is validated against
	// the schema its operation declares, and a field the API does not define is
	// reported at /_feint/conformance.
	//
	// Off by default, because checking means buffering every response body. It
	// is what the conformance run turns on: that traffic comes from a real
	// client, which is broader than any set of cases somebody thought to write.
	Contracts map[string]*contract.Doc
}

// DefaultEnv returns an environment backed by a fresh store, the wall clock,
// random UUIDs and no machine runtime.
func DefaultEnv() *Env {
	return &Env{
		Store:    store.New(),
		Now:      func() time.Time { return time.Now().UTC() },
		NewID:    NewUUID,
		Machines: machine.Noop{},
		Log:      slog.Default(),
	}
}

// NewUUID returns a random RFC 4122 version 4 UUID. Scaleway and Exoscale both
// identify resources this way, and their SDKs validate the format, so the
// emulator cannot hand out arbitrary strings.
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("feint: no entropy for UUID generation: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b[:])
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32]
}

// Route binds one upstream API operation to a handler.
//
// Operation is the canonical upstream name (for Scaleway, the SDK method:
// "instance/v1/API.ListServers"). It is what the drift report joins on, so a
// route with a wrong or missing Operation makes the emulator look less complete
// than it is.
type Route struct {
	Method    string
	Path      string
	Operation string
	Handler   http.HandlerFunc
}

// Pack is one provider implementation.
type Pack interface {
	// Name is the provider key: "scaleway", "outscale", "exoscale".
	Name() string
	// Routes lists everything the pack serves.
	Routes() []Route
	// Declined lists upstream operations the pack knowingly does not serve,
	// each with the reason, so the drift report can separate "not done yet"
	// from "deliberately out of scope" and say which. Every entry must use the
	// same naming as Route.Operation.
	//
	// The reason is data rather than a comment because everything that consumes
	// this list — the coverage report, `feint coverage`, the generated route
	// reference — used to print a count of refusals a reader had to open three
	// provider files to understand.
	Declined() []Decline
	// Env is what a real client of this provider needs in its environment to
	// reach the emulator instead of the cloud.
	//
	// It lives on the pack because the variable names are provider knowledge,
	// and the core must never learn one. The same test as everywhere else: could
	// this line be written identically for another provider? SCW_API_URL could
	// not, so it belongs here.
	//
	// Mandatory rather than optional, unlike Unrouted or Firewaller: a pack that
	// cannot say how to point a client at it is a pack nobody can use, and
	// finding that out at runtime is worse than at compile time.
	Env(endpoint string) Environment
}

// Environment is what a real client needs to reach the emulator, plus whatever
// cannot be expressed as a variable.
type Environment struct {
	// Vars are the variables to export. These reach stdout, so `eval` can
	// consume them, and nothing else may.
	Vars map[string]string
	// Note is printed to stderr, never to stdout.
	//
	// It exists because one of the three providers cannot be redirected by an
	// environment variable at all: the Exoscale CLI reads its endpoint only from
	// its own configuration file. A tool that printed a comment explaining that
	// on stdout would break `eval $(feint env exoscale)` for everyone, and a
	// tool that stayed silent would hand the user variables that do not work.
	Note string
}

// Unrouted is the optional half of a Pack that can answer a request landing in
// its URL space with no route to serve it.
//
// Without one, an unimplemented operation gets net/http's own answer: "404 page
// not found", text/plain. No SDK can parse that, so a client meets a decoding
// error where it should meet an API error, and the emulator looks broken rather
// than incomplete. It is not a corner case either: with 216 Outscale operations
// left to serve, it is the likeliest answer a caller gets.
//
// The core cannot write that body itself — the shape belongs to the provider —
// so it asks the pack whose space was addressed.
type Unrouted interface {
	// Prefixes are the URL prefixes the pack owns, e.g. "/api/v1/". A request
	// under one of them belongs to this pack whether or not a route matches.
	Prefixes() []string
	// NotFound writes the provider's own "no such operation" error.
	NotFound(w http.ResponseWriter, r *http.Request)
}

// Server hosts a set of packs.
type Server struct {
	env      *Env
	packs    []Pack
	mux      *http.ServeMux
	observer *observer
	// stream is the bounded call log, and the one-way channel the page reads it
	// through.
	stream *stream
	// self records the patterns of the endpoints the emulator serves about
	// itself, so they can be enumerated rather than remembered.
	self []string
}

// NewServer mounts the packs. It fails when two packs claim the same route,
// which would otherwise panic inside net/http at a random point in startup.
func NewServer(env *Env, packs ...Pack) (*Server, error) {
	events := newStream()
	s := &Server{
		env:      env,
		packs:    packs,
		mux:      http.NewServeMux(),
		stream:   events,
		observer: newObserver(env.Contracts, events),
	}

	seen := make(map[string]string)
	for _, p := range packs {
		for _, r := range p.Routes() {
			pattern := r.Method + " " + r.Path
			if owner, dup := seen[pattern]; dup {
				return nil, fmt.Errorf("route %q claimed by both %q and %q", pattern, owner, p.Name())
			}
			seen[pattern] = p.Name()
			s.mux.HandleFunc(pattern, s.observer.wrap(p.Name(), r))
		}
	}

	s.mountSelf("GET /_feint/health", s.handleHealth)
	s.mountSelf("GET /_feint/routes", s.handleRoutes)
	s.mountSelf("GET /_feint/conformance", s.handleConformance)
	// Mounted here rather than with the page, and the difference is what it is
	// for: the stream is the page's transport, while the trace is a mechanism a
	// conformance script or a CI job reads after the fact. Tying it to the page
	// would make it unavailable to exactly the callers it was asked for.
	s.mountSelf("GET /_feint/trace", s.handleTrace)
	s.mountSelf("GET /_feint/state", s.handleStateRead)
	s.mountSelf("PUT /_feint/state", s.handleStateWrite)

	// "/" matches anything no other pattern does, and net/http 1.22 patterns are
	// specific-wins, so this never shadows a mounted route.
	s.mux.HandleFunc("/", s.handleUnrouted)
	return s, nil
}

// mountSelf registers an endpoint the emulator serves about itself, and records
// its pattern.
//
// These are deliberately not Routes. A Route names the upstream operation the
// coverage report joins on, and none of these names one: they are feint's own
// surface rather than a provider's. Giving them an invented Operation would put
// each of them in the drift report as an orphan — a route pointing at an
// operation no SDK has — which is precisely the signal that report exists to
// carry.
func (s *Server) mountSelf(pattern string, h http.HandlerFunc) {
	s.self = append(s.self, pattern)
	s.mux.HandleFunc(pattern, h)
}

// SelfRoutes returns the "METHOD /path" pattern of every endpoint the emulator
// serves about itself, in mount order.
//
// Exported for the sake of enumeration. The page under /_feint/ui must never
// become a way to drive the emulator, and the only mechanical form of that
// promise is a list somebody can walk: TestThePageAddsOnlyGETRoutes reads this
// one and fails on anything that is not a GET.
func (s *Server) SelfRoutes() []string {
	out := make([]string, len(s.self))
	copy(out, s.self)
	return out
}

// handleUnrouted answers a request no route claimed, in the dialect of whichever
// pack owns the URL space it landed in.
//
// Prefixes are checked longest first, so a pack owning "/api/v1/" wins over one
// owning "/api/". Nothing claims the path: net/http's plain answer stands, which
// is right for a request that belongs to no provider at all.
func (s *Server) handleUnrouted(w http.ResponseWriter, r *http.Request) {
	best, bestLen := (Unrouted)(nil), -1
	for _, p := range s.packs {
		unrouted, ok := p.(Unrouted)
		if !ok {
			continue
		}
		for _, prefix := range unrouted.Prefixes() {
			if len(prefix) > bestLen && strings.HasPrefix(r.URL.Path, prefix) {
				best, bestLen = unrouted, len(prefix)
			}
		}
	}
	// Logged like any other request, and this is the line the log exists for:
	// a client walking a plan meets one route nobody mounted, the whole apply
	// dies, and no counter anywhere records which one it was. The emulator's own
	// endpoints are left out — the page polls them, and a log of the page
	// watching itself would drown the client traffic.
	rec := &recorder{ResponseWriter: w, status: http.StatusOK}
	started := time.Now()
	if best == nil {
		http.NotFound(rec, r)
	} else {
		best.NotFound(rec, r)
	}
	if !internalPath(r.URL.Path) {
		s.stream.publishExchange(trace.Exchange{
			Method:  r.Method,
			Path:    r.URL.Path,
			Query:   r.URL.RawQuery,
			Status:  rec.status,
			Ms:      float64(time.Since(started).Microseconds()) / 1000,
			Mounted: false,
		})
	}
}

// Handler exposes the mux for http.Server or httptest.
func (s *Server) Handler() http.Handler { return s.mux }

// Packs returns the mounted packs, in mount order.
func (s *Server) Packs() []Pack { return s.packs }

// AllRoutes returns every mounted route, sorted by provider then path, for the
// coverage report and the generated documentation.
func (s *Server) AllRoutes() []Route {
	var out []Route
	for _, p := range s.packs {
		out = append(out, p.Routes()...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path == out[j].Path {
			return out[i].Method < out[j].Method
		}
		return out[i].Path < out[j].Path
	})
	return out
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	providers := make([]string, 0, len(s.packs))
	for _, p := range s.packs {
		providers = append(providers, p.Name())
	}
	driver := "none"
	var capabilities *machine.Capabilities
	if s.env.Machines != nil {
		driver = s.env.Machines.Name()
		// Declared rather than CapabilitiesOf: null on the wire when the driver
		// said nothing, so a reader can tell "no" from "nobody promised". Five
		// falses cannot.
		capabilities = machine.Declared(s.env.Machines)
	}
	// The capabilities are published because the difference between the runtime
	// modes was recorded only in documentation — that is, nowhere at the moment
	// it matters. A conformance suite gating on `capabilities.isolation` asserts
	// what this process can actually prove instead of hardcoding a mode name,
	// and an operator can see what they have without reading a file.
	writeJSON(w, http.StatusOK, map[string]any{
		"status":       "ok",
		"providers":    providers,
		"resources":    s.env.Store.Len(),
		"machines":     driver,
		"capabilities": capabilities,
	})
}

// handleStateRead and handleStateWrite are what `feint snapshot` drives.
//
// The store already knew how to write and read itself: `serve --state` restores
// at boot and writes at exit, which is one snapshot per session and only at the
// two moments the process starts and stops. Naming a state and taking it mid-run
// needs no new storage, only a way to reach the store of a process that is
// already answering — so it is the same two calls, over HTTP.
//
// No provider is named here and none can be: the store holds resources whose
// kinds the packs decide, and neither end of this pair reads them. That is what
// keeps a whole feature out of the packs.
func (s *Server) handleStateRead(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := s.env.Store.Snapshot(w); err != nil {
		// The status is already sent by the time Snapshot can fail on the
		// writer, so there is nothing to report but the log. Log is read
		// through a fallback because an Env built literally — which the tests
		// do — carries no logger, and a nil one panics inside the handler.
		s.log().Error("snapshot the store", "error", err)
	}
}

// log is the logger this server reports through, never nil.
func (s *Server) log() *slog.Logger {
	if s.env.Log != nil {
		return s.env.Log
	}
	return slog.Default()
}

// maxStateBody bounds what a snapshot may weigh on the way in.
//
// Without it, Restore decodes whatever arrives into memory, and a body of
// arbitrary size is a crash reachable from a request — which SECURITY.md
// declares in scope. 64 MiB is far above any real store (a fixture of a few
// hundred resources is kilobytes) and far below what hurts a workstation.
const maxStateBody = 64 << 20

func (s *Server) handleStateWrite(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }()
	body := http.MaxBytesReader(w, r.Body, maxStateBody)
	if err := s.env.Store.Restore(body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "resources": s.env.Store.Len()})
}

func (s *Server) handleRoutes(w http.ResponseWriter, _ *http.Request) {
	type routeView struct {
		Method    string `json:"method"`
		Path      string `json:"path"`
		Operation string `json:"operation"`
	}
	out := make([]routeView, 0)
	for _, r := range s.AllRoutes() {
		out = append(out, routeView{Method: r.Method, Path: r.Path, Operation: r.Operation})
	}
	writeJSON(w, http.StatusOK, out)
}
