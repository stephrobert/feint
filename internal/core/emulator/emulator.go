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
	// Undriven says why no official client reaches this operation, in one line
	// and in the present tense. It is empty for every route a client drives, and
	// a route that gains a client must lose it.
	//
	// Mounted is a protocol promise; driven by a real client is the only proof
	// this project accepts. Fifty-three operations sat between the two with
	// nothing to tell them apart (#174), so a reader could not distinguish
	// UpdateServer — a day-one client path nobody had exercised — from
	// ReadPublicIpRanges, which no fixture has a reason to call. That flatness
	// was the debt, and this field is what removes it: the ones a client can
	// reach were driven, and the rest say why they cannot be.
	//
	// It is data rather than a comment for the reason Decline.Reason is: a
	// control reads it. TestEveryUndrivenOperationSaysWhy fails when an
	// operation the recorded run left undriven carries no reason, and fails
	// again when a reason survives the client that came to drive it — a stale
	// excuse reads exactly like a considered one.
	Undriven string
}

// Pack is one provider implementation.
type Pack interface {
	// Name is the provider key: one lowercase word, unique among the mounted
	// packs (e.g. "scaleway"). The core never holds a list of them — anything
	// keyed by provider derives its names from the packs the server mounts.
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
	// shapeCovered is the set of operations an observed real-cloud shape
	// covers, handed in by SetShapeCovered. nil means no catalogue was given,
	// which the evidence record reports as "unknown" rather than "unobserved".
	shapeCovered map[string]bool
	// observedFields maps an operation to the field paths a real cloud's
	// recorded answer carried, handed in by SetObservedFields the way
	// shapeCovered is: the core knows neither providers nor where recordings
	// live. It is the corroboration the omission check fails on (omissions.go);
	// nil narrows that check to nothing rather than inventing a proof.
	observedFields map[string]map[string]bool
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

	// The table both indexes the routes and rejects a pattern two packs claim.
	// It is consulted from outside this process too — `feint proxy` names an
	// exchange with it — so it owns the mapping rather than each caller
	// rebuilding one. See table.go.
	table, err := NewTable(packs...)
	if err != nil {
		return nil, err
	}
	if err := checkSpaces(packs); err != nil {
		return nil, err
	}
	// Mounting follows the table's own grouping: one ServeMux pattern per base
	// path. A base with no ":action" route mounts its handler directly; a base
	// with any mounts a dispatcher, because net/http cannot tell /x/{id} from
	// /x/{id}:stop — the wildcard spans the whole segment. See Table.
	for pattern, g := range table.groups {
		if len(g.routes) == 1 {
			if m, plainOnly := g.routes[""]; plainOnly {
				s.mux.HandleFunc(pattern, s.observer.wrap(m.Provider, m.Route))
				continue
			}
		}
		wrapped := make(map[string]http.HandlerFunc, len(g.routes))
		for action, m := range g.routes {
			wrapped[action] = s.observer.wrap(m.Provider, m.Route)
		}
		wildcard := g.wildcard
		s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			segment := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			value, action, _ := strings.Cut(segment, ":")
			h, ok := wrapped[action]
			if !ok {
				// An action nobody serves is an unserved operation, and it must
				// answer in the pack's own dialect rather than net/http's.
				s.handleUnrouted(w, r)
				return
			}
			// The wildcard matched "id:action"; the handler's PathValue must see
			// the identifier alone.
			r.SetPathValue(wildcard, value)
			h(w, r)
		})
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
	// The assertion spans behind the behaviour and negative evidence axes: a
	// suite brackets one assertion, and the emulator resolves what it proved
	// from its own observations. See assert.go.
	s.mountSelf("POST /_feint/assert", s.handleAssertOpen)
	s.mountSelf("POST /_feint/assert/{id}", s.handleAssertClose)
	// The store tells the observer about every touch the handlers make, which
	// is what lets a lifecycle be observed rather than declared.
	if env.Store != nil {
		env.Store.Observe(s.observer.storeTouch)
	}

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

// checkSpaces refuses two packs whose unrouted URL spaces overlap.
//
// The same refusal NewTable makes for two packs claiming one route, one level
// up. handleUnrouted resolves by longest prefix, so two packs whose spaces
// overlap do not conflict loudly: one of them simply wins, decided by the
// length of a string, and a client gets the wrong provider's error dialect for
// every operation neither serves.
//
// At start-up rather than in a test, and the difference matters here. A test
// listing the spaces it knows about is a test a fourth pack is absent from,
// green while measuring nothing of it — which is the defect this repository has
// now paid for twice, in Declined() and in the Outscale tag table. This reads
// whatever is mounted, so a pack added tomorrow is checked by existing.
//
// Overlap within one pack is left alone: the same NotFound answers either way,
// so there is nothing to resolve.
// TestTwoPacksMayNotClaimOverlappingSpaces fails without this.
func checkSpaces(packs []Pack) error {
	type claim struct{ provider, prefix string }
	var claims []claim
	for _, p := range packs {
		unrouted, ok := p.(Unrouted)
		if !ok {
			continue
		}
		for _, prefix := range unrouted.Prefixes() {
			claims = append(claims, claim{p.Name(), prefix})
		}
	}
	for i, a := range claims {
		for _, b := range claims[i+1:] {
			if a.provider == b.provider {
				continue
			}
			if strings.HasPrefix(a.prefix, b.prefix) || strings.HasPrefix(b.prefix, a.prefix) {
				return fmt.Errorf(
					"packs %q and %q both claim overlapping URL space (%q and %q): "+
						"an unserved request there would be answered by whichever prefix is longer",
					a.provider, b.provider, a.prefix, b.prefix)
			}
		}
	}
	return nil
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
	hint := ""
	var missing []string
	switch {
	case best != nil:
		best.NotFound(rec, r)
	default:
		// Nobody claimed this URL space, so there is no dialect to answer in.
		// Before falling back to net/http's one-line page, ask the mounted route
		// table whether this path would have matched with a prefix in front of
		// it — the shape a client pointed at the wrong endpoint produces, and
		// the one thing that page never said (#179).
		hint, missing = missingPrefixHint(r.URL.Path, s.AllRoutes())
		if hint != "" {
			writeMispointed(rec, hint)
		} else {
			http.NotFound(rec, r)
		}
	}
	// And in the log too, because some CLIs never surface a 404 body: the
	// operator watching the emulator sees the cause even when their client
	// swallowed it.
	//
	// The prefixes and nothing else. They are read out of this process's own
	// mounted route table; the sentence in the body embeds the request path, and
	// that half never reaches a log record.
	//
	// The first version logged the path, then the hint that contains it. Both
	// were defensible — plainPath allow-lists the path before either exists —
	// but an argument about what an allow-list keeps out is weaker than a value
	// that never came from the client at all. This is the second kind.
	//
	// TestTheLogCarriesNothingTheClientChose fails without it.
	if len(missing) > 0 {
		s.env.Log.Warn("a client is pointing without a served prefix",
			"missing_prefix", strings.Join(missing, " "))
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
	// And which packs hand work to those capabilities (#180). The driver's set
	// is one for the whole process; wiring is per pack, and publishing only the
	// first told a user the firewall was delivered for three packs when one
	// delivered it. The honest check is both: capabilities.firewall says the
	// runtime can, enforced.firewall says who asks it to.
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": HealthSchemaVersion,
		"status":         "ok",
		"providers":      providers,
		"resources":      s.env.Store.Len(),
		"machines":       driver,
		"capabilities":   capabilities,
		"enforced":       enforcement(s.packs),
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
