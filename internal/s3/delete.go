package s3

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

const maxDeleteXMLBytes = 8 << 20

type deleteRequest struct {
	keys  []string
	quiet bool
}

// Names and namespace follow the S3 v1.2.0 serializer/deserializer used by
// exo 1.95.1. TestMountedS3OfficialAWSCLIBytes holds the real client's decoding.
type deletedObject struct {
	Key string
}

type deleteResult struct {
	XMLName xml.Name        `xml:"http://s3.amazonaws.com/doc/2006-03-01/ DeleteResult"`
	Deleted []deletedObject `xml:"Deleted,omitempty"`
}

func (s *Server) deleteObjects(w http.ResponseWriter, r *http.Request, bucket string, query url.Values) {
	for key, values := range query {
		if len(values) != 1 || (key != "delete" && key != "x-id") ||
			(key == "delete" && values[0] != "") || (key == "x-id" && values[0] != "DeleteObjects") {
			s.refuse(w, r, http.StatusBadRequest, "DeleteObjects requires one empty delete query", nil)
			return
		}
	}
	if r.Header.Get("Content-MD5") == "" && r.Header.Get("X-Amz-Checksum-Crc32") == "" &&
		r.Header.Get("X-Amz-Checksum-Sha256") == "" && r.Header.Get("X-Amz-Trailer") == "" {
		s.refuse(w, r, http.StatusBadRequest, "DeleteObjects requires a verified transport checksum", nil)
		return
	}
	body, err := xmlBody(w, r, maxDeleteXMLBytes)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, "invalid DeleteObjects body", err)
		return
	}
	request, err := parseDelete(body)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, "invalid or unsupported DeleteObjects XML", err)
		return
	}
	result := deleteResult{}
	if !request.quiet {
		for _, key := range request.keys {
			result.Deleted = append(result.Deleted, deletedObject{Key: key})
		}
	}
	if err := s.store.DeleteObjects(r.Context(), s.name(bucket), request.keys); err != nil {
		s.storeError(w, r, bucket, err)
		return
	}
	s.writeXML(w, r, http.StatusOK, result)
}

func parseDelete(body []byte) (deleteRequest, error) {
	decoder := xml.NewDecoder(bytes.NewReader(body)) //nolint:gosec // Bounded body; directives and unknown elements are refused.
	result := deleteRequest{}
	var stack []string
	var text strings.Builder
	roots, keyCount, quietCount := 0, 0, 0
	declaration := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return result, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Space != "" && t.Name.Space != s3XMLNamespace {
				return result, errors.New("unsupported DeleteObjects namespace")
			}
			for _, attr := range t.Attr {
				if len(stack) != 0 || attr.Name.Space != "" || attr.Name.Local != "xmlns" || attr.Value != s3XMLNamespace {
					return result, errors.New("unsupported DeleteObjects attribute")
				}
			}
			switch len(stack) {
			case 0:
				if roots != 0 || t.Name.Local != "Delete" {
					return result, errors.New("one Delete root is required")
				}
				roots++
			case 1:
				switch t.Name.Local {
				case "Object":
					keyCount = 0
				case "Quiet":
					if quietCount != 0 {
						return result, errors.New("duplicate Quiet element")
					}
					quietCount++
					text.Reset()
				default:
					return result, errors.New("unsupported Delete member")
				}
			case 2:
				if stack[1] != "Object" || t.Name.Local != "Key" {
					return result, errors.New("object requires one Key; versions are not supported")
				}
				keyCount++
				text.Reset()
			default:
				return result, errors.New("nested DeleteObjects value")
			}
			stack = append(stack, t.Name.Local)
		case xml.EndElement:
			switch t.Name.Local {
			case "Object":
				key := text.String()
				if keyCount != 1 || key == "" || len(key) > 1024 || !utf8.ValidString(key) {
					return result, errors.New("invalid DeleteObjects key")
				}
				result.keys = append(result.keys, key)
				if len(result.keys) > 1000 {
					return result, errors.New("DeleteObjects exceeds 1000 keys")
				}
			case "Quiet":
				value := strings.TrimSpace(text.String())
				if value != "true" && value != "false" {
					return result, errors.New("invalid Quiet boolean")
				}
				result.quiet = value == "true"
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) == 3 || (len(stack) == 2 && stack[1] == "Quiet") {
				text.Write(t)
			} else if strings.TrimSpace(string(t)) != "" {
				return result, errors.New("unexpected DeleteObjects text")
			}
		case xml.ProcInst:
			if roots != 0 || t.Target != "xml" || declaration {
				return result, errors.New("unexpected DeleteObjects instruction")
			}
			declaration = true
		default:
			return result, fmt.Errorf("unsupported DeleteObjects token %T", token)
		}
	}
	if len(stack) != 0 || len(result.keys) == 0 {
		return result, errors.New("DeleteObjects requires a complete nonempty request")
	}
	return result, nil
}
