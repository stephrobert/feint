package s3

import (
	"encoding/xml"
	"net/http"
	"strings"
	"testing"
)

func TestObjectDiscoveryIsPrivateEmptyAndChecksExistence(t *testing.T) {
	s := bucketFixture(t)
	s.cfg.ObjectDiscovery = true
	for _, path := range []string{"/bucket", "/bucket/key"} {
		body := []byte(nil)
		if path == "/bucket/key" {
			body = []byte("bytes")
		}
		if w := request(t, s, http.MethodPut, path, body, nil); w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := request(t, s, http.MethodGet, "/bucket/key?acl=", nil, nil)
	var acl accessControlPolicy
	if err := xml.Unmarshal(w.Body.Bytes(), &acl); err != nil || w.Code != http.StatusOK ||
		acl.Owner != s.bucketOwner() || acl.Grant.Permission != "FULL_CONTROL" ||
		acl.Grant.Grantee.ID != acl.Owner.ID || !strings.Contains(w.Body.String(), `xsi:type="CanonicalUser"`) {
		t.Fatal("private object grant diverged:", err, w.Code, w.Body.String())
	}
	w = request(t, s, http.MethodGet, "/bucket/key?tagging=", nil, nil)
	if w.Code != http.StatusOK || w.Body.String() != `<Tagging xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><TagSet></TagSet></Tagging>` {
		t.Fatal("unwritten object tags did not match the recorded empty tag set:", w.Code, w.Body.String())
	}
	for _, feature := range []string{"acl", "tagging"} {
		if w := request(t, s, http.MethodGet, "/bucket/missing?"+feature+"=", nil, nil); w.Code != http.StatusNotImplemented {
			t.Fatal("missing object acquired successful discovery:", w.Code, w.Body.String())
		}
		if w := request(t, s, http.MethodPut, "/bucket/key?"+feature+"=", []byte("unimplemented"), nil); w.Code != http.StatusNotImplemented {
			t.Fatal("unsupported object setting write succeeded:", w.Code, w.Body.String())
		}
	}
	for _, query := range []string{"acl=&tagging=", "acl=&acl=", "tagging=nonempty"} {
		if w := request(t, s, http.MethodGet, "/bucket/key?"+query, nil, nil); w.Code != http.StatusBadRequest {
			t.Fatal("invalid object query was silently discarded:", w.Code, w.Body.String())
		}
	}
	if w := request(t, s, http.MethodGet, "/bucket/key?acl=", nil, map[string]string{"Range": "bytes=0-1"}); w.Code != http.StatusNotImplemented {
		t.Fatal("object configuration pretended to apply a byte range:", w.Code, w.Body.String())
	}
	s.cfg.ObjectDiscovery = false
	if w := request(t, s, http.MethodGet, "/bucket/key?acl=", nil, nil); w.Code != http.StatusNotImplemented {
		t.Fatal("unmeasured profile acquired object configuration:", w.Code)
	}
}

func TestRecordedAbsentConfigurationChecksBucketAndClonesProfile(t *testing.T) {
	base := bucketFixture(t)
	config := base.cfg
	config.AbsentConfigurations = map[string]AbsentConfiguration{
		"cors": {Status: http.StatusNotFound, Code: "NoSuchCORSConfiguration",
			Message: "The CORS configuration does not exist", RequestIDs: true, Resource: true},
	}
	s, err := New(base.store, config)
	if err != nil {
		t.Fatal(err)
	}
	config.AbsentConfigurations["cors"] = AbsentConfiguration{Status: 400, Code: "changed", Message: "changed"}
	if w := request(t, s, http.MethodPut, "/bucket", nil, nil); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	w := request(t, s, http.MethodGet, "/bucket?cors=", nil, nil)
	var wire absentConfigurationError
	if err := xml.Unmarshal(w.Body.Bytes(), &wire); err != nil || w.Code != http.StatusNotFound ||
		wire.Code != "NoSuchCORSConfiguration" || wire.Message != "The CORS configuration does not exist" ||
		wire.BucketName != "" || wire.Resource != "/bucket" || wire.RequestID == "" ||
		wire.RequestID != wire.HostID || w.Header().Get("X-Amz-Request-Id") != wire.RequestID {
		t.Fatal("recorded error changed or lost its protocol fields:", err, w.Code, w.Body.String())
	}
	w = request(t, s, http.MethodGet, "/missing?cors=", nil, nil)
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "<Code>NoSuchBucket</Code>") {
		t.Fatal("missing bucket acquired an existing bucket's absent configuration:", w.Code, w.Body.String())
	}
	if w := request(t, s, http.MethodPut, "/bucket?cors=", []byte("<CORSConfiguration/>"), nil); w.Code != http.StatusNotImplemented {
		t.Fatal("unimplemented CORS write succeeded:", w.Code, w.Body.String())
	}
}

func TestPrivateBucketACLWriteRefusesAnythingButTheRecordedDefault(t *testing.T) {
	s := bucketFixture(t)
	s.cfg.PrivateACLWrite = true
	if w := request(t, s, "PUT", "/bucket", nil, nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(t, s, "PUT", "/bucket?acl=", nil, map[string]string{"X-Amz-Acl": "private"}); w.Code != 200 {
		t.Fatal("recorded private write failed:", w.Code, w.Body.String())
	}
	for _, acl := range []string{"", "public-read", "authenticated-read"} {
		if w := request(t, s, "PUT", "/bucket?acl=", nil, map[string]string{"X-Amz-Acl": acl}); w.Code != 501 {
			t.Fatal("unmeasured canned ACL succeeded:", acl, w.Code, w.Body.String())
		}
	}
	if w := request(t, s, "PUT", "/bucket?acl=", []byte("<AccessControlPolicy/>"), map[string]string{"X-Amz-Acl": "private"}); w.Code != 501 {
		t.Fatal("policy body silently discarded:", w.Code, w.Body.String())
	}
}

func TestLegacyListingUsesPlainMarkersAndSharedInventory(t *testing.T) {
	s, _ := fixture(t)
	s.cfg.ListObjectsV1 = true
	if w := request(t, s, "PUT", "/bucket", nil, nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, key := range []string{"a", "dir/one", "dir/two", "z"} {
		if w := request(t, s, "PUT", "/bucket/"+key, []byte(key), nil); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	list := func(query string) legacyListResult {
		t.Helper()
		w := request(t, s, "GET", "/bucket?"+query, nil, nil)
		var result legacyListResult
		if err := xml.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 ||
			strings.Contains(w.Body.String(), "ContinuationToken") || strings.Contains(w.Body.String(), "KeyCount") {
			t.Fatal("legacy listing used a V2 wire:", err, w.Code, w.Body.String())
		}
		return result
	}
	first := list("max-keys=1&delimiter=/")
	if first.Name != "bucket" || !first.Truncated || first.Next != "a" || len(first.Objects) != 1 ||
		first.Objects[0].Key != "a" || first.Objects[0].Size != 1 {
		t.Fatal("first legacy page did not reflect inventory:", first)
	}
	second := list("max-keys=1&delimiter=/&marker=a")
	if second.Marker != "a" || second.Next != "dir/" || !second.Truncated ||
		len(second.Objects) != 0 || len(second.Prefixes) != 1 || second.Prefixes[0].Prefix != "dir/" {
		t.Fatal("plain marker failed prefix pagination:", second)
	}
	final := list("max-keys=1&delimiter=/&marker=dir/")
	if final.Truncated || final.Next != "" || len(final.Objects) != 1 || final.Objects[0].Key != "z" {
		t.Fatal("final legacy page repeated entries:", final)
	}
	zero := list("max-keys=0")
	if zero.Truncated || zero.Next != "" || len(zero.Objects) != 0 {
		t.Fatal("zero-size page acquired a nonprogressing marker:", zero)
	}
	for _, query := range []string{"marker=a&marker=b", "marker=" + strings.Repeat("a", 1025), "max-keys=-1"} {
		if w := request(t, s, "GET", "/bucket?"+query, nil, nil); w.Code != 400 {
			t.Fatal("invalid legacy option was ignored:", w.Code, w.Body.String())
		}
	}
	if _, err := parseLegacyListOptions(map[string][]string{"continuation-token": {"v2"}}); err == nil {
		t.Fatal("legacy parser accepted a V2 token")
	}
	if v2 := decodeListing(t, s, "list-type=2"); v2.KeyCount != 4 {
		t.Fatal("legacy support changed the V2 inventory:", v2)
	}
}
