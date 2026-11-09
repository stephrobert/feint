package s3

import (
	"crypto/md5" //nolint:gosec // Fixture for the measured legacy transport checksum.
	"encoding/base64"
	"encoding/xml"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/objectstore"
)

const suspendedXML = `<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>Suspended</Status></VersioningConfiguration>`

func bucketFixture(t *testing.T) *Server {
	t.Helper()
	s, _ := fixture(t)
	s.cfg.BucketConfiguration = true
	s.cfg.MissingBucket = ScalewayMissingBucket
	s.cfg.Namespace = "scaleway/fr-par"
	return s
}

func TestBucketConfigurationRoundTrip(t *testing.T) {
	s := bucketFixture(t)
	body := []byte(`<CreateBucketConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><LocationConstraint>fr-par</LocationConstraint></CreateBucketConfiguration>`)
	w := request(t, s, http.MethodPut, "/bucket", body, map[string]string{
		"X-Amz-Acl": "private", "X-Amz-Bucket-Object-Lock-Enabled": "false",
	})
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(t, s, http.MethodGet, "/bucket?versioning=", nil, nil)
	var initial versioningConfiguration
	if w.Code != http.StatusOK || xml.Unmarshal(w.Body.Bytes(), &initial) != nil || initial.Status != "" {
		t.Fatal("new bucket claimed an unrequested versioning configuration:", w.Code, w.Body.String())
	}
	sum := md5.Sum([]byte(suspendedXML)) //nolint:gosec // S3 request transport checksum.
	w = request(t, s, http.MethodPut, "/bucket?versioning=", []byte(suspendedXML),
		map[string]string{"Content-MD5": base64.StdEncoding.EncodeToString(sum[:])})
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	metadata, err := s.store.BucketMetadata(t.Context(), s.name("bucket"))
	if err != nil || metadata[bucketVersioning] != "Suspended" {
		t.Fatal("successful configuration did not persist its state:", metadata, err)
	}
	if _, err := time.Parse(time.RFC3339Nano, metadata[bucketCreated]); err != nil {
		t.Fatal("bucket creation has no durable creation time:", err)
	}
}

func TestBucketPrivateACLTagsAndScopedListing(t *testing.T) {
	s := bucketFixture(t)
	for _, name := range []string{"bucket", "second"} {
		if w := request(t, s, http.MethodPut, "/"+name, nil, nil); w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if err := s.store.CreateBucketWithMetadata(t.Context(), "exoscale/ch-gva-2/foreign",
		map[string]string{bucketCreated: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	w := request(t, s, http.MethodGet, "/", nil, nil)
	var listed listedBuckets
	if w.Code != http.StatusOK || xml.Unmarshal(w.Body.Bytes(), &listed) != nil || len(listed.Buckets) != 2 ||
		listed.Buckets[0].Name != "bucket" || listed.Buckets[1].Name != "second" || listed.Buckets[0].CreationDate == "" {
		t.Fatal("bucket listing is not the actual scoped inventory:", w.Code, w.Body.String())
	}
	w = request(t, s, http.MethodGet, "/bucket?acl=", nil, nil)
	var acl accessControlPolicy
	if w.Code != http.StatusOK || xml.Unmarshal(w.Body.Bytes(), &acl) != nil ||
		acl.Owner.ID == "" || acl.Owner != s.bucketOwner() || acl.Grant.Permission != "FULL_CONTROL" ||
		acl.Grant.Grantee.ID != acl.Owner.ID || acl.Grant.Grantee.DisplayName != acl.Owner.DisplayName ||
		!strings.Contains(w.Body.String(), `xsi:type="CanonicalUser"`) {
		t.Fatal("default private canonical grant is not truthful:", w.Code, w.Body.String())
	}
	w = request(t, s, http.MethodGet, "/bucket?tagging=", nil, nil)
	var missing missingBucketError
	if w.Code != http.StatusNotFound || xml.Unmarshal(w.Body.Bytes(), &missing) != nil ||
		missing.Code != "NoSuchTagSet" || missing.Message != "The TagSet does not exist" ||
		missing.BucketName == nil || *missing.BucketName != "bucket" ||
		missing.RequestID == "" || missing.RequestID != missing.HostID {
		t.Fatal("absent tags do not follow the recorded cloud error:", w.Code, w.Body.String())
	}
}

func TestBucketConfigurationRefusals(t *testing.T) {
	s := bucketFixture(t)
	if w := request(t, s, http.MethodPut, "/bucket", nil, nil); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, tc := range []struct {
		name, method, path, body string
		headers                  map[string]string
		status                   int
	}{
		{"region", "PUT", "/wrong-region", `<CreateBucketConfiguration><LocationConstraint>nl-ams</LocationConstraint></CreateBucketConfiguration>`, nil, 400},
		{"public-acl", "PUT", "/public-bucket", "", map[string]string{"X-Amz-Acl": "public-read"}, 501},
		{"lock", "PUT", "/locked-bucket", "", map[string]string{"X-Amz-Bucket-Object-Lock-Enabled": "true"}, 501},
		{"enabled", "PUT", "/bucket?versioning=", strings.ReplaceAll(suspendedXML, "Suspended", "Enabled"), nil, 501},
		{"checksum", "PUT", "/bucket?versioning=", suspendedXML, map[string]string{"Content-MD5": "AAAAAAAAAAAAAAAAAAAAAA=="}, 400},
		{"unknown-field", "PUT", "/bucket?versioning=", strings.ReplaceAll(suspendedXML, "Status", "Unknown"), nil, 400},
		{"unknown-attribute", "PUT", "/bucket?versioning=", strings.ReplaceAll(suspendedXML, "<Status>", `<Status extra="discarded">`), nil, 400},
		{"duplicate-field", "PUT", "/bucket?versioning=", `<VersioningConfiguration><Status>Suspended</Status><Status>Suspended</Status></VersioningConfiguration>`, nil, 400},
		{"missing-field", "PUT", "/bucket?versioning=", `<VersioningConfiguration/>`, nil, 400},
		{"multiple-query", "GET", "/bucket?versioning=&acl=", "", nil, 400},
		{"duplicate-query", "GET", "/bucket?versioning=&versioning=", "", nil, 400},
		{"query-value", "GET", "/bucket?versioning=other", "", nil, 400},
		{"object-query", "PUT", "/bucket/key?versioning=", "bytes", nil, 501},
		{"tags-write", "PUT", "/bucket?tagging=", `<Tagging/>`, nil, 501},
		{"large", "PUT", "/bucket?versioning=", strings.Repeat("x", 65537), nil, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := request(t, s, tc.method, tc.path, []byte(tc.body), tc.headers)
			if w.Code != tc.status || !strings.HasPrefix(w.Body.String(), "feint: ") {
				t.Fatal("unsupported configuration acquired a cloud success:", w.Code, w.Body.String())
			}
		})
	}
	metadata, err := s.store.BucketMetadata(t.Context(), s.name("bucket"))
	if err != nil || metadata[bucketVersioning] != "" {
		t.Fatal("refused writes changed bucket state:", metadata, err)
	}
	for _, name := range []string{"wrong-region", "public-bucket", "locked-bucket"} {
		if err := s.store.HeadBucket(t.Context(), s.name(name)); !errors.Is(err, objectstore.ErrBucketNotFound) {
			t.Fatal("refused create published a bucket:", name, err)
		}
	}
}

func TestBucketConfigurationValueRequiresOneLeaf(t *testing.T) {
	for _, body := range []string{
		`<VersioningConfiguration/>`,
		`<VersioningConfiguration><Status>Suspended</Status></VersioningConfiguration><VersioningConfiguration/>`,
		`<VersioningConfiguration><Status><Nested>Suspended</Nested></Status></VersioningConfiguration>`,
		`<VersioningConfiguration xmlns="https://wrong.example"><Status>Suspended</Status></VersioningConfiguration>`,
	} {
		if _, err := configurationValue([]byte(body), "VersioningConfiguration", "Status"); err == nil {
			t.Fatal("invalid closed configuration acquired a parsed value:", body)
		}
	}
	if status, err := configurationValue([]byte(suspendedXML), "VersioningConfiguration", "Status"); err != nil || status != "Suspended" {
		t.Fatal("the accepting configuration failed:", status, err)
	}
}

func TestRestoredBucketConfigurationIsRevalidated(t *testing.T) {
	s := bucketFixture(t)
	if err := s.store.CreateBucket(t.Context(), s.name("legacy")); err != nil {
		t.Fatal(err)
	}
	w := request(t, s, http.MethodGet, "/", nil, nil)
	if w.Code != http.StatusNotImplemented {
		t.Fatal("legacy creation date was invented:", w.Code, w.Body.String())
	}
	if err := s.store.SetBucketMetadata(t.Context(), s.name("legacy"), bucketVersioning, "Enabled"); err != nil {
		t.Fatal(err)
	}
	w = request(t, s, http.MethodGet, "/legacy?versioning=", nil, nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatal("restored version histories were advertised:", w.Code, w.Body.String())
	}
}

func FuzzBucketConfigurationValue(f *testing.F) {
	f.Add(suspendedXML)
	f.Add(`<VersioningConfiguration/>`)
	f.Add(`<VersioningConfiguration><Status>Suspended</Status><Unknown/></VersioningConfiguration>`)
	f.Fuzz(func(t *testing.T, body string) {
		_, _ = configurationValue([]byte(body), "VersioningConfiguration", "Status")
	})
}
