package s3

import (
	"encoding/xml"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

type listedUpload struct {
	Key          string
	ID           string `xml:"UploadId"`
	Initiated    string
	StorageClass string
}

type uploadInventory struct {
	XMLName        xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ ListMultipartUploadsResult"`
	Bucket         string
	KeyMarker      string
	UploadIDMarker string `xml:"UploadIdMarker"`
	NextKey        string `xml:"NextKeyMarker,omitempty"`
	NextID         string `xml:"NextUploadIdMarker,omitempty"`
	Prefix         string
	Max            int            `xml:"MaxUploads"`
	Truncated      bool           `xml:"IsTruncated"`
	Uploads        []listedUpload `xml:"Upload"`
}

func (s *Server) listUploads(w http.ResponseWriter, r *http.Request, bucket string, q url.Values) {
	max, err := multipartNumber(q, "max-uploads", 1000, 1, 1000)
	prefix, prefixErr := listText(q.Get("prefix"), "")
	keyMarker, markerErr := listText(q.Get("key-marker"), "")
	if err != nil || (q.Get("upload-id-marker") != "" && q.Get("key-marker") == "") ||
		prefixErr != nil || markerErr != nil {
		s.refuse(w, r, http.StatusBadRequest, "invalid multipart inventory bounds or XML text", errors.Join(err, prefixErr, markerErr))
		return
	}
	uploads, err := s.store.ListUploads(r.Context(), s.name(bucket))
	if err != nil {
		s.storeError(w, r, bucket, err)
		return
	}
	result := uploadInventory{Bucket: bucket, Max: max, Prefix: prefix,
		KeyMarker: keyMarker, UploadIDMarker: q.Get("upload-id-marker")}
	markerUpload, hasMarker := uploads[result.UploadIDMarker]
	if result.UploadIDMarker != "" && (!hasMarker || markerUpload.Key != result.KeyMarker) {
		s.refuse(w, r, http.StatusBadRequest, "multipart marker no longer names a live upload for its key", nil)
		return
	}
	var entries []listedUpload
	for id, upload := range uploads {
		if !strings.HasPrefix(upload.Key, result.Prefix) {
			continue
		}
		if upload.Key < result.KeyMarker || (upload.Key == result.KeyMarker &&
			(result.UploadIDMarker == "" || upload.Created.Before(markerUpload.Created) ||
				(upload.Created.Equal(markerUpload.Created) && id <= result.UploadIDMarker))) {
			continue
		}
		if _, err := listText(upload.Key, ""); err != nil {
			s.refuse(w, r, http.StatusNotImplemented, "multipart inventory contains an XML-inexpressible key", err)
			return
		}
		entries = append(entries, listedUpload{Key: upload.Key, ID: id,
			Initiated: upload.Created.UTC().Format(time.RFC3339Nano), StorageClass: "STANDARD"})
	}
	slices.SortFunc(entries, func(a, b listedUpload) int {
		if a.Key != b.Key {
			return strings.Compare(a.Key, b.Key)
		}
		if order := uploads[a.ID].Created.Compare(uploads[b.ID].Created); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	result.Truncated = len(entries) > max
	if result.Truncated {
		result.NextKey, result.NextID = entries[max-1].Key, entries[max-1].ID
		entries = entries[:max]
	}
	result.Uploads = entries
	s.writeXML(w, r, http.StatusOK, result)
}

type listedPart struct {
	Number   int    `xml:"PartNumber"`
	Modified string `xml:"LastModified"`
	ETag     string
	Size     int64
}

type partInventory struct {
	XMLName   xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ ListPartsResult"`
	Bucket    string
	Key       string
	ID        string       `xml:"UploadId"`
	Marker    int          `xml:"PartNumberMarker"`
	Next      int          `xml:"NextPartNumberMarker,omitempty"`
	Max       int          `xml:"MaxParts"`
	Truncated bool         `xml:"IsTruncated"`
	Parts     []listedPart `xml:"Part"`
}

func (s *Server) listParts(w http.ResponseWriter, r *http.Request, bucket, key string, q url.Values) {
	max, err := multipartNumber(q, "max-parts", 1000, 1, 1000)
	marker, markerErr := multipartNumber(q, "part-number-marker", 0, 0, 10000)
	if err != nil || markerErr != nil {
		s.refuse(w, r, http.StatusBadRequest, "invalid part inventory bounds", errors.Join(err, markerErr))
		return
	}
	upload, err := s.store.ReadUpload(r.Context(), s.name(bucket), key, q.Get("uploadId"))
	if err != nil {
		s.storeError(w, r, bucket, err)
		return
	}
	var numbers []int
	for number := range upload.Parts {
		if number > marker {
			numbers = append(numbers, number)
		}
	}
	slices.Sort(numbers)
	result := partInventory{Bucket: bucket, Key: key, ID: q.Get("uploadId"), Marker: marker, Max: max,
		Truncated: len(numbers) > max}
	if result.Truncated {
		result.Next = numbers[max-1]
		numbers = numbers[:max]
	}
	for _, number := range numbers {
		part := upload.Parts[number]
		result.Parts = append(result.Parts, listedPart{Number: number, Size: part.Size,
			ETag: partTag(part.Digest), Modified: part.Modified.UTC().Format(time.RFC3339Nano)})
	}
	s.writeXML(w, r, http.StatusOK, result)
}
