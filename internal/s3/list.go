package s3

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type listOptions struct {
	prefix, delimiter, encoding, token, after string
	max                                       int
}

func listQueryKey(key string) bool {
	switch key {
	case "list-type", "prefix", "delimiter", "max-keys", "continuation-token", "start-after", "encoding-type", "fetch-owner", "x-id":
		return true
	}
	return false
}

func parseListOptions(q url.Values) (listOptions, error) {
	options := listOptions{prefix: q.Get("prefix"), delimiter: q.Get("delimiter"),
		encoding: q.Get("encoding-type"), token: q.Get("continuation-token"), after: q.Get("start-after"), max: 1000}
	for key, values := range q {
		if !listQueryKey(key) || len(values) != 1 {
			return options, fmt.Errorf("unknown or duplicate listing parameter %q", key)
		}
	}
	if q.Get("list-type") != "2" || (options.encoding != "" && options.encoding != "url") ||
		(q.Has("fetch-owner") && q.Get("fetch-owner") != "false") || (options.token != "" && options.after != "") {
		return options, errors.New("only ListObjectsV2 without owner requests is supported")
	}
	for _, value := range []string{options.prefix, options.delimiter, options.after} {
		if len(value) > 1024 || !utf8.ValidString(value) {
			return options, errors.New("listing key parameters must be bounded UTF-8")
		}
	}
	if q.Has("max-keys") {
		n, err := strconv.Atoi(q.Get("max-keys"))
		if err != nil || n < 0 || n > 1000 {
			return options, errors.New("max-keys must be between 0 and 1000")
		}
		options.max = n
	}
	if len(options.token) > 4096 {
		return options, errors.New("invalid continuation token length")
	}
	return options, nil
}

type listCursor struct {
	Scope string `json:"scope"`
	After string `json:"after"`
}

func (s *Server) listScope(bucket string, options listOptions) string {
	// A cursor is scope-bound, not signed or authenticated. It is not IAM.
	sum := sha256.Sum256(fmt.Appendf(nil, "%q/%q/%q", s.name(bucket), options.prefix, options.delimiter))
	return hex.EncodeToString(sum[:])
}

func (s *Server) listAfter(bucket string, options listOptions) (string, error) {
	if options.token == "" {
		return options.after, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(options.token)
	if err != nil || !utf8.Valid(raw) {
		return "", fmt.Errorf("decode listing cursor: %w", errors.Join(err, errors.New("cursor must be base64url UTF-8 JSON")))
	}
	var cursor listCursor
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return "", fmt.Errorf("decode listing cursor: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", errors.New("trailing listing cursor data")
	}
	if cursor.Scope != s.listScope(bucket, options) || cursor.After == "" ||
		len(cursor.After) > 1024 || !utf8.ValidString(cursor.After) {
		return "", errors.New("listing cursor does not match the declared scope and prefix")
	}
	return cursor.After, nil
}

type listedObject struct {
	Key          string `xml:"Key"`
	Modified     string `xml:"LastModified"`
	Size         int64  `xml:"Size"`
	StorageClass string `xml:"StorageClass"`
}

type listedPrefix struct {
	Prefix string `xml:"Prefix"`
}

type listResult struct {
	XMLName   xml.Name       `xml:"ListBucketResult"`
	Namespace string         `xml:"xmlns,attr"`
	Name      string         `xml:"Name"`
	Prefix    string         `xml:"Prefix"`
	Delimiter string         `xml:"Delimiter,omitempty"`
	MaxKeys   int            `xml:"MaxKeys"`
	KeyCount  int            `xml:"KeyCount"`
	Truncated bool           `xml:"IsTruncated"`
	Encoding  string         `xml:"EncodingType,omitempty"`
	Token     string         `xml:"ContinuationToken,omitempty"`
	Next      string         `xml:"NextContinuationToken,omitempty"`
	After     string         `xml:"StartAfter,omitempty"`
	Objects   []listedObject `xml:"Contents"`
	Prefixes  []listedPrefix `xml:"CommonPrefixes"`
}

func listText(value, encoding string) (string, error) {
	if encoding == "url" {
		return strings.ReplaceAll(url.QueryEscape(value), "+", "%20"), nil
	}
	for _, c := range value {
		if (c < 0x20 && c != '\t' && c != '\n' && c != '\r') || c == 0xfffe || c == 0xffff {
			return "", errors.New("key needs encoding-type=url to be represented in XML")
		}
	}
	return value, nil
}

func (s *Server) listObjects(w http.ResponseWriter, r *http.Request, bucket string, q url.Values) {
	options, err := parseListOptions(q)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, "invalid S3 listing", err)
		return
	}
	after, err := s.listAfter(bucket, options)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, "invalid S3 listing cursor", err)
		return
	}
	objects, err := s.store.ListObjects(r.Context(), s.name(bucket))
	if err != nil {
		s.storeError(w, r, bucket, err)
		return
	}
	entries := make(map[string]bool)
	for key := range objects {
		if !strings.HasPrefix(key, options.prefix) {
			continue
		}
		name, prefix := key, false
		if options.delimiter != "" {
			if i := strings.Index(strings.TrimPrefix(key, options.prefix), options.delimiter); i >= 0 {
				name, prefix = key[:len(options.prefix)+i+len(options.delimiter)], true
			}
		}
		if name > after {
			entries[name] = prefix
		}
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	result := listResult{Namespace: s3XMLNamespace, Name: bucket, MaxKeys: options.max,
		Encoding: options.encoding, Token: options.token, After: options.after,
		Truncated: options.max > 0 && len(names) > options.max}
	result.Prefix, err = listText(options.prefix, options.encoding)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, "S3 listing prefix cannot be encoded", err)
		return
	}
	result.Delimiter, err = listText(options.delimiter, options.encoding)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, "S3 listing delimiter cannot be encoded", err)
		return
	}
	result.After, err = listText(options.after, options.encoding)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, "S3 listing start-after cannot be encoded", err)
		return
	}
	for _, name := range names[:min(len(names), options.max)] {
		text, err := listText(name, options.encoding)
		if err != nil {
			s.refuse(w, r, http.StatusNotImplemented, "S3 listing key cannot be encoded without encoding-type=url", err)
			return
		}
		result.KeyCount++
		if entries[name] {
			result.Prefixes = append(result.Prefixes, listedPrefix{Prefix: text})
			continue
		}
		obj := objects[name]
		if obj.Modified.IsZero() {
			s.refuse(w, r, http.StatusNotImplemented, "legacy object has no persisted modification date; overwrite it before listing", nil)
			return
		}
		result.Objects = append(result.Objects, listedObject{Key: text,
			Modified: obj.Modified.UTC().Format(time.RFC3339Nano), Size: obj.Size, StorageClass: "STANDARD"})
	}
	if result.Truncated {
		raw, err := json.Marshal(listCursor{Scope: s.listScope(bucket, options), After: names[options.max-1]})
		if err != nil {
			s.refuse(w, r, http.StatusInternalServerError, "S3 listing cursor encoding failed", err)
			return
		}
		result.Next = base64.RawURLEncoding.EncodeToString(raw)
	}
	raw, err := xml.Marshal(result)
	if err != nil {
		s.refuse(w, r, http.StatusInternalServerError, "S3 listing encoding failed", err)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(raw); err != nil {
		s.cfg.Log.ErrorContext(r.Context(), "S3 listing write failed", "error", err)
	}
}
