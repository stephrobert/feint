package s3

import (
	"crypto/md5" //nolint:gosec // Legacy UploadPart transport integrity, not authentication or an ETag.
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func multipartQueryKey(key string) bool {
	switch key {
	case "uploads", "uploadId", "partNumber", "max-uploads", "key-marker", "upload-id-marker",
		"prefix", "max-parts", "part-number-marker", "x-id":
		return true
	}
	return false
}

func (s *Server) multipart(w http.ResponseWriter, r *http.Request, bucket, key string, q url.Values) {
	if _, err := listText(key, ""); err != nil {
		s.refuse(w, r, http.StatusBadRequest, "multipart keys must be lossless XML text", err)
		return
	}
	if len(r.Header.Values("Range")) != 0 {
		s.refuse(w, r, http.StatusNotImplemented, "Range requires an object byte GET, not multipart inventory", nil)
		return
	}
	operation := ""
	allowed := map[string]bool{"x-id": true}
	switch {
	case q.Has("uploads") && !q.Has("uploadId") && q.Get("uploads") == "" && key != "" && r.Method == http.MethodPost:
		operation, allowed["uploads"] = "CreateMultipartUpload", true
	case q.Has("uploads") && !q.Has("uploadId") && q.Get("uploads") == "" && key == "" && r.Method == http.MethodGet:
		operation, allowed["uploads"] = "ListMultipartUploads", true
		for _, name := range []string{"max-uploads", "key-marker", "upload-id-marker", "prefix"} {
			allowed[name] = true
		}
	case q.Get("uploadId") != "" && !q.Has("uploads") && key != "":
		allowed["uploadId"] = true
		switch r.Method {
		case http.MethodPut:
			operation, allowed["partNumber"] = "UploadPart", true
		case http.MethodGet:
			operation = "ListParts"
			allowed["max-parts"], allowed["part-number-marker"] = true, true
		case http.MethodPost:
			operation = "CompleteMultipartUpload"
		case http.MethodDelete:
			operation = "AbortMultipartUpload"
		}
	}
	if operation == "" {
		s.refuse(w, r, http.StatusNotImplemented, "multipart operation is not supported", nil)
		return
	}
	for name, values := range q {
		if !allowed[name] || len(values) != 1 ||
			(name == "x-id" && values[0] != operation) || len(values[0]) > 1024 {
			s.refuse(w, r, http.StatusBadRequest, "invalid or unsupported multipart query", nil)
			return
		}
	}
	switch operation {
	case "CreateMultipartUpload":
		options, err := requestMetadata(r)
		if err != nil {
			s.refuse(w, r, http.StatusBadRequest, "invalid multipart metadata", err)
			return
		}
		body, err := xmlBody(w, r, 1)
		if err != nil || len(body) != 0 {
			s.refuse(w, r, http.StatusBadRequest, "multipart initiation requires an empty body", err)
			return
		}
		id, err := s.store.CreateUpload(r.Context(), s.name(bucket), key, options)
		if err != nil {
			s.storeError(w, r, bucket, err)
			return
		}
		s.writeXML(w, r, http.StatusOK, initiatedUpload{Bucket: bucket, Key: key, ID: id})
	case "UploadPart":
		s.uploadPart(w, r, bucket, key, q)
	case "ListMultipartUploads":
		s.listUploads(w, r, bucket, q)
	case "ListParts":
		s.listParts(w, r, bucket, key, q)
	case "CompleteMultipartUpload":
		s.completeUpload(w, r, bucket, key, q.Get("uploadId"))
	case "AbortMultipartUpload":
		if err := s.store.AbortUpload(r.Context(), s.name(bucket), key, q.Get("uploadId")); err != nil {
			s.storeError(w, r, bucket, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type initiatedUpload struct {
	XMLName xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ InitiateMultipartUploadResult"`
	Bucket  string
	Key     string
	ID      string `xml:"UploadId"`
}

type completedUpload struct {
	XMLName xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ CompleteMultipartUploadResult"`
	Bucket  string
	Key     string
	ETag    string
}

func partTag(digest string) string { return `"` + digest + `"` }

// The SDK defines ETags as opaque entity tags, not necessarily MD5. Multipart
// tokens identify verified SHA-256 content; no MD5 or generic ETag claim is made.
func (s *Server) uploadPart(w http.ResponseWriter, r *http.Request, bucket, key string, q url.Values) {
	number, err := multipartNumber(q, "partNumber", 0, 1, 10000)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, "invalid multipart part number", err)
		return
	}
	upload, size, err := newUpload(w, r, s.cfg.MaxObjectBytes)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, "invalid multipart payload", err)
		return
	}
	var body io.Reader = upload
	if values := r.Header.Values("Content-MD5"); len(values) != 0 {
		sum, err := base64.StdEncoding.DecodeString(values[0])
		if len(values) != 1 || err != nil || len(sum) != md5.Size {
			s.refuse(w, r, http.StatusBadRequest, "invalid multipart Content-MD5", err)
			return
		}
		body = &partChecksum{reader: upload, hash: md5.New(), expected: values[0]} //nolint:gosec // Verify the legacy transport checksum before publication.
	}
	part, err := s.store.PutPart(r.Context(), s.name(bucket), key, q.Get("uploadId"), number, body, size)
	if err != nil {
		s.storeError(w, r, bucket, err)
		return
	}
	if !s.digestHeader(w, r, part) {
		return
	}
	w.Header().Set("ETag", partTag(part.Digest))
	w.Header().Set("X-Amz-Checksum-Crc32", upload.crc32())
	w.WriteHeader(http.StatusOK)
}

type partChecksum struct {
	reader   io.Reader
	hash     hash.Hash
	expected string
}

func (r *partChecksum) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if _, hashErr := r.hash.Write(p[:n]); hashErr != nil {
		return n, hashErr
	}
	if errors.Is(err, io.EOF) && base64.StdEncoding.EncodeToString(r.hash.Sum(nil)) != r.expected {
		return n, fmt.Errorf("%w: multipart Content-MD5 mismatch", errUpload)
	}
	return n, err
}

func multipartNumber(q url.Values, name string, fallback, min, max int) (int, error) {
	if !q.Has(name) {
		if fallback < min {
			return 0, errors.New("missing numeric multipart query")
		}
		return fallback, nil
	}
	raw := q.Get(name)
	if raw == "" || strings.IndexFunc(raw, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return 0, errors.New("multipart query requires decimal digits")
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return 0, errors.New("multipart query exceeds its bounds")
	}
	return n, nil
}

func (s *Server) completeUpload(w http.ResponseWriter, r *http.Request, bucket, key, id string) {
	body, err := xmlBody(w, r, maxDeleteXMLBytes)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, "invalid multipart completion body", err)
		return
	}
	refs, err := completionParts(body)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, "invalid multipart completion XML", err)
		return
	}
	upload, err := s.store.ReadUpload(r.Context(), s.name(bucket), key, id)
	if err != nil {
		s.storeError(w, r, bucket, err)
		return
	}
	for i, ref := range refs {
		part, ok := upload.Parts[ref.Number]
		if !ok || ref.Digest != part.Digest || (i < len(refs)-1 && part.Size < 5<<20) {
			s.refuse(w, r, http.StatusBadRequest, "multipart parts or minimum size do not match", nil)
			return
		}
	}
	obj, err := s.store.CompleteUpload(r.Context(), s.name(bucket), key, id, refs, s.cfg.MaxObjectBytes)
	if err != nil {
		s.storeError(w, r, bucket, err)
		return
	}
	s.writeXML(w, r, http.StatusOK, completedUpload{Bucket: bucket, Key: key, ETag: partTag(obj.Digest)})
}
