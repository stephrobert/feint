package s3

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/stephrobert/feint/internal/core/objectstore"
)

func completionParts(body []byte) ([]objectstore.PartRef, error) {
	decoder := xml.NewDecoder(bytes.NewReader(body)) //nolint:gosec // Bounded input; directives and unrecognised elements are rejected.
	var refs []objectstore.PartRef
	var stack []string
	var text strings.Builder
	var part objectstore.PartRef
	roots, numbers, tags := 0, 0, 0
	declaration := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Space != "" && t.Name.Space != s3XMLNamespace {
				return nil, errors.New("invalid multipart XML namespace")
			}
			for _, attr := range t.Attr {
				if len(stack) != 0 || attr.Name.Space != "" || attr.Name.Local != "xmlns" || attr.Value != s3XMLNamespace {
					return nil, errors.New("invalid multipart XML attribute")
				}
			}
			switch len(stack) {
			case 0:
				if roots != 0 || t.Name.Local != "CompleteMultipartUpload" {
					return nil, errors.New("one multipart completion root is required")
				}
				roots++
			case 1:
				if t.Name.Local != "Part" {
					return nil, errors.New("unsupported multipart completion member")
				}
				part, numbers, tags = objectstore.PartRef{}, 0, 0
			case 2:
				switch t.Name.Local {
				case "PartNumber":
					numbers++
				case "ETag":
					tags++
				default:
					return nil, errors.New("unsupported completed part member")
				}
				text.Reset()
			default:
				return nil, errors.New("nested completed part field")
			}
			stack = append(stack, t.Name.Local)
		case xml.EndElement:
			switch t.Name.Local {
			case "PartNumber":
				raw := text.String()
				if strings.IndexFunc(raw, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
					return nil, errors.New("part number requires decimal digits")
				}
				part.Number, err = strconv.Atoi(raw)
				if err != nil || part.Number < 1 || part.Number > 10000 {
					return nil, errors.New("part number exceeds its bounds")
				}
			case "ETag":
				raw := text.String()
				if len(raw) != 66 || raw[0] != '"' || raw[len(raw)-1] != '"' {
					return nil, errors.New("completed part requires its opaque uploaded tag")
				}
				part.Digest = raw[1 : len(raw)-1]
				digest, err := hex.DecodeString(part.Digest)
				if err != nil || len(digest) != sha256.Size {
					return nil, errors.New("invalid completed part tag")
				}
			case "Part":
				if numbers != 1 || tags != 1 || (len(refs) != 0 && part.Number <= refs[len(refs)-1].Number) {
					return nil, errors.New("one number and tag per ascending part are required")
				}
				refs = append(refs, part)
				if len(refs) > 10000 {
					return nil, errors.New("too many completed parts")
				}
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) == 3 {
				text.Write(t)
			} else if strings.TrimSpace(string(t)) != "" {
				return nil, errors.New("unexpected multipart completion text")
			}
		case xml.ProcInst:
			if roots != 0 || t.Target != "xml" || declaration {
				return nil, errors.New("invalid multipart completion instruction")
			}
			declaration = true
		default:
			return nil, errors.New("unsupported multipart completion token")
		}
	}
	if len(refs) == 0 || len(stack) != 0 {
		return nil, errors.New("multipart completion requires parts")
	}
	return refs, nil
}
