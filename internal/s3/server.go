// Package s3 adapts a neutral ObjectStore to a bounded, path-style S3 subset.
//
// It is a component, not a listener or a mounted provider pack. Callers retain
// ownership of the store. Only direct loopback requests are accepted. SigV4
// requests are accepted without authenticating their secret; this is not IAM.
package s3

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/stephrobert/feint/internal/core/objectstore"
)

// MissingBucketShape selects only the measured NoSuchBucket error envelope.
// It does not imply support for either provider's other S3 operations.
type MissingBucketShape uint8

const (
	// ScalewayMissingBucket follows the anonymous fr-par GET recording.
	ScalewayMissingBucket MissingBucketShape = iota + 1
	// ExoscaleMissingBucket follows the anonymous ch-gva-2 GET recording.
	ExoscaleMissingBucket
)

// Config requires an explicit region, byte bound, measured error shape and log.
// MaxObjectBytes bounds each upload, not cumulative disk use. Callers also need
// HTTP read deadlines, header limits and a cumulative quota before exposure.
type Config struct {
	Region         string
	MaxObjectBytes int64
	MissingBucket  MissingBucketShape
	Log            *slog.Logger
	// Namespace is an opaque deployment scope, not an authenticated tenant.
	Namespace string
	// BucketConfiguration enables the measured private, non-versioned bucket
	// workflow, independently of the missing-bucket error dialect.
	BucketConfiguration bool
	// Set only by the explicit host-door composition, not by direct callers.
	endpointHost string
}

// Server is a direct HTTP handler. Do not put it behind a path-cleaning mux:
// repeated slashes, dot segments and escaped characters are logical key data.
type Server struct {
	store *objectstore.Store
	cfg   Config
}

// New validates explicit dependencies; it neither opens storage nor listens.
func New(store *objectstore.Store, cfg Config) (*Server, error) {
	if store == nil || cfg.Log == nil || cfg.Region == "" || !headerValue(cfg.Region) ||
		len(cfg.Namespace) > 128 || !headerValue(cfg.Namespace) {
		return nil, errors.New("s3: store, logger and a header-safe region are required")
	}
	if cfg.MaxObjectBytes <= 0 || cfg.MaxObjectBytes > (math.MaxInt64-(64<<10))/2 {
		return nil, errors.New("s3: a positive representable upload byte bound is required")
	}
	if cfg.MissingBucket != ScalewayMissingBucket && cfg.MissingBucket != ExoscaleMissingBucket {
		return nil, errors.New("s3: select a measured missing-bucket response shape")
	}
	return &Server{store: store, cfg: cfg}, nil
}

// ServeHTTP implements bucket PUT/HEAD/DELETE, ListObjectsV2 and object
// PUT/GET/HEAD/DELETE, including single-byte-range GETs.
// Other features get an explicit Feint refusal, never a successful empty stub
// or an invented cloud XML error. Signatures and credentials are never logged.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if len(r.Header.Values("Origin")) != 0 {
		s.refuse(w, r, http.StatusForbidden, "browser Origin is not supported", nil)
		return
	}
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil || !peer.Addr().Unmap().IsLoopback() {
		s.refuse(w, r, http.StatusForbidden, "only direct loopback peers are supported", err)
		return
	}
	host := r.Host
	if h, _, splitErr := net.SplitHostPort(host); splitErr == nil {
		host = h
	}
	ip, ipErr := netip.ParseAddr(strings.Trim(host, "[]"))
	if host != "localhost" && (ipErr != nil || !ip.Unmap().IsLoopback()) &&
		(s.cfg.endpointHost == "" || r.Host != s.cfg.endpointHost) {
		s.refuse(w, r, http.StatusNotImplemented, "only explicit loopback path-style endpoints are supported", nil)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, "invalid query", err)
		return
	}
	rawBucket, rawKey, _ := strings.Cut(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
	listing := r.Method == http.MethodGet && rawKey == "" && query.Get("list-type") == "2"
	for key := range query {
		if listing && listQueryKey(key) {
			continue
		}
		if key != "x-id" && (!s.cfg.BucketConfiguration || (key != "versioning" && key != "acl" && key != "tagging")) {
			s.refuse(w, r, http.StatusNotImplemented, "S3 query features are not implemented", nil)
			return
		}
	}
	bucket, bucketErr := url.PathUnescape(rawBucket)
	key, keyErr := url.PathUnescape(rawKey)
	checked, err := s.bucketHeaders(r, query, rawKey == "")
	if err != nil {
		s.refuse(w, r, http.StatusNotImplemented, "S3 bucket header feature is not implemented", err)
		return
	}
	if err := supportedHeaders(checked); err != nil {
		s.refuse(w, r, http.StatusNotImplemented, "S3 header feature is not implemented", err)
		return
	}
	if rawBucket == "" && rawKey == "" && s.cfg.BucketConfiguration &&
		(len(query) == 0 || (len(query) == 1 && query.Has("x-id"))) && r.Method == http.MethodGet {
		s.listBuckets(w, r)
		return
	}
	if bucketErr != nil || keyErr != nil || !bucketName(bucket) || !utf8.ValidString(key) || len(key) > 1024 {
		s.refuse(w, r, http.StatusBadRequest, "invalid bucket or object key", errors.Join(bucketErr, keyErr))
		return
	}
	if rawKey == "" && len(r.Header.Values("Range")) != 0 {
		s.refuse(w, r, http.StatusNotImplemented, "Range requires an object GET", nil)
		return
	}
	if listing {
		s.listObjects(w, r, bucket, query)
		return
	}
	if key == "" {
		if len(query) != 0 && (len(query) != 1 || !query.Has("x-id")) {
			s.bucketConfiguration(w, r, bucket, query)
			return
		}
		s.bucket(w, r, bucket)
		return
	}
	if query.Has("versioning") || query.Has("acl") || query.Has("tagging") {
		s.refuse(w, r, http.StatusNotImplemented, "bucket configuration queries require a bucket, not an object", nil)
		return
	}
	switch r.Method {
	case http.MethodPut:
		s.put(w, r, bucket, key)
	case http.MethodGet, http.MethodHead:
		s.get(w, r, bucket, key)
	case http.MethodDelete:
		err := s.store.DeleteObject(r.Context(), s.name(bucket), key)
		if err != nil && !errors.Is(err, objectstore.ErrObjectNotFound) {
			s.storeError(w, r, bucket, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		s.refuse(w, r, http.StatusNotImplemented, "S3 operation is not implemented", nil)
	}
}

func (s *Server) bucket(w http.ResponseWriter, r *http.Request, name string) {
	var err error
	status := http.StatusOK
	switch r.Method {
	case http.MethodPut:
		if s.cfg.BucketConfiguration {
			body, readErr := configurationBody(w, r)
			if readErr != nil {
				s.refuse(w, r, http.StatusBadRequest, "invalid CreateBucket body", readErr)
				return
			}
			if len(body) != 0 {
				region, decodeErr := configurationValue(body, "CreateBucketConfiguration", "LocationConstraint")
				if decodeErr != nil || region != s.cfg.Region {
					s.refuse(w, r, http.StatusBadRequest, "CreateBucket region must match the declared door", decodeErr)
					return
				}
			}
			err = s.store.CreateBucketWithMetadata(r.Context(), s.name(name), map[string]string{
				bucketCreated: time.Now().UTC().Format(time.RFC3339Nano),
			})
			break
		}
		body, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
		if readErr != nil || len(body) != 0 {
			s.refuse(w, r, http.StatusNotImplemented, "CreateBucket configuration is not implemented", readErr)
			return
		}
		err = s.store.CreateBucket(r.Context(), s.name(name))
	case http.MethodHead:
		err = s.store.HeadBucket(r.Context(), s.name(name))
	case http.MethodDelete:
		err = s.store.DeleteBucket(r.Context(), s.name(name))
		status = http.StatusNoContent
	case http.MethodGet:
		err = s.store.HeadBucket(r.Context(), s.name(name))
		if err == nil {
			s.refuse(w, r, http.StatusNotImplemented, "bucket listing is not implemented", nil)
			return
		}
	default:
		s.refuse(w, r, http.StatusNotImplemented, "S3 operation is not implemented", nil)
		return
	}
	if err != nil {
		s.storeError(w, r, name, err)
		return
	}
	w.Header().Set("X-Amz-Bucket-Region", s.cfg.Region)
	w.WriteHeader(status)
}

func (s *Server) put(w http.ResponseWriter, r *http.Request, bucket, key string) {
	metadata := make(map[string]string)
	for name, values := range r.Header {
		if suffix, ok := strings.CutPrefix(strings.ToLower(name), "x-amz-meta-"); ok {
			if len(values) != 1 || !headerName(suffix) || !headerValue(values[0]) {
				s.refuse(w, r, http.StatusBadRequest, "invalid object metadata header", nil)
				return
			}
			metadata[suffix] = values[0]
		}
	}
	if !headerValue(r.Header.Get("Content-Type")) {
		s.refuse(w, r, http.StatusBadRequest, "invalid content type", nil)
		return
	}
	upload, size, err := newUpload(w, r, s.cfg.MaxObjectBytes)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, "invalid S3 upload", err)
		return
	}
	obj, err := s.store.PutObject(r.Context(), s.name(bucket), key, upload, objectstore.PutOptions{
		Size: size, ContentType: r.Header.Get("Content-Type"), Metadata: metadata,
	})
	if err != nil {
		s.storeError(w, r, bucket, err)
		return
	}
	// The SHA-256 is a verified byte checksum, deliberately not an ETag.
	if !s.digestHeader(w, r, obj) {
		return
	}
	w.Header().Set("X-Amz-Checksum-Crc32", upload.crc32())
	w.WriteHeader(http.StatusOK)
}

func (s *Server) get(w http.ResponseWriter, r *http.Request, bucket, key string) {
	obj, body, err := s.store.GetObject(r.Context(), s.name(bucket), key)
	if err != nil {
		s.storeError(w, r, bucket, err)
		return
	}
	defer func() {
		if err := body.Close(); err != nil {
			s.cfg.Log.ErrorContext(r.Context(), "S3 object reader close failed", "error", err)
		}
	}()
	if !headerValue(obj.ContentType) {
		s.refuse(w, r, http.StatusInternalServerError, "stored metadata cannot be represented as HTTP headers", nil)
		return
	}
	status, length := http.StatusOK, obj.Size
	if values := r.Header.Values("Range"); len(values) != 0 {
		if len(values) != 1 {
			s.refuse(w, r, http.StatusBadRequest, "ambiguous Range header", nil)
			return
		}
		offset, count, rangeErr := byteRange(values[0], obj.Size)
		if rangeErr != nil {
			code := http.StatusBadRequest
			if errors.Is(rangeErr, errRangeUnsatisfiable) {
				code = http.StatusRequestedRangeNotSatisfiable
				w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", obj.Size))
			} else if errors.Is(rangeErr, errRangeUnsupported) {
				code = http.StatusNotImplemented
			}
			s.refuse(w, r, code, "S3 byte range refused", rangeErr)
			return
		}
		if _, err := body.Seek(offset, io.SeekStart); err != nil {
			s.refuse(w, r, http.StatusInternalServerError, "verified object range seek failed", err)
			return
		}
		status, length = http.StatusPartialContent, count
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+count-1, obj.Size))
	}
	seen := make(map[string]bool)
	for key, value := range obj.Metadata {
		canonical := strings.ToLower(key)
		if !headerName(key) || !headerValue(value) || seen[canonical] {
			s.refuse(w, r, http.StatusInternalServerError, "stored metadata cannot be represented as HTTP headers", nil)
			return
		}
		seen[canonical] = true
	}
	if status == http.StatusOK && !s.digestHeader(w, r, obj) {
		return
	}
	w.Header().Set("Accept-Ranges", "bytes")
	if !obj.Modified.IsZero() {
		w.Header().Set("Last-Modified", obj.Modified.UTC().Format(http.TimeFormat))
	}
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.Header().Set("Content-Type", obj.ContentType)
	for key, value := range obj.Metadata {
		w.Header().Set("X-Amz-Meta-"+key, value)
	}
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		if _, err := io.CopyN(w, body, length); err != nil {
			s.cfg.Log.ErrorContext(r.Context(), "S3 download failed after headers", "error", err)
		}
	}
}

func (s *Server) digestHeader(w http.ResponseWriter, r *http.Request, obj objectstore.Object) bool {
	digest, err := hex.DecodeString(obj.Digest)
	if err != nil || len(digest) != 32 {
		s.refuse(w, r, http.StatusInternalServerError, "stored byte checksum is invalid", err)
		return false
	}
	w.Header().Set("X-Amz-Checksum-Sha256", base64.StdEncoding.EncodeToString(digest))
	return true
}

func (s *Server) storeError(w http.ResponseWriter, r *http.Request, bucket string, err error) {
	if errors.Is(err, objectstore.ErrBucketNotFound) {
		s.missingBucket(w, r, bucket)
		return
	}
	status, message := http.StatusInternalServerError, "object storage operation failed"
	var tooLarge *http.MaxBytesError
	switch {
	case errors.Is(err, objectstore.ErrQuota):
		status, message = http.StatusInsufficientStorage, "object storage blob byte budget exceeded"
	case errors.Is(err, objectstore.ErrObjectNotFound), errors.Is(err, objectstore.ErrBucketExists),
		errors.Is(err, objectstore.ErrBucketNotEmpty):
		status, message = http.StatusNotImplemented, "cloud error shape has not been measured for this store outcome"
	case errors.As(err, &tooLarge):
		status, message = http.StatusRequestEntityTooLarge, "upload exceeds the encoded byte bound"
	case errors.Is(err, errUpload), errors.Is(err, objectstore.ErrInvalid):
		status, message = http.StatusBadRequest, "invalid S3 upload or metadata"
	}
	s.refuse(w, r, status, message, err)
}

func (s *Server) refuse(w http.ResponseWriter, r *http.Request, status int, message string, err error) {
	s.cfg.Log.WarnContext(r.Context(), "S3 request refused", "method", r.Method, "status", status, "reason", message, "error", err)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		if _, writeErr := fmt.Fprintln(w, "feint: "+message); writeErr != nil {
			s.cfg.Log.ErrorContext(r.Context(), "S3 refusal response write failed", "error", writeErr)
		}
	}
}

// These fields and their order follow the two anonymous GET controls from
// phase 0, not a synthetic XML fixture. No other cloud error is inferred.
type missingBucketError struct {
	XMLName    xml.Name `xml:"Error"`
	Code       string
	Message    string
	RequestID  string `xml:"RequestId,omitempty"`
	HostID     string `xml:"HostId,omitempty"`
	Resource   string `xml:",omitempty"`
	BucketName string
}

func (s *Server) missingBucket(w http.ResponseWriter, r *http.Request, bucket string) {
	id, hostID := rand.Text(), rand.Text()
	wire := missingBucketError{Code: "NoSuchBucket", Message: "The specified bucket does not exist", BucketName: bucket}
	if s.cfg.MissingBucket == ScalewayMissingBucket {
		hostID = id
		wire.RequestID, wire.HostID, wire.Resource = id, hostID, "/"+bucket
	} else {
		wire.Message += "."
	}
	body, err := xml.Marshal(wire)
	if err != nil {
		s.refuse(w, r, http.StatusInternalServerError, "missing-bucket response encoding failed", err)
		return
	}
	if s.cfg.MissingBucket == ExoscaleMissingBucket {
		body = append([]byte(strings.TrimSuffix(xml.Header, "\n")), body...)
		w.Header().Set("X-Amzn-Request-Id", id)
	}
	w.Header().Set("X-Amz-Request-Id", id)
	w.Header().Set("X-Amz-Id-2", hostID)
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusNotFound)
	if r.Method != http.MethodHead {
		if _, err := w.Write(body); err != nil {
			s.cfg.Log.ErrorContext(r.Context(), "S3 missing-bucket response write failed", "error", err)
		}
	}
}

func bucketName(name string) bool {
	if len(name) < 3 || len(name) > 63 || name[0] == '.' || name[0] == '-' || name[len(name)-1] == '.' || name[len(name)-1] == '-' {
		return false
	}

	for _, c := range name {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '.' && c != '-' {
			return false
		}
	}
	return !strings.Contains(name, "..")
}

func (s *Server) name(bucket string) string {
	if s.cfg.Namespace == "" {
		return bucket
	}
	return s.cfg.Namespace + "/" + bucket
}

func headerValue(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, c := range value {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

func headerName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && !strings.ContainsRune("!#$%&'*+-.^_`|~", c) {
			return false
		}
	}
	return true
}

func supportedHeaders(r *http.Request) error {
	for name := range r.Header {
		lower := strings.ToLower(name)
		switch lower {
		case "range":
			if r.Method != http.MethodGet {
				return fmt.Errorf("range requires GET")
			}
		case "if-match", "if-none-match", "if-modified-since", "if-unmodified-since", "if-range", "content-md5":
			return fmt.Errorf("unsupported header %s", name)
		}
		if !strings.HasPrefix(lower, "x-amz-") || strings.HasPrefix(lower, "x-amz-meta-") {
			continue
		}
		switch lower {
		case "x-amz-date", "x-amz-content-sha256", "x-amz-security-token", "x-amz-user-agent",
			"x-amz-checksum-crc32", "x-amz-checksum-sha256", "x-amz-sdk-checksum-algorithm",
			"x-amz-trailer", "x-amz-decoded-content-length", "x-amz-checksum-mode":
		default:
			return fmt.Errorf("unsupported header %s", name)
		}
	}
	return nil
}
