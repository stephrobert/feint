package s3

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// LocalTransport maps declared cloud S3 authorities exclusively to one loopback
// emulator. It preserves virtual-host buckets, including dotted names, and
// literal escaped object keys. Unknown authorities never reach a dial.
// TLS interception and process network isolation belong to the caller.
type LocalTransport struct {
	target    url.URL
	profiles  map[string]Profile
	transport http.Transport
}

// NewLocalTransport requires an explicit loopback HTTP target without a path.
// It never inherits environment proxies or a cloud-capable fallback transport.
func NewLocalTransport(target string, profiles []Profile) (*LocalTransport, error) {
	base, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("parse local S3 target: %w", err)
	}
	ip := net.ParseIP(base.Hostname())
	if base.Scheme != "http" || ip == nil || !ip.IsLoopback() || base.Port() == "" ||
		base.User != nil || (base.Path != "" && base.Path != "/") || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("s3: local transport requires an explicit loopback HTTP socket")
	}
	t := &LocalTransport{target: *base, profiles: make(map[string]Profile)}
	for _, profile := range profiles {
		if !ValidToken(profile.Provider) || !ValidToken(profile.Region) || profile.CloudHost == "" ||
			strings.ContainsAny(profile.CloudHost, "/:@* \t\r\n") || profile.CloudHost != strings.ToLower(profile.CloudHost) {
			return nil, errors.New("s3: local transport requires declared cloud authorities and scopes")
		}
		for host := range t.profiles {
			if host == profile.CloudHost || strings.HasSuffix(host, "."+profile.CloudHost) ||
				strings.HasSuffix(profile.CloudHost, "."+host) {
				return nil, errors.New("s3: overlapping cloud authority declarations")
			}
		}
		t.profiles[profile.CloudHost] = profile
	}
	if len(t.profiles) == 0 {
		return nil, errors.New("s3: local transport requires at least one declared cloud authority")
	}
	return t, nil
}

// RoundTrip refuses undeclared authorities before sending any request.
func (t *LocalTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL == nil || r.URL.Scheme != "https" || r.URL.User != nil ||
		(r.URL.Port() != "" && r.URL.Port() != "443") || r.URL.Fragment != "" {
		return nil, errors.New("s3: only declared HTTPS cloud requests may be redirected")
	}
	host := strings.ToLower(r.URL.Hostname())
	var profile Profile
	bucket := ""
	found := false
	for base, candidate := range t.profiles {
		if host == base {
			profile, found = candidate, true
			break
		}
		if prefix, matches := strings.CutSuffix(host, "."+base); matches {
			if !bucketName(prefix) {
				return nil, errors.New("s3: invalid virtual-host bucket")
			}
			profile, bucket, found = candidate, prefix, true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("s3: undeclared cloud authority %q; no request forwarded", host)
	}
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	if bucket != "" {
		path = "/" + url.PathEscape(bucket) + path
	}
	path = strings.TrimSuffix(DoorPath(profile.Provider, profile.Region), "/") + path
	decoded, err := url.PathUnescape(path)
	if err != nil {
		return nil, fmt.Errorf("decode mapped S3 path: %w", err)
	}
	out := r.Clone(r.Context())
	out.URL.Scheme, out.URL.Host = t.target.Scheme, t.target.Host
	out.URL.Path, out.URL.RawPath = decoded, path
	out.Host, out.RequestURI = t.target.Host, ""
	return t.transport.RoundTrip(out)
}

// CloseIdleConnections releases only this transport's loopback connections.
func (t *LocalTransport) CloseIdleConnections() { t.transport.CloseIdleConnections() }
