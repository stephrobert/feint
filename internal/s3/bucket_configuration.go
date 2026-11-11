package s3

import (
	"bytes"
	"crypto/md5" //nolint:gosec // Content-MD5 is the client's transport checksum, not an ETag or authentication.
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	s3XMLNamespace   = "http://s3.amazonaws.com/doc/2006-03-01/"
	bucketVersioning = "s3.versioning"
	bucketCreated    = "s3.created"
)

// AbsentConfiguration is a recorded, provider-facing error for an unwritten
// setting. It does not grant support for writing that setting.
type AbsentConfiguration struct {
	Status     int
	Code       string
	Message    string
	BucketName bool
	RequestIDs bool
	Resource   bool
}

// BucketCreationTime reads the date persisted by bucket creation. A legacy
// bucket with no date is an error, never assigned a fabricated current time.
func BucketCreationTime(metadata map[string]string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, metadata[bucketCreated])
}

type absentConfigurationError struct {
	Code       string
	Message    string
	BucketName string   `xml:",omitempty"`
	RequestID  string   `xml:"RequestId,omitempty"`
	HostID     string   `xml:"HostId,omitempty"`
	Resource   string   `xml:",omitempty"`
	XMLName    xml.Name `xml:"Error"`
}

// Bucket configuration remains separate from object upload header support.
// Only the measured create defaults and versioning checksum are admitted.
func (s *Server) bucketHeaders(r *http.Request, query url.Values, bucket bool) (*http.Request, error) {
	if !s.cfg.BucketConfiguration || !bucket || r.Method != http.MethodPut {
		return r, nil
	}
	checked := r.Clone(r.Context())
	if s.cfg.PrivateACLWrite && query.Has("acl") {
		values := r.Header.Values("X-Amz-Acl")
		if len(values) != 1 || values[0] != "private" {
			return nil, errors.New("only the private bucket canned ACL is supported")
		}
		checked.Header.Del("X-Amz-Acl")
		return checked, nil
	}
	if query.Has("versioning") {
		checked.Header.Del("Content-MD5")
		return checked, nil
	}
	if len(query) != 0 && (len(query) != 1 || !query.Has("x-id")) {
		return checked, nil
	}
	for name, allowed := range map[string]string{"X-Amz-Acl": "private", "X-Amz-Bucket-Object-Lock-Enabled": "false"} {
		values := r.Header.Values(name)
		if len(values) != 0 {
			if len(values) != 1 || values[0] != allowed {
				return nil, fmt.Errorf("unsupported bucket default for %s", name)
			}
			checked.Header.Del(name)
		}
	}
	return checked, nil
}

func configurationBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	return xmlBody(w, r, 64<<10)
}

func xmlBody(w http.ResponseWriter, r *http.Request, max int64) ([]byte, error) {
	upload, size, err := newUpload(w, r, max)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(upload, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) != size {
		return nil, errors.New("S3 XML body does not match its bounded decoded size")
	}
	if values := r.Header.Values("Content-MD5"); len(values) != 0 {
		digest := md5.Sum(body) //nolint:gosec // Verify the legacy S3 transport checksum; never use it as identity.
		if len(values) != 1 || values[0] != base64.StdEncoding.EncodeToString(digest[:]) {
			return nil, errors.New("S3 XML Content-MD5 mismatch")
		}
	}
	return body, nil
}

// configurationValue accepts one leaf, not an XML document with silently
// discarded fields. TestBucketConfigurationRefusals holds that closed shape.
func configurationValue(body []byte, rootName, leafName string) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(body)) //nolint:gosec // Bounded body, no external entity resolver, directives rejected below.
	depth, roots, leaves := 0, 0, 0
	var value strings.Builder
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		switch t := token.(type) {
		case xml.StartElement:
			if (t.Name.Space != "" && t.Name.Space != s3XMLNamespace) ||
				(depth == 0 && (roots != 0 || t.Name.Local != rootName)) ||
				(depth == 1 && (leaves != 0 || t.Name.Local != leafName)) || depth > 1 {
				return "", errors.New("unexpected bucket configuration element")
			}
			for _, attr := range t.Attr {
				if attr.Name.Space != "" || attr.Name.Local != "xmlns" || attr.Value != s3XMLNamespace {
					return "", errors.New("unexpected bucket configuration attribute")
				}
			}
			if depth == 0 {
				roots++
			} else {
				leaves++
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 2 {
				value.Write(t)
			} else if strings.TrimSpace(string(t)) != "" {
				return "", errors.New("unexpected bucket configuration text")
			}
		case xml.ProcInst:
			if roots != 0 || t.Target != "xml" {
				return "", errors.New("unexpected bucket configuration instruction")
			}
		default:
			return "", errors.New("unexpected bucket configuration token")
		}
	}
	if depth != 0 || roots != 1 || leaves != 1 {
		return "", errors.New("bucket configuration requires exactly one value")
	}
	return value.String(), nil
}

type versioningConfiguration struct {
	XMLName xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ VersioningConfiguration"`
	Status  string   `xml:",omitempty"`
}

func (s *Server) bucketConfiguration(w http.ResponseWriter, r *http.Request, name string, query url.Values) {
	feature := ""
	for key, values := range query {
		if key == "x-id" {
			continue
		}
		if feature != "" || len(values) != 1 || values[0] != "" {
			s.refuse(w, r, http.StatusBadRequest, "one empty bucket configuration query is required", nil)
			return
		}
		feature = key
	}
	metadata, err := s.store.BucketMetadata(r.Context(), s.name(name))
	if err != nil {
		s.storeError(w, r, name, err)
		return
	}
	if missing, ok := s.cfg.AbsentConfigurations[feature]; ok && r.Method == http.MethodGet {
		response := absentConfigurationError{Code: missing.Code, Message: missing.Message}
		if missing.BucketName {
			response.BucketName = name
		}
		if missing.RequestIDs {
			id := rand.Text()
			response.RequestID, response.HostID = id, id
			w.Header().Set("X-Amz-Request-Id", id)
			w.Header().Set("X-Amz-Id-2", id)
		}
		if missing.Resource {
			response.Resource = "/" + name
		}
		s.writeXML(w, r, missing.Status, response)
		return
	}

	switch {
	case feature == "acl" && r.Method == http.MethodPut && s.cfg.PrivateACLWrite:
		body, err := configurationBody(w, r)
		if err != nil || len(body) != 0 {
			s.refuse(w, r, http.StatusNotImplemented, "only empty-body private bucket ACL writes are implemented", err)
			return
		}
		w.WriteHeader(http.StatusOK)
	case feature == "versioning" && r.Method == http.MethodPut:
		body, readErr := configurationBody(w, r)
		if readErr != nil {
			s.refuse(w, r, http.StatusBadRequest, "invalid bucket versioning body", readErr)
			return
		}
		status, decodeErr := configurationValue(body, "VersioningConfiguration", "Status")
		if decodeErr != nil {
			s.refuse(w, r, http.StatusBadRequest, "invalid bucket versioning XML", decodeErr)
			return
		}
		if status != "Suspended" {
			s.refuse(w, r, http.StatusNotImplemented, "object version histories are not implemented", nil)
			return
		}
		if err := s.store.SetBucketMetadata(r.Context(), s.name(name), bucketVersioning, status); err != nil {
			s.storeError(w, r, name, err)
			return
		}
		w.WriteHeader(http.StatusOK)
	case feature == "versioning" && r.Method == http.MethodGet:
		status := metadata[bucketVersioning]
		if status != "" && status != "Suspended" {
			s.refuse(w, r, http.StatusInternalServerError, "persisted bucket versioning is not supported", nil)
			return
		}
		s.writeXML(w, r, http.StatusOK, versioningConfiguration{Status: status})
	case feature == "acl" && r.Method == http.MethodGet:
		owner := s.bucketOwner()
		s.writeXML(w, r, http.StatusOK, accessControlPolicy{Owner: owner, Grant: aclGrant{
			Grantee: aclGrantee{Type: "CanonicalUser", XSI: "http://www.w3.org/2001/XMLSchema-instance",
				ID: owner.ID, DisplayName: owner.DisplayName}, Permission: "FULL_CONTROL",
		}})
	case feature == "tagging" && r.Method == http.MethodGet:
		id := rand.Text()
		w.Header().Set("X-Amz-Request-Id", id)
		w.Header().Set("X-Amz-Id-2", id)
		// Addressed rather than inlined: missingBucketError's Resource and
		// BucketName became pointers so the Outscale envelope could render
		// <Resource></Resource> present-and-empty, which `omitempty` on a
		// string cannot express. This answer is unchanged — both fields are
		// filled here, as the recording for it has them.
		resource := "/" + name
		s.writeXML(w, r, http.StatusNotFound, missingBucketError{Code: "NoSuchTagSet",
			Message: "The TagSet does not exist", RequestID: id, HostID: id,
			Resource: &resource, BucketName: &name})
	default:
		s.refuse(w, r, http.StatusNotImplemented, "bucket configuration operation is not implemented", nil)
	}
}

func (s *Server) objectConfiguration(w http.ResponseWriter, r *http.Request, bucket, key string, query url.Values) {
	feature := ""
	for name, values := range query {
		if name == "x-id" {
			continue
		}
		if feature != "" || len(values) != 1 || values[0] != "" || (name != "acl" && name != "tagging") {
			s.refuse(w, r, http.StatusBadRequest, "one empty object discovery query is required", nil)
			return
		}
		feature = name
	}
	if len(r.Header.Values("Range")) != 0 {
		s.refuse(w, r, http.StatusNotImplemented, "Range requires object bytes, not object configuration", nil)
		return
	}
	if _, err := s.store.HeadObject(r.Context(), s.name(bucket), key); err != nil {
		s.storeError(w, r, bucket, err)
		return
	}
	if feature == "acl" {
		owner := s.bucketOwner()
		s.writeXML(w, r, http.StatusOK, accessControlPolicy{Owner: owner, Grant: aclGrant{
			Grantee: aclGrantee{Type: "CanonicalUser", XSI: "http://www.w3.org/2001/XMLSchema-instance",
				ID: owner.ID, DisplayName: owner.DisplayName}, Permission: "FULL_CONTROL",
		}})
		return
	}
	s.writeXML(w, r, http.StatusOK, struct {
		XMLName xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ Tagging"`
		TagSet  struct{}
	}{})
}

// These paths, namespaces and the canonical private grant follow the official
// v2.62.0 create cassette. The owner is a stable local identity, not cloud IAM.
type bucketOwner struct {
	DisplayName string
	ID          string
}

type aclGrantee struct {
	XSI         string `xml:"xmlns:xsi,attr"`
	Type        string `xml:"xsi:type,attr"`
	ID          string
	DisplayName string
}

type aclGrant struct {
	Grantee    aclGrantee
	Permission string
}

type accessControlPolicy struct {
	XMLName xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ AccessControlPolicy"`
	Owner   bucketOwner
	Grant   aclGrant `xml:"AccessControlList>Grant"`
}

func (s *Server) bucketOwner() bucketOwner {
	if s.cfg.OwnerID != "" {
		return bucketOwner{DisplayName: "feint", ID: s.cfg.OwnerID}
	}
	sum := sha256.Sum256([]byte("feint-local-owner/" + s.cfg.Namespace))
	return bucketOwner{DisplayName: "feint", ID: hex.EncodeToString(sum[:])}
}

type listedBucket struct {
	Name         string
	CreationDate string
}

type listedBuckets struct {
	XMLName xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ ListAllMyBucketsResult"`
	Owner   bucketOwner
	Buckets []listedBucket `xml:"Buckets>Bucket"`
}

func (s *Server) listBuckets(w http.ResponseWriter, r *http.Request) {
	inventory, err := s.store.ListBucketMetadata(r.Context())
	if err != nil {
		s.storeError(w, r, "", err)
		return
	}
	result := listedBuckets{Owner: s.bucketOwner()}
	for _, name := range slices.Sorted(maps.Keys(inventory)) {
		wireName := name
		if s.cfg.Namespace != "" {
			var matches bool
			wireName, matches = strings.CutPrefix(name, s.cfg.Namespace+"/")
			if !matches {
				continue
			}
		}
		metadata := inventory[name]
		created := metadata[bucketCreated]
		if _, err := BucketCreationTime(metadata); err != nil || !bucketName(wireName) {
			s.refuse(w, r, http.StatusNotImplemented, "legacy bucket listing has no measured creation metadata", err)
			return
		}
		result.Buckets = append(result.Buckets, listedBucket{Name: wireName, CreationDate: created})
	}
	s.writeXML(w, r, http.StatusOK, result)
}

func (s *Server) writeXML(w http.ResponseWriter, r *http.Request, status int, value any) {
	body, err := xml.Marshal(value)
	if err != nil {
		s.refuse(w, r, http.StatusInternalServerError, "bucket response encoding failed", err)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		s.cfg.Log.ErrorContext(r.Context(), "S3 bucket response write failed", "error", err)
	}
}
