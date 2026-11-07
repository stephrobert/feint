package s3

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func beginMultipart(t *testing.T, s *Server, key string) string {
	t.Helper()
	w := request(t, s, "POST", "/bucket/"+url.PathEscape(key)+"?uploads", nil, nil)
	var result initiatedUpload
	if w.Code != 200 || xml.Unmarshal(w.Body.Bytes(), &result) != nil || result.ID == "" || result.Key != key {
		t.Fatal("initiation did not publish a scoped session:", w.Code, w.Body.String())
	}
	return result.ID
}

func completedPartXML(number int, digest string) string {
	return fmt.Sprintf(`<Part><PartNumber>%d</PartNumber><ETag>"%s"</ETag></Part>`, number, digest)
}

func TestMultipartLifecycleUsesRealPartsAndOpaqueTags(t *testing.T) {
	s, store := fixture(t)
	request(t, s, "PUT", "/bucket", nil, nil)
	id := beginMultipart(t, s, "a//../%2F?&")
	path := "/bucket/" + url.PathEscape("a//../%2F?&") + "?uploadId=" + id
	payload := []byte("real part bytes")
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	w := request(t, s, "PUT", path+"&partNumber=1", payload, map[string]string{"Content-MD5": deleteMD5(payload)})
	if w.Code != 200 || w.Header().Get("ETag") != partTag(digest) {
		t.Fatal("uploaded tag is not the verified content token:", w.Code, w.Header(), w.Body.String())
	}
	w = request(t, s, "GET", path, nil, nil)
	var parts partInventory
	if w.Code != 200 || xml.Unmarshal(w.Body.Bytes(), &parts) != nil || len(parts.Parts) != 1 ||
		parts.Parts[0].Size != int64(len(payload)) || parts.Parts[0].ETag != partTag(digest) || parts.Parts[0].Modified == "" {
		t.Fatal("part inventory not derived from durable bytes:", w.Code, w.Body.String())
	}
	body := []byte("<CompleteMultipartUpload>" + completedPartXML(1, digest) + "</CompleteMultipartUpload>")
	w = request(t, s, "POST", path, body, nil)
	var completed completedUpload
	if w.Code != 200 || xml.Unmarshal(w.Body.Bytes(), &completed) != nil || completed.ETag != partTag(digest) {
		t.Fatal("completion failed:", w.Code, w.Body.String())
	}
	w = request(t, s, "GET", "/bucket/"+url.PathEscape("a//../%2F?&"), nil, nil)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), payload) {
		t.Fatal("completion lied about bytes:", w.Code, w.Body.String())
	}
	uploads, err := store.ListUploads(t.Context(), "bucket")
	if err != nil || len(uploads) != 0 {
		t.Fatal("completed upload still listed:", uploads, err)
	}
}

func TestMultipartInventoryPaginationScopesAndAbort(t *testing.T) {
	s, store := fixture(t)
	request(t, s, "PUT", "/bucket", nil, nil)
	first, second := beginMultipart(t, s, "key"), beginMultipart(t, s, "key")
	w := request(t, s, "GET", "/bucket?uploads&max-uploads=1", nil, nil)
	var page uploadInventory
	if w.Code != 200 || xml.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Uploads) != 1 ||
		!page.Truncated || page.Uploads[0].ID != first || page.NextID != first {
		t.Fatal("uploads are not ordered by initiation time:", w.Code, w.Body.String())
	}
	w = request(t, s, "GET", "/bucket?uploads&max-uploads=1&key-marker=key&upload-id-marker="+first, nil, nil)
	page = uploadInventory{}
	if w.Code != 200 || xml.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Uploads) != 1 ||
		page.Truncated || page.Uploads[0].ID != second {
		t.Fatal("upload cursor did not advance:", w.Code, w.Body.String())
	}
	if _, err := store.ReadUpload(t.Context(), "bucket", "wrong", first); err == nil {
		t.Fatal("upload ID crossed its key scope")
	}
	w = request(t, s, "DELETE", "/bucket/key?uploadId="+first, nil, nil)
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(t, s, "GET", "/bucket?uploads", nil, nil)
	page = uploadInventory{}
	if w.Code != 200 || xml.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Uploads) != 1 || page.Uploads[0].ID != second {
		t.Fatal("abort removed another session:", w.Code, w.Body.String())
	}
}

func TestMultipartRefusalsPreserveDurableParts(t *testing.T) {
	for _, name := range []string{"checksum", "number-zero", "number-large", "number-sign", "scope", "duplicate-query", "range", "prefix-xml", "marker-xml", "limit", "small", "tag", "order"} {
		t.Run(name, func(t *testing.T) {
			s, store := fixture(t)
			request(t, s, "PUT", "/bucket", nil, nil)
			id := beginMultipart(t, s, "key")
			a, err := store.PutPart(t.Context(), "bucket", "key", id, 1, bytes.NewBufferString("abc"), 3)
			if err != nil {
				t.Fatal(err)
			}
			path := "/bucket/key?uploadId=" + id
			method, body, headers := "PUT", []byte("new"), map[string]string{}
			path += "&partNumber=1"
			switch name {
			case "checksum":
				headers["Content-MD5"] = deleteMD5([]byte("wrong"))
			case "number-zero":
				path = "/bucket/key?uploadId=" + id + "&partNumber=0"
			case "number-large":
				path = "/bucket/key?uploadId=" + id + "&partNumber=10001"
			case "number-sign":
				path = "/bucket/key?uploadId=" + id + "&partNumber=%2B1"
			case "scope":
				path = "/bucket/other?uploadId=" + id + "&partNumber=1"
			case "duplicate-query":
				path += "&partNumber=1"
			case "range":
				method, path, body = "GET", "/bucket/key?uploadId="+id, nil
				headers["Range"] = "bytes=0-1"
			case "prefix-xml":
				method, path, body = "GET", "/bucket?uploads&prefix=%01", nil
			case "marker-xml":
				method, path, body = "GET", "/bucket?uploads&key-marker=%01", nil
			case "limit", "small", "tag", "order":
				method, path = "POST", "/bucket/key?uploadId="+id
				ref := completedPartXML(1, a.Digest)
				switch name {
				case "limit":
					s.cfg.MaxObjectBytes = 2
				case "small":
					b, err := store.PutPart(t.Context(), "bucket", "key", id, 2, bytes.NewBufferString("def"), 3)
					if err != nil {
						t.Fatal(err)
					}
					ref += completedPartXML(2, b.Digest)
				case "tag":
					ref = completedPartXML(1, strings.Repeat("0", 64))
				case "order":
					ref += ref
				}
				body = []byte("<CompleteMultipartUpload>" + ref + "</CompleteMultipartUpload>")
			}
			w := request(t, s, method, path, body, headers)
			upload, err := store.ReadUpload(t.Context(), "bucket", "key", id)
			if w.Code < 400 || err != nil || upload.Parts[1].Digest != a.Digest {
				t.Fatal("refusal lost or replaced durable part:", name, w.Code, upload, err)
			}
		})
	}
}

func TestCompletionXMLClosedShape(t *testing.T) {
	part := completedPartXML(1, strings.Repeat("a", 64))
	valid := "<CompleteMultipartUpload>" + part + "</CompleteMultipartUpload>"
	if refs, err := completionParts([]byte(valid)); err != nil || len(refs) != 1 {
		t.Fatal("accepting completion was refused:", refs, err)
	}
	for _, body := range []string{
		`<CompleteMultipartUpload/>`,
		valid + valid,
		valid + `<CompleteMultipartUpload/>`,
		strings.Replace(valid, "<Part>", `<Part extra="ignored">`, 1),
		strings.Replace(valid, "</Part>", `<Unknown/></Part>`, 1),
		strings.Replace(valid, "</Part>", `<ETag>"`+strings.Repeat("a", 64)+`"</ETag></Part>`, 1),
		strings.Replace(valid, "</Part>", `<PartNumber>1</PartNumber></Part>`, 1),
		strings.Replace(valid, `<PartNumber>1</PartNumber>`, "", 1),
		strings.Replace(valid, `<ETag>"`+strings.Repeat("a", 64)+`"</ETag>`, "", 1),
		strings.ReplaceAll(valid, strings.Repeat("a", 64), strings.Repeat("g", 64)),
		strings.Replace(valid, "<PartNumber>1", "<PartNumber>+1", 1),
		strings.Replace(valid, "<PartNumber>1", "<PartNumber>10001", 1),
		"<!DOCTYPE CompleteMultipartUpload>" + valid,
		strings.ReplaceAll(valid, "CompleteMultipartUpload", "bad:CompleteMultipartUpload"),
	} {
		if _, err := completionParts([]byte(body)); err == nil {
			t.Fatal("open or invalid completion shape admitted:", body)
		}
	}
}

func FuzzMultipartCompletionClosedShape(f *testing.F) {
	f.Add("<CompleteMultipartUpload>" + completedPartXML(1, strings.Repeat("a", 64)) + "</CompleteMultipartUpload>")
	f.Add("<CompleteMultipartUpload/>")
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > maxDeleteXMLBytes {
			return
		}
		refs, err := completionParts([]byte(body))
		if err == nil {
			if len(refs) == 0 || len(refs) > 10000 {
				t.Fatal("empty or unbounded completion")
			}
			for i, ref := range refs {
				if ref.Number < 1 || ref.Number > 10000 || (i > 0 && ref.Number <= refs[i-1].Number) {
					t.Fatal("invalid accepted part ordering")
				}
			}
		}
	})
}
