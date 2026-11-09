package s3

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/objectstore"
)

// ReportPath is the separate S3 configuration/evidence endpoint. Its counters
// describe HTTP requests, not official-client proof or control-plane coverage.
const ReportPath = "/_feint/s3"

// ReportVersion versions the S3 report independently of control-plane schemas.
const ReportVersion = 2

// UploadReadTimeout bounds a simple PUT body without changing the runtime's
// independent write deadline or the control-plane request-body policy.
const UploadReadTimeout = 30 * time.Second

// Profile is a provider's declared wire dialect and client environment names.
// Variables stay local to the composition root, never taken from an HTTP report.
type Profile struct {
	Provider             string
	Region               string
	MissingBucket        MissingBucketShape
	Variables            []string
	RegionVariable       string
	BucketConfiguration  bool
	AbsentConfigurations map[string]AbsentConfiguration
	ObjectDiscovery      bool
	ListObjectsV1        bool
	OwnerID              string
	PrivateACLWrite      bool
	// CloudHost is the measured authority used by clients ignoring endpoints.
	CloudHost string
	// HostEndpoint selects the host door for clients that discard path prefixes.
	HostEndpoint bool
}

// Door describes an enabled prefix and optional local HTTP proxy authority.
type Door struct {
	Provider string `json:"provider"`
	Region   string `json:"region"`
	Path     string `json:"path"`
	Host     string `json:"host"`
}

// Report owns the wire shape consumed by env/status/doctor and conformance.
type Report struct {
	SchemaVersion  int               `json:"schema_version"`
	Enabled        bool              `json:"enabled"`
	MaxBlobBytes   int64             `json:"max_blob_bytes"`
	MaxObjectBytes int64             `json:"max_object_bytes"`
	Doors          []Door            `json:"doors"`
	HTTPRequests   map[string]uint64 `json:"http_requests"`
}

// DoorPath returns the declared endpoint prefix; provider and region must be
// validated tokens before using it to dispatch a request or export a variable.
func DoorPath(provider, region string) string {
	return ReportPath + "/" + provider + "/" + region + "/"
}

// DoorHost is a synthetic authority, never a cloud hostname or a DNS requirement.
// Like DoorPath, it requires validated provider and region tokens.
func DoorHost(provider, region string) string {
	return provider + "." + region + ".s3.feint.invalid"
}

// ValidToken limits namespace components to short lower-case ASCII tokens.
func ValidToken(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	for _, c := range value {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// Doors dispatches declared S3 paths before any path-cleaning control-plane mux.
// The caller must keep its outer Host/Origin guard and own store shutdown.
type Doors struct {
	next     http.Handler
	log      *slog.Logger
	handlers map[string]*Server
	hosts    map[string]*Server
	mu       sync.Mutex
	report   Report
}

// Mount composes a control-plane handler and explicitly selected S3 doors.
// A nil store and no profiles publishes disabled S3 without activating storage.
func Mount(next http.Handler, store *objectstore.Store, profiles []Profile, maxObjectBytes int64, log *slog.Logger) (*Doors, error) {
	return mount(next, store, profiles, maxObjectBytes, log, false)
}

// MountHosts additionally declares synthetic authorities for the same scopes.
// Handler must be used to receive local absolute-form HTTP proxy requests.
// No request is forwarded and no DNS or extra listener is involved.
func MountHosts(next http.Handler, store *objectstore.Store, profiles []Profile, maxObjectBytes int64, log *slog.Logger) (*Doors, error) {
	return mount(next, store, profiles, maxObjectBytes, log, true)
}

func mount(next http.Handler, store *objectstore.Store, profiles []Profile, maxObjectBytes int64, log *slog.Logger, hosts bool) (*Doors, error) {
	if next == nil || log == nil || (store == nil) != (len(profiles) == 0) {
		return nil, errors.New("s3: handler, logger and consistent explicit storage activation are required")
	}
	d := &Doors{next: next, log: log, handlers: make(map[string]*Server), hosts: make(map[string]*Server), report: Report{
		SchemaVersion: ReportVersion, Enabled: store != nil, Doors: []Door{}, HTTPRequests: make(map[string]uint64),
	}}
	if store == nil {
		return d, nil
	}
	maxBlobBytes := store.BlobLimit()
	if maxBlobBytes <= 0 || maxObjectBytes > maxBlobBytes {
		return nil, errors.New("s3: positive blob budget must cover the per-object limit")
	}
	d.report.MaxBlobBytes, d.report.MaxObjectBytes = maxBlobBytes, maxObjectBytes
	for _, p := range profiles {
		if !ValidToken(p.Provider) || !ValidToken(p.Region) {
			return nil, errors.New("s3: invalid declared provider or region")
		}
		path := DoorPath(p.Provider, p.Region)
		if _, exists := d.handlers[path]; exists {
			return nil, fmt.Errorf("s3: duplicate door %s", path)
		}
		server, err := New(store, Config{Region: p.Region, MaxObjectBytes: maxObjectBytes,
			MissingBucket: p.MissingBucket, Namespace: p.Provider + "/" + p.Region, Log: log,
			BucketConfiguration: p.BucketConfiguration, AbsentConfigurations: p.AbsentConfigurations,
			ObjectDiscovery: p.ObjectDiscovery, ListObjectsV1: p.ListObjectsV1, OwnerID: p.OwnerID,
			PrivateACLWrite: p.PrivateACLWrite})
		if err != nil {
			return nil, err
		}
		d.handlers[path] = server
		door := Door{Provider: p.Provider, Region: p.Region, Path: path}
		if hosts {
			door.Host = DoorHost(p.Provider, p.Region)
			cfg := server.cfg
			cfg.endpointHost = door.Host
			d.hosts[door.Host] = &Server{store: store, cfg: cfg}
		}
		d.report.Doors = append(d.report.Doors, door)
	}
	return d, nil
}

// Handler keeps the control-plane rebinding guard, admitting only declared
// absolute-form HTTP authorities to S3's own loopback/Origin boundary.
func (d *Doors) Handler(addr string) http.Handler {
	guarded := emulator.GuardRebinding(d, addr)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler := d.hosts[r.Host]
		if handler == nil && !r.URL.IsAbs() && r.Method != http.MethodConnect {
			guarded.ServeHTTP(w, r)
			return
		}
		// TestHostDoorsRefuseAmbiguousProxyTargets holds the authority boundary.
		if handler == nil || r.Method == http.MethodConnect || r.URL.Scheme != "http" ||
			r.URL.Host != r.Host || r.URL.User != nil || r.URL.Fragment != "" || r.URL.Opaque != "" {
			d.log.WarnContext(r.Context(), "S3 proxy target refused")
			http.Error(w, "feint: only declared absolute-form HTTP S3 targets are supported; never forwarded", http.StatusForbidden)
			return
		}
		d.serveDoor(w, r, handler, "http://"+r.Host)
	})
}

// Snapshot returns detached report data so readers cannot mutate live evidence.
func (d *Doors) Snapshot() Report {
	d.mu.Lock()
	defer d.mu.Unlock()
	report := d.report
	report.Doors = append([]Door{}, d.report.Doors...)
	report.HTTPRequests = make(map[string]uint64, len(d.report.HTTPRequests))
	for key, value := range d.report.HTTPRequests {
		report.HTTPRequests[key] = value
	}
	return report
}

// ServeHTTP never routes a reserved S3 path through the cleaning mux.
func (d *Doors) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == ReportPath {
		if r.Method != http.MethodGet || r.URL.EscapedPath() != ReportPath || r.URL.RawQuery != "" {
			d.refuse(w, r, "invalid S3 report request")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(d.Snapshot()); err != nil {
			d.log.ErrorContext(r.Context(), "S3 report encoding failed", "error", err)
		}
		return
	}
	if !strings.HasPrefix(r.URL.Path, ReportPath+"/") {
		d.next.ServeHTTP(w, r)
		return
	}
	raw := r.URL.EscapedPath()
	for prefix, handler := range d.handlers {
		remainder, ok := strings.CutPrefix(raw, prefix)
		// SCW ListBuckets uses the canonical bare endpoint rather than its
		// slash-suffixed root. No redirect or path cleaning is involved.
		if !ok && r.Method == http.MethodGet && handler.cfg.BucketConfiguration && raw == strings.TrimSuffix(prefix, "/") {
			remainder, ok = "", true
		}
		if ok {
			path, err := url.PathUnescape("/" + remainder)
			if err != nil {
				d.refuse(w, r, "invalid encoded S3 path")
				return
			}
			request := r.Clone(r.Context())
			request.URL.Path, request.URL.RawPath = path, "/"+remainder
			d.serveDoor(w, request, handler, prefix)
			return
		}
	}
	d.refuse(w, r, "S3 door is not enabled or its prefix is not canonical")
}

func (d *Doors) serveDoor(w http.ResponseWriter, r *http.Request, handler *Server, endpoint string) {
	observed := &doorWriter{ResponseWriter: w}
	defer func() {
		// TestAnInterruptedDoorDoesNotInventResponseEvidence holds this.
		if observed.status == 0 {
			d.log.ErrorContext(r.Context(), "S3 request ended without response headers")
			return
		}
		d.mu.Lock()
		key := endpoint + " " + fmt.Sprint(observed.status)
		d.report.HTTPRequests[key]++
		d.mu.Unlock()
	}()
	if r.Method == http.MethodPut {
		if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(UploadReadTimeout)); err != nil {
			d.log.ErrorContext(r.Context(), "S3 upload read deadline unavailable", "error", err)
			http.Error(observed, "feint: S3 upload read deadline unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	handler.ServeHTTP(observed, r)
}

func (d *Doors) refuse(w http.ResponseWriter, r *http.Request, reason string) {
	d.log.WarnContext(r.Context(), "S3 dispatch refused", "reason", reason)
	http.Error(w, "feint: "+reason, http.StatusNotFound)
}

type doorWriter struct {
	http.ResponseWriter
	status int
}

func (w *doorWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *doorWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func (w *doorWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
